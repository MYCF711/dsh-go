package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"dshgo/internal/compact"
	"dshgo/modules/tools"
)

// 宿主旋钮：`serve` 解析一次，透传给**每一轮**的子进程 `dshd run`。
//
// # 为什么集中成一个结构体
//
// `serve` 起的每一轮就是子进程 `dshd run`（见 serve_tools.go 的 childArgs），
// 而 childArgs 是**逐字段拷贝**：漏一个参数就静默退回子进程的默认值
// （`--auto-compact` 掉回"未设置"= 用户写了 N 也丢、`--max-chars` 掉回"字符臂
// 不参与"= 用户配的字符阈值丢、`--context-window` 变 0……），
// 界面上完全看不出来。以前这 6 组参数在 serve 侧连 flag 都没有，
// 于是"从 serve 侧根本调不了"——这正是"参数存在但没接上"的典型。
//
// 集中成一个结构体之后，加字段只有两处要改：这里（解析一次）和 run 侧的 flag
// （真正的使用点）；serve 侧的拷贝是一行，不会再漏。
type hostRunOptions struct {
	// autoCompact 是自动压缩的**显式覆盖**（三态，见 compact.AutoCompactUnset /
	// compact.AutoCompactOff）：未设置 = 阈值完全按官方口径算；正整数 = 覆盖成
	// 该绝对值；0 = 关闭。出厂**没有**数值默认值——任何宿主自造的默认阈值都会把
	// 大窗口提前截断（历史上那个 40000 就把 1M 窗口截成官方阈值的 1/20）。
	autoCompact int
	// maxChars 是**字符臂**的工具输出外置阈值（字符）。官方 spill-policy 没有这个
	// 旋钮（只有 maxInlineTokens），所以出厂**不设数值默认值**：0 = 字符臂不参与，
	// 只有显式 --max-chars=N 才生效。历史上的 4000 会把工具输出截在官方 token
	// 预算之前（4000 字符 ≈ 1000 估算 token ≪ 官方 12500）。
	maxChars int
	// maxInlineTokens 是官方 #79 `dsh-spill-policy` 的 `config.maxInlineTokens`
	// （文本与图片共享的估算 token 预算）。出厂值就是**官方的 12500**
	// （dsh-base/cordis.patch.yml:417，见 tools.DefaultMaxInlineTokens）；
	// 0 = 未配置（回落 DSHD_MAX_INLINE_TOKENS，再没有就是关闭）。
	// 它必须和 maxChars 一样**逐字段透传**给子进程 `dshd run`：否则"从界面起的会话"
	// 与"命令行起的会话"预算不同，而这种差异在界面上完全看不出来。
	maxInlineTokens int
	// workspaceBytes 是每个工作区指令链的字节上限（渲染进官方 durable 基线帧；0 = 不传）。
	workspaceBytes int
	// contextWindow / maxOutput 是显式容量声明（0 = 按模型容量解析）。
	// run 把它们写进 v4 `request/context`（contextWindow）与请求的 max_tokens（maxOutput）。
	contextWindow int
	maxOutput     int
	// agents 是规则文件路径（空 = 用工作区自己的 AGENTS.md）。
	agents string
	// compactHeadroom / compactPolicies 是官方 context.compaction.headroomTokens /
	// modelPolicies（默认 65536 / 空）。serve 自己不压缩，但必须原样交给
	// 子进程 `dshd run`——否则"从界面起的会话"与"命令行起的会话"阈值不同。
	compactHeadroom int
	compactPolicies string
}

// 这些默认值与 `cmdRun` 的同名 flag **同源**：常量只定义一次，serve 侧不写第二份字面量。
// （`defaultWorkspaceBytes` 在 main.go：65536，run 与 serve 都用它。）
const (
	// autoCompactAuto 是 `--auto-compact=auto` 的写法，意思是"不覆盖"：
	// 阈值完全按官方口径 floor(min(W×0.8, W−O−B)) 算。
	//
	// 它同时是**透传**用的拼写：serve 要把"用户没设过这个旋钮"这个事实交给子进程
	// `dshd run`，而 argv 里只有字符串；`auto` 比传一个数字多带这一位信息，
	// 也不用再加一个隐藏 flag。（字符串 `auto` ↔ 数值 compact.AutoCompactUnset。）
	autoCompactAuto = "auto"
	// defaultReasoningEffort 是**出厂**推理档 —— 官方出厂**不设**，所以这里也是空。
	//
	// 2026-11-21 自纠：上一版这里写死 `"max"`，依据是
	//
	//	D:/DeepSeek Harness/home/profiles/desktop/cordis.patch.yml.bak-...:8
	//
	// **那是本机 $DSH_HOME 下的用户 profile，不是官方出厂配置。** 把用户自己的
	// 偏好当成官方默认写进宿主，等于让每个装机的人都被强加一个他们没选过的档 ——
	// 这正是「很多参数不是用户设的」那类错误的同一个形状。
	//
	// 官方出厂的事实（rc.5 基线，`tmp/GO8-RC5/official-rc5/dsh/node_modules/
	// @deepseek-ai/dsh-base/cordis.patch.yml`）：`reasoningEffort` **零命中**，
	// 出厂不声明这一档；模型目录里各模型自带 `reasoningEfforts` 列表，由用户在
	// 设置里选，没选就不发。
	//
	// 所以：默认空 = 不发；要指定就显式 `--reasoning=max|high|...`。
	defaultReasoningEffort = ""
)

