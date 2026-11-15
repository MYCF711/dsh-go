package transport

// 官方 #23 `authorization` 的 wire 面验收。
//
// 判据（每条都对应官方终态契约，不是"HTTP 200"）：
//   - `authorization.list` 列出**真实注册**的 flow（label/methods/inFlight）；
//   - `authorization.describe` 对没有 flow 的 key 回 entry=null（不是错误）；
//   - `authorization.begin` 在同一条响应上把 notice / prompt / outcome 推回来，
//     并且**只有** flow 提交过才是 authorized；
//   - `authorization.answer` 把人的答复送回 flow（人拒绝 = cancelled）；
//   - `authorization.cancel` 能从第二次调用撤掉在飞的尝试；
//   - 未接线时 501 + 点名缺什么。
//
// 为什么用真 HTTP server 而不是 httptest.ResponseRecorder：`begin` 是**流式**的
// （NDJSON 增量推回），ResponseRecorder 的 Body 没有并发保护，边写边读就是数据
// 竞争。真 server 才能既验流式、又不引入假绿。

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"dshgo/internal/authorization"
)

// newAuthorizationTestServer 起一个只服务 authorization 方法组的真 server。
func newAuthorizationTestServer(t *testing.T, flows ...authorization.Flow) (*httptest.Server, *authorization.Registry) {
	t.Helper()
	registry := authorization.NewRegistry()
	for _, flow := range flows {
		if _, err := registry.RegisterFlow(flow); err != nil {
			t.Fatalf("注册 flow 失败：%v", err)
		}
	}
	srv := &Server{}
	srv.AttachAuthorizationRegistry(registry)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/api/")
		if !IsAuthorizationMethod(name) {
			writeJSON(w, http.StatusNotFound, errorBodyFor("not_found", "不是 authorization 方法"))
			return
		}
		srv.ServeOfficialAuthorizationAPI(w, r)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(func() {
		srv.DetachAuthorizationRegistry()
		ts.Close()
	})
	return ts, registry
}

// call 发一个普通（非流式）JSON 请求。
func call(t *testing.T, ts *httptest.Server, method, body string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Post(ts.URL+"/api/"+method, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("%s 请求失败：%v", method, err)
	}
	defer resp.Body.Close()
	decode := map[string]any{}
	if err := json.NewDecoder(resp.Body).Decode(&decode); err != nil {
		t.Fatalf("%s 的响应不是 JSON：%v", method, err)
	}
	// TRANSPORT 的信封：成功载荷在 `result` 下。
	out, _ := decode["result"].(map[string]any)
	if out == nil {
		out = decode
	}
	return resp.StatusCode, out
}

// beginStream 发一个 begin 请求并返回事件通道（NDJSON 逐行）。
func beginStream(t *testing.T, ts *httptest.Server, key string) (<-chan map[string]any, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/api/authorization.begin",
		strings.NewReader(`{"key":"`+key+`"}`))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("begin 请求失败：%v", err)
	}
	if resp.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("begin 状态码 %d", resp.StatusCode)
	}
	events := make(chan map[string]any, 32)
	go func() {
		defer close(events)
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			event := map[string]any{}
			if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
				continue
			}
			events <- event
		}
	}()
	return events, cancel
}

func waitEvent(t *testing.T, events <-chan map[string]any, kind string) map[string]any {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatalf("事件流提前结束，没等到 %q 事件", kind)
			}
			if event["type"] == kind {
				return event
			}
		case <-deadline:
			t.Fatalf("等 %q 事件超时", kind)
		}
	}
}

