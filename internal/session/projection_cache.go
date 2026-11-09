package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"

	"dshgo/codecs/v4"
	"dshgo/internal/storage"
)

// ProjectionCache 是持久投影缓存（官方 `session-projection-cache`）：
//
//	domain `session_projcache`（per-record 布局）
//	  └─ sessions 表：key = 会话 id
//	       value = {identity, rows: {key → {ver, seq, val}}}
//
// 它是 **fold 捷径，不是权威**：一行只可能过期，不可能错。所以
//   - 每一条写路径都 fail-soft（丢一次写只让下次冷读多 fold 一段尾部）；
//   - `ver` 不匹配的行在读取时整行丢弃，**从不迁移**；
//   - 记录与 header 的不可变身份不匹配时整条不服务（会话 id 是槽位，不是生命周期）。
//
// 读写共用一份一致状态：domain 的内存表服务所有同步读，写走 domain 的写链
// （先 durable 再改内存），所以读不可能看到"磁盘写了、内存没改"或反过来。
type ProjectionCache struct {
	mu     sync.RWMutex
	domain *storage.Domain
	table  *storage.Table
	// registry 是已注册投影单元；默认三台（见 DefaultProjectionRegistry）。
	registry *ProjectionRegistry
	// onWriteError 是写回失败的观察口（fail-soft：失败只记警告，不失败冷读）。
	onWriteError func(error)
	// batch 是写回批量器（官方 writeEveryEvents/writeIntervalMs）；nil 时写回退回同步。
	batch *projectionWriteBatcher
}

// projectionRecordSchema 是持久记录的 schema：identity 绑定生命周期，
// rows 是"每个投影键一行 {ver, seq, val}"。
var projectionRecordSchema = storage.Object(map[string]storage.Schema{
	"identity": storage.Object(map[string]storage.Schema{
		"formatVersion":       storage.Integer(),
		"createdAt":           storage.Integer(),
		"cwd":                 storage.String(),
		"isSeeded":            storage.Boolean(),
		"inheritedEventCount": storage.Integer(),
	}),
	"rows": storage.Record(storage.Object(map[string]storage.Schema{
		"ver": storage.Integer(),
		"seq": storage.Integer(),
		"val": storage.Any(),
	})),
})

// ProjectionDomainSpec 是官方 domain 声明：`session_projcache` v7，
// 兼容 3–6，per-record 布局，坏记录 backup-and-skip。
//
// 为什么 invalidRecords 是 backup-and-skip：缓存是可丢弃的派生数据，一条读
// 不出来的记录不该让宿主起不来——把它移到一侧、记一条警告、这个会话按未
// 缓存处理（下次冷读重建并重写）。
func ProjectionDomainSpec() storage.Spec {
	return storage.MustDefineDomain(storage.Spec{
		Name:               ProjectionDomainName,
		Version:            ProjectionDomainVersion,
		CompatibleVersions: []int{3, 4, 5, 6},
		Layout:             storage.LayoutPerRecord,
		InvalidRecords:     storage.InvalidRecordsBackupAndSkip,
		Tables: map[string]storage.TableSpec{
			ProjectionTableSessions: {Schema: projectionRecordSchema},
		},
	})
}

// OpenProjectionCache 在已挂载的 domain 数据表单上打开缓存。
func OpenProjectionCache(facility *storage.Facility) (*ProjectionCache, error) {
	if facility == nil {
		return nil, fmt.Errorf("投影缓存需要已挂载的 storage domain 表单")
	}
	domain, err := facility.Open(ProjectionDomainSpec())
	if err != nil {
		return nil, fmt.Errorf("打开 %s domain 失败: %w", ProjectionDomainName, err)
	}
	table, err := domain.Table(ProjectionTableSessions)
	if err != nil {
		_ = domain.Close()
		return nil, err
	}
	cache := &ProjectionCache{domain: domain, table: table, registry: DefaultProjectionRegistry()}
	// 官方 write-behind 批量：默认 200 条事件或 5000ms 落一次盘。
	cache.batch = newProjectionWriteBatcher(cache, projectionBatchConfigFromEnv())
	return cache, nil
}

