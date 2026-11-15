package transport

// 官方 rc.2「空 content 续跑」的 produce 侧测试（`conversationContinuation` 的下游）。
//
// 官方语义（`dsh-api-session-controller/lib/index.js`）：
//   - `:4147` 只有**非空**却全空白的 content 才是 `gateway/bad-request`；
//   - `:4152` 空 content 的 user/message 的 `source.kind` 是 `retry`；
//   - `:891-904` 空 content + `mode:"queue"` 且「有可续失败轮 + nextTurn 空 +
//     nextStep 没有 user 来源消息」才放行，否则 `session/agent-busy` +
//     `details.reason = NO_CONTINUABLE_FAILURE_OR_PENDING_INPUT`。
//
// 这里走**真实 handler**（`handleSessionPrompt`），因为这三条都在它里面接线；
// 光测 B3 的纯判定函数（已有 `TestOfficialPromptContinuation*` 覆盖）证明不了入口。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// promptContinuationServer 造一台带官方会话数据源与续跑入口的 Server。
//
// 记下续跑提交的真实参数（sessionID / sourceKind / placement），
// 因为"接受"这件事的证据在**调用**里，不在响应里。
type promptContinuationCall struct {
	sessionID  string
	sourceKind string
	placement  string
}

func promptContinuationServer(t *testing.T, src *fakeOfficialSource, extra Options) (*Server, *[]promptContinuationCall) {
	t.Helper()
	calls := &[]promptContinuationCall{}
	extra.Source = src
	extra.PromptContinuation = func(sessionID, sourceKind, placement string) (PromptResult, error) {
		*calls = append(*calls, promptContinuationCall{sessionID: sessionID, sourceKind: sourceKind, placement: placement})
		return PromptResult{Accepted: true, TurnID: "turn-9"}, nil
	}
	return newTestServer(t, extra), calls
}

// promptFailedTurnEvents 造一段"上一轮因 code 失败"的会话。
func promptFailedTurnEvents(code string) []DurableEvent {
	raw := func(s string) json.RawMessage { return json.RawMessage(s) }
	events := []DurableEvent{
		{Type: "turn/start", Seq: 0, Time: 1000, Data: raw(`{"turn":1}`)},
		{Type: "step/start", Seq: 1, Time: 1001, Data: raw(`{"turn":1,"step":1}`)},
		{Type: "user/message", Seq: 2, Time: 1002, Data: raw(`{"content":[{"type":"text","text":"问"}],"source":{"kind":"user"},"surfaceOp":"append"}`)},
		{Type: "assistant/attempt", Seq: 3, Time: 1003, Data: raw(`{"turn":1,"step":1,"stream":[]}`)},
	}
	if code != "" {
		events = append(events, DurableEvent{Type: "turn/end", Seq: 4, Time: 1004,
			Data: json.RawMessage(`{"turn":1,"reason":{"kind":"error","error":{"code":"` + code + `"}}}`)})
	}
	return events
}

