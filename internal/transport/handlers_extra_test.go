package transport

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var errBoom = errors.New("磁盘满了")

func newTestServer(t *testing.T, o Options) *Server {
	t.Helper()
	s, err := NewServer(o)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func postJSON(t *testing.T, s *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	// 走真实的鉴权入口会需要 token/cookie；这里只测 handler 本身的分支，
	// 鉴权由 server_test.go 的三条测试单独覆盖。
	rec := httptest.NewRecorder()
	s.mu.Lock()
	s.cookies["test-cookie"] = struct{}{}
	s.mu.Unlock()
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "test-cookie"})
	s.handleAPI(rec, req)
	return rec
}

// 删会话：找不到是 404（换个 id 重试），删失败是 500（提示没删掉）——
// 这两种在界面上长得一样，混在一起用户没法判断该不该重试。
func TestDeleteDistinguishesNotFoundFromFailure(t *testing.T) {
	s := newTestServer(t, Options{
		Delete: func(id string) error {
			if id == "missing" {
				return ErrNoSuchSession
			}
			return errBoom
		},
	})
	if rec := postJSON(t, s, "/api/session.delete", `{"sessionId":"missing"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("不存在的会话要 404，拿到 %d", rec.Code)
	}
	if rec := postJSON(t, s, "/api/session.delete", `{"sessionId":"x"}`); rec.Code != http.StatusInternalServerError {
		t.Fatalf("删失败要 500，拿到 %d", rec.Code)
	}
}

func TestDeleteRejectsEmptyID(t *testing.T) {
	s := newTestServer(t, Options{Delete: func(string) error { t.Fatal("不该被调用"); return nil }})
	if rec := postJSON(t, s, "/api/session.delete", `{"sessionId":""}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("空 id 要 400，拿到 %d", rec.Code)
	}
}

// fork 成功要回新 id——界面拿它直接跳转，回空等于让用户手动猜。
func TestForkReturnsNewSessionID(t *testing.T) {
	s := newTestServer(t, Options{Fork: func(id, title string) (string, error) {
		if id != "src" {
			t.Fatalf("源会话 id 没传对：%s", id)
		}
		return "session-new", nil
	}})
	rec := postJSON(t, s, "/api/session.fork", `{"sessionId":"src","title":"分支"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("要 200，拿到 %d", rec.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["sessionId"] != "session-new" {
		t.Fatalf("没回新会话 id：%v", got)
	}
}

// 没接线时要 501 而不是 500：501 说的是"这个能力还没有"，500 说的是"它坏了"。
func TestUnwiredMethodsReturn501(t *testing.T) {
	s := newTestServer(t, Options{})
	for _, path := range []string{"/api/session.delete", "/api/session.fork"} {
		if rec := postJSON(t, s, path, `{"sessionId":"a"}`); rec.Code != http.StatusNotImplemented {
			t.Fatalf("%s 未接线要 501，拿到 %d", path, rec.Code)
		}
	}
}

// total 未知时必须报 truncated=true：另一种选择会让界面以为已经到底，
// 而用户看到的是一条被截断的历史——这类错误不报错，只会让人找不到东西。
func TestLoadWithUnknownTotalIsTruncated(t *testing.T) {
	src := &fakeTailSrc{msgs: []Message{{Seq: 1, Role: "user", Text: "hi"}}, total: -1}
	got, err := LoadSession(src, "s-1", 50)
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != -1 || !got.Truncated {
		t.Fatalf("total=-1 时 truncated 必须是 true，实际 total=%d truncated=%v", got.Total, got.Truncated)
	}
}

type fakeTailSrc struct {
	msgs  []Message
	total int
}

func (f *fakeTailSrc) Messages(string) ([]Message, error) { return f.msgs, nil }
func (f *fakeTailSrc) MessagesTail(string, int) ([]Message, int, error) {
	return f.msgs, f.total, nil
}
