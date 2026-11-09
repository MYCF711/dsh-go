package agent

// 官方 `llm.resolveModelInfo.contextWindowValidation`：
//
//	if (context !== undefined && (!Number.isInteger(context.contextWindow) || context.contextWindow <= 0))
//	    throw new LlmError(..., "INVALID_MODEL_CONTEXT")
//	（tmp/GO3-PARAMS/_official/dsh-llm/lib/types/index.js:629）
//
// 宿主旧实现把"声明了非正数"静默当成"未声明"，再回退内置/自定义默认容量：
// 用户把窗口写成 0 或负数时，界面上看起来一切正常，实际用的是 1M 的默认值
// （`docs/parameter-register.md` #19）。声明本来就是一次"我明确知道"，
// 写错了必须响亮失败，而不是猜一个更宽松的值。
//
// 先红判据：`agent.Loop.Capacity` 是常规请求的声明入口（docs/model-capacity.md §1）；
// 旧代码对 `ContextWindow: -1` 不报错，照发模型请求。

import (
	"context"
	"testing"

	"dshgo/internal/llm"
)

func TestLoopRejectsInvalidDeclaredContextWindow(t *testing.T) {
	// 只钉**负值**：宿主声明面是 int，零值在本仓契约里是"未声明"
	// （`--context-window` 帮助文本、docs/model-capacity.md §1、host_run_options.go
	// 每次透传的 `--context-window 0` 都依赖它）。官方 `undefined` 与"声明 0"的
	// 区分在目录面已经成立：目录里写 `contextWindow: 0` 读盘即被拒
	// （model_catalog.go 的正整数校验），不会静默回退。负值才是旧实现真正静默吞掉
	// 的那一半：把"写错了"当成"没写"，再回退到 1M 默认值。
	for _, declared := range []int{-1, -1000, -1_000_000} {
		chat := &scriptedChat{steps: []*llm.Response{{Content: "ok", FinishReason: "stop"}}}
		loop := &Loop{
			Chat:     chat,
			Model:    "custom/declared",
			Capacity: llm.ModelCapacity{ContextWindow: declared},
			MaxSteps: 1,
		}
		_, err := loop.Run(context.Background(), 1, []llm.Message{{Role: "user", Content: "hi"}})
		if err == nil {
			t.Fatalf("声明 contextWindow=%d 被静默当成未声明（回退默认容量），模型请求照发了 %d 次；"+
				"官方要求 fail-loud INVALID_MODEL_CONTEXT", declared, len(chat.reqs))
		}
		code, ok := llm.ModelContextErrorCode(err)
		if !ok || code != llm.ErrorCodeInvalidModelContext {
			t.Fatalf("contextWindow=%d 的错误 = %v，错误码 = %q/%v，期望 %s",
				declared, err, code, ok, llm.ErrorCodeInvalidModelContext)
		}
		// 声明非法时一个模型请求都不许发出去（fail-closed，不是"发了再报错"）。
		if len(chat.reqs) != 0 {
			t.Fatalf("contextWindow=%d 非法时仍然发了 %d 次模型请求", declared, len(chat.reqs))
		}
	}
}

// TestLoopAcceptsUndeclaredAndPositiveCapacity：零值 = 未声明（宿主既有的
// "0=按模型容量解析"契约，见 docs/model-capacity.md §1），不能被新校验误伤。
func TestLoopAcceptsUndeclaredAndPositiveCapacity(t *testing.T) {
	cases := []struct {
		name     string
		declared llm.ModelCapacity
	}{
		{name: "未声明（零值）", declared: llm.ModelCapacity{}},
		{name: "显式 32K/8K", declared: llm.ModelCapacity{ContextWindow: 32000, MaxOutput: 8000}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chat := &scriptedChat{steps: []*llm.Response{{Content: "ok", FinishReason: "stop"}}}
			loop := &Loop{Chat: chat, Model: "custom/declared", Capacity: tc.declared, MaxSteps: 1}
			if _, err := loop.Run(context.Background(), 1, []llm.Message{{Role: "user", Content: "hi"}}); err != nil {
				t.Fatalf("合法声明被判非法: %v", err)
			}
			if len(chat.reqs) != 1 {
				t.Fatalf("模型请求次数 = %d，期望 1", len(chat.reqs))
			}
		})
	}
}
