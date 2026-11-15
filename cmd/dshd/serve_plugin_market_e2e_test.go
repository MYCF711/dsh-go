package main

// pluginMarket / pluginRegistryProbe 的**端到端**用例：真 HTTP 打 /api/<method>，
// 装配的是生产实现（internal/market 的真索引源 + serve_plugin_market.go），不是替身。
//
// 这一层要证明的正是 GUI #30 依赖的那几件事：
//   - `pluginMarket.search` 的 result 是官方 MarketSearchResult 形状（7 个顶层字段，
//     没有 index/plugins/offset 这些自造字段），entries[].versions 行带
//     install/compatibility/update；
//   - 索引文档的 `metadata.categories/labels/sortKeys` 被解析出来（分类菜单/标签/
//     排序键的数据源）；
//   - `pluginMarket.listIndexes` 的 title 是官方 `string | localized`；
//   - `pluginRegistryProbe.fastest` 无参数，回最先响应的 registry 或 null。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"dshgo/internal/pluginmgr"
	"dshgo/internal/transport"
)

const e2eMarketCommitA = "1111111111111111111111111111111111111111"
const e2eMarketCommitB = "2222222222222222222222222222222222222222"

// e2eMarketIndex 是一份官方 format 1 索引：两个包，一个 npm 来源（含一个不兼容
// 版本与一个预发布版本）、一个 git 来源（可升级）。
const e2eMarketIndex = `{
  "format": 1,
  "title": {"en": "Official Market", "zh": "官方市场"},
  "description": {"en": "plugins"},
  "metadata": {
    "categories": {"tools": {"title": {"en": "Tools"}}},
    "labels": {"official": {"title": {"en": "Official"}}},
    "sortKeys": {"stars": {"title": {"en": "Stars"}, "order": "desc"}}
  },
  "plugins": [
    {"name": "acme.audit", "title": {"en": "Audit"}, "author": "alice",
     "metadata": {"category": "tools", "keywords": ["security"], "labels": ["official"], "sortKeys": {"stars": 9}},
     "versions": [
       {"version": "2.0.0", "publishTimestamp": 1762000000, "source": {"type": "npm"}, "engines": {"dsh": "^0.3.0"}},
       {"version": "2.1.0-beta.1", "publishTimestamp": 1762100000, "source": {"type": "npm"}},
       {"version": "1.5.0", "publishTimestamp": 1761500000, "source": {"type": "npm"}}
     ]},
    {"name": "acme.git", "title": {"en": "Git bundle"},
     "metadata": {"keywords": [], "labels": []},
     "versions": [
       {"version": "2.0.0", "publishTimestamp": 1761900000,
        "source": {"type": "git", "url": "https://example.com/o/r", "commit": "` + e2eMarketCommitB + `"}}
     ]}
  ]
}`

// e2eMarketServer 起一个真实 HTTP 索引源，并让 openE2EMarketAPI 的 store 指向它。
func e2eMarketServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

// e2eMarketEnv 装好 transport.Server（真 /api 服务 + cookie 认证）与生产实现。
func e2eMarketEnv(t *testing.T, bundles []pluginmgr.BundleState) (string, string, *pluginMarketAPI) {
	t.Helper()
	api := newPluginMarketAPI(t.TempDir(), nil)
	if bundles != nil {
		api.bundles = func() ([]pluginmgr.BundleState, error) { return bundles, nil }
	}
	srv, err := transport.NewServer(transport.Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.AttachOfficialPluginMarketAPI(api)
	srv.AttachOfficialPluginRegistryProbeAPI(api)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts.URL, srv.MintInternalCookie(), api
}

func e2eMarketPost(t *testing.T, base, cookie, method, body string) (int, map[string]json.RawMessage) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, base+"/api/"+method, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: "dshd_session", Value: cookie})
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST %s: %v", method, err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("%s 响应不是 JSON 对象：%v（%s）", method, err, raw)
	}
	return response.StatusCode, envelope
}