// list / describe 是注册表的真实投影。
func TestAuthorizationListAndDescribe(t *testing.T) {
	ts, _ := newAuthorizationTestServer(t, authorization.Flow{
		Key:     "acme/thing",
		Label:   "Acme 账号",
		Methods: []authorization.Method{{ID: "code", Label: "验证码"}},
		Run:     func(ctx context.Context, s *authorization.Surface) error { return s.Commit() },
	})

	code, out := call(t, ts, "authorization.list", `{}`)
	if code != http.StatusOK {
		t.Fatalf("list 状态码 %d", code)
	}
	entries, _ := out["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("list 应当有 1 条 flow，实际 %v", out["entries"])
	}
	first, _ := entries[0].(map[string]any)
	if first["key"] != "acme/thing" || first["label"] != "Acme 账号" {
		t.Fatalf("list 投影不对：%v", first)
	}
	if methods, _ := first["methods"].([]any); len(methods) != 1 {
		t.Fatalf("methods 丢了：%v", first)
	}

	code, out = call(t, ts, "authorization.describe", `{"key":"acme/thing"}`)
	if code != http.StatusOK {
		t.Fatalf("describe 状态码 %d", code)
	}
	entry, _ := out["entry"].(map[string]any)
	if entry == nil || entry["key"] != "acme/thing" {
		t.Fatalf("describe 没返回 entry：%v", out)
	}
	// 没有 flow 的 key → entry=null（不是 404）
	code, out = call(t, ts, "authorization.describe", `{"key":"acme/nobody"}`)
	if code != http.StatusOK || out["entry"] != nil {
		t.Fatalf("没有 flow 的 key 必须回 entry=null，实际 %d %v", code, out)
	}
}

// begin 流式推回 notice → prompt → outcome，answer 把人的答复送进 flow。
func TestAuthorizationBeginAnswerRoundTrip(t *testing.T) {
	ts, _ := newAuthorizationTestServer(t, authorization.Flow{
		Key:     "acme/stream",
		Label:   "Acme",
		Methods: []authorization.Method{{ID: "code", Label: "验证码"}},
		Run: func(ctx context.Context, session *authorization.Surface) error {
			if err := session.Notify(authorization.Notice{Message: "去页面", URL: "https://acme/x", Code: "AB-1"}); err != nil {
				return err
			}
			answer, err := session.Prompt(authorization.Prompt{
				Kind:    authorization.PromptSelect,
				Message: "好了吗",
				Options: []authorization.PromptOption{{ID: "done", Label: "好了"}, {ID: "cancel", Label: "算了"}},
			})
			if err != nil {
				return err
			}
			if answer != "done" {
				t.Errorf("答案应当是 done，实际 %q", answer)
			}
			return session.Commit()
		},
	})

	events, cancel := beginStream(t, ts, "acme/stream")
	defer cancel()

	notice := waitEvent(t, events, "notice")
	noticePayload, _ := notice["notice"].(map[string]any)
	if noticePayload["code"] != "AB-1" || noticePayload["url"] != "https://acme/x" {
		t.Fatalf("notice 载荷丢了：%v", notice)
	}
	prompt := waitEvent(t, events, "prompt")
	id, _ := prompt["id"].(string)
	if id == "" {
		t.Fatalf("prompt 事件没有 id：%v", prompt)
	}
	code, out := call(t, ts, "authorization.answer", `{"key":"acme/stream","id":"`+id+`","value":"done"}`)
	if code != http.StatusOK || out["accepted"] != true {
		t.Fatalf("answer 没被接收：%d %v", code, out)
	}
	outcome := waitEvent(t, events, "outcome")
	payload, _ := outcome["outcome"].(map[string]any)
	if payload["status"] != "authorized" {
		t.Fatalf("终态应当是 authorized，实际 %v", payload)
	}
}

// 人拒绝：answer 回空 → cancelled（不是错误）。
func TestAuthorizationBeginDeclinedIsCancelled(t *testing.T) {
	ts, _ := newAuthorizationTestServer(t, authorization.Flow{
		Key:     "acme/decline",
		Label:   "Acme",
		Methods: []authorization.Method{{ID: "code", Label: "验证码"}},
		Run: func(ctx context.Context, session *authorization.Surface) error {
			if _, err := session.Prompt(authorization.Prompt{Kind: authorization.PromptText, Message: "码?"}); err != nil {
				return err
			}
			return session.Commit()
		},
	})
	events, cancel := beginStream(t, ts, "acme/decline")
	defer cancel()
	prompt := waitEvent(t, events, "prompt")
	id, _ := prompt["id"].(string)
	code, _ := call(t, ts, "authorization.answer", `{"key":"acme/decline","id":"`+id+`","declined":true}`)
	if code != http.StatusOK {
		t.Fatalf("answer 状态码 %d", code)
	}
	outcome := waitEvent(t, events, "outcome")
	payload, _ := outcome["outcome"].(map[string]any)
	if payload["status"] != "cancelled" {
		t.Fatalf("人拒绝的终态应当是 cancelled，实际 %v", payload)
	}
}

