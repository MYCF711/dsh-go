// Package transport 是宿主线与界面线唯一的握手点。
//
// 本文件只放契约：方法名与 JSON 形状。不放任何网络代码。
// 界面只认这里的字符串，改了就是破坏契约——先改快照，再两边各改各的。
package transport

// Method 是 /api 上的一个方法名。界面只认这些字符串，不许改。
type Method string

const (
	MethodSessionList    Method = "session.list"
	MethodSessionLoad    Method = "session.load"
	MethodSessionHistory Method = "session.history"
	MethodSessionPrompt  Method = "session.prompt"
	MethodSessionCancel  Method = "session.cancel"
	MethodSessionCreate  Method = "session.create"
)

// SessionMethods 是 U0 第一版要兑现的全部方法（顺序固定，快照按它排）。
func SessionMethods() []Method {
	return []Method{
		MethodSessionList, MethodSessionLoad, MethodSessionHistory,
		MethodSessionPrompt, MethodSessionCancel, MethodSessionCreate,
	}
}

// Message 是 session.load 里的一条消息。
// Role 必须是 user / assistant / tool 之一；tool 那三种字段只在 Role=="tool" 时出现。
type Message struct {
	Seq     int    `json:"seq"`
	Role    string `json:"role"`
	Text    string `json:"text"`
	Tool    string `json:"tool,omitempty"`
	Args    string `json:"args,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// SessionSummary 是 session.list 的一行。Title 要能看懂（一句话摘要）。
type SessionSummary struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	CWD       string `json:"cwd"`
	UpdatedAt string `json:"updatedAt"`
}

// LoadResult 是 session.load 的返回值。
type LoadResult struct {
	Messages  []Message `json:"messages"`
	Total     int       `json:"total"`
	Truncated bool      `json:"truncated"`
}

// PromptResult 是 session.prompt 的返回值：**立刻返回**，回答走 WS。
type PromptResult struct {
	Accepted bool   `json:"accepted"`
	TurnID   string `json:"turnId"`
}
