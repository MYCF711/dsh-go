package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// 官方 spill 保留期 `session.spill.cleanupPeriodDays`（默认 30 天）。
//
// 官方 `dsh-spill-local` 给 spill 文件 30 天保留期并自动回收。宿主这边外置出去的
// 工具输出落在 `<home>/artifacts`（spillDir(home)，与 tools.Options.SpillDir 同一路径），
// 但**只写不清**：一次长跑会话、几十次外置，artifacts 里的文件就只增不减。
//
// 为什么在宿主启动时扫一遍而不是起个定时器：spill 文件的生命周期以"天"为单位，
// 请求级/分钟级的后台协程没有意义，还会在并发上多一个写者。启动时扫一次 = 每次
// 宿主重启都把过期的清掉，代价是一次目录遍历。
const (
	// DefaultSpillCleanupPeriodDays 是官方 `session.spill.cleanupPeriodDays` 的默认值。
	DefaultSpillCleanupPeriodDays = 30
	// EnvSpillCleanupPeriodDays 覆盖 spill 保留天数；0 = 关闭回收。
	EnvSpillCleanupPeriodDays = "DSHD_SPILL_CLEANUP_PERIOD_DAYS"
)

// spillCleanupPeriodDays 解析保留天数：默认 30；0/负数 = 关闭；非法值退回默认。
func spillCleanupPeriodDays() int {
	raw := strings.TrimSpace(os.Getenv(EnvSpillCleanupPeriodDays))
	if raw == "" {
		return DefaultSpillCleanupPeriodDays
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return DefaultSpillCleanupPeriodDays
	}
	return n
}

// cleanupSpillArtifacts 删掉 dir 下最后修改时间早于 now-periodDays 的**普通文件**。
//
// 只删普通文件：artifacts 里将来若出现子目录（例如按会话分组），那属于别的
// 生命周期，不能被这个按天的回收顺手带走。periodDays <= 0 = 关闭，返回 (0, 0, nil)。
// 单个文件删除失败不中断整轮（fail-soft），把失败原因聚合成一个错误返回给调用方
// 记一条日志即可——spill 是可再生数据，清不掉只是占盘，不该让宿主起不来。
func cleanupSpillArtifacts(dir string, periodDays int, now time.Time) (removed int, freed int64, err error) {
	if periodDays <= 0 || dir == "" {
		return 0, 0, nil
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return 0, 0, nil
		}
		return 0, 0, readErr
	}
	cutoff := now.Add(-time.Duration(periodDays) * 24 * time.Hour)
	var firstErr error
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, statErr := e.Info()
		if statErr != nil {
			if firstErr == nil {
				firstErr = statErr
			}
			continue
		}
		if !info.Mode().IsRegular() || !info.ModTime().Before(cutoff) {
			continue
		}
		if removeErr := os.Remove(filepath.Join(dir, e.Name())); removeErr != nil {
			if firstErr == nil {
				firstErr = removeErr
			}
			continue
		}
		removed++
		freed += info.Size()
	}
	return removed, freed, firstErr
}

// sweepSpillArtifactsBestEffort 是启动时的接线：失败只记日志，不影响启动。
//
// 用 stderr 而不是结构化日志：serve 启动阶段的日志口就是 stderr（与看门狗、
// 插件 reconcile 的失败信息同一处），这里没有别的地方可写。
func sweepSpillArtifactsBestEffort(home string) {
	period := spillCleanupPeriodDays()
	if period <= 0 {
		return
	}
	dir := spillDir(home)
	removed, freed, err := cleanupSpillArtifacts(dir, period, time.Now())
	if removed > 0 {
		fmt.Fprintf(os.Stderr, "spill 回收：删除 %d 个超过 %d 天的外置文件（释放 %d bytes）目录=%s\n",
			removed, period, freed, dir)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "spill 回收未清干净（fail-soft，不影响启动）：%v\n", err)
	}
}
