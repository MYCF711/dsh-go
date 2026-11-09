package commands

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"dshgo/internal/transport"
)

// 这一组测试钉住 `/goal` 的生命周期命令：
//   - 每个子命令都**真的**调用了 GoalStore 上对应的那条迁移（不是回一句"好的"）；
//   - 业务失败（缺参数 / 状态迁移不允许）结算成 `kind:"error"` 的**结果**，
//     存储故障才上抛（5xx），两者不能混；
//   - 装进真实 `transport.OfficialCommandRegistry` 之后，
//     register / list / execute / 生命周期 settled 事件（command/run + command/done）
//     走的是同一条真通道。

// fakeGoalStore 记录每次调用，并可以按需注入错误。
type fakeGoalStore struct {
	goal transport.GoalResult

	calls  []string
	setArg string
	// err 非 nil 时每个迁移都返回它。
	err error
}

func (f *fakeGoalStore) record(op string) error {
	f.calls = append(f.calls, op)
	return f.err
}

func (f *fakeGoalStore) Snapshot(string) (transport.GoalResult, error) {
	f.calls = append(f.calls, "snapshot")
	return f.goal, f.err
}

func (f *fakeGoalStore) Set(_ string, objective string) error {
	f.setArg = objective
	return f.record("set")
}

func (f *fakeGoalStore) Pause(string) error    { return f.record("pause") }
func (f *fakeGoalStore) Resume(string) error   { return f.record("resume") }
func (f *fakeGoalStore) Complete(string) error { return f.record("complete") }
func (f *fakeGoalStore) Clear(string) error    { return f.record("clear") }
func (f *fakeGoalStore) Block(_, code, message string) error {
	f.setArg = code + "|" + message
	return f.record("block")
}

func newGoalDefinition(t *testing.T, store GoalStore) transport.OfficialCommandDefinition {
	t.Helper()
	def, err := GoalDefinition(store)
	if err != nil {
		t.Fatalf("GoalDefinition: %v", err)
	}
	return def
}

func runGoalCommand(t *testing.T, store GoalStore, raw string) transport.OfficialCommandOutcome {
	t.Helper()
	def := newGoalDefinition(t, store)
	out, err := def.Handler(context.Background(), transport.OfficialCommandInvocation{
		CommandID: "cmd-test-1", SessionID: "session-a", RawInput: raw,
	})
	if err != nil {
		t.Fatalf("/goal %q 返回 transport 错误（业务失败必须是 error 结果）：%v", raw, err)
	}
	if out.Kind != "success" && out.Kind != "error" {
		t.Fatalf("/goal %q 的 result kind=%q，只取 success/error", raw, out.Kind)
	}
	return out
}

// TestGoalCommandDefinitionIsReal 钉住定义本身：名字/描述/输入提示/handler 都在，
// 且名字是官方字符集（不带斜杠）。
func TestGoalCommandDefinitionIsReal(t *testing.T) {
	def := newGoalDefinition(t, &fakeGoalStore{})
	if def.Name != CommandGoal || def.DefinitionID != CommandGoal {
		t.Fatalf("命令名=%q definitionID=%q，want %q", def.Name, def.DefinitionID, CommandGoal)
	}
	if strings.TrimSpace(def.Description) == "" {
		t.Fatal("description 不能为空（发现界面要显示它）")
	}
	if def.Input == nil || strings.TrimSpace(def.Input.Hint) == "" {
		t.Fatalf("input.hint 缺失：%+v", def.Input)
	}
	if def.Handler == nil {
		t.Fatal("handler 为 nil：注册了也不能执行")
	}
	if _, err := GoalDefinition(nil); err == nil {
		t.Fatal("没有 goal 存储竟然装配成功了")
	}
}

