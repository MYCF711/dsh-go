package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"dshgo/internal/mcp"
)

const (
	// mcpMaxLineBytes 是单条 JSON-RPC 消息的上限。超了就是协议被破坏：
	// 一直读到把内存耗尽，比响亮失败糟得多。
	mcpMaxLineBytes = 4 << 20
	// mcpCloseWait 是关连接时等子进程退出的上限。
	mcpCloseWait = 5 * time.Second
	// mcpStderrBytes 是 stderr 环形缓冲的上限（只留最后一段，用来解释崩溃原因）。
	mcpStderrBytes = 8192
)

// mcpClient 是一个 MCP server 子进程上的 stdio JSON-RPC 连接。
//
// 两条硬约束，缺一条这个客户端就不成立：
//
//  1. **并发安全**：工具调用是多 goroutine 并发的，而 stdio 只有一个读端。
//     所以有且只有一个读泵，按 id 把响应派发给对应的等待者。谁自己再读一次
//     流，谁就会把别人的响应吃掉——那是"工具偶发返回别人的结果"的根因。
//  2. **可取消**：读泵不认 ctx，等待者认。ctx 到期必须立刻返回，不能等一个
//     永远不会来的响应；否则"超时"这件事只存在于调用方的想象里，进程会一直挂着。
type mcpClient struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *os.File
	stderr *mcpCappedBuffer
	tree   *mcpProcessTree

	writeMu sync.Mutex

	mu       sync.Mutex
	pending  map[int64]chan mcpResponse
	nextID   int64
	closed   bool
	readErr  error
	closeOne sync.Once
	// closeResult 记住第一次关闭的判定，重复调用不返回零值（零值看起来像
	// "关闭未确认"，会让重启被永久拒绝）。
	closeResult mcp.CloseResult

	// done 在读泵退出时关闭；processDone 在 cmd.Wait 返回时关闭。
	// 两个分开是因为"流断了"和"进程收了"之间可能差一小段，而 Close 要等的是后者。
	done        chan struct{}
	processDone chan struct{}
	waitErr     error
	waitMu      sync.Mutex
}

// mcpResponse 是一条 JSON-RPC 响应（也用来接服务端主动发的通知）。
type mcpResponse struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *mcpRPCError    `json:"error"`
}

// mcpRPCError 是 JSON-RPC 的 error 成员。
type mcpRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *mcpRPCError) Error() string {
	return fmt.Sprintf("MCP 错误 %d: %s", e.Code, e.Message)
}

// startMCPClient 起子进程并建好连接。
//
// 用**自己的 os.Pipe** 而不是 cmd.StdoutPipe()：后者的文档明确写着"在读完之前
// 调用 Wait 是不对的"，而这里恰好是"Wait 在后台跑、读泵在别处读"。
// 用自己的管道时 Wait 不碰读端，那条竞态从根上不存在。
func startMCPClient(config mcpServerConfig) (*mcpClient, error) {
	cmd := exec.Command(config.Command, config.Args...)
	cmd.Dir = config.Cwd
	cmd.Env = mcpChildEnv(config.Env)

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("MCP 建 stdin 管道: %w", err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		return nil, fmt.Errorf("MCP 建 stdout 管道: %w", err)
	}
	stderr := &mcpCappedBuffer{limit: mcpStderrBytes}
	cmd.Stdin = stdinR
	cmd.Stdout = stdoutW
	cmd.Stderr = stderr
	startErr := cmd.Start()
	// 三个句柄在**父进程**这一侧都要关：子进程已经拿到自己的副本。
	_ = stdinR.Close()
	_ = stdoutW.Close()
	if startErr != nil {
		_ = stdinW.Close()
		_ = stdoutR.Close()
		return nil, startErr
	}
	tree, err := attachMCPProcessTree(cmd)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = cmd.Wait()
		return nil, err
	}
	c := &mcpClient{
		cmd:         cmd,
		stdin:       stdinW,
		stdout:      stdoutR,
		stderr:      stderr,
		tree:        tree,
		pending:     make(map[int64]chan mcpResponse),
		done:        make(chan struct{}),
		processDone: make(chan struct{}),
	}
	go func() {
		waitErr := cmd.Wait()
		c.waitMu.Lock()
		c.waitErr = waitErr
		c.waitMu.Unlock()
		close(c.processDone)
	}()
	go c.pump(bufio.NewReaderSize(stdoutR, 64*1024))
	return c, nil
}

// WaitError 返回子进程退出的原因（还没退出就是 nil）。
func (c *mcpClient) WaitError() error {
	c.waitMu.Lock()
	defer c.waitMu.Unlock()
	return c.waitErr
}

