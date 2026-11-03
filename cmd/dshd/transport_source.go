package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"dshgo/codecs/v4"
	"dshgo/internal/session"
	"dshgo/internal/transport"
)

// sessionSource 是 transport.Source 的宿主侧实现：它才被允许读 v4 日志。
//
// 为什么放在 cmd/ 而不是 internal/transport：transport 是宿主线与界面线的契约
// （红线 R7），它不 import 任何会话日志的编解码包，也不碰文件系统。
// 「读日志 → 映射成 Message」这件事只能由宿主做，所以实现落在这一侧，
// transport 那边只留接口与分页逻辑。
type sessionSource struct {
	home string // <home>/sessions 的上一级
	cwd  string // 用来定位 projectKey；空则按 id 全盘找
}

func newSessionSource(home, cwd string) *sessionSource {
	return &sessionSource{home: home, cwd: cwd}
}

// Messages 实现 transport.Source。
//
// 映射只做三件事（表见任务）：user/message → user、assistant/message → assistant、
// tool/result → tool，其余事件一律跳过。`tool/call` 虽然不是消息，但它带着工具名与参数，
// 用来给紧随其后的 tool/result 补上 Tool/Args（补不了就留空，不编）。
// MessagesSince 只投影 seq > sinceSeq 的事件。
//
// 先过滤再投影，而不是先投影再过滤：投影要解 JSON、拼 text、查 tool 索引，
// 在 24k 事件的会话上那是 400 ms 级的活，而界面补洞时通常只要最后几条。
func (s *sessionSource) MessagesSince(sessionID string, sinceSeq int) ([]transport.Message, error) {
	path, err := s.resolve(sessionID)
	if err != nil {
		return nil, err
	}
	log, err := v4.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if sinceSeq > 0 {
		kept := log.Events[:0]
		for _, ev := range log.Events {
			if ev.Seq > int64(sinceSeq) {
				kept = append(kept, ev)
			}
		}
		log.Events = kept
	}
	return s.messagesOf(log.Events)
}

