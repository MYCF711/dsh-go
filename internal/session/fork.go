package session

import (
	"fmt"
	"sort"
	"time"

	"dshgo/codecs/v4"
	"dshgo/internal/spi"
)

// Fork 从一个已有会话派生一个**新会话**。
//
// 语义：
//   - 继承的是 **surface**（模型真正看到的那部分），不是原始日志的全部事件——
//     被压缩遮蔽过的东西不会跟着过去，否则新会话会立刻重新超窗；
//   - 新 header 记 `parentSession`，血缘可查；
//   - **不继承审批记忆**（审批记忆本来就不落盘，这是刻意的）。
//
// 结果仍然是一份合法的 v4 日志：能被我们的解码器、也能被 DSH 的 zlib 读。
func Fork(src *v4.Log, home, cwd, newID string) (*Log, error) {
	if newID == "" {
		return nil, fmt.Errorf("fork 需要新的会话 id")
	}
	if src.Header.ID == newID {
		return nil, fmt.Errorf("新会话 id 不能和源会话相同")
	}

	surface := Surface(src)
	if len(surface) == 0 {
		return nil, fmt.Errorf("源会话没有可继承的 surface")
	}

	seg, err := v4.EncodeSegment(newID)
	if err != nil {
		return nil, err
	}
	path := dirOf(home, v4.ProjectKey(cwd), seg)
	h := src.Header
	h.ID = newID
	h.Cwd = cwd
	h.ParentSession = src.Header.ID
	h.CreatedAt = 0 // 由 v4.Create 重新打时间

	w, err := v4.Create(path, h)
	if err != nil {
		return nil, err
	}
	l := &Log{w: w, Path: path, now: time.Now}
	defer func() {
		if err != nil {
			l.Close()
		}
	}()

	for _, node := range surface {
		if err = copyNode(l, node); err != nil {
			return nil, err
		}
	}
	return l, nil
}

// copyNode 把 surface 上的一个节点写成新会话里的一条事件。
func copyNode(l *Log, node SurfaceMsg) error {
	m := node.Message
	switch {
	case node.Summary:
		// 压缩替身没法照搬（shadowedSeqs 指向的 seq 在新文件里不存在），
		// 改成一条来源标记为 compact-checkpoint 的 user 消息——语义最接近且不会撒谎。
		return l.Append("user/message", map[string]any{
			"content":   []spi.Block{{Type: "text", Text: m.Content}},
			"source":    map[string]any{"kind": "compact-checkpoint"},
			"role":      "user",
			"id":        NewID(),
			"surfaceOp": "append",
		})

	case node.Type == "system/message":
		return l.Append("system/message", SystemMessageData(1, 1, m.Content))

	case node.Type == "user/message":
		return l.Append("user/message", UserMessage([]spi.Block{{Type: "text", Text: m.Content}}))

	case node.Type == "assistant/message":
		blocks := make([]map[string]any, 0, 1+len(m.ToolCalls))
		if m.Content != "" {
			blocks = append(blocks, TextBlock(m.Content))
		}
		for _, tc := range m.ToolCalls {
			blocks = append(blocks, ToolCallBlock(tc.ID, tc.Function.Name, tc.Function.Arguments))
		}
		return l.Append("assistant/message", AssistantMessageEvent(1, 1, blocks, "stop", nil))

	case node.Type == "tool/result":
		blocks := []spi.Block{{Type: "text", Text: m.Content}}
		return l.Append("tool/result", ToolResultEvent(1, 1, m.ToolCallID, blocks, false, "", nil))
	}
	return nil // 其它类型（developer 等）先跳过，Fork 只保证 surface 语义
}

// dirOf 拼出会话文件路径（与 CreateSession 同规则）。
func dirOf(home, projectKey, seg string) string {
	parts := []string{home, "sessions", projectKey, seg, "session.v4.jsonl.zstd"}
	out := ""
	for i, p := range parts {
		if i > 0 && p != "" {
			out += "/"
		}
		out += p
	}
	return out
}

// ForkIDs 是给诊断用的：列出源会话的 surface 里各类节点数量。
func ForkIDs(src *v4.Log) map[string]int {
	out := map[string]int{}
	for _, n := range Surface(src) {
		key := n.Type
		if n.Summary {
			key = "compaction/summary"
		}
		out[key]++
	}
	return out
}

// SortedKeys 返回有序的键（诊断输出用）。
func SortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
