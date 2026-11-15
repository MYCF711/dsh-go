package creation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"dshgo/codecs/v4"
	"dshgo/internal/actioncards"
	"dshgo/internal/pluginmgr"
	"dshgo/internal/session"
	"dshgo/internal/spi"
)

// ---------------------------------------------------------------------------
// 夹具：真实 home / 真实 pluginManager / 真实 v4 会话
// ---------------------------------------------------------------------------

func openManager(t *testing.T, home string) *pluginmgr.Manager {
	t.Helper()
	manager, err := pluginmgr.Open(filepath.Join(home, "plugins"))
	if err != nil {
		t.Fatalf("pluginmgr.Open: %v", err)
	}
	return manager
}

// writeInstallablePackage 写一份本仓**真能装**的包（dsh.local-plugin@1 + wasm 入口
// + SHA-256）。用它证明 undo/restore 动的是真实 pluginManager 状态。
func writeInstallablePackage(t *testing.T, dir, id, version string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	payload := []byte("creation test wasm payload")
	sum := sha256.Sum256(payload)
	manifest := map[string]any{
		"schema": "dsh.local-plugin@1", "id": id, "version": version,
		"kind": "wasm", "entry": "plugin.wasm",
		"files": map[string]string{"plugin.wasm": hex.EncodeToString(sum[:])},
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), raw, 0o644); err != nil {
		t.Fatalf("写 plugin.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.wasm"), payload, 0o644); err != nil {
		t.Fatalf("写 plugin.wasm: %v", err)
	}
	return dir
}

// newTestService 造一个服务（cards 用独立实例，避免跨测试共享 actioncards.Open 的进程级表）。
func newTestService(t *testing.T, home string, manager *pluginmgr.Manager, opts ...Option) (*Service, *actioncards.Service) {
	t.Helper()
	cards := actioncards.New(home)
	service, err := New(home, manager, cards, opts...)
	if err != nil {
		t.Fatalf("creation.New: %v", err)
	}
	t.Cleanup(service.Close)
	return service, cards
}

type seededCard struct {
	sessionID string
	callID    string
}

// seedProposal 在真实 v4 会话里写一条 propose_action 的 call+成功 result，
// 让 actioncards 投影把它认成一张卡。decision 为 nil 时不预置决定。
func seedProposal(t *testing.T, home string, card seededCard, details map[string]any, decision map[string]any) {
	t.Helper()
	lg, err := session.CreateSession(home, filepath.Join(home, "cwd"), card.sessionID, "default")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := lg.Append("turn/start", session.TurnStartData(1)); err != nil {
		t.Fatalf("turn/start: %v", err)
	}
	args, _ := json.Marshal(map[string]any{
		"action": Action, "title": "本次验收清单", "brief": "在当前会话右侧展示验收清单", "details": details,
	})
	if err := lg.Append("tool/call", session.ToolCallEvent(1, 1, card.callID, "propose_action", string(args))); err != nil {
		t.Fatalf("tool/call: %v", err)
	}
	if err := lg.Append("tool/result", session.ToolResultEvent(1, 1, card.callID,
		[]spi.Block{{Type: "text", Text: "Card shown"}}, false, "", nil)); err != nil {
		t.Fatalf("tool/result: %v", err)
	}
	if decision != nil {
		if err := lg.Append("plugin:action-cards/decision", decision); err != nil {
			t.Fatalf("decision: %v", err)
		}
	}
	if err := lg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// decisionsOf 读真实 v4 里这张卡写过的全部 decision phase（按写入顺序）。
func decisionsOf(t *testing.T, home, sessionID, callID string) []string {
	t.Helper()
	path, err := session.FindByID(home, sessionID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	log, err := v4.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var phases []string
	for _, event := range log.Events {
		if event.Type != "plugin:action-cards/decision" {
			continue
		}
		var decision actioncards.Decision
		if json.Unmarshal(event.Data, &decision) != nil || decision.CallID != callID {
			continue
		}
		phases = append(phases, decision.Phase)
	}
	return phases
}

func payloadOf(t *testing.T, raw string) map[string]json.RawMessage {
	t.Helper()
	var out map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("payload 不是对象: %v", err)
	}
	return out
}

// memoryLog 收集真实失败原因，好让测试断言"拒绝的理由是真的"。
type memoryLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *memoryLog) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *memoryLog) joined() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// ---------------------------------------------------------------------------
// kind 注册形状：propose_action 认它，details 判据照官方
// ---------------------------------------------------------------------------

