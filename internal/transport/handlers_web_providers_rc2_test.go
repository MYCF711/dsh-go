package transport

// `web.listProviders` 的 wire 层测试（官方 0.2.2-rc.2 `dsh-web`）。
//
// 官方契约：方法 `web/listProviders`、`parameters: []`、结果
// `{search: [{id,title?,description?}], fetch: [...]}`
// （`dsh-web/lib/typert.host.js:27-41`、`dsh-web/src/types.ts:196-217`）。
//
// 这里只验协议层：形状、严格无参、未接线时的四字段未实现、数组不许是 null。
// 清单真值推导在 `modules/tool/websearch/providers_test.go`（真接缝 + 真环境）。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type webProviderFake struct {
	list OfficialWebProviderList
	err  error
}

func (f webProviderFake) ListOfficialWebProviders(context.Context) (OfficialWebProviderList, error) {
	return f.list, f.err
}

func available(value bool) *bool { return &value }

func fakeWebProviders() OfficialWebProviderList {
	return OfficialWebProviderList{
		Search: []OfficialWebProviderInfo{
			{ID: "deepseek-search-api", Available: available(true), Default: true, Selected: true},
			{ID: "deepseek-official", Available: available(false),
				Description: "Slower, and every search uses extra model tokens."},
		},
		Fetch: []OfficialWebProviderInfo{{ID: "http", Available: available(true)}},
		Resolution: &OfficialWebProviderResolution{
			Search: OfficialWebProviderSelection{Configured: "", Source: "auto", Selected: "deepseek-search-api"},
			Fetch:  OfficialWebProviderSelection{Configured: "http", Source: "env", Selected: "http"},
		},
		Baseline: &OfficialWebProviderBaseline{
			Search: "deepseek-search-api", SearchRegistered: true,
			Fetch: "http", FetchRegistered: true,
		},
		Limits: &OfficialWebSearchLimits{MaxQueries: 10, MaxResults: 10, SummaryMaxChars: 800, SearchTimeoutMS: 60000},
		Credentials: []OfficialWebCredential{
			{Name: "DEEPSEEK_API_KEY", Configured: true, Role: "credential", Capability: "search"},
		},
	}
}

func serveWebListProviders(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/web.listProviders", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.ServeOfficialWebProviderAPI(w, req)
	return w
}

