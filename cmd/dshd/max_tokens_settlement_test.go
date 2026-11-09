package main

// 官方 `DSH_MAX_TOKENS_AS_SUCCESS` 在**宿主结算层**的差异证据（GO.5 / T1）。
//
// internal/agent 的单测证明"循环怎么判"；这一份证明"日志里写成什么"——也就是
// 只读 v4 文件的消费者（回放、UI、history_search、取证）真正看到的东西：
//
//	env 未设 / true → turn/end{reason.kind: "max-tokens"}（这一轮跑完了，只是被截断）
//	env = false/0/no → turn/end{reason.kind: "error", error.message: …DSH_MAX_TOKENS_AS_SUCCESS…}
//
// 两条路的 kind 必须是这两个值之一，而且**同一条 turn 只能闭合一次**（D1 不变）。

import (
	"context"
	"testing"

	"dshgo/internal/agent"
	"dshgo/internal/llm"
)

// lengthChat 是"每次都只说到一半"的假模型：finish=length 且有正文。
type lengthChat struct{}

func (lengthChat) Stream(_ context.Context, _ llm.Request, onEvent func(llm.Event) error) (*llm.Response, error) {
	resp := &llm.Response{Content: "被截断的半句", FinishReason: "length"}
	if onEvent != nil {
		if err := onEvent(llm.Event{Kind: llm.EventContent, Text: resp.Content}); err != nil {
			return nil, err
		}
	}
	return resp, nil
}

func TestMaxTokensAsSuccessDrivesTurnSettlement(t *testing.T) {
	cases := []struct {
		name       string
		env        string
		wantKind   string
		wantDetail string
		wantErr    bool
	}{
		{"未设置（官方默认 true）", "", "max-tokens", "", false},
		{"显式 true", "true", "max-tokens", "", false},
		{"显式 false", "false", "error", "DSH_MAX_TOKENS_AS_SUCCESS", true},
		{"显式 0", "0", "error", "DSH_MAX_TOKENS_AS_SUCCESS", true},
		{"显式 no", "no", "error", "DSH_MAX_TOKENS_AS_SUCCESS", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(agent.EnvMaxTokensAsSuccess, c.env)

			logw := newD1Log(t)
			var closer *turnCloser
			loop := &agent.Loop{
				Chat:     lengthChat{},
				Model:    "test-model",
				MaxSteps: 4,
				Emit: func(ev agent.Event) error {
					// 与 cmd/dshd 的真实 Emit 同形：turn-end 走 closer，失败由出口兜底。
					if ev.Kind == agent.EventTurnEnd {
						return closer.End(ev.Finish)
					}
					return nil
				},
			}
			closer = newTurnCloser(logw, 1)

			_, err := loop.Run(context.Background(), 1, []llm.Message{{Role: "user", Content: "hi"}})
			if c.wantErr && err == nil {
				t.Fatalf("DSH_MAX_TOKENS_AS_SUCCESS=%q 时这一轮应当失败", c.env)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("DSH_MAX_TOKENS_AS_SUCCESS=%q 时这一轮应当正常收口：%v", c.env, err)
			}
			if exitErr := closer.Exit(context.Background(), err); exitErr != nil {
				t.Fatalf("出口兜底写结局失败: %v", exitErr)
			}
			d1AssertClosed(t, logw.Path, c.wantKind, c.wantDetail)
		})
	}
}
