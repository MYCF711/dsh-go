package llm

// 官方 `retryPolicy`（每 provider 一份）的宿主形状。
//
// 官方原件（app.asar 解包，见 tmp/GO7-RELEASE/gaps-agentloop/official-ref/）：
//
//	dsh-llm-round/lib/index.js:236-345  RetryPolicySchema / resolveRetryPolicy
//	  DEFAULT_MAX_RETRIES=5  DEFAULT_INITIAL_DELAY_MS=500
//	  DEFAULT_MAX_DELAY_MS=10000  DEFAULT_JITTER_RATIO=0.1
//	  DEFAULT_RETRYABLE_CODES=[EMPTY_RESPONSE, RATE_LIMIT, SERVER, TIMEOUT, TRANSPORT]
//	dsh-llm-retry/lib/index.js:29      "retryPolicy belongs under each provider configuration"
//	dsh-llm-retry/README.md:33-51      什么时候用、默认是什么
//
// 为什么要这一层：本仓原来的重试参数是**编译期常量**（retry.go 的
// defaultRetryConfig），生产路径恒走那一份——"每家 provider 一套策略"在宿主
// 没有任何入口。现在策略挂在**模型目录的模型条目**上（`retryPolicy`），由 serve
// 侧读出后经 `DSHD_LLM_RETRY_POLICY` 交给 `dshd run` 子进程，落到那一条 Client。
//
// 三条硬约束：
//  1. **不设时零影响**：环境里没有这一项 = null 策略 = 仍然走 defaultRetryConfig；
//  2. **形状与官方逐字段一致**：`mode` / `maxRetries` / `retryableCodes` / `backoff`，
//     连"未知键要报错"都一样（官方 validateKeys）；
//  3. **写错 fail-loud**：非法值不许静默回落到默认（"我设了"与"实际跑的"必须是同一个）。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	// RetryPolicyEnv 是每 provider 重试策略的传递通道（JSON 对象，官方形状）。
	//
	// 走环境变量而不是再开一条进程间通道：宿主已经用 DSHD_LLM_BASE / DSHD_LLM_KEY /
	// DSHD_LLM_API 把"这一轮用哪个 provider"交给 `dshd run` 子进程，策略是同一个
	// provider 的另一个字段，走同一条路才不会出现"provider 是 A、策略是 B"。
	RetryPolicyEnv = "DSHD_LLM_RETRY_POLICY"

	// RetryModeNormal 是官方默认模式：有次数上限的重试。
	RetryModeNormal = "normal"
	// RetryModeAlways 是官方"无限重试"模式：只由取消/收工停止。
	RetryModeAlways = "always"

	// 官方默认值（dsh-llm/lib/index.js:248-256）。
	officialDefaultRetryCount      = 5
	officialDefaultRetryInitialMs  = 500
	officialDefaultRetryMaxDelayMs = 10000
	officialDefaultRetryJitter     = 0.1

	// maxRetryTimerDelayMs 是官方 MAX_TIMER_DELAY_MS（2^31-1）。
	maxRetryTimerDelayMs = 1<<31 - 1
)

// 官方默认可重试 code 集合。
//
// 宿主能**产生**的只有 RATE_LIMIT（429）/ SERVER（5xx）/ TRANSPORT（连接层瞬时
// 失败）；EMPTY_RESPONSE 与 TIMEOUT 在宿主没有产生点（没有请求级超时——超时只来自
// ctx 取消，而取消绝不重试；空回答由上层 empty-response 策略处理）。这与官方
// "code 不匹配就不重试"的语义一致：列进来也不会误伤。
var officialDefaultRetryableCodes = []string{
	RetryCodeEmptyResponse, RetryCodeRateLimit, RetryCodeServer, RetryCodeTimeout, RetryCodeTransport,
}

// 官方失败 code 词表（dsh-llm/lib/types/error.js）。
const (
	RetryCodeEmptyResponse = "EMPTY_RESPONSE"
	RetryCodeRateLimit     = "RATE_LIMIT"
	RetryCodeServer        = "SERVER"
	RetryCodeTimeout       = "TIMEOUT"
	RetryCodeTransport     = "TRANSPORT"
)

// RetryBackoff 是官方 `retryPolicy.backoff`。
type RetryBackoff struct {
	InitialDelayMs int
	MaxDelayMs     int
	JitterRatio    float64
}

