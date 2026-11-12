package main

// rc.2「归档会话时清理工作树」在**真实宿主**上的验收：真临时 home + 真临时 git
// 仓库 + 真 `/api/workspace.archiveSession` 路由（srv.ServeHTTP）+ 真 git 子进程。
//
// 覆盖任务书的四步接线：
//
//  1. `AttachOfficialForgeRC2API` 的会话目录解析器与 worktree 服务（见
//     serve_forge_rc2_test.go 的 worktreeCleanup 用例）；
//  2. workspace.archiveSession **成功之后**自动排期清理（attachWorktreeArchive 的包装）；
//  3. `ArchiveHost` 的三个回调都是**真实判断**：
//     RetainOnArchive（设置文件）/ Unused（是否还有未归档会话引用）/
//     ReadOnly（创建者会话的沙箱策略）；
//  4. `worktreeSessionStore.AppendCreated` 真的落 `plugin:worktree/created`
//     （没有它就没有任何清理候选，归档永远不会扫）。
//
// 终态判据与官方 archive.ts 一致：干净 checkout → removed；脏 checkout、仍被引用、
// 创建者只读、设置要求保留 → retained；**分支与提交永远保留**。

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dshgo/internal/modes"
	"dshgo/internal/session"
	"dshgo/internal/transport"
	"dshgo/internal/worktree"
)

// archiveCleanupWait 是一条清理在测试里的等待上限（真 git 命令，给足余量）。
const archiveCleanupWait = 30 * time.Second

// archiveHarness 是一套"真 home + 真 git 仓库 + 真 transport 分派"的归档清理夹具。
type archiveHarness struct {
	home string
	repo string
	src  *sessionSource
	srv  *transport.Server
	svc  *worktree.Service
	ws   *officialWorkspaceHost
}

func newArchiveHarness(t *testing.T) *archiveHarness {
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
	t.Cleanup(svc.Close)
	ws := attachOfficialWorkspace(srv, home, src, nil)
	if ws == nil {
		t.Fatal("attachOfficialWorkspace 返回 nil")
	}
	presets, err := modes.NewRegistry(nil)
	if err != nil {
		t.Fatalf("modes.NewRegistry: %v", err)
	}
	sandboxModes := newSandboxModeStore(home)
	if err := sandboxModes.Load(); err != nil {
		t.Fatalf("sandboxModeStore.Load: %v", err)
	}
	// 这就是 serve.go 里接线的那一行（同一个 service、同一个 store 实现）。
	attachWorktreeArchive(srv, worktreeArchiveDeps{
		Home: home, Workspace: ws, Service: svc,
		Sessions: &worktreeSessionStore{src: src},
		Presets:  presets, Modes: sandboxModes,
	})
	return &archiveHarness{home: home, repo: repo, src: src, srv: srv, svc: svc, ws: ws}
}

// newSession 建一份真实 v4 会话（cwd = 临时仓库根）。
func (h *archiveHarness) newSession(t *testing.T, preset string) string {
	t.Helper()
	return h.newSessionAt(t, h.repo, preset)
}

// newSessionAt 建一份真实 v4 会话（cwd 可指定）。
func (h *archiveHarness) newSessionAt(t *testing.T, cwd, preset string) string {
	t.Helper()
	id := session.NewID()
	logw, err := session.CreateSession(h.home, cwd, id, preset)
	if err != nil {
		t.Fatalf("CreateSession(%s, preset=%s): %v", cwd, preset, err)
	}
	if err := logw.Close(); err != nil {
		t.Fatalf("Close session: %v", err)
	}
	return id
}

// appendEvent 往会话日志追加一条真实事件。
func (h *archiveHarness) appendEvent(t *testing.T, sessionID, eventType string, data any) {
	t.Helper()
	path, err := h.src.resolve(sessionID)
	if err != nil {
		t.Fatalf("resolve(%s): %v", sessionID, err)
	}
	logw, err := session.OpenSessionAt(path)
	if err != nil {
		t.Fatalf("OpenSessionAt(%s): %v", path, err)
	}
	defer func() { _ = logw.Close() }()
	if err := logw.Append(eventType, data); err != nil {
		t.Fatalf("append %s: %v", eventType, err)
	}
}

// createWorktree 走真实链路建 checkout：request → pre-step（真 git add/reset）。
func (h *archiveHarness) createWorktree(t *testing.T, sessionID string) worktree.State {
	t.Helper()
	if err := h.svc.RequestWorktree(sessionID, nil); err != nil {
		t.Fatalf("RequestWorktree(%s): %v", sessionID, err)
	}
	if err := h.svc.PreStep(context.Background(), sessionID, 1); err != nil {
		t.Fatalf("PreStep（真建 worktree）: %v", err)
	}
	state, err := h.svc.State(sessionID)
	if err != nil {
		t.Fatalf("State(%s): %v", sessionID, err)
	}
	if state.Status != worktree.StatusCreated {
		t.Fatalf("worktree 状态 = %s（%+v），期望 created", state.Status, state)
	}
	if strings.TrimSpace(state.Directory) == "" || strings.TrimSpace(state.Branch) == "" {
		t.Fatalf("创建的 state 缺目录/分支：%+v", state)
	}
	if _, err := os.Stat(state.Directory); err != nil {
		t.Fatalf("checkout 目录没建出来（%s）: %v", state.Directory, err)
	}
	return state
}

// archive 打真实的 /api/workspace.archiveSession，返回归档集合。
func (h *archiveHarness) archive(t *testing.T, sessionID string) []string {
	t.Helper()
	body, err := json.Marshal(map[string]any{"sessionId": sessionID})
	if err != nil {
		t.Fatalf("序列化请求: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/workspace.archiveSession", bytes.NewReader(body))
	req.AddCookie(&http.Cookie{Name: "dshd_session", Value: h.srv.MintInternalCookie()})
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("workspace.archiveSession(%s) = %d %s", sessionID, rec.Code, rec.Body.String())
	}
	var envelope struct {
		Result struct {
			ArchivedSessionIDs []string `json:"archivedSessionIds"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("archiveSession 响应不是 JSON 对象: %q", rec.Body.String())
	}
	return envelope.Result.ArchivedSessionIDs
}

// noAttempt 断言这个归档**没有**产生清理尝试（设置要求保留时就是这样）。
func (h *archiveHarness) noAttempt(t *testing.T, sessionID string) {
	t.Helper()
	if cleanup, ok := h.svc.CleanupOf(sessionID, nil); ok || cleanup != nil {
		t.Fatalf("%s 不应有清理尝试，实际 %+v", sessionID, cleanup)
	}
}

// waitSettled 轮询到非 scheduled 的终态。
func (h *archiveHarness) waitSettled(t *testing.T, sessionID string) worktree.Cleanup {
	t.Helper()
	deadline := time.Now().Add(archiveCleanupWait)
	for {
		cleanup, ok := h.svc.CleanupOf(sessionID, nil)
		if ok && cleanup != nil && cleanup.Status != worktree.CleanupScheduled {
			return *cleanup
		}
		if time.Now().After(deadline) {
			if ok && cleanup != nil {
				t.Fatalf("%s 的清理停在 %s（期望终态）", sessionID, cleanup.Status)
			}
			t.Fatalf("%s 没有清理尝试（期望有）：AppendCreated 或归档后排期没有接上", sessionID)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// mustCheckoutGone 断言 checkout 目录已被删掉。
func mustCheckoutGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return
	} else if err != nil {
		t.Fatalf("checkout 状态不可判定（%s）: %v", path, err)
	}
	t.Fatalf("干净 checkout 应已被移除：%s", path)
}

// mustCheckoutKept 断言 checkout 目录还在（保留方向）。
func mustCheckoutKept(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("checkout 应保留（%s）: %v", path, err)
	}
}

// mustBranchKept 断言分支还在（分支与提交永远保留）。
func mustBranchKept(t *testing.T, repo, branch string) {
	t.Helper()
	if got := wtGit(t, repo, "branch", "--list", branch); !strings.Contains(got, branch) {
		t.Fatalf("分支 %s 必须永远保留，实际 %q", branch, got)
	}
}

// mustNotInWorktreeList 断言源仓库的 worktree 注册表里已经没有被删的条目。
func mustNotInWorktreeList(t *testing.T, repo, path string) {
	t.Helper()
	list := wtGit(t, repo, "worktree", "list", "--porcelain")
	for _, line := range strings.Split(list, "\n") {
		if strings.HasPrefix(line, "worktree ") && canonicalTestPath(strings.TrimSpace(strings.TrimPrefix(line, "worktree "))) == canonicalTestPath(path) {
			t.Fatalf("被删的 checkout 仍在 git worktree list 里：%s", path)
		}
	}
}

// ---------------------------------------------------------------------------
// 1. 干净 checkout：归档会话 → removed；分支与提交保留
// ---------------------------------------------------------------------------

func TestWorktreeArchiveCleanupRemovesCleanCheckout(t *testing.T) {
	h := newArchiveHarness(t)
	sessionID := h.newSession(t, modes.DefaultID)
	state := h.createWorktree(t, sessionID)
	baseCommit := wtGit(t, h.repo, "rev-parse", "HEAD")

	if archived := h.archive(t, sessionID); len(archived) != 1 || archived[0] != sessionID {
		t.Fatalf("归档集合不对：%v（期望 [%s]）", archived, sessionID)
	}
	cleanup := h.waitSettled(t, sessionID)
	if cleanup.Status != worktree.CleanupRemoved {
		t.Fatalf("干净 checkout 的清理状态 = %s，期望 removed", cleanup.Status)
	}
	if cleanup.Revision < 1 {
		t.Fatalf("revision = %d，期望从 1 开始递增", cleanup.Revision)
	}
	mustCheckoutGone(t, state.Directory)
	mustNotInWorktreeList(t, h.repo, state.Directory)
	// 分支与提交永远保留。
	mustBranchKept(t, h.repo, state.Branch)
	if got := wtGit(t, h.repo, "rev-parse", state.Branch); got != baseCommit {
		t.Fatalf("分支 %s 指向 %s，期望 %s（提交不许被动过）", state.Branch, got, baseCommit)
	}
	// 会话日志本身还在（归档不是删除）。
	if _, err := h.src.resolve(sessionID); err != nil {
		t.Fatalf("归档后会话日志应仍在：%v", err)
	}
}

// ---------------------------------------------------------------------------
// 2. 脏 checkout：归档会话 → retained（改动丢不起）；分支保留
// ---------------------------------------------------------------------------

func TestWorktreeArchiveCleanupRetainsDirtyCheckout(t *testing.T) {
	h := newArchiveHarness(t)
	sessionID := h.newSession(t, modes.DefaultID)
	state := h.createWorktree(t, sessionID)
	// 未跟踪文件：git 会连它一起删，所以必须保留整个 checkout。
	if err := os.WriteFile(filepath.Join(state.Directory, "dirty.txt"), []byte("还没提交\n"), 0o644); err != nil {
		t.Fatalf("写脏文件: %v", err)
	}

	h.archive(t, sessionID)
	cleanup := h.waitSettled(t, sessionID)
	if cleanup.Status != worktree.CleanupRetained {
		t.Fatalf("脏 checkout 的清理状态 = %s，期望 retained", cleanup.Status)
	}
	mustCheckoutKept(t, state.Directory)
	mustBranchKept(t, h.repo, state.Branch)
}

// ---------------------------------------------------------------------------
// 3. 仍被未归档会话引用：retained（Unused 的真实判断）
// ---------------------------------------------------------------------------

func TestWorktreeArchiveCleanupRetainsReferencedCheckout(t *testing.T) {
	h := newArchiveHarness(t)
	creator := h.newSession(t, modes.DefaultID)
	state := h.createWorktree(t, creator)
	// 另一个**未归档**会话就住在这个 checkout 里：它还在用，不能删。
	occupant := h.newSessionAt(t, state.Directory, modes.DefaultID)

	h.archive(t, creator)
	cleanup := h.waitSettled(t, creator)
	if cleanup.Status != worktree.CleanupRetained {
		t.Fatalf("仍被 %s 引用的 checkout 状态 = %s，期望 retained", occupant, cleanup.Status)
	}
	mustCheckoutKept(t, state.Directory)
	mustBranchKept(t, h.repo, state.Branch)
}

// 子代理会话跟着已归档的祖先一起算已归档（官方 ancestorArchived）：否则一条
// 子代理会话就能让 checkout 永远清不掉。
func TestWorktreeArchiveCleanupSkipsArchivedSubagent(t *testing.T) {
	h := newArchiveHarness(t)
	creator := h.newSession(t, modes.DefaultID)
	state := h.createWorktree(t, creator)
	child := h.newSessionAt(t, state.Directory, modes.DefaultID)
	h.appendEvent(t, child, session.EventChildSpawn, map[string]any{
		"childSessionId": child, "parentSessionId": creator, "cwd": state.Directory,
	})

	h.archive(t, creator)
	cleanup := h.waitSettled(t, creator)
	if cleanup.Status != worktree.CleanupRemoved {
		t.Fatalf("祖先已归档的子代理会话不该挡住清理：状态 = %s，期望 removed", cleanup.Status)
	}
	mustCheckoutGone(t, state.Directory)
	mustBranchKept(t, h.repo, state.Branch)
}

// ---------------------------------------------------------------------------
// 4. 设置要求保留：不排期（官方默认 false）
// ---------------------------------------------------------------------------

func TestWorktreeArchiveCleanupHonorsRetainSetting(t *testing.T) {
	h := newArchiveHarness(t)
	settingsPath := filepath.Join(h.home, "state", "worktree-settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatalf("建 state 目录: %v", err)
	}
	if err := os.WriteFile(settingsPath, []byte(`{"retainOnArchive": true}`), 0o600); err != nil {
		t.Fatalf("写设置: %v", err)
	}
	sessionID := h.newSession(t, modes.DefaultID)
	state := h.createWorktree(t, sessionID)

	h.archive(t, sessionID)
	h.noAttempt(t, sessionID)
	mustCheckoutKept(t, state.Directory)
	mustBranchKept(t, h.repo, state.Branch)
}

// ---------------------------------------------------------------------------
// 5. 创建者会话只读：retained（ReadOnly 的真实判断）
// ---------------------------------------------------------------------------

func TestWorktreeArchiveCleanupRetainsReadOnlyCreator(t *testing.T) {
	h := newArchiveHarness(t)
	// 内置 preset `read-only` → 真实 sandbox 档位 read-only（modes.Builtins）。
	sessionID := h.newSession(t, "read-only")
	state := h.createWorktree(t, sessionID)

	h.archive(t, sessionID)
	cleanup := h.waitSettled(t, sessionID)
	if cleanup.Status != worktree.CleanupRetained {
		t.Fatalf("只读创建者的 checkout 状态 = %s，期望 retained", cleanup.Status)
	}
	mustCheckoutKept(t, state.Directory)
	mustBranchKept(t, h.repo, state.Branch)
}

// ---------------------------------------------------------------------------
// 6. serve.go 那两行接上之后：rc.2 的 forgePanel.worktreeCleanup 在真实 Server 上可用
// ---------------------------------------------------------------------------

// TestForgeRC2WiringReadsArchiveCleanup 走**真实 HTTP 路由**调
// `forgePanel.worktreeCleanup`（server.go 的 `IsOfficialForgeLiveshareMethod` 分派），
// 证明接线四步里的第 1、2、4 步在同一个宿主上协同工作：
//   - AttachOfficialForgeRC2API 挂上 12 个方法（会话目录解析器 = src.Cwd）；
//   - 第二个参数是与 worktree 面板**同一个** service（revision/状态同源）；
//   - 归档后排期 → 这个端点读得到终态。
//
// 没接 worktree 服务时这条方法按官方回 null；接了之后必须读到真实清理状态，
// 两者都不是假造的成功。
func TestForgeRC2WiringReadsArchiveCleanup(t *testing.T) {
	h := newArchiveHarness(t)
	sessionID := h.newSession(t, modes.DefaultID)
	state := h.createWorktree(t, sessionID)

	cleanupBody := func(id string) string {
		raw, err := json.Marshal(map[string]any{"sessionId": id, "revision": nil})
		if err != nil {
			t.Fatalf("序列化请求: %v", err)
		}
		return string(raw)
	}
	call := func(id string) (int, string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost,
			"/api/"+string(transport.MethodForgePanelWorktreeCleanup), strings.NewReader(cleanupBody(id)))
		req.AddCookie(&http.Cookie{Name: "dshd_session", Value: h.srv.MintInternalCookie()})
		rec := httptest.NewRecorder()
		h.srv.ServeHTTP(rec, req)
		return rec.Code, strings.TrimSpace(rec.Body.String())
	}

	// 1) 还没挂 rc.2 面：501（点名缺什么），不是 404、也不是静默成功。
	code, body := call(sessionID)
	if code != http.StatusNotImplemented {
		t.Fatalf("没挂实现时 forgePanel.worktreeCleanup = %d %s，期望 501", code, body)
	}

	// 2) 按 serve.go 的接线挂上：同一个 src.Cwd 解析器 + 同一个 worktree service。
	h.srv.AttachOfficialForgeRC2API(newForgeRC2API(func(id string) (string, error) {
		return h.src.Cwd(id)
	}, h.svc))
	t.Cleanup(h.srv.DetachOfficialForgeRC2API)

	// 归档之前：没有清理尝试 → 官方 null（不是假造一个 scheduled）。
	if code, body := call(sessionID); code != http.StatusOK || body != `{"result":null}` {
		t.Fatalf("归档前 worktreeCleanup = %d %s，期望 200 {\"result\":null}", code, body)
	}

	// 3) 归档 → 自动排期 → 端点读到真实终态。
	h.archive(t, sessionID)
	settled := h.waitSettled(t, sessionID)
	code, body = call(sessionID)
	if code != http.StatusOK {
		t.Fatalf("归档后 worktreeCleanup = %d %s，期望 200", code, body)
	}
	var envelope struct {
		Result *struct {
			Revision int64  `json:"revision"`
			Status   string `json:"status"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("worktreeCleanup 响应不是 JSON 对象: %q", body)
	}
	if envelope.Result == nil {
		t.Fatalf("归档后 worktreeCleanup 结果是 null（清理状态没接上）：%s", body)
	}
	if envelope.Result.Revision != settled.Revision || envelope.Result.Status != string(settled.Status) {
		t.Fatalf("worktreeCleanup = {%d,%s}，期望 {%d,%s}",
			envelope.Result.Revision, envelope.Result.Status, settled.Revision, settled.Status)
	}
	if envelope.Result.Status != string(worktree.CleanupRemoved) {
		t.Fatalf("干净 checkout 的清理状态 = %s，期望 removed", envelope.Result.Status)
	}
	mustCheckoutGone(t, state.Directory)
	mustBranchKept(t, h.repo, state.Branch)
}

// ---------------------------------------------------------------------------
// 7. 三个主机回调的单元级判据
// ---------------------------------------------------------------------------
// TestWorktreeArchiveSettings 钉住最小设置读取点的语义（官方默认 false）。
func TestWorktreeArchiveSettings(t *testing.T) {
	home := t.TempDir()
	settings := newWorktreeArchiveSettings(home)
	if settings.RetainOnArchive() {
		t.Fatal("没有任何设置文件时应为官方默认 false（归档时清理）")
	}
	if newWorktreeArchiveSettings("").RetainOnArchive() {
		t.Fatal("没有 home 时应为官方默认 false")
	}
	path := filepath.Join(home, "state", "worktree-settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("建 state 目录: %v", err)
	}
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"显式打开", `{"retainOnArchive": true}`, true},
		{"显式关闭", `{"retainOnArchive": false}`, false},
		{"没有这个键", `{"别的键": 1}`, false},
	}
	for _, tc := range cases {
		if err := os.WriteFile(path, []byte(tc.raw), 0o600); err != nil {
			t.Fatalf("写 %s: %v", tc.name, err)
		}
		if got := newWorktreeArchiveSettings(home).RetainOnArchive(); got != tc.want {
			t.Fatalf("%s：RetainOnArchive = %v，期望 %v", tc.name, got, tc.want)
		}
	}
	// 文件坏了 = 拿不到答案 → 保守保留（不删任何 checkout）。
	if err := os.WriteFile(path, []byte(`{not json`), 0o600); err != nil {
		t.Fatalf("写坏设置: %v", err)
	}
	if !newWorktreeArchiveSettings(home).RetainOnArchive() {
		t.Fatal("设置文件坏了时必须按保留处理（拿不到答案不删 checkout）")
	}
}

