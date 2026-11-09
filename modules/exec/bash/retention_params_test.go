package bash

import (
	"fmt"
	"strings"
	"testing"
)

// TestJobsRetentionEnvDefaults 钉住官方两个默认值：
// `jobs.maxConcurrentJobsPerOwner` = 10、`jobs.settledRetainBytes` = 16384。
// 0 必须表示"显式关闭"（不限 / 不裁剪），非法值退回官方默认。
func TestJobsRetentionEnvDefaults(t *testing.T) {
	if DefaultMaxConcurrentJobsPerOwner != 10 {
		t.Fatalf("官方默认并发上限必须逐值一致：10，实际 %d", DefaultMaxConcurrentJobsPerOwner)
	}
	if DefaultSettledRetainBytes != 16384 {
		t.Fatalf("官方默认已结算保留量必须逐值一致：16384，实际 %d", DefaultSettledRetainBytes)
	}
	t.Setenv(EnvMaxConcurrentJobsPerOwner, "")
	t.Setenv(EnvSettledRetainBytes, "")
	if got := maxConcurrentJobsPerOwner(); got != 10 {
		t.Fatalf("默认并发上限 = %d，期望 10", got)
	}
	// 已结算这一档：**未设置 = 不裁剪**（保持 W69 的"结算后仍 1MiB"契约，零影响）。
	// 官方默认 16384 只在显式启用时生效，见 settledRetainBytes 的注释。
	if got := settledRetainBytes(); got != 0 {
		t.Fatalf("未设置时应当不裁剪（0），实际 %d", got)
	}
	t.Setenv(EnvMaxConcurrentJobsPerOwner, "3")
	t.Setenv(EnvSettledRetainBytes, "4096")
	if got := maxConcurrentJobsPerOwner(); got != 3 {
		t.Fatalf("env 覆盖后并发上限 = %d，期望 3", got)
	}
	if got := settledRetainBytes(); got != 4096 {
		t.Fatalf("env 覆盖后保留量 = %d，期望 4096", got)
	}
	t.Setenv(EnvMaxConcurrentJobsPerOwner, "0")
	t.Setenv(EnvSettledRetainBytes, "0")
	if got := maxConcurrentJobsPerOwner(); got != 0 {
		t.Fatalf("env=0 应当表示不限，实际 %d", got)
	}
	if got := settledRetainBytes(); got != 0 {
		t.Fatalf("env=0 应当表示不裁剪，实际 %d", got)
	}
	t.Setenv(EnvMaxConcurrentJobsPerOwner, "bogus")
	t.Setenv(EnvSettledRetainBytes, "-5")
	if got := maxConcurrentJobsPerOwner(); got != 10 {
		t.Fatalf("非法并发上限应退回默认 10，实际 %d", got)
	}
	// 写错但非空 = 想启用却写错 → 用官方默认 16384（不是退回"不裁剪"）。
	if got := settledRetainBytes(); got != 16384 {
		t.Fatalf("负保留量应退回官方默认 16384，实际 %d", got)
	}
}

// TestMaxConcurrentJobsPerOwnerRejectsExtraRunning 钉住 `jobs.maxConcurrentJobsPerOwner`：
// 同一 owner 的运行中 job 达到上限后，新的后台任务必须 fail-loud（不是排队、不是静默丢弃）；
// 已结算的 job 不占额度。
func TestMaxConcurrentJobsPerOwnerRejectsExtraRunning(t *testing.T) {
	t.Setenv(EnvMaxConcurrentJobsPerOwner, "2")
	t.Setenv(EnvSettledRetainBytes, "0") // 本用例只关心并发闸
	svc := NewService(t.TempDir())

	running := func() *task { return newTask(1) }
	if _, err := svc.register("sleep 1", nil, running()); err != nil {
		t.Fatalf("第 1 个 job 应当通过: %v", err)
	}
	if _, err := svc.register("sleep 2", nil, running()); err != nil {
		t.Fatalf("第 2 个 job 应当通过: %v", err)
	}
	_, err := svc.register("sleep 3", nil, running())
	if err == nil {
		t.Fatal("第 3 个运行中 job 必须被并发上限拒绝，实际成功了（上限没生效）")
	}
	if !strings.Contains(err.Error(), "2") {
		t.Fatalf("错误里应当写明上限 2，实际: %v", err)
	}

	// 结算一个 job → 额度释放（已结算的不算并发）。
	svc.mu.Lock()
	var first *job
	for _, j := range svc.jobs {
		first = j
		break
	}
	svc.mu.Unlock()
	first.task.finish(0, "/")
	if _, err := svc.register("sleep 4", nil, running()); err != nil {
		t.Fatalf("结算一个 job 后应当能再起一个: %v", err)
	}
}

