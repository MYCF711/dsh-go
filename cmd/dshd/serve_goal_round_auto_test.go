package main

// 自动续轮（官方 goal-round-driver 的 `drive()`）的宿主验收。
//
// 上一组（serve_goal_round_test.go）钉的是"计轮"；这一组钉的是**排轮**：
// 计数涨上去但从不自动起下一轮，goal 就只是个计数器——官方驱动器的全部价值
// 在于它真的替人往下跑，并且在完成/暂停/阻塞/预算用尽/失败时**停得住**。

import (
	"context"
	"errors"
	"sync"
	"testing"

	"dshgo/internal/transport"
)

// goalRoundCall 是一次真实 runner 调用（自动续轮要能从 turnID 与提示词上认出来）。
type goalRoundCall struct {
	TurnID string
	Text   string
}

// goalRoundRecorder 记录每次 runner 调用；写入 v4 的仍是真实会话日志。
type goalRoundRecorder struct {
	mu    sync.Mutex
	calls []goalRoundCall
	// onCall 在**写 v4 之前**跑，用来模拟外部状态变化（pause / 失败）。
	onCall func(index int, sessionID string) error
}

func (r *goalRoundRecorder) runner(home string) turnRunner {
	return turnRunnerFunc(func(_ context.Context, sessionID, turnID, text string) error {
		r.mu.Lock()
		index := len(r.calls)
		r.calls = append(r.calls, goalRoundCall{TurnID: turnID, Text: text})
		hook := r.onCall
		r.mu.Unlock()
		if hook != nil {
			if err := hook(index, sessionID); err != nil {
				return err
			}
		}
		return writeGoalRoundTurn(home, sessionID, text, "completed")
	})
}

func (r *goalRoundRecorder) snapshot() []goalRoundCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]goalRoundCall, len(r.calls))
	copy(out, r.calls)
	return out
}

// setGoalCap 给 fixture 里的目标钉一个真实上限（既有 setGoal 不传上限，落 0）。
func (f *goalRoundFixture) setGoalCap(t *testing.T, sessionID string, cap int) {
	t.Helper()
	f.mutateGoalRecord(t, sessionID, func(record *goalRecord) { record.Goal.MaxGoalRounds = cap })
}

// TestGoalRoundAutoContinuationRunsNextTurn 是这一块的**主红点**：
// 一轮正常收工之后，active + armed 且还有预算的 goal 必须自动排下一轮，
// 直到预算用尽，然后写 `round-limit` blocker 并停。
func TestGoalRoundAutoContinuationRunsNextTurn(t *testing.T) {
	home := t.TempDir()
	const sid = "session-goal-auto"
	createGoalRoundSession(t, home, sid)
	f := newGoalRoundFixture(t, home)
	if err := f.store.Set(sid, "把自动续轮从零实现出来"); err != nil {
		t.Fatalf("设 goal: %v", err)
	}
	f.setGoalCap(t, sid, 3)

	rec := &goalRoundRecorder{}
	p := newPromptService(f.driver.wrap(rec.runner(home)))
	if _, err := p.Submit(context.Background(), sid, "开始"); err != nil {
		t.Fatalf("提交第一轮: %v", err)
	}
	waitGoalRoundIdle(t, p, sid)

	calls := rec.snapshot()
	if len(calls) != 3 {
		t.Fatalf("真实 runner 调用=%d，want 3（上限 3 轮）：%+v", len(calls), calls)
	}
	if got := f.count(t, sid); got != 3 {
		t.Fatalf("roundsStarted=%d，want 3", got)
	}
	// 第一轮是用户那一轮，后两轮是自动续轮：turnID 必须各不相同，
	// 且能看出是从哪一轮接出来的。
	if calls[0].TurnID == calls[1].TurnID || calls[1].TurnID == calls[2].TurnID {
		t.Fatalf("自动续轮复用了 turnID：%+v", calls)
	}
	if calls[1].TurnID != goalRoundTurnID(calls[0].TurnID, 2) {
		t.Fatalf("第 2 轮 turnID=%q，want %q", calls[1].TurnID, goalRoundTurnID(calls[0].TurnID, 2))
	}
	if rounds := goalRoundFrames(f.pushed); len(rounds) != 3 {
		t.Fatalf("续轮帧=%d，want 3", len(rounds))
	}
	for i, frame := range goalRoundFrames(f.pushed) {
		roundFrame(t, frame, i+1)
	}

	// 预算用尽 → 写 round-limit blocker（官方 code 与文案逐字）。
	result, err := f.store.Snapshot(sid)
	if err != nil {
		t.Fatalf("读 goal: %v", err)
	}
	if result.Goal.Phase != "blocked" {
		t.Fatalf("轮次用尽后 phase=%q，want blocked", result.Goal.Phase)
	}
	if result.Goal.BlockedReason == nil ||
		result.Goal.BlockedReason.Code != "round-limit" ||
		result.Goal.BlockedReason.Message != "Goal reached its configured limit of 3 rounds." {
		t.Fatalf("blocker=%+v，want 官方 round-limit 文案", result.Goal.BlockedReason)
	}

	// blocked 之后不再自动续轮：再交一轮只跑用户那一轮。
	before := len(rec.snapshot())
	if _, err := p.Submit(context.Background(), sid, "再来一轮"); err != nil {
		t.Fatalf("blocked 后提交: %v", err)
	}
	waitGoalRoundIdle(t, p, sid)
	if got := len(rec.snapshot()); got != before+1 {
		t.Fatalf("blocked 之后还在自动续轮：%d → %d", before, got)
	}
}

