package sandbox

// 受控根与 runner 的跨平台契约测试。
//
// 这三条不碰 Win32：会话 id 校验、danger-full-access 拒绝、以及**如实上报**的隔离层级。
// 真机制的三条证明在 `runner_windows_test.go`（Windows only）与
// `runner_other_test.go`（非 Windows 明确 SKIP）。

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionRootRejectsUnsafeSessionIDs(t *testing.T) {
	parent := t.TempDir()
	for _, bad := range []string{
		"", ".", "..", "a/b", `a\b`, "C:evil", "a:b", "trail.", "trail ", "line\nbreak",
		strings.Repeat("x", 129),
	} {
		if _, err := SessionRoot(parent, bad); !errors.Is(err, ErrRunnerRootInvalid) {
			t.Errorf("会话 id %q 应当被拒（ErrRunnerRootInvalid），实际 err=%v", bad, err)
		}
	}
	good, err := SessionRoot(parent, "sess-0123456789abcdef")
	if err != nil {
		t.Fatalf("正常会话 id 被拒: %v", err)
	}
	if !strings.HasSuffix(good, "sess-0123456789abcdef") {
		t.Fatalf("受控根路径拼错: %s", good)
	}
}

func TestRunnerRejectsDangerFullAccess(t *testing.T) {
	// danger-full-access 的语义就是「不虚构路径沙盒」；套上受限令牌自相矛盾。
	_, err := NewRunner(RunnerSpec{SessionID: "s1", HomeDir: t.TempDir(), Mode: ModeDangerFullAccess})
	if !errors.Is(err, ErrRunnerUnavailable) {
		t.Fatalf("danger-full-access 应当拒绝建 runner（ErrRunnerUnavailable），实际 %v", err)
	}
}

func TestRunnerEnvEmptyIsNil(t *testing.T) {
	// 空值不进环境：读回来「未设置」和「设成空」必须不可区分，否则子进程会以为启用了。
	if got := RunnerEnv(""); got != nil {
		t.Fatalf("空受控根不该生成环境行，实际 %v", got)
	}
	if got := RunnerEnv("  "); got != nil {
		t.Fatalf("空白受控根不该生成环境行，实际 %v", got)
	}
	got := RunnerEnv("/x/sandbox-roots")
	if len(got) != 1 || got[0] != EnvACLRoot+"=/x/sandbox-roots" {
		t.Fatalf("受控根环境行不对: %v", got)
	}
}

// TestEnforcementForIsHonest 守「不许把没到的层级写成 DONE」。
//
// 没配受控根、档位是 danger-full-access、或平台没有实现时，必须是
// policy-only-no-os-isolation 并给出**具体原因**；只有三种条件都成立才允许报
// windows-acl-write-only-partial，而且它自己就带着 partial。
func TestEnforcementForIsHonest(t *testing.T) {
	home := t.TempDir()

	// 1) 没配受控根 —— 无论什么平台都只能报策略层。
	enf, reason := EnforcementFor(RunnerSpec{SessionID: "s1", HomeDir: home, Mode: ModeWorkspaceWrite})
	if enf != EnforcementPolicyOnlyNoOSIsolation {
		t.Fatalf("未配置受控根时 enforcement 必须是 %s，实际 %s", EnforcementPolicyOnlyNoOSIsolation, enf)
	}
	if !strings.Contains(reason, EnvACLRoot) {
		t.Fatalf("未配置受控根的原因要指出 %s，实际 %q", EnvACLRoot, reason)
	}

	// 2) danger-full-access —— 那一档明确不启用 runner。
	enf, _ = EnforcementFor(RunnerSpec{SessionID: "s1", ParentDir: SandboxRootsDir(home), Mode: ModeDangerFullAccess})
	if enf != EnforcementPolicyOnlyNoOSIsolation {
		t.Fatalf("danger-full-access 的 enforcement 必须是 %s，实际 %s", EnforcementPolicyOnlyNoOSIsolation, enf)
	}

	// 3) 配了受控根：只有真的有平台实现才允许报 ACL 档。
	enf, reason = EnforcementFor(RunnerSpec{SessionID: "s1", ParentDir: SandboxRootsDir(home), Mode: ModeWorkspaceWrite})
	if platformSupportsRunner() {
		if enf != EnforcementWindowsACLWriteOnly {
			t.Fatalf("Windows + 受控根时 enforcement 应当是 %s，实际 %s", EnforcementWindowsACLWriteOnly, enf)
		}
		if !strings.Contains(string(enf), "partial") {
			t.Fatalf("ACL 档的名字必须自带 partial，实际 %q", enf)
		}
		if !strings.Contains(reason, "读/网络/进程不受限") {
			t.Fatalf("ACL 档的原因必须写明哪些没受限，实际 %q", reason)
		}
	} else if enf != EnforcementPolicyOnlyNoOSIsolation {
		t.Fatalf("本平台没有 runner 实现，enforcement 必须是 %s，实际 %s", EnforcementPolicyOnlyNoOSIsolation, enf)
	}
}

// TestSandboxRootsDirLayout 钉住受控根的位置形状：<home>/sandbox-roots/<session-id>。
func TestSandboxRootsDirLayout(t *testing.T) {
	home := t.TempDir()
	roots := SandboxRootsDir(home)
	if want := filepath.Join(home, "sandbox-roots"); roots != want {
		t.Fatalf("受控根父目录应当是 %s，实际 %s", want, roots)
	}
	root, err := SessionRoot(roots, "sess-x")
	if err != nil {
		t.Fatalf("SessionRoot: %v", err)
	}
	if want := filepath.Join(home, "sandbox-roots", "sess-x"); root != want {
		t.Fatalf("受控根应当是 %s，实际 %s", want, root)
	}
}
