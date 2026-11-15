package pwsh

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"dshgo/internal/spi"
)

// stubShellEnv 是测试用的受管环境提供者。
//
// 与 `modules/exec/bash` 包里的那份同形但不共享：两个模块不许互相 import（R2），
// 各自留一份几行的小实现，比为了省 10 行而破例便宜。
type stubShellEnv struct {
	env map[string]string
	err error
}

func (s *stubShellEnv) Collect(context.Context) (map[string]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := make(map[string]string, len(s.env))
	for k, v := range s.env {
		out[k] = v
	}
	return out, nil
}

// withShellEnv 把 stub 接到工具上。生产路径是执行时从装配作用域惰性 inject
// `spi.ShellEnvKey`；同包测试直接写那份缓存，等价且不用搭模块图。
func withShellEnv(tl *Tool, p spi.ShellEnvProvider) *Tool {
	tl.envMu.Lock()
	defer tl.envMu.Unlock()
	tl.envProvider = p
	tl.envTried = true
	return tl
}

// stubEnvFailure 模拟 internal/shellenv 的 ShellEnvError：只暴露一个码
// （本包不许 import 那个包，消费方按接口断言识别）。
type stubEnvFailure struct{ code string }

func (e *stubEnvFailure) Error() string {
	return `bash env contributor "boom" returned a non-string value for "DSH_X"`
}
func (e *stubEnvFailure) ShellEnvCode() string { return e.code }

// seamStub 是实现了带 ctx 后台能力的接缝桩，用来证明"后台任务也按这次调用的
// 会话算受管环境"。
type seamStub struct {
	gotSession string
}

func (s *seamStub) Run(string, time.Duration) (spi.ExecResult, error) {
	return spi.ExecResult{}, nil
}

func (s *seamStub) RunBackgroundContext(ctx context.Context, _ string) (spi.ExecResult, error) {
	s.gotSession = spi.SessionFrom(ctx)
	return spi.ExecResult{JobID: "job-stub"}, nil
}

func TestBackgroundSeamCarriesSessionContext(t *testing.T) {
	requirePowerShell(t)
	seam := &seamStub{}
	tool := withShellEnv(newTestTool(t, WithExecSeam(seam)), &stubShellEnv{env: map[string]string{"DSH_SHELL": "1"}})
	raw, err := json.Marshal(map[string]any{
		"description":       "后台跑一条",
		"command":           "Start-Sleep -Seconds 1",
		"run_in_background": true,
	})
	if err != nil {
		t.Fatalf("构造参数: %v", err)
	}
	res, err := tool.Exec(spi.WithSession(context.Background(), "sess-bg"), raw)
	if err != nil {
		t.Fatalf("后台执行失败: %v（%v）", err, res.Data)
	}
	if seam.gotSession != "sess-bg" {
		t.Fatalf("接缝收到的会话 = %q，期望 sess-bg", seam.gotSession)
	}
}

func TestManagedEnvironListDropsInherited(t *testing.T) {
	base := []string{"Path=C:\\Windows", "DSH_HOME=/old", "dsh_shell=1", "DSHD_KEEP=1"}
	got := managedEnvironList(base, map[string]string{"DSH_B": "b", "DSH_A": "a"})
	// 受管前缀大小写不敏感；`DSHD_` 不是 `DSH_`，不许误伤；
	// 快照按**键名有序**追加，保证每次执行给出的顺序一致。
	want := []string{"Path=C:\\Windows", "DSHD_KEEP=1", "DSH_A=a", "DSH_B=b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("managedEnvironList = %v，期望 %v", got, want)
	}
}

func TestManagedEnvironUnwiredIsNil(t *testing.T) {
	// 未接线：返回 nil，`cmd.Env` 保持不动，子进程原样继承宿主环境（旧行为）。
	env, err := newTestTool(t).managedEnviron(context.Background())
	if err != nil {
		t.Fatalf("未接线时不该报错: %v", err)
	}
	if env != nil {
		t.Fatalf("未接线时不该给环境覆盖，实际 %v", env)
	}
}

