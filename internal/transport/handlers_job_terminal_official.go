package transport

// 官方桌面端 0.2.2-rc.1 的 job.*（3）与 terminal.*（8），L1 语义对齐层。
//
// # 契约来源（只读镜像，未改安装目录）
//
//	app.asar sha256 5fa8a6b21bceb8f3e99e30efa2270efd0cc773a11e42aa0488f804a64b34e637
//	@deepseek-ai/dsh-api-job-controller/lib/typert.host.js:73/77   job/list   {sessionId} -> {type:'rows',jobs}
//	@deepseek-ai/dsh-api-job-controller/lib/typert.host.js:64/69   job/kill   {sessionId,jobId} -> {outcome}
//	@deepseek-ai/dsh-api-job-controller/lib/typert.host.js:7/13    job/follow {sessionId?,jobId,from?} -> opened|output|status
//	@deepseek-ai/dsh-api-terminal-controller/lib/typert.host.js:82/89   terminal/create request {shellPath?,id,cols,rows}
//	@deepseek-ai/dsh-api-terminal-controller/lib/typert.host.js:107      terminal/environment
//	@deepseek-ai/dsh-api-terminal-controller/lib/typert.host.js:22       terminal/attach result（snapshot|output|state|ack）
//
// # 这一层做什么、不做什么
//
// 做：把官方参数与结果**按官方字段名**翻译成现有宿主能力，沿用本仓的四字段错误
// 契约与既有 cookie 认证（本文件不碰认证：路由必须挂在 handleAPI 的 authorized
// 之后，见 RESULT 的接线清单）。
//
// # 流式方法：真流，不是一次性帧
//
// 官方 `job.list`、`job.follow`、`terminal.attach`、`terminal.retain` 都是 typert
// `mode:'stream'`（官方 zod 见 /tmp/official-api-src 的只读镜像）：
//
//	job/list          rows（whole-set：打开一帧，之后每次（合并后的）生命周期
//	                  提交再发一整份；没有自然结束——载体关闭才结束）
//	job/follow        opened → output* → status
//	terminal/attach   snapshot → output* → state（+ 官方 uplink 的 write/resize）
//	terminal/retain   retained → 保持打开的寿命流
//
// 本宿主 HTTP 面与既有 6 个流方法（official_stream_face.go）**同一形状**：
// `200 + application/x-ndjson; charset=utf-8`，一行 = 官方一帧、没有 result 包壳；
// 正常结束 = 连接关闭；中途失败 = 最后一行 `{"type":"error","error":{…四字段…}}`。
// 选 NDJSON 不选 SSE 的理由与那 6 个方法一样：同一进程里两套长连接形状只会让
// 客户端多一份解析器。
//
// 旧实现回的是 `{"result":{…,"oneShot":true}}` 的一次性帧。语法上诚实（标了
// oneShot），产品上三个官方方法全部不可用：follow 只能轮询、attach 只能拿快照、
// retain 拿完就断。**这个残口在这里关闭**——`oneShot` 不再出现在任何 wire 帧上。
//
// 没有流能力的 Svc（只实现 unary 面）必须回 **501 not_implemented** 并点名缺
// 什么能力，绝不回一次性帧冒充流：空 end / 单帧都会被客户端当成"流正常结束"。
//
// 仍不做（不许假装）：
//   - 官方 attach 的 uplink（write/resize）只在官方 mux 面存在；本宿主 HTTP 面
//     是**下行真流 + 上行缺席**（官方 HTTP 协议本身没有上行通道），写入走既有
//     `terminal.input`（宿主侧对它做官方 PTY 终端回落，见 serve_terminal_api.go
//     的 `InputByID`/`OutputByID` 与 WIRING.md），resize 走本层
//     `officialPTYTerminalAPI.Resize`。因此 snapshot 帧的 `writable` 描述的是
//     **终端本身可写**，不是"这条 HTTP 连接接受上行"。
//   - 官方 job.kill 的 `already-finished` 是一个成功 outcome；本仓契约要求
//     "任务已结束" 落 409 job_finished（既有 jobs.stop 也是这么回的），
//     所以这里 409 优先，`already-finished` 仅在底层服务真的这么回报时才出现。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"
)

// 官方 11 个方法名。顺序与 parity 清单一致，方便和 docs/official-desktop-api-parity.md 对读。
const (
	MethodJobList   Method = "job.list"
	MethodJobKill   Method = "job.kill"
	MethodJobFollow Method = "job.follow"

	MethodTerminalCreate      Method = "terminal.create"
	MethodTerminalAttach      Method = "terminal.attach"
	MethodTerminalList        Method = "terminal.list"
	MethodTerminalClose       Method = "terminal.close"
	MethodTerminalEnvironment Method = "terminal.environment"
	MethodTerminalShells      Method = "terminal.shells"
	MethodTerminalRename      Method = "terminal.rename"
	MethodTerminalRetain      Method = "terminal.retain"
)

// OfficialJobTerminalMethods 返回本层负责的 11 个方法。
//
// **它们是 additive 扩展**：不得加入 SessionMethods 的 canonical 25，
// 也不得进 TaskActionMethods（那是 jobs.stop / schedule.* 的既有扩展面）。
func OfficialJobTerminalMethods() []Method {
	return []Method{
		MethodJobList, MethodJobKill, MethodJobFollow,
		MethodTerminalCreate, MethodTerminalAttach, MethodTerminalList, MethodTerminalClose,
		MethodTerminalEnvironment, MethodTerminalShells, MethodTerminalRename, MethodTerminalRetain,
	}
}

// Official 层的稳定错误。**status 与 code 都在本文件里定死**，不靠中文消息猜：
// 猜错码的后果是客户端走错分支（把"任务已结束"当成"宿主崩了"去重试）。
var (
	// ErrJTOfficialUnsupported：本宿主没有这个能力（回 501 not_implemented）。
	// 用它的时候必须说清缺的是什么，禁止用它掩盖实现 bug。
	ErrJTOfficialUnsupported = errors.New("官方方法在本宿主未实现")
	// ErrJTOfficialTerminalGone：终端身份不存在或已在本会话关闭（回 404）。
	ErrJTOfficialTerminalGone = errors.New("没有这个终端")
	// ErrJTOfficialBadParam：底层服务发现参数不合法（回 400）。
	ErrJTOfficialBadParam = errors.New("参数不合法")
)

// ---------------------------------------------------------------------------
// 官方结果形状（字段名逐字对齐 zod；可选字段用 omitempty，空值不出现）
// ---------------------------------------------------------------------------

// OfficialJobOutput 对应 JobView.output。
//
// total 是"下一块从哪个字节开始"，earliest 是仍保留的最旧字节；
// 两者都是**字节**偏移（官方是 byte offset，不是 rune）。
type OfficialJobOutput struct {
	Total      int64    `json:"total"`
	Earliest   int64    `json:"earliest"`
	SpillPaths []string `json:"spillPaths,omitempty"`
}

