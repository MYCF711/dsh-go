package main

// pluginMarket 的 rc.2 契约测试：版本行 `update`（官方 listingOf 判据）与
// 私有 Git 仓库详情回退（https 被拒 → SSH 由 RepositoryCache 负责，这里只验编排
// 与四字段错误投影）。
//
// 权威：`dsh-plugin-market/src/index.ts:481-487`、`src/types.ts` 的
// MarketListingVersion.update、`dsh-plugin-market-index/src/index.ts:329-334`
// 的 marketInstallTarget。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"dshgo/internal/market"
	"dshgo/internal/pluginmgr"
	officialpluginmgr "dshgo/internal/pluginmgr/official"
	"dshgo/internal/transport"
)

const (
	testCommitA = "1111111111111111111111111111111111111111"
	testCommitB = "2222222222222222222222222222222222222222"
)

// marketIndexServer 起一个真实 HTTP 索引源；body 是索引 JSON。
func marketIndexServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func newTestMarketAPI(t *testing.T, bundles []pluginmgr.BundleState, repositories marketRepositoryReader) (*pluginMarketAPI, string) {
	t.Helper()
	home := t.TempDir()
	api := &pluginMarketAPI{store: market.Open(home)}
	if bundles != nil {
		api.bundles = func() ([]pluginmgr.BundleState, error) { return bundles, nil }
	}
	api.repositories = repositories
	return api, home
}

// TestMarketInstallSpecMatchesOfficial：官方 marketInstallTarget 的三条形状
// （npm 名+版本；git 补 .git 后缀；path 用 `&path:/<dir>`）。
func TestMarketInstallSpecMatchesOfficial(t *testing.T) {
	cases := []struct {
		name    string
		version market.Version
		want    string
		ok      bool
	}{
		{"npm-row", market.Version{Version: "2.0.0", Source: &market.Source{Type: "npm"}}, "acme.audit@2.0.0", true},
		{"git-row", market.Version{Version: "2.0.0", Source: &market.Source{Type: "git", URL: "https://example.com/o/r", Commit: testCommitA}},
			"git+https://example.com/o/r.git#" + testCommitA, true},
		{"git-row-path", market.Version{Version: "2.0.0", Source: &market.Source{Type: "git", URL: "https://example.com/o/r.git", Commit: testCommitA, Path: "packages/a"}},
			"git+https://example.com/o/r.git#" + testCommitA + "&path:/packages/a", true},
		{"git-missing-commit", market.Version{Version: "2.0.0", Source: &market.Source{Type: "git", URL: "https://example.com/o/r"}}, "", false},
		{"git-missing-url", market.Version{Version: "2.0.0", Source: &market.Source{Type: "git", Commit: testCommitA}}, "", false},
		{"no-source", market.Version{Version: "2.0.0"}, "", false},
		{"unknown-type", market.Version{Version: "2.0.0", Source: &market.Source{Type: "tarball"}}, "", false},
	}
	for _, testCase := range cases {
		got, ok := marketInstallSpec("acme.audit", testCase.version)
		if ok != testCase.ok || got != testCase.want {
			t.Fatalf("%s: marketInstallSpec = (%q, %v)，期望 (%q, %v)", testCase.name, got, ok, testCase.want, testCase.ok)
		}
	}
}

