package session

import (
	"testing"

	"dshgo/codecs/v4"
)

// 官方 rc.2 新增 `conversationContinuation` 投影（`stateVersion: 4`）：
//
//	`dsh-api-session-controller/lib/index.js:479-513`（定义）、`:706`（注册）、
//	可续错误码集 `:459`（RATE_LIMIT/SERVER/TIMEOUT/TRANSPORT/EMPTY_RESPONSE/STREAM_CLOSED）。
//
// 投影值 = 可续跑的最新失败 `turn/end` 的 seq，或 null。
func TestConversationContinuationUnitRegistered(t *testing.T) {
	registry := DefaultProjectionRegistry()
	unit, ok := registry.Unit(ProjectionKeyConversationContinuation)
	if !ok {
		t.Fatalf("conversationContinuation 必须注册在默认投影表里")
	}
	if unit.StateVersion != 4 {
		t.Fatalf("stateVersion = %d，官方是 4（rc.1 的 3 已被这个键占用过）", unit.StateVersion)
	}
}

func continuationTracker() *ProjectionTracker {
	return NewProjectionTracker(DefaultProjectionRegistry(), nil, -1, v4.Header{ID: "s1", Version: 4}, 0)
}

func appliedEnding(t *testing.T, events ...v4.Event) any {
	t.Helper()
	tracker := continuationTracker()
	for _, ev := range events {
		tracker.Apply(ev)
	}
	return tracker.Snapshot().Values[ProjectionKeyConversationContinuation]
}

func userMsgEvent(t *testing.T, seq int64, kind string, blocks int, surfaceOp string) v4.Event {
	t.Helper()
	content := make([]map[string]any, 0, blocks)
	for i := 0; i < blocks; i++ {
		content = append(content, map[string]any{"type": "text", "text": "问"})
	}
	data := map[string]any{
		"content": content,
		"source":  map[string]any{"kind": kind},
		"role":    "user",
	}
	if surfaceOp != "" {
		data["surfaceOp"] = surfaceOp
	}
	return testEvent(t, "user/message", seq, data)
}

func assistantAttemptEvent(t *testing.T, seq int64) v4.Event {
	t.Helper()
	return testEvent(t, "assistant/attempt", seq, map[string]any{"turn": 1, "step": 1, "stream": []any{}})
}

func turnEndEvent(t *testing.T, seq int64, reason map[string]any) v4.Event {
	t.Helper()
	return testEvent(t, "turn/end", seq, map[string]any{"turn": 1, "reason": reason})
}

// 三段全中才有资格续跑：admitted（人类输入被接收）+ failedAttempt（尝试过）+ 可续错误码。
func TestConversationContinuationEligibleSequence(t *testing.T) {
	ending := appliedEnding(t,
		testEvent(t, "turn/start", 0, map[string]any{"turn": 1}),
		userMsgEvent(t, 1, "user", 1, "append"),
		assistantAttemptEvent(t, 2),
		turnEndEvent(t, 3, map[string]any{"kind": "error", "error": map[string]any{"code": "RATE_LIMIT"}}),
	)
	if ending != int64(3) {
		t.Fatalf("conversationContinuation = %#v，want 3（那次 turn/end 的 seq）", ending)
	}
}

func TestConversationContinuationSixCodes(t *testing.T) {
	for _, code := range ContinuableFailureCodes {
		ending := appliedEnding(t,
			testEvent(t, "turn/start", 0, map[string]any{"turn": 1}),
			userMsgEvent(t, 1, "user", 1, "append"),
			assistantAttemptEvent(t, 2),
			turnEndEvent(t, 3, map[string]any{"kind": "error", "error": map[string]any{"code": code}}),
		)
		if ending != int64(3) {
			t.Fatalf("可续码 %s 应产出 ending=3，得到 %#v", code, ending)
		}
	}
	if len(ContinuableFailureCodes) != 6 {
		t.Fatalf("官方可续错误码恰好 6 个，得到 %d：%v", len(ContinuableFailureCodes), ContinuableFailureCodes)
	}
}

