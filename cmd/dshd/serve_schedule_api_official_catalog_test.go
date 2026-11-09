package main

// T03：官方 schedule.catalog / deleteInactive / deliveryLimits 在宿主侧的**真实实现**测试。
//
// 全部走真实临时 home + 真实落盘文件（不 mock 存储）：
//   - catalog 的"全量"必须是磁盘上的**全部**任务（跨会话文件），排序照官方
//     compareEntries（active 先、scheduledAt 升序；非 active 按最近一次变化倒序）；
//   - deleteInactive 只删 status=ended —— paused 是可恢复的，必须留着；
//   - deliveryLimits 必须等于宿主**真实强制执行**的登记下限，不许编一个数。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"dshgo/internal/schedule"
	"dshgo/internal/transport"
)

// createScheduleForTest 用真宿主建一条任务，失败直接 Fatal。
func createScheduleForTest(t *testing.T, fx *scheduleHostFixture, req transport.OfficialScheduleCreateRequest) transport.OfficialScheduleEntry {
	t.Helper()
	entry, err := fx.host.api().Create(req)
	if err != nil {
		t.Fatalf("Create(%s/%s): %v", req.Title, req.Timing.Kind, err)
	}
	return entry
}

func TestScheduleHostCatalogIsFullOrderedAndReadOnly(t *testing.T) {
	home := scheduleHome(t)
	fx := newScheduleHostFixture(t, home)

	// 空 home：全量目录是空数组（非 nil、无错误），不是 501 也不是 null。
	empty, err := fx.host.api().Catalog()
	if err != nil {
		t.Fatalf("空目录 Catalog: %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("空目录 Catalog = %#v，want 非 nil 空切片", empty)
	}

	targetS1 := transport.OfficialScheduleTarget{Kind: "session", SessionID: "s1"}
	targetS2 := transport.OfficialScheduleTarget{Kind: "session", SessionID: "s2"}
	slow := createScheduleForTest(t, fx, transport.OfficialScheduleCreateRequest{
		Title: "慢", Prompt: "slow",
		Timing: transport.OfficialScheduleTiming{Kind: "every", EverySeconds: 600},
		Target: &targetS1,
	})
	fast := createScheduleForTest(t, fx, transport.OfficialScheduleCreateRequest{
		Title: "快", Prompt: "fast",
		Timing: transport.OfficialScheduleTiming{Kind: "every", EverySeconds: 60},
		Target: &targetS2,
	})
	daily := createScheduleForTest(t, fx, transport.OfficialScheduleCreateRequest{
		Title: "每天", Prompt: "daily",
		Timing: transport.OfficialScheduleTiming{Kind: "daily",
			Daily: &transport.OfficialScheduleDailyTime{Time: "10:00", TimeZone: "UTC"}},
		Target: &targetS1,
	})

	// 全量：跨会话文件（s1 两条、s2 一条）都在；active 按 scheduledAt 升序。
	entries, err := fx.host.api().Catalog()
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("Catalog 条数 = %d，want 3（全量、跨会话文件）: %#v", len(entries), entries)
	}
	gotOrder := []string{entries[0].ID, entries[1].ID, entries[2].ID}
	wantOrder := []string{fast.ID, slow.ID, daily.ID}
	if strings.Join(gotOrder, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("active 排序 = %v，want %v（按 scheduledAt 升序）", gotOrder, wantOrder)
	}
	byID := map[string]transport.OfficialScheduleEntry{}
	for _, entry := range entries {
		byID[entry.ID] = entry
	}
	if got := byID[fast.ID]; got.Prompt != "fast" || got.Target.SessionID != "s2" ||
		got.Status != schedule.StatusActive || got.ScheduledAt != fast.ScheduledAt {
		t.Fatalf("catalog 里的 fast 与 create 回包不一致: %#v", got)
	}
	if got := byID[daily.ID]; got.Kind != "daily" || got.Time != "10:00:00" || got.TimeZone != "UTC" {
		t.Fatalf("catalog 里的 daily 形状错: %#v", got)
	}
	if got := byID[daily.ID]; got.InvolvedSessionIDs == nil {
		t.Fatal("catalog 条目的 involvedSessionIds 是 nil（编码后会变 null）")
	}
	for _, entry := range entries {
		if entry.LastDelivery != nil {
			t.Fatalf("还没投递过就有 lastDelivery: %#v", entry.LastDelivery)
		}
	}

	// 暂停两条（先 daily 后 slow）：非 active 按最近一次变化倒序，slow 在前。
	off := false
	fx.now = time.Date(2026, 11, 7, 9, 10, 0, 0, time.Local)
	if _, err := fx.host.api().Edit(transport.OfficialScheduleEditRequest{
		ID: daily.ID, Action: "update", Patch: &transport.OfficialSchedulePatch{Active: &off},
	}); err != nil {
		t.Fatalf("暂停 daily: %v", err)
	}
	fx.now = time.Date(2026, 11, 7, 9, 20, 0, 0, time.Local)
	if _, err := fx.host.api().Edit(transport.OfficialScheduleEditRequest{
		ID: slow.ID, Action: "update", Patch: &transport.OfficialSchedulePatch{Active: &off},
	}); err != nil {
		t.Fatalf("暂停 slow: %v", err)
	}
	entries, err = fx.host.api().Catalog()
	if err != nil {
		t.Fatalf("暂停后 Catalog: %v", err)
	}
	gotOrder = []string{entries[0].ID, entries[1].ID, entries[2].ID}
	wantOrder = []string{fast.ID, slow.ID, daily.ID}
	if strings.Join(gotOrder, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("暂停后排序 = %v，want %v（active 先；非 active 按最近变化倒序）", gotOrder, wantOrder)
	}
	if entries[1].Status != schedule.StatusPaused || entries[2].Status != schedule.StatusPaused {
		t.Fatalf("暂停后 status 没体现: %#v", entries)
	}

	// 真投递一次：catalog 必须回真实 lastDelivery（与 history 侧车同一份字节）。
	if _, err := fx.host.api().Run(fast.ID); err != nil {
		t.Fatalf("Run(fast): %v", err)
	}
	if fx.submit.count() != 1 {
		t.Fatalf("Run 没真触发提交: %d", fx.submit.count())
	}

	// 读路径不许写盘：catalog 前后两个会话文件逐字节相同。
	before := map[string][]byte{}
	for _, sessionID := range []string{"s1", "s2"} {
		raw, err := os.ReadFile(scheduleSessionFile(home, sessionID))
		if err != nil {
			t.Fatalf("读 %s 基线: %v", sessionID, err)
		}
		before[sessionID] = raw
	}
	entries, err = fx.host.api().Catalog()
	if err != nil {
		t.Fatalf("投递后 Catalog: %v", err)
	}
	for _, sessionID := range []string{"s1", "s2"} {
		after, err := os.ReadFile(scheduleSessionFile(home, sessionID))
		if err != nil {
			t.Fatalf("读 %s 现状: %v", sessionID, err)
		}
		if !bytes.Equal(before[sessionID], after) {
			t.Fatalf("catalog 写盘了：%s 在只读调用前后不一致", scheduleSessionFile(home, sessionID))
		}
	}
	byID = map[string]transport.OfficialScheduleEntry{}
	for _, entry := range entries {
		byID[entry.ID] = entry
	}
	delivered := byID[fast.ID].LastDelivery
	if delivered == nil || delivered.MessageID != "turn-1" || delivered.TargetSessionID != "s2" {
		t.Fatalf("catalog 的 lastDelivery 不是真实投递凭据: %#v", delivered)
	}

	// catalog 与 view 必须是同一条投影：字段集合/值完全一致。
	viewedAny, err := fx.host.api().View(slow.ID)
	if err != nil {
		t.Fatalf("View(slow): %v", err)
	}
	if !reflect.DeepEqual(scheduleEntryOf(t, viewedAny), byID[slow.ID]) {
		t.Fatalf("catalog 与 view 对同一条任务的投影不一致:\n catalog=%#v\n view=%#v",
			byID[slow.ID], viewedAny)
	}
}

