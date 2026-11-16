package settingsform

// 官方 0.2.2-rc.2 `dsh-experimental-client-ui-cot-translation` 的设置命名空间。
//
// 权威源：
//   - src/client/mount.ts:81：configForms.get('cot-translation')；这是设置表单 ns，
//     与 Remote 服务名 cotTranslation 不是同一个字符串。
//   - src/index.ts:31-34：provider 四值 union、targetLanguage pattern、两者 volatile。
//   - src/preferences.ts:7,10-12：语言正则与 provider 声明顺序。
//
// 部署默认值有一项有意差异：官方 provider 默认 bing（src/index.ts:32），本仓不代理
// 匿名第三方翻译端点，真实可用路线只有 deepseek-official / deepseek-account，因此
// 默认沿用在 namespace 落地前就已经生效的 deepseek-official，避免升级后默认翻译
// 立刻变成 501。其余字段、枚举顺序、语言正则与官方一致。
const (
	NamespaceCotTranslation           = "cot-translation"
	FieldCotTranslationProvider       = "provider"
	FieldCotTranslationTargetLanguage = "targetLanguage"

	DefaultCotTranslationProvider       = "deepseek-official"
	DefaultCotTranslationTargetLanguage = "auto"

	CotTranslationLanguagePattern = `^(?:auto|[a-zA-Z]{2,8}(?:-[a-zA-Z0-9]{1,8})*)$`
)

// CotTranslationProviderPreferences 的顺序逐字对应 PROVIDER_PREFERENCES。
var CotTranslationProviderPreferences = []string{
	"bing", "google", "deepseek-account", "deepseek-official",
}

func cotTranslationNamespaceSpec() NamespaceSpec {
	return uiNamespaceSpec(uiNamespace{
		ns:    NamespaceCotTranslation,
		title: "Reasoning translation",
		fields: []uiField{
			uiEnumField(FieldCotTranslationProvider, CotTranslationProviderPreferences, DefaultCotTranslationProvider),
			uiStringField(FieldCotTranslationTargetLanguage, DefaultCotTranslationTargetLanguage).
				withPattern(CotTranslationLanguagePattern, ""),
		},
		base: map[string]any{
			FieldCotTranslationProvider:       DefaultCotTranslationProvider,
			FieldCotTranslationTargetLanguage: DefaultCotTranslationTargetLanguage,
		},
	})
}
