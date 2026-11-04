package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	// 思考块得回原始事件取：Surface 只装 text + tool_calls（见 reasoningBlocksBySeq）。
	reasoning := reasoningBlocksBySeq(src)

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
		if err = copyNode(l, node, reasoning[node.Seq]); err != nil {
			return nil, err
		}
	}
	return l, nil
}

// reasoningBlocksBySeq 标出哪些 assistant/message 事件带思考块（seq → 思考正文，按原顺序）。
//
// 为什么不能从 SurfaceMsg 拿：`llm.Message` 没有 reasoning 字段，`type:"reasoning"`
// 的块在投影那一步就被丢掉了。fork 要保住"它为什么这么干"，只能回原始事件按 seq 取。
// 这样也**不用改 SurfaceMsg 的公开形状**（只读 v4.Log.Events）。
//
// 与 cmd/dshd/serve_turn.go 的同名函数同一个识别形状：message.content 优先、顶层
// content 兜底、只认 `type == "reasoning"` 且正文非空。差别只有一处——那边只回答
// "有没有"（取第一条就够，llm.Message 装不下正文），这里要把正文搬进新日志，
// 所以**全带**，不把第二条思考块当"重复的"丢掉。
func reasoningBlocksBySeq(log *v4.Log) map[int64][]string {
	out := make(map[int64][]string)
	for _, ev := range log.Events {
		if ev.Type != "assistant/message" {
			continue
		}
		var d struct {
			Message struct {
				Content []block `json:"content"`
			} `json:"message"`
			Content []block `json:"content"`
		}
		if err := json.Unmarshal(ev.Data, &d); err != nil {
			continue
		}
		blocks := d.Message.Content
		if len(blocks) == 0 {
			blocks = d.Content
		}
		var texts []string
		for _, b := range blocks {
			if b.Type == "reasoning" && strings.TrimSpace(b.Text) != "" {
				texts = append(texts, b.Text)
			}
		}
		if len(texts) > 0 {
			out[ev.Seq] = texts
		}
	}
	return out
}

// copyNode 把 surface 上的一个节点写成新会话里的一条事件。
//
// reasoning 是源事件里的思考正文（surface 装不下，见 reasoningBlocksBySeq），
// 只有 assistant/message 会用到。
func copyNode(l *Log, node SurfaceMsg, reasoning []string) error {
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
		// 块顺序与 cmd/dshd/serve_turn.go 的 assistantBlocks 一致：
		// 思考 → 正文 → 工具调用。
		//
		// 以前这里只铺 text + tool_calls：fork 出来的会话看起来一切正常，
		// 只是模型再也想不起父会话为什么那么干；而"只有思考"的回答会变成
		// 一条 content 为空的事件（投影读回来一个空气泡）。
		blocks := make([]map[string]any, 0, len(reasoning)+1+len(m.ToolCalls))
		for _, text := range reasoning {
			blocks = append(blocks, ReasoningBlock(text))
		}
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

// FindByID 在 <home>/sessions/*/<segment>/ 里按会话 id 找会话文件。
//
// 只用于"客户端只给了 id"的场景（比如 ACP 的 deleteSession）。
// 找到多个就报错——同名会话不该被随便挑一个删掉。
func FindByID(home, sessionID string) (string, error) {
	seg, err := v4.EncodeSegment(sessionID)
	if err != nil {
		return "", err
	}
	pattern := filepath.Join(home, "sessions", "*", seg, "session.v4.jsonl.zstd")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return "", err
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("找不到会话 %s（在 %s 下）", sessionID, home)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("会话 %s 在多个项目目录下都有（%d 个），请显式给 cwd", sessionID, len(matches))
	}
}

// ResolvePath 找一份会话的文件路径：先按 (cwd, id) 算，找不到再按 id 全库找。
//
// 为什么需要回退：projectKey 是按 **cwd 字符串**算出来的，同一个逻辑 workspace
// 换个盘符/挂载点/另一台机器，目录名就变了。而 session id 是全局唯一的（uuid），
// 实测踩过：拿正确的 id + 换过的 cwd 去 resume，报"找不到会话"。
//
// 与 FindByID 的取舍一致：多处命中就报错让人指定 cwd，不替人猜。
func ResolvePath(home, cwd, sessionID string) (string, error) {
	direct, err := SessionPath(home, cwd, sessionID)
	if err != nil {
		return "", err
	}
	if _, statErr := os.Stat(direct); statErr == nil {
		return direct, nil
	}
	found, ferr := FindByID(home, sessionID)
	if ferr != nil {
		return "", fmt.Errorf("按 cwd 算出的位置没有（%s），按 id 也没找到: %w", direct, ferr)
	}
	return found, nil
}
