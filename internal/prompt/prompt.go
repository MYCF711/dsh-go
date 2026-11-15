// Package prompt 负责系统提示的**装配**：把若干段拼成一条 system 消息。
//
// 规矩：
//  1. 装配必须**确定性**——同样的输入必须得到同样的字节（否则会话快照会抖）；
//  2. 每段都有稳定的名字，空段直接消失（不留空行）；
//  3. 任何外部内容（AGENTS.md 等）必须**有界**并标注截断。
package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"dshgo/internal/spi"
)

// Context 是装配所需的运行时信息。
type Context struct {
	Model    string
	Cwd      string
	Platform string
	Date     string
	Tools    []spi.Tool
	// ExtraRulesPath 是额外要注入的规则文件（绝对路径或相对 Cwd）。
	// 用途：给不同角色的会话喂不同的手册——维护者读 AGENTS.md，
	// 写代码的执行者读 docs/WORKER.md。两者内容差一个量级。
	ExtraRulesPath string
	// GlobalRulesPath 是全局 AGENTS.md 的路径（绝对路径或相对 Cwd）；空 = 不注入全局规则。
	//
	// 它是官方用户全局链的**第一根**（`$DSH_HOME/AGENTS.md`）：harness home 的进程策略文件。
	GlobalRulesPath string
	// SharedGlobalRulesPath 是用户全局链的**第二根**（`$DSH_AGENTS_HOME/AGENTS.md`，
	// 未设环境变量时 `~/.agents/AGENTS.md`）；空 = 这条链只有一根。
	//
	// 官方 `@deepseek-ai/dsh-agent-instructions/README.md:34`："The first request includes
	// one durable baseline message with the user-global chain — `$DSH_HOME/AGENTS.md`,
	// then `<agentsHome>/AGENTS.md`"；`:40` 明确两个根各自怎么解析
	// （"The shared agents root resolves the same way, from `$DSH_AGENTS_HOME` or
	// `~/.agents`"）。两根**同属一个候选组**（`:224` "The two user-global roots share
	// one group, so the shared-root file collapses behind a matching harness-home file"），
	// 超预算时 harness-home **先于** shared-root 被丢（`:74`）。
	SharedGlobalRulesPath string
	// MaxGlobalBytes 与 MaxWorkspaceBytes 是两条规则链各自的正文字节上限，互不争用。
	MaxGlobalBytes    int
	MaxWorkspaceBytes int
	// PersonaPrefix 是官方 `systemPrompt.personaPrefix`（环境变量
	// `DSH_SYSTEM_PROMPT`，见 @deepseek-ai/dsh-sdk-minimal/cordis.patch.yml:118）：
	// 系统提示的**第一段**，用来替换默认人格。
	//
	// 空 = 不注入（宿主既有的提示词形状逐字节不变）。宿主不内置官方 sdk-minimal
	// 的那句默认 persona：那句属于"整个系统提示只有 persona"的最小 SDK 包，
	// 而宿主的提示是 AGENTS.md + 工具说明 + 运行时事实的装配体，把 sdk-minimal
	// 的默认串塞进来是另一种产品形状，不是能力补齐。要官方那句话，显式设
	// `DSH_SYSTEM_PROMPT='You are a helpful software engineer assistant.'` 即可。
	PersonaPrefix string
	// EnableInstructionChain 打开官方 `@deepseek-ai/dsh-agent-instructions` 的
	// **project root → session cwd** 指令链（实现与官方引文见 instructions.go）。
	//
	// 零值 = 关闭：`Default` 继续走旧的单点 `WorkspaceRules`，宿主既有提示形状
	// 逐字节不变。为什么默认关闭而不是默认开：官方链把项目段从
	// 「一个 AGENTS.md」换成「一条带 `<system-reminder>` 框架的链」，默认翻会
	// 改掉所有既有字节与既有测试断言；官方自己也是"挂载插件才生效"
	// （README.md:30 "Mount this plugin when agents should work from the
	// workspace's own instruction files"）。
	EnableInstructionChain bool
	// ProjectRootMarkers 是官方 `projectRootMarkers`（src/config.ts:11，默认 `['.git']`）：
	// 用这些目录名从 cwd 向上认项目根；找不到就用 cwd 自己（不猜祖先）。
	ProjectRootMarkers []string
	// InstructionFileCandidates 是官方 `instructionFileCandidates`
	// （src/config.ts:12，默认 `['AGENTS.md', 'CLAUDE.md']`）：每个目录先加载的 base 候选。
	InstructionFileCandidates []string
	// LocalInstructionFileCandidates 是官方 `localInstructionFileCandidates`
	// （src/config.ts:13，默认 `['AGENTS.local.md', 'CLAUDE.local.md']`）：base 之后加载的
	// local 叠加；与同目录 base 候选**同组**去重（官方 src/config.ts:34-37
	// "under the same per-directory trimmed-content dedup"）。
	LocalInstructionFileCandidates []string
	// MaxSourceBytes 是官方 `maxSourceBytes`（src/config.ts:14，默认 1048576）：
	// **单个源文件**的上限，超过就整文件丢弃（README 表格 "larger files are ignored"），
	// 不是截断。0 = 用默认值；负值 = 关闭整条链（与 MaxWorkspaceBytes 的哨兵约定一致）。
	MaxSourceBytes int
}

