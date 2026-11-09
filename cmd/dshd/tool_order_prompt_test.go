package main

// 官方 `systemPrompt.toolOrder`（宿主：`DSHD_SYSTEM_PROMPT_TOOL_ORDER`）的接线。
//
// 官方原件（app.asar:dsh/node_modules/@deepseek-ai/dsh-system-prompt/lib/index.js:221）：
//   toolOrder: z.array(z.string()).default(void 0)
// 语义（:68/:89-96）：列出的工具按配置顺序排，`<unlisted-tools>` 处插入未列出的
// 工具（按名字排序）；默认 undefined = 装配顺序。
//
// 判据（缺一即红）：
//  1. 设了就必须**真的改变提示里的工具顺序**（不是只被读了一下）；
//  2. 没设时提示词逐字节不变（默认形状不能被这条新入口带偏）；
//  3. 配置非法（重复名 / 缺 rest 标记 / 列了未注册的工具）必须 fail-loud，
//     而不是静默退回装配顺序——写错的配置看起来"生效了"是最难查的。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"dshgo/internal/prompt"
	"dshgo/internal/spi"
)

// fixedRegistry 是固定工具表的 spi.ToolRegistry（提示装配只需要顺序）。
type fixedRegistry []spi.Tool

func (r fixedRegistry) Register(spi.Tool) error     { return nil }
func (r fixedRegistry) Tools() []spi.Tool           { return r }
func (r fixedRegistry) Get(string) (spi.Tool, bool) { return nil, false }

// namedTool 是一个只有名字的只读工具（ToolGuide 会把它列进"只读工具"）。
type namedTool struct{ name string }

func (t namedTool) Name() string            { return t.name }
func (t namedTool) Description() string     { return "d" }
func (t namedTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t namedTool) Caps() spi.Caps          { return spi.Caps{} }
func (t namedTool) Exec(context.Context, json.RawMessage) (spi.Result, error) {
	return spi.Result{}, nil
}

func registryOf(names ...string) fixedRegistry {
	out := make(fixedRegistry, 0, len(names))
	for _, name := range names {
		out = append(out, namedTool{name: name})
	}
	return out
}

func TestToolOrderEnvReachesSystemPrompt(t *testing.T) {
	t.Setenv(toolOrderEnv, "write,"+prompt.ToolOrderRest+"")
	dir := t.TempDir()
	got, err := buildSystemPromptWithHome(registryOf("read", "write", "shell"), "m", dir, "", defaultWorkspaceBytes)
	if err != nil {
		t.Fatalf("合法配置不该报错: %v", err)
	}
	// write 先出现；其余（read, shell）按名字排序插在 rest 处。
	if !strings.Contains(got, "只读工具：write, read, shell。") {
		t.Fatalf("提示里的工具顺序没有按配置重排：\n%s", got)
	}
}

func TestToolOrderUnsetKeepsPromptBytes(t *testing.T) {
	dir := t.TempDir()
	reg := registryOf("write", "read", "shell")
	t.Setenv(toolOrderEnv, "")
	a, err := buildSystemPromptWithHome(reg, "m", dir, "", defaultWorkspaceBytes)
	if err != nil {
		t.Fatal(err)
	}
	// 未设置 = 装配顺序（write,read,shell），不是字典序（read,shell,write）。
	if !strings.Contains(a, "只读工具：write, read, shell。") {
		t.Fatalf("未配置必须保持装配顺序：\n%s", a)
	}
}

func TestToolOrderInvalidConfigFailsLoud(t *testing.T) {
	cases := []struct {
		name   string
		value  string
		substr string
	}{
		{"缺 rest 标记", "read,write", "unlisted-tools"},
		{"重复工具", "read,read," + prompt.ToolOrderRest, "重复"},
		{"未注册工具", "ghost," + prompt.ToolOrderRest, "ghost"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(toolOrderEnv, c.value)
			if _, err := buildSystemPromptWithHome(registryOf("read", "write"), "m", t.TempDir(), "", defaultWorkspaceBytes); err == nil {
				t.Fatal("非法配置必须 fail-loud，不能静默退回装配顺序")
			} else if !strings.Contains(err.Error(), c.substr) {
				t.Fatalf("错误信息要包含 %q，实际 %v", c.substr, err)
			}
		})
	}
}
