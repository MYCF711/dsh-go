package transport

// pluginMarket 4 个方法与 pluginRegistryProbe.fastest 的 L1 契约测试。
//
// 权威：`@deepseek-ai/dsh-plugin-market/lib/typert.host.js`（MarketSearchResult /
// MarketIndexSummary / PluginDetail）与 `dsh-client-ui-plugin-manager/lib/
// typert.host.js`（fastest → string | null）。
//
// 这里钉三件事：
//  1. **结果字段逐个对**：search 有且只有官方的 7 个顶层字段（没有 index/plugins/
//     offset 这些自造字段），entries/versions 的子形状也是官方的；
//  2. **union 形状**：title 是 string | localized；nextOffset 末页是显式 null；
//     compatibility 是对象不是 bool；source 只带该分支的字段；
//  3. **参数严格性**：缺必填 / 未知字段 / 类型与范围不符一律 400（官方 strictObject）。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

type fakeMarketAPI struct {
	err            error
	result         OfficialMarketSearchResult
	fastest        string
	gotQuery       OfficialMarketQuery
	gotURL         string
	gotIndexes     []string
	gotDetail      []string
	fastestCalls   int
	detailResult   OfficialMarketEntryDetail
	indexes        []OfficialMarketIndex
	lastIndexCalls int
}

func (f *fakeMarketAPI) ListIndexes(context.Context) ([]OfficialMarketIndex, error) {
	f.lastIndexCalls++
	return f.indexes, f.err
}

func (f *fakeMarketAPI) SetIndexes(_ context.Context, urls []string) error {
	f.gotIndexes = urls
	return f.err
}

func (f *fakeMarketAPI) Search(_ context.Context, url string, query OfficialMarketQuery) (OfficialMarketSearchResult, error) {
	f.gotURL = url
	f.gotQuery = query
	return f.result, f.err
}

func (f *fakeMarketAPI) ReadEntryDetail(_ context.Context, url, name, version, language string) (OfficialMarketEntryDetail, error) {
	f.gotDetail = []string{url, name, version, language}
	return f.detailResult, f.err
}

func (f *fakeMarketAPI) Fastest(context.Context) (string, error) {
	f.fastestCalls++
	return f.fastest, f.err
}

// marketHTTP 起一个真实 /api 服务（含 cookie 认证），返回 base 与 cookie。
func marketHTTP(t *testing.T, attach func(*Server)) (string, string, *Server) {
	t.Helper()
	s, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if attach != nil {
		attach(s)
	}
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return ts.URL, s.MintInternalCookie(), s
}

