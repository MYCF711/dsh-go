package terminal_test

// 真 PTY 核心的**行为**验证：不用管道 shell 冒充。
//
// 每条测试都在问同一个问题：这个能力是**真的**吗？
//   - 输入真的到了子进程的 stdin（回显的是命令执行结果，不是把字符串存进 buffer）；
//   - resize 真的改了子进程看到的窗口尺寸（用 `stty size` 从子进程读回来）；
//   - 退出码是真的进程退出码（不是编的 0）；
//   - 滚动缓冲真的**有界**且会淘汰旧字节（earliest>0 且 lost=true）。
//
// 纪律：只用 t.TempDir()；只关本测试自己起的会话；长命令加超时。

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"dshgo/internal/terminal"
)

// testBash 找本机 bash，优先级与 modules/exec/bash 的 findBash 一致。
func testBash(t *testing.T) string {
	t.Helper()
	candidates := []string{}
	if p := os.Getenv("DSHD_BASH"); p != "" {
		candidates = append(candidates, p)
	}
	if p, err := exec.LookPath("bash"); err == nil {
		candidates = append(candidates, p)
	}
	if runtime.GOOS == "windows" {
		candidates = append(candidates,
			`C:\Program Files\Git\bin\bash.exe`,
			`C:\Program Files\Git\usr\bin\bash.exe`)
	}
	for _, c := range candidates {
		if abs, err := filepath.Abs(c); err == nil {
			if st, err := os.Stat(abs); err == nil && !st.IsDir() {
				return abs
			}
		}
	}
	t.Skip("本机没有 bash：PTY 行为测试需要真实 shell")
	return ""
}

