package session

import (
	"fmt"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

// TestListSessionsConcurrencyBoundedAndOrderStable 钉住官方
// `session.persistence.listConcurrency`（默认 3）：列会话的日志读取得有界并发，
// 且并发后的结果必须与串行**逐条相同**（面板顺序不能因并发而抖动）。
func TestListSessionsConcurrencyBoundedAndOrderStable(t *testing.T) {
	home := t.TempDir()
	for i := 0; i < 6; i++ {
		mkSession(t, home, "/tmp/proj-a", fmt.Sprintf("sess-%d", i), "hi", "yo")
	}

	serial, err := ListSessionsWithOptions(home, "", ListOptions{Concurrency: 1, WorkSliceMS: 1 << 20})
	if err != nil {
		t.Fatalf("串行列会话失败: %v", err)
	}
	if len(serial) != 6 {
		t.Fatalf("串行列出 %d 条，期望 6", len(serial))
	}

	orig := listDescribe
	listDescribe = func(path string, info os.FileInfo) Listing {
		cur := inflight.Add(1)
		for {
			max := maxInflight.Load()
			if cur <= max || maxInflight.CompareAndSwap(max, cur) {
				break
			}
		}
		defer inflight.Add(-1)
		time.Sleep(20 * time.Millisecond)
		return orig(path, info)
	}
	defer func() { listDescribe = orig }()
	inflight.Store(0)
	maxInflight.Store(0)

	start := time.Now()
	got, err := ListSessionsWithOptions(home, "", ListOptions{Concurrency: 3})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("并行列会话失败: %v", err)
	}
	if n := maxInflight.Load(); n != 3 {
		t.Fatalf("并发路数 = %d，期望 3（官方 listConcurrency 默认值）", n)
	}
	// 6 个会话各 20ms：串行 120ms，3 路并发应约 40ms。
	if elapsed >= 100*time.Millisecond {
		t.Fatalf("列会话耗时 %v，看起来仍是串行（并发度没生效）", elapsed)
	}
	if !reflect.DeepEqual(got, serial) {
		t.Fatalf("并发结果与串行不一致：\n并发=%+v\n串行=%+v", got, serial)
	}
}

// TestListSessionsWorkSliceYields 钉住官方 `sessionController.listWorkSliceMs`
// （默认 16）：列会话按时间切片推进，切片之间让出，结果与不切片一致。
func TestListSessionsWorkSliceYields(t *testing.T) {
	home := t.TempDir()
	for i := 0; i < 4; i++ {
		mkSession(t, home, "/tmp/proj-b", fmt.Sprintf("sess-slice-%d", i), "hi", "yo")
	}

	orig := listDescribe
	listDescribe = func(path string, info os.FileInfo) Listing {
		time.Sleep(3 * time.Millisecond)
		return orig(path, info)
	}
	defer func() { listDescribe = orig }()

	whole, err := ListSessionsWithOptions(home, "", ListOptions{Concurrency: 1, WorkSliceMS: 1 << 20})
	if err != nil {
		t.Fatalf("整段列会话失败: %v", err)
	}

	base := listSlices.Load()
	sliced, err := ListSessionsWithOptions(home, "", ListOptions{Concurrency: 1, WorkSliceMS: 1})
	if err != nil {
		t.Fatalf("切片列会话失败: %v", err)
	}
	if n := listSlices.Load() - base; n < 2 {
		t.Fatalf("切片数 = %d，期望 >= 2（1ms 预算 + 4×3ms 工作）", n)
	}
	if !reflect.DeepEqual(sliced, whole) {
		t.Fatalf("切片结果与整段不一致：\n切片=%+v\n整段=%+v", sliced, whole)
	}
}

// TestListConcurrencyAndSliceDefaults 钉住两个官方默认值（3 / 16）与环境变量覆盖，
// 并确认非法值退回默认（不能让一个手滑的 env 把并发度变成 0 或负数）。
func TestListConcurrencyAndSliceDefaults(t *testing.T) {
	t.Setenv(EnvListConcurrency, "")
	t.Setenv(EnvListWorkSliceMS, "")
	if got := listConcurrencyFromEnv(); got != DefaultListConcurrency {
		t.Fatalf("默认并发度 = %d，期望 %d", got, DefaultListConcurrency)
	}
	if got := listWorkSliceFromEnv(); got != DefaultListWorkSliceMS {
		t.Fatalf("默认切片预算 = %d，期望 %d", got, DefaultListWorkSliceMS)
	}
	if DefaultListConcurrency != 3 || DefaultListWorkSliceMS != 16 {
		t.Fatalf("官方默认值必须逐值一致：listConcurrency=3 / listWorkSliceMs=16，实际 %d / %d",
			DefaultListConcurrency, DefaultListWorkSliceMS)
	}

	t.Setenv(EnvListConcurrency, "6")
	t.Setenv(EnvListWorkSliceMS, "25")
	if got := listConcurrencyFromEnv(); got != 6 {
		t.Fatalf("env 覆盖后并发度 = %d，期望 6", got)
	}
	if got := listWorkSliceFromEnv(); got != 25 {
		t.Fatalf("env 覆盖后切片预算 = %d，期望 25", got)
	}

	t.Setenv(EnvListConcurrency, "abc")
	t.Setenv(EnvListWorkSliceMS, "-4")
	if got := listConcurrencyFromEnv(); got != DefaultListConcurrency {
		t.Fatalf("非法并发度 env 应退回默认，实际 %d", got)
	}
	if got := listWorkSliceFromEnv(); got != DefaultListWorkSliceMS {
		t.Fatalf("非正切片预算 env 应退回默认，实际 %d", got)
	}
}

var (
	inflight    atomic.Int64
	maxInflight atomic.Int64
)