// RetryPolicy 是一份已经按官方规则解析、填好默认值、校验过的重试策略。
type RetryPolicy struct {
	Mode           string   // "normal" | "always"
	MaxRetries     int      // 仅 normal；官方默认 5
	RetryableCodes []string // 仅 normal；官方默认五个 code
	Backoff        RetryBackoff
}

// RetriesCode 报告这条策略是否接纳某个失败 code。
//
// always 模式不受 code 限制（官方："retries every model-request failure"）。
func (p RetryPolicy) RetriesCode(code string) bool {
	if p.Mode == RetryModeAlways {
		return true
	}
	for _, candidate := range p.RetryableCodes {
		if candidate == code {
			return true
		}
	}
	return false
}

// retryConfig 把官方策略翻译成宿主的重试参数。
//
// 字段对应：attempts = maxRetries+1（首发 + 重试）、base = initialDelayMs、
// maxWait = maxDelayMs、jitter = jitterRatio；budget 保持 0（官方 normal 模式
// 没有"总等待预算"这个概念，只有次数与单次上限）。
func (p RetryPolicy) retryConfig() retryConfig {
	cfg := retryConfig{
		base:    time.Duration(p.Backoff.InitialDelayMs) * time.Millisecond,
		maxWait: time.Duration(p.Backoff.MaxDelayMs) * time.Millisecond,
		jitter:  p.Backoff.JitterRatio,
	}
	if p.Mode == RetryModeAlways {
		cfg.unbounded = true
	} else {
		cfg.attempts = p.MaxRetries + 1
	}
	if codes := retryCodeSet(p.RetryableCodes); len(codes) > 0 {
		cfg.codes = codes
	}
	return cfg
}

func retryCodeSet(codes []string) map[string]bool {
	if len(codes) == 0 {
		return nil
	}
	out := make(map[string]bool, len(codes))
	for _, code := range codes {
		out[code] = true
	}
	return out
}

// ParseRetryPolicy 解析一份官方形状的 retryPolicy。
//
// 与官方 resolveRetryPolicy 逐条对齐：
//   - 缺省（空字节）= 官方 normal 默认；
//   - `mode` 必须显式写 normal / always（官方 zod 里是 required）；
//   - 未知键一律报错（官方 validateKeys）；
//   - backoff 三个字段各自有默认值与取值区间；initialDelayMs <= maxDelayMs；
//   - normal：maxRetries 是 >=0 的整数、retryableCodes 非空且无重复；
//   - always：maxRetries / retryableCodes 允许出现但**不生效**（官方同样接受）。
func ParseRetryPolicy(raw []byte) (RetryPolicy, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return defaultRetryPolicy(), nil
	}
	var obj map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	if err := decoder.Decode(&obj); err != nil {
		return RetryPolicy{}, fmt.Errorf("retryPolicy 不是 JSON 对象: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return RetryPolicy{}, fmt.Errorf("retryPolicy 含额外 JSON: %w", err)
	}
	if obj == nil {
		return RetryPolicy{}, fmt.Errorf("retryPolicy 必须是 JSON 对象（不能是 null）")
	}

	allowed := map[string]bool{"mode": true, "maxRetries": true, "retryableCodes": true, "backoff": true}
	// 报错要可复现：map 遍历顺序随机，所以按键名排序找第一个非法键。
	for _, key := range sortedKeys(obj) {
		if !allowed[key] {
			return RetryPolicy{}, fmt.Errorf("retryPolicy: unknown key %q（官方只认 mode/maxRetries/retryableCodes/backoff）", key)
		}
	}

	rawMode, ok := obj["mode"]
	if !ok {
		return RetryPolicy{}, fmt.Errorf(`retryPolicy.mode 必须显式写 "normal" 或 "always"`)
	}
	var mode string
	if err := json.Unmarshal(rawMode, &mode); err != nil {
		return RetryPolicy{}, fmt.Errorf("retryPolicy.mode 必须是 string: %w", err)
	}

	backoff, err := parseRetryBackoff(obj["backoff"])
	if err != nil {
		return RetryPolicy{}, err
	}

	switch mode {
	case RetryModeNormal:
		policy := defaultRetryPolicy()
		policy.Backoff = backoff
		if rawMax, ok := obj["maxRetries"]; ok {
			var maxRetries int
			if err := json.Unmarshal(rawMax, &maxRetries); err != nil {
				return RetryPolicy{}, fmt.Errorf("retryPolicy.maxRetries 必须是整数: %w", err)
			}
			if maxRetries < 0 {
				return RetryPolicy{}, fmt.Errorf("retryPolicy.maxRetries 不能为负数（实得 %d）", maxRetries)
			}
			policy.MaxRetries = maxRetries
		}
		if rawCodes, ok := obj["retryableCodes"]; ok {
			var codes []string
			if err := json.Unmarshal(rawCodes, &codes); err != nil {
				return RetryPolicy{}, fmt.Errorf("retryPolicy.retryableCodes 必须是字符串数组: %w", err)
			}
			if len(codes) == 0 {
				return RetryPolicy{}, fmt.Errorf("retryPolicy.retryableCodes 不能为空数组（那等于永不重试，请删掉这个键）")
			}
			seen := map[string]bool{}
			for _, code := range codes {
				if strings.TrimSpace(code) == "" {
					return RetryPolicy{}, fmt.Errorf("retryPolicy.retryableCodes 含空字符串")
				}
				if seen[code] {
					return RetryPolicy{}, fmt.Errorf("retryPolicy.retryableCodes 含重复值 %q", code)
				}
				seen[code] = true
			}
			policy.RetryableCodes = append([]string(nil), codes...)
		}
		return policy, nil
	case RetryModeAlways:
		return RetryPolicy{
			Mode:           RetryModeAlways,
			RetryableCodes: append([]string(nil), officialDefaultRetryableCodes...),
			Backoff:        backoff,
		}, nil
	default:
		return RetryPolicy{}, fmt.Errorf(`retryPolicy.mode 必须是 "normal" 或 "always"（实得 %q）`, mode)
	}
}

