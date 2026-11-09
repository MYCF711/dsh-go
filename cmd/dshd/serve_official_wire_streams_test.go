package main

// 官方 4 条会话流 + 工具结果流在**真实宿主**上的验收。
//
// 与 serve_official_wire_follow_test.go 同一套夹具（真 v4 日志 + 真挂载 + 真 mux 帧），
// 这里覆盖 follow 之外的几条：observeEvent / observeProjections / observeState /
// toolResults.watch。它们各自只证明"这个宿主真的能出这类帧"，不证明官方客户端
// 怎么消费（那是 official-client E2E 的事）。

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// tryReadJSON 读一帧；超时/EOF 返回 ok=false（不 t.Fatalf）。
func tryReadJSON(w *wireWS, timeout time.Duration) (map[string]any, bool) {
	_ = w.conn.SetReadDeadline(time.Now().Add(timeout))
	defer w.conn.SetReadDeadline(time.Time{})
	var header [2]byte
	if _, err := io.ReadFull(w.br, header[:]); err != nil {
		return nil, false
	}
	op := header[0] & 0x0f
	length := uint64(header[1] & 0x7f)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(w.br, ext[:]); err != nil {
			return nil, false
		}
		length = uint64(ext[0])<<8 | uint64(ext[1])
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(w.br, ext[:]); err != nil {
			return nil, false
		}
		length = 0
		for _, b := range ext {
			length = length<<8 | uint64(b)
		}
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(w.br, payload); err != nil {
		return nil, false
	}
	if op != 0x1 {
		return map[string]any{"_op": op}, true
	}
	var frame map[string]any
	if err := json.Unmarshal(payload, &frame); err != nil {
		return map[string]any{"_raw": string(payload)}, true
	}
	return frame, true
}

// openMuxStream 新开一条 mux 连接并 open 指定 endpoint。
func openMuxStream(t *testing.T, h *officialWireHarness, streamID, endpoint, args string) *wireWS {
	t.Helper()
	ws := dialWireMuxRetry(t, h.mounted.URL, h.officialCookies(t))
	t.Cleanup(ws.Close)
	frame := fmt.Sprintf(`{"type":"open","streamId":%q,"endpoint":%q,"payload":{"args":%s}}`, streamID, endpoint, args)
	ws.writeText(t, frame)
	return ws
}

// TestOfficialWireSessionFollowIncludesSeqZero 是"seq 0 永远取不到"那个坑在
// **真实宿主**上的回归：sinceSeq=-1 表示"我什么都没拿"，第一条实时帧必须是 seq 0。
func TestOfficialWireSessionFollowIncludesSeqZero(t *testing.T) {
	h := newOfficialWireHarness(t)
	fixture := newOfficialTestSession(t, h.home, "follow-seq0")

	// sinceSeq=-1 + throughSeq=-1：切点钉在 -1，records 必须为空，然后从 seq 0 开始。
	args := fmt.Sprintf(`{"address":{"kind":"session","sessionId":%q},"sinceSeq":-1}`, fixture.id)
	ws := openMuxStream(t, h, "z", "session/follow", args)
	snapshot := followFrame(t, ws, 3*time.Second)
	if snapshot["type"] != "snapshot" {
		t.Fatalf("首帧必须是 snapshot：%v", snapshot)
	}
	if cursor, _ := snapshot["cursor"].(float64); int64(cursor) != -1 {
		t.Fatalf("sinceSeq=-1 时 cursor 必须是 -1：%v", snapshot["cursor"])
	}
	if records, _ := snapshot["records"].([]any); len(records) != 0 {
		t.Fatalf("sinceSeq=-1 表示不重放历史，records 必须为空：%v", snapshot["records"])
	}
	first := followFrame(t, ws, 3*time.Second)
	event, _ := first["event"].(map[string]any)
	if seq, ok := event["seq"].(float64); !ok || seq != 0 {
		t.Fatalf("sinceSeq=-1 的第一条实时帧必须是 seq 0：%v", first)
	}
}

