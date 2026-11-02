// Package session 是事件溯源与 canonical 模型。
//
// 这一层的职责边界：内核只认本包的 canonical 事件；DSH 的 v4 文件格式是
// 编解码器（codecs/v4），不是内核结构。拔掉 v4 不需要改内核一行。
package session

import (
	"encoding/json"
	"fmt"

	"dshgo/internal/spi"
)

// Block 就是 spi.Block：内容块只有一份定义，避免会话层与 agent 层各自漂移。
type Block = spi.Block

// ProjectResult 把内部 Result 投影成会话边界上的 content[]。
//
// 这是「结构化是宿主内部契约、v4 是边界编码」的那条缝：
// 内部可以有 Data / Artifacts / Truncated / Hint，落到会话里只剩文本块。
// 顺序固定：摘要 → 数据 → 截断说明 → 提示，保证同一结果永远产生同样的字节。
func ProjectResult(r spi.Result) []Block {
	out := make([]Block, 0, 4)

	// 工具自己渲染好了就用它的（对齐 DSH 的呈现约定），否则退回 Summary+JSON。
	if r.Text != "" {
		out = append(out, Block{Type: "text", Text: r.Text})
	} else {
		if r.Summary != "" {
			out = append(out, Block{Type: "text", Text: r.Summary})
		}
		if r.Data != nil {
			if b, err := json.Marshal(r.Data); err == nil && len(b) > 0 {
				out = append(out, Block{Type: "text", Text: string(b)})
			}
		}
	}

	if r.Truncated != nil {
		out = append(out, Block{
			Type: "text",
			Text: fmt.Sprintf("（已截断：保留 %d/%d 字节，全文在 %s）",
				r.Truncated.Kept, r.Truncated.Total, r.Truncated.Pointer),
		})
	}
	if r.Hint != "" {
		out = append(out, Block{Type: "text", Text: "提示：" + r.Hint})
	}
	if len(out) == 0 {
		out = append(out, Block{Type: "text", Text: "（无输出）"})
	}
	return out
}

// Flatten 把内容块拼成一段纯文本（给不接受分块的老接口用）。
func Flatten(blocks []Block) string {
	out := ""
	for i, b := range blocks {
		if i > 0 {
			out += "\n"
		}
		out += b.Text
	}
	return out
}
