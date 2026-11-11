package teamtask

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"dshgo/internal/spi"
)

// 本文件的测试对齐官方 0.2.2-rc.2 契约（本文件只加约束，不删旧测试）：
//
//	工具参数/输出 schema  node_modules/@deepseek-ai/dsh-tool-agent-team/src/index.ts:36-60,117-234
//	视图字段与推导        …/dsh-agent-team/src/types.ts:60-91、task-view.ts:12-56
//	writeScope 归一化     …/dsh-agent-team/src/validation.ts:26-34
//	CAS 与动作矩阵        …/dsh-agent-team/src/task-board.ts:109-214

// ---------------------------------------------------------------- 测试小工具

// execOn 在给定 board 上执行一次工具调用（可以用它构造带 leadSession 的板子）。
func execOn(t *testing.T, b *board, session, kind string, args map[string]any) (spi.Result, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	ctx := context.Background()
	if session != "" {
		ctx = spi.WithSession(ctx, session)
	}
	return (&Tool{b: b, kind: kind}).Exec(ctx, raw)
}

// dataOf 把 Result.Data 走一遍 JSON 再取 map——不依赖实现里的具体类型。
func dataOf(t *testing.T, res spi.Result) map[string]any {
	t.Helper()
	raw, err := json.Marshal(res.Data)
	if err != nil {
		t.Fatalf("marshal Data: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal Data: %v", err)
	}
	return m
}

// viewOf 取 get/create/update 结果里的官方任务视图（Data.task）。
func viewOf(t *testing.T, res spi.Result) map[string]any {
	t.Helper()
	m := dataOf(t, res)
	v, ok := m["task"].(map[string]any)
	if !ok {
		t.Fatalf("Data.task 不是对象: %#v", m)
	}
	return v
}

// revisionOf 读一条任务当前的 revision（走公开工具面，和模型看到的一样）。
func revisionOf(t *testing.T, dir, session, id string) int {
	t.Helper()
	res, err := execTool(t, dir, kindGet, session, map[string]any{"task_id": id})
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	rev, ok := viewOf(t, res)["revision"].(float64)
	if !ok {
		t.Fatalf("视图里没有 revision: %#v", res.Data)
	}
	return int(rev)
}

// fieldNum / fieldStrings 是健壮读取：字段缺失或类型不对时返回 false，让断言失败而不是 panic。
func fieldNum(m map[string]any, key string) (float64, bool) {
	v, ok := m[key].(float64)
	return v, ok
}