// sortedKeys 返回稳定顺序的键（报错信息要可复现）。
func sortedKeys(obj map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(obj))
	for key := range obj {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// defaultRetryPolicy 是官方 normal 默认策略。
func defaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		Mode:           RetryModeNormal,
		MaxRetries:     officialDefaultRetryCount,
		RetryableCodes: append([]string(nil), officialDefaultRetryableCodes...),
		Backoff: RetryBackoff{
			InitialDelayMs: officialDefaultRetryInitialMs,
			MaxDelayMs:     officialDefaultRetryMaxDelayMs,
			JitterRatio:    officialDefaultRetryJitter,
		},
	}
}

// parseRetryBackoff 解析官方 `retryPolicy.backoff`；缺省字段取官方默认值。
func parseRetryBackoff(raw json.RawMessage) (RetryBackoff, error) {
	backoff := RetryBackoff{
		InitialDelayMs: officialDefaultRetryInitialMs,
		MaxDelayMs:     officialDefaultRetryMaxDelayMs,
		JitterRatio:    officialDefaultRetryJitter,
	}
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return backoff, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return RetryBackoff{}, fmt.Errorf("retryPolicy.backoff 必须是 JSON 对象: %w", err)
	}
	for _, key := range sortedKeys(obj) {
		switch key {
		case "initialDelayMs", "maxDelayMs", "jitterRatio":
		default:
			return RetryBackoff{}, fmt.Errorf("retryPolicy.backoff: unknown key %q（官方只认 initialDelayMs/maxDelayMs/jitterRatio）", key)
		}
	}
	if v, ok := obj["initialDelayMs"]; ok {
		if err := json.Unmarshal(v, &backoff.InitialDelayMs); err != nil {
			return RetryBackoff{}, fmt.Errorf("retryPolicy.backoff.initialDelayMs 必须是整数: %w", err)
		}
	}
	if v, ok := obj["maxDelayMs"]; ok {
		if err := json.Unmarshal(v, &backoff.MaxDelayMs); err != nil {
			return RetryBackoff{}, fmt.Errorf("retryPolicy.backoff.maxDelayMs 必须是整数: %w", err)
		}
	}
	if v, ok := obj["jitterRatio"]; ok {
		if err := json.Unmarshal(v, &backoff.JitterRatio); err != nil {
			return RetryBackoff{}, fmt.Errorf("retryPolicy.backoff.jitterRatio 必须是数字: %w", err)
		}
	}
	if backoff.InitialDelayMs <= 0 || backoff.InitialDelayMs > maxRetryTimerDelayMs {
		return RetryBackoff{}, fmt.Errorf("retryPolicy.backoff.initialDelayMs 必须是 1..%d 的整数（实得 %d）",
			maxRetryTimerDelayMs, backoff.InitialDelayMs)
	}
	if backoff.MaxDelayMs <= 0 || backoff.MaxDelayMs > maxRetryTimerDelayMs {
		return RetryBackoff{}, fmt.Errorf("retryPolicy.backoff.maxDelayMs 必须是 1..%d 的整数（实得 %d）",
			maxRetryTimerDelayMs, backoff.MaxDelayMs)
	}
	if backoff.InitialDelayMs > backoff.MaxDelayMs {
		return RetryBackoff{}, fmt.Errorf("retryPolicy.backoff.initialDelayMs(%d) 不能大于 maxDelayMs(%d)",
			backoff.InitialDelayMs, backoff.MaxDelayMs)
	}
	if backoff.JitterRatio < 0 || backoff.JitterRatio > 1 {
		return RetryBackoff{}, fmt.Errorf("retryPolicy.backoff.jitterRatio 必须在 0..1（实得 %v）", backoff.JitterRatio)
	}
	return backoff, nil
}

