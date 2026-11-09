package terminal

import (
	"fmt"
	"sync"
	"time"
)

// closeGrace 是 Close 等读泵收工的兜底上限：
// 进程树已被终止，读端应该立刻出错；等不到也不能把调用方挂死。
const closeGrace = 5 * time.Second

// Config 是起一条 PTY 会话的全部参数。
type Config struct {
	// Path 是 shell 可执行文件（绝对路径；调用方负责解析）。
	Path string
	// Args 是传给 shell 的参数（交互性来自 tty 本身，不需要 -i）。
	Args []string
	// Cwd 是初始工作目录（空 = 继承当前进程）。
	Cwd string
	// Cols/Rows 是初始窗口尺寸（必须为正）。
	Cols int
	Rows int
	// ScrollbackRows 是保留多少行滚动缓冲（官方 environment.scrollback 语义）；
	// 0 = 默认 1000 行，负数 = 非法。
	ScrollbackRows int
	// ScrollbackBytes 是内存安全硬顶（字节）；0 = 默认 1 MiB，负数 = 非法。
	ScrollbackBytes int
}

// platformProcess 是平台相关的 PTY 句柄。
//
// 只有两个平台实现：真 ConPTY（Windows）与"没有实现"（其它）。
type platformProcess interface {
	// Read 从伪终端输出管道读（阻塞）。
	Read(p []byte) (int, error)
	// Write 写进伪终端输入管道（阻塞）。
	Write(p []byte) (int, error)
	// Resize 改子进程看到的窗口尺寸。
	Resize(cols, rows int) error
	// PID 是子进程（shell 自己）的 PID。
	PID() int
	// Kill 终止整棵进程树（Job Object）。
	Kill() error
	// Wait 阻塞到子进程退出并返回真实退出码。
	Wait() (int, error)
	// Close 回收句柄与伪控制台（幂等）。
	Close() error
}

// Session 是一条活着的 PTY 会话。
//
// 并发契约：Input/Output/Resize 可并发调用；Close 幂等。
type Session struct {
	cfg  Config
	sb   *Scrollback
	proc platformProcess

	done      chan struct{}
	closeOnce sync.Once
	closeErr  error

	mu       sync.Mutex
	closed   bool
	exited   bool
	exitCode int
	waitErr  error
}

// Open 起一条真 PTY 会话（Windows = ConPTY；其它平台返回 ErrUnsupported）。
func Open(cfg Config) (*Session, error) {
	if cfg.Path == "" {
		return nil, fmt.Errorf("%w: shell 路径为空", ErrStart)
	}
	if cfg.Cols <= 0 || cfg.Rows <= 0 {
		return nil, fmt.Errorf("%w: 行列必须为正（拿到 %dx%d）", ErrBadSize, cfg.Cols, cfg.Rows)
	}
	if cfg.ScrollbackRows < 0 {
		return nil, fmt.Errorf("%w: 滚动缓冲行数不能为负（拿到 %d）", ErrBadSize, cfg.ScrollbackRows)
	}
	if cfg.ScrollbackRows == 0 {
		cfg.ScrollbackRows = defaultScrollbackRows
	}
	if cfg.ScrollbackBytes < 0 {
		return nil, fmt.Errorf("%w: 滚动缓冲字节上限不能为负（拿到 %d）", ErrBadSize, cfg.ScrollbackBytes)
	}
	if cfg.ScrollbackBytes == 0 {
		cfg.ScrollbackBytes = defaultScrollbackBytes
	}
	proc, err := openPlatform(cfg)
	if err != nil {
		return nil, err
	}
	s := &Session{
		cfg:  cfg,
		sb:   NewScrollback(cfg.ScrollbackRows, cfg.ScrollbackBytes),
		proc: proc,
		done: make(chan struct{}),
	}
	// 两个循环各管一件事，**不互相等**：
	//   - waitLoop 等真实退出码（进程一退就关 done，不等输出管道 EOF）；
	//   - pump 一直排空输出，直到管道 EOF 或被 Close 打断。
	// 合成一个循环会让"进程已退出但 conhost 还挂着管道"变成永远等不到退出。
	go s.waitLoop()
	go s.pump()
	return s, nil
}

