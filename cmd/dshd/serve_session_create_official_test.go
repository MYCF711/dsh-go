package main

// 官方 `session.create` 宿主实现的真会话夹具。
//
// 传输层（internal/transport/session_create_official_test.go）只证明"参数与信封"；
// 这里用**真 v4 会议日志**证明四个官方字段真的落到了宿主：
// workspaceId 决定 cwd、sessionId 复用空白会话、agentPreset 进 header、
// 冲突/归档/未知 preset 都不静默。

import (
	"errors"
	"testing"

	"dshgo/codecs/v4"
	"dshgo/internal/session"
	"dshgo/internal/transport"
)

// officialCreateFixture 是"真 home + 真 registry + 真 v4 存储"的最小装配。
type officialCreateFixture struct {
	home    string
	src     *sessionSource
	ws      *officialWorkspaceHost
	creator *officialSessionCreator
}

func newOfficialCreateFixture(t *testing.T) *officialCreateFixture {
	t.Helper()
	home := t.TempDir()
	src := newSessionSource(home, "")
	ws := newOfficialWorkspaceHost(home, src, nil)
	settings := newServeSettingsStore("", "ask", newModelOverrideStore(home), newPermissionOverrideStore(home))
	return &officialCreateFixture{
		home: home,
		src:  src,
		ws:   ws,
		creator: &officialSessionCreator{
			home:     home,
			src:      src,
			settings: settings,
			workspace: func() *officialWorkspaceHost {
				return ws
			},
		},
	}
}

// ptr 造一个"给了值"的可选参数（官方判据是 !== undefined，不是真值）。
func ptr(value string) *string { return &value }

func (f *officialCreateFixture) createWorkspace(t *testing.T, dir string) string {
	t.Helper()
	created, err := f.ws.createWorkspace(dir)
	if err != nil {
		t.Fatalf("建工作区 %s: %v", dir, err)
	}
	return created.Workspace.WorkspaceID
}

func (f *officialCreateFixture) header(t *testing.T, sessionID string) v4.Header {
	t.Helper()
	path, err := session.FindByID(f.home, sessionID)
	if err != nil {
		t.Fatalf("找不到会话 %s: %v", sessionID, err)
	}
	header, err := v4.ReadHeader(path)
	if err != nil {
		t.Fatalf("读会话 %s header: %v", sessionID, err)
	}
	return header
}

func (f *officialCreateFixture) sessionCount(t *testing.T) int {
	t.Helper()
	list, err := session.ListSessions(f.home, "")
	if err != nil {
		t.Fatalf("列会话: %v", err)
	}
	return len(list)
}

func (f *officialCreateFixture) appendTurnStart(t *testing.T, cwd, sessionID string) {
	t.Helper()
	lg, err := session.OpenSession(f.home, cwd, sessionID)
	if err != nil {
		t.Fatalf("打开会话 %s: %v", sessionID, err)
	}
	if err := lg.Append("turn/start", map[string]any{"turn": 1}); err != nil {
		t.Fatalf("追加 turn/start: %v", err)
	}
	if err := lg.Close(); err != nil {
		t.Fatalf("关闭会话 %s: %v", sessionID, err)
	}
}

func officialFault(t *testing.T, err error) *transport.OfficialWorkspaceFault {
	t.Helper()
	var fault *transport.OfficialWorkspaceFault
	if !errors.As(err, &fault) {
		t.Fatalf("必须是带官方码的 OfficialWorkspaceFault，实际 %T: %v", err, err)
	}
	return fault
}

