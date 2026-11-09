package bash

// PTY 终端：**真伪控制台**承载的持久 shell 会话。
//
// # 与管道式终端的关系
//
// `OpenTerminal`（terminal.go）起的是 `bash --noprofile --norc -s` + 三条管道：
// 它能跑命令，但**不是终端**——没有窗口尺寸、没有屏幕模型、没有滚动缓冲。
// `OpenPTY` 起的是 ConPTY 会话（internal/terminal）：
//
//	能力            管道式            PTY（本文件）
//	窗口尺寸        无                ResizePseudoConsole，真的生效
//	输出            原始字节尾部      真屏幕字节流（含 ANSI）
//	resize          无                Terminal.Resize
//	退出码          自报哨兵          真实进程退出码
//	滚动缓冲        无（报 0）        Scrollback（行数 + 字节双上限）
//
// 两条路径**互不替代**：管道式是既有 `terminal.open` 的实现，必须不回归；
// PTY 是官方 `terminal.*` 的目标后端，由 cmd/dshd 的 PTY 适配层消费。
//
// # 归属
//
// 进程树归属（Windows Job Object / Unix 进程组）仍由 process*.go 的 processControl
// 负责：`attachPTYProcessTree` 把 PTY 会话包成 processTreeNative，
// 于是 `Service.Close` / `CloseTerminal` 的收工路径对两种会话是同一套。

import (
	"fmt"

	"dshgo/internal/spi"
	"dshgo/internal/terminal"
)

// PTYOptions 是起一条真 PTY 终端会话的参数。
type PTYOptions struct {
	// Cwd 是初始工作目录（空 = Service 当前目录）。
	Cwd string
	// Path 是 shell 可执行文件（空 = findBash()）。
	Path string
	// Args 是传给 shell 的参数（nil = 默认 `--noprofile --norc`；
	// 交互性来自 tty 本身，不需要 -i）。
	Args []string
	// Cols/Rows 是初始窗口尺寸（必须为正）。
	Cols int
	Rows int
	// ScrollbackRows 是保留行数（0 = 1000，官方 environment.scrollback 语义）。
	ScrollbackRows int
	// ScrollbackBytes 是内存安全硬顶（0 = 1 MiB）。
	ScrollbackBytes int
}

// defaultPTYArgs 让 shell 不读 profile/rc：
// 宿主是长驻进程，用户的 rc 里可能有交互式提示、别名甚至 `exit`，
// 那会让"终端能不能开"取决于用户家目录内容。要定制 shell 就显式传 Args。
var defaultPTYArgs = []string{"--noprofile", "--norc"}

// OpenPTY 起一条真伪控制台会话，并按既有 Service 的终端表登记成一条 Terminal。
//
// 失败就是失败：起不来返回错误，**不**退化成管道实现。
func (s *Service) OpenPTY(opts PTYOptions) (*Terminal, error) {
	s.mu.Lock()
	base := s.cwd
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return nil, spi.ErrServiceClosed
	}

	cwd := opts.Cwd
	if cwd == "" {
		cwd = base
	}
	path := opts.Path
	if path == "" {
		found, err := findBash()
		if err != nil {
			return nil, err
		}
		path = found
	}
	args := opts.Args
	if args == nil {
		args = defaultPTYArgs
	}

	ptySession, err := terminal.Open(terminal.Config{
		Path:            path,
		Args:            args,
		Cwd:             cwd,
		Cols:            opts.Cols,
		Rows:            opts.Rows,
		ScrollbackRows:  opts.ScrollbackRows,
		ScrollbackBytes: opts.ScrollbackBytes,
	})
	if err != nil {
		return nil, err
	}

	control, err := attachPTYProcessTree(ptySession)
	if err != nil {
		_ = ptySession.Close()
		return nil, err
	}

	// 复用 session 的归属字段：control 负责杀树，stdin 就是 PTY 输入管道。
	// 输出**不**走 readLoop/sink（那会进无界 buf），由 terminal.Session 自己的
	// 读泵写进有界 scrollback —— 见 Terminal.PTY()。
	holder := &session{cwd: cwd, control: control, pty: ptySession, stdin: ptySession}
	t := &Terminal{id: fmt.Sprintf("pty-%d", s.nextTermID()), sess: holder}

	// 进程退出后把 dead 标出来（Terminal.Output 的兼容语义要用它）。
	go func() {
		<-ptySession.Done()
		t.mu.Lock()
		t.dead = true
		t.mu.Unlock()
	}()

	s.mu.Lock()
	if s.terms == nil {
		s.terms = map[string]*Terminal{}
	}
	s.terms[t.id] = t
	s.mu.Unlock()
	return t, nil
}

// OpenPTYTerminal 是 OpenPTY 的常用形态：默认 shell + 指定尺寸。
func (s *Service) OpenPTYTerminal(cwd string, cols, rows int) (*Terminal, error) {
	return s.OpenPTY(PTYOptions{Cwd: cwd, Cols: cols, Rows: rows})
}

// Resize 改 PTY 终端的窗口尺寸（真的作用在子进程上）。
func (t *Terminal) Resize(cols, rows int) error {
	t.mu.Lock()
	sess := t.sess
	dead := t.dead
	t.mu.Unlock()
	if dead || sess == nil || sess.pty == nil {
		return fmt.Errorf("终端已经关了")
	}
	return sess.pty.Resize(cols, rows)
}

// PTY 返回底层真 PTY 会话；管道式终端返回 nil。
//
// 调用方（cmd/dshd 的 PTY 适配层）用它取真实输出游标、滚动缓冲与退出码。
func (t *Terminal) PTY() *terminal.Session {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.sess == nil {
		return nil
	}
	return t.sess.pty
}

// Scrollback 返回有界滚动缓冲；管道式终端返回 nil（它没有滚动缓冲）。
func (t *Terminal) Scrollback() *terminal.Scrollback {
	if pty := t.PTY(); pty != nil {
		return pty.Scrollback()
	}
	return nil
}
