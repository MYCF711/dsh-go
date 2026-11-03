package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
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
	// payloads 留着原始负载，供重连时重放（requestId → 负载 + 属于哪个会话）。
	payloads map[string]pendingPayload
	stdin    io.WriteCloser
	// writeMu 串行化对子进程 stdin 的写。
	//
	// 为什么必须有：审批可以有**两个**来源同时结算——客户端点了允许、
	// 或超时那条 goroutine 到期。两边都会调 reply 往同一根 stdin 写一行 JSON，
	// 不串行就会写出交错的半行，子进程解析失败，而现象是"审批偶尔没生效"。
	writeMu sync.Mutex
	emit    func(sessionID, typ string, data json.RawMessage)
	// onJobs 收到子进程推上来的后台任务清单（`@@JOBS@@` 行）。
	// nil = 这个宿主不关心（老测试）。
	onJobs func(jobs []transport.JobInfo)

	// timeout 到了没人答 → 按**拒绝**结算。没人回答时"默认同意"是最坏的选择。
	timeout time.Duration

	// allowed 记「这个会话里这个工具以后都允许」。
	//
	// 边界说清楚：**只在内存里、只在这个 serve 进程内有效**，进程退出即失效——
	// 和 --policy 的生命周期一致。不落盘是刻意的：一次"以后都允许"若跨进程生效，
	// 用户换了会话、换了项目还会被静默放行，那不是他能预期的。
	allowed map[string]map[string]bool
}

// pendingPayload 是一条挂起审批的原始请求。
type pendingPayload struct {
	sessionID string
	tool      string
	data      json.RawMessage
}

func newApprovalBroker(stdin io.WriteCloser, emit func(sessionID, typ string, data json.RawMessage)) *approvalBroker {
	return &approvalBroker{
		pending:  map[string]chan bool{},
		payloads: map[string]pendingPayload{},
		allowed:  map[string]map[string]bool{},
		stdin:    stdin,
		emit:     emit,
		timeout:  approvalTimeout(),
	}
}