// 官方形状：search / fetch 两个数组，每条 id 必有，title/description 可缺席。
func TestOfficialWebListProvidersShape(t *testing.T) {
	s := &Server{}
	s.AttachOfficialWebProviderAPI(webProviderFake{list: fakeWebProviders()})
	defer s.DetachOfficialWebProviderAPI()

	w := serveWebListProviders(t, s, "{}")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var envelope struct {
		Result struct {
			Search      []map[string]any `json:"search"`
			Fetch       []map[string]any `json:"fetch"`
			Resolution  map[string]any   `json:"resolution"`
			Baseline    map[string]any   `json:"baseline"`
			Limits      map[string]any   `json:"limits"`
			Credentials []map[string]any `json:"credentials"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("body 不是官方 `{result: …}` 外壳: %v %s", err, w.Body.String())
	}
	result := envelope.Result
	if len(result.Search) != 2 || len(result.Fetch) != 1 {
		t.Fatalf("search/fetch 条数不对: %+v", result)
	}
	if got := result.Search[0]["id"]; got != "deepseek-search-api" {
		t.Fatalf("id 必须原样透出；实际 %v", got)
	}
	// 没有 title 的条目不许出现 title 键（官方"没声明就缺席"）。
	if _, ok := result.Search[0]["title"]; ok {
		t.Fatalf("没声明 title 的 provider 不该有 title 键: %+v", result.Search[0])
	}
	if got, ok := result.Search[1]["description"]; !ok || got == "" {
		t.Fatalf("provider 声明了 description 就必须透出: %+v", result.Search[1])
	}
	// 宿主附加字段（本仓"能力下限：只能更多不能更少"）。
	if result.Search[0]["available"] != true || result.Search[0]["default"] != true {
		t.Fatalf("available/default 必须透出: %+v", result.Search[0])
	}
	if result.Resolution["search"] == nil || result.Baseline["search"] != "deepseek-search-api" {
		t.Fatalf("resolution/baseline 必须透出: %+v %+v", result.Resolution, result.Baseline)
	}
	if result.Limits["maxQueries"] != float64(10) || result.Limits["maxResults"] != float64(10) {
		t.Fatalf("limits 必须透出 rc.2 的 10/10: %+v", result.Limits)
	}
	if len(result.Credentials) != 1 {
		t.Fatalf("credentials 必须透出: %+v", result.Credentials)
	}
}

// 官方 `parameters: []`：任何未知参数都必须 400，不能静默忽略。
func TestOfficialWebListProvidersRejectsParams(t *testing.T) {
	s := &Server{}
	s.AttachOfficialWebProviderAPI(webProviderFake{list: fakeWebProviders()})
	defer s.DetachOfficialWebProviderAPI()

	for _, body := range []string{`{"searchProvider":"x"}`, `{"query":"x"}`, `[]`, `"x"`} {
		if w := serveWebListProviders(t, s, body); w.Code != http.StatusBadRequest {
			t.Fatalf("参数 %s 必须 400；实际 %d %s", body, w.Code, w.Body.String())
		}
	}
	// 空对象与空 body 都是合法的"无参数"。
	for _, body := range []string{`{}`, ``} {
		if w := serveWebListProviders(t, s, body); w.Code != http.StatusOK {
			t.Fatalf("无参数（%q）必须 200；实际 %d %s", body, w.Code, w.Body.String())
		}
	}
}

// 未接线：结构化未实现（四字段），不许返回空清单假装成功。
func TestOfficialWebListProvidersNotWired(t *testing.T) {
	s := &Server{}
	w := serveWebListProviders(t, s, "{}")
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("未接线必须 501；实际 %d %s", w.Code, w.Body.String())
	}
	// 四字段错误装在 `{"error": {...}}` 里（与 /api 其余方法同一出口）。
	var body struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"code", "message", "hint", "retryable"} {
		if _, ok := body.Error[field]; !ok {
			t.Fatalf("四字段错误缺 %q: %s", field, w.Body.String())
		}
	}
	if body.Error["code"] != "not_implemented" {
		t.Fatalf("code 该是 not_implemented: %v", body.Error["code"])
	}
	// `hint` 是码级通用文案（共享出口给），具体缺哪一行写在 message 里——
	// 未接线时用户看到的必须是可照做的那一行，不是"未实现"三个字。
	if message, _ := body.Error["message"].(string); !strings.Contains(message, "AttachOfficialWebProviderAPI") {
		t.Fatalf("message 要点名缺哪一行接线: %q", message)
	}
	if hint, _ := body.Error["hint"].(string); hint == "" {
		t.Fatal("hint 不能为空（四字段契约）")
	}
}

// 不是 web 方法时 404（不吞掉相邻方法族）。
func TestOfficialWebListProvidersWrongMethod(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/api/web.somethingElse", strings.NewReader("{}"))
	w := httptest.NewRecorder()
	s.ServeOfficialWebProviderAPI(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("非 web 方法必须 404；实际 %d", w.Code)
	}
	if !IsOfficialWebProviderMethod("web.listProviders") || IsOfficialWebProviderMethod("web.other") {
		t.Fatal("IsOfficialWebProviderMethod 判定不对")
	}
}

// 两个数组即使为空也必须序列化成 `[]`（界面按数组渲染，null 会变成"加载失败"）。
func TestOfficialWebListProvidersEmptyArrays(t *testing.T) {
	s := &Server{}
	s.AttachOfficialWebProviderAPI(webProviderFake{list: OfficialWebProviderList{}})
	defer s.DetachOfficialWebProviderAPI()

	w := serveWebListProviders(t, s, "{}")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"search":null`) || strings.Contains(w.Body.String(), `"fetch":null`) {
		t.Fatalf("空清单必须是 [] 而不是 null: %s", w.Body.String())
	}
}

// 真实现返回错误时：非 officialError 收敛成内部错误（四字段），不 200 + 空清单。
func TestOfficialWebListProvidersErrorPropagates(t *testing.T) {
	s := &Server{}
	s.AttachOfficialWebProviderAPI(webProviderFake{err: errors.New("接缝装配失败：DSHD_SEARCH_URL 非法")})
	defer s.DetachOfficialWebProviderAPI()

	w := serveWebListProviders(t, s, "{}")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("实现报错必须 500；实际 %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"result"`) && !strings.Contains(w.Body.String(), `"code"`) {
		t.Fatalf("错误形状不对: %s", w.Body.String())
	}
}
