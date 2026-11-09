package transport

// 官方 `cotTranslation.limits`（@deepseek-ai/dsh-experimental-client-ui-cot-translation
// 0.2.2-rc.1，app.asar 只读解：lib/typert.host.js:6-18 / :24-45）：
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
//   - provider = deepseek-official（translate 省略 provider 时的默认网关路径）；
//   - targetLanguage = auto（官方 "follow the UI language" 的默认值；本仓不
//     持久化语言偏好，客户端可传具体 BCP 47 覆盖）；
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
	result, err := officialCotTranslationLimits(params)
	if err != nil {
		writeOfficialError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func officialCotTranslationLimits(p *officialParams) (any, error) {
	if err := p.finish(); err != nil {
		return nil, err
	}
	return OfficialCotTranslationLimits{
		MaxTextChars: cotTranslationMaxChars,
		Preferences: OfficialCotTranslationPreferences{
			Provider:       cotProviderDSOfficial,
			TargetLanguage: "auto",
		},
		AvailableProviders: []string{cotProviderDSOfficial, cotProviderDSAccount},
	}, nil
}
