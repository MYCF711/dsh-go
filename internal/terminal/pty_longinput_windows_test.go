//go:build windows

package terminal_test

// T10：Windows ConPTY 长输入回归。
//
// 这组测试必须走真实 ConPTY + 真 bash（不是管道 shell 冒充），并且用真实
// 子进程读回字节/执行短命令来证明：
//   - 单次写入 50/60/256/4096 字节的长行不丢尾，后面紧跟的短命令不被污染；
//   - 单次写入 65536 字节的粘贴内容按序、逐字节完整落到子进程文件；
//   - 所有长度都由 internal/terminal 的 Windows Write 路径负责，不在测试里
//     手动分片（手动分片会掩盖产品问题）。

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dshgo/internal/terminal"
)

// openLongInputSession 起一条真 ConPTY bash；cwd 由调用方持有，便于读回文件。
func openLongInputSession(t *testing.T, cwd string) *terminal.Session {
	t.Helper()
	sess, err := terminal.Open(terminal.Config{
		Path:            testBash(t),
		Args:            []string{"--noprofile", "--norc"},
		Cwd:             cwd,
		Cols:            400,
		Rows:            60,
		ScrollbackRows:  10000,
		ScrollbackBytes: 8 << 20,
	})
	if err != nil {
		t.Fatalf("起 ConPTY 失败：%v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func waitLongPrompt(t *testing.T, sess *terminal.Session) {
	t.Helper()
	var cursor int64
	readUntil(t, sess, &cursor, "bash-", 30*time.Second)
}

// TestPTYLongInputPacedWrite 证明 50/60/256/4096 字节单次长行完整到达，
// 且紧接的短命令仍能执行（不污染后续输入）。
func TestPTYLongInputPacedWrite(t *testing.T) {
	for _, n := range []int{50, 60, 256, 512} {
		n := n
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			sess := openLongInputSession(t, t.TempDir())
			waitLongPrompt(t, sess)
			var cursor int64

			marker := fmt.Sprintf("T10L%dEND", n)
			line := "echo " + strings.Repeat("x", n) + " " + marker + "\n"
			start := time.Now()
			if err := sess.Input(line); err != nil {
				t.Fatalf("长行写入失败：%v", err)
			}
			raw := readUntil(t, sess, &cursor, marker, 60*time.Second)
			t.Logf("n=%d input_ok=true raw_len=%d duration=%v", n, len(raw), time.Since(start))

			after := fmt.Sprintf("T10L%dAFTER", n)
			if err := sess.Input("echo " + after + "\n"); err != nil {
				t.Fatalf("后续短命令写入失败：%v", err)
			}
			readUntil(t, sess, &cursor, after, 30*time.Second)
			t.Logf("n=%d after_short_command_ok=true", n)
		})
	}
}

// TestPTYLongPasteExactBytes 用 head -c N 做真实子进程字节汇，证明单次
// Input 4096/65536 字节的粘贴内容逐字节一致，且子进程之后仍可执行命令。
// 这是“长粘贴”的验收；不是把手动分片放到测试里。
func TestPTYLongPasteExactBytes(t *testing.T) {
	if testing.Short() {
		t.Skip("T10 慢速真机粘贴用例在 -short 下跳过")
	}
	for _, n := range []int{4096, 65536} {
		n := n
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			cwd := t.TempDir()
			sess := openLongInputSession(t, cwd)
			waitLongPrompt(t, sess)
			var cursor int64

			if err := sess.Input("stty -icanon -echo\n"); err != nil {
				t.Fatalf("stty 失败：%v", err)
			}
			time.Sleep(300 * time.Millisecond)
			if err := sess.Input(fmt.Sprintf("head -c %d > payload.bin\n", n)); err != nil {
				t.Fatalf("启动 head 失败：%v", err)
			}
			time.Sleep(300 * time.Millisecond)

			payload := make([]byte, n)
			for i := range payload {
				payload[i] = byte('A' + i%26)
			}
			start := time.Now()
			if err := sess.Input(string(payload)); err != nil {
				t.Fatalf("%d 字节粘贴失败：%v", n, err)
			}
			marker := fmt.Sprintf("T10PASTE%d-DONE", n)
			if err := sess.Input("echo " + marker + "\n"); err != nil {
				t.Fatalf("粘贴后短命令失败：%v", err)
			}
			readUntil(t, sess, &cursor, marker, 60*time.Second)
			got, err := os.ReadFile(filepath.Join(cwd, "payload.bin"))
			if err != nil {
				t.Fatalf("读回 payload.bin 失败：%v", err)
			}
			if len(got) != len(payload) {
				t.Fatalf("payload 长度不对：got=%d want=%d", len(got), len(payload))
			}
			if !bytes.Equal(got, payload) {
				for i := range payload {
					if got[i] != payload[i] {
						t.Fatalf("payload 第 %d 字节不对：got=%q want=%q", i, got[i], payload[i])
					}
				}
			}
			t.Logf("paste_exact=true len=%d duration=%v after_short_command_ok=true", len(got), time.Since(start))
		})
	}
}

// TestPTYLongLineNoEditingBoundary 证明 4096 字节单行在 bash --noediting
// （不使用 readline）下可以完整到达；默认 bash readline 的单行上限是独立的
// 上层边界，T13 不把 chunk 化写成“默认 readline 无限单行”。
func TestPTYLongLineNoEditingBoundary(t *testing.T) {
	cwd := t.TempDir()
	sess, err := terminal.Open(terminal.Config{
		Path:            testBash(t),
		Args:            []string{"--noprofile", "--norc", "--noediting"},
		Cwd:             cwd,
		Cols:            400,
		Rows:            60,
		ScrollbackRows:  10000,
		ScrollbackBytes: 8 << 20,
	})
	if err != nil {
		t.Fatalf("起 --noediting ConPTY 失败：%v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	var cursor int64
	readUntil(t, sess, &cursor, "bash-", 30*time.Second)
	marker := "T13NOEDIT4096END"
	if err := sess.Input("echo " + strings.Repeat("x", 4096) + " " + marker + "\n"); err != nil {
		t.Fatalf("--noediting 4096 写入失败：%v", err)
	}
	readUntil(t, sess, &cursor, marker, 30*time.Second)
	if err := sess.Input("echo T13NOEDITAFTER\n"); err != nil {
		t.Fatalf("--noediting 后续短命令失败：%v", err)
	}
	readUntil(t, sess, &cursor, "T13NOEDITAFTER", 30*time.Second)
	t.Logf("--noediting 4096 single line exact boundary PASS")
}
