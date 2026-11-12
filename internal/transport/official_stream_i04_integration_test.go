package transport

// I04 集成接线测试（integrator 亲自补）：
//
//  1. 三处 projections baseline **同源**：HTTP `session.projections`、
//     `session/follow` 的 snapshot.projections、`session/control` 的 baseline
//     在**同一 fixture** 上键集合完全一致（T04 WIRING §3 的验收项）。
//  2. observeProjections 的增量帧 key 与 HTTP values 键同名（todos / subagent /
//     subagentTiming）。
//  3. `userQuestions/attachWait` 的 **mux 流**：一帧 {remainingMs} 后持有 claim，
//     关流只释放 claim、不结算；wait 已结束 = 零帧结束；HTTP unary L1 仍然可用
//     （IsOfficialMuxStreamMethod 与 IsOfficialStreamMethod 分开的原因）。
//
// 这些断言钉的是"接线后的跨面一致性"，不是 T04 的单项实现本身。

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"
)

// projectionHostSource 在 mutableOfficialSource（OfficialSessionHost）上补齐
// OfficialProjectionHost 的三个键，用来在 fixture 里凑出 HTTP 的 10 键 baseline。
type projectionHostSource struct {
	*mutableOfficialSource
	preset string
}

func (p *projectionHostSource) ModelSelection(string) (*OfficialModelSelection, *OfficialModelSelection, error) {
	return &OfficialModelSelection{Provider: "gateway", Model: "deepseek-chat"}, nil, nil
}

func (p *projectionHostSource) AgentPreset(string) (*string, error) { return &p.preset, nil }

func (p *projectionHostSource) SubagentCatalog(string) ([]OfficialSubagentCatalogEntry, error) {
	return []OfficialSubagentCatalogEntry{{ID: "child-1", Mode: "continuable", Label: ""}}, nil
}

// i04ProjectionFixture 覆盖 HTTP projections 的 10 个键：
// 事件可推的（metadata/title/todos/subagent/subagentTiming）+ load 两键 +
// 宿主三键（modelSelection/agentPreset/subagentCatalog）。
func i04ProjectionFixture() []DurableEvent {
	return []DurableEvent{
		eventAt(0, "turn/start", `{"turn":1}`),
		eventAt(1, "user/message", `{"content":[{"type":"text","text":"hi"}],"source":{"kind":"user"}}`),
		eventAt(2, "session/title", `{"title":"I04 标题"}`),
		eventAt(3, "tool/call", `{"name":"todo_write","arguments":"{\"todos\":[{\"content\":\"run gate\",\"status\":\"pending\"}]}"}`),
		eventAt(4, "subagent/spawn", `{"childSessionId":"s1","parentSessionId":"p0"}`),
		eventAt(5, "turn/start", `{"turn":2}`),
		eventAt(6, "turn/end", `{"turn":1,"reason":{"kind":"completed"}}`),
	}
}

func i04ProjectionServer(t *testing.T) *Server {
	t.Helper()
	src := &projectionHostSource{mutableOfficialSource: newMutableOfficialSource(i04ProjectionFixture()), preset: "default"}
	return newTestServer(t, Options{
		Source:   src,
		Journals: NewJournals(nil),
		List: func() ([]SessionSummary, error) {
			return []SessionSummary{{ID: "s1"}}, nil
		},
		Load: func(sessionID string, _ int) (LoadResult, error) {
			return LoadResult{CWD: `D:\work\s1`, Permission: "workspace-write"}, nil
		},
	})
}

