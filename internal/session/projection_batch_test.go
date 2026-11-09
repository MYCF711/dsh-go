package session

import (
	"testing"
	"time"
)

// TestProjectionBatchConfigEnvDefaults 钉住官方两个默认值（200 / 5000ms）与覆盖语义。
func TestProjectionBatchConfigEnvDefaults(t *testing.T) {
	if DefaultProjectionWriteEveryEvents != 200 || DefaultProjectionWriteIntervalMS != 5000 {
		t.Fatalf("官方默认值必须逐值一致：writeEveryEvents=200 / writeIntervalMs=5000，实际 %d / %d",
			DefaultProjectionWriteEveryEvents, DefaultProjectionWriteIntervalMS)
	}
	t.Setenv(EnvProjectionWriteEveryEvents, "")
	t.Setenv(EnvProjectionWriteIntervalMS, "")
	cfg := projectionBatchConfigFromEnv()
	if cfg.EveryEvents != 200 || cfg.Interval != 5*time.Second {
		t.Fatalf("默认批量配置 = %+v", cfg)
	}
	t.Setenv(EnvProjectionWriteEveryEvents, "7")
	t.Setenv(EnvProjectionWriteIntervalMS, "250")
	cfg = projectionBatchConfigFromEnv()
	if cfg.EveryEvents != 7 || cfg.Interval != 250*time.Millisecond {
		t.Fatalf("env 覆盖后 = %+v", cfg)
	}
	t.Setenv(EnvProjectionWriteEveryEvents, "0")
	if got := projectionBatchConfigFromEnv().EveryEvents; got != 0 {
		t.Fatalf("env=0 应当表示只按时间落盘，实际 %d", got)
	}
	t.Setenv(EnvProjectionWriteEveryEvents, "bogus")
	if got := projectionBatchConfigFromEnv().EveryEvents; got != 200 {
		t.Fatalf("非法值应退回默认 200，实际 %d", got)
	}
}

// TestProjectionWriteBehindCoalesces 钉住 write-behind 的核心收益：
// 同一条会话攒着的多次写回合并成**一次**落盘；累计到 200 条事件才立即落盘。
func TestProjectionWriteBehindCoalesces(t *testing.T) {
	root := t.TempDir()
	fixture := newTestFixture(t)
	identity := IdentityOfHeader(fixture.header, 0)
	cache := openTestCache(t, root)
	// 固定配置，不受外部 env 影响。
	cache.batch = newProjectionWriteBatcher(cache, projectionBatchConfig{EveryEvents: 200, Interval: time.Hour})
	b := cache.batch
	rows := map[string]ProjectionRow{ProjectionKeyTitle: {Ver: 1, Seq: 1, Val: "x"}}

	cache.WriteBehind(fixture.header.ID, identity, rows, 50)
	cache.WriteBehind(fixture.header.ID, identity, rows, 50)
	cache.WriteBehind(fixture.header.ID, identity, rows, 50)
	if got := b.writes.Load(); got != 0 {
		t.Fatalf("累计 150 条事件（<200）不该落盘，实际落盘 %d 次", got)
	}
	// 读穿写回：还没落盘，但读必须看到最新切点。
	if _, ok := cache.RecordFor(fixture.header.ID, identity); !ok {
		t.Fatal("批量器里的检查点必须能被读到（读穿写回）")
	}
	cache.Flush()
	if got := b.writes.Load(); got != 1 {
		t.Fatalf("三次写应及时合并成 1 次落盘，实际 %d", got)
	}

	// 达到事件阈值：立即落盘，不等 Flush。
	cache.WriteBehind(fixture.header.ID, identity, rows, 200)
	if got := b.writes.Load(); got != 2 {
		t.Fatalf("累计到 200 条事件必须立即落盘，实际落盘 %d 次", got)
	}
}

// TestProjectionWriteBehindIntervalFlushes 钉住按时间落盘：间隔到了必须有真实写。
func TestProjectionWriteBehindIntervalFlushes(t *testing.T) {
	root := t.TempDir()
	fixture := newTestFixture(t)
	identity := IdentityOfHeader(fixture.header, 0)
	cache := openTestCache(t, root)
	cache.batch = newProjectionWriteBatcher(cache, projectionBatchConfig{EveryEvents: 200, Interval: 20 * time.Millisecond})
	cache.WriteBehind(fixture.header.ID, identity, map[string]ProjectionRow{
		ProjectionKeyTitle: {Ver: 1, Seq: 3, Val: "y"},
	}, 1)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cache.batch.writes.Load() >= 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("20ms 间隔到了仍未落盘（writes=%d）", cache.batch.writes.Load())
}

// TestColdSnapshotGoesThroughWriteBehind 证明 Restore 的写回真的走了批量器
// （不是"加了批量器但没人用"）。
func TestColdSnapshotGoesThroughWriteBehind(t *testing.T) {
	root := t.TempDir()
	fixture := newTestFixture(t)
	identity := IdentityOfHeader(fixture.header, 0)
	cache := openTestCache(t, root)
	cache.batch = newProjectionWriteBatcher(cache, projectionBatchConfig{EveryEvents: 200, Interval: time.Hour})

	if _, err := cache.ColdSnapshot(fixture.header.ID, identity, fixture.header, 0, fixture.logEvents(t)); err != nil {
		t.Fatalf("冷读失败: %v", err)
	}
	if got := cache.batch.writes.Load(); got != 0 {
		t.Fatalf("6 条事件的冷读不该立即落盘，实际 %d 次", got)
	}
	cache.Flush()
	if got := cache.batch.writes.Load(); got != 1 {
		t.Fatalf("Flush 后应当恰好落盘 1 次，实际 %d", got)
	}
}