// waitLoop 等子进程退出并记下真实退出码。
func (s *Session) waitLoop() {
	code, waitErr := s.proc.Wait()
	s.mu.Lock()
	s.exited = true
	s.exitCode = code
	s.waitErr = waitErr
	s.mu.Unlock()
	close(s.done)
}

// pump 是唯一的读循环：把伪终端输出搬进有界滚动缓冲，直到 EOF。
func (s *Session) pump() {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.proc.Read(buf)
		if n > 0 {
			s.sb.Write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// Input 把一段**原样**字节写进终端（调用方负责换行/控制序列）。
func (s *Session) Input(data string) error {
	if data == "" {
		return nil
	}
	s.mu.Lock()
	closed, exited := s.closed, s.exited
	s.mu.Unlock()
	if closed {
		return ErrClosed
	}
	if exited {
		return ErrExited
	}
	if _, err := s.proc.Write([]byte(data)); err != nil {
		return fmt.Errorf("写终端失败: %w", err)
	}
	return nil
}

// Write 让 Session 满足 io.Writer（Input 的字节形态）。
func (s *Session) Write(p []byte) (int, error) {
	if err := s.Input(string(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Output 返回从 since 起的增量输出、下一个游标，以及会话是否已经退出。
//
// 游标是**绝对字节偏移**；被淘汰的字节不会伪装成连续数据（见 Scrollback）。
func (s *Session) Output(since int64) (data []byte, next int64, dead bool) {
	data, next, _ = s.sb.Since(since)
	return data, next, s.Exited()
}

// Tail 返回屏幕尾部快照（官方 attach 的 screen）与当前总游标。
func (s *Session) Tail(maxBytes int) (string, int64) { return s.sb.Tail(maxBytes) }

// Resize 改子进程窗口尺寸（真的走 ResizePseudoConsole）。
func (s *Session) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("%w: 行列必须为正（拿到 %dx%d）", ErrBadSize, cols, rows)
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return ErrClosed
	}
	if err := s.proc.Resize(cols, rows); err != nil {
		return fmt.Errorf("resize 失败: %w", err)
	}
	s.mu.Lock()
	s.cfg.Cols, s.cfg.Rows = cols, rows
	s.mu.Unlock()
	return nil
}

// Size 返回当前窗口尺寸。
func (s *Session) Size() (cols, rows int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Cols, s.cfg.Rows
}

// PID 是 shell 子进程的 PID（0 = 不知道）。
func (s *Session) PID() int {
	if s == nil || s.proc == nil {
		return 0
	}
	return s.proc.PID()
}

// Kill 终止整棵进程树（Job Object），幂等。
func (s *Session) Kill() error {
	if s == nil || s.proc == nil {
		return nil
	}
	return s.proc.Kill()
}

// Close 回收会话：终止进程树、关伪控制台、等读泵收工。幂等。
func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		s.closeErr = s.proc.Close()
		select {
		case <-s.done:
		case <-time.After(closeGrace):
		}
	})
	return s.closeErr
}

// Done 在子进程退出（或会话被关闭）后关闭。
func (s *Session) Done() <-chan struct{} {
	if s == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return s.done
}

// Exited 报告子进程是否已经退出。
func (s *Session) Exited() bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exited
}

// ExitCode 返回真实退出码；ok=false 表示进程还没退出（不是"退出码 0"）。
func (s *Session) ExitCode() (code int, ok bool) {
	if s == nil {
		return 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exitCode, s.exited
}

// WaitError 是等待进程退出时的错误（只有系统调用失败才有）。
func (s *Session) WaitError() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.waitErr
}

// Total 是累计写出的总字节数（绝对游标）。
func (s *Session) Total() int64 { return s.sb.Total() }

// Earliest 是仍保留的最旧字节偏移。
func (s *Session) Earliest() int64 { return s.sb.Earliest() }

// Scrollback 返回底层有界缓冲（只读用途）。
func (s *Session) Scrollback() *Scrollback {
	if s == nil {
		return nil
	}
	return s.sb
}
