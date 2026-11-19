package transport

// 界面状态（`uiState.get/set/delete`）的 handler 测试。
//
// 这里验五件事：
//  1. 参数原样转发、值**逐字节**不进不改（宿主不解释状态内容）；
//  2. 结构错误挡在调用宿主之前（空 key、未知字段、尾随垃圾、空 value/null）；
//  3. 没存过是 200 + found:false（官方 localStorage.getItem 也回 null，不是错误）；
//  4. key 非法（宿主用 ErrUIStateBadKey 包过）是 400，不是 500——客户端该改请求；
//  5. 未接线是 501，不许静默成功。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeUIStateStore 是最小实现：把 get/set/delete 的入参抓下来，行为可注入。
type fakeUIStateStore struct {
	values  map[string][]byte
	getErr  error
	setErr  error
	delErr  error
	setKey  string
	setRaw  []byte
	getKey  string
	delKey  string
	setCall int
	getCall int
	delCall int
}

func newFakeUIStateStore() *fakeUIStateStore {
	return &fakeUIStateStore{values: map[string][]byte{}}
}

func (f *fakeUIStateStore) Get(key string) (UIState, bool, error) {
	f.getKey = key
	f.getCall++
	if f.getErr != nil {
		return UIState{}, false, f.getErr
	}
	value, ok := f.values[key]
	if !ok {
		return UIState{}, false, nil
	}
	return UIState{Key: key, Value: value}, true, nil
}

func (f *fakeUIStateStore) Set(key string, value []byte) error {
	f.setKey = key
	f.setRaw = append([]byte(nil), value...)
	f.setCall++
	if f.setErr != nil {
		return f.setErr
	}
	f.values[key] = append([]byte(nil), value...)
	return nil
}

func (f *fakeUIStateStore) Delete(key string) (bool, error) {
	f.delKey = key
	f.delCall++
	if f.delErr != nil {
		return false, f.delErr
	}
	_, ok := f.values[key]
	delete(f.values, key)
	return ok, nil
}

func postUIState(t *testing.T, s *Server, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/"+method, strings.NewReader(body))
	rec := httptest.NewRecorder()
	switch method {
	case "uiState.get":
		s.handleUIStateGet(rec, req)
	case "uiState.set":
		s.handleUIStateSet(rec, req)
	case "uiState.delete":
		s.handleUIStateDelete(rec, req)
	default:
		t.Fatalf("未知方法 %s", method)
	}
	return rec
}

// uiStateResult 拆出 handler 成功信封里的 result 对象。
func uiStateResult(t *testing.T, rec *httptest.ResponseRecorder) map[string]json.RawMessage {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("响应不是 JSON：%v（%s）", err, rec.Body.String())
	}
	raw, ok := env["result"]
	if !ok {
		t.Fatalf("没有 result 键：%s", rec.Body.String())
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("result 不是对象：%v", err)
	}
	return out
}

func TestUIStateSetForwardsKeyAndRawValue(t *testing.T) {
	store := newFakeUIStateStore()
	s := &Server{opt: Options{UIState: store}}
	// 值里的空白与键序都要逐字节保留：宿主是搬运工，不是解释器。
	body := `{"key":"dsh.sidebar-browser.v1.session-5f0d","value":{"b":2,  "a":[1,2,3]}}`
	rec := postUIState(t, s, "uiState.set", body)
	out := uiStateResult(t, rec)
	if store.setKey != "dsh.sidebar-browser.v1.session-5f0d" {
		t.Fatalf("key 转发错：%q", store.setKey)
	}
	if got := string(store.setRaw); got != `{"b":2,  "a":[1,2,3]}` {
		t.Fatalf("value 不是逐字节原样：%q", got)
	}
	var bytesCount float64
	if err := json.Unmarshal(out["bytes"], &bytesCount); err != nil {
		t.Fatalf("bytes 不是数字：%v", err)
	}
	if int(bytesCount) != len(store.setRaw) {
		t.Fatalf("bytes=%v，实际 %d", bytesCount, len(store.setRaw))
	}
	if got := string(out["key"]); got != `"dsh.sidebar-browser.v1.session-5f0d"` {
		t.Fatalf("回包 key=%s", got)
	}
}

func TestUIStateGetReturnsExactBytes(t *testing.T) {
	store := newFakeUIStateStore()
	store.values["dsh.user-questions.drafts.v1"] = []byte(`{"q1":"草稿"}`)
	s := &Server{opt: Options{UIState: store}}
	out := uiStateResult(t, postUIState(t, s, "uiState.get", `{"key":"dsh.user-questions.drafts.v1"}`))
	if string(out["found"]) != "true" {
		t.Fatalf("found=%s", out["found"])
	}
	if got := string(out["value"]); got != `{"q1":"草稿"}` {
		t.Fatalf("value 不是逐字节原样：%s", got)
	}
}

func TestUIStateGetMissingIsFoundFalseNot404(t *testing.T) {
	store := newFakeUIStateStore()
	s := &Server{opt: Options{UIState: store}}
	out := uiStateResult(t, postUIState(t, s, "uiState.get", `{"key":"dsh.sidebar-browser.v1.session-none"}`))
	if string(out["found"]) != "false" {
		t.Fatalf("没存过必须 found:false，实际 %s", out["found"])
	}
	if _, ok := out["value"]; ok {
		t.Fatalf("没存过不许带 value：%v", out)
	}
}

