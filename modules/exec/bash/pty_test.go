package bash

// 执行层 PTY 终端的真实行为验证。
//
// 这里不测"接口长得对不对"，只测真东西：
//   - 两条 PTY 会话互不串（A/B 独立）；
//   - 退出码来自真实进程，且**普通管道式 bash 不受影响**（自然退出不回归）；
//   - close 之后整棵进程树停止（Job Object 归属）；
//   - resize 真的作用在子进程窗口上。

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// openPTYForTest 起一条真 PTY 终端；非 Windows 上跳过（那边是官方形状 unimplemented）。
func openPTYForTest(t *testing.T, svc *Service, cols, rows int) *Terminal {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("ConPTY 只在 Windows 上实现")
	}
	requireBash(t)
	term, err := svc.OpenPTYTerminal(t.TempDir(), cols, rows)
	if err != nil {
		t.Fatalf("起 PTY 终端失败：%v", err)
	}
	return term
}

// ptyReadUntil 从 cursor 起读，直到出现 marker。
func ptyReadUntil(t *testing.T, term *Terminal, cursor *int64, marker string, timeout time.Duration) string {
	t.Helper()
	sess := term.PTY()
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
			t.Fatalf("终端在等到 %q 之前退出了；已读到 %q", marker, sb.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等不到 %q；已读到 %q", marker, sb.String())
	return ""
}

// TestPTYTerminalABIsolation 证明两条 PTY 会话的输入/输出互不串。
func TestPTYTerminalABIsolation(t *testing.T) {
	svc := newSvc(t)
	termA := openPTYForTest(t, svc, 80, 24)
	termB := openPTYForTest(t, svc, 80, 24)
	if termA.ID() == termB.ID() {
		t.Fatalf("两条终端的 id 必须不同：%q", termA.ID())
	}

	markerA := fmt.Sprintf("SESSION-A-%d", time.Now().UnixNano())
	markerB := fmt.Sprintf("SESSION-B-%d", time.Now().UnixNano())
	if err := termA.Input("echo " + markerA + "\n"); err != nil {
		t.Fatalf("写 A 失败：%v", err)
	}
	if err := termB.Input("echo " + markerB + "\n"); err != nil {
		t.Fatalf("写 B 失败：%v", err)
	}

	var cursorA, cursorB int64
	outA := ptyReadUntil(t, termA, &cursorA, markerA, 20*time.Second)
	outB := ptyReadUntil(t, termB, &cursorB, markerB, 20*time.Second)
	if strings.Contains(outA, markerB) {
		t.Fatalf("A 的输出里出现了 B 的 marker：%q", outA)
	}
	if strings.Contains(outB, markerA) {
		t.Fatalf("B 的输出里出现了 A 的 marker：%q", outB)
	}
	if termA.PTY().PID() == termB.PTY().PID() {
		t.Fatalf("两条会话必须是两个进程，拿到同一个 PID %d", termA.PTY().PID())
	}
}

// TestPTYTerminalNaturalExitDoesNotRegress 证明两件事同时成立：
//  1. PTY 会话里的 `exit N` 真的让进程退出，退出码是 N；
//  2. 同一 Service 上的**普通管道式 bash** 不受影响（自然退出不回归）。
func TestPTYTerminalNaturalExitDoesNotRegress(t *testing.T) {
	svc := newSvc(t)
	term := openPTYForTest(t, svc, 80, 24)
	if err := term.Input("exit 5\n"); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	select {
	case <-term.PTY().Done():
	case <-time.After(30 * time.Second):
		t.Fatal("exit 之后 30s 还没退出")
	}
	if code, ok := term.PTY().ExitCode(); !ok || code != 5 {
		t.Fatalf("PTY 退出码必须是真实的 5，拿到 (%d,%v)", code, ok)
	}

	// 普通管道式会话（既有能力）必须照常工作。
	res, err := svc.Run("echo pipe-still-works", 10*time.Second)
	if err != nil {
		t.Fatalf("普通 bash 会话回归了：%v", err)
	}
	if strings.TrimSpace(res.Output) != "pipe-still-works" {
		t.Fatalf("普通 bash 输出回归：%q", res.Output)
	}
	if res.ExitCode != 0 {
		t.Fatalf("普通 bash 退出码回归：%d", res.ExitCode)
	}
}

// TestPTYTerminalCloseReclaimsProcessTree 证明 close 真的回收整棵进程树：
// 终端里起的后台循环在 close 之后必须停止写心跳文件。
func TestPTYTerminalCloseReclaimsProcessTree(t *testing.T) {
	svc := newSvc(t)
	cwd := t.TempDir()
	if runtime.GOOS != "windows" {
		t.Skip("ConPTY 只在 Windows 上实现")
	}
	requireBash(t)
	term, err := svc.OpenPTYTerminal(cwd, 80, 24)
	if err != nil {
		t.Fatalf("起 PTY 终端失败：%v", err)
	}

	beat := filepath.Join(cwd, "heartbeat-pty.txt")
	cmd := fmt.Sprintf("nohup bash -c 'while true; do date +%%s%%N > %q; sleep 0.1; done' >/dev/null 2>&1 &\n", beat)
	if err := term.Input(cmd); err != nil {
		t.Fatalf("起心跳失败：%v", err)
	}

	readBeat := func() string {
		raw, err := os.ReadFile(beat)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(raw))
	}
	deadline := time.Now().Add(15 * time.Second)
	for readBeat() == "" && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if readBeat() == "" {
		t.Fatal("心跳文件没出现：后台循环没跑起来，测试无效")
	}
	first := readBeat()
	changed := false
	for i := 0; i < 60 && !changed; i++ {
		time.Sleep(50 * time.Millisecond)
		changed = readBeat() != first
	}
	if !changed {
		t.Fatal("心跳没在动：测试无效")
	}

	if err := svc.CloseTerminal(term.ID()); err != nil {
		t.Fatalf("close 失败：%v", err)
	}
	time.Sleep(400 * time.Millisecond)
	after := readBeat()
	time.Sleep(600 * time.Millisecond)
	if now := readBeat(); now != after {
		t.Fatalf("close 之后进程树还在写心跳：%q → %q", after, now)
	}
}