// TestOfficialSessionCreateReusesBlankWorkspaceSession 是 GUI 账本 #16 要的那条：
// 空工作区里按官方 `{workspaceId, sessionId?}` 建会话，第二次带同一个 sessionId
// 必须**复用它**，不多造一个空会话。
//
// 官方语义：commands.ts:139-145 的 reusable（未归档 + 是该工作区成员）
// 且 agent.ts:369-370 的"没有任何 turn/start"。
func TestOfficialSessionCreateReusesBlankWorkspaceSession(t *testing.T) {
	f := newOfficialCreateFixture(t)
	dir := t.TempDir()
	wsID := f.createWorkspace(t, dir)

	first, err := f.creator.create(transport.OfficialSessionCreateRequest{
		WorkspaceID: ptr(wsID),
		AgentPreset: ptr("read-only"),
	})
	if err != nil {
		t.Fatalf("按 workspaceId 建会话: %v", err)
	}
	if first.SessionID == "" || first.AgentPreset != "read-only" {
		t.Fatalf("SessionCreateValue 不对：%+v", first)
	}
	header := f.header(t, first.SessionID)
	if want := v4.NormalizeCwd(dir); header.Cwd != want {
		t.Fatalf("header.cwd=%q，期望工作区目录 %q", header.Cwd, want)
	}
	if header.AgentPreset != "read-only" {
		t.Fatalf("header.agentPreset=%q，期望请求的 read-only", header.AgentPreset)
	}
	if n := f.sessionCount(t); n != 1 {
		t.Fatalf("建会话后应该有 1 份会话，实际 %d", n)
	}

	second, err := f.creator.create(transport.OfficialSessionCreateRequest{
		WorkspaceID: ptr(wsID),
		SessionID:   ptr(first.SessionID),
	})
	if err != nil {
		t.Fatalf("复用空白会话: %v", err)
	}
	if second.SessionID != first.SessionID {
		t.Fatalf("必须复用同一个空白会话：first=%s second=%s", first.SessionID, second.SessionID)
	}
	// 请求没点名 preset → 如实回会话 header 里的真实 preset（不是空串）。
	if second.AgentPreset != "read-only" {
		t.Fatalf("复用结果的 agentPreset=%q，期望会话真实 preset read-only", second.AgentPreset)
	}
	if n := f.sessionCount(t); n != 1 {
		t.Fatalf("复用不得多造会话，实际 %d 份", n)
	}
	reusable, err := f.ws.sessionReusable(wsID, first.SessionID)
	if err != nil || !reusable {
		t.Fatalf("复用的会话必须仍是该工作区成员：reusable=%v err=%v", reusable, err)
	}
}

// TestOfficialSessionCreateMintsNewIDWhenNamedSessionRanATurn：官方 agent.ts:369-370
// 的空白判据是"没有 turn/start"。已经开过一轮的会话不能被复用，宿主另铸新 id
// （SessionCreateValue 的注释：返回的 id 可能与请求的 sessionId 不同）。
func TestOfficialSessionCreateMintsNewIDWhenNamedSessionRanATurn(t *testing.T) {
	f := newOfficialCreateFixture(t)
	dir := t.TempDir()

	created, err := f.creator.create(transport.OfficialSessionCreateRequest{Cwd: ptr(dir)})
	if err != nil {
		t.Fatalf("建会话: %v", err)
	}
	f.appendTurnStart(t, v4.NormalizeCwd(dir), created.SessionID)

	again, err := f.creator.create(transport.OfficialSessionCreateRequest{
		Cwd:       ptr(dir),
		SessionID: ptr(created.SessionID),
	})
	if err != nil {
		t.Fatalf("对已跑过的会话再 create: %v", err)
	}
	if again.SessionID == created.SessionID {
		t.Fatalf("开过 turn 的会话不得复用：仍然返回 %s", again.SessionID)
	}
	if n := f.sessionCount(t); n != 2 {
		t.Fatalf("应当另建一份新会话，实际 %d 份", n)
	}
}

// TestOfficialSessionCreateUsesNamedIDWhenNothingStored：官方
// "the identity of a new Session when none is stored under it"（types.ts:274）。
func TestOfficialSessionCreateUsesNamedIDWhenNothingStored(t *testing.T) {
	f := newOfficialCreateFixture(t)
	dir := t.TempDir()
	const requested = "11111111-2222-3333-4444-555555555555"

	created, err := f.creator.create(transport.OfficialSessionCreateRequest{
		Cwd:       ptr(dir),
		SessionID: ptr(requested),
	})
	if err != nil {
		t.Fatalf("按预分配 id 建会话: %v", err)
	}
	if created.SessionID != requested {
		t.Fatalf("没存过这个名字时必须用它建会话，实际返回 %q", created.SessionID)
	}
	if header := f.header(t, requested); header.ID != requested {
		t.Fatalf("v4 header.id=%q，期望请求的 id %q", header.ID, requested)
	}
}