// OfficialJobView 对应 JobView。
type OfficialJobView struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Label 是产物自己给的一行标签；本宿主是命令首行（有界）。
	Label string `json:"label"`
	// Owner 是归属会话；未归属的 job 不出现该字段。
	Owner string `json:"owner,omitempty"`
	// OutputLimitBytes 是产物声明的上限；本宿主不声明，故不出现。
	OutputLimitBytes int `json:"outputLimitBytes,omitempty"`
	// Status 取 running|stopping|completed|killed|failed。
	Status    string `json:"status"`
	Progress  string `json:"progress,omitempty"`
	Detail    string `json:"detail,omitempty"`
	StartedAt int64  `json:"startedAt"`
	// FinishedAt 只在已结算时出现；0 表示服务没有给出，不出现。
	FinishedAt int64             `json:"finishedAt,omitempty"`
	Output     OfficialJobOutput `json:"output"`
}

// OfficialJobListFrame 对应 JobListFrame（官方只有 rows 一种）。
type OfficialJobListFrame struct {
	Type string            `json:"type"`
	Jobs []OfficialJobView `json:"jobs"`
}

// OfficialJobKillValue 对应 JobKillValue。
type OfficialJobKillValue struct {
	Outcome string `json:"outcome"`
}

// OfficialJobChunk 对应 JobChunk。at 是**绝对字节偏移**。
type OfficialJobChunk struct {
	At        int64  `json:"at"`
	Text      string `json:"text"`
	Channel   string `json:"channel,omitempty"`
	GapBefore bool   `json:"gapBefore,omitempty"`
}

// OfficialJobFollowOpened 是 job/follow 的**开场帧**（官方 zod `type:'opened'`）。
//
// From 是"第一块 output 从这里继续"的绝对字节偏移：它要么等于请求里的 from，
// 要么（请求省略 from、或请求的位置已被保留窗口淘汰时）等于 earliest。
type OfficialJobFollowOpened struct {
	Type string          `json:"type"`
	Job  OfficialJobView `json:"job"`
	From int64           `json:"from"`
}

// OfficialJobFollowOutput 是 job/follow 的**增量帧**（官方 zod `type:'output'`）。
//
// chunks 是**必填数组**（可以为空——"这一刻没有新输出"是合法帧）；
// next 是下一次从哪个字节继续；lossy=true 表示请求的偏移与 chunks 之间有字节
// 已被淘汰（此时对应 chunk 带 gapBefore=true）。
type OfficialJobFollowOutput struct {
	Type   string             `json:"type"`
	Chunks []OfficialJobChunk `json:"chunks"`
	Next   int64              `json:"next"`
	Lossy  bool               `json:"lossy,omitempty"`
}

// OfficialJobFollowStatus 是 job/follow 的**终态帧**（官方 zod `type:'status'`）。
//
// 发出它之后流结束：官方语义是"输出已排干且任务已结算"。
type OfficialJobFollowStatus struct {
	Type string          `json:"type"`
	Job  OfficialJobView `json:"job"`
}

// OfficialTerminalShell 对应 TerminalShell。
//
// Supported 是本仓 additive 字段：本宿主的持久 shell 会话只能起默认 shell
// （底层 modules/exec/bash 写死 bash），其余候选即使装上也不可 create。
// 官方没有这个字段；给出来是为了让客户端能把不可用的项灰掉，而不是点了报错。
type OfficialTerminalShell struct {
	Path      string   `json:"path"`
	Args      []string `json:"args"`
	Name      string   `json:"name"`
	Supported *bool    `json:"supported,omitempty"`
}

// OfficialWebTerminalInfo 对应 WebTerminalInfo。
type OfficialWebTerminalInfo struct {
	ID       string                `json:"id"`
	Title    string                `json:"title"`
	Shell    OfficialTerminalShell `json:"shell"`
	Cwd      string                `json:"cwd"`
	Cols     int                   `json:"cols"`
	Rows     int                   `json:"rows"`
	State    string                `json:"state"`
	ExitCode *int                  `json:"exitCode"`
	Error    string                `json:"error,omitempty"`
}