// TestGoalRoundContinuationPromptIsOfficial 钉住提示词本身：
// 官方 `renderGoalRoundPrompt` 的逐字形状（JSON 引号的目标 + round/cap）。
func TestGoalRoundContinuationPromptIsOfficial(t *testing.T) {
	home := t.TempDir()
	const sid = "session-goal-prompt"
	createGoalRoundSession(t, home, sid)
	f := newGoalRoundFixture(t, home)
	const objective = "把 \"引号\" 与\n换行都保住"
	if err := f.store.Set(sid, objective); err != nil {
		t.Fatalf("设 goal: %v", err)
	}
	f.setGoalCap(t, sid, 2)

	rec := &goalRoundRecorder{}
	p := newPromptService(f.driver.wrap(rec.runner(home)))
	if _, err := p.Submit(context.Background(), sid, "开始"); err != nil {
		t.Fatalf("提交: %v", err)
	}
	waitGoalRoundIdle(t, p, sid)

	calls := rec.snapshot()
	if len(calls) != 2 {
		t.Fatalf("runner 调用=%d，want 2", len(calls))
	}
	wantPrefix := "<goal_round>\nObjective: \"把 \\\"引号\\\" 与\\n换行都保住\"\nRound: 2/2\n\n"
	if !hasPrefix(calls[1].Text, wantPrefix) {
		t.Fatalf("第 2 轮提示词前缀=%q，want %q", calls[1].Text, wantPrefix)
	}
	// 直接对渲染函数钉一次：模板不能漂。
	rendered := renderGoalRoundPrompt(&transport.GoalSnapshot{Objective: objective, MaxGoalRounds: 2}, 2)
	if rendered != calls[1].Text {
		t.Fatalf("渲染函数与真实提示词不一致：\n渲染=%q\n实际=%q", rendered, calls[1].Text)
	}
}

// hasPrefix 是 strings.HasPrefix 的薄包装（这里显式写出来，免得为了一个前缀
// 断言把 strings 混进这一组测试的 import 里）。
func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// TestGoalRoundFailureStopsWithoutSpin 钉住"失败不空转"：
// 自动轮失败 → 停 + 撤资格，绝不拿同一轮无限重跑。
func TestGoalRoundFailureStopsWithoutSpin(t *testing.T) {
	home := t.TempDir()
	const sid = "session-goal-nospin"
	createGoalRoundSession(t, home, sid)
	f := newGoalRoundFixture(t, home)
	if err := f.store.Set(sid, "自动轮失败要停"); err != nil {
		t.Fatalf("设 goal: %v", err)
	}
	f.setGoalCap(t, sid, 100)

	rec := &goalRoundRecorder{onCall: func(index int, _ string) error {
		if index == 1 {
			return errors.New("自动轮模型连不上")
		}
		return nil
	}}
	p := newPromptService(f.driver.wrap(rec.runner(home)))
	if _, err := p.Submit(context.Background(), sid, "开始"); err != nil {
		t.Fatalf("提交: %v", err)
	}
	waitGoalRoundIdle(t, p, sid)

	if got := len(rec.snapshot()); got != 2 {
		t.Fatalf("runner 调用=%d，want 2（第 2 轮失败后必须停）", got)
	}
	if got := f.count(t, sid); got != 1 {
		t.Fatalf("roundsStarted=%d，want 1（失败那一轮不算）", got)
	}
	// 撤资格之后，即使再跑一轮也不自动续（官方 agent/error → disarm）。
	before := len(rec.snapshot())
	if _, err := p.Submit(context.Background(), sid, "再来一轮"); err != nil {
		t.Fatalf("提交: %v", err)
	}
	waitGoalRoundIdle(t, p, sid)
	if got := len(rec.snapshot()); got != before+1 {
		t.Fatalf("失败之后还在自动续轮：%d → %d", before, got)
	}
}

