package main

// 审批的**归属**：一条答复只属于问出它的那条请求、那个子进程、那一轮。
//
// 这三条测试对应三处真实的串号路径（都在这三个文件里写过原因）：
//
//	1. 子进程侧：应答通道是一条共享 stdin 分发出来的，收到别的 requestId 的
//	   答复必须丢掉而不是当成自己的（approval_stdio.go: awaitApprovalResponse）；
//	2. 父进程侧：答复要写回**问出这条请求的那个会话**的子进程，不是 broker
//	   "最后一次见到的那根 stdin"（serve_approval.go: pendingAsk.stdin）；
//	3. 轮次侧：新子进程起来时，上一轮挂着的审批已经是死状态，必须摘掉
//	   而不是留着让重连把它重放成一条永远答不上的气泡。
//
// 三条都不需要改协议、不需要加弹窗，只是让实现回到它自己声明的契约上。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"dshgo/internal/transport"
)

// ① 子进程不许把**别人的**答复当成自己的。
//
// 红侧事实（修前）：`markerAsk` 只 `json.Unmarshal` 出 `allow`，从不看
// requestId。于是通道里如果先躺着一条来给别处的 `allow:true`（工具级超时之后
// 迟到的批准、或另一个会话的子进程被写错的那一行），下一次审批会直接拿走它——
// 用户看到的是"我没点这个，它自己跑了"。
func TestAwaitApprovalResponseIgnoresOtherRequestsAnswers(t *testing.T) {
	ch := make(chan string, 2)
	ch <- approvalResponseTag + `{"requestId":"perm-other","allow":true}`

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	allow, ok := awaitApprovalResponse(ctx, ch, "perm-mine")
	if ok {
		t.Fatalf("别的请求的 allow:true 被当成了自己的答复（ok=%v allow=%v）", ok, allow)
	}
	if allow {
		t.Fatal("别的请求的 allow:true 被当成了自己的批准")
	}

	// 自己那条仍然照常生效（丢掉别人的不等于把通道搞坏）。
	ch <- approvalResponseTag + `{"requestId":"perm-mine","allow":true}`
	ownCtx, ownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer ownCancel()
	allow, ok = awaitApprovalResponse(ownCtx, ch, "perm-mine")
	if !ok || !allow {
		t.Fatalf("自己那条答复没被认出来（ok=%v allow=%v）", ok, allow)
	}
}

// ② 答复要写回**问出这条请求的那个会话**的子进程。
//
// 红侧事实（修前）：`reply` 用 broker 的 `b.stdin`。`promptService` 的 busy 是
// 按会话判的，所以一个 serve 进程里两个会话可以同时各跑一轮；`useStdin` 换的
// 是**唯一**那个槽位，于是给会话 A 的答复写进了会话 B 的子进程。
// `terminal_bridge.go` 的 writeReply 注释里把这件事当成"requestId 不认得，
// 会被忽略"——那要靠 ① 才成立，而修前 ① 并不成立。
func TestApprovalReplyGoesToAskingSessionsChild(t *testing.T) {
	emitted := &emitLog{}
	b := newApprovalBroker(nil, func(sid, typ string, data json.RawMessage) {
		emitted.push(typ)
	})

	stdinA := &fakeStdin{}
	stdinB := &fakeStdin{}
	b.useStdinFor("sA", stdinA)
	b.useStdinFor("sB", stdinB) // B 后起：broker 的"当前 stdin"是 B

	b.Watch(strings.NewReader(approvalRequestTag+
		`{"requestId":"perm-a","sessionId":"sA","tool":"bash"}`+"\n"), nil)
	waitFrames(t, emitted, 1)

	if err := b.Resolve("perm-a", "allow_once"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	waitLines(t, stdinA, 1)

	if n := stdinB.count(); n != 0 {
		t.Fatalf("给会话 sA 的批准被写进了会话 sB 的子进程：%q", stdinB.wrote(0))
	}
	if !strings.Contains(stdinA.wrote(0), "perm-a") {
		t.Fatalf("sA 的子进程没拿到自己那条答复：%q", stdinA.wrote(0))
	}
}

// ③ 新子进程起来时，上一轮挂着的审批必须摘掉。
//
// 红侧事实（修前）：`pending` 只由结算（回答/超时/取消）删除。上一轮的子进程
// 已经没了、那一行答复写进死管道，而界面上那条 `approval/request` 会一直被
// `PendingFrames` 重放成"还在等你批准"；用户点"允许"得到 `{accepted:true}`，
// 却没有任何进程会执行它——**宿主把死状态说成了活的**。
func TestNewTurnDropsPreviousTurnsPendingApprovals(t *testing.T) {
	emitted := &emitLog{}
	b := newApprovalBroker(nil, func(sid, typ string, data json.RawMessage) {
		emitted.push(typ)
	})
	b.timeout = 40 * time.Millisecond

	first := &fakeStdin{}
	b.useStdinFor("s1", first)
	b.Watch(strings.NewReader(approvalRequestTag+
		`{"requestId":"perm-old","sessionId":"s1","tool":"write"}`+"\n"), nil)
	waitFrames(t, emitted, 1)
	if got := len(b.Pending("s1")); got != 1 {
		t.Fatalf("第一轮的审批没挂上：%d 条", got)
	}

	// 下一轮起来了：新子进程、新 stdin。上一轮那条审批已经不可能被回答。
	second := &fakeStdin{}
	b.useStdinFor("s1", second)

	if got := len(b.Pending("s1")); got != 0 {
		t.Fatalf("上一轮的审批还在重放列表里：%d 条（界面会显示一条永远答不上的气泡）", got)
	}
	if err := b.Resolve("perm-old", "allow_once"); err != transport.ErrNoSuchRequest {
		t.Fatalf("对已经换轮的请求作答应当是 ErrNoSuchRequest，实际 %v", err)
	}

	// 过掉它原来的超时点：不该再推 approval/timeout，也不该往新子进程写应答。
	time.Sleep(120 * time.Millisecond)
	if hasType(emitted.all(), "approval/timeout") {
		t.Fatalf("上一轮的审批换轮后还推了超时：%v", emitted.all())
	}
	if n := second.count(); n != 0 {
		t.Fatalf("上一轮的结算写进了新子进程：%q", second.wrote(0))
	}
}
