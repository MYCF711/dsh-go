package compact

import (
	"context"
	"errors"
	"testing"

	"dshgo/internal/llm"
)

// 这一组钉住官方 0.2.2 `context.compaction.compactionRetries`（默认 1）与
// `context.compaction.maxOverflowRetries`（默认 1）的宿主等价。
//
// 先红点：宿主以前一次 `Summarize` 失败就让整次压缩失败（`compactauto.go` /
// `acp.go` 都是 `if err != nil { return err }`），摘要调用抖一下这一轮就白压，
// 上下文继续往上顶。

type flakyChat struct {
	calls   int
	failFor int
	reply   string
	err     error
}

func (c *flakyChat) Stream(context.Context, llm.Request, func(llm.Event) error) (*llm.Response, error) {
	c.calls++
	if c.calls <= c.failFor {
		if c.err != nil {
			return nil, c.err
		}
		return nil, errors.New("网关 502")
	}
	return &llm.Response{Content: c.reply, FinishReason: "stop"}, nil
}

func TestSummarizeWithRetryRetriesOnceByDefault(t *testing.T) {
	chat := &flakyChat{failFor: 1, reply: "摘要"}
	policy := RetryPolicyFromEnv(func(string) string { return "" })
	if policy.Retries != DefaultCompactRetries || policy.OverflowRetries != DefaultCompactOverflowRetries {
		t.Fatalf("默认策略 = %+v，期望 retries=%d overflowRetries=%d",
			policy, DefaultCompactRetries, DefaultCompactOverflowRetries)
	}
	out, err := SummarizeWithRetry(context.Background(), chat, "m", surfaceOf(4), policy)
	if err != nil {
		t.Fatalf("首次失败后应该重试成功：%v", err)
	}
	if out != "摘要" || chat.calls != 2 {
		t.Fatalf("摘要 = %q，模型调用 %d 次；期望一次重试（2 次）", out, chat.calls)
	}
}

func TestSummarizeWithRetryExhaustsConfiguredRetries(t *testing.T) {
	chat := &flakyChat{failFor: 3, reply: "摘要"}
	policy := RetryPolicy{Retries: 2}
	if _, err := SummarizeWithRetry(context.Background(), chat, "m", surfaceOf(4), policy); err == nil {
		t.Fatal("重试次数用尽后必须把错误冒出来")
	}
	if chat.calls != 3 {
		t.Fatalf("模型调用 %d 次，期望 1 首发 + 2 重试", chat.calls)
	}

	noRetry := &flakyChat{failFor: 1, reply: "摘要"}
	if _, err := SummarizeWithRetry(context.Background(), noRetry, "m", surfaceOf(4), RetryPolicy{Retries: 0}); err == nil {
		t.Fatal("Retries=0 时首次失败必须直接失败")
	}
	if noRetry.calls != 1 {
		t.Fatalf("Retries=0 却调用了 %d 次模型", noRetry.calls)
	}
}

// 取消/超时不是"抖动"：重试只会白烧配额，还会把取消拖成一次长等待。
// 已经取消的 ctx 连首发都不该发（一次都不调模型）。
func TestSummarizeWithRetryStopsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	chat := &flakyChat{failFor: 10, reply: "摘要"}
	if _, err := SummarizeWithRetry(ctx, chat, "m", surfaceOf(4), RetryPolicy{Retries: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消必须原样冒出来，实际 %v", err)
	}
	if chat.calls != 0 {
		t.Fatalf("已取消的 ctx 还调了 %d 次模型", chat.calls)
	}
}

// 参数错误（没有可摘要的消息）重试也不会有不同结果：一次都不重试。
func TestSummarizeWithRetryDoesNotRetryEmptyInput(t *testing.T) {
	chat := &flakyChat{reply: "摘要"}
	if _, err := SummarizeWithRetry(context.Background(), chat, "m", nil, RetryPolicy{Retries: 1}); err == nil {
		t.Fatal("空输入必须报错")
	}
	if chat.calls != 0 {
		t.Fatalf("空输入不该调用模型，实际 %d 次", chat.calls)
	}
}

func TestRetryPolicyFromEnv(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want RetryPolicy
	}{
		{"默认", nil, RetryPolicy{Retries: 1, OverflowRetries: 1}},
		{"显式 0", map[string]string{EnvCompactRetries: "0", EnvCompactOverflowRetries: "0"}, RetryPolicy{}},
		{"显式 3", map[string]string{EnvCompactRetries: "3", EnvCompactOverflowRetries: "2"}, RetryPolicy{Retries: 3, OverflowRetries: 2}},
		{"非法回默认", map[string]string{EnvCompactRetries: "abc", EnvCompactOverflowRetries: "-1"}, RetryPolicy{Retries: 1, OverflowRetries: 1}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := RetryPolicyFromEnv(func(key string) string { return test.env[key] })
			if got != test.want {
				t.Fatalf("策略 = %+v，期望 %+v", got, test.want)
			}
		})
	}
}

func TestIsContextOverflow(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"普通抖动", errors.New("网关 502"), false},
		{
			"OpenAI 形状",
			&llm.GatewayError{Kind: llm.GatewayStatus, Code: 400,
				Body: `{"error":{"message":"This model's maximum context length is 65536 tokens","code":"context_length_exceeded"}}`},
			true,
		},
		{
			"Anthropic 形状",
			&llm.GatewayError{Kind: llm.GatewayStatus, Code: 400,
				Body: `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 220000 tokens > 200000 maximum"}}`},
			true,
		},
		{
			"中文网关",
			&llm.GatewayError{Kind: llm.GatewayStatus, Code: 400,
				Body: `{"error":{"message":"输入内容过长，超出上下文长度限制"}}`},
			true,
		},
		{
			"认证失败不算溢出",
			&llm.GatewayError{Kind: llm.GatewayStatus, Code: 401,
				Body: `{"error":{"message":"context length: invalid api key"}}`},
			false,
		},
		{
			"连不上不算溢出",
			&llm.GatewayError{Kind: llm.GatewayConnect, Err: errors.New("dial tcp: refused")},
			false,
		},
		{"裸文案也认", errors.New("context length exceeded"), true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := IsContextOverflow(test.err); got != test.want {
				t.Fatalf("IsContextOverflow(%v) = %v，期望 %v", test.err, got, test.want)
			}
		})
	}
}
