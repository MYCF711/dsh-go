package transport

// llm.listProviders / llm.listConfigurableProviders 的官方 wire 入口。
// 真值由宿主注入（internal/llm 的 ProviderAdapter/catalog）；本文件只做严格
// 顶层参数校验与官方数组形状，不做静态列表。

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
)

const (
	MethodLLMListProviders             Method = "llm.listProviders"
	MethodLLMListConfigurableProviders Method = "llm.listConfigurableProviders"
)

type OfficialProviderInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type OfficialProviderNativeAuth struct {
	Env   []string `json:"env"`
	OAuth bool     `json:"oauth"`
}

type OfficialConfigurableProvider struct {
	Provider       string                      `json:"provider"`
	DisplayName    string                      `json:"displayName"`
	SettingsNS     string                      `json:"settingsNs"`
	SettingsPath   []string                    `json:"settingsPath"`
	Declared       bool                        `json:"declared,omitempty"`
	DefaultBaseURL string                      `json:"defaultBaseURL,omitempty"`
	BaseURLEnv     []string                    `json:"baseURLEnv,omitempty"`
	NativeAuth     *OfficialProviderNativeAuth `json:"nativeAuth,omitempty"`
	Error          string                      `json:"error,omitempty"`
}

type OfficialLLMProviderAPI interface {
	ListOfficialProviders(ctx context.Context) ([]OfficialProviderInfo, error)
	ListOfficialConfigurableProviders(ctx context.Context) ([]OfficialConfigurableProvider, error)
}

var officialLLMProviderMount = struct {
	mu sync.RWMutex
	m  map[*Server]OfficialLLMProviderAPI
}{m: map[*Server]OfficialLLMProviderAPI{}}

func (s *Server) AttachOfficialLLMProviderAPI(api OfficialLLMProviderAPI) {
	if s == nil {
		return
	}
	officialLLMProviderMount.mu.Lock()
	defer officialLLMProviderMount.mu.Unlock()
	officialLLMProviderMount.m[s] = api
}

func (s *Server) DetachOfficialLLMProviderAPI() {
	officialLLMProviderMount.mu.Lock()
	defer officialLLMProviderMount.mu.Unlock()
	delete(officialLLMProviderMount.m, s)
}

func (s *Server) officialLLMProviderAPI() (OfficialLLMProviderAPI, bool) {
	officialLLMProviderMount.mu.RLock()
	defer officialLLMProviderMount.mu.RUnlock()
	api, ok := officialLLMProviderMount.m[s]
	return api, ok
}

func IsOfficialLLMProviderMethod(name string) bool {
	return Method(name) == MethodLLMListProviders || Method(name) == MethodLLMListConfigurableProviders
}

func (s *Server) ServeOfficialLLMProviderAPI(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/")
	if !IsOfficialLLMProviderMethod(name) {
		writeJSON(w, http.StatusNotFound, errorBodyFor("not_found", "不是 llm provider 方法"))
		return
	}
	api, ok := s.officialLLMProviderAPI()
	if !ok {
		writeOfficialError(w, officialNotImplemented("%s 未接线：宿主没有 AttachOfficialLLMProviderAPI", name))
		return
	}
	if r.Body != nil {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeOfficialError(w, officialBadRequest("读参数失败: %v", err))
			return
		}
		if len(raw) > 0 {
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(raw, &obj); err != nil || len(obj) != 0 {
				writeOfficialError(w, officialBadRequest("%s 不接受参数（官方 parameters: []）", name))
				return
			}
		}
	}
	var (
		result any
		err    error
	)
	switch Method(name) {
	case MethodLLMListProviders:
		result, err = api.ListOfficialProviders(r.Context())
	case MethodLLMListConfigurableProviders:
		result, err = api.ListOfficialConfigurableProviders(r.Context())
	}
	if err != nil {
		var typed *officialError
		if errors.As(err, &typed) {
			writeOfficialError(w, err)
			return
		}
		writeOfficialError(w, officialInternal("%v", err))
		return
	}
	writeJSON(w, http.StatusOK, result)
}