// Registry 返回投影单元注册表（调用方据此知道有哪些键）。
func (c *ProjectionCache) Registry() *ProjectionRegistry { return c.registry }

// DomainTable 返回底层表句柄（诊断/测试用）。
func (c *ProjectionCache) DomainTable() (*storage.Table, error) {
	if c.table == nil {
		return nil, fmt.Errorf("投影缓存没有打开")
	}
	return c.table, nil
}

// SetWriteErrorSink 安装写回失败观察口（serve 层可以接 logger）。
func (c *ProjectionCache) SetWriteErrorSink(sink func(error)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onWriteError = sink
}

// Subscribe 注册 domain 变更监听（官方 `domain/changed` 的宿主形状）。
func (c *ProjectionCache) Subscribe(fn func(storage.Change)) func() {
	return c.domain.Subscribe(fn)
}

// Close 关闭缓存。幂等。
//
// 关之前先 flush 待写项：批量器是延迟落盘的，而 Close 往往就是"宿主这一轮结束了"，
// 不 flush 就等于把最后那段检查点丢掉（虽然缓存可重建，但白丢一次写没法解释）。
func (c *ProjectionCache) Close() error {
	if c.batch != nil {
		c.batch.flushAll()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.domain == nil {
		return nil
	}
	return c.domain.Close()
}

// RecordFor 读一个会话的检查点记录，**只在**记录与调用方持有的日志身份
// 完整匹配时返回。
func (c *ProjectionCache) RecordFor(id string, expected ProjectionIdentity) (*ProjectionRecord, bool) {
	// 读穿写回：批量器里攒着的那份比磁盘（domain 内存表）更新。
	if record, ok := c.batch.lookup(id, expected.Matches); ok {
		return record, true
	}
	record, ok := c.storedRecord(id)
	if !ok {
		return nil, false
	}
	if !record.Identity.Matches(expected) {
		return nil, false
	}
	return record, true
}

// CachedSnapshot 是零 IO 的列表读：只按记录里的行给出（版本匹配的）线上值。
//
// 它匹配生命周期身份而不是继承切点：列表只有 header，切点它给不出，而同一
// 格式代次内切点在 fork 时就固定了，不区分任何额外生命周期。返回的
// `asOfSeq` 是所服务行的**最低**水位——消费方据此知道这份值有多旧。
func (c *ProjectionCache) CachedSnapshot(id string, meta ProjectionIdentity, keys []string) (ProjectionSnapshot, bool) {
	// 读穿写回：批量器里那份比磁盘新；列表读只比生命周期身份（见 LifecycleMatches）。
	record, ok := c.batch.lookup(id, meta.LifecycleMatches)
	if !ok {
		record, ok = c.storedRecord(id)
	}
	if !ok || !record.Identity.LifecycleMatches(meta) {
		return ProjectionSnapshot{}, false
	}
	values := c.registry.View(record.Rows, keys)
	if len(values) == 0 {
		return ProjectionSnapshot{}, false
	}
	asOfSeq := int64(-1)
	for key, row := range record.Rows {
		if _, served := values[key]; !served {
			continue
		}
		if asOfSeq < 0 || row.Seq < asOfSeq {
			asOfSeq = row.Seq
		}
	}
	if asOfSeq < 0 {
		return ProjectionSnapshot{}, false
	}
	return ProjectionSnapshot{AsOfSeq: asOfSeq, Values: values}, true
}

// Write 用一条完整记录替换该会话的检查点（整值纪律：每次都是完整切点）。
func (c *ProjectionCache) Write(id string, identity ProjectionIdentity, rows map[string]ProjectionRow) error {
	// 同步写会覆盖批量器里那份pending：不丢的话读穿写回会拿到更旧的切点。
	c.batch.drop(id)
	stored, err := recordToStored(ProjectionRecord{Identity: identity, Rows: rows})
	if err != nil {
		return fmt.Errorf("序列化投影检查点失败: %w", err)
	}
	return c.table.Put(id, stored)
}

// WriteBehind 把一次写回排进批量器（官方 write-behind 语义）。
//
// events 是这次写回顺带 fold 的事件数；累计到官方阈值就立即落盘，否则等间隔。
// 批量器不存在时（例如只构造了零值缓存）退回同步 Write，语义不会更好也不会更坏。
func (c *ProjectionCache) WriteBehind(id string, identity ProjectionIdentity, rows map[string]ProjectionRow, events int64) {
	if c.batch == nil {
		if err := c.Write(id, identity, rows); err != nil {
			c.reportWriteError(err)
		}
		return
	}
	c.batch.enqueue(id, identity, rows, events)
}

// Flush 立刻落盘所有待写项（测试与关停用）。
func (c *ProjectionCache) Flush() {
	if c.batch != nil {
		c.batch.flushAll()
	}
}

// Checkpoint 拍一份当前状态并落盘（不 fail-soft：调用方决定怎么处理）。
func (c *ProjectionCache) Checkpoint(id string, identity ProjectionIdentity, states map[string]ProjectionState) (map[string]ProjectionRow, error) {
	rows := c.registry.Checkpoint(states)
	if err := c.Write(id, identity, rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// RestoreFloor 返回该会话尾部读的起读 seq（没有可用检查点时是 0 = 全量重读）。
func (c *ProjectionCache) RestoreFloor(id string, expected ProjectionIdentity) int64 {
	record, ok := c.RecordFor(id, expected)
	if !ok {
		return 0
	}
	return c.registry.RestoreFloor(record.Rows)
}

// Restore 是冷读：从检查点续跑尾部事件，返回切点快照，并把刷新后的检查点
// 写回（fail-soft，写回失败只影响下次冷读要 fold 的长度）。
func (c *ProjectionCache) Restore(
	id string,
	expected ProjectionIdentity,
	header v4.Header,
	inheritedEventCount int64,
	events []v4.Event,
	baseSeq int64,
) (ProjectionSnapshot, error) {
	rows := map[string]ProjectionRow{}
	if record, ok := c.RecordFor(id, expected); ok {
		rows = record.Rows
	}
	snapshot, refreshed, err := c.registry.Restore(rows, events, baseSeq, header, inheritedEventCount)
	if err != nil {
		return ProjectionSnapshot{}, err
	}
	// 写回走批量器（官方 write-behind）：累计 200 条事件或满 5000ms 才真正落盘。
	// len(events) 是这次冷读新 fold 的事件数，正好是"写放大"的来源。
	c.WriteBehind(id, expected, refreshed, int64(len(events)))
	return snapshot, nil
}

// ColdSnapshot 是完整日志的冷读（baseSeq = 0）。
func (c *ProjectionCache) ColdSnapshot(
	id string,
	expected ProjectionIdentity,
	header v4.Header,
	inheritedEventCount int64,
	events []v4.Event,
) (ProjectionSnapshot, error) {
	return c.Restore(id, expected, header, inheritedEventCount, events, 0)
}

func (c *ProjectionCache) reportWriteError(err error) {
	c.mu.RLock()
	sink := c.onWriteError
	c.mu.RUnlock()
	if sink != nil {
		sink(err)
	}
}

// storedRecord 读一条记录并解成结构体；任何形状不对都当作没有（缓存语义：
// 读不出来就重算，不猜）。
func (c *ProjectionCache) storedRecord(id string) (*ProjectionRecord, bool) {
	if c.table == nil {
		return nil, false
	}
	raw, ok := c.table.Get(id)
	if !ok {
		return nil, false
	}
	mapped, ok := raw.(map[string]any)
	if !ok {
		return nil, false
	}
	encoded, err := json.Marshal(mapped)
	if err != nil {
		return nil, false
	}
	var record ProjectionRecord
	if err := json.Unmarshal(encoded, &record); err != nil {
		return nil, false
	}
	return &record, true
}

// recordToStored 把结构体转成 domain 存储的 map 形状（json.Number 保真）。
func recordToStored(record ProjectionRecord) (map[string]any, error) {
	encoded, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var out map[string]any
	if err := decoder.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}
