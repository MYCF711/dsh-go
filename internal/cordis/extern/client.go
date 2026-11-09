package extern

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// Options 是启动外部 node 进程的参数。
type Options struct {
	// NodePath 留空表示从 PATH 找 node；显式给值时不做任何猜测。
	NodePath string
	// Dir 是子进程的工作目录（留空 = 引导脚本所在临时目录）。
	Dir string
	// Env 是追加到 os.Environ() 之后的环境变量（"K=V"）。
	Env []string
	// MaxMessageBytes 限制单帧大小，0 用默认 8 MiB。
	MaxMessageBytes int
	// StderrBytes 限制保留的 stderr 尾部字节数，0 用默认 32 KiB。
	StderrBytes int
	// StartupTimeout 限制"起进程 + 握手"的时间，0 用默认 15s。
	StartupTimeout time.Duration
}

const (
	defaultMaxMessageBytes = 8 << 20
	defaultStderrBytes     = 32 << 10
	defaultStartupTimeout  = 15 * time.Second
)

// Client 是一个受管的外部 node 进程。
//
// 并发安全：Call 可被多个 goroutine 同时调用；Close 与 Call 并发时，
// 在途的 Call 会返回 ErrNotRunning。
type Client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	tree   processTree
	dir    string
	stderr *ringBuffer

	writeMu sync.Mutex

	mu      sync.Mutex
	pending map[uint64]chan *response
	runs    map[string]*RunRecord
	nextID  atomic.Uint64

	closed   atomic.Bool
	waitDone chan struct{}
	waitErr  error
}

// Start 起一个外部 node 进程并完成握手。
//
// 找不到/跑不了 node 时返回 *UnavailableError（响亮降级，见 doc.go）。
func Start(ctx context.Context, opts Options) (*Client, error) {
	nodePath, unres := resolveNode(opts)
	if unres != nil {
		return nil, unres
	}
	if unres := probeNode(ctx, nodePath); unres != nil {
		return nil, unres
	}

	dir, script, err := writeBootstrap()
	if err != nil {
		return nil, fmt.Errorf("cordis/extern: 写引导脚本失败: %w", err)
	}

	maxMsg := opts.MaxMessageBytes
	if maxMsg <= 0 {
		maxMsg = defaultMaxMessageBytes
	}
	stderrLimit := opts.StderrBytes
	if stderrLimit <= 0 {
		stderrLimit = defaultStderrBytes
	}

	cmd := exec.Command(nodePath, script)
	cmd.Dir = dir
	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}
	cmd.Env = append(os.Environ(), "NODE_NO_WARNINGS=1")
	cmd.Env = append(cmd.Env, opts.Env...)
	cmd.Stderr = nil // 下面用 pipe 自己收
	buildProcAttrs(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("cordis/extern: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("cordis/extern: stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("cordis/extern: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, &UnavailableError{
			Code:   CodeNodeUnusable,
			Reason: "node 起不来：" + err.Error(),
			Hint:   "确认 node 可执行且没有被安全软件拦住；继续用 Go 原生插件不受影响。",
		}
	}

	c := &Client{
		cmd:      cmd,
		stdin:    stdin,
		dir:      dir,
		stderr:   newRingBuffer(stderrLimit),
		pending:  make(map[uint64]chan *response),
		waitDone: make(chan struct{}),
	}

	tree, terr := attachProcessTree(cmd)
	if terr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("cordis/extern: 挂 Job Object 失败: %w", terr)
	}
	c.tree = tree

	go c.readLoop(stdout, maxMsg)
	go func() { _, _ = io.Copy(c.stderr, stderrPipe) }()
	go func() {
		c.waitErr = cmd.Wait()
		close(c.waitDone)
	}()

	// 握手：真实问一次 pid，证明协议通、进程活。
	hs := defaultStartupTimeout
	if opts.StartupTimeout > 0 {
		hs = opts.StartupTimeout
	}
	hctx, hcancel := context.WithTimeout(ctx, hs)
	defer hcancel()
	var pid struct {
		PID int `json:"pid"`
	}
	if err := c.Call(hctx, "pid", nil, &pid); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("cordis/extern: 握手失败（node 起了但不按协议说话）: %w", err)
	}
	return c, nil
}