// TestPTYTerminalResizeIsReal 证明 Terminal.Resize 真的作用在子进程窗口上。
func TestPTYTerminalResizeIsReal(t *testing.T) {
	svc := newSvc(t)
	term := openPTYForTest(t, svc, 100, 30)
	var cursor int64
	// 循环打印尺寸：ConPTY 在处理 resize 通知期间可能吞掉紧随其后的输入字节，
	// 单发第二条命令会偶发 "stty size" → "tty size"。循环让后续迭代补上。
	if err := term.Input("for i in {1..200}; do stty size; sleep 0.05; done\n"); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	ptyReadUntil(t, term, &cursor, "30 100", 20*time.Second)
	if err := term.Resize(140, 45); err != nil {
		t.Fatalf("resize 失败：%v", err)
	}
	ptyReadUntil(t, term, &cursor, "45 140", 20*time.Second)
}

// TestPTYTerminalScrollbackBounded 证明执行层暴露的是**有界**滚动缓冲。
func TestPTYTerminalScrollbackBounded(t *testing.T) {
	svc := newSvc(t)
	term := openPTYForTest(t, svc, 80, 24)
	sb := term.Scrollback()
	if sb == nil {
		t.Fatal("PTY 终端必须带滚动缓冲")
	}
	if sb.Limit() <= 0 {
		t.Fatalf("滚动缓冲上限必须为正，拿到 %d", sb.Limit())
	}
	if err := term.Input("for i in {1..100}; do echo SCROLL-$i; done\n"); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	var cursor int64
	ptyReadUntil(t, term, &cursor, "SCROLL-100", 30*time.Second)
	if sb.Total() < int64(cursor) {
		t.Fatalf("滚动缓冲 total 必须覆盖已读字节：total=%d cursor=%d", sb.Total(), cursor)
	}
}
