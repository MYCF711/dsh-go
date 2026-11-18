package llm

import (
	"context"
	"net"
	"net/url"
	"os"
	"testing"
	"time"
)

// requireLiveGateway 是"真机端点"这条用例的**前置条件**判据，不是判据的一部分。
//
// 为什么单独拆出来（2026-11-19，task-86）：
//
// 这条用例回答的问题是「模型流式返回的 reasoning 是否被我方正确接收」——
// 那是**产品行为**。而"网关今天就开着吗"是**环境前置条件**，两件事红起来长得一样：
//
//	真机调用失败: 连不上模型网关 http://127.0.0.1:9/v1：确认网关在跑、--base 地址对
//
// 端点不可用时判 FAIL，等于把"没开网关"记成"产品坏了"——CI 上就是一条红得没有信息量的
// 记录（同一类错误参见 cmd/dshd 的 ConPTY 就绪判据）。所以先探活：端口连不上 ⇒ SKIP
// 并把**原因和地址**打出来；端口活着但返回内容不对 ⇒ 仍然 FAIL（那才是产品判据）。
//
// 判据只做 TCP 连通性，不发认证请求：探活本身不该依赖 key，也不该产生模型调用费用。
func requireLiveGateway(t *testing.T, base string) {
	t.Helper()
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" {
		t.Skipf("模型网关地址不可解析（%q）：%v —— 端点前置条件不满足，不是产品判据", base, err)
	}
	host := parsed.Host
	if parsed.Port() == "" {
		if parsed.Scheme == "https" {
			host = net.JoinHostPort(parsed.Hostname(), "443")
		} else {
			host = net.JoinHostPort(parsed.Hostname(), "80")
		}
	}
	conn, err := net.DialTimeout("tcp", host, 3*time.Second)
	if err != nil {
		t.Skipf("模型端点不可用（%s）：%v —— 本用例要真网关，端点没起来时这是环境前置条件，不判 FAIL；"+
			"起好网关（或给 DSHD_LLM_BASE 指到能用的地址）再跑", base, err)
	}
	_ = conn.Close()
}

// TestLiveMagpie 打真机：验证「能流式收到 reasoning」这条 P0 判据的前半。
//
// 默认跳过（CI 上没有网关）。本机跑：
//
//	DSHD_LIVE_LLM=1 go test ./internal/llm -run TestLiveMagpie -v
func TestLiveMagpie(t *testing.T) {
	if os.Getenv("DSHD_LIVE_LLM") != "1" {
		t.Skip("设置 DSHD_LIVE_LLM=1 才跑真机")
	}
	base := os.Getenv("DSHD_LLM_BASE")
	if base == "" {
		base = "http://127.0.0.1:3425/v1"
	}
	requireLiveGateway(t, base)
	model := os.Getenv("DSHD_LLM_MODEL")
	if model == "" {
		model = "group/deepseek-v4.1-flash-1017"
	}

	c := New(base, os.Getenv("DSHD_LLM_KEY"))
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	var reasoningBytes, contentBytes, chunks int
	start := time.Now()
	resp, err := c.Stream(ctx, Request{
		Model:     model,
		Messages:  []Message{{Role: "user", Content: "用一句话说明为什么 1+1=2。"}},
		MaxTokens: 128,
	}, func(e Event) error {
		chunks++
		switch e.Kind {
		case EventReasoning:
			reasoningBytes += len(e.Text)
		case EventContent:
			contentBytes += len(e.Text)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("真机调用失败: %v", err)
	}
	t.Logf("模型=%s 时长=%s 分片=%d reasoning=%d 字节 content=%d 字节 finish=%q",
		model, time.Since(start).Round(time.Millisecond), chunks, reasoningBytes, len(resp.Content), resp.FinishReason)
	t.Logf("usage: %+v", resp.Usage)

	if contentBytes == 0 {
		t.Fatal("没有收到任何 content 增量")
	}
	if reasoningBytes == 0 {
		t.Error("没有收到 reasoning 增量（模型/网关可能没开思维链；不算失败，但要记下来）")
	}
}
