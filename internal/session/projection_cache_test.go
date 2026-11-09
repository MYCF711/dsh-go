package session

import (
	"path/filepath"
	"testing"

	"dshgo/codecs/v4"
	"dshgo/internal/storage"
)

func openTestCache(t *testing.T, root string) *ProjectionCache {
	t.Helper()
	hub := storage.NewHub()
	if _, err := hub.RegisterBackend("json", storage.NewJSONBackend(root)); err != nil {
		t.Fatalf("注册后端: %v", err)
	}
	facility := storage.NewFacility(hub, "json", nil)
	cache, err := OpenProjectionCache(facility)
	if err != nil {
		t.Fatalf("打开投影缓存: %v", err)
	}
	t.Cleanup(func() { _ = cache.Close(); _ = facility.Close() })
	return cache
}

// testFixture 是一份确定的会话日志 + 它涉及的真实绝对目录。
// 目录必须真实存在且绝对：workingDirectory 的 schema 就是"非空绝对路径"。
type testFixture struct {
	header v4.Header
	events []v4.Event
	cwdA   string
	cwdB   string
}

func newTestFixture(t *testing.T) testFixture {
	t.Helper()
	cwd := t.TempDir()
	return testFixture{
		header: v4.Header{Type: "session", Version: 4, ID: "session-1", CreatedAt: 1000, Cwd: cwd},
		cwdA:   filepath.Join(cwd, "a"),
		cwdB:   filepath.Join(cwd, "b"),
	}
}

func (f testFixture) logEvents(t *testing.T) []v4.Event {
	t.Helper()
	return []v4.Event{
		testEvent(t, "turn/start", 0, map[string]any{"turn": 1}),
		userPrompt(t, 1, "user"),
		testEvent(t, "session/title", 2, map[string]any{"title": "第一版"}),
		testEvent(t, EventWorkingDirectoryChange, 3, map[string]any{"cwd": f.cwdA}),
		testEvent(t, "session/title", 4, map[string]any{"title": "第二版"}),
		testEvent(t, EventWorkingDirectoryChange, 5, map[string]any{"cwd": f.cwdB}),
	}
}

func TestProjectionCacheReopenSurvives(t *testing.T) {
	root := t.TempDir()
	fixture := newTestFixture(t)
	identity := IdentityOfHeader(fixture.header, 0)

	cache := openTestCache(t, root)
	snapshot, err := cache.ColdSnapshot(fixture.header.ID, identity, fixture.header, 0, fixture.logEvents(t))
	if err != nil {
		t.Fatalf("冷读失败: %v", err)
	}
	if snapshot.AsOfSeq != 5 {
		t.Fatalf("asOfSeq = %d", snapshot.AsOfSeq)
	}
	if snapshot.Values[ProjectionKeyTitle] != "第二版" {
		t.Fatalf("title = %#v", snapshot.Values[ProjectionKeyTitle])
	}
	if err := cache.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// "重开" = 新进程：checkpoint 必须从磁盘回来。
	reopened := openTestCache(t, root)
	record, ok := reopened.RecordFor(fixture.header.ID, identity)
	if !ok {
		t.Fatal("重开之后 checkpoint 不见了（projection 重开丢失）")
	}
	if record.Identity != identity {
		t.Fatalf("identity = %+v，期望 %+v", record.Identity, identity)
	}
	if record.Rows[ProjectionKeyTitle].Seq != 5 {
		t.Fatalf("title 行 = %+v", record.Rows[ProjectionKeyTitle])
	}
	cached, ok := reopened.CachedSnapshot(fixture.header.ID, identity, nil)
	if !ok {
		t.Fatal("CachedSnapshot 应当命中")
	}
	if cached.Values[ProjectionKeyTitle] != "第二版" || cached.Values[ProjectionKeyWorkingDirectory] != fixture.cwdB {
		t.Fatalf("cached values = %#v", cached.Values)
	}
	if cached.AsOfSeq != 5 {
		t.Fatalf("cached asOfSeq = %d（必须是最低 watermark）", cached.AsOfSeq)
	}
}

