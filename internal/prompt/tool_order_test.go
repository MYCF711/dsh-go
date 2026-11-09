package prompt

import (
	"strings"
	"testing"

	"dshgo/internal/spi"
)

// GO.7 / t11 #25：官方 `systemPrompt.toolOrder`（默认 undefined = 装配顺序）。
//
// 官方原件（app.asar:dsh/node_modules/@deepseek-ai/dsh-system-prompt/lib/index.js）：
//
//	:68   const TOOL_ORDER_REST = "<unlisted-tools>";   // 其余工具插在这里
//	:74   function validateToolOrder(toolOrder) { … 重复名报错 …
//	:81     if (!seen.has("<unlisted-tools>")) throw new Error(`toolOrder must contain …`)
//	:89   function orderTools(tools, toolOrder, knownNames) {
//	:92     const unknown = toolOrder.filter((n) => n !== REST && !knownNames.has(n));
//	:93     if (unknown.length > 0) throw new Error(`toolOrder lists unregistered tool …`)
//	:95     const rest = tools.filter((t) => !listed.has(t.name)).sort(compareToolNames);
//	:96     return toolOrder.flatMap((n) => n === REST ? rest : tools.filter((t) => t.name === n));
//
// 宿主以前没有任何覆盖入口：工具顺序 = 注册表遍历顺序，想调顺序得改代码。
// 默认（未配置）必须与现在**逐字节一致**——先红后绿的第一条就是它。

func fakeTools(names ...string) []spi.Tool {
	out := make([]spi.Tool, 0, len(names))
	for _, name := range names {
		out = append(out, fakeReal{name: name})
	}
	return out
}

func toolNames(tools []spi.Tool) string {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name())
	}
	return strings.Join(names, ",")
}

// 未配置（官方默认 undefined）= 装配顺序**原样**，不是按名字排序。
func TestOrderToolsNilKeepsAssemblyOrder(t *testing.T) {
	// 故意给一个不是字典序的装配顺序：排序实现会把它变成 read,shell,write。
	tools := fakeTools("write", "read", "shell")
	got, err := OrderTools(tools, nil)
	if err != nil {
		t.Fatalf("未配置不该报错: %v", err)
	}
	if names := toolNames(got); names != "write,read,shell" {
		t.Fatalf("未配置必须保持装配顺序 write,read,shell，实际 %s", names)
	}
}

// 空串 = 未配置（环境变量没设/只有空白）。
func TestParseToolOrderEmptyIsNil(t *testing.T) {
	for _, raw := range []string{"", "   ", "\t\n"} {
		order, err := ParseToolOrder(raw)
		if err != nil {
			t.Fatalf("%q 应当等于未配置: %v", raw, err)
		}
		if order != nil {
			t.Fatalf("%q 应当得到 nil，实际 %#v", raw, order)
		}
	}
}

func TestParseToolOrderSplitsAndTrims(t *testing.T) {
	order, err := ParseToolOrder(" read, write ,<unlisted-tools> , ")
	if err != nil {
		t.Fatalf("合法配置不该报错: %v", err)
	}
	want := []string{"read", "write", ToolOrderRest}
	if len(order) != len(want) {
		t.Fatalf("得到 %#v，期望 %#v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("第 %d 项 = %q，期望 %q（整份 %#v）", i, order[i], want[i], order)
		}
	}
}

// 重复列出同一个工具 = 配置错误（官方 validateToolOrder 第一条）。
func TestParseToolOrderRejectsDuplicate(t *testing.T) {
	if _, err := ParseToolOrder("read,read," + ToolOrderRest); err == nil {
		t.Fatal("重复工具名必须报错")
	}
}

// 没有 rest 标记 = 未列出的工具会消失，官方明确报错。
func TestParseToolOrderRequiresRestMarker(t *testing.T) {
	_, err := ParseToolOrder("read,write")
	if err == nil {
		t.Fatal("缺 <unlisted-tools> 必须报错")
	}
	if !strings.Contains(err.Error(), ToolOrderRest) {
		t.Fatalf("错误信息要点名标记 %q，实际 %v", ToolOrderRest, err)
	}
}

// 列出的工具按配置顺序，未列出的插在 rest 标记处（保持装配顺序里的相对顺序）。
func TestOrderToolsListedThenRest(t *testing.T) {
	tools := fakeTools("read", "write", "shell", "grep")
	got, err := OrderTools(tools, []string{"write", ToolOrderRest, "read"})
	if err != nil {
		t.Fatalf("合法配置不该报错: %v", err)
	}
	// rest = 未列出的工具，按名字排序（官方 compareToolNames）：grep, shell。
	if names := toolNames(got); names != "write,grep,shell,read" {
		t.Fatalf("顺序 = %s，期望 write,grep,shell,read", names)
	}
}

// 列了未注册的工具 = 配置错误（官方 orderTools 第 93 行的报错）。
func TestOrderToolsUnknownNameFails(t *testing.T) {
	_, err := OrderTools(fakeTools("read"), []string{"read", "ghost", ToolOrderRest})
	if err == nil {
		t.Fatal("未注册的工具名必须报错")
	}
	for _, want := range []string{"ghost", "read"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误信息要同时点名 %q 与已知工具，实际 %v", want, err)
		}
	}
}

// 工具 provider 自己叫 <unlisted-tools> 是保留名冲突（官方 orderTools 第 90 行）。
func TestOrderToolsRejectsReservedToolName(t *testing.T) {
	if _, err := OrderTools(fakeTools("read", ToolOrderRest), []string{ToolOrderRest}); err == nil {
		t.Fatal("注册了保留名必须报错")
	}
}

// 提示里的工具清单要按配置顺序渲染（ToolGuide 读的是 Context.Tools 的顺序）。
func TestToolGuideRendersConfiguredOrder(t *testing.T) {
	ordered, err := OrderTools(fakeTools("b", "c", "a"), []string{"c", ToolOrderRest, "b"})
	if err != nil {
		t.Fatalf("合法配置不该报错: %v", err)
	}
	got := Assemble(Context{Tools: ordered}, ToolGuide{})
	if !strings.Contains(got, "只读工具：c, a, b。") {
		t.Fatalf("提示里的工具顺序应当是 c, a, b，实际：\n%s", got)
	}
}
