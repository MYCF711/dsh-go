package transport

// session/control 的官方契约测试。
//
// 官方依据：dsh-api-session-controller/lib/typert.host.js:1970-1985（零参数
// mode:'stream'）、lib/types/control.js:30-64（baseline → projection），以及
// typert.host.js:3587-3592/3695-3696/3735-3736（逐字段 schema）。
// control 不是 ready 流，也没有 follow 的 cursor/records/hasMore；重连重新读
// 真实 baseline，之后只交付新 projection，不重放旧 projection。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func controlTestServer(t *testing.T, src *mutableOfficialSource, ids ...string) *Server {
	t.Helper()
	return newTestServer(t, Options{
		Source:   src,
		Journals: NewJournals(nil),
		List: func() ([]SessionSummary, error) {
			out := make([]SessionSummary, 0, len(ids))
			for _, id := range ids {
				out = append(out, SessionSummary{ID: id})
			}
			return out, nil
		},
		Load: func(sessionID string, _ int) (LoadResult, error) {
			return LoadResult{CWD: `D:\work\` + sessionID, Permission: "workspace-write"}, nil
		},
	})
}

func openControlForTest(t *testing.T, srv *Server) OfficialFrameStream {
	t.Helper()
	stream, fault := srv.OpenOfficialFrameStream(context.Background(), "session/control", json.RawMessage(`{}`))
	if fault != nil {
		t.Fatalf("打开 session/control 失败：%v", fault)
	}
	t.Cleanup(stream.Close)
	return stream
}

func nextControlForTest(t *testing.T, stream OfficialFrameStream) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	raw, done, fault := stream.Next(ctx)
	if fault != nil || done {
		t.Fatalf("读取 control 帧失败：done=%v fault=%v", done, fault)
	}
	var frame map[string]any
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("control 帧不是 JSON：%v；raw=%s", err, raw)
	}
	return frame
}

func TestOfficialControlStreamBaselineThenProjection(t *testing.T) {
	src := newMutableOfficialSource([]DurableEvent{
		eventAt(0, "turn/start", `{"turn":1}`),
		eventAt(1, "user/message", `{"content":[{"type":"text","text":"hello"}],"source":{"kind":"user"}}`),
		eventAt(2, "session/title", `{"title":"真实标题"}`),
	})
	srv := controlTestServer(t, src, "s1")
	if !IsOfficialStreamMethod("session/control") || !IsOfficialStreamMethod("session.control") {
		t.Fatal("session/control 与归一化后的 session.control 都必须被识别为官方流")
	}

	stream := openControlForTest(t, srv)
	baseline := nextControlForTest(t, stream)
	if baseline["type"] != "baseline" {
		t.Fatalf("首帧必须是 baseline，不是 ready/假流：%v", baseline)
	}
	for _, forbidden := range []string{"cursor", "records", "hasMore"} {
		if _, exists := baseline[forbidden]; exists {
			t.Fatalf("control baseline 不应带 follow 字段 %s：%v", forbidden, baseline)
		}
	}
	value, _ := baseline["value"].(map[string]any)
	projections, _ := value["projections"].(map[string]any)
	block, _ := projections["s1"].(map[string]any)
	if block["asOfSeq"] != float64(2) {
		t.Fatalf("baseline 水位必须来自真实 durable logEnd=2：%v", block)
	}
	values, _ := block["values"].(map[string]any)
	if values["title"] != "真实标题" || values["workingDirectory"] != `D:\work\s1` {
		t.Fatalf("baseline values 必须来自真实事件与 load 投影：%v", values)
	}
	metadata, _ := values["sessionListMetadata"].(map[string]any)
	if metadata["blank"] != false || metadata["lastPromptAt"] != float64(1001) {
		t.Fatalf("turn/user 必须折叠成真实 sessionListMetadata：%v", metadata)
	}
	permissions, _ := values["permissions"].(map[string]any)
	if permissions["currentValue"] != "workspace-write" {
		t.Fatalf("permissions 必须来自真实 load：%v", permissions)
	}

	src.append(eventAt(3, "session/title", `{"title":"更新标题"}`))
	update := nextControlForTest(t, stream)
	if update["type"] != "projection" || update["sessionId"] != "s1" || update["seq"] != float64(3) {
		t.Fatalf("后续帧必须是该会话新 durable seq 的 projection：%v", update)
	}
	changes, _ := update["changes"].([]any)
	if len(changes) != 1 {
		t.Fatalf("标题事件只应产生一个 change：%v", changes)
	}
	change, _ := changes[0].(map[string]any)
	if change["key"] != "title" || change["val"] != "更新标题" {
		t.Fatalf("projection change 形状或真值不对：%v", change)
	}
}

func TestOfficialControlReconnectReadsBaselineWithoutReplay(t *testing.T) {
	src := newMutableOfficialSource([]DurableEvent{
		eventAt(0, "turn/start", `{"turn":1}`),
		eventAt(1, "session/title", `{"title":"旧标题"}`),
	})
	srv := controlTestServer(t, src, "s1")
	first := openControlForTest(t, srv)
	_ = nextControlForTest(t, first)
	src.append(eventAt(2, "session/title", `{"title":"断线前已收"}`))
	_ = nextControlForTest(t, first)

	second := openControlForTest(t, srv)
	baseline := nextControlForTest(t, second)
	value, _ := baseline["value"].(map[string]any)
	projections, _ := value["projections"].(map[string]any)
	block, _ := projections["s1"].(map[string]any)
	values, _ := block["values"].(map[string]any)
	if block["asOfSeq"] != float64(2) || values["title"] != "断线前已收" {
		t.Fatalf("重连必须重读最新真实 baseline：%v", block)
	}

	// 没有新事件时 Next 只等取消，不得把 seq=2 的旧 projection 再发一遍。
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Millisecond)
	raw, done, fault := second.Next(ctx)
	cancel()
	if fault != nil || !done || raw != nil {
		t.Fatalf("无新事件时应等待直至取消，不得重放/伪造：raw=%s done=%v fault=%v", raw, done, fault)
	}

	src.append(eventAt(3, "user/message", `{"content":[],"source":{"kind":"user"}}`))
	update := nextControlForTest(t, second)
	if update["seq"] != float64(3) {
		t.Fatalf("重连后只应交付新事件 seq=3：%v", update)
	}
}

func TestOfficialControlBaselineIncludesListedSessions(t *testing.T) {
	src := newMutableOfficialSource([]DurableEvent{eventAt(0, "turn/start", `{"turn":1}`)})
	srv := controlTestServer(t, src, "s1", "s2")
	stream := openControlForTest(t, srv)
	baseline := nextControlForTest(t, stream)
	value, _ := baseline["value"].(map[string]any)
	projections, _ := value["projections"].(map[string]any)
	if len(projections) != 2 || projections["s1"] == nil || projections["s2"] == nil {
		t.Fatalf("baseline 必须覆盖真实 List 返回的每个会话：%v", projections)
	}
}

func TestOfficialControlDoesNotInventBaselineWithoutInventory(t *testing.T) {
	src := newMutableOfficialSource(nil)
	srv := newTestServer(t, Options{Source: src, Journals: NewJournals(nil)})
	_, fault := srv.OpenOfficialFrameStream(context.Background(), "session/control", json.RawMessage(`{}`))
	if fault == nil || fault.Status != 501 || !strings.Contains(fault.Message, "会话清单") {
		t.Fatalf("没有真实会话清单时必须明确未接线，不能伪造空 baseline：%v", fault)
	}
}

func TestOfficialControlRejectsUnknownArguments(t *testing.T) {
	src := newMutableOfficialSource(nil)
	srv := controlTestServer(t, src)
	_, fault := srv.OpenOfficialFrameStream(context.Background(), "session/control", json.RawMessage(`{"sinceSeq":-1}`))
	if fault == nil || fault.Status != 400 {
		t.Fatalf("官方 control 是零参数流；未知字段必须在开流前 400：%v", fault)
	}
}
