// Package analytics 是宿主本地的产品分析管道。
//
// 官方 `productAnalytics.*` 只有三个方法（enabled/report/watchPolicy），且默认
// 把 Desktop 事件发往官方 OTel exporter。本仓的**唯一差异是默认值**：默认
// mode="local"，一个字节都不出网；采集、脱敏、JSONL hash 链、聚合、
// 可选 exporter、策略流全部真实现（能力一项不少）。
//
// 红线（不是口号，代码里逐条可核）：
//
//  1. 默认零外连：Policy.Mode == "local" 时 Report 不创建任何 HTTP 请求；
//     CollectorURL 即使填了也不会被使用（测试用 httptest 桩证伪）。
//  2. 只存脱敏后的结构化元数据：事件名/时间戳/白名单属性；prompt/正文/
//     文件路径/密钥在入库前被替换或丢弃（见 redact.go）。
//  3. 每次写入都进 append-only 的 events-YYYYMM.jsonl，记录带 prevHash/hash；
//     可用 Verify 复算，PURGE 可一键清空。
//  4. exporter 只在 mode="local+export" 且 CollectorURL 非空时存在；导出前
//     再走一次脱敏（export.go 的 sanitizeExportBatch），并带 policyVersion。
package analytics

import (
	"fmt"
	"strings"
)

// Mode 是产品分析的两种生效模式。
const (
	// ModeLocal 是默认值：只落本地，零外连。
	ModeLocal = "local"
	// ModeLocalExport 表示本地落盘之外，再把脱敏后的记录 POST 给用户配置的
	// collector。没有 collector URL 时 UpdatePolicy 会拒绝这个模式。
	ModeLocalExport = "local+export"
)

// DefaultRetentionDays / DefaultSamplingRate 是默认策略值。
const (
	DefaultRetentionDays = 30
	DefaultSamplingRate  = 1.0
	// DefaultPolicyVersion 是首次创建策略文件的版本号。
	DefaultPolicyVersion int64 = 1
)

// Policy 是 `productAnalytics.enabled` / `watchPolicy` 背后的真实策略。
//
// 官方 `enabled` 的结果只是 bool，`watchPolicy` 只流 bool；本结构里的其余字段
// 通过 `dshd analytics policy` 可读可改（用户能核对），不影响官方 wire 形状。
type Policy struct {
	// Enabled 是当前是否接受新事件。禁用后 Report 直接丢弃，不写盘、不出网。
	Enabled bool `json:"enabled"`
	// Mode 只能是 ModeLocal 或 ModeLocalExport。
	Mode string `json:"mode"`
	// CollectorURL 只允许 http/https；mode=local 时即使有值也不使用。
	//
	// 来源优先级（见 env.go）：本字段非空 = 显式配置，压过环境变量；
	// 为空时由 `DSH_PRODUCT_ANALYTICS_OTLP_URL` / `DSH_TELEMETRY_OTLP_URL`
	// （以及宿主前缀 `DSHD_*`）补齐；都没有就是默认空 = 本地零出网。
	CollectorURL string `json:"collectorUrl,omitempty"`
	// RetentionDays 是本地事件保留天数（0/负数表示不自动清理）。
	RetentionDays int `json:"retentionDays"`
	// SamplingRate 是 [0,1] 的采样率；1 表示全收，0 表示全丢。
	SamplingRate float64 `json:"samplingRate"`
	// PolicyVersion 单调递增，随每次策略更新/外部文件变化递增；事件与导出批次
	// 都带这个版本，便于用户核对"这条事件当时按哪版策略处理"。
	PolicyVersion int64 `json:"policyVersion"`
}

// DefaultPolicy 返回本仓默认策略：enabled=true, local 零出网, 全采样。
func DefaultPolicy() Policy {
	return Policy{
		Enabled:       true,
		Mode:          ModeLocal,
		RetentionDays: DefaultRetentionDays,
		SamplingRate:  DefaultSamplingRate,
		PolicyVersion: DefaultPolicyVersion,
	}
}

// Normalize 把缺省/零值补齐成可执行策略，但不掩盖非法值。
//
// 只对"文件里省略了字段"做补齐（enabled 明确 false 时保留 false；mode 空串
// 视为 local；PolicyVersion 0 视为 1）；未知 mode、越界采样率、非法 URL 仍
// 返回错误，绝不静默改成看似合法的值。
func (p Policy) Normalize() (Policy, error) {
	if p.Mode == "" {
		p.Mode = ModeLocal
	}
	if p.Mode != ModeLocal && p.Mode != ModeLocalExport {
		return p, fmt.Errorf("analytics policy: mode 只能是 %q 或 %q，收到 %q", ModeLocal, ModeLocalExport, p.Mode)
	}
	if p.SamplingRate < 0 || p.SamplingRate > 1 {
		return p, fmt.Errorf("analytics policy: samplingRate 必须在 [0,1]，收到 %v", p.SamplingRate)
	}
	if p.PolicyVersion <= 0 {
		p.PolicyVersion = DefaultPolicyVersion
	}
	p.CollectorURL = strings.TrimSpace(p.CollectorURL)
	if p.CollectorURL != "" {
		if err := validateCollectorURL(p.CollectorURL); err != nil {
			return p, fmt.Errorf("analytics policy: collectorUrl %w", err)
		}
	}
	if p.Mode == ModeLocalExport && p.CollectorURL == "" {
		return p, fmt.Errorf("analytics policy: mode=%q 需要 collectorUrl", ModeLocalExport)
	}
	return p, nil
}
