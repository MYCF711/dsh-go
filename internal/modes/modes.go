// Package modes 是"模式（preset）"：决定一次会话里模型能看到哪些工具。
//
// PLAN §6 的三条硬规矩：
//  1. **内置模式只读**（`Builtin = true`）——用户层 patch 命中即报错，不静默覆盖；
//  2. 工具可见性由模式决定，**模式在一次会话内不变** → 工具表稳定，不击穿前缀缓存；
//  3. 装卸类工具只在用户自建模式下可见（内置模式一律不给）。
package modes

import "fmt"

// Mode 是一个模式。
type Mode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Builtin     bool   `json:"builtin"`
	// Tools 是白名单；为空表示"全部可见"。
	Tools []string `json:"tools,omitempty"`
	// Approval 是该模式的审批倾向（空 = 沿用宿主配置）。
	Approval string `json:"approval,omitempty"`
}

// Allows 判断某个工具在这个模式下是否可见。
func (m Mode) Allows(tool string) bool {
	if len(m.Tools) == 0 {
		return true
	}
	for _, t := range m.Tools {
		if t == tool {
			return true
		}
	}
	return false
}

// DefaultID 是默认模式。
const DefaultID = "default"

// Builtins 返回编译进二进制的内置模式（只读）。
func Builtins() []Mode {
	return []Mode{
		{
			ID:          DefaultID,
			Name:        "Default",
			Description: "全部工具可用。",
			Builtin:     true,
		},
		{
			ID:          "read-only",
			Name:        "Read only",
			Description: "只能读：read / glob / grep。用来审阅代码或做排查。",
			Builtin:     true,
			Tools:       []string{"read", "glob", "grep"},
			Approval:    "deny",
		},
	}
}

// Lookup 按 id 找内置模式。
func Lookup(id string) (Mode, bool) {
	if id == "" {
		id = DefaultID
	}
	for _, m := range Builtins() {
		if m.ID == id {
			return m, true
		}
	}
	return Mode{}, false
}

// Resolve 返回模式；未知模式返回错误（**不静默回落到 default**）。
func Resolve(id string) (Mode, error) {
	m, ok := Lookup(id)
	if !ok {
		return Mode{}, fmt.Errorf("未知模式 %q（可选：%s）", id, IDs())
	}
	return m, nil
}

// IDs 列出所有内置模式 id。
func IDs() string {
	ms := Builtins()
	out := ""
	for i, m := range ms {
		if i > 0 {
			out += ", "
		}
		out += m.ID
	}
	return out
}

// OverrideBuiltin 检查"用户层是否试图改写内置模式"。命中即报错（PLAN §6）。
func OverrideBuiltin(id string) error {
	if m, ok := Lookup(id); ok && m.Builtin {
		return fmt.Errorf("模式 %q 是内置的，只读；想改就从它复制一份新 id", id)
	}
	return nil
}
