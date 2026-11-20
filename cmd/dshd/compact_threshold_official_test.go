package main

// 自动压缩的**生效阈值**。
//
// # 改前的缺陷（用户报的"默认配置根本用不了"）
//
// 宿主有一个自己编的出厂默认值 `defaultAutoCompact = 40000`，它被拿去和官方口径
// **取小**，于是 1M 窗口的模型在 4 万 token 就自动压缩（官方 800000）——早了 20 倍，
// 聊不了几句上下文就被压走。
//
// # 修法：删掉这个假默认值，不是"改大"它
//
// 官方阈值只有一个来源：`floor(min(W × thresholdRatio, W − O − B))`
// （`dsh-compaction-basic/lib/index.js:132`）。所以：
//
//   - 不写 `--auto-compact` → 阈值就是这个公式的结果，**不经过任何宿主截断**；
//   - `--auto-compact=N`    → 显式覆盖（官方没有这个字段，是我们额外的旋钮）；
//   - `--auto-compact=0/false` → 关闭；
//   - 窗口未知/预算不成立   → 官方抛 `TargetPressureConfigError`（`:126-131`，
//     窗口未知那条在 `:958`「no context capacity ... configure contextWindow」），
//     宿主照 do：报配置错误，**不给阈值**（不退回 40000，也不静默跳过）。
//
// 四格判据见 TestCompactThresholdOfficialFormula。
//
// 官方原件：dsh-compaction-basic/lib/index.js:124-134（公式与校验）、`:844-851`
// （pre-step 的 catch：同一个目标告警一次后跳过压缩、继续这一轮）。

import (
	"context"
	"flag"
	"testing"

	"dshgo/codecs/v4"
	"dshgo/internal/compact"
	"dshgo/internal/llm"
	"dshgo/internal/session"
)

// parseAutoCompact 用**生产的**注册函数解析 `--auto-compact`，返回三态取值。
//
// 不直接给结构体字段赋值：三个入口（run/acp/serve）注册的是同一个
// addAutoCompactFlag，测它就等于测三个入口的解析口径。
func parseAutoCompact(t *testing.T, argv []string) int {
	t.Helper()
	fs := flag.NewFlagSet("auto-compact-test", flag.ContinueOnError)
	tokens := compact.AutoCompactUnset
	addAutoCompactFlag(fs, &tokens)
	if err := fs.Parse(argv); err != nil {
		t.Fatalf("解析 %v 失败：%v", argv, err)
	}
	return tokens
}

