package transport

import (
	"net/http"
	"sync/atomic"
	"testing"

	"dshgo/internal/settingsform"
)

func newCotPreferencesTestServer(t *testing.T) (*Server, *settingsform.Service) {
	t.Helper()
	docs := defaultTestSettings()
	docs[settingsform.NamespaceCotTranslation] = settingsform.Document{Base: map[string]any{
		settingsform.FieldCotTranslationProvider:       settingsform.DefaultCotTranslationProvider,
		settingsform.FieldCotTranslationTargetLanguage: settingsform.DefaultCotTranslationTargetLanguage,
	}}
	service := settingsform.NewService(newTestSettingsStore(t, docs), settingsform.HostRegistry())
	return &Server{opt: Options{CotTranslationSettings: service}}, service
}

func TestCotTranslationLimitsAndTranslateShareLiveSettings(t *testing.T) {
	s, settings := newCotPreferencesTestServer(t)
	revision := int64(0)
	mutation, err := settings.Mutate(settingsform.NamespaceCotTranslation, []settingsform.PathOp{
		{Op: "set", Path: []string{settingsform.FieldCotTranslationProvider}, Value: cotProviderDSAccount},
		{Op: "set", Path: []string{settingsform.FieldCotTranslationTargetLanguage}, Value: "ja"},
	}, &revision)
	if err != nil {
		t.Fatal(err)
	}

	code, out := parityServe(t, s.ServeOfficialCotTranslationLimitsAPI, "cotTranslation.limits", `{}`)
	if code != http.StatusOK {
		t.Fatalf("limits HTTP=%d：%v", code, out)
	}
	preferences := out["preferences"].(map[string]any)
	if preferences["provider"] != cotProviderDSAccount || preferences["targetLanguage"] != "ja" {
		t.Fatalf("设置写入后 limits 未刷新：%v", preferences)
	}

	var gatewayCalls int32
	mmFakeGateway(t, "こんにちは", &gatewayCalls, http.StatusOK)
	for _, payload := range []string{
		`{"request":{"text":"你好","targetLanguage":"ja","provider":"deepseek-official"}}`,
		`{"request":{"text":"你好","targetLanguage":"en","provider":"deepseek-account"}}`,
	} {
		code, body := mmServe(t, s, "cotTranslation.translate", payload)
		if code != http.StatusBadRequest {
			t.Fatalf("过期偏好必须 400，实际 %d：%s", code, body)
		}
	}
	if got := atomic.LoadInt32(&gatewayCalls); got != 0 {
		t.Fatalf("过期偏好不该打网关，实际 %d 次", got)
	}
	code, body := mmServe(t, s, "cotTranslation.translate",
		`{"request":{"text":"你好","targetLanguage":"ja","provider":"deepseek-account"}}`)
	if code != http.StatusOK {
		t.Fatalf("当前偏好应真实翻译，HTTP=%d：%s", code, body)
	}
	if got := atomic.LoadInt32(&gatewayCalls); got != 1 {
		t.Fatalf("有效请求应恰好打一次网关，实际 %d", got)
	}

	// reset 后下一场 limits 立即回到真实默认，translate 省略 provider 也走该默认。
	resetRevision := mutation.Namespace.Revision
	if _, err := settings.Reset(settingsform.NamespaceCotTranslation, nil, &resetRevision); err != nil {
		t.Fatal(err)
	}
	_, out = parityServe(t, s.ServeOfficialCotTranslationLimitsAPI, "cotTranslation.limits", `{}`)
	preferences = out["preferences"].(map[string]any)
	if preferences["provider"] != cotProviderDSOfficial || preferences["targetLanguage"] != "auto" {
		t.Fatalf("reset 后 limits=%v", preferences)
	}
	code, body = mmServe(t, s, "cotTranslation.translate", `{"request":{"text":"你好","targetLanguage":"en"}}`)
	if code != http.StatusOK {
		t.Fatalf("reset 后默认路线应可用，HTTP=%d：%s", code, body)
	}
}
