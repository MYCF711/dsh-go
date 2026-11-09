package storage

import (
	"regexp"
	"sort"
)

// UNIT_NAME_RE 是官方 `UNIT_NAME_RE` 的等价物：单元名与表名都要能直接当
// 文件名与 SQL 标识符片段用（不需要转义）。
var unitNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// ValidateUnitName 校验单元/表/domain 名字。
func ValidateUnitName(name string) error {
	if !unitNamePattern.MatchString(name) {
		return Errorf(CodeInvalidName, "名字 %q 必须匹配 %s", name, unitNamePattern.String())
	}
	return nil
}

// Layout 是单元在介质上的布局。
//
//   - LayoutSingle：整个单元一份文档（可读、人类友好）；
//   - LayoutPerRecord：一条记录一份文档（写一条记录不重写整棵树）。
//
// 空串等价 LayoutSingle（官方 `layout` 可选，缺省 single）。
type Layout string

const (
	LayoutSingle    Layout = "single"
	LayoutPerRecord Layout = "per-record"
)

// Descriptor 是后端打开一个单元时需要的全部静态信息。
type Descriptor struct {
	Name               string
	Version            int
	Tables             []string
	HasGlobal          bool
	Layout             Layout
	CompatibleVersions []int
}

// EffectiveLayout 把空布局归一成 single。
func (d Descriptor) EffectiveLayout() Layout {
	if d.Layout == "" {
		return LayoutSingle
	}
	return d.Layout
}

// acceptedVersions 返回本单元认得的全部版本戳（当前 + 声明的兼容版本）。
func (d Descriptor) acceptedVersions() []int {
	out := make([]int, 0, 1+len(d.CompatibleVersions))
	out = append(out, d.Version)
	out = append(out, d.CompatibleVersions...)
	return out
}

func (d Descriptor) acceptsVersion(version int) bool {
	for _, accepted := range d.acceptedVersions() {
		if accepted == version {
			return true
		}
	}
	return false
}

func (d Descriptor) declaresTable(table string) bool {
	for _, declared := range d.Tables {
		if declared == table {
			return true
		}
	}
	return false
}

// State 是一次 LoadAll 的完整返回：版本戳、global 槽位、以及每个声明表的记录。
//
// Tables 里每个声明表都**存在且非 nil**（空 map），调用方不必判空。
type State struct {
	Version int
	Global  any
	Tables  map[string]map[string]any
}

// Clone 返回 state 的浅拷贝（记录值本身是 JSON 值，调用方按需再克隆）。
func (s State) Clone() State {
	out := State{Version: s.Version, Global: s.Global, Tables: make(map[string]map[string]any, len(s.Tables))}
	for table, records := range s.Tables {
		clone := make(map[string]any, len(records))
		for key, value := range records {
			clone[key] = value
		}
		out.Tables[table] = clone
	}
	return out
}

// TableNames 返回按字典序排列的表名（确定性输出用）。
func (s State) TableNames() []string {
	names := make([]string, 0, len(s.Tables))
	for table := range s.Tables {
		names = append(names, table)
	}
	sort.Strings(names)
	return names
}

// Unit 是一个已打开的单元。写入是**调用方串行**的：单元只保证单次调用
// 发布的是完整、durable 的文档；写顺序由 domain 的写链拥有。
type Unit interface {
	// LoadAll 重新读介质并返回权威状态。单元自己不持有可变的"真相"时
	// （per-record 布局）也照此语义返回磁盘上的当前内容。
	LoadAll() (State, error)
	// PutRecord 原子地替换一条记录。
	PutRecord(table, key string, value any) error
	// DeleteRecord 删除一条记录。键不存在时返回 (false, nil)（幂等）。
	DeleteRecord(table, key string) (bool, error)
	// DeleteRecords 批量删除，返回真正删掉的条数。
	DeleteRecords(table string, keys []string) (int, error)
	// SetGlobal 写入 global 单例槽位。
	SetGlobal(value any) error
	// Close 释放单元；重复调用必须幂等。
	Close() error
}

// RecordBackup 是单元的可选能力：把一条读不出来的记录移到一侧。
// domain 的 `invalidRecords: backup-and-skip` 策略需要它；没有这个能力的
// 单元遇到坏记录只能让打开失败。
type RecordBackup interface {
	BackupRecord(table, key string) (path string, err error)
}

// KVFacet 是后端的数据形状。目前只有 `kv` 一种（官方同样只实现了它）。
type KVFacet interface {
	Open(desc Descriptor) (Unit, error)
}

// Backend 是一个已挂载的介质。`KV()` 返回 nil 表示这个介质没有 KV 数据
// 形状（例如将来的 log-only 后端）——路由到它的 domain 会拿到
// `facet-unsupported`。
type Backend interface {
	KV() KVFacet
}
