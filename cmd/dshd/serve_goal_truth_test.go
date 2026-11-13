package main

// HS-GOAL-TRUTH 的真链探针：**模型用 create_goal 建的目标必须进自动续轮**。
//
// 先红证据见 RESULT.md：同一条探针在修复前 exit 1（`create_goal` 成功，但
// serve 的 goal store 里没有这个会话，驱动判 goalStop）。它不是"再造一个假
// store"，而是把两条生产路径**原样**跑一遍：
//  1. 模型侧：`builtinModules` 真装配出来的 `create_goal` 工具（serve 的子进程
//     跑的就是这张表），按线上调用形状 `reg.Get("create_goal").Exec(spi.WithSession(...))`；
//  2. 宿主侧：serve 父进程的 `goalOverrideStore` + `goalRoundDriver`（官方
//     goal-round-driver 的落点），喂一轮真实 v4 `turn/end{completed}`。
//
// 双真值存在时两条路径各写各的文件；单真值之后工具写的必须就是驱动读的那一份。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"dshgo/internal/agent"
	"dshgo/internal/kernel"
	"dshgo/internal/spi"
	"dshgo/internal/transport"
	goalmod "dshgo/modules/tool/goal"
)

// goalTruthCreateViaModelTool 用**生产装配**跑一次 create_goal，返回工具回包里的目标。
//
// 为什么不用 `&goal.Tool{}` 直接构造：那绕过了 kernel 装配与工具表，
// 也就绕过了 main.go 传给模块的目录——而那正是本缺陷的所在。
func goalTruthCreateViaModelTool(t *testing.T, home, sessionID, objective string, maxRounds int) *goalmod.Goal {
	t.Helper()
	k := kernel.New("goal-truth-probe")
	defer k.Root().Dispose()
	// 与 main.go 的接线逐字一致：整张 builtinModules 表，goal 模块拿
	// `<home>/state/goals`（`builtinModules` 里的那一行）。
	if _, err := kernel.Assemble(k.Root(), builtinModules(".", home, 0, nil)); err != nil {
		t.Fatalf("装配 builtinModules: %v", err)
	}
	reg, err := kernel.Inject(k.Root(), spi.ToolsKey)
	if err != nil {
		t.Fatalf("取工具表: %v", err)
	}
	tool, ok := reg.Get("create_goal")
	if !ok {
		t.Fatal("工具表里没有 create_goal")
	}
	args, err := json.Marshal(map[string]any{"objective": objective, "max_goal_rounds": maxRounds})
	if err != nil {
		t.Fatal(err)
	}
	res, err := tool.Exec(spi.WithSession(context.Background(), sessionID), args)
	if err != nil {
		t.Fatalf("create_goal: %v", err)
	}
	m, _ := res.Data.(map[string]any)
	created, ok := m["goal"].(*goalmod.Goal)
	if !ok || created == nil {
		t.Fatalf("create_goal 没回 goal：%#v", res.Data)
	}
	return created
}

// TestGoalTruthModelCreatedGoalReachesRoundDriver 是 HS-GOAL-TRUTH 的主探针。
//
// 绿：`create_goal` 建出来的目标**就是** serve 的 goal store 里那一条
// （id/revision/objective 全等），驱动在该会话正常收工之后自动排下一轮。
func TestGoalTruthModelCreatedGoalReachesRoundDriver(t *testing.T) {
	home := t.TempDir()
	sessionID := "goal-truth-session-a"
	createGoalRoundSession(t, home, sessionID)

	created := goalTruthCreateViaModelTool(t, home, sessionID, "把 goal 单真值做完并跑通测试", 3)
	t.Logf("模型建出的目标：id=%s revision=%d phase=%s maxRounds=%d",
		created.ID, created.Revision, created.Phase, created.MaxGoalRounds)

	// 旧双真值目录必须**不再被写**：模型侧唯一落点就是规范文档。
	if _, err := os.Stat(filepath.Join(home, "state", "goals", sessionID+".json")); !os.IsNotExist(err) {
		t.Fatalf("旧 state/goals 目录又被写了（双真值复活）：%v", err)
	}

	// 宿主侧：serve 父进程的 store + 官方 goal-round-driver。
	store := newGoalOverrideStore(home)
	if err := store.Load(); err != nil {
		t.Fatalf("读 goal store: %v", err)
	}
	// serve.go 的接线：goal/change 与 WS/history 共用同一张 journal。
	journals := transport.NewJournals(nil)
	var pushed []transport.ServerFrame
	store.journal = &goalChangeJournal{journals: journals, pub: func(_ string, frame transport.ServerFrame) {
		pushed = append(pushed, frame)
	}}
	snapshot, err := store.Snapshot(sessionID)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapshot.Goal == nil {
		t.Fatalf("模型建的目标 %q 在 serve 的 goal store 里不存在（双真值）", created.ID)
	}
	if snapshot.Goal.ID != created.ID || snapshot.Goal.Revision != created.Revision ||
		snapshot.Goal.Objective != created.Objective || snapshot.Goal.MaxGoalRounds != created.MaxGoalRounds {
		t.Fatalf("两条路径的快照分叉：工具=%+v 驱动=%+v", created, snapshot.Goal)
	}

	// 喂一轮真实 v4 turn/end{completed}，再问驱动"要不要续轮"。
	// 活体状态与生产一致：`goalRoundRunner.Run` 在轮次前后各登记一次
	// （驱动只在 whole-agent idle 时排下一轮）。
	driver := newGoalRoundDriver(store, home, agent.NewRegistry(agent.RegistryOptions{}))
	if err := driver.Load(); err != nil {
		t.Fatalf("读游标: %v", err)
	}
	driver.agentStarted(sessionID)
	if err := writeGoalRoundTurn(home, sessionID, "第一轮", "completed"); err != nil {
		t.Fatalf("写 v4 轮次: %v", err)
	}
	driver.agentSettled(sessionID)
	if err := driver.Advance(sessionID); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	decision, cont, err := driver.decideAfterTurn(sessionID)
	if err != nil {
		t.Fatalf("decideAfterTurn: %v", err)
	}
	if decision != goalContinue {
		t.Fatalf("模型建的目标没有自动续轮（decision=%v，期望 goalContinue）", decision)
	}
	// 第 1 轮是用户那一轮（已收工），自动续轮从第 2 轮开始。
	if cont.Round != 2 || cont.Prompt == "" {
		t.Fatalf("续轮内容不成立：%+v", cont)
	}
	// 续轮提示词里的目标必须逐字来自模型建的那一条（JSON 引号形状）。
	if want := renderGoalRoundPrompt(snapshot.Goal, 2); cont.Prompt != want {
		t.Fatalf("续轮提示词不是官方形状：\n got=%q\nwant=%q", cont.Prompt, want)
	}

	// 轮次推进要落回同一份文档：`goals.get` 与 `get_goal` 看到同一个 roundsStarted。
	after, err := store.Snapshot(sessionID)
	if err != nil {
		t.Fatalf("推进后 Snapshot: %v", err)
	}
	if after.RoundsStarted == nil || *after.RoundsStarted != 1 {
		t.Fatalf("roundsStarted=%v，want 1", after.RoundsStarted)
	}
	if after.Goal.Revision != created.Revision {
		t.Fatalf("推进轮次改了 revision：%d → %d", created.Revision, after.Goal.Revision)
	}
	if frames := goalRoundFrames(pushed); len(frames) != 1 {
		t.Fatalf("续轮帧=%d，want 1", len(frames))
	}
}
