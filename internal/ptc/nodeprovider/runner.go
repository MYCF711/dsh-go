package nodeprovider

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"dshgo/internal/ptc"
)

// bindReply 是一条绑定调用的回复。
type bindReply struct {
	value json.RawMessage
	err   error
}

// runState 是**一次**求值的状态：日志与字节预算。
//
// 常驻会话（scriptPersistence）里进程跨多次求值复用，所以这些计数不能挂在
// session 上——否则第二轮会继承第一轮的日志与预算。官方也是每次求值一只
// OutputLedger（`worker.ts:101-104`）。
type runState struct {
	mu       sync.Mutex
	logs     []string
	logBytes int64
}

// appendLog 收一条日志；超过预算返回 false（调用方负责报 output-limit）。
func (r *runState) appendLog(text string, limit int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, text)
	r.logBytes += int64(len(text)) + 1
	return r.logBytes <= limit
}

func (r *runState) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.logs))
	copy(out, r.logs)
	return out
}

// session 是**一个受管进程**：一次性运行或按 stateOwner 常驻。
//
// 生命周期：start → 握手 → 收帧 → settle → close（终止并回收进程树）。
// 常驻会话在 settle 之后**不关**，下一轮直接再发 eval（官方 worker.ts 同形）。
type session struct {
	p    *Provider
	tree processTree

	cmd   *exec.Cmd
	stdin io.WriteCloser

	writeMu sync.Mutex

	frames  chan frame
	done    chan struct{}
	stderr  *ringBuffer
	waitErr error
	exited  chan struct{}

	// format/retains 由 provider 的 executionMode 决定，整个会话固定。
	format  ptc.ProgramFormat
	retains bool
	// busy 是"这一轮有人在用"的单飞闸（官方 worker.ts:76-79 的 assertIdle）。
	busy atomic.Bool

	specMu sync.RWMutex
	spec   ptc.RunSpec
	runs   int

	settleMu sync.Mutex
	settled  bool
	killed   atomic.Bool
}

func (s *session) setSpec(spec ptc.RunSpec) {
	s.specMu.Lock()
	s.spec = spec
	s.specMu.Unlock()
}

func (s *session) currentBindings() []ptc.BindingNamespace {
	s.specMu.RLock()
	defer s.specMu.RUnlock()
	return s.spec.Bindings
}

func (s *session) isClosed() bool {
	s.settleMu.Lock()
	defer s.settleMu.Unlock()
	return s.settled
}

// ringBuffer 保留 stderr 的**尾部**：进程被打死时前半段基本都是噪音，
// 真正说明问题的那句在最后。
type ringBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func newRingBuffer(limit int) *ringBuffer {
	if limit <= 0 {
		limit = 32 << 10
	}
	return &ringBuffer{limit: limit}
}

func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.limit {
		r.buf = r.buf[len(r.buf)-r.limit:]
	}
	return len(p), nil
}

func (r *ringBuffer) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buf)
}

