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
func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	home := fs.String("home", envOr("DSHD_HOME", ""), "会话根目录（必填）")
	// 留空 = 让 transport 用 127.0.0.1:0，端口由 OS 分配。
	serveSteps := fs.Int("max-steps", 6, "每轮最多几步（子进程 run 的上限）")
	// H12：会话内容列宽（逻辑像素）。0 = 不知道，界面自己算。
	// 为什么不给默认值：官方是 min(748*0.702, 82%)，直接抄 748 会让别的窗口尺寸下偏宽/偏窄。
	serveWidth := fs.Int("content-width", 0, "会话内容列宽（逻辑像素）；0 = 不知道，界面自己算")
	servePolicy := fs.String("policy", "deny", "子进程用哪个审批策略：deny/allow（serve 没有终端，ask 等于永远挂起）")
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

	// 会话数据的来源：宿主这一侧才被允许读 v4 日志（红线 R7）。
	// cwd 传空 = 按 id 全盘找，与 `session.resume` 的兜底一致。
	src := newSessionSource(*home, "")

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
	var epochSeq int64
	journals := transport.NewJournals(func() int64 { epochSeq++; return epochSeq })
	lr := &loopRunner{home: *home, policy: *servePolicy, maxSteps: *serveSteps, journals: journals}
	mr := &modelRunner{home: *home, journals: journals}
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
		Addr:   *addr,
		// H14：把序号域交给 server——WS 推送与 session.history 必须读同一份，
		// 否则客户端补洞时补到的是另一个 seq 域的数据。
		Journals: journals,
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
		WorkspaceList: func() ([]transport.WorkspaceEntry, error) {
			return serveWorkspaceList()
		},
		Delete: func(sessionID string) error {
			return serveDelete(*home, sessionID)
		},
		Fork: func(sessionID, title string) (string, error) {
			return serveFork(*home, sessionID, title)
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
