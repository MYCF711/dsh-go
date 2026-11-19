// `agent-instructions`（官方 #53 的 durable 层）在 **run 入口**（`dshd run`）的接线。
//
// # 这一层做什么（对照官方 `src/index.ts` 的生命周期）
//
//   - **基线**（官方 index.ts:139-205，`compose` 的 baseline 分支）：第一处模型请求之前，
//     把「项目 root→cwd 链」的渲染结果落成 durable `user/message`
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
// # 为什么接在 run 入口而不是 Loop 里
//
// 落盘目标是**这条会话的 v4 日志**（`logw`），而 v4 是单写者——只有 run 入口持有它
// （serve 每轮起一个 `dshd run` 子进程，ACP 同样）。放 Loop 里就得把 writer 传进
// internal/agent，那会让循环包认识会话日志格式，越出它的分层（循环只认 `llm.Message`
// 与事件）。
//
// # 开关（两个，都要开）
//
//   - `DSHD_INSTRUCTION_CHAIN=0` 关：与 cwd 链同一个开关（见 acp.go 的
//     `instructionChainEnabled`），链关掉时 durable 层整体不挂载。
//   - `DSHD_INSTRUCTION_DURABLE=1` 开：**durable 层自己的开关，默认关**。
//     原因写在 `aiInstrDurableEnabled` 里：系统提示已经带着同一份 cwd 链，同时开会把
//     指令注进请求两次并改掉落盘字节；去重整条要改判据（生产提示词字节整体变化），
//     需要明确裁定，不在本单自行翻。默认关时生产报文与改动前**逐字节一致**。
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

// aiInstrDurableEnabled 是 durable 层的挂载开关（**默认关**，显式 `DSHD_INSTRUCTION_DURABLE=1` 开）。
//
// 为什么默认关：cwd 指令链已经在**系统提示**里（`EnableInstructionChain` 默认开），
// 而官方把指令放在一条 durable `user/message` 里。两者同时开会把同一份指令注进请求两次
// （整份基线各一份 = 首轮 prompt 直接翻倍），并且会改掉 run/acp 的落盘字节
// （`TestCheckpointCLIRunSurvivesKill` 这类按字节钉着"请求前最后一帧就是 user/message"的
// 探针会红）。去掉重复只有一条路：让 durable 帧成为唯一载体、系统提示不再装配 project 链
// ——那是**改判据**（生产提示词字节整体变化），按项目纪律要有明确裁定，不在本单自行翻。
//
// 所以本单交付的是**可用、可测、可打开的层**：`DSHD_INSTRUCTION_DURABLE=1` 一个开关生效；
// 默认关时生产报文与改动前逐字节一致。缺口登记 §53 已把这条与裁定项写清。
func aiInstrDurableEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("DSHD_INSTRUCTION_DURABLE"))) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}

// newAIInstrRunner 建一个 run 入口的投影器；链或 durable 开关关掉时返回 nil（整体不挂载）。
//
// 配置与 cwd 链同源：home 来自进程参数，agents home 走 `$DSH_AGENTS_HOME` → `~/.agents`
// （acp.go 的 `resolveSharedGlobalRulesPath` 解析的是**文件路径**；这里要的是**根目录**，
// 输出形状不同、消费方不同，不是同一规则的第二份实现）。预算沿用 cwd 链的官方默认
// （65,536 / 每条链，`defaultWorkspaceBytes`）。
func newAIInstrRunner(home string) *aiInstrRunner {
	if !instructionChainEnabled() || !aiInstrDurableEnabled() {
		return nil
	}
	runner := &aiInstrRunner{
		runner: ainstr.NewRunner(),
		config: ainstr.ResolveConfig(ainstr.Config{
			DshHome:    home,
			AgentsHome: sharedAgentsHomeRoot(),
			MaxBytes:   defaultWorkspaceBytes,
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
// 共用一份会让 A 会话的 touch 触发 B 会话的投影。链被关掉时返回 nil（钩子全 nil）。
func (r *acpRunner) aiInstrFor(sessionID string) *aiInstrRunner {
	r.aiInstrMu.Lock()
	defer r.aiInstrMu.Unlock()
	if r.aiInstr == nil {
		r.aiInstr = map[string]*aiInstrRunner{}
	}
	if runner, ok := r.aiInstr[sessionID]; ok {
		return runner
	}
	runner := newAIInstrRunner(r.home)
	r.aiInstr[sessionID] = runner
	return runner
}
