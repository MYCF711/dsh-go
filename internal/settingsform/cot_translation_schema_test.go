package settingsform

import (
	"reflect"
	"testing"
)

func TestCotTranslationNamespaceReadWriteResetAndValidation(t *testing.T) {
	service, store := clientUIService(t)
	described, err := service.Describe()
	if err != nil {
		t.Fatal(err)
	}
	view := namespaceViewFor(t, described, NamespaceCotTranslation)
	wantDefaults := map[string]any{
		FieldCotTranslationProvider:       DefaultCotTranslationProvider,
		FieldCotTranslationTargetLanguage: DefaultCotTranslationTargetLanguage,
	}
	if !viewValueEqual(t, view.Value, wantDefaults) || !viewValueEqual(t, view.Base, wantDefaults) {
		t.Fatalf("cot 默认值不等于真实可用路线：value=%v base=%v", view.Value, view.Base)
	}
	section := sectionFor(t, described, NamespaceCotTranslation)
	provider := fieldFor(t, section, FieldCotTranslationProvider)
	if !reflect.DeepEqual(provider.Enum, CotTranslationProviderPreferences) {
		t.Fatalf("provider 枚举/顺序=%v，want %v", provider.Enum, CotTranslationProviderPreferences)
	}

	revision := int64(0)
	mutation, err := service.Mutate(NamespaceCotTranslation, []PathOp{
		{Op: "set", Path: []string{FieldCotTranslationProvider}, Value: "deepseek-account"},
		{Op: "set", Path: []string{FieldCotTranslationTargetLanguage}, Value: "zh-Hans"},
	}, &revision)
	if err != nil {
		t.Fatalf("合法 cot 偏好必须可写：%v", err)
	}
	if mutation.Namespace.Value[FieldCotTranslationProvider] != "deepseek-account" ||
		mutation.Namespace.Value[FieldCotTranslationTargetLanguage] != "zh-Hans" {
		t.Fatalf("写后有效值不对：%v", mutation.Namespace.Value)
	}
	// 新 Service 重开同一 store，证明不是只改了返回对象。
	reopened := NewService(store, HostRegistry())
	persisted, err := reopened.Get(NamespaceCotTranslation)
	if err != nil {
		t.Fatal(err)
	}
	if got := fieldFor(t, persisted, FieldCotTranslationTargetLanguage).Current; got != "zh-Hans" {
		t.Fatalf("重开后 targetLanguage=%v", got)
	}

	for _, entry := range []struct {
		field string
		value any
		code  string
	}{
		{FieldCotTranslationProvider, "bogus", "invalid_enum"},
		{FieldCotTranslationTargetLanguage, "zh_CN", "invalid_value"},
		{FieldCotTranslationTargetLanguage, "", "invalid_value"},
		{FieldCotTranslationTargetLanguage, 1, "invalid_type"},
	} {
		before := len(store.writes)
		badRevision := persisted.Revision
		_, err := reopened.Mutate(NamespaceCotTranslation, []PathOp{{
			Op: "set", Path: []string{entry.field}, Value: entry.value,
		}}, &badRevision)
		if AsError(err).Code != entry.code {
			t.Fatalf("%s=%v：code=%v，want %s（err=%v）", entry.field, entry.value, AsError(err).Code, entry.code, err)
		}
		if len(store.writes) != before {
			t.Fatalf("%s=%v 被拒绝却仍写盘", entry.field, entry.value)
		}
	}

	resetRevision := persisted.Revision
	reset, err := reopened.Reset(NamespaceCotTranslation, nil, &resetRevision)
	if err != nil {
		t.Fatal(err)
	}
	if !viewValueEqual(t, reset.Namespace.Value, wantDefaults) || len(reset.Namespace.User) != 0 {
		t.Fatalf("reset 未回到默认：value=%v user=%v", reset.Namespace.Value, reset.Namespace.User)
	}
}

func TestCotTranslationSchemaMatchesOfficialShape(t *testing.T) {
	schema := normalizeSchemaEnvelope(t, jsonValue(t, cotTranslationNamespaceSpec().Schema)).(map[string]any)
	dict := schema["dict"].(map[string]any)
	provider := dict[FieldCotTranslationProvider].(map[string]any)
	if provider["type"] != "union" {
		t.Fatalf("provider schema type=%v，want union", provider["type"])
	}
	var values []string
	for _, raw := range provider["list"].([]any) {
		values = append(values, raw.(map[string]any)["value"].(string))
	}
	if !reflect.DeepEqual(values, CotTranslationProviderPreferences) {
		t.Fatalf("schema provider=%v，want %v", values, CotTranslationProviderPreferences)
	}
	language := dict[FieldCotTranslationTargetLanguage].(map[string]any)
	meta := language["meta"].(map[string]any)
	pattern := meta["pattern"].(map[string]any)
	if language["type"] != "string" || pattern["source"] != CotTranslationLanguagePattern || meta["default"] != "auto" {
		t.Fatalf("targetLanguage schema 与官方不符：%v", language)
	}
}
