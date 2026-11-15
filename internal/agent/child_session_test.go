package agent

// 可续问子代理的 durable 落点：先红后绿都靠这里钉住。
//
// 这些用例只碰真实文件：真 v4 会话、真 zstd 帧、真 FindByID。不造旁路记录——
// 因为"跨进程续问"能成立的唯一理由就是这些帧真的在盘上。

import (
	"encoding/json"
	"strings"
	"testing"

	"dshgo/codecs/v4"
	"dshgo/internal/session"
	"dshgo/internal/spi"
)

// newChildFixture 造一个只含 sessions 目录的空 home。
func newChildFixture(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// readSession 按 id 读回会话日志（等价于"换一个进程/重启后再读"）。
func readSession(t *testing.T, home, id string) *v4.Log {
	t.Helper()
	path, err := session.FindByID(home, id)
	if err != nil {
		t.Fatalf("按 id 找会话 %q: %v", id, err)
	}
	log, err := v4.ReadFile(path)
	if err != nil {
		t.Fatalf("读会话 %q: %v", id, err)
	}
	return log
}

func TestChildSpawnWritesDurableParentFrame(t *testing.T) {
	home := newChildFixture(t)
	rec, err := ChildSpawn{Home: home, ParentSessionID: "parent-1", Cwd: home}.Spawn(ChildSpawnOptions{
		ChildSessionID: "child-1",
		Prompt:         "数一下 sessions 目录里有几个文件",
		Answer:         "三个",
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if rec.ChildSessionID != "child-1" || rec.ParentSessionID != "parent-1" {
		t.Fatalf("记录不对: %+v", rec)
	}
	if rec.SpawnedAt == 0 {
		t.Fatal("spawnedAt 没落盘")
	}

	// 真读回：文件必须存在，第一帧必须是父子帧，帧里两个 id 都在。
	log := readSession(t, home, "child-1")
	if len(log.Events) < 3 {
		t.Fatalf("期望至少 3 帧（spawn/user/assistant），实得 %d", len(log.Events))
	}
	if log.Events[0].Type != EventChildSpawn {
		t.Fatalf("第一帧是 %q，期望 %q", log.Events[0].Type, EventChildSpawn)
	}
	var frame ChildRecord
	if err := json.Unmarshal(log.Events[0].Data, &frame); err != nil {
		t.Fatalf("解父子帧: %v", err)
	}
	if frame.ChildSessionID != "child-1" || frame.ParentSessionID != "parent-1" {
		t.Fatalf("父子帧不对: %+v", frame)
	}

	// 委派往返必须真的进了子会话历史（续问的前提：它记得自己做过什么）。
	surface := session.Surface(log)
	if len(surface) != 2 {
		t.Fatalf("期望 2 条消息（user+assistant），实得 %d", len(surface))
	}
	if surface[0].Message.Role != "user" || !strings.Contains(surface[0].Message.Content, "几个文件") {
		t.Fatalf("第一条不是委派正文: %+v", surface[0].Message)
	}
	if surface[1].Message.Role != "assistant" || !strings.Contains(surface[1].Message.Content, "三个") {
		t.Fatalf("第二条不是子代理正文: %+v", surface[1].Message)
	}
}

// TestChildSpawnRefusesToRebuildExistingSession 钉住"不许截断历史"。
//
// v4.Create 打开即截断。如果没有这道闸，重启后重新认领 → 再 spawn 一次 →
// 子会话的历史被清成只剩一行，而调用方看到的是成功。
func TestChildSpawnRefusesToRebuildExistingSession(t *testing.T) {
	home := newChildFixture(t)
	spawn := ChildSpawn{Home: home, ParentSessionID: "parent-1", Cwd: home}
	if _, err := spawn.Spawn(ChildSpawnOptions{ChildSessionID: "child-1", Prompt: "第一次", Answer: "好"}); err != nil {
		t.Fatalf("第一次 spawn: %v", err)
	}
	before := readSession(t, home, "child-1")

	_, err := spawn.Spawn(ChildSpawnOptions{ChildSessionID: "child-1", Prompt: "第二次", Answer: "好"})
	if err == nil {
		t.Fatal("重建既有子会话必须失败")
	}
	if !strings.Contains(err.Error(), "拒绝重建") {
		t.Fatalf("错误没说清是拒绝重建: %v", err)
	}
	after := readSession(t, home, "child-1")
	if len(after.Events) != len(before.Events) {
		t.Fatalf("历史被改动了: %d → %d 帧", len(before.Events), len(after.Events))
	}
}

func TestChildSpawnRequiresHomeAndParent(t *testing.T) {
	home := newChildFixture(t)
	if _, err := (ChildSpawn{Home: home}).Spawn(ChildSpawnOptions{Prompt: "x"}); err == nil {
		t.Fatal("没有父会话 id 必须失败（没有父就没有授权边界）")
	}
	if _, err := (ChildSpawn{ParentSessionID: "p"}).Spawn(ChildSpawnOptions{Prompt: "x"}); err == nil {
		t.Fatal("没有 home 必须失败")
	}
	// 子会话不能就是父会话：那等于自授权。
	if _, err := (ChildSpawn{Home: home, ParentSessionID: "same"}).Spawn(ChildSpawnOptions{ChildSessionID: "same"}); err == nil {
		t.Fatal("child == parent 必须失败")
	}
}

// TestFindChildRecoversAcrossProcesses 是"重启/换进程"的正面用例：
// 只给 home + child id（不知道父、也没有内存状态），必须认得出归属。
func TestFindChildRecoversAcrossProcesses(t *testing.T) {
	home := newChildFixture(t)
	if _, err := (ChildSpawn{Home: home, ParentSessionID: "parent-A", Cwd: home}).
		Spawn(ChildSpawnOptions{ChildSessionID: "child-A", Prompt: "做件事", Answer: "做完了"}); err != nil {
		t.Fatalf("spawn: %v", err)
	}

	rec, ok, err := FindChild(home, "child-A")
	if err != nil {
		t.Fatalf("FindChild: %v", err)
	}
	if !ok {
		t.Fatal("认不出刚落盘的儿子")
	}
	if rec.ParentSessionID != "parent-A" || rec.ChildSessionID != "child-A" {
		t.Fatalf("认出了错的归属: %+v", rec)
	}
	if rec.Cwd != home {
		t.Fatalf("cwd 没恢复: %q", rec.Cwd)
	}
}

// TestFindChildDoesNotInventOwnership 钉住"不编"：普通会话、空 id、不存在的
// 会话，一律 false；普通会话不许因为"它在 sessions 目录里"就被当成某个父的孩子。
func TestFindChildDoesNotInventOwnership(t *testing.T) {
	home := newChildFixture(t)
	// 一条普通会话（没有任何父子帧）。
	lg, err := session.CreateSession(home, home, "plain-1", "default")
	if err != nil {
		t.Fatalf("建普通会话: %v", err)
	}
	if err := lg.Append("user/message", session.UserMessage([]spi.Block{{Type: "text", Text: "我自己开的会话"}})); err != nil {
		t.Fatalf("写普通会话: %v", err)
	}
	if err := lg.Close(); err != nil {
		t.Fatalf("关普通会话: %v", err)
	}

	if rec, ok, err := FindChild(home, "plain-1"); err != nil || ok {
		t.Fatalf("普通会话被认成了子会话: ok=%v rec=%+v err=%v", ok, rec, err)
	}
	if _, ok, err := FindChild(home, "不存在的会话"); err != nil || ok {
		t.Fatalf("不存在的会话被认出来了: ok=%v err=%v", ok, err)
	}
	if _, ok, err := FindChild(home, ""); err != nil || ok {
		t.Fatalf("空 id 被认出来了: ok=%v err=%v", ok, err)
	}
	if _, ok, err := FindChild("", "child-A"); err != nil || ok {
		t.Fatalf("没有 home 被认出来了: ok=%v err=%v", ok, err)
	}
}

// TestFindChildReportsTrueParent 钉住"归属只判一次"：
// 拿错父来问，也得先回**真实**父，让调用方去判 cross_session；
// 不能因为父不匹配就回"没这个会话"（那会把 403 变成 404 语义）。
func TestFindChildReportsTrueParent(t *testing.T) {
	home := newChildFixture(t)
	if _, err := (ChildSpawn{Home: home, ParentSessionID: "parent-A", Cwd: home}).
		Spawn(ChildSpawnOptions{ChildSessionID: "child-A", Prompt: "x", Answer: "y"}); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	rec, ok, err := FindChild(home, "child-A")
	if err != nil || !ok {
		t.Fatalf("FindChild: ok=%v err=%v", ok, err)
	}
	if rec.ParentSessionID != "parent-A" {
		t.Fatalf("真实父被改写了: %+v", rec)
	}
}

// TestChildSpawnAllocatesIdentity 钉住"空 id 由宿主分配"，且分配出来的
// 会话真能按 id 找回来（EncodeSegment 要能处理它）。
func TestChildSpawnAllocatesIdentity(t *testing.T) {
	home := newChildFixture(t)
	rec, err := (ChildSpawn{Home: home, ParentSessionID: "parent-1", Cwd: home}).
		Spawn(ChildSpawnOptions{Prompt: "只有提问，没有回答"})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if rec.ChildSessionID == "" || rec.ChildSessionID == "parent-1" {
		t.Fatalf("id 分配不对: %+v", rec)
	}
	if _, err := session.FindByID(home, rec.ChildSessionID); err != nil {
		t.Fatalf("分配的 id 找不回来: %v", err)
	}
	// 只有提问时不许编一个空回答。
	log := readSession(t, home, rec.ChildSessionID)
	if got := len(session.Surface(log)); got != 1 {
		t.Fatalf("期望只有 1 条消息（提问），实得 %d", got)
	}
}

// TestChildSpawnWritesStructuredSeed 钉住 fork 种子在子会话里的落盘形状。
//
// 官方 `dsh-subagent-fork-in-process/src/index.ts:72-78`：fork 的种子在创建时取一次，
// 之后成为**子会话自己的 durable transcript**。所以这里证明的不是"写了点文字"，
// 而是三件结构性的事：
//  1. 种子事件按原顺序排、data 原样（assistant 的 tool-call/reasoning 块、tool/result 的
//     isError 与 cwd 都还在）；
//  2. user 种子消息带 `source.kind = fork-seed`（区分"继承来的历史"与"派它的这一趟"）；
//  3. 顺序是 父子帧 → catalog/种子 → 这一趟的 prompt/answer。
func TestChildSpawnWritesStructuredSeed(t *testing.T) {
	home := newChildFixture(t)
	seed := []SeedEvent{
		{
			Type: "user/message", Seq: 1, Time: 1,
			Data: json.RawMessage(`{"content":[{"type":"text","text":"父会话第 1 句"}],"source":{"kind":"user"},"role":"user"}`),
		},
		{
			Type: "assistant/message", Seq: 2, Time: 2,
			Data: json.RawMessage(`{"turn":1,"step":1,"message":{"role":"assistant","content":[` +
				`{"type":"reasoning","text":"想过"},{"type":"tool-call","id":"c1","name":"read","arguments":"{}"}]},` +
				`"stream":[{"type":"finish","reason":{"kind":"tool-calls"}}],"surfaceOp":"append"}`),
		},
		{
			Type: "tool/result", Seq: 3, Time: 3,
			Data: json.RawMessage(`{"turn":1,"step":1,"message":{"role":"tool","toolCallId":"c1",` +
				`"content":[{"type":"text","text":"结果正文"}],"isError":true,"id":"r1"},` +
				`"meta":{"cwd":"/tmp/x"},"surfaceOp":"append"}`),
		},
	}
	rec, err := (ChildSpawn{Home: home, ParentSessionID: "parent-seed", Cwd: home}).Spawn(ChildSpawnOptions{
		ChildSessionID: "child-seed",
		Prompt:         "这一趟的任务",
		Answer:         "这一趟的回答",
		Seed:           seed,
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if rec.ChildSessionID != "child-seed" {
		t.Fatalf("id 不对: %+v", rec)
	}
	back := readSession(t, home, "child-seed")

	var types []string
	for _, ev := range back.Events {
		types = append(types, ev.Type)
	}
	want := []string{
		EventChildSpawn,
		"user/message", "assistant/message", "tool/result",
		"user/message", "assistant/message",
	}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("子会话事件序列 = %v，要 %v（帧 → 种子 → 这一趟）", types, want)
	}

	// 1) data 原样：块身份、isError、cwd 全在。
	body := string(back.Events[2].Data)
	if !strings.Contains(body, `"reasoning"`) || !strings.Contains(body, `"tool-call"`) {
		t.Fatalf("assistant 种子的块身份丢了: %s", body)
	}
	if !strings.Contains(body, `"kind":"tool-calls"`) {
		t.Fatalf("assistant 种子的 finish 原因被改写了: %s", body)
	}
	tool := string(back.Events[3].Data)
	if !strings.Contains(tool, `"isError":true`) || !strings.Contains(tool, `"cwd":"/tmp/x"`) {
		t.Fatalf("tool/result 种子的事实被改写了: %s", tool)
	}

	// 2) 种子的 user 消息带来源标记；这一趟的 prompt **不带**（它不是继承来的）。
	if !strings.Contains(string(back.Events[1].Data), `"kind":"fork-seed"`) {
		t.Fatalf("种子 user 消息没有 fork-seed 标记: %s", back.Events[1].Data)
	}
	if strings.Contains(string(back.Events[4].Data), `"kind":"fork-seed"`) {
		t.Fatalf("这一趟的 prompt 被标成了继承历史: %s", back.Events[4].Data)
	}
}

// TestChildSpawnRejectsSeedEventWithoutType 钉住"坏种子响亮失败"。
//
// 缺 type 的事件搬进子会话后没有任何投影认得它（Surface 只认 5 类）：
// 写进去等于留一条永远读不回来的记录，比"这次委派没成"更难查。
func TestChildSpawnRejectsSeedEventWithoutType(t *testing.T) {
	home := newChildFixture(t)
	_, err := (ChildSpawn{Home: home, ParentSessionID: "p", Cwd: home}).Spawn(ChildSpawnOptions{
		ChildSessionID: "child-bad-seed",
		Prompt:         "x",
		Seed:           []SeedEvent{{Data: json.RawMessage(`{"content":[]}`)}},
	})
	if err == nil {
		t.Fatal("缺 type 的种子事件必须让 spawn 失败")
	}
	if !strings.Contains(err.Error(), "type") {
		t.Fatalf("错误里要说清缺的是什么: %v", err)
	}
}
