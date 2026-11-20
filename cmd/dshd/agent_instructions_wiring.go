// `agent-instructions`（官方 #53 的 durable 层）在**两个生产入口**（`dshd run` /
// ACP）的接线。
//
// # 这一层做什么（对照官方 `src/index.ts` 的生命周期）
//
//   - **基线**（官方 index.ts:139-205，`compose` 的 baseline 分支）：第一处模型请求之前，
//     把「用户全局链（`$DSH_HOME` → `<agentsHome>`）+ 项目 root→cwd 链」的渲染结果
//     落成 durable `user/message`
//     （`source.kind='agent-instructions'`，带 `baseline:true` + `baselineIdentity` +
//     `changes`）。之后每一轮靠**身份对账**决定要不要重发：身份相等 → 只做增量
//     reconcile；不等（cwd 变了 / 配置变了）→ 发**替换型**基线并显式 remove 旧基线里
//     没有覆盖的 scope。
//   - **touch → 嵌套发现**（官方 index.ts:358-374 + state.ts:302-304）：read/write/edit
//     成功的文件路径在 step 边界喂给 reconcile，`pkg/` 这类**后代目录**里此前没进上下文
//     的指令文件会在下一步以 `Additional instructions from:` 追加进来；文件内容变了是
//     `Updated instructions from:`；文件没了是 `Instructions removed:`。
//   - **跨轮状态**：真值只有日志。`ainstr.FrameStateFromLog` 每次现读现重放，
//     重启/断线后自然对齐（官方 resume reconcile 的机制；连"基线被预算挡掉的 scope"
//     本仓也从日志推，比官方的内存表更强）。
//
// # 唯一载体（默认形状，官方形状）
//
// **工作区指令只有一个载体：这条 durable 基线帧。** 官方 README.md:34：
//
//	"The first request includes one durable baseline message with the user-global chain
//	 — `$DSH_HOME/AGENTS.md`, then `<agentsHome>/AGENTS.md` — followed by the project
//	 chain: every existing candidate file from the project root down to the session
//	 working directory, in broad-to-specific order."
//
// 所以系统提示里**不装配任何指令段**（`prompt.Context.InstructionsInDurableFrame`，
// 见 `prompt.Default`）：用户全局链两根 + 项目链全在这一条 `user/message` 里，
// 请求里每份 AGENTS.md 的内容恒出现**一次**。这条判据只有一处：
// `instructionsCarriedByDurableFrame` —— 系统提示侧与 durable 侧共用它，两边不可能
// 各判各的（各判各的就会一边开一边关，同一份指令注两次或一次都不注）。
//
// # 什么情况下不是唯一载体（四条显式出口，都与"默认"无关）
//
//   - `--agents <path>`：宿主独有的**显式单手册**入口（官方没有这个字段）。给了就是
//     "只注入这一本"，durable 的自动发现链让位，系统提示按旧形状带那一本。
//   - `DSHD_INSTRUCTION_CHAIN=0`：整条链关掉（老开关）。durable 不挂载，系统提示回到
//     旧形状（`GlobalRules` + 单点 `WorkspaceRules`）。
//   - `DSHD_INSTRUCTION_DURABLE=0`：只关 durable 层（回滚开关）。同样回到旧形状。
//   - 没有 harness home 的装配（`buildSystemPromptWithHome(..., home="")`）：durable 帧
//     是会话日志里的内容，没有 home 就没有这条会话，也就没有载体——系统提示按旧形状。
//
// # 为什么接在 run 入口而不是 Loop 里
//
// 落盘目标是**这条会话的 v4 日志**（`logw`），而 v4 是单写者——只有 run 入口持有它
// （serve 每轮起一个 `dshd run` 子进程，ACP 同样）。放 Loop 里就得把 writer 传进
// internal/agent，那会让循环包认识会话日志格式，越出它的分层（循环只认 `llm.Message`
// 与事件）。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"dshgo/codecs/v4"
	"dshgo/internal/agent"
	"dshgo/internal/ainstr"
	"dshgo/internal/session"
)

// aiInstrRunner 是一个 run 进程（一个会话）的 durable 指令投影器。
type aiInstrRunner struct {
	runner *ainstr.Runner
	config ainstr.Config
}

// v4EventSource 是 `codecs/v4.Log` 对 `ainstr.LoadSessionEvents` 的适配
// （`ainstr` 不 import `codecs/v4`，分层与官方包一致：它只认接口）。
type v4EventSource struct {
	log *v4.Log
}

func (s v4EventSource) Types() []string {
	out := make([]string, 0, len(s.log.Events))
	for _, event := range s.log.Events {
		out = append(out, event.Type)
	}
	return out
}

func (s v4EventSource) Payloads() [][]byte {
	out := make([][]byte, 0, len(s.log.Events))
	for _, event := range s.log.Events {
		out = append(out, []byte(event.Data))
	}
	return out
}