func TestScheduleHostDeleteInactiveOnlyRemovesEnded(t *testing.T) {
	home := scheduleHome(t)
	fx := newScheduleHostFixture(t, home)

	targetS1 := transport.OfficialScheduleTarget{Kind: "session", SessionID: "s1"}
	targetS2 := transport.OfficialScheduleTarget{Kind: "session", SessionID: "s2"}
	active := createScheduleForTest(t, fx, transport.OfficialScheduleCreateRequest{
		Title: "活跃", Prompt: "active",
		Timing: transport.OfficialScheduleTiming{Kind: "every", EverySeconds: 300},
		Target: &targetS1,
	})
	paused := createScheduleForTest(t, fx, transport.OfficialScheduleCreateRequest{
		Title: "暂停", Prompt: "paused",
		Timing: transport.OfficialScheduleTiming{Kind: "every", EverySeconds: 600},
		Target: &targetS1,
	})
	oneShot := createScheduleForTest(t, fx, transport.OfficialScheduleCreateRequest{
		Title: "一次性", Prompt: "one-shot",
		Timing: transport.OfficialScheduleTiming{Kind: "after", AfterSeconds: 900},
		Target: &targetS2,
	})
	off := false
	if _, err := fx.host.api().Edit(transport.OfficialScheduleEditRequest{
		ID: paused.ID, Action: "update", Patch: &transport.OfficialSchedulePatch{Active: &off},
	}); err != nil {
		t.Fatalf("暂停: %v", err)
	}

	// 让一次性任务真到点结束：advance 之后 status=ended（不是靠测试手写状态）。
	dueAt := scheduleParse(t, oneShot.ScheduledAt).Add(time.Second)
	dues := schedule.DueAt(scheduleLoadDir(t, home, "s2"), dueAt)
	if len(dues) != 1 {
		t.Fatalf("到点应当有 1 条 due，得到 %d", len(dues))
	}
	if _, err := schedule.Advance(filepath.Join(home, "state", "schedules"), "s2", dues, dueAt); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if items := scheduleLoadDir(t, home, "s2"); len(items) != 1 || items[0].Status != schedule.StatusEnded {
		t.Fatalf("一次性任务没变成 ended: %#v", items)
	}

	// 只删 ended：deleted=1，且跨会话文件（s2）也真删。
	res, err := fx.host.api().DeleteInactive()
	if err != nil {
		t.Fatalf("DeleteInactive: %v", err)
	}
	if res.Deleted != 1 {
		t.Fatalf("deleted = %d，want 1", res.Deleted)
	}
	if _, ok := readStoredItem(t, home, "s2", oneShot.ID); ok {
		t.Fatal("ended 任务还在 s2 文件里")
	}

	// active 与 paused 一个都不许动：官方只删 ended，paused 是可恢复的。
	if _, ok := readStoredItem(t, home, "s1", active.ID); !ok {
		t.Fatal("active 任务被误删")
	}
	pausedItem, ok := readStoredItem(t, home, "s1", paused.ID)
	if !ok {
		t.Fatal("paused 任务被误删（官方 deleteInactive 只删 ended）")
	}
	if pausedItem.Status != schedule.StatusPaused {
		t.Fatalf("paused 任务状态被改写: %#v", pausedItem)
	}

	// 没有 ended 之后必须回 0（0 是成功，不是错误），且文件不变。
	beforePaused, err := os.ReadFile(scheduleSessionFile(home, "s1"))
	if err != nil {
		t.Fatalf("读 s1 基线: %v", err)
	}
	res, err = fx.host.api().DeleteInactive()
	if err != nil {
		t.Fatalf("第二次 DeleteInactive: %v", err)
	}
	if res.Deleted != 0 {
		t.Fatalf("第二次 deleted = %d，want 0", res.Deleted)
	}
	afterPaused, err := os.ReadFile(scheduleSessionFile(home, "s1"))
	if err != nil {
		t.Fatalf("读 s1 现状: %v", err)
	}
	if !bytes.Equal(beforePaused, afterPaused) {
		t.Fatal("没有 ended 时 deleteInactive 也改写了日程文件（应当是 0 次写入）")
	}

	// 目录都不存在时也是 0 + nil error（不是"文件不存在"的 500）。
	bareHome := t.TempDir()
	bareHost := newOfficialScheduleHost(officialScheduleDeps{home: bareHome})
	res, err = bareHost.api().DeleteInactive()
	if err != nil {
		t.Fatalf("空 home DeleteInactive: %v", err)
	}
	if res.Deleted != 0 {
		t.Fatalf("空 home deleted = %d，want 0", res.Deleted)
	}
	if _, err := os.Stat(bareHome + "/state/schedules"); !os.IsNotExist(err) {
		t.Fatalf("deleteInactive 不该凭空建日程目录: %v", err)
	}
}

