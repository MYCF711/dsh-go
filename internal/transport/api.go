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
	// MethodSessionApprove 回一次审批（H13）。请求走 WS，应答走 HTTP，与 prompt/cancel 一致。
	MethodSessionApprove Method = "session.approve"
	MethodSessionCreate  Method = "session.create"
	MethodSessionDelete  Method = "session.delete"
	MethodSessionFork    Method = "session.fork"
	// MethodSessionCompact 让客户端能主动压一次（界面上的"压缩"，官方 /compact）。
	MethodSessionCompact Method = "session.compact"
	MethodWorkspaceList  Method = "workspace.list"
	// MethodJobsList / MethodJobsOutput 是**后台任务**（bash 的
	// `run_in_background: true` 起出来的那批）。底层 `spi.Job` 早就实现了
	// List/Output/Kill，只是 Transport 上一直没有这两个方法，界面就看不到它们。
	MethodJobsList   Method = "jobs.list"
	MethodJobsOutput Method = "jobs.output"
	// MethodScheduleList 列定时任务。存储层（internal/schedule）早就有了，
	// `dshd schedule list` 一直在用；缺的只是 Transport 上这个方法，
	// 所以界面一直看不到"这个工作区有哪些定时任务"。
	MethodScheduleList Method = "schedule.list"
	// MethodSessionSetModel 给会话设一个"下一轮用哪个模型"的覆盖。
	//
	// 为什么需要：`session.load` 里的 model 是**上一轮实际用的**（只读投影），
	// 而界面上的模型选择器要能改。没有这个方法，那个下拉就只能画死。
	// 覆盖存在 `<home>/state/session-model.json`（**不碰 v4**：R5 说读路径不写盘）。
	MethodSessionSetModel Method = "session.setModel"
	// MethodSessionSetPermission 给会话设一个"下一轮用什么审批策略"的覆盖。
	//
	// 和 setModel 同构：`load` 里的 permission 是只读投影，界面要能改就得有这个方法。
	// 覆盖存 `<home>/state/session-permission.json`（**不碰 v4**）。
	MethodSessionSetPermission Method = "session.setPermission"
	// MethodModelsList 问网关有哪些模型（OpenAI 兼容的 GET /v1/models）。
	//
	// 为什么必须由宿主转发：网关地址与凭据只在宿主这一侧（R7 界面不直连模型出口）。
	// 界面要模型下拉就只能走这里——或者在界面里硬编码模型名，那会过期、且会骗人。
	MethodModelsList Method = "models.list"
	// MethodTerminalOpen/Input/Output 是一条**长连接 shell** 的三个动作。
	//
	// 和 `jobs.*` 的区别：job 是"跑一条命令、有结束"；terminal 是"一直开着、
	// 用户随时敲"。底层 `modules/exec/bash` 的 `session` 本来就是持久 PTY，
	// 只是一直没有"开一个留住它"的入口。
	// MethodSessionSetGoal 设/清这条会话的 goal（一句话：现在在做什么）。
	//
	// 和 setModel/setPermission 同构，只是值是一段自由文本。
	// 界面上的 GoalBar 就停靠在 composer 上方——它在回答"我在干嘛"，
	// 而这个问题每时每刻都在被问。
	MethodSessionSetGoal Method = "session.setGoal"
	MethodTerminalOpen   Method = "terminal.open"
	MethodTerminalInput  Method = "terminal.input"
	MethodTerminalOutput Method = "terminal.output"
	MethodFSRead         Method = "fs.read"
	MethodFSWrite        Method = "fs.write"
	MethodConfigGet      Method = "config.get"
	MethodConfigSet      Method = "config.set"
)

// SessionMethods 是 U0 第一版要兑现的全部方法（顺序固定，快照按它排）。
func SessionMethods() []Method {
	return []Method{
		MethodSessionList, MethodSessionLoad, MethodSessionHistory,
		MethodSessionPrompt, MethodSessionCancel, MethodSessionApprove, MethodSessionCreate,
		MethodSessionDelete, MethodSessionFork, MethodSessionCompact, MethodWorkspaceList,
		MethodJobsList, MethodJobsOutput, MethodScheduleList,
		MethodSessionSetModel, MethodSessionSetPermission, MethodModelsList,
		MethodSessionSetGoal,
		MethodTerminalOpen, MethodTerminalInput, MethodTerminalOutput,
		MethodFSRead, MethodFSWrite, MethodConfigGet, MethodConfigSet,
	}
}