// aiInstrDurableEnabled 是 durable 层的开关（**默认开**，官方形状；
// `DSHD_INSTRUCTION_DURABLE=0` 显式关）。
//
// 三值约定与 `instructionChainEnabled` 同一种形状：未设/空前缀 = 默认（开）、
// `0/false/off/no` = 关。关掉它必须与系统提示侧**同一个判断**：只关 durable 而不把
// 指令放回系统提示，等于一次都不注（比注两次更糟，且没有任何断言会红）。
// 所以这个函数只被 `instructionsCarriedByDurableFrame` 消费，不要单独读它。
func aiInstrDurableEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("DSHD_INSTRUCTION_DURABLE"))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// instructionsCarriedByDurableFrame 回答"这一轮的**工作区指令由谁承载**"。
//
// 唯一真值，两个消费方共用（不许各判各的）：
//   - 系统提示侧：为真 ⇒ `prompt.Context.InstructionsInDurableFrame`，
//     `GlobalRules` / `WorkspaceRules` / `ProjectInstructions` 全部不装配；
//   - durable 侧：为真 ⇒ `newAIInstrRunner` 建投影器（基线帧 = 唯一载体）。
//
// 四条让位条件写在文件头（`--agents` / `DSHD_INSTRUCTION_CHAIN=0` /
// `DSHD_INSTRUCTION_DURABLE=0` / 没有 harness home）。没有第五个条件：
// 默认形状就是"durable 帧唯一承载"，不存在"还要再设一个变量才生效"。
func instructionsCarriedByDurableFrame(home string) bool {
	if strings.TrimSpace(home) == "" {
		// 没有 harness home 就没有这条会话的 durable 记录（日志在 home 下），
		// 也就没有载体。这条装配入口（home=""）因此保持旧形状。
		return false
	}
	if !instructionChainEnabled() {
		return false
	}
	// `--agents` 是宿主的显式单手册入口：只注入那一本，自动发现链让位。
	if strings.TrimSpace(*rulesPath) != "" {
		return false
	}
	return aiInstrDurableEnabled()
}

// aiInstrChainMaxBytes 把宿主 `--workspace-bytes` 的三值口径映射成官方 `maxBytes`。
//
//	0  = 官方默认（每条链 65,536，见 `defaultWorkspaceBytes`）；
//	>0 = 该链的正文字节上限（官方语义：两条链各自一份）；
//	<0 = 这条链不注入（官方 `maxBytes <= 0` 时什么都不渲染，src/files.ts:474）。
//
// 必须由调用方把宿主自己的哨兵值换算好再传：run 的 `--workspace-bytes 0` 表示"不传"
// （`workspaceBytesForPrompt` 给 -1），而 ACP 的 0 表示"用默认"。两种约定各自保留，
// 换算点就在调用方（见 main.go / acp.go 的 `newAIInstrRunner` 调用）。
func aiInstrChainMaxBytes(workspaceBytes int) int {
	if workspaceBytes == 0 {
		return defaultWorkspaceBytes
	}
	return workspaceBytes
}

// newAIInstrRunner 建一个 run 入口的投影器；不是唯一载体时返回 nil（整体不挂载）。
//
// 配置与 cwd 链同源：home 来自进程参数，agents home 走 `$DSH_AGENTS_HOME` → `~/.agents`
// （acp.go 的 `resolveSharedGlobalRulesPath` 解析的是**文件路径**；这里要的是**根目录**，
// 输出形状不同、消费方不同，不是同一规则的第二份实现）。预算与系统提示同源
// （`--workspace-bytes`）：两者不同源时，"系统提示里截到 8,000"与"durable 帧里给 65,536"
// 会同时成立，`--workspace-bytes` 就成了一个只影响其中一半的开关。
func newAIInstrRunner(home string, workspaceBytes int) *aiInstrRunner {
	if !instructionsCarriedByDurableFrame(home) {
		return nil
	}
	runner := &aiInstrRunner{
		runner: ainstr.NewRunner(),
		config: ainstr.ResolveConfig(ainstr.Config{
			DshHome:    home,
			AgentsHome: sharedAgentsHomeRoot(),
			MaxBytes:   aiInstrChainMaxBytes(workspaceBytes),
		}),
	}
	return runner
}

// sharedAgentsHomeRoot 是用户全局链第二根的**根目录**（`$DSH_AGENTS_HOME` → `~/.agents`）。
func sharedAgentsHomeRoot() string {
	if root := strings.TrimSpace(os.Getenv(sharedAgentsHomeEnv)); root != "" {
		return root
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".agents")
}

