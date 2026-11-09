package main

// 官方 0.2.2-rc.1 的 job.* / terminal.* 在**宿主侧**的真实实现。
//
// # 复用的是什么（不是新造一套）
//
//   - job.*：既有 jobs 服务（`jobActionBroker`）——它已经有会话围栏（跨会话 403）、
//     实例/PID 映像校验、"已结束 409"、保留输出目录 `<home>/state/jobs/<sid>/<id>.json`，
//     以及子进程 IPC（本文一个字都不重写那套判断）。
//   - terminal.*：HS-08 的**真 ConPTY** 后端（`terminal_pty.go` 的
//     `officialPTYTerminalAPI`）——`internal/terminal` 的 ConPTY 会话、真实窗口
//     尺寸、真实退出码、有界滚动缓冲。本文件只做两件它不做的事：
//     **流式帧机**与**组装**。
//
// # 为什么不再有管道式终端
//
// 历史实现（本文件里的 `officialTerminalAPI`）包在 `modules/exec/bash` 的
// 管道式持久 shell 之上：cols/rows 只是元数据（没有 SIGWINCH）、attach 的
// screen 是原始输出尾部（没有屏幕模型）、environment.scrollback 恒为 0。
// 那是"不是 PTY"的临时态。HS-16D 把它**接上真 PTY**并删掉旧实现：
// 两套后端并存会让"官方 API 到底跑在哪个之上"没法回答。
//
// # 缺能力怎么办
//
// 返回 transport.ErrJTOfficialUnsupported（→ 501 not_implemented）+ 说清缺什么。
// 例如非 Windows 平台上 `terminal.create` 起不了 ConPTY（`internal/terminal`
// 如实回 ErrUnsupported），这里原样上抛，**不降级成管道冒充**。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"dshgo/internal/officialwire"
	"dshgo/internal/transport"
	"dshgo/modules/exec/bash"
)

const (
	// officialTerminalMaxInputBytes 是**本层 Input 强制**的单次写入上限。
	//
	// 注意：单次**上限**（64 KiB，协议层声明）不等于单次**成功写入**（见
	// officialTerminalInputChunkBytes，ConPTY 的输入批次有一个小得多的硬限）。
	officialTerminalMaxInputBytes = 64 * 1024
	// officialTerminalInputChunkBytes 是写 ConPTY 输入管道的**单片**字节上限。
	//
	// 实测（Windows ConPTY，T05 证据）：一次写入超过约 50 字节时，conhost 会把
	// 整批输入丢掉——不是部分写、也不返回错误：50 字符的命令能跑，60 字符的命令
	// 连回显都没有，且之后同一终端的输入也会被带坏。官方 uplink 的 write 帧是
	// 任意长度字符串（粘贴、多行），所以本层必须切片转发。
	//
	// 根因在 internal/terminal 的 ConPTY 写入路径（HS-08 的交付面，T05 不越界修；
	// 已登记在 RESULT 的"交给后续任务的发现"）。这里 32 字节留出余量，保证官方
	// terminal.attach 的 uplink 与既有 terminal.input 回落不会静默丢字。
	officialTerminalInputChunkBytes = 32
	// officialTerminalMaxScreenBytes 是快照/保留文本的上限（有界输出）。
	officialTerminalMaxScreenBytes = 256 * 1024
	// officialTerminalMaxCols / MaxRows 是 create 接受的尺寸上限（超出即截断，不报错）。
	officialTerminalMaxCols = 500
	officialTerminalMaxRows = 200
	// officialFollowPageBytes 是 job.follow 单帧携带的字节上限。
	officialFollowPageBytes = 64 * 1024
	// officialFollowMaxReadBytes 与执行层 1 MiB 保留窗口对齐：再往前读也读不到。
	officialFollowMaxReadBytes = 1024 * 1024
	// officialFollowMaxPages 是翻页上限（rune 游标 → 字节窗口的换算成本上限）。
	officialFollowMaxPages = 64
	// officialTerminalMaxIDRunes 是终端身份的字符上限（官方 id 由调用方给）。
	officialTerminalMaxIDRunes = 120
	// 官方 api-terminal-controller 的四条生命周期预算（默认值逐字对齐官方）。
	// 环境变量是宿主的配置入口（官方是插件配置项，宿主没有插件配置面）。
	officialTerminalDisposeGrace      = 1000 * time.Millisecond // terminal.disposeGraceMs
	officialTerminalCleanupRetry      = 60 * time.Second        // terminal.cleanupRetryMs
	officialTerminalActivityPoll      = 30 * time.Second        // terminal.activityPollIntervalMs
	officialTerminalUnattendedTimeout = 2 * time.Hour           // terminal.unattendedTimeoutMs

	// officialTerminalMaxTerminals 是官方 `terminal.maxTerminals`（默认 8）：
	// 同一个会话里**同时存活**的终端条数上限。没有它就能无限开 PTY，每条都占一个
	// conhost 进程与滚动缓冲，机器资源可被打满。计数只看未 closed 的条目
	// （closed 的留在 map 里是为了 terminal.close 的幂等语义，不占额度）。
	officialTerminalMaxTerminals = 8
	// officialRetainedPerSession 是每个会话保留布局的条数上限（有界池）。
	officialRetainedPerSession = 16
	// officialLabelMaxRunes 是 job 标签（命令首行）的字符上限。
	officialLabelMaxRunes = 200
	// officialFollowPollInterval 是帧机的**兜底**轮询间隔。
	//
	// 为什么是轮询：job.follow 的数据源是执行层的保留输出（子进程 IPC），
	// 不是本进程的 Transport journal——没有可等的广播。100ms 与既有
	// session.wait 的采样间隔同档：界面上看不出来，又不会把一次订阅变成忙等。
	officialFollowPollInterval = 100 * time.Millisecond
)

// ---------------------------------------------------------------------------
// job.*：包在既有 jobs 服务之上
// ---------------------------------------------------------------------------

// officialJobService 把既有 jobs 服务翻译成官方 job.* 的字段。
//
// 三个回调都由 serve.go 提供（roster 读 lr.jobs，output/stop 走 jobActionBroker），
// 本文件不自己找进程、不自己读盘。
type officialJobService struct {
	roster func(sessionID string) []transport.JobInfo
	output func(ctx context.Context, sessionID, id string, cursor, limit int, wait time.Duration) (transport.JobOutput, error)
	stop   func(ctx context.Context, sessionID, id string) (transport.JobStopResult, error)
}

// JobList 是官方 job/list：只列**这条会话能看到的**（自己的 + 未归属的）。
func (s *officialJobService) JobList(sessionID string) (transport.OfficialJobListFrame, error) {
	if s == nil || s.roster == nil {
		return transport.OfficialJobListFrame{}, fmt.Errorf("%w: 这个宿主没有接 jobs 清单", transport.ErrJTOfficialUnsupported)
	}
	if !validJobActionPart(sessionID) {
		return transport.OfficialJobListFrame{}, fmt.Errorf("%w: 非法 sessionId", transport.ErrJTOfficialBadParam)
	}
	infos := s.roster(sessionID)
	views := make([]transport.OfficialJobView, 0, len(infos))
	for _, info := range infos {
		// 归属围栏：别人的任务不出现在这条会话的清单里。
		if info.SessionID != "" && info.SessionID != sessionID {
			continue
		}
		views = append(views, officialJobView(info))
	}
	return transport.OfficialJobListFrame{Type: "rows", Jobs: views}, nil
}

