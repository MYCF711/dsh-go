package main

// `--max-inline-tokens` 的**生产接线**证明（官方 0.2.2 base #79
// `@deepseek-ai/dsh-spill-policy` 的 `config.maxInlineTokens`）。
//
// 这一层以前只有环境变量 `DSHD_MAX_INLINE_TOKENS`：`modules/tools` 的能力是真的，
// 但宿主三个入口（run / serve / acp）里没有一处 flag，`--help` 里也看不见——
// 属于"能力已在、只差接线"。本文件钉住四件事：
//
//  1. `toolsOptions` 真的把值放进 `tools.Options.MaxInlineTokens`（叶子装配）；
//  2. `builtinModules` → `tools.Module` 那条真实装配链上，值真的生效
//     （token 预算臂真的把大结果换掉，且关掉时一个字节都不动）；
//  3. 非法值（负）在**装配期**就炸（官方 "Invalid config fails at load" 形状），
//     不是每次工具调用才炸；
//  4. `serve` 解析一次、透传给子进程 `dshd run` 的 argv 里有这一项
//     （serve 自己不建流水线，它唯一的责任就是原样传下去）。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
)

// inlineTokensStubTool 造一条必然超 token 预算、又**不会**被字符臂先拿走的结果。
type inlineTokensStubTool struct {
	name string
	res  spi.Result
}

func (t *inlineTokensStubTool) Name() string            { return t.name }
func (t *inlineTokensStubTool) Caps() spi.Caps          { return spi.Caps{} }
func (t *inlineTokensStubTool) Description() string     { return "接线测试用的结果发射器" }
func (t *inlineTokensStubTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *inlineTokensStubTool) Exec(context.Context, json.RawMessage) (spi.Result, error) {
	return t.res, nil
}

// 叶子：toolsOptions 把 flag 值原样放进 Options。
func TestToolsOptionsCarriesMaxInlineTokens(t *testing.T) {
	opts := toolsOptions(t.TempDir(), defaultMaxChars, 777)
	if opts.MaxInlineTokens != 777 {
		t.Fatalf("toolsOptions 没把 maxInlineTokens 放进 Options：%d", opts.MaxInlineTokens)
	}
	// 0 = 未配置：必须保持 0（modules/tools 才会回落到 DSHD_MAX_INLINE_TOKENS）。
	if got := toolsOptions(t.TempDir(), defaultMaxChars, 0).MaxInlineTokens; got != 0 {
		t.Fatalf("0 必须原样传下去（未配置语义），得到 %d", got)
	}
}

// 真实装配链：builtinModules(wd, home, maxChars, maxInlineTokens, nil) 的值
// 真的到得了流水线——超预算的大结果被换成"头/尾 + notice + 恢复文件"。
func TestMaxInlineTokensTakesEffectInProductionAssembly(t *testing.T) {
	home := t.TempDir()
	wd := t.TempDir()
	// 字符臂调到很大：这条用例只测 token 臂，别让字符臂先命中。
	const hugeChars = 1 << 20
	body := strings.Repeat("0123456789abcdef", 500) // 8000 字符 ≈ 2000 估算 token，预算 300 必超

	run := func(t *testing.T, maxInlineTokens int) spi.Result {
		t.Helper()
		k := kernel.New("inline-token-wiring")
		defer k.Root().Dispose()
		if _, err := kernel.Assemble(k.Root(), builtinModules(wd, home, hugeChars, maxInlineTokens, nil)); err != nil {
			t.Fatalf("装配失败（maxInlineTokens=%d）: %v", maxInlineTokens, err)
		}
		// 流水线持有的是同一个注册表：装配之后注册的工具对 runner 立刻可见。
		reg, err := kernel.Inject(k.Root(), spi.ToolsKey)
		if err != nil {
			t.Fatal(err)
		}
		if err := reg.Register(&inlineTokensStubTool{name: "stub_emit", res: spi.Result{Text: body}}); err != nil {
			t.Fatal(err)
		}
		runner, err := kernel.Inject(k.Root(), spi.ToolRunnerKey)
		if err != nil {
			t.Fatal(err)
		}
		res, err := runner.Run(context.Background(), "stub_emit", json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("跑工具失败: %v", err)
		}
		return res
	}

	t.Run("配了预算就真的按预算保留", func(t *testing.T) {
		res := run(t, 300)
		if res.Text == body {
			t.Fatal("maxInlineTokens=300 时 8000 字符结果必须被换掉，实际原样内联")
		}
		if !strings.Contains(res.Text, "(Omitted ") {
			t.Fatalf("要给出官方形状的省略说明: %q", res.Text)
		}
		if !strings.Contains(res.Text, "Full formatted result stored at: ") {
			t.Fatalf("要给出恢复文件位置: %q", res.Text)
		}
		if len(res.Artifacts) != 1 {
			t.Fatalf("应登记 1 个恢复文件，得到 %d", len(res.Artifacts))
		}
	})

	t.Run("没配就不动（0 = 未配置）", func(t *testing.T) {
		res := run(t, 0)
		if res.Text != body {
			t.Fatal("maxInlineTokens=0（未配置）时不许改写结果：token 臂必须完全没生效")
		}
		if len(res.Artifacts) != 0 {
			t.Fatalf("未配置时不该落盘，实际 %d 个 artifact", len(res.Artifacts))
		}
	})
}

// 非法值在装配期 fail-loud（官方 index.ts:52-54：插件加载期就 throw，
// 不能拖到每次调用——否则表现为"每次工具调用都失败"，没人想到是参数写错了）。
func TestNegativeMaxInlineTokensFailsAtAssembly(t *testing.T) {
	k := kernel.New("inline-token-negative")
	defer k.Root().Dispose()
	_, err := kernel.Assemble(k.Root(), builtinModules(t.TempDir(), t.TempDir(), defaultMaxChars, -1, nil))
	if err == nil {
		t.Fatal("负的 --max-inline-tokens 必须在装配期报错，不能静默当成 0")
	}
	if !strings.Contains(err.Error(), "MaxInlineTokens") && !strings.Contains(err.Error(), "max-inline-tokens") {
		t.Fatalf("错误必须点名这个参数，实际: %v", err)
	}
}

// serve 不建流水线：它唯一的责任是把这个旋钮原样交给子进程 `dshd run`
// （逐字段拷贝漏一个，界面侧设置的预算就静默退回 run 的写死默认值）。
func TestServeForwardsMaxInlineTokensToRunChild(t *testing.T) {
	opts := &hostRunOptions{maxInlineTokens: 1234}
	argv := strings.Join(opts.args(), " ")
	if !strings.Contains(argv, "--max-inline-tokens 1234") {
		t.Fatalf("serve→run 没把这个预算传下去（argv=%q）", argv)
	}
}