func TestProposeActionAcceptsKindAndRejectsBadDetails(t *testing.T) {
	home := t.TempDir()
	_, cards := newTestService(t, home, openManager(t, home))
	seedSkillLoadedSession(t, home, "s1")

	tool := cards.Tool()
	ctx := spi.WithSession(context.Background(), "s1")
	result, err := tool.Exec(ctx, json.RawMessage(`{"action":"create_personalized_page","title":"本次验收清单",`+
		`"brief":"在当前会话右侧展示验收清单","details":{"slot":"sidebar-right","initialContent":"1. 导出成功"}}`))
	if err != nil {
		t.Fatalf("propose_action 应接受已注册的 kind: %v", err)
	}
	data, _ := result.Data.(map[string]any)
	if data["action"] != Action {
		t.Fatalf("工具返回的 action = %+v", data)
	}
	if data["invitation"] != Invitation {
		t.Fatalf("invitation 必须逐字等于官方文案，实际 %q", data["invitation"])
	}
	if !strings.Contains(result.Text, Invitation) {
		t.Fatalf("工具 Text 应含邀请句，实际 %q", result.Text)
	}

	// details 妍据：slot 必填、必须是官方三个枚举之一、initialContent 只能是字符串。
	for _, broken := range []string{
		`{"action":"create_personalized_page","title":"t","brief":"b","details":{}}`,
		`{"action":"create_personalized_page","title":"t","brief":"b","details":{"slot":"middle"}}`,
		`{"action":"create_personalized_page","title":"t","brief":"b","details":{"slot":"page","initialContent":42}}`,
	} {
		if _, err := tool.Exec(ctx, json.RawMessage(broken)); err == nil {
			t.Fatalf("坏 details 必须被拒绝: %s", broken)
		}
	}
}

