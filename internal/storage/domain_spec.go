package storage

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
)

// Schema 是一条记录的校验器。官方用 zod；Go 侧没有泛型反射的运行时 schema，
// 所以这里是一台**真实的**小引擎：domain 打开时、每次写入前都要过它。
//
// 为什么不用 `any` 空过：`invalid-record` 的 detail（domain/table/key）就是
// 靠它在读盘时定位出来的；没有校验，坏记录会在第一次使用时才炸，而且不知道
// 是哪条。
type Schema interface {
	Validate(value any) error
}

// SchemaFunc 让调用方用函数字面量声明 schema。
type SchemaFunc func(value any) error

// Validate 实现 Schema。
func (f SchemaFunc) Validate(value any) error { return f(value) }

// Any 接受任何 JSON 值（含 nil）。用于"值形状由上层自己保证"的槽位。
func Any() Schema { return SchemaFunc(func(any) error { return nil }) }

// String 只接受 string。
func String() Schema {
	return SchemaFunc(func(value any) error {
		if _, ok := value.(string); !ok {
			return fmt.Errorf("期望 string，收到 %T", value)
		}
		return nil
	})
}

// Boolean 只接受 bool。
func Boolean() Schema {
	return SchemaFunc(func(value any) error {
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("期望 boolean，收到 %T", value)
		}
		return nil
	})
}

// Integer 只接受整数（含 JSON 解码出来的 json.Number 与整值 float64）。
func Integer() Schema {
	return SchemaFunc(func(value any) error {
		switch typed := value.(type) {
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			return nil
		case json.Number:
			if _, err := typed.Int64(); err != nil {
				return fmt.Errorf("期望整数，收到 %v", typed)
			}
			return nil
		case float64:
			if math.Trunc(typed) != typed {
				return fmt.Errorf("期望整数，收到 %v", typed)
			}
			return nil
		default:
			return fmt.Errorf("期望整数，收到 %T", value)
		}
	})
}

// Nullable 允许 nil，其余交给 inner。
func Nullable(inner Schema) Schema {
	return SchemaFunc(func(value any) error {
		if value == nil {
			return nil
		}
		return inner.Validate(value)
	})
}

// Record 是 `map[string]T`：每个值都要通过 of。
func Record(of Schema) Schema {
	return SchemaFunc(func(value any) error {
		records, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("期望 map[string]any，收到 %T", value)
		}
		keys := make([]string, 0, len(records))
		for key := range records {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if err := of.Validate(records[key]); err != nil {
				return fmt.Errorf("记录 %q: %w", key, err)
			}
		}
		return nil
	})
}

// Object 是**严格**对象：未声明的键一律拒绝。
//
// 官方 zod 默认会把未知键 strip 掉；这里刻意不 strip——存储层静默丢字段
// 等于数据损坏，而且故障会在下一次重开时才发现。
func Object(fields map[string]Schema) Schema {
	return SchemaFunc(func(value any) error {
		records, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("期望对象，收到 %T", value)
		}
		keys := make([]string, 0, len(records))
		for key := range records {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			schema, declared := fields[key]
			if !declared {
				return fmt.Errorf("未声明的字段 %q", key)
			}
			if err := schema.Validate(records[key]); err != nil {
				return fmt.Errorf("字段 %q: %w", key, err)
			}
		}
		return nil
	})
}

// TableSpec 是一张表的声明。
type TableSpec struct {
	Schema Schema
}

// GlobalSpec 是 global 单例槽位的声明。
//
// Initial 是介质里还没有值时的启动默认；它自己也必须通过 Schema 校验。
type GlobalSpec struct {
	Schema  Schema
	Initial any
}

// InvalidRecordsPolicy 是坏记录的处置策略。
type InvalidRecordsPolicy string

// InvalidRecordsBackupAndSkip：把读不出来的记录移到一侧（`<key>.json.bak.<stamp>`），
// 记一条警告后跳过。只对派生数据（缓存）正确——它不是"修复"，是"别让它挡住启动"。
const InvalidRecordsBackupAndSkip InvalidRecordsPolicy = "backup-and-skip"

