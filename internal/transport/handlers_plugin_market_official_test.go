package transport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeMarketAPI struct{ err error }

func (f fakeMarketAPI) ListIndexes(context.Context) ([]OfficialMarketIndex, error) {
	return []OfficialMarketIndex{{URL: "https://m/index.json", Title: map[string]string{"en": "M"}}}, f.err
}
func (f fakeMarketAPI) SetIndexes(context.Context, []string) error { return f.err }
func (f fakeMarketAPI) Search(context.Context, string, OfficialMarketQuery) (OfficialMarketSearchResult, error) {
	return OfficialMarketSearchResult{Index: OfficialMarketIndex{URL: "https://m/index.json"}, Plugins: []OfficialMarketPlugin{{Name: "acme.audit"}}, Total: 1, FetchedAt: 1}, f.err
}
func (f fakeMarketAPI) ReadEntryDetail(context.Context, string, string, string, string) (OfficialMarketEntryDetail, error) {
	return OfficialMarketEntryDetail{Status: "found", Origin: "readme", Markdown: "# hi\n"}, f.err
}
func (f fakeMarketAPI) Fastest(context.Context, []string) (OfficialRegistryProbeResult, error) {
	return OfficialRegistryProbeResult{Registry: "https://registry.example", LatencyMs: 1}, f.err
}

func TestOfficialPluginMarketHTTP(t *testing.T) {
	s, err := NewServer(Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.AttachOfficialPluginMarketAPI(fakeMarketAPI{})
	s.AttachOfficialPluginRegistryProbeAPI(fakeMarketAPI{})
	t.Cleanup(s.DetachOfficialPluginMarketAPI)
	t.Cleanup(s.DetachOfficialPluginRegistryProbeAPI)
	cases := []struct{ method, body string }{
		{"pluginMarket.listIndexes", `{}`},
		{"pluginMarket.setIndexes", `{"urls":["https://m/index.json"]}`},
		{"pluginMarket.search", `{"url":"https://m/index.json","query":{"sort":{"kind":"index"},"offset":0,"limit":1}}`},
		{"pluginMarket.readEntryDetail", `{"url":"https://m/index.json","name":"acme.audit","version":"1.0.0","language":"en"}`},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodPost, "/api/"+c.method, strings.NewReader(c.body))
		rec := httptest.NewRecorder()
		s.ServeOfficialPluginMarketAPI(rec, req, c.method)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"result"`) {
			t.Fatalf("%s = %d %s", c.method, rec.Code, rec.Body.String())
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/api/pluginRegistryProbe.fastest", strings.NewReader(`{"urls":["https://registry.example"]}`))
	rec := httptest.NewRecorder()
	s.ServeOfficialPluginRegistryProbeAPI(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "registry.example") {
		t.Fatalf("fastest = %d %s", rec.Code, rec.Body.String())
	}

	empty, _ := NewServer(Options{})
	req = httptest.NewRequest(http.MethodPost, "/api/pluginMarket.listIndexes", strings.NewReader(`{}`))
	rec = httptest.NewRecorder()
	empty.ServeOfficialPluginMarketAPI(rec, req, "pluginMarket.listIndexes")
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "registry_unavailable") {
		t.Fatalf("unwired = %d %s", rec.Code, rec.Body.String())
	}
}
