package session

import (
	"encoding/json"

	"dshgo/codecs/v4"
	"dshgo/internal/llm"
)

// SurfaceMsg 是 surface 上的一个节点（模型真正看到的东西）。
type SurfaceMsg struct {
	Seq     int64
	Type    string // 事件类型
	Source  string // user / system / developer / tool / compact-checkpoint
	Message llm.Message
	Summary bool // 来自 compaction/summary
}

// Surface 把事件日志折叠成 surface。
//
// 三条规则（research/21 §"投影"）：
//  1. 只有 5 类事件参与派生；
//  2. `compaction/summary` / `compaction/prune` 的 `shadowedSeqs` **从 surface 消失**（日志本身永不删）；
//  3. 摘要本身作为 `compact-checkpoint` 节点进入 surface——它是"被压缩历史的替身"。
//
// 日志里仍有全部事件；这里只是派生。
func Surface(log *v4.Log) []SurfaceMsg {
	hidden := map[int64]bool{}
	var checkpoints []struct {
		seq   int64
		order int
	}
	order := map[int64]int{}

	for i, ev := range log.Events {
		order[ev.Seq] = i
		switch ev.Type {
		case "compaction/summary", "compaction/prune":
			var d struct {
				Summary       []block `json:"summary"`
				ShadowedSeqs  []int64 `json:"shadowedSeqs"`
				ShadowedRange *struct {
					Start int64 `json:"start"`
					End   int64 `json:"end"`
				} `json:"shadowedRange"`
			}
			if err := json.Unmarshal(ev.Data, &d); err != nil {
				continue
			}
			for _, s := range d.ShadowedSeqs {
				hidden[s] = true
			}
			// shadowedRange 是 shadowedSeqs 的宽松上界；只在没有明细时兜底。
			if len(d.ShadowedSeqs) == 0 && d.ShadowedRange != nil {
				for s := d.ShadowedRange.Start; s <= d.ShadowedRange.End; s++ {
					hidden[s] = true
				}
			}
			if ev.Type == "compaction/summary" {
				checkpoints = append(checkpoints, struct {
					seq   int64
					order int
				}{ev.Seq, i})
			}
		}
	}

	// surfaceOp 的替换：把 [start,end] 从 surface 摘掉（日志里不动）。
	for _, ev := range log.Events {
		kind, start, end := ev.SurfaceOpKind()
		if kind != "replace" {
			continue
		}
		for s := start; s <= end; s++ {
			hidden[s] = true
		}
	}

	summaryText := map[int64]string{}
	for _, cp := range checkpoints {
		ev := log.Events[cp.order]
		var d struct {
			Summary []block `json:"summary"`
		}
		if err := json.Unmarshal(ev.Data, &d); err == nil {
			summaryText[cp.seq] = joinText(d.Summary)
		}
	}

	out := make([]SurfaceMsg, 0, len(log.Events))
	for _, ev := range log.Events {
		if hidden[ev.Seq] {
			continue
		}
		if text, ok := summaryText[ev.Seq]; ok {
			out = append(out, SurfaceMsg{
				Seq: ev.Seq, Type: ev.Type, Source: "compact-checkpoint",
				Summary: true,
				Message: llm.Message{Role: "user", Content: text},
			})
			continue
		}
		if m, src, ok := surfaceMessage(ev); ok {
			out = append(out, SurfaceMsg{Seq: ev.Seq, Type: ev.Type, Source: src, Message: m})
		}
	}
	return out
}

// surfaceMessage 把一条耐久事件变成 surface 消息。不在 5 类里的返回 ok=false。
func surfaceMessage(ev v4.Event) (llm.Message, string, bool) {
	switch ev.Type {
	case "system/message", "developer/message":
		var d struct {
			Message struct {
				Content []block `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(ev.Data, &d); err != nil {
			return llm.Message{}, "", false
		}
		role := "system"
		if ev.Type == "developer/message" {
			role = "developer"
		}
		return llm.Message{Role: role, Content: joinText(d.Message.Content)}, role, true

	case "user/message":
		m, ok := userMessage(ev.Data)
		if !ok {
			return llm.Message{}, "", false
		}
		src := "user"
		var d struct {
			Source struct {
				Kind string `json:"kind"`
			} `json:"source"`
		}
		if json.Unmarshal(ev.Data, &d) == nil && d.Source.Kind != "" {
			src = d.Source.Kind
		}
		return m, src, true

	case "assistant/message":
		m, ok := assistantMessage(ev.Data)
		return m, "assistant", ok

	case "tool/result":
		m, ok := toolMessage(ev.Data)
		return m, "tool", ok
	}
	return llm.Message{}, "", false
}
