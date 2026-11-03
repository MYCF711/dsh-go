package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"dshgo/internal/transport"
)

// approvalBroker 是审批的**父进程半边**（H13）。
//
// 链路：子进程 stderr 上的一行 `@@APPROVAL@@ {json}`
//
//	→ 这里解析并发 `approval/request`（进 journal，可重放）
//	→ 界面 POST session.approve
//	→ Resolve 把决定写回子进程 stdin 的一行 `@@APPROVAL-RESP@@ {json}`
//
// 为什么不做成"父进程直接执行工具"：工具跑在子进程里，父进程没有那次调用的上下文。
// 父进程只当**传话的**，判定仍然发生在真正执行的地方。
type approvalBroker struct {
	mu      sync.Mutex
	pending map[string]chan bool
	stdin   io.WriteCloser
	emit    func(sessionID, typ string, data json.RawMessage)

	// timeout 到了没人答 → 按**拒绝**结算。没人回答时"默认同意"是最坏的选择。
	timeout time.Duration
}

func newApprovalBroker(stdin io.WriteCloser, emit func(sessionID, typ string, data json.RawMessage)) *approvalBroker {
	return &approvalBroker{
		pending: map[string]chan bool{},
		stdin:   stdin,
		emit:    emit,
		timeout: 120 * time.Second,
	}
}

// useStdin 换成本轮子进程的 stdin（每轮一个子进程，管道是新的）。
func (b *approvalBroker) useStdin(w io.WriteCloser) {
	b.mu.Lock()
	b.stdin = w
	b.mu.Unlock()
}

// setEmit 挂上推送出口（由 serve 在启动时注入，闭包捕获 loopRunner）。
func (b *approvalBroker) setEmit(fn func(sessionID, typ string, data json.RawMessage)) {
	b.mu.Lock()
	b.emit = fn
	b.mu.Unlock()
}

// Watch 逐行扫子进程的 stderr，遇到审批请求就登记并推给界面。
//
// **必须逐行**：stderr 之前是 bytes.Buffer（跑完才读），那样审批要等到轮次结束——
// 而审批的意义就是"跑到一半停下来问"。这里是唯一的改动理由。
func (b *approvalBroker) Watch(r io.Reader, alsoTo io.Writer) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		// 原样留给运维日志：审批行也要能在 serve 的 stderr 里看到，
		// 不然出了事分不清"没问"和"问了没人答"。
		if alsoTo != nil {
			fmt.Fprintln(alsoTo, line)
		}
		req, ok := parseApprovalLine(line)
		if !ok {
			continue
		}
		b.handle(req)
	}
}

type approvalReq struct {
	RequestID string          `json:"requestId"`
	SessionID string          `json:"sessionId"`
	Tool      string          `json:"tool"`
	Args      json.RawMessage `json:"args"`
}

func parseApprovalLine(line string) (approvalReq, bool) {
	if !strings.HasPrefix(line, approvalRequestTag) {
		return approvalReq{}, false
	}
	var r approvalReq
	if json.Unmarshal([]byte(strings.TrimPrefix(line, approvalRequestTag)), &r) != nil {
		return approvalReq{}, false
	}
	if r.RequestID == "" {
		return approvalReq{}, false
	}
	return r, true
}

func (b *approvalBroker) handle(req approvalReq) {
	ch := make(chan bool, 1)
	b.mu.Lock()
	b.pending[req.RequestID] = ch
	b.mu.Unlock()

	// 形状与 ACP 的 session/request_permission 对齐（docs/h13-approval.md §契约）：
	// 界面两边显示同一套东西，用户在不同客户端看到的措辞才不会打架。
	payload, _ := json.Marshal(map[string]any{
		"requestId": req.RequestID,
		"sessionId": req.SessionID,
		"toolCall": map[string]any{
			"toolCallId": req.RequestID,
			"title":      approvalTitle(req.Tool, string(req.Args)),
			"kind":       approvalKind(req.Tool),
			"status":     "pending",
			"rawInput":   req.Args,
		},
		"options": []map[string]any{
			{"optionId": "allow_once", "name": "允许这一次", "kind": "allow_once"},
			{"optionId": "allow_always", "name": "这个会话里以后都允许", "kind": "allow_always"},
			{"optionId": "reject_once", "name": "拒绝", "kind": "reject_once"},
		},
	})
	if b.emit != nil {
		b.emit(req.SessionID, "approval/request", payload)
	}

	// 超时按拒绝结算，并让界面知道"是超时，不是有人点了拒绝"。
	go func() {
		select {
		case allow := <-ch:
			b.reply(req.RequestID, allow)
		case <-time.After(b.timeout):
			b.forget(req.RequestID)
			if b.emit != nil {
				b.emit(req.SessionID, "approval/timeout", payload)
			}
			b.reply(req.RequestID, false)
		}
	}()
}

// Resolve 是 session.approve 的落点：把界面的决定交给等待中的那一轮。
func (b *approvalBroker) Resolve(requestID, optionID string) error {
	allow, ok := decisionOf(optionID)
	if !ok {
		return transport.ErrBadOption
	}
	b.mu.Lock()
	ch, found := b.pending[requestID]
	if found {
		delete(b.pending, requestID)
	}
	b.mu.Unlock()
	if !found {
		// 已经答过、已经超时、或者是编的——三种都当作"这个请求不在了"。
		return transport.ErrNoSuchRequest
	}
	ch <- allow
	return nil
}

func (b *approvalBroker) forget(id string) {
	b.mu.Lock()
	delete(b.pending, id)
	b.mu.Unlock()
}

// reply 把决定写回子进程。写失败不影响判定本身（子进程读不到会按拒绝处理）。
func (b *approvalBroker) reply(requestID string, allow bool) {
	if b.stdin == nil {
		return
	}
	line, _ := json.Marshal(map[string]any{"requestId": requestID, "allow": allow})
	fmt.Fprintf(b.stdin, "%s%s\n", approvalResponseTag, line)
}

// decisionOf 把 optionId 翻成 true/false；不认识的返回 false,false。
//
// "不认识就当拒绝"在这里不成立：得让调用方明确报 400，
// 否则界面传了个错拼的 optionId，用户会以为"我点了允许但没生效"。
func decisionOf(optionID string) (allow bool, ok bool) {
	switch optionID {
	case "allow_once", "allow_always":
		return true, true
	case "reject_once":
		return false, true
	}
	return false, false
}
