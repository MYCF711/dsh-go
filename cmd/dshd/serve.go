package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"crypto/rand"
	"encoding/binary"

	"dshgo/internal/session"
	"dshgo/internal/transport"
)

// errNoModel 是 serve 的 turnRunner 给出的回答：**故意**失败。
//
// 这不是"忘了做"。真起一轮要有模型、工具流水线、审批与落盘那一整套
// （cmdRun / cmdACP 里的东西），而 serve 这一版只负责把链路打通：
// 界面能连上、能列会话、能读首屏与历史、能发 prompt 并拿到 turnID。
//
// 如果这里假装成功（返回 nil 而不产生任何事件），界面会看到
// "prompt 发出去了、turnID 也有了，但 WS 上永远没人说话"——那种沉默
// 比一条明确的错误难查十倍。所以 runner 直接拒绝，让"没接模型"这件事
// 在第一时间以错误的形式说出来。
var errNoModel = errors.New("serve 还没接模型；prompt 用来看链路，不产生真实回答")

// noModelRunner 是 turnRunner 的空实现：每次 Run 都拒绝。
//
// promptService 会把这个错误丢掉（它只负责清 busy），但接口语义是对的：
// 这一轮**失败了**，不是"还在跑"。
type noModelRunner struct {
	// journals 是所有帧的序号来源（H14）。这一类型通常没有流事件，
	// 但结局帧仍要与其它帧同一个域，否则客户端补洞时缺了"这一轮结束了"。
	journals *transport.Journals
	// pub 是推事件的出口。为 nil 时只记错误，不推。
	pub func(topic string, f transport.ServerFrame)
}

func (r noModelRunner) Run(_ context.Context, sessionID, turnID, _ string) error {
	err := errNoModel
	// prompt 已经回过 200 了，界面唯一的消息来源就是这里。
	// 不推这一帧，界面会永远停在"正在思考"。
	emitTurnFrame(r.journals, r.pub, sessionID, turnID, err)
	return err
}

// announcePolicy 把子进程实际使用的审批策略打到屏幕上。
//
// 为什么必须打：serve 无人值守，写操作会被拒绝；用户看不见这条，
// 就会把"工具被拒"当成"模型不会用工具"。**看不见的放宽与失误没有区别。**
func announcePolicy(w io.Writer, policy string, maxSteps int) {
	fmt.Fprintf(w, "policy %s（无人值守；写操作会被拒绝）；每轮最多 %d 步\n", policy, maxSteps)
}

// readyLine 是打到 stdout 的那一行，壳按它解析。
//
// 字段顺序由结构体顺序决定（encoding/json 的保证），壳可以按字面解析；
// 但更稳的做法是按 JSON 解析——这里保持最小形状，不额外加字段。
type readyLine struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