// JobFollow 是**遗留**的一页读取入口，只为满足接口签名（HS-08 的 PTY 适配层
// 与既有测试按它编译）。wire **不会**走到它：job.follow 由
// `OfficialJobTerminalStreams.OpenJobFollow` 提供真流。
//
// Deprecated: 用 OpenJobFollow。这里明确拒绝而不是回一页，避免有人把
// "读一页"当成"实现过 follow"（那正是 HS-09 指出的残口）。
func (s *officialJobService) JobFollow(ctx context.Context, sessionID, jobID string, from int64) (transport.OfficialJobFollowPage, error) {
	return transport.OfficialJobFollowPage{}, fmt.Errorf(
		"%w: job.follow 是 typert mode:'stream'，wire 走 OpenJobFollow（一次性一页不算实现）",
		transport.ErrJTOfficialUnsupported)
}

// officialJobView 是 JobInfo → JobView 的唯一换算点。
//
// 已知缺口（都写在这里，不靠文档口头约定）：
//   - startedAt 恒为 0：既有 jobs 服务不暴露登记时间（spi.JobInfo 没有这个字段）。
//     官方要求它是 number，0 表示"不知道"，比编一个假时间诚实。
//   - finishedAt 不出现：同上。
//   - kind 恒为 "shell"：本宿主的后台任务都是 bash 执行层起的。
//   - status 只能分辨 running / completed / failed：killed 与 stopping 需要
//     产物自报的生命周期状态，执行层没有（停掉就是 done + 非零退出码）。
func officialJobView(info transport.JobInfo) transport.OfficialJobView {
	view := transport.OfficialJobView{
		ID:        info.ID,
		Kind:      "shell",
		Label:     officialJobLabel(info.Command),
		Status:    "running",
		StartedAt: 0,
		Output:    transport.OfficialJobOutput{Total: int64(info.Bytes)},
	}
	if info.SessionID != "" {
		view.Owner = info.SessionID
	}
	if info.Done {
		if info.ExitCode == 0 {
			view.Status = "completed"
		} else {
			view.Status = "failed"
		}
		view.Detail = fmt.Sprintf("exit code: %d", info.ExitCode)
	}
	return view
}

// officialJobLabel 把命令压成一行标签（官方 label 是"one-line label"）。
func officialJobLabel(cmd string) string {
	line := cmd
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(line)
	if utf8.RuneCountInString(line) > officialLabelMaxRunes {
		runes := []rune(line)
		line = string(runes[:officialLabelMaxRunes])
	}
	return line
}

// JobKill 是官方 job/kill：复用既有 Stop（围栏/映像校验/409 都在那里）。
func (s *officialJobService) JobKill(ctx context.Context, sessionID, jobID string) (transport.OfficialJobKillValue, error) {
	if s == nil || s.stop == nil {
		return transport.OfficialJobKillValue{}, fmt.Errorf("%w: 这个宿主没有接 jobs 停止动作", transport.ErrJTOfficialUnsupported)
	}
	if !validJobActionPart(sessionID) || !validJobActionPart(jobID) {
		return transport.OfficialJobKillValue{}, fmt.Errorf("%w: 非法 sessionId/jobId", transport.ErrJTOfficialBadParam)
	}
	if _, err := s.stop(ctx, sessionID, jobID); err != nil {
		// 错误原样上抛：transport 层按 sentinel 翻 403/404/409/500。
		// **不在这里把"已结束"翻译成 already-finished**：本仓契约要求 409，
		// 而且客户端据此才知道该去读保留输出，而不是以为停成功了。
		return transport.OfficialJobKillValue{}, err
	}
	return transport.OfficialJobKillValue{Outcome: "requested"}, nil
}

// jobReadState 是"从保留输出读一页真值"的结果。
type jobReadState struct {
	info     transport.JobInfo
	text     string
	done     bool
	exitCode int
	// anchor 是"总字节数"（绝对偏移的锚点）；earliest 是仍保留的最旧字节。
	anchor   int64
	earliest int64
}

// readState 按既有分页把保留输出读回来（有界：最多 1 MiB / 64 页）。
//
// 绝对总量的锚点优先用清单里的 JobInfo.Bytes（写出的总字节），
// 清单没有这一行时退化成"本次读到的字节数"——**不编造**更大的总量。
func (s *officialJobService) readState(ctx context.Context, sessionID, jobID string) (jobReadState, error) {
	text, done, exitCode, truncated, err := s.readAll(ctx, sessionID, jobID)
	if err != nil {
		return jobReadState{}, err
	}
	info := transport.JobInfo{ID: jobID, SessionID: sessionID, Done: done, ExitCode: exitCode, Bytes: len(text)}
	if s.roster != nil {
		for _, candidate := range s.roster(sessionID) {
			if candidate.ID == jobID && (candidate.SessionID == "" || candidate.SessionID == sessionID) {
				info = candidate
				info.Done, info.ExitCode = done, exitCode
				break
			}
		}
	}
	anchor := int64(len(text))
	if int64(info.Bytes) > anchor {
		anchor = int64(info.Bytes)
	}
	earliest := anchor - int64(len(text))
	if earliest < 0 {
		earliest = 0
	}
	_ = truncated
	return jobReadState{info: info, text: text, done: done, exitCode: exitCode,
		anchor: anchor, earliest: earliest}, nil
}

// readAll 按既有分页把保留输出读回来（有界：最多 1 MiB / 64 页）。
func (s *officialJobService) readAll(ctx context.Context, sessionID, jobID string) (string, bool, int, bool, error) {
	var sb strings.Builder
	cursor := 0
	done := false
	exitCode := 0
	truncated := false
	for page := 0; page < officialFollowMaxPages; page++ {
		out, err := s.output(ctx, sessionID, jobID, cursor, transport.MaxJobOutputLimit, 0)
		if err != nil {
			return "", false, 0, false, err
		}
		sb.WriteString(out.Output)
		cursor = out.NextCursor
		done = out.Done
		exitCode = out.ExitCode
		truncated = truncated || out.Truncated
		if !out.HasMore || sb.Len() >= officialFollowMaxReadBytes {
			break
		}
	}
	text := sb.String()
	if len(text) > officialFollowMaxReadBytes {
		text = text[len(text)-officialFollowMaxReadBytes:]
	}
	return text, done, exitCode, truncated, nil
}

// ---------------------------------------------------------------------------
// job.follow 帧机：opened → output* → status
// ---------------------------------------------------------------------------

// officialJobFollowStream 是 job/follow 的**拉模式帧机**。
//
// 每一帧都是"此刻的真实保留窗口"算出来的：offset 是**绝对字节**游标，
// 帧里的 at/next 就是它的坐标。慢客户端只会让自己这条流落后，不会让宿主的
// 保留输出无界增长（读的是既有 1 MiB 有界窗口）。
type officialJobFollowStream struct {
	svc       *officialJobService
	sessionID string
	jobID     string
	// offset 是下一个要交付的绝对字节偏移（-1 = 还没定开场锚点）。
	offset   int64
	opened   bool
	finished bool
	ticker   *time.Ticker
	// closed 是 Close 唤醒 Next 的通道（Close 可能在另一个 goroutine 调用）。
	closed   chan struct{}
	stopOnce sync.Once
}

var _ transport.OfficialFrameStream = (*officialJobFollowStream)(nil)