// TestCompactThresholdOfficialFormula 是这次修复的主判据（四格）。
func TestCompactThresholdOfficialFormula(t *testing.T) {
	// 官方公式在这两个窗口下的期望值：
	//   1M 窗口、128K 预留、64K headroom → floor(min(800000, 806464)) = 800000
	//   100K 窗口、8K 预留、64K headroom → floor(min(80000, 26272))   = 26272
	cases := []struct {
		name   string
		argv   []string
		model  string
		window int
		maxOut int
		want   int
		on     bool
		werr   bool
	}{
		{
			name: "出厂默认+1M窗口", model: "group/m", window: 1_000_000, maxOut: 128_000,
			want: 800_000, on: true,
		},
		{
			// 空模型 id 才真的是"窗口未知"：非空自定义模型会被补 1M 默认窗口。
			name: "出厂默认+窗口未知", model: "", window: 0, maxOut: 0,
			want: 0, on: true, werr: true,
		},
		{
			name: "显式覆盖+1M窗口", argv: []string{"--auto-compact", "50000"},
			model: "group/m", window: 1_000_000, maxOut: 128_000,
			want: 50_000, on: true,
		},
		{
			// 覆盖不依赖窗口：用户写的数就是阈值。
			name: "显式覆盖+窗口未知", argv: []string{"--auto-compact", "50000"},
			model: "", window: 0, maxOut: 0,
			want: 50_000, on: true,
		},
		{
			name: "true等于不覆盖", argv: []string{"--auto-compact=true"},
			model: "group/m", window: 1_000_000, maxOut: 128_000,
			want: 800_000, on: true,
		},
		{
			name: "auto等于不覆盖", argv: []string{"--auto-compact=auto"},
			model: "group/m", window: 1_000_000, maxOut: 128_000,
			want: 800_000, on: true,
		},
		{
			name: "false关闭", argv: []string{"--auto-compact=false"},
			model: "group/m", window: 1_000_000, maxOut: 128_000,
			want: 0, on: false, werr: true,
		},
		{
			name: "0关闭", argv: []string{"--auto-compact=0"},
			model: "group/m", window: 1_000_000, maxOut: 128_000,
			want: 0, on: false, werr: true,
		},
		{
			// 小窗口：官方公式本身由 W−O 决定，跟覆盖与否无关。
			name: "出厂默认+100K窗口", model: "group/m", window: 100_000, maxOut: 8_192,
			want: 26_272, on: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newCompactTestRunner(t, tc.model, tc.window, tc.maxOut)
			r.autoCompact = parseAutoCompact(t, tc.argv)

			if got := r.autoCompactEnabled(); got != tc.on {
				t.Fatalf("argv=%v 开关=%v，期望 %v", tc.argv, got, tc.on)
			}
			got, err := r.compactThreshold()
			if tc.werr {
				if err == nil {
					t.Fatalf("argv=%v 应报配置错误/无阈值，却给出 %d", tc.argv, got)
				}
				if got != 0 {
					t.Fatalf("argv=%v 报错时不该给阈值，实际 %d", tc.argv, got)
				}
				t.Logf("开关=%v 阈值=报错（%v）", tc.on, err)
				return
			}
			if err != nil {
				t.Fatalf("argv=%v 不该报错：%v", tc.argv, err)
			}
			if got != tc.want {
				t.Fatalf("argv=%v 窗口=%d 阈值=%d，期望 %d", tc.argv, r.ctxWindow, got, tc.want)
			}
			// 贴出实际值：这一列就是要写进交付说明的证据。
			t.Logf("阈值=%d（开关=%v，窗口=%d，预留=%d，headroom=%d）",
				got, tc.on, r.ctxWindow, r.capacity.MaxOutput, r.compactPolicy().HeadroomTokens)
		})
	}
}

// TestAutoCompactOffKeepsRunCompactorDisabled 钉另一条链（`dshd run` 的每步钩子）：
// 0 必须让压缩器**不存在**，而不是"用某个阈值继续压"。
func TestAutoCompactOffKeepsRunCompactorDisabled(t *testing.T) {
	for _, argv := range [][]string{{"--auto-compact=false"}, {"--auto-compact", "0"}} {
		override := parseAutoCompact(t, argv)
		if compact.AutoCompactEnabled(override) {
			t.Fatalf("argv=%v 解析成 %d，仍被当成开启", argv, override)
		}
		if _, err := compact.HostThreshold(override, compact.DefaultPolicy().Budget(1_000_000, 128_000)); err == nil {
			t.Fatalf("argv=%v 关闭时不该算出阈值", argv)
		}
		if runCompactor(nil, nil, override, compact.DefaultPolicy(), llm.ModelCapacity{}, "group/m", nil) != nil {
			t.Fatalf("argv=%v 关闭时必须返回 nil 压缩器（不许改成用默认值）", argv)
		}
	}
}

// childAutoCompact 按**子进程的同一个注册函数**再解析 serve 生成的 argv 里那一段，
// 断言透传链路的两个端点用同一条规则、只差 argv。
func childAutoCompact(t *testing.T, args []string) int {
	t.Helper()
	value := ""
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--auto-compact" {
			value = args[i+1]
		}
	}
	if value == "" {
		t.Fatalf("子进程 argv 里没有 --auto-compact：%v", args)
	}
	return parseAutoCompact(t, []string{"--auto-compact", value})
}