// MessagesTail 只投影最后 limit 条消息，同时把总数算出来。
//
// 为什么要单独一个：`session.load` 是界面点开会话时的第一条 method，
// 它只要首屏 50 条，但现在走的是"先投影全部再截尾"——24k 事件的会话要 480 ms。
// 计数只需要认事件类型，投影才要解 JSON、拼正文，两者代价差一个数量级。
func (s *sessionSource) MessagesTail(sessionID string, limit int) ([]transport.Message, int, error) {
	path, err := s.resolve(sessionID)
	if err != nil {
		return nil, 0, err
	}
	log, err := v4.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	isMsg := func(t string) bool {
		return t == "user/message" || t == "assistant/message" || t == "tool/result"
	}
	total := 0
	for _, ev := range log.Events {
		if isMsg(ev.Type) {
			total++
		}
	}
	if limit <= 0 || limit > total {
		limit = total
	}
	if limit == 0 {
		return []transport.Message{}, 0, nil
	}
	// 从后往前取 limit 个，保持原顺序。
	keep := make([]v4.Event, 0, limit)
	seen := 0
	for i := len(log.Events) - 1; i >= 0; i-- {
		if !isMsg(log.Events[i].Type) {
			continue
		}
		if seen == limit {
			break
		}
		keep = append(keep, log.Events[i])
		seen++
	}
	for i, j := 0, len(keep)-1; i < j; i, j = i+1, j-1 {
		keep[i], keep[j] = keep[j], keep[i]
	}
	msgs, err := s.messagesOf(keep)
	if err != nil {
		return nil, 0, err
	}
	return msgs[:min(len(msgs), limit)], total, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (s *sessionSource) Messages(sessionID string) ([]transport.Message, error) {
	path, err := s.resolve(sessionID)
	if err != nil {
		return nil, err
	}
	// 读日志出错原样返回：调用方要看得懂是权限、损坏还是尾部半帧，
	// 这里包一层反而会把 v4 已经写好的诊断盖掉。
	log, err := v4.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return s.messagesOf(log.Events)
}

// messagesOf 把一份（可能已被过滤过的）日志投影成消息。
func (s *sessionSource) messagesOf(events []v4.Event) ([]transport.Message, error) {
	calls := indexToolCalls(events)

	out := make([]transport.Message, 0, len(events))
	for _, ev := range events {
		switch ev.Type {
		case "user/message":
			text, ok := textOf(ev.Data, false)
			if !ok {
				continue
			}
			out = append(out, transport.Message{Seq: int(ev.Seq), Role: "user", Text: text})

		case "assistant/message":
			text, ok := textOf(ev.Data, true)
			if !ok {
				continue
			}
			out = append(out, transport.Message{Seq: int(ev.Seq), Role: "assistant", Text: text})

		case "tool/result":
			text, callID, ok := toolResultText(ev.Data)
			if !ok {
				continue
			}
			msg := transport.Message{Seq: int(ev.Seq), Role: "tool", Text: text}
			if c, found := calls[callID]; found {
				msg.Tool = c.name
				msg.Args = c.args
			}
			out = append(out, msg)
		}
	}

	// 契约要求按 Seq 升序（transport.LoadSession/HistorySession 都依赖它）。
	// 日志本来就是顺序追加的，这里排序只是兜住"文件被手工拼过"的边角。
	sort.SliceStable(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

// resolve 找出会话文件的位置。
//
// 不用 session.ResolvePath 的原因：它把"没找到"和"多处命中"都合成一句话，
// 调用方拿不到 transport.ErrNoSuchSession 这个哨兵值——界面得靠它区分
// 「会话没了」和「读失败」，所以这里自己判一次，多出来的是 10 行判定。
func (s *sessionSource) resolve(sessionID string) (string, error) {
	if sessionID == "" {
		return "", transport.ErrNoSuchSession
	}
	// 界面从列表拿到的是 "session-<uuid>"，磁盘上是裸 uuid；两种都得认，
	// 否则界面把 id 原样传回来就是 404，而那个 404 看起来像"会话不存在"。
	var segs []string
	for _, id := range session.CandidateIDs(sessionID) {
		seg, err := v4.EncodeSegment(id)
		if err != nil {
			continue
		}
		segs = append(segs, seg)
	}
	if len(segs) == 0 {
		// 非法 id（空、带路径分隔符之类）等于没有这个会话，不是读失败。
		return "", transport.ErrNoSuchSession
	}

	var matches []string
	for _, seg := range segs {
		if s.cwd != "" {
			direct := filepath.Join(s.home, "sessions", v4.ProjectKey(s.cwd), seg, "session.v4.jsonl.zstd")
			if _, statErr := os.Stat(direct); statErr == nil {
				return direct, nil
			}
		}
		got, gerr := filepath.Glob(filepath.Join(s.home, "sessions", "*", seg, "session.v4.jsonl.zstd"))
		if gerr != nil {
			return "", gerr
		}
		if len(got) > 0 {
			matches = got
			break
		}
	}
	switch len(matches) {
	case 0:
		return "", transport.ErrNoSuchSession
	case 1:
		return matches[0], nil
	default:
		// 与 session.FindByID 的取舍一致：多处命中就报错让人指定 cwd，不替人猜。
		return "", fmt.Errorf("会话 %s 在多个项目目录下都有（%d 个），请显式给 cwd", sessionID, len(matches))
	}
}

// block 是 v4 内容块的最小形状。
type block struct {
	Type      string `json:"type"`
	Text      string `json:"text"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// textOf 取一条消息的可见正文。
//
// user/message 的 content 在 data 顶层；assistant/message 的在 data.message.content。
// 只拼 type=="text" 的块——tool-call 块不是正文，它由 tool/result 那边对应。
func textOf(data json.RawMessage, nested bool) (string, bool) {
	var d struct {
		Content []block `json:"content"`
		Message struct {
			Content []block `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return "", false
	}
	blocks := d.Content
	if nested {
		blocks = d.Message.Content
	}
	return joinText(blocks), true
}

// toolResultText 取 tool/result 的正文与 callId。
func toolResultText(data json.RawMessage) (string, string, bool) {
	var d struct {
		Message struct {
			ToolCallID string  `json:"toolCallId"`
			Content    []block `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return "", "", false
	}
	return joinText(d.Message.Content), d.Message.ToolCallID, true
}

func joinText(blocks []block) string {
	out := ""
	for _, b := range blocks {
		if b.Type != "text" {
			continue
		}
		if out != "" {
			out += "\n"
		}
		out += b.Text
	}
	return out
}

type toolCallInfo struct {
	name string
	args string
}

// indexToolCalls 建 callId → 工具名/参数的索引，给 tool/result 补 Tool/Args。
func indexToolCalls(events []v4.Event) map[string]toolCallInfo {
	idx := make(map[string]toolCallInfo)
	for _, ev := range events {
		if ev.Type != "tool/call" {
			continue
		}
		var d struct {
			CallID    string `json:"callId"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}
		if err := json.Unmarshal(ev.Data, &d); err != nil {
			continue
		}
		if d.CallID == "" {
			continue
		}
		idx[d.CallID] = toolCallInfo{name: d.Name, args: d.Arguments}
	}
	return idx
}