// TestPluginMarketSearchEndToEnd 是 GUI #30 A/B 两块的宿主门：真 HTTP + 真索引源。
func TestPluginMarketSearchEndToEnd(t *testing.T) {
	index := e2eMarketServer(t, e2eMarketIndex)
	bundles := []pluginmgr.BundleState{
		{Name: "acme.audit", Version: "1.0.0", Source: "acme.audit@1.0.0"},
		{Name: "acme.git", Version: "1.0.0", Source: "git+https://example.com/o/r.git#" + e2eMarketCommitA},
	}
	base, cookie, api := e2eMarketEnv(t, bundles)
	if err := api.store.SetURLs([]string{index.URL + "/index.json"}); err != nil {
		t.Fatal(err)
	}

	code, envelope := e2eMarketPost(t, base, cookie, "pluginMarket.search",
		`{"url":"`+index.URL+`/index.json","query":{"sort":{"kind":"index"},"offset":0,"limit":10}}`)
	if code != http.StatusOK {
		t.Fatalf("search = %d %+v", code, envelope)
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(envelope["result"], &result); err != nil {
		t.Fatalf("result 不是对象：%v（%s）", err, envelope["result"])
	}

	// 官方 7 个顶层字段一个不能少（description 是可选的第 8 个），多出自造字段也红。
	requiredFields := []string{"entries", "fetchedAt", "metadata", "nextOffset", "title", "total", "url"}
	for _, field := range requiredFields {
		if _, ok := result[field]; !ok {
			t.Fatalf("缺官方字段 %q：%s", field, envelope["result"])
		}
	}
	allowedFields := map[string]bool{"description": true}
	for _, field := range requiredFields {
		allowedFields[field] = true
	}
	for field := range result {
		if !allowedFields[field] {
			t.Fatalf("多出自造字段 %q（官方 MarketSearchResult 没有它）：%s", field, envelope["result"])
		}
	}
	if string(result["nextOffset"]) != "null" {
		t.Fatalf("末页 nextOffset 必须是 null：%s", result["nextOffset"])
	}
	if string(result["total"]) != "2" {
		t.Fatalf("total = %s", result["total"])
	}
	var fetchedAt int64
	if err := json.Unmarshal(result["fetchedAt"], &fetchedAt); err != nil || fetchedAt <= 0 {
		t.Fatalf("fetchedAt 必须是毫秒整数：%s（%v）", result["fetchedAt"], err)
	}

	// metadata 三组声明：分类/标签/排序键必须被解析出来（GUI 工具栏的数据源）。
	var metadata struct {
		Categories map[string]struct {
			Title json.RawMessage `json:"title"`
		} `json:"categories"`
		Labels map[string]struct {
			Title json.RawMessage `json:"title"`
		} `json:"labels"`
		SortKeys map[string]struct {
			Title json.RawMessage `json:"title"`
			Order string          `json:"order"`
		} `json:"sortKeys"`
	}
	if err := json.Unmarshal(result["metadata"], &metadata); err != nil {
		t.Fatalf("metadata 解不开：%v（%s）", err, result["metadata"])
	}
	// 声明标题是单语言 → 按官方 union 的字符串分支下发。
	if string(metadata.Categories["tools"].Title) != `"Tools"` {
		t.Fatalf("metadata.categories 没解析出来：%s", result["metadata"])
	}
	if string(metadata.Labels["official"].Title) != `"Official"` {
		t.Fatalf("metadata.labels 没解析出来：%s", result["metadata"])
	}
	if metadata.SortKeys["stars"].Order != "desc" {
		t.Fatalf("metadata.sortKeys 没解析出来：%s", result["metadata"])
	}
	// 索引 title 是多语言 → 按官方 union 的对象分支下发。
	if !strings.Contains(string(result["title"]), `"zh":"官方市场"`) || !strings.Contains(string(result["title"]), `"en":"Official Market"`) {
		t.Fatalf("title = %s", result["title"])
	}

	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(result["entries"], &entries); err != nil || len(entries) != 2 {
		t.Fatalf("entries = %s（%v）", result["entries"], err)
	}
	audit := entries[0]
	if string(audit["name"]) != `"acme.audit"` {
		t.Fatalf("entries[0] = %s", audit["name"])
	}
	// defaultVersion = 最高兼容且稳定的版本：2.0.0 不兼容（engines.dsh ^0.3.0）、
	// 2.1.0-beta.1 是预发布且包里有稳定版 → 1.5.0。
	if string(audit["defaultVersion"]) != `"1.5.0"` {
		t.Fatalf("defaultVersion = %s（期望 1.5.0）", audit["defaultVersion"])
	}
	// 已装 1.0.0、默认版本 1.5.0 → update-available。
	var installation transport.OfficialMarketInstallation
	if err := json.Unmarshal(audit["installation"], &installation); err != nil ||
		installation.Status != "update-available" || installation.InstalledVersion != "1.0.0" {
		t.Fatalf("installation = %s（%v）", audit["installation"], err)
	}
	var versions []transport.OfficialMarketListingVersion
	if err := json.Unmarshal(audit["versions"], &versions); err != nil || len(versions) != 3 {
		t.Fatalf("versions 解不开（%v）：%s", err, audit["versions"])
	}
	// 版本行按 SemVer 降序：2.1.0-beta.1 排在 2.0.0 前（预发布参与比较）。
	if versions[0].Version != "2.1.0-beta.1" {
		t.Fatalf("版本降序不对：%s", audit["versions"])
	}
	var incompatible transport.OfficialMarketListingVersion
	for _, version := range versions {
		if version.Version == "2.0.0" {
			incompatible = version
		}
	}
	if incompatible.Compatibility.Compatible {
		t.Fatalf("engines.dsh=^0.3.0 在这个宿主上必须判不兼容：%s", audit["versions"])
	}
	if incompatible.Compatibility.RuntimeVersion != pluginmgr.HostRuntimeVersion {
		t.Fatalf("compatibility.runtimeVersion = %q，宿主版本 %q",
			incompatible.Compatibility.RuntimeVersion, pluginmgr.HostRuntimeVersion)
	}
	if incompatible.Compatibility.Requirements["engines.dsh"] != "^0.3.0" {
		t.Fatalf("compatibility.requirements = %+v", incompatible.Compatibility.Requirements)
	}
	if incompatible.Compatibility.Exempted == nil || *incompatible.Compatibility.Exempted {
		t.Fatalf("没有豁免时必须显式 exempted=false：%s", audit["versions"])
	}
	// npm 版本：install.spec = name@version，source 是 npm 分支。
	var npmVersion transport.OfficialMarketListingVersion
	for _, version := range versions {
		if version.Version == "1.5.0" {
			npmVersion = version
		}
	}
	if npmVersion.Install.Spec != "acme.audit@1.5.0" {
		t.Fatalf("install.spec = %q", npmVersion.Install.Spec)
	}
	if npmVersion.Source.Type != "npm" || npmVersion.Source.URL != "" {
		t.Fatalf("npm source = %+v", npmVersion.Source)
	}
	// update 只认 git 副本：npm 装的副本恒 false。
	for _, version := range versions {
		if version.Update {
			t.Fatalf("registry 副本不该 update=true：%+v", version)
		}
	}

	// git 来源的包：同一仓库另一 commit → update=true，install.spec 是钉住 commit 的
	// git+https 形式。
	gitEntry := entries[1]
	if string(gitEntry["name"]) != `"acme.git"` {
		t.Fatalf("entries[1] = %s", gitEntry["name"])
	}
	var gitVersions []transport.OfficialMarketListingVersion
	if err := json.Unmarshal(gitEntry["versions"], &gitVersions); err != nil || len(gitVersions) != 1 {
		t.Fatalf("git versions = %s（%v）", gitEntry["versions"], err)
	}
	gitVersion := gitVersions[0]
	if !gitVersion.Update {
		t.Fatalf("同仓库另一 commit 的新版本必须 update=true：%+v", gitVersion)
	}
	wantSpec := "git+https://example.com/o/r.git#" + e2eMarketCommitB
	if gitVersion.Install.Spec != wantSpec {
		t.Fatalf("git install.spec = %q，期望 %q", gitVersion.Install.Spec, wantSpec)
	}
	if gitVersion.Source.Type != "git" || gitVersion.Source.Commit != e2eMarketCommitB {
		t.Fatalf("git source = %+v", gitVersion.Source)
	}
}

// TestPluginMarketListIndexesEndToEnd：listIndexes 走真索引源，title 按官方 union
// 下发（多语言 → 对象）。
func TestPluginMarketListIndexesEndToEnd(t *testing.T) {
	index := e2eMarketServer(t, e2eMarketIndex)
	base, cookie, api := e2eMarketEnv(t, nil)
	if err := api.store.SetURLs([]string{index.URL + "/index.json"}); err != nil {
		t.Fatal(err)
	}
	code, envelope := e2eMarketPost(t, base, cookie, "pluginMarket.listIndexes", `{}`)
	if code != http.StatusOK {
		t.Fatalf("listIndexes = %d %+v", code, envelope)
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(envelope["result"], &items); err != nil || len(items) != 1 {
		t.Fatalf("listIndexes = %s（%v）", envelope["result"], err)
	}
	if string(items[0]["url"]) != `"`+index.URL+`/index.json"` {
		t.Fatalf("url = %s", items[0]["url"])
	}
	if !strings.Contains(string(items[0]["title"]), `"en":"Official Market"`) {
		t.Fatalf("title = %s", items[0]["title"])
	}
	if _, ok := items[0]["error"]; ok {
		t.Fatalf("读成功的索引不该带 error：%+v", items[0])
	}
}

// TestPluginMarketSetIndexesEndToEnd：setIndexes 真写盘（不是假装成功），非法 URL 是
// 调用方参数问题（400），不是上游不可用（502）。
func TestPluginMarketSetIndexesEndToEnd(t *testing.T) {
	index := e2eMarketServer(t, e2eMarketIndex)
	base, cookie, api := e2eMarketEnv(t, nil)
	url := index.URL + "/index.json"
	code, envelope := e2eMarketPost(t, base, cookie, "pluginMarket.setIndexes", `{"urls":["`+url+`"]}`)
	if code != http.StatusOK {
		t.Fatalf("setIndexes = %d %+v", code, envelope)
	}
	if trimmed := strings.TrimSpace(string(envelope["result"])); trimmed != "{}" {
		t.Fatalf("官方是 void；本仓用空对象表达没有载荷：%s", envelope["result"])
	}
	// 真落盘：内存里的索引源列表与磁盘文件都要变。
	if got := api.store.ListURLs(); len(got) != 1 || got[0] != url {
		t.Fatalf("索引源没写进去：%v", got)
	}
	// 写完就能 listIndexes 读到真实标题。
	code, envelope = e2eMarketPost(t, base, cookie, "pluginMarket.listIndexes", `{}`)
	if code != http.StatusOK || !strings.Contains(string(envelope["result"]), "Official Market") {
		t.Fatalf("写完读不回来：%d %s", code, envelope["result"])
	}
	// 非法 URL → 400（官方 plugin-market/invalid-index-url 同一类），不是 502。
	code, envelope = e2eMarketPost(t, base, cookie, "pluginMarket.setIndexes", `{"urls":["ftp://bad/index.json"]}`)
	if code != http.StatusBadRequest {
		t.Fatalf("非法索引 URL 要 400，拿到 %d %+v", code, envelope)
	}
	if _, ok := envelope["result"]; ok {
		t.Fatalf("400 响应里不许有 result：%+v", envelope)
	}
}

// TestPluginRegistryProbeFastestEndToEnd：无参数，回**最先响应**那个 registry 的
// origin URL；都不响应 → null。官方两个 ping 地址在测试里换成两个本地端点。
func TestPluginRegistryProbeFastestEndToEnd(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(slow.Close)
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(fast.Close)
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(broken.Close)

	base, cookie, api := e2eMarketEnv(t, nil)
	api.probeEndpoints = []string{slow.URL + "/-/ping", fast.URL + "/-/ping"}
	api.probeClient = &http.Client{Timeout: 2 * time.Second}

	code, envelope := e2eMarketPost(t, base, cookie, "pluginRegistryProbe.fastest", ``)
	if code != http.StatusOK {
		t.Fatalf("fastest = %d %+v", code, envelope)
	}
	if want := `"` + fast.URL + `/` + `"`; string(envelope["result"]) != want {
		t.Fatalf("fastest = %s，期望最先响应的 %s", envelope["result"], want)
	}

	// 都不成功 → 官方 null（不是 0/空对象）。
	api.probeEndpoints = []string{broken.URL + "/-/ping", broken.URL + "/-/ping"}
	code, envelope = e2eMarketPost(t, base, cookie, "pluginRegistryProbe.fastest", `{}`)
	if code != http.StatusOK || string(envelope["result"]) != "null" {
		t.Fatalf("都不响应要 result:null，拿到 %d %s", code, envelope["result"])
	}
}
