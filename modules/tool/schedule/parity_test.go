package schedule

// 官方 rc.2 的 schedule 契约（`docs/tool-catalog.md` §`@deepseek-ai/dsh-tool-schedule`）：
//
//	schedule_create required = title + prompt + rrule + target
//	schedule_view   required = id
//	schedule_edit   required = id + action（update | delete）
//	schedule_list   active / limit / offset 全可选
//
// 这里的判据都是**行为**，不只看 schema：rrule 必须真按周期算出下一次、
// target 必须落成宿主认的 target_kind、edit 必须真暂停/恢复、乐观并发必须真不写盘。

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	schedulestore "dshgo/internal/schedule"
	"dshgo/internal/spi"
)

func schemaOf(t *testing.T, kind string) (props map[string]json.RawMessage, required []string) {
	t.Helper()
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(mk(t.TempDir(), kind).Schema(), &s); err != nil {
		t.Fatalf("%s 的 schema 不是合法 JSON: %v", kind, err)
	}
	return s.Properties, s.Required
}

func hasAll(t *testing.T, kind string, props map[string]json.RawMessage, names ...string) {
	t.Helper()
	for _, n := range names {
		if _, ok := props[n]; !ok {
			t.Errorf("%s 缺参数 %q", kind, n)
		}
	}
}

func requiredSet(req []string) map[string]bool {
	out := map[string]bool{}
	for _, r := range req {
		out[r] = true
	}
	return out
}

// 四个官方工具的 required / properties 必须逐条对上。
func TestSchemasMatchOfficialRc2(t *testing.T) {
	// create：官方 required 四项，且旧形状的字段仍在（兼容别名，能力下限）。
	props, req := schemaOf(t, kindCreate)
	hasAll(t, kindCreate, props, "title", "prompt", "rrule", "target")
	hasAll(t, kindCreate, props, "kind", "every_seconds", "daily_at", "weekly_at", "weekdays", "allow_writes")
	want := requiredSet([]string{"title", "prompt", "rrule", "target"})
	got := requiredSet(req)
	for k := range want {
		if !got[k] {
			t.Errorf("schedule_create 的 required 少了 %q（官方 rc.2 必填）: %v", k, req)
		}
	}
	// 关键：不能把旧的 kind 继续写成 required——模型照官方传参就会漏它。
	if got["kind"] {
		t.Errorf("schedule_create 仍把 kind 标成 required：%v（官方 rc.2 没有它）", req)
	}

	// view：官方 required = id。
	props, req = schemaOf(t, kindView)
	hasAll(t, kindView, props, "id")
	if !requiredSet(req)["id"] {
		t.Errorf("schedule_view 的 id 必须是 required: %v", req)
	}

	// edit：官方 required = id + action，其余全是可选的能力参数。
	props, req = schemaOf(t, kindEdit)
	hasAll(t, kindEdit, props, "id", "action", "title", "prompt", "rrule", "target", "active", "expectedUpdatedAt")
	r := requiredSet(req)
	if !r["id"] || !r["action"] || len(r) != 2 {
		t.Errorf("schedule_edit 的 required 应只有 id+action，实际 %v", req)
	}

	// list：官方三个可选参数都要有。
	props, req = schemaOf(t, kindList)
	hasAll(t, kindList, props, "active", "limit", "offset")
	if len(req) != 0 {
		t.Errorf("schedule_list 不该有 required: %v", req)
	}

	// 旧名保留（deprecated 别名）：名字还在，语义转发到 edit。
	if mk("", kindUpdate).Name() != "schedule_update" || mk("", kindDelete).Name() != "schedule_delete" {
		t.Fatal("旧工具名被改掉了：schedule_update / schedule_delete 必须保留为别名")
	}
	if got := mk("", kindEdit).Description(); !strings.Contains(got, "id") {
		t.Errorf("schedule_edit 的描述没点明按 id 操作: %q", got)
	}
}

