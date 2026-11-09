package pluginmgr

// 工具声明的校验与翻译。
//
// 三条硬约束（与 dsh.toml 的 wasm 工具声明同源，见 cmd/dshd/wasmload.go 的
// newWasmToolSpec 注释）：
//
//   - 身份来自 manifest，不从客人那里问：Name/Description/Schema/Caps 是稳定性
//     契约（R4），审批与沙箱也靠 Caps 判定——让客人自己声明"我只会读"，
//     等于让它自己给自己发许可证；
//   - 任何一处不对都在**安装/装载**时炸，不留到第一次调用：漏名字是模型看到一个
//     空名字的工具，漏 schema 是参数没法校验，这些在运行期表现为"模型不会用"；
//   - 失败必须能指出"改哪一行"：工具声明的错误是给人改 plugin.json 的。

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"dshgo/internal/spi"
)

// DefaultToolTimeout 是工具声明没写 timeout_ms 时的单次调用预算。
//
// 与 wasmhost.DefaultTimeout 取同一个值：两条装载路径（dsh.toml / 私有包）
// 对同一个工具必须给出同样的超时，否则"同样的插件换个装法行为就变了"。
const DefaultToolTimeout = 10 * time.Second

// validateToolDeclarations 校验包内全部工具声明。空列表合法（不提供能力的包）。
func validateToolDeclarations(tools []ToolDeclaration) error {
	seen := make(map[string]bool, len(tools))
	for index, decl := range tools {
		name := strings.TrimSpace(decl.Name)
		if name == "" {
			return fmt.Errorf("第 %d 个工具没有 name：工具名是模型看到的名字，必须显式给出", index+1)
		}
		if strings.ContainsAny(name, " \t\r\n") {
			return fmt.Errorf("工具名 %q 里有空白字符：它要能被模型原样写进 tool_calls，不能带空格", name)
		}
		if seen[name] {
			return fmt.Errorf("工具名 %q 出现了两次：包内工具名必须唯一（工具表按名字存，第二处登记必然失败）", name)
		}
		seen[name] = true
		if strings.TrimSpace(decl.Description) == "" {
			return fmt.Errorf("工具 %q 没有 description：Description 是模型看到的「什么时候用它」，缺了它模型只能靠猜", name)
		}
		if len(decl.Schema) == 0 || strings.TrimSpace(string(decl.Schema)) == "" {
			return fmt.Errorf(
				`工具 %q 没有 schema：参数 schema 必须写在 plugin.json 里`+
					`（例："schema": {"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}）`, name)
		}
		var probe map[string]any
		if err := json.Unmarshal(decl.Schema, &probe); err != nil || probe == nil {
			return fmt.Errorf("工具 %q 的 schema 不是合法的 JSON 对象：%v（原文 %q）", name, err, string(decl.Schema))
		}
		if _, err := ParseToolCaps(decl.Caps); err != nil {
			return fmt.Errorf("工具 %q: %w", name, err)
		}
		if decl.TimeoutMS < 0 {
			return fmt.Errorf("工具 %q 的 timeout_ms = %d，不能是负数", name, decl.TimeoutMS)
		}
	}
	return nil
}

// ParseToolCaps 把声明里的能力名翻成 spi.Caps。
//
// 默认全 false 不是客人的自我介绍，而是**沙箱强制**的：wasm 宿主不给客人任何
// FS preopen、不传 args/env、只挂 wasi_snapshot_preview1 的最小面（见
// internal/wasmhost/wasmhost.go 的 Load）。声明更严的能力只是让审批更早介入。
func ParseToolCaps(names []string) (spi.Caps, error) {
	var c spi.Caps
	for _, raw := range names {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "":
			// TOML/JSON 里多写一个空串不算错，忽略。
		case "writes":
			c.Writes = true
		case "executes":
			c.Executes = true
		case "network":
			c.Network = true
		case "destructive":
			c.Destructive = true
		default:
			return c, fmt.Errorf(
				"caps 里有不认识的能力 %q：只认 writes / executes / network / destructive"+
					"（与 spi.Caps 的四个字段一一对应，拼错会让审批判错档）", raw)
		}
	}
	return c, nil
}

// ToolTimeout 把声明里的毫秒数翻成 duration（0 = 默认预算）。
func ToolTimeout(ms int) time.Duration {
	if ms <= 0 {
		return DefaultToolTimeout
	}
	return time.Duration(ms) * time.Millisecond
}
