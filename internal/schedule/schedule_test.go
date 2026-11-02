package schedule

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// every：从登记的时间一格一格往前推，跨过 now；不许原地不动（会立刻再触发一次）。
func TestNextAfterEvery(t *testing.T) {
	it := Item{Kind: "every", EverySec: 300, NextAt: "2026-11-03T05:44:17+08:00"}
	got := nextAfter(it, at("2026-11-03T05:44:17+08:00"))
	if !got.After(at("2026-11-03T05:44:17+08:00")) {
		t.Fatalf("下一次必须严格晚于 now，得到 %v", got)
	}
	if want := at("2026-11-03T05:49:17+08:00"); !got.Equal(want) {
		t.Fatalf("下一次 = %v，期望 %v", got, want)
	}
}

// every 落后好几格时，要一次推到 now 之后，而不是只加一格。
func TestNextAfterEveryCatchesUp(t *testing.T) {
	it := Item{Kind: "every", EverySec: 60, NextAt: "2026-11-03T05:00:00+08:00"}
	got := nextAfter(it, at("2026-11-03T05:10:30+08:00"))
	if want := at("2026-11-03T05:11:00+08:00"); !got.Equal(want) {
		t.Fatalf("赶上后 = %v，期望 %v", got, want)
	}
}

// daily：当天时刻已过就明天，没过就今天。
func TestNextAfterDaily(t *testing.T) {
	it := Item{Kind: "daily", DailyAt: "23:59"}
	if got, want := nextAfter(it, at("2026-11-03T05:00:00+08:00")), at("2026-11-03T23:59:00+08:00"); !got.Equal(want) {
		t.Fatalf("没过点 = %v，期望 %v", got, want)
	}
	if got, want := nextAfter(it, at("2026-11-04T00:30:00+08:00")), at("2026-11-04T23:59:00+08:00"); !got.Equal(want) {
		t.Fatalf("过了点 = %v，期望 %v", got, want)
	}
}

// weekly：只在指定星期触发；当天时刻没过也算。
func TestNextAfterWeekly(t *testing.T) {
	it := Item{Kind: "weekly", WeeklyAt: "08:00", Weekdays: []int{1, 3}} // 周一、周三
	// 2026-11-03 是周二 → 下一个是周三 08:00
	got := nextAfter(it, at("2026-11-03T05:00:00+08:00"))
	if want := at("2026-11-04T08:00:00+08:00"); !got.Equal(want) {
		t.Fatalf("周二之后 = %v，期望 %v", got, want)
	}
}

// 坏输入不许 panic，也不许返回零值时间（那会让任务疯狂触发）。
func TestBadInputsDoNotPanic(t *testing.T) {
	for _, it := range []Item{
		{Kind: "every", EverySec: 0, NextAt: "不是时间"},
		{Kind: "daily", DailyAt: "25:99"},
		{Kind: "weekly", WeeklyAt: "x", Weekdays: nil},
		{Kind: "unknown"},
	} {
		got := nextAfter(it, at("2026-11-03T05:00:00+08:00"))
		if !got.After(at("2026-11-03T05:00:00+08:00")) {
			t.Fatalf("%+v 的兜底也必须晚于 now，得到 %v", it, got)
		}
	}
}

// 到期判定 + 推进要能落盘，且只推进到期的那些。
func TestDueAtAndAdvance(t *testing.T) {
	dir := t.TempDir()
	doc := `{"schedules":[
	  {"id":"sch-1","kind":"every","every_seconds":300,"next_at":"2026-11-03T05:00:00+08:00"},
	  {"id":"sch-2","kind":"daily","daily_at":"23:59","next_at":"2026-11-03T23:59:00+08:00"}
	]}`
	if err := os.WriteFile(filepath.Join(dir, "s1.json"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	items, err := Load(dir, "s1")
	if err != nil {
		t.Fatal(err)
	}
	now := at("2026-11-03T05:10:00+08:00")
	dues := DueAt(items, now)
	if len(dues) != 1 || dues[0].Item.ID != "sch-1" {
		t.Fatalf("只有 sch-1 到期，得到 %+v", dues)
	}
	if n, err := Advance(dir, "s1", dues, now); err != nil || n != 1 {
		t.Fatalf("推进失败: n=%d err=%v", n, err)
	}
	back, _ := Load(dir, "s1")
	if back[0].NextAt != "2026-11-03T05:15:00+08:00" {
		t.Fatalf("sch-1 下次 = %q，期望 05:15", back[0].NextAt)
	}
	if back[1].NextAt != "2026-11-03T23:59:00+08:00" {
		t.Fatalf("没到期的不该被改：%q", back[1].NextAt)
	}
}

// 目录不存在时是"没有任务"，不是错误。
func TestLoadMissingDir(t *testing.T) {
	items, err := Load(filepath.Join(t.TempDir(), "nope"), "x")
	if err != nil || items != nil {
		t.Fatalf("应当返回空且无错: %v %v", items, err)
	}
}