// approvalTimeout 是"没人回答就按拒绝算"的等待时长。
//
// 默认 120s；`DSHD_APPROVAL_TIMEOUT_SEC` 可以覆盖。
// 为什么要可配：**超时这条路径默认永远跑不到**（两分钟没人答才算数），
// 如果没有调小的办法，它就只能是"看起来实现了"。
func approvalTimeout() time.Duration {
	if v := os.Getenv("DSHD_APPROVAL_TIMEOUT_SEC"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 120 * time.Second
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
		// 后台任务清单也走这条 stderr 通道（见 main.go 的 emitJobs）。
		// **先认它再认审批**：两个标记前缀不同，顺序不影响正确性，
		// 但把新的放前面，读代码的人一眼看到"这条通道不止审批一件事"。
		if jobs, ok := parseJobsLine(line); ok {
			// onJobs 没接就当没有这件事：这条通道主要是审批，
			// 后台任务清单是顺路带过来的（原先这里是个空 else 分支，静态检查抓到了）。
			if b.onJobs != nil {
				b.onJobs(jobs)
			}
			continue
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

// parseJobsLine 解析 `@@JOBS@@ [{...}]`。
func parseJobsLine(line string) ([]transport.JobInfo, bool) {
	const tag = "@@JOBS@@ "
	if !strings.HasPrefix(line, tag) {
		return nil, false
	}
	var jobs []transport.JobInfo
	if err := json.Unmarshal([]byte(strings.TrimPrefix(line, tag)), &jobs); err != nil {
		return nil, false // 坏行当没看见：拒绝比"半个清单"好
	}
	return jobs, true
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
	// 用户之前在这个会话里对**同一个工具**点过"以后都允许"：不再问，直接放行。
	//
	// 不问就不发 approval/request——界面不会闪出一个已经不需要回答的气泡。
	// 但要在 stderr 留一行：自动化放行必须能在日志里查得到，否则事后没人能解释
	// "这次为什么没问我"。
	if b.alwaysAllowed(req.SessionID, req.Tool) {
		fmt.Fprintf(os.Stderr, "审批：会话 %s 的 %s 早先已选择「以后都允许」，本轮自动放行\n",
			req.SessionID, req.Tool)
		b.reply(req.RequestID, true)
		return
	}

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
	// 留着原始负载：客户端断开重连后要把这个"还在等"的问题再摆到它面前。
	b.mu.Lock()
	b.payloads[req.RequestID] = pendingPayload{sessionID: req.SessionID, tool: req.Tool, data: payload}
	b.mu.Unlock()

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

// Pending 返回这个会话上**还没被回答**的审批请求（原始负载）。
//
// 为什么需要重放：客户端可能断开重连、甚至整进程重启，那些气泡就没了；
// 而请求在宿主这边仍然挂着——子进程还在等。不重放的话，用户永远看不到那个问题，
// 子进程就一直等到超时被拒，看起来像"卡住了"。
func (b *approvalBroker) Pending(sessionID string) []json.RawMessage {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]json.RawMessage, 0, len(b.payloads))
	for _, p := range b.payloads {
		if p.sessionID == sessionID {
			out = append(out, p.data)
		}
	}
	return out
}

// PendingFrames 是 Pending 的帧形状版本，直接给 transport 用。
//
// 重放的帧**不带 seq**：它不是事件流里新的一帧（历史里已经有那条 approval/request），
// 只是把"仍然有效"的状态再摆一次；带号会让客户端以为又多了一条事件。
func (b *approvalBroker) PendingFrames(sessionID string) []transport.ServerFrame {
	raw := b.Pending(sessionID)
	out := make([]transport.ServerFrame, 0, len(raw))
	for _, d := range raw {
		out = append(out, transport.ServerFrame{Type: "approval/request", Data: d})
	}
	return out
}

// Resolve 是 session.approve 的落点：把界面的决定交给等待中的那一轮。
func (b *approvalBroker) Resolve(requestID, optionID string) error {
	allow, ok := decisionOf(optionID)
	if !ok {
		return transport.ErrBadOption
	}
	b.mu.Lock()
	ch, found := b.pending[requestID]
	var pend pendingPayload
	if found {
		pend = b.payloads[requestID]
		delete(b.pending, requestID)
		delete(b.payloads, requestID) // 答过了就不再重放
	}
	b.mu.Unlock()
	if !found {
		// 已经答过、已经超时、或者是编的——三种都当作"这个请求不在了"。
		return transport.ErrNoSuchRequest
	}
	// "以后都允许"要真的记住：否则界面给了这个选项、用户点了，
	// 下次同一个工具还被问一遍——**选项在、语义空**比没这个选项更糟。
	if optionID == "allow_always" {
		b.markAlways(pend.sessionID, pend.tool)
	}
	ch <- allow
	return nil
}

// alwaysAllowed 查「这个会话里这个工具已经选了以后都允许」。
func (b *approvalBroker) alwaysAllowed(sessionID, tool string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	m, ok := b.allowed[sessionID]
	return ok && m[tool]
}

// markAlways 记下「以后都允许」。粒度是**会话 + 工具名**，不是工具 + 参数：
// 一次"以后都允许 bash"就代表用户接受这个会话里所有 bash，
// 细化到参数会变成"每次都问一个新的"，那正是用户点这个选项想避免的。
func (b *approvalBroker) markAlways(sessionID, tool string) {
	if sessionID == "" || tool == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.allowed[sessionID] == nil {
		b.allowed[sessionID] = map[string]bool{}
	}
	b.allowed[sessionID][tool] = true
}

func (b *approvalBroker) forget(id string) {
	b.mu.Lock()
	delete(b.pending, id)
	delete(b.payloads, id)
	b.mu.Unlock()
}

// reply 把决定写回子进程。写失败不影响判定本身（子进程读不到会按拒绝处理）。
func (b *approvalBroker) reply(requestID string, allow bool) {
	if b.stdin == nil {
		return
	}
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
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