func TestConversationContinuationRejectsNonContinuable(t *testing.T) {
	cases := []struct {
		name   string
		events []v4.Event
	}{
		{"不可续错误码", []v4.Event{
			testEvent(t, "turn/start", 0, map[string]any{"turn": 1}),
			userMsgEvent(t, 1, "user", 1, "append"),
			assistantAttemptEvent(t, 2),
			turnEndEvent(t, 3, map[string]any{"kind": "error", "error": map[string]any{"code": "CONTEXT_LENGTH_EXCEEDED"}}),
		}},
		{"正常结束", []v4.Event{
			testEvent(t, "turn/start", 0, map[string]any{"turn": 1}),
			userMsgEvent(t, 1, "user", 1, "append"),
			assistantAttemptEvent(t, 2),
			turnEndEvent(t, 3, map[string]any{"kind": "completed"}),
		}},
		{"没尝试过（无 assistant/attempt）", []v4.Event{
			testEvent(t, "turn/start", 0, map[string]any{"turn": 1}),
			userMsgEvent(t, 1, "user", 1, "append"),
			turnEndEvent(t, 3, map[string]any{"kind": "error", "error": map[string]any{"code": "RATE_LIMIT"}}),
		}},
		{"没有 admitted 的人类输入", []v4.Event{
			testEvent(t, "turn/start", 0, map[string]any{"turn": 1}),
			assistantAttemptEvent(t, 2),
			turnEndEvent(t, 3, map[string]any{"kind": "error", "error": map[string]any{"code": "RATE_LIMIT"}}),
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := appliedEnding(t, c.events...); got != nil {
				t.Fatalf("不该产出 continuation，得到 %#v", got)
			}
		})
	}
}

// admitted 的判定照官方逐字（`:494-500`）：
//   - source.kind == 'user' 且 content 非空；
//   - source.kind == 'retry' 且 content 为空（空 prompt 续跑自己写下的那条消息）；
//   - 且必须在 append surface 上（replace 的改写不算 admit）。
func TestConversationContinuationAdmissionRules(t *testing.T) {
	cases := []struct {
		name    string
		message v4.Event
		want    bool
	}{
		{"user+非空", userMsgEvent(t, 1, "user", 1, "append"), true},
		{"retry+空", userMsgEvent(t, 1, "retry", 0, "append"), true},
		{"user+空", userMsgEvent(t, 1, "user", 0, "append"), false},
		{"retry+非空", userMsgEvent(t, 1, "retry", 1, "append"), false},
		{"system+非空", userMsgEvent(t, 1, "system", 1, "append"), false},
		{"user+非空但 replace", userMsgEvent(t, 1, "user", 1, "replace"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ending := appliedEnding(t,
				testEvent(t, "turn/start", 0, map[string]any{"turn": 1}),
				c.message,
				assistantAttemptEvent(t, 2),
				turnEndEvent(t, 3, map[string]any{"kind": "error", "error": map[string]any{"code": "TIMEOUT"}}),
			)
			got := ending != nil
			if got != c.want {
				t.Fatalf("admitted = %v，want %v（ending=%#v）", got, c.want, ending)
			}
		})
	}
}

// turn/start 把它清空；step/start 与 assistant/message 清掉 failedAttempt。
func TestConversationContinuationResets(t *testing.T) {
	events := []v4.Event{
		testEvent(t, "turn/start", 0, map[string]any{"turn": 1}),
		userMsgEvent(t, 1, "user", 1, "append"),
		assistantAttemptEvent(t, 2),
		turnEndEvent(t, 3, map[string]any{"kind": "error", "error": map[string]any{"code": "SERVER"}}),
		testEvent(t, "turn/start", 4, map[string]any{"turn": 2}),
	}
	if got := appliedEnding(t, events...); got != nil {
		t.Fatalf("turn/start 之后 continuation 必须清空，得到 %#v", got)
	}

	// 新的一步开始后 failedAttempt 被清掉 → 只有"最后一步失败"才可续。
	events = []v4.Event{
		testEvent(t, "turn/start", 0, map[string]any{"turn": 1}),
		userMsgEvent(t, 1, "user", 1, "append"),
		assistantAttemptEvent(t, 2),
		testEvent(t, "step/start", 3, map[string]any{"turn": 1, "step": 2}),
		turnEndEvent(t, 4, map[string]any{"kind": "error", "error": map[string]any{"code": "SERVER"}}),
	}
	if got := appliedEnding(t, events...); got != nil {
		t.Fatalf("step/start 之后 failedAttempt 已清，不该可续，得到 %#v", got)
	}
}

// transport 侧要复用同一台折叠：给一个不经过注册表的入口，避免两处各折一份。
func TestEvaluateContinuationMatchesProjection(t *testing.T) {
	events := []v4.Event{
		testEvent(t, "turn/start", 0, map[string]any{"turn": 1}),
		userMsgEvent(t, 1, "user", 1, "append"),
		assistantAttemptEvent(t, 2),
		turnEndEvent(t, 3, map[string]any{"kind": "error", "error": map[string]any{"code": "STREAM_CLOSED"}}),
	}
	ending, ok := EvaluateContinuation(events)
	if !ok || ending != 3 {
		t.Fatalf("EvaluateContinuation = (%d,%v)，want (3,true)", ending, ok)
	}
	if !IsContinuableFailure("RATE_LIMIT") || IsContinuableFailure("UNKNOWN") {
		t.Fatalf("可续错误码集判定与官方不一致")
	}
}