// start 起一个受管进程并完成握手。retains=true 表示这次会话会跨多次求值常驻。
func (p *Provider) start(ctx context.Context, nodePath string, spec ptc.RunSpec, retains bool) (*session, error) {
	cmd := exec.Command(nodePath, "--no-warnings", p.heapArg(), p.script)
	cmd.Dir = spec.Cwd
	cmd.Env = append(os.Environ(), "NODE_NO_WARNINGS=1")
	buildProcAttrs(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	s := &session{
		p:       p,
		spec:    spec,
		cmd:     cmd,
		stdin:   stdin,
		frames:  make(chan frame, 64),
		done:    make(chan struct{}),
		stderr:  newRingBuffer(0),
		exited:  make(chan struct{}),
		format:  p.format,
		retains: retains,
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("起 node 失败: %w", err)
	}
	if tree, treeErr := attachProcessTree(cmd); treeErr == nil {
		s.tree = tree
	} else {
		// 收不到整棵树就只杀根：能力降级如实记一句，但仍然算"受管"。
		s.stderr.Write([]byte("nodeprovider: 未能挂接进程树 Job Object：" + treeErr.Error() + "\n"))
	}

	go func() {
		_, _ = io.Copy(s.stderr, stderr)
	}()
	go func() {
		s.waitErr = cmd.Wait()
		close(s.exited)
	}()
	go s.readFrames(stdout)

	if err := s.handshake(ctx); err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

// handshake 等 worker 的 ready 帧。起进程/握手也有自己的上限，别让"node 卡在
// 启动"占满整个执行预算。
func (s *session) handshake(ctx context.Context) error {
	budget := 15 * time.Second
	if s.spec.TimeoutMs != nil {
		remaining := time.Duration(*s.spec.TimeoutMs) * time.Millisecond
		if remaining > 0 && remaining < budget {
			budget = remaining
		}
	}
	timer := time.NewTimer(budget)
	defer timer.Stop()
	for {
		select {
		case f, ok := <-s.frames:
			if !ok {
				return fmt.Errorf("worker 在握手前退出：%s", strings.TrimSpace(s.stderr.String()))
			}
			if f.T == "ready" {
				return nil
			}
			// 握手帧之前的其它帧是协议违规：worker 必须先 ready。
			return fmt.Errorf("握手前后收到意外帧 %q", f.T)
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("等 node 握手超过 %s", budget)
		case <-s.exited:
			return fmt.Errorf("worker 在握手前进程退出：%s", strings.TrimSpace(s.stderr.String()))
		}
	}
}

// readFrames 把 stdout 逐行解成帧。单帧超上限就是协议违规，直接关通道。
func (s *session) readFrames(r io.Reader) {
	defer close(s.frames)
	scanner := bufio.NewScanner(r)
	max := s.p.cfg.MaxMessageBytes
	if max <= 0 {
		max = 128 << 20
	}
	scanner.Buffer(make([]byte, 0, 64<<10), int(max))
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var f frame
		if err := json.Unmarshal(line, &f); err != nil {
			s.emit(frame{T: "protocol", Message: "控制帧不是合法 JSON：" + err.Error()})
			return
		}
		if !s.emit(f) {
			return
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			s.emit(frame{T: "protocol", Message: fmt.Sprintf("控制帧超过 %d 字节上限", max)})
		}
	}
}

// emit 把一帧交给读循环；session 已经收尾时返回 false（写不进去就别把
// goroutine 永久堵在这条通道上）。
func (s *session) emit(f frame) bool {
	select {
	case s.frames <- f:
		return true
	case <-s.done:
		return false
	}
}

// write 写一帧。写失败只说明对端已经死了，交给上层按退出码归类。
func (s *session) write(f frame) error {
	encoded, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if int64(len(encoded)+1) > s.p.cfg.MaxMessageBytes {
		return fmt.Errorf("帧超过 %d 字节上限", s.p.cfg.MaxMessageBytes)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err = s.stdin.Write(append(encoded, '\n'))
	return err
}

// discardStaleFrames 丢掉上一次求值结算后可能还排在管道里的帧。
//
// worker 在 settle 之后不再发帧（`settled` 闸），所以正常情况下这里什么都不丢；
// 这条只是防止"上一轮的尾巴被当成这一轮的帧"——常驻会话里那会串状态。
func (s *session) discardStaleFrames() {
	for {
		select {
		case _, ok := <-s.frames:
			if !ok {
				return
			}
		default:
			return
		}
	}
}

// execute 发 eval 并跑到结算。
//
// started 是这次 Run 进入的时刻：elapsed 预算覆盖**启动 + 握手 + 执行**
// （官方原文："An enabled deadline covers runtime setup and execution"），
// 所以这里要把起进程已经花掉的那段扣掉。
func (s *session) execute(ctx context.Context, spec ptc.RunSpec, started time.Time, rs *runState) ptc.RunResult {
	s.setSpec(spec)
	s.discardStaleFrames()
	s.runs++
	runID := uint64(s.runs)

	var deadline <-chan time.Time
	var timer *time.Timer
	var budgetMs int64
	if spec.TimeoutMs != nil {
		budgetMs = *spec.TimeoutMs
		remaining := time.Duration(budgetMs)*time.Millisecond - time.Since(started)
		if remaining <= 0 {
			s.terminate()
			return ptc.Fail(ptc.FailureTimeout,
				fmt.Sprintf("执行超过 %d ms 预算（还没把程序交给 worker）", budgetMs), rs.snapshot())
		}
		timer = time.NewTimer(remaining)
		defer timer.Stop()
		deadline = timer.C
	}

	if err := s.write(frame{
		T:            "eval",
		ID:           runID,
		Program:      spec.Program,
		Format:       string(s.format),
		Cwd:          spec.Cwd,
		MaxOutput:    s.p.cfg.MaxOutputBytes,
		Namespaces:   wireNamespaces(spec.Bindings),
		RetainsState: s.retains,
	}); err != nil {
		return ptc.Fail(ptc.FailureWorkerExit, "把程序交给 worker 失败："+err.Error(), rs.snapshot())
	}

	for {
		select {
		case f, ok := <-s.frames:
			if !ok {
				return s.workerExit(rs)
			}
			switch f.T {
			case "log":
				if !rs.appendLog(f.Text, s.p.cfg.MaxOutputBytes) {
					s.terminate()
					return ptc.Fail(ptc.FailureOutputLimit,
						fmt.Sprintf("捕获输出超过 %d 字节上限，运行已终止", s.p.cfg.MaxOutputBytes), rs.snapshot())
				}
			case "bind":
				go s.handleBind(ctx, f)
			case "done":
				return s.doneResult(f, rs)
			case "fail":
				return ptc.Fail(ptc.FailureKind(f.Kind), f.Message, rs.snapshot())
			case "protocol":
				s.terminate()
				return ptc.Fail(ptc.FailureProtocol, f.Message, rs.snapshot())
			case "ready":
				// 重复 ready 无害，忽略。
			default:
				s.terminate()
				return ptc.Fail(ptc.FailureProtocol, "未知控制帧 "+f.T, rs.snapshot())
			}
		case <-ctx.Done():
			s.terminate()
			return ptc.Fail(ptc.FailureAbort, "运行被调用方取消", rs.snapshot())
		case <-deadline:
			s.terminate()
			return ptc.Fail(ptc.FailureTimeout,
				fmt.Sprintf("执行超过 %d ms 预算", budgetMs), rs.snapshot())
		}
	}
}

// doneResult 组装成功结果，并按总字节预算再核一次（日志 + 完成值）。
func (s *session) doneResult(f frame, rs *runState) ptc.RunResult {
	logs := rs.snapshot()
	total := int64(0)
	for _, line := range logs {
		total += int64(len(line)) + 1
	}
	result := ptc.RunResult{Logs: logs}
	if f.Has != nil && *f.Has {
		total += int64(len(f.Value))
		if total > s.p.cfg.MaxOutputBytes {
			return ptc.Fail(ptc.FailureOutputLimit,
				fmt.Sprintf("日志与完成值合计超过 %d 字节上限", s.p.cfg.MaxOutputBytes), logs)
		}
		var value any
		decoder := json.NewDecoder(strings.NewReader(string(f.Value)))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return ptc.Fail(ptc.FailureInvalidOutput, "完成值不是可解码的 JSON："+err.Error(), logs)
		}
		result.Value = value
		result.HasValue = true
	}
	return result
}

// workerExit 把"对端没了"归类成 worker-exit，并带上退出码与 stderr 尾巴。
func (s *session) workerExit(rs *runState) ptc.RunResult {
	<-s.exited
	exit := "未知"
	if s.waitErr != nil {
		exit = s.waitErr.Error()
	} else if s.cmd.ProcessState != nil {
		exit = s.cmd.ProcessState.String()
	}
	tail := strings.TrimSpace(s.stderr.String())
	message := "worker 在结算前退出（" + exit + "）"
	if tail != "" {
		message += "；stderr 尾部：" + truncate(tail, 2000)
	}
	return ptc.Fail(ptc.FailureWorkerExit, message, rs.snapshot())
}

// handleBind 处理一条嵌套绑定调用。
//
// 绑定函数在**宿主**侧执行，所以审批/沙箱/超时全部照宿主既有通道走；
// 程序只是"问一句、等一句"，它拿不到任何绕过通道的路径。
func (s *session) handleBind(ctx context.Context, f frame) {
	callID := f.Call
	reply := func(r bindReply) {
		out := frame{T: "bindResult", Call: callID}
		if r.err != nil {
			out.OK = boolPtr(false)
			out.Message = r.err.Error()
		} else {
			out.OK = boolPtr(true)
			if r.value == nil {
				out.Value = json.RawMessage("null")
			} else {
				out.Value = r.value
			}
		}
		if err := s.write(out); err != nil {
			// 对端已死：没人收这条回复了，Run 的读循环会自行归类退出。
			_ = err
		}
	}

	fn, ok := lookupBinding(s.currentBindings(), f.NS, f.FN)
	if !ok {
		reply(bindReply{err: fmt.Errorf("没有名为 %s.%s 的绑定", f.NS, f.FN)})
		return
	}

	var args any
	if len(f.Args) > 0 && string(f.Args) != "null" {
		decoder := json.NewDecoder(strings.NewReader(string(f.Args)))
		decoder.UseNumber()
		if err := decoder.Decode(&args); err != nil {
			reply(bindReply{err: fmt.Errorf("参数不是无损 JSON：%v", err)})
			return
		}
	}

	value, err := fn(ctx, args)
	if err != nil {
		reply(bindReply{err: err})
		return
	}
	if problem := ptc.LosslessJSONProblem(value); problem != "" {
		reply(bindReply{err: fmt.Errorf("绑定 %s.%s 的返回值无法无损穿过 JSON 边界：%s", f.NS, f.FN, problem)})
		return
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		reply(bindReply{err: fmt.Errorf("绑定 %s.%s 的返回值序列化失败：%v", f.NS, f.FN, err)})
		return
	}
	reply(bindReply{value: encoded})
}

func lookupBinding(namespaces []ptc.BindingNamespace, global, member string) (ptc.BindingFunction, bool) {
	for _, ns := range namespaces {
		if ns.Global != global {
			continue
		}
		fn, ok := ns.Functions[member]
		return fn, ok
	}
	return nil, false
}

func wireNamespaces(namespaces []ptc.BindingNamespace) []wireNS {
	out := make([]wireNS, 0, len(namespaces))
	for _, ns := range namespaces {
		members := make([]string, 0, len(ns.Functions))
		for name := range ns.Functions {
			members = append(members, name)
		}
		entry := wireNS{Global: ns.Global, Members: members}
		if ns.ErrorClass != nil {
			entry.ErrorClass = &wireErrorClass{
				Name:               ns.ErrorClass.Name,
				MemberNameProperty: ns.ErrorClass.MemberNameProperty,
			}
		}
		out = append(out, entry)
	}
	return out
}

// terminate 收整棵进程树：先给一点合作停止的宽限，再硬杀，最后等退出。
func (s *session) terminate() {
	if s == nil || s.cmd == nil || s.cmd.Process == nil {
		return
	}
	if s.killed.Swap(true) {
		<-s.exited
		return
	}
	select {
	case <-s.exited:
		return
	default:
	}
	if s.tree != nil {
		_ = s.tree.Stop()
	}
	_ = s.cmd.Process.Kill()
	select {
	case <-s.exited:
	case <-time.After(s.p.cfg.Grace):
	}
}

// close 结束这个会话：终止进程树、关闭管道、回收临时资源。
func (s *session) close() {
	s.settleMu.Lock()
	if !s.settled {
		s.settled = true
		close(s.done)
	}
	s.settleMu.Unlock()
	s.terminate()
	if s.tree != nil {
		_ = s.tree.Close()
	}
	_ = s.stdin.Close()
}

// truncate 把 stderr 尾部裁到 limit **字节**以内，且截点一定落在 UTF-8 字符边界上。
//
// 为什么不能直接 `text[:limit]`：子进程 stderr 里是中文/混编码，按字节切会把
// 一个多字节字符切成半个——这条 message 会跟着 workerExit → ptc.Fail 变成
// **工具结果**交给模型和界面，json.Marshal 把那半个字符编码成 U+FFFD，
// 用户看到末尾一团乱码。退到边界最多少 3 个字节，预算口径不变。
func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…（截断）"
}
