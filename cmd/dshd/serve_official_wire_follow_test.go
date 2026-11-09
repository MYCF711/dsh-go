package main

// `session.follow` 在**真实宿主**上的首帧/续传/取消验收。
//
// 为什么必须在这里测：`internal/transport` 的 follow 用例用的是注入的假数据源，
// 它们证明的是"帧机按 sinceSeq 切片"，证明不了"这个宿主真的能出 snapshot"。
// 真实宿主缺 header 来源时，follow 会回 `gateway/method-unavailable` —— 一个
// 单测全绿、官方客户端却永远拿不到会话的东西。所以这条用例走完整挂载：
// 真 transport + 真 officialwire + 真 v4 日志 + 真 mux 帧。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"dshgo/internal/session"
)

// followFrame 读一条 item 帧并返回它的 value（跳过 ping/pong）。
func followFrame(t *testing.T, ws *wireWS, timeout time.Duration) map[string]any {
	t.Helper()
	frame := ws.readJSON(t, timeout)
	if frame["type"] != "item" {
		t.Fatalf("应是 item 帧，实际 %v", frame)
	}
	value, ok := frame["value"].(map[string]any)
	if !ok {
		t.Fatalf("item 的 value 不是对象：%v", frame)
	}
	return value
}

// dialWireMuxRetry 在同一个 Peer 上重新握手。
//
// 一个 Peer 同时只允许一条 API socket（官方 409），上一条刚关时服务端可能还没
// Detach —— 重连窗口里的 409 是**协议正确**，不是故障，所以这里重试。
func dialWireMuxRetry(t *testing.T, base string, cookies []*http.Cookie) *wireWS {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		ws, response := dialWireMux(t, base, cookies)
		if response.StatusCode == http.StatusSwitchingProtocols {
			return ws
		}
		ws.Close()
		if time.Now().After(deadline) {
			t.Fatalf("重连等不到 101，最后一次 %d", response.StatusCode)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// openFollow 在一条 mux 连接上 open session/follow。
func openFollow(t *testing.T, h *officialWireHarness, ws *wireWS, streamID, args string) {
	t.Helper()
	frame := fmt.Sprintf(`{"type":"open","streamId":%q,"endpoint":"session/follow","payload":{"args":%s}}`, streamID, args)
	ws.writeText(t, frame)
}

// appendDurable 往真实 v4 日志追加一条事件（等价于宿主写路径）。
func appendDurable(t *testing.T, h *officialWireHarness, fixture officialTestSession, typ, data string) int64 {
	t.Helper()
	lg, err := session.OpenSession(h.home, fixture.cwd, fixture.id)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if err := lg.Append(typ, json.RawMessage(data)); err != nil {
		t.Fatalf("Append %s: %v", typ, err)
	}
	seq := lg.Seq() - 1
	if err := lg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return seq
}

// TestOfficialWireSessionFollowRealStream 钉住 follow 的三件事：
// ① 首帧是真 snapshot（header 来自真实 v4 头，不是零值）；② 之后真跟随新事件；
// ③ 续传（sinceSeq=cursor）不重放历史。
func TestOfficialWireSessionFollowRealStream(t *testing.T) {
	h := newOfficialWireHarness(t)
	fixture := newOfficialTestSession(t, h.home, "follow-real")

	ws, response := dialWireMux(t, h.mounted.URL, h.officialCookies(t))
	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("mux 升级应 101，实际 %d", response.StatusCode)
	}
	defer ws.Close()

	openFollow(t, h, ws, "f", fmt.Sprintf(`{"address":{"kind":"session","sessionId":%q}}`, fixture.id))
	snapshot := followFrame(t, ws, 3*time.Second)
	if snapshot["type"] != "snapshot" {
		t.Fatalf("首帧必须是 snapshot，实际 %v", snapshot)
	}
	header, ok := snapshot["header"].(map[string]any)
	if !ok {
		t.Fatalf("snapshot 缺 header：%v", snapshot)
	}
	if header["id"] != fixture.id {
		t.Fatalf("header.id 必须是真实会话 id %q：%v", fixture.id, header)
	}
	if header["version"] != float64(4) {
		t.Fatalf("header.version 必须是真实 v4 版本号 4：%v", header)
	}
	if createdAt, _ := header["createdAt"].(float64); createdAt <= 0 {
		t.Fatalf("header.createdAt 必须来自真实 header（不是编的 0）：%v", header)
	}
	if cursor, _ := snapshot["cursor"].(float64); int64(cursor) != fixture.lastSeq {
		t.Fatalf("cursor 应是日志末端 %d：%v", fixture.lastSeq, snapshot["cursor"])
	}
	if records, _ := snapshot["records"].([]any); len(records) == 0 {
		t.Fatalf("snapshot.records 不该为空（真实日志有 9 条事件）：%v", snapshot)
	}
	if more, _ := snapshot["hasMore"].(bool); more {
		t.Fatalf("整份日志都在一页内时 hasMore 必须是 false：%v", snapshot)
	}

	// ② 真跟随：追加一条 durable 事件，必须作为 event 帧推下来。
	liveSeq := appendDurable(t, h, fixture, "assistant/message",
		`{"content":[{"type":"text","text":"follow-live"}]}`)
	live := followFrame(t, ws, 3*time.Second)
	if live["type"] != "event" {
		t.Fatalf("实时帧必须是 event，实际 %v", live)
	}
	event, _ := live["event"].(map[string]any)
	if seq, ok := event["seq"].(float64); !ok || int64(seq) != liveSeq {
		t.Fatalf("实时帧 seq 应是 %d：%v", liveSeq, event)
	}

	// ③ 续传：拿 cursor 重连，不重放历史，也不丢后续。
	ws.Close()
	resumedSocket := dialWireMuxRetry(t, h.mounted.URL, h.officialCookies(t))
	defer resumedSocket.Close()
	resumeArgs := fmt.Sprintf(`{"address":{"kind":"session","sessionId":%q},"sinceSeq":%d}`, fixture.id, liveSeq)
	openFollow(t, h, resumedSocket, "r", resumeArgs)
	resumed := followFrame(t, resumedSocket, 3*time.Second)
	if resumed["type"] != "snapshot" {
		t.Fatalf("续传首帧仍是 snapshot：%v", resumed)
	}
	if cursor, _ := resumed["cursor"].(float64); int64(cursor) != liveSeq {
		t.Fatalf("续传 cursor 必须钉在 %d：%v", liveSeq, resumed["cursor"])
	}
	if records, _ := resumed["records"].([]any); len(records) != 0 {
		t.Fatalf("续传不许重放历史（records 必须为空）：%v", resumed["records"])
	}
	nextSeq := appendDurable(t, h, fixture, "assistant/message",
		`{"content":[{"type":"text","text":"follow-after-resume"}]}`)
	first := followFrame(t, resumedSocket, 3*time.Second)
	ev, _ := first["event"].(map[string]any)
	if seq, ok := ev["seq"].(float64); !ok || int64(seq) != nextSeq {
		t.Fatalf("续传后第一帧应是 %d（不重放、不丢）：%v", nextSeq, first)
	}
}

// TestOfficialWireSessionFollowIsolatedPerSession 钉住 A/B：两条会话各跟各的。
func TestOfficialWireSessionFollowIsolatedPerSession(t *testing.T) {
	h := newOfficialWireHarness(t)
	a := newOfficialTestSession(t, h.home, "follow-a")
	b := newOfficialTestSession(t, h.home, "follow-b")

	wsA := dialWireMuxRetry(t, h.mounted.URL, h.officialCookies(t))
	defer wsA.Close()
	openFollow(t, h, wsA, "a", fmt.Sprintf(`{"address":{"kind":"session","sessionId":%q}}`, a.id))
	snapA := followFrame(t, wsA, 3*time.Second)
	headerA, _ := snapA["header"].(map[string]any)
	if headerA["id"] != a.id {
		t.Fatalf("A 流的 header.id 必须是 %q：%v", a.id, headerA)
	}

	appendDurable(t, h, b, "assistant/message", `{"content":[{"type":"text","text":"only-b"}]}`)
	// A 流不许收到 B 的事件：等到超时就是结论（读超时被 readRawFrame 报成 error）。
	if _, _, err := readRawFrame(wsA, 700*time.Millisecond); err == nil {
		t.Fatalf("A 流收到了别的会话的事件（A/B 串流）")
	}
}

// TestOfficialWireSessionFollowCancelStops 钉住取消：cancel 之后不再推帧。
func TestOfficialWireSessionFollowCancelStops(t *testing.T) {
	h := newOfficialWireHarness(t)
	fixture := newOfficialTestSession(t, h.home, "follow-cancel")

	ws := dialWireMuxRetry(t, h.mounted.URL, h.officialCookies(t))
	defer ws.Close()
	openFollow(t, h, ws, "c", fmt.Sprintf(`{"address":{"kind":"session","sessionId":%q}}`, fixture.id))
	_ = followFrame(t, ws, 3*time.Second)
	ws.writeText(t, `{"type":"cancel","streamId":"c"}`)
	time.Sleep(50 * time.Millisecond)
	appendDurable(t, h, fixture, "assistant/message", `{"content":[{"type":"text","text":"after-cancel"}]}`)
	if _, _, err := readRawFrame(ws, 700*time.Millisecond); err == nil {
		t.Fatalf("cancel 之后仍在推帧")
	}
}
