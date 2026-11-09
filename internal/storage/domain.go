package storage

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Change 是一次已提交的写入引起的变更通知（官方 `domain/changed`）。
//
// 事件在**写入已经 durable 之后**才发：监听器看到的值与介质一致，不会被
// 一次失败的写骗到。Operation 只有 "put" 与 "deleted" 两种；deleted 的
// Value 为 nil。
type Change struct {
	Domain    string
	Table     string
	Key       string
	Operation string
	Value     any
}

const (
	operationPut     = "put"
	operationDeleted = "deleted"
)

// Facility 是 domain 数据表单（官方 `ctx.storage.domain`）：按 route 表把
// domain 名字解析到后端，打开单元，并在一个进程内保证同一个 domain 名字
// 只被打开一次。
type Facility struct {
	mu       sync.Mutex
	hub      *Hub
	backend  string
	routes   map[string]string
	domains  map[string]*Domain
	reserved map[string]bool
}

// NewFacility 建一个数据表单。backend 是默认后端名字，routes 覆盖单个
// domain 的路由（官方 Config.routes）。
func NewFacility(hub *Hub, backend string, routes map[string]string) *Facility {
	if hub == nil {
		hub = NewHub()
	}
	cloned := make(map[string]string, len(routes))
	for domain, name := range routes {
		cloned[domain] = name
	}
	return &Facility{hub: hub, backend: backend, routes: cloned, domains: map[string]*Domain{}, reserved: map[string]bool{}}
}

// Open 打开一个已声明的 domain。每一步失败都整体失败（不留半个 domain）：
//
//   - 名字已经打开 → `already-open`；
//   - 路由到的后端没挂载 → `backend-not-found` 原样透传；
//   - 后端没有 KV 数据形状 → `facet-unsupported`；
//   - 介质版本不认 → `version-mismatch` 原样透传；
//   - 记录没过 schema → `invalid-record`（带 table/key；声明了
//     `invalidRecords: backup-and-skip` 且单元支持备份时改成备份并跳过）。
func (f *Facility) Open(spec Spec) (*Domain, error) {
	validated, err := DefineDomain(spec)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	if f.reserved[validated.Name] {
		f.mu.Unlock()
		return nil, Errorf(CodeAlreadyOpen, "domain %q 已经打开了", validated.Name)
	}
	f.reserved[validated.Name] = true
	f.mu.Unlock()

	release := func() {
		f.mu.Lock()
		delete(f.reserved, validated.Name)
		f.mu.Unlock()
	}

	backendName := f.backend
	if routed, ok := f.routes[validated.Name]; ok && routed != "" {
		backendName = routed
	}
	backend, err := f.hub.Backend(backendName)
	if err != nil {
		release()
		return nil, err
	}
	facet := backend.KV()
	if facet == nil {
		release()
		return nil, Errorf(CodeFacetUnsupported, "domain %q 路由到的后端 %q 没有 kv 数据形状", validated.Name, backendName)
	}
	unit, err := facet.Open(DescriptorOf(validated))
	if err != nil {
		release()
		return nil, err
	}
	state, err := unit.LoadAll()
	if err != nil {
		_ = unit.Close()
		release()
		return nil, err
	}

	tables := make(map[string]*Table, len(validated.Tables))
	skipped := make([]InvalidRecordDetail, 0)
	for tableName, tableSpec := range validated.Tables {
		table := &Table{name: tableName, records: map[string]any{}}
		rawRecords, ok := state.Tables[tableName]
		if !ok {
			rawRecords = map[string]any{}
		}
		keys := make([]string, 0, len(rawRecords))
		for key := range rawRecords {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value := rawRecords[key]
			if validateErr := tableSpec.Schema.Validate(value); validateErr != nil {
				// 坏记录只在"派生数据"策略下才允许跳过；其余情况整次打开失败。
				if validated.InvalidRecords == InvalidRecordsBackupAndSkip {
					if backup, ok := unit.(RecordBackup); ok {
						if _, backupErr := backup.BackupRecord(tableName, key); backupErr != nil {
							_ = unit.Close()
							release()
							return nil, fmt.Errorf("domain %q 表 %q 的坏记录 %q 备份失败: %w", validated.Name, tableName, key, backupErr)
						}
						skipped = append(skipped, InvalidRecordDetail{Domain: validated.Name, Table: tableName, Key: key})
						continue
					}
				}
				_ = unit.Close()
				release()
				return nil, invalidRecord(validated.Name, tableName, key, validateErr)
			}
			table.records[key] = value
		}
		tables[tableName] = table
	}

	domain := &Domain{
		name:     validated.Name,
		spec:     validated,
		unit:     unit,
		tables:   tables,
		listener: map[int]func(Change){},
		skipped:  skipped,
		facility: f,
	}
	if validated.Global != nil {
		domain.hasGlobal = true
		value := state.Global
		if value == nil {
			value = validated.Global.Initial
		} else if err := validated.Global.Schema.Validate(value); err != nil {
			_ = unit.Close()
			release()
			return nil, invalidRecord(validated.Name, "", "", err)
		}
		domain.global = value
	}
	for _, table := range domain.tables {
		table.domain = domain
	}

	f.mu.Lock()
	f.domains[validated.Name] = domain
	f.mu.Unlock()
	return domain, nil
}

