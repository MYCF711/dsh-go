// Package modes 是"模式（preset）"：决定一次会话里模型能看到哪些工具。
//
// PLAN §6 的三条硬规矩：
//  1. **内置模式只读**（`Builtin = true`）——用户层 patch 命中即报错，不静默覆盖；
//  2. 工具可见性由模式决定，**模式在一次会话内不变** → 工具表稳定，不击穿前缀缓存；
//  3. 装卸类工具原则上只在用户自建模式下可见；唯一例外是官方 cordis 组合
//     明确声明的 plugin / plugin_manager，其他内置模式仍不给。
package modes

import (
	"fmt"
	"runtime"
	"strings"

	"dshgo/internal/sandbox"
)

// Mode 是一个模式。
type Mode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Builtin     bool   `json:"builtin"`
	// Tools 是白名单；为空表示"全部可见"。
	Tools []string `json:"tools,omitempty"`
	// PromptPrefix 是该模式的 persona 首段；{{model}} 在每次装配时替换成真实模型。
	// 空 = 不添加 persona，宿主原系统提示逐字节不变。
	PromptPrefix string `json:"promptPrefix,omitempty"`
	// Approval 是该模式的审批倾向（空 = 沿用宿主配置）。
	Approval string `json:"approval,omitempty"`
	// Sandbox 是该模式对应的真实 runtime sandbox 档位：
	// read-only / workspace-write / danger-full-access。
	//
	// 空 = workspace-write（安全默认：只收窄，不放宽）。显式写了未知值，
	// 装配期 LoadDir 与 SandboxMode 都会 fail-loud，不静默降级。
	Sandbox string `json:"sandbox,omitempty"`
}

// SandboxMode 把会话 mode 映射成 runtime sandbox 档位。
//
// 映射是**真值**，不是显示值：工具、FS、命令闸门都按它判定。
// 未知 sandbox 字段给出 ErrUnknownMode，绝不回落到某一档。
func (m Mode) SandboxMode() (sandbox.Mode, error) {
	raw := m.Sandbox
	if raw == "" {
		return sandbox.ModeWorkspaceWrite, nil
	}
	mode, err := sandbox.ParseMode(raw)
	if err != nil {
		return "", fmt.Errorf("模式 %q 的 sandbox=%q 无效: %w", m.ID, m.Sandbox, err)
	}
	return mode, nil
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

// CordisID 是官方 Web Creator 选择的 agentPreset。
const CordisID = "cordis"

// CordisPromptPrefix 逐字对应官方 cordis.patch.yml 的 folded prefix。
// {{model}} 不是给用户看的占位符：ACP / run 装配时必须换成当轮真实模型。
const CordisPromptPrefix = "You are a coding agent powered by the {{model}} model. You help the user customize DeepSeek Harness itself."

// PersonaPrefix 把模式里的模型占位符替换成本轮真实模型。
func (m Mode) PersonaPrefix(model string) string {
	return strings.ReplaceAll(m.PromptPrefix, "{{model}}", model)
}

// cordisTools 是官方组合在本宿主真实存在的工具映射。
// tool-cordis 的 cordis_inspect_* 没有宿主实现，绝不能因为官方 entry-list
// 声明了它就把它加进运行时白名单。Windows / 非 Windows 的 shell 条件与官方一致。
func cordisTools() []string {
	tools := []string{
		"read", "write", "edit", "glob", "grep",
		"job_list", "job_output", "job_kill",
		"schedule_create", "schedule_list", "schedule_view", "schedule_edit",
		"create_goal", "get_goal", "update_goal", "exit_plan_mode",
		"subagent", "subagent_fork", "workflow", "ask_user_question", "todo_write",
		"web_search", "web_fetch", "skill", "present", "plugin", "plugin_manager",
	}
	if runtime.GOOS == "windows" {
		return append(tools, "pwsh")
	}
	return append(tools, "bash")
}

// Builtins 返回编译进二进制的内置模式（只读）。
func Builtins() []Mode {
	return []Mode{
		{
			ID:          DefaultID,
			Name:        "Default",
			Description: "全部工具可用；写入限制在 canonical 工作区内（workspace-write 档）。",
			Builtin:     true,
			Sandbox:     string(sandbox.ModeWorkspaceWrite),
		},
		{
			ID:          "read-only",
			Name:        "Read only",
			Description: "只能读：read / glob / grep / history_search。用来审阅代码或做排查。",
			Builtin:     true,
			Tools:       []string{"read", "glob", "grep", "history_search"},
			Approval:    "deny",
			Sandbox:     string(sandbox.ModeReadOnly),
		},
		{
			ID:          "workspace-write",
			Name:        "Workspace write",
			Description: "全部工具可用；只允许写 canonical 工作区，命令 cwd 也必须在工作区内。",
			Builtin:     true,
			Sandbox:     string(sandbox.ModeWorkspaceWrite),
		},
		{
			ID:          "danger-full-access",
			Name:        "Danger full access",
			Description: "全部工具可用；不虚构路径沙盒，写/执行可越出工作区（仍保留审批与读后写）。",
			Builtin:     true,
			Sandbox:     string(sandbox.ModeDangerFullAccess),
		},
		{
			ID:           CordisID,
			Name:         "Cordis",
			Description:  "官方 Creator 组合的真实宿主映射：定制 DeepSeek Harness；不含宿主未实现的 cordis_inspect 工具。",
			Builtin:      true,
			Tools:        cordisTools(),
			PromptPrefix: CordisPromptPrefix,
			Sandbox:      string(sandbox.ModeWorkspaceWrite),
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
