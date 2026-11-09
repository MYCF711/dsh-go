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