// seedSkillLoadedSession 写一条"本轮加载过 personalized-pages skill"的真实 v4 记录，
// 让 propose_action 的 skill 校验通过。
func seedSkillLoadedSession(t *testing.T, home, sessionID string) {
	t.Helper()
	lg, err := session.CreateSession(home, filepath.Join(home, "cwd"), sessionID, "default")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := lg.Append("turn/start", session.TurnStartData(1)); err != nil {
		t.Fatalf("turn/start: %v", err)
	}
	args, _ := json.Marshal(map[string]any{"name": Skill})
	if err := lg.Append("tool/call", session.ToolCallEvent(1, 1, "skill-call", "skill", string(args))); err != nil {
		t.Fatalf("skill tool/call: %v", err)
	}
	if err := lg.Append("tool/result", session.ToolResultEvent(1, 1, "skill-call",
		[]spi.Block{{Type: "text", Text: "skill loaded"}}, false, "", nil)); err != nil {
		t.Fatalf("skill tool/result: %v", err)
	}
	if err := lg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// create：生产路径（没有 Creator）在写盘前如实拒绝
// ---------------------------------------------------------------------------

func TestCreateRefusedWithoutCreatorWritesNothing(t *testing.T) {
	home := t.TempDir()
	manager := openManager(t, home)
	log := &memoryLog{}
	_, cards := newTestService(t, home, manager, WithLogf(log.logf))
	seedProposal(t, home, seededCard{sessionID: "s1", callID: "call-1"},
		map[string]any{"slot": "sidebar-right", "initialContent": "1. 导出成功"}, nil)

	result, err := actOnCard(t, cards, "s1", "call-1", "create", `{"slot":"sidebar-right"}`)
	if err != nil {
		t.Fatalf("Act create: %v", err)
	}
	if result.Status != actioncards.StatusRefused || result.Reason != ReasonCreatorUnavailable {
		t.Fatalf("没有 Creator 时必须 refused{%s}，实际 %+v", ReasonCreatorUnavailable, result)
	}
	if phases := decisionsOf(t, home, "s1", "call-1"); len(phases) != 0 {
		t.Fatalf("拒绝不该写任何 decision，实际 %v", phases)
	}
	// 真正的"没写盘"：两个保留目录都不该出现。
	if _, err := os.Stat(filepath.Join(home, CreatedPluginsDir)); !os.IsNotExist(err) {
		t.Fatalf("拒绝时不该创建 profile 的 %s 目录: %v", CreatedPluginsDir, err)
	}
	if _, err := os.Stat(filepath.Join(home, "cwd", StagingDirName)); !os.IsNotExist(err) {
		t.Fatalf("拒绝时不该创建暂存目录: %v", err)
	}
	if !strings.Contains(log.joined(), ReasonCreatorUnavailable) {
		t.Fatalf("拒绝原因必须进日志（可诊断），实际 %q", log.joined())
	}
}

func TestCreateRefusesInvalidLocation(t *testing.T) {
	home := t.TempDir()
	manager := openManager(t, home)
	_, cards := newTestService(t, home, manager)
	seedProposal(t, home, seededCard{sessionID: "s1", callID: "call-1"},
		map[string]any{"slot": "page"}, nil)

	for _, payload := range []string{
		`{}`,
		`{"slot":"middle"}`,
		`{"slot":"page","customLocation":"   "}`,
		`{"slot":"page","customLocation":"` + strings.Repeat("长", 1001) + `"}`,
	} {
		result, err := actOnCard(t, cards, "s1", "call-1", "create", payload)
		if err != nil {
			t.Fatalf("Act create(%s): %v", payload, err)
		}
		if result.Status != actioncards.StatusRefused || result.Reason != ReasonInvalidLocation {
			t.Fatalf("非法 location 必须 refused{%s}，payload=%s 实际 %+v", ReasonInvalidLocation, payload, result)
		}
	}
	if phases := decisionsOf(t, home, "s1", "call-1"); len(phases) != 0 {
		t.Fatalf("非法 location 不该写 decision，实际 %v", phases)
	}
}

// ---------------------------------------------------------------------------
// create：注入 Creator 后 ⑤⑥⑦（真校验、真复制、真安装）全部真实执行
// ---------------------------------------------------------------------------

type fixtureCreator struct {
	mu       sync.Mutex
	requests []CreateRequest
	write    func(request CreateRequest)
}

func (f *fixtureCreator) Create(_ context.Context, request CreateRequest) error {
	f.mu.Lock()
	f.requests = append(f.requests, request)
	f.mu.Unlock()
	f.write(request)
	return nil
}

func TestCreateWithCreatorReallyWritesValidatesAndFailsInstall(t *testing.T) {
	home := t.TempDir()
	manager := openManager(t, home)
	log := &memoryLog{}
	creator := &fixtureCreator{write: func(request CreateRequest) {
		pkg := defaultGeneratedPackage(request.BundleName)
		pkg.extra = map[string]string{"lib/panel.js": "export const panel = 1"}
		writeGeneratedPackageAt(t, request.SourceDirectory, pkg)
	}}
	_, cards := newTestService(t, home, manager, WithCreator(creator), WithLogf(log.logf))
	seedProposal(t, home, seededCard{sessionID: "s1", callID: "call-1"},
		map[string]any{"slot": "sidebar-left", "initialContent": "第 1 周 12，第 2 周 9"}, nil)

	result, err := actOnCard(t, cards, "s1", "call-1", "create",
		`{"slot":"sidebar-left","customLocation":"左边第二个入口"}`)
	if err != nil {
		t.Fatalf("Act create: %v", err)
	}
	if result.Status != actioncards.StatusSettled || result.Decision == nil {
		t.Fatalf("create 应同步给最终 settled，实际 %+v", result)
	}
	if result.Decision.Phase != PhaseFailed {
		t.Fatalf("本仓安装器不接受 Cordis/npm 包，phase 必须是 %s（绝不能是 installed），实际 %s",
			PhaseFailed, result.Decision.Phase)
	}
	if _, ok := result.Decision.Data["bundleName"]; !ok {
		t.Fatalf("decision 必须记 bundleName（官方 PageData），实际 %+v", result.Decision.Data)
	}
	if result.Decision.Data["slot"] != SlotSidebarLeft {
		t.Fatalf("decision.slot = %v", result.Decision.Data["slot"])
	}
	if result.Decision.Data["customLocation"] != "左边第二个入口" {
		t.Fatalf("decision.customLocation = %v", result.Decision.Data["customLocation"])
	}

	// Creator 真的拿到了官方的计划字段。
	if len(creator.requests) != 1 {
		t.Fatalf("Creator 应被调用一次，实际 %d", len(creator.requests))
	}
	request := creator.requests[0]
	if request.Title != "本次验收清单" || !strings.Contains(request.Brief, "验收清单") {
		t.Fatalf("Creator 请求缺 title/brief: %+v", request)
	}
	if !request.HasInitialContent || !strings.Contains(request.InitialContent, "第 1 周 12") {
		t.Fatalf("Creator 请求缺 initialContent: %+v", request)
	}
	if request.Slot != SlotSidebarLeft || request.CustomLocation != "左边第二个入口" {
		t.Fatalf("Creator 请求的 location 必须是用户在卡上选的: %+v", request)
	}

	// 真落盘：暂存目录 + profile 保留副本（含真实文件）。
	staging := request.SourceDirectory
	if _, err := os.Stat(filepath.Join(staging, "package.json")); err != nil {
		t.Fatalf("Creator 没在暂存目录写出包: %v", err)
	}
	id, err := IDOfBundleName(request.BundleName)
	if err != nil {
		t.Fatalf("IDOfBundleName: %v", err)
	}
	copyDir := ProfileCopyDir(home, id)
	raw, err := os.ReadFile(filepath.Join(copyDir, "package.json"))
	if err != nil {
		t.Fatalf("profile 保留副本没落盘: %v", err)
	}
	if !strings.Contains(string(raw), request.BundleName) {
		t.Fatalf("保留副本的 package.json 名字不对: %s", raw)
	}
	if _, err := os.Stat(filepath.Join(copyDir, "lib", "panel.js")); err != nil {
		t.Fatalf("保留副本缺子目录文件: %v", err)
	}
	// 保留副本仍然是"可安装候选"：官方 restore 就靠它。
	if err := ValidateBundle(copyDir, request.BundleName); err != nil {
		t.Fatalf("保留副本应通过同一套校验: %v", err)
	}

	// 真安装器的真实拒绝：pluginManager 里没有这个包（不是假 installed）。
	if _, installed, err := manager.Bundle(request.BundleName); err != nil || installed {
		t.Fatalf("本仓安装器不该装进 Cordis/npm 包: installed=%v err=%v", installed, err)
	}
	// 日志里必须是**真实**安装器原因（Cordis/JS 运行时），不是我们编的字符串。
	if !strings.Contains(log.joined(), "pluginManager 安装") {
		t.Fatalf("必须记录安装阶段失败: %q", log.joined())
	}
	if !strings.Contains(log.joined(), "Cordis") && !strings.Contains(log.joined(), "JS") {
		t.Fatalf("失败原因必须来自真实安装器（Cordis/JS 运行时），实际 %q", log.joined())
	}

	// 中间 phase 也真实落盘：creating（起 Creator 之前）→ installing（真装之前）→ failed。
	phases := decisionsOf(t, home, "s1", "call-1")
	want := []string{PhaseCreating, PhaseInstalling, PhaseFailed}
	if strings.Join(phases, ",") != strings.Join(want, ",") {
		t.Fatalf("decision 序列应为 %v，实际 %v", want, phases)
	}
	view, err := cards.View("s1")
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if view.Decisions["call-1"].Phase != PhaseFailed {
		t.Fatalf("投影应是 failed，实际 %+v", view.Decisions["call-1"])
	}
}

// ---------------------------------------------------------------------------
// undo：真的卸载
// ---------------------------------------------------------------------------

func TestUndoReallyUninstalls(t *testing.T) {
	home := t.TempDir()
	manager := openManager(t, home)
	_, cards := newTestService(t, home, manager)
	id := "11111111-2222-4333-8444-555555555555"
	bundleName := BundleName(id)
	pkg := writeInstallablePackage(t, filepath.Join(home, "pkg"), bundleName, "1.0.0")
	if _, err := manager.Install(pkg); err != nil {
		t.Fatalf("Install: %v", err)
	}
	seedProposal(t, home, seededCard{sessionID: "s1", callID: "call-1"},
		map[string]any{"slot": "page"},
		map[string]any{"callId": "call-1", "action": Action, "phase": PhaseInstalled,
			"data": map[string]any{"slot": "page", "bundleName": bundleName}})

	result, err := actOnCard(t, cards, "s1", "call-1", "undo", `{}`)
	if err != nil {
		t.Fatalf("Act undo: %v", err)
	}
	if result.Status != actioncards.StatusSettled || result.Decision == nil || result.Decision.Phase != PhaseRemoved {
		t.Fatalf("undo 应记 removed，实际 %+v", result)
	}
	if result.Decision.Data["application"] != ApplicationApplied {
		t.Fatalf("undo 后 application 应为 applied，实际 %+v", result.Decision.Data)
	}
	// 真实副作用：pluginManager 里真的没有了。
	if _, ok, err := manager.Bundle(bundleName); err != nil || ok {
		t.Fatalf("undo 必须真的卸载: ok=%v err=%v", ok, err)
	}
}

func TestUndoWithoutInstalledBundleRecordsRemoved(t *testing.T) {
	home := t.TempDir()
	_, cards := newTestService(t, home, openManager(t, home))
	bundleName := BundleName("11111111-2222-4333-8444-555555555555")
	seedProposal(t, home, seededCard{sessionID: "s1", callID: "call-1"},
		map[string]any{"slot": "page"},
		map[string]any{"callId": "call-1", "action": Action, "phase": PhaseInstalled,
			"data": map[string]any{"slot": "page", "bundleName": bundleName}})

	result, err := actOnCard(t, cards, "s1", "call-1", "undo", `{}`)
	if err != nil {
		t.Fatalf("Act undo: %v", err)
	}
	if result.Status != actioncards.StatusSettled || result.Decision.Phase != PhaseRemoved {
		t.Fatalf("没有已安装包时按官方记 removed，实际 %+v", result)
	}
}

// ---------------------------------------------------------------------------
// restore：先校验保留副本，再真装（真装会失败，因为本仓没有 JS 运行时）
// ---------------------------------------------------------------------------

func TestRestoreRefusesWhenCopyMissing(t *testing.T) {
	home := t.TempDir()
	_, cards := newTestService(t, home, openManager(t, home))
	bundleName := BundleName("11111111-2222-4333-8444-555555555555")
	seedProposal(t, home, seededCard{sessionID: "s1", callID: "call-1"},
		map[string]any{"slot": "page"},
		map[string]any{"callId": "call-1", "action": Action, "phase": PhaseRemoved,
			"data": map[string]any{"slot": "page", "bundleName": bundleName, "application": ApplicationApplied}})

	result, err := actOnCard(t, cards, "s1", "call-1", "restore", `{}`)
	if err != nil {
		t.Fatalf("Act restore: %v", err)
	}
	if result.Status != actioncards.StatusRefused || result.Reason != ReasonSourceMissing {
		t.Fatalf("保留副本不存在必须 refused{%s}，实际 %+v", ReasonSourceMissing, result)
	}
}

func TestRestoreReallyValidatesCopyAndRetriesInstall(t *testing.T) {
	home := t.TempDir()
	manager := openManager(t, home)
	log := &memoryLog{}
	_, cards := newTestService(t, home, manager, WithLogf(log.logf))
	id := "11111111-2222-4333-8444-555555555555"
	bundleName := BundleName(id)
	// 真实保留副本：一张"曾经生成过"的页面包。
	writeGeneratedPackageAt(t, ProfileCopyDir(home, id), defaultGeneratedPackage(bundleName))
	seedProposal(t, home, seededCard{sessionID: "s1", callID: "call-1"},
		map[string]any{"slot": "page"},
		map[string]any{"callId": "call-1", "action": Action, "phase": PhaseRemoved,
			"data": map[string]any{"slot": "page", "bundleName": bundleName, "application": ApplicationApplied}})

	result, err := actOnCard(t, cards, "s1", "call-1", "restore", `{}`)
	if err != nil {
		t.Fatalf("Act restore: %v", err)
	}
	if result.Status != actioncards.StatusSettled || result.Decision == nil || result.Decision.Phase != PhaseFailed {
		t.Fatalf("restore 真装被本仓安装器拒绝后应记 failed，实际 %+v", result)
	}
	// 官方会把上一次的 application 从收据里去掉（它描述的是移除）。
	if _, present := result.Decision.Data["application"]; present {
		t.Fatalf("restore 的收据不该保留上一次的 application，实际 %+v", result.Decision.Data)
	}
	if _, installed, err := manager.Bundle(bundleName); err != nil || installed {
		t.Fatalf("不该假装装上: installed=%v err=%v", installed, err)
	}
	if !strings.Contains(log.joined(), "恢复") {
		t.Fatalf("restore 的真实失败原因必须进日志，实际 %q", log.joined())
	}
	phases := decisionsOf(t, home, "s1", "call-1")
	// 序列包含夹具预置的 removed：真实重试会把 installing 先落盘，再落最终结果。
	if strings.Join(phases, ",") != strings.Join([]string{PhaseRemoved, PhaseInstalling, PhaseFailed}, ",") {
		t.Fatalf("restore 的 decision 序列 = %v", phases)
	}
}

// ---------------------------------------------------------------------------
// reconcile / cancel / dismiss
// ---------------------------------------------------------------------------

func TestReconcileUsesRealInstalledState(t *testing.T) {
	home := t.TempDir()
	manager := openManager(t, home)
	_, cards := newTestService(t, home, manager)
	id := "22222222-3333-4444-8555-666666666666"
	bundleName := BundleName(id)
	pkg := writeInstallablePackage(t, filepath.Join(home, "pkg"), bundleName, "1.0.0")
	if _, err := manager.Install(pkg); err != nil {
		t.Fatalf("Install: %v", err)
	}
	seedProposal(t, home, seededCard{sessionID: "s1", callID: "call-1"},
		map[string]any{"slot": "page"},
		map[string]any{"callId": "call-1", "action": Action, "phase": PhaseUnconfirmed,
			"data": map[string]any{"slot": "page", "bundleName": bundleName}})

	result, err := actOnCard(t, cards, "s1", "call-1", "cancel", `{}`)
	if err != nil {
		t.Fatalf("Act cancel: %v", err)
	}
	if result.Status != actioncards.StatusSettled || result.Decision == nil || result.Decision.Phase != PhaseInstalled {
		t.Fatalf("复核到真实已安装时应记 installed，实际 %+v", result)
	}
	if result.Decision.Data["bundleName"] != bundleName {
		t.Fatalf("复核结果必须带真实 bundleName，实际 %+v", result.Decision.Data)
	}
	// 装上了但没启用 → 官方记 application:failed（recover() 的判据）。
	if result.Decision.Data["application"] != ApplicationFailed {
		t.Fatalf("未启用的已安装包应记 application:failed，实际 %+v", result.Decision.Data)
	}
}

func TestReconcileWithoutInstalledBundleRecordsInterrupted(t *testing.T) {
	home := t.TempDir()
	_, cards := newTestService(t, home, openManager(t, home))
	bundleName := BundleName("33333333-4444-4555-8666-777777777777")
	seedProposal(t, home, seededCard{sessionID: "s1", callID: "call-1"},
		map[string]any{"slot": "page"},
		map[string]any{"callId": "call-1", "action": Action, "phase": PhaseUnconfirmed,
			"data": map[string]any{"slot": "page", "bundleName": bundleName}})

	result, err := actOnCard(t, cards, "s1", "call-1", "cancel", `{}`)
	if err != nil {
		t.Fatalf("Act cancel: %v", err)
	}
	if result.Status != actioncards.StatusSettled || result.Decision.Phase != PhaseInterrupted {
		t.Fatalf("没装上时应记 interrupted（可重试），实际 %+v", result)
	}
}

func TestCancelWithoutDecisionIsRefused(t *testing.T) {
	home := t.TempDir()
	_, cards := newTestService(t, home, openManager(t, home))
	seedProposal(t, home, seededCard{sessionID: "s1", callID: "call-1"},
		map[string]any{"slot": "page"}, nil)

	result, err := actOnCard(t, cards, "s1", "call-1", "cancel", `{}`)
	if err != nil {
		t.Fatalf("Act cancel: %v", err)
	}
	if result.Status != actioncards.StatusRefused || result.Reason != ReasonUnavailable {
		t.Fatalf("没有 decision 的 cancel 应 refused{unavailable}，实际 %+v", result)
	}
}

func TestDismissRecordsChosenLocation(t *testing.T) {
	home := t.TempDir()
	_, cards := newTestService(t, home, openManager(t, home))
	seedProposal(t, home, seededCard{sessionID: "s1", callID: "call-1"},
		map[string]any{"slot": "page"}, nil)

	result, err := actOnCard(t, cards, "s1", "call-1", "dismiss",
		`{"slot":"sidebar-left","customLocation":"  左边第二个入口  "}`)
	if err != nil {
		t.Fatalf("Act dismiss: %v", err)
	}
	if result.Status != actioncards.StatusSettled || result.Decision == nil || result.Decision.Phase != "dismissed" {
		t.Fatalf("dismiss 应 settled{dismissed}，实际 %+v", result)
	}
	if result.Decision.Data["slot"] != SlotSidebarLeft {
		t.Fatalf("Not now 也要记下用户选的位置，实际 %+v", result.Decision.Data)
	}
	if result.Decision.Data["customLocation"] != "左边第二个入口" {
		t.Fatalf("customLocation 应 trim 后记录，实际 %+v", result.Decision.Data)
	}

	// 没有合法 payload 时退回 proposal.details.slot（官方判据）。
	seedProposal(t, home, seededCard{sessionID: "s2", callID: "call-2"},
		map[string]any{"slot": "sidebar-right"}, nil)
	result, err = actOnCard(t, cards, "s2", "call-2", "dismiss", `{"customLocation":"   "}`)
	if err != nil {
		t.Fatalf("Act dismiss: %v", err)
	}
	if result.Decision.Data["slot"] != SlotSidebarRight {
		t.Fatalf("应从 proposal.details 兜底 slot，实际 %+v", result.Decision.Data)
	}
	if _, present := result.Decision.Data["customLocation"]; present {
		t.Fatalf("非法 customLocation 不该记录，实际 %+v", result.Decision.Data)
	}
}

// ---------------------------------------------------------------------------
// skill
// ---------------------------------------------------------------------------

func TestEnsureSkillWritesHonestSkillFile(t *testing.T) {
	home := t.TempDir()
	manager := openManager(t, home)
	pkg := writeInstallablePackage(t, filepath.Join(home, "pkg"), "acme.audit", "1.0.0")
	if _, err := manager.Install(pkg); err != nil {
		t.Fatalf("Install: %v", err)
	}
	service, _ := newTestService(t, home, manager)
	if err := service.EnsureSkill(context.Background()); err != nil {
		t.Fatalf("EnsureSkill: %v", err)
	}
	raw, err := os.ReadFile(SkillFile(home))
	if err != nil {
		t.Fatalf("skill 文件没写出来: %v", err)
	}
	body := string(raw)
	for _, want := range []string{Action, "sidebar-right", "initialContent", ReasonCreatorUnavailable, "acme.audit"} {
		if !strings.Contains(body, want) {
			t.Fatalf("skill 文件缺 %q: %s", want, body)
		}
	}
	// 真实目录：已安装包出现在能力目录里。
	if !strings.Contains(body, "| acme.audit |") {
		t.Fatalf("能力目录应来自真实 pluginManager 状态: %s", body)
	}
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

// actOnCard 走**真实** actionCards.Service.Act（含 phase 校验、decision 落盘）。
func actOnCard(t *testing.T, cards *actioncards.Service, sessionID, callID, verb, payload string) (actioncards.Result, error) {
	t.Helper()
	var raw map[string]json.RawMessage
	if strings.TrimSpace(payload) != "" {
		raw = payloadOf(t, payload)
	}
	return cards.Act(context.Background(), sessionID, callID, verb, raw)
}

// writeGeneratedPackageAt 把一份生成包写进指定目录（Creator 夹具复用）。
func writeGeneratedPackageAt(t *testing.T, dir string, pkg generatedPackage) {
	t.Helper()
	pkg.write(t, dir)
}