// TestGoalCommandStatusIsReadOnly 钉住默认形态（老 `/goal` 行为逐字保留）：
// 无目标回"没有 goal"，有目标回 id/phase/objective/轮次。
func TestGoalCommandStatusIsReadOnly(t *testing.T) {
	store := &fakeGoalStore{}
	out := runGoalCommand(t, store, "")
	if out.Kind != "success" || out.Text != "本会话没有 goal。" {
		t.Fatalf("无目标时 /goal=%+v", out)
	}

	rounds := 2
	blocked := &transport.GoalBlockedReason{Code: "round-limit", Message: "用尽 2 轮"}
	store.goal = transport.GoalResult{
		Goal: &transport.GoalSnapshot{
			ID: "goal-1", Revision: 3, Objective: "把 X 做完", Phase: "blocked",
			MaxGoalRounds: 5, BlockedReason: blocked,
		},
		RoundsStarted: &rounds,
	}
	out = runGoalCommand(t, store, "status")
	for _, want := range []string{"goal-1", "blocked", "把 X 做完", "轮次：2", "上限 5", "用尽 2 轮", "round-limit"} {
		if !strings.Contains(out.Text, want) {
			t.Fatalf("status 文本缺 %q：%s", want, out.Text)
		}
	}
	// 只读：status 不许碰任何写迁移。
	for _, call := range store.calls {
		if call != "snapshot" {
			t.Fatalf("status 调了写迁移：%v", store.calls)
		}
	}
}

// TestGoalCommandLifecycleTransitions 钉住每条写子命令都落到真实存储方法。
func TestGoalCommandLifecycleTransitions(t *testing.T) {
	cases := []struct {
		raw     string
		wantOp  string
		wantArg string
	}{
		{"set 把 X 做完并跑通测试", "set", "把 X 做完并跑通测试"},
		{"  SET   带前后空格  ", "set", "带前后空格"},
		{"pause", "pause", ""},
		{"resume", "resume", ""},
		{"complete", "complete", ""},
		{"clear", "clear", ""},
		{"block 等 CI 权限", "block", "user-blocked|等 CI 权限"},
	}
	for _, tc := range cases {
		store := &fakeGoalStore{}
		out := runGoalCommand(t, store, tc.raw)
		if out.Kind != "success" {
			t.Fatalf("/goal %q 失败：%+v", tc.raw, out)
		}
		if len(store.calls) != 1 || store.calls[0] != tc.wantOp {
			t.Fatalf("/goal %q 实际调用=%v，want [%s]", tc.raw, store.calls, tc.wantOp)
		}
		if tc.wantArg != "" && store.setArg != tc.wantArg {
			t.Fatalf("/goal %q 参数=%q，want %q", tc.raw, store.setArg, tc.wantArg)
		}
	}
}

// TestGoalCommandRejectsBadInput 钉住业务失败：缺参数 / 未知子命令都是
// `kind:"error"` 的结果（HTTP 200 + durable done 帧），不是 5xx。
func TestGoalCommandRejectsBadInput(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"set", "需要目标正文"},
		{"set    ", "需要目标正文"},
		{"block", "需要一句具体障碍"},
		{"frobnicate", "未知的 /goal 子命令"},
		{"p", "未知的 /goal 子命令"},
	}
	for _, tc := range cases {
		store := &fakeGoalStore{}
		out := runGoalCommand(t, store, tc.raw)
		if out.Kind != "error" {
			t.Fatalf("/goal %q 应当结算成 error，实际 %+v", tc.raw, out)
		}
		if !strings.Contains(out.Text, tc.want) {
			t.Fatalf("/goal %q 错误文本=%q，want 含 %q", tc.raw, out.Text, tc.want)
		}
		if len(store.calls) != 0 {
			t.Fatalf("/goal %q 不该碰存储：%v", tc.raw, store.calls)
		}
	}
}

// TestGoalCommandObjectiveBound 钉住上界：目标会原样回给模型，无界输入等于
// 让人往上下文里塞任意长度的正文。
func TestGoalCommandObjectiveBound(t *testing.T) {
	store := &fakeGoalStore{}
	out := runGoalCommand(t, store, "set "+strings.Repeat("长", maxGoalObjectiveRunes+1))
	if out.Kind != "error" || !strings.Contains(out.Text, "上界") {
		t.Fatalf("超长目标=%+v", out)
	}
	if len(store.calls) != 0 {
		t.Fatalf("超长目标不该落盘：%v", store.calls)
	}
}

