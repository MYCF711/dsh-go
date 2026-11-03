package transport

import (
	"encoding/json"
	"net/http"
	"testing"
)

// fakeSrc 是一个可控的 Source：想看哪条字段就让它返回什么。
type fakeSrc struct {
	msgs  []Message
	model string
}

func (f fakeSrc) Messages(string) ([]Message, error) { return f.msgs, nil }

// modelSrc 带 model：用独立字段，避免和外层 fakeSrc 的字段名打架。
type modelSrc struct {
	src   fakeSrc
	model string
}

func (m modelSrc) Messages(id string) ([]Message, error) { return m.src.Messages(id) }
func (m modelSrc) Model(string) (string, error)          { return m.model, nil }

// H5：load 返回的是**这一进程真实的 policy**，不是 mode、不是 preset。
//
// 判据写死成四种取值：界面只认这四个词，多一个就得猜。
func TestLoadCarriesProcessPolicy(t *testing.T) {
	for _, p := range []string{"deny", "ask", "allow", "auto"} {
		s := newTestServer(t, Options{Policy: p, Load: emptyLoad})
		rec := postJSON(t, s, "/api/session.load", `{"id":"s1"}`)
		var env struct {
			Result struct {
				Permission string `json:"permission"`
			} `json:"result"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("%s：响应不是 JSON", p)
		}
		if env.Result.Permission != p {
			t.Fatalf("该报 %q，实际 %q", p, env.Result.Permission)
		}
	}
}

// H3：`messages[].view` 要原样透传；没有 view 时字段整个省略（不是空对象）。
//
// 空对象会让界面以为"有 view 但是空的"，于是去渲染一个空卡片。
func TestLoadPassesViewThrough(t *testing.T) {
	src := fakeSrc{msgs: []Message{
		{Seq: 1, Role: "tool", Text: "有视图", View: json.RawMessage(`{"kind":"table"}`)},
		{Seq: 2, Role: "assistant", Text: "没有视图"},
	}}
	s := newTestServer(t, Options{
		Load: func(string, int) (LoadResult, error) {
			return LoadSession(src, "s1", 10)
		},
	})
	rec := postJSON(t, s, "/api/session.load", `{"id":"s1"}`)

	var env struct {
		Result struct {
			Messages []struct {
				View *json.RawMessage `json:"view"`
			} `json:"messages"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("响应不是 JSON：%v", err)
	}
	if len(env.Result.Messages) != 2 {
		t.Fatalf("应有 2 条消息，实际 %d", len(env.Result.Messages))
	}
	if env.Result.Messages[0].View == nil {
		t.Fatalf("第一条的 view 丢了")
	}
	if env.Result.Messages[1].View != nil {
		t.Fatalf("没有 view 的那条不该出现该字段")
	}
}

// H4：model 来自宿主，不认识就省略——**不许拿默认值冒充**。
func TestLoadCarriesModelOrOmitsIt(t *testing.T) {
	withModel := modelSrc{model: "group/deepseek-v4.1-flash1017"}
	s := newTestServer(t, Options{
		Load: func(string, int) (LoadResult, error) { return LoadSession(withModel, "s1", 10) },
	})
	if body := postJSON(t, s, "/api/session.load", `{"id":"s1"}`).Body.String(); !stringsContains(body, "deepseek") {
		t.Fatalf("model 没带上：%s", body)
	}

	// 没有 model 的 Source：字段必须不出现。
	s2 := newTestServer(t, Options{
		Load: func(string, int) (LoadResult, error) { return LoadSession(fakeSrc{}, "s1", 10) },
	})
	if body := postJSON(t, s2, "/api/session.load", `{"id":"s1"}`).Body.String(); stringsContains(body, `"model"`) {
		t.Fatalf("不知道 model 时不该编一个：%s", body)
	}
}

func stringsContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

var _ = http.StatusOK
