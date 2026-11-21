package bash

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// 保留窗口的**头部裁剪点必须落在 UTF-8 字符边界上**。
//
// 官方在这件事上自相矛盾：`dsh-subprocess-local/src/output.ts:174-179` 按字节
// 精确认尾（`this.chunks[0] = head.subarray(excess)`），出口用
// `slice.toString('utf8')`（:265-267）→ 窗口首字节落在多字节字符中间时吐替换字符；
// 而同一个发行版里 `dsh-jobs-local/src/ring.ts:30-32` 的 `utf8Tail` 做同一件事
// 是**对的**（把起点推过续字节 `10xxxxxx`，注释写明 "the surviving text never
// starts inside a code point"）。我们原来抄的是错的那一版
// （`modules/exec/bash/session.go` 的 `drop := len(t.buf) - maxRetainedJobOutput`
// 直接 `copy`），于是中文输出溢出保留窗口后，模型看到的第一行以 U+FFFD 开头，
// 会当成"命令吐了乱码"而不是"输出被截断、原文在 spill 里"。
//
// 这条测试用**真多字节载荷**把裁剪点顶到字符中间，所以它对"按字节裁"必然红。
func TestTaskRetentionCutsOnRuneBoundary(t *testing.T) {
	// 3 字节/字的「中」，总数刚好超过运行窗口。
	const rune3 = "中"
	unit := len(rune3) // 3
	chars := (maxRetainedJobOutput / unit) + 5000
	payload := []byte(strings.Repeat(rune3, chars))

	// 前提自证：按字节直接裁确实会切在字符中间（否则这条用例证明不了任何事）。
	naiveDrop := len(payload) - maxRetainedJobOutput
	if naiveDrop%unit == 0 {
		t.Fatalf("夹具失效：naiveDrop=%d 恰好落在字符边界，构造不出缺陷现场", naiveDrop)
	}

	task := newTask(1)
	task.append(payload)
	base, total, truncated := task.retention()

	if total != int64(len(payload)) || !truncated {
		t.Fatalf("全量/截断标记错: total=%d want=%d truncated=%v", total, len(payload), truncated)
	}
	// 不变量：base 是已裁掉的字节数，total-base 必须等于实际缓冲长度。
	if total-base != int64(task.bufferedBytes()) {
		t.Fatalf("base 与缓冲长度不自洽: total=%d base=%d buffered=%d", total, base, task.bufferedBytes())
	}
	// 最多为对齐多丢 3 字节（一个 4 字节字符的续字节数上限），不许无界多丢。
	if over := int(base) - naiveDrop; over < 0 || over > 3 {
		t.Fatalf("对齐只许多丢 0..3 字节，实际多丢 %d（naiveDrop=%d base=%d）", over, naiveDrop, base)
	}

	text := task.text()
	if !utf8.ValidString(text) {
		t.Fatalf("保留窗口不是合法 UTF-8：前 24 字节 % x", []byte(text)[:24])
	}
	if strings.HasPrefix(text, "\uFFFD") {
		t.Fatalf("模型会看到以替换字符开头的输出（窗口首字节切在字符中间）：%q", text[:16])
	}
	// 窗口内容本身必须是原载荷的**连续子串**——对齐不能顺手改内容。
	if !strings.Contains(string(payload), text) {
		t.Fatal("保留窗口不是原输出的连续子串")
	}
}

// 结算裁剪（官方 `jobs.settledRetainBytes`）走同一条路径，同样要落在字符边界上。
func TestSettledTrimCutsOnRuneBoundary(t *testing.T) {
	const rune3 = "中"
	unit := len(rune3)
	const budget = 1000 // 不是 3 的倍数
	t.Setenv(EnvSettledRetainBytes, "1000")

	payload := []byte(strings.Repeat(rune3, 400)) // 1200 字节
	naiveDrop := len(payload) - budget
	if naiveDrop%unit == 0 {
		t.Fatalf("夹具失效：naiveDrop=%d 落在字符边界", naiveDrop)
	}

	task := newTask(1)
	task.append(payload)
	task.finish(0, "")
	base, total, truncated := task.retention()

	if total != int64(len(payload)) || !truncated {
		t.Fatalf("全量/截断标记错: total=%d truncated=%v", total, truncated)
	}
	if total-base != int64(task.bufferedBytes()) {
		t.Fatalf("base 与缓冲长度不自洽: total=%d base=%d buffered=%d", total, base, task.bufferedBytes())
	}
	if over := int(base) - naiveDrop; over < 0 || over > 3 {
		t.Fatalf("结算裁剪对齐只许多丢 0..3 字节，实际 %d", over)
	}
	text := task.text()
	if !utf8.ValidString(text) || strings.HasPrefix(text, "\uFFFD") {
		t.Fatalf("结算裁剪切在字符中间：%q", text[:16])
	}
	if !strings.Contains(string(payload), text) {
		t.Fatal("结算裁剪后的窗口不是原输出的连续子串")
	}
}

// 纯 ASCII 流不许因为对齐多丢一个字节：对齐只在续字节上生效。
// 这条与既有 `TestTaskOutputRetentionIsBounded`（断言 base==123）互为守卫，
// 一起保证"零代价"——对 ASCII 载荷一个数字都没动。
func TestRetentionASCIIIsNotOverDropped(t *testing.T) {
	payload := []byte(strings.Repeat("x", maxRetainedJobOutput+123))
	task := newTask(1)
	task.append(payload)
	base, total, _ := task.retention()
	if base != 123 || total != int64(len(payload)) || task.bufferedBytes() != maxRetainedJobOutput {
		t.Fatalf("ASCII 载荷的裁剪量必须与改前完全一致: base=%d total=%d buffered=%d",
			base, total, task.bufferedBytes())
	}
}

// dropHeadLocked 的边界：drop 为 0 / 等于整个缓冲时不能越界，也不能丢内容。
func TestDropHeadLockedBounds(t *testing.T) {
	task := newTask(1)
	task.append([]byte("中文abc"))
	task.mu.Lock()
	task.dropHeadLocked(0)
	if got := string(task.buf); got != "中文abc" {
		task.mu.Unlock()
		t.Fatalf("drop=0 不许动内容：%q", got)
	}
	task.dropHeadLocked(len(task.buf))
	rest, base := string(task.buf), task.base
	task.mu.Unlock()
	if rest != "" || base != int64(len("中文abc")) {
		t.Fatalf("drop 到空：buf=%q base=%d", rest, base)
	}
}