// CompactResult 是 session.compact 的返回值。
//
// Compacted=false 表示"现在不需要压"，**不是错误**：会话还没长到该压的时候，
// 这是正常结果；把它当错误会让界面弹一个红框告诉用户"操作失败"。
type CompactResult struct {
	Compacted bool  `json:"compacted"`
	From      int64 `json:"from,omitempty"`
	To        int64 `json:"to,omitempty"`
	Shadowed  int   `json:"shadowed,omitempty"`
	Summary   int   `json:"summaryChars,omitempty"`
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
	// Reasoning 是这条助手消息的思考正文（可选，2026-11-04 加，**只加不改**）。
	//
	// 为什么必须有：v4 的 assistant/message 里 `type=reasoning` 的块不只是一句
	// "它想过"——模型想了、却没有输出正文时，它就是这次回答的**全部内容**。
	// 旧投影只取 text 块，读回来是一条空消息；而 serve 推的 assistant/message 帧
	// 本来就带 reasoning（serve_turn.go 只在正文为空时才带）。断线重连/重开
	// 走 session.load 时没有它，等于把这条回答丢了。
	//
	// 空串表示"这条没有思考块"，或"有正文，不必再带思考"——与推帧同形：
	// 有正文时思考是草稿，不是已经给用户的回答。老客户端忽略未知键即可。
	Reasoning string `json:"reasoning,omitempty"`
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
	// Goal 是这条会话当前在做的事（一句话）。空 = 没设过，界面不画 GoalBar。
	Goal string `json:"goal,omitempty"`
	// Permission 是当前审批策略（deny/ask/allow/auto，H5）。
	// **不是 mode**：mode 决定模型能看见哪些工具，权限决定写操作要不要问人。
	Permission string `json:"permission,omitempty"`
	// ContentWidth 是会话内容列的宽度（逻辑像素，H12）。
	//
	// 为什么由宿主给而不是界面自己算：界面在槽位里只拿得到整窗宽度，
	// 而官方气泡上限是 `min(列宽*0.702, 82%)`——没有列宽这个数就只能瞎猜比例。
	// **0 表示不知道**（omitempty），界面沿用自己那套；不许填一个"看着像"的默认值。
	ContentWidth int `json:"contentWidth,omitempty"`
	// UsageTotal 是**整个会话**的累计用量（H1 的顶层合计）。
	//
	// 与逐条 usage 的关系：逐条是"那一轮花了多少"，这个是"这个会话一共花了多少"。
	// **tail 切页不许改变它**——切页只影响返回哪几条消息，不影响全会话统计。
	// 键名与逐条一致（inputTokens/outputTokens），另加 cacheReadTokens 累计。
	UsageTotal *UsageMsg `json:"usageTotal,omitempty"`
	// ContextTokens 是**最近一条** assistant 的 totalTokens，也就是"当前上下文占多少"。
	// 它跟累计值是两件事：累计一直涨，上下文会随压缩回落。
	ContextTokens int `json:"contextTokens,omitempty"`
	// ContextWindow 是这条会话模型的上限（token）。
	// **0 表示不知道**：界面据此决定"画进度条"还是"只报数字"。
	// 不许填一个"看着像"的默认值——那会让百分比永远算错。
	ContextWindow int `json:"contextWindow,omitempty"`
	// JournalEpoch/LastSeq 让客户端拿到"我现在该从哪个水位接着收"（H14）。
	// load 只是首屏投影，**不能用来补洞**；但它必须告诉客户端 journal 在哪。
	JournalEpoch int64 `json:"journalEpoch,omitempty"`
	LastSeq      int   `json:"lastSeq,omitempty"`
	// CWD 是这条会话的工作目录（绝对路径，Windows 风格）。
	//
	// 为什么必须由宿主给：`workspace.list` 只回 `{name,isDir,size}`（没有 path），
	// 界面要读某个文件就得拼「工作区根 + name」——而界面不知道根在哪，
	// 于是 `fs.read` 永远调不出来。空字符串表示不知道（omitempty），
	// **不许填当前进程 cwd 冒充**：多会话可能属于不同工作区。
	CWD string `json:"cwd,omitempty"`
}

// HistoryResult 是 session.history 的返回值（H14）。
//
// **frames 是原始帧，不是投影后的 messages**：补洞要能恢复 reasoning/tool/approval，
// 而投影只保留 text。lastSeq/journalEpoch 一并给出，客户端据此判断水位是否可用。
type HistoryResult struct {
	Frames       []ServerFrame `json:"frames"`
	LastSeq      int           `json:"lastSeq"`
	JournalEpoch int64         `json:"journalEpoch"`
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
