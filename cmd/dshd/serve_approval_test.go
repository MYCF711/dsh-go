package main

import (
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"dshgo/internal/transport"
)

// fakeStdin 记录父进程写回子进程的每一行。
type fakeStdin struct{ lines []string }

func (f *fakeStdin) Write(p []byte) (int, error) {
	f.lines = append(f.lines, string(p))
	return len(p), nil
}
func (f *fakeStdin) Close() error { return nil }

// H13：界面的"允许"必须真的传回子进程。
//
// 这条链路最短的一环是"决定写回去了没有"——写不回去，子进程读不到就按拒绝处理，
// 用户点了允许却什么都没发生，而且**没有任何报错**。
func TestBrokerResolveWritesAllowBack(t *testing.T) {
	stdin := &fakeStdin{}
	var emitted []string
	b := newApprovalBroker(stdin, func(sid, typ string, data json.RawMessage) {
		emitted = append(emitted, typ)
	})

	line := approvalRequestTag + `{"requestId":"perm-1","sessionId":"s1","tool":"write","args":{"path":"a.txt"}}`
	b.Watch(strings.NewReader(line+"\n"), nil)

	// 等到 approval/request 推出去
	deadline := time.Now().Add(2 * time.Second)
	for len(emitted) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if len(emitted) == 0 || emitted[0] != "approval/request" {
		t.Fatalf("该先推 approval/request，实际 %v", emitted)
	}

	if err := b.Resolve("perm-1", "allow_once"); err != nil {
		t.Fatalf("Resolve 失败：%v", err)
	}
	// 等写回
	deadline = time.Now().Add(2 * time.Second)
	for len(stdin.lines) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if len(stdin.lines) == 0 {
		t.Fatalf("决定没写回子进程——这条链就是断的")
	}
	got := stdin.lines[0]
	if !strings.HasPrefix(got, approvalResponseTag) || !strings.Contains(got, `"allow":true`) {
		t.Fatalf("写回的行不对：%q", got)
	}
}

// H13：拒绝也要写回 false，不能只是"不写"。
//
// 不写的话子进程读不到会自己按拒绝处理——看起来对，但界面上分不清
// "用户点了拒绝"和"宿主根本没回"，两种情况的日志会一样。
func TestBrokerResolveWritesRejectBack(t *testing.T) {
	stdin := &fakeStdin{}
	b := newApprovalBroker(stdin, func(string, string, json.RawMessage) {})
	line := approvalRequestTag + `{"requestId":"perm-2","sessionId":"s1","tool":"write"}`
	b.Watch(strings.NewReader(line+"\n"), nil)

	if err := b.Resolve("perm-2", "reject_once"); err != nil {
		t.Fatalf("Resolve 失败：%v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(stdin.lines) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if len(stdin.lines) == 0 || !strings.Contains(stdin.lines[0], `"allow":false`) {
		t.Fatalf("拒绝也要写回 false，实际 %v", stdin.lines)
	}
}

// H13：认不出的 optionId → ErrBadOption（400），不能当"允许"也不能沉默。
func TestBrokerRejectsUnknownOption(t *testing.T) {
	b := newApprovalBroker(&fakeStdin{}, nil)
	if err := b.Resolve("whatever", "yolo"); err != transport.ErrBadOption {
		t.Fatalf("未知 optionId 要 ErrBadOption，实际 %v", err)
	}
}

// H13：答过一次之后再答同一条 → ErrNoSuchRequest（404）。
func TestBrokerDoubleResolveIs404(t *testing.T) {
	b := newApprovalBroker(&fakeStdin{}, func(string, string, json.RawMessage) {})
	line := approvalRequestTag + `{"requestId":"perm-3","sessionId":"s1","tool":"write"}`
	b.Watch(strings.NewReader(line+"\n"), nil)
	if err := b.Resolve("perm-3", "allow_once"); err != nil {
		t.Fatalf("第一次该成功：%v", err)
	}
	if err := b.Resolve("perm-3", "allow_once"); err != transport.ErrNoSuchRequest {
		t.Fatalf("第二次要 ErrNoSuchRequest，实际 %v", err)
	}
}

// H13：超时按**拒绝**结算，并推 approval/timeout（让界面知道是超时，不是有人拒绝）。
func TestBrokerTimeoutDenies(t *testing.T) {
	stdin := &fakeStdin{}
	var mu sync.Mutex
	var types []string
	b := newApprovalBroker(stdin, func(sid, typ string, data json.RawMessage) {
		mu.Lock()
		types = append(types, typ)
		mu.Unlock()
	})
	b.timeout = 30 * time.Millisecond

	line := approvalRequestTag + `{"requestId":"perm-4","sessionId":"s1","tool":"bash"}`
	b.Watch(strings.NewReader(line+"\n"), nil)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := len(types) >= 2 && len(stdin.lines) > 0
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(types) < 2 || types[1] != "approval/timeout" {
		t.Fatalf("超时该推 approval/timeout，实际 %v", types)
	}
	if len(stdin.lines) == 0 || !strings.Contains(stdin.lines[0], `"allow":false`) {
		t.Fatalf("超时必须按拒绝写回，实际 %v", stdin.lines)
	}
}

var _ = io.EOF

// H13：断线重连时要把**还挂着**的审批重放出来。
//
// 不重放的话，客户端重连后界面上什么都没有，而子进程还在那边等——
// 看起来就是"卡住了"，且没有任何线索说明它在等一个从没显示过的问题。
func TestBrokerReplaysPendingApproval(t *testing.T) {
	b := newApprovalBroker(&fakeStdin{}, func(string, string, json.RawMessage) {})
	line := approvalRequestTag + `{"requestId":"perm-9","sessionId":"s9","tool":"write"}`
	b.Watch(strings.NewReader(line+"\n"), nil)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(b.Pending("s9")) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	pending := b.Pending("s9")
	if len(pending) != 1 {
		t.Fatalf("该有 1 条挂起，实际 %d", len(pending))
	}
	if !strings.Contains(string(pending[0]), "perm-9") {
		t.Fatalf("重放的负载里要有 requestId：%s", pending[0])
	}

	// 别的会话不该看到它。
	if got := b.Pending("other"); len(got) != 0 {
		t.Fatalf("会话之间串了：%d", len(got))
	}

	// 答过之后就不再重放。
	if err := b.Resolve("perm-9", "allow_once"); err != nil {
		t.Fatalf("Resolve 失败：%v", err)
	}
	if got := b.Pending("s9"); len(got) != 0 {
		t.Fatalf("答过之后不该再重放，实际 %d 条", len(got))
	}
}