func fieldStrings(m map[string]any, key string) ([]string, bool) {
	raw, ok := m[key].([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		s, ok := item.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

func boardBytes(t *testing.T, dir string) []byte {
	t.Helper()
	raw, err := readFileRaw(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("回读任务板: %v", err)
	}
	return raw
}

// ---------------------------------------------------------------- 1) expected_revision 必填

// 官方 team_task_update 的 expected_revision 是必填（index.ts:205），缺了就不该动板子。
func TestTeamTaskUpdateRequiresExpectedRevision(t *testing.T) {
	dir := t.TempDir()
	id := createTask(t, dir, "session-alice", "缺 CAS 前置的任务")
	before := boardBytes(t, dir)

	res, err := execTool(t, dir, kindUpdate, "session-alice", map[string]any{
		"task_id": id,
		"action":  "claim",
	})
	if err == nil {
		t.Fatalf("缺 expected_revision 必须报错，实际成功: %#v", res.Data)
	}
	if !strings.Contains(res.Hint, "expected_revision") {
		t.Errorf("Hint 要点名 expected_revision，实际 %q", res.Hint)
	}
	if !strings.Contains(res.Hint, "team_task_get") {
		t.Errorf("Hint 要指向 team_task_get（先拿最新 revision），实际 %q", res.Hint)
	}

	// 拒绝必须是"没写进去"：文件字节完全不变。
	if after := boardBytes(t, dir); !bytes.Equal(before, after) {
		t.Fatalf("被拒后任务板被改了:\n前 %s\n后 %s", before, after)
	}
}

// ---------------------------------------------------------------- 2) 真 CAS

// 官方 task-board.ts:128-133：revision 不匹配 → TEAM_TASK_STALE_REVISION，且任务保持不变。
func TestTeamTaskUpdateRejectsStaleRevision(t *testing.T) {
	dir := t.TempDir()
	id := createTask(t, dir, "session-alice", "CAS 目标")

	if _, err := execTool(t, dir, kindUpdate, "session-alice", map[string]any{
		"task_id":           id,
		"expected_revision": 1,
		"action":            "claim",
	}); err != nil {
		t.Fatalf("用正确 revision 1 claim 应当成功: %v", err)
	}
	if got := revisionOf(t, dir, "session-alice", id); got != 2 {
		t.Fatalf("claim 后 revision = %d，期望 2（每次变更 +1）", got)
	}
	before := boardBytes(t, dir)

	// 拿旧 revision 再改：必须被 CAS 挡住。
	res, err := execTool(t, dir, kindUpdate, "session-alice", map[string]any{
		"task_id":           id,
		"expected_revision": 1,
		"action":            "complete",
	})
	if err == nil {
		t.Fatalf("过期 revision 必须被拒，实际成功: %#v", res.Data)
	}
	if !strings.Contains(res.Hint, "TEAM_TASK_STALE_REVISION") {
		t.Errorf("Hint 要带官方错误码语义 TEAM_TASK_STALE_REVISION，实际 %q", res.Hint)
	}
	if !strings.Contains(res.Hint, "team_task_get") {
		t.Errorf("Hint 要指向 team_task_get，实际 %q", res.Hint)
	}
	if after := boardBytes(t, dir); !bytes.Equal(before, after) {
		t.Fatalf("stale 拒绝必须不落盘:\n前 %s\n后 %s", before, after)
	}

	// 任务本身没变：还是 in_progress、revision 2、owner 仍是 alice。
	res, err = execTool(t, dir, kindGet, "session-alice", map[string]any{"task_id": id})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	view := viewOf(t, res)
	if view["status"] != statusInProgress {
		t.Errorf("被拒后 status = %v，期望 %s", view["status"], statusInProgress)
	}
	if rev, ok := fieldNum(view, "revision"); !ok || rev != 2 {
		t.Errorf("被拒后 revision = %v，期望 2", view["revision"])
	}
	if view["owner"] != "session-alice" {
		t.Errorf("被拒后 owner = %v，期望 session-alice", view["owner"])
	}
}

// ---------------------------------------------------------------- 3) reassign

// 官方 task-board.ts:188-203：reassign 只有 lead 能做；owner 省略 = 卸任；
// 只对 pending/in_progress 有效；指派时任务必须已可开工。
func TestTeamTaskUpdateReassign(t *testing.T) {
	dir := t.TempDir()
	// 宿主近似口径：接线时指定 lead 会话（board.leadSession），非 lead 的 reassign 必须被拒。
	lead := &board{dir: dir, leadSession: "session-lead"}

	res, err := execOn(t, lead, "session-lead", kindCreate, map[string]any{
		"subject": "需要转派的任务", "description": "reassign 用例",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id := dataOf(t, res)["id"].(string)

	// 非 lead 不能 reassign。
	noLead, err := execOn(t, lead, "session-bob", kindUpdate, map[string]any{
		"task_id": id, "expected_revision": 1, "action": "reassign", "owner": "bob",
	})
	if err == nil {
		t.Fatalf("非 lead 的 reassign 必须被拒: %#v", noLead.Data)
	}
	if !strings.Contains(noLead.Hint, "TEAM_LEAD_REQUIRED") {
		t.Errorf("Hint 要带 TEAM_LEAD_REQUIRED，实际 %q", noLead.Hint)
	}

	// 非法目标：空串与不合规名字都要报错，且任务不动。
	for _, bad := range []string{"", "Bob", "bob smith", "-bob"} {
		res, err := execOn(t, lead, "session-lead", kindUpdate, map[string]any{
			"task_id": id, "expected_revision": 1, "action": "reassign", "owner": bad,
		})
		if err == nil {
			t.Fatalf("reassign 到非法目标 %q 必须报错: %#v", bad, res.Data)
		}
		if !strings.Contains(res.Hint, "TEAM_") {
			t.Errorf("目标 %q 的 Hint 要带官方错误码语义，实际 %q", bad, res.Hint)
		}
	}

	// lead 转派给 bob：owner 变成 bob，任务进入 in_progress，revision +1。
	res, err = execOn(t, lead, "session-lead", kindUpdate, map[string]any{
		"task_id": id, "expected_revision": 1, "action": "reassign", "owner": "bob",
	})
	if err != nil {
		t.Fatalf("lead reassign 到 bob: %v (hint %s)", err, res.Hint)
	}
	view := viewOf(t, res)
	if view["owner"] != "bob" {
		t.Errorf("reassign 后 owner = %v，期望 bob", view["owner"])
	}
	if view["status"] != statusInProgress {
		t.Errorf("reassign 到具体 owner 后 status = %v，期望 in_progress", view["status"])
	}
	if rev, ok := fieldNum(view, "revision"); !ok || rev != 2 {
		t.Errorf("reassign 后 revision = %v，期望 2", view["revision"])
	}

	// 省略 owner = 卸任：退回 pending 并清 owner。
	res, err = execOn(t, lead, "session-lead", kindUpdate, map[string]any{
		"task_id": id, "expected_revision": 2, "action": "reassign",
	})
	if err != nil {
		t.Fatalf("reassign 省略 owner（卸任）: %v (hint %s)", err, res.Hint)
	}
	view = viewOf(t, res)
	if view["owner"] != "" {
		t.Errorf("卸任后 owner = %v，期望空", view["owner"])
	}
	if view["status"] != statusPending {
		t.Errorf("卸任后 status = %v，期望 pending", view["status"])
	}

	// 有未完成 blocker 的任务不能转派给具体 owner（官方 TEAM_TASK_BLOCKED）。
	blockerRes, err := execOn(t, lead, "session-lead", kindCreate, map[string]any{
		"subject": "阻塞者", "description": "先做这个",
	})
	if err != nil {
		t.Fatalf("create blocker: %v", err)
	}
	blockedRes, err := execOn(t, lead, "session-lead", kindCreate, map[string]any{
		"subject": "被阻塞的任务", "description": "等阻塞者",
		"blocked_by": []string{dataOf(t, blockerRes)["id"].(string)},
	})
	if err != nil {
		t.Fatalf("create blocked: %v", err)
	}
	blockedID := dataOf(t, blockedRes)["id"].(string)
	res, err = execOn(t, lead, "session-lead", kindUpdate, map[string]any{
		"task_id": blockedID, "expected_revision": 1, "action": "reassign", "owner": "bob",
	})
	if err == nil {
		t.Fatalf("被阻塞的任务不能转派给 owner: %#v", res.Data)
	}
	if !strings.Contains(res.Hint, "TEAM_TASK_BLOCKED") {
		t.Errorf("Hint 要带 TEAM_TASK_BLOCKED，实际 %q", res.Hint)
	}

	// 已完成的任务不能 reassign（官方 TEAM_TASK_INVALID_TRANSITION）。
	doneRes, err := execOn(t, lead, "session-lead", kindCreate, map[string]any{
		"subject": "已完成的", "description": "complete 之后不能转派",
	})
	if err != nil {
		t.Fatalf("create done: %v", err)
	}
	doneID := dataOf(t, doneRes)["id"].(string)
	if _, err := execOn(t, lead, "session-lead", kindUpdate, map[string]any{
		"task_id": doneID, "expected_revision": 1, "action": "complete",
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	res, err = execOn(t, lead, "session-lead", kindUpdate, map[string]any{
		"task_id": doneID, "expected_revision": 2, "action": "reassign", "owner": "bob",
	})
	if err == nil {
		t.Fatalf("已完成任务不能 reassign: %#v", res.Data)
	}
	if !strings.Contains(res.Hint, "TEAM_TASK_INVALID_TRANSITION") {
		t.Errorf("Hint 要带 TEAM_TASK_INVALID_TRANSITION，实际 %q", res.Hint)
	}
}

// ---------------------------------------------------------------- 4) 视图字段

// 官方 TASK_VIEW_SCHEMA（index.ts:36-51）+ task-view.ts:22-24,35-56：
// writeScopes 落盘并归一化、ready 随 blocker 完成变化、camelCase 与 snake_case 兼容键同时在。
func TestTeamTaskViewHasWriteScopesAndReady(t *testing.T) {
	dir := t.TempDir()
	ctx := spi.WithSession(context.Background(), "session-alice")

	res, err := mk(dir, kindCreate).Exec(ctx, json.RawMessage(`{
		"subject":"带写域的任务","description":"写域归一化用例",
		"write_scopes":["modules/tool/teamtask/","docs\\b2-report.md","./Makefile"]}`))
	if err != nil {
		t.Fatalf("create with write_scopes: %v", err)
	}
	aID := dataOf(t, res)["id"].(string)

	// create 的返回就是官方视图：revision=1、ready=true（没有 blocker）、writeScopes 已归一化。
	view := viewOf(t, res)
	if rev, ok := fieldNum(view, "revision"); !ok || rev != 1 {
		t.Errorf("create 后 revision = %v，期望 1", view["revision"])
	}
	if view["ready"] != true {
		t.Errorf("没有 blocker 的 pending 任务 ready 应为 true，实际 %v", view["ready"])
	}
	got, ok := fieldStrings(view, "writeScopes")
	if !ok {
		t.Fatalf("视图缺 writeScopes: %#v", view)
	}
	want := []string{"modules/tool/teamtask", "docs/b2-report.md", "Makefile"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("writeScopes = %v，期望归一化后 %v（反斜杠→斜杠、去 ./、去尾 /）", got, want)
	}
	// 兼容键不能丢：老消费者按 snake_case 读。
	if view["write_scopes"] == nil {
		t.Errorf("宿主兼容键 write_scopes 丢了: %#v", view)
	}

	// get 回同样内容。
	gres, err := mk(dir, kindGet).Exec(ctx, json.RawMessage(`{"task_id":"`+aID+`"}`))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	gview := viewOf(t, gres)
	if !equalStrings(gview["writeScopes"], want) {
		t.Errorf("get writeScopes = %v，期望 %v", gview["writeScopes"], want)
	}
	if gview["ready"] != true {
		t.Errorf("get ready = %v，期望 true", gview["ready"])
	}

	// 依赖它的任务：blocker 没完成时 ready=false，完成之后 ready=true。
	bres, err := mk(dir, kindCreate).Exec(ctx, json.RawMessage(`{
		"subject":"依赖任务","description":"blocker 完成才 ready","blocked_by":["`+aID+`"]}`))
	if err != nil {
		t.Fatalf("create dependent: %v", err)
	}
	bID := dataOf(t, bres)["id"].(string)
	if viewOf(t, bres)["ready"] != false {
		t.Errorf("blocker 未完成时 ready 应为 false，实际 %v", viewOf(t, bres)["ready"])
	}
	// blockedBy 用官方 camelCase，同时保留 blocked_by。
	if bview := viewOf(t, bres); !equalStrings(bview["blockedBy"], []string{aID}) || !equalStrings(bview["blocked_by"], []string{aID}) {
		t.Errorf("blockedBy/blocked_by = %v / %v，期望都含 %s", bview["blockedBy"], bview["blocked_by"], aID)
	}

	if _, err := mk(dir, kindUpdate).Exec(ctx, json.RawMessage(`{"task_id":"`+aID+`","expected_revision":1,"action":"complete"}`)); err != nil {
		t.Fatalf("complete blocker: %v", err)
	}
	bgot, err := mk(dir, kindGet).Exec(ctx, json.RawMessage(`{"task_id":"`+bID+`"}`))
	if err != nil {
		t.Fatalf("get dependent: %v", err)
	}
	if viewOf(t, bgot)["ready"] != true {
		t.Errorf("blocker 完成后 ready 应为 true，实际 %v", viewOf(t, bgot)["ready"])
	}
}

// equalStrings 比较任意 JSON 数组与期望字符串切片。
func equalStrings(value any, want []string) bool {
	raw, err := json.Marshal(value)
	if err != nil {
		return false
	}
	var got []string
	if err := json.Unmarshal(raw, &got); err != nil {
		return false
	}
	return strings.Join(got, "\x00") == strings.Join(want, "\x00")
}

// ---------------------------------------------------------------- 5) 写域重叠告警

// 官方 task-view.ts:36-42：与**其它 in_progress 任务**写域按路径段重叠 → 告警含对方 id。
func TestTeamTaskWriteScopeOverlapWarns(t *testing.T) {
	dir := t.TempDir()
	parent := &board{dir: dir}

	create := func(scopes []string, subject string) string {
		t.Helper()
		res, err := execOn(t, parent, "session-lead", kindCreate, map[string]any{
			"subject": subject, "description": "写域重叠用例", "write_scopes": scopes,
		})
		if err != nil {
			t.Fatalf("create %q: %v", subject, err)
		}
		return dataOf(t, res)["id"].(string)
	}
	claim := func(id string) {
		t.Helper()
		if _, err := execOn(t, parent, "session-lead", kindUpdate, map[string]any{
			"task_id": id, "expected_revision": 1, "action": "claim",
		}); err != nil {
			t.Fatalf("claim %s: %v", id, err)
		}
	}

	aID := create([]string{"src/a"}, "A 改 src/a")
	bID := create([]string{"src/a/b"}, "B 改 src/a/b")
	cID := create([]string{"src/abc"}, "C 改 src/abc（不是路径段重叠）")

	claim(aID)
	claim(cID)

	// a 与 c 都是 in_progress，但 "src/a" 与 "src/abc" 不按路径段重叠 → 无告警。
	av, err := execOn(t, parent, "session-lead", kindGet, map[string]any{"task_id": aID})
	if err != nil {
		t.Fatalf("get a: %v", err)
	}
	if w, _ := fieldStrings(viewOf(t, av), "writeScopeWarnings"); len(w) != 0 {
		t.Errorf("src/a 与 src/abc 不应告警，实际 %v", w)
	}

	claim(bID)
	av, _ = execOn(t, parent, "session-lead", kindGet, map[string]any{"task_id": aID})
	aw, ok := fieldStrings(viewOf(t, av), "writeScopeWarnings")
	if !ok || len(aw) != 1 || !strings.Contains(aw[0], bID) {
		t.Fatalf("A 的告警应含对方 id %s，实际 %v", bID, aw)
	}
	if !strings.Contains(aw[0], "write scopes overlap with") {
		t.Errorf("告警文案要对齐官方 `write scopes overlap with <id>`，实际 %q", aw[0])
	}
	// 兼容别名 write_scope_warnings 与官方 camelCase 同值。
	if alias, ok := fieldStrings(viewOf(t, av), "write_scope_warnings"); !ok || len(alias) != 1 || alias[0] != aw[0] {
		t.Errorf("write_scope_warnings 别名应等于 writeScopeWarnings，实际 %v", viewOf(t, av)["write_scope_warnings"])
	}

	// 反向也要告警（官方两侧独立投影）。
	bv, _ := execOn(t, parent, "session-lead", kindGet, map[string]any{"task_id": bID})
	bw, ok := fieldStrings(viewOf(t, bv), "writeScopeWarnings")
	if !ok || len(bw) != 1 || !strings.Contains(bw[0], aID) {
		t.Fatalf("B 的告警应含对方 id %s，实际 %v", aID, bw)
	}

	// 已完成的任务不再参与重叠判定（官方只看 in_progress）。
	if _, err := execOn(t, parent, "session-lead", kindUpdate, map[string]any{
		"task_id": aID, "expected_revision": 2, "action": "complete",
	}); err != nil {
		t.Fatalf("complete a: %v", err)
	}
	bv, _ = execOn(t, parent, "session-lead", kindGet, map[string]any{"task_id": bID})
	if w, _ := fieldStrings(viewOf(t, bv), "writeScopeWarnings"); len(w) != 0 {
		t.Errorf("A 完成后 B 不应再告警，实际 %v", w)
	}
}

// ---------------------------------------------------------------- 6) list 过滤与分页

// 官方 index.ts:144-181：status/owner/unowned/ready + cursor/limit → {tasks, nextCursor?}；
// owner 与 unowned 互斥是参数校验失败。
func TestTeamTaskListOwnerUnownedReadyFilters(t *testing.T) {
	dir := t.TempDir()
	board := &board{dir: dir}
	session := func(s string) context.Context { return spi.WithSession(context.Background(), s) }

	create := func(subject string, blockedBy []string) string {
		t.Helper()
		args := map[string]any{"subject": subject, "description": subject}
		if blockedBy != nil {
			args["blocked_by"] = blockedBy
		}
		res, err := execOn(t, board, "session-alice", kindCreate, args)
		if err != nil {
			t.Fatalf("create %q: %v", subject, err)
		}
		return dataOf(t, res)["id"].(string)
	}

	aID := create("alice 认领的", nil)
	bID := create("没人认领的", nil)
	cID := create("被阻塞的", []string{aID})

	// a 认领 → in_progress 且不是 ready；c 因为有未完成 blocker 不是 ready。
	if _, err := execOn(t, board, "session-alice", kindUpdate, map[string]any{
		"task_id": aID, "expected_revision": 1, "action": "claim",
	}); err != nil {
		t.Fatalf("claim a: %v", err)
	}

	list := func(args map[string]any) (spi.Result, error) {
		return execOn(t, board, "session-alice", kindList, args)
	}

	// owner + unowned 互斥 → 参数校验失败。
	res, err := list(map[string]any{"owner": "alice", "unowned": true})
	if err == nil {
		t.Fatalf("owner 与 unowned 同时给必须报错: %#v", res.Data)
	}
	if !strings.Contains(res.Hint, "owner") || !strings.Contains(res.Hint, "unowned") {
		t.Errorf("Hint 要说清 owner/unowned 互斥，实际 %q", res.Hint)
	}

	// owner 过滤。
	res, err = list(map[string]any{"owner": "session-alice"})
	if err != nil {
		t.Fatalf("list owner: %v", err)
	}
	data := dataOf(t, res)
	tasks := data["tasks"].([]any)
	if len(tasks) != 1 || tasks[0].(map[string]any)["id"] != aID {
		t.Fatalf("owner 过滤结果 = %v，期望只有 %s", tasks, aID)
	}
	if total, _ := fieldNum(data, "total"); total != 1 {
		t.Errorf("total = %v，期望 1", data["total"])
	}
	if count, _ := fieldNum(data, "count"); count != 1 {
		t.Errorf("count = %v，期望 1", data["count"])
	}
	if _, ok := data["nextCursor"]; ok {
		t.Errorf("单页结果不该有 nextCursor: %v", data)
	}

	// unowned 过滤。
	res, err = list(map[string]any{"unowned": true})
	if err != nil {
		t.Fatalf("list unowned: %v", err)
	}
	ids := idsOf(t, res)
	if len(ids) != 2 || !contains(ids, bID) || !contains(ids, cID) {
		t.Fatalf("unowned 过滤结果 = %v，期望 [%s %s]", ids, bID, cID)
	}
	// 不传 unowned 时不被过滤掉（bool 指针语义）。
	res, err = list(map[string]any{"unowned": false})
	if err != nil {
		t.Fatalf("list unowned=false: %v", err)
	}
	if ids := idsOf(t, res); len(ids) != 1 || ids[0] != aID {
		t.Fatalf("unowned=false 结果 = %v，期望只有有 owner 的 %s", ids, aID)
	}

	// ready 过滤：只有 b（pending 且无 blocker）是 ready。
	res, err = list(map[string]any{"ready": true})
	if err != nil {
		t.Fatalf("list ready: %v", err)
	}
	if ids := idsOf(t, res); len(ids) != 1 || ids[0] != bID {
		t.Fatalf("ready=true 结果 = %v，期望只有 %s", ids, bID)
	}
	res, err = list(map[string]any{"ready": false})
	if err != nil {
		t.Fatalf("list ready=false: %v", err)
	}
	if ids := idsOf(t, res); len(ids) != 2 || !contains(ids, aID) || !contains(ids, cID) {
		t.Fatalf("ready=false 结果 = %v，期望 [%s %s]", ids, aID, cID)
	}

	// limit / cursor 分页 + nextCursor。
	res, err = list(map[string]any{"limit": 1})
	if err != nil {
		t.Fatalf("list limit=1: %v", err)
	}
	data = dataOf(t, res)
	if items, ok := data["tasks"].([]any); !ok || len(items) != 1 {
		t.Fatalf("limit=1 应只回 1 条: %v", data)
	}
	if next, ok := fieldNum(data, "nextCursor"); !ok || next != 1 {
		t.Fatalf("limit=1 时应给 nextCursor=1，实际 %v", data["nextCursor"])
	}
	if data["truncated"] != true {
		t.Errorf("还有下一页时 truncated 应为 true，实际 %v", data["truncated"])
	}
	res, err = list(map[string]any{"limit": 1, "cursor": 2})
	if err != nil {
		t.Fatalf("list cursor=2: %v", err)
	}
	data = dataOf(t, res)
	if _, ok := data["nextCursor"]; ok {
		t.Errorf("最后一页不该有 nextCursor: %v", data)
	}
	if data["truncated"] != false {
		t.Errorf("最后一页 truncated 应为 false，实际 %v", data["truncated"])
	}
	// 越界 cursor 返回空页，不是错误。
	res, err = list(map[string]any{"cursor": 99})
	if err != nil {
		t.Fatalf("越界 cursor 不该报错: %v", err)
	}
	if got, _ := fieldNum(dataOf(t, res), "count"); got != 0 {
		t.Errorf("越界 cursor 应回空页，实际 %v 条", got)
	}

	// 参数非法：limit 越界、cursor 负数。
	for _, args := range []map[string]any{{"limit": 0}, {"limit": 101}, {"cursor": -1}} {
		if res, err := list(args); err == nil {
			t.Fatalf("%v 必须报参数错误: %#v", args, res.Data)
		}
	}

	_ = session("session-alice") // 保持 context 语义可读性（会话由 execOn 传）
}

func idsOf(t *testing.T, res spi.Result) []string {
	t.Helper()
	data := dataOf(t, res)
	items, ok := data["tasks"].([]any)
	if !ok {
		t.Fatalf("Data.tasks 不是数组: %#v", data)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("tasks 的元素不是对象: %#v", item)
		}
		id, _ := obj["id"].(string)
		out = append(out, id)
	}
	return out
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- 7) 非法 write scope

// 官方 validation.ts:26-34：绝对路径 / 盘符 / 含 .. 或 . 或空段 → TEAM_INVALID_WRITE_SCOPE。
func TestTeamTaskInvalidWriteScopeRejected(t *testing.T) {
	bad := []string{
		"/abs/path",
		"C:/windows",
		`c:\temp`,
		"../escape",
		"a/../b",
		"a/./b",
		"a//b",
		"./",
		"",
	}
	for _, scope := range bad {
		dir := t.TempDir()
		res, err := execTool(t, dir, kindCreate, "session-alice", map[string]any{
			"subject": "非法写域", "description": "应当被拒", "write_scopes": []string{scope},
		})
		if err == nil {
			t.Fatalf("write_scopes=[%q] 必须被拒: %#v", scope, res.Data)
		}
		if !strings.Contains(res.Hint, "TEAM_INVALID_WRITE_SCOPE") {
			t.Errorf("scope %q 的 Hint 要带 TEAM_INVALID_WRITE_SCOPE，实际 %q", scope, res.Hint)
		}
		// 拒绝必须没落盘：板子文件不该被建出来。
		if bytes := boardBytes(t, dir); len(bytes) != 0 {
			t.Errorf("scope %q 被拒后仍写了板子: %s", scope, bytes)
		}
	}

	// edit 的 write_scopes 同样归一化校验，且非法时不落盘。
	dir := t.TempDir()
	id := createTask(t, dir, "session-alice", "改写域")
	before := boardBytes(t, dir)
	res, err := execTool(t, dir, kindUpdate, "session-alice", map[string]any{
		"task_id": id, "expected_revision": 1, "action": "edit", "write_scopes": []string{"../x"},
	})
	if err == nil {
		t.Fatalf("edit 传非法 write_scopes 必须被拒: %#v", res.Data)
	}
	if !strings.Contains(res.Hint, "TEAM_INVALID_WRITE_SCOPE") {
		t.Errorf("edit 的 Hint 要带 TEAM_INVALID_WRITE_SCOPE，实际 %q", res.Hint)
	}
	if after := boardBytes(t, dir); !bytes.Equal(before, after) {
		t.Errorf("edit 被拒后不该落盘:\n前 %s\n后 %s", before, after)
	}

	// edit 可以整份替换合法写域。
	res, err = execTool(t, dir, kindUpdate, "session-alice", map[string]any{
		"task_id": id, "expected_revision": 1, "action": "edit",
		"write_scopes": []string{"modules/tool/teamtask/", "modules/tool/teamtask/"},
	})
	if err != nil {
		t.Fatalf("edit 合法 write_scopes: %v (hint %s)", err, res.Hint)
	}
	if got, ok := fieldStrings(viewOf(t, res), "writeScopes"); !ok || len(got) != 1 || got[0] != "modules/tool/teamtask" {
		t.Errorf("edit 后 writeScopes = %v，期望归一化去重后的 [modules/tool/teamtask]", viewOf(t, res)["writeScopes"])
	}
}

// ---------------------------------------------------------------- 8) revision 连续 + 旧数据

// 官方 types.ts:63 / task-board.ts:218-221：revision 从 1 起、每次变更 +1 且连续。
// 老宿主落盘文件没有 revision：读到 0 要补 1，否则第一次 CAS 就被自己的旧数据判 stale。
func TestTeamTaskRevisionContinuityAndLegacyDefault(t *testing.T) {
	dir := t.TempDir()
	id := createTask(t, dir, "session-lead", "revision 连续性")

	steps := []struct {
		action string
		extra  map[string]any
		want   int
	}{
		{"claim", map[string]any{"owner": "alice"}, 2},
		{"edit", map[string]any{"subject": "改过的标题"}, 3},
		{"set_dependencies", map[string]any{"blocked_by": []string{}}, 4},
		{"complete", nil, 5},
		{"reopen", nil, 6},
	}
	for _, step := range steps {
		args := map[string]any{"task_id": id, "expected_revision": step.want - 1, "action": step.action}
		for k, v := range step.extra {
			args[k] = v
		}
		if _, err := execTool(t, dir, kindUpdate, "session-lead", args); err != nil {
			t.Fatalf("%s: %v", step.action, err)
		}
		if got := revisionOf(t, dir, "session-lead", id); got != step.want {
			t.Fatalf("%s 后 revision = %d，期望 %d", step.action, got, step.want)
		}
	}

	// 旧文件（没有 revision 字段）→ 读到 1，且能正常 CAS。
	legacyDir := t.TempDir()
	legacy := `{"tasks":[{"id":"task-0001-abcd","subject":"旧任务","description":"旧格式",` +
		`"status":"pending","owner":"","blocked_by":[],"created_at":"2026-01-01T00:00:00Z",` +
		`"updated_at":"2026-01-01T00:00:00Z"}]}`
	legacyBoard := &board{dir: legacyDir}
	if err := os.WriteFile(legacyBoard.path(), []byte(legacy), 0o644); err != nil {
		t.Fatalf("写旧格式板子: %v", err)
	}
	if got := revisionOf(t, legacyDir, "session-alice", "task-0001-abcd"); got != 1 {
		t.Fatalf("旧文件 revision = %d，期望补成 1", got)
	}
	res, err := execTool(t, legacyDir, kindUpdate, "session-alice", map[string]any{
		"task_id": "task-0001-abcd", "expected_revision": 1, "action": "claim",
	})
	if err != nil {
		t.Fatalf("旧文件按 revision 1 CAS: %v (hint %s)", err, res.Hint)
	}
	if rev, ok := fieldNum(viewOf(t, res), "revision"); !ok || rev != 2 {
		t.Errorf("旧文件 claim 后 revision = %v，期望 2", viewOf(t, res)["revision"])
	}
}

// ---------------------------------------------------------------- 9) schema 契约

// 工具声明面必须与官方一致（官方 index.ts:117-234）：update 必填 expected_revision、
// action 是 8 值全集、list 有 5 个过滤/分页参数、create 有 write_scopes；
// force 保留但要标注"宿主扩展"。
func TestTeamTaskSchemasMatchOfficialContract(t *testing.T) {
	schemaOf := func(kind string) map[string]any {
		t.Helper()
		var m map[string]any
		if err := json.Unmarshal(mk(t.TempDir(), kind).Schema(), &m); err != nil {
			t.Fatalf("%s schema 不是合法 JSON: %v", kind, err)
		}
		return m
	}
	props := func(m map[string]any) map[string]any {
		p, _ := m["properties"].(map[string]any)
		return p
	}

	// update
	up := schemaOf(kindUpdate)
	required, _ := up["required"].([]any)
	hasRevision := false
	for _, r := range required {
		if r == "expected_revision" {
			hasRevision = true
		}
	}
	if !hasRevision {
		t.Errorf("team_task_update 必须把 expected_revision 列为 required，实际 %v", required)
	}
	action, _ := props(up)["action"].(map[string]any)
	enum, _ := action["enum"].([]any)
	official := []string{"claim", "release", "edit", "set_dependencies", "complete", "reopen", "reassign", "delete"}
	if len(enum) != len(official) {
		t.Fatalf("action enum = %v，期望官方 8 值 %v", enum, official)
	}
	for i, v := range official {
		if enum[i] != v {
			t.Fatalf("action enum[%d] = %v，期望 %v（官方顺序）", i, enum[i], v)
		}
	}
	force, _ := props(up)["force"].(map[string]any)
	if force == nil {
		t.Fatalf("force 是宿主扩展，应保留并在 schema 里说明")
	}
	if desc, _ := force["description"].(string); !strings.Contains(desc, "宿主扩展") {
		t.Errorf("force 的 description 必须标注宿主扩展（官方无此参数），实际 %q", desc)
	}

	// list：5 个官方参数都在。
	listProps := props(schemaOf(kindList))
	for _, key := range []string{"status", "owner", "unowned", "ready", "cursor", "limit"} {
		if listProps[key] == nil {
			t.Errorf("team_task_list schema 缺参数 %q", key)
		}
	}

	// create：write_scopes 在。
	if props(schemaOf(kindCreate))["write_scopes"] == nil {
		t.Errorf("team_task_create schema 缺参数 write_scopes")
	}
}
