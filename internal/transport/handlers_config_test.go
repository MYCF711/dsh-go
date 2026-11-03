package transport

import (
	"net/http"
	"strings"
	"testing"
)

// 空配置必须是 {} 不是 null：界面拿到 null 会当成"没有配置"，拿到 {} 才是"配置是空的"。
func TestConfigGetEmptyIsObject(t *testing.T) {
	s := newTestServer(t, Options{ConfigGet: func() (map[string]string, error) { return nil, nil }})
	rec := postJSON(t, s, "/api/config.get", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("要 200，拿到 %d", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); !strings.Contains(got, `"values":{}`) {
		t.Fatalf("空配置应当是 {}，实际 %s", got)
	}
}

// 空 values 是参数错：静默成功会让界面以为"保存了"，其实什么都没写。
func TestConfigSetRejectsEmptyValues(t *testing.T) {
	s := newTestServer(t, Options{ConfigSet: func(map[string]string) error { t.Fatal("不该被调用"); return nil }})
	if rec := postJSON(t, s, "/api/config.set", `{"values":{}}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("空 values 要 400，拿到 %d", rec.Code)
	}
}

func TestConfigSetReportsCount(t *testing.T) {
	var got map[string]string
	s := newTestServer(t, Options{ConfigSet: func(kv map[string]string) error { got = kv; return nil }})
	rec := postJSON(t, s, "/api/config.set", `{"values":{"theme":"dark","lang":"zh"}}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"updated":2`) {
		t.Fatalf("要 200 且 updated:2，拿到 %d %s", rec.Code, rec.Body.String())
	}
	if len(got) != 2 || got["theme"] != "dark" {
		t.Fatalf("值没传对：%v", got)
	}
}

func TestConfigUnwiredIs501(t *testing.T) {
	s := newTestServer(t, Options{})
	for _, p := range []string{"/api/config.get", "/api/config.set"} {
		if rec := postJSON(t, s, p, `{"values":{"a":"b"}}`); rec.Code != http.StatusNotImplemented {
			t.Fatalf("%s 未接线要 501，拿到 %d", p, rec.Code)
		}
	}
}
