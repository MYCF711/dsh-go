package subagent

// 官方 `agentLoop.agents[].id`（stable label）在**工具层**的形状：
//
//	{ "id": "reviewer", "description": "看一遍", "prompt": "…" }
//
// 三条钉子：
//  1. `id` 必须出现在 schema 里且**可选**（模型才发得出来，老调用方不受影响）；
//  2. `id` 原样透传给宿主（宿主据此派生 `${id}-session-<uuid>`）；
//  3. **description 不是 id**：只给 description 时 AgentID 必须为空——
//     "不许把 description 当 id 用"这句话在代码里就落在这条断言上。

import (
	"context"
	"encoding/json"
	"testing"

	"dshgo/internal/spi"
)

// spiResult 是一次成功的假委派结果。
func spiResult(text string) spi.SubagentResult {
	return spi.SubagentResult{Text: text, Steps: 1}
}

func TestSchemaExposesAgentIDAsOptionalString(t *testing.T) {
	var s struct {
		Properties map[string]struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(schema, &s); err != nil {
		t.Fatalf("schema 不是合法 JSON: %v", err)
	}
	prop, ok := s.Properties["id"]
	if !ok {
		t.Fatal("schema 里没有 id：模型永远发起不了具名委派（官方 agents[].id 无从对应）")
	}
	if prop.Type != "string" {
		t.Fatalf("id 类型 = %q，期望 string", prop.Type)
	}
	if prop.Description == "" {
		t.Fatal("id 没有描述：模型不知道这是稳定标签，会当成本次任务的临时名字")
	}
	for _, name := range s.Required {
		if name == "id" {
			t.Fatal("id 不能是必填：老调用方不传它必须仍然合法")
		}
	}
}

func TestAgentIDPassesThroughToHost(t *testing.T) {
	svc := &fakeRunner{res: spiResult("好")}
	tool := &Tool{svc: svc}
	if _, err := tool.Exec(context.Background(), json.RawMessage(
		`{"id":"reviewer","description":"查一件事","prompt":"自包含的任务描述"}`)); err != nil {
		t.Fatal(err)
	}
	if svc.got.AgentID != "reviewer" {
		t.Fatalf("id 没有透传到宿主: %#v", svc.got)
	}
	// 尾随空白要裁掉，否则派生的会话 id 会带空格、落在盘上找不着。
	if _, err := tool.Exec(context.Background(), json.RawMessage(
		`{"id":"  reviewer  ","description":"查一件事","prompt":"p"}`)); err != nil {
		t.Fatal(err)
	}
	if svc.got.AgentID != "reviewer" {
		t.Fatalf("id 没被裁空白: %q", svc.got.AgentID)
	}
}

// description 只是给人看的短描述。它**永远**不能变成 agent id——
// 否则每次委派的会话 id 都会随描述文字变，跨轮就再也指不回同一个子代理。
func TestDescriptionNeverBecomesAgentID(t *testing.T) {
	svc := &fakeRunner{res: spiResult("好")}
	tool := &Tool{svc: svc}
	if _, err := tool.Exec(context.Background(), json.RawMessage(
		`{"description":"reviewer","prompt":"自包含的任务描述"}`)); err != nil {
		t.Fatal(err)
	}
	if svc.got.AgentID != "" {
		t.Fatalf("description 被当成了 agent id: %q", svc.got.AgentID)
	}
}
