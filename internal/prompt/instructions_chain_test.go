package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// 这些测试钉的是官方 `@deepseek-ai/dsh-agent-instructions` 的 project 指令链契约，
// 不是"函数存在"：每条都造真实目录树、走真实装配（`Default` 或它实际调用的
// `ProjectInstructions.Build`），再对着官方 README/src 的语义断言。

func writeInstructionFile(t *testing.T, filePath, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// markProjectRoot 造官方默认项目根标记 `.git`（src/config.ts:11）。
func markProjectRoot(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func requireInstructionOrder(t *testing.T, text string, wants ...string) {
	t.Helper()
	prev := -1
	for _, want := range wants {
		idx := strings.Index(text, want)
		if idx < 0 {
			t.Fatalf("链里缺 %q：\n%s", want, text)
		}
		if idx <= prev {
			t.Fatalf("%q 出现顺序不是宽→窄：\n%s", want, text)
		}
		prev = idx
	}
}

// TestInstructionChainBroadToSpecificFromProjectRoot 钉 README.md:34：
// "every existing candidate file from the project root down to the session working
// directory, in broad-to-specific order"，且整段是官方 `<system-reminder>` 形状。
func TestInstructionChainBroadToSpecificFromProjectRoot(t *testing.T) {
	root := t.TempDir()
	markProjectRoot(t, root)
	writeInstructionFile(t, filepath.Join(root, "AGENTS.md"), "root-rules")
	writeInstructionFile(t, filepath.Join(root, "pkg", "AGENTS.md"), "pkg-rules")
	writeInstructionFile(t, filepath.Join(root, "pkg", "sub", "CLAUDE.md"), "sub-rules")

	got := Default(Context{Cwd: filepath.Join(root, "pkg", "sub"), EnableInstructionChain: true})

	requireInstructionOrder(t, got,
		"Instructions from: AGENTS.md",
		"Instructions from: pkg/AGENTS.md",
		"Instructions from: pkg/sub/CLAUDE.md",
	)
	for _, want := range []string{"root-rules", "pkg-rules", "sub-rules"} {
		if !strings.Contains(got, want) {
			t.Fatalf("三个目录的文件都要在链里，缺 %q：\n%s", want, got)
		}
	}
	if n := strings.Count(got, systemReminderOpen); n != 1 {
		t.Fatalf("一条链只该有一个 `<system-reminder>` 框架，实际 %d 个：\n%s", n, got)
	}
	if n := strings.Count(got, systemReminderClose); n != 1 {
		t.Fatalf("框架闭合标签应恰好 1 个，实际 %d 个：\n%s", n, got)
	}
	// 官方 README.md:76-77：全放得下时不出现预算通知。
	if strings.Contains(got, "Workspace instruction budget") {
		t.Fatalf("没超预算时不该出现预算通知：\n%s", got)
	}
	// 链接管项目段：旧的单点 `工作区约定` 段让位，同一份 AGENTS.md 不重复注入。
	if strings.Contains(got, "工作区约定") {
		t.Fatalf("启用链后旧单点项目段应让位（否则同一文件进提示词两次）：\n%s", got)
	}
}

// TestInstructionChainReloadsOnCwdChange 钉 README.md:28：
// "Changing the Session's working directory reloads the applicable project instruction
// chain before the next request."——同一个 Context 换 Cwd，链跟着变。
func TestInstructionChainReloadsOnCwdChange(t *testing.T) {
	root := t.TempDir()
	markProjectRoot(t, root)
	writeInstructionFile(t, filepath.Join(root, "AGENTS.md"), "root-rules")
	writeInstructionFile(t, filepath.Join(root, "pkg", "AGENTS.md"), "pkg-rules")
	writeInstructionFile(t, filepath.Join(root, "pkg", "sub", "CLAUDE.md"), "sub-rules")

	ctx := Context{Cwd: filepath.Join(root, "pkg", "sub"), EnableInstructionChain: true}
	atSub := Default(ctx)

	ctx.Cwd = filepath.Join(root, "pkg")
	atPkg := Default(ctx)

	if atSub == atPkg {
		t.Fatal("换 cwd 后链没有重载（两次装配字节相同）")
	}
	if !strings.Contains(atSub, "sub-rules") {
		t.Fatalf("cwd=sub 时该带着 sub 目录的文件：\n%s", atSub)
	}
	if strings.Contains(atPkg, "sub-rules") {
		t.Fatalf("cwd=pkg 时链里不该再有 sub 目录的文件：\n%s", atPkg)
	}
	requireInstructionOrder(t, atPkg, "Instructions from: AGENTS.md", "Instructions from: pkg/AGENTS.md")
}

// TestInstructionChainDedupsWithinCandidateGroup 钉 README.md:34/224：
// "Within one candidate group, files whose content matches after trimming render once:
// a `CLAUDE.md` that duplicates its `AGENTS.md` is not repeated."
// local 叠加与 base 候选**同组**（src/config.ts:28-32），所以同样折叠。
func TestInstructionChainDedupsWithinCandidateGroup(t *testing.T) {
	root := t.TempDir()
	markProjectRoot(t, root)
	writeInstructionFile(t, filepath.Join(root, "AGENTS.md"), "shared-body\n")
	writeInstructionFile(t, filepath.Join(root, "CLAUDE.md"), "  shared-body\n\n")
	writeInstructionFile(t, filepath.Join(root, "AGENTS.local.md"), "shared-body")

	chain := ProjectInstructions{}.Build(Context{
		Cwd: root, EnableInstructionChain: true, MaxWorkspaceBytes: 4096,
	})
	if n := strings.Count(chain, "Instructions from:"); n != 1 {
		t.Fatalf("同目录同内容的候选只该渲染一次，实际 %d 段：\n%s", n, chain)
	}
	if n := strings.Count(chain, "shared-body"); n != 1 {
		t.Fatalf("同内容被重复渲染 %d 次：\n%s", n, chain)
	}
	// 官方保留"发现顺序里最早的那个"，所以留下的是 AGENTS.md。
	if !strings.Contains(chain, "Instructions from: AGENTS.md") {
		t.Fatalf("该留下同组最早的 AGENTS.md：\n%s", chain)
	}
	for _, dropped := range []string{"CLAUDE.md", "AGENTS.local.md"} {
		if strings.Contains(chain, "Instructions from: "+dropped) {
			t.Fatalf("%s 与 AGENTS.md 同内容，应被折叠：\n%s", dropped, chain)
		}
	}
}

// TestInstructionChainDedupIsPerGroupNotGlobal 钉 README.md:224：
// "Deduplication is content-based within one candidate group ... a distinct real copy ...
// loads in full alongside it"——不同目录的同内容文件各自渲染，不是全局去重。
func TestInstructionChainDedupIsPerGroupNotGlobal(t *testing.T) {
	root := t.TempDir()
	markProjectRoot(t, root)
	writeInstructionFile(t, filepath.Join(root, "AGENTS.md"), "identical-body")
	writeInstructionFile(t, filepath.Join(root, "pkg", "AGENTS.md"), "identical-body")

	chain := ProjectInstructions{}.Build(Context{
		Cwd: filepath.Join(root, "pkg"), EnableInstructionChain: true, MaxWorkspaceBytes: 4096,
	})
	if n := strings.Count(chain, "Instructions from:"); n != 2 {
		t.Fatalf("不同候选组即使内容相同也应各渲染一次，实际 %d 段：\n%s", n, chain)
	}
	requireInstructionOrder(t, chain,
		"Instructions from: AGENTS.md",
		"Instructions from: pkg/AGENTS.md",
	)
}

// TestInstructionChainLocalOverlayAfterBase 钉官方候选顺序（src/config.ts:46-47，
// 以及 README.md:38 "`AGENTS.local.md` and `CLAUDE.local.md` are additive local overlays"）：
// 同一目录先 base 后 local；内容不同就都渲染。
func TestInstructionChainLocalOverlayAfterBase(t *testing.T) {
	root := t.TempDir()
	markProjectRoot(t, root)
	writeInstructionFile(t, filepath.Join(root, "AGENTS.md"), "base-body")
	writeInstructionFile(t, filepath.Join(root, "AGENTS.local.md"), "local-body")
	writeInstructionFile(t, filepath.Join(root, "CLAUDE.local.md"), "claude-local-body")

	chain := ProjectInstructions{}.Build(Context{
		Cwd: root, EnableInstructionChain: true, MaxWorkspaceBytes: 4096,
	})
	requireInstructionOrder(t, chain,
		"Instructions from: AGENTS.md",
		"Instructions from: AGENTS.local.md",
		"Instructions from: CLAUDE.local.md",
	)
	for _, want := range []string{"base-body", "local-body", "claude-local-body"} {
		if !strings.Contains(chain, want) {
			t.Fatalf("缺 %q：\n%s", want, chain)
		}
	}
}

// TestInstructionChainBudgetDropsBroadFilesAndTruncatesMostSpecific 钉 README.md:74：
// "Within a chain it keeps the most specific files first: it drops whole broader files,
// truncates the last remaining file ..."，且渲染结果不超 maxBytes。
func TestInstructionChainBudgetDropsBroadFilesAndTruncatesMostSpecific(t *testing.T) {
	root := t.TempDir()
	markProjectRoot(t, root)
	writeInstructionFile(t, filepath.Join(root, "AGENTS.md"), strings.Repeat("A", 2000))
	writeInstructionFile(t, filepath.Join(root, "pkg", "AGENTS.md"), strings.Repeat("B", 2000))
	writeInstructionFile(t, filepath.Join(root, "pkg", "sub", "CLAUDE.md"), strings.Repeat("C", 2000))

	const budget = 900
	chain := ProjectInstructions{}.Build(Context{
		Cwd:                    filepath.Join(root, "pkg", "sub"),
		EnableInstructionChain: true,
		MaxWorkspaceBytes:      budget,
	})
	if len(chain) > budget {
		t.Fatalf("链渲染 %d 字节，超过 maxBytes=%d：\n%s", len(chain), budget, chain)
	}
	if !strings.Contains(chain, "Workspace instruction budget 900 bytes:") {
		t.Fatalf("超预算必须出现官方预算通知：\n%s", chain)
	}
	// 宽的两个整文件被丢（不是截断）。
	if !strings.Contains(chain, "omitted AGENTS.md, pkg/AGENTS.md") {
		t.Fatalf("宽到窄的省略顺序不对：\n%s", chain)
	}
	if strings.Contains(chain, strings.Repeat("A", 100)) || strings.Contains(chain, strings.Repeat("B", 100)) {
		t.Fatalf("宽的整文件被丢后不该还留着正文：\n%s", chain)
	}
	// 最具体的那个文件被截断，且确实留了正文。
	if !strings.Contains(chain, "truncated pkg/sub/CLAUDE.md from 2000 to ") {
		t.Fatalf("最具体文件应被截断并记账：\n%s", chain)
	}
	if !strings.Contains(chain, strings.Repeat("C", 32)) {
		t.Fatalf("最具体文件截断后仍应有正文：\n%s", chain)
	}
	if strings.Contains(chain, strings.Repeat("C", 2000)) {
		t.Fatalf("最具体文件没有真的被截断：\n%s", chain)
	}
	if !strings.Contains(chain, "Instructions from: pkg/sub/CLAUDE.md") {
		t.Fatalf("被截断的文件仍要有标题：\n%s", chain)
	}
}

// TestInstructionChainIgnoresUnreadableAndOversizedCandidates 钉官方
// "候选缺席/读失败只跳过它自己"与 maxSourceBytes：
//
//	README.md:225（组失败命运属于 refresh 层，基线装配层没有 last-good 可保）
//	config.ts:31-32 "Maximum UTF-8 bytes read from one instruction file; larger files are ignored"
//
// 要求：不 panic、不吞掉其它文件、超 maxSourceBytes 的文件整文件丢弃。
func TestInstructionChainIgnoresUnreadableAndOversizedCandidates(t *testing.T) {
	root := t.TempDir()
	markProjectRoot(t, root)
	// 候选名被目录占住：os.Stat 说它不是普通文件 → 按缺席跳过，不 panic。
	if err := os.Mkdir(filepath.Join(root, "AGENTS.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeInstructionFile(t, filepath.Join(root, "CLAUDE.md"), "root-claude-survives")
	// 超过 MaxSourceBytes（64）的文件整文件丢弃，不截断。
	writeInstructionFile(t, filepath.Join(root, "pkg", "AGENTS.md"), strings.Repeat("X", 200))
	writeInstructionFile(t, filepath.Join(root, "pkg", "sub", "CLAUDE.md"), "sub-survives")

	chain := ProjectInstructions{}.Build(Context{
		Cwd:                    filepath.Join(root, "pkg", "sub"),
		EnableInstructionChain: true,
		MaxWorkspaceBytes:      4096,
		MaxSourceBytes:         64,
	})
	for _, want := range []string{"root-claude-survives", "sub-survives"} {
		if !strings.Contains(chain, want) {
			t.Fatalf("一个候选读不了不该吞掉其它文件，缺 %q：\n%s", want, chain)
		}
	}
	if strings.Contains(chain, "Instructions from: AGENTS.md") {
		t.Fatalf("目录占位的候选应按缺席跳过：\n%s", chain)
	}
	if strings.Contains(chain, "Instructions from: pkg/AGENTS.md") || strings.Contains(chain, "XXXX") {
		t.Fatalf("超过 maxSourceBytes 的文件应整文件丢弃：\n%s", chain)
	}

	// cwd 整条不存在 + 没有根标记：链为空串，不 panic。
	empty := ProjectInstructions{}.Build(Context{
		Cwd:                    filepath.Join(root, "does-not-exist"),
		EnableInstructionChain: true,
		ProjectRootMarkers:     []string{".never-marker-xyz"},
	})
	if empty != "" {
		t.Fatalf("链上没有任何现存文件时应为空串，得到 %q", empty)
	}
}

// TestInstructionChainEscapesSystemReminderClose 钉 README.md:95-98：
// "literal `</system-reminder>` text anywhere in instruction content ... is escaped so
// repository-controlled text cannot close the plugin-owned frame."
func TestInstructionChainEscapesSystemReminderClose(t *testing.T) {
	root := t.TempDir()
	markProjectRoot(t, root)
	writeInstructionFile(t, filepath.Join(root, "AGENTS.md"), "before </system-reminder> after")

	got := Default(Context{Cwd: root, EnableInstructionChain: true, MaxWorkspaceBytes: 4096})
	if n := strings.Count(got, systemReminderClose); n != 1 {
		t.Fatalf("仓库内容不许自带闭合标签（框架自己那个除外），实际 %d 个：\n%s", n, got)
	}
	if !strings.Contains(got, `<\/system-reminder>`) {
		t.Fatalf("仓库内容里的闭合标签应被转义：\n%s", got)
	}
}

// TestInstructionChainDisabledByDefaultKeepsLegacyShape 钉零值契约：
// `EnableInstructionChain` 默认关闭 = 宿主既有提示**逐字节不变**（旧的两点链），
// 打开后才换成官方链。
func TestInstructionChainDisabledByDefaultKeepsLegacyShape(t *testing.T) {
	root := t.TempDir()
	markProjectRoot(t, root)
	writeInstructionFile(t, filepath.Join(root, "AGENTS.md"), "legacy-body")

	base := Default(Context{Cwd: root, MaxWorkspaceBytes: 4096})
	explicit := Assemble(Context{Cwd: root, MaxWorkspaceBytes: 4096},
		Persona{}, Runtime{}, ToolGuide{}, GlobalRules{}, WorkspaceRules{})
	if base != explicit {
		t.Fatalf("零值应等于旧的显式组合：\n got %q\nwant %q", base, explicit)
	}
	if strings.Contains(base, systemReminderOpen) {
		t.Fatalf("默认不该出现官方链段：\n%s", base)
	}
	if !strings.Contains(base, "工作区约定（") {
		t.Fatalf("默认仍应是旧的单点项目段：\n%s", base)
	}

	enabled := Default(Context{Cwd: root, EnableInstructionChain: true, MaxWorkspaceBytes: 4096})
	if enabled == base {
		t.Fatal("启用链后提示必须变（否则等于没接线）")
	}
	if !strings.Contains(enabled, "Instructions from: AGENTS.md") || !strings.Contains(enabled, "legacy-body") {
		t.Fatalf("启用链后应有官方段：\n%s", enabled)
	}
}

// TestInstructionChainNoRootMarkerUsesCwd 钉 README.md:42：
// "Root discovery climbs only when a marker probe confirms that the marker is absent"，
// 以及官方 findProjectRoot 的"无标记 → 用 cwd 自己"（src/files.ts:232-252）。
func TestInstructionChainNoRootMarkerUsesCwd(t *testing.T) {
	parent := t.TempDir()
	writeInstructionFile(t, filepath.Join(parent, "AGENTS.md"), "parent-rules-should-not-load")
	child := filepath.Join(parent, "child")
	writeInstructionFile(t, filepath.Join(child, "AGENTS.md"), "child-rules")

	chain := ProjectInstructions{}.Build(Context{
		Cwd:                    child,
		EnableInstructionChain: true,
		// 一个永远找不到的标记：确认"找不到就用 cwd 自己"，不往上扩散链。
		ProjectRootMarkers: []string{".never-marker-xyz"},
	})
	if !strings.Contains(chain, "child-rules") {
		t.Fatalf("cwd 自己的文件要在链里：\n%s", chain)
	}
	if strings.Contains(chain, "parent-rules-should-not-load") {
		t.Fatalf("没有根标记时不该把父目录拉进链：\n%s", chain)
	}
}

// TestInstructionChainYieldsToExtraRulesPath 钉宿主既有语义：
// `ExtraRulesPath`（`--agents`）是显式指定的单本手册，官方没有这个字段；
// 给出它时自动发现链让位，不能"两本手册同时进提示词"。
func TestInstructionChainYieldsToExtraRulesPath(t *testing.T) {
	root := t.TempDir()
	markProjectRoot(t, root)
	writeInstructionFile(t, filepath.Join(root, "AGENTS.md"), "auto-rules")
	writeInstructionFile(t, filepath.Join(root, "WORKER.md"), "worker-rules")

	got := Default(Context{
		Cwd:                    root,
		EnableInstructionChain: true,
		ExtraRulesPath:         "WORKER.md",
		MaxWorkspaceBytes:      4096,
	})
	if !strings.Contains(got, "worker-rules") {
		t.Fatalf("显式手册必须注入：\n%s", got)
	}
	if strings.Contains(got, "auto-rules") || strings.Contains(got, systemReminderOpen) {
		t.Fatalf("显式手册优先时不该再自动注入工作区链：\n%s", got)
	}
}

// TestInstructionChainNeverExceedsBudgetAndStaysDeterministic 把"链不超 maxBytes"
// 做成可执行性质：预算从 1 字节扫到 1200 字节，每一档都必须
//   - 渲染字节数 ≤ 预算（官方 README.md:74 的预算硬约束，三级退化都不许越界）；
//   - UTF-8 合法（官方 truncateUtf8 src/render.ts:84-94 不许切碎码元）；
//   - 两次装配字节相同（装配规矩 §1 确定性）。
func TestInstructionChainNeverExceedsBudgetAndStaysDeterministic(t *testing.T) {
	root := t.TempDir()
	markProjectRoot(t, root)
	writeInstructionFile(t, filepath.Join(root, "AGENTS.md"), strings.Repeat("宽-宽-宽\n", 80))
	writeInstructionFile(t, filepath.Join(root, "pkg", "AGENTS.md"), strings.Repeat("中-中\n", 80))
	writeInstructionFile(t, filepath.Join(root, "pkg", "sub", "CLAUDE.md"), strings.Repeat("最具体-最具体\n", 80))
	cwd := filepath.Join(root, "pkg", "sub")

	for budget := 1; budget <= 1200; budget += 7 {
		ctx := Context{Cwd: cwd, EnableInstructionChain: true, MaxWorkspaceBytes: budget}
		got := ProjectInstructions{}.Build(ctx)
		if len(got) > budget {
			t.Fatalf("预算 %d 下渲染了 %d 字节：\n%s", budget, len(got), got)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("预算 %d 下截断切碎了 UTF-8 码元：%q", budget, got)
		}
		if again := (ProjectInstructions{}).Build(ctx); again != got {
			t.Fatalf("预算 %d 下两次装配字节不同（不确定性）", budget)
		}
	}
}

// TestInstructionChainNegativeBudgetsDisable 钉宿主的哨兵约定：
// 负预算 / 负源上限 / 未启用 = 这条链不注入（与 MaxWorkspaceBytes 既有语义一致）。
func TestInstructionChainNegativeBudgetsDisable(t *testing.T) {
	root := t.TempDir()
	markProjectRoot(t, root)
	writeInstructionFile(t, filepath.Join(root, "AGENTS.md"), "body")

	for _, tc := range []struct {
		name string
		ctx  Context
	}{
		{name: "not_enabled", ctx: Context{Cwd: root}},
		{name: "negative_chain_budget", ctx: Context{Cwd: root, EnableInstructionChain: true, MaxWorkspaceBytes: -1}},
		{name: "negative_source_cap", ctx: Context{Cwd: root, EnableInstructionChain: true, MaxSourceBytes: -1}},
		{name: "empty_cwd", ctx: Context{EnableInstructionChain: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := (ProjectInstructions{}).Build(tc.ctx); got != "" {
				t.Fatalf("%s 时应为空串，得到 %q", tc.name, got)
			}
		})
	}
}

// TestInstructionChainStopsAtNearestRootMarker 钉 README.md:42：
// "Root discovery climbs only when a marker probe confirms that the marker is absent"——
// 最近的含标记目录就是根，不再往上把外层项目一起拉进链。
func TestInstructionChainStopsAtNearestRootMarker(t *testing.T) {
	outer := t.TempDir()
	markProjectRoot(t, outer)
	writeInstructionFile(t, filepath.Join(outer, "AGENTS.md"), "outer-rules")
	inner := filepath.Join(outer, "inner")
	markProjectRoot(t, inner)
	writeInstructionFile(t, filepath.Join(inner, "AGENTS.md"), "inner-rules")
	deep := filepath.Join(inner, "deep")
	writeInstructionFile(t, filepath.Join(deep, "AGENTS.md"), "deep-rules")

	chain := ProjectInstructions{}.Build(Context{Cwd: deep, EnableInstructionChain: true})
	requireInstructionOrder(t, chain,
		"Instructions from: AGENTS.md",
		"Instructions from: deep/AGENTS.md",
	)
	for _, want := range []string{"inner-rules", "deep-rules"} {
		if !strings.Contains(chain, want) {
			t.Fatalf("最近的根（inner）到 cwd 之间都要在链里，缺 %q：\n%s", want, chain)
		}
	}
	if strings.Contains(chain, "outer-rules") {
		t.Fatalf("找到最近的根就该停，不该把更外层项目拉进来：\n%s", chain)
	}
}
