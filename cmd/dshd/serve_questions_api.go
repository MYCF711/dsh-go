package main

// 官方 `userQuestions.attachWait` 在**宿主侧**的真实定时等待通道。
//
// # 官方语义
//
// `@deepseek-ai/dsh-user-questions` 的 `attachWait` 是 `@Remote({mode:"stream"})`：
// 只在 `askTimed` 立过 TimedQuestionWait 时 yield 一帧
// `{ remainingMs: max(0, deadline - Date.now()) }`，wait 结束之后**一帧都不发**。
//
// # 本仓的落点
//
// 本宿主没有"工具带 timeout 的提问"（`ask_user_question` 靠结束本轮把控制权
// 交回人），所以没有任何 callId 自带 deadline。为了给出**真值**而不是编一个数：
//
//	deadline = 问题被问出来的那一刻（durable `tool/result` 帧上的 time）
//	         + 宿主前台等待窗口（agentQuestionWaitTimeout，默认 120 s）
//
// remainingMs 就是这个窗口的真实剩余。窗口走完 → 等待结束 → **不发帧**
// （L1 投影成空结果），而不是回一个 0 让 UI 继续转圈。
//
// 与官方的一处显式差异（已登记）：官方的窗口由**调用方**给
// （`askTimed(timeoutMs)`），本仓由**宿主**配置——因为本仓的 ask 工具没有
// timeout 入参。数值是真的，来源不同。

import (
	"time"

	"dshgo/internal/transport"
)

// agentQuestionAskedAt 找出一个 ask_user_question 调用**被问出来**的时刻。
//
// 判据与 `agentOpenQuestionCall` 完全同源（同一个 durable 日志、同一条
// `tool/result`），区别只是这里还要那一帧上的时间。
func (a *agentOfficialAPI) agentQuestionAskedAt(sessionID, callID string) (time.Time, error) {
	question, err := a.agentQuestionCall(sessionID, callID)
	if err != nil {
		return time.Time{}, err
	}
	return question.askedAt, nil
}

// ensureQuestionWaits 懒建等待表（宿主对象被复用，表只造一次）。
func (a *agentOfficialAPI) ensureQuestionWaits() *transport.OfficialQuestionWaits {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.questionWaits == nil {
		a.questionWaits = transport.NewOfficialQuestionWaits(transport.OfficialQuestionWaitOptions{})
	}
	return a.questionWaits
}

// userQuestionsAttachWaitHost 是 `userQuestions.attachWait` 的宿主实现。
//
// 返回 (nil, nil) 表示**不发帧**（官方语义：wait 已经结束或压根没有 wait）。
func (a *agentOfficialAPI) userQuestionsAttachWaitHost(sessionID, callID string) (*transport.OfficialQuestionWait, error) {
	if err := a.agentEnsureSession(sessionID); err != nil {
		return nil, err
	}
	askedAt, err := a.agentQuestionAskedAt(sessionID, callID)
	if err != nil {
		return nil, err
	}
	// 先问"这个问题还开着吗"：官方 attachWait 只在 wait **还在**时 yield 一帧，
	// 被回答/关掉之后一帧都不发。只看时钟会让一个已经答完的问题继续按 120 s
	// 窗口回剩余时长——界面就会对着早已结束的提问转圈。
	open, err := a.agentOpenQuestionCall(sessionID, callID)
	if err != nil {
		return nil, err
	}
	waits := a.ensureQuestionWaits()
	if !open {
		waits.Settle(sessionID, callID)
		return nil, nil
	}
	remaining, live := waits.Arm(sessionID, callID, askedAt)
	if !live {
		return nil, nil
	}
	return &transport.OfficialQuestionWait{RemainingMs: remaining}, nil
}
