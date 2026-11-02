package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"dshgo/codecs/v4"
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

	calls := indexToolCalls(log.Events)

	out := make([]transport.Message, 0, len(log.Events))
	for _, ev := range log.Events {
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
	seg, err := v4.EncodeSegment(sessionID)
	if err != nil {
		// 非法 id（空、带路径分隔符之类）等于没有这个会话，不是读失败。
		return "", transport.ErrNoSuchSession
	}

	if s.cwd != "" {
		direct := filepath.Join(s.home, "sessions", v4.ProjectKey(s.cwd), seg, "session.v4.jsonl.zstd")
		if _, statErr := os.Stat(direct); statErr == nil {
			return direct, nil
		}
	}

	pattern := filepath.Join(s.home, "sessions", "*", seg, "session.v4.jsonl.zstd")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return "", err
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