// Stderr 返回子进程 stderr 的尾部；报错时贴给人看，比"连接断了"有用得多。
func (c *mcpClient) Stderr() string {
	if c.stderr == nil {
		return ""
	}
	return c.stderr.String()
}

// pump 是**唯一**的读端。
func (c *mcpClient) pump(reader *bufio.Reader) {
	defer close(c.done)
	for {
		line, err := readMCPLine(reader, mcpMaxLineBytes)
		if err != nil {
			c.fail(err)
			return
		}
		if len(line) == 0 {
			continue
		}
		var msg mcpResponse
		if err := json.Unmarshal(line, &msg); err != nil {
			// 不是 JSON 就是协议被破坏。继续读下去只会把一个坏连接当好连接用。
			c.fail(fmt.Errorf("MCP stdout 出现非 JSON 行（%d 字节）: %w", len(line), err))
			return
		}
		if len(msg.ID) == 0 {
			// 服务端主动发的通知（notifications/tools/list_changed 等）。
			// 本仓不在读泵里做重同步：重同步要拿管理器锁，而读泵是全局单点。
			continue
		}
		var id int64
		if json.Unmarshal(msg.ID, &id) != nil {
			continue // 非本仓发出的 id 形状：不是给我们的
		}
		c.mu.Lock()
		ch, ok := c.pending[id]
		if ok {
			delete(c.pending, id)
		}
		c.mu.Unlock()
		if !ok {
			continue // 迟到的响应（等待者已经超时走了）：丢掉，不能让读泵卡住
		}
		ch <- msg
	}
}

// fail 记录读端失败原因，并把所有等待者从阻塞里放出来。
func (c *mcpClient) fail(err error) {
	c.mu.Lock()
	if c.readErr == nil {
		c.readErr = err
	}
	readErr := c.readErr
	pending := c.pending
	c.pending = make(map[int64]chan mcpResponse)
	c.mu.Unlock()
	for _, ch := range pending {
		select {
		case ch <- mcpResponse{Error: &mcpRPCError{Code: -32099, Message: "MCP 连接已断开: " + readErr.Error()}}:
		default:
		}
	}
}

// call 发一条请求并等它的响应。
func (c *mcpClient) call(ctx context.Context, method string, params any, out any) error {
	// 先看 ctx：已经取消的调用不该占一个 id，也不该往子进程写东西。
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("MCP %s: %w", method, err)
	}
	c.mu.Lock()
	if c.closed {
		err := c.readErr
		c.mu.Unlock()
		if err == nil {
			err = errors.New("连接已关闭")
		}
		return fmt.Errorf("MCP %s: %w", method, err)
	}
	c.nextID++
	id := c.nextID
	ch := make(chan mcpResponse, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return fmt.Errorf("MCP %s 请求序列化失败: %w", method, err)
	}
	c.writeMu.Lock()
	_, err = c.stdin.Write(append(raw, '\n'))
	c.writeMu.Unlock()
	if err != nil {
		return fmt.Errorf("MCP %s 写失败: %w", method, err)
	}

	select {
	case resp := <-ch:
		return decodeMCPResponse(method, resp, out)
	case <-ctx.Done():
		return fmt.Errorf("MCP %s 等待响应超时: %w", method, ctx.Err())
	case <-c.done:
		// 读泵退出和响应可能同时到：先非阻塞取一次，别把已经拿到的结果丢掉。
		select {
		case resp := <-ch:
			return decodeMCPResponse(method, resp, out)
		default:
		}
		c.mu.Lock()
		readErr := c.readErr
		c.mu.Unlock()
		if readErr == nil {
			readErr = errors.New("连接已关闭")
		}
		return fmt.Errorf("MCP %s: %w", method, readErr)
	}
}