// Spec 是 domain 的单一来源声明：名字、版本、布局、表与 global 的 schema。
type Spec struct {
	Name               string
	Version            int
	CompatibleVersions []int
	Layout             Layout
	InvalidRecords     InvalidRecordsPolicy
	Tables             map[string]TableSpec
	Global             *GlobalSpec
}

// DefineDomain 校验并原样返回声明。任何装配错误都在**碰介质之前**失败。
func DefineDomain(spec Spec) (Spec, error) {
	if err := ValidateUnitName(spec.Name); err != nil {
		return Spec{}, err
	}
	if spec.Version < 0 {
		return Spec{}, Errorf(CodeInvalidSpec, "domain %q 的 version 必须是非负整数，收到 %d", spec.Name, spec.Version)
	}
	for _, compatible := range spec.CompatibleVersions {
		if compatible < 0 || compatible >= spec.Version {
			return Spec{}, Errorf(CodeInvalidSpec,
				"domain %q 的 compatibleVersions 条目必须是非负整数且小于 version %d，收到 %d",
				spec.Name, spec.Version, compatible)
		}
	}
	switch spec.Layout {
	case "", LayoutSingle, LayoutPerRecord:
	default:
		return Spec{}, Errorf(CodeInvalidSpec, "domain %q 的 layout 只能是 single 或 per-record，收到 %q", spec.Name, spec.Layout)
	}
	switch spec.InvalidRecords {
	case "", InvalidRecordsBackupAndSkip:
	default:
		return Spec{}, Errorf(CodeInvalidSpec, "domain %q 的 invalidRecords 只能是 backup-and-skip，收到 %q", spec.Name, spec.InvalidRecords)
	}
	if len(spec.Tables) == 0 {
		return Spec{}, Errorf(CodeInvalidSpec, "domain %q 没有声明任何表", spec.Name)
	}
	for table, tableSpec := range spec.Tables {
		if err := ValidateUnitName(table); err != nil {
			return Spec{}, err
		}
		if tableSpec.Schema == nil {
			return Spec{}, Errorf(CodeInvalidSpec, "domain %q 的表 %q 缺少 record schema", spec.Name, table)
		}
	}
	if spec.Global != nil {
		if spec.Global.Schema == nil {
			return Spec{}, Errorf(CodeInvalidSpec, "domain %q 声明了 global 却没有 schema", spec.Name)
		}
		if spec.Global.Schema.Validate(nil) == nil {
			return Spec{}, Errorf(CodeInvalidSpec,
				"domain %q 的 global schema 不许接受 null：null 是介质的\"从未写过\"哨兵，"+
					"可空 global 重开之后无法与缺席区分", spec.Name)
		}
		if err := spec.Global.Schema.Validate(spec.Global.Initial); err != nil {
			return Spec{}, Errorf(CodeInvalidSpec, "domain %q 的 global 初始值不合 schema: %v", spec.Name, err)
		}
	}
	return spec, nil
}

// MustDefineDomain 是包级声明用的 DefineDomain；装配错误直接 panic（启动即失败）。
func MustDefineDomain(spec Spec) Spec {
	validated, err := DefineDomain(spec)
	if err != nil {
		panic(fmt.Sprintf("storage: 非法 domain 声明: %v", err))
	}
	return validated
}

// DescriptorOf 把声明投影成后端打开单元时需要的描述符（表的顺序固定）。
func DescriptorOf(spec Spec) Descriptor {
	tables := make([]string, 0, len(spec.Tables))
	for table := range spec.Tables {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	desc := Descriptor{
		Name:      spec.Name,
		Version:   spec.Version,
		Tables:    tables,
		HasGlobal: spec.Global != nil,
		Layout:    spec.EffectiveLayout(),
	}
	if len(spec.CompatibleVersions) > 0 {
		desc.CompatibleVersions = append([]int(nil), spec.CompatibleVersions...)
	}
	return desc
}

// EffectiveLayout 把声明里的空布局归一。
func (s Spec) EffectiveLayout() Layout {
	if s.Layout == "" {
		return LayoutSingle
	}
	return s.Layout
}
