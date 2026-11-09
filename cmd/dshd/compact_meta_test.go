package main

// 压缩元数据的 `model` 必须是**真实使用的模型 id**。
//
// 同一条 CompactionMeta 在三条路上写：
//   - `cmd/dshd/compactauto.go:66` 用调用方传进来的 `model`；
//   - `cmd/dshd/acp.go:1202` 用 `r.model`；
//   - `cmd/dshd/main.go` 的 run 路径曾经写死 `"run"`（字面量，不是模型）。
//
// v4 是"这条摘要用什么模型压的"的唯一真相来源；写死一个不是模型的词，等于
// 让事后的人无法判断摘要质量差异是 Prompt 变了还是模型换了。

import (
	"context"
	"encoding/json"
	"testing"

	"dshgo/codecs/v4"
	"dshgo/internal/compact"
	"dshgo/internal/llm"
	"dshgo/internal/session"
	"dshgo/internal/spi"
)

const compactMetaModel = "group/deepseek-v4.1-flash-1017"

// seedCompactableSession 造一份"足够长、有模型可见消息"的会话。
func seedCompactableSession(t *testing.T, home, sid string) *session.Log {
	t.Helper()
	lg, err := session.CreateSession(home, t.TempDir(), sid, "default")
	if err != nil {
		t.Fatalf("建会话: %v", err)
	}
	t.Cleanup(func() { _ = lg.Close() })
	for i := 0; i < 6; i++ {
		if err := lg.Append("user/message", session.UserMessage([]spi.Block{
			{Type: "text", Text: "第 " + string(rune('0'+i)) + " 条用户消息，内容要够长才会被选中压缩。"},
		})); err != nil {
			t.Fatalf("写 user/message: %v", err)
		}
		if err := lg.Append("assistant/message", session.AssistantMessageEvent(1, i+1,
			[]map[string]any{session.TextBlock("第 " + string(rune('0'+i)) + " 条回答。")}, "stop", nil)); err != nil {
			t.Fatalf("写 assistant/message: %v", err)
		}
	}
	return lg
}

// compactionSummaryModel 读回 v4 里最后一条 compaction/summary 的 model 字段。
func compactionSummaryModel(t *testing.T, path string) string {
	t.Helper()
	back, err := v4.ReadFile(path)
	if err != nil {
		t.Fatalf("读回日志: %v", err)
	}
	model := ""
	found := false
	for _, ev := range back.Events {
		if ev.Type != "compaction/summary" {
			continue
		}
		var data struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(ev.Data, &data); err != nil {
			t.Fatalf("解析 compaction/summary: %v", err)
		}
		model = data.Model
		found = true
	}
	if !found {
		t.Fatal("日志里没有 compaction/summary：压缩没有真的发生")
	}
	return model
}

func TestRunCompactionMetaUsesRealModel(t *testing.T) {
	home := t.TempDir()
	lg := seedCompactableSession(t, home, "session-compact-meta")
	base := d1SSE(t, d1Text("摘要正文", "stop"))

	// budget=1：任何非空 surface 都超预算，压缩必定触发。
	cmp := runCompactor(llm.New(base, ""), lg, 1, compact.DefaultPolicy(), llm.ModelCapacity{}, compactMetaModel, &session.Meter{})
	if cmp == nil {
		t.Fatal("budget>0 时 runCompactor 必须返回可用的压缩器")
	}
	if _, err := cmp(context.Background(), 1, nil); err != nil {
		t.Fatalf("压缩出错: %v", err)
	}
	if got := compactionSummaryModel(t, lg.Path); got != compactMetaModel {
		t.Fatalf("压缩元数据 model=%q，期望真实模型 %q；写死 \"run\" 会让 v4 无法回答“摘要用哪个模型压的”",
			got, compactMetaModel)
	}
}