// defaultReasoningEffortFromEnv 是三个入口（run / acp / serve）共用的默认值解析。
//
// 与 `--base` 同一形状：`envOr(env, 唯一常量)`，env 只在非空时覆盖。
func defaultReasoningEffortFromEnv() string {
	return envOr("DSHD_REASONING_EFFORT", defaultReasoningEffort)
}

// defaultMaxInlineTokensFromEnv 是三个入口（run / acp / serve）共用的 token 预算默认值。
//
// 出厂值 = **官方的** 12500（`tools.DefaultMaxInlineTokens`，原件
// `dsh-base/cordis.patch.yml:417`）；`DSHD_MAX_INLINE_TOKENS` 只在非空时覆盖它——
// 与 `--base` / `--reasoning` 同一形状：env 只当默认值，显式 flag 仍然优先。
//
// 非法 env 返回 0：flag 注册期没有错误返回口，而 0 会让装配期的
// `modules/tools.resolveOptions` 再读同一个变量并报**原文**错误（启动期 fail-loud，
// 消息里带非法值本身）。显式 `--max-inline-tokens=N` 仍然优先，不被坏 env 拖垮。
//
// 为什么出厂值不能是 0（改前就是这样）：官方语义里 0/省略 = **关闭**整个 token
// 保留策略，于是宿主出厂等于"官方能力一项都没开"，只剩字符臂在按自造的 4000 截。
func defaultMaxInlineTokensFromEnv() int {
	v, configured, err := tools.MaxInlineTokensFromEnv(os.Getenv)
	if err != nil {
		return 0
	}
	if !configured {
		return tools.DefaultMaxInlineTokens
	}
	return v
}

// addSpillBudgetFlags 是 run / acp / serve 三个入口注册**工具结果预算**旋钮的唯一一处。
//
// 为什么必须共用：同一份配置换个入口就换了预算，是这组参数最难解释的差异（该文件
// 顶部那段注释说的就是这个）。共用一个注册函数之后，"三入口一致"是结构性的，
// 而且它可以被直接解析（TestInlineBudgetOfficialDefaults 就是这么测的）。
//
// 两个旋钮的出厂值**都来自官方**，不是我们拍的：
//
//   - `--max-inline-tokens`：官方 `@deepseek-ai/dsh-spill-policy` 的
//     `config.maxInlineTokens: 12500`（`dsh-base/cordis.patch.yml:417`）。
//     改前出厂是 0 = 官方语义的"关闭保留策略"——等于官方能力一项都没开。
//   - `--max-chars`：官方 spill-policy **没有**按字符的旋钮（最接近的
//     `thresholdChars: 8192` 属于另一个插件 tool-result-pruner），所以出厂
//     **不设数值默认值**：0 = 字符臂不参与，显式写 N 才生效。改前那个 4000 是宿主
//     自造的，它 ≈1000 估算 token，会把官方 12500 的预算整个架空。
func addSpillBudgetFlags(fs *flag.FlagSet, maxChars, maxInlineTokens *int) {
	fs.IntVar(maxChars, "max-chars", 0,
		"工具输出外置阈值（字符；官方无此旋钮 → 出厂不设=不参与，显式写才生效）")
	fs.IntVar(maxInlineTokens, "max-inline-tokens", defaultMaxInlineTokensFromEnv(),
		"工具结果内联的估算 token 预算（文本+图片共享；官方出厂 12500；0=未配置，回落 DSHD_MAX_INLINE_TOKENS）")
}

// addHostRunFlags 把宿主旋钮注册到给定 flagset，返回解析目标。
//
// run / serve 都调它一次就能拿到完全一致的默认值与语义；哪天要加第 7 个旋钮，
// 只改这里与 childArgs 的 append，不会出现"两个入口各写一份默认值"。
func addHostRunFlags(fs *flag.FlagSet) *hostRunOptions {
	headroom, err := compactHeadroomFromEnv(os.Getenv)
	if err != nil {
		// 解析失败时留一个**非法值**，让 args() 之后的启动路径照样 fail-loud，
		// 而不是在 flag 注册期 panic（注册期没有错误返回口）。
		headroom = -1
	}
	o := &hostRunOptions{autoCompact: compact.AutoCompactUnset, compactHeadroom: headroom,
		compactPolicies: os.Getenv(EnvCompactModelPolicies)}
	addAutoCompactFlag(fs, &o.autoCompact)
	addSpillBudgetFlags(fs, &o.maxChars, &o.maxInlineTokens)
	fs.IntVar(&o.workspaceBytes, "workspace-bytes", defaultWorkspaceBytes,
		"每个工作区指令链的字节上限（进 durable 帧；0=不传）")
	fs.IntVar(&o.contextWindow, "context-window", 0,
		"显式上下文窗口（写进 v4 request/context，界面据此算占比）；0=按模型容量解析")
	fs.IntVar(&o.maxOutput, "max-output", 0,
		"显式单次最大输出 token；0=按模型容量解析")
	fs.StringVar(&o.agents, "agents", "",
		"规则文件路径（默认工作区 AGENTS.md）")
	fs.IntVar(&o.compactHeadroom, "compact-headroom-tokens", o.compactHeadroom,
		"窗口下方预留多少 token 的安全带（官方 headroomTokens，默认 65536；0=不留）")
	fs.StringVar(&o.compactPolicies, "compact-model-policies", o.compactPolicies,
		"按 provider/model 覆盖压缩阈值（JSON 数组；空=不覆盖）")
	return o
}

