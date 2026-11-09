package compact

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"

	"dshgo/internal/llm"
	"dshgo/internal/session"
)

// 官方 0.2.2 的压缩重试参数（`@deepseek-ai/dsh-compaction-basic`）：
//
//	compactionRetries   = 1  首次压缩之后压力仍高于阈值时，再压几次
//	maxOverflowRetries  = 1  确认上下文溢出之后，压缩并重试请求几次
//
// 宿主以前的形状是「`Summarize` 失败 → 整次压缩失败 → 这一轮白压」：摘要调用
// 抖一下（429/空响应）上下文就继续往上顶，直到真溢出。这里补的正是这段收敛：
// 摘要失败重试、溢出后压缩重试，两个数都可由 env 覆盖（0 = 关闭）。
const (
	// EnvCompactRetries 覆盖「摘要失败后再试几次」。
	EnvCompactRetries = "DSHD_COMPACT_RETRIES"
	// EnvCompactOverflowRetries 覆盖「上下文溢出后压缩重试几次」。
	EnvCompactOverflowRetries = "DSHD_COMPACT_OVERFLOW_RETRIES"

	// DefaultCompactRetries 是官方 `compactionRetries` 的出厂值。
	DefaultCompactRetries = 1
	// DefaultCompactOverflowRetries 是官方 `maxOverflowRetries` 的出厂值。
	DefaultCompactOverflowRetries = 1
)

// RetryPolicy 是压缩相关的重试预算。
type RetryPolicy struct {
	// Retries 是首发之外的额外摘要尝试次数（官方 `compactionRetries`，默认 1）。
	Retries int
	// OverflowRetries 是上下文溢出后的压缩重试轮数（官方 `maxOverflowRetries`，默认 1）。
	OverflowRetries int
}

// RetryPolicyFromEnv 读 env 得到重试预算。
//
// 非法值（空、非整数、负数）一律回落到出厂默认：env 是运维口子，不是配置
// 校验点——但**绝不**把负数当"无限重试"，那会让一次坏配置把会话卡在压缩循环里。
func RetryPolicyFromEnv(getenv func(string) string) RetryPolicy {
	if getenv == nil {
		getenv = os.Getenv
	}
	return RetryPolicy{
		Retries:         retryCount(getenv(EnvCompactRetries), DefaultCompactRetries),
		OverflowRetries: retryCount(getenv(EnvCompactOverflowRetries), DefaultCompactOverflowRetries),
	}
}

func retryCount(raw string, fallback int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return fallback
	}
	return n
}

// SummarizeWithRetry 在**可重试**失败上重试摘要生成。
//
// 不重试的两种：
//   - 参数错误（没有可摘要的消息）：重试也不会有不同结果，一次都不调模型；
//   - 上下文已取消/超时：那不是抖动，重试只会把取消拖成一次长等待。
//
// 返回的是**最后一次**失败（`errors.Is` 能追到原始错误），不会把中间几次的
// 错误吞掉换成一个笼统的"重试失败"。
func SummarizeWithRetry(ctx context.Context, chat Chat, model string, items []session.SurfaceMsg, policy RetryPolicy) (string, error) {
	if len(items) == 0 {
		// 走一遍 Summarize 拿到它那句准确的参数错误；不会调用模型。
		return Summarize(ctx, chat, model, items)
	}
	attempts := 1
	if policy.Retries > 0 {
		attempts += policy.Retries
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				// 已经失败过一次、现在又被取消：取消是**当前**最该报的原因。
				return "", err
			}
			return "", err
		}
		out, err := Summarize(ctx, chat, model, items)
		if err == nil {
			return out, nil
		}
		lastErr = err
	}
	return "", lastErr
}

// overflowSignals 是"provider 说这次请求超窗了"的文案特征。
//
// 各家 wire 的措辞都不一样，但都指向同一个可执行动作：**压缩后重试**。
// 不在这个清单里的错误一律不重试——把 401/404 当溢出重试只会掩盖真实原因。
var overflowSignals = []string{
	"context_length_exceeded",
	"maximum context length",
	"context length",
	"context window",
	"prompt is too long",
	"too many tokens",
	"reduce the length",
	"input is too long",
	"maximum number of tokens",
	"上下文长度",
	"上下文窗口",
	"超出上下文",
	"输入过长",
	"内容过长",
	"超出最大",
}

// IsContextOverflow 判断一个模型调用错误是不是"上下文窗口溢出"。
//
// 官方 `maxOverflowRetries` 的触发条件就是这个（"after a confirmed
// context-window overflow"）：只有确认是溢出，压缩才有意义——其它错误压完
// 还是同一个结果，白花一次摘要的钱。
func IsContextOverflow(err error) bool {
	if err == nil {
		return false
	}
	var gateway *llm.GatewayError
	if errors.As(err, &gateway) {
		if gateway.Kind != llm.GatewayStatus {
			return false // 连不上网关：不是窗口问题
		}
		switch gateway.Code {
		case 400, 413, 422:
			// 参数类状态码才可能带"太长了"；401/403/404/429/5xx 一律不认。
		default:
			return false
		}
	}
	text := strings.ToLower(err.Error())
	for _, signal := range overflowSignals {
		if strings.Contains(text, signal) {
			return true
		}
	}
	return false
}