// TestPromptEmptyContentRejectedWhenNoContinuableFailure：没有可续失败轮时，
// 空 content + queue 必须回 `session/agent-busy` + `details.reason`，
// 而且**不能**把请求悄悄交给 runner。
func TestPromptEmptyContentRejectedWhenNoContinuableFailure(t *testing.T) {
	src := &fakeOfficialSource{events: promptFailedTurnEvents("CONTEXT_LENGTH_EXCEEDED"), logEnd: 4}
	s, calls := promptContinuationServer(t, src, Options{})

	rec := postOfficial(t, s.handleSessionPrompt, `{"sessionId":"s1","mode":"queue","content":[]}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d，want 409（%s）", rec.Code, rec.Body.String())
	}
	obj := errorObjectFromRecorder(t, rec)
	if obj["code"] != "session/agent-busy" {
		t.Fatalf("code = %v，want session/agent-busy", obj["code"])
	}
	if obj["retryable"] != true {
		t.Fatalf("agent-busy 必须可重试（等当前轮/队列消化完再发）：%v", obj)
	}
	details, ok := obj["details"].(map[string]any)
	if !ok {
		t.Fatalf("errors 里缺 details（官方 details.reason 的落点）：%v", obj)
	}
	if details["reason"] != OfficialPromptContinuationReason {
		t.Fatalf("details.reason = %v，want %s", details["reason"], OfficialPromptContinuationReason)
	}
	if len(*calls) != 0 {
		t.Fatalf("被拒的请求不许交给 runner：%+v", *calls)
	}
}

// TestPromptEmptyContentAcceptsContinuableFailure：有可续失败轮 + queue + 无排队项 →
// accepted，且提交给宿主的 `source.kind` 是官方 `retry`，落点是 queue（本仓 followup）。
func TestPromptEmptyContentAcceptsContinuableFailure(t *testing.T) {
	for _, code := range []string{"RATE_LIMIT", "SERVER", "TIMEOUT", "TRANSPORT", "EMPTY_RESPONSE", "STREAM_CLOSED"} {
		t.Run(code, func(t *testing.T) {
			src := &fakeOfficialSource{events: promptFailedTurnEvents(code), logEnd: 4}
			s, calls := promptContinuationServer(t, src, Options{})

			rec := postOfficial(t, s.handleSessionPrompt, `{"sessionId":"s1","mode":"queue","content":[]}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s 是可续码，status = %d（%s）", code, rec.Code, rec.Body.String())
			}
			result := unwrapResult(t, rec.Body.Bytes())
			if result["accepted"] != true {
				t.Fatalf("accepted = %v，want true", result["accepted"])
			}
			if len(*calls) != 1 {
				t.Fatalf("续跑必须交给宿主一次：%+v", *calls)
			}
			call := (*calls)[0]
			if call.sourceKind != "retry" {
				t.Fatalf("source.kind = %q，want retry（官方 :4152）", call.sourceKind)
			}
			if call.placement != string(PlacementFollowup) {
				t.Fatalf("官方 mode=queue 的落点 = %q，want %q", call.placement, PlacementFollowup)
			}
			if call.sessionID != "s1" {
				t.Fatalf("sessionID = %q", call.sessionID)
			}
		})
	}
}

// TestPromptEmptyContentRequiresQueueMode：官方只允许 `mode:"queue"` 续跑；
// steer/缺省 mode 一律 agent-busy（否则会伪造成"注入当前轮"）。
func TestPromptEmptyContentRequiresQueueMode(t *testing.T) {
	for _, body := range []string{
		`{"sessionId":"s1","mode":"steer","content":[]}`,
		`{"sessionId":"s1","content":[]}`,
	} {
		src := &fakeOfficialSource{events: promptFailedTurnEvents("RATE_LIMIT"), logEnd: 4}
		s, calls := promptContinuationServer(t, src, Options{})
		rec := postOfficial(t, s.handleSessionPrompt, body)
		if rec.Code != http.StatusConflict {
			t.Fatalf("%s：status = %d，want 409（%s）", body, rec.Code, rec.Body.String())
		}
		if len(*calls) != 0 {
			t.Fatalf("%s：非 queue 不许续跑：%+v", body, *calls)
		}
	}
}

// TestPromptEmptyContentBlockedByRealPendingQueue：`inbox.nextTurn` 的判据是
// promptService 的**真实**待发队列（LiveState.QueueIDs），写死 false 就过不了这条。
func TestPromptEmptyContentBlockedByRealPendingQueue(t *testing.T) {
	src := &fakeOfficialSource{
		events: promptFailedTurnEvents("RATE_LIMIT"), logEnd: 4,
		live: OfficialLiveState{Running: true, QueueIDs: []string{"turn-2"}},
	}
	s, calls := promptContinuationServer(t, src, Options{})
	rec := postOfficial(t, s.handleSessionPrompt, `{"sessionId":"s1","mode":"queue","content":[]}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("有排队项时必须 agent-busy：status = %d（%s）", rec.Code, rec.Body.String())
	}
	if len(*calls) != 0 {
		t.Fatalf("有排队项时不许续跑：%+v", *calls)
	}
}

// TestPromptEmptyContentBlockedByPendingNextStepUser：`inbox.nextStep` 里还有
// user 来源消息（steer 注入后未被下一个 step 消费）时必须 agent-busy。
func TestPromptEmptyContentBlockedByPendingNextStepUser(t *testing.T) {
	raw := func(s string) json.RawMessage { return json.RawMessage(s) }
	events := promptFailedTurnEvents("")
	// 跑着的一轮里，最后一步开始之后又注入了一条用户消息。
	events = append(events,
		DurableEvent{Type: "step/start", Seq: 4, Time: 1004, Data: raw(`{"turn":1,"step":2}`)},
		DurableEvent{Type: "user/message", Seq: 5, Time: 1005,
			Data: raw(`{"content":[{"type":"text","text":"顺带问一句"}],"source":{"kind":"user"},"surfaceOp":"append"}`)},
	)
	src := &fakeOfficialSource{events: events, logEnd: 5, live: OfficialLiveState{Running: true}}
	s, calls := promptContinuationServer(t, src, Options{})
	rec := postOfficial(t, s.handleSessionPrompt, `{"sessionId":"s1","mode":"queue","content":[]}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("nextStep 还有用户消息时必须 agent-busy：status = %d（%s）", rec.Code, rec.Body.String())
	}
	if len(*calls) != 0 {
		t.Fatalf("nextStep 非空时不许续跑：%+v", *calls)
	}
}

