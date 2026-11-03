// Package transport 是宿主线与界面线唯一的握手点。
//
// 本文件只放契约：方法名与 JSON 形状。不放任何网络代码。
// 界面只认这里的字符串，改了就是破坏契约——先改快照，再两边各改各的。
package transport

import "encoding/json"

// Method 是 /api 上的一个方法名。界面只认这些字符串，不许改。
type Method string

const (
	MethodSessionList    Method = "session.list"
	MethodSessionLoad    Method = "session.load"
	MethodSessionHistory Method = "session.history"
	MethodSessionPrompt  Method = "session.prompt"
	MethodSessionCancel  Method = "session.cancel"
	MethodSessionCreate  Method = "session.create"
	MethodSessionDelete  Method = "session.delete"
	MethodSessionFork    Method = "session.fork"
	MethodWorkspaceList  Method = "workspace.list"
	MethodFSRead         Method = "fs.read"
	MethodFSWrite        Method = "fs.write"
	MethodConfigGet      Method = "config.get"
	MethodConfigSet      Method = "config.set"
)

// SessionMethods 是 U0 第一版要兑现的全部方法（顺序固定，快照按它排）。
func SessionMethods() []Method {
	return []Method{
		MethodSessionList, MethodSessionLoad, MethodSessionHistory,
		MethodSessionPrompt, MethodSessionCancel, MethodSessionCreate,
		MethodSessionDelete, MethodSessionFork, MethodWorkspaceList,
		MethodFSRead, MethodFSWrite, MethodConfigGet, MethodConfigSet,
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
	// Time 是这条消息对应事件的墙钟时间（epoch 毫秒）。
	// 界面要算 tok/s：输出 token ÷ 相邻两条助手消息的时间差。
	// 没有它就算不出速率——这是 H2，2026-11-03 界面线提的。
	Time int64 `json:"time,omitempty"`
	// Usage 是助手消息带的用量（H1）。键与 v4 的 usage 一致，界面直接读。
	// nil 表示这次没有用量信息——**不许编 0**，那样界面会显示"0 tok/s"。
	Usage *UsageMsg `json:"usage,omitempty"`
	// View 是工具结果的渲染声明（H3），原样来自 spi.ViewSpec。
	// 缺省不出现——界面见到 `kind` 才选渲染器，没有就退回「工具名 + 摘要」。
	View json.RawMessage `json:"view,omitempty"`
}

// UsageMsg 是一条助手消息的用量。字段名照 v4 的 usage 原样搬，不另起一套。
type UsageMsg struct {
	InputTokens     int `json:"inputTokens,omitempty"`
	OutputTokens    int `json:"outputTokens,omitempty"`
	TotalTokens     int `json:"totalTokens,omitempty"`
	CacheReadTokens int `json:"cacheReadTokens,omitempty"`
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
	// Model 是这条会话最近一次请求用的模型 id（H4）。
	// **空字符串表示不知道**，界面就该整段不画——不许拿我们的默认值冒充，
	// 那样用户会以为"这个会话是用默认模型跑的"，而它可能不是。
	Model string `json:"model,omitempty"`
}

// PromptResult 是 session.prompt 的返回值：**立刻返回**，回答走 WS。
type PromptResult struct {
	Accepted bool   `json:"accepted"`
	TurnID   string `json:"turnId"`
}

// WorkspaceEntry 是 workspace.list 返回的一条。
//
// 只有三个字段：多一个都得由界面负责渲染，而界面现在只用得上这三个。
// 以后要加（比如修改时间）再显式改快照，不要"顺手多带点"。
type WorkspaceEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
}

// FSContent 是 fs.read 的返回：路径、正文、字节数。
type FSContent struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Size    int64  `json:"size"`
}