// TestOfficialSessionCreateCwdConflictKeepsOfficialShape：官方 agent.ts:363-365 的
// cwd 冲突 = `session/conflict` {sessionId, requestedCwd, existingCwd}
// （commands.ts:556-561 的 rejectCreation 映射）。不得静默建一份新的。
func TestOfficialSessionCreateCwdConflictKeepsOfficialShape(t *testing.T) {
	f := newOfficialCreateFixture(t)
	dirA, dirB := t.TempDir(), t.TempDir()
	created, err := f.creator.create(transport.OfficialSessionCreateRequest{Cwd: ptr(dirA)})
	if err != nil {
		t.Fatalf("建会话: %v", err)
	}

	_, err = f.creator.create(transport.OfficialSessionCreateRequest{
		Cwd:       ptr(dirB),
		SessionID: ptr(created.SessionID),
	})
	if err == nil {
		t.Fatal("cwd 不符必须响亮失败，不能建第二份会话")
	}
	fault := officialFault(t, err)
	if fault.Code != "session/conflict" {
		t.Fatalf("官方码 = %q，期望 session/conflict", fault.Code)
	}
	if fault.Details["sessionId"] != created.SessionID {
		t.Fatalf("details.sessionId = %v", fault.Details["sessionId"])
	}
	if fault.Details["requestedCwd"] != v4.NormalizeCwd(dirB) {
		t.Fatalf("details.requestedCwd = %v", fault.Details["requestedCwd"])
	}
	if fault.Details["existingCwd"] != v4.NormalizeCwd(dirA) {
		t.Fatalf("details.existingCwd = %v", fault.Details["existingCwd"])
	}
	if n := f.sessionCount(t); n != 1 {
		t.Fatalf("冲突时不得新增会话，实际 %d 份", n)
	}
}

// TestOfficialSessionCreateCwdAloneCreatesThere：没有 workspaceId 时 cwd 就是会话目录
// （官方 commands.ts:135 `cwd = workspace?.path ?? request.cwd ?? defaultCwd`）。
// 旧宿主只解 mode + 忽略 cwd，会把会话建到进程 cwd——这条钉住修复。
func TestOfficialSessionCreateCwdAloneCreatesThere(t *testing.T) {
	f := newOfficialCreateFixture(t)
	dir := t.TempDir()

	created, err := f.creator.create(transport.OfficialSessionCreateRequest{Cwd: ptr(dir)})
	if err != nil {
		t.Fatalf("按 cwd 建会话: %v", err)
	}
	if header := f.header(t, created.SessionID); header.Cwd != v4.NormalizeCwd(dir) {
		t.Fatalf("header.cwd=%q，期望请求的 cwd %q（不是进程 cwd）", header.Cwd, v4.NormalizeCwd(dir))
	}
}

// TestOfficialSessionCreateAgentPresetAndLegacyModeReachHeader：官方 agentPreset 与
// 旧 mode（同一个 header 字段）都必须真的落到 v4 header，且结果如实回它。
func TestOfficialSessionCreateAgentPresetAndLegacyModeReachHeader(t *testing.T) {
	f := newOfficialCreateFixture(t)
	dir := t.TempDir()

	official, err := f.creator.create(transport.OfficialSessionCreateRequest{
		Cwd:         ptr(dir),
		AgentPreset: ptr("read-only"),
	})
	if err != nil {
		t.Fatalf("agentPreset 建会话: %v", err)
	}
	if official.AgentPreset != "read-only" {
		t.Fatalf("结果 agentPreset=%q", official.AgentPreset)
	}
	if header := f.header(t, official.SessionID); header.AgentPreset != "read-only" {
		t.Fatalf("header.agentPreset=%q，期望 read-only", header.AgentPreset)
	}

	legacy, err := f.creator.create(transport.OfficialSessionCreateRequest{
		Cwd:  ptr(dir),
		Mode: "workspace-write",
	})
	if err != nil {
		t.Fatalf("旧 mode 建会话: %v", err)
	}
	if header := f.header(t, legacy.SessionID); header.AgentPreset != "workspace-write" {
		t.Fatalf("旧 mode 没有落到 header.agentPreset：%q", header.AgentPreset)
	}
	if legacy.AgentPreset != "workspace-write" {
		t.Fatalf("旧 mode 的结果 agentPreset=%q", legacy.AgentPreset)
	}
}

