package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type cordisErrAPI struct{ err error }

func (a cordisErrAPI) HandleOfficialCordis(context.Context, string, json.RawMessage) (any, error) {
	return nil, a.err
}

// 反例：CordisAPIError 必须仍走统一 errorBodyFor，且 hint 不能被丢掉。
func TestCordisAPIErrorUsesUnifiedErrorBody(t *testing.T) {
	s := &Server{}
	s.AttachOfficialCordisAPI(cordisErrAPI{err: &CordisAPIError{
		Status: http.StatusNotFound, Code: "not_found", Message: "没有这个运行", Hint: "先 invoke",
	}})
	req := httptest.NewRequest("POST", "/api/dynamicCordisRunner.settleUserRun", strings.NewReader(`{"requestId":"x"}`))
	w := httptest.NewRecorder()
	s.ServeOfficialCordisAPI(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body map[string]map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	e := body["error"]
	if len(body) != 1 || e["code"] != "not_found" || !strings.Contains(e["message"].(string), "先 invoke") {
		t.Fatalf("错误体不是统一四字段/丢 hint：%s", w.Body.String())
	}
}

type cordisNullAPI struct{}

func (cordisNullAPI) HandleOfficialCordis(context.Context, string, json.RawMessage) (any, error) {
	return NullCordisResult(), nil
}

// 官方结果就是 `z.literal(null)` 的那两个 report 方法：出口必须是
// `{"result":null}`（null 是**值**），不能变成"空载荷 500"，也不能塞自制回执。
func TestCordisNullReceiptIsLiteralNull(t *testing.T) {
	s := &Server{}
	s.AttachOfficialCordisAPI(cordisNullAPI{})
	for _, m := range []string{
		"dynamicCordisRunner.reportClientGuardFailure",
		"dynamicCordisRunner.reportRenderFailure",
	} {
		req := httptest.NewRequest("POST", "/api/"+m, strings.NewReader(`{"agentId":"s","pluginId":"p","pluginRunId":"r","failure":{}}`))
		w := httptest.NewRecorder()
		s.ServeOfficialCordisAPI(w, req)
		if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"result":null}` {
			t.Fatalf("%s: status=%d body=%s", m, w.Code, w.Body.String())
		}
	}
}

// cordisRecorderAPI 只记录被认领的方法名，用于验证"真的走到真实现"。
type cordisRecorderAPI struct{ calls []string }

func (a *cordisRecorderAPI) HandleOfficialCordis(_ context.Context, method string, _ json.RawMessage) (any, error) {
	a.calls = append(a.calls, method)
	return map[string]any{"ok": true}, nil
}

// 路由顺序（server.go：`IsOfficialCordisMethod` 在 `IsOfficialMediaMarketMethod` 之前）：
// 12 个 cordis 方法必须先由本文件的 ServeOfficialCordisAPI 认领。
//
// handlers_media_market_official.go 里原先那张 `officialCordisSpecs` R6-501 表对这些
// 方法是**整条不可达**的（每个键都被 cordis 分支抢先），2026-11-15 删除。这条测试
// 同时钉住两件事：真实现认领、以及 media/market 组**不再**认领——同一方法两处结论
// 必然漂，那正是"账本写 501、实际早有真实现"的成因。
func TestCordisMethodsRoutedToRealAPINotMediaMarket(t *testing.T) {
	names := []string{
		"dynamicCordisRunner.inventory",
		"dynamicCordisRunner.getClientCode",
		"dynamicCordisRunner.invoke",
		"dynamicCordisRunner.reportClientGuardFailure",
		"dynamicCordisRunner.reportRenderFailure",
		"dynamicCordisRunner.resolveInspectQuery",
		"dynamicCordisRunner.resolveRequestRun",
		"dynamicCordisRunner.runHostHalf",
		"dynamicCordisRunner.settleUserRun",
		"dynamicCordisRunner.stopFromPanel",
		"dynamicCordisRunner.syncInspectManifest",
		"dynamicCordisRunner.undefineFromPanel",
	}
	if len(names) != 12 {
		t.Fatalf("cordis 方法必须是 12 个，拿到 %d", len(names))
	}
	s := newTestServer(t, Options{})
	defer s.Close()
	api := &cordisRecorderAPI{}
	s.AttachOfficialCordisAPI(api)
	for _, name := range names {
		if !IsOfficialCordisMethod(name) {
			t.Fatalf("%s 不在 IsOfficialCordisMethod 里", name)
		}
		if IsOfficialMediaMarketMethod(name) {
			t.Fatalf("%s 被 media/market 组重复认领：同一方法两处结论必然漂", name)
		}
		rec := postJSON(t, s, "/api/"+name, `{"pluginId":"p"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s 没走到真实现：status=%d body=%s", name, rec.Code, rec.Body.String())
		}
	}
	if len(api.calls) != len(names) {
		t.Fatalf("真实现应被调用 %d 次，实际 %d 次: %v", len(names), len(api.calls), api.calls)
	}
	for i, name := range names {
		if api.calls[i] != name {
			t.Fatalf("第 %d 个方法走到 %q，应为 %q", i, api.calls[i], name)
		}
	}
}
