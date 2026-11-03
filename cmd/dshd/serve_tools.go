package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

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
	pub      func(topic string, f transport.ServerFrame)
	home     string
	policy   string
	maxSteps int
}

func (r *loopRunner) Run(ctx context.Context, sessionID, turnID, text string) error {
	exe, err := os.Executable()
	if err != nil {
		publishTurn(r.pub, "session:"+sessionID, turnID, err)
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
		publishTurn(r.pub, "session:"+sessionID, turnID, err)
		return err
	}

	// 边跑边推：日志是 append 的，按条数差量推就够。
	done := make(chan struct{})
	go r.follow(ctx, sessionID, done)

	werr := cmd.Wait()
	close(done)

	topic := "session:" + sessionID
	if werr != nil {
		e := fmt.Errorf("run 失败：%v；stderr: %s", werr, strings.TrimSpace(tail(stderr.String(), 400)))
		publishTurn(r.pub, topic, turnID, e)
		return e
	}
	publishTurn(r.pub, topic, turnID, nil)
	return nil
}

// follow 每 200ms 看一眼日志，把新出现的消息推出去。
func (r *loopRunner) follow(ctx context.Context, sessionID string, done <-chan struct{}) {
	src := newSessionSource(r.home, "")
	seen := 0
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-tick.C:
			msgs, err := src.Messages(sessionID)
			if err != nil || len(msgs) <= seen {
				continue
			}
			for _, m := range msgs[seen:] {
				data, _ := json.Marshal(map[string]any{"role": m.Role, "text": m.Text})
				if r.pub != nil {
					r.pub("session:"+sessionID, transport.ServerFrame{Seq: m.Seq, Type: "message", Data: data})
				}
			}
			seen = len(msgs)
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