// TestProjectionCacheColdFoldResumes 证明冷读不重算整份日志：从 checkpoint 的
// 尾部续跑，结果与全量 fold 完全一致，并把新的 checkpoint 写回。
func TestProjectionCacheColdFoldResumes(t *testing.T) {
	root := t.TempDir()
	fixture := newTestFixture(t)
	identity := IdentityOfHeader(fixture.header, 0)
	events := fixture.logEvents(t)

	cache := openTestCache(t, root)
	// 先用完整日志拿到"正确答案"。
	full, err := cache.ColdSnapshot(fixture.header.ID, identity, fixture.header, 0, events)
	if err != nil {
		t.Fatal(err)
	}
	// 再用前缀建立 checkpoint（模拟"上一次冷读在 seq 2 停下"）。
	registry := cache.Registry()
	_, prefixRows, err := registry.Restore(nil, events[:3], 0, fixture.header, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Write(fixture.header.ID, identity, prefixRows); err != nil {
		t.Fatalf("写 checkpoint: %v", err)
	}
	if floor := cache.RestoreFloor(fixture.header.ID, identity); floor != 2 {
		t.Fatalf("restoreFloor = %d，期望 2（最低 watermark 2 的前一格）", floor)
	}

	// 尾部从 floor 开始读：只把 seq 2..5 交给续跑。
	resumed, err := cache.Restore(fixture.header.ID, identity, fixture.header, 0, events[2:], 2)
	if err != nil {
		t.Fatalf("续跑失败: %v", err)
	}
	if resumed.AsOfSeq != 5 {
		t.Fatalf("续跑 asOfSeq = %d", resumed.AsOfSeq)
	}
	if got, want := resumed.Values[ProjectionKeyTitle], full.Values[ProjectionKeyTitle]; got != want {
		t.Fatalf("续跑 title = %#v，全量 = %#v", got, want)
	}
	if got, want := resumed.Values[ProjectionKeyWorkingDirectory], full.Values[ProjectionKeyWorkingDirectory]; got != want {
		t.Fatalf("续跑 cwd = %#v，全量 = %#v", got, want)
	}

	// 写回之后重开，应当是新切点（seq 5），不需要再 fold。
	// 官方 write-behind：写回先攒在批量器里，落盘发生在阈值/间隔/Flush。
	// 这里显式 Flush —— 断言仍然盯"落盘的是 seq 5"，只是不等 5s 的定时器。
	cache.Flush()
	reopened := openTestCache(t, root)
	record, ok := reopened.RecordFor(fixture.header.ID, identity)
	if !ok {
		t.Fatal("写回没有落盘")
	}
	for _, key := range []string{ProjectionKeySessionListMetadata, ProjectionKeyTitle, ProjectionKeyWorkingDirectory} {
		if record.Rows[key].Seq != 5 {
			t.Fatalf("%s 写回 seq = %d，期望 5", key, record.Rows[key].Seq)
		}
	}
}

func TestProjectionCacheVersionMismatchDiscardsRow(t *testing.T) {
	root := t.TempDir()
	fixture := newTestFixture(t)
	identity := IdentityOfHeader(fixture.header, 0)
	cache := openTestCache(t, root)
	table, err := cache.DomainTable()
	if err != nil {
		t.Fatal(err)
	}
	// 直接写一条"未来版本"的行：读路径必须整行丢弃（cache 语义，不迁移）。
	record := map[string]any{
		"identity": map[string]any{
			"formatVersion": identity.FormatVersion,
			"createdAt":     identity.CreatedAt,
			"cwd":           identity.Cwd,
		},
		"rows": map[string]any{
			ProjectionKeyTitle: map[string]any{"ver": 99, "seq": 3, "val": "stale"},
		},
	}
	if err := table.Put(fixture.header.ID, record); err != nil {
		t.Fatalf("写坏行: %v", err)
	}
	if snapshot, ok := cache.CachedSnapshot(fixture.header.ID, identity, nil); ok {
		if _, present := snapshot.Values[ProjectionKeyTitle]; present {
			t.Fatalf("ver 不匹配的行必须丢弃，得到 %#v", snapshot.Values)
		}
	}
}

func TestProjectionCacheIdentityMismatchNotServed(t *testing.T) {
	root := t.TempDir()
	fixture := newTestFixture(t)
	identity := IdentityOfHeader(fixture.header, 0)
	cache := openTestCache(t, root)
	if _, err := cache.ColdSnapshot(fixture.header.ID, identity, fixture.header, 0, fixture.logEvents(t)); err != nil {
		t.Fatal(err)
	}

	// 同一个 session id 被删除后重建（createdAt 变了）= 另一段生命周期。
	recreated := identity
	recreated.CreatedAt = identity.CreatedAt + 1
	if _, ok := cache.RecordFor(fixture.header.ID, recreated); ok {
		t.Fatal("另一段生命周期的记录必须不服务（session id 是槽位，不是生命周期）")
	}
	if _, ok := cache.CachedSnapshot(fixture.header.ID, recreated, nil); ok {
		t.Fatal("CachedSnapshot 也必须做 lifecycle 匹配")
	}
	// 列表读不看 inheritedEventCount（header 只能给到 lifecycle 身份）。
	otherCut := identity
	otherCut.InheritedEventCount = 7
	if _, ok := cache.CachedSnapshot(fixture.header.ID, otherCut, nil); !ok {
		t.Fatal("lifecycle 相同的记录必须服务给列表读")
	}
}

func TestProjectionCacheChangeNotification(t *testing.T) {
	root := t.TempDir()
	fixture := newTestFixture(t)
	identity := IdentityOfHeader(fixture.header, 0)
	cache := openTestCache(t, root)

	var changes []storage.Change
	unsubscribe := cache.Subscribe(func(change storage.Change) { changes = append(changes, change) })
	if _, err := cache.ColdSnapshot(fixture.header.ID, identity, fixture.header, 0, fixture.logEvents(t)); err != nil {
		t.Fatal(err)
	}
	// 官方 write-behind（writeEveryEvents=200 / writeIntervalMs=5000）：写回先攒在批量器里，
	// 落盘（以及 domain/changed 通知）发生在阈值/间隔/Flush。这里显式 Flush 让断言
	// 盯着"落盘后恰好一条通知"，而不是把定时器当成测试的时钟。
	cache.Flush()
	if len(changes) != 1 {
		t.Fatalf("changes = %#v，期望一条", changes)
	}
	change := changes[0]
	if change.Domain != ProjectionDomainName || change.Table != ProjectionTableSessions || change.Key != fixture.header.ID || change.Operation != "put" {
		t.Fatalf("change = %+v", change)
	}
	unsubscribe()
	if _, err := cache.ColdSnapshot(fixture.header.ID, identity, fixture.header, 0, fixture.logEvents(t)); err != nil {
		t.Fatal(err)
	}
	cache.Flush() // 同一条写回路径：落盘了，但订阅已退订，不该再有第二条通知。
	if len(changes) != 1 {
		t.Fatalf("退订后不该再收到变更: %#v", changes)
	}
}

func TestProjectionCacheClosedRejects(t *testing.T) {
	root := t.TempDir()
	fixture := newTestFixture(t)
	cache := openTestCache(t, root)
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cache.Close(); err != nil {
		t.Fatalf("close 必须幂等: %v", err)
	}
	if _, ok := cache.RecordFor(fixture.header.ID, IdentityOfHeader(fixture.header, 0)); ok {
		t.Fatal("关闭后读必须失败")
	}
}

func TestProjectionCacheStoreShape(t *testing.T) {
	// 介质形状是外部契约：官方 domain 名 session_projcache、版本 7、per-record 布局
	// （一条记录一个文档，写一条 checkpoint 不重写整棵树）。
	spec := ProjectionDomainSpec()
	if spec.Name != "session_projcache" {
		t.Fatalf("domain 名 = %q", spec.Name)
	}
	if spec.Version != 7 {
		t.Fatalf("domain 版本 = %d，期望 7", spec.Version)
	}
	if spec.Layout != storage.LayoutPerRecord {
		t.Fatalf("layout = %q，期望 per-record", spec.Layout)
	}
	if len(spec.Tables) != 1 {
		t.Fatalf("tables = %v，期望只有 sessions", spec.Tables)
	}
	if _, ok := spec.Tables[ProjectionTableSessions]; !ok {
		t.Fatalf("缺少 %q 表", ProjectionTableSessions)
	}
	if spec.CompatibleVersions == nil || len(spec.CompatibleVersions) != 4 {
		t.Fatalf("compatibleVersions = %v，期望 [3 4 5 6]", spec.CompatibleVersions)
	}
}