// OfficialTerminalCreateRequest 对应 TerminalCreateRequest。
//
// id 由**调用方**给（官方语义：caller-generated identity，用来做幂等）。
type OfficialTerminalCreateRequest struct {
	ShellPath string `json:"shellPath,omitempty"`
	ID        string `json:"id"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
}

// OfficialTerminalEnvironment 对应 TerminalEnvironment。
type OfficialTerminalEnvironment struct {
	Cwd              string `json:"cwd"`
	MaxInputBytes    int    `json:"maxInputBytes"`
	MaxBufferedBytes int    `json:"maxBufferedBytes"`
	MaxCols          int    `json:"maxCols"`
	MaxRows          int    `json:"maxRows"`
	Scrollback       int    `json:"scrollback"`
}

// OfficialTerminalSnapshot 是 attach 的**首帧值**（官方 snapshot 帧的宿主侧形态）。
//
// 注意：它是**服务面内部值**，不是 wire 帧。官方 wire 上不许出现 OneShot 字段
// （见 OfficialTerminalSnapshotFrame）；OneShot 保留只是为了让 HS-08 的 PTY
// 适配层与其测试继续编译，**handler 不会把它编进响应**。
type OfficialTerminalSnapshot struct {
	Type     string                  `json:"type"`
	Sequence int64                   `json:"sequence"`
	Screen   string                  `json:"screen"`
	Info     OfficialWebTerminalInfo `json:"info"`
	Writable bool                    `json:"writable"`
	OneShot  bool                    `json:"oneShot"`
}

// OfficialTerminalInputWindow 对应官方 snapshot 帧的可选 inputWindow。
//
// MaxBytes 是"等待 Host 消费的完整编码帧"上限（本层 = 单次写入上限）；
// FrameOverheadBytes 是紧凑 JSON item 里 value 字段存在时的信封开销。
type OfficialTerminalInputWindow struct {
	MaxBytes           int `json:"maxBytes"`
	FrameOverheadBytes int `json:"frameOverheadBytes"`
}

// OfficialTerminalSnapshotFrame 是 attach 流的**首帧**（官方 zod `type:'snapshot'`）。
//
// 与 OfficialTerminalSnapshot 的区别只有一个：**没有 OneShot**。这就是"残口关闭"
// 在 wire 上的判据——客户端看到 snapshot 之后还会收到 output / state 帧。
type OfficialTerminalSnapshotFrame struct {
	Type        string                       `json:"type"`
	Sequence    int64                        `json:"sequence"`
	Screen      string                       `json:"screen"`
	Info        OfficialWebTerminalInfo      `json:"info"`
	Writable    bool                         `json:"writable"`
	InputWindow *OfficialTerminalInputWindow `json:"inputWindow,omitempty"`
}

// OfficialTerminalOutputFrame 是 attach 流的**增量帧**（官方 zod `type:'output'`）。
//
// Sequence 是这一帧之后的绝对字节偏移（下一帧从这里继续）。
type OfficialTerminalOutputFrame struct {
	Type     string `json:"type"`
	Sequence int64  `json:"sequence"`
	Data     string `json:"data"`
}

// OfficialTerminalStateFrame 是 attach 流的**终态帧**（官方 zod `type:'state'`）。
//
// 进程退出后发出，然后流结束：之后不会再有 output 帧（"已退出"是终态）。
type OfficialTerminalStateFrame struct {
	Type     string                  `json:"type"`
	Info     OfficialWebTerminalInfo `json:"info"`
	Writable bool                    `json:"writable"`
}

// OfficialTerminalAckFrame 是 attach 流的**上行确认帧**（官方 zod `type:'ack'`）。
//
// 官方 uplink 只在 mux 面存在：客户端发 write/resize，宿主回一帧 ack 确认
// 已接受（refusal 非空 = 拒绝，值取 read-only / not-running）。
type OfficialTerminalAckFrame struct {
	Type     string `json:"type"`
	Sequence int64  `json:"sequence"`
	Refusal  string `json:"refusal,omitempty"`
}

// OfficialTerminalUplink 是 attach 帧机可选的**上行**能力（mux 面用）。
//
// HTTP 面一条请求一条响应，没有上行通道；官方 uplink 只在 `/api/remote.mux`
// 的 open 流上存在。Lead 在 officialwire 的 UplinkStream 适配里把 item.value
// 转进来即可（见 WIRING.md）。
//
// 返回 error 表示拒绝这一帧：适配层必须把它翻成 `gateway/bad-request` 错误帧，
// **不许静默丢输入**（丢输入的表现是"我打字了但终端没反应"，最难查的一类）。
type OfficialTerminalUplink interface {
	Push(frame json.RawMessage) error
}

// OfficialTerminalRetentionFrame 对应 TerminalRetentionFrame（只有 retained 一种）。
//
// 官方 result 描述是「a hold acknowledgement followed by an open lifetime
// stream」：这一帧是**开场**，之后连接保持打开直到调用方取消。
type OfficialTerminalRetentionFrame struct {
	Type string `json:"type"`
}

// ---------------------------------------------------------------------------
// 服务面
// ---------------------------------------------------------------------------

// OfficialJobTerminalService 是官方 job.*/terminal.* 需要的能力。
//
// 实现放在宿主侧（cmd/dshd/serve_terminal_api.go）：job.* 复用既有 jobs 服务
// （Job Object/进程树归属、实例校验、跨会话 403、已结束 409 都由它给），
// terminal.* 复用 terminal.open/input/output 背后的真实 shell 会话。
//
// 每个方法返回的错误按下面这套 sentinel 翻状态码；未实现的能力必须返回
// ErrJTOfficialUnsupported（501），**不许返回零值当成功**。
//
// # 四个流式方法（JobList / JobFollow / TerminalAttach / TerminalRetain）
//
// 官方它们都是 `mode:'stream'`，所以 **wire 上不再走 unary 方法**：
// handler 优先要求 Svc 实现 OfficialJobTerminalStreams（真流）；没实现就 501。
// 这些 unary 签名保留是为了让 HS-08 的 PTY 适配层（`officialPTYJobTerminalService`）
// 继续满足本接口，同时给"一页读取 / 一份 whole-set 快照"这种内部用法一个稳定入口
// （`OpenJobList` 与 `OpenJobFollow` 的帧机就是从它们取真值再投影成 wire 帧的）；
// **任何 wire 路径都不许再调用它们回一次性帧**。
type OfficialJobTerminalService interface {
	// JobList 列这条会话能看到的 job（自己的 + 未归属的）。
	// 它仍是真值来源：job.list 的 whole-set 帧由 OpenJobList 按它生成。
	JobList(sessionID string) (OfficialJobListFrame, error)
	// JobKill 让人来杀一个后台任务；跨会话/实例不符由实现判错。
	JobKill(ctx context.Context, sessionID, jobID string) (OfficialJobKillValue, error)
	// JobFollow 从 from 字节起读一页；已排干且已结算时返回 status 帧。
	// Deprecated: wire 走 OfficialJobTerminalStreams.OpenJobFollow（真流）。
	JobFollow(ctx context.Context, sessionID, jobID string, from int64) (OfficialJobFollowPage, error)

	TerminalCreate(ctx context.Context, sessionID string, req OfficialTerminalCreateRequest) (OfficialWebTerminalInfo, error)
	// TerminalAttach 回一帧真实快照。
	// Deprecated: wire 走 OfficialJobTerminalStreams.OpenTerminalAttach（真流）。
	TerminalAttach(ctx context.Context, sessionID, id string) (OfficialTerminalSnapshot, error)
	TerminalList(sessionID string) ([]OfficialWebTerminalInfo, error)
	TerminalClose(ctx context.Context, sessionID, id string) error
	TerminalEnvironment(ctx context.Context, sessionID string) (OfficialTerminalEnvironment, error)
	TerminalShells(ctx context.Context, sessionID string) ([]OfficialTerminalShell, error)
	TerminalRename(ctx context.Context, sessionID, id, title string) error
	// TerminalRetain 标记保留并落盘。
	// Deprecated: wire 走 OfficialJobTerminalStreams.OpenTerminalRetain（真流）。
	TerminalRetain(ctx context.Context, sessionID, id string) error
}

// OfficialJobFollowPage 是"一页读取"的内部结果（**不是 wire 帧**）。
//
// 为什么单独一个类型：wire 帧是 opened/output/status 三种（官方 zod），
// 一页读取只是实现细节；混用会让实现以为"回一帧就是流"。
type OfficialJobFollowPage struct {
	Type   string
	Job    *OfficialJobView
	From   *int64
	Chunks []OfficialJobChunk
	Next   *int64
	Lossy  bool
}

// OfficialJobTerminalStreams 是官方四个 `mode:'stream'` 方法的**真流能力**。
//
// 实现它 = 这四条方法在 wire 上是真流；不实现 = 明确 501。
// 半实现（只实现一部分）不允许：要么四条都真流，要么全部 501。
//
// from < 0 表示"请求没给 from"（官方：从仍保留的最旧字节开始）。
type OfficialJobTerminalStreams interface {
	// OpenJobList 打开 job/list 的帧机：打开即一帧 whole-set rows，之后每个
	// （合并后的）生命周期提交再发一整份。**没有自然结束**：载体关闭才结束。
	OpenJobList(ctx context.Context, sessionID string) (OfficialFrameStream, error)
	// OpenJobFollow 打开 job/follow 的帧机：opened → output* → status，然后结束。
	OpenJobFollow(ctx context.Context, sessionID, jobID string, from int64) (OfficialFrameStream, error)
	// OpenTerminalAttach 打开 attach 的帧机：snapshot → output* → state，然后结束。
	OpenTerminalAttach(ctx context.Context, sessionID, id string) (OfficialFrameStream, error)
	// OpenTerminalRetain 打开 retain 的帧机：retained 一帧后**保持打开**直到取消。
	OpenTerminalRetain(ctx context.Context, sessionID, id string) (OfficialFrameStream, error)
}

// OfficialJobTerminal 是 11 个方法的路由与参数校验层。
//
// 接线（Lead）：Options 加一个 `OfficialJobTerminal *OfficialJobTerminal` 字段，
// 在 handleAPI 的 authorized 之后、switch 的 default 之前加两行：
//
//	if s.opt.OfficialJobTerminal != nil && s.opt.OfficialJobTerminal.Handles(name) {
//		s.opt.OfficialJobTerminal.Serve(w, r); return
//	}
//
// 这样 11 个方法一次接上，且**不会**碰到既有 case（jobs.* / terminal.open|input|output）。
type OfficialJobTerminal struct {
	Svc OfficialJobTerminalService
}

// Handles 报这个方法名是不是本层的（只认官方 11 个，绝不抢既有方法）。
func (h *OfficialJobTerminal) Handles(method string) bool {
	switch Method(method) {
	case MethodJobList, MethodJobKill, MethodJobFollow,
		MethodTerminalCreate, MethodTerminalAttach, MethodTerminalList, MethodTerminalClose,
		MethodTerminalEnvironment, MethodTerminalShells, MethodTerminalRename, MethodTerminalRetain:
		return true
	}
	return false
}

// Serve 按 URL 里的方法名分发（Lead 的单点接线入口）。
func (h *OfficialJobTerminal) Serve(w http.ResponseWriter, r *http.Request) {
	method := strings.TrimPrefix(r.URL.Path, "/api/")
	switch Method(method) {
	case MethodJobList:
		h.HandleJobList(w, r)
	case MethodJobKill:
		h.HandleJobKill(w, r)
	case MethodJobFollow:
		h.HandleJobFollow(w, r)
	case MethodTerminalCreate:
		h.HandleTerminalCreate(w, r)
	case MethodTerminalAttach:
		h.HandleTerminalAttach(w, r)
	case MethodTerminalList:
		h.HandleTerminalList(w, r)
	case MethodTerminalClose:
		h.HandleTerminalClose(w, r)
	case MethodTerminalEnvironment:
		h.HandleTerminalEnvironment(w, r)
	case MethodTerminalShells:
		h.HandleTerminalShells(w, r)
	case MethodTerminalRename:
		h.HandleTerminalRename(w, r)
	case MethodTerminalRetain:
		h.HandleTerminalRetain(w, r)
	default:
		writeJSON(w, http.StatusNotImplemented, errorBodyFor("not_implemented",
			method+" 不是官方 job.*/terminal.* 方法，本层不接管"))
	}
}

// ---------------------------------------------------------------------------
// 参数：官方字段名 + 既有别名（sessionId/agentId、jobId/id）
// ---------------------------------------------------------------------------

// officialIdentity 收会话身份。官方 wire 字段是 `agentId`（typert 的 lookup 参数），
// 本仓既有方法一律用 `sessionId`；两边都认，但**冲突必须报错**——
// 猜任何一个都会把 A 会话的操作打到 B 会话上。
type officialIdentity struct {
	SessionID string `json:"sessionId"`
	AgentID   string `json:"agentId"`
}

func (o officialIdentity) resolve(method string) (string, error) {
	switch {
	case o.SessionID != "" && o.AgentID != "" && o.SessionID != o.AgentID:
		return "", fmt.Errorf("%w: %s 同时给了 sessionId=%q 与 agentId=%q，两者不一致时不许猜",
			ErrJTOfficialBadParam, method, o.SessionID, o.AgentID)
	case o.SessionID != "":
		return o.SessionID, nil
	case o.AgentID != "":
		return o.AgentID, nil
	}
	return "", fmt.Errorf("%w: %s 缺 sessionId（官方 wire 名是 agentId，两个都认）", ErrJTOfficialBadParam, method)
}

// officialJobRef 收 job 身份：官方 `jobId`，既有 jobs.stop 用 `id`。
type officialJobRef struct {
	JobID string `json:"jobId"`
	ID    string `json:"id"`
}

func (o officialJobRef) resolve(method string) (string, error) {
	switch {
	case o.JobID != "" && o.ID != "" && o.JobID != o.ID:
		return "", fmt.Errorf("%w: %s 同时给了 jobId=%q 与 id=%q，两者不一致时不许猜",
			ErrJTOfficialBadParam, method, o.JobID, o.ID)
	case o.JobID != "":
		return o.JobID, nil
	case o.ID != "":
		return o.ID, nil
	}
	return "", fmt.Errorf("%w: %s 缺 jobId（既有写法 id 也认）", ErrJTOfficialBadParam, method)
}

// officialTerminalRef 收终端身份（官方 id 由 create 的调用方给）。
type officialTerminalRef struct {
	ID string `json:"id"`
}

func (o officialTerminalRef) resolve(method string) (string, error) {
	if strings.TrimSpace(o.ID) == "" {
		return "", fmt.Errorf("%w: %s 缺 id（终端身份由 terminal.create 的调用方给出）", ErrJTOfficialBadParam, method)
	}
	return o.ID, nil
}

// decodeJTOfficialParams 用既有 decodeTaskActionParams（裸参数对象 + 未知字段 fail-loud）。
func decodeJTOfficialParams(r *http.Request, target any) error {
	if err := decodeTaskActionParams(r, target); err != nil {
		return fmt.Errorf("%w: %v", ErrJTOfficialBadParam, err)
	}
	return nil
}

// writeJTOfficialResult 是成功侧的**唯一**出口（与既有方法同信封）。
func writeJTOfficialResult(w http.ResponseWriter, result any) {
	writeJSON(w, http.StatusOK, map[string]any{"result": result})
}

// writeJTOfficialVoid 给官方的 void 结果用。
//
// 官方 close/rename 的结果是 z.void()；本仓的空 result 会被判成
// `empty_result` 编码失败，所以这里回 {"ok":true}——additive 字段，不是假数据。
func writeJTOfficialVoid(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]any{"result": map[string]any{"ok": true}})
}

// writeJTOfficialError 是失败侧的**唯一**出口：四字段信封仍然只由 errorObject 造。
//
// 状态码映射就是语义：400 改参数、403 换会话、404 换 id、409 别重试、
// 501 换方法、500 可以重试。
func writeJTOfficialError(w http.ResponseWriter, method string, err error) {
	switch {
	case errors.Is(err, ErrJTOfficialBadParam):
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", method+"："+err.Error()))
	case errors.Is(err, ErrCrossSession):
		writeJSON(w, http.StatusForbidden, errorBodyFor("cross_session", method+"："+err.Error()))
	case errors.Is(err, ErrNoSuchJob), errors.Is(err, ErrJTOfficialTerminalGone), errors.Is(err, ErrNoSuchSession):
		writeJSON(w, http.StatusNotFound, errorBodyFor("not_found", method+"："+err.Error()))
	case errors.Is(err, ErrJobFinished):
		writeJSON(w, http.StatusConflict, errorBodyFor("job_finished", method+"："+err.Error()))
	case errors.Is(err, ErrBadJobCursor):
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", method+"："+err.Error()))
	case errors.Is(err, ErrJTOfficialUnsupported), errors.Is(err, ErrNotImplemented):
		writeJSON(w, http.StatusNotImplemented, errorBodyFor("not_implemented", method+"："+err.Error()))
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// 调用方自己走了：这不是服务端错误，但也别回 200。
		writeJSON(w, http.StatusRequestTimeout, errorBodyFor("internal", method+"：调用被取消（"+err.Error()+"）"))
	default:
		writeJSON(w, http.StatusInternalServerError, errorBodyFor("internal", method+"："+err.Error()))
	}
}

// ---------------------------------------------------------------------------
// job.*
// ---------------------------------------------------------------------------

// HandleJobList 是官方 job/list 的**真流**入口。
//
// 官方语义（job-controller/lib/typert.host.js:184）：whole-set 帧——打开时一帧
// `{type:'rows',jobs:[…]}`，之后每次（合并后的）生命周期提交再发一整份；
// **没有自然结束**，载体关闭才结束。所以这里和其余三条流一样走 NDJSON，
// 不再回一帧 `{result:…}`（单帧会让客户端把"流正常结束"当真，列表从此不再刷新）。
func (h *OfficialJobTerminal) HandleJobList(w http.ResponseWriter, r *http.Request) {
	raw, fault := readOfficialStreamBody(r)
	if fault != nil {
		writeJTOfficialStreamFault(w, fault)
		return
	}
	h.serveOfficialStream(w, r, MethodJobList, func(ctx context.Context) (OfficialFrameStream, *OfficialStreamFault) {
		return h.openJobListStream(ctx, raw)
	})
}

// openJobListStream 解析 job.list 参数并打开帧机（HTTP 与 mux 两条面共用）。
func (h *OfficialJobTerminal) openJobListStream(ctx context.Context, raw json.RawMessage) (OfficialFrameStream, *OfficialStreamFault) {
	const method = MethodJobList
	var params officialIdentity
	if fault := decodeOfficialStreamArgs(method, raw, &params); fault != nil {
		return nil, fault
	}
	sessionID, err := params.resolve(string(method))
	if err != nil {
		return nil, officialJobTerminalFault(method, err)
	}
	streams, ok := h.streamCapability()
	if !ok {
		return nil, unwiredStreamFault(method)
	}
	stream, err := streams.OpenJobList(ctx, sessionID)
	if err != nil {
		return nil, officialJobTerminalFault(method, err)
	}
	return stream, nil
}

// HandleJobKill 是官方 job/kill（unary，结果 {outcome}）。
func (h *OfficialJobTerminal) HandleJobKill(w http.ResponseWriter, r *http.Request) {
	const method = string(MethodJobKill)
	var params officialJobParams
	if err := decodeJTOfficialParams(r, &params); err != nil {
		writeJTOfficialError(w, method, err)
		return
	}
	sessionID, err := params.officialIdentity.resolve(method)
	if err != nil {
		writeJTOfficialError(w, method, err)
		return
	}
	jobID, err := params.officialJobRef.resolve(method)
	if err != nil {
		writeJTOfficialError(w, method, err)
		return
	}
	if h.Svc == nil {
		writeJSON(w, http.StatusNotImplemented, errorBodyFor("not_implemented", method+" 未接线"))
		return
	}
	value, err := h.Svc.JobKill(r.Context(), sessionID, jobID)
	if err != nil {
		writeJTOfficialError(w, method, err)
		return
	}
	switch value.Outcome {
	case "requested", "already-finished":
	default:
		writeJSON(w, http.StatusInternalServerError, errorBodyFor("internal",
			method+"：outcome 只能是 requested/already-finished，实现给了 "+value.Outcome))
		return
	}
	writeJTOfficialResult(w, value)
}

// HandleJobFollow 是官方 job/follow 的**真流**入口（opened → output* → status）。
func (h *OfficialJobTerminal) HandleJobFollow(w http.ResponseWriter, r *http.Request) {
	raw, fault := readOfficialStreamBody(r)
	if fault != nil {
		writeJTOfficialStreamFault(w, fault)
		return
	}
	h.serveOfficialStream(w, r, MethodJobFollow, func(ctx context.Context) (OfficialFrameStream, *OfficialStreamFault) {
		return h.openJobFollowStream(ctx, raw)
	})
}

// openJobFollowStream 解析 job.follow 参数并打开帧机（HTTP 与 mux 两条面共用）。
func (h *OfficialJobTerminal) openJobFollowStream(ctx context.Context, raw json.RawMessage) (OfficialFrameStream, *OfficialStreamFault) {
	const method = MethodJobFollow
	var params struct {
		officialJobParams
		From *int64 `json:"from"`
	}
	if fault := decodeOfficialStreamArgs(method, raw, &params); fault != nil {
		return nil, fault
	}
	sessionID, err := params.officialJobParams.officialIdentity.resolve(string(method))
	if err != nil {
		return nil, officialJobTerminalFault(method, err)
	}
	jobID, err := params.officialJobParams.officialJobRef.resolve(string(method))
	if err != nil {
		return nil, officialJobTerminalFault(method, err)
	}
	// from 省略 = 从仍保留的最旧字节起（官方描述），内部用 -1 表达。
	var from int64 = -1
	if params.From != nil {
		if *params.From < 0 {
			return nil, officialJobTerminalFault(method, fmt.Errorf(
				"%w: from 不能为负（现在是 %d）", ErrJTOfficialBadParam, *params.From))
		}
		from = *params.From
	}
	streams, ok := h.streamCapability()
	if !ok {
		return nil, unwiredStreamFault(method)
	}
	stream, err := streams.OpenJobFollow(ctx, sessionID, jobID, from)
	if err != nil {
		return nil, officialJobTerminalFault(method, err)
	}
	return stream, nil
}

// readOfficialStreamBody 读流式方法的请求体（有界）。
//
// 为什么单独一个函数：两条面（HTTP / mux）共用同一份上限与同一种错误形状，
// 各写一份就会出现"mux 收 1 MiB、HTTP 收 4 MiB"这种只在一条面复现的差异。
func readOfficialStreamBody(r *http.Request) (json.RawMessage, *OfficialStreamFault) {
	if r == nil || r.Body == nil {
		return nil, officialStreamBadRequest("", "流式方法需要 JSON 参数对象（请求体为空）")
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, officialStreamBodyLimit))
	if err != nil {
		return nil, officialStreamBadRequest("", "读请求体失败：%v", err)
	}
	return json.RawMessage(raw), nil
}

// officialTerminalParams 是 terminal.attach/close/retain 的共同参数：会话 + 终端身份。
type officialTerminalParams struct {
	officialIdentity
	officialTerminalRef
}

// officialJobParams 是 job.kill / job.follow 的共同参数：会话 + 任务身份。
type officialJobParams struct {
	officialIdentity
	officialJobRef
}

// streamCapability 取 Svc 的真流能力（没接线 / 没实现都返回 false）。
func (h *OfficialJobTerminal) streamCapability() (OfficialJobTerminalStreams, bool) {
	if h == nil || h.Svc == nil {
		return nil, false
	}
	streams, ok := h.Svc.(OfficialJobTerminalStreams)
	if !ok || streams == nil {
		return nil, false
	}
	return streams, true
}

// unwiredStreamFault 是"没有流能力"的唯一故障：**501 + 点名缺什么**。
//
// 为什么不回一次性帧：官方这三个方法是 `mode:'stream'`。回一帧 `{result:…}`
// 会让客户端把"流正常结束"当成"只有一帧"，产品上表现为 follow 不再更新、
// attach 拿不到后续输出、retain 立刻释放。明确 501 才是可诊断的。
func unwiredStreamFault(method Method) *OfficialStreamFault {
	return &OfficialStreamFault{
		Code: "gateway/method-unavailable", Status: http.StatusNotImplemented,
		Message: string(method) + " 未接线：宿主没有实现官方流能力（OfficialJobTerminalStreams）；" +
			"这条方法是 typert mode:'stream'，一次性帧不算实现",
	}
}

// serveOfficialStream 是三个流式方法的**唯一**传输实现（HTTP NDJSON 长连接）。
//
// 与 official_stream_face.go 的 serveOfficialStream 同形状（一行一帧、
// opening 帧预取、中途失败写 error 行），但**不复用**它：那个是
// `(s *Server)` 上的 6 个会话流方法，参数解析与故障词表都绑在 Server 上。
// 这里只借同一份信封/故障工具，保证两条面的帧与错误码不分叉。
func (h *OfficialJobTerminal) serveOfficialStream(w http.ResponseWriter, r *http.Request, method Method, open func(context.Context) (OfficialFrameStream, *OfficialStreamFault)) {
	stream, openFault := open(r.Context())
	if openFault != nil {
		writeJTOfficialStreamFault(w, openFault)
		return
	}
	if stream == nil {
		writeJTOfficialStreamFault(w, &OfficialStreamFault{
			Code: "gateway/internal", Status: http.StatusInternalServerError,
			Message: string(method) + "：实现返回了 nil 流",
		})
		return
	}
	defer stream.Close()

	// opening 帧必须在**写响应头之前**取到：参数/能力错误仍要能回四字段 + 状态码。
	head, done, fault := stream.Next(r.Context())
	if fault != nil {
		writeJTOfficialStreamFault(w, fault)
		return
	}
	if done {
		writeJTOfficialStreamFault(w, &OfficialStreamFault{
			Code: "gateway/internal", Status: http.StatusInternalServerError,
			Message: string(method) + "：流在 opening 帧之前就结束了（帧机实现错误）",
		})
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	writeLine := func(frame json.RawMessage) bool {
		if len(frame) == 0 {
			return true
		}
		if _, err := w.Write(append(frame, '\n')); err != nil {
			return false
		}
		if flusher != nil {
			flusher.Flush()
		}
		return true
	}

	if !writeLine(head) {
		return
	}
	for {
		frame, done, fault := stream.Next(r.Context())
		if fault != nil {
			// 已经开始写帧，不能改状态码；用一行错误帧结束。
			body := errorBodyFor(officialJobTerminalFaultCode(fault.Status), fault.Message)
			body["type"] = "error"
			if fault.Field != "" {
				body["field"] = fault.Field
			}
			writeLine(mustMarshalStreamLine(body))
			return
		}
		if done {
			return
		}
		if !writeLine(frame) {
			return
		}
	}
}

// officialJobTerminalFault 把宿主错误折叠成流故障（词表与 unary 面同一份语义）。
func officialJobTerminalFault(method Method, err error) *OfficialStreamFault {
	if err == nil {
		return &OfficialStreamFault{Code: "gateway/internal", Status: http.StatusInternalServerError,
			Message: string(method) + "：宿主返回了 nil 错误"}
	}
	fault := func(code string, status int) *OfficialStreamFault {
		return &OfficialStreamFault{Code: code, Status: status, Message: string(method) + "：" + err.Error()}
	}
	switch {
	case errors.Is(err, ErrJTOfficialBadParam), errors.Is(err, ErrBadJobCursor):
		return fault("gateway/bad-request", http.StatusBadRequest)
	case errors.Is(err, ErrCrossSession):
		return fault("gateway/forbidden", http.StatusForbidden)
	case errors.Is(err, ErrNoSuchJob), errors.Is(err, ErrJTOfficialTerminalGone), errors.Is(err, ErrNoSuchSession):
		return fault("gateway/session-not-found", http.StatusNotFound)
	case errors.Is(err, ErrJobFinished):
		return fault("gateway/job-finished", http.StatusConflict)
	case errors.Is(err, ErrJTOfficialUnsupported), errors.Is(err, ErrNotImplemented):
		return fault("gateway/method-unavailable", http.StatusNotImplemented)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return fault("gateway/internal", http.StatusRequestTimeout)
	default:
		return fault("gateway/internal", http.StatusInternalServerError)
	}
}

// OfficialJobTerminalStreamFault 把宿主错误翻译成流故障（给宿主侧的帧机用）。
//
// 导出它是为了让 `cmd/dshd` 的帧机与 HTTP 面用**同一份**状态码/网关码词表：
// 两处各写一份映射，迟早会出现"HTTP 回 403、mux 回 500"这种只在一侧复现的错。
func OfficialJobTerminalStreamFault(method string, err error) *OfficialStreamFault {
	return officialJobTerminalFault(Method(method), err)
}

// officialJobTerminalFaultCode 把流故障翻成本仓四字段码（unary 面既有词表）。
func officialJobTerminalFaultCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "bad_request"
	case http.StatusForbidden:
		return "cross_session"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "job_finished"
	case http.StatusNotImplemented:
		return "not_implemented"
	default:
		return "internal"
	}
}

// OpenFrameStream 是官方 **mux 面**的统一打开入口（Lead 接线用，见 WIRING.md）。
//
// 官方一个 `mode:'stream'` 方法同时出现在两处：`POST /api/<method>`（本文件的
// NDJSON 长连接）与 `/api/remote.mux` 的 `open` 帧。两条面必须共用同一台帧机，
// 否则会出现"HTTP 上对了、mux 上重放历史"这种只在某一侧复现的 bug。
//
// 参数/能力错误 → *OfficialStreamFault（mux 面翻 gateway/* 错误帧）；
// 成功 → 帧机（opening 帧**已预取**，首帧错误在 ready 之前暴露，与
// official_stream_face.go 的七条流同一语义）。
func (h *OfficialJobTerminal) OpenFrameStream(ctx context.Context, method string, args json.RawMessage) (OfficialFrameStream, *OfficialStreamFault) {
	normalized := normalizeOfficialEndpoint(method)
	var stream OfficialFrameStream
	var fault *OfficialStreamFault
	switch normalized {
	case MethodJobList:
		stream, fault = h.openJobListStream(ctx, args)
	case MethodJobFollow:
		stream, fault = h.openJobFollowStream(ctx, args)
	case MethodTerminalAttach:
		stream, fault = h.openTerminalAttachStream(ctx, args)
	case MethodTerminalRetain:
		stream, fault = h.openTerminalRetainStream(ctx, args)
	default:
		if h.Handles(string(normalized)) {
			return nil, &OfficialStreamFault{
				Code: "gateway/method-unavailable", Status: http.StatusNotImplemented,
				Message: string(normalized) + " 不是流式方法：mux 的 open 只承载 mode:'stream' 方法",
			}
		}
		return nil, &OfficialStreamFault{
			Code: "gateway/method-unavailable", Status: http.StatusNotImplemented,
			Message: string(normalized) + " 不是官方 job.*/terminal.* 方法，本层不接管",
		}
	}
	if fault != nil {
		return nil, fault
	}
	if stream == nil {
		return nil, &OfficialStreamFault{
			Code: "gateway/internal", Status: http.StatusInternalServerError,
			Message: string(normalized) + "：实现返回了 nil 流",
		}
	}
	// opening 帧预取：参数/能力/身份错误必须在 mux 回 ready **之前**变成
	// open 错误，而不是 ready 之后的第一帧 error（客户端会以为流已经建立）。
	head, done, headFault := stream.Next(ctx)
	if headFault != nil {
		stream.Close()
		return nil, headFault
	}
	if done {
		stream.Close()
		return nil, &OfficialStreamFault{
			Code: "gateway/internal", Status: http.StatusInternalServerError,
			Message: string(normalized) + "：流在 opening 帧之前就结束了（帧机实现错误）",
		}
	}
	headStream := &officialJobTerminalHeadStream{stream: stream, head: head, ctx: ctx}
	// attach 帧机实现 OfficialTerminalUplink：mux 的上行必须能穿过预取头
	// （officialHeadStream 只有 Next/Close，会把 Push 弄丢）。只给真正有上行
	// 能力的流套 uplink 外壳，其它流保持"不接受上行"的既有语义。
	if uplink, ok := stream.(OfficialTerminalUplink); ok {
		return &officialUplinkHeadStream{officialJobTerminalHeadStream: headStream, uplink: uplink}, nil
	}
	return headStream, nil
}

// officialJobTerminalHeadStream 是 job/terminal mux 流的**预取头**：
// opening 帧先交出去，之后转发底层帧机。与 official_stream_face.go 的
// officialHeadStream 同语义，区别是本类型可由 uplink 外壳扩展（见下）。
type officialJobTerminalHeadStream struct {
	stream OfficialFrameStream
	head   json.RawMessage
	ctx    context.Context
}

func (h *officialJobTerminalHeadStream) Next(ctx context.Context) (json.RawMessage, bool, *OfficialStreamFault) {
	if h.head != nil {
		head := h.head
		h.head = nil
		return head, false, nil
	}
	return h.stream.Next(ctx)
}

func (h *officialJobTerminalHeadStream) Close() { h.stream.Close() }

// officialUplinkHeadStream 给 attach 流补上 mux 的 Push：官方 uplink 的
// write/resize 在 mux 面是 item 帧，必须原样转给帧机（拒绝要回错误帧，不静默丢）。
type officialUplinkHeadStream struct {
	*officialJobTerminalHeadStream
	uplink OfficialTerminalUplink
}

func (h *officialUplinkHeadStream) Push(value json.RawMessage) error {
	if h.uplink == nil {
		return fmt.Errorf("%w: 这条流不接受上行数据", ErrJTOfficialBadParam)
	}
	return h.uplink.Push(value)
}

// writeJTOfficialStreamFault 是"还没开始写帧"时的错误出口：状态码 + 四字段。
func writeJTOfficialStreamFault(w http.ResponseWriter, fault *OfficialStreamFault) {
	if fault == nil {
		writeJSON(w, http.StatusInternalServerError, errorBodyFor("internal", "流故障是 nil"))
		return
	}
	body := errorBodyFor(officialJobTerminalFaultCode(fault.Status), fault.Message)
	if fault.Field != "" {
		body["field"] = fault.Field
	}
	writeJSON(w, fault.Status, body)
}

// ---------------------------------------------------------------------------
// terminal.*
// ---------------------------------------------------------------------------

// HandleTerminalCreate 是官方 terminal/create。
//
// 参数两种形状都认：官方 wire 的 `{request:{shellPath?,id,cols,rows}}`，
// 以及本仓惯用的扁平 `{shellPath?,id,cols,rows}`。混用即 400。
func (h *OfficialJobTerminal) HandleTerminalCreate(w http.ResponseWriter, r *http.Request) {
	const method = string(MethodTerminalCreate)
	var params struct {
		officialIdentity
		Request *OfficialTerminalCreateRequest `json:"request"`
		// 扁平形状（与 request 互斥）
		ShellPath string `json:"shellPath"`
		ID        string `json:"id"`
		Cols      int    `json:"cols"`
		Rows      int    `json:"rows"`
	}
	if err := decodeJTOfficialParams(r, &params); err != nil {
		writeJTOfficialError(w, method, err)
		return
	}
	sessionID, err := params.officialIdentity.resolve(method)
	if err != nil {
		writeJTOfficialError(w, method, err)
		return
	}
	flatUsed := params.ShellPath != "" || params.ID != "" || params.Cols != 0 || params.Rows != 0
	var req OfficialTerminalCreateRequest
	switch {
	case params.Request != nil && flatUsed:
		writeJTOfficialError(w, method, fmt.Errorf(
			"%w: %s 的 request 与扁平字段（shellPath/id/cols/rows）不能同时用，只挑一种形状",
			ErrJTOfficialBadParam, method))
		return
	case params.Request != nil:
		req = *params.Request
	default:
		req = OfficialTerminalCreateRequest{ShellPath: params.ShellPath, ID: params.ID, Cols: params.Cols, Rows: params.Rows}
	}
	switch {
	case strings.TrimSpace(req.ID) == "":
		writeJTOfficialError(w, method, fmt.Errorf("%w: %s 缺 id（终端身份由调用方给，用于幂等）", ErrJTOfficialBadParam, method))
		return
	case req.Cols <= 0 || req.Rows <= 0:
		writeJTOfficialError(w, method, fmt.Errorf("%w: %s 的 cols/rows 必须是正整数（收到 %d×%d）",
			ErrJTOfficialBadParam, method, req.Cols, req.Rows))
		return
	}
	if h.Svc == nil {
		writeJSON(w, http.StatusNotImplemented, errorBodyFor("not_implemented", method+" 未接线"))
		return
	}
	info, err := h.Svc.TerminalCreate(r.Context(), sessionID, req)
	if err != nil {
		writeJTOfficialError(w, method, err)
		return
	}
	writeJTOfficialResult(w, info)
}

// HandleTerminalAttach 是官方 terminal/attach 的**真流**入口（snapshot → output* → state）。
//
// 上行（官方 uplink 的 write/resize）不在 HTTP 面：写入走既有 terminal.input，
// resize 走宿主层的 `officialPTYTerminalAPI.Resize`（接线见 WIRING.md）。
func (h *OfficialJobTerminal) HandleTerminalAttach(w http.ResponseWriter, r *http.Request) {
	raw, fault := readOfficialStreamBody(r)
	if fault != nil {
		writeJTOfficialStreamFault(w, fault)
		return
	}
	h.serveOfficialStream(w, r, MethodTerminalAttach, func(ctx context.Context) (OfficialFrameStream, *OfficialStreamFault) {
		return h.openTerminalAttachStream(ctx, raw)
	})
}

// openTerminalAttachStream 解析 terminal.attach 参数并打开帧机（两条面共用）。
func (h *OfficialJobTerminal) openTerminalAttachStream(ctx context.Context, raw json.RawMessage) (OfficialFrameStream, *OfficialStreamFault) {
	const method = MethodTerminalAttach
	var params officialTerminalParams
	if fault := decodeOfficialStreamArgs(method, raw, &params); fault != nil {
		return nil, fault
	}
	sessionID, err := params.officialIdentity.resolve(string(method))
	if err != nil {
		return nil, officialJobTerminalFault(method, err)
	}
	id, err := params.officialTerminalRef.resolve(string(method))
	if err != nil {
		return nil, officialJobTerminalFault(method, err)
	}
	streams, ok := h.streamCapability()
	if !ok {
		return nil, unwiredStreamFault(method)
	}
	stream, err := streams.OpenTerminalAttach(ctx, sessionID, id)
	if err != nil {
		return nil, officialJobTerminalFault(method, err)
	}
	return stream, nil
}

// HandleTerminalList 是官方 terminal/list（result 是数组）。
func (h *OfficialJobTerminal) HandleTerminalList(w http.ResponseWriter, r *http.Request) {
	const method = string(MethodTerminalList)
	var params officialIdentity
	if err := decodeJTOfficialParams(r, &params); err != nil {
		writeJTOfficialError(w, method, err)
		return
	}
	sessionID, err := params.resolve(method)
	if err != nil {
		writeJTOfficialError(w, method, err)
		return
	}
	if h.Svc == nil {
		writeJSON(w, http.StatusNotImplemented, errorBodyFor("not_implemented", method+" 未接线"))
		return
	}
	infos, err := h.Svc.TerminalList(sessionID)
	if err != nil {
		writeJTOfficialError(w, method, err)
		return
	}
	if infos == nil {
		infos = []OfficialWebTerminalInfo{}
	}
	writeJTOfficialResult(w, infos)
}

// HandleTerminalClose 是官方 terminal/close（官方：repeated closes succeed）。
func (h *OfficialJobTerminal) HandleTerminalClose(w http.ResponseWriter, r *http.Request) {
	const method = string(MethodTerminalClose)
	var params officialTerminalParams
	if err := decodeJTOfficialParams(r, &params); err != nil {
		writeJTOfficialError(w, method, err)
		return
	}
	sessionID, err := params.officialIdentity.resolve(method)
	if err != nil {
		writeJTOfficialError(w, method, err)
		return
	}
	id, err := params.officialTerminalRef.resolve(method)
	if err != nil {
		writeJTOfficialError(w, method, err)
		return
	}
	if h.Svc == nil {
		writeJSON(w, http.StatusNotImplemented, errorBodyFor("not_implemented", method+" 未接线"))
		return
	}
	if err := h.Svc.TerminalClose(r.Context(), sessionID, id); err != nil {
		writeJTOfficialError(w, method, err)
		return
	}
	writeJTOfficialVoid(w)
}

// HandleTerminalEnvironment 是官方 terminal/environment（6 个字段全是必填）。
func (h *OfficialJobTerminal) HandleTerminalEnvironment(w http.ResponseWriter, r *http.Request) {
	const method = string(MethodTerminalEnvironment)
	var params officialIdentity
	if err := decodeJTOfficialParams(r, &params); err != nil {
		writeJTOfficialError(w, method, err)
		return
	}
	sessionID, err := params.resolve(method)
	if err != nil {
		writeJTOfficialError(w, method, err)
		return
	}
	if h.Svc == nil {
		writeJSON(w, http.StatusNotImplemented, errorBodyFor("not_implemented", method+" 未接线"))
		return
	}
	env, err := h.Svc.TerminalEnvironment(r.Context(), sessionID)
	if err != nil {
		writeJTOfficialError(w, method, err)
		return
	}
	writeJTOfficialResult(w, env)
}

// HandleTerminalShells 是官方 terminal/shells（真实探测，不是硬编码清单）。
func (h *OfficialJobTerminal) HandleTerminalShells(w http.ResponseWriter, r *http.Request) {
	const method = string(MethodTerminalShells)
	var params officialIdentity
	if err := decodeJTOfficialParams(r, &params); err != nil {
		writeJTOfficialError(w, method, err)
		return
	}
	sessionID, err := params.resolve(method)
	if err != nil {
		writeJTOfficialError(w, method, err)
		return
	}
	if h.Svc == nil {
		writeJSON(w, http.StatusNotImplemented, errorBodyFor("not_implemented", method+" 未接线"))
		return
	}
	shells, err := h.Svc.TerminalShells(r.Context(), sessionID)
	if err != nil {
		writeJTOfficialError(w, method, err)
		return
	}
	if shells == nil {
		shells = []OfficialTerminalShell{}
	}
	for i := range shells {
		if shells[i].Args == nil {
			shells[i].Args = []string{}
		}
	}
	writeJTOfficialResult(w, shells)
}

// OfficialTerminalTitleMaxRunes 是官方 rename 的上限（index.ts:229「at most 120 characters」）。
//
// 按**字符**而不是字节：中文标题 40 个字就 120 字节，按字节算会误判。
const OfficialTerminalTitleMaxRunes = 120

// HandleTerminalRename 是官方 terminal/rename（void 结果）。
func (h *OfficialJobTerminal) HandleTerminalRename(w http.ResponseWriter, r *http.Request) {
	const method = string(MethodTerminalRename)
	var params struct {
		officialIdentity
		officialTerminalRef
		Title string `json:"title"`
	}
	if err := decodeJTOfficialParams(r, &params); err != nil {
		writeJTOfficialError(w, method, err)
		return
	}
	sessionID, err := params.officialIdentity.resolve(method)
	if err != nil {
		writeJTOfficialError(w, method, err)
		return
	}
	id, err := params.officialTerminalRef.resolve(method)
	if err != nil {
		writeJTOfficialError(w, method, err)
		return
	}
	title := strings.TrimSpace(params.Title)
	switch {
	case title == "":
		writeJTOfficialError(w, method, fmt.Errorf("%w: %s 的 title 不能为空", ErrJTOfficialBadParam, method))
		return
	case utf8.RuneCountInString(title) > OfficialTerminalTitleMaxRunes:
		writeJTOfficialError(w, method, fmt.Errorf("%w: %s 的 title 最多 %d 个字符（收到 %d）",
			ErrJTOfficialBadParam, method, OfficialTerminalTitleMaxRunes, utf8.RuneCountInString(title)))
		return
	}
	if h.Svc == nil {
		writeJSON(w, http.StatusNotImplemented, errorBodyFor("not_implemented", method+" 未接线"))
		return
	}
	if err := h.Svc.TerminalRename(r.Context(), sessionID, id, title); err != nil {
		writeJTOfficialError(w, method, err)
		return
	}
	writeJTOfficialVoid(w)
}

// HandleTerminalRetain 是官方 terminal/retain 的**真流**入口。
//
// 官方 result 描述：「a hold acknowledgement followed by an open lifetime
// stream」——先 yield 一帧 {type:'retained'}，然后连接保持打开，直到调用方
// 取消（取消即释放保留窗口）。**一次性确认是旧残口**：客户端会以为持有已结束。
func (h *OfficialJobTerminal) HandleTerminalRetain(w http.ResponseWriter, r *http.Request) {
	raw, fault := readOfficialStreamBody(r)
	if fault != nil {
		writeJTOfficialStreamFault(w, fault)
		return
	}
	h.serveOfficialStream(w, r, MethodTerminalRetain, func(ctx context.Context) (OfficialFrameStream, *OfficialStreamFault) {
		return h.openTerminalRetainStream(ctx, raw)
	})
}

// openTerminalRetainStream 解析 terminal.retain 参数并打开寿命流（两条面共用）。
func (h *OfficialJobTerminal) openTerminalRetainStream(ctx context.Context, raw json.RawMessage) (OfficialFrameStream, *OfficialStreamFault) {
	const method = MethodTerminalRetain
	var params officialTerminalParams
	if fault := decodeOfficialStreamArgs(method, raw, &params); fault != nil {
		return nil, fault
	}
	sessionID, err := params.officialIdentity.resolve(string(method))
	if err != nil {
		return nil, officialJobTerminalFault(method, err)
	}
	id, err := params.officialTerminalRef.resolve(string(method))
	if err != nil {
		return nil, officialJobTerminalFault(method, err)
	}
	streams, ok := h.streamCapability()
	if !ok {
		return nil, unwiredStreamFault(method)
	}
	stream, err := streams.OpenTerminalRetain(ctx, sessionID, id)
	if err != nil {
		return nil, officialJobTerminalFault(method, err)
	}
	return stream, nil
}

// officialMethodFromRequest 给测试与其他调用方解出方法名（/api/<method> → <method>）。
func officialMethodFromRequest(r *http.Request) string {
	if r == nil || r.URL == nil {
		return ""
	}
	// 有的服务器会拼上查询串；这里只取路径部分。
	if u, err := url.Parse(r.URL.String()); err == nil && u.Path != "" {
		return strings.TrimPrefix(u.Path, "/api/")
	}
	return strings.TrimPrefix(r.URL.Path, "/api/")
}