// rrule 必须**真按周期**算出下一次：不是把 DTSTART 抄一遍，也不是"24 小时后再看"。
func TestCreateRRuleComputesRealNextOccurrence(t *testing.T) {
	dir := t.TempDir()
	ctx := ctxWith("acp-rrule")
	now := time.Now()

	// 每日 09:00（本地时区）：下一次必须是今天或明天的 09:00，且严格晚于 now。
	res, err := mk(dir, kindCreate).Exec(ctx, json.RawMessage(`{
		"title":"每日早报","prompt":"汇总昨天的提交","rrule":"DTSTART:20260101T090000\nRRULE:FREQ=DAILY","target":"this_session"}`))
	if err != nil {
		t.Fatalf("官方形状的 create 失败: %v", err)
	}
	items, err := (&store{dir: dir}).load("acp-rrule")
	if err != nil || len(items) != 1 {
		t.Fatalf("落盘 = %+v, err=%v", items, err)
	}
	it := items[0]
	if it.Kind != kindRRule {
		t.Fatalf("kind = %q，期望 %q", it.Kind, kindRRule)
	}
	if !strings.Contains(it.RRule, "RRULE:FREQ=DAILY") || !strings.HasPrefix(it.RRule, "DTSTART") {
		t.Fatalf("rrule 原文没落盘（到点就没法推进下一次）: %q", it.RRule)
	}
	next, err := time.Parse(time.RFC3339, it.NextAt)
	if err != nil {
		t.Fatalf("next_at = %q 不是 RFC3339: %v", it.NextAt, err)
	}
	if !next.After(now) {
		t.Fatalf("next_at=%s 不在将来（now=%s）", next, now)
	}
	if next.Hour() != 9 || next.Minute() != 0 {
		t.Fatalf("next_at=%s 的时刻不是 DTSTART 给的 09:00——rrule 没被真解析", next)
	}
	if d := next.Sub(now); d > 25*time.Hour {
		t.Fatalf("next_at 距现在 %s，超过一天：每日规则算错了", d)
	}
	// this_session 要落成宿主认的 target_kind=session + 目标会话。
	if it.TargetKind != targetKindSession || it.TargetSession != "acp-rrule" {
		t.Fatalf("target 落盘 = %q/%q，期望 session/acp-rrule", it.TargetKind, it.TargetSession)
	}
	if res.Summary == "" || !strings.Contains(res.Summary, it.ID) {
		t.Fatalf("Summary 没有点名任务: %q", res.Summary)
	}

	// 每周一 09:30：下一次必须落在周一，且时刻是 09:30。
	res, err = mk(dir, kindCreate).Exec(ctx, json.RawMessage(`{
		"title":"周报","prompt":"写周报","rrule":"DTSTART:20260105T093000\nRRULE:FREQ=WEEKLY;BYDAY=MO","target":"new"}`))
	if err != nil {
		t.Fatalf("weekly rrule 失败: %v", err)
	}
	items, _ = (&store{dir: dir}).load("acp-rrule")
	if len(items) != 2 {
		t.Fatalf("期望 2 条，实际 %d", len(items))
	}
	weekly := items[1]
	next, err = time.Parse(time.RFC3339, weekly.NextAt)
	if err != nil {
		t.Fatal(err)
	}
	if next.Weekday() != time.Monday {
		t.Fatalf("每周规则的下一次 = %s（%s），不是周一", next, next.Weekday())
	}
	if next.Hour() != 9 || next.Minute() != 30 || next.Second() != 0 {
		t.Fatalf("每周规则的下一次时刻 = %s，不是 09:30:00", next.Format("15:04:05"))
	}
	if next.Before(now) {
		t.Fatalf("下次 = %s 在过去", next)
	}
	if weekly.TargetKind != targetNew {
		t.Fatalf("target=new 落盘 = %q", weekly.TargetKind)
	}
}

