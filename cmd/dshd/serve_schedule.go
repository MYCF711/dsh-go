package main

import (
	"path/filepath"

	"dshgo/internal/schedule"
	"dshgo/internal/transport"
)

// serveScheduleList 读 `<home>/state/schedules/` 下的全部定时任务。
//
// 和 `dshd schedule list` 共享同一个存储层（`internal/schedule`），
// 所以两边看到的东西一定一致——不会出现"CLI 说有 3 条、界面显示 0 条"。
//
// 读失败就返回空：**定时任务读不到不该让 serve 报错**，
// 它只是界面上的一块信息，不是会话功能。
func serveScheduleList(home string) []transport.ScheduleItem {
	dir := filepath.Join(home, "state", "schedules")
	sessions, err := schedule.Sessions(dir)
	if err != nil {
		return nil
	}
	out := make([]transport.ScheduleItem, 0, len(sessions))
	for _, sid := range sessions {
		items, err := schedule.Load(dir, sid)
		if err != nil {
			continue // 单个会话坏了不拖累其它的
		}
		for _, it := range items {
			out = append(out, transport.ScheduleItem{
				ID:       it.ID,
				Title:    it.Title,
				Kind:     it.Kind,
				NextAt:   it.NextAt,
				EverySec: it.EverySec,
				DailyAt:  it.DailyAt,
				WeeklyAt: it.WeeklyAt,
			})
		}
	}
	return out
}
