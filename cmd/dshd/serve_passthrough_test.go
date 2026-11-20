package main

// serve → 子进程 `dshd run` 的宿主旋钮透传。
//
// # 缺陷形状（改前实测）
//
// `dshd serve --max-chars 123` → `flag provided but not defined: -max-chars`；
// 六组旗标在 serve 侧**根本没有**，于是生产 serve 链上它们恒为子进程 run 的
// 写死默认值（自动压缩 40000 / 外置 4000 / 字节上限 65536 / 容量 0 / 0），
// 从 serve 侧怎么都调不了。
//
// 这组测试钉两件事：
//  1. serve 侧解析出来的值原样进 childArgs（不是断言常量、不是断言结构体）；
//  2. 生产的 modelOverride 路径（每轮新建 runner 副本）也没有漏掉它们——
//     副本是逐字段拷贝，漏一个就静默退回默认值。

import (
	"context"
	"flag"
	"path/filepath"
	"strconv"
	"testing"

	"dshgo/internal/compact"
	"dshgo/internal/session"
	"dshgo/modules/tools"
)

// parseServeHostRunFlags 用 serve 的同一套注册函数解析参数（不启动服务器）。
func parseServeHostRunFlags(t *testing.T, argv []string) *hostRunOptions {
	t.Helper()
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	opts := addHostRunFlags(fs)
	if err := fs.Parse(argv); err != nil {
		t.Fatalf("serve 侧应当认识这些宿主旗标：%v", err)
	}
	return opts
}

// serve --xxx 的值必须原样出现在子进程 argv 里。
func TestServeHostRunFlagsReachChildArgs(t *testing.T) {
	opts := parseServeHostRunFlags(t, []string{
		"--max-chars", "123",
		"--workspace-bytes", "456",
		"--auto-compact=false",
		"--context-window", "789",
		"--max-output", "321",
		"--agents", "docs/WORKER.md",
	})
	if opts.maxChars != 123 || opts.workspaceBytes != 456 {
		t.Fatalf("serve 解析出的 maxChars/workspaceBytes = %d/%d，期望 123/456", opts.maxChars, opts.workspaceBytes)
	}
	if opts.autoCompact != 0 {
		t.Fatalf("--auto-compact=false 应当解析成关闭（0），实际 %d", opts.autoCompact)
	}
	if opts.contextWindow != 789 || opts.maxOutput != 321 {
		t.Fatalf("serve 解析出的 contextWindow/maxOutput = %d/%d，期望 789/321", opts.contextWindow, opts.maxOutput)
	}
	if opts.agents != "docs/WORKER.md" {
		t.Fatalf("serve 解析出的 agents = %q", opts.agents)
	}

	runner := &loopRunner{home: "h", policy: "allow", maxSteps: defaultTurnMaxSteps, model: "m", runOpts: opts}
	args := runner.childArgs("sid", "hello")
	for _, pair := range [][2]string{
		{"--max-chars", "123"},
		{"--workspace-bytes", "456"},
		{"--auto-compact", "0"},
		{"--context-window", "789"},
		{"--max-output", "321"},
		{"--agents", "docs/WORKER.md"},
	} {
		if !containsArgPair(args, pair[0], pair[1]) {
			t.Fatalf("子进程 argv 缺 %s %s：%v", pair[0], pair[1], args)
		}
	}
}