// TestArchiveFactsFoldsCreatedAndSpawn 钉住 ArchiveFacts 的三件事：有效目录、
// 创建记录、子代理父子帧。AppendCreated 不接上时这里就是空的（清理永远没有候选）。
func TestArchiveFactsFoldsCreatedAndSpawn(t *testing.T) {
	home := t.TempDir()
	repo := newWorktreeRepo(t)
	id := session.NewID()
	logw, err := session.CreateSession(home, repo, id, modes.DefaultID)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	creation := worktree.Creation{
		Path:           filepath.Join(repo, ".agents", "worktrees", "dsh-aaaaaaaa-0001"),
		Branch:         "dsh-aaaaaaaa-0001",
		BaseCommit:     wtGit(t, repo, "rev-parse", "HEAD"),
		RepositoryRoot: repo,
	}
	if err := logw.Append(worktree.EventTypeCreated, creation); err != nil {
		t.Fatalf("写 created 记录: %v", err)
	}
	if err := logw.Append(session.EventChildSpawn, map[string]any{
		"childSessionId": id, "parentSessionId": "parent-under-test", "cwd": repo,
	}); err != nil {
		t.Fatalf("写 subagent/spawn 帧: %v", err)
	}
	if err := logw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	store := &worktreeSessionStore{src: newSessionSource(home, "")}
	// AppendCreated 是 CreatedAppender：宿主 store 必须实现它（可选扩展）。
	if _, ok := any(store).(worktree.CreatedAppender); !ok {
		t.Fatal("worktreeSessionStore 必须实现 worktree.CreatedAppender，否则归档没有清理候选")
	}
	snapshot, parent, err := store.ArchiveFacts(id)
	if err != nil {
		t.Fatalf("ArchiveFacts: %v", err)
	}
	if len(snapshot.Creations) != 1 || snapshot.Creations[0].Path != creation.Path {
		t.Fatalf("创建记录没折出来：%+v", snapshot.Creations)
	}
	if parent != "parent-under-test" {
		t.Fatalf("子代理父会话 = %q，期望 parent-under-test", parent)
	}
	if canonicalTestPath(snapshot.Cwd) != canonicalTestPath(repo) {
		t.Fatalf("有效工作目录 = %q，期望 %q", snapshot.Cwd, repo)
	}
}