// Close 关闭所有还开着的 domain。
func (f *Facility) Close() error {
	f.mu.Lock()
	domains := make([]*Domain, 0, len(f.domains))
	for _, domain := range f.domains {
		domains = append(domains, domain)
	}
	f.mu.Unlock()
	var errs []error
	for _, domain := range domains {
		if err := domain.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (f *Facility) release(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.domains, name)
	delete(f.reserved, name)
}

// Domain 是一个已打开的 domain：权威内存状态 + 每 domain 一条写链 + 变更事件。
//
// 读是内存的（同步，不碰介质）；每次写都排队到写链上，**先落盘、再改内存、
// 最后发事件**——所以被拒绝的写不会让内存与介质分叉。
type Domain struct {
	mu      sync.RWMutex
	writeMu sync.Mutex

	name     string
	spec     Spec
	unit     Unit
	tables   map[string]*Table
	facility *Facility
	skipped  []InvalidRecordDetail

	global    any
	hasGlobal bool

	listenerMu  sync.Mutex
	listener    map[int]func(Change)
	listenerSeq int

	disposing bool
	closed    bool
	failure   error
	closeOnce sync.Once
	closeErr  error
}

// Name 返回 domain 名字。
func (d *Domain) Name() string { return d.name }

// Spec 返回校验过的声明。
func (d *Domain) Spec() Spec { return d.spec }

// Skipped 返回打开时按 backup-and-skip 跳过的坏记录（诊断用）。
func (d *Domain) Skipped() []InvalidRecordDetail {
	out := make([]InvalidRecordDetail, len(d.skipped))
	copy(out, d.skipped)
	return out
}

// Table 解析一张已声明的表。未声明的名字是调用方 bug，直接报错。
func (d *Domain) Table(name string) (*Table, error) {
	table, ok := d.tables[name]
	if !ok {
		return nil, fmt.Errorf("domain %q 没有声明表 %q", d.name, name)
	}
	return table, nil
}

// Global 返回 global 单例句柄。没声明 global 的 domain 调用它是调用方 bug。
func (d *Domain) Global() (*GlobalHandle, error) {
	if !d.hasGlobal {
		return nil, fmt.Errorf("domain %q 没有声明 global", d.name)
	}
	return &GlobalHandle{domain: d}, nil
}

// Subscribe 注册一个变更监听器，返回退订函数。
//
// **监听器不许回写同一个 domain**：写链是非重入的（官方是 promise 链，
// 这里是一个互斥锁）。需要"收到变更再写"的场景请另行排队。
func (d *Domain) Subscribe(fn func(Change)) func() {
	d.listenerMu.Lock()
	defer d.listenerMu.Unlock()
	d.listenerSeq++
	id := d.listenerSeq
	d.listener[id] = fn
	return func() {
		d.listenerMu.Lock()
		defer d.listenerMu.Unlock()
		delete(d.listener, id)
	}
}

// Close 关闭 domain：立刻拒绝新写，等已排队的写排空，再关单元。
// 幂等——重复调用共用同一次 teardown。
func (d *Domain) Close() error {
	d.closeOnce.Do(func() {
		d.mu.Lock()
		d.disposing = true
		d.mu.Unlock()

		// 等已排队的写排空（写链的锁就是队列本身）。
		d.writeMu.Lock()
		d.closeErr = d.unit.Close()
		d.writeMu.Unlock()

		d.mu.Lock()
		d.closed = true
		d.mu.Unlock()
		if d.facility != nil {
			d.facility.release(d.name)
		}
	})
	return d.closeErr
}

// assertReadable 是读路径的统一闸门：关闭或隔离之后一律拒绝。
func (d *Domain) assertReadable() error {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed || d.disposing {
		return Errorf(CodeClosed, "domain %q 已经关闭", d.name)
	}
	if d.failure != nil {
		return d.failure
	}
	return nil
}

// enqueue 把一次写排到 domain 的写链上。
func (d *Domain) enqueue(job func() error) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()

	d.mu.RLock()
	closed, disposing, failure := d.closed, d.disposing, d.failure
	d.mu.RUnlock()
	if closed || disposing {
		return Errorf(CodeClosed, "domain %q 已经关闭", d.name)
	}
	if failure != nil {
		return failure
	}
	err := job()
	if err != nil && IsCode(err, CodeUnknownState) {
		// unknown-state 之后读和写都必须拒绝：内存与介质可能已经分叉。
		d.mu.Lock()
		d.failure = err
		d.mu.Unlock()
	}
	return err
}