// TestOfficialSessionCreateUnknownAgentPresetIsLoud：官方 preset registry
// src/index.ts:222-226 → `agent-preset/not-found` {agentPreset, available}。
// 不许把不存在的 preset 写进 header（界面会显示一个点不出来的名字）。
func TestOfficialSessionCreateUnknownAgentPresetIsLoud(t *testing.T) {
	f := newOfficialCreateFixture(t)
	dir := t.TempDir()

	_, err := f.creator.create(transport.OfficialSessionCreateRequest{
		Cwd:         ptr(dir),
		AgentPreset: ptr("no-such-preset"),
	})
	if err == nil {
		t.Fatal("未知 preset 必须响亮失败")
	}
	fault := officialFault(t, err)
	if fault.Code != "agent-preset/not-found" {
		t.Fatalf("官方码 = %q", fault.Code)
	}
	if fault.Details["agentPreset"] != "no-such-preset" {
		t.Fatalf("details.agentPreset = %v", fault.Details["agentPreset"])
	}
	available, ok := fault.Details["available"].([]string)
	if !ok || len(available) == 0 {
		t.Fatalf("details.available 必须是真实 preset id 列表：%v", fault.Details["available"])
	}
	if n := f.sessionCount(t); n != 0 {
		t.Fatalf("preset 校验失败时不得建会话，实际 %d 份", n)
	}
}

// TestOfficialSessionCreateReusePresetChangeIsLoud 钉本仓**有意**的差异：
// 官方复用时会改写会话 preset（agent.ts:400-410 追加 agent-preset/selected），
// 本仓 preset 在 append-only 的 v4 header 里、runner 也读 header（不信投影），
// 所以"显式点名了不同 preset"只能响亮拒绝（commands.ts:550-555 的官方码），
// 绝不能回成功然后按旧 preset 跑。
func TestOfficialSessionCreateReusePresetChangeIsLoud(t *testing.T) {
	f := newOfficialCreateFixture(t)
	dir := t.TempDir()

	created, err := f.creator.create(transport.OfficialSessionCreateRequest{
		Cwd:         ptr(dir),
		AgentPreset: ptr("read-only"),
	})
	if err != nil {
		t.Fatalf("建空白会话: %v", err)
	}
	_, err = f.creator.create(transport.OfficialSessionCreateRequest{
		Cwd:         ptr(dir),
		SessionID:   ptr(created.SessionID),
		AgentPreset: ptr("default"),
	})
	if err == nil {
		t.Fatal("复用并改名 preset 必须响亮失败（header 是 append-only）")
	}
	fault := officialFault(t, err)
	if fault.Code != "agent-preset/conflict" {
		t.Fatalf("官方码 = %q，期望 agent-preset/conflict", fault.Code)
	}
	if fault.Details["sessionId"] != created.SessionID || fault.Details["requestedPreset"] != "default" {
		t.Fatalf("details 不对：%v", fault.Details)
	}
	if n := f.sessionCount(t); n != 1 {
		t.Fatalf("失败时不得多造会话，实际 %d 份", n)
	}
}

// TestOfficialSessionCreateArchivedSessionIsNotReused：reusable 谓词的第一条
// （commands.ts:142 `!archivedSessionIds.includes(sessionId)`）。归档会话不得复用，
// 但也不能报错——官方会另铸一个新会话。
func TestOfficialSessionCreateArchivedSessionIsNotReused(t *testing.T) {
	f := newOfficialCreateFixture(t)
	dir := t.TempDir()

	created, err := f.creator.create(transport.OfficialSessionCreateRequest{Cwd: ptr(dir)})
	if err != nil {
		t.Fatalf("建会话: %v", err)
	}
	if _, err := f.ws.archiveSession(created.SessionID, false); err != nil {
		t.Fatalf("归档会话: %v", err)
	}
	again, err := f.creator.create(transport.OfficialSessionCreateRequest{
		Cwd:       ptr(dir),
		SessionID: ptr(created.SessionID),
	})
	if err != nil {
		t.Fatalf("对已归档会话再 create 不该报错（官方另铸新 id）: %v", err)
	}
	if again.SessionID == created.SessionID {
		t.Fatalf("已归档会话不得被复用：仍然返回 %s", again.SessionID)
	}
	if n := f.sessionCount(t); n != 2 {
		t.Fatalf("应当另外建一份，实际 %d 份", n)
	}
}

