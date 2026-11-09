package compact

// 官方压缩策略的**可配置面**（`@deepseek-ai/dsh-compaction-basic`）：
//
//	headroomTokens     = 65536  窗口下方预留的安全带（context.compaction.headroomTokens）
//	thresholdRatio     = 0.8    触发阈值占窗口的比例
//	maxTokens          = headroomTokens  一次请求预留的完成 token（没显式给就取 headroom）
//	compactionRetries  = 1      压完仍超阈值时再压几次
//	maxOverflowRetries = 1      溢出后压缩并重放这一轮几次
//	modelPolicies      = []     按 provider/model 精确覆盖上面全部字段
//
// 官方原件：tmp/GO3-PARAMS/_official/dsh-compaction-basic/lib/index.js:59-87
//（resolveConfig）、:110-150（resolveCompactSpec 的 fail-loud 判据）。
//
// 为什么需要这一层：宿主原来只有**一个**绝对阈值（`--auto-compact`）与一份
// 进程级重试预算，1M 窗口与 32K 窗口共用同一套数字。官方允许按目标覆盖，
// 小窗口模型才不会被"看起来安全"的绝对阈值顶穿。
//
// # 三条硬约束
//
//  1. **默认行为不变**：不配置 modelPolicies 时，解析结果就是既有默认值；
//  2. **非法配置 fail-loud**：重复目标、非正整数、比例越界、retain ≥ threshold
//     一律报错——静默忽略会让人以为"我配了 0.5"，实际跑的是 0.8；
//  3. **目标必须精确**：provider/model 逐字匹配（`*` 是唯一的通配），不做
//     前缀/大小写/模糊匹配——压缩阈值配错的后果是"该压的时候没压"。

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const (
	// DefaultHeadroomTokens 是官方 `headroomTokens` 的出厂值（65536）。
	DefaultHeadroomTokens = 65536
	// Wildcard 是 modelPolicies 里唯一的通配符（provider 或 model 都可以写）。
	Wildcard = "*"
)

// ModelPolicy 是官方 `modelPolicies[]` 的一条精确目标覆盖。
//
// 指针字段表达"没配"：零值（0 / 0.0）在官方语义里是**合法配置值**
// （`headroomTokens: 0` 就是不留安全带），不能用零值代表缺省。
type ModelPolicy struct {
	// Provider 是提供方 id；空 或 `*` = 任意 provider。
	Provider string `json:"provider,omitempty"`
	// Model 是模型 id；`*` = 该 provider 下任意模型。
	Model string `json:"model"`

	HeadroomTokens     *int     `json:"headroomTokens,omitempty"`
	ThresholdRatio     *float64 `json:"thresholdRatio,omitempty"`
	MaxTokens          *int     `json:"maxTokens,omitempty"`
	CompactionRetries  *int     `json:"compactionRetries,omitempty"`
	MaxOverflowRetries *int     `json:"maxOverflowRetries,omitempty"`
}

// Policy 是**解析后**的一套压缩参数（默认值 + 命中的覆盖已合并）。
type Policy struct {
	HeadroomTokens     int
	ThresholdRatio     float64
	MaxTokens          int // 0 = 没显式给（用请求/适配器声明）
	CompactionRetries  int
	MaxOverflowRetries int
}

// DefaultPolicy 返回出厂策略：与官方默认逐值相同。
func DefaultPolicy() Policy {
	return Policy{
		HeadroomTokens:     DefaultHeadroomTokens,
		ThresholdRatio:     DefaultThresholdRatio,
		CompactionRetries:  DefaultCompactRetries,
		MaxOverflowRetries: DefaultCompactOverflowRetries,
	}
}

// WithRetry 把 env 解析出来的重试预算并进默认策略（`DSHD_COMPACT_RETRIES` 等）。
func (p Policy) WithRetry(r RetryPolicy) Policy {
	p.CompactionRetries = r.Retries
	p.MaxOverflowRetries = r.OverflowRetries
	return p
}

// ParseModelPolicies 解析 `--compact-model-policies` 的 JSON 数组。
//
// 空串 = 没有覆盖（返回 nil，调用方按默认策略走）。其余任何不合法形状一律
// 返回错误：这份配置只在启动时解析一次，错了就是**这一进程的压缩阈值全错**。
func ParseModelPolicies(raw string) ([]ModelPolicy, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var policies []ModelPolicy
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&policies); err != nil {
		return nil, fmt.Errorf("modelPolicies 不是合法 JSON 数组（%w）：%s", err, raw)
	}
	if err := ValidateModelPolicies(policies); err != nil {
		return nil, err
	}
	return policies, nil
}