// TestMarketVersionUpdatableThreeStates：官方三态（无来源 / registry 副本 /
// git 副本）下 `update` 的取值。
//
// 固定口径（官方 index.ts:481-487）：
//   - 只有 `pluginManager` 把副本列成 `update:'git'` 时才有 origin；
//   - registry 装的副本 update 恒 false —— "发行版有新版本"由
//     installation.status=different-version 表达，不塞进 update；
//   - 索引没有 source 时判不了同仓库，update=false（缺字段，不是官方不支持）。
func TestMarketVersionUpdatableThreeStates(t *testing.T) {
	gitBundle := pluginmgr.BundleState{Name: "acme.audit", Version: "1.0.0",
		Source: "git+https://example.com/o/r.git#" + testCommitA}
	gitRow := market.Version{Version: "2.0.0", Source: &market.Source{Type: "git", URL: "https://example.com/o/r", Commit: testCommitB}}
	cases := []struct {
		name    string
		version market.Version
		bundle  pluginmgr.BundleState
		has     bool
		want    bool
	}{
		{"同仓库另一 commit 且更新 → true", gitRow, gitBundle, true, true},
		{"同版本不更新 → false", market.Version{Version: "1.0.0", Source: gitRow.Source}, gitBundle, true, false},
		{"更旧版本 → false", market.Version{Version: "0.9.0", Source: gitRow.Source}, gitBundle, true, false},
		{"不同仓库 → false", market.Version{Version: "2.0.0", Source: &market.Source{Type: "git", URL: "https://example.com/other/repo", Commit: testCommitB}}, gitBundle, true, false},
		{"同仓库不同 path 目录 → false", market.Version{Version: "2.0.0", Source: &market.Source{Type: "git", URL: "https://example.com/o/r", Commit: testCommitB, Path: "packages/a"}}, gitBundle, true, false},
		{"索引缺 source → false（缺字段）", market.Version{Version: "2.0.0"}, gitBundle, true, false},
		{"registry 副本 → false（官方只认 git 副本）", market.Version{Version: "3.0.0", Source: &market.Source{Type: "npm"}},
			pluginmgr.BundleState{Name: "acme.audit", Version: "1.0.0", Source: "acme.audit@1.0.0"}, true, false},
		{"未安装 → false", gitRow, pluginmgr.BundleState{}, false, false},
		{"已装副本不是完整 commit（短 commit）→ false", gitRow,
			pluginmgr.BundleState{Name: "acme.audit", Version: "1.0.0", Source: "git+https://example.com/o/r.git#abc1234"}, true, false},
	}
	for _, testCase := range cases {
		got := marketVersionUpdatable("acme.audit", testCase.version, testCase.bundle, testCase.has)
		if got != testCase.want {
			t.Fatalf("%s: update = %v，期望 %v", testCase.name, got, testCase.want)
		}
	}
}

