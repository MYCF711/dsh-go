package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"dshgo/internal/llm"
)

type sessionIdentityAppender struct{}

func (sessionIdentityAppender) Append(string, any) error { return nil }

func TestLoopPassesSessionIdentityWithoutWireFields(t *testing.T) {
	var captured llm.Request
	appender := sessionIdentityAppender{}
	chat := sideQueryChatFunc(func(_ context.Context, req llm.Request, _ func(llm.Event) error) (*llm.Response, error) {
		captured = req
		return &llm.Response{Content: "完成", FinishReason: "stop"}, nil
	})
	loop := &Loop{Chat: chat, Model: "m", SessionID: "session-A", SessionLog: appender}
	if _, err := loop.Run(context.Background(), 1, []llm.Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	if captured.SessionID != "session-A" || captured.SessionLog == nil {
		t.Fatalf("Loop 没把 live session 身份/写口交给请求: %+v", captured)
	}
	raw, err := json.Marshal(captured)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"SessionID", "SessionLog", "session-A"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("非 wire 字段泄漏进请求 JSON（%q）: %s", forbidden, raw)
		}
	}
}
