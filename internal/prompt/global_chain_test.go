package prompt

// 这一组钉住 base 插件 #53 `agent-instructions` 的**用户全局双根链**：
// harness-home（`$DSH_HOME/AGENTS.md`）之后是 shared agents root
// （`$DSH_AGENTS_HOME/AGENTS.md`，未设环境变量时 `~/.agents/AGENTS.md`）。
//
// 官方逐条（`@deepseek-ai/dsh-agent-instructions/README.md`，rc.2 解包树）：
//
//	:34  "The first request includes one durable baseline message with the
//	      user-global chain — `$DSH_HOME/AGENTS.md`, then `<agentsHome>/AGENTS.md`"
//	:40  "The shared agents root resolves the same way, from `$DSH_AGENTS_HOME` or
//	      `~/.agents`."
//	:74  "User-global files are therefore omitted or truncated only when they exceed
//	      their own budget, with the harness-home file omitted before the shared-root file"
//	:224 "The two user-global roots share one group, so the shared-root file collapses
//	      behind a matching harness-home file."
//
// 形状说明（本仓的刻意差异，已登记）：官方把这条链渲染进它自己的
// `<system-reminder>` 帧；本仓的两根渲染在**既有** `全局约定（path）：正文` 块形状里
// ——因为那条帧会让生产提示词字节整体翻掉，而既有探针
// （`cmd/dshd/prompt_budget_probe_test.go`、本包 `budget_test.go`）按字节钉着现形状。
// 这里断言的是那三件**语义**（第二根、同组去重、harness-home 先丢），不是帧形状。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeGlobalRoot(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// userGlobalContext 造一个"两根都在"的装配上下文（不经过 Assemble，预算显式）。
func userGlobalContext(t *testing.T, harnessText, sharedText string, limit int) Context {
	t.Helper()
	root := t.TempDir()
	harness := filepath.Join(root, "home", "AGENTS.md")
	shared := filepath.Join(root, "agents", "AGENTS.md")
	if harnessText != "" {
		writeGlobalRoot(t, harness, harnessText)
	}
	if sharedText != "" {
		writeGlobalRoot(t, shared, sharedText)
	}
	return Context{
		Cwd:                   root,
		GlobalRulesPath:       harness,
		SharedGlobalRulesPath: shared,
		MaxGlobalBytes:        limit,
	}
}

// TestUserGlobalChainRendersBothRootsInOfficialOrder：两根都读到就都渲染，
// harness-home 在前（官方 :34 "then `<agentsHome>/AGENTS.md`"）。
func TestUserGlobalChainRendersBothRootsInOfficialOrder(t *testing.T) {
	ctx := userGlobalContext(t, "harness-home 手册", "shared-root 手册", 65536)
	got := GlobalRules{}.Build(ctx)

	harnessAt := strings.Index(got, "harness-home 手册")
	sharedAt := strings.Index(got, "shared-root 手册")
	if harnessAt < 0 || sharedAt < 0 {
		t.Fatalf("两根没有都渲染（harness=%d shared=%d）：\n%s", harnessAt, sharedAt, got)
	}
	if harnessAt > sharedAt {
		t.Fatalf("顺序反了：shared-root 排在 harness-home 前面（官方 :34 要求 harness 先）:\n%s", got)
	}
	// 两块，不是一块里糊在一起：每根都有自己的 `全局约定（path）：` 头。
	if n := strings.Count(got, "全局约定（"); n != 2 {
		t.Fatalf("章节头 %d 个，期望 2（每根一块）：\n%s", n, got)
	}
	if !strings.Contains(got, filepath.ToSlash(ctx.GlobalRulesPath)) ||
		!strings.Contains(got, filepath.ToSlash(ctx.SharedGlobalRulesPath)) {
		t.Fatalf("块头没有写明各自的真实路径（模型要知道这段来自哪个根）:\n%s", got)
	}
}