// TestMarketSearchRowsAlwaysCarryUpdate：版本行的 `update` 必须**总是显式下发**
// （不是 omitempty）——缺席会被客户端读成"老宿主，别显示升级按钮"。
func TestMarketSearchRowsAlwaysCarryUpdate(t *testing.T) {
	index := `{"version":1,"title":{"en":"Market"},"description":{"en":"plugins"},"plugins":[
	  {"name":"acme.audit","title":{"en":"Audit"},"description":{"en":"audit"},"keywords":[],
	   "versions":[
	     {"version":"2.0.0","source":{"type":"git","url":"https://example.com/o/r","commit":"` + testCommitB + `"}},
	     {"version":"3.0.0","source":{"type":"npm"}}]},
	  {"name":"acme.plain","title":{"en":"Plain"},"description":{"en":"plain"},
	   "versions":[{"version":"1.0.0"}]}]}`
	server := marketIndexServer(t, index)
	bundles := []pluginmgr.BundleState{{Name: "acme.audit", Version: "1.0.0",
		Source: "git+https://example.com/o/r.git#" + testCommitA}}
	api, _ := newTestMarketAPI(t, bundles, nil)
	if err := api.store.SetURLs([]string{server.URL + "/index.json"}); err != nil {
		t.Fatal(err)
	}
	result, err := api.Search(context.Background(), server.URL+"/index.json", transport.OfficialMarketQuery{
		Limit: 50, Sort: map[string]any{"kind": "index"}})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	found := map[string]transport.OfficialMarketPlugin{}
	for _, plugin := range result.Plugins {
		found[plugin.Name] = plugin
	}
	audit, ok := found["acme.audit"]
	if !ok || len(audit.Versions) != 2 {
		t.Fatalf("acme.audit 版本行缺失：%+v", found)
	}
	if !audit.Versions[0].Update {
		t.Fatalf("同仓库另一 commit 的新版本必须 update=true：%+v", audit.Versions[0])
	}
	if audit.Versions[1].Update {
		t.Fatalf("npm 版本行不许 update=true（官方只认 git 副本）：%+v", audit.Versions[1])
	}
	if plain := found["acme.plain"]; len(plain.Versions) != 1 || plain.Versions[0].Update {
		t.Fatalf("缺 source 的索引行 update 必须是 false：%+v", plain.Versions)
	}

	// wire 上必须有 `"update":false` 这个键（不能靠 omitempty 消失）。
	raw, err := json.Marshal(audit.Versions[1])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"update":false`) {
		t.Fatalf("update 必须显式下发：%s", raw)
	}
}

// fakeRepositoryReader 是仓库读取的最小替身：只验 market 侧编排，真实 git 与
// SSH 回退的证据在 internal/pluginmgr/official/repository_cache_test.go。
type fakeRepositoryReader struct {
	files     map[string]string
	failures  error
	requested []string
}

func (f *fakeRepositoryReader) ReadFile(_ context.Context, url, commit, name string) ([]byte, error) {
	f.requested = append(f.requested, url+"#"+commit+":"+name)
	if f.failures != nil {
		return nil, f.failures
	}
	if content, ok := f.files[name]; ok {
		return []byte(content), nil
	}
	return nil, &officialpluginmgr.RepositoryFailure{Reason: "invalid", Message: name + " 不存在"}
}

const privateIndex = `{"version":1,"title":{"en":"Market"},"description":{"en":"plugins"},"plugins":[
  {"name":"acme.private","title":{"en":"Private"},"description":{"en":"private"},
   "versions":[{"version":"2.0.0","source":{"type":"git","url":"https://example.com/secret.git","commit":"` + testCommitB + `"}}]},
  {"name":"acme.public","keywords":[],
   "versions":[{"version":"1.0.0","source":{"type":"npm"}}]}]}`

// TestMarketReadEntryDetailFallsBackToRepository：索引没有内联 README 时，按
// 这一版声明的 git 来源读仓库 README（origin=repository）。
func TestMarketReadEntryDetailFallsBackToRepository(t *testing.T) {
	server := marketIndexServer(t, privateIndex)
	reader := &fakeRepositoryReader{files: map[string]string{"README.md": "# 私有插件\n"}}
	api, _ := newTestMarketAPI(t, nil, reader)
	if err := api.store.SetURLs([]string{server.URL + "/index.json"}); err != nil {
		t.Fatal(err)
	}
	detail, err := api.ReadEntryDetail(context.Background(), server.URL+"/index.json", "acme.private", "2.0.0", "en")
	if err != nil {
		t.Fatalf("详情回退失败：%v", err)
	}
	if detail.Status != "found" || detail.Origin != "repository" || !strings.Contains(detail.Markdown, "私有插件") {
		t.Fatalf("detail = %+v", detail)
	}
	if len(reader.requested) == 0 || !strings.Contains(reader.requested[0], testCommitB) {
		t.Fatalf("仓库读取没有带版本钉住的 commit：%v", reader.requested)
	}
}

// TestMarketReadEntryDetailRepositoryFailureIsFourField：仓库读取失败必须回
// 结构化四字段错误（码 detail_unavailable + reason + 两条诊断），不许假成功。
func TestMarketReadEntryDetailRepositoryFailureIsFourField(t *testing.T) {
	server := marketIndexServer(t, privateIndex)
	reader := &fakeRepositoryReader{failures: &officialpluginmgr.RepositoryFailure{
		Reason:  "denied",
		Message: "https://example.com/secret.git: git fetch: Authentication failed\nssh://git@example.com/secret.git: git fetch: Permission denied (publickey)",
	}}
	api, _ := newTestMarketAPI(t, nil, reader)
	if err := api.store.SetURLs([]string{server.URL + "/index.json"}); err != nil {
		t.Fatal(err)
	}
	_, err := api.ReadEntryDetail(context.Background(), server.URL+"/index.json", "acme.private", "2.0.0", "en")
	if err == nil {
		t.Fatal("仓库被拒必须报错")
	}
	var marketErr *transport.PluginMarketError
	if !errors.As(err, &marketErr) {
		t.Fatalf("错误必须是 *transport.PluginMarketError：%v", err)
	}
	if marketErr.Code != "detail_unavailable" || marketErr.Status != http.StatusBadGateway {
		t.Fatalf("错误码/状态 = %q/%d", marketErr.Code, marketErr.Status)
	}
	if !strings.Contains(marketErr.Message, "denied") || !strings.Contains(marketErr.Message, "ssh") {
		t.Fatalf("失败信息必须带原因与 SSH 回退口径：%s", marketErr.Message)
	}
}

// TestMarketReadEntryDetailNoGitSourceIsNone：没有 git 来源的版本没有仓库可读，
// 如实回 status=none（不是错误，也不是假文档）。
func TestMarketReadEntryDetailNoGitSourceIsNone(t *testing.T) {
	server := marketIndexServer(t, privateIndex)
	reader := &fakeRepositoryReader{files: map[string]string{"README.md": "# 不该被读到\n"}}
	api, _ := newTestMarketAPI(t, nil, reader)
	if err := api.store.SetURLs([]string{server.URL + "/index.json"}); err != nil {
		t.Fatal(err)
	}
	detail, err := api.ReadEntryDetail(context.Background(), server.URL+"/index.json", "acme.public", "1.0.0", "en")
	if err != nil {
		t.Fatalf("npm 版本没有仓库回退：%v", err)
	}
	if detail.Status != "none" {
		t.Fatalf("detail = %+v", detail)
	}
	if len(reader.requested) != 0 {
		t.Fatalf("npm 版本不该去读 git 仓库：%v", reader.requested)
	}
}