// TestManagedEnvVisibleInRealPowerShell 是真子进程断言：注入后，继承来的 DSH_* 看不见，
// 快照里的键看得见，近似前缀 DSHD_ 不被误伤。
func TestManagedEnvVisibleInRealPowerShell(t *testing.T) {
	requirePowerShell(t)
	t.Setenv("DSH_INHERITED", "from-host")
	t.Setenv("DSHD_KEEP", "keep-me")

	tool := withShellEnv(newTestTool(t), &stubShellEnv{env: map[string]string{
		"DSH_HOME":       "/tmp/dsh-home",
		"DSH_SHELL":      "1",
		"DSH_SESSION_ID": "sess-1",
	}})
	command := "Get-ChildItem env: | Where-Object { $_.Name -like 'DSH*' } | ForEach-Object { $_.Name + '=' + $_.Value }"
	raw, err := json.Marshal(map[string]string{"description": "列受管环境", "command": command})
	if err != nil {
		t.Fatalf("构造参数: %v", err)
	}
	res, err := tool.Exec(context.Background(), raw)
	if err != nil {
		t.Fatalf("执行失败: %v（%s）", err, res.Text)
	}
	lines := strings.Split(strings.TrimSpace(res.Text), "\n")
	got := make([]string, 0, len(lines))
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			got = append(got, line)
		}
	}
	sort.Strings(got)
	want := []string{"DSHD_KEEP=keep-me", "DSH_HOME=/tmp/dsh-home", "DSH_SESSION_ID=sess-1", "DSH_SHELL=1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("子进程环境 = %v，期望 %v", got, want)
	}
}

// TestUnwiredPowerShellKeepsInheritedEnv 证明未接线时退化为"不注入"。
func TestUnwiredPowerShellKeepsInheritedEnv(t *testing.T) {
	requirePowerShell(t)
	t.Setenv("DSH_INHERITED", "from-host")

	tool := newTestTool(t) // 没有 shellEnv 能力：旧行为
	raw, err := json.Marshal(map[string]string{
		"description": "读继承变量",
		"command":     "[Environment]::GetEnvironmentVariable('DSH_INHERITED')",
	})
	if err != nil {
		t.Fatalf("构造参数: %v", err)
	}
	res, err := tool.Exec(context.Background(), raw)
	if err != nil {
		t.Fatalf("执行失败: %v（%s）", err, res.Text)
	}
	if got := strings.TrimSpace(res.Text); got != "from-host" {
		t.Fatalf("未接线时子进程应原样继承环境，实际 %q", got)
	}
}

// TestManagedEnvFailureStopsPowerShell：贡献者违规时命令**不跑**（官方语义），
// 且 Hint 说清是什么坏了。
func TestManagedEnvFailureStopsPowerShell(t *testing.T) {
	tool := withShellEnv(newTestTool(t), &stubShellEnv{err: &stubEnvFailure{code: "SHELL_ENV_CONTRIBUTION_INVALID"}})
	raw, err := json.Marshal(map[string]string{"description": "不该跑", "command": "Write-Output ran"})
	if err != nil {
		t.Fatalf("构造参数: %v", err)
	}
	res, err := tool.Exec(context.Background(), raw)
	if err == nil {
		t.Fatal("受管环境构建失败时命令不该执行")
	}
	if got := managedEnvErrorCode(err); got != "SHELL_ENV_CONTRIBUTION_INVALID" {
		t.Fatalf("错误码 = %q，期望 SHELL_ENV_CONTRIBUTION_INVALID（%v）", got, err)
	}
	if !strings.Contains(res.Hint, "受管 shell 环境") || !strings.Contains(res.Hint, "SHELL_ENV_CONTRIBUTION_INVALID") {
		t.Fatalf("Hint 没说清是什么坏了: %q", res.Hint)
	}
}