// Close 停掉兜底 ticker 并唤醒正在等待的 Next（幂等）。
func (s *officialJobFollowStream) Close() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.closed)
	})
	if s.ticker != nil {
		s.ticker.Stop()
	}
}

// Next 取下一帧：开场 opened、增量 output、终态 status，之后 done。
func (s *officialJobFollowStream) Next(ctx context.Context) (json.RawMessage, bool, *transport.OfficialStreamFault) {
	if s.finished {
		return nil, true, nil
	}
	if !s.opened {
		return s.open(ctx)
	}
	for {
		state, err := s.svc.readState(ctx, s.sessionID, s.jobID)
		if err != nil {
			return nil, false, transport.OfficialJobTerminalStreamFault("job.follow", err)
		}
		lossy := false
		if s.offset < state.earliest {
			// 请求的位置已被保留窗口淘汰：如实标 lossy + gapBefore。
			lossy = true
			s.offset = state.earliest
		}
		if s.offset < state.anchor {
			start := int(s.offset - state.earliest)
			if start < 0 {
				start = 0
			}
			if start > len(state.text) {
				start = len(state.text)
			}
			end := start + officialFollowPageBytes
			if end > len(state.text) {
				end = len(state.text)
			}
			chunk := transport.OfficialJobChunk{At: s.offset, Text: state.text[start:end]}
			if lossy {
				chunk.GapBefore = true
			}
			next := s.offset + int64(len(chunk.Text))
			s.offset = next
			raw, merr := json.Marshal(transport.OfficialJobFollowOutput{
				Type:   "output",
				Chunks: []transport.OfficialJobChunk{chunk},
				Next:   next,
				Lossy:  lossy,
			})
			if merr != nil {
				return nil, false, &transport.OfficialStreamFault{
					Code: "gateway/internal", Status: 500,
					Message: "job.follow：序列化 output 帧失败：" + merr.Error(),
				}
			}
			return raw, false, nil
		}
		if state.done {
			view := officialJobView(state.info)
			view.Output = transport.OfficialJobOutput{Total: state.anchor, Earliest: state.earliest}
			s.finished = true
			raw, merr := json.Marshal(transport.OfficialJobFollowStatus{Type: "status", Job: view})
			if merr != nil {
				return nil, false, &transport.OfficialStreamFault{
					Code: "gateway/internal", Status: 500,
					Message: "job.follow：序列化 status 帧失败：" + merr.Error(),
				}
			}
			return raw, false, nil
		}
		select {
		case <-ctx.Done():
			s.finished = true
			return nil, true, nil
		case <-s.closed:
			s.finished = true
			return nil, true, nil
		case <-s.ticker.C:
		}
	}
}

// open 发开场帧（opened）：它把 from 锚点钉死，之后每一帧都从 offset 继续。
func (s *officialJobFollowStream) open(ctx context.Context) (json.RawMessage, bool, *transport.OfficialStreamFault) {
	state, err := s.svc.readState(ctx, s.sessionID, s.jobID)
	if err != nil {
		return nil, false, transport.OfficialJobTerminalStreamFault("job.follow", err)
	}
	from := s.offset // -1 = 请求没给 from
	if from < 0 || from < state.earliest {
		from = state.earliest
	}
	s.offset = from
	s.opened = true
	view := officialJobView(state.info)
	view.Output = transport.OfficialJobOutput{Total: state.anchor, Earliest: state.earliest}
	raw, merr := json.Marshal(transport.OfficialJobFollowOpened{Type: "opened", Job: view, From: from})
	if merr != nil {
		return nil, false, &transport.OfficialStreamFault{
			Code: "gateway/internal", Status: 500,
			Message: "job.follow：序列化 opened 帧失败：" + merr.Error(),
		}
	}
	return raw, false, nil
}

// ---------------------------------------------------------------------------
// job.list 帧机：whole-set rows 帧
// ---------------------------------------------------------------------------

// officialJobListPollInterval 是 whole-set 帧的兜底轮询间隔。
//
// 为什么轮询：既有 jobs 注册表（serve.go 里的 lr.jobs）没有订阅回调，只有一个
// 按会话过滤的 roster 快照。官方语义是「打开一帧，之后每次（合并后的）生命周期
// 提交再发一整份」；在只有拉取快照的数据源上，最诚实的投影就是**轮询 + 内容
// 比对**：只有可见集合真的变了才发帧，不会定时重复同一份冒充"更新"。
const officialJobListPollInterval = 100 * time.Millisecond

// officialJobListStream 是 job/list 的**拉模式帧机**。
//
// 首帧在 Open 时算好并预取（参数/围栏/能力错误因此在写响应头之前暴露）；
// 之后只在 roster 的 JSON 表示变化时发帧。**没有自然结束**：ctx 取消或 Close
// 才结束（官方："The stream has no natural end; the carrier closes it."）。
type officialJobListStream struct {
	svc       *officialJobService
	sessionID string
	ticker    *time.Ticker
	closed    chan struct{}
	stopOnce  sync.Once
	// opening 是首帧（Open 时算好；取走后为 nil）。
	opening []byte
	// last 是上一次已发出的整帧；只用于变更检测，不发给客户端。
	last []byte
}

var _ transport.OfficialFrameStream = (*officialJobListStream)(nil)

// Close 停掉兜底 ticker 并唤醒正在等待的 Next（幂等）。
func (s *officialJobListStream) Close() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() { close(s.closed) })
	if s.ticker != nil {
		s.ticker.Stop()
	}
}

// Next 首帧返回 opening，之后只在可见集合真的变化时返回新帧。
func (s *officialJobListStream) Next(ctx context.Context) (json.RawMessage, bool, *transport.OfficialStreamFault) {
	if s.opening != nil {
		frame := s.opening
		s.opening = nil
		return frame, false, nil
	}
	for {
		select {
		case <-ctx.Done():
			return nil, true, nil
		case <-s.closed:
			return nil, true, nil
		case <-s.ticker.C:
		}
		raw, err := s.frame()
		if err != nil {
			return nil, false, transport.OfficialJobTerminalStreamFault("job.list", err)
		}
		if string(raw) == string(s.last) {
			continue
		}
		s.last = raw
		return raw, false, nil
	}
}

// frame 按当前 roster 生成一整帧 whole-set rows（官方 JobListFrame）。
func (s *officialJobListStream) frame() (json.RawMessage, error) {
	frame, err := s.svc.JobList(s.sessionID)
	if err != nil {
		return nil, err
	}
	// 官方 JobListFrame 的 type 只有 'rows'；实现没填就补上，填错就是实现 bug。
	if frame.Type == "" {
		frame.Type = "rows"
	}
	if frame.Type != "rows" {
		return nil, fmt.Errorf("job.list：结果帧 type 只能是 rows，实现给了 %q", frame.Type)
	}
	if frame.Jobs == nil {
		frame.Jobs = []transport.OfficialJobView{}
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		return nil, fmt.Errorf("job.list：序列化 rows 帧失败：%w", err)
	}
	return raw, nil
}