func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sameKeys(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// TestI04ProjectionsBaselineIsSharedAcrossThreeFaces 是 WIRING §3 的交叉验收：
// 同一会话、同一 fixture，HTTP / follow / control 三处 baseline 的 values 键一致。
func TestI04ProjectionsBaselineIsSharedAcrossThreeFaces(t *testing.T) {
	srv := i04ProjectionServer(t)
	ctx := context.Background()

	// HTTP session.projections。
	rec := postOfficial(t, srv.handleSessionProjections, `{"sessionId":"s1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("session.projections status=%d body=%s", rec.Code, rec.Body.String())
	}
	var httpResult struct {
		Result OfficialProjections `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &httpResult); err != nil {
		t.Fatalf("session.projections 结果不是 JSON：%v body=%s", err, rec.Body.String())
	}
	httpKeys := sortedKeys(httpResult.Result.Values)

	// follow 的 snapshot.projections。
	follow, fault := srv.OpenOfficialFrameStream(ctx, "session/follow",
		json.RawMessage(`{"address":{"kind":"session","sessionId":"s1"}}`))
	if fault != nil {
		t.Fatalf("打开 session/follow 失败：%v", fault)
	}
	defer follow.Close()
	followRaw, done, ffault := follow.Next(ctx)
	if ffault != nil || done {
		t.Fatalf("follow 首帧失败：done=%v fault=%v", done, ffault)
	}
	var snapshot struct {
		Type        string              `json:"type"`
		Projections OfficialProjections `json:"projections"`
	}
	if err := json.Unmarshal(followRaw, &snapshot); err != nil {
		t.Fatalf("follow snapshot 不是 JSON：%v", err)
	}
	if snapshot.Type != "snapshot" {
		t.Fatalf("follow 首帧必须是 snapshot：%s", followRaw)
	}
	followKeys := sortedKeys(snapshot.Projections.Values)

	// control 的 baseline.projections["s1"]。
	control, fault := srv.OpenOfficialFrameStream(ctx, "session/control", json.RawMessage(`{}`))
	if fault != nil {
		t.Fatalf("打开 session/control 失败：%v", fault)
	}
	defer control.Close()
	controlRaw, done, cfault := control.Next(ctx)
	if cfault != nil || done {
		t.Fatalf("control 首帧失败：done=%v fault=%v", done, cfault)
	}
	var baseline struct {
		Type  string `json:"type"`
		Value struct {
			Projections map[string]OfficialProjections `json:"projections"`
		} `json:"value"`
	}
	if err := json.Unmarshal(controlRaw, &baseline); err != nil {
		t.Fatalf("control baseline 不是 JSON：%v", err)
	}
	controlBlock, ok := baseline.Value.Projections["s1"]
	if !ok {
		t.Fatalf("control baseline 缺 s1：%s", controlRaw)
	}
	controlKeys := sortedKeys(controlBlock.Values)

	want := []string{
		"agentPreset", "contextPressure", "modelSelection", "permissions", "sessionListMetadata",
		"sessionStats", "subagent", "subagentCatalog", "subagentTiming", "title", "todos", "workingDirectory",
	}
	if !sameKeys(httpKeys, want) {
		t.Fatalf("HTTP values 键 = %v，want %v", httpKeys, want)
	}
	if !sameKeys(followKeys, httpKeys) {
		t.Fatalf("follow baseline 键 = %v，与 HTTP %v 不一致", followKeys, httpKeys)
	}
	if !sameKeys(controlKeys, httpKeys) {
		t.Fatalf("control baseline 键 = %v，与 HTTP %v 不一致", controlKeys, httpKeys)
	}
	if httpResult.Result.Values["todos"] == nil {
		t.Fatalf("todos 应来自最后一次 todo_write：%v", httpResult.Result.Values["todos"])
	}
	if httpResult.Result.Values["subagent"] == nil || httpResult.Result.Values["subagentTiming"] == nil {
		t.Fatalf("subagent/subagentTiming 应来自本会话自己的 spawn：%v", httpResult.Result.Values)
	}
}

// TestI04ObserveProjectionsDeltaKeysMatchHTTP 钉住增量帧的 key 与 HTTP 同名：
// todos（整份替换）、subagent、subagentTiming 都要能作为变化帧发出。
func TestI04ObserveProjectionsDeltaKeysMatchHTTP(t *testing.T) {
	srv := i04ProjectionServer(t)
	ctx := context.Background()
	stream, fault := srv.OpenOfficialFrameStream(ctx, "session/observeProjections",
		json.RawMessage(`{"sessionId":"s1"}`))
	if fault != nil {
		t.Fatalf("打开 observeProjections 失败：%v", fault)
	}
	defer stream.Close()
	if _, done, sfault := stream.Next(ctx); sfault != nil || done {
		t.Fatalf("observeProjections ready 帧失败：done=%v fault=%v", done, sfault)
	}

	// 追加三条：todo_write → todos；再用本会话自己的 spawn 之后的 turn 事件
	// 触发 subagentTiming（subagent 身份沿用 fixture 里 seq=4 的那条）。
	src := srv.opt.Source.(*projectionHostSource)
	src.append(eventAt(7, "tool/call", `{"name":"todo_write","arguments":"{\"todos\":[{\"content\":\"ship\",\"status\":\"completed\"}]}"}`))
	src.append(eventAt(8, "subagent/spawn", `{"childSessionId":"s1","parentSessionId":"p0"}`))
	src.append(eventAt(9, "turn/start", `{"turn":3}`))

	seen := map[string]bool{}
	deadline := time.Now().Add(3 * time.Second)
	for !(seen["todos"] && seen["subagent"] && seen["subagentTiming"]) && time.Now().Before(deadline) {
		raw, done, sfault := stream.Next(ctx)
		if sfault != nil {
			t.Fatalf("observeProjections 增量帧故障：%v", sfault)
		}
		if done {
			break
		}
		var frame struct {
			Type    string                     `json:"type"`
			Changes []officialProjectionChange `json:"changes"`
		}
		if err := json.Unmarshal(raw, &frame); err != nil {
			continue
		}
		if frame.Type != "projection" {
			continue
		}
		for _, change := range frame.Changes {
			seen[change.Key] = true
		}
	}
	for _, key := range []string{"todos", "subagent", "subagentTiming"} {
		if !seen[key] {
			t.Fatalf("observeProjections 没有发出 %q 变化帧（seen=%v）", key, seen)
		}
	}
}

// TestI04AttachWaitMuxStreamClaimsAndNeverSettles 钉住 attachWait 的 mux 流：
// 一帧 {remainingMs} → 持有 claim → 关流只释放 claim；不调用任何结算入口。
func TestI04AttachWaitMuxStreamClaimsAndNeverSettles(t *testing.T) {
	srv := newTestServer(t, Options{Source: newMutableOfficialSource(nil), Journals: NewJournals(nil)})
	claims := make([]bool, 0, 2)
	polls := 0
	settled := 0
	srv.AttachOfficialAgentAPI(OfficialAgentAPI{
		UserQuestionsAttachWait: func(sessionID, callID string) (*OfficialQuestionWait, error) {
			if sessionID != "s1" || callID != "c1" {
				t.Fatalf("attachWait 参数透传错了：%q/%q", sessionID, callID)
			}
			polls++
			if polls == 1 {
				return &OfficialQuestionWait{RemainingMs: 4200}, nil
			}
			return nil, nil
		},
		UserQuestionsClaimed: func(sessionID, callID string, claimed bool) {
			claims = append(claims, claimed)
		},
		UserQuestionsAnswer: func(sessionID, callID string, answer OfficialQuestionAnswer) (bool, error) {
			settled++
			return true, nil
		},
	})

	ctx := context.Background()
	stream, fault := srv.OpenOfficialFrameStream(ctx, "userQuestions/attachWait",
		json.RawMessage(`{"agentId":"s1","callId":"c1"}`))
	if fault != nil {
		t.Fatalf("打开 attachWait mux 流失败：%v", fault)
	}
	raw, done, sfault := stream.Next(ctx)
	if sfault != nil || done {
		t.Fatalf("attachWait 首帧失败：done=%v fault=%v", done, sfault)
	}
	var wait OfficialQuestionWait
	if err := json.Unmarshal(raw, &wait); err != nil {
		t.Fatalf("首帧不是 {remainingMs}：%s", raw)
	}
	if wait.RemainingMs != 4200 {
		t.Fatalf("remainingMs = %d，want 4200（来自宿主真实值）", wait.RemainingMs)
	}
	if len(claims) != 1 || !claims[0] {
		t.Fatalf("首帧之后必须持有 claim（true），claims=%v", claims)
	}

	// 一帧之后不再发帧：等 wait 结束（第二次 AttachWait 回 nil）→ done。
	waitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if raw, done, sfault := stream.Next(waitCtx); !done || raw != nil || sfault != nil {
		t.Fatalf("一帧之后必须静默等到 wait 结束（不发第二帧）：raw=%s done=%v fault=%v", raw, done, sfault)
	}
	stream.Close()
	if len(claims) != 2 || claims[1] != false {
		t.Fatalf("wait 结束/关流必须释放 claim（false），claims=%v", claims)
	}
	if settled != 0 {
		t.Fatalf("关流绝不能结算问题（settled=%d）", settled)
	}

	// wait 已经结束 → 零帧结束也是合法流（不是"帧机实现错误"）。
	zero := newTestServer(t, Options{Source: newMutableOfficialSource(nil), Journals: NewJournals(nil)})
	zeroClaims := 0
	zero.AttachOfficialAgentAPI(OfficialAgentAPI{
		UserQuestionsAttachWait: func(string, string) (*OfficialQuestionWait, error) { return nil, nil },
		UserQuestionsClaimed:    func(string, string, bool) { zeroClaims++ },
	})
	zeroStream, fault := zero.OpenOfficialFrameStream(ctx, "userQuestions/attachWait",
		json.RawMessage(`{"sessionId":"s1","callId":"c1"}`))
	if fault != nil {
		t.Fatalf("wait 已结束时打开 attachWait 流应成功（零帧）：%v", fault)
	}
	if raw, done, sfault := zeroStream.Next(ctx); !done || raw != nil || sfault != nil {
		t.Fatalf("wait 已结束必须是零帧结束：raw=%s done=%v fault=%v", raw, done, sfault)
	}
	if zeroClaims != 0 {
		t.Fatalf("零帧流不应 claim：%d", zeroClaims)
	}
	zeroStream.Close()

	// HTTP unary L1 不受 mux 接线影响：无 NDJSON marker 时仍是 unary 读数。
	// 用一台**新** Server + 新等待（mux 用例的等待已被上面的 Next 轮询掉），
	// 避免把"等待已结束 → 空 result"误判成形状回归。
	unarySrv := newTestServer(t, Options{Source: newMutableOfficialSource(nil), Journals: NewJournals(nil)})
	unarySettled := 0
	unarySrv.AttachOfficialAgentAPI(OfficialAgentAPI{
		UserQuestionsAttachWait: func(string, string) (*OfficialQuestionWait, error) {
			return &OfficialQuestionWait{RemainingMs: 777}, nil
		},
		UserQuestionsAnswer: func(string, string, OfficialQuestionAnswer) (bool, error) {
			unarySettled++
			return true, nil
		},
	})
	rec := postOfficial(t, func(w http.ResponseWriter, r *http.Request) {
		unarySrv.serveOfficialAgentAPI(w, r, string(MethodUserQuestionsAttachWait))
	}, `{"agentId":"s1","callId":"c1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("attachWait 的 unary L1 必须仍然 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "remainingMs") || !strings.Contains(rec.Body.String(), "777") {
		t.Fatalf("unary L1 结果形状变了：%s", rec.Body.String())
	}
	if settled != 0 || unarySettled != 0 {
		t.Fatalf("unary 读数也不应结算：mux=%d unary=%d", settled, unarySettled)
	}
}