// openTest 起一条真 PTY 会话（交互式 bash：有提示符、有作业控制）。
func openTest(t *testing.T, scrollbackRows int) *terminal.Session {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("ConPTY 只在 Windows 上实现；非 Windows 走官方形状 unimplemented 用例")
	}
	sess, err := terminal.Open(terminal.Config{
		Path:            testBash(t),
		Args:            []string{"--noprofile", "--norc"},
		Cwd:             t.TempDir(),
		Cols:            100,
		Rows:            30,
		ScrollbackRows:  scrollbackRows,
		ScrollbackBytes: 256 * 1024,
	})
	if err != nil {
		t.Fatalf("起 PTY 失败：%v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

// readUntil 从 cursor 起持续读，直到出现 marker 或会话退出；超时即失败。
func readUntil(t *testing.T, sess *terminal.Session, cursor *int64, marker string, timeout time.Duration) string {
	t.Helper()
	var sb strings.Builder
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, next, dead := sess.Output(*cursor)
		if len(data) > 0 {
			sb.Write(data)
			*cursor = next
		}
		if strings.Contains(sb.String(), marker) {
			return sb.String()
		}
		if dead {
			t.Fatalf("会话在等到 %q 之前退出了；已读到 %q", marker, sb.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等不到 %q；已读到 %q", marker, sb.String())
	return ""
}

// TestPTYCreatesRealSessionAndStreamsIO 是 PTY 核心的最小真相：
// 起真进程、输入到它的 stdin、从它的输出管道读回命令执行结果。
func TestPTYCreatesRealSessionAndStreamsIO(t *testing.T) {
	sess := openTest(t, 256*1024)
	if sess.PID() <= 0 {
		t.Fatalf("真 PTY 必须有子进程 PID，拿到 %d", sess.PID())
	}
	marker := fmt.Sprintf("PTY-REAL-%d", time.Now().UnixNano())
	if err := sess.Input("echo " + marker + "\n"); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	var cursor int64
	raw := readUntil(t, sess, &cursor, marker, 20*time.Second)
	// 真实字节证据：ConPTY 的原始输出（含 ANSI 控制序列），不是"把字符串存进 buffer"。
	t.Logf("PTY_RAW_BYTES=%q", raw)
	if sess.Total() < int64(len(marker)) {
		t.Fatalf("sequence 必须是真实已写字节数（>%d），拿到 %d", len(marker), sess.Total())
	}
	if sess.Earliest() != 0 {
		t.Fatalf("没超过上限时最早保留字节应是 0，拿到 %d", sess.Earliest())
	}
}

// TestPTYResizeIsReal 证明 resize 真的作用在子进程上：
// 子进程用 `stty size` 读回来的行列必须跟着变。
func TestPTYResizeIsReal(t *testing.T) {
	sess := openTest(t, 256*1024)
	var cursor int64

	// 用**循环**打印尺寸而不是单发两条命令：ConPTY 在处理 resize 通知期间
	// 可能吞掉紧随其后的输入字节（实测第二条 "stty size" 被吞成 "tty size"），
	// 循环让后续迭代补上，测试才不会偶发假红。
	// 初始 100x30：stty size 输出 "30 100"（行 列）。
	if err := sess.Input("for i in {1..200}; do stty size; sleep 0.05; done\n"); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	readUntil(t, sess, &cursor, "30 100", 20*time.Second)

	if err := sess.Resize(140, 45); err != nil {
		t.Fatalf("resize 失败：%v", err)
	}
	raw := readUntil(t, sess, &cursor, "45 140", 20*time.Second)
	t.Logf("PTY_RESIZE_EVIDENCE=initial\"30 100\" after_resize\"45 140\" raw_len=%d", len(raw))
}

// TestPTYExitCodeIsReal 证明退出码来自真实进程（不是编的 0）。
func TestPTYExitCodeIsReal(t *testing.T) {
	sess := openTest(t, 64*1024)
	if err := sess.Input("exit 7\n"); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	select {
	case <-sess.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("exit 之后 30s 还没退出")
	}
	code, ok := sess.ExitCode()
	if !ok {
		t.Fatal("进程退出了却没有退出码")
	}
	if code != 7 {
		t.Fatalf("退出码必须是真实的 7，拿到 %d", code)
	}
	if _, err := sess.Write([]byte("x")); !errors.Is(err, terminal.ErrExited) && !errors.Is(err, terminal.ErrClosed) {
		t.Fatalf("退出的会话写入必须报 exited/closed，拿到 %v", err)
	}
}

// TestPTYScrollbackIsBounded 证明滚动缓冲有界、淘汰旧字节、且尾部仍可取。
//
// 两道闸分别验：行数（官方 environment.scrollback 语义）与字节硬顶（内存安全）。
func TestPTYScrollbackIsBounded(t *testing.T) {
	t.Run("按行淘汰", func(t *testing.T) {
		const rows = 20
		sess := openTest(t, rows)
		if sess.Scrollback() == nil {
			t.Fatal("PTY 会话必须带滚动缓冲")
		}
		if sess.Scrollback().Limit() != rows {
			t.Fatalf("行数上限必须是配置的 %d，拿到 %d", rows, sess.Scrollback().Limit())
		}
		// 200 行远超 20 行。
		if err := sess.Input("for i in {1..200}; do printf 'LINE-%03d-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\\n' \"$i\"; done\n"); err != nil {
			t.Fatalf("写入失败：%v", err)
		}
		var cursor int64
		readUntil(t, sess, &cursor, "LINE-200", 30*time.Second)

		if sess.Total() <= 0 {
			t.Fatalf("总字节数必须为正，拿到 %d", sess.Total())
		}
		if sess.Earliest() <= 0 {
			t.Fatalf("超过行数上限之后必须淘汰旧字节（earliest>0），拿到 %d", sess.Earliest())
		}
		if got := sess.Scrollback().Rows(); got > rows {
			t.Fatalf("保留行数必须有界：%d > %d", got, rows)
		}
		data, _, lost := sess.Scrollback().Since(0)
		if !lost {
			t.Fatal("游标 0 已经被淘汰，必须如实报 lost=true")
		}
		if !strings.Contains(string(data), "LINE-200") {
			t.Fatalf("尾部必须保留最后一行，实际尾部 %q", tailString(string(data), 120))
		}
	})

	t.Run("字节硬顶", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			t.Skip("ConPTY 只在 Windows 上实现")
		}
		sess, err := terminal.Open(terminal.Config{
			Path:            testBash(t),
			Args:            []string{"--noprofile", "--norc"},
			Cwd:             t.TempDir(),
			Cols:            100,
			Rows:            30,
			ScrollbackRows:  10000, // 行数不设限，先撞字节硬顶
			ScrollbackBytes: 1024,
		})
		if err != nil {
			t.Fatalf("起 PTY 失败：%v", err)
		}
		defer func() { _ = sess.Close() }()

		if err := sess.Input("for i in {1..100}; do printf 'BYTECAP-%03d-yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy\\n' \"$i\"; done\n"); err != nil {
			t.Fatalf("写入失败：%v", err)
		}
		var cursor int64
		readUntil(t, sess, &cursor, "BYTECAP-100", 30*time.Second)
		data, _, _ := sess.Scrollback().Since(0)
		if len(data) > 1024 {
			t.Fatalf("滚动缓冲必须受字节硬顶约束：%d > 1024", len(data))
		}
		if sess.Earliest() <= 0 {
			t.Fatalf("撞到硬顶必须淘汰旧字节，earliest=%d", sess.Earliest())
		}
	})
}

func tailString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// TestPTYBadConfigIsLoud 证明参数错误不会静默变成"能用"。
func TestPTYBadConfigIsLoud(t *testing.T) {
	cases := []struct {
		name string
		cfg  terminal.Config
	}{
		{"空路径", terminal.Config{Cols: 80, Rows: 24}},
		{"零列", terminal.Config{Path: testBash(t), Cols: 0, Rows: 24}},
		{"零行", terminal.Config{Path: testBash(t), Cols: 80, Rows: 0}},
		{"负尺寸", terminal.Config{Path: testBash(t), Cols: -1, Rows: -1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := terminal.Open(tc.cfg); !errors.Is(err, terminal.ErrBadSize) && !errors.Is(err, terminal.ErrStart) {
				t.Fatalf("非法配置必须报错，拿到 %v", err)
			}
		})
	}
}