// TestUserGlobalChainCollapsesDuplicateSharedRoot：trim 之后内容相同 = 同组重复，
// shared-root 塌在 harness-home 后面（官方 :224），只渲染一份且是 harness-home。
func TestUserGlobalChainCollapsesDuplicateSharedRoot(t *testing.T) {
	ctx := userGlobalContext(t, "同一本手册\n", "\n  同一本手册  \n", 65536)
	got := GlobalRules{}.Build(ctx)

	if n := strings.Count(got, "全局约定（"); n != 1 {
		t.Fatalf("同组重复应只渲染一份，实际 %d 份：\n%s", n, got)
	}
	if !strings.Contains(got, filepath.ToSlash(ctx.GlobalRulesPath)) {
		t.Fatalf("塌掉后留下的不是 harness-home（官方 :224 是 shared-root 塌在后面）:\n%s", got)
	}
	if strings.Contains(got, filepath.ToSlash(ctx.SharedGlobalRulesPath)) {
		t.Fatalf("shared-root 仍然出现在块头里（它该被去重掉）:\n%s", got)
	}
}

// TestUserGlobalChainOmitsHarnessHomeFirst：两根合起来超预算时整份丢掉
// harness-home，再截断留下的 shared-root（官方 :74）。
func TestUserGlobalChainOmitsHarnessHomeFirst(t *testing.T) {
	ctx := userGlobalContext(t, strings.Repeat("A", 300), strings.Repeat("B", 300), 100)
	got := GlobalRules{}.Build(ctx)

	if strings.Contains(got, strings.Repeat("A", 10)) {
		t.Fatalf("超预算时 harness-home 应整份被丢（官方 :74），实际还在：\n%.200s", got)
	}
	if !strings.Contains(got, filepath.ToSlash(ctx.SharedGlobalRulesPath)) {
		t.Fatalf("留下的那一根应是 shared-root（harness-home 先丢）:\n%s", got)
	}
	body := budgetRuleBody(t, got, "全局约定")
	if body != strings.Repeat("B", 100) {
		t.Fatalf("留下的那一根应截到预算 %d 字节，实际 %d 字节", 100, len(body))
	}
	if !strings.Contains(got, "以上内容已截断") {
		t.Fatalf("截断必须可见（不许静默丢内容）:\n%s", got)
	}
}

// TestUserGlobalChainSingleRootKeepsLegacyBytes：只有 harness-home 一根时，
// 逐字节等同改动前的 `rulesFromFile`（既有探针按这个形状钉着）。
func TestUserGlobalChainSingleRootKeepsLegacyBytes(t *testing.T) {
	ctx := userGlobalContext(t, strings.Repeat("G", 200), "", 64)
	legacy := rulesFromFile("全局约定", ctx.GlobalRulesPath, 64)
	if got := (GlobalRules{}).Build(ctx); got != legacy {
		t.Fatalf("单根形状变了（既有探针会红）:\n got %q\nwant %q", got, legacy)
	}
}

// TestUserGlobalChainReadsSharedRootWhenHarnessHomeMissing：harness-home 不存在
// 不代表这条链没有内容——第二根自己就能成为唯一一块（官方 :34 的两根是**然后**，
// 不是"前者必须在"）。
func TestUserGlobalChainReadsSharedRootWhenHarnessHomeMissing(t *testing.T) {
	ctx := userGlobalContext(t, "", "只有 shared-root 有手册", 65536)
	got := GlobalRules{}.Build(ctx)
	if !strings.Contains(got, "只有 shared-root 有手册") {
		t.Fatalf("harness-home 缺席时 shared-root 没进提示词:\n%s", got)
	}
	if !strings.Contains(got, filepath.ToSlash(ctx.SharedGlobalRulesPath)) {
		t.Fatalf("块头没写 shared-root 的真实路径:\n%s", got)
	}
}

// TestUserGlobalChainDisabledByNegativeBudget：`--workspace-bytes=0`（= -1）时
// 整条用户全局链不注入——两根一起消失。
func TestUserGlobalChainDisabledByNegativeBudget(t *testing.T) {
	ctx := userGlobalContext(t, "harness-home 手册", "shared-root 手册", -1)
	if got := (GlobalRules{}).Build(ctx); got != "" {
		t.Fatalf("负预算应整条链不注入，实际：\n%s", got)
	}
}
