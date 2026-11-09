package main

// ACP 的 sandbox 档位必须真的落到执行口上。
//
// 背景（HS-05 先红）：ACP 是**一个进程多会话**，而 `modes.Mode` 里已经有
// `sandbox` 字段（read-only / workspace-write / danger-full-access）。
// 旧实现只把 mode 用于工具可见性与审批倾向——于是一个 `sandbox=read-only`
// 但工具表不设白名单的自建模式，在 ACP 里照样能起 bash 进程。
//
// 这里不认工具名：只看 Caps（Executes/Writes）+ 会话当前的 sandbox 档位。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"dshgo/internal/agent"
	"dshgo/internal/modes"
	"dshgo/internal/sandbox"
	"dshgo/internal/session"
	"dshgo/internal/spi"
	"dshgo/modules/fs/local"
)

// sseToolCallArgs 与 acp_test.go 的 sseToolCall 同构，只是可以给工具参数。
func sseToolCallArgs(t *testing.T, tool, args string) string {
	t.Helper()
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		if atomic.AddInt32(&n, 1) == 1 {
			fmt.Fprintf(w, "data: %s\n\n", fmt.Sprintf(
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function",`+
					`"function":{"name":%q,"arguments":%q}}]},"finish_reason":"tool_calls"}]}`,
				tool, args))
		} else {
			fmt.Fprintf(w, "data: %s\n\n",
				`{"choices":[{"delta":{"content":"好"},"finish_reason":"stop"}]}`)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		f.Flush()
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// sandboxHarness 造一个 acpRunner：工具表只有 tool，内层执行器只记账。
// cwd 固定，A/B 两个会话才能落在同一个工作区（ACP 一个进程只服务一个工作区）。
func sandboxHarness(t *testing.T, modeID string, modeReg *modes.Registry, tool string, caps spi.Caps, args string) (*acpRunner, *recorder, *int, string) {
	t.Helper()
	reg := &fakeReg{tools: map[string]spi.Tool{tool: &fakeTool{name: tool, caps: caps}}}
	inner := &recorder{}
	r := newACPRunner(reg, inner, sseToolCallArgs(t, tool, args), "m", t.TempDir(), 2)
	t.Cleanup(func() { _ = r.Shutdown() })
	r.ctxWindow = 0
	r.modeReg = modeReg
	r.modeID = modeID
	asked := 0
	r.ask = func(context.Context, string, string, string) (bool, error) {
		asked++
		return true, nil
	}
	cwd := t.TempDir()
	// 生产入口在 cmdACP 里做同一件事（失败 fail-loud）；这里手工装上闸门。
	if err := r.initSandbox(cwd, r.home, ""); err != nil {
		t.Fatalf("装 sandbox 闸门失败: %v", err)
	}
	return r, inner, &asked, cwd
}

func promptAt(t *testing.T, r *acpRunner, sessionID, cwd string) ([]agent.Event, error) {
	t.Helper()
	var evs []agent.Event
	_, _, err := r.Prompt(context.Background(), sessionID, cwd, "做点事", func(e agent.Event) error {
		evs = append(evs, e)
		return nil
	})
	return evs, err
}

func readOnlyAllToolsMode(t *testing.T) *modes.Registry {
	t.Helper()
	reg, err := modes.NewRegistry([]modes.Mode{{
		ID: "ro-all", Name: "Read only (all tools visible)", Sandbox: "read-only",
	}})
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// read-only 档位：命令必须在**起进程之前**被拒绝。
func TestACPSandboxReadOnlyModeDeniesCommandBeforeProcess(t *testing.T) {
	r, inner, _, cwd := sandboxHarness(t, "ro-all", readOnlyAllToolsMode(t),
		"bash", spi.Caps{Executes: true, Writes: true}, `{"command":"echo hi","description":"测试"}`)

	evs, err := promptAt(t, r, "s-ro", cwd)
	if err != nil {
		t.Fatalf("Prompt 出错: %v", err)
	}
	if inner.called != 0 {
		t.Fatalf("read-only 会话仍然执行了 %d 次命令——sandbox 档位没有落到执行口", inner.called)
	}
	ev := toolResultOf(evs, "bash")
	if ev == nil || ev.Result == nil {
		t.Fatalf("应当有一条 bash 的工具结果事件: %+v", evs)
	}
	if !strings.Contains(ev.Result.Hint, "sandbox") && !strings.Contains(ev.Result.Hint, "沙盒") {
		t.Fatalf("Hint 应当说清是 sandbox 档位拒绝（而不是别的原因）: %q", ev.Result.Hint)
	}
}

// 一个进程只能有一个 FS 边界：不同 sandbox 档位的会话必须显式拒绝，不能静默共享。
func TestACPSandboxProcessBoundaryRejectsDivergentMode(t *testing.T) {
	reg, err := modes.NewRegistry([]modes.Mode{{ID: "ro-all", Sandbox: "read-only"}})
	if err != nil {
		t.Fatal(err)
	}
	r, inner, _, cwd := sandboxHarness(t, "ro-all", reg,
		"bash", spi.Caps{Executes: true}, `{"command":"echo hi","description":"测试"}`)

	if _, err := promptAt(t, r, "s-A", cwd); err != nil {
		t.Fatalf("A 会话（read-only）不应报错: %v", err)
	}
	if inner.called != 0 {
		t.Fatalf("A 会话 read-only 仍然执行了命令")
	}

	// 冲突可以在 set_mode 或第一次 prompt 时暴露，两处都必须是同一条"档位冲突"。
	_, setErr := r.SetMode(context.Background(), "s-B", modes.DefaultID)
	if setErr == nil {
		_, setErr = promptAt(t, r, "s-B", cwd)
	}
	if setErr == nil {
		t.Fatal("同进程里创建第二个不同 sandbox 档位的会话必须失败，而不是静默共用一个进程级边界")
	}
	if !strings.Contains(setErr.Error(), "sandbox") && !strings.Contains(setErr.Error(), "沙盒") {
		t.Fatalf("错误要说清是 sandbox 档位冲突: %v", setErr)
	}
}

// fork 出来的会话必须继承父会话当前的 sandbox 档位，不能借 fork 放大权限。
func TestACPForkInheritsSandboxMode(t *testing.T) {
	r, inner, _, cwd := sandboxHarness(t, "ro-all", readOnlyAllToolsMode(t),
		"bash", spi.Caps{Executes: true}, `{"command":"echo hi","description":"测试"}`)

	if _, err := promptAt(t, r, "s-parent", cwd); err != nil {
		t.Fatal(err)
	}
	child, err := r.Fork(context.Background(), "s-parent", cwd)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := promptAt(t, r, child, cwd); err != nil {
		t.Fatal(err)
	}
	if inner.called != 0 {
		t.Fatalf("fork 出来的会话继承了可执行权限：执行了 %d 次", inner.called)
	}
}

// resume 时 header 里的 preset 解析不出来，必须响亮失败——静默回落到 default
// 就是一次权限放大。
func TestACPResumeUnknownPresetFailsLoud(t *testing.T) {
	r, _, _, cwd := sandboxHarness(t, modes.DefaultID, mustBuiltinModes(t),
		"bash", spi.Caps{Executes: true}, `{"command":"echo hi"}`)
	home := r.home
	id := "s-ghost"
	logw, err := session.CreateSession(home, cwd, id, "ghost-sandbox-mode")
	if err != nil {
		t.Fatal(err)
	}
	if err := logw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Resume(context.Background(), id, cwd); err == nil {
		t.Fatal("未知 preset 的会话 resume 必须报错，不能静默用 default")
	}
}

func mustBuiltinModes(t *testing.T) *modes.Registry {
	t.Helper()
	reg, err := modes.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// additionalDirectories：读放行、写一律拒绝；目录必须真实存在。
func TestACPAdditionalDirectoriesAreReadOnly(t *testing.T) {
	r, _, _, cwd := sandboxHarness(t, modes.DefaultID, mustBuiltinModes(t),
		"bash", spi.Caps{Executes: true}, `{"command":"echo hi"}`)
	extra := t.TempDir()
	note := filepath.Join(extra, "note.txt")
	if err := os.WriteFile(note, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	fs, err := local.New(cwd)
	if err != nil {
		t.Fatal(err)
	}
	r.fsExtra = fs

	if err := r.SetAdditionalDirectories("s-extra", []string{extra}); err != nil {
		t.Fatalf("合法 additionalDirectories 应接受: %v", err)
	}
	if _, err := fs.Read(note); err != nil {
		t.Fatalf("additionalDirectories 必须真的可读: %v", err)
	}
	if err := fs.Write(note, []byte("must not write")); err == nil {
		t.Fatal("additionalDirectories 必须只读")
	}

	ctx := spi.WithSession(context.Background(), "s-extra")
	read, err := r.sandbox.EvaluateFS(ctx, sandbox.OperationRead, note, "")
	if err != nil {
		t.Fatal(err)
	}
	if read.Action != sandbox.ActionAllow || read.Reason != sandbox.ReasonExtraRead {
		t.Fatalf("additionalDirectories 读判定 = %+v", read)
	}
	write, err := r.sandbox.EvaluateFS(ctx, sandbox.OperationWrite, note, "")
	if err != nil {
		t.Fatal(err)
	}
	if write.Action != sandbox.ActionDeny || write.Reason != sandbox.ReasonExtraReadOnly {
		t.Fatalf("additionalDirectories 写判定 = %+v", write)
	}

	if err := r.SetAdditionalDirectories("s-extra", []string{filepath.Join(extra, "missing")}); err == nil {
		t.Fatal("不存在的 additionalDirectories 必须 fail-loud")
	}
}

// 一次性 danger：审批通过后只执行这一次；换调用/换路径都不继承。
func TestACPOneShotDangerApprovalRunsTheCallOnce(t *testing.T) {
	outside := t.TempDir()
	args := fmt.Sprintf(`{"file_path":%q,"content":"x","sandbox_permissions":"danger-full-access"}`,
		filepath.Join(outside, "x.txt"))
	r, inner, asked, cwd := sandboxHarness(t, modes.DefaultID, mustBuiltinModes(t), "w", spi.Caps{Writes: true}, args)

	if _, err := promptAt(t, r, "s-esc", cwd); err != nil {
		t.Fatalf("Prompt 出错: %v", err)
	}
	if *asked == 0 {
		t.Fatal("越界写请求了 danger-full-access，必须先问审批")
	}
	if inner.called != 1 {
		t.Fatalf("批准之后应当执行一次，实际 %d", inner.called)
	}
}

// 需要审批但没有任何审批通道：fail-closed（不能静默按 allow 跑）。
func TestACPSandboxNeedsApprovalWithoutChannelFailsClosed(t *testing.T) {
	outside := t.TempDir()
	args := fmt.Sprintf(`{"file_path":%q,"content":"x","sandbox_permissions":"danger-full-access"}`,
		filepath.Join(outside, "x.txt"))
	r, inner, _, cwd := sandboxHarness(t, modes.DefaultID, mustBuiltinModes(t), "w", spi.Caps{Writes: true}, args)
	r.ask = nil

	evs, err := promptAt(t, r, "s-nochannel", cwd)
	if err != nil {
		t.Fatalf("Prompt 本身不该失败（工具结果是拒绝）: %v", err)
	}
	if inner.called != 0 {
		t.Fatalf("没有审批通道时工具不能执行，实际 %d", inner.called)
	}
	ev := toolResultOf(evs, "w")
	if ev == nil || ev.Result == nil || !strings.Contains(ev.Result.Hint, "审批") {
		t.Fatalf("应给出审批通道缺失的 Hint: %+v", ev)
	}
}

// 工具自己没有 sandbox 闸门（进程环境未接线）时，sandbox_permissions 由宿主消费：
// 否则工具会按"策略层未接线"的防降级规则 fail-loud，而这次调用其实已经判定过。
func TestACPSandboxPermissionArgConsumedWhenToolsUnwired(t *testing.T) {
	outside := t.TempDir()
	args := fmt.Sprintf(`{"file_path":%q,"content":"x","sandbox_permissions":"danger-full-access"}`,
		filepath.Join(outside, "x.txt"))
	r, inner, asked, cwd := sandboxHarness(t, modes.DefaultID, mustBuiltinModes(t), "w", spi.Caps{Writes: true}, args)
	capture := &capturingRunner{}
	r.runner = capture

	if _, err := promptAt(t, r, "s-strip", cwd); err != nil {
		t.Fatalf("Prompt 出错: %v", err)
	}
	if *asked == 0 {
		t.Fatal("danger 请求必须先问审批")
	}
	if capture.called != 1 {
		t.Fatalf("批准后应执行一次，实际 %d", capture.called)
	}
	if strings.Contains(string(capture.args), "sandbox_permissions") {
		t.Fatalf("宿主已消费的 sandbox_permissions 不应再交给工具: %s", capture.args)
	}
	if inner.called != 0 {
		t.Fatal("换了 runner 之后不应再走 recorder")
	}
}

type capturingRunner struct {
	called int
	args   json.RawMessage
}

func (c *capturingRunner) Run(_ context.Context, _ string, args json.RawMessage) (spi.Result, error) {
	c.called++
	c.args = append(json.RawMessage(nil), args...)
	return spi.Result{Summary: "执行了"}, nil
}

// read-only 档位连写类工具也要在写盘前拒绝。
func TestACPSandboxReadOnlyModeDeniesWriteTool(t *testing.T) {
	r, inner, _, cwd := sandboxHarness(t, "ro-all", readOnlyAllToolsMode(t),
		"w", spi.Caps{Writes: true}, `{"file_path":"inside.txt","content":"x"}`)
	if _, err := promptAt(t, r, "s-ro-write", cwd); err != nil {
		t.Fatalf("Prompt 出错: %v", err)
	}
	if inner.called != 0 {
		t.Fatalf("read-only 会话执行了写工具 %d 次", inner.called)
	}
}
