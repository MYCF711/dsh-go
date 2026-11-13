package main

// 官方 agent 侧 25 个方法的**宿主侧**真实语义测试。
//
// 这里不测形状（形状与参数校验在 internal/transport/handlers_agent_official_test.go），
// 只测"到底有没有真的动到真实状态"：goal 的 durable 迁移、反馈的重启可读、
// 上传是否真的落盘、skill/引用是否真扫目录、未实现的地方是不是响亮报错。

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"dshgo/codecs/v4"
	"dshgo/internal/agent"
	"dshgo/internal/kernel"
	"dshgo/internal/modes"
	"dshgo/internal/session"
	"dshgo/internal/spi"
	"dshgo/internal/transport"
)

// agentAPIFixture 是一套"真实 home + 真实 journal 持久化 + 真实会话来源"的最小宿主。
type agentAPIFixture struct {
	t        *testing.T
	home     string
	goals    *goalOverrideStore
	journals *transport.Journals
	src      *sessionSource
	core     *agentOfficialAPI
	api      transport.OfficialAgentAPI
}

func newAgentAPIFixture(t *testing.T) *agentAPIFixture {
	t.Helper()
	// 夹具必须切断宿主的 skill 根环境变量：`skills.list` 会真扫
	// `<home>/skills` 与 `DSHD_SKILLS_EXTRA`，若开发机上设了它们，
	// "空目录"之类的断言就会看到别人的 skill（假绿/假红各一半）。
	for _, key := range []string{"DSHD_SKILLS_EXTRA", "DSHD_SKILLS_BUNDLED", "DSH_BUNDLED_SKILL_DIR"} {
		t.Setenv(key, "")
	}
	home := t.TempDir()
	f := &agentAPIFixture{t: t, home: home}
	f.rebuild()
	return f
}

// rebuild 模拟一次进程重启：重建 journal 表、从磁盘恢复帧、重读 goal 存储。
func (f *agentAPIFixture) rebuild() {
	f.t.Helper()
	journals := transport.NewJournals(nil)
	store, err := restoreServeJournals(f.home, journals)
	if err != nil {
		f.t.Fatalf("恢复 journal: %v", err)
	}
	journals.SetPersistence(store)
	goals := newGoalOverrideStore(f.home)
	if err := goals.Load(); err != nil {
		f.t.Fatalf("读 goal store: %v", err)
	}
	goals.journal = &goalChangeJournal{journals: journals}
	src := newSessionSource(f.home, "")
	src.goals = goals
	core := newAgentOfficialAPICore(agentOfficialDeps{
		home: f.home, goals: goals, journals: journals, src: src,
	})
	f.journals, f.goals, f.src, f.core = journals, goals, src, core
	f.api = core.bind()
}

// newSession 建一条真实 v4 会话（可带 cwd）。
func (f *agentAPIFixture) newSession(cwd string, events func(*session.Log)) string {
	f.t.Helper()
	id, err := serveID()
	if err != nil {
		f.t.Fatalf("生成会话 id: %v", err)
	}
	if cwd == "" {
		cwd = f.home
	}
	lg, err := session.CreateSession(f.home, cwd, id, modes.DefaultID)
	if err != nil {
		f.t.Fatalf("建会话: %v", err)
	}
	if events != nil {
		events(lg)
	}
	if err := lg.Close(); err != nil {
		f.t.Fatalf("关闭会话: %v", err)
	}
	return id
}

func (f *agentAPIFixture) goalFile() string {
	return filepath.Join(f.home, "state", "session-goal.json")
}

func (f *agentAPIFixture) storedGoal(sessionID string) (goalRecord, bool) {
	f.t.Helper()
	var doc goalDocument
	raw, err := os.ReadFile(f.goalFile())
	if err != nil {
		if os.IsNotExist(err) {
			return goalRecord{}, false
		}
		f.t.Fatalf("读 goal 文件: %v", err)
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		f.t.Fatalf("解析 goal 文件: %v", err)
	}
	record, ok := doc.Goals[sessionID]
	return record, ok
}

// ---------------------------------------------------------------------------
// goals.*
// ---------------------------------------------------------------------------