// MarshalJSON 按官方形状序列化（给 DSHD_LLM_RETRY_POLICY 用）。
func (p RetryPolicy) MarshalJSON() ([]byte, error) {
	type backoffJSON struct {
		InitialDelayMs int     `json:"initialDelayMs"`
		MaxDelayMs     int     `json:"maxDelayMs"`
		JitterRatio    float64 `json:"jitterRatio"`
	}
	if p.Mode == RetryModeAlways {
		return json.Marshal(struct {
			Mode    string      `json:"mode"`
			Backoff backoffJSON `json:"backoff"`
		}{
			Mode: p.Mode,
			Backoff: backoffJSON{
				InitialDelayMs: p.Backoff.InitialDelayMs,
				MaxDelayMs:     p.Backoff.MaxDelayMs,
				JitterRatio:    p.Backoff.JitterRatio,
			},
		})
	}
	return json.Marshal(struct {
		Mode           string      `json:"mode"`
		MaxRetries     int         `json:"maxRetries"`
		RetryableCodes []string    `json:"retryableCodes"`
		Backoff        backoffJSON `json:"backoff"`
	}{
		Mode:           p.Mode,
		MaxRetries:     p.MaxRetries,
		RetryableCodes: p.RetryableCodes,
		Backoff: backoffJSON{
			InitialDelayMs: p.Backoff.InitialDelayMs,
			MaxDelayMs:     p.Backoff.MaxDelayMs,
			JitterRatio:    p.Backoff.JitterRatio,
		},
	})
}

// RetryPolicyFromEnv 从环境里取这一轮的 per-provider 策略。
//
// 返回 nil, nil 表示**没设**——调用方必须保持原样（zero impact），不许拿一个
// "默认策略"顶上去：那会把 defaultRetryConfig 与官方默认值混成两份真值。
func RetryPolicyFromEnv(env func(string) string) (*RetryPolicy, error) {
	if env == nil {
		return nil, nil
	}
	raw := strings.TrimSpace(env(RetryPolicyEnv))
	if raw == "" {
		return nil, nil
	}
	policy, err := ParseRetryPolicy([]byte(raw))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", RetryPolicyEnv, err)
	}
	return &policy, nil
}

// applyRetryPolicy 给这条 client 装上策略；nil 不改变任何东西。
func (c *Client) applyRetryPolicy(policy *RetryPolicy) {
	if c == nil || policy == nil {
		return
	}
	cfg := policy.retryConfig()
	c.retry = &cfg
}

// ApplyRetryPolicy 是 applyRetryPolicy 的导出版本（`dshd run` 侧接线用）。
func (c *Client) ApplyRetryPolicy(policy *RetryPolicy) { c.applyRetryPolicy(policy) }

// HasRetryPolicy 报告这条 client 是否被显式装过策略。
func (c *Client) HasRetryPolicy() bool {
	return c != nil && c.retry != nil
}
