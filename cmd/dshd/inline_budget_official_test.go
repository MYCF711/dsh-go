package main

// 工具结果预算的两个旋钮：**出厂值只许来自官方**。
//
// # 改前的缺陷（用户 2026-11-21：「很多不是我设置的参数」）
//
//  1. `--max-inline-tokens` 出厂 0。官方 `dsh-spill-policy` 的语义是
//     "omitting maxInlineTokens disables retention"（0/省略 = **关闭**），
//     所以出厂等于把官方能力整个关掉，只有知道 `DSHD_MAX_INLINE_TOKENS` 的人能打开。
//  2. `--max-chars` 出厂 4000（`defaultMaxChars`）。官方 spill-policy **没有**按字符
//     的旋钮（最接近的 `thresholdChars: 8192` 属于另一个插件
//     `dsh-compaction-tool-result-pruner`）——4000 是我们自己拍的。
//
// 两个一起才有这个后果：4000 字符 ≈ 1000 估算 token，远小于官方 12500，
// 于是字符臂**永远先赢**，官方预算一次都没生效，模型看到的工具输出恒被截在 4000 字符。
//
// # 官方原件
//
// `app.asar:dsh/node_modules/@deepseek-ai/dsh-base/cordis.patch.yml:417`
// （`spill-policy` 的 `config: maxInlineTokens: 12500`），机器可读清单
// `tmp/GO3-PARAMS/official.json` 的 `toolResult.spill.maxInlineTokens` 同值。
//
// # 四格判据
//
//	| 场景                          | 期望                                       |
//	|------------------------------|--------------------------------------------|
//	| 出厂默认                      | 走官方 token 预算 12500，不被字符阈值截断   |
//	| 显式 --max-chars=4000         | 4000 生效（覆盖手段还在）                   |
//	| 显式 --max-inline-tokens=N    | N 生效                                      |
//	| DSHD_MAX_INLINE_TOKENS        | 生效                                        |
//
// 每一格都走**生产的注册函数**（run/acp/serve 共用 `addSpillBudgetFlags`）与
// **生产的装配链**（`builtinModules` → `tools.Module`），不是给 Options 直赋字段。

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
	"dshgo/modules/tools"
)

// parseSpillBudget 用生产的注册函数解析两个旋钮。
//
// 三个入口（run / acp / serve）注册的是**同一个** addSpillBudgetFlags，所以测它就
// 等于测三个入口的解析口径（与 `--auto-compact` 的 addAutoCompactFlag 同一手法）。
func parseSpillBudget(t *testing.T, argv []string) (maxChars, maxInlineTokens int) {
	t.Helper()
	fs := flag.NewFlagSet("spill-budget", flag.ContinueOnError)
	addSpillBudgetFlags(fs, &maxChars, &maxInlineTokens)
	if err := fs.Parse(argv); err != nil {
		t.Fatalf("解析 %v 失败：%v", argv, err)
	}
	return maxChars, maxInlineTokens
}

