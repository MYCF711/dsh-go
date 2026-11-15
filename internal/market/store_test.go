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

// officialIndex 是一份**官方 format 1** 索引（`dsh-plugin-market-index` 的
// `parseMarketIndex` 认可的写法）：字符串/对象两种 LocalizedText、声明过的
// categories/labels/sortKeys、带 source 与整数 publishTimestamp 的版本行。
const officialIndex = `{
  "format": 1,
  "title": {"en": "Market", "zh": "市场"},
  "description": {"en": "plugins"},
  "metadata": {
    "categories": {"tools": {"title": {"en": "Tools"}}},
    "labels": {"official": {"title": {"en": "Official"}}},
    "sortKeys": {"stars": {"title": {"en": "Stars"}, "order": "desc"}}
  },
  "plugins": [
    {"name": "acme.audit", "title": "Audit", "description": {"en": "security audit"},
     "author": "alice", "icon": "icons/audit.png",
     "repository": {"url": "https://example.com/acme/audit", "directory": "packages/audit"},
     "metadata": {"category": "tools", "keywords": ["security"], "labels": ["official"], "sortKeys": {"stars": 5}},
     "versions": [
       {"version": "2.0.0", "publishTimestamp": 1762000000, "source": {"type": "npm"},
        "engines": {"dsh": "^0.2.0"}, "readme": "# Audit\n"},
       {"version": "1.0.0", "publishTimestamp": 1761000000, "source": {"type": "npm"}}
     ]},
    {"name": "acme.format", "title": {"en": "Format"}, "description": {"en": "formatter"},
     "author": "bob",
     "metadata": {"keywords": ["format"], "labels": []},
     "versions": [
       {"version": "1.0.0", "publishTimestamp": 1761500000, "source": {"type": "npm"}}
     ]}
  ]
}`

func indexServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(officialIndex))
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
	if err != nil || len(list) != 1 || list[0].Title["en"] != "Market" || list[0].Title["zh"] != "市场" {
		t.Fatalf("list = %+v, err=%v", list, err)
	}
	res, err := reopened.Search(context.Background(), srv.URL+"/index.json", Query{Text: "audit", Offset: 0, Limit: 1, Sort: map[string]any{"kind": "index"}})
	if err != nil || res.Total != 1 || len(res.Plugins) != 1 || res.Plugins[0].Name != "acme.audit" {
		t.Fatalf("search = %+v, err=%v", res, err)
	}
	// 版本行按 SemVer 降序（官方 validator 同一行为）。
	if versions := res.Plugins[0].Versions; len(versions) != 2 || versions[0].Version != "2.0.0" || versions[0].PublishTimestamp != 1762000000 {
		t.Fatalf("版本行 = %+v", res.Plugins[0].Versions)
	}
	// 相对 icon 按索引 URL 解析成绝对 URL（官方 parseMarketIndex 同一行为）。
	if icon := res.Plugins[0].Icon; icon != srv.URL+"/icons/audit.png" {
		t.Fatalf("相对 icon 没解析：%q", icon)
	}
	if repository := res.Plugins[0].Repository; repository == nil || repository.Directory != "packages/audit" {
		t.Fatalf("repository = %+v", res.Plugins[0].Repository)
	}
	res, err = reopened.Search(context.Background(), srv.URL+"/index.json",
		Query{Category: "tools", Offset: 1, Limit: 1, Sort: map[string]any{"kind": "published", "direction": "desc"}})
	if err != nil || res.Total != 1 || len(res.Plugins) != 0 || res.NextOffset != nil {
		t.Fatalf("分类筛选/分页 = %+v, err=%v", res, err)
	}
	res, err = reopened.Search(context.Background(), srv.URL+"/index.json",
		Query{Offset: 1, Limit: 1, Sort: map[string]any{"kind": "published", "direction": "desc"}})
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