// TestGoalRoundMaxTokensDisarms 钉住官方 stop 条件：
// 被 max tokens 截断收工 → 撤资格 + 不排下一轮（不拿半截回答去接）。
func TestGoalRoundMaxTokensDisarms(t *testing.T) {
	home := t.TempDir()
	const sid = "session-goal-maxtokens"
	createGoalRoundSession(t, home, sid)
	f := newGoalRoundFixture(t, home)
	if err := f.store.Set(sid, "截断不续轮"); err != nil {
		t.Fatalf("设 goal: %v", err)
	}
	f.setGoalCap(t, sid, 5)

	ran := 0
	truncated := f.driver.wrap(turnRunnerFunc(func(_ context.Context, sessionID, _, text string) error {
		ran++
		return writeGoalRoundTurn(home, sessionID, text, "max-tokens")
	}))
	p := newPromptService(truncated)
	if _, err := p.Submit(context.Background(), sid, "截断的一轮"); err != nil {
		t.Fatalf("提交: %v", err)
	}
	waitGoalRoundIdle(t, p, sid)

	if ran != 1 {
		t.Fatalf("max-tokens 之后又跑了 %d 轮，want 1", ran)
	}
	if got := f.count(t, sid); got != 0 {
		t.Fatalf("max-tokens 轮次 roundsStarted=%d，want 0", got)
	}
	// 资格已撤：下一轮（哪怕正常完成）也不续。
	if _, err := p.Submit(context.Background(), sid, "正常的一轮"); err != nil {
		t.Fatalf("提交: %v", err)
	}
	waitGoalRoundIdle(t, p, sid)
	if ran != 2 {
		t.Fatalf("撤资格之后还在续轮：%d", ran)
	}
}

// TestGoalRoundMountNeverArms 钉住官方 "mounting the driver over an existing
// agent never arms a goal"：重启后挂载看到的 active goal 一律 disarmed，
// 必须由人显式 resume 才重新自动续轮。
func TestGoalRoundMountNeverArms(t *testing.T) {
	home := t.TempDir()
	const sid = "session-goal-mount"
	createGoalRoundSession(t, home, sid)
	f := newGoalRoundFixture(t, home)
	if err := f.store.Set(sid, "重启不许自动复活"); err != nil {
		t.Fatalf("设 goal: %v", err)
	}
	f.setGoalCap(t, sid, 3)
	// 挂载时重算一次：这就是 serve 启动时做的事。
	if err := f.driver.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	rec := &goalRoundRecorder{}
	p := newPromptService(f.driver.wrap(rec.runner(home)))
	if _, err := p.Submit(context.Background(), sid, "挂载后的第一轮"); err != nil {
		t.Fatalf("提交: %v", err)
	}
	waitGoalRoundIdle(t, p, sid)
	if got := len(rec.snapshot()); got != 1 {
		t.Fatalf("挂载后自动续了 %d 轮，want 1（挂载从不 arm）", got)
	}

	// 人显式 resume 之后才重新自动续轮。
	if err := f.store.Pause(sid); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if err := f.store.Resume(sid); err != nil {
		t.Fatalf("resume: %v", err)
	}
	// 上限 3：resume 那一轮算第 2 轮，自动续到第 3 轮就写 round-limit 停下。
	if _, err := p.Submit(context.Background(), sid, "resume 之后的一轮"); err != nil {
		t.Fatalf("提交: %v", err)
	}
	waitGoalRoundIdle(t, p, sid)
	if got := len(rec.snapshot()); got != 3 {
		t.Fatalf("resume 之后应当继续续轮到上限：调用=%d，want 3", got)
	}
	result, err := f.store.Snapshot(sid)
	if err != nil {
		t.Fatalf("读 goal: %v", err)
	}
	if result.Goal.Phase != "blocked" || result.Goal.BlockedReason == nil ||
		result.Goal.BlockedReason.Code != "round-limit" {
		t.Fatalf("resume 后跑到上限应当写 round-limit blocker：%+v", result.Goal)
	}
}