func (d *Domain) emit(change Change) {
	d.listenerMu.Lock()
	listeners := make([]func(Change), 0, len(d.listener))
	for _, listener := range d.listener {
		listeners = append(listeners, listener)
	}
	d.listenerMu.Unlock()
	for _, listener := range listeners {
		func() {
			// 写已经 durable：监听器 panic 不许把它拖成失败。
			defer func() { _ = recover() }()
			listener(change)
		}()
	}
}

// Table 是一张表的稳定句柄。记录在 domain 打开时整体载入内存。
type Table struct {
	domain  *Domain
	name    string
	mu      sync.RWMutex
	records map[string]any
}

// Entry 是 Entries 的一行。
type Entry struct {
	Key   string
	Value any
}

// Get 读一条记录（同步，不碰介质）。
func (t *Table) Get(key string) (any, bool) {
	if err := t.domain.assertReadable(); err != nil {
		return nil, false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	value, ok := t.records[key]
	return value, ok
}

// Entries 按 key 升序返回全部记录（确定性输出：Go 的 map 无序，直接返回
// 会让同一状态产生不同字节）。
func (t *Table) Entries() []Entry {
	if t.domain.assertReadable() != nil {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	keys := make([]string, 0, len(t.records))
	for key := range t.records {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]Entry, 0, len(keys))
	for _, key := range keys {
		out = append(out, Entry{Key: key, Value: t.records[key]})
	}
	return out
}

// Keys 按升序返回全部 key。
func (t *Table) Keys() []string {
	entries := t.Entries()
	keys := make([]string, 0, len(entries))
	for _, entry := range entries {
		keys = append(keys, entry.Key)
	}
	return keys
}

// Size 返回记录条数。
func (t *Table) Size() int {
	if t.domain.assertReadable() != nil {
		return 0
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.records)
}

// Put 写一条记录：schema 校验 → 落盘 → 改内存 → 发变更。
func (t *Table) Put(key string, value any) error {
	return t.domain.enqueue(func() error {
		if err := t.domain.spec.Tables[t.name].Schema.Validate(value); err != nil {
			return invalidRecord(t.domain.name, t.name, key, err)
		}
		if err := t.domain.unit.PutRecord(t.name, key, value); err != nil {
			return err
		}
		t.mu.Lock()
		t.records[key] = value
		t.mu.Unlock()
		t.domain.emit(Change{Domain: t.domain.name, Table: t.name, Key: key, Operation: operationPut, Value: value})
		return nil
	})
}

// Delete 删除一条记录。键不存在时是幂等 no-op（返回 false）。
func (t *Table) Delete(key string) (bool, error) {
	removed := false
	err := t.domain.enqueue(func() error {
		t.mu.RLock()
		_, present := t.records[key]
		t.mu.RUnlock()
		if !present {
			return nil
		}
		if _, err := t.domain.unit.DeleteRecord(t.name, key); err != nil {
			return err
		}
		t.mu.Lock()
		delete(t.records, key)
		t.mu.Unlock()
		removed = true
		t.domain.emit(Change{Domain: t.domain.name, Table: t.name, Key: key, Operation: operationDeleted})
		return nil
	})
	return removed, err
}

// DeleteMany 批量删除，返回真正删掉的条数。一次调用只发布一次介质变更。
func (t *Table) DeleteMany(keys []string) (int, error) {
	removed := 0
	err := t.domain.enqueue(func() error {
		unique := make([]string, 0, len(keys))
		seen := make(map[string]bool, len(keys))
		t.mu.RLock()
		for _, key := range keys {
			if seen[key] {
				continue
			}
			seen[key] = true
			if _, present := t.records[key]; present {
				unique = append(unique, key)
			}
		}
		t.mu.RUnlock()
		if len(unique) == 0 {
			return nil
		}
		count, err := t.domain.unit.DeleteRecords(t.name, unique)
		if err != nil {
			return err
		}
		t.mu.Lock()
		for _, key := range unique {
			delete(t.records, key)
		}
		t.mu.Unlock()
		for _, key := range unique {
			t.domain.emit(Change{Domain: t.domain.name, Table: t.name, Key: key, Operation: operationDeleted})
		}
		removed = count
		return nil
	})
	return removed, err
}

// Update 读-改-写一条已存在的记录。目标不存在时稳定码 `missing-key`。
func (t *Table) Update(key string, fn func(current any) (any, error)) (any, error) {
	var next any
	err := t.domain.enqueue(func() error {
		t.mu.RLock()
		current, present := t.records[key]
		t.mu.RUnlock()
		if !present {
			return Errorf(CodeMissingKey, "domain %q 表 %q 没有记录 %q 可更新", t.domain.name, t.name, key)
		}
		updated, err := fn(current)
		if err != nil {
			return err
		}
		if err := t.domain.spec.Tables[t.name].Schema.Validate(updated); err != nil {
			return invalidRecord(t.domain.name, t.name, key, err)
		}
		if err := t.domain.unit.PutRecord(t.name, key, updated); err != nil {
			return err
		}
		t.mu.Lock()
		t.records[key] = updated
		t.mu.Unlock()
		next = updated
		t.domain.emit(Change{Domain: t.domain.name, Table: t.name, Key: key, Operation: operationPut, Value: updated})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return next, nil
}

// GlobalHandle 是 global 单例的稳定句柄。
type GlobalHandle struct {
	domain *Domain
}

// Get 读 global 当前值。
func (g *GlobalHandle) Get() (any, error) {
	if err := g.domain.assertReadable(); err != nil {
		return nil, err
	}
	g.domain.mu.RLock()
	defer g.domain.mu.RUnlock()
	return g.domain.global, nil
}

// Set 写 global：schema 校验 → 落盘 → 改内存 → 发变更。
func (g *GlobalHandle) Set(value any) error {
	return g.domain.enqueue(func() error {
		if err := g.domain.spec.Global.Schema.Validate(value); err != nil {
			return invalidRecord(g.domain.name, "", "", err)
		}
		if err := g.domain.unit.SetGlobal(value); err != nil {
			return err
		}
		g.domain.mu.Lock()
		g.domain.global = value
		g.domain.mu.Unlock()
		g.domain.emit(Change{Domain: g.domain.name, Table: "", Key: "", Operation: operationPut, Value: value})
		return nil
	})
}