// TestGoalCommandTransitionErrorIsResult 钉住两类错误的边界：
// 状态迁移错误 → error 结果；存储故障 → 上抛（5xx）。
func TestGoalCommandTransitionErrorIsResult(t *testing.T) {
	store := &fakeGoalStore{err: fmt.Errorf("%w: 只有 active goal 才能 pause", transport.ErrGoalTransition)}
	out := runGoalCommand(t, store, "pause")
	if out.Kind != "error" || !strings.Contains(out.Text, "只有 active goal 才能 pause") {
		t.Fatalf("迁移错误=%+v", out)
	}

	store = &fakeGoalStore{err: fmt.Errorf("%w: 磁盘只读", transport.ErrStatePersistence)}
	def := newGoalDefinition(t, store)
	if _, err := def.Handler(context.Background(), transport.OfficialCommandInvocation{
		CommandID: "cmd-test-1", SessionID: "session-a", RawInput: "pause",
	}); err == nil {
		t.Fatal("存储故障竟然被吞成了成功结果")
	}
}

// TestCommandRegistryRegisterListExecute 走**真实**注册表：
// register → list（升序描述符）→ execute（command/run + command/done 成对），
// 这条链路就是官方 `commands.*` 的 L1 投影。
func TestCommandRegistryRegisterListExecute(t *testing.T) {
	type lifecycle struct {
		eventType string
		data      map[string]any
	}
	var frames []lifecycle
	registry := transport.NewOfficialCommandRegistry(transport.OfficialCommandRegistryOptions{
		InstanceToken: "test",
		Lifecycle: func(sessionID, eventType string, data any) (int64, error) {
			payload, _ := data.(map[string]any)
			frames = append(frames, lifecycle{eventType: eventType, data: payload})
			return int64(len(frames)), nil
		},
	})

	store := &fakeGoalStore{}
	def := newGoalDefinition(t, store)
	unregister, err := registry.Register(def)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	list := registry.List()
	if len(list) != 1 || list[0].Name != CommandGoal {
		t.Fatalf("List=%+v", list)
	}
	if list[0].Input == nil || list[0].Input.Hint == "" {
		t.Fatalf("List 丢了 input 声明：%+v", list[0])
	}

	execution, err := registry.Execute(context.Background(), "session-a", "/goal set 把 X 做完", nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if execution == nil || execution.CommandID == "" {
		t.Fatalf("Execute 回执=%+v", execution)
	}
	if execution.Result.Kind != "success" {
		t.Fatalf("result=%+v", execution.Result)
	}
	if len(store.calls) != 1 || store.calls[0] != "set" {
		t.Fatalf("命令没落到存储：%v", store.calls)
	}
	// 生命周期成对：先 run 后 done，且 done 带 kind。
	if len(frames) != 2 {
		t.Fatalf("生命周期帧=%+v", frames)
	}
	if frames[0].eventType != "command/run" || frames[0].data["name"] != CommandGoal {
		t.Fatalf("run 帧=%+v", frames[0])
	}
	if frames[1].eventType != "command/done" || frames[1].data["kind"] != "success" {
		t.Fatalf("done 帧=%+v", frames[1])
	}
	if frames[0].data["commandId"] != frames[1].data["commandId"] {
		t.Fatalf("run/done 的 commandId 没配对：%+v vs %+v", frames[0], frames[1])
	}

	// 语法/名字不解析 → 官方 undefined（(nil,nil)），且**什么都不写**。
	before := len(frames)
	for _, line := range []string{"goal pause", "/nope", "/goalx pause", "/"} {
		execution, err := registry.Execute(context.Background(), "session-a", line, nil)
		if err != nil || execution != nil {
			t.Fatalf("%q 应当 undefined，实际 %+v %v", line, execution, err)
		}
	}
	if len(frames) != before {
		t.Fatalf("未解析的命令写了生命周期帧：%d → %d", before, len(frames))
	}

	// 注销之后不再可执行：只删自己那一份。
	unregister()
	if got := registry.List(); len(got) != 0 {
		t.Fatalf("注销后 List=%+v", got)
	}
	if execution, err := registry.Execute(context.Background(), "session-a", "/goal", nil); err != nil || execution != nil {
		t.Fatalf("注销后仍然执行了：%+v %v", execution, err)
	}
}