// openOfficialJobListStream 是 job.list 帧机的唯一构造点（HTTP 与 mux 共用）。
func openOfficialJobListStream(svc *officialJobService, sessionID string) (transport.OfficialFrameStream, error) {
	if svc == nil || svc.roster == nil {
		return nil, fmt.Errorf("%w: 这个宿主没有接 jobs 清单", transport.ErrJTOfficialUnsupported)
	}
	if !validJobActionPart(sessionID) {
		return nil, fmt.Errorf("%w: 非法 sessionId", transport.ErrJTOfficialBadParam)
	}
	stream := &officialJobListStream{
		svc: svc, sessionID: sessionID,
		ticker: time.NewTicker(officialJobListPollInterval),
		closed: make(chan struct{}),
	}
	raw, err := stream.frame()
	if err != nil {
		stream.Close()
		return nil, err
	}
	stream.opening = raw
	stream.last = raw
	return stream, nil
}

// ---------------------------------------------------------------------------
// terminal.attach 帧机：snapshot → output* → state
// ---------------------------------------------------------------------------

// officialTerminalAttachStream 是 attach 的**拉模式帧机**。
//
// 首帧是真实快照（官方 `snapshot`），之后每次从真 PTY 的滚动缓冲按**绝对
// 字节游标**取增量；进程退出后发 `state` 终态帧并结束。
//
// 上行（官方 uplink 的 write/resize）不在 HTTP 面：写入走既有 terminal.input，
// resize 走 `officialPTYTerminalAPI.Resize`（见 WIRING.md）。所以 snapshot 的
// `writable` 描述的是**终端本身可写**，不是"这条连接接受上行"。
type officialTerminalAttachStream struct {
	api       *officialPTYTerminalAPI
	sessionID string
	id        string

	mu       sync.Mutex
	offset   int64
	opened   bool
	finished bool
	// pending 是上行帧处理完后要**先于**输出帧下发的 ack 帧（官方 result 的
	// `{type:'ack', sequence, refusal?}`）。
	pending  []json.RawMessage
	ticker   *time.Ticker
	closed   chan struct{}
	stopOnce sync.Once
}

var (
	_ transport.OfficialFrameStream    = (*officialTerminalAttachStream)(nil)
	_ transport.OfficialTerminalUplink = (*officialTerminalAttachStream)(nil)
)

// Close 停掉兜底 ticker 并唤醒正在等待的 Next（幂等）。
func (s *officialTerminalAttachStream) Close() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.closed)
	})
	if s.ticker != nil {
		s.ticker.Stop()
	}
}

