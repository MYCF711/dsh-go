// tool_order.go 实现官方 `systemPrompt.toolOrder`：工具在提示里的顺序覆盖。
//
// 官方原件（app.asar:dsh/node_modules/@deepseek-ai/dsh-system-prompt/lib/index.js）：
//
//	:68  const TOOL_ORDER_REST = "<unlisted-tools>";
//	:74  function validateToolOrder(toolOrder)        // 重复名 / 缺 rest 标记报错
//	:89  function orderTools(tools, toolOrder, known) // 未注册名报错、rest 处插入其余工具
//	:221 toolOrder: z.array(z.string()).default(void 0)
//
// 为什么默认必须是"装配顺序"而不是"按名字排序"：宿主以前没有覆盖入口，工具清单
// 就是注册表遍历顺序；**默认行为必须与现在逐字节一致**（会话快照、A/B 语料都
// 建立在这个字节形状上）。官方的 `orderTools` 在未配置时按名字排序，这里不跟：
// 那是官方装配顺序本来就按名字排的结果，不是"排序"这件事本身。
//
// 校验与应用分开（都导出）：
//   - ParseToolOrder 解析配置串（环境变量）并做**与已知工具无关**的校验；
//   - OrderTools 按已知工具表做**与配置无关**的校验 + 重排；
//   - 入口把两个 error 都变成启动失败（fail-loud），绝不静默退回装配顺序。
package prompt

import (
	"fmt"
	"sort"
	"strings"

	"dshgo/internal/spi"
)

// ToolOrderRest 是官方 toolOrder 里的保留标记：未列出的工具插在这个位置。
//
// 它不是一个真工具名——工具 provider 返回同名工具会被拒绝（官方 :90 同理）。
const ToolOrderRest = "<unlisted-tools>"

// ParseToolOrder 解析官方 `systemPrompt.toolOrder` 的宿主形状：逗号分隔的工具名。
//
// 空串（含全空白）= 未配置 = 装配顺序（官方默认 undefined）。非空时必须满足
// 官方的两条配置期校验：同名只许出现一次；必须带 rest 标记（否则没列出的
// 工具会从提示里消失——那是配置事故，不是"隐藏"）。
func ParseToolOrder(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	order := make([]string, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for _, part := range parts {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		if seen[name] {
			return nil, fmt.Errorf("toolOrder 重复列出 %q：每个工具只能出现一次", name)
		}
		seen[name] = true
		order = append(order, name)
	}
	if len(order) == 0 {
		return nil, fmt.Errorf("toolOrder 只有空白：要么不设，要么给出工具名（含 %q 标记）", ToolOrderRest)
	}
	if !seen[ToolOrderRest] {
		return nil, fmt.Errorf("toolOrder 必须包含 %q 这个“其余工具”标记：否则没列出的工具会从提示里消失", ToolOrderRest)
	}
	return order, nil
}

// OrderTools 按官方 toolOrder 重排工具表。
//
// order 为 nil（未配置）= 原样返回，一个字节都不动。配置了就必须能对上：
// 列了未注册的工具名 → error（点名未知项与全部已知工具）；工具表里出现
// rest 保留名 → error。rest 处的工具按名字排序（code-unit，与机器 locale 无关）。
func OrderTools(tools []spi.Tool, order []string) ([]spi.Tool, error) {
	if len(order) == 0 {
		return tools, nil
	}
	known := make(map[string]spi.Tool, len(tools))
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		name := tool.Name()
		if name == ToolOrderRest {
			return nil, fmt.Errorf("工具 provider 返回了保留名 %q（它只属于 toolOrder 的 rest 标记）", ToolOrderRest)
		}
		known[name] = tool
		names = append(names, name)
	}

	listed := make(map[string]bool, len(order))
	for _, name := range order {
		listed[name] = true
	}
	var unknown []string
	for _, name := range order {
		if name == ToolOrderRest {
			continue
		}
		if _, ok := known[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(names)
		quoted := make([]string, 0, len(unknown))
		for _, name := range unknown {
			quoted = append(quoted, fmt.Sprintf("%q", name))
		}
		return nil, fmt.Errorf("toolOrder 列了未注册的工具 %s；已注册：%s",
			strings.Join(quoted, "、"), strings.Join(names, ", "))
	}

	out := make([]spi.Tool, 0, len(tools))
	for _, name := range order {
		if name == ToolOrderRest {
			rest := make([]spi.Tool, 0, len(tools))
			for _, tool := range tools {
				if !listed[tool.Name()] {
					rest = append(rest, tool)
				}
			}
			sort.Slice(rest, func(i, j int) bool { return rest[i].Name() < rest[j].Name() })
			out = append(out, rest...)
			continue
		}
		out = append(out, known[name])
	}
	return out, nil
}