func TestUIStateDeleteReportsWhetherItExisted(t *testing.T) {
	store := newFakeUIStateStore()
	store.values["dsh.sidebar-browser.v1.session-a"] = []byte(`{}`)
	s := &Server{opt: Options{UIState: store}}
	out := uiStateResult(t, postUIState(t, s, "uiState.delete", `{"key":"dsh.sidebar-browser.v1.session-a"}`))
	if string(out["deleted"]) != "true" {
		t.Fatalf("删掉已存在的一份必须 deleted:true，实际 %s", out["deleted"])
	}
	out = uiStateResult(t, postUIState(t, s, "uiState.delete", `{"key":"dsh.sidebar-browser.v1.session-a"}`))
	if string(out["deleted"]) != "false" {
		t.Fatalf("第二次删必须 deleted:false，实际 %s", out["deleted"])
	}
}

func TestUIStateRejectsStructuralErrorsBeforeHost(t *testing.T) {
	cases := []struct{ name, method, body string }{
		{"坏 JSON", "uiState.get", `{`},
		{"null", "uiState.get", `null`},
		{"数组", "uiState.get", `[]`},
		{"尾随垃圾", "uiState.get", `{"key":"a"}{"key":"b"}`},
		{"未知字段", "uiState.get", `{"key":"a","nope":1}`},
		{"空 key", "uiState.get", `{"key":""}`},
		{"set 空 key", "uiState.set", `{"key":"","value":{"a":1}}`},
		{"set 缺 value", "uiState.set", `{"key":"dsh.x.v1"}`},
		{"set 显式 null", "uiState.set", `{"key":"dsh.x.v1","value":null}`},
		{"delete 空 key", "uiState.delete", `{"key":""}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := newFakeUIStateStore()
			s := &Server{opt: Options{UIState: store}}
			rec := postUIState(t, s, c.method, c.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d，want 400（body=%s）", rec.Code, rec.Body.String())
			}
			if store.getCall+store.setCall+store.delCall != 0 {
				t.Fatalf("结构错误必须在调用宿主之前挡掉，实际调了 %d 次",
					store.getCall+store.setCall+store.delCall)
			}
			if code := scanErrorResponse(t, c.name, rec.Code, rec.Header(), rec.Body.Bytes()); code != "bad_request" {
				t.Fatalf("code=%q，want bad_request", code)
			}
		})
	}
}

func TestUIStateBadKeyIs400NotHostFailure(t *testing.T) {
	// 宿主用 ErrUIStateBadKey 包一层就说明"这是调用方的错"；transport 要把它
	// 翻成 400，而不是 500（500 会让客户端以为该重试）。
	store := newFakeUIStateStore()
	store.setErr = fmt.Errorf("%w: key 含非法字符", ErrUIStateBadKey)
	store.getErr = fmt.Errorf("%w: key 为空", ErrUIStateBadKey)
	store.delErr = fmt.Errorf("%w: key 太长", ErrUIStateBadKey)
	s := &Server{opt: Options{UIState: store}}
	for _, method := range []string{"uiState.get", "uiState.set", "uiState.delete"} {
		body := `{"key":"../etc/passwd"}`
		if method == "uiState.set" {
			body = `{"key":"../etc/passwd","value":{}}`
		}
		rec := postUIState(t, s, method, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s：status=%d，want 400（body=%s）", method, rec.Code, rec.Body.String())
		}
		if code := scanErrorResponse(t, method, rec.Code, rec.Header(), rec.Body.Bytes()); code != "bad_request" {
			t.Fatalf("%s：code=%q，want bad_request", method, code)
		}
	}
}

func TestUIStateHostFailureIs500Internal(t *testing.T) {
	store := newFakeUIStateStore()
	store.setErr = fmt.Errorf("磁盘满了")
	s := &Server{opt: Options{UIState: store}}
	rec := postUIState(t, s, "uiState.set", `{"key":"dsh.x.v1","value":{}}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d，want 500", rec.Code)
	}
	if code := scanErrorResponse(t, "uiState.set", rec.Code, rec.Header(), rec.Body.Bytes()); code != "internal" {
		t.Fatalf("code=%q，want internal", code)
	}
}

func TestUIStateUnwiredIs501(t *testing.T) {
	s := &Server{opt: Options{}}
	for _, c := range []struct{ method, body string }{
		{"uiState.get", `{"key":"dsh.x.v1"}`},
		{"uiState.set", `{"key":"dsh.x.v1","value":{}}`},
		{"uiState.delete", `{"key":"dsh.x.v1"}`},
	} {
		rec := postUIState(t, s, c.method, c.body)
		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("%s：status=%d，want 501（body=%s）", c.method, rec.Code, rec.Body.String())
		}
		// 必须说清"未接线"，而不是通用 501 的 "not implemented"：
		// 两者都回 not_implemented，但前者带上方法名，排查时不用猜是哪条。
		if !strings.Contains(rec.Body.String(), c.method) {
			t.Fatalf("%s：501 文案没点名方法：%s", c.method, rec.Body.String())
		}
		if code := scanErrorResponse(t, c.method, rec.Code, rec.Header(), rec.Body.Bytes()); code != "not_implemented" {
			t.Fatalf("%s：code=%q，want not_implemented", c.method, code)
		}
	}
}
