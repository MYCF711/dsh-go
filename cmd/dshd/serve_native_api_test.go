package main

// 5 个原生方法的宿主端到端：真实 HTTP 路由 + 真实设置文档 + 可注入原生服务；
// 另加一条真实 Windows API 探针（关联查询/canOpen，不弹目录框）。

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"dshgo/internal/native"
	"dshgo/internal/settingsform"
	"dshgo/internal/transport"
)

type fakeNativeService struct {
	mu           sync.Mutex
	pickPath     string
	pickErr      error
	canOpen      bool
	opened       []string
	revealed     []string
	openWith     [][2]string
	applications []native.Application
}

func (f *fakeNativeService) PickFolder(context.Context) (string, error) {
	if f.pickErr != nil {
		return "", f.pickErr
	}
	return f.pickPath, nil
}

func (f *fakeNativeService) OpenDocument(_ context.Context, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = append(f.opened, path)
	return nil
}

func (f *fakeNativeService) CanOpen() bool { return f.canOpen }

func (f *fakeNativeService) FileManager() string { return "explorer" }

func (f *fakeNativeService) Reveal(_ context.Context, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revealed = append(f.revealed, path)
	return nil
}

func (f *fakeNativeService) OpenWith(_ context.Context, path, applicationID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.openWith = append(f.openWith, [2]string{path, applicationID})
	return nil
}

func (f *fakeNativeService) Applications(context.Context, string) ([]native.Application, error) {
	return f.applications, nil
}

type nativeHarness struct {
	home   string
	srv    *transport.Server
	svc    *fakeNativeService
	client *http.Client
	base   string
}