func TestAgentOfficialGoalsLifecycle(t *testing.T) {
	f := newAgentAPIFixture(t)
	sessionID := f.newSession("", nil)

	// 还没建 goal：get 回 Goal=nil（不是错误）。
	got, err := f.api.GoalGet(sessionID)
	if err != nil {
		t.Fatalf("空 goal 的 get 报错了: %v", err)
	}
	if got.Goal != nil {
		t.Fatalf("还没建 goal，get 却给了 %+v", got.Goal)
	}

	maxRounds := 5
	ref, err := f.api.GoalCreate(sessionID, transport.OfficialGoalCreateRequest{
		Objective: "把 24 个方法对齐", MaxGoalRounds: &maxRounds,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if ref.ID == "" || ref.Revision != 1 {
		t.Fatalf("create 回的 ref 是 %+v", ref)
	}
	// 真实落盘：同一个文件、同一个 versioned 文档（不是第二份存储）。
	record, ok := f.storedGoal(sessionID)
	if !ok || record.Goal == nil {
		t.Fatalf("goal 没落进 %s", f.goalFile())
	}
	if record.Goal.ID != ref.ID || record.Goal.Phase != "active" || record.Goal.MaxGoalRounds != 5 {
		t.Fatalf("落盘的 goal 是 %+v", record.Goal)
	}

	got, err = f.api.GoalGet(sessionID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	view := got.Goal
	if view == nil {
		t.Fatal("create 之后 get 没有 goal")
	}
	if view.Activation != "armed" {
		t.Fatalf("create 之后 activation 应是 armed，实际 %q", view.Activation)
	}
	if view.CreatedAt <= 0 || view.UpdatedAt < view.CreatedAt {
		t.Fatalf("时间戳不成立：createdAt=%d updatedAt=%d", view.CreatedAt, view.UpdatedAt)
	}
	if view.RoundsStarted != 0 {
		t.Fatalf("新建 goal 的 roundsStarted 应为 0，实际 %d", view.RoundsStarted)
	}

	// 已有未完成 goal 时再 create 必须报冲突（官方 GOAL_ALREADY_EXISTS）。
	if _, err := f.api.GoalCreate(sessionID, transport.OfficialGoalCreateRequest{Objective: "再来一个"}); err == nil {
		t.Fatal("已有 active goal 还能再 create")
	} else if !errors.Is(err, transport.ErrGoalTransition) {
		t.Fatalf("重复 create 的错误类型不对: %v", err)
	}

	objective := "改成一句话"
	edited, err := f.api.GoalEdit(sessionID, ref, transport.OfficialGoalEditRequest{Objective: &objective})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if edited.Revision != 2 || edited.Objective != objective {
		t.Fatalf("edit 之后是 %+v", edited)
	}
	if edited.CreatedAt != view.CreatedAt {
		t.Fatalf("edit 改了 createdAt：%d → %d", view.CreatedAt, edited.CreatedAt)
	}
	if edited.UpdatedAt < view.UpdatedAt {
		t.Fatalf("edit 把 updatedAt 弄回去了：%d → %d", view.UpdatedAt, edited.UpdatedAt)
	}

	// 过期 ref 必须拒（乐观并发）。
	if _, err := f.api.GoalPause(sessionID, ref); err == nil {
		t.Fatal("用过期 ref pause 竟然成功了")
	}

	paused, err := f.api.GoalPause(sessionID, transport.OfficialGoalRef{ID: edited.ID, Revision: edited.Revision})
	if err != nil {
		t.Fatalf("pause: %v", err)
	}
	if paused.Phase != "paused" || paused.Activation != "disarmed" {
		t.Fatalf("pause 之后是 %+v", paused)
	}
	resumed, err := f.api.GoalResume(sessionID, transport.OfficialGoalRef{ID: paused.ID, Revision: paused.Revision})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumed.Phase != "active" || resumed.Activation != "armed" {
		t.Fatalf("resume 之后是 %+v", resumed)
	}

	// complete 之后再 complete：官方是**错**，不是静默成功。
	completed, err := f.api.GoalComplete(sessionID, transport.OfficialGoalRef{ID: resumed.ID, Revision: resumed.Revision})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if completed.Phase != "complete" || completed.Activation != "disarmed" {
		t.Fatalf("complete 之后是 %+v", completed)
	}
	if _, err := f.api.GoalComplete(sessionID, transport.OfficialGoalRef{ID: completed.ID, Revision: completed.Revision}); err == nil {
		t.Fatal("重复 complete 竟然成功了（应为 invalid transition）")
	}

	// 清掉：回 tombstone（revision 比被清的快照大一），并且真的从存储里删掉。
	tombstone, err := f.api.GoalClear(sessionID, transport.OfficialGoalRef{ID: completed.ID, Revision: completed.Revision})
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if tombstone.ID != completed.ID || tombstone.Revision != completed.Revision+1 {
		t.Fatalf("tombstone 是 %+v", tombstone)
	}
	if _, ok := f.storedGoal(sessionID); ok {
		t.Fatal("clear 之后存储里还有这条 goal")
	}
	got, err = f.api.GoalGet(sessionID)
	if err != nil {
		t.Fatalf("clear 之后 get: %v", err)
	}
	if got.Goal != nil {
		t.Fatalf("clear 之后还有 goal: %+v", got.Goal)
	}
	// 已 clear 再 clear：没有当前 goal → 冲突（官方 GOAL_NOT_FOUND 路径）。
	if _, err := f.api.GoalClear(sessionID, tombstone); err == nil {
		t.Fatal("没有 goal 时 clear 竟然成功了")
	}

	// 完成之后可以再开一个新 goal（官方 create 允许 phase=complete 之后新建）。
	next, err := f.api.GoalCreate(sessionID, transport.OfficialGoalCreateRequest{Objective: "下一件事"})
	if err != nil {
		t.Fatalf("clear 之后 create: %v", err)
	}
	if next.Revision != 1 || next.ID == ref.ID {
		t.Fatalf("新 goal 应该是新 id + revision 1：%+v", next)
	}
}

// goal 的时间戳必须能从 durable journal 还原（重启后不变），且没有历史时不编。
func TestAgentOfficialGoalTimesSurviveRestart(t *testing.T) {
	f := newAgentAPIFixture(t)
	sessionID := f.newSession("", nil)
	if _, err := f.api.GoalCreate(sessionID, transport.OfficialGoalCreateRequest{Objective: "重启也记得"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	before, err := f.api.GoalGet(sessionID)
	if err != nil || before.Goal == nil {
		t.Fatalf("get: %v", err)
	}

	f.rebuild() // 新进程：内存缓存全丢，只剩磁盘

	after, err := f.api.GoalGet(sessionID)
	if err != nil {
		t.Fatalf("重启后 get: %v", err)
	}
	if after.Goal == nil {
		t.Fatal("重启后 goal 不见了")
	}
	if after.Goal.CreatedAt != before.Goal.CreatedAt || after.Goal.UpdatedAt != before.Goal.UpdatedAt {
		t.Fatalf("重启后时间戳变了：%+v → %+v", before.Goal, after.Goal)
	}
	// activation 是 process-local 的，重启后必须回到 disarmed（官方语义）。
	if after.Goal.Activation != "disarmed" {
		t.Fatalf("重启后 activation 应重置为 disarmed，实际 %q", after.Goal.Activation)
	}
}

// 旧扁平格式迁移出来的 goal 没有 goal/change 历史 → 明确报错，不编时间。
func TestAgentOfficialLegacyGoalDoesNotFabricateTimes(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o755); err != nil {
		t.Fatalf("建 state 目录: %v", err)
	}
	legacy := map[string]string{"session-legacy": "旧目标"}
	raw, _ := json.Marshal(legacy)
	if err := os.WriteFile(filepath.Join(home, "state", "session-goal.json"), raw, 0o600); err != nil {
		t.Fatalf("写旧格式 goal: %v", err)
	}
	goals := newGoalOverrideStore(home)
	if err := goals.Load(); err != nil {
		t.Fatalf("迁移旧 goal: %v", err)
	}
	journals := transport.NewJournals(nil)
	goals.journal = &goalChangeJournal{journals: journals}
	src := newSessionSource(home, "")
	core := newAgentOfficialAPICore(agentOfficialDeps{home: home, goals: goals, journals: journals, src: src})
	api := core.bind()

	// 会话本身要真实存在，否则会先撞 no_such_session。
	id := "legacy-session"
	lg, err := session.CreateSession(home, home, id, modes.DefaultID)
	if err != nil {
		t.Fatalf("建会话: %v", err)
	}
	if err := lg.Close(); err != nil {
		t.Fatalf("关闭会话: %v", err)
	}
	// 迁移是按 session id 存的，键要用真实的 id。
	//
	// 单真值（HS-GOAL-TRUTH）之后 store 每次取锁都从盘上刷新，所以这次改键
	// 必须一并落盘，否则下一次刷新就把它覆盖回旧键。
	goals.mu.Lock()
	record := goals.m["session-legacy"]
	delete(goals.m, "session-legacy")
	goals.m[id] = record
	renamed := cloneGoalMap(goals.m)
	if err := goals.persistLocked(goalDocument{Version: goalStoreVersion, Goals: renamed}); err != nil {
		goals.mu.Unlock()
		t.Fatalf("落盘改键: %v", err)
	}
	goals.mu.Unlock()

	if _, err := api.GoalGet(id); err == nil {
		t.Fatal("没有迁移历史的 goal 竟然给出了时间戳")
	} else if !strings.Contains(err.Error(), "无法还原") {
		t.Fatalf("错误没说明时间无法还原: %v", err)
	}
}

// ---------------------------------------------------------------------------
// commands.* / subagents.* / userQuestions.attachWait
// ---------------------------------------------------------------------------

// commands.* / subagents.* / userQuestions.attachWait 现在是**真实现**，
// 原来那份"五个都 501"的断言已经过期。
//
// 这里改成钉住新的真实语义：**有真实支撑就走真实状态，没有就响亮失败**。
// 两个方向都不许静默成功——这才是那份断言真正想守的东西。
func TestAgentOfficialCommandsSubagentsQuestionsAreReal(t *testing.T) {
	f := newAgentAPIFixture(t)
	sessionID := f.newSession("", nil)

	// commands.list 必须是**真实**注册表（非空、可按名字找到）。
	commands, err := f.api.CommandsList(sessionID)
	if err != nil {
		t.Fatalf("commands.list: %v", err)
	}
	if len(commands) == 0 {
		t.Fatal("宿主有真实注册表时 list 不能是空数组")
	}
	if _, ok := findCommand(commands, "help"); !ok {
		t.Fatalf("注册表里没有 help：%v", commands)
	}

	// commands.execute 真的执行。
	exec, err := f.api.CommandsExecute(sessionID, "/help", nil)
	if err != nil {
		t.Fatalf("commands.execute: %v", err)
	}
	if exec == nil || exec.Result.Kind != "success" {
		t.Fatalf("commands.execute 没真执行：%+v", exec)
	}

	// 没有子会话时 subagents.prompt 必须**响亮**失败（绝不编 messageId）。
	receipt, err := f.api.SubagentsPrompt(transport.OfficialSubagentPromptRequest{
		RequestID: "r1", ParentSessionID: sessionID, ChildSessionID: "child-nope",
		Mode: "continuable", Delivery: "queue",
		Content: []transport.OfficialSubagentPromptContent{{Type: "text", Text: "x"}},
	})
	if err == nil {
		t.Fatalf("没有子会话竟然回了回执：%+v", receipt)
	}
	if receipt.MessageID != "" {
		t.Fatalf("失败时不许带回执：%+v", receipt)
	}

	// 未知子会话的中断同样响亮。
	if _, err := f.api.SubagentsInterruptByParent("child-nope", sessionID); err == nil {
		t.Fatal("未知子会话的中断竟然成功了")
	}

	// 没有这个 question 调用 → 响亮失败（remainingMs 不许编）。
	if _, err := f.api.UserQuestionsAttachWait(sessionID, "call-1"); err == nil {
		t.Fatal("不存在的 question 调用竟然回了 remainingMs")
	}
}

func findCommand(list []transport.OfficialCommand, name string) (transport.OfficialCommand, bool) {
	for _, cmd := range list {
		if cmd.Name == name {
			return cmd, true
		}
	}
	return transport.OfficialCommand{}, false
}

// userQuestions.answer 走真实的 ask_user_question 记录 + 真实 prompt 通道。
func TestAgentOfficialUserQuestionsAnswer(t *testing.T) {
	f := newAgentAPIFixture(t)
	var submitted []string
	f.core.deps.submit = func(sessionID, text string) (transport.PromptResult, error) {
		submitted = append(submitted, text)
		return transport.PromptResult{Accepted: true, TurnID: "turn-1"}, nil
	}
	f.api = f.core.bind()

	sessionID := f.newSession("", func(lg *session.Log) {
		if err := lg.Append("tool/call", session.ToolCallEvent(1, 1, "call-ask", "ask_user_question", `{"questions":[]}`)); err != nil {
			t.Fatalf("追加 tool/call: %v", err)
		}
		if err := lg.Append("tool/result", session.ToolResultEvent(1, 1, "call-ask", nil, false, "", nil)); err != nil {
			t.Fatalf("追加 tool/result: %v", err)
		}
	})

	stillOpen, err := f.api.UserQuestionsAnswer(sessionID, "call-ask", transport.OfficialQuestionAnswer{
		Answers: []transport.OfficialQuestionAnswerItem{{ID: "q1", Selected: []string{"A"}}},
	})
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	if !stillOpen {
		t.Fatal("问题还开着，答案应该被接受")
	}
	if len(submitted) != 1 || !strings.Contains(submitted[0], "q1") || !strings.Contains(submitted[0], "A") {
		t.Fatalf("答案没有作为用户消息送进会话：%v", submitted)
	}

	// 之后来了用户消息 → 问题已关：再答就不投递、回 false。
	f2 := f.newSession("", nil)
	_ = f2
	sessionID2 := f.newSession("", func(lg *session.Log) {
		_ = lg.Append("tool/call", session.ToolCallEvent(1, 1, "call-ask2", "ask_user_question", `{}`))
		_ = lg.Append("tool/result", session.ToolResultEvent(1, 1, "call-ask2", nil, false, "", nil))
		_ = lg.Append("user/message", session.UserMessage(nil))
	})
	submitted = nil
	stillOpen, err = f.api.UserQuestionsAnswer(sessionID2, "call-ask2", transport.OfficialQuestionAnswer{
		Answers: []transport.OfficialQuestionAnswerItem{{ID: "q1", Selected: []string{"B"}}},
	})
	if err != nil {
		t.Fatalf("已关闭的问题再答不该报错: %v", err)
	}
	if stillOpen || len(submitted) != 0 {
		t.Fatalf("已关闭的问题不该再投递：open=%v submitted=%v", stillOpen, submitted)
	}
	// 不存在的 callId：明确报"没有这次调用"。
	if _, err := f.api.UserQuestionsAnswer(sessionID2, "nope", transport.OfficialQuestionAnswer{
		Answers: []transport.OfficialQuestionAnswerItem{{ID: "q1", Selected: []string{"B"}}},
	}); err == nil {
		t.Fatal("不存在的 callId 竟然被接受了")
	}
}

// ---------------------------------------------------------------------------
// agentPresets.*
// ---------------------------------------------------------------------------

func TestAgentOfficialPresets(t *testing.T) {
	f := newAgentAPIFixture(t)
	sessionID := f.newSession("", nil)

	doc, err := f.api.AgentPresetRead(modes.DefaultID)
	if err != nil {
		t.Fatalf("读默认 preset: %v", err)
	}
	// 生产绑定由 WIRING 指向 agentPresetReadYAML；旧方法仍保留 TOML 供
	// 既有直接调用。两种正文都必须是真实声明，不能是同一份 TOML 冒充 YAML。
	if doc.AgentPreset != modes.DefaultID ||
		(!strings.Contains(doc.Content, "builtin: true") && !strings.HasPrefix(doc.Content, "- id: ")) {
		t.Fatalf("内置 preset 的正文既不是 TOML 也不是 entry-list YAML：%q", doc.Content)
	}
	if _, err := f.api.AgentPresetRead("nope"); err == nil {
		t.Fatal("未知 preset 竟然读到了")
	}

	// 用户自建模式：读到的必须是磁盘上的 TOML 原文。
	modesDir := filepath.Join(f.home, "modes")
	if err := os.MkdirAll(modesDir, 0o755); err != nil {
		t.Fatalf("建 modes 目录: %v", err)
	}
	tomlBody := "id = \"review\"\nname = \"Review\"\ndescription = \"只读审阅\"\ntools = [\"read\", \"grep\"]\n"
	if err := os.WriteFile(filepath.Join(modesDir, "review.toml"), []byte(tomlBody), 0o600); err != nil {
		t.Fatalf("写用户模式: %v", err)
	}
	userDoc, err := f.api.AgentPresetRead("review")
	if err != nil {
		t.Fatalf("读用户 preset: %v", err)
	}
	if userDoc.Content != tomlBody && !strings.HasPrefix(userDoc.Content, "- id: ") {
		t.Fatalf("用户 preset 正文既不是磁盘 TOML 也不是 entry-list YAML：%q", userDoc.Content)
	}

	selected, err := f.api.AgentPresetSelect(sessionID, "review")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if selected != "review" {
		t.Fatalf("select 回了 %q", selected)
	}
	// 真实落盘：重启后仍能读到这次选择。
	var docState agentPresetDocument
	found, err := agentReadDoc(filepath.Join(f.home, "state", "session-preset.json"), &docState)
	if err != nil || !found {
		t.Fatalf("preset 选择没落盘: found=%v err=%v", found, err)
	}
	if docState.Sessions[sessionID] != "review" {
		t.Fatalf("落盘的选择是 %v", docState.Sessions)
	}
	// 未知 preset / 未知会话都必须响亮失败。
	if _, err := f.api.AgentPresetSelect(sessionID, "nope"); err == nil {
		t.Fatal("未知 preset 竟然 select 成功")
	}
	if _, err := f.api.AgentPresetSelect("no-such-session", "review"); !errors.Is(err, transport.ErrNoSuchSession) {
		t.Fatalf("未知会话的错误类型不对: %v", err)
	}
}

// agentPresets.list：名册来自真实 modes（内置 + `<home>/modes/*.toml`）。
//
// `isDefault` 不是猜的：会话不点名 preset 时的回落值就是 `modes.DefaultID`。
// 坏 TOML 必须让 list 响亮失败（`modes.LoadDir` 不做静默跳过），不许编 broken 行。
func TestAgentOfficialPresetsList(t *testing.T) {
	f := newAgentAPIFixture(t)
	roster, err := f.api.AgentPresetsList()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(roster.Presets) != len(modes.Builtins()) {
		t.Fatalf("内置名册应有 %d 项，实际 %d：%+v",
			len(modes.Builtins()), len(roster.Presets), roster.Presets)
	}
	defaults := 0
	for _, p := range roster.Presets {
		if !p.IsDefault {
			continue
		}
		defaults++
		if p.ID != modes.DefaultID {
			t.Fatalf("只有 %q 可以是默认，实际默认是 %q", modes.DefaultID, p.ID)
		}
	}
	if defaults != 1 {
		t.Fatalf("默认 preset 应恰好 1 个，实际 %d", defaults)
	}

	// 用户自建模式出现在名册里（磁盘真值），且不是默认。
	modesDir := filepath.Join(f.home, "modes")
	if err := os.MkdirAll(modesDir, 0o755); err != nil {
		t.Fatalf("建 modes 目录: %v", err)
	}
	tomlBody := "id = \"review\"\nname = \"Review\"\ndescription = \"只读审阅\"\ntools = [\"read\", \"grep\"]\n"
	if err := os.WriteFile(filepath.Join(modesDir, "review.toml"), []byte(tomlBody), 0o600); err != nil {
		t.Fatalf("写用户模式: %v", err)
	}
	roster, err = f.api.AgentPresetsList()
	if err != nil {
		t.Fatalf("带用户模式的 list: %v", err)
	}
	var review *transport.OfficialAgentPreset
	for i := range roster.Presets {
		if roster.Presets[i].ID == "review" {
			review = &roster.Presets[i]
		}
	}
	if review == nil {
		t.Fatalf("用户模式没进名册：%+v", roster.Presets)
	}
	if review.IsDefault || review.Name != "Review" || review.Description != "只读审阅" {
		t.Fatalf("用户模式行不对：%+v", *review)
	}

	// A/B：另一个宿主（另一个 home）看不到这个模式。
	other := newAgentAPIFixture(t)
	rosterB, err := other.api.AgentPresetsList()
	if err != nil {
		t.Fatalf("B 的 list: %v", err)
	}
	for _, p := range rosterB.Presets {
		if p.ID == "review" {
			t.Fatalf("B 的 home 里没有 review，名册却出现了：%+v", rosterB.Presets)
		}
	}

	// 坏 TOML：整份名册响亮失败，不静默跳过、不编 broken。
	if err := os.WriteFile(filepath.Join(modesDir, "broken.toml"), []byte("id = \n"), 0o600); err != nil {
		t.Fatalf("写坏模式: %v", err)
	}
	if _, err := f.api.AgentPresetsList(); err == nil {
		t.Fatal("坏 TOML 竟然被静默跳过：名册失败必须响亮")
	}
}

// ---------------------------------------------------------------------------
// fileReferences.list
// ---------------------------------------------------------------------------

func TestAgentOfficialFileReferencesList(t *testing.T) {
	f := newAgentAPIFixture(t)
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, "src"), 0o755); err != nil {
		t.Fatalf("建 src: %v", err)
	}
	for _, name := range []string{"alpha.go", "beta.go", "notes.md"} {
		if err := os.WriteFile(filepath.Join(work, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("写 %s: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(work, "src", "handler.go"), []byte("x"), 0o600); err != nil {
		t.Fatalf("写 handler.go: %v", err)
	}
	sessionID := f.newSession(work, nil)

	all, err := f.api.FileReferencesList(sessionID, "")
	if err != nil {
		t.Fatalf("列根目录: %v", err)
	}
	kinds := map[string]string{}
	for _, ref := range all {
		kinds[ref.Path] = ref.Kind
	}
	if kinds["src"] != "directory" || kinds["alpha.go"] != "file" {
		t.Fatalf("引用候选 %v", kinds)
	}

	filtered, err := f.api.FileReferencesList(sessionID, "a")
	if err != nil {
		t.Fatalf("前缀过滤: %v", err)
	}
	if len(filtered) != 1 || filtered[0].Path != "alpha.go" {
		t.Fatalf("前缀 'a' 的结果 %+v", filtered)
	}

	nested, err := f.api.FileReferencesList(sessionID, "src/ha")
	if err != nil {
		t.Fatalf("子目录: %v", err)
	}
	if len(nested) != 1 || nested[0].Path != "src/handler.go" {
		t.Fatalf("子目录结果 %+v", nested)
	}

	if _, err := f.api.FileReferencesList(sessionID, "../../etc/passwd"); err == nil {
		t.Fatal("逃出工作目录的引用竟然被接受了")
	}
}

// ---------------------------------------------------------------------------
// fileUploads.upload
// ---------------------------------------------------------------------------

func TestAgentOfficialFileUploads(t *testing.T) {
	f := newAgentAPIFixture(t)
	sessionID := f.newSession("", nil)
	payload := []byte("hello 上传\n")
	value, err := f.api.FileUploadsUpload(sessionID, transport.OfficialFileUploadRequest{
		Data: base64.StdEncoding.EncodeToString(payload),
		Name: `C:\Users\someone\报告.txt`,
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if value.File.Bytes != int64(len(payload)) {
		t.Fatalf("bytes=%d", value.File.Bytes)
	}
	if strings.ContainsAny(value.File.Name, `\/:`) {
		t.Fatalf("显示名没被清洗成叶子名：%q", value.File.Name)
	}
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])
	if value.File.AttachmentID != digest {
		t.Fatalf("attachmentId 不是内容寻址：%q", value.File.AttachmentID)
	}
	blobPath := filepath.Join(f.home, "state", "uploads", "blobs", digest)
	stored, err := os.ReadFile(blobPath)
	if err != nil {
		t.Fatalf("上传的文件没落盘: %v", err)
	}
	if string(stored) != string(payload) {
		t.Fatalf("落盘内容不一致")
	}
	// 回执记录里带着接收方会话（回执只在那个 Agent 作用域里有效）。
	var receipt struct {
		ReceiptID string `json:"receiptId"`
		SessionID string `json:"sessionId"`
		Name      string `json:"name"`
		Bytes     int64  `json:"bytes"`
	}
	raw, err := os.ReadFile(filepath.Join(f.home, "state", "uploads", value.ReceiptID+".json"))
	if err != nil {
		t.Fatalf("回执没落盘: %v", err)
	}
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatalf("解析回执: %v", err)
	}
	if receipt.SessionID != sessionID || receipt.Bytes != int64(len(payload)) {
		t.Fatalf("回执内容 %+v", receipt)
	}

	if _, err := f.api.FileUploadsUpload(sessionID, transport.OfficialFileUploadRequest{Data: "不是base64!!"}); err == nil {
		t.Fatal("非法 base64 竟然被接受了")
	}
	oversize := make([]byte, agentUploadMaxBytes+1)
	if _, err := f.api.FileUploadsUpload(sessionID, transport.OfficialFileUploadRequest{
		Data: base64.StdEncoding.EncodeToString(oversize),
	}); err == nil {
		t.Fatal("超过上限的上传竟然被接受了")
	}
}

// ---------------------------------------------------------------------------
// messageFeedback.* / sessionFeedback.record
// ---------------------------------------------------------------------------

func TestAgentOfficialMessageFeedbackDurable(t *testing.T) {
	f := newAgentAPIFixture(t)
	sessionID := f.newSession("", nil)

	list, err := f.api.MessageFeedbackList(sessionID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !list.OK || list.Value == nil || len(list.Value.Items) != 0 {
		t.Fatalf("空反馈的 list 是 %+v", list)
	}

	put, err := f.api.MessageFeedbackPut(transport.OfficialMessageFeedbackPutRequest{
		SessionID: sessionID, MessageID: "msg-1", Rating: "positive", Note: "答得准", IfVersion: nil,
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if !put.OK || put.Value == nil || put.Value.Version == "" {
		t.Fatalf("put 结果 %+v", put)
	}
	version := put.Value.Version

	// 版本不匹配 → 业务失败（带 current），不是 500。
	conflict, err := f.api.MessageFeedbackPut(transport.OfficialMessageFeedbackPutRequest{
		SessionID: sessionID, MessageID: "msg-1", Rating: "negative", IfVersion: strPtr("stale"),
	})
	if err != nil {
		t.Fatalf("版本冲突不该是错误: %v", err)
	}
	if conflict.OK || conflict.Error == nil || conflict.Error.Code != "version-conflict" {
		t.Fatalf("版本冲突结果 %+v", conflict)
	}
	if len(conflict.Error.Current) == 0 || string(conflict.Error.Current) == "null" {
		t.Fatalf("版本冲突必须给出当前项：%q", conflict.Error.Current)
	}

	// 完全相同的写入是 no-op：版本不动。
	same, err := f.api.MessageFeedbackPut(transport.OfficialMessageFeedbackPutRequest{
		SessionID: sessionID, MessageID: "msg-1", Rating: "positive", Note: "答得准", IfVersion: &version,
	})
	if err != nil {
		t.Fatalf("no-op put: %v", err)
	}
	if !same.OK || same.Value.Version != version {
		t.Fatalf("相同写入改了版本：%+v", same.Value)
	}

	// 重启后仍然读得到（真实持久化）。
	f.rebuild()
	list, err = f.api.MessageFeedbackList(sessionID)
	if err != nil {
		t.Fatalf("重启后 list: %v", err)
	}
	if len(list.Value.Items) != 1 || list.Value.Items[0].MessageID != "msg-1" {
		t.Fatalf("重启后反馈丢了：%+v", list.Value)
	}

	del, err := f.api.MessageFeedbackDelete(transport.OfficialMessageFeedbackDeleteRequest{
		SessionID: sessionID, MessageID: "msg-1", IfVersion: version,
	})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !del.OK || del.Value == nil || !del.Value.Absent {
		t.Fatalf("delete 结果 %+v", del)
	}
	// 再删一次：稳定的 absent 后置条件，不报冲突。
	again, err := f.api.MessageFeedbackDelete(transport.OfficialMessageFeedbackDeleteRequest{
		SessionID: sessionID, MessageID: "msg-1", IfVersion: version,
	})
	if err != nil || !again.OK {
		t.Fatalf("重复 delete 应该仍然 ok：%+v %v", again, err)
	}
	// 不存在的会话 → session-not-found（业务失败，不是错误）。
	missing, err := f.api.MessageFeedbackList("no-such-session")
	if err != nil {
		t.Fatalf("未知会话不该是错误: %v", err)
	}
	if missing.OK || missing.Error == nil || missing.Error.Code != "session-not-found" {
		t.Fatalf("未知会话结果 %+v", missing)
	}
}

func TestAgentOfficialSessionFeedbackRecord(t *testing.T) {
	f := newAgentAPIFixture(t)
	sessionID := f.newSession("", nil)
	result, err := f.api.SessionFeedbackRecord(transport.OfficialSessionFeedbackRequest{
		SessionID: sessionID, Text: "界面还是有点慢", Category: "service-stability",
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if !result.OK || result.Value == nil || !result.Value.Recorded {
		t.Fatalf("record 结果 %+v", result)
	}
	var doc agentSessionFeedbackDocument
	found, err := agentReadDoc(filepath.Join(f.home, "state", "session-feedback.json"), &doc)
	if err != nil || !found {
		t.Fatalf("会话反馈没落盘: %v", err)
	}
	if len(doc.Items) != 1 || doc.Items[0].SessionID != sessionID || doc.Items[0].Text != "界面还是有点慢" {
		t.Fatalf("落盘内容 %+v", doc.Items)
	}
	missing, err := f.api.SessionFeedbackRecord(transport.OfficialSessionFeedbackRequest{SessionID: "nope"})
	if err != nil {
		t.Fatalf("未知会话不该是错误: %v", err)
	}
	if missing.OK || missing.Error == nil || missing.Error.Code != "session-not-found" {
		t.Fatalf("未知会话结果 %+v", missing)
	}
}

func strPtr(s string) *string { return &s }

// ---------------------------------------------------------------------------
// sessionReferenceResolver.candidates / toolResults.watch
// ---------------------------------------------------------------------------

func TestAgentOfficialSessionReferenceCandidates(t *testing.T) {
	f := newAgentAPIFixture(t)
	work := t.TempDir()
	self := f.newSession(work, nil)
	other := f.newSession(work, nil)
	elsewhere := f.newSession(t.TempDir(), nil)

	candidates, err := f.api.SessionReferenceCandidates(self, "")
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	ids := map[string]transport.OfficialSessionReferenceCandidate{}
	for _, c := range candidates {
		ids[c.SessionID] = c
	}
	if _, has := ids[self]; has {
		t.Fatal("自己不该出现在候选里")
	}
	if _, has := ids[other]; !has {
		t.Fatalf("同工作区的会话没出现：%+v", candidates)
	}
	if got := ids[other]; got.SameWorkspace != true || !strings.HasPrefix(got.Mention, "@[") ||
		!strings.HasSuffix(got.Mention, "](dsh-session:"+other+")") {
		t.Fatalf("同工作区候选 %+v", got)
	}
	if got := ids[elsewhere]; got.SameWorkspace {
		t.Fatalf("不同工作区的 sameWorkspace 应为 false：%+v", got)
	}
	// 同工作区的排在前面（真实排序依据）。
	if candidates[0].SessionID != other {
		t.Fatalf("排序没把同工作区放前面：%+v", candidates)
	}
	// query 过滤真的生效。
	filtered, err := f.api.SessionReferenceCandidates(self, elsewhere[:8])
	if err != nil {
		t.Fatalf("按 query 过滤: %v", err)
	}
	if len(filtered) != 1 || filtered[0].SessionID != elsewhere {
		t.Fatalf("query 过滤结果 %+v", filtered)
	}
}

func TestAgentOfficialToolResultsWatch(t *testing.T) {
	f := newAgentAPIFixture(t)
	var resultSeq int64
	sessionID := f.newSession("", func(lg *session.Log) {
		if err := lg.Append("tool/call", session.ToolCallEvent(1, 1, "call-1", "bash", `{"command":"echo hi"}`)); err != nil {
			t.Fatalf("tool/call: %v", err)
		}
		if err := lg.Append("tool/result", session.ToolResultEvent(1, 1, "call-1", nil, false, "", nil)); err != nil {
			t.Fatalf("tool/result: %v", err)
		}
	})
	// 从 durable 日志里取真实的 seq（不猜）。
	path, err := f.src.resolve(sessionID)
	if err != nil {
		t.Fatalf("解析会话路径: %v", err)
	}
	log, err := v4.ReadFile(path)
	if err != nil {
		t.Fatalf("读 v4: %v", err)
	}
	for _, ev := range log.Events {
		if ev.Type == "tool/result" {
			resultSeq = ev.Seq
		}
	}
	if resultSeq == 0 {
		t.Fatal("夹具里没有 tool/result")
	}

	watch, err := f.api.ToolResultsWatch(sessionID, resultSeq)
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	if !watch.Available {
		t.Fatalf("存在的工具结果应该 available：%+v", watch)
	}
	var value map[string]any
	if err := json.Unmarshal(watch.Value, &value); err != nil {
		t.Fatalf("value 不是 JSON: %v", err)
	}
	if value["callId"] != "call-1" || value["tool"] != "bash" {
		t.Fatalf("工具结果内容 %v", value)
	}

	// 不存在的 seq → available:false（官方的 cold read unavailable），不是错误。
	absent, err := f.api.ToolResultsWatch(sessionID, resultSeq+100)
	if err != nil {
		t.Fatalf("不存在的 seq 不该报错: %v", err)
	}
	if absent.Available {
		t.Fatalf("不存在的 seq 竟然 available：%+v", absent)
	}
	if absent.Value != nil {
		t.Fatalf("available=false 时不该有 value：%q", absent.Value)
	}
}

// A/B 两条会话的 goal 与反馈绝不互相污染。
func TestAgentOfficialSessionsDoNotCross(t *testing.T) {
	f := newAgentAPIFixture(t)
	a := f.newSession("", nil)
	b := f.newSession("", nil)
	if _, err := f.api.GoalCreate(a, transport.OfficialGoalCreateRequest{Objective: "A 的事"}); err != nil {
		t.Fatalf("A create: %v", err)
	}
	gotB, err := f.api.GoalGet(b)
	if err != nil {
		t.Fatalf("B get: %v", err)
	}
	if gotB.Goal != nil {
		t.Fatalf("A 的 goal 串到了 B：%+v", gotB.Goal)
	}
	if _, err := f.api.MessageFeedbackPut(transport.OfficialMessageFeedbackPutRequest{
		SessionID: a, MessageID: "m", Rating: "positive", IfVersion: nil,
	}); err != nil {
		t.Fatalf("A 写反馈: %v", err)
	}
	listB, err := f.api.MessageFeedbackList(b)
	if err != nil {
		t.Fatalf("B list: %v", err)
	}
	if len(listB.Value.Items) != 0 {
		t.Fatalf("A 的反馈串到了 B：%+v", listB.Value.Items)
	}
}

// 保证 fixture 生成的文件路径在测试里被真的用到（避免 helper 漂移）。
func TestAgentOfficialFixtureUsesGoalStoreFile(t *testing.T) {
	f := newAgentAPIFixture(t)
	sessionID := f.newSession("", nil)
	if _, err := f.api.GoalCreate(sessionID, transport.OfficialGoalCreateRequest{Objective: "写文件"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := os.Stat(f.goalFile()); err != nil {
		t.Fatalf("goal 存储文件不存在（%s）：%v", f.goalFile(), err)
	}
	if fmt.Sprintf("%T", f.api.GoalGet) == "" {
		t.Fatal("unreachable")
	}
}

// ---------------------------------------------------------------------------
// subagent 工具 → 宿主可续问（HOST-SUBAGENT-CONTINUABLE-B 的交界）
// ---------------------------------------------------------------------------

// TestSubagentToolContinuableReachesServeAdoption 钉住"模型侧入口真的能发起可续问"。
//
// 两条缺口在这里接上：工具 schema 里的 `continuable` 必须一路走到 subRunner 的
// 真实 spawn（durable v4 子会话 + `subagent/spawn` 父子帧），而 serve 侧
// （**另一个进程**：这里用同 home 的第二套宿主模拟，它没有任何内存状态）
// 必须能从盘上认领这个 child。任何一环断开，真机上 `subagents.prompt`
// 都只会回 no_such_session。
//
// 为什么走 kernel 装配而不是直接 new 工具：`Tool.svc` 未导出，模型能看到的
// 也只有装配后的工具表。通过 `reg.Get("subagent")` 拿到的那一份，就是线上
// 模型实际调到的那一份。
func TestSubagentToolContinuableReachesServeAdoption(t *testing.T) {
	e := newSubagentE2E(t, nil)
	parent := e.newSession("")
	before := mustSessionIDs(t, e.home)

	// 生产调用点：真 kernel 装配 + 真 subProvider，只把模型换成假 chat。
	subp := &subProvider{}
	subp.Bind(&subRunner{
		chat: subagentFakeChat{reply: "在的"}, tools: subagentEmptyRegistry{},
		model: "test-model", maxSteps: 2, timeout: 10 * time.Second, home: e.home,
	})
	k := kernel.New("subagent-tool-e2e")
	defer k.Root().Dispose()
	if _, err := kernel.Assemble(k.Root(), builtinModules(".", e.home, 0, subp)); err != nil {
		t.Fatalf("装配: %v", err)
	}
	reg, err := kernel.Inject(k.Root(), spi.ToolsKey)
	if err != nil {
		t.Fatalf("取工具表: %v", err)
	}
	tool, ok := reg.Get("subagent")
	if !ok {
		t.Fatal("工具表里没有 subagent")
	}

	// 模型就是这么调的：参数走 JSON，不是 Go 结构体。
	res, err := tool.Exec(spi.WithSession(context.Background(), parent),
		json.RawMessage(`{"description":"续问探针","prompt":"回答：在的","continuable":true}`))
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !strings.Contains(res.Text, "在的") {
		t.Fatalf("子代理正文没回来: %q", res.Text)
	}

	// 盘上必须**多出恰好一条**子会话（多数一条 = 重复 spawn，少一条 = 没落盘）。
	after := mustSessionIDs(t, e.home)
	if len(after) != len(before)+1 {
		t.Fatalf("contiuable 委派没有留下恰好一条子会话：before=%v after=%v", before, after)
	}
	var child string
	for _, id := range after {
		if !strIn(id, before) {
			child = id
		}
	}
	if child == "" || child == parent {
		t.Fatalf("没认出新的子会话 id：before=%v after=%v", before, after)
	}
	rec, found, err := agent.FindChild(e.home, child)
	if err != nil || !found {
		t.Fatalf("盘上认不回父子归属: found=%v err=%v", found, err)
	}
	if rec.ParentSessionID != parent {
		t.Fatalf("归属不对: %+v（期望父 %s）", rec, parent)
	}

	// serve 侧（新进程 = 空内存表）必须能认领并投递。
	other := newSubagentE2EAt(t, e.home, nil)
	status, raw := other.post("subagents.prompt", promptRequest("req-tool-cont", parent, child, "queue"))
	if status != http.StatusOK {
		t.Fatalf("续问 status=%d 期望 200；body=%s", status, raw)
	}
	if _, messageID := decode(t, raw); messageID == "" {
		t.Fatalf("续问没回 messageId: %s", raw)
	}
	if len(other.delivered) != 1 || !strings.HasPrefix(other.delivered[0], child+"|") {
		t.Fatalf("投递没落到子会话上: %v", other.delivered)
	}
}

// TestSubagentToolWithoutContinuableWritesNoSession 钉住默认关闭的**宿主侧**证据。
//
// 工具层逐字节断言（modules/tool/subagent）证明请求形状没变；这一条证明
// "没变"在宿主上意味着**盘上零新增**——不传 continuable 的委派不会在
// session.list 里多出一条能被人续问的子会话（否则那个 child 会一直
// 以 no_such_session 之外的方式存在，授权边界就多了一个口子）。
func TestSubagentToolWithoutContinuableWritesNoSession(t *testing.T) {
	e := newSubagentE2E(t, nil)
	parent := e.newSession("")
	before := mustSessionIDs(t, e.home)

	subp := &subProvider{}
	subp.Bind(&subRunner{
		chat: subagentFakeChat{reply: "做完了"}, tools: subagentEmptyRegistry{},
		model: "test-model", maxSteps: 2, timeout: 10 * time.Second, home: e.home,
	})
	k := kernel.New("subagent-tool-e2e")
	defer k.Root().Dispose()
	if _, err := kernel.Assemble(k.Root(), builtinModules(".", e.home, 0, subp)); err != nil {
		t.Fatalf("装配: %v", err)
	}
	reg, err := kernel.Inject(k.Root(), spi.ToolsKey)
	if err != nil {
		t.Fatalf("取工具表: %v", err)
	}
	tool, ok := reg.Get("subagent")
	if !ok {
		t.Fatal("工具表里没有 subagent")
	}
	if _, err := tool.Exec(spi.WithSession(context.Background(), parent),
		json.RawMessage(`{"description":"一次性","prompt":"回答：做完了"}`)); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	after := mustSessionIDs(t, e.home)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("不可续问委派却写了会话：%v → %v", before, after)
	}

	// 那个 child 不存在，所以即使有人拿一个 id 来投，也必须是 no_such_session。
	status, raw := e.post("subagents.prompt", promptRequest("req-none", parent, "child-never-spawned", "queue"))
	if status != http.StatusBadRequest {
		t.Fatalf("不存在的 child status=%d 期望 400；body=%s", status, raw)
	}
	if code, messageID := decode(t, raw); code != "no_such_session" || messageID != "" {
		t.Fatalf("code=%q messageId=%q（都必须为空/no_such_session）；body=%s", code, messageID, raw)
	}
}

// mustSessionIDs 列 home 下所有会话 id（升序，便于逐项比对）。
func mustSessionIDs(t *testing.T, home string) []string {
	t.Helper()
	list, err := listSessions(home, "")
	if err != nil {
		t.Fatalf("列会话: %v", err)
	}
	ids := make([]string, 0, len(list))
	for _, s := range list {
		ids = append(ids, s.ID)
	}
	sort.Strings(ids)
	return ids
}

// strIn 是 slices.Contains 的本地版（本包还没用 Go 1.21 的 slices，少一个 import）。
func strIn(want string, set []string) bool {
	for _, v := range set {
		if v == want {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 真机：continuable 委派 → 父轮结束后 subagents.prompt 续问
// ---------------------------------------------------------------------------

// newSubagentRealGateway 是脚本化的 OpenAI 兼容网关（真 SSE 流）。
//
// 为什么脚本化而不是打真模型：这条用例要证的是**宿主的接线**（工具参数 →
// 真实 spawn → 跨进程认领 → 投递），模型愿意调哪个工具属于另一条线。
// 脚本按"最后一条 user 消息 + 有没有 tool 消息"决定回什么，因此整轮是确定的。
func newSubagentRealGateway(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":[]}`)
			return
		}
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		var req struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
		var lastUser string
		hasTool := false
		for _, m := range req.Messages {
			if m.Role == "tool" {
				hasTool = true
			}
			if m.Role == "user" {
				var s string
				if json.Unmarshal(m.Content, &s) == nil {
					lastUser = s
				}
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		switch {
		case hasTool:
			// 工具已执行完：父轮收尾。
			fmt.Fprint(w, "data: "+`{"choices":[{"delta":{"content":"子代理说：在的"},"finish_reason":"stop"}]}`+"\n\n")
		case strings.Contains(lastUser, "不要 continuable"):
			// 不可续问：参数里**没有** continuable 这个键。
			fmt.Fprint(w, "data: "+`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-nc","type":"function",`+
				`"function":{"name":"subagent","arguments":"{\"description\":\"一次性\",\"prompt\":\"回答：做完了\"}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n")
		case strings.Contains(lastUser, "请调用 subagent"):
			// 可续问：模型就是这么发 continuable 的。
			fmt.Fprint(w, "data: "+`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-c","type":"function",`+
				`"function":{"name":"subagent","arguments":"{\"description\":\"续问探针\",\"prompt\":\"回答：在的\",\"continuable\":true}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n")
		case strings.Contains(lastUser, "继续说说刚才那个结论"):
			// 续问那一轮（child 会话的第二轮）。
			fmt.Fprint(w, "data: "+`{"choices":[{"delta":{"content":"续问：在的"},"finish_reason":"stop"}]}`+"\n\n")
		default:
			// 子代理自己的模型调用。
			fmt.Fprint(w, "data: "+`{"choices":[{"delta":{"content":"在的"},"finish_reason":"stop"}]}`+"\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
}

// waitSubagentTurnDone 等某一轮真收工（turn/done），turn/error 立刻红。
func waitSubagentTurnDone(t *testing.T, client *http.Client, base, sid string, sinceSeq int, turnID string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		h := postPluginGlue(t, client, base, "session.history",
			map[string]any{"id": sid, "sinceSeq": sinceSeq, "limit": 2000}, http.StatusOK)["result"].(map[string]any)
		frames, _ := h["frames"].([]any)
		for _, raw := range frames {
			frame, _ := raw.(map[string]any)
			data, _ := frame["data"].(map[string]any)
			if data["turnId"] != turnID {
				continue
			}
			switch frame["type"] {
			case "turn/error":
				t.Fatalf("turn %s 失败: %+v", turnID, frame)
			case "turn/done":
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("等 turn %s 收工超时（%d 帧）", turnID, len(frames))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// promptSubagentTurn 发一条 prompt 并等它那一轮真收工。
func promptSubagentTurn(t *testing.T, client *http.Client, base, sid, prompt string) {
	t.Helper()
	h := postPluginGlue(t, client, base, "session.history",
		map[string]any{"id": sid, "sinceSeq": 0, "limit": 2000}, http.StatusOK)["result"].(map[string]any)
	since := 0
	if v, ok := h["lastSeq"].(float64); ok {
		since = int(v)
	}
	res := postPluginGlue(t, client, base, "session.prompt",
		map[string]any{"id": sid, "text": prompt}, http.StatusOK)["result"].(map[string]any)
	turnID, _ := res["turnId"].(string)
	if turnID == "" {
		t.Fatalf("session.prompt 未返回 turnId: %+v", res)
	}
	waitSubagentTurnDone(t, client, base, sid, since, turnID)
}

// sessionIDs 走真 HTTP 的 session.list 拿当前 home 的全部会话 id。
func sessionIDs(t *testing.T, client *http.Client, base string) []string {
	t.Helper()
	res := postPluginGlue(t, client, base, "session.list", map[string]any{}, http.StatusOK)["result"].(map[string]any)
	list, _ := res["sessions"].([]any)
	ids := make([]string, 0, len(list))
	for _, raw := range list {
		item, _ := raw.(map[string]any)
		if id, ok := item["id"].(string); ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// subagentPromptWire 是官方 SubagentPromptRequest 的线上形状。
func subagentPromptWire(requestID, parent, child, text string) map[string]any {
	return map[string]any{"request": map[string]any{
		"requestId":       requestID,
		"parentSessionId": parent,
		"childSessionId":  child,
		"mode":            "continuable",
		"delivery":        "queue",
		"content":         []map[string]any{{"type": "text", "text": text}},
	}}
}

// waitLogText 轮询子会话的 v4 日志直到出现 want。
//
// 为什么要轮询：投递（subagents.prompt）只负责**接受**，续问那一轮是
// 另一个 `dshd run` 子进程在跑；日志是它写的，落地有先后。
func waitLogText(t *testing.T, home, id, want string) {
	t.Helper()
	path, err := session.FindByID(home, id)
	if err != nil {
		t.Fatalf("找会话 %q: %v", id, err)
	}
	deadline := time.Now().Add(90 * time.Second)
	for {
		if log, err := v4.ReadFile(path); err == nil {
			for _, ev := range log.Events {
				if strings.Contains(string(ev.Data), want) || strings.Contains(string(ev.Raw), want) {
					return
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("等了 90s，子会话 %s 的日志里仍没有 %q", id, want)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestSubagentContinuableRealMachine 是真机证据：**真 dshd serve + 真子进程 +
// 真 v4 会话**，只有模型是脚本化的。
//
// 五条：
//  1. 模型带 continuable=true 调 subagent → 盘上真的多出一条子会话，归属父会话；
//  2. 父轮真收工（turn/done）之后，subagents.prompt 续问成功并回真 messageId；
//  3. 那条续问真的跑了一轮（子会话日志里出现续问那一轮的 assistant 正文）；
//  4. 不带 continuable 的委派**盘上零新增**（默认关闭不是"少写一半"，是一个字节都不写）；
//  5. 拿一个不存在的 child 去投 → 400 no_such_session，绝不回 messageId。
func TestSubagentContinuableRealMachine(t *testing.T) {
	gateway := newSubagentRealGateway(t)
	t.Cleanup(gateway.Close)
	t.Setenv("DSHD_LLM_BASE", gateway.URL+"/v1")
	t.Setenv("DSHD_LLM_KEY", "")
	t.Setenv("DSHD_SERVE_MODEL_RUNNER", "")

	exe := buildPluginGlueDSHD(t)
	home := t.TempDir()
	server := startPluginGlueServer(t, exe, home, t.TempDir())
	client := connectPluginGlueServer(t, server)
	client.Timeout = 90 * time.Second

	// --- 1/2/3：continuable 委派 → 父轮收工 → 续问 ---
	parent := postPluginGlue(t, client, server.base, "session.create",
		map[string]any{}, http.StatusOK)["result"].(map[string]any)["sessionId"].(string)
	promptSubagentTurn(t, client, server.base, parent,
		"请调用 subagent 工具一次：description='续问探针'，prompt='回答：在的'，continuable=true。")

	ids := sessionIDs(t, client, server.base)
	if len(ids) != 2 {
		t.Fatalf("continuable 委派应留下恰好一条子会话：%v", ids)
	}
	var child string
	for _, id := range ids {
		if id != parent {
			child = id
		}
	}
	rec, found, err := agent.FindChild(home, child)
	if err != nil || !found {
		t.Fatalf("盘上认不回父子归属: found=%v err=%v", found, err)
	}
	if rec.ParentSessionID != parent {
		t.Fatalf("归属不对: %+v（期望父 %s）", rec, parent)
	}

	receipt := postPluginGlue(t, client, server.base, "subagents.prompt",
		subagentPromptWire("req-real-cont", parent, child, "继续说说刚才那个结论"), http.StatusOK)["result"].(map[string]any)
	messageID, _ := receipt["messageId"].(string)
	if messageID == "" {
		t.Fatalf("续问没有 messageId: %+v", receipt)
	}
	waitLogText(t, home, child, "续问：在的")

	// --- 4/5：不可续问 = 盘上零新增；不存在的 child 仍 no_such_session ---
	parent2 := postPluginGlue(t, client, server.base, "session.create",
		map[string]any{}, http.StatusOK)["result"].(map[string]any)["sessionId"].(string)
	before := sessionIDs(t, client, server.base)
	promptSubagentTurn(t, client, server.base, parent2,
		"请调用 subagent 工具一次：description='一次性'，prompt='回答：做完了'。不要 continuable。")
	after := sessionIDs(t, client, server.base)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("不可续问委派却写了会话：%v → %v", before, after)
	}
	envelope := postPluginGlue(t, client, server.base, "subagents.prompt",
		subagentPromptWire("req-real-none", parent2, "child-never-spawned", "在吗"), http.StatusBadRequest)
	errObj, _ := envelope["error"].(map[string]any)
	if errObj["code"] != "no_such_session" {
		t.Fatalf("不存在的 child code=%v 期望 no_such_session", errObj["code"])
	}
	if _, hasResult := envelope["result"]; hasResult {
		t.Fatalf("失败却带了 result: %+v", envelope)
	}
	t.Logf("SUBAGENT_REAL OK=5 FAIL=0 parent=%s child=%s messageId=%s", parent, child, messageID)
}

// ---------------------------------------------------------------------------
// rc.2 AgentTeam 面（`agentTeams.setEnabled`）与 `subagents.interruptByUser`
// ---------------------------------------------------------------------------

// newChildSession 建一条**真的是别人子会话**的 v4 会话：盘上有 durable
// `subagent/spawn` 父子帧（`internal/agent.FindChild` 的判据同源）。
func (f *agentAPIFixture) newChildSession(parentID string) string {
	f.t.Helper()
	id, err := serveID()
	if err != nil {
		f.t.Fatalf("生成子会话 id: %v", err)
	}
	lg, err := session.CreateSession(f.home, f.home, id, modes.DefaultID)
	if err != nil {
		f.t.Fatalf("建子会话: %v", err)
	}
	rec := agent.ChildRecord{
		ChildSessionID: id, ParentSessionID: parentID, SpawnedAt: time.Now().UnixMilli(),
	}
	if err := lg.Append(agent.EventChildSpawn, rec); err != nil {
		f.t.Fatalf("写 subagent/spawn: %v", err)
	}
	if err := lg.Close(); err != nil {
		f.t.Fatalf("关闭子会话: %v", err)
	}
	return id
}

// teamModeEvents 读会话 v4 日志里**全部** `team/mode` 事件（按 seq 顺序）。
func (f *agentAPIFixture) teamModeEvents(sessionID string) []session.TeamModePayload {
	f.t.Helper()
	events, err := f.core.agentReadEvents(sessionID)
	if err != nil {
		f.t.Fatalf("读会话 %s 的 v4 事件: %v", sessionID, err)
	}
	out := make([]session.TeamModePayload, 0, 1)
	for _, ev := range events {
		if ev.Type != session.EventTeamMode {
			continue
		}
		payload, perr := session.ParseTeamModePayload(ev.Data)
		if perr != nil {
			f.t.Fatalf("seq %d 的 team/mode 载荷非法: %v", ev.Seq, perr)
		}
		out = append(out, payload)
	}
	return out
}

// journalFrames 读该会话 Transport journal 里的**全部**原始帧（`session.history`
// 读的就是这一份）。
func (f *agentAPIFixture) journalFrames(sessionID string) []transport.ServerFrame {
	f.t.Helper()
	return f.journals.Get(sessionID).Window(transport.SinceNothing, 0).Frames
}

// 状态变化 → **恰有一条** `team/mode` 事件（v4）与**恰有一帧**（Transport journal）。
func TestAgentTeamsSetEnabledAppendsTeamModeEvent(t *testing.T) {
	f := newAgentAPIFixture(t)
	root := f.newSession("", nil)

	// 从未设置过 = 官方默认 false，且没有任何事件（不是"写了一条 false"）。
	if got, err := f.core.agentTeamsIsEnabled(root); err != nil || got {
		t.Fatalf("未设置时应当默认关，得到 (%v, %v)", got, err)
	}
	if evs := f.teamModeEvents(root); len(evs) != 0 {
		t.Fatalf("未设置时不该有 team/mode 事件：%+v", evs)
	}
	if frames := f.journalFrames(root); len(frames) != 0 {
		t.Fatalf("未设置时不该有 journal 帧：%+v", frames)
	}

	if err := f.core.agentTeamsSetEnabled(transport.OfficialAgentTeamSetEnabledRequest{
		Agent: root, Enabled: true,
	}); err != nil {
		t.Fatalf("开 Team: %v", err)
	}

	// 官方形状：`team/mode {enabled:boolean}`，一条。
	evs := f.teamModeEvents(root)
	if len(evs) != 1 || !evs[0].Enabled {
		t.Fatalf("team/mode 事件 = %+v，want 恰好一条 {enabled:true}", evs)
	}
	// Transport journal：`session.history` 能读到的原始帧。
	frames := f.journalFrames(root)
	if len(frames) != 1 {
		t.Fatalf("journal 帧数 = %d，want 1：%+v", len(frames), frames)
	}
	if frames[0].Seq != 1 || frames[0].Type != "team/mode" || frames[0].Time <= 0 {
		t.Fatalf("journal 帧 = %+v，want {seq:1,type:team/mode,time>0}", frames[0])
	}
	var payload struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(frames[0].Data, &payload); err != nil || !payload.Enabled {
		t.Fatalf("journal 帧载荷 = %s（err=%v），want {\"enabled\":true}", frames[0].Data, err)
	}
	if got, err := f.core.agentTeamsIsEnabled(root); err != nil || !got {
		t.Fatalf("开 Team 后 isEnabled = (%v, %v)，want true", got, err)
	}
}

// 同值重复设置**不重复 append**（官方 `if (this.isEnabled(root) !== enabled)`）；
// 关闭真的生效，且再关一次也不重复。
func TestAgentTeamsSetEnabledIsIdempotentAndCanDisable(t *testing.T) {
	f := newAgentAPIFixture(t)
	root := f.newSession("", nil)

	if err := f.core.agentTeamsSetEnabled(transport.OfficialAgentTeamSetEnabledRequest{Agent: root, Enabled: true}); err != nil {
		t.Fatalf("开 Team: %v", err)
	}
	// 再设一次同值：状态不变，事件与帧都不该多。
	if err := f.core.agentTeamsSetEnabled(transport.OfficialAgentTeamSetEnabledRequest{Agent: root, Enabled: true}); err != nil {
		t.Fatalf("重复开 Team: %v", err)
	}
	if evs := f.teamModeEvents(root); len(evs) != 1 {
		t.Fatalf("同值重复设置 append 了额外事件：%+v", evs)
	}
	if frames := f.journalFrames(root); len(frames) != 1 {
		t.Fatalf("同值重复设置多推了 journal 帧：%+v", frames)
	}

	// 关闭：生效，且只多一条。
	if err := f.core.agentTeamsSetEnabled(transport.OfficialAgentTeamSetEnabledRequest{Agent: root, Enabled: false}); err != nil {
		t.Fatalf("关 Team: %v", err)
	}
	evs := f.teamModeEvents(root)
	if len(evs) != 2 || !evs[0].Enabled || evs[1].Enabled {
		t.Fatalf("关 Team 后事件 = %+v，want [{true} {false}]", evs)
	}
	if frames := f.journalFrames(root); len(frames) != 2 || frames[0].Seq != 1 || frames[1].Seq != 2 {
		t.Fatalf("关 Team 后 journal 帧 = %+v，want seq 连续 1,2", frames)
	}
	if got, err := f.core.agentTeamsIsEnabled(root); err != nil || got {
		t.Fatalf("关闭后应当 false，得到 (%v, %v)", got, err)
	}
	// 再关一次：仍然只有两条。
	if err := f.core.agentTeamsSetEnabled(transport.OfficialAgentTeamSetEnabledRequest{Agent: root, Enabled: false}); err != nil {
		t.Fatalf("重复关 Team: %v", err)
	}
	if evs := f.teamModeEvents(root); len(evs) != 2 {
		t.Fatalf("同值重复关闭 append 了额外事件：%+v", evs)
	}
}

// 选择必须**真实持久化**：写盘之后"重启宿主"（重建 core + 从盘恢复 journal）仍能读回。
func TestAgentTeamsSetEnabledPersistsAcrossRestart(t *testing.T) {
	f := newAgentAPIFixture(t)
	root := f.newSession("", nil)

	if err := f.core.agentTeamsSetEnabled(transport.OfficialAgentTeamSetEnabledRequest{
		Agent: root, Enabled: true,
	}); err != nil {
		t.Fatalf("开 Team: %v", err)
	}

	// 模拟进程重启：新 core、新 journal 表，只能从盘上读。
	f.rebuild()
	got, err := f.core.agentTeamsIsEnabled(root)
	if err != nil {
		t.Fatalf("重启后读选择: %v", err)
	}
	if !got {
		t.Fatal("重启后 Team 选择丢了：这不是真实持久化（v4 事件必须落盘）")
	}
	// journal 也要能恢复同一帧（`session.history` 断线补洞靠它）。
	frames := f.journalFrames(root)
	if len(frames) != 1 || frames[0].Type != "team/mode" || frames[0].Seq != 1 {
		t.Fatalf("重启后 journal 帧 = %+v，want 一条 team/mode seq=1", frames)
	}
}

// 只有树根能改：子会话调用 → ErrOfficialTeamLeadRequired，且**不 append 事件/帧**。
func TestAgentTeamsSetEnabledRejectsDescendant(t *testing.T) {
	f := newAgentAPIFixture(t)
	root := f.newSession("", nil)
	child := f.newChildSession(root)

	err := f.core.agentTeamsSetEnabled(transport.OfficialAgentTeamSetEnabledRequest{Agent: child, Enabled: true})
	if !errors.Is(err, transport.ErrOfficialTeamLeadRequired) {
		t.Fatalf("子会话改 Team 模式应当是 TEAM_LEAD_REQUIRED，实际 %v", err)
	}
	if evs := f.teamModeEvents(child); len(evs) != 0 {
		t.Fatalf("被拒的调用不该 append 事件：%+v", evs)
	}
	if frames := f.journalFrames(child); len(frames) != 0 {
		t.Fatalf("被拒的调用不该推 journal 帧：%+v", frames)
	}

	// 根会话自己仍然可以改（判据是"自己有没有父"，不是"是不是有子"）。
	if err := f.core.agentTeamsSetEnabled(transport.OfficialAgentTeamSetEnabledRequest{Agent: root, Enabled: true}); err != nil {
		t.Fatalf("根会话改 Team 模式应当成功：%v", err)
	}
}

// 会话不存在 → no_such_session（不建、不猜、不 append）。
func TestAgentTeamsSetEnabledRejectsUnknownSession(t *testing.T) {
	f := newAgentAPIFixture(t)
	err := f.core.agentTeamsSetEnabled(transport.OfficialAgentTeamSetEnabledRequest{Agent: "no-such-session", Enabled: true})
	if !errors.Is(err, transport.ErrNoSuchSession) {
		t.Fatalf("未知会话应当是 no_such_session，实际 %v", err)
	}
	if frames := f.journalFrames("no-such-session"); len(frames) != 0 {
		t.Fatalf("未知会话不该有 journal 帧：%+v", frames)
	}
}

// 写入失败**不 append**：v4 打不开写入时，既没有事件也没有 journal 帧——
// 绝不能出现"事件说开了但状态没变"。
func TestAgentTeamsSetEnabledWriteFailureAppendsNothing(t *testing.T) {
	f := newAgentAPIFixture(t)
	root := f.newSession("", nil)

	path, err := f.src.resolve(root)
	if err != nil {
		t.Fatalf("解析会话路径: %v", err)
	}
	// 只读：`OpenSessionAt` 的 O_WRONLY 打不开（Windows 上就是 read-only 属性）。
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatalf("把会话文件设成只读: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	err = f.core.agentTeamsSetEnabled(transport.OfficialAgentTeamSetEnabledRequest{Agent: root, Enabled: true})
	if !errors.Is(err, transport.ErrStatePersistence) {
		t.Fatalf("写入失败应当是 ErrStatePersistence，实际 %v", err)
	}
	if evs := f.teamModeEvents(root); len(evs) != 0 {
		t.Fatalf("写入失败却 append 了事件：%+v", evs)
	}
	if frames := f.journalFrames(root); len(frames) != 0 {
		t.Fatalf("写入失败却推了 journal 帧：%+v", frames)
	}
	if got, err := f.core.agentTeamsIsEnabled(root); err != nil || got {
		t.Fatalf("写入失败后状态必须仍是关，得到 (%v, %v)", got, err)
	}
}

// 历史里读不懂的 `team/mode` 不能当成"没设置过"：那会把用户的开关静默关掉。
func TestAgentTeamsSetEnabledRejectsMalformedTeamModeEvent(t *testing.T) {
	f := newAgentAPIFixture(t)
	root := f.newSession("", func(lg *session.Log) {
		if err := lg.Append(session.EventTeamMode, map[string]any{"enabled": "yes"}); err != nil {
			t.Fatalf("写坏载荷: %v", err)
		}
	})
	err := f.core.agentTeamsSetEnabled(transport.OfficialAgentTeamSetEnabledRequest{Agent: root, Enabled: true})
	if !errors.Is(err, transport.ErrStatePersistence) {
		t.Fatalf("坏载荷应当响亮 ErrStatePersistence，实际 %v", err)
	}
}

// 真 HTTP 验收：`session.history` 读到 `team/mode` 原始帧，`session.projections`
// 折出 `teamMode`（官方 Team 开关的 GUI 依赖）。
func TestAgentTeamsSetEnabledSessionHistoryAndProjectionsReadTeamMode(t *testing.T) {
	f := newAgentAPIFixture(t)
	root := f.newSession("", nil)
	if err := f.core.agentTeamsSetEnabled(transport.OfficialAgentTeamSetEnabledRequest{Agent: root, Enabled: true}); err != nil {
		t.Fatalf("开 Team: %v", err)
	}

	srv := startTeamModeHTTPServer(t, f.home, f.journals)
	defer srv.close()

	// `session.history`：原始帧，seq 与 journal 同一份（不是 v4 seq）。
	var history transport.HistoryResult
	raw := callW145HTTP(t, srv, "session.history", map[string]any{"id": root, "sinceSeq": 0}, http.StatusOK)
	if err := json.Unmarshal(raw, &history); err != nil {
		t.Fatalf("解 session.history: %v", err)
	}
	if len(history.Frames) != 1 || history.Frames[0].Type != "team/mode" || history.Frames[0].Seq != 1 {
		t.Fatalf("session.history 帧 = %+v，want 一条 team/mode seq=1", history.Frames)
	}
	if history.LastSeq != 1 || history.JournalEpoch == 0 {
		t.Fatalf("session.history 水位 = %+v，want lastSeq=1、epoch 非零", history)
	}

	// `session.projections`：GUI 读的那个键。
	var projections struct {
		AsOfSeq int64                      `json:"asOfSeq"`
		Values  map[string]json.RawMessage `json:"values"`
	}
	raw = callW145HTTP(t, srv, "session.projections", map[string]any{"sessionId": root}, http.StatusOK)
	if err := json.Unmarshal(raw, &projections); err != nil {
		t.Fatalf("解 session.projections: %v", err)
	}
	teamMode, ok := projections.Values["teamMode"]
	if !ok {
		t.Fatalf("session.projections 没有 teamMode 键（GUI 官方 Team 开关不会渲染）：%+v", projections.Values)
	}
	var mode struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(teamMode, &mode); err != nil || !mode.Enabled {
		t.Fatalf("teamMode 投影 = %s（err=%v），want {\"enabled\":true}", teamMode, err)
	}
}

// startTeamModeHTTPServer 起一个真 HTTP server（同一份 home + **同一份** journal 表），
// 并挂上官方会话宿主——`session.history` / `session.projections` 走真 handler。
func startTeamModeHTTPServer(t *testing.T, home string, journals *transport.Journals) w145HTTPServer {
	t.Helper()
	opts := withOfficialSessionOptions(home, transport.Options{
		Source:   newSessionSource(home, ""),
		List:     func() ([]transport.SessionSummary, error) { return listSessions(home, "") },
		Journals: journals,
	}, nil)
	srv, err := transport.NewServer(opts)
	if err != nil {
		t.Fatalf("建 server: %v", err)
	}
	base, err := srv.Start()
	if err != nil {
		_ = srv.Close()
		t.Fatalf("起 server: %v", err)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		_ = srv.Close()
		t.Fatalf("cookie jar: %v", err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(base + "/connect?token=" + srv.Token())
	if err != nil {
		_ = srv.Close()
		t.Fatalf("connect: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		_ = srv.Close()
		t.Fatalf("connect status=%d", resp.StatusCode)
	}
	return w145HTTPServer{base: base, client: client, close: srv.Close}
}

// interruptByUser：Gateway 按目标会话授权；不存在/空闲/已完成是 accepted no-op。
func TestAgentTeamInterruptByUser(t *testing.T) {
	f := newAgentAPIFixture(t)
	live := f.newSession("", nil)

	var interrupted []string
	core := newAgentOfficialAPICore(agentOfficialDeps{
		home: f.home, journals: f.journals, src: f.src,
		interrupt: func(sessionID string) error {
			interrupted = append(interrupted, sessionID)
			return nil
		},
	})

	accepted, err := core.agentInterruptByUser(live)
	if err != nil {
		t.Fatalf("真实会话中断: %v", err)
	}
	if !accepted.Accepted {
		t.Fatalf("结果必须是 accepted：%+v", accepted)
	}
	if len(interrupted) != 1 || interrupted[0] != live {
		t.Fatalf("取消信号没打到目标会话：%v", interrupted)
	}

	// 不存在的目标：官方 accepted no-op（信号被接纳，只是没有活的目标）。
	accepted, err = core.agentInterruptByUser("no-such-session")
	if err != nil || !accepted.Accepted {
		t.Fatalf("不存在的目标应当 accepted no-op，得到 (%+v, %v)", accepted, err)
	}

	// 空闲目标（没有在飞的一轮）也是 accepted：ErrNoSuchTurn 不能被渲染成失败。
	idle := newAgentOfficialAPICore(agentOfficialDeps{
		home: f.home, journals: f.journals, src: f.src,
		interrupt: func(string) error { return ErrNoSuchTurn },
	})
	accepted, err = idle.agentInterruptByUser(live)
	if err != nil || !accepted.Accepted {
		t.Fatalf("空闲目标应当 accepted，得到 (%+v, %v)", accepted, err)
	}

	// 取消通道报别的错 → 冒泡（绝不假装 accepted）。
	broken := newAgentOfficialAPICore(agentOfficialDeps{
		home: f.home, journals: f.journals, src: f.src,
		interrupt: func(string) error { return errors.New("取消通道坏了") },
	})
	if _, err := broken.agentInterruptByUser(live); err == nil {
		t.Fatal("取消通道失败必须报错，不能假 accepted")
	}

	// 没有取消通道 → 响亮未实现。
	unwired := newAgentOfficialAPICore(agentOfficialDeps{home: f.home, journals: f.journals, src: f.src})
	if _, err := unwired.agentInterruptByUser(live); !errors.Is(err, transport.ErrOfficialNotImplemented) {
		t.Fatalf("没有取消通道应当 not_implemented，实际 %v", err)
	}
	if _, err := core.agentInterruptByUser("  "); !errors.Is(err, transport.ErrOfficialBadInput) {
		t.Fatalf("空 childSessionId 应当 bad_input，实际 %v", err)
	}
}

// bindTeamRC2 交回的两个方法必须都接上（否则分派面会回 501）。
func TestAgentTeamBindRC2IsWired(t *testing.T) {
	f := newAgentAPIFixture(t)
	core := newAgentOfficialAPICore(agentOfficialDeps{home: f.home, journals: f.journals, src: f.src,
		interrupt: func(string) error { return nil }})
	api := core.bindTeamRC2()
	if api.SetEnabled == nil || api.InterruptByUser == nil {
		t.Fatalf("Team rc.2 方法表没接全：%+v", api)
	}
	agentAPI, teamAPI := newAgentOfficialAPIWithTeamRC2(agentOfficialDeps{
		home: f.home, journals: f.journals, src: f.src, interrupt: func(string) error { return nil },
	})
	if agentAPI.SubagentsPrompt == nil || teamAPI.SetEnabled == nil {
		t.Fatal("newAgentOfficialAPIWithTeamRC2 必须同时交回两面")
	}
}