// rrule 给了但 target 没给：必须报错（target 是官方 required），且不落盘。
func TestCreateRRuleRequiresTarget(t *testing.T) {
	dir := t.TempDir()
	ctx := ctxWith("acp-nt")
	res, err := mk(dir, kindCreate).Exec(ctx, json.RawMessage(`{
		"title":"没有目标","prompt":"x","rrule":"DTSTART:20260101T090000\nRRULE:FREQ=DAILY"}`))
	if err == nil {
		t.Fatal("缺 target 必须失败（官方 required）")
	}
	if !strings.Contains(res.Hint, "target") {
		t.Fatalf("Hint 要点名 target: %q", res.Hint)
	}
	if items, _ := (&store{dir: dir}).load("acp-nt"); len(items) != 0 {
		t.Fatalf("失败时不该落盘，实际 %d 条", len(items))
	}
}

// 坏 rrule 必须 fail-loud 并说清缺什么（不是静默成功）。
func TestCreateInvalidRRuleFailsLoud(t *testing.T) {
	dir := t.TempDir()
	ctx := ctxWith("acp-bad")
	res, err := mk(dir, kindCreate).Exec(ctx, json.RawMessage(`{
		"title":"坏规则","prompt":"x","rrule":"FREQ=DAILY","target":"this_session"}`))
	if err == nil {
		t.Fatal("没有 DTSTART 行的 rrule 必须失败")
	}
	if !strings.Contains(res.Hint, "DTSTART") {
		t.Fatalf("Hint 要说清缺 DTSTART: %q", res.Hint)
	}

	// 规则成立但永远不再触发（COUNT=1 且 DTSTART 已过，无下一次）。
	res, err = mk(dir, kindCreate).Exec(ctx, json.RawMessage(`{
		"title":"用尽","prompt":"x","rrule":"DTSTART:20200101T090000\nRRULE:FREQ=DAILY;COUNT=1","target":"this_session"}`))
	if err == nil {
		t.Fatal("没有下一次的 rrule 必须失败，不能登记一条永远不触发的任务")
	}
	if !strings.Contains(res.Hint, "没有下一次") && !strings.Contains(res.Hint, "COUNT") {
		t.Fatalf("Hint 要说明为什么没有下一次: %q", res.Hint)
	}
}

// 新建的 rrule 记录必须能被**宿主到点引擎**读懂并按原规则推进（同一份字节）。
func TestRRuleRecordIsUnderstoodByHostEngine(t *testing.T) {
	dir := t.TempDir()
	ctx := ctxWith("acp-host")
	if _, err := mk(dir, kindCreate).Exec(ctx, json.RawMessage(`{
		"title":"宿主对拍","prompt":"x","rrule":"DTSTART:20260101T090000\nRRULE:FREQ=DAILY","target":"this_session"}`)); err != nil {
		t.Fatal(err)
	}
	items, err := schedulestore.Load(dir, "acp-host")
	if err != nil || len(items) != 1 {
		t.Fatalf("宿主读不到工具写的记录: %+v err=%v", items, err)
	}
	if items[0].Kind != "rrule" || items[0].RRule == "" {
		t.Fatalf("宿主看到的 kind/rrule = %q/%q", items[0].Kind, items[0].RRule)
	}
	// DueAt(now) 只挑到期的；给一个"一年后"的 now，这条应当到期且能算出下一次。
	dues := schedulestore.DueAt(items, time.Now().AddDate(1, 0, 0))
	if len(dues) != 1 {
		t.Fatalf("宿主引擎没认出这条 rrule（DueAt=%d）", len(dues))
	}
	if dues[0].Ends {
		t.Fatal("FREQ=DAILY 不该被判成'没有下一次'")
	}
	if !dues[0].Next.IsZero() {
		h, m, _ := dues[0].Next.Clock()
		if h != 9 || m != 0 {
			t.Fatalf("宿主算出的下一次 %s 不是 09:00", dues[0].Next)
		}
	}
}

