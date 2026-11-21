package main

// jobs.output 的**归属**：一条"查任务输出"请求只属于发起它的那个会话、那个子进程。
//
// 这条线与审批同一个根因：父进程问子进程要输出时曾用 broker 的"最后一根 stdin"
// （serve_approval.go 的 askJobsOut），而 busy 是按会话判的——两个会话同时跑时，
// A 的请求会被写进 B 的子进程。生产现在只走 scoped 路径
// （serve.go 的 JobsOutputScoped → jobActions.Output → liveRun(sessionID).stdin），
// 本文件钉住它"按会话绑死"：把请求改回共享/全局槽位，这两条会立刻红。

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"dshgo/internal/transport"
)

// jobSessionChild 是"一个会话那一轮的子进程"的最小替身：只读**自己那根**
// stdin 上的 jobs-out 请求，并按自己的身份回一条应答（输出里带自己的标记）。
type jobSessionChild struct {
	sessionID string
	stdin     io.WriteCloser
	requests  chan jobActionRequest
	done      chan struct{}
}

// startJobSessionChild 登记一个会话的 run 并起应答协程。
// replySession 非空时故意改用那个身份回答，用于钉"答复身份不符必须拒绝"。
func startJobSessionChild(t *testing.T, broker *jobActionBroker, sessionID, marker, replySession string) *jobSessionChild {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pr, pw := io.Pipe()
	run, err := broker.registerRun(sessionID, os.Getpid(), exe, pw)
	if err != nil {
		t.Fatalf("登记会话 %s 的 run：%v", sessionID, err)
	}
	child := &jobSessionChild{
		sessionID: sessionID,
		stdin:     pw,
		requests:  make(chan jobActionRequest, 8),
		done:      make(chan struct{}),
	}
	go func() {
		defer close(child.done)
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, jobsOutReqTag) {
				continue
			}
			var req jobActionRequest
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, jobsOutReqTag)), &req); err != nil {
				t.Errorf("会话 %s 的子进程收到坏请求：%q: %v", sessionID, line, err)
				return
			}
			child.requests <- req
			reply := req.SessionID
			if replySession != "" {
				reply = replySession
			}
			out := transport.JobOutput{
				ID:        req.ID,
				SessionID: reply,
				Output:    marker + ":" + req.ID,
				Done:      true,
			}
			resp := jobActionResponse{RequestID: req.RequestID, SessionID: reply, ID: req.ID, Output: &out}
			raw, err := json.Marshal(resp)
			if err != nil {
				t.Errorf("会话 %s 的应答编不出来：%v", sessionID, err)
				return
			}
			broker.deliver(jobsOutRespTag + string(raw))
		}
	}()
	t.Cleanup(func() {
		broker.unregisterRun(run)
		_ = pw.Close()
		select {
		case <-child.done:
		case <-time.After(2 * time.Second):
			t.Errorf("会话 %s 的子进程应答协程没退出", sessionID)
		}
	})
	return child
}

// 两会话同时查**同一个 job id**（正是旧的全局槽位会串号的情形）：
// 每根 stdin 只该收到本会话那一条请求，每段输出只该回到发起它的会话。
func TestJobActionsOutputStaysInAskingSessionsChild(t *testing.T) {
	broker := newJobActionBroker(t.TempDir())
	childA := startJobSessionChild(t, broker, "session-a", "A", "")
	childB := startJobSessionChild(t, broker, "session-b", "B", "")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	type result struct {
		sessionID string
		output    string
		err       error
	}
	results := make(chan result, 2)
	for _, sessionID := range []string{"session-a", "session-b"} {
		sessionID := sessionID
		go func() {
			out, err := broker.Output(ctx, sessionID, "job-1", 0, 16, 0)
			results <- result{sessionID: out.SessionID, output: out.Output, err: err}
		}()
	}

	got := map[string]string{}
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err != nil {
			t.Fatalf("%s 的 jobs.output 失败：%v", r.sessionID, r.err)
		}
		got[r.sessionID] = r.output
	}
	if got["session-a"] != "A:job-1" {
		t.Fatalf("会话 A 拿到了不属于它的输出：%q（应当 A:job-1）", got["session-a"])
	}
	if got["session-b"] != "B:job-1" {
		t.Fatalf("会话 B 拿到了不属于它的输出：%q（应当 B:job-1）", got["session-b"])
	}

	// 每根 stdin 只该收到本会话那一条请求。
	for _, child := range []*jobSessionChild{childA, childB} {
		select {
		case req := <-child.requests:
			if req.SessionID != child.sessionID || req.ID != "job-1" || req.RequestID == "" {
				t.Fatalf("会话 %s 的子进程收到不匹配的请求：%+v", child.sessionID, req)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("会话 %s 的子进程没收到自己那条请求（被写到全局槽位了？）", child.sessionID)
		}
		select {
		case extra := <-child.requests:
			t.Fatalf("会话 %s 的子进程收到了第二条/不属于它的请求：%+v", child.sessionID, extra)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// 子进程用别的会话身份回答时，宿主必须按跨会话拒绝，而不是把这段输出交给调用方。
func TestJobActionsOutputRejectsAnswerFromOtherSession(t *testing.T) {
	broker := newJobActionBroker(t.TempDir())
	startJobSessionChild(t, broker, "session-a", "A", "session-b")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := broker.Output(ctx, "session-a", "job-1", 0, 16, 0)
	if !errors.Is(err, transport.ErrCrossSession) {
		t.Fatalf("别的会话身份的答复必须按跨会话拒绝，实际 %v", err)
	}
}
