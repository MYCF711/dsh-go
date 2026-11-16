package transport

// 官方 `cotTranslation.limits`（@deepseek-ai/dsh-experimental-client-ui-cot-translation
// 0.2.2-rc.2，src/index.ts:46-53 / lib/typert.host.js:37-51）：
//
//	cotTranslation.limits() → {
//	  maxTextChars: number,
//	  preferences: { provider: google|bing|deepseek-account|deepseek-official,
//	                 targetLanguage: string },
//	  availableProviders: Provider[]
//	}
//
// 零参数、direct 调用。它是"不发送文本也能读到的当前翻译偏好吗与上限"，
// 不是翻译本身；所以这里必须来自本仓真实实现，不能返回占位空对象。
//
// 真实来源（与 handlers_media_market_official.go 的 translate 共用）：
//   - maxTextChars = cotTranslationMaxChars（translate 的同一判据，UTF-16 长度）；
//   - provider/targetLanguage = `cot-translation` namespace 的当前有效值；生产
//     serve 与 settings.form.* 共用同一 store，写后下一次 limits/translate 立即生效。
//     未注入设置服务的旧嵌入方才回退 deepseek-official/auto；
//   - availableProviders = 仅 deepseek-official / deepseek-account：translate
//     对 google/bing 明确 501（本仓不代理第三方匿名翻译端点），不能把它们
//     列成"可用"。

import (
	"net/http"
	"strings"
)

// MethodCotTranslationLimits 是官方 zero-arg 读方法。
const MethodCotTranslationLimits Method = "cotTranslation.limits"

// OfficialCotTranslationPreferences 是 preferences 对象。
type OfficialCotTranslationPreferences struct {
	Provider       string `json:"provider"`
	TargetLanguage string `json:"targetLanguage"`
}

// OfficialCotTranslationLimits 是 limits 的结果（字段逐字对齐官方 zod）。
type OfficialCotTranslationLimits struct {
	MaxTextChars       int                               `json:"maxTextChars"`
	Preferences        OfficialCotTranslationPreferences `json:"preferences"`
	AvailableProviders []string                          `json:"availableProviders"`
}

// IsOfficialCotTranslationLimitsMethod 只认 limits；translate 仍由 media 组接。
func IsOfficialCotTranslationLimitsMethod(name string) bool {
	return Method(name) == MethodCotTranslationLimits
}

// ServeOfficialCotTranslationLimitsAPI 是 `/api/cotTranslation.limits` 的入口。
func (s *Server) ServeOfficialCotTranslationLimitsAPI(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/")
	if !IsOfficialCotTranslationLimitsMethod(name) {
		writeJSON(w, http.StatusNotFound, errorBodyFor("not_found", "不是 cotTranslation.limits"))
		return
	}
	params, err := decodeOfficialParams(r)
	if err != nil {
		writeOfficialError(w, err)
		return
	}
	result, err := s.officialCotTranslationLimits(params)
	if err != nil {
		writeOfficialError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) officialCotTranslationLimits(p *officialParams) (any, error) {
	if err := p.finish(); err != nil {
		return nil, err
	}
	preferences, err := s.cotTranslationPreferences()
	if err != nil {
		return nil, err
	}
	return OfficialCotTranslationLimits{
		MaxTextChars:       cotTranslationMaxChars,
		Preferences:        preferences,
		AvailableProviders: []string{cotProviderDSOfficial, cotProviderDSAccount},
	}, nil
}

// cotTranslationPreferences 每次请求都读持久 namespace，不在 Server 里再缓存一份。
// 因此 settings.form.set/reset/mutate 成功后的下一次 limits 就返回新值；读取失败
// 响亮报错，不拿旧偏好冒充当前事实。
func (s *Server) cotTranslationPreferences() (OfficialCotTranslationPreferences, error) {
	fallback := OfficialCotTranslationPreferences{
		Provider: cotProviderDSOfficial, TargetLanguage: "auto",
	}
	if s == nil || s.opt.CotTranslationSettings == nil {
		return fallback, nil
	}
	section, err := s.opt.CotTranslationSettings.Get("cot-translation")
	if err != nil {
		return OfficialCotTranslationPreferences{}, officialInternal("读取 cot-translation 设置失败：%s", err)
	}
	out := fallback
	for _, field := range section.Fields {
		value, ok := field.Current.(string)
		if !ok {
			return OfficialCotTranslationPreferences{}, officialInternal(
				"cot-translation.%s 的当前值不是 string（%T）", field.Name, field.Current)
		}
		switch field.Name {
		case "provider":
			out.Provider = value
		case "targetLanguage":
			out.TargetLanguage = value
		}
	}
	return out, nil
}
