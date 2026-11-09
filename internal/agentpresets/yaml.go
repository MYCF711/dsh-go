// Package agentpresets renders one host preset as the official agent-preset
// `read` payload: a top-level **entry-list YAML** of plugin rows.
//
// 官方契约（app.asar 0.2.2-rc.1，@deepseek-ai/dsh-agent-preset-registry）：
//
//	readDocument(agentPreset) -> { agentPreset, content, name?, description? }
//	content = yaml.dump(preset.plugins, { schema: entryListSchema })
//
// entryListSchema 的硬判据（dsh-agent-preset-registry/lib/types/definition.js）：
// 顶层必须是数组；每一行必须是 map，且 `name` 是非空字符串；`group: true` 时
// `config` 里再嵌一层 entry-list。官方 `!!js` 条件是 Loader 方言，本宿主的
// preset 是真实工具可见性/审批/沙箱声明，没有表达式节点，所以不伪造 `!!js`。
//
// 本宿主没有"每个 preset 一组独立 Cordis 插件"的装载器；真实组成是
// tools × approval × sandbox。这里把它声明为**一行宿主 preset 插件**的 config，
// 既满足官方 entry-list 形状，也不把不存在的插件列表写成成功。
package agentpresets

import (
	"fmt"
	"strconv"
	"strings"

	"dshgo/internal/modes"
)

// PresetPluginName 是宿主 preset 行在 entry-list 里的插件名。
//
// 这不是官方 @deepseek-ai/dsh-agent-preset 的冒名：行 id 带 dsh-go-preset-
// 前缀，config 里明确写出本宿主的真实字段。官方客户端只读展示，不拿它
// 反向装载。
const PresetPluginName = "dsh-go/agent-preset"

// Document 把一条真实 preset 渲染成 entry-list YAML。
func Document(mode modes.Mode) (string, error) {
	if strings.TrimSpace(mode.ID) == "" {
		return "", fmt.Errorf("agentpresets: preset 缺 id，不能渲染成 entry-list")
	}
	var b strings.Builder
	b.WriteString("- id: ")
	b.WriteString(quote(rowID(mode.ID)))
	b.WriteString("\n  name: ")
	b.WriteString(quote(PresetPluginName))
	b.WriteString("\n  config:\n")
	writeKey(&b, "id", quote(mode.ID), 4)
	writeKey(&b, "name", quote(mode.Name), 4)
	writeKey(&b, "description", quote(mode.Description), 4)
	writeKey(&b, "builtin", strconv.FormatBool(mode.Builtin), 4)
	if len(mode.Tools) == 0 {
		writeKey(&b, "tools", "[]", 4)
	} else {
		b.WriteString("    tools:\n")
		for _, tool := range mode.Tools {
			b.WriteString("      - ")
			b.WriteString(quote(tool))
			b.WriteString("\n")
		}
	}
	if mode.Approval != "" {
		writeKey(&b, "approval", quote(mode.Approval), 4)
	}
	if mode.Sandbox != "" {
		writeKey(&b, "sandbox", quote(mode.Sandbox), 4)
	}
	return b.String(), nil
}

func writeKey(b *strings.Builder, key, value string, indent int) {
	b.WriteString(strings.Repeat(" ", indent))
	b.WriteString(key)
	b.WriteString(": ")
	b.WriteString(value)
	b.WriteString("\n")
}

// quote 用 YAML 双引号标量。JSON/Go 的转义集合是 YAML 双引号的子集：
// 换行、引号、反斜杠与控制字符都能被 js-yaml 正确读回。
func quote(value string) string { return strconv.Quote(value) }

func rowID(presetID string) string {
	var b strings.Builder
	b.WriteString("dsh-go-preset-")
	for _, r := range presetID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}