// 不写 --agents 时不传这个参数：空串在 run 侧是"用工作区自己的 AGENTS.md"，
// 显式传空会是另一种语义。
func TestServeHostRunFlagsOmitEmptyAgents(t *testing.T) {
	// 出厂 token 预算是"官方 12500 或环境变量"（env 只当默认值）：不隔离的话，
	// 外部恰好设了 DSHD_MAX_INLINE_TOKENS 就会让下面那条断言红在一个无关变量上。
	t.Setenv("DSHD_MAX_INLINE_TOKENS", "")
	opts := parseServeHostRunFlags(t, nil)
	runner := &loopRunner{home: "h", policy: "allow", maxSteps: defaultTurnMaxSteps, model: "m", runOpts: opts}
	args := runner.childArgs("sid", "hello")
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--agents" {
			t.Fatalf("没有 --agents 时不该出现在 argv：%v", args)
		}
	}
	// 其余五个旋钮仍然要带（它们的默认值就是 run 的默认值，传与不传等价，
	// 但显式传保证"父进程的值是唯一真值"）。
	//
	// --auto-compact 的默认值用 `auto` 传而不是 `40000`：这一位决定子进程把阈值
	// 当成"出厂默认（跟官方口径）"还是"用户硬上限"。传数字会让界面起的会话退回
	// "4 万 token 就压缩"（见 compact_threshold_official_test.go）。
	if !containsArgPair(args, "--auto-compact", autoCompactAuto) {
		t.Fatalf("默认自动压缩阈值没有透传成 auto：%v", args)
	}
	// --max-chars 的出厂值是 **0**（字符臂不参与）：官方 spill-policy 没有这个旋钮，
	// 我们自造的 4000 已删（它会抢在官方 12500 token 预算之前截断）。仍然要透传：
	// 0 是"字符臂关闭"这一事实，不能被"不传"顶掉（不传 = 子进程自己的默认，同样 0，
	// 但那就不是"父进程的值是唯一真值"了）。
	if !containsArgPair(args, "--max-chars", strconv.Itoa(0)) {
		t.Fatalf("出厂字符阈值没有透传：%v", args)
	}
	// 出厂 token 预算必须是**官方的 12500**，不是 0（0 = 关闭保留策略）。
	if !containsArgPair(args, "--max-inline-tokens", strconv.Itoa(tools.DefaultMaxInlineTokens)) {
		t.Fatalf("出厂 token 预算没有按官方 12500 透传：%v", args)
	}
}

// 生产路径：modelOverrideLoopRunner 每轮新建 runner 副本，这些值也必须到达真正被 spawn 的子进程。
func TestServeForwardsHostRunOptionsToTurnChild(t *testing.T) {
	home := t.TempDir()
	capture := filepath.Join(t.TempDir(), "argv.json")
	t.Setenv("DSHD_MODEL_WIRE_ARGV", capture)
	t.Setenv("DSHD_SERVE_NO_FOLLOW", "1")
	sid := "session-host-run-options"
	lg, err := session.CreateSession(home, t.TempDir(), sid, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := lg.Close(); err != nil {
		t.Fatal(err)
	}

	opts := parseServeHostRunFlags(t, []string{
		"--max-chars", "123",
		"--workspace-bytes", "456",
		"--auto-compact", "0",
		"--context-window", "789",
		"--max-output", "321",
	})
	base := &loopRunner{home: home, policy: "deny", maxSteps: defaultTurnMaxSteps, model: "m", runOpts: opts}
	runner := &modelOverrideLoopRunner{base: base}
	if err := runner.Run(context.Background(), sid, "turn-"+sid, "prompt"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	_, args := readLoopModelArg(t, capture)
	for _, pair := range [][2]string{
		{"--max-chars", "123"},
		{"--workspace-bytes", "456"},
		{"--auto-compact", "0"},
		{"--context-window", "789"},
		{"--max-output", "321"},
	} {
		if !containsArgPair(args, pair[0], pair[1]) {
			t.Fatalf("modelOverride 路径没有把 %s %s 传给子进程：%v", pair[0], pair[1], args)
		}
	}
}

// 四种写法落到同一个三态上：false/0 → 关闭、true/auto/不写 → 不覆盖（官方口径）、
// 整数 → 原样覆盖。出厂**没有**数值默认值可回退。
func TestAutoCompactFlagAcceptsBooleanAndInteger(t *testing.T) {
	cases := []struct {
		argv []string
		want int
	}{
		{argv: []string{"--auto-compact=false"}, want: compact.AutoCompactOff},
		{argv: []string{"--auto-compact=true"}, want: compact.AutoCompactUnset},
		{argv: []string{"--auto-compact=" + autoCompactAuto}, want: compact.AutoCompactUnset},
		{argv: []string{"--auto-compact", "0"}, want: compact.AutoCompactOff},
		{argv: []string{"--auto-compact", "8000"}, want: 8000},
		{argv: nil, want: compact.AutoCompactUnset},
	}
	for _, tc := range cases {
		opts := parseServeHostRunFlags(t, tc.argv)
		if opts.autoCompact != tc.want {
			t.Fatalf("argv=%v autoCompact=%d，期望 %d", tc.argv, opts.autoCompact, tc.want)
		}
	}
	// 负数既不是"关闭"也不是"不写"，是拼错了——必须报错，不能静默变成某一个态。
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	_ = addHostRunFlags(fs)
	fs.SetOutput(nopWriter{})
	if err := fs.Parse([]string{"--auto-compact=-1"}); err == nil {
		t.Fatal("--auto-compact=-1 必须报错（关闭要写 0 或 false）")
	}
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }
