package settingsform

import (
	"fmt"
	"sync"
)

// 命名空间注册面（官方 settings 的"由插件声明 namespace/字段 schema"）。
//
// 既有实现把两个 namespace 写死在 `New` 里：模型/权限是唯一能配的东西。
// 官方的形状是每个插件用 schemastery 声明自己的 namespace 与字段，设置界面
// 只是把注册表 describe 出来。这里补的就是这张表——**注册是 effect**，
// 返回的 disposer 只撤掉这一次注册；重复注册 fail-loud（装配 bug）。
const (
	// FieldTypeString 是默认字段类型（不写类型的老调用点行为不变）。
	FieldTypeString = "string"
	// FieldTypeBoolean 映射官方 schema 的 boolean。
	FieldTypeBoolean = "boolean"
	// FieldTypeInteger 映射官方 schema 的 integer（含最小/最大值）。
	FieldTypeInteger = "integer"
)

// FieldSpec 是插件声明的一个设置字段。
type FieldSpec struct {
	Name string
	// Type 为空等价 FieldTypeString。
	Type string
	// Enum 只对 string 字段生效。
	Enum []string
	// MinLength / MaxLength 只对 string 字段生效（按 UTF-8 字节计）。
	MinLength *int
	MaxLength *int
	// Minimum / Maximum 只对 integer 字段生效。
	Minimum *int64
	Maximum *int64
	// Secret 表示"只报有没有值，值本身从不上线"（官方 SettingsNamespaceView.secrets）。
	Secret bool
}

// NamespaceSpec 是一个命名空间的声明。
type NamespaceSpec struct {
	NS      string
	Title   string
	Applies string
	Fields  []FieldSpec
}

// Registry 是已注册命名空间的有序表。
type Registry struct {
	mu    sync.RWMutex
	specs []NamespaceSpec
}

// NewRegistry 建一张空注册表。
func NewRegistry() *Registry { return &Registry{} }

// Register 注册一个命名空间。任何装配错误都在服务创建之前失败。
func (r *Registry) Register(spec NamespaceSpec) (func(), error) {
	if spec.NS == "" {
		return nil, &Error{Code: "bad_request", Message: "设置命名空间不能为空", Hint: "给一个稳定的 ns 名字", Status: 400}
	}
	if len(spec.Fields) == 0 {
		return nil, &Error{
			Code:    "bad_request",
			Message: fmt.Sprintf("设置命名空间 %q 没有字段", spec.NS),
			Hint:    "没有字段的命名空间不许拿空 schema 假装可用",
			Status:  400,
		}
	}
	seen := make(map[string]bool, len(spec.Fields))
	for _, field := range spec.Fields {
		if field.Name == "" {
			return nil, &Error{Code: "bad_request", Message: "设置字段名不能为空", Status: 400}
		}
		if seen[field.Name] {
			return nil, &Error{
				Code:    "bad_request",
				Message: fmt.Sprintf("设置命名空间 %q 的字段 %q 重复", spec.NS, field.Name),
				Status:  400,
			}
		}
		seen[field.Name] = true
		switch field.Type {
		case "", FieldTypeString, FieldTypeBoolean, FieldTypeInteger:
		default:
			return nil, &Error{
				Code:    "bad_request",
				Message: fmt.Sprintf("设置字段 %s.%s 的类型 %q 不受支持", spec.NS, field.Name, field.Type),
				Hint:    "只支持 string / boolean / integer",
				Status:  400,
			}
		}
		if field.MinLength != nil && field.MaxLength != nil && *field.MinLength > *field.MaxLength {
			return nil, &Error{
				Code:    "bad_request",
				Message: fmt.Sprintf("设置字段 %s.%s 的 minLength > maxLength", spec.NS, field.Name),
				Status:  400,
			}
		}
		if field.Minimum != nil && field.Maximum != nil && *field.Minimum > *field.Maximum {
			return nil, &Error{
				Code:    "bad_request",
				Message: fmt.Sprintf("设置字段 %s.%s 的 minimum > maximum", spec.NS, field.Name),
				Status:  400,
			}
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.specs {
		if existing.NS == spec.NS {
			return nil, &Error{
				Code:    "duplicate_namespace",
				Message: fmt.Sprintf("设置命名空间 %q 已经注册过了", spec.NS),
				Hint:    "同一个 ns 只能注册一次；重复注册是装配 bug",
				Status:  409,
			}
		}
	}
	r.specs = append(r.specs, cloneNamespaceSpec(spec))
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		for index, existing := range r.specs {
			if existing.NS == spec.NS {
				r.specs = append(r.specs[:index], r.specs[index+1:]...)
				return
			}
		}
	}, nil
}

// Specs 返回注册顺序的声明快照（调用方改不动内部状态）。
func (r *Registry) Specs() []NamespaceSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]NamespaceSpec, 0, len(r.specs))
	for _, spec := range r.specs {
		out = append(out, cloneNamespaceSpec(spec))
	}
	return out
}

func cloneNamespaceSpec(spec NamespaceSpec) NamespaceSpec {
	clone := spec
	clone.Fields = make([]FieldSpec, len(spec.Fields))
	for index, field := range spec.Fields {
		copied := field
		copied.Enum = append([]string(nil), field.Enum...)
		if field.MinLength != nil {
			value := *field.MinLength
			copied.MinLength = &value
		}
		if field.MaxLength != nil {
			value := *field.MaxLength
			copied.MaxLength = &value
		}
		if field.Minimum != nil {
			value := *field.Minimum
			copied.Minimum = &value
		}
		if field.Maximum != nil {
			value := *field.Maximum
			copied.Maximum = &value
		}
		clone.Fields[index] = copied
	}
	return clone
}

// DefaultRegistry 返回宿主当前真实存在的两个命名空间（model / permission）。
//
// 名字、标题、枚举与长度上限逐字保留：`settings.describe` 是线上 58 个方法
// 之一，形状不能改。
func DefaultRegistry() *Registry {
	one, max := 1, 256
	registry := NewRegistry()
	if _, err := registry.Register(NamespaceSpec{
		NS:      NamespaceModel,
		Title:   "Default model",
		Applies: "live",
		Fields:  []FieldSpec{{Name: FieldDefaultModel, MinLength: &one, MaxLength: &max}},
	}); err != nil {
		panic(fmt.Sprintf("settingsform: 默认 model 命名空间非法: %v", err))
	}
	if _, err := registry.Register(NamespaceSpec{
		NS:      NamespacePermission,
		Title:   "Default permission",
		Applies: "live",
		Fields:  []FieldSpec{{Name: FieldDefaultPreset, Enum: []string{"deny", "ask", "allow", "auto"}}},
	}); err != nil {
		panic(fmt.Sprintf("settingsform: 默认 permission 命名空间非法: %v", err))
	}
	return registry
}