// TestStoreParsesMarketMetadata：`metadata.categories/labels/sortKeys` 必须解析出来
// —— GUI 的分类菜单、标签过滤、排序键全靠这三张声明表（缺了它们工具栏无数据）。
func TestStoreParsesMarketMetadata(t *testing.T) {
	home := t.TempDir()
	srv := indexServer(t)
	store := Open(home)
	store.Client = srv.Client()
	if err := store.SetURLs([]string{srv.URL + "/index.json"}); err != nil {
		t.Fatal(err)
	}
	res, err := store.Search(context.Background(), srv.URL+"/index.json", Query{Limit: 10, Sort: map[string]any{"kind": "index"}})
	if err != nil {
		t.Fatal(err)
	}
	metadata := res.Index.Metadata
	if title := metadata.Categories["tools"].Title["en"]; title != "Tools" {
		t.Fatalf("metadata.categories 没解析：%+v", metadata.Categories)
	}
	if title := metadata.Labels["official"].Title["en"]; title != "Official" {
		t.Fatalf("metadata.labels 没解析：%+v", metadata.Labels)
	}
	if declaration := metadata.SortKeys["stars"]; declaration.Order != "desc" || declaration.Title["en"] != "Stars" {
		t.Fatalf("metadata.sortKeys 没解析：%+v", metadata.SortKeys)
	}
	// 插件自己的元数据：分类/标签/排序值，且字符串形式的 title 归一成 {en: …}。
	audit := res.Plugins[0]
	if audit.Metadata.Category != "tools" || len(audit.Metadata.Labels) != 1 || audit.Metadata.SortKeys["stars"] != 5 {
		t.Fatalf("插件 metadata = %+v", audit.Metadata)
	}
	if audit.Title["en"] != "Audit" {
		t.Fatalf("字符串标题没归一：%+v", audit.Title)
	}
	// 按声明排序键排序：没有值的包在两个方向上都排最后（官方规则）。
	sorted, err := store.Search(context.Background(), srv.URL+"/index.json",
		Query{Limit: 10, Sort: map[string]any{"kind": "key", "id": "stars", "direction": "asc"}})
	if err != nil {
		t.Fatal(err)
	}
	if sorted.Plugins[0].Name != "acme.audit" || sorted.Plugins[1].Name != "acme.format" {
		t.Fatalf("排序键排序 = %+v", sorted.Plugins)
	}
	// 标签过滤只认声明过的 id。
	filtered, err := store.Search(context.Background(), srv.URL+"/index.json",
		Query{Limit: 10, Labels: []string{"official"}, Sort: map[string]any{"kind": "index"}})
	if err != nil || filtered.Total != 1 || filtered.Plugins[0].Name != "acme.audit" {
		t.Fatalf("标签过滤 = %+v, err=%v", filtered.Plugins, err)
	}
}

// TestStoreRejectsNonFormat1Index：官方索引必须声明 `format: 1`。本仓早先自造的
// `{version:1,…}` 文档（版本没有 source/publishTimestamp）**产不出官方版本行**，
// 所以按官方口径判非法并带上原因，而不是静默补一个假来源。
func TestStoreRejectsNonFormat1Index(t *testing.T) {
	documents := map[string]string{
		"旧 version:1 形状":       `{"version":1,"title":{"en":"Market"},"plugins":[]}`,
		"format 不是 1":          `{"format":2,"title":{"en":"Market"},"plugins":[]}`,
		"缺 title":              `{"format":1,"plugins":[]}`,
		"版本缺 source":           `{"format":1,"title":{"en":"M"},"plugins":[{"name":"a","versions":[{"version":"1.0.0","publishTimestamp":1}]}]}`,
		"版本缺 publishTimestamp": `{"format":1,"title":{"en":"M"},"plugins":[{"name":"a","versions":[{"version":"1.0.0","source":{"type":"npm"}}]}]}`,
		"未声明的分类":               `{"format":1,"title":{"en":"M"},"plugins":[{"name":"a","metadata":{"category":"tools"},"versions":[{"version":"1.0.0","publishTimestamp":1,"source":{"type":"npm"}}]}]}`,
		"未声明的标签":               `{"format":1,"title":{"en":"M"},"plugins":[{"name":"a","metadata":{"labels":["official"]},"versions":[{"version":"1.0.0","publishTimestamp":1,"source":{"type":"npm"}}]}]}`,
	}
	for name, document := range documents {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(document))
		}))
		store := Open(t.TempDir())
		store.Client = server.Client()
		_, err := store.Search(context.Background(), server.URL+"/index.json", Query{Limit: 10})
		server.Close()
		if err == nil {
			t.Fatalf("%s 必须判索引非法", name)
		}
	}
}

// TestStoreLocalizedTextUnion：官方 LocalizedText 是 `string | {en:…}`，两种写法都认，
// 下发时单语言回字符串、多语言回对象（客户端 union 两种都收）。
func TestStoreLocalizedTextUnion(t *testing.T) {
	var text LocalizedText
	if err := json.Unmarshal([]byte(`"plain"`), &text); err != nil || text["en"] != "plain" {
		t.Fatalf("字符串形式 = %+v, err=%v", text, err)
	}
	raw, err := json.Marshal(LocalizedText{"en": "Only"})
	if err != nil || string(raw) != `"Only"` {
		t.Fatalf("单语言应回字符串：%s (%v)", raw, err)
	}
	raw, err = json.Marshal(LocalizedText{"en": "Only", "zh": "唯一"})
	if err != nil || string(raw) != `{"en":"Only","zh":"唯一"}` {
		t.Fatalf("多语言应回对象：%s (%v)", raw, err)
	}
}
