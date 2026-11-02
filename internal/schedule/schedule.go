// Package schedule 让"登记过的定时任务"真的会到期。
//
// 分工：
//   - `modules/tool/schedule` 只负责**登记**（create/list/update/delete，算出 next_at）
//   - 这个包负责**到点判定**与**推进 next_at**
//
// 为什么不做成常驻 ticker：宿主的两种运行形态（`run` 一次性、`acp` 长连）
// 都没有"宿主主动发起一轮对话"的通道。硬起 goroutine 只会得到一个
// 会自己调模型、但没人能插话的怪东西。
// 所以这里只回答两个问题——"现在有谁到期了"和"把它们推到下一次"——
// 由调用方（cron、shell 循环、以后的 ACP 客户端）决定怎么处理。
package schedule

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Item 与 modules/tool/schedule 里的结构逐字对齐：两边读的是同一份 JSON。
type Item struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Prompt   string `json:"prompt"`
	Kind     string `json:"kind"`
	EverySec int    `json:"every_seconds,omitempty"`
	DailyAt  string `json:"daily_at,omitempty"`
	Weekdays []int  `json:"weekdays,omitempty"`
	WeeklyAt string `json:"weekly_at,omitempty"`
	NextAt   string `json:"next_at"`
}

// Due 是一个到期的任务，带上它的归属会话。
type Due struct {
	Session string
	Item    Item
	Next    time.Time
}

// Load 读某个会话的定时任务；没有文件就是空的。
func Load(dir, sessionID string) ([]Item, error) {
	b, err := os.ReadFile(filepath.Join(dir, sessionID+".json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var doc struct {
		Schedules []Item `json:"schedules"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("定时任务文件坏了（%s）: %w", sessionID+".json", err)
	}
	return doc.Schedules, nil
}

// Sessions 列出所有登记过任务的会话 id。
func Sessions(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		out = append(out, strings.TrimSuffix(e.Name(), ".json"))
	}
	sort.Strings(out)
	return out, nil
}

// Due 找出 now 之前该触发的任务（含 now），按到期时间升序。
func DueAt(items []Item, now time.Time) []Due {
	var out []Due
	for _, it := range items {
		t, err := time.Parse(time.RFC3339, it.NextAt)
		if err != nil {
			// next_at 坏了不静默跳过：调用方需要知道这一条永远不会触发。
			continue
		}
		if t.After(now) {
			continue
		}
		out = append(out, Due{Item: it, Next: nextAfter(it, now)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Item.NextAt < out[j].Item.NextAt })
	return out
}

// nextAfter 算出严格晚于 now 的下一次触发时间。
func nextAfter(it Item, now time.Time) time.Time {
	switch it.Kind {
	case "every":
		if it.EverySec < 60 {
			return now.Add(time.Minute)
		}
		step := time.Duration(it.EverySec) * time.Second
		// 从登记的 next_at 一格一格往前推，跨过 now。
		t, err := time.Parse(time.RFC3339, it.NextAt)
		if err != nil {
			return now.Add(step)
		}
		for !t.After(now) {
			t = t.Add(step)
		}
		return t
	case "daily":
		hh, mm, ok := parseClock(it.DailyAt)
		if !ok {
			return now.Add(24 * time.Hour)
		}
		t := time.Date(now.Year(), now.Month(), now.Day(), hh, mm, 0, 0, now.Location())
		if !t.After(now) {
			t = t.AddDate(0, 0, 1)
		}
		return t
	case "weekly":
		hh, mm, ok := parseClock(it.WeeklyAt)
		if !ok || len(it.Weekdays) == 0 {
			return now.AddDate(0, 0, 7)
		}
		set := map[int]bool{}
		for _, d := range it.Weekdays {
			set[((d%7)+7)%7] = true
		}
		// 最多看 8 天：今天剩下的时间也算一次机会。
		for i := 0; i <= 8; i++ {
			day := now.AddDate(0, 0, i)
			if !set[int(day.Weekday())] {
				continue
			}
			t := time.Date(day.Year(), day.Month(), day.Day(), hh, mm, 0, 0, now.Location())
			if t.After(now) {
				return t
			}
		}
		return now.AddDate(0, 0, 7)
	}
	return now.Add(24 * time.Hour)
}

func parseClock(s string) (int, int, bool) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	hh, err1 := strconv.Atoi(parts[0])
	mm, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, 0, false
	}
	return hh, mm, true
}

// Advance 把到期的任务推到下一次，写回文件。返回被推进的条数。
//
// 先写 .tmp 再 rename：与工具模块用同一套写法，进程被砍在半路也不会留下半个 JSON。
func Advance(dir, sessionID string, dues []Due, now time.Time) (int, error) {
	if len(dues) == 0 {
		return 0, nil
	}
	items, err := Load(dir, sessionID)
	if err != nil {
		return 0, err
	}
	next := map[string]string{}
	for _, d := range dues {
		next[d.Item.ID] = d.Next.Format(time.RFC3339)
	}
	n := 0
	for i := range items {
		if v, ok := next[items[i].ID]; ok {
			items[i].NextAt = v
			n++
		}
	}
	b, err := json.MarshalIndent(map[string]any{"schedules": items}, "", "  ")
	if err != nil {
		return 0, err
	}
	b = append(b, '\n')
	p := filepath.Join(dir, sessionID+".json")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return 0, err
	}
	return n, os.Rename(tmp, p)
}
