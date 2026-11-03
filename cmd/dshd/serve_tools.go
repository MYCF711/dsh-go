package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"dshgo/codecs/v4"
	"dshgo/internal/transport"
)

// loopRunner 是 serve 的真 runner：起子进程 `dshd run`，把新消息推给界面。
//
// 为什么用子进程：跑工具要 kernel.Assemble 那一整套，它埋在 cmdRun 里；
// 搬出来会同时影响 run / schedule run / subagent 三条路。子进程是零风险复用——
// run 已经被 400 多个测试、真机、e2e 验过，不用重验一遍。
//
// 写锁：子进程按既有规矩拿会话写锁；这里**只读**日志，不抢锁。
type loopRunner struct {
	// journals 是**每会话一条**的序号域（H14）。帧的 seq 由它分配，
	// WS 推送与 session.history 读同一份——否则客户端判断断号时看到的是假象。
	// nil 时 emit 退化成"只推不记账"（老测试不必改）。
	journals *transport.Journals
	pub      func(topic string, f transport.ServerFrame)
	home     string
	policy   string
	maxSteps int
}

func (r *loopRunner) Run(ctx context.Context, sessionID, turnID, text string) error {
	exe, err := os.Executable()
	if err != nil {
		r.emitTurn(sessionID, turnID, err)
		return err
	}
	args := []string{"run",
		"--home", r.home,
		"--session", sessionID,
		"--prompt", text,
		"--policy", r.policy,
		"--max-steps", fmt.Sprint(r.maxSteps),
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		r.emitTurn(sessionID, turnID, err)
		return err
	}
	// 把子进程的身份写在 stderr 上：出问题时第一句该问的是"它到底起没起来"。
	// 上一轮就是因为没有这一行，分不清"被杀"和"压根没起"。
	fmt.Fprintf(os.Stderr, "spawn pid=%d argv=%v\n", cmd.Process.Pid, args)

	// 边跑边推：日志是 append 的，按条数差量推就够。
	// 必须**等 follow 真的退出**再发终局帧：否则它可能在我们发出 turn/done 之后
	// 又推一帧（子进程刚写完、我们刚读完的那个窗口），界面就会看到"结束后还有消息"。
	var prog followProgress
	var fw sync.WaitGroup
	done := make(chan struct{})
	// DSHD_SERVE_NO_FOLLOW=1 时不起 follow goroutine。
	// 只为二分：增长到底来自"spawn 子进程"还是"follow 读日志"。
	if os.Getenv("DSHD_SERVE_NO_FOLLOW") != "1" {
		fw.Add(1)
		go func() {
			defer fw.Done()
			r.follow(ctx, sessionID, done, &prog)
		}()
	}

	werr := cmd.Wait()
	// 子进程说了什么，运维侧必须看得到。
	// 没有这一行时，serve 起的子进程失败只能靠 WS 上的 turn/error 观察，
	// 而 e2e/curl 场景根本没人读 WS——于是"它没写盘"就变成一个无解释的现象。
	if s := strings.TrimSpace(stderr.String()); s != "" {
		fmt.Fprintf(os.Stderr, "child(stderr): %s\n", tail(s, 800))
	}
	close(done)
	fw.Wait()

	topic := "session:" + sessionID
	if werr != nil {
		e := fmt.Errorf("run 失败：%v；stderr: %s", werr, strings.TrimSpace(tail(stderr.String(), 400)))
		r.emitTurn(sessionID, turnID, e)
		return e
	}
	// 结局帧的序号由 journal 统一分配：它一定排在已经推出去的那些事件之后，
	// 不再需要"把计数器推到日志末尾"这种影子同步——那样两个来源迟早对不上。
	_ = topic
	r.emitTurn(sessionID, turnID, nil)
	return nil
}