// ValidateModelPolicies 校验一份覆盖表（重复目标 / 数值域）。
func ValidateModelPolicies(policies []ModelPolicy) error {
	seen := make(map[string]struct{}, len(policies))
	for i, p := range policies {
		target := strings.TrimSpace(p.Model)
		if target == "" {
			return fmt.Errorf("modelPolicies[%d] 缺 model（要覆盖哪一个模型？写 %q 表示全部）", i, Wildcard)
		}
		key := strings.TrimSpace(p.Provider) + "\x00" + target
		if _, dup := seen[key]; dup {
			return fmt.Errorf("modelPolicies[%d] 与前面的条目重复：provider=%q model=%q"+
				"（同一目标只能有一条，否则哪条生效取决于书写顺序）", i, p.Provider, p.Model)
		}
		seen[key] = struct{}{}
		if p.HeadroomTokens != nil && *p.HeadroomTokens < 0 {
			return fmt.Errorf("modelPolicies[%d]（%s）headroomTokens 不能为负：%d", i, target, *p.HeadroomTokens)
		}
		if p.MaxTokens != nil && *p.MaxTokens <= 0 {
			return fmt.Errorf("modelPolicies[%d]（%s）maxTokens 必须是正整数：%d", i, target, *p.MaxTokens)
		}
		if p.ThresholdRatio != nil {
			ratio := *p.ThresholdRatio
			if ratio <= 0 || ratio > 1 {
				return fmt.Errorf("modelPolicies[%d]（%s）thresholdRatio 必须落在 (0,1]：%v", i, target, ratio)
			}
		}
		for name, value := range map[string]*int{
			"compactionRetries":  p.CompactionRetries,
			"maxOverflowRetries": p.MaxOverflowRetries,
		} {
			if value != nil && *value < 0 {
				return fmt.Errorf("modelPolicies[%d]（%s）%s 不能为负：%d", i, target, name, *value)
			}
		}
	}
	return nil
}

// Resolve 为 model 选出有效策略：默认值 + 命中的那条覆盖。
//
// 匹配顺序（官方精确目标语义）：
//  1. `provider/model` 逐字命中（provider 非空且不是 `*`）；
//  2. `*/model` 与单个 `model`（provider 省略）命中——宿主只有一条 llm 通道，
//     模型 id 可能带也可能不带 provider 前缀；
//  3. `provider/*`。
//
// 都不命中 = 默认策略。多条同时命中是**配置错误**（Validate 已挡重复目标，
// 这里再挡一次跨形状的歧义：例如同时写 `*​/m` 与 `p/m`）。
func Resolve(policies []ModelPolicy, defaults Policy, model string) Policy {
	model = strings.TrimSpace(model)
	// 宿主模型 id 通常带 provider 前缀（`group/deepseek-…`）；官方的
	// modelPolicies 把 provider 与 model 分开写，所以两种拼法都要能命中：
	// 逐字比较整条 id，也逐字比较去掉 provider 前缀之后的那一段。
	provider, bare := "", model
	if i := strings.Index(model, "/"); i > 0 {
		provider, bare = model[:i], model[i+1:]
	}
	best := defaults
	bestScore := -1
	for _, p := range policies {
		if !targetMatches(p, provider, model, bare) {
			continue
		}
		// 更精确的目标优先：p/m > p/* > */m > */*。
		if score := policySpecificity(p); score > bestScore {
			best = mergePolicy(defaults, p)
			bestScore = score
		}
	}
	return best
}

func policySpecificity(p ModelPolicy) int {
	provider := strings.TrimSpace(p.Provider)
	model := strings.TrimSpace(p.Model)
	switch {
	case provider != "" && provider != Wildcard && model != Wildcard:
		return 3
	case provider != "" && provider != Wildcard:
		return 2
	case model != Wildcard:
		return 1
	default:
		return 0
	}
}

func targetMatches(p ModelPolicy, provider, full, bare string) bool {
	wantProvider := strings.TrimSpace(p.Provider)
	wantModel := strings.TrimSpace(p.Model)
	if wantProvider != "" && wantProvider != Wildcard && wantProvider != provider {
		return false
	}
	if wantModel == Wildcard {
		return true
	}
	return wantModel == full || wantModel == bare
}

func mergePolicy(defaults Policy, p ModelPolicy) Policy {
	out := defaults
	if p.HeadroomTokens != nil {
		out.HeadroomTokens = *p.HeadroomTokens
	}
	if p.ThresholdRatio != nil {
		out.ThresholdRatio = *p.ThresholdRatio
	}
	if p.MaxTokens != nil {
		out.MaxTokens = *p.MaxTokens
	}
	if p.CompactionRetries != nil {
		out.CompactionRetries = *p.CompactionRetries
	}
	if p.MaxOverflowRetries != nil {
		out.MaxOverflowRetries = *p.MaxOverflowRetries
	}
	return out
}

// Budget 把策略摊成一份窗口预算（官方 resolveCompactSpec 的宿主形状）。
//
// reservedFallback 是"请求头没声明 maxTokens 时 adapter 给的每请求默认值"；
// 策略显式给了 maxTokens 就压过它（官方 config.maxTokens 语义）。
func (p Policy) Budget(contextWindow, reservedFallback int) PressureBudget {
	reserved := p.MaxTokens
	if reserved <= 0 {
		reserved = reservedFallback
	}
	return PressureBudget{
		ContextWindow:            contextWindow,
		ReservedCompletionTokens: reserved,
		HeadroomTokens:           p.HeadroomTokens,
		ThresholdRatio:           p.ThresholdRatio,
	}
}

// Describe 是给诊断日志用的稳定描述（不参与判定）。
func (p Policy) Describe() string {
	return fmt.Sprintf("headroom=%d ratio=%.2f maxTokens=%d retries=%d overflow=%d",
		p.HeadroomTokens, p.ThresholdRatio, p.MaxTokens, p.CompactionRetries, p.MaxOverflowRetries)
}

// SortedTargets 只给测试/文档用：稳定的目标清单。
func SortedTargets(policies []ModelPolicy) []string {
	out := make([]string, 0, len(policies))
	for _, p := range policies {
		out = append(out, strings.TrimSpace(p.Provider)+"/"+strings.TrimSpace(p.Model))
	}
	sort.Strings(out)
	return out
}
