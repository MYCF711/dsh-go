package main

// 官方 terminal.* 的 PTY 后端验证。
//
// 与 serve_terminal_api_test.go 的分工：
//   - 那里验的是**管道式**持久 shell（既有路径，必须不回归）；
//   - 这里验的是**真 ConPTY** 后端：官方字段（state/exitCode/sequence/scrollback）
//     全部有真实来源，且能力缺失时是官方形状的 501 而不是假成功。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"dshgo/internal/terminal"
	"dshgo/internal/transport"
	"dshgo/modules/exec/bash"
)

// newTestPTYTerminalAPI 建一套 PTY 后端（临时 home + 真实 bash.Service）。
func newTestPTYTerminalAPI(t *testing.T, cwd string) (*officialPTYTerminalAPI, string) {
	t.Helper()
	home := t.TempDir()
	svc := bash.NewService(cwd)
	t.Cleanup(svc.Close)
	api, err := newOfficialPTYTerminalAPI(home, svc,
		func(string) (string, error) { return cwd, nil }, 1000)
	if err != nil {
		t.Fatalf("起 PTY 终端层失败：%v", err)
	}
	t.Cleanup(api.Close)
	return api, home
}

// waitPTYSnapshot 轮询 attach 快照直到出现 marker。
func waitPTYSnapshot(t *testing.T, api *officialPTYTerminalAPI, sessionID, id, marker string) transport.OfficialTerminalSnapshot {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var last transport.OfficialTerminalSnapshot
	for time.Now().Before(deadline) {
		snap, err := api.TerminalAttach(context.Background(), sessionID, id)
		if err != nil {
			t.Fatalf("attach 失败：%v", err)
		}
		last = snap
		if strings.Contains(snap.Screen, marker) {
			return snap
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("等不到输出 %q（最后 screen=%q）", marker, last.Screen)
	return last
}

// TestPTYTerminalOfficialShape 是官方字段的真实来源验证：
// create 起真进程、attach 给真快照、input 真写、output 真增量、resize 真生效、
// environment 报真实 scrollback、close 真回收。
func TestPTYTerminalOfficialShape(t *testing.T) {
	cwd := t.TempDir()
	api, _ := newTestPTYTerminalAPI(t, cwd)
	ctx := context.Background()

	info, err := api.TerminalCreate(ctx, "sess-a", transport.OfficialTerminalCreateRequest{
		ID: "t1", Cols: 100, Rows: 30,
	})
	if err != nil {
		t.Fatalf("create 失败：%v", err)
	}
	if info.State != "running" {
		t.Fatalf("刚起的 PTY 终端 state 要 running，拿到 %q", info.State)
	}
	if info.ExitCode != nil {
		t.Fatalf("运行中的终端 exitCode 必须是 null，拿到 %v", *info.ExitCode)
	}
	if info.Cols != 100 || info.Rows != 30 {
		t.Fatalf("尺寸要如实回填 100x30，拿到 %dx%d", info.Cols, info.Rows)
	}
	if info.Cwd != cwd {
		t.Fatalf("cwd 要如实回填 %q，拿到 %q", cwd, info.Cwd)
	}

	marker := fmt.Sprintf("PTY-OFFICIAL-%d", time.Now().UnixNano())
	if err := api.Input("sess-a", "t1", "echo "+marker+"\n"); err != nil {
		t.Fatalf("写终端失败：%v", err)
	}
	snap := waitPTYSnapshot(t, api, "sess-a", "t1", marker)
	if !snap.Writable {
		t.Fatal("活着的终端必须 writable=true")
	}
	if snap.Sequence <= 0 {
		t.Fatalf("sequence 必须是真实已写字节数（>0），拿到 %d", snap.Sequence)
	}
	if !snap.OneShot {
		t.Fatal("attach 是一次性快照，必须 oneShot=true")
	}

	// resize 是官方 TerminalInput.resize 的语义：真的改子进程窗口。
	if err := api.Resize("sess-a", "t1", 140, 45); err != nil {
		t.Fatalf("resize 失败：%v", err)
	}
	if err := api.Input("sess-a", "t1", "stty size\n"); err != nil {
		t.Fatalf("写终端失败：%v", err)
	}
	waitPTYSnapshot(t, api, "sess-a", "t1", "45 140")

	// output 是增量字节游标：从 0 读到的必须覆盖 attach 的 sequence。
	out, err := api.Output("sess-a", "t1", 0)
	if err != nil {
		t.Fatalf("output 失败：%v", err)
	}
	if int64(out.Next) < snap.Sequence {
		t.Fatalf("output.next 必须 >= attach.sequence：%d < %d", out.Next, snap.Sequence)
	}
	if !strings.Contains(out.Output, marker) {
		t.Fatalf("output 增量里必须有真实输出 %q，实际 %q", marker, tailOf(out.Output, 200))
	}

	env, err := api.TerminalEnvironment(ctx, "sess-a")
	if err != nil {
		t.Fatalf("environment 失败：%v", err)
	}
	if env.Scrollback <= 0 {
		t.Fatalf("真 PTY 必须报真实滚动缓冲（>0），拿到 %d", env.Scrollback)
	}
	if env.MaxInputBytes <= 0 || env.MaxCols <= 0 || env.MaxRows <= 0 {
		t.Fatalf("environment 的限制必须为正：%+v", env)
	}
	if env.Cwd != cwd {
		t.Fatalf("environment.cwd 必须是会话目录，拿到 %q", env.Cwd)
	}

	if err := api.TerminalClose(ctx, "sess-a", "t1"); err != nil {
		t.Fatalf("close 失败：%v", err)
	}
	// 官方语义：重复 close 成功。
	if err := api.TerminalClose(ctx, "sess-a", "t1"); err != nil {
		t.Fatalf("重复 close 必须成功，拿到 %v", err)
	}
	if list, err := api.TerminalList("sess-a"); err != nil || len(list) != 0 {
		t.Fatalf("close 之后 list 不许再有它：%v %+v", err, list)
	}
}

func tailOf(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// TestPTYTerminalCrossSession 证明 A/B 会话互不串（与既有管道实现同一套围栏语义）。
func TestPTYTerminalCrossSession(t *testing.T) {
	cwd := t.TempDir()
	api, _ := newTestPTYTerminalAPI(t, cwd)
	ctx := context.Background()
	if _, err := api.TerminalCreate(ctx, "sess-a", transport.OfficialTerminalCreateRequest{ID: "t1", Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	if list, err := api.TerminalList("sess-b"); err != nil || len(list) != 0 {
		t.Fatalf("B 会话不许看到 A 的终端：%v %+v", err, list)
	}
	if _, err := api.TerminalAttach(ctx, "sess-b", "t1"); !errors.Is(err, transport.ErrCrossSession) {
		t.Fatalf("跨会话 attach 必须是 cross_session，拿到 %v", err)
	}
	if err := api.TerminalClose(ctx, "sess-b", "t1"); !errors.Is(err, transport.ErrCrossSession) {
		t.Fatalf("跨会话 close 必须是 cross_session，拿到 %v", err)
	}
}

// TestPTYTerminalExitCodeOfficialShape 证明退出码出现在官方字段里（不是编的 0）。
func TestPTYTerminalExitCodeOfficialShape(t *testing.T) {
	cwd := t.TempDir()
	api, _ := newTestPTYTerminalAPI(t, cwd)
	ctx := context.Background()
	if _, err := api.TerminalCreate(ctx, "sess-a", transport.OfficialTerminalCreateRequest{ID: "t1", Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	if err := api.Input("sess-a", "t1", "exit 3\n"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		list, err := api.TerminalList("sess-a")
		if err != nil {
			t.Fatal(err)
		}
		if len(list) == 1 && list[0].State == "exited" {
			if list[0].ExitCode == nil || *list[0].ExitCode != 3 {
				t.Fatalf("exited 的终端必须带真实 exitCode=3，拿到 %v", list[0].ExitCode)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("exit 3 之后 30s 终端状态还没变成 exited")
}

// TestPTYUnsupportedMapsToOfficial501 证明非 Windows/无 ConPTY 时是**官方形状**的未实现：
// 错误必须能被 transport 层翻成 501 not_implemented，而不是 500 或假成功。
func TestPTYUnsupportedMapsToOfficial501(t *testing.T) {
	wrapped := fmt.Errorf("起 PTY: %w", terminal.ErrUnsupported)
	if !errors.Is(mapPTYError(wrapped), transport.ErrJTOfficialUnsupported) {
		t.Fatalf("ErrUnsupported 必须映射成 transport.ErrJTOfficialUnsupported，拿到 %v", mapPTYError(wrapped))
	}
	if mapPTYError(nil) != nil {
		t.Fatal("nil 必须原样返回")
	}
	other := errors.New("别的错误")
	if !errors.Is(mapPTYError(other), other) {
		t.Fatal("非未实现错误必须原样上抛（不能吞成 501）")
	}
}
