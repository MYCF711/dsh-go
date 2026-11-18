package session

import (
	"fmt"
	"os"
	"reflect"
	"sync"
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

	// 判据：**同时在飞的 describe 路数**，不是墙钟耗时。
	//
	// 原来这里断言 `elapsed < 100ms`（6 个会话各 sleep 20ms：串行 120ms、3 路并发约 40ms）。
	// 那条断言验的不是"有没有并发"，而是"这台机器比 100ms 快"——2 核 CI runner 上同时跑
	// 十几个包时，3 路并发照样能超过 100ms，于是红线**轮流出现在不同用例上**（task-86 的
	// "不同测试轮流失败"）。同一类错误还有一个先例：ConPTY 就绪判据把 `ESC[?9001h` 垃圾
	// 当成"有输出"。
	//
	// 现在的判据与机器快慢无关：3 路并发 ⇒ **必须能同时看到 3 个 describe 卡在门栅上**。
	// 机器慢只会让它们到得更晚，不会让它们变少；实现退化成串行/被互斥锁串起来 ⇒ 永远只有
	// 1 个卡在门栅上 ⇒ 门栅超时 ⇒ 红。它比原来的耗时断言更严：既证明"并发发生了"，
	// 又证明"并发度就是 3"，而且不再依赖任何墙钟阈值。
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	entered := make(chan struct{}, 16)

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
		select {
		case entered <- struct{}{}:
		default: // 门栅满了也不阻塞：正常最多 6 个 describe
		}
		// 卡在门栅上直到主协程放行。兜底超时只是防止"实现退化成串行"时整个用例挂死，
		// 它不参与判定——判定看的是主协程能不能凑齐 3 路。
		select {
		case <-release:
		case <-time.After(barrierBail):
		}
		return orig(path, info)
	}
	defer func() { listDescribe = orig; releaseAll() }()
	t.Cleanup(releaseAll)
	inflight.Store(0)
	maxInflight.Store(0)

	type listResult struct {
		listing []Listing
		err     error
	}
	done := make(chan listResult, 1)
	go func() {
		got, err := ListSessionsWithOptions(home, "", ListOptions{Concurrency: 3})
		done <- listResult{listing: got, err: err}
	}()

	for i := 1; i <= 3; i++ {
		select {
		case <-entered:
		case <-time.After(barrierWait):
			releaseAll()
			t.Fatalf("只凑到 %d 路并发 describe，期望 3（官方 listConcurrency 默认值）："+
				"并发度没生效或被串行化了", i-1)
		}
		// 同时性：第 i 路进来时，前面 i-1 路必须**还卡在门栅上**（inflight 未减）。
		// 没有这一条，"一个一个来"也能凑够 3 次进入，判据就退化成"被调用过 3 次"。
		if n := inflight.Load(); n < int64(i) {
			releaseAll()
			t.Fatalf("第 %d 路 describe 进来时在飞的只有 %d 路：并发被串行化了"+
				"（期望同时 %d 路）", i, n, i)
		}
	}
	releaseAll()
	var res listResult
	select {
	case res = <-done:
	case <-time.After(barrierWait):
		t.Fatalf("放行后列会话仍不返回（%v）", barrierWait)
	}
	if res.err != nil {
		t.Fatalf("并行列会话失败: %v", res.err)
	}
	if n := maxInflight.Load(); n != 3 {
		t.Fatalf("并发路数 = %d，期望 3（官方 listConcurrency 默认值）", n)
	}
	if !reflect.DeepEqual(res.listing, serial) {
		t.Fatalf("并发结果与串行不一致：\n并发=%+v\n串行=%+v", res.listing, serial)
	}
}

// 门栅的两个预算：barrierWait 是"凑齐 3 路"的耐心，barrierBail 是兜底放行。
// 都只是防挂死的上限，不参与判定——真正判"有没有并发"的是能不能凑齐 3 路。
const (
	barrierWait = 30 * time.Second
	barrierBail = 5 * time.Second
)

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
