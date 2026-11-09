package main

// forgePanel worktree 6 方法在**真实宿主 + 真实临时 git repo + 真实会话日志**上的
// 端到端验收（HTTP 路由 → transport 契约 → worktree 服务 → git 子进程）。
//
// 覆盖：request → pre-step 真建 → hold 放行 → continueInProject 移回 →
// restore 重建（目录被删/分支被删两条路）→ 取消/重试拒绝 → 重启折叠。

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dshgo/internal/session"
	"dshgo/internal/transport"
	"dshgo/internal/worktree"
)

func canonicalTestPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	return filepath.Clean(path)
}

func wtGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newWorktreeRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	wtGit(t, dir, "init", "-q")
	wtGit(t, dir, "config", "user.email", "t103@example.com")
	wtGit(t, dir, "config", "user.name", "T103")
	wtGit(t, dir, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wtGit(t, dir, "add", "README.md")
	wtGit(t, dir, "commit", "-q", "-m", "init")
	return dir
}

type worktreeHarness struct {
	home   string
	repo   string
	srv    *transport.Server
	svc    *worktree.Service
	client *http.Client
	base   string
}

func newWorktreeHarness(t *testing.T) *worktreeHarness {
	t.Helper()
	home := t.TempDir()
	repo := newWorktreeRepo(t)
	src := newSessionSource(home, "")
	srv, err := transport.NewServer(transport.Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	svc := attachOfficialWorktree(srv, src)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		srv.ServeOfficialForgeWorktreeAPI(w, r, strings.TrimPrefix(r.URL.Path, "/api/"))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return &worktreeHarness{home: home, repo: repo, srv: srv, svc: svc, client: ts.Client(), base: ts.URL}
}

func (h *worktreeHarness) newSession(t *testing.T) string {
	t.Helper()
	id := session.NewID()
	logw, err := session.CreateSession(h.home, h.repo, id, "standard")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := logw.Close(); err != nil {
		t.Fatalf("Close session: %v", err)
	}
	return id
}

func (h *worktreeHarness) appendEvent(t *testing.T, sessionID, eventType string, data any) {
	t.Helper()
	path, err := newSessionSource(h.home, "").resolve(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	logw, err := session.OpenSessionAt(path)
	if err != nil {
		t.Fatal(err)
	}
	defer logw.Close()
	if err := logw.Append(eventType, data); err != nil {
		t.Fatal(err)
	}
}

type worktreeResponse struct {
	status int
	body   map[string]any
}

func (h *worktreeHarness) call(t *testing.T, method, body string) worktreeResponse {
	t.Helper()
	resp, err := h.client.Post(h.base+"/api/"+method, "application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("POST %s: %v", method, err)
	}
	defer resp.Body.Close()
	var decoded map[string]any
	raw := new(bytes.Buffer)
	if _, err := raw.ReadFrom(resp.Body); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(raw.String()) != "" {
		if err := json.Unmarshal(raw.Bytes(), &decoded); err != nil {
			t.Fatalf("%s 响应不是 JSON: %q", method, raw.String())
		}
	}
	return worktreeResponse{status: resp.StatusCode, body: decoded}
}

func (h *worktreeHarness) state(t *testing.T, sessionID string) worktree.State {
	t.Helper()
	state, err := h.svc.State(sessionID)
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	return state
}

func (h *worktreeHarness) errorCode(t *testing.T, resp worktreeResponse) string {
	t.Helper()
	if resp.body == nil {
		t.Fatalf("没有错误体（status=%d）", resp.status)
	}
	errObj, ok := resp.body["error"].(map[string]any)
	if !ok {
		t.Fatalf("没有四字段错误：%#v", resp.body)
	}
	for _, field := range []string{"code", "message", "hint", "retryable"} {
		if _, ok := errObj[field]; !ok {
			t.Fatalf("错误缺 %s：%#v", field, errObj)
		}
	}
	return errObj["code"].(string)
}

func TestWorktreeAPILifecycleEndToEnd(t *testing.T) {
	h := newWorktreeHarness(t)
	sessionID := h.newSession(t)

	// 1) requestWorktree：真实记录（open），还没有目录。
	resp := h.call(t, "forgePanel.requestWorktree", `{"sessionId":`+jsonQuote(sessionID)+`,"from":null}`)
	if resp.status != http.StatusOK {
		t.Fatalf("requestWorktree = %d %#v", resp.status, resp.body)
	}
	if result, ok := resp.body["result"].(map[string]any); !ok || len(result) != 0 {
		t.Fatalf("requestWorktree 官方 void → result 空对象，实际 %#v", resp.body)
	}
	state := h.state(t, sessionID)
	if state.Status != worktree.StatusOpen || state.Branch == "" {
		t.Fatalf("请求后状态 = %+v", state)
	}
	branch := state.Branch
	dir := filepath.Join(h.repo, ".agents", "worktrees", branch)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("pre-step 前不应创建目录: %v", err)
	}

	// 2) inbox hold + pre-step：消息等待，创建完成后放行。
	holdDone := make(chan error, 1)
	go func() { holdDone <- worktreeHold(context.Background(), h.svc, sessionID) }()
	select {
	case err := <-holdDone:
		t.Fatalf("创建前 hold 不应放行: %v", err)
	case <-time.After(80 * time.Millisecond):
	}
	preStep := worktreePreStep(h.svc, sessionID, nil)
	if err := preStep(context.Background(), 1); err != nil {
		t.Fatalf("pre-step: %v", err)
	}
	select {
	case err := <-holdDone:
		if err != nil {
			t.Fatalf("创建后 hold = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("创建后 hold 没放行")
	}
	state = h.state(t, sessionID)
	if state.Status != worktree.StatusCreated {
		t.Fatalf("创建后状态 = %+v", state)
	}
	if info, err := os.Stat(filepath.Join(dir, "README.md")); err != nil || info.IsDir() {
		t.Fatalf("worktree 没有真实检出文件: %v", err)
	}
	if !strings.Contains(wtGit(t, h.repo, "branch", "--list", branch), branch) {
		t.Fatalf("git 里没有分支 %s", branch)
	}
	if canonicalTestPath(state.Cwd) != canonicalTestPath(dir) {
		t.Fatalf("会话目录 = %q，期望 %q", state.Cwd, dir)
	}

	// 2b) T155：目录被外部删除后，上一次 restore 中断（add 成功、记录没写）——
	// restore 必须幂等完成，而不是 409 present。
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	wtGit(t, h.repo, "worktree", "prune")
	wtGit(t, h.repo, "worktree", "add", dir, branch)
	if resp := h.call(t, "forgePanel.restoreWorktree", `{"sessionId":`+jsonQuote(sessionID)+`}`); resp.status != 200 {
		t.Fatalf("中断 restore = %d %#v", resp.status, resp.body)
	}
	state = h.state(t, sessionID)
	if state.Status != worktree.StatusCreated || canonicalTestPath(state.Cwd) != canonicalTestPath(dir) {
		t.Fatalf("resume 后状态 = %+v", state)
	}

	// 2c) 用户真的在这个 checkout 里工作（HEAD 移动）：必须仍按官方 409 present。
	if err := os.WriteFile(filepath.Join(dir, "user.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	wtGit(t, dir, "add", "user.txt")
	wtGit(t, dir, "commit", "-q", "-m", "user work")
	if resp := h.call(t, "forgePanel.restoreWorktree", `{"sessionId":`+jsonQuote(sessionID)+`}`); resp.status != 409 ||
		h.errorCode(t, resp) != worktree.CodeWorktreePresent {
		t.Fatalf("真实 present = %d %s", resp.status, h.errorCode(t, resp))
	}

	// 3) continueInProject：会话移回源 checkout，记录结束。
	resp = h.call(t, "forgePanel.continueInProject", `{"sessionId":`+jsonQuote(sessionID)+`}`)
	if resp.status != http.StatusOK {
		t.Fatalf("continueInProject = %d %#v", resp.status, resp.body)
	}
	state = h.state(t, sessionID)
	if state.Status != worktree.StatusWithdrawn || canonicalTestPath(state.Cwd) != canonicalTestPath(h.repo) {
		t.Fatalf("continueInProject 后 = %+v", state)
	}
	if resp := h.call(t, "forgePanel.restoreWorktree", `{"sessionId":`+jsonQuote(sessionID)+`}`); resp.status != 409 ||
		h.errorCode(t, resp) != worktree.CodeNotCreated {
		t.Fatalf("已结束后 restore = %d %s", resp.status, h.errorCode(t, resp))
	}

	// 4) 另一个会话：restore 重建被删的 worktree（目录被删 + 分支被删两条路）。
	otherID := h.newSession(t)
	if resp := h.call(t, "forgePanel.requestWorktree", `{"sessionId":`+jsonQuote(otherID)+`}`); resp.status != 200 {
		t.Fatalf("other request = %d", resp.status)
	}
	if err := worktreePreStep(h.svc, otherID, nil)(context.Background(), 1); err != nil {
		t.Fatalf("other pre-step: %v", err)
	}
	otherState := h.state(t, otherID)
	otherDir := otherState.Directory
	otherBranch := otherState.Branch
	if err := os.RemoveAll(otherDir); err != nil {
		t.Fatal(err)
	}
	if resp := h.call(t, "forgePanel.restoreWorktree", `{"sessionId":`+jsonQuote(otherID)+`}`); resp.status != 200 {
		t.Fatalf("restore = %d %#v", resp.status, resp.body)
	}
	if _, err := os.Stat(filepath.Join(otherDir, "README.md")); err != nil {
		t.Fatalf("restore 后没有文件: %v", err)
	}
	// 分支也被删：从起始 commit 重建。
	if err := os.RemoveAll(otherDir); err != nil {
		t.Fatal(err)
	}
	wtGit(t, h.repo, "worktree", "prune")
	wtGit(t, h.repo, "branch", "-D", otherBranch)
	if resp := h.call(t, "forgePanel.restoreWorktree", `{"sessionId":`+jsonQuote(otherID)+`}`); resp.status != 200 {
		t.Fatalf("分支删除后 restore = %d %#v", resp.status, resp.body)
	}
	if _, err := os.Stat(filepath.Join(otherDir, "README.md")); err != nil {
		t.Fatalf("从 baseCommit 重建后没有文件: %v", err)
	}

	// 5) 参数/状态错误：四字段、正确码。
	if resp := h.call(t, "forgePanel.requestWorktree", `{"sessionId":`+jsonQuote(otherID)+`,"extra":1}`); resp.status != 400 ||
		h.errorCode(t, resp) != "bad_request" {
		t.Fatalf("未知字段 = %d %s", resp.status, h.errorCode(t, resp))
	}
	if resp := h.call(t, "forgePanel.retryWorktree", `{"sessionId":`+jsonQuote(otherID)+`}`); resp.status != 409 ||
		h.errorCode(t, resp) != worktree.CodeNotWaiting {
		t.Fatalf("已创建后 retry = %d %s", resp.status, h.errorCode(t, resp))
	}
	startedID := h.newSession(t)
	h.appendEvent(t, startedID, "turn/start", map[string]any{"turn": 1})
	if resp := h.call(t, "forgePanel.requestWorktree", `{"sessionId":`+jsonQuote(startedID)+`}`); resp.status != 400 ||
		h.errorCode(t, resp) != worktree.CodeSessionStarted {
		t.Fatalf("turn 已开始 request = %d %s", resp.status, h.errorCode(t, resp))
	}

	// 6) 重启：新 Server + 新 service 读同一份会话日志。
	restartedSrv, err := transport.NewServer(transport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer restartedSrv.Close()
	restarted := attachOfficialWorktree(restartedSrv, newSessionSource(h.home, ""))
	restartedState, err := restarted.State(otherID)
	if err != nil || restartedState.Status != worktree.StatusCreated || restartedState.Directory == "" {
		t.Fatalf("重启后状态 = %+v err=%v", restartedState, err)
	}
	// 重启后目录被删：新 service 必须能恢复（不能因为旧实例的 present 状态卡住）。
	if err := os.RemoveAll(restartedState.Directory); err != nil {
		t.Fatal(err)
	}
	if err := restarted.RestoreWorktree(otherID); err != nil {
		t.Fatalf("重启后 restore: %v", err)
	}
	restartedState, err = restarted.State(otherID)
	if err != nil || restartedState.Status != worktree.StatusCreated {
		t.Fatalf("重启恢复后状态 = %+v err=%v", restartedState, err)
	}
	if _, err := os.Stat(filepath.Join(restartedState.Directory, "README.md")); err != nil {
		t.Fatalf("重启恢复后没有文件: %v", err)
	}
	if err := worktreeHold(context.Background(), restarted, otherID); err != nil {
		t.Fatalf("重启后 hold 应放行: %v", err)
	}
}
