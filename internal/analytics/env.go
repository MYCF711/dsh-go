package analytics

// 官方 OTLP 端点环境变量 → 策略 CollectorURL 的接线（GO.5 / T1）。
//
// 官方原件（app.asar 只读解出）：
//   - `DSH_TELEMETRY_OTLP_URL`         @dsa/dsh-base/cordis.patch.yml:225
//     默认 https://dsh-otel-collector.deepseeksvc.com/v1/logs
//   - `DSH_PRODUCT_ANALYTICS_OTLP_URL` @dsa/dsh-web-app/cordis.patch.yml:47
//     覆盖 host product telemetry 的导出目的地（没有出厂默认）
//
// 接线为什么只改 URL、不改 mode：
// 本仓的唯一差异是**默认值**（默认 mode=local，一个字节都不出网）。读了 env 就把
// mode 抬成 local+export，等于"因为设了一个端点就默认外发"，那正好踩掉红线 1。
// 所以环境变量只负责回答"端点在哪"，"发不发"仍由策略的 mode 决定。
//
// 优先级（单点，不散落）：
//
//	显式配置（policy.json 的非空 collectorUrl） > 环境变量 > 默认（空 = 本地零出网）
//
// 环境变量之间也有顺序：越具体的名字越优先（product analytics 专用名 > 通用
// telemetry 名），同一档里宿主自己的 `DSHD_*` 前缀先命中（与
// internal/spi/search.go 的双前缀约定一致）。

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// 官方 env 名（逐字，不转小写、不加别的前缀）。
const (
	// EnvTelemetryOTLPURL 是官方的会话遥测端点覆盖变量。
	EnvTelemetryOTLPURL = "DSH_TELEMETRY_OTLP_URL"
	// EnvProductAnalyticsOTLPURL 是官方的 host product telemetry 端点覆盖变量。
	EnvProductAnalyticsOTLPURL = "DSH_PRODUCT_ANALYTICS_OTLP_URL"
)

// 宿主前缀别名：本仓其它配置统一用 DSHD_*，这里同时认，语义完全相同。
const (
	envHostTelemetryOTLPURL        = "DSHD_TELEMETRY_OTLP_URL"
	envHostProductAnalyticsOTLPURL = "DSHD_PRODUCT_ANALYTICS_OTLP_URL"
)

// collectorURLEnvNames 是端点环境变量的**完整**优先级顺序。
//
// 单点定义：解析、诊断输出、测试都引用这一份，避免"多写一处就漂移"。
var collectorURLEnvNames = []string{
	envHostProductAnalyticsOTLPURL,
	EnvProductAnalyticsOTLPURL,
	envHostTelemetryOTLPURL,
	EnvTelemetryOTLPURL,
}

// CollectorURLFromEnv 返回环境变量实际给出的端点与变量名。
//
// 空串（含只有空格）等价于"没设"：不把空值当端点，也不让空值挡住后面的变量。
func CollectorURLFromEnv(getenv func(string) string) (value, name string, ok bool) {
	if getenv == nil {
		getenv = os.Getenv
	}
	for _, candidate := range collectorURLEnvNames {
		if trimmed := strings.TrimSpace(getenv(candidate)); trimmed != "" {
			return trimmed, candidate, true
		}
	}
	return "", "", false
}

// WithEnvCollectorURL 把环境变量端点叠加到策略上（显式配置优先）。
//
// 返回的 third 值是"这次生效的 env 名"（没生效时为空），供诊断/日志说清
// "端点是从哪来的"。非法 URL 在这里就返回错误——fail-loud，不静默回退成默认。
func WithEnvCollectorURL(policy Policy, getenv func(string) string) (Policy, string, error) {
	if strings.TrimSpace(policy.CollectorURL) != "" {
		// 策略文件里已经显式写了端点：它是配置，env 不覆盖。
		return policy, "", nil
	}
	value, name, ok := CollectorURLFromEnv(getenv)
	if !ok {
		return policy, "", nil
	}
	if err := validateCollectorURL(value); err != nil {
		return policy, name, fmt.Errorf("analytics: 环境变量 %s %w", name, err)
	}
	policy.CollectorURL = value
	return policy, name, nil
}

// validateCollectorURL 是 collector 端点唯一校验点（Normalize 与 env 共用）。
func validateCollectorURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("必须是 http/https URL，收到 %q", raw)
	}
	return nil
}