func decodeMCPResponse(method string, resp mcpResponse, out any) error {
	if resp.Error != nil {
		return resp.Error
	}
	if len(resp.Result) == 0 {
		return fmt.Errorf("MCP %s 缺 result", method)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(resp.Result, out); err != nil {
		return fmt.Errorf("MCP %s 的 result 不是预期形状: %w", method, err)
	}
	return nil
}

// notify 发一条通知（无 id，无响应）。
func (c *mcpClient) notify(method string, params any) error {
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.stdin.Write(append(raw, '\n'))
	return err
}

// Close 收掉整棵进程树并等到 Wait 返回。幂等。
func (c *mcpClient) Close() mcp.CloseResult {
	return c.CloseWithBudget(mcpCloseWait, nil)
}

// CloseWithBudget 是有界、可判定的关闭：收掉整棵进程树，并在预算内确认它真的
// 退出了。
//
// 为什么需要"可判定"：官方把传输关闭（closeConfirmed）与本地清理（cleanupError）
// 分开报告，而 `mcpManager.probe`/`restart` 正是按这两位数决定"能不能替换"。
// 一个只返回 void 的 Close 让所有调用方都只能猜"大概关掉了吧"——猜错的代价是
// 两个实例抢同一个工具命名空间。
//
// 顺序与 Close 相同：先关 stdin（让守规矩的 server 自己退），再
// TerminateJobObject（Windows 上一次收掉全部后代），最后 Process.Kill 兜底。
// 只 Kill 父进程的话，孙进程会变成孤儿继续持有端口/文件锁。
//
// budget <= 0 时用默认 mcpCloseWait；ctx 只影响等待（提前取消 = 不确认关闭）。
func (c *mcpClient) CloseWithBudget(budget time.Duration, ctx context.Context) mcp.CloseResult {
	if budget <= 0 {
		budget = mcpCloseWait
	}
	var result mcp.CloseResult
	c.closeOne.Do(func() {
		var cleanupErr error
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()

		// stdin 已随子进程退出而断掉时这里是 EPIPE：那不是清理失败。
		if err := c.stdin.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			cleanupErr = fmt.Errorf("关 MCP stdin: %w", err)
		}
		if c.tree != nil {
			if err := c.tree.Stop(); err != nil {
				cleanupErr = fmt.Errorf("收 MCP 进程树: %w", err)
			}
		}
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill() // 只杀本 client 启动并登记的 PID
		}
		timer := time.NewTimer(budget)
		defer timer.Stop()
		var canceled <-chan struct{}
		if ctx != nil {
			canceled = ctx.Done()
		}
		select {
		case <-c.processDone:
			result.CloseConfirmed = true
		case <-canceled:
			result.CloseConfirmed = false
			if cleanupErr == nil {
				cleanupErr = errors.New("关 MCP 连接被取消：子进程是否已退出未知")
			}
		case <-timer.C:
			result.CloseConfirmed = false
			if cleanupErr == nil {
				cleanupErr = fmt.Errorf("MCP 子进程未在 %s 内退出", budget)
			}
		}
		if c.tree != nil {
			if err := c.tree.Close(); err != nil && cleanupErr == nil {
				cleanupErr = fmt.Errorf("释放 MCP Job Object: %w", err)
			}
		}
		_ = c.stdout.Close()
		if cleanupErr != nil {
			result.CleanupError = cleanupErr.Error()
		}
		c.mu.Lock()
		c.closeResult = result
		c.mu.Unlock()
	})
	c.mu.Lock()
	stored := c.closeResult
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return stored
	}
	return result
}

// readMCPLine 按行读，但把"单行超限"和"读错误"分开报。
func readMCPLine(reader *bufio.Reader, limit int) ([]byte, error) {
	var out []byte
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(out)+len(fragment) > limit {
			return nil, fmt.Errorf("MCP stdio 消息超过 %d 字节", limit)
		}
		out = append(out, fragment...)
		if err == nil {
			return bytes.TrimSpace(out), nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return nil, err
		}
	}
}

// mcpChildEnv 造子进程环境。
//
// 官方 @deepseek-ai/dsh-mcp-client 的做法：父环境先清洗掉形状像凭据的变量
// （KEY|PASSWORD|SECRET|TOKEN）与本仓自己的 DSH_*，再合上配置里显式写的 env，
// 所以**显式覆盖永远生效**。MCP server 是第三方代码，没有任何理由让它读到
// 本进程的全部凭据。
func mcpChildEnv(extra map[string]string) []string {
	parent := os.Environ()
	out := make([]string, 0, len(parent)+len(extra))
	for _, kv := range parent {
		name := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			name = kv[:i]
		}
		if isMCPCredentialShaped(name) {
			continue
		}
		out = append(out, kv)
	}
	for key, value := range extra {
		out = append(out, key+"="+value)
	}
	return out
}

func isMCPCredentialShaped(name string) bool {
	upper := strings.ToUpper(name)
	if strings.HasPrefix(upper, "DSHD_") || strings.HasPrefix(upper, "DSH_") {
		return true
	}
	for _, needle := range []string{"KEY", "PASSWORD", "SECRET", "TOKEN"} {
		if strings.Contains(upper, needle) {
			return true
		}
	}
	return false
}

// mcpCappedBuffer 只保留最后 limit 字节：崩溃原因通常就在最后那几行。
type mcpCappedBuffer struct {
	limit int
	mu    sync.Mutex
	buf   []byte
}

func (b *mcpCappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if b.limit > 0 && len(b.buf) > b.limit {
		b.buf = append([]byte(nil), b.buf[len(b.buf)-b.limit:]...)
	}
	return len(p), nil
}

func (b *mcpCappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(string(b.buf))
}