// view：按 id 看一条，字段齐全（含 prompt / rrule / target）。
func TestViewReturnsFullRecord(t *testing.T) {
	dir := t.TempDir()
	ctx := ctxWith("acp-view")
	if _, err := mk(dir, kindCreate).Exec(ctx, json.RawMessage(`{
		"title":"看一眼","prompt":"要看到这句","rrule":"DTSTART:20260101T090000\nRRULE:FREQ=DAILY","target":"new_once"}`)); err != nil {
		t.Fatal(err)
	}
	items, _ := (&store{dir: dir}).load("acp-view")
	id := items[0].ID

	res, err := mk(dir, kindView).Exec(ctx, json.RawMessage(`{"id":"`+id+`"}`))
	if err != nil {
		t.Fatalf("view 失败: %v", err)
	}
	m, _ := res.Data.(map[string]any)
	got, ok := m["schedule"].(Schedule)
	if !ok {
		t.Fatalf("view 的 Data.schedule 类型 = %T", m["schedule"])
	}
	if got.ID != id || got.Prompt != "要看到这句" || got.RRule == "" || got.TargetKind != targetNewOnce {
		t.Fatalf("view 回来字段不全: %+v", got)
	}
	if active, _ := m["active"].(bool); !active {
		t.Fatal("缺省 status 的任務必须算活跃")
	}

	// 找不到必须报错并给可用 id。
	res, err = mk(dir, kindView).Exec(ctx, json.RawMessage(`{"id":"sch-9999-zzzz"}`))
	if err == nil {
		t.Fatal("不存在的 id 必须报错")
	}
	if !strings.Contains(res.Hint, id) {
		t.Fatalf("Hint 该给出真实 id %s: %q", id, res.Hint)
	}
}

