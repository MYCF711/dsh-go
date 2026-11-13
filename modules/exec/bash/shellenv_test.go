package bash

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"dshgo/internal/spi"
)

// stubShellEnv 是测试用的受管环境提供者：静态 env 或按 ctx 现算的 fn 二选一。
type stubShellEnv struct {
	env map[string]string
	fn  func(ctx context.Context) (map[string]string, error)
	err error

	mu       sync.Mutex
	calls    int
	sessions []string
}

func (s *stubShellEnv) Collect(ctx context.Context) (map[string]string, error) {
	s.mu.Lock()
	s.calls++
	s.sessions = append(s.sessions, spi.SessionFrom(ctx))
	s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	if s.fn != nil {
		return s.fn(ctx)
	}
	out := make(map[string]string, len(s.env))
	for k, v := range s.env {
		out[k] = v
	}
	return out, nil
}

// withShellEnv 把 stub 接到 Service 上。生产路径是装配期从 `*kernel.Scope` inject
// （`spi.ShellEnvKey`）；同包测试直接写这份迟到的缓存字段，等价且不用搭模块图。
func withShellEnv(svc *Service, p spi.ShellEnvProvider) *Service {
	svc.envMu.Lock()
	defer svc.envMu.Unlock()
	svc.envProvider = p
	svc.envTried = true
	return svc
}

// stubEnvFailure 模拟 internal/shellenv 的 ShellEnvError：只暴露一个码。
// bash 包不许 import 那个包（R2），消费方按接口断言识别——这里顺带证明那条路能走通。
type stubEnvFailure struct{ code string }

func (e *stubEnvFailure) Error() string {
	return `bash env contributor "boom" returned undeclared key "DSH_X"`
}
func (e *stubEnvFailure) ShellEnvCode() string { return e.code }

func TestStripManagedEnv(t *testing.T) {
	base := []string{"PATH=/bin", "DSH_HOME=/old", "dsh_shell=1", "DSHD_KEEP=1", "HOME=/root"}
	got := stripManagedEnv(base)
	// 受管前缀大小写不敏感（Windows 环境名不区分大小写）；
	// `DSHD_` 不是 `DSH_`，一个字节都不许误伤。
	want := []string{"PATH=/bin", "DSHD_KEEP=1", "HOME=/root"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stripManagedEnv = %v，期望 %v", got, want)
	}
}

func TestShellScriptManagedShape(t *testing.T) {
	script := shellScript("echo hi", 7, map[string]string{"DSH_B": "b", "DSH_A": "a'q"}, true)
	// 先清空既有 DSH_*，再按**键名有序** export 快照。
	if !strings.Contains(script, "for __dshd_k in ${!DSH_@}; do unset \"$__dshd_k\"; done; unset __dshd_k\n") {
		t.Fatalf("缺少清空既有 DSH_* 的前缀:\n%s", script)
	}
	if !strings.Contains(script, "export DSH_A='a'\\''q' DSH_B='b'\n") {
		t.Fatalf("export 语句不对（顺序或转义）:\n%s", script)
	}
	// 命令与哨兵保持旧形状：受管环境不许改命令体本身。
	if !strings.Contains(script, "{ echo hi\n} 2>&1; __dshd_rc=$?") {
		t.Fatalf("命令体形状变了:\n%s", script)
	}
	if !strings.Contains(script, "\\001DSHD:7:") {
		t.Fatalf("哨兵序号丢了:\n%s", script)
	}
}

func TestShellScriptUnmanagedKeepsLegacyShape(t *testing.T) {
	// 未接线路径必须**逐字节**保持改造前的脚本：既有测试与既有行为都不许动。
	got := shellScript("echo hi", 3, nil, false)
	want := "{ echo hi\n} 2>&1; __dshd_rc=$?\nprintf '\\001DSHD:3:%d\\001%s\\001\\n' \"$__dshd_rc\" \"$PWD\"\n"
	if got != want {
		t.Fatalf("未接线脚本 = %q，期望 %q", got, want)
	}
}

// TestManagedEnvVisibleInRealShell 是真子进程断言：注入后，继承来的 DSH_* 看不见，
// 快照里的三个键看得见，近似前缀 DSHD_ 不被误伤。
func TestManagedEnvVisibleInRealShell(t *testing.T) {
	requireBash(t)
	t.Setenv("DSH_INHERITED", "from-host")
	t.Setenv("DSHD_KEEP", "keep-me")

	svc := withShellEnv(newSvc(t), &stubShellEnv{env: map[string]string{
		"DSH_HOME":       "/tmp/dsh-home",
		"DSH_SHELL":      "1",
		"DSH_SESSION_ID": "sess-1",
	}})
	res, err := svc.RunContext(context.Background(), "env | grep '^DSH' | sort", 10*time.Second)
	if err != nil {
		t.Fatalf("执行失败: %v（输出 %q）", err, res.Output)
	}
	got := strings.Split(strings.TrimSpace(res.Output), "\n")
	want := []string{"DSHD_KEEP=keep-me", "DSH_HOME=/tmp/dsh-home", "DSH_SESSION_ID=sess-1", "DSH_SHELL=1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("子进程环境 = %v，期望 %v", got, want)
	}
}

