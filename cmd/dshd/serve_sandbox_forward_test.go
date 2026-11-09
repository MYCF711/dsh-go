package main

// T15：modelOverride 生产路径必须把 sandbox store/presets 透传给下一轮 loopRunner。
//
// T17 §4 的 blocker：serve_loop.go 的 Run/RunContent 逐字段拷 loopRunner 时漏了
// sandboxPresets/sandboxStore，子进程因此收不到 DSHD_SANDBOX_MODE（read-only 下
// bash/write 真实写盘）。这里用测试进程的 run 拦截器记录**真实子进程 Env**，
// 分别钉住默认 modelOverride 的 Run 与 RunContent 两条路，以及 A/B 不串。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/modes"
	"dshgo/internal/sandbox"
	"dshgo/internal/transport"
)

func readChildEnvFile(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("子进程没有记录 Env: %v", err)
	}
	var env []string
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatalf("解析子进程 Env: %v", err)
	}
	return env
}

func childEnvValue(env []string, key string) (string, bool) {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix), true
		}
	}
	return "", false
}

func newSandboxForwardRunner(t *testing.T, home string, reg *modes.Registry, store *sandboxModeStore) *modelOverrideLoopRunner {
	t.Helper()
	base := &loopRunner{
		home: home, policy: "deny", maxSteps: 6, model: "sandbox-forward-model",
		sandboxPresets: reg, sandboxStore: store,
	}
	return &modelOverrideLoopRunner{base: base}
}

func assertChildSandboxMode(t *testing.T, env []string, want string) {
	t.Helper()
	got, ok := childEnvValue(env, sandbox.EnvMode)
	if !ok {
		t.Fatalf("子进程没有 %s（modelOverride 漏传 sandbox 字段）：env=%v", sandbox.EnvMode, env)
	}
	if got != want {
		t.Fatalf("子进程 %s=%q，want %q", sandbox.EnvMode, got, want)
	}
}

// TestModelOverrideForwardsSandboxEnvToRunChild：默认 modelOverride 生产路径的
// Run 必须把 read-only 档位写进真实子进程 Env；B 会话仍 workspace-write。
func TestModelOverrideForwardsSandboxEnvToRunChild(t *testing.T) {
	home := t.TempDir()
	reg := newSandboxTestRegistry(t)
	store := mustSandboxStore(t, home)
	a := createSandboxSession(t, home, modes.DefaultID)
	b := createSandboxSession(t, home, modes.DefaultID)
	approvals := newPermissionOverrideStore(home)
	if _, err := sandboxSetPermission(home, a, "read-only", reg, store, approvals); err != nil {
		t.Fatalf("设置 A read-only: %v", err)
	}

	capture := filepath.Join(t.TempDir(), "argv.json")
	envCapture := filepath.Join(t.TempDir(), "env.json")
	t.Setenv("DSHD_MODEL_WIRE_ARGV", capture)
	t.Setenv("DSHD_MODEL_WIRE_ENV", envCapture)
	runner := newSandboxForwardRunner(t, home, reg, store)

	if err := runner.Run(context.Background(), a, "turn-a", "prompt-a"); err != nil {
		t.Fatalf("Run(A): %v", err)
	}
	assertChildSandboxMode(t, readChildEnvFile(t, envCapture), "read-only")

	if err := runner.Run(context.Background(), b, "turn-b", "prompt-b"); err != nil {
		t.Fatalf("Run(B): %v", err)
	}
	assertChildSandboxMode(t, readChildEnvFile(t, envCapture), "workspace-write")
}

// TestModelOverrideForwardsSandboxEnvToRunContentChild：RunContent 是第二条
// 逐字段拷贝路径，同样必须透传（附件/块输入不能退回默认档）。
func TestModelOverrideForwardsSandboxEnvToRunContentChild(t *testing.T) {
	home := t.TempDir()
	reg := newSandboxTestRegistry(t)
	store := mustSandboxStore(t, home)
	sid := createSandboxSession(t, home, modes.DefaultID)
	approvals := newPermissionOverrideStore(home)
	if _, err := sandboxSetPermission(home, sid, "read-only", reg, store, approvals); err != nil {
		t.Fatalf("设置 read-only: %v", err)
	}

	capture := filepath.Join(t.TempDir(), "argv.json")
	envCapture := filepath.Join(t.TempDir(), "env.json")
	t.Setenv("DSHD_MODEL_WIRE_ARGV", capture)
	t.Setenv("DSHD_MODEL_WIRE_ENV", envCapture)
	runner := newSandboxForwardRunner(t, home, reg, store)

	blocks := []transport.ContentBlock{{Type: "text", Text: "prompt-content"}}
	if err := runner.RunContent(context.Background(), sid, "turn-content", blocks); err != nil {
		t.Fatalf("RunContent: %v", err)
	}
	assertChildSandboxMode(t, readChildEnvFile(t, envCapture), "read-only")
}