// edit：暂停 → 恢复，且恢复后 next_at 重新落在将来。
func TestEditPausesAndResumes(t *testing.T) {
	dir := t.TempDir()
	ctx := ctxWith("acp-pause")
	if _, err := mk(dir, kindCreate).Exec(ctx, json.RawMessage(`{
		"title":"可暂停","prompt":"x","rrule":"DTSTART:20260101T090000\nRRULE:FREQ=DAILY","target":"this_session"}`)); err != nil {
		t.Fatal(err)
	}
	items, _ := (&store{dir: dir}).load("acp-pause")
	id := items[0].ID
	before := items[0].UpdatedAt

	// 暂停
	if _, err := mk(dir, kindEdit).Exec(ctx, json.RawMessage(`{"id":"`+id+`","action":"update","active":false}`)); err != nil {
		t.Fatalf("暂停失败: %v", err)
	}
	items, _ = (&store{dir: dir}).load("acp-pause")
	if items[0].Status != schedulestore.StatusPaused {
		t.Fatalf("暂停后 status = %q", items[0].Status)
	}
	if items[0].UpdatedAt == before {
		t.Fatal("暂停没有更新 updated_at（乐观并发就失效了）")
	}
	// 暂停的任务不算到期：宿主引擎与 list 过滤都必须同意。
	hostItems, err := schedulestore.Load(dir, "acp-pause")
	if err != nil {
		t.Fatal(err)
	}
	if dues := schedulestore.DueAt(hostItems, time.Now().AddDate(1, 0, 0)); len(dues) != 0 {
		t.Fatalf("暂停的任务仍被宿主判为到期: %+v", dues)
	}
	res, err := mk(dir, kindList).Exec(ctx, json.RawMessage(`{"active":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := res.Data.(map[string]any); m["total"].(int) != 0 {
		t.Fatalf("list active=true 仍看得到暂停的任务: %+v", m)
	}

	// 恢复：next_at 必须是将来（不是把旧值原样留着）。
	if _, err := mk(dir, kindEdit).Exec(ctx, json.RawMessage(`{"id":"`+id+`","action":"update","active":true}`)); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	items, _ = (&store{dir: dir}).load("acp-pause")
	if items[0].Status == schedulestore.StatusPaused {
		t.Fatal("恢复后 status 仍是 paused")
	}
	next, err := time.Parse(time.RFC3339, items[0].NextAt)
	if err != nil {
		t.Fatal(err)
	}
	if !next.After(time.Now()) {
		t.Fatalf("恢复后 next_at = %s 不在将来", next)
	}

	// 恢复失败路径：规则已经用尽（COUNT=1 的旧记录）时必须报错而不是假装恢复。
	// 直接构造一条这样的记录（create 会拒绝没有下一次的规则）。
	items, _ = (&store{dir: dir}).load("acp-pause")
	items[0].RRule = "DTSTART:20200101T090000\nRRULE:FREQ=DAILY;COUNT=1"
	items[0].Status = schedulestore.StatusPaused
	if err := (&store{dir: dir}).save("acp-pause", items); err != nil {
		t.Fatal(err)
	}
	res, err = mk(dir, kindEdit).Exec(ctx, json.RawMessage(`{"id":"`+id+`","action":"update","active":true}`))
	if err == nil {
		t.Fatal("规则用尽时恢复必须失败")
	}
	if !strings.Contains(res.Hint, "rrule") {
		t.Fatalf("Hint 要指向新 rrule: %q", res.Hint)
	}
}

// edit 的乐观并发：expectedUpdatedAt 不一致 ⇒ schedule_conflict 结果、什么都不改。
func TestEditConflictChangesNothing(t *testing.T) {
	dir := t.TempDir()
	ctx := ctxWith("acp-conflict")
	if _, err := mk(dir, kindCreate).Exec(ctx, json.RawMessage(`{
		"title":"原始","prompt":"x","rrule":"DTSTART:20260101T090000\nRRULE:FREQ=DAILY","target":"this_session"}`)); err != nil {
		t.Fatal(err)
	}
	items, _ := (&store{dir: dir}).load("acp-conflict")
	id := items[0].ID
	before := items[0]

	res, err := mk(dir, kindEdit).Exec(ctx, json.RawMessage(
		`{"id":"`+id+`","action":"update","title":"被拦住的名字","expectedUpdatedAt":"2020-01-01T00:00:00Z"}`))
	if err != nil {
		t.Fatalf("冲突不该是错误（官方是 200 + schedule_conflict）: %v", err)
	}
	m, _ := res.Data.(map[string]any)
	if m["code"] != "schedule_conflict" {
		t.Fatalf("冲突结果码 = %v", m["code"])
	}
	items, _ = (&store{dir: dir}).load("acp-conflict")
	if items[0].Title != "原始" || items[0].UpdatedAt != before.UpdatedAt {
		t.Fatalf("冲突时不该写盘: %+v", items[0])
	}

	// 拿对 updated_at 就能改。
	if _, err := mk(dir, kindEdit).Exec(ctx, json.RawMessage(
		`{"id":"`+id+`","action":"update","title":"改好了","expectedUpdatedAt":"`+before.UpdatedAt+`"}`)); err != nil {
		t.Fatalf("正确的 expectedUpdatedAt 应当能改: %v", err)
	}
	items, _ = (&store{dir: dir}).load("acp-conflict")
	if items[0].Title != "改好了" {
		t.Fatalf("没改上: %+v", items[0])
	}
}

// edit 换 rrule：kind 仍是 rrule、旧固定周期字段被清掉、下一次按新规则算。
func TestEditRRuleSwitchesRecurrence(t *testing.T) {
	dir := t.TempDir()
	ctx := ctxWith("acp-switch")
	if _, err := mk(dir, kindCreate).Exec(ctx, json.RawMessage(`{
		"title":"换规则","prompt":"x","rrule":"DTSTART:20260101T090000\nRRULE:FREQ=DAILY","target":"this_session"}`)); err != nil {
		t.Fatal(err)
	}
	items, _ := (&store{dir: dir}).load("acp-switch")
	id := items[0].ID

	if _, err := mk(dir, kindEdit).Exec(ctx, json.RawMessage(
		`{"id":"`+id+`","action":"update","rrule":"DTSTART:20260102T140000\nRRULE:FREQ=WEEKLY;BYDAY=TU,TH"}`)); err != nil {
		t.Fatalf("换 rrule 失败: %v", err)
	}
	items, _ = (&store{dir: dir}).load("acp-switch")
	it := items[0]
	if it.Kind != kindRRule || !strings.Contains(it.RRule, "BYDAY=TU,TH") {
		t.Fatalf("新规则没落盘: kind=%q rrule=%q", it.Kind, it.RRule)
	}
	if it.EverySec != 0 || it.DailyAt != "" || it.WeeklyAt != "" || len(it.Weekdays) != 0 {
		t.Fatalf("换了 rrule 还留着旧固定周期字段: %+v", it)
	}
	next, err := time.Parse(time.RFC3339, it.NextAt)
	if err != nil {
		t.Fatal(err)
	}
	if next.Weekday() != time.Tuesday && next.Weekday() != time.Thursday {
		t.Fatalf("下一次 = %s（%s），不是 BYDAY 里的周二/周四", next, next.Weekday())
	}
	if h, m, _ := next.Clock(); h != 14 || m != 0 {
		t.Fatalf("下一次时刻 = %02d:%02d，不是新 DTSTART 的 14:00", h, m)
	}
}

// edit action=delete 与旧别名 schedule_delete / schedule_update 必须是同一条逻辑。
func TestEditDeleteAndLegacyAliases(t *testing.T) {
	dir := t.TempDir()
	ctx := ctxWith("acp-alias")
	// 旧形状 create（兼容别名）仍然可用。
	if _, err := mk(dir, kindCreate).Exec(ctx, json.RawMessage(
		`{"title":"旧别名","prompt":"x","kind":"every","every_seconds":300}`)); err != nil {
		t.Fatalf("旧形状 create 失效了（能力下限）: %v", err)
	}
	items, _ := (&store{dir: dir}).load("acp-alias")
	if items[0].Kind != kindEvery {
		t.Fatalf("旧形状 kind = %q", items[0].Kind)
	}
	id := items[0].ID

	// schedule_update 别名 → 改名字
	if _, err := mk(dir, kindUpdate).Exec(ctx, json.RawMessage(`{"id":"`+id+`","title":"别名改的"}`)); err != nil {
		t.Fatalf("schedule_update 别名失效: %v", err)
	}
	items, _ = (&store{dir: dir}).load("acp-alias")
	if items[0].Title != "别名改的" {
		t.Fatalf("别名没改上: %+v", items[0])
	}

	// schedule_edit action=delete 删掉
	if _, err := mk(dir, kindEdit).Exec(ctx, json.RawMessage(`{"id":"`+id+`","action":"delete"}`)); err != nil {
		t.Fatalf("edit delete 失败: %v", err)
	}
	if items, _ := (&store{dir: dir}).load("acp-alias"); len(items) != 0 {
		t.Fatalf("edit delete 没删掉: %d 条", len(items))
	}

	// schedule_delete 别名也删得掉（再建一条）
	if _, err := mk(dir, kindCreate).Exec(ctx, json.RawMessage(
		`{"title":"再建","prompt":"x","kind":"every","every_seconds":300}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := mk(dir, kindDelete).Exec(ctx, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("schedule_delete 别名失效（只有一条时应当能省 id）: %v", err)
	}
	if items, _ := (&store{dir: dir}).load("acp-alias"); len(items) != 0 {
		t.Fatalf("别名没删掉: %d 条", len(items))
	}

	// edit 的 action 缺省/非法必须报错并说清可选值。
	res, err := mk(dir, kindEdit).Exec(ctx, json.RawMessage(`{"id":"x"}`))
	if err == nil {
		t.Fatal("缺 action 必须失败")
	}
	if !strings.Contains(res.Hint, "update") || !strings.Contains(res.Hint, "delete") {
		t.Fatalf("Hint 要列出 update/delete: %q", res.Hint)
	}
}

// list：active 过滤 + limit/offset 分页（官方三个可选参数都得真有作用）。
func TestListFiltersAndPaginates(t *testing.T) {
	dir := t.TempDir()
	ctx := ctxWith("acp-page")
	for i := 0; i < 3; i++ {
		if _, err := mk(dir, kindCreate).Exec(ctx, json.RawMessage(
			`{"title":"任务`+string(rune('A'+i))+`","prompt":"x","kind":"every","every_seconds":300}`)); err != nil {
			t.Fatal(err)
		}
	}
	items, _ := (&store{dir: dir}).load("acp-page")
	// 暂停第二条
	if _, err := mk(dir, kindEdit).Exec(ctx, json.RawMessage(
		`{"id":"`+items[1].ID+`","action":"update","active":false}`)); err != nil {
		t.Fatal(err)
	}

	res, err := mk(dir, kindList).Exec(ctx, json.RawMessage(`{"limit":1}`))
	if err != nil {
		t.Fatal(err)
	}
	m, _ := res.Data.(map[string]any)
	if m["total"].(int) != 3 || m["limit"].(int) != 1 || m["has_more"].(bool) != true {
		t.Fatalf("分页字段不对: %+v", m)
	}
	page, _ := m["schedules"].([]Schedule)
	if len(page) != 1 {
		t.Fatalf("limit=1 返回 %d 条", len(page))
	}
	// 活跃的排前面：第一页必须是活跃任务。
	if !isActive(page[0]) {
		t.Fatalf("第一页第一条不是活跃任务: %+v", page[0])
	}

	res, err = mk(dir, kindList).Exec(ctx, json.RawMessage(`{"offset":2,"limit":10}`))
	if err != nil {
		t.Fatal(err)
	}
	m, _ = res.Data.(map[string]any)
	if m["offset"].(int) != 2 || m["has_more"].(bool) != false {
		t.Fatalf("offset 没生效: %+v", m)
	}
	if page, _ := m["schedules"].([]Schedule); len(page) != 1 {
		t.Fatalf("offset=2 应只剩 1 条，实际 %d", len(page))
	}

	res, err = mk(dir, kindList).Exec(ctx, json.RawMessage(`{"active":false}`))
	if err != nil {
		t.Fatal(err)
	}
	m, _ = res.Data.(map[string]any)
	if m["total"].(int) != 1 {
		t.Fatalf("active=false 应当只看到 1 条暂停的，实际 %v", m["total"])
	}
	if page, _ := m["schedules"].([]Schedule); page[0].Status != schedulestore.StatusPaused {
		t.Fatalf("active=false 回来的不是暂停任务: %+v", page[0])
	}
}

// schedule_view 必须声明并发安全（官方 isConcurrencySafe: () => true），
// 而 edit 与两个写别名必须独占。
func TestViewIsConcurrencySafe(t *testing.T) {
	dir := t.TempDir()
	if !spi.ConcurrencySafe(mk(dir, kindView), json.RawMessage(`{}`)) {
		t.Fatal("schedule_view 必须声明并发安全（官方 isConcurrencySafe: () => true）")
	}
	for _, kind := range []string{kindEdit, kindUpdate, kindDelete} {
		if spi.ConcurrencySafe(mk(dir, kind), json.RawMessage(`{}`)) {
			t.Fatalf("%s 会读-改-写同一份文件，必须独占", kind)
		}
	}
	if mk(dir, kindView).Caps().NeedsApproval() {
		t.Fatal("schedule_view 只读，不该要审批")
	}
}
