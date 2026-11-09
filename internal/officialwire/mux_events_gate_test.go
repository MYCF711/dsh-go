package officialwire

// `$events` 的**准入门**：官方把它与 `$events/result` 放在同一道门上。
//
// 官方 gateway 的两处代码是同一条规则：
//
//	openWireStream    → requireInteractions(peer, endpoint)   // lib/index.js:1015
//	dispatchRpc       → requireInteractions(peer, endpoint)   // lib/index.js:995
//	func requireInteractions(peer, endpoint) {
//	    if (peer.interactions !== "answer") throw gateway/interactions-forbidden  // :1484-1486
//	}
//
// 也就是说"能收到事件"与"能回答事件"在官方是**同一个判据**。少了 open 这一侧，
// 宿主会把 waterfall 发给一个**永远答不了**的 Peer：它一旦真的去答，`$events/result`
// 回 gateway/interactions-forbidden，而客户端 `answer()` 的调用方在 catch 里
// `failed.abort(error)` —— 整条 generation 被打死并重连。
//
// 这就是"事件先于 result 可见"：事件已经可见，而作答能力在同一时刻并不存在。
// 门必须开在 ready 之前，不能等到 result 才拒。

import (
	"testing"
	"time"
)

// TestMuxEventsRequiresAnswerInteraction 钉住：interactions=none 的 Peer 打不开 `$events`。
//
// 首帧必须是 error（不是 ready）：ready 一出现，客户端就会认为"事件流就绪"，
// 而它其实没有作答能力。
func TestMuxEventsRequiresAnswerInteraction(t *testing.T) {
	source := newFakeEventSource()
	_, ts, client := startWire(t, eventsConfig(source), nil)
	ws, _ := dialAuthorized(t, ts, client, InteractionsNone)

	ws.writeText(t, openFrame("ev", EndpointEvents, ""))
	frame := ws.readJSONFrame(t, 2*time.Second)
	if frame["type"] != FrameError {
		t.Fatalf("none 型 Peer 打开 $events 必须直接错误帧（不能先 ready）：%v", frame)
	}
	failure, ok := frame["error"].(map[string]any)
	if !ok {
		t.Fatalf("error 帧缺 error 对象：%v", frame)
	}
	if failure["code"] != CodeInteractionsForbidden {
		t.Fatalf("应是 %s（与官方 requireInteractions 同码），实际 %v", CodeInteractionsForbidden, failure)
	}

	// 流已经结束：之后不再有任何帧（尤其不许补发 ready）。
	if _, _, err := ws.readFrame(t, 500*time.Millisecond); err == nil {
		t.Fatalf("错误帧之后不该再下发任何帧")
	}

	// 事件源一次都没被订阅：拒绝发生在建流之前，不是"建了再丢弃"。
	source.mu.Lock()
	subscribed := len(source.subs)
	source.mu.Unlock()
	if subscribed != 0 {
		t.Fatalf("被拒绝的 Peer 不该订阅到事件源，实际订阅 %d 次", subscribed)
	}
}

// TestMuxEventsResultAndOpenShareOneGate 钉住两侧同码：同一个 none 型 Peer
// 既打不开 `$events`，`$events/result` 也回同一个 gateway/interactions-forbidden。
func TestMuxEventsResultAndOpenShareOneGate(t *testing.T) {
	source := newFakeEventSource()
	_, ts, client := startWire(t, eventsConfig(source), map[string]Binding{
		EndpointEventResult: eventsResultBinding(source),
	})
	connectAndCookies(t, ts, client, InteractionsNone)

	status, payload := postEnvelope(t, ts, client, EndpointEventResult,
		envelopeBody("r-1", EndpointEventResult,
			`{"clientId":"c1","eventId":"e1","outcome":{"kind":"next"}}`))
	failure := failureOf(t, decodeEnvelope(t, status, payload))
	if failure["code"] != CodeInteractionsForbidden {
		t.Fatalf("none 型 Peer 的 $events/result 应回 %s，实际 %v", CodeInteractionsForbidden, failure)
	}
}

// TestMuxEventsOpenRejectsNonEmptyArgs 钉住 `$events` 的空 args 契约。
//
// 官方 openRemoteEvents（lib/index.js:1024-1026）要求 payload 恰好一个键 `args`、
// 且 args 是**零键**对象；否则 gateway/arguments-invalid。容忍多余键等于接受另一套
// 方言：客户端以为订阅带了参数，宿主却按"无参数"跑，两边对同一帧的理解不同。
func TestMuxEventsOpenRejectsNonEmptyArgs(t *testing.T) {
	_, ts, client := startWire(t, testServerConfig(), nil)
	ws, _ := dialAuthorized(t, ts, client, InteractionsAnswer)

	ws.writeText(t, openFrame("ev", EndpointEvents, `{"sinceSeq":0}`))
	frame := ws.readJSONFrame(t, 2*time.Second)
	if frame["type"] != FrameError {
		t.Fatalf("非空 args 必须错误帧（不能 ready）：%v", frame)
	}
	failure, _ := frame["error"].(map[string]any)
	if failure["code"] != CodeArgumentsInvalid {
		t.Fatalf("应是 %s，实际 %v", CodeArgumentsInvalid, failure)
	}
}
