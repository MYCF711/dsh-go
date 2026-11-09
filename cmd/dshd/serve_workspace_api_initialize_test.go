package main

// 官方 `workspace/initializeDefault` 的**宿主侧**测试：真 registry 文件、真目录、
// 真 v4 会话历史；首次创建 created=true，二次（含进程重启）复用同一个 id。
//
// 契约原件：tmp/GO2-TEAM/T01/official-ref/dsh-workspace__lib__index.js:425-438
//   - defaultWorkspaceId 已存在 → 复用它（即使后来有了别的工作区/会话）；
//   - registry、归档集、会话历史任一非空 → undefined（不适用）；
//   - 解析 `<Documents>/deepseek-harness/default-workspace` → MkdirAll → 真实
//     canonical 路径；标题取请求路径末段 `default-workspace`；
//   - 删除该注册后自动创建永久禁用（指针保留、记录不在 → undefined）。

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// workspaceInitializeDefaultRegistry 读取 registry 文件为可断言的结构。
func workspaceInitializeDefaultRegistry(t *testing.T, home string) officialWorkspaceDocument {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(home, "state", "workspaces.json"))
	if err != nil {
		t.Fatalf("读 registry: %v", err)
	}
	var doc officialWorkspaceDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("解析 registry: %v", err)
	}
	return doc
}

// TestWorkspaceInitializeDefaultFirstUseCreatesThenReusesSameID 验收主链：
// 首次 created=true 且真建目录/真落盘；二次（同进程 + 重启）同一 id、created=false。
func TestWorkspaceInitializeDefaultFirstUseCreatesThenReusesSameID(t *testing.T) {
	home := t.TempDir()
	documents := t.TempDir()
	h := newOfficialWorkspaceHarnessAt(t, home)
	h.host.documentsDir = func() (string, error) { return documents, nil }

	first, created, err := h.host.initializeDefault()
	if err != nil {
		t.Fatalf("首次 initializeDefault: %v", err)
	}
	if !created {
		t.Fatal("首次 initializeDefault 必须 created=true")
	}
	if first.Workspace.WorkspaceID == "" {
		t.Fatal("首次 initializeDefault 没给出 workspaceId")
	}
	id := first.Workspace.WorkspaceID
	if first.Workspace.Title != "default-workspace" {
		t.Fatalf("初始标题应取请求路径末段 default-workspace，实际 %q", first.Workspace.Title)
	}
	wantDir := filepath.Join(documents, "deepseek-harness", "default-workspace")
	// t.TempDir 在 Windows 上可能是 8.3 短名；宿主的 canonical 路径是真实长名。
	wantCanonical := officialCanonicalPath(wantDir)
	if resolved, rerr := filepath.EvalSymlinks(wantCanonical); rerr == nil {
		wantCanonical = filepath.Clean(resolved)
	}
	if !officialSamePath(first.Workspace.Path, wantCanonical) {
		t.Fatalf("默认工作区路径应是 %q（canonical %q），实际 %q", wantDir, wantCanonical, first.Workspace.Path)
	}
	st, err := os.Stat(wantDir)
	if err != nil || !st.IsDir() {
		t.Fatalf("默认工作区目录没有真建出来（%s）：%v", wantDir, err)
	}
	if _, err := time.Parse(time.RFC3339, first.Workspace.CreatedAt); err != nil {
		t.Fatalf("createdAt 不是真实 ISO-8601：%q（%v）", first.Workspace.CreatedAt, err)
	}

	// 真落盘：registry 里必须有这条记录与默认指针。
	doc := workspaceInitializeDefaultRegistry(t, home)
	if doc.DefaultWorkspaceID != id {
		t.Fatalf("registry.defaultWorkspaceId=%q，期望 %q", doc.DefaultWorkspaceID, id)
	}
	if doc.Workspaces[id] == nil {
		t.Fatalf("registry 里没有默认工作区记录 %s", id)
	}

	// 第二次走完整 transport 路径（官方 result 只有 {workspace}，没有 created）。
	res := h.ok(t, "workspace.initializeDefault", map[string]any{})
	if got, _ := officialWorkspaceView(t, res)["workspaceId"].(string); got != id {
		t.Fatalf("二次调用换了 id：%q → %q", id, got)
	}
	if _, has := res["created"]; has {
		t.Fatalf("官方 result 不许有 created 字段：%v", res)
	}
	again, created, err := h.host.initializeDefault()
	if err != nil {
		t.Fatalf("二次 initializeDefault: %v", err)
	}
	if created {
		t.Fatal("二次 initializeDefault 必须 created=false")
	}
	if again.Workspace.WorkspaceID != id {
		t.Fatalf("二次 initializeDefault 换了 id：%q → %q", id, again.Workspace.WorkspaceID)
	}

	// 后来又有别的工作区，也不影响"默认工作区身份"的复用。
	other := newOfficialWorkspaceDir(t, t.TempDir(), "another")
	if res := h.ok(t, "workspace.create", map[string]any{"path": other}); res["created"] != true {
		t.Fatalf("前置 create 应 created=true：%v", res)
	}
	afterOther, created, err := h.host.initializeDefault()
	if err != nil {
		t.Fatalf("有其它工作区后 initializeDefault: %v", err)
	}
	if created || afterOther.Workspace.WorkspaceID != id {
		t.Fatalf("有其它工作区后应复用默认 id=%q、created=false；实际 id=%q created=%v",
			id, afterOther.Workspace.WorkspaceID, created)
	}

	// 进程重启：新 host 从同一 registry 读回，同一 id，且**不再解析 Documents**。
	restarted := newOfficialWorkspaceHarnessAt(t, home)
	restarted.host.documentsDir = func() (string, error) {
		t.Fatal("defaultWorkspaceId 已持久化时不该再解析 Documents")
		return "", nil
	}
	res = restarted.ok(t, "workspace.initializeDefault", map[string]any{})
	if got, _ := officialWorkspaceView(t, res)["workspaceId"].(string); got != id {
		t.Fatalf("重启后 initializeDefault 换了 id：%q → %q", id, got)
	}
	if _, created, err = restarted.host.initializeDefault(); err != nil || created {
		t.Fatalf("重启后 initializeDefault 应 created=false 且无错误，实际 created=%v err=%v", created, err)
	}
}

