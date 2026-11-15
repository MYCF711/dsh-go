package main

// directoryPicker 互斥组合的**宿主侧**测试：事实采样、声明解析、端到端两条组合。
//
// 端到端部分是"真 HTTP 打宿主"：真 transport.Server（Start → /connect 换 cookie）→
// 真 /api 路由 → 真 seam。每条组合都断言**只有一套**能服务，另一套回官方形状的
// `directory-picker/unavailable`（code 前缀 + details.capability）。

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/transport"
)

// TestResolveDirectoryPickerKind 钉声明解析：两个字面量 + 自动兜底 + 拼错必红。
//
// 自动为什么是 browse：官方 resolve.ts:55-61 选 native 的前提里有一条本宿主
// **证明不了**（"这个进程坐在交互桌面里"），官方对含糊情形的兜底就是 browse
// （resolve.ts:50-51）。所以未声明 = browse；要 native 必须显式声明。
func TestResolveDirectoryPickerKind(t *testing.T) {
	cases := []struct {
		declaration string
		want        transport.DirectoryPickerKind
	}{
		{"native", transport.DirectoryPickerKindNative},
		{"NATIVE", transport.DirectoryPickerKindNative},
		{" native ", transport.DirectoryPickerKindNative},
		{"browse", transport.DirectoryPickerKindBrowse},
		{"", transport.DirectoryPickerKindBrowse},
	}
	for _, c := range cases {
		got, err := resolveDirectoryPickerKind(c.declaration)
		if err != nil {
			t.Fatalf("声明 %q 不该报错：%v", c.declaration, err)
		}
		if got != c.want {
			t.Fatalf("声明 %q → %q，期望 %q", c.declaration, string(got), string(c.want))
		}
	}
	// 拼错的声明不许静默退回某一档（那会让 --directory-picker 变成假开关）。
	for _, bad := range []string{"automatic", "nat", "both", "native,browse"} {
		if _, err := resolveDirectoryPickerKind(bad); err == nil {
			t.Fatalf("声明 %q 应当报错", bad)
		}
	}
}

// TestNativeBlocker 是官方 resolve.ts 事实判据的逐条对照（每条都给一句可核对的理由）。
func TestNativeBlocker(t *testing.T) {
	attended := directoryPickerFacts{bindHost: "127.0.0.1:8080", platform: "windows", hasDesktop: true}
	if got := nativeBlocker(attended); got != "" {
		t.Fatalf("本地有桌面的 Windows 宿主不该有阻碍，得到 %q", got)
	}

	cases := []struct {
		name  string
		facts directoryPickerFacts
		want  string // 期望出现在理由里
	}{
		{
			name:  "信任了非回环权威",
			facts: directoryPickerFacts{bindHost: "127.0.0.1:8080", platform: "windows", hasDesktop: true, allowsRemoteAuthorities: true},
			want:  "非回环权威",
		},
		{
			name:  "绑非回环地址",
			facts: directoryPickerFacts{bindHost: "0.0.0.0:8080", platform: "windows", hasDesktop: true},
			want:  "不是回环地址",
		},
		{
			name:  "SSH 会话里起的",
			facts: directoryPickerFacts{bindHost: "127.0.0.1:8080", platform: "windows", hasDesktop: true, ssh: true},
			want:  "SSH",
		},
		{
			name:  "平台不是 Windows",
			facts: directoryPickerFacts{bindHost: "127.0.0.1:8080", platform: "linux", hasDesktop: true},
			want:  "只实现了 Windows",
		},
		{
			name:  "没有交互桌面",
			facts: directoryPickerFacts{bindHost: "127.0.0.1:8080", platform: "windows"},
			want:  "没有交互桌面会话",
		},
	}
	for _, c := range cases {
		got := nativeBlocker(c.facts)
		if got == "" {
			t.Fatalf("%s：应当给出阻碍理由", c.name)
		}
		if !strings.Contains(got, c.want) {
			t.Fatalf("%s：理由没点名 %q：%q", c.name, c.want, got)
		}
	}
}

