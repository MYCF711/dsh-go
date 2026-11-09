// Package commands 是 `/goal` 命令的**真实生命周期**实现。
//
// # 为什么需要它
//
// 官方 builtin 命令集里有一条 `/goal`（`@deepseek-ai/dsh-command-goal`，官方插件
// #61）：它让人在 composer 里直接建目标、暂停、恢复、完成、清空，而不用让模型
// 去调工具。本宿主原来只有"显示 goal"这一条只读路径（`cmd/dshd/serve_commands_api.go`
// 的 `commandGoal`），写操作一条都没有——于是 `/goal pause` 这类输入只能落成
// "显示"，用户以为暂停了，实际目标还在跑。这就是 HS-11 的"goal 生命周期"红点。
//
// # 边界
//
// 只做**命令语义**：解析子命令、校验参数、把结果归一化成官方 Outcome。
// 真正的状态迁移、落盘、`goal/change` 帧全部走 GoalStore（宿主既有的
// `goalOverrideStore`）——命令层不写第二份 goal 存储，也不持有状态。
//
// 与官方的一处显式差异（已登记）：官方的 `/goal` 子命令词表由
// `dsh-command-goal` 的本地化文案定义，本宿主按中文产品口径给出等价词表；
// 语义（set/pause/resume/complete/clear/status）逐条对齐。
package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"dshgo/internal/transport"
)

// CommandGoal 是官方 builtin 命令名（不带斜杠）。
const CommandGoal = "goal"

// GoalStore 是 `/goal` 操作的真实目标生命周期。
//
// 为什么用窄接口而不是直接拿 `*goalOverrideStore`：命令层住在 `internal/`，
// 不该认识 `cmd/dshd` 的具体存储类型（红线 R2：模块只拿能力，不拿全局单例）。
// 接口里的每个方法都对应一条**真实的** durable 迁移。
type GoalStore interface {
	// Snapshot 读当前会话的目标（没有目标时 Goal=nil）。
	Snapshot(sessionID string) (transport.GoalResult, error)
	// Set 建/改目标；objective 为空 = clear（既有存储语义）。
	Set(sessionID, objective string) error
	// Pause 暂停一个 active 目标。
	Pause(sessionID string) error
	// Resume 恢复一个 paused/active/blocked 目标（预算用尽时拒绝）。
	Resume(sessionID string) error
	// Complete 把目标标成终态 complete。
	Complete(sessionID string) error
	// Block 写一条带稳定 code 的阻塞原因。
	Block(sessionID, code, message string) error
	// Clear 清掉目标（保留 clear 墓碑帧）。
	Clear(sessionID string) error
}

// goalInputHint 是发现界面上的自由输入提示（官方 `input.hint`）。
const goalInputHint = "status | set <目标> | pause | resume | complete | clear"

// GoalDefinition 把 `/goal` 装配成一条官方命令定义。
//
// 返回 (definition, error) 而不是直接 panic：装配期失败要能在 serve 启动时
// 响亮报出来（`commands.register` 是 fail-loud 的）。
func GoalDefinition(store GoalStore) (transport.OfficialCommandDefinition, error) {
	if store == nil {
		return transport.OfficialCommandDefinition{}, fmt.Errorf("commands: /goal 没有 goal 存储")
	}
	h := &goalHandler{store: store}
	return transport.OfficialCommandDefinition{
		DefinitionID: CommandGoal,
		Name:         CommandGoal,
		Description:  "查看或管理本会话的目标（阶段 / 轮次 / 剩余轮数）",
		Input:        &transport.OfficialCommandInput{Hint: goalInputHint},
		Handler:      h.run,
	}, nil
}

// goalHandler 只持有一个真实存储；命令层自身无状态。
type goalHandler struct {
	store GoalStore
}

