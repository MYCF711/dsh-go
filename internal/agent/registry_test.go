package agent

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// 这一组测试钉住活体 agent 注册表的真值：
//   - 状态只有 idle ↔ running 两态，边只在**真的变了**的时候发；
//   - 父子地址与委派深度（官方 `resolveChildDepth` 的单调下限 + 上限）；
//   - `subagent/start` / `subagent/end` 成对，`end.stopReason` 只认官方五个值；
//   - 并发迁移不丢状态、不重复发边。
//
// 全部走公开方法，不碰内部 map：注册表对外只有一个真值，测试也不该有第二个。

// recorder 是**串行**的事件记录器：注册表在持锁状态下同步调 Emit，
// 所以这里不需要额外锁就能保证顺序（这正是那个设计约束的意义）。
type recorder struct {
	events []AgentEvent
}

func (r *recorder) emit(ev AgentEvent) { r.events = append(r.events, ev) }

func (r *recorder) kinds() []string {
	out := make([]string, 0, len(r.events))
	for _, ev := range r.events {
		out = append(out, ev.Kind)
	}
	return out
}

func newTestRegistry(t *testing.T) (*Registry, *recorder) {
	t.Helper()
	rec := &recorder{}
	return NewRegistry(RegistryOptions{Emit: rec.emit}), rec
}

