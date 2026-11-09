package agent

// 官方 `agentLoop.agents[].id` 的宿主落点：**具名 agent** 的稳定标识 + 由它派生的
// 子会话 id，以及 (父会话, agent id) → 子会话 id 的 durable 绑定。
//
// 官方原文（app.asar:dsh/node_modules/@deepseek-ai/dsh-agent-loop/lib/index.js:1351）：
//
//	const configuredId = sessionId ?? brandString(`${id}-session-${randomUUID()}`);
//
// 也就是说：`id` 是**稳定标签**，`sessionId` 缺省时按 `${id}-session-<uuid>` 铸一个。
// 这组用例钉住三件事，缺一条都不算"能复现官方派生规则"：
//  1. 形状真的是 `${id}-session-<uuid4>`，而不是随便拼；
//  2. `<uuid>` 每次新铸（同一个 id 再 spawn 一次得到的是另一条子会话）；
//  3. `description` 永远不会被当成 id 用（没有 id 就是没有 id）。
//
// 只碰真实文件：真 v4 会话、真 zstd 帧、真 FindByID——因为"跨进程复用同一个
// 子代理"的唯一依据就是这些帧真的在盘上。

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"dshgo/internal/session"
)

// officialAgentChildID 是 `${id}-session-<uuid4>` 的逐字符判据。
var officialAgentChildID = regexp.MustCompile(
	`^reviewer-session-[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// plainSessionID 是"宿主自己分配"的裸 uuid（旧行为，不带 agent 标签）。
var plainSessionID = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestSpawnMintsOfficialChildSessionIDFromAgentID(t *testing.T) {
	home := newChildFixture(t)
	spawn := ChildSpawn{Home: home, ParentSessionID: "parent-1", Cwd: home}

	rec, err := spawn.Spawn(ChildSpawnOptions{
		AgentID: "reviewer", Prompt: "看一遍会话", Answer: "看完了",
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if !officialAgentChildID.MatchString(rec.ChildSessionID) {
		t.Fatalf("子会话 id = %q，不满足官方 ${id}-session-<uuid4>", rec.ChildSessionID)
	}
	if rec.AgentID != "reviewer" {
		t.Fatalf("记录里没有 agentId: %+v", rec)
	}
	// 盘上真有一条会话，而且父子帧里写着 agentId（重启后按名字认人靠它）。
	log := readSession(t, home, rec.ChildSessionID)
	if log.Events[0].Type != EventChildSpawn {
		t.Fatalf("第一帧不是父子帧: %q", log.Events[0].Type)
	}
	var frame map[string]any
	if err := json.Unmarshal(log.Events[0].Data, &frame); err != nil {
		t.Fatalf("父子帧不是 JSON: %v", err)
	}
	if frame["agentId"] != "reviewer" {
		t.Fatalf("父子帧没记 agentId: %v", frame)
	}

	// 同一个 id 再 spawn：官方 randomUUID() 每次新铸 —— 不能复用上一条，
	// 否则第二次委派会把第一次的历史当成自己的（v4 是 append-only）。
	second, err := spawn.Spawn(ChildSpawnOptions{
		AgentID: "reviewer", Prompt: "再看一遍", Answer: "又看完了",
	})
	if err != nil {
		t.Fatalf("第二次 spawn: %v", err)
	}
	if second.ChildSessionID == rec.ChildSessionID {
		t.Fatalf("两次 spawn 拿到同一条子会话 %q：uuid 没有新铸", second.ChildSessionID)
	}
	if !officialAgentChildID.MatchString(second.ChildSessionID) {
		t.Fatalf("第二次的子会话 id = %q 不合规", second.ChildSessionID)
	}
}

// TestSpawnWithoutAgentIDKeepsBareSessionID 钉住"不设时零影响"：
//
// 不给 AgentID 就还是宿主自己分配的裸 uuid，一个字节都不变——老调用方
// （`continuable=true` 但不给 id）不该因为这次改动多出 `-session-` 之类的形状。
func TestSpawnWithoutAgentIDKeepsBareSessionID(t *testing.T) {
	home := newChildFixture(t)
	rec, err := ChildSpawn{Home: home, ParentSessionID: "parent-1", Cwd: home}.Spawn(ChildSpawnOptions{
		Prompt: "做事", Answer: "做完了",
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if !plainSessionID.MatchString(rec.ChildSessionID) {
		t.Fatalf("没有 AgentID 时子会话 id 应该是裸 uuid，实得 %q", rec.ChildSessionID)
	}
	if strings.Contains(rec.ChildSessionID, "-session-") {
		t.Fatalf("没有 AgentID 却派生了 agent 形状: %q", rec.ChildSessionID)
	}
	if rec.AgentID != "" {
		t.Fatalf("没有 AgentID 却记了 agentId: %+v", rec)
	}
	// 父子帧也必须还是老形状（没有 agentId 键）：既有读者按原样解析。
	log := readSession(t, home, rec.ChildSessionID)
	var frame map[string]any
	if err := json.Unmarshal(log.Events[0].Data, &frame); err != nil {
		t.Fatalf("父子帧不是 JSON: %v", err)
	}
	if _, ok := frame["agentId"]; ok {
		t.Fatalf("没有 AgentID 的父子帧不该出现 agentId 键: %v", frame)
	}
}

// TestSpawnRejectsInvalidAgentID 钉住输入校验：agent id 会变成会话目录名的一部分，
// 空白/控制字符/超长都必须当场拒绝，而不是写出一条找不着的会话。
func TestSpawnRejectsInvalidAgentID(t *testing.T) {
	home := newChildFixture(t)
	spawn := ChildSpawn{Home: home, ParentSessionID: "parent-1", Cwd: home}
	cases := map[string]string{
		"首尾空白": " reviewer ",
		"中间空白": "re viewer",
		"换行":   "reviewer\n",
		"制表符":  "reviewer\t",
		"超长":   strings.Repeat("a", 129),
		"只有空白": "   ",
	}
	for name, id := range cases {
		if _, err := spawn.Spawn(ChildSpawnOptions{AgentID: id, Prompt: "p", Answer: "a"}); err == nil {
			t.Fatalf("%s 的 agent id %q 应该被拒绝", name, id)
		}
	}
	// 合法边界：128 字节整好是上限，不能因为"接近上限"就拒。
	ok := strings.Repeat("b", 128)
	if _, err := spawn.Spawn(ChildSpawnOptions{AgentID: ok, Prompt: "p", Answer: "a"}); err != nil {
		t.Fatalf("128 字节的 agent id 应该合法: %v", err)
	}
}

// TestAgentChildBindingSurvivesReopen 钉住 durable 绑定：
//
// "跨轮复用同一个子代理"必须跨进程成立，所以绑定只能落在 <home>/state 下，
// 不能只是内存里的一张表。这里用"重新打开一次"模拟换进程。
func TestAgentChildBindingSurvivesReopen(t *testing.T) {
	home := newChildFixture(t)
	if _, ok, err := FindAgentChild(home, "parent-1", "reviewer"); err != nil || ok {
		t.Fatalf("还没有绑定时不该认到: ok=%v err=%v", ok, err)
	}
	if err := RecordAgentChild(home, "parent-1", "reviewer", "reviewer-session-aaa"); err != nil {
		t.Fatalf("记绑定: %v", err)
	}
	child, ok, err := FindAgentChild(home, "parent-1", "reviewer")
	if err != nil || !ok || child != "reviewer-session-aaa" {
		t.Fatalf("认绑定: child=%q ok=%v err=%v", child, ok, err)
	}
	// 另一个父会话不能串味：授权边界是按父会话立的。
	if _, ok, _ := FindAgentChild(home, "parent-2", "reviewer"); ok {
		t.Fatal("不同父会话认到了同一条绑定")
	}
	// 同一个 (父, id) 再记一次：后写的赢（agent 换会话就是换地址）。
	if err := RecordAgentChild(home, "parent-1", "reviewer", "reviewer-session-bbb"); err != nil {
		t.Fatalf("覆盖绑定: %v", err)
	}
	if child, _, _ := FindAgentChild(home, "parent-1", "reviewer"); child != "reviewer-session-bbb" {
		t.Fatalf("绑定没被覆盖: %q", child)
	}
}

// TestAppendChildTurnKeepsHistory 钉住复用路径的历史连续性：
// 复用同一个子代理时，这一趟往返必须**追加**到同一条子会话，而不是新开一条。
func TestAppendChildTurnKeepsHistory(t *testing.T) {
	home := newChildFixture(t)
	rec, err := ChildSpawn{Home: home, ParentSessionID: "parent-1", Cwd: home}.Spawn(ChildSpawnOptions{
		AgentID: "reviewer", Prompt: "第一件事", Answer: "做完了",
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if err := AppendChildTurn(home, home, rec.ChildSessionID, "第二件事", "也做完了"); err != nil {
		t.Fatalf("追加第二趟: %v", err)
	}
	log := readSession(t, home, rec.ChildSessionID)
	var texts []string
	for _, ev := range log.Events {
		if ev.Type != "user/message" && ev.Type != "assistant/message" {
			continue
		}
		texts = append(texts, string(ev.Data))
	}
	joined := strings.Join(texts, "\n")
	for _, want := range []string{"第一件事", "做完了", "第二件事", "也做完了"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("子会话历史缺 %q:\n%s", want, joined)
		}
	}
	// 还是同一条会话，没有被重建（v4 是 append-only，重建等于抹历史）。
	if path, err := session.FindByID(home, rec.ChildSessionID); err != nil || path == "" {
		t.Fatalf("复用后子会话不见了: %v", err)
	}
}