// TestSettledRetainBytesTrimsOnlySettled 钉住 `jobs.settledRetainBytes` 的两条语义：
//  1. **运行中不裁**：运行中任务按运行窗口保留（这里 64KiB < 1MiB，必须原样留着）；
//  2. **结算后裁到预算**：finish 之后保留窗口缩到 16384 字节，total 与 base 仍如实反映"裁了多少"。
func TestSettledRetainBytesTrimsOnlySettled(t *testing.T) {
	// 显式启用官方语义（默认不启用，见 settledRetainBytes 注释）。
	t.Setenv(EnvSettledRetainBytes, "16384")
	payload := []byte(strings.Repeat("x", 64*1024))
	task := newTask(7)
	task.append(payload)

	if got := task.bufferedBytes(); got != len(payload) {
		t.Fatalf("运行中不该被已结算预算裁剪：buffered=%d，期望 %d", got, len(payload))
	}
	if _, _, truncated := task.retention(); truncated {
		t.Fatal("运行中不该出现 truncated")
	}

	task.finish(0, "/")
	if got := task.bufferedBytes(); got > DefaultSettledRetainBytes {
		t.Fatalf("结算后保留 %d 字节，超过官方预算 %d", got, DefaultSettledRetainBytes)
	}
	base, total, truncated := task.retention()
	if total != int64(len(payload)) {
		t.Fatalf("total 必须仍是全量字节数 %d，实际 %d", len(payload), total)
	}
	if base != int64(len(payload)-DefaultSettledRetainBytes) {
		t.Fatalf("base = %d，期望 %d（裁掉的字节数）", base, len(payload)-DefaultSettledRetainBytes)
	}
	if !truncated {
		t.Fatal("裁过之后必须报告 truncated")
	}
	// 裁掉的是最旧的字节：page 从 0 开始读应当被夹到 base，并如实报 truncated。
	page, _, err := task.page("job-x", 0, 4096)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if !page.Truncated || page.Offset != base {
		t.Fatalf("page 应当从 base 起读并报 truncated：offset=%d truncated=%v", page.Offset, page.Truncated)
	}
}

// TestSettledRetainBytesDefaultKeepsCurrentBehavior 钉住"不设置时零影响"：
// 未设 DSHD_SETTLED_RETAIN_BYTES 时，已结算任务的保留窗口与改动前**完全一致**
// （运行窗口 1MiB 内原样保留，不冒 truncated）。W69 验收用例依赖这条。
func TestSettledRetainBytesDefaultKeepsCurrentBehavior(t *testing.T) {
	t.Setenv(EnvSettledRetainBytes, "")
	payload := []byte(strings.Repeat("z", 64*1024))
	task := newTask(9)
	task.append(payload)
	task.finish(0, "/")
	if got := task.bufferedBytes(); got != len(payload) {
		t.Fatalf("未设置时不该裁剪：buffered=%d，期望 %d", got, len(payload))
	}
	if base, _, truncated := task.retention(); base != 0 || truncated {
		t.Fatalf("未设置时不该前进 base：base=%d truncated=%v", base, truncated)
	}
}

// TestSettledRetainBytesZeroDisablesTrim 钉住 env=0 = 不裁剪（显式关闭，不是"裁到 0"）。
func TestSettledRetainBytesZeroDisablesTrim(t *testing.T) {
	t.Setenv(EnvSettledRetainBytes, "0")
	payload := []byte(strings.Repeat("y", 4096))
	task := newTask(8)
	task.append(payload)
	task.finish(0, "/")
	if got := task.bufferedBytes(); got != len(payload) {
		t.Fatalf("env=0 时不该裁剪：buffered=%d，期望 %d", got, len(payload))
	}
}

// TestMaxConcurrentJobsPerOwnerZeroMeansUnlimited 钉住 env=0 = 不限。
func TestMaxConcurrentJobsPerOwnerZeroMeansUnlimited(t *testing.T) {
	t.Setenv(EnvMaxConcurrentJobsPerOwner, "0")
	svc := NewService(t.TempDir())
	for i := 0; i < DefaultMaxConcurrentJobsPerOwner+3; i++ {
		if _, err := svc.register(fmt.Sprintf("sleep %d", i), nil, newTask(i)); err != nil {
			t.Fatalf("env=0 时第 %d 个 job 不该被拒: %v", i+1, err)
		}
	}
}