// TestOfficialWireSessionObserveStreams 钉住三条 observe 流各出真帧。
func TestOfficialWireSessionObserveStreams(t *testing.T) {
	t.Run("observeEvent", func(t *testing.T) {
		h := newOfficialWireHarness(t)
		fixture := newOfficialTestSession(t, h.home, "observe-event")
		ws := openMuxStream(t, h, "oe", "session/observeEvent", fmt.Sprintf(`{"sessionId":%q}`, fixture.id))
		if ready := followFrame(t, ws, 3*time.Second); ready["type"] != "ready" {
			t.Fatalf("observeEvent 首帧必须是 ready（不重放历史）：%v", ready)
		}
		liveSeq := appendDurable(t, h, fixture, "assistant/message",
			`{"content":[{"type":"text","text":"observed"}]}`)
		live := followFrame(t, ws, 3*time.Second)
		event, _ := live["event"].(map[string]any)
		if live["type"] != "event" || int64(event["seq"].(float64)) != liveSeq {
			t.Fatalf("observeEvent 必须推真实新事件（seq=%d）：%v", liveSeq, live)
		}
	})

	t.Run("observeProjections", func(t *testing.T) {
		h := newOfficialWireHarness(t)
		fixture := newOfficialTestSession(t, h.home, "observe-projections")
		ws := openMuxStream(t, h, "op", "session/observeProjections", fmt.Sprintf(`{"sessionId":%q}`, fixture.id))
		if ready := followFrame(t, ws, 3*time.Second); ready["type"] != "ready" {
			t.Fatalf("observeProjections 首帧必须是 ready：%v", ready)
		}
		liveSeq := appendDurable(t, h, fixture, "session/title", `{"title":"真实标题"}`)
		frame := followFrame(t, ws, 3*time.Second)
		if frame["type"] != "projection" {
			t.Fatalf("observeProjections 应出 projection 帧：%v", frame)
		}
		if int64(frame["seq"].(float64)) != liveSeq {
			t.Fatalf("projection.seq 应是事件 seq %d：%v", liveSeq, frame)
		}
		changes, _ := frame["changes"].([]any)
		if len(changes) == 0 {
			t.Fatalf("title 事件必须改 title 键（空数组只表示「这条没改可见键」）：%v", frame)
		}
	})

	t.Run("observeState", func(t *testing.T) {
		h := newOfficialWireHarness(t)
		fixture := newOfficialTestSession(t, h.home, "observe-state")
		ws := openMuxStream(t, h, "os", "session/observeState", fmt.Sprintf(`{"sessionId":%q}`, fixture.id))
		state := followFrame(t, ws, 3*time.Second)
		// 官方 ObserveState 的帧没有 type 判别式（是状态对象本身）。
		if _, has := state["cursor"]; !has {
			t.Fatalf("observeState 首帧必须是真实状态对象（带 cursor）：%v", state)
		}
		if _, has := state["active"]; !has {
			t.Fatalf("observeState 必须带 active：%v", state)
		}
	})
}

// TestOfficialWireToolResultsWatch 钉住 toolResults.watch 是**真读 durable 结果**：
// 命中 tool/result 事件回 available:true + 真实 value；非 tool/result 回 available:false。
func TestOfficialWireToolResultsWatch(t *testing.T) {
	h := newOfficialWireHarness(t)
	fixture := newOfficialTestSession(t, h.home, "tool-watch")
	// 真实 agent API（toolResults.watch 只依赖 home + sessionSource，不需要 runner 依赖）。
	core := newAgentOfficialAPICore(agentOfficialDeps{
		home: h.home,
		src:  newSessionSource(h.home, ""),
	})
	h.srv.AttachOfficialAgentAPI(core.bind())

	ws := openMuxStream(t, h, "tr", "toolResults/watch",
		fmt.Sprintf(`{"sessionId":%q,"seq":%d}`, fixture.id, fixture.toolSeq))
	first := followFrame(t, ws, 3*time.Second)
	if available, _ := first["available"].(bool); !available {
		t.Fatalf("seq=%d 是真实 tool/result，必须 available:true：%v", fixture.toolSeq, first)
	}
	value, ok := first["value"].(map[string]any)
	if !ok {
		t.Fatalf("available:true 必须带 value：%v", first)
	}
	if int64(value["seq"].(float64)) != fixture.toolSeq {
		t.Fatalf("value.seq 应是 %d：%v", fixture.toolSeq, value)
	}
	if value["callId"] != "call-1" {
		t.Fatalf("value.callId 必须是真实 call-1：%v", value)
	}

	// 非 tool/result 的 seq → available:false（官方 cold read 语义，不是错误）。
	// 同一个 Peer 只允许一条 socket，所以先关掉上一条再重连。
	ws.Close()
	ws2 := openMuxStream(t, h, "tr2", "toolResults/watch",
		fmt.Sprintf(`{"sessionId":%q,"seq":%d}`, fixture.id, fixture.turnStartSeq))
	second := followFrame(t, ws2, 3*time.Second)
	if available, _ := second["available"].(bool); available {
		t.Fatalf("seq=%d 不是 tool/result，必须 available:false：%v", fixture.turnStartSeq, second)
	}
	if _, has := second["value"]; has {
		t.Fatalf("available:false 不许带 value：%v", second)
	}
}

// TestOfficialWireWorkspaceDirectoryChanges 钉住目录目标不是"假 ready"：
// ready 之后建一个直接子项，必须真的收到 change 帧（真实文件系统 + 真实版本令牌）。
func TestOfficialWireWorkspaceDirectoryChanges(t *testing.T) {
	h := newOfficialWireHarness(t)
	fixture := newOfficialTestSession(t, h.home, "dir-watch")
	dir := filepath.Join(fixture.cwd, "watched")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	args := fmt.Sprintf(`{"workspaceFileScopeId":%q,"path":"watched"}`, fixture.id)
	ws := openMuxStream(t, h, "d", "workspaceFiles/changes", args)
	if ready := followFrame(t, ws, 3*time.Second); ready["kind"] != "ready" {
		t.Fatalf("首帧必须是 ready：%v", ready)
	}
	if err := os.WriteFile(filepath.Join(dir, "child.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("目录里建了子项，但 4s 内没有 change 帧")
		}
		frame, ok := tryReadJSON(ws, 800*time.Millisecond)
		if !ok {
			continue
		}
		if frame["type"] != "item" {
			continue
		}
		value, _ := frame["value"].(map[string]any)
		if value["kind"] != "change" {
			continue
		}
		change, _ := value["change"].(map[string]any)
		if change["version"] == nil || change["version"] == "" {
			t.Fatalf("change 必须带版本令牌：%v", value)
		}
		if change["absent"] == true {
			t.Fatalf("目录还在，不该报 absent：%v", value)
		}
		return
	}
}