// args 返回透传给子进程 `dshd run` 的参数（每轮都带，确保父进程的值是唯一真值）。
func (o *hostRunOptions) args() []string {
	if o == nil {
		return nil
	}
	args := []string{
		"--auto-compact", o.autoCompactArg(),
		"--max-chars", strconv.Itoa(o.maxChars),
		"--max-inline-tokens", strconv.Itoa(o.maxInlineTokens),
		"--workspace-bytes", strconv.Itoa(o.workspaceBytes),
		"--context-window", strconv.Itoa(o.contextWindow),
		"--max-output", strconv.Itoa(o.maxOutput),
		"--compact-headroom-tokens", strconv.Itoa(o.compactHeadroom),
	}
	// 空 = 子进程按默认策略走；显式传空串会覆盖成"没有覆盖"，语义不同。
	if strings.TrimSpace(o.compactPolicies) != "" {
		args = append(args, "--compact-model-policies", o.compactPolicies)
	}
	// 空 = 让子进程用工作区自己的 AGENTS.md。显式传空串会覆盖成"不要规则"，
	// 那是另一种语义，不能靠"传个空值碰运气"。
	if strings.TrimSpace(o.agents) != "" {
		args = append(args, "--agents", o.agents)
	}
	return args
}

// autoCompactArg 是透传给子进程 `dshd run` 的 `--auto-compact` 取值。
//
// 三态各自一个拼写，子进程不需要再猜"这个数字是谁给的"：
//
//	未设置 → "auto"  子进程按官方口径算阈值
//	0      → "0"     子进程按关闭处理
//	N > 0  → "N"     子进程按用户覆盖处理
func (o *hostRunOptions) autoCompactArg() string {
	switch {
	case o.autoCompact > compact.AutoCompactOff:
		return strconv.Itoa(o.autoCompact)
	case o.autoCompact == compact.AutoCompactOff:
		return "0"
	default:
		return autoCompactAuto
	}
}

// addAutoCompactFlag 是 run / serve / acp 三个入口注册 `--auto-compact` 的唯一一处。
//
// 以前三个入口各抄一份 help 字符串，于是同一条规则有三种说法；现在只有这一份。
func addAutoCompactFlag(fs *flag.FlagSet, target *int) {
	fs.Var(autoCompactValue{target: target}, "auto-compact",
		"自动压缩阈值：不写/auto=按官方口径（窗口×0.8 与 W−O−B 取小）| N=覆盖为该绝对值 | 0/false=关闭")
}

// autoCompactValue 让 `--auto-compact` 同时接受整数、auto 与 true/false。
//
// 三种写法的语义（都在同一个三态上，没有第二份状态）：
//
//	不写 / auto / true → 不覆盖，阈值按官方口径（compact.AutoCompactUnset）
//	0 / false          → 关闭（compact.AutoCompactOff）
//	N > 0              → 覆盖为该绝对值
//
// 为什么要布尔写法：用户说"关掉自动压缩"最自然的是 `--auto-compact=false`，
// 只认整数时那条命令会在解析期就失败（`invalid value "false" for flag`）。
type autoCompactValue struct {
	target *int
}

func (v autoCompactValue) String() string {
	if v.target == nil {
		return ""
	}
	// `--help` 显示 "default auto"：出厂没有数值默认值，阈值是官方口径算出来的。
	if !compact.AutoCompactEnabled(*v.target) {
		return "0"
	}
	if *v.target == compact.AutoCompactUnset {
		return autoCompactAuto
	}
	return strconv.Itoa(*v.target)
}

func (v autoCompactValue) Set(raw string) error {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case autoCompactAuto, "true":
		// "打开自动压缩"，不是"要某个数值"：阈值交给官方口径。
		*v.target = compact.AutoCompactUnset
		return nil
	case "false":
		*v.target = compact.AutoCompactOff
		return nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("要整数、auto 或 true/false（0=关闭），收到 %q", raw)
	}
	if n < 0 {
		return fmt.Errorf("不能是负数（0=关闭，不写=按官方口径），收到 %d", n)
	}
	*v.target = n
	return nil
}