// follow 每 200ms 看一次日志，把 seq 更大的**全部事件**推出去。
//
// 为什么不是只推消息：界面要显示"它调了什么工具"。
// 只推 assistant/message 的话，用户看到答案却看不到过程——
// 而"看不到过程"正是这类 agent 界面最不能接受的事。
func (r *loopRunner) follow(ctx context.Context, sessionID string, done <-chan struct{}, prog *followProgress) {
	src := newSessionSource(r.home, "")
	// 全程复用一个解码器：这个循环每 200ms 跑一次，建一次解码器就够。
	//
	// DSHD_SERVE_NO_REUSE=1 时退回"每次新建"，只为做 A/B——
	// 改动的收益必须能被同一台机器上的一次对照证明，不能靠"改完看着好了"。
	noReuse := os.Getenv("DSHD_SERVE_NO_REUSE") == "1"
	var rd *v4.Reader
	if !noReuse {
		var rerr error
		rd, rerr = v4.NewReader()
		if rerr != nil {
			return
		}
		defer rd.Close()
	}
	var (
		offset  int64
		lastSeq int64
		path    string
	)
	defer func() {
		prog.offset, prog.lastSeq, prog.path = offset, lastSeq, path
	}()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-done:
			// 收尾再读一次。子进程刚写完的 assistant/message、turn/end
			// 常常在 done 之后才落盘；不读这一次，界面永远看不到最后一条回答。
			// 2026-11-03 实测：会话文件里有 seq=3 assistant/message，流里没有。
			r.drain(rd, noReuse, path, &offset, &lastSeq, sessionID)
			return
		case <-ctx.Done():
			return
		case <-tick.C:
			if path == "" {
				p, err := src.resolve(sessionID)
				if err != nil {
					continue
				}
				path = p
			}
			// 二分用：DSHD_FOLLOW_MODE=resolve 只解析路径就睡；=read 再读但不推。
			mode := os.Getenv("DSHD_FOLLOW_MODE")
			if mode == "resolve" {
				continue
			}
			// 增量读：只解上次之后的新帧。全量重读在 2.2 万事件的会话上是 301ms，
			// 而这里每 200ms 读一次——读得比跑得还慢，界面会越落越远。
			var evs []v4.Event
			var next int64
			var err error
			if noReuse {
				evs, next, err = v4.ReadSince(path, offset)
			} else {
				evs, next, err = rd.ReadSince(path, offset)
			}
			if err != nil {
				continue
			}
			offset = next
			if mode == "read" {
				continue
			}
			dbgFollow := os.Getenv("DSHD_DEBUG_FOLLOW") != ""
			for _, ev := range evs {
				if dbgFollow {
					fmt.Fprintf(os.Stderr, "follow: got seq=%d type=%s lastSeq=%d\n", ev.Seq, ev.Type, lastSeq)
				}
				if ev.Seq <= lastSeq {
					if dbgFollow {
						fmt.Fprintf(os.Stderr, "follow: SKIP seq=%d (<= lastSeq=%d)\n", ev.Seq, lastSeq)
					}
					continue
				}
				lastSeq = ev.Seq
				// 走 emit：由 journal 发号。**不要再拿 ev.Seq 当送达序号**——
				// 那是 v4 日志的序号，跟这条连接上推了几帧没关系。
				r.emit(sessionID, ev.Type, ev.Data)
			}
		}
	}
}

// drain 把 offset 之后的所有事件读完并推出去。
//
// 存在的理由只有一个：follow 的 select 会在 done 时立刻 return，
// 而子进程最后写的那几帧（assistant/message、turn/end）往往晚一步落盘。
func (r *loopRunner) drain(rd *v4.Reader, noReuse bool, path string, offset *int64, lastSeq *int64, sessionID string) {
	for {
		var evs []v4.Event
		var next int64
		var err error
		if noReuse {
			evs, next, err = v4.ReadSince(path, *offset)
		} else {
			evs, next, err = rd.ReadSince(path, *offset)
		}
		if err != nil || len(evs) == 0 {
			return
		}
		*offset = next
		for _, ev := range evs {
			if ev.Seq <= *lastSeq {
				continue
			}
			*lastSeq = ev.Seq
			r.emit(sessionID, ev.Type, ev.Data)
		}
	}
}

// tail 取字符串末尾 n 个字节（错误正文可能很长，界面只要看得懂最后一句）。
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