// TestServeAutoCompactPassthroughKeepsTriState 钉 serve → `dshd run` 的透传：
// 三态必须原样到达子进程。只用数字透传的话，"不覆盖"到了子进程会被当成
// "用户要这个数"——界面起的会话阈值就与命令行不一致了。
func TestServeAutoCompactPassthroughKeepsTriState(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want int
	}{
		{"不写", nil, compact.AutoCompactUnset},
		{"true", []string{"--auto-compact=true"}, compact.AutoCompactUnset},
		{"auto", []string{"--auto-compact=auto"}, compact.AutoCompactUnset},
		{"覆盖", []string{"--auto-compact", "50000"}, 50_000},
		{"其它覆盖", []string{"--auto-compact", "8000"}, 8_000},
		{"关闭", []string{"--auto-compact=false"}, compact.AutoCompactOff},
		{"零关闭", []string{"--auto-compact", "0"}, compact.AutoCompactOff},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := parseServeHostRunFlags(t, tc.argv)
			if opts.autoCompact != tc.want {
				t.Fatalf("serve 侧解析出 %d，期望 %d", opts.autoCompact, tc.want)
			}
			if got := childAutoCompact(t, opts.args()); got != tc.want {
				t.Fatalf("子进程拿到 %d，父进程是 %d", got, tc.want)
			}
		})
	}
}

// TestRunCompactorUsesResolvedWindow 钉 `dshd run` 这条链的窗口解析。
//
// 命令行/界面起的子进程通常**不写** `--context-window`（serve 透传的就是 0）。
// 如果 runCompactor 直接拿这个 0 去算，阈值会永远算不出来（官方口径要求正窗口），
// 而同一进程里的请求早就按模型容量（1M）发了。
//
// 判据用一份估算被钉在 10 万 token 的历史：它必须**超过**旧的 4 万、**不到**
// 官方口径的 80 万，于是"按哪个阈值判"一眼可分。
func TestRunCompactorUsesResolvedWindow(t *testing.T) {
	l := buildLongSession(t)
	defer l.Close()

	back, err := v4.ReadFile(l.Path)
	if err != nil {
		t.Fatal(err)
	}
	raw := (&session.Meter{}).Estimate(session.Surface(back))
	if raw <= 0 {
		t.Fatalf("夹具没造出可估算的历史（raw=%d）", raw)
	}
	// 服务端锚点把估算放大到 100000：40000 < 100000 < 800000。
	meter := &session.Meter{}
	meter.Observe(100_000, raw)

	chat := &countingChat{}
	const model = "group/deepseek-v4.1-flash-1017" // 内置表 1000000/128000
	def := runCompactor(chat, l, compact.AutoCompactUnset, compact.DefaultPolicy(),
		llm.ModelCapacity{}, model, meter)
	if def == nil {
		t.Fatal("默认开启时必须有压缩器")
	}
	if hist, err := def(context.Background(), 1, nil); err != nil {
		t.Fatal(err)
	} else if hist != nil {
		t.Fatal("10 万 token 在官方口径（80 万）之下，不该压缩")
	}
	if chat.called != 0 {
		t.Fatalf("默认阈值下 10 万 token 就压了 %d 次：窗口没解析，仍在按 4 万判", chat.called)
	}

	// 用户显式覆盖成 100：必须压。同一个窗口、同一份历史，只换覆盖值。
	override := runCompactor(chat, l, 100, compact.DefaultPolicy(), llm.ModelCapacity{}, model, meter)
	if override == nil {
		t.Fatal("覆盖值 > 0 时必须有压缩器")
	}
	hist, err := override(context.Background(), 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if chat.called == 0 || len(hist) == 0 {
		t.Fatalf("显式 --auto-compact=100 没生效（摘要调用 %d 次，历史 %d 条）", chat.called, len(hist))
	}
}