// TestGoalRoundPauseDuringContinuationStops 钉住"暂停抑制续轮"：
// 自动轮里被人 pause 掉，下一轮就不许再排。
func TestGoalRoundPauseDuringContinuationStops(t *testing.T) {
	home := t.TempDir()
	const sid = "session-goal-pause-mid"
	createGoalRoundSession(t, home, sid)
	f := newGoalRoundFixture(t, home)
	if err := f.store.Set(sid, "自动轮里暂停就停"); err != nil {
		t.Fatalf("设 goal: %v", err)
	}
	f.setGoalCap(t, sid, 5)

	rec := &goalRoundRecorder{onCall: func(index int, sessionID string) error {
		if index == 1 {
			// 第 2 轮（自动轮）开始时被人暂停。
			return f.store.Pause(sessionID)
		}
		return nil
	}}
	p := newPromptService(f.driver.wrap(rec.runner(home)))
	if _, err := p.Submit(context.Background(), sid, "开始"); err != nil {
		t.Fatalf("提交: %v", err)
	}
	waitGoalRoundIdle(t, p, sid)

	if got := len(rec.snapshot()); got != 2 {
		t.Fatalf("runner 调用=%d，want 2（pause 之后必须停）", got)
	}
	result, err := f.store.Snapshot(sid)
	if err != nil {
		t.Fatalf("读 goal: %v", err)
	}
	if result.Goal.Phase != "paused" {
		t.Fatalf("phase=%q，want paused", result.Goal.Phase)
	}
}

// TestGoalRoundUncappedGoalDoesNotAutoContinue 钉住保守口径：
// 本宿主 legacy `session.setGoal` 落 maxGoalRounds=0（= 未配置上限）。
// "没有上限"不能等于"无限自动跑"——所以不自动续轮，只计轮。
func TestGoalRoundUncappedGoalDoesNotAutoContinue(t *testing.T) {
	home := t.TempDir()
	const sid = "session-goal-uncapped"
	createGoalRoundSession(t, home, sid)
	f := newGoalRoundFixture(t, home)
	if err := f.store.Set(sid, "没有上限就只计轮"); err != nil {
		t.Fatalf("设 goal: %v", err)
	}
	rec := &goalRoundRecorder{}
	p := newPromptService(f.driver.wrap(rec.runner(home)))
	if _, err := p.Submit(context.Background(), sid, "一轮"); err != nil {
		t.Fatalf("提交: %v", err)
	}
	waitGoalRoundIdle(t, p, sid)

	if got := len(rec.snapshot()); got != 1 {
		t.Fatalf("未配置上限也自动续轮了：%d 轮（会无限跑）", got)
	}
	if got := f.count(t, sid); got != 1 {
		t.Fatalf("roundsStarted=%d，want 1（计轮不受影响）", got)
	}
}

// TestGoalRoundResumeRejectsExhaustedBudget 钉住 store 的 resume 预算判据
// （官方：`roundsStarted >= maxGoalRounds` 时拒绝 resume）。
func TestGoalRoundResumeRejectsExhaustedBudget(t *testing.T) {
	home := t.TempDir()
	const sid = "session-goal-budget"
	createGoalRoundSession(t, home, sid)
	f := newGoalRoundFixture(t, home)
	if err := f.store.Set(sid, "预算用尽不许 resume"); err != nil {
		t.Fatalf("设 goal: %v", err)
	}
	f.setGoalCap(t, sid, 1)
	if err := f.store.Pause(sid); err != nil {
		t.Fatalf("pause: %v", err)
	}
	// 把已用轮次推满上限。
	f.mutateGoalRecord(t, sid, func(record *goalRecord) {
		one := 1
		record.RoundsStarted = &one
	})

	err := f.store.Resume(sid)
	if !errors.Is(err, transport.ErrGoalTransition) {
		t.Fatalf("预算用尽 resume 的错误=%v，want ErrGoalTransition", err)
	}
}