func postMarket(t *testing.T, base, cookie, method, body string) (int, map[string]json.RawMessage) {
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

// marketKeys 是 JSON 对象的键集合（排好序，便于逐字比对）。
func marketKeys(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("不是 JSON 对象：%v（%s）", err, raw)
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// objectKeys 返回一个已解码 JSON 对象的键（排好序）。
func objectKeys(t *testing.T, object map[string]json.RawMessage) []string {
	t.Helper()
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// mustObject 把一个字段解码成 JSON 对象。
func mustObject(t *testing.T, object map[string]json.RawMessage, key string) map[string]json.RawMessage {
	t.Helper()
	var out map[string]json.RawMessage
	marketDecode(t, mustRaw(t, object, key), &out)
	return out
}

func marketDecode(t *testing.T, raw json.RawMessage, target any) {
	t.Helper()
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("解码失败：%v（%s）", err, raw)
	}
}

// sampleSearchResult 是一页"官方形状"的结果：两条 entry，一条装了、一条没装。
func sampleSearchResult() OfficialMarketSearchResult {
	exempted := false
	return OfficialMarketSearchResult{
		URL:   "https://market.example/index.json",
		Title: OfficialLocalizedText{"en": "Market", "zh": "市场"},
		Metadata: OfficialMarketIndexMetadata{
			Categories: map[string]OfficialMarketDeclaration{"tools": {Title: OfficialLocalizedText{"en": "Tools"}}},
			Labels:     map[string]OfficialMarketDeclaration{"official": {Title: OfficialLocalizedText{"en": "Official"}}},
			SortKeys:   map[string]OfficialMarketSortKey{"stars": {Title: OfficialLocalizedText{"en": "Stars"}, Order: "desc"}},
		},
		FetchedAt: 1762000000123,
		Entries: []OfficialMarketEntry{{
			Name:     "acme.audit",
			Title:    OfficialLocalizedText{"en": "Audit"},
			Metadata: OfficialMarketPluginMetadata{Category: "tools", Keywords: []string{"security"}, Labels: []string{"official"}, SortKeys: map[string]float64{"stars": 7}},
			Versions: []OfficialMarketListingVersion{{
				Version:          "2.0.0",
				PublishTimestamp: 1762000000,
				Source:           OfficialMarketSource{Type: "git", URL: "https://example.com/o/r", Commit: strings.Repeat("a", 40)},
				Install:          OfficialMarketInstall{Spec: "git+https://example.com/o/r.git#" + strings.Repeat("a", 40)},
				Engines:          OfficialMarketEngines{DSH: "^0.2.0"},
				Compatibility: OfficialMarketCompatibility{Compatible: false, RuntimeVersion: "9.8.0-test",
					Requirements: map[string]string{"engines.dsh": "^0.3.0"}, Exempted: &exempted},
				Update: true,
			}},
			DefaultVersion: "2.0.0",
			Installation:   OfficialMarketInstallation{Status: "installed", InstalledVersion: "1.0.0"},
		}},
		Total:      1,
		NextOffset: nil,
	}
}

func TestOfficialMarketSearchResultHasOfficialFieldsOnly(t *testing.T) {
	api := &fakeMarketAPI{result: sampleSearchResult()}
	base, cookie, _ := marketHTTP(t, func(s *Server) { s.AttachOfficialPluginMarketAPI(api) })
	code, envelope := postMarket(t, base, cookie, "pluginMarket.search",
		`{"url":"https://market.example/index.json","query":{"sort":{"kind":"index"},"offset":0,"limit":50}}`)
	if code != http.StatusOK {
		t.Fatalf("search = %d %+v", code, envelope)
	}
	if _, ok := envelope["error"]; ok {
		t.Fatalf("成功响应里不许有 error：%+v", envelope)
	}
	// 顶层**有且只有**官方这 7 个字段（description 可选，这里没给）。
	got := strings.Join(marketKeys(t, envelope["result"]), ",")
	want := "entries,fetchedAt,metadata,nextOffset,title,total,url"
	if got != want {
		t.Fatalf("search 顶层字段 = [%s]，官方要 [%s]", got, want)
	}
	var result map[string]json.RawMessage
	marketDecode(t, envelope["result"], &result)
	if string(result["nextOffset"]) != "null" {
		t.Fatalf("末页 nextOffset 必须显式 null（缺席与 null 语义不同）：%s", result["nextOffset"])
	}
	if string(result["url"]) != `"https://market.example/index.json"` {
		t.Fatalf("url = %s", result["url"])
	}
	// metadata 三组声明都要在，客户端靠它画分类/标签/排序键。
	metadata := strings.Join(marketKeys(t, result["metadata"]), ",")
	if metadata != "categories,labels,sortKeys" {
		t.Fatalf("metadata 三组 = [%s]", metadata)
	}
	var categories map[string]OfficialMarketDeclaration
	marketDecode(t, result["metadata"], &struct {
		Categories *map[string]OfficialMarketDeclaration `json:"categories"`
	}{Categories: &categories})
	if title := categories["tools"].Title["en"]; title != "Tools" {
		t.Fatalf("metadata.categories 丢了解析结果：%+v", categories)
	}
	// entries 的形状：官方子字段一个不少。
	var entries []json.RawMessage
	marketDecode(t, result["entries"], &entries)
	if len(entries) != 1 {
		t.Fatalf("entries = %d", len(entries))
	}
	rawEntry := entries[0]
	var entryObject map[string]json.RawMessage
	marketDecode(t, rawEntry, &entryObject)
	entryFields := strings.Join(objectKeys(t, entryObject), ",")
	for _, field := range []string{"name", "metadata", "versions", "installation", "defaultVersion"} {
		if !strings.Contains(entryFields, field) {
			t.Fatalf("entry 缺官方字段 %q：[%s]", field, entryFields)
		}
	}
	firstVersion := firstEntry(t, mustRaw(t, entryObject, "versions"))
	versionFields := strings.Join(objectKeys(t, firstVersion), ",")
	wantVersion := "compatibility,engines,install,publishTimestamp,source,update,version"
	if versionFields != wantVersion {
		t.Fatalf("版本行字段 = [%s]，官方要 [%s]", versionFields, wantVersion)
	}
	if string(firstVersion["update"]) != "true" {
		t.Fatalf("update 必须显式下发：%s", firstVersion["update"])
	}
	if string(firstVersion["publishTimestamp"]) != "1762000000" {
		t.Fatalf("publishTimestamp 必须是 Unix 秒数：%s", firstVersion["publishTimestamp"])
	}
	// compatibility 是 union 对象，不是 bool。
	compatibilityRaw := firstVersion["compatibility"]
	if trimmed := strings.TrimSpace(string(compatibilityRaw)); trimmed == "true" || trimmed == "false" {
		t.Fatalf("compatibility 必须是对象：%s", compatibilityRaw)
	}
	var compatibility OfficialMarketCompatibility
	marketDecode(t, compatibilityRaw, &compatibility)
	if compatibility.Compatible || compatibility.Exempted == nil || *compatibility.Exempted {
		t.Fatalf("compatibility = %+v", compatibility)
	}
	// source 只带 git 分支的字段（npm 分支的 registry 不许漏出来）。
	if source := strings.Join(objectKeys(t, mustObject(t, firstVersion, "source")), ","); source != "commit,type,url" {
		t.Fatalf("git source 字段 = [%s]", source)
	}
	// install 只有 spec（这一版没声明 registry）。
	if install := strings.Join(objectKeys(t, mustObject(t, firstVersion, "install")), ","); install != "spec" {
		t.Fatalf("install 字段 = [%s]", install)
	}
	// 参数投影正确。
	if api.gotURL != "https://market.example/index.json" || api.gotQuery.Limit != 50 ||
		api.gotQuery.Sort.Kind != "index" || api.gotQuery.Offset != 0 {
		t.Fatalf("宿主参数 = %q %+v", api.gotURL, api.gotQuery)
	}
}

func mustRaw(t *testing.T, object map[string]json.RawMessage, key string) json.RawMessage {
	t.Helper()
	raw, ok := object[key]
	if !ok {
		t.Fatalf("缺字段 %q", key)
	}
	return raw
}

func firstEntry(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var list []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("不是 JSON 数组：%v（%s）", err, raw)
	}
	if len(list) == 0 {
		t.Fatalf("数组为空：%s", raw)
	}
	return list[0]
}

// TestOfficialMarketListIndexesTitleUnion：title 可以是字符串，也可以是对象
// （官方 union），两种都下发得出去。
func TestOfficialMarketListIndexesTitleUnion(t *testing.T) {
	api := &fakeMarketAPI{indexes: []OfficialMarketIndex{{
		URL:   "https://m/index.json",
		Title: OfficialLocalizedText{"en": "Market"},
	}, {
		URL:         "https://m/other.json",
		Description: OfficialLocalizedText{"en": "d"},
		Error:       "HTTP 500",
	}}}
	base, cookie, _ := marketHTTP(t, func(s *Server) { s.AttachOfficialPluginMarketAPI(api) })
	code, envelope := postMarket(t, base, cookie, "pluginMarket.listIndexes", `{}`)
	if code != http.StatusOK {
		t.Fatalf("listIndexes = %d %+v", code, envelope)
	}
	var items []map[string]json.RawMessage
	marketDecode(t, envelope["result"], &items)
	if len(items) != 2 {
		t.Fatalf("items = %d", len(items))
	}
	// 单语言 title 按 union 的字符串分支下发。
	if string(items[0]["title"]) != `"Market"` {
		t.Fatalf("单语言 title 应为字符串：%s", items[0]["title"])
	}
	if _, ok := items[1]["title"]; ok {
		t.Fatalf("没有 title 的索引不该下发 title 键：%+v", items[1])
	}
	if string(items[1]["error"]) != `"HTTP 500"` {
		t.Fatalf("error = %s", items[1]["error"])
	}
}

// TestOfficialMarketSetIndexesIsVoid：官方结果是 z.void()，宿主回 result:null。
func TestOfficialMarketSetIndexesIsVoid(t *testing.T) {
	api := &fakeMarketAPI{}
	base, cookie, _ := marketHTTP(t, func(s *Server) { s.AttachOfficialPluginMarketAPI(api) })
	code, envelope := postMarket(t, base, cookie, "pluginMarket.setIndexes", `{"urls":["https://a/i.json","https://b/i.json"]}`)
	if code != http.StatusOK {
		t.Fatalf("setIndexes = %d %+v", code, envelope)
	}
	// 官方是 z.void()；本仓成功信封不接受 null，用空对象表达"没有载荷"。
	if trimmed := strings.TrimSpace(string(envelope["result"])); trimmed != "{}" {
		t.Fatalf("void 结果必须是空对象（本仓不接受 result:null）：%s", envelope["result"])
	}
	if len(api.gotIndexes) != 2 {
		t.Fatalf("宿主没收到索引源：%v", api.gotIndexes)
	}
}

// TestOfficialRegistryProbeFastestNoArgs：**无参数**、结果 `string | null`。
func TestOfficialRegistryProbeFastestNoArgs(t *testing.T) {
	api := &fakeMarketAPI{fastest: "https://registry.npmjs.org/"}
	base, cookie, _ := marketHTTP(t, func(s *Server) { s.AttachOfficialPluginRegistryProbeAPI(api) })
	code, envelope := postMarket(t, base, cookie, "pluginRegistryProbe.fastest", ``)
	if code != http.StatusOK {
		t.Fatalf("fastest = %d %+v", code, envelope)
	}
	if string(envelope["result"]) != `"https://registry.npmjs.org/"` {
		t.Fatalf("fastest 结果必须是 registry URL 字符串：%s", envelope["result"])
	}
	// 都没响应 → 官方 null；不许回 {registry,latencyMs} 这种自造对象。
	api.fastest = ""
	code, envelope = postMarket(t, base, cookie, "pluginRegistryProbe.fastest", `{}`)
	if code != http.StatusOK || string(envelope["result"]) != "null" {
		t.Fatalf("无可用 registry 要 result:null，拿到 %d %s", code, envelope["result"])
	}
	// 官方没有参数：多给一个字段就是客户端接错，400 而不是忽略。
	if code, _ := postMarket(t, base, cookie, "pluginRegistryProbe.fastest", `{"urls":["https://a"]}`); code != http.StatusBadRequest {
		t.Fatalf("带参数必须 400，拿到 %d", code)
	}
}

// TestOfficialMarketRejectsBadParams：官方 strictObject 的严格性（原媒体组测试
// 里对 pluginMarket.* 的这批断言搬到这里，一条不少）。
func TestOfficialMarketRejectsBadParams(t *testing.T) {
	api := &fakeMarketAPI{}
	base, cookie, _ := marketHTTP(t, func(s *Server) {
		s.AttachOfficialPluginMarketAPI(api)
		s.AttachOfficialPluginRegistryProbeAPI(api)
	})
	cases := []struct{ method, body, why string }{
		{"pluginMarket.listIndexes", `{"x":1}`, "listIndexes 无参数"},
		{"pluginMarket.search", `{"query":{"sort":{"kind":"index"},"offset":0,"limit":1}}`, "缺 url"},
		{"pluginMarket.search", `{"url":""}`, "url 空串"},
		{"pluginMarket.search", `{"url":"u"}`, "缺 query"},
		{"pluginMarket.search", `{"url":"u","query":{"offset":0,"limit":1}}`, "缺 sort"},
		{"pluginMarket.search", `{"url":"u","query":{"sort":{"kind":"index"}}}`, "缺 offset/limit"},
		{"pluginMarket.search", `{"url":"u","query":{"sort":{"kind":"index"},"offset":0}}`, "缺 limit"},
		{"pluginMarket.search", `{"url":"u","query":{"sort":{"kind":"index"},"offset":-1,"limit":1}}`, "offset 为负"},
		{"pluginMarket.search", `{"url":"u","query":{"sort":{"kind":"index"},"offset":0,"limit":0}}`, "limit 太小"},
		{"pluginMarket.search", `{"url":"u","query":{"sort":{"kind":"index"},"offset":0,"limit":101}}`, "limit 太大"},
		{"pluginMarket.search", `{"url":"u","query":{"sort":{"kind":"index"},"offset":0.5,"limit":1}}`, "offset 不是整数"},
		{"pluginMarket.search", `{"url":"u","query":{"sort":"index","offset":0,"limit":1}}`, "sort 不是对象"},
		{"pluginMarket.search", `{"url":"u","query":{"sort":{"kind":"hot"},"offset":0,"limit":1}}`, "sort.kind 非法"},
		{"pluginMarket.search", `{"url":"u","query":{"sort":{"kind":"published"},"offset":0,"limit":1}}`, "published 缺 direction"},
		{"pluginMarket.search", `{"url":"u","query":{"sort":{"kind":"key","direction":"asc"},"offset":0,"limit":1}}`, "key 缺 id"},
		{"pluginMarket.search", `{"url":"u","query":{"sort":{"kind":"index"},"offset":0,"limit":1,"labels":["a",2]}}`, "labels 元素不是字符串"},
		{"pluginMarket.search", `{"url":"u","query":{"sort":{"kind":"index"},"offset":0,"limit":1,"refresh":"yes"}}`, "refresh 不是布尔"},
		{"pluginMarket.search", `{"url":"u","query":{"sort":{"kind":"index"},"offset":0,"limit":1,"nope":true}}`, "query 有未知字段"},
		{"pluginMarket.search", `{"url":"u","query":{"sort":{"kind":"index"},"offset":0,"limit":1},"junk":1}`, "顶层未知字段"},
		{"pluginMarket.readEntryDetail", `{"name":"n","version":"1","language":"zh"}`, "缺 url"},
		{"pluginMarket.readEntryDetail", `{"url":"u","version":"1","language":"zh"}`, "缺 name"},
		{"pluginMarket.readEntryDetail", `{"url":"u","name":"n","language":"zh"}`, "缺 version"},
		{"pluginMarket.readEntryDetail", `{"url":"u","name":"n","version":"1"}`, "缺 language"},
		{"pluginMarket.readEntryDetail", `{"url":"u","name":"n","version":"1","language":"zh","options":{"npmFileSources":{"a":1}}}`, "npmFileSources 不是数组"},
		{"pluginMarket.readEntryDetail", `{"url":"u","name":"n","version":"1","language":"zh","options":{"npmFileSources":["cdn"]}}`, "npmFileSources 取值非法"},
		{"pluginMarket.readEntryDetail", `{"url":"u","name":"n","version":"1","language":"zh","options":{"refresh":"x"}}`, "refresh 不是布尔"},
		{"pluginMarket.readEntryDetail", `{"url":"u","name":"n","version":"1","language":"zh","junk":1}`, "未知字段"},
		{"pluginMarket.setIndexes", `{}`, "缺 urls"},
		{"pluginMarket.setIndexes", `{"urls":"https://a"}`, "urls 不是数组"},
		{"pluginMarket.setIndexes", `{"urls":[1]}`, "元素不是字符串"},
		{"pluginMarket.setIndexes", `{"urls":[],"x":1}`, "未知字段"},
	}
	for _, testCase := range cases {
		code, envelope := postMarket(t, base, cookie, testCase.method, testCase.body)
		if code != http.StatusBadRequest {
			t.Fatalf("%s（%s）要 400，拿到 %d %+v", testCase.why, testCase.body, code, envelope)
		}
		if _, ok := envelope["result"]; ok {
			t.Fatalf("%s 400 响应里不许有 result：%+v", testCase.why, envelope)
		}
	}
	// 合法参数必须放行（不是"全拒"）。
	valid := []struct{ method, body string }{
		{"pluginMarket.search", `{"url":"u","query":{"text":"lsp","sort":{"kind":"relevance"},"offset":0,"limit":50}}`},
		{"pluginMarket.search", `{"url":"u","query":{"sort":{"kind":"key","id":"stars","direction":"desc"},"offset":2,"limit":10,"labels":["official"],"refresh":true,"fetchedAt":1762000000}}`},
		{"pluginMarket.readEntryDetail", `{"url":"u","name":"n","version":"1","language":"zh","options":{"refresh":true,"npmFileSources":["jsdelivr"]}}`},
		{"pluginMarket.setIndexes", `{"urls":[]}`},
	}
	for _, testCase := range valid {
		if code, envelope := postMarket(t, base, cookie, testCase.method, testCase.body); code != http.StatusOK {
			t.Fatalf("%s 合法参数要 200，拿到 %d %+v", testCase.body, code, envelope)
		}
	}
}

// TestOfficialMarketUnwiredIsStructured503：没有装配真实实现时是结构化 503
// （四个字段），不是 501、也不是空成功。
func TestOfficialMarketUnwiredIsStructured503(t *testing.T) {
	base, cookie, _ := marketHTTP(t, nil)
	for _, method := range []string{"pluginMarket.listIndexes", "pluginMarket.search", "pluginRegistryProbe.fastest"} {
		code, envelope := postMarket(t, base, cookie, method, `{}`)
		if code != http.StatusServiceUnavailable {
			t.Fatalf("%s 未接线要 503，拿到 %d %+v", method, code, envelope)
		}
		var failure struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			Hint      string `json:"hint"`
			Retryable bool   `json:"retryable"`
		}
		marketDecode(t, mustRaw(t, envelope, "error"), &failure)
		if failure.Code != "registry_unavailable" || failure.Message == "" || failure.Hint == "" {
			t.Fatalf("%s 503 必须是四字段错误：%+v", method, failure)
		}
	}
}
