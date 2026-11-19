package main

// 界面状态（`uiState.get/set/delete`）：**生产接线 + 真 HTTP + 真磁盘**。
//
// 与 `internal/transport/handlers_ui_state_test.go` 的分工：
//   - 那里验契约层（转发、结构校验、错误码、501），宿主是桩；
//   - 这里验 `withUIStateOptions` 挂上去的那份生产实现：请求走真 /api，
//     值真落到 `<home>/state/ui-state/<key>.json`，重启（新实例）后仍在，
//     非法 key 在**写任何文件之前**被拒。
//
// 这条链是 #64 ⑦ 的地基：官方把已读标记放在 Electron `Local Storage`，
// 本宿主没有那个容器，所以要有这个落点；它**不是** settings 通道
// （settings.mutate 的 revision 栅栏会让界面点击和用户保存设置互相踩）。

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/transport"
)

type uiStateHarness struct {
	home   string
	base   string
	client *http.Client
}

func newUIStateHarness(t *testing.T) *uiStateHarness {
	t.Helper()
	home := t.TempDir()
	srv, err := transport.NewServer(withUIStateOptions(home, transport.Options{}))
	if err != nil {
		t.Fatalf("建 server: %v", err)
	}
	base, err := srv.Start()
	if err != nil {
		_ = srv.Close()
		t.Fatalf("起 server: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(base + "/connect?token=" + srv.Token())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("connect status=%d", resp.StatusCode)
	}
	return &uiStateHarness{home: home, base: base, client: client}
}

// call 打一次 /api/<method>，返回状态码与解开的 result 对象。
func (h *uiStateHarness) call(t *testing.T, method string, params map[string]any) (int, map[string]json.RawMessage) {
	t.Helper()
	body, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("encode params: %v", err)
	}
	resp, err := h.client.Post(h.base+"/api/"+method, "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	defer resp.Body.Close()
	var env map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("%s 响应不是 JSON：%v", method, err)
	}
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, env
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(env["result"], &out); err != nil {
		t.Fatalf("%s 的 result 不是对象：%v", method, err)
	}
	return resp.StatusCode, out
}

func TestUIStatePersistsToHomeAndSurvivesRestart(t *testing.T) {
	h := newUIStateHarness(t)
	key := "dsh.sidebar-browser.v1.session-5f0d1a2b"
	value := map[string]any{"byTab": map[string]any{"browser": map[string]any{"url": "https://example.com"}}}
	status, out := h.call(t, "uiState.set", map[string]any{"key": key, "value": value})
	if status != http.StatusOK {
		t.Fatalf("uiState.set status=%d out=%v", status, out)
	}

	// 1) 磁盘上真有一个文件，名字就是 key，内容就是那份 JSON。
	path := filepath.Join(h.home, "state", "ui-state", key+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("没落到 %s：%v", path, err)
	}
	var stored map[string]any
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("落盘内容不是 JSON：%v（%s）", err, raw)
	}
	if _, ok := stored["byTab"]; !ok {
		t.Fatalf("落盘内容丢了 byTab：%s", raw)
	}

	// 2) 读回来逐字节相同（宿主不解释、不改写）。
	status, out = h.call(t, "uiState.get", map[string]any{"key": key})
	if status != http.StatusOK || string(out["found"]) != "true" {
		t.Fatalf("uiState.get status=%d out=%v", status, out)
	}
	if got := string(out["value"]); got != string(raw) {
		t.Fatalf("读回的值与磁盘不同：\n读回 %s\n磁盘 %s", got, raw)
	}

	// 3) 换一个实例（等价于进程重启）：新 store 直接读盘，仍在。
	restarted := withUIStateOptions(h.home, transport.Options{})
	entry, found, err := restarted.UIState.Get(key)
	if err != nil || !found {
		t.Fatalf("重启后读不到：found=%v err=%v", found, err)
	}
	if string(entry.Value) != string(raw) {
		t.Fatalf("重启后值变了：%s", entry.Value)
	}

	// 4) delete 后文件消失，再 get 是 found:false（不是错误）。
	status, out = h.call(t, "uiState.delete", map[string]any{"key": key})
	if status != http.StatusOK || string(out["deleted"]) != "true" {
		t.Fatalf("uiState.delete status=%d out=%v", status, out)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("delete 之后文件还在：%v", err)
	}
	_, out = h.call(t, "uiState.get", map[string]any{"key": key})
	if string(out["found"]) != "false" {
		t.Fatalf("删掉后 found=%s，want false", out["found"])
	}
	// 再删一次是 deleted:false，不报错。
	status, out = h.call(t, "uiState.delete", map[string]any{"key": key})
	if status != http.StatusOK || string(out["deleted"]) != "false" {
		t.Fatalf("重复 delete status=%d out=%v", status, out)
	}
}

func TestUIStateRejectsKeysThatCannotBeFilenames(t *testing.T) {
	h := newUIStateHarness(t)
	dir := filepath.Join(h.home, "state", "ui-state")
	for _, key := range []string{
		"../escape",  // 路径分隔符
		"a/b",        // 分隔符
		`a\b`,        // Windows 分隔符
		"..",         // 相对路径
		".hidden",    // 隐藏名（与 .ui-state-*.tmp 争地盘）
		"with space", // 空白
		"colon:name", // Windows 保留字符
		"tab\tname",  // 控制字符
		strings.Repeat("k", maxUIStateKeyLen+1),
	} {
		status, _ := h.call(t, "uiState.set", map[string]any{"key": key, "value": map[string]any{"a": 1}})
		if status != http.StatusBadRequest {
			t.Fatalf("key %q 应被拒（400），实际 %d", key, status)
		}
	}
	// 一个文件都不许被建出来：拒绝必须发生在写盘之前。
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("读目录: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("非法 key 建出了文件：%v", entries)
	}

	// 官方那两个真键必须过（不是"越严越好"：官方形状要能用）。
	for _, key := range []string{
		"dsh.sidebar-browser.v1.session-5f0d1a2b",
		"dsh.user-questions.drafts.v1",
	} {
		status, out := h.call(t, "uiState.set", map[string]any{"key": key, "value": map[string]any{"a": 1}})
		if status != http.StatusOK {
			t.Fatalf("官方键 %q 被拒：%d %v", key, status, out)
		}
	}
}

func TestUIStateRefusesOversizedValue(t *testing.T) {
	h := newUIStateHarness(t)
	big := strings.Repeat("x", maxUIStateValueBytes+16)
	status, _ := h.call(t, "uiState.set", map[string]any{"key": "dsh.big.v1", "value": big})
	if status != http.StatusBadRequest {
		t.Fatalf("超限值应被拒（400），实际 %d", status)
	}
	if _, err := os.Stat(filepath.Join(h.home, "state", "ui-state", "dsh.big.v1.json")); !os.IsNotExist(err) {
		t.Fatalf("超限值不许落盘：%v", err)
	}
}

func TestUIStateZeroByteFileReadsAsAbsent(t *testing.T) {
	// 0 字节 = 写坏了或被清空：按"没有"处理，不假装有一份空状态
	// （空字节不是合法 JSON，界面拿回去只会解析失败）。
	home := t.TempDir()
	dir := filepath.Join(home, "state", "ui-state")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dsh.empty.v1.json"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	entry, found, err := newUIStateStore(home).Get("dsh.empty.v1")
	if err != nil || found {
		t.Fatalf("0 字节文件应读成不存在：found=%v err=%v entry=%+v", found, err, entry)
	}
}