// TestPromptContentWhitespaceRejectedButEmptyAllowed：官方只对**非空却全空白**的
// content 回 400；空 content 走续跑判定（这里是没数据源时落回旧路径）。
func TestPromptContentWhitespaceRejectedButEmptyAllowed(t *testing.T) {
	src := &fakeOfficialSource{events: promptFailedTurnEvents("RATE_LIMIT"), logEnd: 4}
	extra := Options{Source: src, PromptBlocksPlacement: func(string, []ContentBlock, string) (PromptResult, error) {
		return PromptResult{Accepted: true, TurnID: "turn-1"}, nil
	}}
	s := newTestServer(t, extra)
	rec := postOfficial(t, s.handleSessionPrompt, `{"sessionId":"s1","mode":"queue","content":[{"type":"text","text":"   "}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("全空白 content 必须 400：status = %d（%s）", rec.Code, rec.Body.String())
	}
	if obj := errorObjectFromRecorder(t, rec); obj["code"] != "bad_request" {
		t.Fatalf("code = %v，want bad_request", obj["code"])
	}
}

// TestPromptOfficialModeMapsPlacement：官方 `mode` 只补 placement 缺省的情形。
func TestPromptOfficialModeMapsPlacement(t *testing.T) {
	cases := []struct {
		body      string
		placement string
	}{
		{`{"sessionId":"s1","mode":"queue","text":"hi"}`, string(PlacementFollowup)},
		{`{"sessionId":"s1","mode":"steer","text":"hi"}`, string(PlacementSteer)},
		{`{"sessionId":"s1","text":"hi"}`, string(PlacementTurn)},
		// 显式 placement 优先：canonical 客户端行为不变。
		{`{"sessionId":"s1","mode":"queue","placement":"now","text":"hi"}`, string(PlacementNow)},
	}
	for _, c := range cases {
		var got string
		extra := Options{Source: &fakeOfficialSource{}, PromptPlacement: func(_, _, placement string) (PromptResult, error) {
			got = placement
			return PromptResult{Accepted: true, TurnID: "turn-1"}, nil
		}}
		s := newTestServer(t, extra)
		rec := postOfficial(t, s.handleSessionPrompt, c.body)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s：status = %d（%s）", c.body, rec.Code, rec.Body.String())
		}
		if got != c.placement {
			t.Fatalf("%s：placement = %q，want %q", c.body, got, c.placement)
		}
	}
}

// TestPromptRejectsUnknownMode：官方 mode 值域只有 queue/steer，别的值必须 400
// （静默当 turn 会把"注入"伪造成"新轮"）。
func TestPromptRejectsUnknownMode(t *testing.T) {
	s := newTestServer(t, Options{Source: &fakeOfficialSource{},
		PromptPlacement: func(string, string, string) (PromptResult, error) {
			t.Fatal("非法 mode 不该走到 runner")
			return PromptResult{}, nil
		}})
	rec := postOfficial(t, s.handleSessionPrompt, `{"sessionId":"s1","mode":"later","text":"hi"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d，want 400（%s）", rec.Code, rec.Body.String())
	}
}

// errorObjectFromRecorder 解错误信封，把顶层 details 一起并进 error 对象方便断言
// （实现把 details 平铺在顶层：`error` 必须恰好四字段）。
func errorObjectFromRecorder(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	body := rec.Body.Bytes()
	var env struct {
		Error   map[string]any `json:"error"`
		Details map[string]any `json:"details"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("解错误信封: %v（%s）", err, body)
	}
	if env.Error == nil {
		t.Fatalf("不是错误信封：%s", body)
	}
	if env.Details != nil {
		env.Error["details"] = env.Details
	}
	return env.Error
}