func TestScheduleHostDeliveryLimitsMatchesAdmissionFloor(t *testing.T) {
	home := scheduleHome(t)
	fx := newScheduleHostFixture(t, home)

	limits, err := fx.host.api().DeliveryLimits()
	if err != nil {
		t.Fatalf("DeliveryLimits: %v", err)
	}
	// 必须是 transport 入口真实拒掉的那条线，不是宿主里另写的一个数。
	if want := int64(transport.MinScheduleEverySeconds()); limits.NewSessionIntervalSeconds != want {
		t.Fatalf("newSessionIntervalSeconds = %d，want %d（MinScheduleEverySeconds）",
			limits.NewSessionIntervalSeconds, want)
	}
	if limits.NewSessionIntervalSeconds != 60 {
		t.Fatalf("newSessionIntervalSeconds = %d，want 官方默认/本宿主下限 60 秒",
			limits.NewSessionIntervalSeconds)
	}

	// HTTP 形状：无参方法的空 body 与 {} 都是同一个意思，结果只有那一个字段。
	post := func(body string) (int, map[string]any, map[string]any) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/schedule.deliveryLimits", bytes.NewBufferString(body))
		rec := httptest.NewRecorder()
		transport.ServeOfficialSchedule(rec, req, "schedule.deliveryLimits", fx.host.api())
		var envelope map[string]any
		if rec.Body.Len() > 0 {
			if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("响应不是 JSON 对象: %q", rec.Body.String())
			}
		}
		return rec.Code, envelope, envelope
	}
	for _, body := range []string{"", "{}"} {
		code, envelope, _ := post(body)
		if code != http.StatusOK {
			t.Fatalf("body=%q 状态 = %d，want 200: %#v", body, code, envelope)
		}
		result, ok := envelope["result"].(map[string]any)
		if !ok {
			t.Fatalf("body=%q 的成功响应没有 result 对象: %#v", body, envelope)
		}
		if result["newSessionIntervalSeconds"] != float64(60) {
			t.Fatalf("body=%q 结果 = %#v，want {newSessionIntervalSeconds:60}", body, result)
		}
		if len(result) != 1 {
			t.Fatalf("结果只能是 {newSessionIntervalSeconds}（官方 zod 只有这一个字段）: %#v", result)
		}
	}

	// 读限额不加载、不激活、更不创建任何任务：目录里一条都没有。
	entries, err := os.ReadDir(filepath.Join(home, "state", "schedules"))
	if err != nil {
		t.Fatalf("读 schedules 目录: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("deliveryLimits 不该产生任务文件: %v", entries)
	}
}