// TestOfficialSessionCreateUnknownWorkspaceDoesNotCreate：官方 commands.ts:126-132
// → `workspace/not-found` {workspaceId}，且不得退回进程 cwd 假建。
func TestOfficialSessionCreateUnknownWorkspaceDoesNotCreate(t *testing.T) {
	f := newOfficialCreateFixture(t)

	_, err := f.creator.create(transport.OfficialSessionCreateRequest{WorkspaceID: ptr("ws-missing")})
	if err == nil {
		t.Fatal("未知 workspaceId 必须失败")
	}
	fault := officialFault(t, err)
	if fault.Code != "workspace/not-found" || fault.Details["workspaceId"] != "ws-missing" {
		t.Fatalf("官方错误形状不对：%v %v", fault.Code, fault.Details)
	}
	if n := f.sessionCount(t); n != 0 {
		t.Fatalf("失败时不得建会话，实际 %d 份", n)
	}
}

// TestOfficialSessionCreateEmptyWorkspaceIDIsNotFound：给了空串 workspaceId 也必须
// 走 registry 查无此物（官方判据是 `!== undefined`，commands.ts:126-132），
// 不能被当成省略后在进程 cwd 建一个会话——旧 transport 注释专门记过这条。
func TestOfficialSessionCreateEmptyWorkspaceIDIsNotFound(t *testing.T) {
	f := newOfficialCreateFixture(t)

	_, err := f.creator.create(transport.OfficialSessionCreateRequest{WorkspaceID: ptr("")})
	if err == nil {
		t.Fatal("空 workspaceId 必须走 registry 并失败")
	}
	fault := officialFault(t, err)
	if fault.Code != "workspace/not-found" {
		t.Fatalf("官方码 = %q，期望 workspace/not-found", fault.Code)
	}
	if value, ok := fault.Details["workspaceId"]; !ok || value != "" {
		t.Fatalf("details.workspaceId 必须是那个空串（不是缺键）：%v", fault.Details)
	}
	if n := f.sessionCount(t); n != 0 {
		t.Fatalf("失败时不得建会话，实际 %d 份", n)
	}
}

// TestOfficialSessionCreateEmptySessionIDIsRejected：官方判据是 `!== undefined`
// （commands.ts:141-145），空串不是合法 SessionId；当"没给"处理会静默建新会话，
// 客户端却以为它复用了 ""。
func TestOfficialSessionCreateEmptySessionIDIsRejected(t *testing.T) {
	f := newOfficialCreateFixture(t)
	dir := t.TempDir()

	_, err := f.creator.create(transport.OfficialSessionCreateRequest{
		Cwd:       ptr(dir),
		SessionID: ptr(""),
	})
	if err == nil {
		t.Fatal("空 sessionId 必须响亮失败")
	}
	if fault := officialFault(t, err); fault.Code != "gateway/bad-request" {
		t.Fatalf("官方码 = %q，期望 gateway/bad-request", fault.Code)
	}
	if n := f.sessionCount(t); n != 0 {
		t.Fatalf("失败时不得建会话，实际 %d 份", n)
	}
}

// TestOfficialAttachSessionFailureCarriesPublishedSessionID 钉"已发布未挂上"的
// 宿主侧形状（commands.ts:146-156）：details 里必须有那份真实存在的会话 id 与
// 工作区 id —— 客户端靠它把会话当 Ungrouped 暴露（manager.ts:855-857）。
func TestOfficialAttachSessionFailureCarriesPublishedSessionID(t *testing.T) {
	f := newOfficialCreateFixture(t)
	wsDir, otherDir := t.TempDir(), t.TempDir()
	wsID := f.createWorkspace(t, wsDir)

	// 会话真实建在别的目录：它的 header.cwd 不属于这个工作区。
	created, err := f.creator.create(transport.OfficialSessionCreateRequest{Cwd: ptr(otherDir)})
	if err != nil {
		t.Fatalf("建会话: %v", err)
	}
	err = f.ws.attachSession(wsID, created.SessionID)
	if err == nil {
		t.Fatal("不属于该工作区的会话不得挂进去")
	}
	fault := officialFault(t, err)
	if fault.Code != "session/workspace-attach-failed" {
		t.Fatalf("官方码 = %q", fault.Code)
	}
	if fault.Details["sessionId"] != created.SessionID || fault.Details["workspaceId"] != wsID {
		t.Fatalf("details 必须带 {sessionId, workspaceId}：%v", fault.Details)
	}
	reusable, err := f.ws.sessionReusable(wsID, created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if reusable {
		t.Fatal("挂失败的会话不得出现在工作区成员里")
	}
}
