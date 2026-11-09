package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type llmProviderFake struct{}

func (llmProviderFake) ListOfficialProviders(context.Context) ([]OfficialProviderInfo, error) {
	return []OfficialProviderInfo{{ID: "openai-completions", Name: "OpenAI-compatible"}}, nil
}
func (llmProviderFake) ListOfficialConfigurableProviders(context.Context) ([]OfficialConfigurableProvider, error) {
	return []OfficialConfigurableProvider{{Provider: "openai-completions", DisplayName: "OpenAI-compatible",
		SettingsNS: "llm", SettingsPath: []string{"providers", "openai-completions"},
		NativeAuth: &OfficialProviderNativeAuth{Env: []string{"OPENAI_API_KEY"}, OAuth: false}}}, nil
}

func TestOfficialLLMProviderShapeAndStrictParams(t *testing.T) {
	s := &Server{}
	s.AttachOfficialLLMProviderAPI(llmProviderFake{})
	for _, method := range []string{"llm.listProviders", "llm.listConfigurableProviders"} {
		req := httptest.NewRequest("POST", "/api/"+method, strings.NewReader("{}"))
		w := httptest.NewRecorder()
		s.ServeOfficialLLMProviderAPI(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s status=%d %s", method, w.Code, w.Body.String())
		}
		var envelope struct {
			Result []map[string]any `json:"result"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil || len(envelope.Result) != 1 {
			t.Fatalf("%s body=%s err=%v", method, w.Body.String(), err)
		}
	}
	req := httptest.NewRequest("POST", "/api/llm.listProviders", strings.NewReader(`{"x":1}`))
	w := httptest.NewRecorder()
	s.ServeOfficialLLMProviderAPI(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("官方 parameters:[] 必须拒未知参数，status=%d %s", w.Code, w.Body.String())
	}
}
