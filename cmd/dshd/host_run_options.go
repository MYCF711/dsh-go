package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// 宿主旋钮：`serve` 解析一次，透传给**每一轮**的子进程 `dshd run`。
//
// # 为什么集中成一个结构体
//
// `serve` 起的每一轮就是子进程 `dshd run`（见 serve_tools.go 的 childArgs），
// 而 childArgs 是**逐字段拷贝**：漏一个参数就静默退回子进程的写死默认值
// （`--auto-compact` 变 40000、`--max-chars` 变 4000、`--context-window` 变 0……），
// 界面上完全看不出来。以前这 6 组参数在 serve 侧连 flag 都没有，
// 于是"从 serve 侧根本调不了"——这正是"参数存在但没接上"的典型。
//
// 集中成一个结构体之后，加字段只有两处要改：这里（解析一次）和 run 侧的 flag
// （真正的使用点）；serve 侧的拷贝是一行，不会再漏。
type hostRunOptions struct {
	// autoCompact 是自动压缩的 token 阈值（0 = 关闭）。
	autoCompact int
	// maxChars 是工具输出外置阈值（字符）。
	maxChars int
	// workspaceBytes 是 AGENTS.md 进系统提示的字节上限（0 = 不传）。
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
	// defaultAutoCompact 是自动压缩的默认阈值：40k token。
	defaultAutoCompact = 40000
	// defaultMaxChars 是工具输出外置的默认阈值。
	defaultMaxChars = 4000
)

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
	o := &hostRunOptions{autoCompact: defaultAutoCompact, compactHeadroom: headroom,
		compactPolicies: os.Getenv(EnvCompactModelPolicies)}
	fs.Var(autoCompactValue{target: &o.autoCompact}, "auto-compact",
		"每步前估算超过多少 token 就自动压缩（0/false=关闭，true=默认 40000）")
	fs.IntVar(&o.maxChars, "max-chars", defaultMaxChars,
		"工具输出超过多少字符就外置成 artifact（越小越省 token，越大越少重读）")
	fs.IntVar(&o.workspaceBytes, "workspace-bytes", defaultWorkspaceBytes,
		"AGENTS.md 进系统提示的字节上限（0=不传）")
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
		"--auto-compact", strconv.Itoa(o.autoCompact),
		"--max-chars", strconv.Itoa(o.maxChars),
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

// autoCompactValue 让 `--auto-compact` 同时接受整数与 true/false。
//
// 为什么要布尔写法：用户说"关掉自动压缩"最自然的是 `--auto-compact=false`，
// 只认整数时那条命令会在解析期就失败（`invalid value "false" for flag`），
// 而 run 侧 0 才表示关闭。两种写法落到同一个整数上，语义只有一份。
type autoCompactValue struct {
	target *int
}

func (v autoCompactValue) String() string {
	if v.target == nil {
		return ""
	}
	return strconv.Itoa(*v.target)
}

func (v autoCompactValue) Set(raw string) error {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true":
		*v.target = defaultAutoCompact
		return nil
	case "false":
		*v.target = 0
		return nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("要整数或 true/false（0=关闭），收到 %q", raw)
	}
	if n < 0 {
		return fmt.Errorf("不能是负数（0=关闭），收到 %d", n)
	}
	*v.target = n
	return nil
}