// Provider 是提示词的一段。返回空串即表示这一段这轮不出现。
type Provider interface {
	Name() string
	Build(Context) string
}

// defaultMaxWorkspaceBytes 与 defaultMaxGlobalBytes 是官方 dsh-base 给**每条指令链**
// 的预算。官方原件（asar 只读解出，@deepseek-ai/dsh-agent-instructions/README.md）：
//
//	"dsh-base already includes it with a 65,536-byte budget for each instruction
//	 chain ... a message that holds both chains is at most twice maxBytes."
//
// 以前这里写的是 8000（一次 14 KB AGENTS.md 的旧实测折中），比官方小 8 倍：
// 40 KiB 的 home/AGENTS.md 会被截掉 80%，模型看不到后半本手册。两条链各自
// 65,536、互不争用；要更小/更大用 --workspace-bytes（0=不传）。
const defaultMaxWorkspaceBytes = 65536
const defaultMaxGlobalBytes = 65536

// Assemble 按顺序拼接所有非空段。
//
// 预算三值约定（与 --workspace-bytes 的帮助文案一致）：
//   - 0（零值/未设置）= 用官方默认预算（每条链 65,536 字节）；
//   - 负值 = 这条链**不注入**（`--workspace-bytes=0` 的「不传」由入口映射成 -1）；
//   - 正数 = 这条链的正文字节上限。
func Assemble(ctx Context, providers ...Provider) string {
	if ctx.MaxGlobalBytes == 0 {
		ctx.MaxGlobalBytes = defaultMaxGlobalBytes
	}
	if ctx.MaxWorkspaceBytes == 0 {
		ctx.MaxWorkspaceBytes = defaultMaxWorkspaceBytes
	}

	var parts []string
	for _, p := range providers {
		s := strings.TrimSpace(p.Build(ctx))
		if s == "" {
			continue
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "\n\n")
}

// ---------- 内置的几段 ----------

// Persona 是官方 `systemPrompt.personaPrefix`：一句话说清"你是谁"。
//
// 它是**第一段**（装配顺序里排在 Runtime 之前）：官方把它当人格前缀，位置换了
// 就变成"附注"而不是"人格"。空串（含全空白）= 这一段不出现。
type Persona struct{}

func (Persona) Name() string { return "persona" }

func (Persona) Build(c Context) string { return strings.TrimSpace(c.PersonaPrefix) }

// Runtime 说明"你在哪、用什么模型、今天几号"。
type Runtime struct{}

func (Runtime) Name() string { return "runtime" }

func (Runtime) Build(c Context) string {
	var b strings.Builder
	b.WriteString("Runtime: dsh-go.\n")
	if c.Model != "" {
		fmt.Fprintf(&b, "Model: %s.\n", c.Model)
	}
	if c.Cwd != "" {
		fmt.Fprintf(&b, "Workspace: %s.\n", filepath.ToSlash(c.Cwd))
	}
	if c.Platform != "" {
		fmt.Fprintf(&b, "Platform: %s.\n", c.Platform)
	}
	if c.Date != "" {
		fmt.Fprintf(&b, "Date: %s.\n", c.Date)
	}
	return b.String()
}

// ToolGuide 用一两句话说明工具怎么用（工具定义本身走 API 的 tools 字段，不在这里重复）。
type ToolGuide struct{}

func (ToolGuide) Name() string { return "tools" }

func (ToolGuide) Build(c Context) string {
	if len(c.Tools) == 0 {
		return ""
	}
	var ro, rw []string
	for _, t := range c.Tools {
		if t.Caps().NeedsApproval() {
			rw = append(rw, t.Name())
		} else {
			ro = append(ro, t.Name())
		}
	}
	var b strings.Builder
	b.WriteString("需要信息就调用工具，不要猜。\n")
	if len(ro) > 0 {
		fmt.Fprintf(&b, "只读工具：%s。\n", strings.Join(ro, ", "))
	}
	if len(rw) > 0 {
		fmt.Fprintf(&b, "会改世界的工具：%s——调用前需要用户批准，被拒绝时换只读方式或先说明意图。\n",
			strings.Join(rw, ", "))
	}
	if c.Cwd != "" {
		b.WriteString("路径一律用正斜杠、相对工作区根。\n")
	}
	return b.String()
}

// GlobalRules 把用户的全局 AGENTS.md 带进来；它与工作区规则各有独立预算。
//
// 用户全局链有**两个根**（官方 README.md:34/40）：harness home 的
// `$DSH_HOME/AGENTS.md`，然后 shared agents root 的 `$DSH_AGENTS_HOME/AGENTS.md`
// （未设环境变量时 `~/.agents/AGENTS.md`）。两个根**同属一个候选组**：
//
//   - 内容 trim 之后相同的后者塌在前者后面，只渲染一份（官方 README.md:224
//     "The two user-global roots share one group, so the shared-root file collapses
//     behind a matching harness-home file"）；
//   - 两根合起来超预算时，harness-home **先**被整份丢掉，再截断留下的那一根
//     （官方 README.md:74 "User-global files are therefore omitted or truncated only
//     when they exceed their own budget, with the harness-home file omitted before
//     the shared-root file"）。
//
// 只有一根时逐字节等同改动前（`rulesFromFile` 的同一个块形状）：shared root 缺席
// （绝大多数机器上 `~/.agents/AGENTS.md` 不存在）不产生任何字节差异。
type GlobalRules struct{}

func (GlobalRules) Name() string { return "global" }

// globalRulesCandidates 把两根归一成绝对路径候选，顺序 = 官方"先 harness-home、
// 后 shared-root"。空路径不占位（缺席的根不参与去重与预算）。
func globalRulesCandidates(c Context) []string {
	out := make([]string, 0, 2)
	for _, p := range []string{c.GlobalRulesPath, c.SharedGlobalRulesPath} {
		if strings.TrimSpace(p) == "" {
			continue
		}
		if !filepath.IsAbs(p) && c.Cwd != "" {
			p = filepath.Join(c.Cwd, p)
		}
		out = append(out, p)
	}
	return out
}

// globalRuleFile 是一条**读到内容**的用户全局候选。
//
// text 保留原始字节（含前后空白）：截断预算按官方口径算的是**源文件字节**
// （`rulesFromFile` 的 `len(text) > limit`），trim 只发生在渲染那一步。
type globalRuleFile struct {
	path string
	text string
}

func (GlobalRules) Build(c Context) string {
	candidates := globalRulesCandidates(c)
	if len(candidates) == 0 {
		return ""
	}
	return userGlobalRulesSection(candidates, c.MaxGlobalBytes)
}

// userGlobalRulesSection 读候选、按官方候选组语义去重，再按预算渲染。
//
// 读失败/空文件只跳过它自己（与 `rulesFromFile` 一致）：一根读不了不该连坐另一根，
// 也不该把"读不了"伪装成"没有全局约定"之外的任何东西。
func userGlobalRulesSection(candidates []string, limit int) string {
	if limit < 0 {
		return ""
	}
	seen := make(map[string]bool, len(candidates))
	files := make([]globalRuleFile, 0, len(candidates))
	for _, p := range candidates {
		b, err := os.ReadFile(p)
		if err != nil || len(b) == 0 {
			continue
		}
		text := string(b)
		// 同一个候选组：trim 之后内容相同 = 重复，后来者塌在前者后面
		// （官方 README.md:224 "Deduplication is content-based within one candidate group"）。
		key := strings.TrimSpace(text)
		if seen[key] {
			continue
		}
		seen[key] = true
		files = append(files, globalRuleFile{path: p, text: text})
	}
	if len(files) == 0 {
		return ""
	}
	if limit == 0 {
		// 0 = 未设置（调用方没走 Assemble 的默认预算填充）：不设上限。
		return joinUserGlobalBlocks(files, 0)
	}
	// 预算按**源文件正文字节**算（与 `rulesFromFile` 的 `len(text) > limit` 同一口径，
	// 段标题与截断说明不占预算）：两根合得下就都渲染，合不下才按官方顺序丢。
	total := 0
	for _, f := range files {
		total += len(f.text)
	}
	if total <= limit {
		return joinUserGlobalBlocks(files, 0)
	}
	// 超预算：官方先把整份更宽的丢掉——用户全局链里 harness-home 先于 shared-root。
	if len(files) > 1 {
		return joinUserGlobalBlocks(files[len(files)-1:], limit)
	}
	return joinUserGlobalBlocks(files, limit)
}

// joinUserGlobalBlocks 把候选渲染成 `全局约定（path）：\n正文` 块，块间一个空行；
// limit>0 时对**最后一块**的正文按字节截断（与 `rulesFromFile` 同一形状与同一说明）。
func joinUserGlobalBlocks(files []globalRuleFile, limit int) string {
	blocks := make([]string, 0, len(files))
	for i, f := range files {
		text := f.text
		if limit > 0 && i == len(files)-1 && len(text) > limit {
			text = text[:limit] + truncationNotice
		}
		blocks = append(blocks,
			"全局约定（"+filepath.ToSlash(f.path)+"）：\n"+strings.TrimSpace(text))
	}
	return strings.Join(blocks, "\n\n")
}

// WorkspaceRules 把工作区里的 AGENTS.md 带进来（有界、可缺省）。
type WorkspaceRules struct{}

func (WorkspaceRules) Name() string { return "workspace" }

func (WorkspaceRules) Build(c Context) string {
	if c.Cwd == "" {
		return ""
	}
	candidates := make([]string, 0, 2)
	if c.ExtraRulesPath != "" {
		p := c.ExtraRulesPath
		if !filepath.IsAbs(p) {
			p = filepath.Join(c.Cwd, p)
		}
		candidates = append(candidates, p)
	}
	candidates = append(candidates, filepath.Join(c.Cwd, "AGENTS.md"))
	for _, p := range candidates {
		if section := rulesFromFile("工作区约定", p, c.MaxWorkspaceBytes); section != "" {
			return section
		}
	}
	return ""
}

// truncationNotice 是任何一个被预算截断的正文尾部说明（全局链与工作区链同一句）。
const truncationNotice = "\n\n（以上内容已截断：原文件更大，需要细节请用 read 工具）"

// rulesFromFile 的 limit 只限制文件正文，不包含段标题和截断说明。
// limit 为负 = 这条指令链关闭（`--workspace-bytes=0` 的「不传」语义）。
func rulesFromFile(title, path string, limit int) string {
	if limit < 0 {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return ""
	}
	text := string(b)
	if limit > 0 && len(text) > limit {
		text = text[:limit] + truncationNotice
	}
	return title + "（" + filepath.ToSlash(path) + "）：\n" + strings.TrimSpace(text)
}

// ---------- 便捷入口 ----------

// Default 是默认的装配组合。
//
// `EnableInstructionChain` 打开时，项目段换成官方 project 指令链（ProjectInstructions）：
// 官方的一条消息里用户全局链在前、项目链在后（README.md:34），所以链排在 GlobalRules 之后。
// `ExtraRulesPath` 是宿主的**显式单手册**入口（官方没有这个字段）：一旦给了它就按既有语义
// 只注入那一本，自动发现链让位——否则两本手册会同时进提示词，与既有语义（extra_test.go
// "两本手册不能同时进提示词"）冲突。
func Default(c Context) string {
	var workspace Provider = WorkspaceRules{}
	if c.EnableInstructionChain && strings.TrimSpace(c.ExtraRulesPath) == "" {
		workspace = ProjectInstructions{}
	}
	return Assemble(c, Persona{}, Runtime{}, ToolGuide{}, GlobalRules{}, workspace)
}
