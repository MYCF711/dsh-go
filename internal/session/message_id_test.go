package session

import (
	"encoding/json"
	"regexp"
	"testing"

	"dshgo/internal/spi"
)

// 官方 v4 的消息对象都带 `MessageBase.id`（`@deepseek-ai/dsh-llm` 的
// `src/message.ts:139-141`，由 `createMessage` 在 `src/message.ts:226` 用
// `randomUUID()` 分配一次）。宿主是 Go 侧 writer，就必须在**写的时候**分配一个
// 等价的持久 id 并落进 v4，读路径只许原样取出来。
//
// 这一组钉住"每个消息构造器都在写路径给身份"，漏掉哪个构造器就等于那个角色的
// 消息在界面侧永远没有可引用的持久身份（只能退回 `v4:<seq>` 位置键）。
var persistedIDShape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func messageIDAt(t *testing.T, data map[string]any) string {
	t.Helper()
	if id, ok := data["id"].(string); ok {
		return id
	}
	msg, ok := data["message"].(map[string]any)
	if !ok {
		return ""
	}
	id, _ := msg["id"].(string)
	return id
}

func TestMessageConstructorsAssignPersistentIdentity(t *testing.T) {
	cases := []struct {
		name string
		data map[string]any
	}{
		{"user/message", UserMessage([]spi.Block{{Type: "text", Text: "问题"}})},
		{"user/message(content)", UserContentMessage([]ContentBlock{{Type: "text", Text: "问题"}})},
		{"assistant/message", AssistantMessageEvent(1, 1,
			[]map[string]any{TextBlock("回答")}, "stop", nil)},
		{"system/message", SystemMessageData(1, 1, "系统提示")},
		{"tool/result", ToolResultEvent(1, 1, "call_1",
			[]spi.Block{{Type: "text", Text: "结果"}}, false, `D:\work`, nil)},
	}
	seen := map[string]string{}
	for _, c := range cases {
		id := messageIDAt(t, c.data)
		if id == "" {
			t.Errorf("%s 的构造器没有写持久 message id：%v", c.name, c.data)
			continue
		}
		if !persistedIDShape.MatchString(id) {
			t.Errorf("%s 的 id=%q 不是官方 MessageId 的 uuidv4 线上形状", c.name, id)
		}
		if prev, dup := seen[id]; dup {
			t.Errorf("%s 与 %s 撞了同一个 id=%q（身份必须每次新建）", c.name, prev, id)
		}
		seen[id] = c.name
	}
}

// 落盘往返：id 必须在 JSON 里原样过去、原样回来。它是**数据**，不是进程内句柄——
// 否则换一个进程读同一个文件就取不到了。
func TestPersistedIdentitySurvivesJSONRoundTrip(t *testing.T) {
	payloads := []struct {
		name string
		data map[string]any
		at   string // 取值位置
	}{
		{"user/message", UserMessage([]spi.Block{{Type: "text", Text: "问题"}}), "top"},
		{"assistant/message", AssistantMessageEvent(1, 1,
			[]map[string]any{TextBlock("回答")}, "stop", nil), "message"},
		{"system/message", SystemMessageData(1, 1, "系统提示"), "message"},
		{"tool/result", ToolResultEvent(1, 1, "call_1",
			[]spi.Block{{Type: "text", Text: "结果"}}, false, "", nil), "message"},
	}
	for _, p := range payloads {
		want := messageIDAt(t, p.data)
		raw, err := json.Marshal(p.data)
		if err != nil {
			t.Fatalf("%s 序列化: %v", p.name, err)
		}
		var back map[string]any
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatalf("%s 反序列化: %v", p.name, err)
		}
		var got string
		switch p.at {
		case "top":
			got, _ = back["id"].(string)
		case "message":
			msg, _ := back["message"].(map[string]any)
			got, _ = msg["id"].(string)
		}
		if got == "" || got != want {
			t.Fatalf("%s 落盘往返后 id=%q，期望 %q（raw=%s）", p.name, got, want, raw)
		}
	}
}