// TestWorkspaceInitializeDefaultIneligibleWithExistingWorkspace registry 非空 →
// 官方 undefined（不创建），且不得落一个空 registry。
func TestWorkspaceInitializeDefaultIneligibleWithExistingWorkspace(t *testing.T) {
	home := t.TempDir()
	documents := t.TempDir()
	h := newOfficialWorkspaceHarnessAt(t, home)
	h.host.documentsDir = func() (string, error) { return documents, nil }

	dir := newOfficialWorkspaceDir(t, t.TempDir(), "existing")
	h.ok(t, "workspace.create", map[string]any{"path": dir})

	value, created, err := h.host.initializeDefault()
	if err != nil {
		t.Fatalf("initializeDefault: %v", err)
	}
	if created || value.Workspace.WorkspaceID != "" {
		t.Fatalf("registry 非空时官方是 undefined，实际 value=%+v created=%v", value, created)
	}
	if got := workspaceInitializeDefaultRegistry(t, home).DefaultWorkspaceID; got != "" {
		t.Fatalf("不适用时不许写 defaultWorkspaceId，实际 %q", got)
	}
	if _, err := os.Stat(filepath.Join(documents, "deepseek-harness", "default-workspace")); !os.IsNotExist(err) {
		t.Fatalf("不适用时不许建默认目录：%v", err)
	}
}

// TestWorkspaceInitializeDefaultIneligibleWithSessionHistory 真实 v4 会话历史非空
// → undefined，且**不产生** registry 文件（withDocumentWriteWhen 的只读语义）。
func TestWorkspaceInitializeDefaultIneligibleWithSessionHistory(t *testing.T) {
	home := t.TempDir()
	documents := t.TempDir()
	h := newOfficialWorkspaceHarnessAt(t, home)
	h.host.documentsDir = func() (string, error) { return documents, nil }

	sessionDir := newOfficialWorkspaceDir(t, t.TempDir(), "sess")
	newRealSession(t, home, sessionDir, "ffffffff-0000-0000-0000-00000000init")

	value, created, err := h.host.initializeDefault()
	if err != nil {
		t.Fatalf("initializeDefault: %v", err)
	}
	if created || value.Workspace.WorkspaceID != "" {
		t.Fatalf("有会话历史时官方是 undefined，实际 value=%+v created=%v", value, created)
	}
	if _, err := os.Stat(filepath.Join(home, "state", "workspaces.json")); !os.IsNotExist(err) {
		t.Fatalf("不适用时不许留下 registry 文件：%v", err)
	}
	if _, err := os.Stat(filepath.Join(documents, "deepseek-harness", "default-workspace")); !os.IsNotExist(err) {
		t.Fatalf("不适用时不许建默认目录：%v", err)
	}
}

// TestWorkspaceInitializeDefaultDeletedRegistrationStaysDisabled 删除默认注册后
// 自动创建永久禁用：指针保留、记录不在 → 继续 undefined，不重建。
func TestWorkspaceInitializeDefaultDeletedRegistrationStaysDisabled(t *testing.T) {
	home := t.TempDir()
	documents := t.TempDir()
	h := newOfficialWorkspaceHarnessAt(t, home)
	h.host.documentsDir = func() (string, error) { return documents, nil }

	first, created, err := h.host.initializeDefault()
	if err != nil || !created {
		t.Fatalf("首次 initializeDefault: created=%v err=%v", created, err)
	}
	id := first.Workspace.WorkspaceID
	h.ok(t, "workspace.delete", map[string]any{"workspaceId": id})

	value, created, err := h.host.initializeDefault()
	if err != nil {
		t.Fatalf("删除后 initializeDefault: %v", err)
	}
	if created || value.Workspace.WorkspaceID != "" {
		t.Fatalf("删除默认注册后必须永久禁用，实际 value=%+v created=%v", value, created)
	}
	doc := workspaceInitializeDefaultRegistry(t, home)
	if doc.DefaultWorkspaceID != id {
		t.Fatalf("删除后默认指针应保留 %q，实际 %q", id, doc.DefaultWorkspaceID)
	}
	if doc.Workspaces[id] != nil {
		t.Fatalf("默认工作区记录应已删除：%v", doc.Workspaces[id])
	}
}

// TestWorkspaceInitializeDefaultDocumentsUnavailableIsInternal Documents 解析失败
// 是宿主内部错误（500 internal），不是静默 undefined，也不是假目录。
func TestWorkspaceInitializeDefaultDocumentsUnavailableIsInternal(t *testing.T) {
	home := t.TempDir()
	h := newOfficialWorkspaceHarnessAt(t, home)
	h.host.documentsDir = func() (string, error) {
		return "", os.ErrNotExist
	}
	msg := h.fail(t, "workspace.initializeDefault", map[string]any{}, http.StatusInternalServerError)
	if len(msg) == 0 {
		t.Fatal("错误信封的 message 为空")
	}
	if _, err := os.Stat(filepath.Join(home, "state", "workspaces.json")); !os.IsNotExist(err) {
		t.Fatalf("Documents 不可用时不许留下 registry 文件：%v", err)
	}
}
