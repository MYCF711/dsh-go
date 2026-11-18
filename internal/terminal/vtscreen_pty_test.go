package terminal_test

// 屏幕模型的**真 PTY** 验证：不靠手工构造字节流，而是让真 bash 用 `\r` 重画一行，
// 再从 `ScreenLines` 读回"屏幕上的样子"，并同时断言旧口径（原始字节尾部）里
// 还留着被覆盖掉的旧内容——这就是修掉的那个缺陷本身。

import (
	"strings"
	"testing"
	"time"

	"dshgo/internal/terminal"
)

// waitScreenReady 等 shell 真的起来（屏幕上出现内容且安静下来）。
//
// 判据与 `cmd/dshd` 的真实链路测试同源：ConPTY 一开始会吐一串模式序列，
// 那时候往 PTY 写会丢，所以必须等到"有内容 + 静默一小段"。
func waitScreenReady(t *testing.T, sess *terminal.Session) []string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var last string
	var sameSince time.Time
	for {
		lines := sess.ScreenLines()
		joined := strings.Join(lines, "\n")
		if joined != last {
			last, sameSince = joined, time.Now()
		} else if joined != "" && time.Since(sameSince) >= 300*time.Millisecond {
			return lines
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("20s 内 shell 没有进入就绪状态；当前屏幕行 = %#v", lines)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitScreenLine 等到屏幕上出现满足判据的一行为止。
func waitScreenLine(t *testing.T, sess *terminal.Session, name string, ok func(lines []string) bool) []string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		lines := sess.ScreenLines()
		if ok(lines) {
			return lines
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("20s 内 %s 没有出现；当前屏幕行 = %#v", name, lines)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestPTYScreenLinesShowAppliedScreen 是一句话的判据：
// **读出来的是屏幕上是什么，不是写入顺序拼起来是什么。**
func TestPTYScreenLinesShowAppliedScreen(t *testing.T) {
	sess := openTest(t, 100)
	waitScreenReady(t, sess)
	// bash 的 printf 会解释 \r 与 \033：先写 AAA，回车回来清掉本行，再写 BBB。
	if err := sess.Input("printf 'AAA\\r\\033[2KBBB\\n'\n"); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	lines := waitScreenLine(t, sess, "屏幕上出现 BBB", func(lines []string) bool {
		for _, line := range lines {
			if strings.TrimSpace(line) == "BBB" {
				return true
			}
		}
		return false
	})
	// 屏幕上必须有一行**恰好**是 BBB（旧口径下这一行是 "AAA\r\033[2KBBB" 那种碎片）。
	//
	// 注意不能断言"整屏不含 AAA"：bash 会把这条命令行**回显**出来，回显那行本来就
	// 带着 AAA，那是输入回显而不是屏幕没重画。判据只认"有没有一行正好是被覆盖的旧内容"。
	for _, line := range lines {
		if strings.TrimSpace(line) == "AAA" {
			t.Fatalf("被覆盖掉的 AAA 单独占了一行（屏幕没重画）: %#v", lines)
		}
	}

	// 同一时刻的旧口径：原始字节尾部里 AAA 与 \r 都还在，按写入顺序拼出来就是碎片。
	tail, _ := sess.Tail(0)
	if !strings.Contains(tail, "AAA") {
		t.Fatalf("旧口径应当仍保留被覆盖的 AAA（否则这条对比没有意义）: %q", tail)
	}
	if !strings.Contains(tail, "\r") {
		t.Fatalf("旧口径的原始尾部里应当还有 \\r: %q", tail)
	}
}

// TestPTYScreenSnapshotReplaysInFreshScreen 钉住 attach 的 `screen` 口径：
// 官方是 `serialize()` 造成的重放流（`src/terminal.ts:104`），消费方把它写进自己的
// 终端就该得到同一屏。这里把快照喂给一个全新屏幕，断言两边逻辑行一致。
func TestPTYScreenSnapshotReplaysInFreshScreen(t *testing.T) {
	sess := openTest(t, 100)
	waitScreenReady(t, sess)
	if err := sess.Input("echo SNAPSHOT-MARKER-7a1\n"); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	// 回显 + 输出各一次 → 说明命令真的执行完了。
	waitScreenLine(t, sess, "命令回显与输出", func(lines []string) bool {
		return strings.Count(strings.Join(lines, "\n"), "SNAPSHOT-MARKER-7a1") >= 2
	})

	cols, rows := sess.Size()
	// 快照与屏幕行是两次读取，中间 shell 可能还在动（下一个提示符）——所以按
	// "连续两次一致"收口，而不是一次就断言。
	deadline := time.Now().Add(20 * time.Second)
	for {
		lines := sess.ScreenLines()
		replayed := terminal.ScreenFromSnapshot(sess.ScreenSnapshot(0), cols, rows)
		if strings.Join(replayed, "\n") == strings.Join(lines, "\n") {
			if len(replayed) == 0 {
				t.Fatal("屏幕行与重放结果同时为空，这条用例证明不了任何东西")
			}
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("20s 内快照重放与屏幕行没有收敛:\n重放 = %#v\n源屏 = %#v", replayed, lines)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
