package agent

// 官方 `DSH_MAX_TOKENS_AS_SUCCESS` 的接入证据（GO.5 / T1）。
//
// 官方语义（app.asar: @dsa/dsh-sdk-app/cordis.patch.yml:21，默认 true）：
// **把 max-tokens 结束当成功，除非显式 false**。所以：
//   - unset / true / 1 / yes → finish=length 照旧是"这一轮跑完了"（turn/end finish=length）；
//   - false / 0 / no → finish=length 结算成失败，错误原文点名这个变量。
//
// 两条路线的差别是**结算**，不是文案：一个返回 nil 并发出 turn-end，
// 一个返回 error 且不发出 turn-end（失败结局由调用方出口补 turn/end{kind:error}）。

import (
	"context"
	"os"
	"strings"
	"testing"

	"dshgo/internal/llm"
)

func TestMaxTokensFinishIsSuccessByDefault(t *testing.T) {
	for _, value := range []string{"", "true", "1", "yes", "TRUE"} {
		t.Run("env="+value, func(t *testing.T) {
			t.Setenv("DSH_MAX_TOKENS_AS_SUCCESS", value)
			chat := &scriptedChat{steps: []*llm.Response{{
				Content:      "被截断的半句",
				FinishReason: "length",
			}}}
			l, events := newLoop(t, chat)

			hist, err := l.Run(context.Background(), 1, []llm.Message{{Role: "user", Content: "hi"}})
			if err != nil {
				t.Fatalf("默认语义下 finish=length 不该失败：%v", err)
			}
			if len(hist) == 0 || hist[len(hist)-1].Content != "被截断的半句" {
				t.Fatalf("历史里没有这次回答：%+v", hist)
			}
			last := (*events)[len(*events)-1]
			if last.Kind != EventTurnEnd {
				t.Fatalf("最后一个事件不是 turn-end：%v", kinds(*events))
			}
			if last.Finish != "length" {
				t.Fatalf("turn-end finish = %q（应为 length）", last.Finish)
			}
		})
	}
}

func TestMaxTokensFinishFailsWhenEnvFalse(t *testing.T) {
	for _, value := range []string{"false", "0", "no", "FALSE", " false "} {
		t.Run("env="+strings.TrimSpace(value), func(t *testing.T) {
			t.Setenv("DSH_MAX_TOKENS_AS_SUCCESS", value)
			chat := &scriptedChat{steps: []*llm.Response{{
				Content:      "被截断的半句",
				FinishReason: "length",
			}}}
			l, events := newLoop(t, chat)

			_, err := l.Run(context.Background(), 1, []llm.Message{{Role: "user", Content: "hi"}})
			if err == nil {
				t.Fatalf("DSH_MAX_TOKENS_AS_SUCCESS=%q 时 finish=length 应当结算成失败", value)
			}
			if !strings.Contains(err.Error(), "DSH_MAX_TOKENS_AS_SUCCESS") {
				t.Fatalf("错误原文没有点名变量：%v", err)
			}
			for _, kind := range kinds(*events) {
				if kind == string(EventTurnEnd) {
					t.Fatalf("失败路径不该发出 turn-end：%v", kinds(*events))
				}
			}
		})
	}
}

// 非 length 的 finish 不受这个变量影响（不能误伤正常收工）。
func TestMaxTokensEnvDoesNotAffectOtherFinishes(t *testing.T) {
	t.Setenv("DSH_MAX_TOKENS_AS_SUCCESS", "false")
	chat := &scriptedChat{steps: []*llm.Response{{Content: "正常收工", FinishReason: "stop"}}}
	l, events := newLoop(t, chat)

	if _, err := l.Run(context.Background(), 1, []llm.Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("finish=stop 被 DSH_MAX_TOKENS_AS_SUCCESS 误伤：%v", err)
	}
	last := (*events)[len(*events)-1]
	if last.Kind != EventTurnEnd || last.Finish != "stop" {
		t.Fatalf("最后一个事件 = %+v", last)
	}
}

// 解析器本身：只认官方说的"显式 false"，其余一律 true。
func TestMaxTokensAsSuccessParsesEnv(t *testing.T) {
	cases := map[string]bool{
		"":        true,
		"true":    true,
		"TRUE":    true,
		"1":       true,
		"yes":     true,
		"on":      true,
		"maybe":   true, // 非"显式 false"→官方语义是成功
		"false":   false,
		"FALSE":   false,
		"0":       false,
		"no":      false,
		" false ": false,
		"NO":      false,
	}
	for value, want := range cases {
		got := MaxTokensAsSuccess(func(name string) string {
			if name == "DSH_MAX_TOKENS_AS_SUCCESS" {
				return value
			}
			return ""
		})
		if got != want {
			t.Fatalf("MaxTokensAsSuccess(%q) = %v，应为 %v", value, got, want)
		}
	}
	if !MaxTokensAsSuccess(nil) {
		t.Fatal("getenv 为 nil 时应按默认 true")
	}
	// 真实进程环境下也走同一条判断（os.Getenv 是生产用法）。
	t.Setenv("DSH_MAX_TOKENS_AS_SUCCESS", "no")
	if MaxTokensAsSuccess(os.Getenv) {
		t.Fatal("os.Getenv 路径没有读到 no")
	}
}