// TestIsLoopbackBindHost：判错方向的代价是"把外部可达的绑定当成只有本机能连"，
// 所以解析不了的主机名一律不算回环。
func TestIsLoopbackBindHost(t *testing.T) {
	cases := map[string]bool{
		"":               true,
		"127.0.0.1:0":    true,
		"127.0.0.1:5173": true,
		"localhost:8080": true,
		"[::1]:8080":     true,
		"::1":            true,
		"0.0.0.0:8080":   false,
		"[::]:8080":      false,
		"192.168.1.9:80": false,
		"example.com":    false,
		"myhost:8080":    false,
	}
	for addr, want := range cases {
		if got := isLoopbackBindHost(addr); got != want {
			t.Fatalf("isLoopbackBindHost(%q) = %v，期望 %v", addr, got, want)
		}
	}
}

// TestDirectoryPickerFactsFromEnv：SSH 与"桌面会话"两个事实都只看环境，
// 且服务会话（Services）不算桌面。
func TestDirectoryPickerFactsFromEnv(t *testing.T) {
	env := func(kv map[string]string) func(string) string {
		return func(k string) string { return kv[k] }
	}

	console := directoryPickerFactsFromEnv("", "windows", env(map[string]string{"SESSIONNAME": "Console"}))
	if !console.hasDesktop {
		t.Fatal("SESSIONNAME=Console 应当算有交互桌面")
	}
	rdp := directoryPickerFactsFromEnv("", "windows", env(map[string]string{"SESSIONNAME": "RDP-Tcp#3"}))
	if !rdp.hasDesktop {
		t.Fatal("RDP 会话也算有桌面（框能弹在远程桌面里）")
	}
	services := directoryPickerFactsFromEnv("", "windows", env(map[string]string{"SESSIONNAME": "Services"}))
	if services.hasDesktop {
		t.Fatal("服务会话（Services）没有交互桌面")
	}
	none := directoryPickerFactsFromEnv("", "windows", env(nil))
	if none.hasDesktop {
		t.Fatal("没有 SESSIONNAME 就不是桌面会话")
	}
	for _, key := range []string{"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY"} {
		if got := directoryPickerFactsFromEnv("", "windows", env(map[string]string{key: "x"})); !got.ssh {
			t.Fatalf("%s 存在时应当判成 SSH 启动", key)
		}
	}
	if facts := directoryPickerFactsFromEnv("10.0.0.2:8080", "windows", env(nil)); facts.bindHost != "10.0.0.2:8080" {
		t.Fatalf("绑定地址没被采到：%q", facts.bindHost)
	}
}