// TestAgentRegistryStartStopStatusEvents 是这一块的红点：
// 以前没有活体状态——没人能回答"这个会话现在在跑还是闲"。
func TestAgentRegistryStartStopStatusEvents(t *testing.T) {
	reg, rec := newTestRegistry(t)

	if _, err := reg.Create("session-a", ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := rec.kinds(); len(got) != 1 || got[0] != EventAgentCreated {
		t.Fatalf("创建边=%v，want [agent/created]", got)
	}
	if !reg.Idle("session-a") {
		t.Fatal("刚登记的 agent 应当是 idle")
	}

	running, err := reg.Start("session-a")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if running.Status != AgentRunning {
		t.Fatalf("Start 后状态=%q，want running", running.Status)
	}
	if reg.Idle("session-a") {
		t.Fatal("跑起来之后不该再是 idle")
	}

	// 重复 Start 幂等：状态没变就不再发边（一条 status 边对应一次真实迁移）。
	if _, err := reg.Start("session-a"); err != nil {
		t.Fatalf("重复 Start: %v", err)
	}
	if got := rec.kinds(); len(got) != 2 || got[1] != EventAgentStatus {
		t.Fatalf("重复 Start 多发了边：%v", got)
	}
	if rec.events[1].Status != AgentRunning {
		t.Fatalf("status 边内容=%+v，want running", rec.events[1])
	}

	idle, err := reg.Stop("session-a")
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if idle.Status != AgentIdle {
		t.Fatalf("Stop 后状态=%q，want idle", idle.Status)
	}
	if !reg.Idle("session-a") {
		t.Fatal("收工之后应当是 idle")
	}
	if got := rec.kinds(); len(got) != 3 || got[2] != EventAgentStatus || rec.events[2].Status != AgentIdle {
		t.Fatalf("收工边=%v %+v", got, rec.events)
	}
}

// TestAgentRegistryCreateIdempotentAndUnknownLoud 钉住两条相反的语义：
// 重复登记幂等（同一轮被两个入口各调一次），但收工一个不存在的 agent 必须响亮。
func TestAgentRegistryCreateIdempotentAndUnknownLoud(t *testing.T) {
	reg, rec := newTestRegistry(t)
	first, err := reg.Create("session-a", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	second, err := reg.Create("session-a", "")
	if err != nil {
		t.Fatalf("重复 Create: %v", err)
	}
	if first.SessionID != second.SessionID || first.CreatedAt != second.CreatedAt {
		t.Fatalf("重复登记改了快照：%+v → %+v", first, second)
	}
	if got := rec.kinds(); len(got) != 1 {
		t.Fatalf("重复登记多发了边：%v", got)
	}

	if _, err := reg.Stop("session-nope"); err == nil {
		t.Fatal("收工一个不存在的 agent 竟然成功了：状态会永远歪着")
	}
	if _, err := reg.Create("", ""); err == nil {
		t.Fatal("空 session id 竟然登记成功了")
	}
}

// TestAgentRegistryParentMustExist 钉住授权边界：
// 没有父就没有归属，子会话不许凭空出现（编一个父等于没有归属）。
func TestAgentRegistryParentMustExist(t *testing.T) {
	reg, _ := newTestRegistry(t)
	if _, err := reg.Create("child", "parent-nope"); err == nil {
		t.Fatal("父会话不存在时子会话竟然登记成功了")
	}
	if _, err := reg.Create("self", ""); err != nil {
		t.Fatalf("建根 agent: %v", err)
	}
	if _, err := reg.Create("self", "self"); err == nil {
		t.Fatal("自己当自己的父竟然成功了")
	}
	child, err := reg.Create("child", "self")
	if err != nil {
		t.Fatalf("建子 agent: %v", err)
	}
	if child.Depth != 1 || child.ParentID != "self" {
		t.Fatalf("子 agent 地址=%+v，want depth=1 parent=self", child)
	}
}

// TestAgentRegistryDepthCap 钉住官方 `maxDepth`：
// 超过上限的委派必须失败，且**不能**留下半个 agent。
func TestAgentRegistryDepthCap(t *testing.T) {
	rec := &recorder{}
	reg := NewRegistry(RegistryOptions{Emit: rec.emit, MaxDepth: 1})
	if _, err := reg.Create("root", ""); err != nil {
		t.Fatalf("建根: %v", err)
	}
	if _, err := reg.Create("child", "root"); err != nil {
		t.Fatalf("深度 1 应当允许: %v", err)
	}
	if _, err := reg.Create("grandchild", "child"); err == nil {
		t.Fatal("深度 2 超过上限 1 竟然成功了")
	}
	if _, ok := reg.Get("grandchild"); ok {
		t.Fatal("被拒绝的 agent 不许留在注册表里")
	}
}

// TestAgentRegistrySubagentLifecycle 钉住 `subagent/start` → `subagent/end` 边：
// 地址（runId/provider/id/local）与真实 stopReason 都要原样出现在边上。
func TestAgentRegistrySubagentLifecycle(t *testing.T) {
	reg, rec := newTestRegistry(t)
	if _, err := reg.Create("parent", ""); err != nil {
		t.Fatalf("建父: %v", err)
	}
	run, err := reg.BeginSubagent("parent", "child-1", "group/deepseek-v4.1-flash-1017", "run-1")
	if err != nil {
		t.Fatalf("BeginSubagent: %v", err)
	}
	if run.RunID != "run-1" || run.ChildID != "child-1" || !run.Local {
		t.Fatalf("委派地址=%+v", run)
	}
	// 委派一开始就是 running：子代理在跑，不是闲。
	if status, ok := reg.Status("child-1"); !ok || status != AgentRunning {
		t.Fatalf("子代理状态=%q ok=%v，want running", status, ok)
	}
	start := rec.events[len(rec.events)-1]
	if start.Kind != EventSubagentStart || start.Subagent == nil || start.Subagent.RunID != "run-1" {
		t.Fatalf("start 边=%+v", start)
	}

	settled, err := reg.EndSubagent("run-1", SubagentStopCompleted, "")
	if err != nil {
		t.Fatalf("EndSubagent: %v", err)
	}
	if settled.StopReason != SubagentStopCompleted || settled.ChildID != "child-1" {
		t.Fatalf("end 边=%+v", settled)
	}
	end := rec.events[len(rec.events)-1]
	if end.Kind != EventSubagentEnd || end.Subagent == nil || end.Subagent.StopReason != SubagentStopCompleted {
		t.Fatalf("end 边=%+v", end)
	}
	// 结算后 child 从注册表里消失：停止 = 表里没有。
	if _, ok := reg.Get("child-1"); ok {
		t.Fatal("结算后的子代理还在注册表里")
	}
	// 同一个 run 不能结算两次（否则一次委派会发两条 end 边）。
	if _, err := reg.EndSubagent("run-1", SubagentStopCompleted, ""); err == nil {
		t.Fatal("重复结算同一个 run 竟然成功了")
	}
}

// TestAgentRegistrySubagentStopReasonIsClosed 钉住 stopReason 的封闭词表：
// 编一个新的等于让读方把失败当成功。
func TestAgentRegistrySubagentStopReasonIsClosed(t *testing.T) {
	reg, _ := newTestRegistry(t)
	if _, err := reg.Create("parent", ""); err != nil {
		t.Fatalf("建父: %v", err)
	}
	if _, err := reg.BeginSubagent("parent", "child", "p", "run-x"); err != nil {
		t.Fatalf("BeginSubagent: %v", err)
	}
	if _, err := reg.EndSubagent("run-x", "done", ""); err == nil {
		t.Fatal("非法 stopReason 竟然结算成功了")
	}
	// 失败之后 run 还在（没有结算），必须还能用合法原因结算。
	if _, err := reg.EndSubagent("run-x", SubagentStopError, "模型报错"); err != nil {
		t.Fatalf("合法原因结算失败: %v", err)
	}
	if _, err := reg.BeginSubagent("parent", "child", "p", "run-x"); err != nil {
		t.Fatalf("结算后 run id 应当可以复用: %v", err)
	}
}

// TestAgentRegistryListAndDispose 钉住读取与收尾：
// List 固定升序（断言/日志不因 map 随机性抖动），Dispose 幂等且发一次边。
func TestAgentRegistryListAndDispose(t *testing.T) {
	reg, rec := newTestRegistry(t)
	for _, id := range []string{"b-session", "a-session", "c-session"} {
		if _, err := reg.Create(id, ""); err != nil {
			t.Fatalf("Create(%s): %v", id, err)
		}
	}
	list := reg.List()
	if len(list) != 3 || list[0].SessionID != "a-session" || list[1].SessionID != "b-session" || list[2].SessionID != "c-session" {
		t.Fatalf("List 不是升序：%+v", list)
	}
	before := len(rec.events)
	if err := reg.Dispose("a-session"); err != nil {
		t.Fatalf("Dispose: %v", err)
	}
	if got := rec.kinds(); len(got) != before+1 || got[len(got)-1] != EventAgentDisposed {
		t.Fatalf("dispose 边=%v", got)
	}
	// 幂等：重复收尾不再发边。
	if err := reg.Dispose("a-session"); err != nil {
		t.Fatalf("重复 Dispose: %v", err)
	}
	if got := rec.kinds(); len(got) != before+1 {
		t.Fatalf("重复 Dispose 多发了边：%v", got)
	}
	if _, ok := reg.Get("a-session"); ok {
		t.Fatal("dispose 之后还能读到")
	}
}

// TestAgentRegistryConcurrentStatusTransitions 钉住并发：
// 多个会话各自 start/stop 不串状态，边数 = 真实迁移数。
func TestAgentRegistryConcurrentStatusTransitions(t *testing.T) {
	reg, rec := newTestRegistry(t)
	const n = 16
	for i := 0; i < n; i++ {
		if _, err := reg.Create(fmt.Sprintf("session-%02d", i), ""); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("session-%02d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for round := 0; round < 8; round++ {
				if _, err := reg.Start(id); err != nil {
					t.Errorf("Start(%s): %v", id, err)
					return
				}
				if _, err := reg.Stop(id); err != nil {
					t.Errorf("Stop(%s): %v", id, err)
					return
				}
			}
		}()
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		id := fmt.Sprintf("session-%02d", i)
		if !reg.Idle(id) {
			t.Fatalf("%s 收工后不是 idle", id)
		}
	}
	// 每会话：created + 8 次 status（start/stop 交替各一次，状态真的变了）。
	want := n + n*16
	if len(rec.events) != want {
		t.Fatalf("边数=%d，want %d（每会话 created + 16 次 status 迁移）", len(rec.events), want)
	}
	// 边上的时间单调不减（同一把锁内取时钟，不会倒流）。
	var last int64
	for _, ev := range rec.events {
		if ev.At < last {
			t.Fatalf("边时间倒流：%d < %d（%+v）", ev.At, last, ev)
		}
		last = ev.At
	}
}

// TestAgentRegistryStartAutoRegisters 钉住"第一次看到一轮"的便捷语义：
// 生产里不需要先手工登记才不会漏状态。
func TestAgentRegistryStartAutoRegisters(t *testing.T) {
	reg, rec := newTestRegistry(t)
	snapshot, err := reg.Start("session-new")
	if err != nil {
		t.Fatalf("Start 未知会话: %v", err)
	}
	if snapshot.Status != AgentRunning {
		t.Fatalf("自动登记后状态=%q，want running", snapshot.Status)
	}
	if got := rec.kinds(); len(got) != 1 || got[0] != EventAgentStatus {
		t.Fatalf("自动登记只该发一条 status 边：%v", got)
	}
	// 自动登记的根 agent 没有父、深度 0。
	if snapshot.ParentID != "" || snapshot.Depth != 0 {
		t.Fatalf("自动登记的快照=%+v", snapshot)
	}
}

// TestAgentRegistryZeroValueIsSafe 钉住 nil 安全：包装层在测试里可能不带注册表。
func TestAgentRegistryZeroValueIsSafe(t *testing.T) {
	var reg *Registry
	if _, ok := reg.Get("x"); ok {
		t.Fatal("nil 注册表读出了 agent")
	}
	if reg.Idle("x") {
		t.Fatal("nil 注册表说是 idle")
	}
	if list := reg.List(); list != nil {
		t.Fatalf("nil 注册表 List=%+v", list)
	}
	if _, err := reg.Start("x"); err == nil {
		t.Fatal("nil 注册表 Start 竟然成功了")
	}
	if err := reg.Dispose("x"); err == nil {
		t.Fatal("nil 注册表 Dispose 竟然成功了")
	}
}

// TestAgentRegistryNowIsInjectable 钉住时钟注入：快照上的时间就是注入的时钟。
func TestAgentRegistryNowIsInjectable(t *testing.T) {
	fixed := time.Unix(1_700_000_000, 0)
	reg := NewRegistry(RegistryOptions{Now: func() time.Time { return fixed }})
	snapshot, err := reg.Create("session-a", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if snapshot.CreatedAt != fixed.UnixMilli() || snapshot.ChangedAt != fixed.UnixMilli() {
		t.Fatalf("快照时间=%+v，want %d", snapshot, fixed.UnixMilli())
	}
}