// runSpillProbe 用生产装配链跑一条必然"够大"的工具结果。
//
// 工具名不叫 read：`read` 是官方明文的例外（spill-policy 不接管 read 的结果，
// 否则"读回被外置的全文"会二次外置死循环），见 modules/tools/spill_policy.go。
func runSpillProbe(t *testing.T, maxChars, maxInlineTokens int, body string) spi.Result {
	t.Helper()
	home := t.TempDir()
	wd := t.TempDir()
	k := kernel.New("inline-budget-official")
	defer k.Root().Dispose()
	if _, err := kernel.Assemble(k.Root(), builtinModules(wd, home, maxChars, maxInlineTokens, nil)); err != nil {
		t.Fatalf("装配失败（maxChars=%d maxInlineTokens=%d）: %v", maxChars, maxInlineTokens, err)
	}
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

// asciiBody 造 n 个 ASCII 字符：Estimator 是 ascii/4 + 其它，所以 n/4 就是估算 token。
func asciiBody(n int) string { return strings.Repeat("x", n) }

// TestInlineBudgetOfficialDefaults 是这次修复的主判据（四格 + 官方值边界）。
func TestInlineBudgetOfficialDefaults(t *testing.T) {
	// 1) 出厂值：token 预算 = 官方 12500，字符臂 = 不参与（0）。
	t.Run("出厂默认", func(t *testing.T) {
		t.Setenv("DSHD_MAX_INLINE_TOKENS", "") // 隔离环境：只看出厂值
		maxChars, maxInlineTokens := parseSpillBudget(t, nil)
		if maxChars != 0 {
			t.Fatalf("--max-chars 出厂必须是 0（不参与），得到 %d：官方 spill-policy 没有字符旋钮", maxChars)
		}
		if maxInlineTokens != tools.DefaultMaxInlineTokens {
			t.Fatalf("--max-inline-tokens 出厂必须是官方 %d，得到 %d",
				tools.DefaultMaxInlineTokens, maxInlineTokens)
		}
		// 9000 字符 ≈ 2250 估算 token：官方预算内 → 一个字都不许动。
		// 改前（maxChars=4000）这一格会被字符臂截断，所以它同时钉住
		// "出厂不被字符阈值提前截断"。
		body := asciiBody(9000)
		res := runSpillProbe(t, maxChars, maxInlineTokens, body)
		if res.Text != body {
			t.Fatalf("出厂默认下 9000 字符（≈2250 token）被改动了：Text %d 字符，原 %d 字符",
				len([]rune(res.Text)), len([]rune(body)))
		}
		if res.Truncated != nil || len(res.Artifacts) != 0 {
			t.Fatalf("出厂默认不该外置/截断，得到 Truncated=%v Artifacts=%d", res.Truncated, len(res.Artifacts))
		}
		t.Logf("出厂(max-chars=0,max-inline-tokens=%d)：9000 字符(≈2250 token) → 保留 %d 字符，Truncated=%v，Artifacts=%d",
			maxInlineTokens, len([]rune(res.Text)), res.Truncated != nil, len(res.Artifacts))

		// 把"12500"这个数钉死：
		//   50000 字符 = 12500 估算 token（== 预算）→ 预算内，原样
		//   50004 字符 = 12501 估算 token（> 预算）→ 超预算，必须换头/尾
		// 两格夹起来，预算只能是 12500 本身。
		atBudget := asciiBody(50000)
		if got := runSpillProbe(t, maxChars, maxInlineTokens, atBudget); got.Text != atBudget {
			t.Fatalf("恰好 12500 估算 token 的结果必须原样内联（官方 total<=budget 语义），实际 %d 字符",
				len([]rune(got.Text)))
		}
		overBudget := asciiBody(50004)
		over := runSpillProbe(t, maxChars, maxInlineTokens, overBudget)
		if over.Text == overBudget {
			t.Fatalf("12501 估算 token（刚超官方 12500 一格）必须被 token 臂换掉，实际原样内联")
		}
		if !strings.Contains(over.Text, "(Omitted ") {
			t.Fatalf("超预算必须给官方形状的省略说明：%.120q", over.Text)
		}
		t.Logf("出厂边界：50000 字符(12500 token)==预算 → 原样；50004 字符(12501 token) → 保留 %d 字符 + notice",
			len([]rune(over.Text)))
	})

	// 2) 显式 --max-chars=4000：字符臂的覆盖手段还在。
	t.Run("显式max-chars", func(t *testing.T) {
		t.Setenv("DSHD_MAX_INLINE_TOKENS", "")
		maxChars, maxInlineTokens := parseSpillBudget(t, []string{"--max-chars", "4000"})
		if maxChars != 4000 {
			t.Fatalf("显式 --max-chars=4000 没生效：%d", maxChars)
		}
		body := asciiBody(9000)
		res := runSpillProbe(t, maxChars, maxInlineTokens, body)
		if res.Text == body {
			t.Fatal("显式 --max-chars=4000 时 9000 字符必须被字符臂截断，实际原样内联")
		}
		// Data 为空时字符臂只截正文（不落 artifact），所以指针是空的——
		// 这条契约由 omittedNotice 的"无指针"分支兜住，模型仍然知道中间少了东西。
		if res.Truncated == nil {
			t.Fatalf("字符臂截断必须留下 Truncated 证据，得到 nil（Text %.60q）", res.Text)
		}
		if !strings.Contains(res.Text, "…（中间省略 ") {
			t.Fatalf("字符臂截断必须带省略说明：%.120q", res.Text)
		}
		if seen := len([]rune(res.Text)); seen > 4000 {
			t.Fatalf("模型可见正文 %d 字符，超过显式阈值 4000", seen)
		}
		t.Logf("--max-chars=4000：9000 字符 → 保留 %d 字符，Truncated=%v", len([]rune(res.Text)), res.Truncated != nil)
	})

	// 3) 显式 --max-inline-tokens=N：token 臂的覆盖手段还在。
	t.Run("显式max-inline-tokens", func(t *testing.T) {
		t.Setenv("DSHD_MAX_INLINE_TOKENS", "")
		maxChars, maxInlineTokens := parseSpillBudget(t, []string{"--max-inline-tokens", "300"})
		if maxInlineTokens != 300 {
			t.Fatalf("显式 --max-inline-tokens=300 没生效：%d", maxInlineTokens)
		}
		if maxChars != 0 {
			t.Fatalf("只给 token 预算时字符臂必须仍然不参与，得到 %d", maxChars)
		}
		res := runSpillProbe(t, maxChars, maxInlineTokens, asciiBody(9000))
		if !strings.Contains(res.Text, "(Omitted ") || !strings.Contains(res.Text, "Full formatted result stored at: ") {
			t.Fatalf("预算 300 时 9000 字符必须被 token 臂换成头/尾 + 恢复文件位置：%.160q", res.Text)
		}
		if len(res.Artifacts) != 1 {
			t.Fatalf("应登记 1 个恢复文件，得到 %d", len(res.Artifacts))
		}
		t.Logf("--max-inline-tokens=300：9000 字符 → 保留 %d 字符，Artifacts=%d", len([]rune(res.Text)), len(res.Artifacts))
	})

	// 4) DSHD_MAX_INLINE_TOKENS：环境变量只是**默认值**，显式 flag 仍然优先。
	t.Run("环境变量", func(t *testing.T) {
		t.Setenv("DSHD_MAX_INLINE_TOKENS", "300")
		maxChars, maxInlineTokens := parseSpillBudget(t, nil)
		if maxInlineTokens != 300 {
			t.Fatalf("DSHD_MAX_INLINE_TOKENS=300 没成为默认值：%d", maxInlineTokens)
		}
		res := runSpillProbe(t, maxChars, maxInlineTokens, asciiBody(9000))
		if !strings.Contains(res.Text, "(Omitted ") {
			t.Fatalf("环境变量给的预算必须真的生效：%.160q", res.Text)
		}
		t.Logf("DSHD_MAX_INLINE_TOKENS=300：9000 字符 → 保留 %d 字符，Artifacts=%d", len([]rune(res.Text)), len(res.Artifacts))
		// 显式 flag 压过环境变量（env 只能当默认值）。
		if _, explicit := parseSpillBudget(t, []string{"--max-inline-tokens", "9000"}); explicit != 9000 {
			t.Fatalf("显式 flag 必须压过环境变量，得到 %d", explicit)
		}
	})
}

// TestGuardSpillBudgetFlagsRegisteredByAllEntries 钉住"三入口共用同一处注册"。
//
// 四个入口里 run / acp / serve 必须真的调用 addSpillBudgetFlags：只是把函数写出来
// 而某个入口没调，那个入口的 `--max-chars`/`--max-inline-tokens` 就会回到
// flagset 里"没有这个旗标"，而四格判据（测的是函数本身）照样全绿。
func TestGuardSpillBudgetFlagsRegisteredByAllEntries(t *testing.T) {
	call := "addSpillBudgetFlags(fs,"
	for _, file := range []string{"main.go", "acp.go", "host_run_options.go"} {
		raw, err := os.ReadFile(filepath.Join(".", file))
		if err != nil {
			t.Fatalf("读 %s 失败: %v", file, err)
		}
		if strings.Count(string(raw), call) != 1 {
			t.Errorf("%s 里 %q 必须恰好出现一次（run/acp/serve 三入口共用同一处注册），实际 %d 次",
				file, call, strings.Count(string(raw), call))
		}
	}
}
