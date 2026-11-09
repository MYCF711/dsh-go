package transport

// HS-16B 先红（schedule 域）：字段语义与官方 zod 契约不一致的三处。
//
// 契约原件（只读镜像）：
//
//	dsh-schedule/lib/typert.host.js  7 个 kind 分支的 required 列表
//	  after  : ["id","kind","title","prompt","afterSeconds","scheduledAt",
//	            "target","involvedSessionIds","status"]
//	  daily  : [... "time","timeZone" ...]
//	  weekly : [... "time","timeZone","weekdays" ...]
//	  cron   : [... "expression","timeZone" ...]
//	  rrule  : ["kind","id","title","prompt","rrule","scheduledAt", …]
//	  即：**kind 专属字段是 required**。`afterSeconds:0` / `time:"00:00:00.000"` /
//	  `timeZone:"UTC"` / `weekdays:[1]` / `rrule:"…"` 都必须出现在结果里。
//	dsh-schedule/lib/typert.host.js  history 参数：
//	  `before` 是"上一页最旧一条的 messageId"，**排他游标**；官方 union 还有一个
//	  专门分支 `delivery_cursor_not_found`。
//
// 红点（当前实现与官方不一致）：
//
//	1. `OfficialScheduleEntry` 的 kind 专属字段全带 `omitempty`：
//	   `at` 之外任何一种任务，只要值恰好是 Go 零值（after_seconds 不可能、
//	   但 time/weekdays/rrule/cron **可以是**）就从 JSON 里消失，客户端拿到的
//	   对象缺 required 字段 → 官方 zod 直接解析失败。
//	2. history 的 `before` 命中不了时回的是 `delivery_cursor_not_found`，
//	   但 `cmd/dshd` 侧把 cursor miss 写成了 `schedule_not_found`，
//	   `checkScheduleResultType` 也接受任意 `OfficialScheduleMiss`——
//	   两种含义（任务不存在 / 游标不存在）在 wire 上不可区分。
//	3. rrule 的 timeZone 在 entry 里根本没有字段，官方 rrule 分支没有 timeZone，
//	   但 daily/weekly/cron 需要；`TimeZone` 带 omitempty 时 `UTC`（合法值）
//	   只要被当成空串就消失（见 1）。

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestOfficialScheduleEntryKeepsKindRequiredFields 任一 kind 的 required 字段
// 都必须出现在 wire 上，即使它的值是零值。
func TestOfficialScheduleEntryKeepsKindRequiredFields(t *testing.T) {
	// 关键点：`Time`/`TimeZone`/`Weekdays` 用 omitempty 表达"这个 kind 没有它"，
	// 于是一个**合法的空值**（timeZone 空串在官方是 required 字段的零值）被误删。
	// 官方 zod 的 required 列表要求字段存在，值可以是空串——形状错就解析失败。
	entries := []OfficialScheduleEntry{
		{ID: "a", Kind: "daily", Title: "t", Prompt: "p", Time: "00:00:00.000", TimeZone: "UTC",
			ScheduledAt: "2026-11-08T00:00:00Z", Status: "active",
			Target: OfficialScheduleTarget{Kind: "session", SessionID: "s1"}},
		{ID: "w", Kind: "weekly", Title: "t", Prompt: "p", Time: "09:00:00.000", TimeZone: "UTC",
			Weekdays: []int{1}, ScheduledAt: "2026-11-09T01:00:00Z", Status: "active",
			Target: OfficialScheduleTarget{Kind: "session", SessionID: "s1"}},
		{ID: "c", Kind: "cron", Title: "t", Prompt: "p", Expression: "0 9 * * *", TimeZone: "",
			ScheduledAt: "2026-11-08T01:00:00Z", Status: "active",
			Target: OfficialScheduleTarget{Kind: "session", SessionID: "s1"}},
		{ID: "r", Kind: "rrule", Title: "t", Prompt: "p",
			RRule:       "DTSTART:20261108T010000Z\nRRULE:FREQ=DAILY",
			ScheduledAt: "2026-11-09T01:00:00Z", Status: "active",
			Target: OfficialScheduleTarget{Kind: "session", SessionID: "s1"}},
	}
	for _, entry := range entries {
		raw, err := json.Marshal(entry)
		if err != nil {
			t.Fatalf("%s 序列化失败: %v", entry.Kind, err)
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil {
			t.Fatalf("%s 反序列化失败: %v", entry.Kind, err)
		}
		required := map[string][]string{
			"daily":  {"time", "timeZone"},
			"weekly": {"time", "timeZone", "weekdays"},
			"cron":   {"expression", "timeZone"},
			"rrule":  {"rrule"},
		}[entry.Kind]
		for _, key := range required {
			if _, has := obj[key]; !has {
				t.Fatalf("%s 的 required 字段 %q 丢了（kind 专属字段不许 omitempty）：%s", entry.Kind, key, raw)
			}
		}
	}
}

// TestOfficialScheduleHistoryCursorMissIsDistinguishable
// 官方 union 里"游标不存在"是 `delivery_cursor_not_found`，"任务不存在"是
// `schedule_not_found`。两者必须能被客户端分开，而且**都只能是那两个字面量**。
func TestOfficialScheduleHistoryCursorMissIsDistinguishable(t *testing.T) {
	srv := scheduleOfficialServer(t, OfficialScheduleAPI{
		History: func(req OfficialScheduleHistoryRequest) (any, error) {
			return OfficialScheduleMiss{ID: req.ID, Code: "delivery_cursor_not_found"}, nil
		},
	})
	code, out := callScheduleOfficial(t, srv, "schedule.history", `{"id":"a","limit":10,"before":"msg-x"}`)
	if code != http.StatusOK {
		t.Fatalf("cursor miss 是 union 成员（200），实际 %d：%v", code, out)
	}
	if got, _ := out["code"].(string); got != "delivery_cursor_not_found" {
		t.Fatalf("code = %q，want delivery_cursor_not_found", got)
	}

	// 非法 code 必须被拦（官方只有 4 个 union 码），不能原样上线。
	srv = scheduleOfficialServer(t, OfficialScheduleAPI{
		History: func(req OfficialScheduleHistoryRequest) (any, error) {
			return OfficialScheduleMiss{ID: req.ID, Code: "whatever"}, nil
		},
	})
	code, out = callScheduleOfficial(t, srv, "schedule.history", `{"id":"a","limit":10}`)
	if code == http.StatusOK {
		t.Fatalf("宿主返回非官方 code 必须报 500（不许原样上线）：%v", out)
	}
}