// cmdServe 实现 `dshd serve --home <dir> [--addr 127.0.0.1:0]`。
//
// 起来之后往 stdout 打**一行** ready 信息，格式必须完全一致（壳要解析它）：
//
//	ready {"url":"http://127.0.0.1:52341","token":"<一次性 token>"}
//
// 然后阻塞到 Ctrl-C / SIGTERM，收到信号后 Close() 再退出。
//
// 三个要点：
//   - 端口默认交给 OS（--addr 留空 → transport 用 127.0.0.1:0），不许写死；
//   - URL 只用 Start() 的返回值，绝不自己拼（壳与宿主必须看到同一个地址）；
//   - stdout 只放那一行 ready。日志/提示一律走 stderr，免得壳把诊断当协议解析。
//
// newEpochFunc 造一个"跨进程唯一且单调"的 epoch 发生器。
//
// 单独成函数是为了可测：两次调用必须给出不相交的区间——这正是
// "宿主重启后 epoch 撞号、客户端静默丢帧"那个 bug 的判据。
func newEpochFunc() func() int64 {
	// 基准用**随机 63 位**，不用时间：时间是"两次启动可能落在同一毫秒"的，
	// 而这里要的是"绝不可能撞"（撞了客户端会静默丢帧）。同一进程内再自增。
	var b [8]byte
	base := time.Now().UnixNano() // 随机源不可用时的兜底，绝不会走到
	if _, err := rand.Read(b[:]); err == nil {
		base = int64(binary.LittleEndian.Uint64(b[:]) & 0x7FFF_FFFF_FFFF_FFFF)
	}
	var n int64
	return func() int64 { n++; return base + n }
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	home := fs.String("home", envOr("DSHD_HOME", ""), "会话根目录（必填）")
	// 留空 = 让 transport 用 127.0.0.1:0，端口由 OS 分配。
	serveSteps := fs.Int("max-steps", 6, "每轮最多几步（子进程 run 的上限）")
	// H12：会话内容列宽（逻辑像素）。0 = 不知道，界面自己算。
	// 为什么不给默认值：官方是 min(748*0.702, 82%)，直接抄 748 会让别的窗口尺寸下偏宽/偏窄。
	serveWidth := fs.Int("content-width", 0, "会话内容列宽（逻辑像素）；0 = 不知道，界面自己算")
	// ask 现在是**能用**的：子进程走 @@APPROVAL@@ 标记通道，把问题转给界面（H13）。
	// 这条帮助文字以前写着"ask 等于永远挂起"，那是 H13 之前的事实，
	// 留着它会让人以为审批不能用——**过期的帮助文本比没有帮助更糟**。
	servePolicy := fs.String("policy", "deny", "子进程用哪个审批策略：deny/allow/ask（ask 会把审批转给界面）")
	// 模型必须能选：serve 起子进程跑真模型，用户换模型却只能去改环境变量，
	// 那是"藏起来的开关"。默认值和 run 保持一致。
	serveModel := fs.String("model", envOr("DSHD_LLM_MODEL", "group/deepseek-v4.1-flash1017"), "模型 id（透传给子进程）")
	// 推理强度透传给子进程；空 = 不要。界面线要能看到 reasoning 就得从这里打开。
	serveReasoning := fs.String("reasoning", os.Getenv("DSHD_REASONING_EFFORT"),
		"推理强度：空=不要（默认）| xhigh | max")
	addr := fs.String("addr", "", "监听地址（默认 127.0.0.1:0，端口由 OS 分配）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *home == "" {
		return errors.New("缺 --home")
	}
	// 和 run/acp 同一条防线：--home 指到文件上要在建任何东西之前就说清楚。
	if err := checkHome(*home); err != nil {
		return err
	}

	// 会话级模型覆盖（读盘失败不拦启动：界面上少一个下拉，比启不来好）。
	modelOverrides := newModelOverrideStore(*home)
	if err := modelOverrides.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "会话模型覆盖读盘失败（%v），本轮全部按默认模型跑\n", err)
	}

	// 会话级审批策略覆盖（同款；读盘失败就全按 --policy 走）。
	permissionOverrides := newPermissionOverrideStore(*home)
	if err := permissionOverrides.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "会话权限覆盖读盘失败（%v），本轮全部按 --policy 走\n", err)
	}

	// 会话数据的来源：宿主这一侧才被允许读 v4 日志（红线 R7）。
	// cwd 传空 = 按 id 全盘找，与 `session.resume` 的兜底一致。
	src := newSessionSource(*home, "")
	src.perms = permissionOverrides

	// Prompt / Cancel 走同一个服务，busy 的唯一结束点由它保证。
	// runner 现在固定拒绝（见 errNoModel 的注释）。
	// runner 要先建好（prompt 服务要它），但推事件的出口得等 srv 建完才有——
	// 所以用指针，建完再赋。
	// 子进程跑 run：工具循环、审批、压缩、落盘全部走既有的那一套。
	// DSHD_SERVE_MODEL_RUNNER=1 时走"不起子进程"的 runner。
	// 只为二分长跑时的内存增长：增长到底来自"跑一轮"，还是"子进程 + follow"。
	var runner turnRunner
	useModelRunner := os.Getenv("DSHD_SERVE_MODEL_RUNNER") == "1"
	// 一个 serve 进程内所有会话共用一张 journal 表：里面按 sessionID 分域，
	// 所以"共用表"不等于"共用序号"（H14）。
	// epoch 必须是**跨进程唯一**的。
	//
	// 踩过（界面审计实测复现）：原来是进程内自增（1,2,3…），宿主重启后
	// 同一会话又拿到 epoch=1。客户端带着旧水位 {sinceSeq:1, journalEpoch:1} 重订，
	// 世代"匹配"所以不发 session/reset，而 hub 按 seq > sinceSeq 过滤，
	// 把新 journal 的 seq=1 丢掉 —— 8 秒 0 帧，history 里明明有那条事件。
	// 客户端无报错、无 gap，界面表现就是"发出去没反应"。
	//
	// 改成拿启动时刻做基准：两次启动撞上同一纳秒不可能，而它仍然是单调递增的整数。
	journals := transport.NewJournals(newEpochFunc())
	lr := &loopRunner{home: *home, policy: *servePolicy, maxSteps: *serveSteps,
		journals: journals, reasoning: *serveReasoning, model: *serveModel}
	mr := &modelRunner{home: *home, journals: journals}

	// 审批的父进程半边（H13）：整进程一个 broker，每轮换 stdin。
	// emit 用 lr 的方法，所以帧一样进 journal、一样可重放。
	broker := newApprovalBroker(nil, nil)
	broker.setEmit(func(sid, typ string, data json.RawMessage) { lr.emit(sid, typ, data) })
	// 子进程推上来的后台任务清单 → 存到 lr，jobs.list 从这里读。
	broker.onJobs = func(jobs []transport.JobInfo) {
		lr.jobsMu.Lock()
		lr.jobs = jobs
		lr.jobsMu.Unlock()
	}
	lr.broker = broker
	if useModelRunner {
		runner = mr
	} else {
		runner = lr
	}
	prompts := newPromptService(runner)

	// 建会话的能力在起来之前就确定（cwd 定一次，不随后续请求漂移）。
	create, err := serveCreate(*home, "")
	if err != nil {
		return err
	}

	srv, err := transport.NewServer(transport.Options{
		Source: src,
		// 后台任务：**只读子进程推上来的那份**。JobsOutput 仍然空着——
		// 取某一条的实时输出需要子进程那边有"按 id 查"的应答通道，
		// 而当前协议是单向推清单，没有 request/response 对。
		JobsList: func() []transport.JobInfo {
			lr.jobsMu.Lock()
			defer lr.jobsMu.Unlock()
			return lr.jobs
		},
		// 定时任务：直接读 `<home>/state/schedules/`（和 `dshd schedule list` 同一个来源）。
		// **不复用 lr**：它不是子进程推的，是磁盘上的文件，每次问都能拿到最新的。
		ScheduleList: func() []transport.ScheduleItem {
			return serveScheduleList(*home)
		},
		// 会话级模型覆盖：写 <home>/state/session-model.json。
		// **不写 v4**（R5 读路径不写盘）；这是界面状态，进了日志会污染重放。
		SetSessionModel: func(sessionID, model string) error {
			return modelOverrides.Set(sessionID, model)
		},
		// 审批策略覆盖：写 <home>/state/session-permission.json（白名单在 handler 里校）。
		SetSessionPermission: func(sessionID, perm string) error {
			return permissionOverrides.Set(sessionID, perm)
		},
		Addr: *addr,
		// H14：把序号域交给 server——WS 推送与 session.history 必须读同一份，
		// 否则客户端补洞时补到的是另一个 seq 域的数据。
		Journals: journals,
		// H13：界面的允许/拒绝落到这里，由 broker 写回子进程 stdin。
		Approve: broker.Resolve,
		// 断线重连时把还挂着的审批重放给新连接：请求没被回答，子进程就一直等。
		PendingApprovals: broker.PendingFrames,
		// H5：把这一进程真正的审批策略报给界面。**不是 mode**。
		Policy:       *servePolicy,
		ContentWidth: *serveWidth,
		// List 复用 transport_list.go 的转换：列表只有一份真相，
		// 不要在这里写第二套"读磁盘 → SessionSummary"。
		List: func() ([]transport.SessionSummary, error) {
			return listSessions(*home, "")
		},
		Load: func(sessionID string, limit int) (transport.LoadResult, error) {
			return transport.LoadSession(src, sessionID, limit)
		},
		History: func(sessionID string, sinceSeq, limit int) (transport.Page, error) {
			return transport.HistorySession(src, sessionID, sinceSeq, limit)
		},
		ConfigGet: func() (map[string]string, error) { return serveConfigGet(*home) },
		ConfigSet: func(kv map[string]string) error { return serveConfigSet(*home, kv) },
		FSRead: func(path string) (transport.FSContent, error) {
			return serveFSRead(path)
		},
		FSWrite: func(path, content string) error {
			return serveFSWrite(path, content)
		},
		WorkspaceList: func(path string) ([]transport.WorkspaceEntry, error) {
			return serveWorkspaceList(path)
		},
		Delete: func(sessionID string) error {
			return serveDelete(*home, sessionID)
		},
		Fork: func(sessionID, title string) (string, error) {
			return serveFork(*home, sessionID, title)
		},
		// Compact：界面上的"压缩"。走和 CLI `dshd session compact --auto`
		// **同一段代码**（autoCompact），否则两条入口迟早压出两种结果。
		Compact: func(sessionID string, keep int) (transport.CompactResult, error) {
			path, err := session.ResolvePath(*home, "", sessionID)
			if err != nil {
				return transport.CompactResult{}, err
			}
			res, err := autoCompact(context.Background(), path, keep, 0,
				envOr("DSHD_LLM_MODEL", "group/deepseek-v4.1-flash1017"),
				envOr("DSHD_LLM_BASE", "http://127.0.0.1:3425/v1"))
			if err != nil {
				return transport.CompactResult{}, err
			}
			return transport.CompactResult{
				Compacted: res.Compacted, From: res.From, To: res.To,
				Shadowed: res.Shadowed, Summary: res.Summary,
			}, nil
		},
		Prompt: func(sessionID, text string) (transport.PromptResult, error) {
			// Submit 立刻返回，回答走 WS；这里绝不能等模型。
			// ctx 用 Background：turn 的生命周期长于这次 HTTP 请求，
			// 绑在请求上会在响应写完时把刚要开始的一轮掐掉。
			return prompts.Submit(context.Background(), sessionID, text)
		},
		Cancel: func(sessionID string) error {
			return prompts.Cancel(sessionID)
		},
		// Create 真建一份 v4 会话（见 serve_create.go）：id 用 crypto/rand、
		// header 带 agentPreset、返回前 Close 刷盘。cwd 传空 = serve 自己的当前目录。
		Create: create,
	})
	if err != nil {
		return err
	}
	if useModelRunner {
		mr.pub = srv.Publish
	} else {
		lr.pub = srv.Publish
	}

	base, err := srv.Start()
	if err != nil {
		return err
	}

	// 壳解析这一行；格式一字不差。
	line, err := json.Marshal(readyLine{URL: base, Token: srv.Token()})
	if err != nil {
		_ = srv.Close()
		return err
	}
	fmt.Fprintf(os.Stdout, "ready %s\n", line)
	// 打 stderr，不打 stdout：stdout 只放那一行 ready（壳按行解析握手）。
	// H11（2026-11-03 界面线报）：这行原来打在 stdout，客户端把第二行当成了协议内容。
	announcePolicy(os.Stderr, lr.policy, lr.maxSteps)

	// 阻塞到 Ctrl-C / SIGTERM。用信号而不是 stdin EOF：
	// serve 可能由壳拉起，stdin 会立刻是 EOF，那不代表"该关了"。
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	sig := <-stop
	signal.Stop(stop)

	if err := srv.Close(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "serve 已关闭（%v）\n", sig)
	return nil
}
