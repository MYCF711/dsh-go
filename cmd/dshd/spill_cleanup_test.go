package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSpillCleanupPeriodDaysEnv 钉住官方默认 30 天与覆盖语义。
func TestSpillCleanupPeriodDaysEnv(t *testing.T) {
	if DefaultSpillCleanupPeriodDays != 30 {
		t.Fatalf("官方默认值必须逐值一致：30，实际 %d", DefaultSpillCleanupPeriodDays)
	}
	t.Setenv(EnvSpillCleanupPeriodDays, "")
	if got := spillCleanupPeriodDays(); got != 30 {
		t.Fatalf("默认保留天数 = %d，期望 30", got)
	}
	t.Setenv(EnvSpillCleanupPeriodDays, "7")
	if got := spillCleanupPeriodDays(); got != 7 {
		t.Fatalf("env 覆盖后 = %d，期望 7", got)
	}
	t.Setenv(EnvSpillCleanupPeriodDays, "0")
	if got := spillCleanupPeriodDays(); got != 0 {
		t.Fatalf("env=0 应当表示关闭回收，实际 %d", got)
	}
	t.Setenv(EnvSpillCleanupPeriodDays, "bogus")
	if got := spillCleanupPeriodDays(); got != 30 {
		t.Fatalf("非法值应退回默认 30，实际 %d", got)
	}
}

// TestCleanupSpillArtifactsRemovesOnlyExpiredRegularFiles 钉住回收语义：
// 只删过期普通文件；新文件、子目录一律保留；period<=0 完全不动。
func TestCleanupSpillArtifactsRemovesOnlyExpiredRegularFiles(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	old := filepath.Join(dir, "tool-old.txt")
	fresh := filepath.Join(dir, "tool-fresh.txt")
	sub := filepath.Join(dir, "per-session")
	for _, p := range []string{old, fresh} {
		if err := os.WriteFile(p, []byte("spill payload"), 0o644); err != nil {
			t.Fatalf("写夹具失败: %v", err)
		}
	}
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("建子目录失败: %v", err)
	}
	oldTime := now.Add(-40 * 24 * time.Hour)
	if err := os.Chtimes(old, oldTime, oldTime); err != nil {
		t.Fatalf("改 mtime 失败: %v", err)
	}
	// 子目录也做旧：它不该被按天回收带走（子目录另有生命周期）。
	if err := os.Chtimes(sub, oldTime, oldTime); err != nil {
		t.Fatalf("改子目录 mtime 失败: %v", err)
	}

	removed, freed, err := cleanupSpillArtifacts(dir, 30, now)
	if err != nil {
		t.Fatalf("回收报错: %v", err)
	}
	if removed != 1 || freed != int64(len("spill payload")) {
		t.Fatalf("回收结果 removed=%d freed=%d，期望 1 个 / %d bytes", removed, freed, len("spill payload"))
	}
	if _, statErr := os.Stat(old); !os.IsNotExist(statErr) {
		t.Fatalf("过期文件仍在: %v", statErr)
	}
	if _, statErr := os.Stat(fresh); statErr != nil {
		t.Fatalf("新文件被误删: %v", statErr)
	}
	if _, statErr := os.Stat(sub); statErr != nil {
		t.Fatalf("子目录被误删: %v", statErr)
	}

	// 关闭档：period=0 一个都不动。
	if removed, _, err := cleanupSpillArtifacts(dir, 0, now); err != nil || removed != 0 {
		t.Fatalf("period=0 应当完全不动，实际 removed=%d err=%v", removed, err)
	}
	// 目录不存在不算错误（全新 home 还没外置过文件）。
	if removed, _, err := cleanupSpillArtifacts(filepath.Join(dir, "nope"), 30, now); err != nil || removed != 0 {
		t.Fatalf("目录不存在应当静默跳过，实际 removed=%d err=%v", removed, err)
	}
}

// TestSpillCleanupTargetsSameDirAsTools 钉住"回收目录 == 工具外置目录"：
// 两份路径一旦漂开，回收就是在扫一个没人写的空目录（看着有实现，实际无效）。
func TestSpillCleanupTargetsSameDirAsTools(t *testing.T) {
	home := t.TempDir()
	if got, want := spillDir(home), filepath.Join(home, "artifacts"); got != want {
		t.Fatalf("spill 目录 = %q，期望 %q（tools.Options.SpillDir 同一路径）", got, want)
	}
}