func newNativeHarness(t *testing.T) *nativeHarness {
	t.Helper()
	home := t.TempDir()
	fake := &fakeNativeService{
		pickPath: filepath.Join(home, "picked"),
		canOpen:  true,
		applications: []native.Application{
			{ID: `C:\Windows\System32\notepad.exe`, Name: "Notepad", Default: true},
			{ID: "Microsoft.WindowsNotepad_8wekyb3d8bbwe!App", Name: "Notepad (UWP)"},
		},
	}
	settingsStore := newServeSettingsStore("", "ask", newModelOverrideStore(home), newPermissionOverrideStore(home))
	srv, err := transport.NewServer(transport.Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	attachOfficialNative(srv, fake, func() (string, error) {
		return ensureSettingsDocument(home, settingsStore)
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		srv.ServeOfficialNativeAPI(w, r, strings.TrimPrefix(r.URL.Path, "/api/"))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return &nativeHarness{home: home, srv: srv, svc: fake, client: ts.Client(), base: ts.URL}
}

func (h *nativeHarness) post(t *testing.T, method, body string) (int, map[string]any) {
	t.Helper()
	resp, err := h.client.Post(h.base+"/api/"+method, "application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("POST %s: %v", method, err)
	}
	defer resp.Body.Close()
	var decoded map[string]any
	raw := new(bytes.Buffer)
	_, _ = raw.ReadFrom(resp.Body)
	if strings.TrimSpace(raw.String()) != "" {
		if err := json.Unmarshal(raw.Bytes(), &decoded); err != nil {
			t.Fatalf("%s 响应不是 JSON: %q", method, raw.String())
		}
	}
	return resp.StatusCode, decoded
}

func TestNativeAPILifecycle(t *testing.T) {
	h := newNativeHarness(t)

	// pick：真实路径（服务注入）
	status, body := h.post(t, "directoryPicker.pick", `{}`)
	if status != 200 || body["result"] != h.svc.pickPath {
		t.Fatalf("pick = %d %#v", status, body)
	}
	// pick：取消 → null
	h.svc.pickErr = native.ErrCancelled
	status, body = h.post(t, "directoryPicker.pick", `{}`)
	if status != 200 || body["result"] != nil {
		t.Fatalf("pick 取消 = %d %#v", status, body)
	}
	h.svc.pickErr = nil

	// settings.openSettingsDocument：真实文件 + 打开
	status, body = h.post(t, "settings.openSettingsDocument", `{}`)
	if status != 200 || body["result"].(map[string]any)["opened"] != true {
		t.Fatalf("settings = %d %#v", status, body)
	}
	documentPath := filepath.Join(h.home, "settings.json")
	raw, err := os.ReadFile(documentPath)
	if err != nil {
		t.Fatalf("设置文档没生成: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("设置文档不是 JSON: %v", err)
	}
	if _, ok := document[settingsform.NamespaceModel]; !ok {
		t.Fatalf("设置文档缺 model 段: %#v", document)
	}
	if len(h.svc.opened) != 1 || h.svc.opened[0] != documentPath {
		t.Fatalf("没有用默认程序打开设置文档: %#v", h.svc.opened)
	}
	// 第二次不覆盖用户编辑
	if err := os.WriteFile(documentPath, []byte("{\"user\":\"edited\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if status, _ := h.post(t, "settings.openSettingsDocument", `{}`); status != 200 {
		t.Fatalf("第二次 settings = %d", status)
	}
	if raw, _ := os.ReadFile(documentPath); !strings.Contains(string(raw), "edited") {
		t.Fatalf("设置文档被覆盖: %q", raw)
	}

	// canOpenWorkspacePath
	status, body = h.post(t, "session.canOpenWorkspacePath", `{}`)
	if status != 200 || body["result"] != true {
		t.Fatalf("canOpen = %d %#v", status, body)
	}

	// openWorkspacePath：默认/reveal/application 三条路
	status, body = h.post(t, "session.openWorkspacePath", `{"path":"C:\\a.txt"}`)
	if status != 200 || len(h.svc.opened) != 3 {
		t.Fatalf("open 默认 = %d %#v opened=%v", status, body, h.svc.opened)
	}
	status, _ = h.post(t, "session.openWorkspacePath", `{"path":"C:\\a.txt","action":"reveal"}`)
	if status != 200 || len(h.svc.revealed) != 1 {
		t.Fatalf("open reveal = %d revealed=%v", status, h.svc.revealed)
	}
	status, _ = h.post(t, "session.openWorkspacePath", `{"path":"C:\\a.txt","application":"C:\\app.exe"}`)
	if status != 200 || len(h.svc.openWith) != 1 || h.svc.openWith[0][0] != `C:\a.txt` {
		t.Fatalf("open application = %d %#v", status, h.svc.openWith)
	}

	// workspacePathApplications
	status, body = h.post(t, "session.workspacePathApplications", `{"path":"C:\\a.txt"}`)
	apps, ok := body["result"].([]any)
	if status != 200 || !ok || len(apps) != 2 {
		t.Fatalf("applications = %d %#v", status, body)
	}
	first := apps[0].(map[string]any)
	if first["id"] != `C:\Windows\System32\notepad.exe` || first["default"] != true || first["icon"] != nil {
		t.Fatalf("application 形状 = %#v", first)
	}

	// 参数/未接线
	if status, _ := h.post(t, "session.openWorkspacePath", `{}`); status != 400 {
		t.Fatalf("缺 path = %d", status)
	}
	if status, _ := h.post(t, "session.openWorkspacePath", `{"path":"C:\\a","action":"run"}`); status != 400 {
		t.Fatalf("坏 action = %d", status)
	}
	if status, _ := h.post(t, "session.workspacePathApplications", `{"path":"C:\\a","extra":1}`); status != 400 {
		t.Fatalf("未知字段 = %d", status)
	}

	// 没有设置文档依赖 → 结构化 501（不是假成功）
	noDocSrv, err := transport.NewServer(transport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer noDocSrv.Close()
	attachOfficialNative(noDocSrv, h.svc, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/settings.openSettingsDocument", strings.NewReader(`{}`))
	noDocSrv.ServeOfficialNativeAPI(rec, req, "settings.openSettingsDocument")
	if rec.Code != 501 {
		t.Fatalf("没有设置文档 = %d %q", rec.Code, rec.Body.String())
	}
}

// TestNativeRealWindowsProbe 用真实 Windows API 查询文件关联与 canOpen（不弹框）。
func TestNativeRealWindowsProbe(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "probe.txt")
	if err := os.WriteFile(file, []byte("probe"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv, err := transport.NewServer(transport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	attachOfficialNative(srv, native.NewService(), nil)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		srv.ServeOfficialNativeAPI(w, r, strings.TrimPrefix(r.URL.Path, "/api/"))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := ts.Client().Post(ts.URL+"/api/session.canOpenWorkspacePath", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var canBody map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&canBody)
	resp.Body.Close()
	if resp.StatusCode != 200 || canBody["result"] != true {
		t.Fatalf("真实 canOpen = %d %#v", resp.StatusCode, canBody)
	}

	resp, err = ts.Client().Post(ts.URL+"/api/session.workspacePathApplications",
		"application/json", strings.NewReader(`{"path":`+jsonQuote(file)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	var appsBody map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&appsBody)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("真实 applications = %d %#v", resp.StatusCode, appsBody)
	}
	apps, _ := appsBody["result"].([]any)
	t.Logf("真实 .txt 关联处理程序：%d 个", len(apps))
	for _, app := range apps {
		row := app.(map[string]any)
		if row["id"] == "" || row["name"] == "" {
			t.Fatalf("处理程序缺 id/name: %#v", row)
		}
	}
}
