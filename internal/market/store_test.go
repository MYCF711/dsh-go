package market

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func indexServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version":     1,
			"title":       map[string]string{"en": "Market"},
			"description": map[string]string{"en": "plugins"},
			"plugins": []map[string]any{
				{"name": "acme.audit", "title": map[string]string{"en": "Audit"}, "description": map[string]string{"en": "security audit"},
					"author": "alice", "keywords": []string{"security"}, "category": "tools", "labels": []string{"official"},
					"versions": []map[string]any{{"version": "2.0.0", "publishedAt": "2026-11-08T00:00:00Z", "readme": "# Audit\n"}}},
				{"name": "acme.format", "title": map[string]string{"en": "Format"}, "description": map[string]string{"en": "formatter"},
					"author": "bob", "keywords": []string{"format"}, "category": "tools", "labels": []string{},
					"versions": []map[string]any{{"version": "1.0.0", "publishedAt": "2026-11-07T00:00:00Z"}}},
			},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestStorePersistenceSearchAndDetail(t *testing.T) {
	home := t.TempDir()
	srv := indexServer(t)
	s := Open(home)
	s.Client = srv.Client()
	if _, err := s.List(context.Background()); err != ErrNoIndexes {
		t.Fatalf("空索引源应 ErrNoIndexes，得到 %v", err)
	}
	if err := s.SetURLs([]string{srv.URL + "/index.json"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "state", "plugin-market-indexes.json")); err != nil {
		t.Fatalf("索引源没落盘：%v", err)
	}
	reopened := Open(home)
	reopened.Client = srv.Client()
	if got := reopened.ListURLs(); len(got) != 1 || got[0] != srv.URL+"/index.json" {
		t.Fatalf("重开丢失索引源：%v", got)
	}
	list, err := reopened.List(context.Background())
	if err != nil || len(list) != 1 || list[0].Title["en"] != "Market" {
		t.Fatalf("list = %+v, err=%v", list, err)
	}
	res, err := reopened.Search(context.Background(), srv.URL+"/index.json", Query{Text: "audit", Offset: 0, Limit: 1, Sort: map[string]any{"kind": "index"}})
	if err != nil || res.Total != 1 || len(res.Plugins) != 1 || res.Plugins[0].Name != "acme.audit" {
		t.Fatalf("search = %+v, err=%v", res, err)
	}
	res, err = reopened.Search(context.Background(), srv.URL+"/index.json", Query{Category: "tools", Offset: 1, Limit: 1, Sort: map[string]any{"kind": "published", "direction": "desc"}})
	if err != nil || res.Total != 2 || len(res.Plugins) != 1 || res.Plugins[0].Name != "acme.format" || res.NextOffset != nil {
		t.Fatalf("分页/排序 = %+v, err=%v", res, err)
	}
	text, found, err := reopened.ReadEntryDetail(context.Background(), srv.URL+"/index.json", "acme.audit", "2.0.0", "en")
	if err != nil || !found || text != "# Audit\n" {
		t.Fatalf("detail = %q found=%v err=%v", text, found, err)
	}
	if _, found, _ := reopened.ReadEntryDetail(context.Background(), srv.URL+"/index.json", "nope", "1.0.0", "en"); found {
		t.Fatal("不存在的条目不能 found")
	}
	if err := reopened.SetURLs([]string{"ftp://bad"}); err == nil {
		t.Fatal("非 http(s) URL 必须拒绝")
	}
}
