package v4

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// ReadSummary 从日志尾部往回找第一条助手/用户消息，返回截断到 maxRunes 的一句话。
//
// 有界：最多解 maxBytes 字节的帧（从最后一帧往前数）。找不到就返回空串和 nil——
// 没有消息的会话是合法的（header + 控制事件），那不是错误。
//
// 为什么要有它：列会话只要一句话，全量解码一份大会话要几十毫秒（183 个会话实测 4.37 秒）。
func ReadSummary(path string, maxBytes, maxRunes int) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("读会话文件失败: %w", err)
	}
	frames, _, err := splitFrames(b)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}

	dec, err := zstd.NewReader(nil)
	if err != nil {
		return "", fmt.Errorf("建解码器失败: %w", err)
	}
	defer dec.Close()

	budget := maxBytes
	var assistant, user string
	for i := len(frames) - 1; i >= 0 && budget > 0; i-- {
		f := frames[i]
		budget -= f.length
		raw, derr := decodeFrame(dec, b[f.offset:f.offset+f.length])
		if derr != nil {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var e struct {
				Type string          `json:"type"`
				Data json.RawMessage `json:"data"`
			}
			if json.Unmarshal([]byte(line), &e) != nil {
				continue
			}
			text := firstText(raw)
			if strings.TrimSpace(text) == "" {
				continue
			}
			switch e.Type {
			case "assistant/message":
				if assistant == "" {
					assistant = text
				}
			case "user/message":
				if user == "" {
					user = text
				}
			}
		}
		if assistant != "" {
			break
		}
	}
	out := assistant
	if out == "" {
		out = user
	}
	return ClipOneLine(out, maxRunes), nil
}

// ClipOneLine 把多行压成一行并截到 maxRunes 个字符。
func ClipOneLine(s string, maxRunes int) string {
	s = strings.Join(strings.Fields(s), " ")
	if maxRunes <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "…"
}

// firstText 从事件 data 里挖出第一段文字。
//
// 不去猜具体层级：v4 的消息体形状有好几种（message.content 块数组、
// content 直接是块数组、或更老的字符串），**递归找第一个 "text" 字符串**最省事，
// 也不会因为上游多加一层就整个失效。
func firstText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	return walkText(v)
}

func walkText(v any) string {
	switch t := v.(type) {
	case string:
		return ""
	case []any:
		for _, e := range t {
			if s := walkText(e); s != "" {
				return s
			}
		}
	case map[string]any:
		if s, ok := t["text"].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
		for _, k := range []string{"message", "content", "blocks"} {
			if e, ok := t[k]; ok {
				if s := walkText(e); s != "" {
					return s
				}
			}
		}
		for _, e := range t {
			if s := walkText(e); s != "" {
				return s
			}
		}
	}
	return ""
}
