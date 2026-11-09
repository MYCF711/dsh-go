package main

// `userQuestions.attachWait` 的**结算**语义（官方 stream 的"wait 结束后一帧都不发"）。
//
// 官方 TimedQuestionWait 有三条结束路径：超时、被回答、流关闭。超时与递减
// 已经由 TestOfficialQuestionAttachWaitIsReal 钉住；这里钉**被回答**那一条：
// 问题已经答完（durable 日志里 tool/result 之后出现 user/message），
// attachWait 就必须一帧都不发。
//
// 红点：以前它只看时钟，问题答完之后还会按 120 s 窗口回一个正的 remainingMs，
// 界面于是对着一个早已结束的提问继续转圈。

import (
	"testing"
	"time"

	"dshgo/internal/session"
	"dshgo/internal/spi"
	"dshgo/internal/transport"
)

// appendQuestionAnswer 把"人回答了这个问题"落成一条真实 user/message。
//
// 判据与宿主 `agentOpenQuestionCall` 同源：本宿主里 ask 结束本轮，
// 人的下一条用户消息就是回答，也就把这个问题关掉了。
func appendQuestionAnswer(t *testing.T, f *agentAPIFixture, sessionID, text string) {
	t.Helper()
	path, err := f.src.resolve(sessionID)
	if err != nil {
		t.Fatalf("解析会话 %s: %v", sessionID, err)
	}
	lg, err := session.OpenSessionAt(path)
	if err != nil {
		t.Fatalf("打开会话 %s: %v", sessionID, err)
	}
	defer lg.Close()
	if err := lg.Append("user/message", session.UserMessage([]spi.Block{{Type: "text", Text: text}})); err != nil {
		t.Fatalf("追加回答: %v", err)
	}
}

// TestOfficialQuestionAttachWaitSettlesAfterAnswer 钉住"被回答 → 不再发帧"。
func TestOfficialQuestionAttachWaitSettlesAfterAnswer(t *testing.T) {
	f := newAgentAPIFixture(t)
	sessionID := f.newSession("", func(lg *session.Log) {
		if err := lg.Append("tool/call", session.ToolCallEvent(1, 1, "call-ask", "ask_user_question", `{"questions":[]}`)); err != nil {
			t.Fatalf("追加 tool/call: %v", err)
		}
		if err := lg.Append("tool/result", session.ToolResultEvent(1, 1, "call-ask", nil, false, "", nil)); err != nil {
			t.Fatalf("追加 tool/result: %v", err)
		}
	})

	// 真实时钟 + 可拨动的 now：deadline 从 durable 的 tool/result 帧时间起算。
	now := time.Now()
	f.core.questionWaits = transport.NewOfficialQuestionWaits(transport.OfficialQuestionWaitOptions{
		Timeout: 10 * time.Second,
		Now:     func() time.Time { return now },
	})

	first, err := f.api.UserQuestionsAttachWait(sessionID, "call-ask")
	if err != nil {
		t.Fatalf("问题还开着时 attachWait: %v", err)
	}
	if first == nil || first.RemainingMs <= 0 {
		t.Fatalf("问题还开着时应当回一帧真实剩余：%+v", first)
	}

	// 人回答了：durable 日志里 tool/result 之后出现 user/message。
	appendQuestionAnswer(t, f, sessionID, "选 B")

	// 窗口还剩 10 s，但问题已经结束：官方语义是一帧都不发。
	after, err := f.api.UserQuestionsAttachWait(sessionID, "call-ask")
	if err != nil {
		t.Fatalf("答完之后的 attachWait: %v", err)
	}
	if after != nil {
		t.Fatalf("问题已经答完，不该再发帧：%+v", after)
	}

	// 结算之后**不许**被重新 arm：再来一次仍然是空帧（否则断线重连会把
	// 一个已经结束的提问重新挂回界面）。
	again, err := f.api.UserQuestionsAttachWait(sessionID, "call-ask")
	if err != nil {
		t.Fatalf("结算后重连 attachWait: %v", err)
	}
	if again != nil {
		t.Fatalf("结算后的等待被重新 arm 了：%+v", again)
	}
}

// TestOfficialQuestionAttachWaitUnknownCallStillLoud 钉住错误路径没有回归：
// 不存在的 callId 仍然响亮失败，不会被"先看开没开"这一步吞成空帧。
func TestOfficialQuestionAttachWaitUnknownCallStillLoud(t *testing.T) {
	f := newAgentAPIFixture(t)
	sessionID := f.newSession("", nil)
	if _, err := f.api.UserQuestionsAttachWait(sessionID, "call-nope"); err == nil {
		t.Fatal("没有这个 question 调用竟然成功了")
	}
}