// TestManagedEnvRebuiltPerExecution 证明"每次执行重建"：同一个常驻 shell 里，
// 会话 id 换一次，命令看到的 DSH_SESSION_ID 就换一次（不是钉在进程环境里）。
func TestManagedEnvRebuiltPerExecution(t *testing.T) {
	requireBash(t)
	svc := withShellEnv(newSvc(t), &stubShellEnv{fn: func(ctx context.Context) (map[string]string, error) {
		return map[string]string{"DSH_SESSION_ID": spi.SessionFrom(ctx)}, nil
	}})
	run := func(session string) string {
		t.Helper()
		ctx := spi.WithSession(context.Background(), session)
		res, err := svc.RunContext(ctx, `printf 'id=%s' "$DSH_SESSION_ID"`, 10*time.Second)
		if err != nil {
			t.Fatalf("执行失败: %v", err)
		}
		return strings.TrimSpace(res.Output)
	}
	if got := run("sess-a"); got != "id=sess-a" {
		t.Fatalf("第一次执行 = %q，期望 id=sess-a", got)
	}
	if got := run("sess-b"); got != "id=sess-b" {
		t.Fatalf("第二次执行 = %q，期望 id=sess-b（同一个 shell 里必须重建）", got)
	}
}

// TestManagedEnvClearsPreviousExports 证明命令自己 export 的 DSH_* 不会漏到下一条命令。
func TestManagedEnvClearsPreviousExports(t *testing.T) {
	requireBash(t)
	svc := withShellEnv(newSvc(t), &stubShellEnv{env: map[string]string{"DSH_SHELL": "1"}})
	ctx := context.Background()
	if _, err := svc.RunContext(ctx, "export DSH_LEAKED=1; echo ok", 10*time.Second); err != nil {
		t.Fatalf("第一条命令失败: %v", err)
	}
	res, err := svc.RunContext(ctx, `printf '[%s]' "${DSH_LEAKED:-gone}"`, 10*time.Second)
	if err != nil {
		t.Fatalf("第二条命令失败: %v", err)
	}
	if got := strings.TrimSpace(res.Output); got != "[gone]" {
		t.Fatalf("上一条命令留下的 DSH_* 没被清掉: %q", got)
	}
}

// TestUnwiredShellEnvKeepsLegacyBehaviour 证明未接线时退化为"不注入、也不清理"。
func TestUnwiredShellEnvKeepsLegacyBehaviour(t *testing.T) {
	requireBash(t)
	t.Setenv("DSH_INHERITED", "from-host")
	svc := newSvc(t) // 没有 shellEnv 能力：旧行为
	res, err := svc.RunContext(context.Background(), `printf '[%s]' "$DSH_INHERITED"`, 10*time.Second)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if got := strings.TrimSpace(res.Output); got != "[from-host]" {
		t.Fatalf("未接线时子进程应原样继承环境，实际 %q", got)
	}
}

// TestManagedEnvFailureStopsCommand：贡献者违规时命令**不跑**（官方语义），
// 且工具层给出带码指路的 Hint。
func TestManagedEnvFailureStopsCommand(t *testing.T) {
	requireBash(t)
	svc := withShellEnv(newSvc(t), &stubShellEnv{err: &stubEnvFailure{code: "SHELL_ENV_CONTRIBUTION_INVALID"}})

	if _, err := svc.RunContext(context.Background(), "echo should-not-run", 10*time.Second); err == nil {
		t.Fatal("受管环境构建失败时命令不该执行")
	} else if got := managedEnvErrorCode(err); got != "SHELL_ENV_CONTRIBUTION_INVALID" {
		t.Fatalf("错误码 = %q，期望 SHELL_ENV_CONTRIBUTION_INVALID（%v）", got, err)
	}
	svc.mu.Lock()
	sess := svc.sess
	svc.mu.Unlock()
	if sess != nil {
		t.Fatal("受管环境构建失败时不该起 shell 进程")
	}

	tool := &shellTool{svc: svc, budget: time.Second}
	res, err := tool.Exec(context.Background(), json.RawMessage(`{"command":"echo hi","description":"x"}`))
	if err == nil {
		t.Fatal("工具层应把受管环境失败报成失败")
	}
	if !strings.Contains(res.Hint, "受管 shell 环境") || !strings.Contains(res.Hint, "SHELL_ENV_CONTRIBUTION_INVALID") {
		t.Fatalf("Hint 没说清是什么坏了: %q", res.Hint)
	}
}
