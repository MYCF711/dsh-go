// Package bash 是执行层的默认实现：真实 bash 进程（R6），进程内解释器一律不用。
//
// 设计取舍：
//   - **持久会话**：一个 bash 进程常驻，cwd 跨命令保留；
//   - **不持久环境变量**：`export` 不会留到下一次调用（每条命令在子 shell 里展开）；
//   - **自动转后台**：一条命令空闲超过预算，就把整个会话交给后台任务，下一次调用换新会话。
package bash

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"dshgo/internal/spi"
)

// markerRe 匹配命令结束哨兵：\x01DSHD:<seq>:<rc>\x01<cwd>\x01\n
var markerRe = regexp.MustCompile("\x01DSHD:(\\d+):(-?\\d+)\x01([^\x01]*)\x01\n")

// findBash 找 bash：环境变量优先，其次 PATH，最后 Git for Windows 的常见位置。
func findBash() (string, error) {
	if p := os.Getenv("DSHD_BASH"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	if p, err := exec.LookPath("bash"); err == nil {
		return p, nil
	}
	for _, p := range []string{
		`C:\Program Files\Git\bin\bash.exe`,
		`C:\Program Files\Git\usr\bin\bash.exe`,
	} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("找不到 bash：装 Git for Windows，或把 bash 加进 PATH，或设 DSHD_BASH")
}

// session 是一个常驻 bash 进程。同一时刻只跑一条命令。
type session struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	out   io.ReadCloser

	mu   sync.Mutex
	task *task
	cwd  string
	dead bool
	seq  int
}

func startSession(cwd string) (*session, error) {
	bash, err := findBash()
	if err != nil {
		return nil, err
	}
	c := exec.Command(bash, "--noprofile", "--norc", "-s")
	c.Dir = cwd
	stdin, err := c.StdinPipe()
	if err != nil {
		return nil, err
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	c.Stdout = pw
	c.Stderr = pw
	if err := c.Start(); err != nil {
		pr.Close()
		pw.Close()
		return nil, fmt.Errorf("启动 bash 失败: %w", err)
	}
	pw.Close() // 父进程不留写端，否则读不到 EOF
	s := &session{cmd: c, stdin: stdin, out: pr, cwd: cwd}
	go s.readLoop()
	return s, nil
}

func (s *session) readLoop() {
	br := bufio.NewReaderSize(s.out, 32*1024)
	buf := make([]byte, 8192)
	for {
		n, err := br.Read(buf)
		if n > 0 {
			s.mu.Lock()
			t := s.task
			s.mu.Unlock()
			if t != nil {
				t.append(buf[:n])
				t.scan()
			}
		}
		if err != nil {
			s.mu.Lock()
			t := s.task
			s.dead = true
			s.mu.Unlock()
			if t != nil {
				t.finish(-1, "")
			}
			return
		}
	}
}

func (s *session) isDead() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dead
}

func (s *session) busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.task != nil && !s.task.finished()
}

func (s *session) close() {
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	if s.stdin != nil {
		_ = s.stdin.Close()
	}
	if s.out != nil {
		_ = s.out.Close()
	}
}

// run 写一条命令并等它结束（或空闲超时）。
func (s *session) run(cmd string, idle time.Duration) (spi.ExecResult, *task, error) {
	s.mu.Lock()
	if s.dead {
		s.mu.Unlock()
		return spi.ExecResult{}, nil, fmt.Errorf("bash 会话已退出，需要新会话")
	}
	s.seq++
	t := &task{seq: s.seq, sig: make(chan struct{}, 1)}
	s.task = t
	s.mu.Unlock()

	script := fmt.Sprintf(
		"{ %s\n} 2>&1; __dshd_rc=$?\nprintf '\\001DSHD:%d:%%d\\001%%s\\001\\n' \"$__dshd_rc\" \"$PWD\"\n",
		cmd, t.seq)
	if _, err := io.WriteString(s.stdin, script); err != nil {
		return spi.ExecResult{}, nil, fmt.Errorf("写命令到 bash 失败: %w", err)
	}

	timer := time.NewTimer(idle)
	defer timer.Stop()
	for {
		if t.finished() {
			s.mu.Lock()
			if s.task == t {
				s.task = nil
			}
			s.mu.Unlock()
			rc, cwd, _ := t.result()
			if cwd != "" {
				s.cwd = cwd
			}
			if rc == -1 && s.isDead() {
				return spi.ExecResult{Output: t.text(), ExitCode: -1, Cwd: cwd}, t,
					fmt.Errorf("命令结束了 bash 会话（比如 exit）：已丢弃该会话，下次调用会自动重开")
			}
			return spi.ExecResult{Output: t.text(), ExitCode: rc, Cwd: cwd}, t, nil
		}
		select {
		case <-t.sig:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(idle)
		case <-timer.C:
			// 空闲超时：会话交给后台任务，调用方负责换新会话。
			return spi.ExecResult{Output: t.text(), MovedToBg: true}, t, nil
		}
	}
}

// task 是一条命令的累积输出与结束状态。
type task struct {
	seq int

	mu   sync.Mutex
	buf  bytes.Buffer
	done bool
	rc   int
	cwd  string
	sig  chan struct{}
}

func (t *task) append(b []byte) {
	t.mu.Lock()
	if !t.done {
		t.buf.Write(b)
	}
	t.mu.Unlock()
	t.ping()
}

// scan 在累积缓冲里找哨兵；找到就把输出截断并标记完成。
func (t *task) scan() {
	t.mu.Lock()
	s := t.buf.String()
	if !strings.Contains(s, "\x01DSHD:") {
		t.mu.Unlock()
		return
	}
	loc := markerRe.FindStringSubmatchIndex(s)
	if loc == nil {
		t.mu.Unlock()
		return
	}
	// FindStringSubmatchIndex 的布局：[整体0,整体1, 组1起,组1止, 组2起,组2止, 组3起,组3止]
	// 组1 = seq，组2 = 退出码，组3 = PWD。
	rc, _ := strconv.Atoi(s[loc[4]:loc[5]])
	cwd := s[loc[6]:loc[7]]
	before := s[:loc[0]]
	t.buf.Reset()
	t.buf.WriteString(before)
	if !t.done {
		t.done = true
		t.rc = rc
		t.cwd = cwd
	}
	t.mu.Unlock()
	t.ping()
}

func (t *task) finish(rc int, cwd string) {
	t.mu.Lock()
	if !t.done {
		t.done = true
		t.rc = rc
		t.cwd = cwd
	}
	t.mu.Unlock()
	t.ping()
}

func (t *task) ping() {
	select {
	case t.sig <- struct{}{}:
	default:
	}
}

func (t *task) finished() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.done
}

func (t *task) text() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimRight(t.buf.String(), "\n")
}

func (t *task) result() (int, string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.rc, t.cwd, t.done
}