// Baseline 在第一处模型请求之前跑一次基线投影。
//
// 返回值是**这一轮要不要把帧正文并进请求消息**：官方把上下文折进本步的已认领批次
// （index.ts:351-355），模型在**同一轮**就看得到；宿主这边帧是落盘的，但本轮请求的
// 消息列表来自"落盘前的快照 + 用户消息"，所以调用方必须把返回的正文再追加到 hist 一次，
// 否则第一轮模型看不见它（第二轮起从日志重放才有）。
//
// 失败语义：**响亮失败**（返回错误，本轮不派发）——投影写不进去却继续跑，模型会拿到
// "没有上下文"的一轮，而日志里没有任何痕迹；那正是官方 fail-closed（#78）要防的形状。
func (a *aiInstrRunner) Baseline(logw *session.Log, cwd string) (string, error) {
	if a == nil {
		return "", nil
	}
	composed, err := a.composeAndCommit(logw, cwd, nil)
	if err != nil {
		return "", fmt.Errorf("agent-instructions 基线投影失败: %w", err)
	}
	if composed.Frame == nil {
		return "", nil
	}
	return composed.Frame.Text, nil
}

// AfterStep 在 step 边界跑一次 touch 驱动的投影（官方 step/end 分支）。
//
// `paths` 是这一步 read/write/edit 碰过的文件路径（`EndStep` 攒批取出）。
// 空批直接跳过（不发空帧；官方 `compose` 的 `content.length === 0` 同一早退）。
//
// 失败语义与基线**不同**：只告警不中断（官方 projection 失败 `ctx.logger.warn` 后
// 继续，`src/index.ts:290-292`）——指令刷新是增强能力，不是请求前缀的一部分；
// 但必须留痕，"文件改了没刷新"不能变成查不到原因。
func (a *aiInstrRunner) AfterStep(logw *session.Log, cwd string, paths []string) {
	if a == nil || len(paths) == 0 {
		return
	}
	if _, err := a.composeAndCommit(logw, cwd, paths); err != nil {
		fmt.Fprintf(os.Stderr, "agent-instructions: step 投影失败（本轮继续）: %v\n", err)
	}
}

// composeAndCommit 是两次投影的共同主体：现读日志 → compose → 落盘。
func (a *aiInstrRunner) composeAndCommit(logw *session.Log, cwd string, touched []string) (ainstr.Composed, error) {
	options := ainstr.Options{
		Config:       a.config,
		Cwd:          cwd,
		TouchedPaths: touched,
	}
	return a.runner.ComposeAndCommit(options,
		func() (ainstr.FrameState, error) {
			log, err := v4.ReadFile(logw.Path)
			if err != nil {
				return ainstr.FrameState{}, err
			}
			return ainstr.FrameStateFromLog(v4EventSource{log: log}), nil
		},
		func(frame ainstr.Frame) error { return commitAIInstrFrame(logw, frame) },
	)
}

// commitAIInstrFrame 把一帧落成 durable `user/message`（v4 单写者纪律：只走 logw.Append）。
func commitAIInstrFrame(logw *session.Log, frame ainstr.Frame) error {
	event := ainstr.FrameEventOf(frame)
	return logw.Append(event.Type, event.Data)
}

// aiInstrOnToolCallSettled 是 Loop 的 touch 采集钩子。
//
// 判据只有一份（`agent.ToolTouchPaths` → `ainstr.FilePathFromToolCall`）；
// 失败的调用在这里被丢掉（官方 `!result.isError`，`src/index.ts:361`）。
func (a *aiInstrRunner) OnToolCallSettled(toolName string, arguments json.RawMessage, success bool) {
	if a == nil || !success {
		return
	}
	for _, path := range agent.ToolTouchPaths(toolName, arguments) {
		a.runner.Touch(path)
	}
}

// PreStep 包住既有的 PreStep：checkpoint 优先（fail-closed 语义不变），
// 成功后标记 step 开始（touch 只攒不投——官方 `projectTouch` 的 step-open 分支）。
func (a *aiInstrRunner) PreStep(inner func(context.Context, int) error) func(context.Context, int) error {
	if a == nil {
		return inner
	}
	return func(ctx context.Context, step int) error {
		if inner != nil {
			if err := inner(ctx, step); err != nil {
				return err
			}
		}
		a.runner.BeginStep()
		return nil
	}
}

// OnStepEnd 在 step 结束时取出攒批并投影。
func (a *aiInstrRunner) OnStepEnd(logw *session.Log, cwd string) {
	if a == nil {
		return
	}
	paths := a.runner.EndStep()
	a.AfterStep(logw, cwd, paths)
}

// aiInstrFor 返回某会话的 durable 投影器（ACP 进程内多会话 ⇒ 每会话一份）。
//
// 与 run 入口的差别只有"按会话隔离"：version 缓存与攒批都是每会话状态，
// 共用一份会让 A 会话的 touch 触发 B 会话的投影。不是唯一载体时返回 nil（钩子全 nil）。
func (r *acpRunner) aiInstrFor(sessionID string) *aiInstrRunner {
	r.aiInstrMu.Lock()
	defer r.aiInstrMu.Unlock()
	if r.aiInstr == nil {
		r.aiInstr = map[string]*aiInstrRunner{}
	}
	if runner, ok := r.aiInstr[sessionID]; ok {
		return runner
	}
	runner := newAIInstrRunner(r.home, r.workspaceBytes)
	r.aiInstr[sessionID] = runner
	return runner
}
