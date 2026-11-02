package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"dshgo/codecs/v4"
	"dshgo/internal/kernel"
	"dshgo/internal/spi"
)

// historyHolder 把"当前会话的日志"暴露成可检索的服务。
//
// 它是**动态**的：装配时还没有会话，所以这里只是一个指针，
// 每轮 prompt 前由 ACP 层指过去。
type historyHolder struct {
	mu  sync.RWMutex
	dir string // 会话文件路径（空 = 还没有活动会话）
}

func (h *historyHolder) set(path string) {
	h.mu.Lock()
	h.dir = path
	h.mu.Unlock()
}

func (h *historyHolder) Search(query string, limit int) ([]spi.HistoryHit, error) {
	if query == "" {
		return nil, fmt.Errorf("query 为空")
	}
	h.mu.RLock()
	path := h.dir
	h.mu.RUnlock()
	if path == "" {
		return nil, fmt.Errorf("没有活动会话")
	}
	log, err := v4.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读会话失败: %w", err)
	}

	// 哪些 seq 已经被压缩遮蔽——命中了也要标出来，别让模型以为它还在表面上。
	hidden := hiddenSeqs(log)

	hits := make([]spi.HistoryHit, 0, limit)
	for _, ev := range log.Events {
		text := eventText(ev)
		if text == "" || !strings.Contains(text, query) {
			continue
		}
		hits = append(hits, spi.HistoryHit{
			Seq:     ev.Seq,
			Type:    ev.Type,
			Role:    roleOf(ev.Type),
			Excerpt: excerpt(text, query),
			Hidden:  hidden[ev.Seq],
		})
		if len(hits) >= limit {
			break
		}
	}
	return hits, nil
}

// hiddenSeqs 收集所有被压缩遮蔽的 seq。
func hiddenSeqs(log *v4.Log) map[int64]bool {
	out := map[int64]bool{}
	for _, ev := range log.Events {
		if ev.Type != "compaction/summary" && ev.Type != "compaction/prune" {
			continue
		}
		var d struct {
			ShadowedSeqs []int64 `json:"shadowedSeqs"`
		}
		if json.Unmarshal(ev.Data, &d) != nil {
			continue
		}
		for _, s := range d.ShadowedSeqs {
			out[s] = true
		}
	}
	return out
}

// eventText 抽出一段可用于检索的正文。
func eventText(ev v4.Event) string {
	var d struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return ""
	}
	raw := d.Message.Content
	if len(raw) == 0 {
		raw = d.Content
	}
	if len(raw) == 0 {
		return ""
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func roleOf(t string) string {
	switch t {
	case "user/message":
		return "user"
	case "assistant/message":
		return "assistant"
	case "tool/result":
		return "tool"
	case "system/message":
		return "system"
	}
	return ""
}

// excerpt 取命中位置前后各一小段，避免把整段历史倒给模型。
func excerpt(text, query string) string {
	const before, after = 80, 120
	idx := strings.Index(text, query)
	if idx < 0 {
		return text
	}
	start := idx - before
	if start < 0 {
		start = 0
	}
	end := idx + len(query) + after
	if end > len(text) {
		end = len(text)
	}
	s := text[start:end]
	s = strings.ReplaceAll(s, "\n", " ")
	prefix := ""
	if start > 0 {
		prefix = "…"
	}
	suffix := ""
	if end < len(text) {
		suffix = "…"
	}
	return prefix + s + suffix
}

func historyModule(h *historyHolder) kernel.Module {
	return kernel.Module{
		ID:       "history",
		Backend:  kernel.BackendStatic,
		Provides: []string{"history"},
		Register: func(s *kernel.Scope) error {
			return kernel.Provide(s, spi.HistoryKey, spi.HistorySearch(h))
		},
	}
}
