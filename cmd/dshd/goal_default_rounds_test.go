package main

import (
	"testing"

	"dshgo/internal/transport"
)

// TestGoalDefaultMaxRoundsEnv 钉住官方 `goal.defaultMaxGoalRounds` 的默认值 256
// 与覆盖语义（0 = 显式不限；非法/负数退回官方默认）。
func TestGoalDefaultMaxRoundsEnv(t *testing.T) {
	if DefaultGoalMaxRounds != 256 {
		t.Fatalf("官方默认值必须逐值一致：256，实际 %d", DefaultGoalMaxRounds)
	}
	t.Setenv(EnvGoalDefaultMaxRounds, "")
	if got := goalDefaultMaxRounds(); got != 256 {
		t.Fatalf("默认轮次上限 = %d，期望 256", got)
	}
	t.Setenv(EnvGoalDefaultMaxRounds, "10")
	if got := goalDefaultMaxRounds(); got != 10 {
		t.Fatalf("env 覆盖后 = %d，期望 10", got)
	}
	t.Setenv(EnvGoalDefaultMaxRounds, "0")
	if got := goalDefaultMaxRounds(); got != 0 {
		t.Fatalf("env=0 应当表示显式不限，实际 %d", got)
	}
	t.Setenv(EnvGoalDefaultMaxRounds, "-3")
	if got := goalDefaultMaxRounds(); got != 256 {
		t.Fatalf("负值应退回默认 256，实际 %d", got)
	}
	t.Setenv(EnvGoalDefaultMaxRounds, "bogus")
	if got := goalDefaultMaxRounds(); got != 256 {
		t.Fatalf("非法值应退回默认 256，实际 %d", got)
	}
}

// TestGoalCreateMaterializesDefaultMaxRounds 证明默认值真的落到 create 的存储里：
// 不传 maxGoalRounds 时，落盘记录必须是 256（官方 create 时物化默认值）。
func TestGoalCreateMaterializesDefaultMaxRounds(t *testing.T) {
	t.Setenv(EnvGoalDefaultMaxRounds, "")
	f := newAgentAPIFixture(t)
	sessionID := f.newSession("", nil)

	if _, err := f.api.GoalCreate(sessionID, transport.OfficialGoalCreateRequest{Objective: "官方默认轮次"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	record, ok := f.storedGoal(sessionID)
	if !ok || record.Goal == nil {
		t.Fatalf("goal 没落进 %s", f.goalFile())
	}
	if record.Goal.MaxGoalRounds != DefaultGoalMaxRounds {
		t.Fatalf("create 未传上限时落盘 maxGoalRounds = %d，期望 %d（官方默认）",
			record.Goal.MaxGoalRounds, DefaultGoalMaxRounds)
	}

	// 显式 0 = 退回旧的无上限语义，不能被默认值覆盖。
	sessionID2 := f.newSession("", nil)
	zero := 0
	if _, err := f.api.GoalCreate(sessionID2, transport.OfficialGoalCreateRequest{
		Objective: "显式不限", MaxGoalRounds: &zero,
	}); err != nil {
		t.Fatalf("create(0): %v", err)
	}
	record2, ok := f.storedGoal(sessionID2)
	if !ok || record2.Goal == nil {
		t.Fatalf("第二个 goal 没落盘")
	}
	if record2.Goal.MaxGoalRounds != 0 {
		t.Fatalf("显式传 0 应当保持 0（不限），实际 %d", record2.Goal.MaxGoalRounds)
	}
}