// new / new_once 的 target 分支在官方 zod 里把 `create` 列为 required：
// catalog 把条目原样发到线上，所以 `create` 键必须在（空对象也算）。
func TestScheduleHostCatalogNewTargetKeepsCreateKey(t *testing.T) {
	home := scheduleHome(t)
	fx := newScheduleHostFixture(t, home)

	created := createScheduleForTest(t, fx, transport.OfficialScheduleCreateRequest{
		Title: "每次新建", Prompt: "new-target",
		Timing: transport.OfficialScheduleTiming{Kind: "every", EverySeconds: 300},
		Target: &transport.OfficialScheduleTarget{
			Kind:   "new",
			Create: &transport.OfficialScheduleCreateInfo{CWD: "/tmp/project", AgentPreset: "preset-a"},
		},
	})
	if created.Target.Create == nil {
		t.Fatalf("create 回包的 new target 少了 create 键: %#v", created.Target)
	}

	// 线上形状（catalog 是数组）：target 是对象，且带 create 对象。
	req := httptest.NewRequest(http.MethodPost, "/api/schedule.catalog", bytes.NewBufferString(`{}`))
	rec := httptest.NewRecorder()
	transport.ServeOfficialSchedule(rec, req, "schedule.catalog", fx.host.api())
	if rec.Code != http.StatusOK {
		t.Fatalf("catalog 状态 = %d: %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Result []struct {
			ID     string                     `json:"id"`
			Target map[string]json.RawMessage `json:"target"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("catalog 响应不是 JSON: %v", err)
	}
	var target map[string]json.RawMessage
	for _, entry := range envelope.Result {
		if entry.ID == created.ID {
			target = entry.Target
		}
	}
	if target == nil {
		t.Fatalf("catalog 里没有 id=%s: %s", created.ID, rec.Body.String())
	}
	if string(target["kind"]) != `"new"` {
		t.Fatalf("target.kind = %s，want \"new\"", target["kind"])
	}
	createRaw, ok := target["create"]
	if !ok {
		t.Fatalf("target 少了 required 的 create 键: %s", rec.Body.String())
	}
	var createObj map[string]any
	if err := json.Unmarshal(createRaw, &createObj); err != nil || createObj == nil {
		t.Fatalf("target.create 不是对象: %s", createRaw)
	}
	if createObj["cwd"] != "/tmp/project" || createObj["agentPreset"] != "preset-a" {
		t.Fatalf("target.create 丢了真实参数: %#v", createObj)
	}
}
