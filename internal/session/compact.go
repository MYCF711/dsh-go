package session

import (
	"fmt"
	"sort"

	"dshgo/codecs/v4"
	"dshgo/internal/spi"
)

// Span 是要被压缩遮蔽的区间（按事件 seq，含端点）。
type Span struct {
	Start int64
	End   int64
}

// CompactionMeta 落进 compaction/summary 的元信息。
type CompactionMeta struct {
	Provider      string         `json:"provider,omitempty"`
	Model         string         `json:"model,omitempty"`
	MaxTokens     int            `json:"maxTokens,omitempty"`
	Usage         map[string]any `json:"usage,omitempty"`
	TokenCount    int            `json:"shadowedTokenCount,omitempty"`
	RawOutput     string         `json:"rawOutput,omitempty"`
	LLMStreamCall bool           `json:"llmStreamCall,omitempty"`
}

// Compact 追加 `compaction/start` → `compaction/summary` → `compaction/end` 三条事件，
// 返回被遮蔽的 seq 列表。
//
// 三条铁律（PLAN §10 / research/21）：
//  1. **日志永不删**——只追加，遮蔽发生在派生期；
//  2. `shadowedSeqs` 必须是被遮蔽事件的精确列表（不是范围近似）；
//  3. 摘要进入 surface 成为替身节点。
func Compact(l *Log, events []v4.Event, span Span, summary []spi.Block, meta CompactionMeta) (string, []int64, error) {
	if span.Start > span.End {
		return "", nil, fmt.Errorf("区间非法：start=%d > end=%d", span.Start, span.End)
	}
	shadowed := make([]int64, 0, span.End-span.Start+1)
	for _, ev := range events {
		if ev.Seq >= span.Start && ev.Seq <= span.End {
			shadowed = append(shadowed, ev.Seq)
		}
	}
	if len(shadowed) == 0 {
		return "", nil, fmt.Errorf("区间 [%d,%d] 里没有任何事件", span.Start, span.End)
	}
	sort.Slice(shadowed, func(i, j int) bool { return shadowed[i] < shadowed[j] })

	id := "cmp-" + NewID()
	blocks := make([]map[string]any, 0, len(summary))
	for _, b := range summary {
		blocks = append(blocks, map[string]any{"type": b.Type, "text": b.Text})
	}

	if err := l.Append("compaction/start", map[string]any{
		"compactionId": id,
	}); err != nil {
		return "", nil, err
	}
	if err := l.Append("compaction/summary", map[string]any{
		"compactionId":       id,
		"summary":            blocks,
		"shadowedRange":      map[string]any{"start": span.Start, "end": span.End},
		"shadowedSeqs":       shadowed,
		"shadowedTokenCount": meta.TokenCount,
		"provider":           meta.Provider,
		"model":              meta.Model,
		"maxTokens":          meta.MaxTokens,
		"usage":              meta.Usage,
		"rawOutput":          meta.RawOutput,
		"llmStreamCall":      meta.LLMStreamCall,
	}); err != nil {
		return "", nil, err
	}
	if err := l.Append("compaction/end", map[string]any{
		"compactionId": id,
	}); err != nil {
		return "", nil, err
	}
	return id, shadowed, nil
}