// TestAttachDirectoryPickerSeamRejectsNilDeps：没有 host / 没有 srv 都要报错，
// 不许"挂了个寂寞"再在请求时 501。
func TestAttachDirectoryPickerSeamRejectsNilDeps(t *testing.T) {
	srv, err := transport.NewServer(transport.Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer func() { _ = srv.Close() }()
	if err := attachOfficialDirectoryPickerSeam(nil, newOfficialWorkspaceHost(t.TempDir(), nil, nil), transport.DirectoryPickerKindBrowse); err == nil {
		t.Fatal("srv 是 nil 应当报错")
	}
	if err := attachOfficialDirectoryPickerSeam(srv, nil, transport.DirectoryPickerKindBrowse); err == nil {
		t.Fatal("host 是 nil 应当报错")
	}
}

// TestDirectoryPickerSeamEndToEnd 是**真 HTTP** 的两条组合验收。
//
// 每条组合都：真 server + 真 /connect cookie + 真 /api 路由，然后
//   - 属于本次组合的两个 browse 动词（或 pick）200，形状逐字段核；
//   - 另一套的动词 501 + `directory-picker/unavailable` 前缀 + details.capability。
//
// 关键断言是"只能选一个"：native 组合下 browse 一个都不通，browse 组合下 pick
// 一次都不许调用原生服务——不是"两个都能，只是后一个报错"。
func TestDirectoryPickerSeamEndToEnd(t *testing.T) {
	t.Run("native 组合：pick 通，browse 全拒", func(t *testing.T) {
		h := newSeamHTTPServer(t)
		picked := filepath.Join(h.home, "picked-dir")
		if err := os.Mkdir(picked, 0o755); err != nil {
			t.Fatal(err)
		}
		h.native.pickPath = picked
		if err := h.declare(t, transport.DirectoryPickerKindNative); err != nil {
			t.Fatalf("声明 native: %v", err)
		}

		code, out := h.post(t, "directoryPicker.pick", `{}`)
		if code != http.StatusOK {
			t.Fatalf("native 组合的 pick 应 200，实际 %d（%v）", code, out)
		}
		if out["result"] != picked {
			t.Fatalf("pick 的 result 应是原生服务给的绝对路径，实际 %v", out["result"])
		}
		if h.native.pickCalls() != 1 {
			t.Fatalf("pick 应只调用原生服务一次，实际 %d", h.native.pickCalls())
		}

		for _, method := range []string{"directoryPicker.list", "directoryPicker.createDirectory"} {
			body := `{}`
			if strings.HasSuffix(method, "createDirectory") {
				body = `{"path":` + jsonString(picked) + `,"name":"nope"}`
			}
			code, out = h.post(t, method, body)
			assertSeamRefusal(t, method, code, out, "native")
			if method == "directoryPicker.createDirectory" {
				if _, err := os.Stat(filepath.Join(picked, "nope")); !os.IsNotExist(err) {
					t.Fatalf("被拒的 createDirectory 竟然落盘了：%v", err)
				}
			}
		}
	})

	t.Run("browse 组合：list/createDirectory 通，pick 拒", func(t *testing.T) {
		h := newSeamHTTPServer(t)
		picked := filepath.Join(h.home, "must-not-be-picked")
		if err := os.Mkdir(picked, 0o755); err != nil {
			t.Fatal(err)
		}
		h.native.pickPath = picked
		if err := h.declare(t, transport.DirectoryPickerKindBrowse); err != nil {
			t.Fatalf("声明 browse: %v", err)
		}

		code, out := h.post(t, "directoryPicker.list", `{}`)
		if code != http.StatusOK {
			t.Fatalf("browse 组合的 list 应 200，实际 %d（%v）", code, out)
		}
		listing, ok := out["result"].(map[string]any)
		if !ok {
			t.Fatalf("list 的 result 应是官方 DirectoryListing 对象：%v", out)
		}
		for _, key := range []string{"path", "home", "crumbs", "entries", "truncated"} {
			if _, has := listing[key]; !has {
				t.Fatalf("list 结果缺官方字段 %s：%v", key, listing)
			}
		}
		if got, _ := listing["path"].(string); filepath.Clean(got) != filepath.Clean(h.pickerHome) {
			t.Fatalf("省略 path 应列宿主 home=%q，实际 %q", h.pickerHome, got)
		}

		code, out = h.post(t, "directoryPicker.createDirectory",
			`{"path":`+jsonString(h.pickerHome)+`,"name":"seam-created"}`)
		if code != http.StatusOK {
			t.Fatalf("browse 组合的 createDirectory 应 200，实际 %d（%v）", code, out)
		}
		created, _ := out["result"].(string)
		if filepath.Clean(created) != filepath.Join(filepath.Clean(h.pickerHome), "seam-created") {
			t.Fatalf("createDirectory 的 result 应是新目录绝对路径，实际 %v", out["result"])
		}
		if _, err := os.Stat(filepath.Join(h.pickerHome, "seam-created")); err != nil {
			t.Fatalf("createDirectory 没真建目录（不许假成功）：%v", err)
		}

		code, out = h.post(t, "directoryPicker.pick", `{}`)
		assertSeamRefusal(t, "directoryPicker.pick", code, out, "browse")
		if h.native.pickCalls() != 0 {
			t.Fatalf("browse 组合下 pick 一次都不许调用原生服务，实际 %d 次", h.native.pickCalls())
		}
	})
}

// seamHTTPServer 是一台真 server：真 transport、真 /api 路由、真 cookie。
type seamHTTPServer struct {
	base       string
	client     *http.Client
	home       string
	pickerHome string
	native     *fakeNativeService
	srv        *transport.Server
	host       *officialWorkspaceHost
}

// newSeamHTTPServer 起 server 并**先挂原生能力**（pick 的提供方），组合由用例声明。
func newSeamHTTPServer(t *testing.T) *seamHTTPServer {
	t.Helper()
	home := t.TempDir()
	pickerHome := t.TempDir()
	if err := os.Mkdir(filepath.Join(pickerHome, "child-a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pickerHome, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv, err := transport.NewServer(transport.Options{})
	if err != nil {
		t.Fatalf("建 server: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	base, err := srv.Start()
	if err != nil {
		t.Fatalf("起 server: %v", err)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(base + "/connect?token=" + url.QueryEscape(srv.Token()))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("connect status=%d", resp.StatusCode)
	}

	fake := &fakeNativeService{}
	attachOfficialNative(srv, fake, nil)

	host := newOfficialWorkspaceHost(home, nil, nil)
	if host == nil {
		t.Fatal("newOfficialWorkspaceHost 返回 nil")
	}
	host.pickerHome = pickerHome
	return &seamHTTPServer{
		base: base, client: client, home: home, pickerHome: pickerHome,
		native: fake, srv: srv, host: host,
	}
}

// declare 按声明的组合挂 seam（与 serve.go 的装配同一条路）。
func (h *seamHTTPServer) declare(t *testing.T, kind transport.DirectoryPickerKind) error {
	t.Helper()
	return attachOfficialDirectoryPickerSeam(h.srv, h.host, kind)
}

// post 打一次真 /api 方法（带 cookie）。
func (h *seamHTTPServer) post(t *testing.T, method, body string) (int, map[string]any) {
	t.Helper()
	resp, err := h.client.Post(h.base+"/api/"+method, "application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("POST %s: %v", method, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读 %s 响应: %v", method, err)
	}
	var out map[string]any
	if len(bytes.TrimSpace(raw)) > 0 {
		if uerr := json.Unmarshal(raw, &out); uerr != nil {
			t.Fatalf("%s 的响应不是 JSON 对象: %q", method, string(raw))
		}
	}
	return resp.StatusCode, out
}

// assertSeamRefusal 断言拒绝是官方形状。
func assertSeamRefusal(t *testing.T, method string, code int, out map[string]any, wantServed string) {
	t.Helper()
	if code != http.StatusNotImplemented {
		t.Fatalf("%s 应 501（本次组合没有这个交互），实际 %d（%v）", method, code, out)
	}
	errObj, ok := out["error"].(map[string]any)
	if !ok {
		t.Fatalf("%s 缺四字段 error 对象：%v", method, out)
	}
	for _, key := range []string{"code", "message", "hint", "retryable"} {
		if _, has := errObj[key]; !has {
			t.Fatalf("%s 的 error 缺 %s：%v", method, key, errObj)
		}
	}
	if errObj["code"] != "not_implemented" {
		t.Fatalf("%s 的 error.code = %v，期望 not_implemented", method, errObj["code"])
	}
	msg, _ := errObj["message"].(string)
	if !strings.HasPrefix(msg, "directory-picker/unavailable: ") {
		t.Fatalf("%s 的 message 必须以官方码开头：%q", method, msg)
	}
	details, ok := out["details"].(map[string]any)
	if !ok {
		t.Fatalf("%s 的拒绝缺顶层 details（官方 {capability}）：%v", method, out)
	}
	if served, _ := details["capability"].(string); served != wantServed {
		t.Fatalf("%s 的 details.capability = %q，期望 %q", method, served, wantServed)
	}
}

// 注：JSON 字面量拼 body 用的 jsonString 在本包已有唯一一份（serve_office_kit_test.go），
// 这里不再写第二份——"同一条规则两个实现"正是 single-source 守卫抓的那类病。