// run 分派 `/goal` 的子命令。
//
// 业务失败（没有目标可 pause、预算用尽、参数缺失）一律结算成
// `kind:"error"` 的**结果**：它们是命令语义的一部分，HTTP 200 + durable
// `command/done{kind:"error"}`，不是 transport 500。只有存储读写失败才上抛
// （那是宿主内部错误，`commands.execute` 会记成 contained error）。
func (h *goalHandler) run(_ context.Context, inv transport.OfficialCommandInvocation) (transport.OfficialCommandOutcome, error) {
	sub, rest := splitGoalInput(inv.RawInput)
	switch sub {
	case "", "status":
		return h.status(inv.SessionID)
	case "set":
		objective := strings.TrimSpace(rest)
		if objective == "" {
			return goalError("/goal set 需要目标正文：/goal set 把 X 做完并跑通测试"), nil
		}
		if len([]rune(objective)) > maxGoalObjectiveRunes {
			return goalError(fmt.Sprintf("/goal set 的目标有 %d 字，超过 %d 上界：压缩成一句话再设",
				len([]rune(objective)), maxGoalObjectiveRunes)), nil
		}
		if err := h.store.Set(inv.SessionID, objective); err != nil {
			return goalTransitionError("set", err)
		}
		return transport.OfficialCommandOutcome{Kind: "success", Text: "已设目标：" + objective}, nil
	case "pause":
		if err := h.store.Pause(inv.SessionID); err != nil {
			return goalTransitionError("pause", err)
		}
		return transport.OfficialCommandOutcome{Kind: "success", Text: "目标已暂停：恢复用 /goal resume。"}, nil
	case "resume":
		if err := h.store.Resume(inv.SessionID); err != nil {
			return goalTransitionError("resume", err)
		}
		return transport.OfficialCommandOutcome{Kind: "success", Text: "目标已恢复：自动续轮重新打开。"}, nil
	case "complete":
		if err := h.store.Complete(inv.SessionID); err != nil {
			return goalTransitionError("complete", err)
		}
		return transport.OfficialCommandOutcome{Kind: "success", Text: "目标已标成 complete。"}, nil
	case "clear":
		if err := h.store.Clear(inv.SessionID); err != nil {
			return goalTransitionError("clear", err)
		}
		return transport.OfficialCommandOutcome{Kind: "success", Text: "目标已清空。"}, nil
	case "block":
		reason := strings.TrimSpace(rest)
		if reason == "" {
			return goalError("/goal block 需要一句具体障碍：/goal block 等 CI 权限"), nil
		}
		if err := h.store.Block(inv.SessionID, "user-blocked", reason); err != nil {
			return goalTransitionError("block", err)
		}
		return transport.OfficialCommandOutcome{Kind: "success", Text: "目标已标成 blocked：" + reason}, nil
	}
	return goalError(fmt.Sprintf("未知的 /goal 子命令 %q：%s", sub, goalInputHint)), nil
}

// status 是 `/goal` 的默认形态（也是老 `/goal` 的行为，逐字保留）。
func (h *goalHandler) status(sessionID string) (transport.OfficialCommandOutcome, error) {
	result, err := h.store.Snapshot(sessionID)
	if err != nil {
		return goalTransitionError("status", err)
	}
	if result.Goal == nil {
		return transport.OfficialCommandOutcome{Kind: "success", Text: "本会话没有 goal。"}, nil
	}
	goal := result.Goal
	var b strings.Builder
	fmt.Fprintf(&b, "goal %s：%s\n", goal.ID, goal.Phase)
	fmt.Fprintf(&b, "目标：%s\n", goal.Objective)
	rounds := 0
	if result.RoundsStarted != nil {
		rounds = *result.RoundsStarted
	}
	fmt.Fprintf(&b, "轮次：%d", rounds)
	if goal.MaxGoalRounds > 0 {
		fmt.Fprintf(&b, " / 上限 %d", goal.MaxGoalRounds)
	}
	if goal.BlockedReason != nil {
		fmt.Fprintf(&b, "\n阻塞：%s（%s）", goal.BlockedReason.Message, goal.BlockedReason.Code)
	}
	return transport.OfficialCommandOutcome{Kind: "success", Text: b.String()}, nil
}

// maxGoalObjectiveRunes 与 goal 工具的上界一致：目标会原样回给模型，
// 无界输入等于让人往上下文里塞任意长度的正文。
const maxGoalObjectiveRunes = 4000

// splitGoalInput 把 rawInput 拆成子命令与剩余正文。
//
// 大小写不敏感（人打字不会全是小写），但**不**接受缩写：`/goal p` 不是 pause，
// 猜错了会暂停一个用户以为在跑的目标。
func splitGoalInput(raw string) (string, string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", ""
	}
	sub, rest, _ := strings.Cut(trimmed, " ")
	return strings.ToLower(sub), rest
}

// goalError 造一条 error 结果（官方 `normalizeResult`：text 必须非空）。
func goalError(text string) transport.OfficialCommandOutcome {
	return transport.OfficialCommandOutcome{Kind: "error", Text: text}
}

// goalTransitionError 把存储错误翻成读方该看到的东西。
//
//   - 状态迁移错误（`ErrGoalTransition`）是**命令语义**的一部分：结算成
//     `kind:"error"` 结果（HTTP 200 + durable command/done{error}），
//     文本保留"只有 active 才能 pause"这类具体原因——吞掉它人只能猜；
//   - 其余（落盘/journal 失败）是宿主内部错误：上抛，由 commands.execute
//     记成 contained error 并回 5xx，不伪装成"命令说不行"。
func goalTransitionError(op string, err error) (transport.OfficialCommandOutcome, error) {
	if errors.Is(err, transport.ErrGoalTransition) {
		return goalError(fmt.Sprintf("/goal %s 失败：%v", op, err)), nil
	}
	return transport.OfficialCommandOutcome{}, fmt.Errorf("/goal %s: %w", op, err)
}