// Push 应用一帧官方 uplink（`write` / `resize`），并把 ack 排进下行队列。
//
// 官方 uplink 只在 mux 面存在（HTTP 一条请求一条响应，没有上行通道）。
// 参数错误必须**拒绝**（不静默丢输入）：返回 error，由 mux 适配层翻成
// gateway/bad-request 错误帧。
func (s *officialTerminalAttachStream) Push(raw json.RawMessage) error {
	var frame struct {
		Type     string `json:"type"`
		Sequence *int64 `json:"sequence"`
		Data     string `json:"data"`
		Cols     int    `json:"cols"`
		Rows     int    `json:"rows"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		return fmt.Errorf("%w: uplink 帧不是 JSON 对象：%v", transport.ErrJTOfficialBadParam, err)
	}
	switch frame.Type {
	case "write":
		if frame.Sequence == nil {
			return fmt.Errorf("%w: write 帧需要 sequence（客户端已消费到的绝对偏移）", transport.ErrJTOfficialBadParam)
		}
		if err := inputOfficialTerminal(s.api, s.sessionID, s.id, frame.Data); err != nil {
			s.enqueueAck(*frame.Sequence, refusalOf(err))
			return err
		}
		// ack.sequence 回显**上行帧自己的序号**（官方客户端按
		// `ack.sequence == acknowledged+1` 逐帧推进），不是输出字节偏移。
		s.enqueueAck(*frame.Sequence, "")
		return nil
	case "resize":
		if frame.Sequence == nil {
			return fmt.Errorf("%w: resize 帧需要 sequence", transport.ErrJTOfficialBadParam)
		}
		if frame.Cols <= 0 || frame.Rows <= 0 {
			return fmt.Errorf("%w: resize 的 cols/rows 必须是正整数（收到 %dx%d）",
				transport.ErrJTOfficialBadParam, frame.Cols, frame.Rows)
		}
		if err := s.api.Resize(s.sessionID, s.id, frame.Cols, frame.Rows); err != nil {
			s.enqueueAck(*frame.Sequence, refusalOf(err))
			return err
		}
		s.enqueueAck(*frame.Sequence, "")
		return nil
	}
	return fmt.Errorf("%w: uplink 帧 type 只能是 write/resize，收到 %q",
		transport.ErrJTOfficialBadParam, frame.Type)
}

// refusalOf 把写失败翻成官方 refusal 词（read-only / not-running）。
func refusalOf(err error) string {
	if errors.Is(err, transport.ErrJTOfficialTerminalGone) {
		return "not-running"
	}
	return "read-only"
}

// enqueueAck 排一帧 ack（refusal 为空 = 接受）。
func (s *officialTerminalAttachStream) enqueueAck(sequence int64, refusal string) {
	frame := transport.OfficialTerminalAckFrame{Type: "ack", Sequence: sequence, Refusal: refusal}
	raw, err := json.Marshal(frame)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.pending = append(s.pending, raw)
	s.mu.Unlock()
}

// takePending 取走一帧待发 ack（没有就返回 nil）。
func (s *officialTerminalAttachStream) takePending() json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) == 0 {
		return nil
	}
	frame := s.pending[0]
	s.pending = s.pending[1:]
	return frame
}

// Next 取下一帧：snapshot → output* → state，之后 done。
func (s *officialTerminalAttachStream) Next(ctx context.Context) (json.RawMessage, bool, *transport.OfficialStreamFault) {
	if s.finished {
		return nil, true, nil
	}
	// 上行处理完的 ack 先于输出帧下发（官方：ack 确认提供方已接受）。
	if ack := s.takePending(); ack != nil {
		return ack, false, nil
	}
	if !s.opened {
		snap, err := s.api.TerminalAttach(ctx, s.sessionID, s.id)
		if err != nil {
			return nil, false, transport.OfficialJobTerminalStreamFault("terminal.attach", err)
		}
		s.offset = snap.Sequence
		s.opened = true
		raw, merr := json.Marshal(transport.OfficialTerminalSnapshotFrame{
			Type: "snapshot", Sequence: snap.Sequence, Screen: snap.Screen,
			Info: snap.Info, Writable: snap.Writable,
		})
		if merr != nil {
			return nil, false, &transport.OfficialStreamFault{
				Code: "gateway/internal", Status: 500,
				Message: "terminal.attach：序列化 snapshot 帧失败：" + merr.Error(),
			}
		}
		return raw, false, nil
	}
	for {
		out, err := s.api.Output(s.sessionID, s.id, int(s.offset))
		if err != nil {
			return nil, false, transport.OfficialJobTerminalStreamFault("terminal.attach", err)
		}
		if out.Output != "" {
			s.offset = int64(out.Next)
			raw, merr := json.Marshal(transport.OfficialTerminalOutputFrame{
				Type: "output", Sequence: int64(out.Next), Data: out.Output,
			})
			if merr != nil {
				return nil, false, &transport.OfficialStreamFault{
					Code: "gateway/internal", Status: 500,
					Message: "terminal.attach：序列化 output 帧失败：" + merr.Error(),
				}
			}
			return raw, false, nil
		}
		if out.Dead {
			info, ierr := s.terminalInfo()
			if ierr != nil {
				return nil, false, transport.OfficialJobTerminalStreamFault("terminal.attach", ierr)
			}
			s.finished = true
			raw, merr := json.Marshal(transport.OfficialTerminalStateFrame{
				Type: "state", Info: info, Writable: false,
			})
			if merr != nil {
				return nil, false, &transport.OfficialStreamFault{
					Code: "gateway/internal", Status: 500,
					Message: "terminal.attach：序列化 state 帧失败：" + merr.Error(),
				}
			}
			return raw, false, nil
		}
		select {
		case <-ctx.Done():
			s.finished = true
			return nil, true, nil
		case <-s.closed:
			s.finished = true
			return nil, true, nil
		case <-s.ticker.C:
		}
	}
}

// terminalInfo 取终态帧要的 WebTerminalInfo（已退出的身份仍在表里）。
//
// 必须先进 screenOf：它把**真实退出码**与 state 同步进 entry（Output 只报
// Dead，不改元数据）。不同步就会出现"进程已退出、state 还是 running"这种
// 自相矛盾的终态帧。
func (s *officialTerminalAttachStream) terminalInfo() (transport.OfficialWebTerminalInfo, error) {
	entry, err := s.api.lookup(s.sessionID, s.id, "terminal.attach")
	if err != nil {
		return transport.OfficialWebTerminalInfo{}, err
	}
	s.api.screenOf(entry)
	return entry.info(), nil
}

// ---------------------------------------------------------------------------
// terminal.retain 帧机：retained 一帧，然后保持打开直到取消
// ---------------------------------------------------------------------------

// officialTerminalRetainStream 是 retain 的**寿命流**。
//
// 官方 result 描述：「a hold acknowledgement followed by an open lifetime
// stream」。所以这里发完 `{type:'retained'}` 之后**保持打开**，直到调用方取消；
// 取消即释放保留窗口。
//
// 保留效果本身（布局 + 屏幕尾部落盘、不被无人值守回收）由
// `officialPTYTerminalAPI.TerminalRetain` 负责，不靠这条 HTTP 连接活着：
// 连接断了不撤销已经写下的保留产物（那是客户端的持有凭证，不是租约）。
type officialTerminalRetainStream struct {
	api       *officialPTYTerminalAPI
	sessionID string
	id        string
	sent      bool
	finished  bool
	closed    chan struct{}
	stopOnce  sync.Once
}

var _ transport.OfficialFrameStream = (*officialTerminalRetainStream)(nil)

// Close 唤醒正在等待的 Next（幂等）。
func (s *officialTerminalRetainStream) Close() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() { close(s.closed) })
}

// Next 首帧 retained，之后阻塞到 ctx 取消再正常结束。
func (s *officialTerminalRetainStream) Next(ctx context.Context) (json.RawMessage, bool, *transport.OfficialStreamFault) {
	if s.finished {
		return nil, true, nil
	}
	if !s.sent {
		if err := s.api.TerminalRetain(ctx, s.sessionID, s.id); err != nil {
			return nil, false, transport.OfficialJobTerminalStreamFault("terminal.retain", err)
		}
		s.sent = true
		raw, merr := json.Marshal(transport.OfficialTerminalRetentionFrame{Type: "retained"})
		if merr != nil {
			return nil, false, &transport.OfficialStreamFault{
				Code: "gateway/internal", Status: 500,
				Message: "terminal.retain：序列化 retained 帧失败：" + merr.Error(),
			}
		}
		return raw, false, nil
	}
	select {
	case <-ctx.Done():
	case <-s.closed:
	}
	s.finished = true
	return nil, true, nil
}

// ---------------------------------------------------------------------------
// 组装：给 Lead 的一行接线入口
// ---------------------------------------------------------------------------

// officialJobTerminalService 把 job.* 与 terminal.* 两半合成 transport 要的服务面。
//
//   - 内嵌 `*officialJobService`：unary 方法（JobList/JobKill/JobFollow 一页读取）。
//   - 内嵌 `*officialPTYTerminalAPI`：terminal.* 的 unary 方法（HS-08 的真 ConPTY 后端）。
//   - 本类型显式实现 `transport.OfficialJobTerminalStreams`：四个流式方法的帧机。
//
// 为什么流式方法要显式写在**本类型**上：HS-08 的 `officialPTYTerminalAPI`
// 提供的是 unary 快照/保留原语；wire 需要的是流。把两者分开，
// "有没有流能力"就是一个可测试的类型断言（没有就 501），而不是靠实现里猜。
type officialJobTerminalService struct {
	*officialJobService
	*officialPTYTerminalAPI
}

// 编译期证明：组装出来的服务面同时满足 unary 接口与流能力接口。
var (
	_ transport.OfficialJobTerminalService = (*officialJobTerminalService)(nil)
	_ transport.OfficialJobTerminalStreams = (*officialJobTerminalService)(nil)
)

// Close 释放终端层（关掉本层起的会话）。
func (s *officialJobTerminalService) Close() {
	if s != nil && s.officialPTYTerminalAPI != nil {
		s.officialPTYTerminalAPI.Close()
	}
}

// Handler 返回可以直接挂到 transport.Options 上的路由对象。
//
// Lead 的接线只需要三行：构造 → Options 上挂 Handler() → defer Close()。
func (s *officialJobTerminalService) Handler() *transport.OfficialJobTerminal {
	return &transport.OfficialJobTerminal{Svc: s}
}

// OpenJobList 打开 job/list 的帧机（whole-set rows；没有自然结束）。
func (s *officialJobTerminalService) OpenJobList(ctx context.Context, sessionID string) (transport.OfficialFrameStream, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: 官方 job/terminal 服务面是 nil", transport.ErrJTOfficialUnsupported)
	}
	return openOfficialJobListStream(s.officialJobService, sessionID)
}

// OpenJobFollow 打开 job/follow 的帧机（from < 0 = 从仍保留的最旧字节起）。
func (s *officialJobTerminalService) OpenJobFollow(ctx context.Context, sessionID, jobID string, from int64) (transport.OfficialFrameStream, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: 官方 job/terminal 服务面是 nil", transport.ErrJTOfficialUnsupported)
	}
	return openOfficialJobFollowStream(s.officialJobService, sessionID, jobID, from)
}

// OpenTerminalAttach 打开 attach 的帧机（snapshot → output* → state）。
func (s *officialJobTerminalService) OpenTerminalAttach(ctx context.Context, sessionID, id string) (transport.OfficialFrameStream, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: 官方 job/terminal 服务面是 nil", transport.ErrJTOfficialUnsupported)
	}
	return openOfficialTerminalAttachStream(s.officialPTYTerminalAPI, sessionID, id)
}

// OpenTerminalRetain 打开 retain 的寿命流（retained 一帧后保持打开）。
func (s *officialJobTerminalService) OpenTerminalRetain(ctx context.Context, sessionID, id string) (transport.OfficialFrameStream, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: 官方 job/terminal 服务面是 nil", transport.ErrJTOfficialUnsupported)
	}
	return openOfficialTerminalRetainStream(s.officialPTYTerminalAPI, sessionID, id)
}

// ---------------------------------------------------------------------------
// 生产接线类型（`newOfficialPTYJobTerminalService`）的同一份流能力
// ---------------------------------------------------------------------------
//
// 为什么方法写在本文件而不是 terminal_pty.go：terminal_pty.go 属于 HS-08 的
// 交付面，T05 的写作用域只到 serve_terminal_api*.go；Go 允许在同一个包的
// 另一个文件里给类型补方法。这样 **serve.go 当前的构造一行都不用改**，
// 生产装配就不会再出现"服务面有真流、Options 挂的却是没有流能力的类型"的
// 静默 501（HS-16D 集成后实际就是这个状态，见 RESULT 的发现记录）。

var _ transport.OfficialJobTerminalStreams = (*officialPTYJobTerminalService)(nil)

// OpenJobList 打开 job/list 的 whole-set rows 帧机。
func (s *officialPTYJobTerminalService) OpenJobList(ctx context.Context, sessionID string) (transport.OfficialFrameStream, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: 官方 job/terminal 服务面是 nil", transport.ErrJTOfficialUnsupported)
	}
	return openOfficialJobListStream(s.officialJobService, sessionID)
}

// OpenJobFollow 打开 job/follow 的帧机（opened → output* → status）。
func (s *officialPTYJobTerminalService) OpenJobFollow(ctx context.Context, sessionID, jobID string, from int64) (transport.OfficialFrameStream, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: 官方 job/terminal 服务面是 nil", transport.ErrJTOfficialUnsupported)
	}
	return openOfficialJobFollowStream(s.officialJobService, sessionID, jobID, from)
}

// OpenTerminalAttach 打开 attach 的帧机（snapshot → output* → state）。
func (s *officialPTYJobTerminalService) OpenTerminalAttach(ctx context.Context, sessionID, id string) (transport.OfficialFrameStream, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: 官方 job/terminal 服务面是 nil", transport.ErrJTOfficialUnsupported)
	}
	return openOfficialTerminalAttachStream(s.officialPTYTerminalAPI, sessionID, id)
}

// OpenTerminalRetain 打开 retain 的寿命流（retained 一帧后保持打开）。
func (s *officialPTYJobTerminalService) OpenTerminalRetain(ctx context.Context, sessionID, id string) (transport.OfficialFrameStream, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: 官方 job/terminal 服务面是 nil", transport.ErrJTOfficialUnsupported)
	}
	return openOfficialTerminalRetainStream(s.officialPTYTerminalAPI, sessionID, id)
}

// openOfficialJobFollowStream 是 job/follow 帧机的唯一构造点。
func openOfficialJobFollowStream(svc *officialJobService, sessionID, jobID string, from int64) (transport.OfficialFrameStream, error) {
	if svc == nil {
		return nil, fmt.Errorf("%w: 这个宿主没有接 jobs 输出", transport.ErrJTOfficialUnsupported)
	}
	if !validJobActionPart(sessionID) || !validJobActionPart(jobID) {
		return nil, fmt.Errorf("%w: 非法 sessionId/jobId", transport.ErrJTOfficialBadParam)
	}
	if from < -1 {
		return nil, fmt.Errorf("%w: from 必须 >= 0（-1 = 未给，从最旧保留字节起）", transport.ErrJTOfficialBadParam)
	}
	return &officialJobFollowStream{
		svc: svc, sessionID: sessionID, jobID: jobID, offset: from,
		ticker: time.NewTicker(officialFollowPollInterval),
		closed: make(chan struct{}),
	}, nil
}

// openOfficialTerminalAttachStream 是 attach 帧机的唯一构造点。
func openOfficialTerminalAttachStream(api *officialPTYTerminalAPI, sessionID, id string) (transport.OfficialFrameStream, error) {
	if api == nil {
		return nil, fmt.Errorf("%w: 这个宿主没有接真 PTY 终端", transport.ErrJTOfficialUnsupported)
	}
	if !validJobActionPart(sessionID) {
		return nil, fmt.Errorf("%w: 非法 sessionId", transport.ErrJTOfficialBadParam)
	}
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("%w: terminal.attach 缺 id", transport.ErrJTOfficialBadParam)
	}
	return &officialTerminalAttachStream{
		api: api, sessionID: sessionID, id: id,
		ticker: time.NewTicker(officialFollowPollInterval),
		closed: make(chan struct{}),
	}, nil
}

// openOfficialTerminalRetainStream 是 retain 寿命流的唯一构造点。
func openOfficialTerminalRetainStream(api *officialPTYTerminalAPI, sessionID, id string) (transport.OfficialFrameStream, error) {
	if api == nil {
		return nil, fmt.Errorf("%w: 这个宿主没有接真 PTY 终端", transport.ErrJTOfficialUnsupported)
	}
	if !validJobActionPart(sessionID) {
		return nil, fmt.Errorf("%w: 非法 sessionId", transport.ErrJTOfficialBadParam)
	}
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("%w: terminal.retain 缺 id", transport.ErrJTOfficialBadParam)
	}
	return &officialTerminalRetainStream{
		api: api, sessionID: sessionID, id: id,
		closed: make(chan struct{}),
	}, nil
}

// ---------------------------------------------------------------------------
// 既有 terminal.input/output 的官方 PTY 回落（上行）
// ---------------------------------------------------------------------------
//
// 官方 wire 的上行只在 mux 面（attach 的 write/resize）；本仓 HTTP 面的一问一答
// 没有上行通道，但既有 additive 的 `terminal.input`/`terminal.output` 只认
// 管道式 `bash.Service` 的终端，官方 `terminal.create` 起的 PTY 终端它看不见。
// 这里给宿主一个**按 id 回落**的入口：serve.go 的 Options 闭包先试官方 PTY，
// 只有"没有这个 id"才回落既有管道终端（接线见 WIRING.md §3）。
//
// 官方 id 是调用方给的、按会话作用域，跨会话可能撞名。所以按 id 查找时：
//   - 唯一命中 → 用它；
//   - 多会话撞名 → 400（**不猜**；猜错就是往别人会话的终端里打字）；
//   - 没有 → ErrJTOfficialTerminalGone（调用方据此回落，而不是当成 500）。

// inputOfficialTerminal 按片写 ConPTY 输入（原因见 officialTerminalInputChunkBytes）。
//
// 单次**总量**仍受 officialTerminalMaxInputBytes（environment.maxInputBytes）约束：
// 分片只改变"怎么写"，不放宽协议声明的上限。
func inputOfficialTerminal(api *officialPTYTerminalAPI, sessionID, id, data string) error {
	if api == nil {
		return fmt.Errorf("%w: %s", transport.ErrJTOfficialTerminalGone, id)
	}
	if len(data) > officialTerminalMaxInputBytes {
		return fmt.Errorf("%w: 单次写入 %d 字节超过上限 %d",
			transport.ErrJTOfficialBadParam, len(data), officialTerminalMaxInputBytes)
	}
	for len(data) > 0 {
		n := officialTerminalInputChunkBytes
		if n > len(data) {
			n = len(data)
		}
		if err := api.Input(sessionID, id, data[:n]); err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

// lookupTerminalByID 按只有 id 的口径找官方 PTY 终端。
func (a *officialPTYTerminalAPI) lookupTerminalByID(id, method string) (*officialPTYEntry, error) {
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("%w: %s 缺 id", transport.ErrJTOfficialBadParam, method)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var hit *officialPTYEntry
	for _, entry := range a.entries {
		if entry.id != id || entry.closed {
			continue
		}
		if hit != nil && hit.sessionID != entry.sessionID {
			return nil, fmt.Errorf("%w: 终端 id %q 在多个会话里存在（%s 只给 id，无法判定会话）",
				transport.ErrJTOfficialBadParam, id, method)
		}
		hit = entry
	}
	if hit == nil {
		return nil, fmt.Errorf("%w: %s", transport.ErrJTOfficialTerminalGone, id)
	}
	return hit, nil
}

// InputByID 按 id 写官方 PTY 终端（既有 terminal.input 的回落入口）。
func (a *officialPTYTerminalAPI) InputByID(id, data string) error {
	entry, err := a.lookupTerminalByID(id, "terminal.input")
	if err != nil {
		return err
	}
	return inputOfficialTerminal(a, entry.sessionID, id, data)
}

// OutputByID 按 id 读官方 PTY 终端的增量输出（既有 terminal.output 的回落入口）。
func (a *officialPTYTerminalAPI) OutputByID(id string, since int) (transport.TerminalOutput, error) {
	entry, err := a.lookupTerminalByID(id, "terminal.output")
	if err != nil {
		return transport.TerminalOutput{}, err
	}
	return a.Output(entry.sessionID, id, since)
}

// InputByID 是官方服务面对既有 terminal.input 的回落入口。
func (s *officialJobTerminalService) InputByID(id, data string) error {
	if s == nil || s.officialPTYTerminalAPI == nil {
		return fmt.Errorf("%w: %s", transport.ErrJTOfficialTerminalGone, id)
	}
	return s.officialPTYTerminalAPI.InputByID(id, data)
}

// OutputByID 是官方服务面对既有 terminal.output 的回落入口。
func (s *officialJobTerminalService) OutputByID(id string, since int) (transport.TerminalOutput, error) {
	if s == nil || s.officialPTYTerminalAPI == nil {
		return transport.TerminalOutput{}, fmt.Errorf("%w: %s", transport.ErrJTOfficialTerminalGone, id)
	}
	return s.officialPTYTerminalAPI.OutputByID(id, since)
}

// InputByID 是生产接线类型对既有 terminal.input 的回落入口。
func (s *officialPTYJobTerminalService) InputByID(id, data string) error {
	if s == nil || s.officialPTYTerminalAPI == nil {
		return fmt.Errorf("%w: %s", transport.ErrJTOfficialTerminalGone, id)
	}
	return s.officialPTYTerminalAPI.InputByID(id, data)
}

// Input 是官方服务面的写入入口：与 InputByID 一样按片写（原因见
// officialTerminalInputChunkBytes）。声明在外层类型上，调用方（测试、宿主适配、
// 官方 attach 帧机）都不会绕过分片——绕过去的表现就是长粘贴静默丢字。
func (s *officialJobTerminalService) Input(sessionID, id, data string) error {
	if s == nil {
		return fmt.Errorf("%w: 官方 job/terminal 服务面是 nil", transport.ErrJTOfficialUnsupported)
	}
	return inputOfficialTerminal(s.officialPTYTerminalAPI, sessionID, id, data)
}

// Input 是生产接线类型的写入入口（同上，按片写）。
func (s *officialPTYJobTerminalService) Input(sessionID, id, data string) error {
	if s == nil {
		return fmt.Errorf("%w: 官方 job/terminal 服务面是 nil", transport.ErrJTOfficialUnsupported)
	}
	return inputOfficialTerminal(s.officialPTYTerminalAPI, sessionID, id, data)
}

// OutputByID 是生产接线类型对既有 terminal.output 的回落入口。
func (s *officialPTYJobTerminalService) OutputByID(id string, since int) (transport.TerminalOutput, error) {
	if s == nil || s.officialPTYTerminalAPI == nil {
		return transport.TerminalOutput{}, fmt.Errorf("%w: %s", transport.ErrJTOfficialTerminalGone, id)
	}
	return s.officialPTYTerminalAPI.OutputByID(id, since)
}

// ---------------------------------------------------------------------------
// mux 上行适配（officialwire.UplinkStream）
// ---------------------------------------------------------------------------

// officialUplinkStream 把 mux 的上行 item 转给 transport 的 attach 上行能力。
//
// 官方 uplink 只在 `/api/remote.mux` 的 open 流上存在：客户端发 write/resize，
// 宿主处理后回 ack（帧机已实现），拒绝时返回错误帧——**不静默丢输入**。
// Lead 只需在 serve_official_wire_stream.go 的 Open 返回处把
// `&officialMuxStream{stream: stream}` 换成 `wrapOfficialMuxUplinkStream(...)`
// （见 WIRING.md §2）；适配器放在本文件是为了与帧机共用同一份错误翻译。
type officialUplinkStream struct {
	officialwire.Stream
	uplink transport.OfficialTerminalUplink
}

// Push 把一帧上行转给 attach 帧机；拒绝要变成错误帧，不能吞掉。
func (s *officialUplinkStream) Push(value json.RawMessage) *officialwire.Error {
	if s == nil || s.uplink == nil {
		return officialwire.NewError("gateway/method-unavailable", "这条流不接受上行数据", nil)
	}
	if err := s.uplink.Push(value); err != nil {
		return officialwire.NewError("gateway/bad-request", err.Error(),
			map[string]any{"reason": "uplink-rejected"})
	}
	return nil
}

// wrapOfficialMuxUplinkStream 只给真正实现上行能力的流加 Push。
// 其它流原样返回：mux 对它们仍回"这条流不接受上行数据"，行为不变。
func wrapOfficialMuxUplinkStream(stream *officialMuxStream) officialwire.Stream {
	if stream == nil {
		return nil
	}
	if uplink, ok := stream.stream.(transport.OfficialTerminalUplink); ok {
		return &officialUplinkStream{Stream: stream, uplink: uplink}
	}
	return stream
}

// newOfficialJobTerminalService 组装官方 11 个方法的服务面（**真 PTY 后端**）。
//
// 参数全部来自既有 serve() 里的对象，本函数不新建任何执行层：
//   - svc：terminal.open 用的那个 *bash.Service（必须同一个）；
//   - jobs：既有 jobActionBroker（围栏/映像校验/409 都在里面）；
//   - roster：按会话给后台任务清单（serve.go 里从 lr.jobs 过滤）；
//   - cwdOf：会话工作目录（serve.go 里是 src.Cwd）。
//
// serve.go 那一行**不用改**：它一直调本函数，本任务把实现从管道式换成真 PTY。
func newOfficialJobTerminalService(
	home string,
	svc *bash.Service,
	jobs *jobActionBroker,
	roster func(sessionID string) []transport.JobInfo,
	cwdOf func(sessionID string) (string, error),
) (*officialJobTerminalService, error) {
	terminals, err := newOfficialPTYTerminalAPI(home, svc, cwdOf, officialPTYScrollbackRowsFromEnv())
	if err != nil {
		return nil, err
	}
	jobsSide := &officialJobService{roster: roster}
	if jobs != nil {
		jobsSide.output = jobs.Output
		jobsSide.stop = jobs.Stop
	}
	return &officialJobTerminalService{officialJobService: jobsSide, officialPTYTerminalAPI: terminals}, nil
}

// ---------------------------------------------------------------------------
// 终端 shell 探测（PTY 后端与既有 unary 方法共用同一份判据）
// ---------------------------------------------------------------------------

// officialShell 是探测结果 + 它是不是本宿主真正能起的那个。
type officialShell struct {
	transport.OfficialTerminalShell
	isDefault bool
}

// resolveOfficialShell 解析 create 的 shellPath：空 = 默认；指向默认 = 默认；
// 指向别的真实存在但本宿主起不来的 = 501（不是 400：参数没错，能力没到）。
func resolveOfficialShell(raw string) (officialShell, error) {
	def, err := officialDefaultShell()
	if err != nil {
		return officialShell{}, fmt.Errorf("%w: 找不到可用的默认 shell：%v", transport.ErrJTOfficialUnsupported, err)
	}
	if strings.TrimSpace(raw) == "" {
		return def, nil
	}
	abs, err := resolveShellPath(raw)
	if err != nil {
		return officialShell{}, fmt.Errorf("%w: shellPath %q 不是可执行文件：%v", transport.ErrJTOfficialBadParam, raw, err)
	}
	if strings.EqualFold(abs, def.Path) {
		return def, nil
	}
	// 能探测到但起不来：底层模块写死 bash 引导，这里如实回 501。
	// （Windows 上 PTY 后端会真的起目标 shell；这条分支留给非 Windows。）
	if runtime.GOOS != "windows" {
		return officialShell{}, fmt.Errorf("%w: 本宿主只支持默认 shell（%s），收到 %s",
			transport.ErrJTOfficialUnsupported, def.Path, abs)
	}
	return officialShell{
		OfficialTerminalShell: transport.OfficialTerminalShell{
			Path: abs, Args: shellArgsFor(abs), Name: filepath.Base(abs),
		},
	}, nil
}

// officialDefaultShell 必须和 modules/exec/bash 的 findBash **同一套优先级**：
// DSHD_BASH → PATH 里的 bash → Git for Windows 的两个常见位置。
//
// 这里重复了一份（模块那边没导出），所以 discoverOfficialShells 的测试会
// 用"起一个终端 + 跑一条命令回读"来证明默认 shell 真的能跑——不靠这份复制的正确性。
func officialDefaultShell() (officialShell, error) {
	candidates := []string{}
	if p := os.Getenv("DSHD_BASH"); p != "" {
		candidates = append(candidates, p)
	}
	if p, err := exec.LookPath("bash"); err == nil {
		candidates = append(candidates, p)
	}
	if runtime.GOOS == "windows" {
		candidates = append(candidates,
			`C:\Program Files\Git\bin\bash.exe`,
			`C:\Program Files\Git\usr\bin\bash.exe`)
	}
	for _, c := range candidates {
		abs, err := resolveShellPath(c)
		if err != nil {
			continue
		}
		return officialShell{
			OfficialTerminalShell: transport.OfficialTerminalShell{
				Path: abs, Args: shellArgsFor(abs), Name: filepath.Base(abs),
			},
			isDefault: true,
		}, nil
	}
	return officialShell{}, fmt.Errorf("没有可用的 bash（装 Git for Windows / 加进 PATH / 设 DSHD_BASH）")
}

// resolveShellPath 把候选解析成绝对路径并确认它是可执行文件。
func resolveShellPath(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("空路径")
	}
	abs := raw
	if !filepath.IsAbs(abs) {
		found, err := exec.LookPath(raw)
		if err != nil {
			return "", err
		}
		abs = found
	}
	abs, err := filepath.Abs(abs)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	st, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if st.IsDir() {
		return "", fmt.Errorf("%s 是目录", abs)
	}
	if runtime.GOOS == "windows" && !strings.EqualFold(filepath.Ext(abs), ".exe") &&
		!strings.EqualFold(filepath.Ext(abs), ".com") && !strings.EqualFold(filepath.Ext(abs), ".bat") {
		return "", fmt.Errorf("%s 不是 Windows 可执行文件", abs)
	}
	return abs, nil
}

// shellArgsFor 与官方 shells.js 的 profile() 对齐：
// cmd → []；pwsh/powershell → ["-NoLogo"]；其余 → ["-i"]。
//
// 本宿主的默认 shell 是 bash，但**不能**给它 -i：PTY 的交互性来自 tty 本身，
// 加 -i 会让提示符与作业控制进到输出里（官方 profile() 的意图是"别让 shell
// 读 profile"，不是"强行交互"）。
func shellArgsFor(path string) []string {
	kind := strings.ToLower(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
	switch kind {
	case "cmd":
		return []string{}
	case "pwsh", "powershell":
		return []string{"-NoLogo"}
	case "bash", "sh", "zsh", "dash":
		return []string{}
	}
	return []string{"-i"}
}

// discoverOfficialShells 真实探测：默认 shell 第一，其余按可执行名去重。
func discoverOfficialShells() ([]transport.OfficialTerminalShell, error) {
	def, err := officialDefaultShell()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", transport.ErrJTOfficialUnsupported, err)
	}
	// supported 的判据与 create 完全一致：默认 shell 永远可起；非默认只在
	// Windows 上可起（PTY 直接起目标 shell），其它平台 create 会明确 501。
	// 两处判据必须同源，否则界面会把"点了必报错"的项画成可用。
	supportedFor := func(isDefault bool) bool { return isDefault || runtime.GOOS == "windows" }
	supported := supportedFor(true)
	out := []transport.OfficialTerminalShell{{
		Path: def.Path, Args: def.Args, Name: def.Name, Supported: &supported,
	}}
	seen := map[string]bool{strings.ToLower(def.Name): true}
	candidates := []string{"cmd.exe", "powershell.exe", "pwsh.exe", "bash.exe", "sh"}
	if runtime.GOOS != "windows" {
		candidates = []string{"sh", "bash", "zsh", "dash", "fish"}
	}
	for _, candidate := range candidates {
		abs, err := resolveShellPath(candidate)
		if err != nil {
			continue // 没装就是没装，不是错误
		}
		name := filepath.Base(abs)
		key := strings.ToLower(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		flag := supportedFor(false)
		out = append(out, transport.OfficialTerminalShell{
			Path: abs, Args: shellArgsFor(abs), Name: name, Supported: &flag,
		})
	}
	return out, nil
}

// defaultTerminalTitle 是 create 没给标题时的默认值（官方是 shell 名）。
func defaultTerminalTitle(shell officialShell, id string) string {
	if name := shell.Name; name != "" {
		return name
	}
	return id
}

// clampTerminalSize 把尺寸夹进 environment 报的 maxCols/maxRows。
func clampTerminalSize(cols, rows int) (int, int) {
	if cols > officialTerminalMaxCols {
		cols = officialTerminalMaxCols
	}
	if rows > officialTerminalMaxRows {
		rows = officialTerminalMaxRows
	}
	return cols, rows
}

// ---------------------------------------------------------------------------
// 布局落盘（rename/retain 的"真持久化"）
// ---------------------------------------------------------------------------

type persistedTerminalLayout struct {
	Version   int                         `json:"version"`
	SessionID string                      `json:"sessionId"`
	Terminals []persistedTerminalIdentity `json:"terminals"`
}

type persistedTerminalIdentity struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	ShellPath string   `json:"shellPath"`
	ShellName string   `json:"shellName"`
	ShellArgs []string `json:"shellArgs"`
	Cwd       string   `json:"cwd"`
	Cols      int      `json:"cols"`
	Rows      int      `json:"rows"`
	Retained  bool     `json:"retained"`
	// Screen 只在 Retained 时写（有界尾部）：这是 retain 的真实产物。
	Screen  string `json:"screen,omitempty"`
	SavedAt int64  `json:"savedAt"`
}

// 说明：布局的编解码类型在这里，读写由 `officialPTYTerminalAPI` 的
// persistSession/loadLayouts 负责（terminal_pty.go）。以前管道式后端也有一份
// 同名的 persist/load，HS-16D 删掉了它——两套后端写同一份布局会互相覆盖。
