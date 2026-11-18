package transport

// `terminal.resize` 是 `terminal.input` 那条 HTTP 回落投影的同型补全
// （官方 uplink 的 write/resize 都不在 HTTP 面，见
// handlers_job_terminal_official.go:1300）。
//
// 这里验四件事：
//  1. 参数原样转发（sessionId + 老的 agentId 别名 + id + cols/rows）；
//  2. 结构错误挡在调用宿主之前（缺 id / 缺 sessionId / 尺寸不是正整数）；
//  3. 未接线是 501，不许静默成功；
//  4. 官方终端域的错误映射**复用唯一那份**（参数 400、跨会话 403、终端没了 404），
//     而不是自己再写一套状态码。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// postTerminalResize 打一次 /api/terminal.resize 并返回 recorder。
func postTerminalResize(s *Server, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/terminal.resize", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleTerminalResize(rec, req)
	return rec
}

func TestTerminalResizeForwardsSessionAndSize(t *testing.T) {
	var gotSession, gotID string
	var gotCols, gotRows int
	s := &Server{opt: Options{TerminalResize: func(sessionID, id string, cols, rows int) error {
		gotSession, gotID, gotCols, gotRows = sessionID, id, cols, rows
		return nil
	}}}
	rec := postTerminalResize(s, `{"sessionId":"s1","id":"t1","cols":140,"rows":45}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("resize 失败：%d %s", rec.Code, rec.Body.String())
	}
	if gotSession != "s1" || gotID != "t1" || gotCols != 140 || gotRows != 45 {
		t.Fatalf("resize 参数未原样转发：session=%q id=%q cols=%d rows=%d", gotSession, gotID, gotCols, gotRows)
	}
	// 与 terminal.input 同型：成功回 {"result":{"ok":true}}。
	if !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("成功结果要与 terminal.input 同型（result.ok=true）：%s", rec.Body.String())
	}
}

// agentId 是 terminal.input 认的旧名，resize 同型接受它（否则同一份调用方
// 在 write 上能过、在 resize 上被拒，得为同一个会话身份记两个参数名）。
func TestTerminalResizeAcceptsAgentIDAlias(t *testing.T) {
	var gotSession string
	s := &Server{opt: Options{TerminalResize: func(sessionID, id string, cols, rows int) error {
		gotSession = sessionID
		return nil
	}}}
	rec := postTerminalResize(s, `{"agentId":"agent-7","id":"t1","cols":80,"rows":24}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("agentId 别名必须可用：%d %s", rec.Code, rec.Body.String())
	}
	if gotSession != "agent-7" {
		t.Fatalf("agentId 未解析成 sessionId，拿到 %q", gotSession)
	}
}

func TestTerminalResizeRejectsStructuralErrors(t *testing.T) {
	called := false
	s := &Server{opt: Options{TerminalResize: func(string, string, int, int) error {
		called = true
		return nil
	}}}
	cases := []struct {
		name string
		body string
	}{
		{"坏 JSON", `{`},
		{"缺 id", `{"sessionId":"s1","cols":80,"rows":24}`},
		{"缺 sessionId", `{"id":"t1","cols":80,"rows":24}`},
		{"cols 不是正整数", `{"sessionId":"s1","id":"t1","cols":0,"rows":24}`},
		{"rows 不是正整数", `{"sessionId":"s1","id":"t1","cols":80,"rows":0}`},
		{"负尺寸", `{"sessionId":"s1","id":"t1","cols":-1,"rows":-1}`},
	}
	for _, c := range cases {
		rec := postTerminalResize(s, c.body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s：必须是 400，拿到 %d %s", c.name, rec.Code, rec.Body.String())
		}
	}
	if called {
		t.Fatal("结构错误必须在调用宿主之前挡掉（否则宿主会被无意义地打一遍）")
	}
}

func TestTerminalResizeUnwiredIs501(t *testing.T) {
	s := &Server{opt: Options{}}
	rec := postTerminalResize(s, `{"sessionId":"s1","id":"t1","cols":140,"rows":45}`)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("未接线必须 501（不许静默成功）：%d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"code":"not_implemented"`) {
		t.Fatalf("未接线要回 not_implemented，拿到 %s", rec.Body.String())
	}
}

// 官方终端域的错误映射只有一份（writeJTOfficialError）：这条钉住 resize 也走它，
// 不然就会出现"HTTP 回 404、mux 回 400"这种只在一侧复现的错。
func TestTerminalResizeReusesOfficialErrorMapping(t *testing.T) {
	table := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"官方档位越界", fmt.Errorf("%w: cols∈[2,500]、rows∈[1,200]", ErrJTOfficialBadParam), http.StatusBadRequest, "bad_request"},
		{"终端不属于本会话", ErrCrossSession, http.StatusForbidden, "cross_session"},
		{"终端没了", ErrJTOfficialTerminalGone, http.StatusNotFound, "not_found"},
		{"宿主没这个能力", ErrJTOfficialUnsupported, http.StatusNotImplemented, "not_implemented"},
	}
	for _, c := range table {
		s := &Server{opt: Options{TerminalResize: func(string, string, int, int) error { return c.err }}}
		rec := postTerminalResize(s, `{"sessionId":"s1","id":"t1","cols":140,"rows":45}`)
		if rec.Code != c.status {
			t.Fatalf("%s：要 %d，拿到 %d %s", c.name, c.status, rec.Code, rec.Body.String())
		}
		// 四字段信封：错误体只有一处构造（errorObject），这条也顺带复验。
		scanErrorResponse(t, c.name, rec.Code, rec.Header(), rec.Body.Bytes())
		if !strings.Contains(rec.Body.String(), `"code":"`+c.code+`"`) {
			t.Fatalf("%s：码要 %q，拿到 %s", c.name, c.code, rec.Body.String())
		}
	}
}
