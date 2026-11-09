package llm

// 官方 `llm.resolveModelInfo.contextWindowValidation` 的宿主落点
// （tmp/GO3-PARAMS/_official/dsh-llm/lib/types/index.js:629）：
//
//	if (context !== undefined && (!Number.isInteger(context.contextWindow) || context.contextWindow <= 0))
//	    throw new LlmError(..., "INVALID_MODEL_CONTEXT")
//
// 宿主声明面是 int（`--context-window` / `agent.Loop.Capacity`），零值在本仓契约里
// 表示"未声明"（docs/model-capacity.md §1）。旧实现把**负值**也归进"未声明"，
// 于是"写错了"被静默替换成内置/自定义默认容量：用户看不到自己写错了
// （docs/parameter-register.md #19）。

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateDeclaredContextWindow(t *testing.T) {
	for _, value := range []int{-1, -255, -1_000_000} {
		err := ValidateDeclaredContextWindow("custom/model", value)
		if err == nil {
			t.Fatalf("contextWindow=%d 被接受：负值必须是 INVALID_MODEL_CONTEXT", value)
		}
		var invalid *InvalidModelContextError
		if !errors.As(err, &invalid) {
			t.Fatalf("错误类型 = %T（%v），期望 *InvalidModelContextError", err, err)
		}
		if invalid.Code() != ErrorCodeInvalidModelContext {
			t.Fatalf("错误码 = %q，期望 %q", invalid.Code(), ErrorCodeInvalidModelContext)
		}
		if invalid.Field != "contextWindow" || invalid.Value != value {
			t.Fatalf("错误字段 = %s=%d，期望 contextWindow=%d", invalid.Field, invalid.Value, value)
		}
		if code, ok := ModelContextErrorCode(err); !ok || code != ErrorCodeInvalidModelContext {
			t.Fatalf("ModelContextErrorCode = %q/%v", code, ok)
		}
		// 错误文案要能照做：说清"正整数"、"不会静默回退"、"0 = 不声明"。
		for _, want := range []string{"正整数", "INVALID_MODEL_CONTEXT", "静默回退"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("错误文案缺 %q：%s", want, err.Error())
			}
		}
	}
	// 0 是"未声明"，正值是合法声明：都不能被这个校验误伤。
	for _, value := range []int{0, 1, 32000, 1_000_000} {
		if err := ValidateDeclaredContextWindow("custom/model", value); err != nil {
			t.Fatalf("contextWindow=%d 被判非法: %v", value, err)
		}
	}
}

func TestResolveModelCapacityChecked(t *testing.T) {
	if _, err := ResolveModelCapacityChecked("custom/x", ModelCapacity{ContextWindow: -1}); err == nil {
		t.Fatal("负值声明没有被拦下")
	}
	// 合法/未声明时与旧解析点逐值一致：新校验不能顺手改掉默认值。
	cases := []struct {
		modelID  string
		declared ModelCapacity
	}{
		{modelID: "custom/x"},
		{modelID: "custom/x", declared: ModelCapacity{ContextWindow: 32000, MaxOutput: 8000}},
		{modelID: "group/deepseek-v4.1-flash-1017"},
	}
	for _, tc := range cases {
		got, err := ResolveModelCapacityChecked(tc.modelID, tc.declared)
		if err != nil {
			t.Fatalf("合法声明报错: %v", err)
		}
		if want := ResolveModelCapacity(tc.modelID, tc.declared); got != want {
			t.Fatalf("ResolveModelCapacityChecked(%q, %+v) = %+v，与旧解析点 %+v 不一致",
				tc.modelID, tc.declared, got, want)
		}
	}
}

// TestModelContextErrorCodeIgnoresOtherErrors：别的错误不能被误判成这个码，
// 否则 Transport 会把"网关 500"翻译成"你的窗口写错了"。
func TestModelContextErrorCodeIgnoresOtherErrors(t *testing.T) {
	if code, ok := ModelContextErrorCode(errors.New("boom")); ok || code != "" {
		t.Fatalf("普通错误被识别成 %q", code)
	}
	if code, ok := ModelContextErrorCode(nil); ok || code != "" {
		t.Fatalf("nil 被识别成 %q", code)
	}
}