// PID 返回外部进程 pid（0 表示没有）。
func (c *Client) PID() int {
	if c == nil || c.cmd == nil || c.cmd.Process == nil {
		return 0
	}
	return c.cmd.Process.Pid
}

// Alive 报告进程是否还在跑（未被 Close、未自行退出）。
func (c *Client) Alive() bool {
	if c == nil || c.closed.Load() {
		return false
	}
	select {
	case <-c.waitDone:
		return false
	default:
		return true
	}
}

// StderrTail 返回外部进程 stderr 的尾部（诊断用，有界）。
func (c *Client) StderrTail() string {
	if c == nil || c.stderr == nil {
		return ""
	}
	return c.stderr.String()
}

// Call 发起一次调用。out 为 nil 时只等待成功。
//
// ctx 超时 → ErrTimeout；ctx 取消 → ErrCanceled；两者都**只影响这次调用**，
// 进程继续活着（已经发出的请求如果之后回来了，会被丢弃）。
func (c *Client) Call(ctx context.Context, method string, params any, out any) error {
	if c == nil || c.closed.Load() {
		return ErrNotRunning
	}
	if !c.Alive() {
		return fmt.Errorf("%w（已退出：%v）", ErrNotRunning, c.waitErr)
	}

	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return &ParamError{Method: method, Err: err}
		}
		raw = b
	}

	id := c.nextID.Add(1)
	ch := make(chan *response, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	frame, err := json.Marshal(request{JSONRPC: "2.0", ID: id, Method: method, Params: raw})
	if err != nil {
		return &ParamError{Method: method, Err: err}
	}
	c.writeMu.Lock()
	_, werr := c.stdin.Write(append(frame, '\n'))
	c.writeMu.Unlock()
	if werr != nil {
		if c.closed.Load() || !c.Alive() {
			return fmt.Errorf("%w（写管道失败：%v）", ErrNotRunning, werr)
		}
		return fmt.Errorf("cordis/extern: 写请求失败: %w", werr)
	}

	select {
	case resp := <-ch:
		if resp.Error != nil {
			return &RPCError{
				Method:  method,
				Code:    resp.Error.Code,
				Message: resp.Error.Message,
				Data:    resp.Error.Data,
			}
		}
		if out != nil && len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, out); err != nil {
				return fmt.Errorf("cordis/extern: 解析 %s 的结果失败: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("%w: %s", ErrTimeout, method)
		}
		return fmt.Errorf("%w: %s", ErrCanceled, method)
	case <-c.waitDone:
		return fmt.Errorf("%w（进程退出：%v）", ErrNotRunning, c.waitErr)
	}
}

// Close 关 stdin → 杀整棵进程树 → 等退出 → 清临时目录。可重入。
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	if c.closed.Swap(true) {
		return nil
	}
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.tree != nil {
		_ = c.tree.Stop()
	}
	select {
	case <-c.waitDone:
	case <-time.After(5 * time.Second):
		if c.cmd != nil && c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		select {
		case <-c.waitDone:
		case <-time.After(5 * time.Second):
		}
	}
	if c.tree != nil {
		_ = c.tree.Close()
	}
	if c.dir != "" {
		_ = os.RemoveAll(c.dir)
	}
	return nil
}

func (c *Client) readLoop(stdout io.Reader, maxMsg int) {
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), maxMsg)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var resp response
		if err := json.Unmarshal(line, &resp); err != nil {
			continue // 非协议行一律丢（stderr 才是日志）
		}
		c.mu.Lock()
		ch := c.pending[resp.ID]
		c.mu.Unlock()
		if ch != nil {
			select {
			case ch <- &resp:
			default:
			}
		}
	}
}

// ringBuffer 是 stderr 的有界尾部缓冲。
type ringBuffer struct {
	mu    sync.Mutex
	buf   []byte
	limit int
}

func newRingBuffer(limit int) *ringBuffer {
	if limit <= 0 {
		limit = defaultStderrBytes
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
