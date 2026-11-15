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
