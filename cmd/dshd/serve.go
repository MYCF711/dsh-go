package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
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
type noModelRunner struct{}

func (noModelRunner) Run(context.Context, string, string, string) error {
	return errNoModel
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
	prompts := newPromptService(noModelRunner{})

	srv, err := transport.NewServer(transport.Options{
		Source: src,
		Addr:   *addr,
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
		Prompt: func(sessionID, text string) (transport.PromptResult, error) {
			// Submit 立刻返回，回答走 WS；这里绝不能等模型。
			// ctx 用 Background：turn 的生命周期长于这次 HTTP 请求，
			// 绑在请求上会在响应写完时把刚要开始的一轮掐掉。
			return prompts.Submit(context.Background(), sessionID, text)
		},
		Cancel: func(sessionID string) error {
			return prompts.Cancel(sessionID)
		},
		// Create 不做：建会话要落到 v4 日志（session.OpenSessionAt 那套，
		// 还要抢 <会话文件>.lock 保证同一份日志只有一个写入者），那是另一块活。
		// 这里明确报错，而不是返回一个假 id——假 id 会让界面把不存在的会话
		// 写进左栏，用户点进去才发现打不开。
		Create: func(mode string) (string, error) {
			return "", errors.New("serve 还没实现建会话")
		},
	})
	if err != nil {
		return err
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