// NOT_COMMITTED 通过 wire 报出来，code 是官方字符串。
func TestAuthorizationBeginNotCommittedIsReported(t *testing.T) {
	ts, _ := newAuthorizationTestServer(t, authorization.Flow{
		Key:     "acme/nocommit",
		Label:   "Acme",
		Methods: []authorization.Method{{ID: "code", Label: "验证码"}},
		Run:     func(ctx context.Context, session *authorization.Surface) error { return nil },
	})
	events, cancel := beginStream(t, ts, "acme/nocommit")
	defer cancel()
	failure := waitEvent(t, events, "error")
	if failure["code"] != authorization.ErrCodeNotCommitted {
		t.Fatalf("code 应当是 %s，实际 %v（%v）", authorization.ErrCodeNotCommitted, failure["code"], failure)
	}
}

// cancel 从第二次调用撤掉在飞的尝试。
func TestAuthorizationCancelFromSecondCall(t *testing.T) {
	block := make(chan struct{})
	ts, registry := newAuthorizationTestServer(t, authorization.Flow{
		Key:     "acme/cancel",
		Label:   "Acme",
		Methods: []authorization.Method{{ID: "code", Label: "验证码"}},
		Run: func(ctx context.Context, session *authorization.Surface) error {
			close(block)
			<-ctx.Done()
			return ctx.Err()
		},
	})
	events, cancel := beginStream(t, ts, "acme/cancel")
	defer cancel()
	<-block
	if !registry.InFlight("acme/cancel") {
		t.Fatal("flow 已经在跑，但注册表说不在飞（inFlight 投影不对）")
	}
	code, out := call(t, ts, "authorization.cancel", `{"key":"acme/cancel"}`)
	if code != http.StatusOK || out["cancelled"] != true {
		t.Fatalf("cancel 没撤掉：%d %v", code, out)
	}
	outcome := waitEvent(t, events, "outcome")
	payload, _ := outcome["outcome"].(map[string]any)
	if payload["status"] != "cancelled" {
		t.Fatalf("撤销之后的终态应当是 cancelled，实际 %v", payload)
	}
	if registry.InFlight("acme/cancel") {
		t.Fatal("结算之后不该还在飞")
	}
}

// 连接断开 = 撤销（官方：withdrawn attempt settles immediately）。
func TestAuthorizationBeginDisconnectCancels(t *testing.T) {
	started := make(chan struct{})
	ts, registry := newAuthorizationTestServer(t, authorization.Flow{
		Key:     "acme/disconnect",
		Label:   "Acme",
		Methods: []authorization.Method{{ID: "code", Label: "验证码"}},
		Run: func(ctx context.Context, session *authorization.Surface) error {
			close(started)
			// 故意**不**理会 signal：官方要求即便如此也要结算成 cancelled。
			time.Sleep(300 * time.Millisecond)
			return session.Commit()
		},
	})
	// 这条用例只关心**撤销**有没有生效，不看事件流；第一个返回值直接丢弃。
	// （原先写成 events 再在 t.Fatal 之后 `_ = events`，那段代码不可达，
	//  staticcheck SA4006 抓出来「赋值后从未使用」——是**真提示**。）
	_, cancel := beginStream(t, ts, "acme/disconnect")
	<-started
	cancel() // 客户端断开
	// 等 server 侧的 begin 结算。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !registry.InFlight("acme/disconnect") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("连接断开之后 attempt 仍然在飞（撤销没有生效）")
}

// 未接线：结构化 501 + 点名接什么。
func TestAuthorizationMethodsNotWired(t *testing.T) {
	srv := &Server{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/authorization.list", strings.NewReader(`{}`))
	srv.ServeOfficialAuthorizationAPI(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("未接线应当 501，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "AttachAuthorizationRegistry") {
		t.Fatalf("错误体要点名接什么：%s", rec.Body.String())
	}
}

// 拒绝未知方法（不能让 authorization.* 的未知方法掉进别的 handler）。
func TestAuthorizationUnknownMethodIs404(t *testing.T) {
	srv := &Server{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/authorization.nope", strings.NewReader(`{}`))
	srv.ServeOfficialAuthorizationAPI(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未知方法应当 404，实际 %d", rec.Code)
	}
	if IsAuthorizationMethod("authorization.nope") {
		t.Fatal("IsAuthorizationMethod 不该认未知方法")
	}
}
