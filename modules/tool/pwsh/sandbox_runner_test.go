package pwsh

// ACL 受控根接线的测试（不依赖 Windows：本机事实由能力探测决定）。
//
// 这两条守的是「不许静默降级」：
//   - 没配受控根 → enforcement 必须是 policy-only，且没有 runner_note（未启用不是问题）；
//   - 配了受控根但受限身份里 shell 起不来 → 命令照常执行，但 enforcement 仍是 policy-only，
//     **并且** runner_note 必须写明原因。若本机确实能在受限身份下跑 shell，则必须是
//     ACL 档 + 非空 sandbox_root，二者不许自相矛盾。

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"dshgo/internal/sandbox"
)

func TestNoACLRootReportsPolicyOnly(t *testing.T) {
	// 不装 Gate：这两条测的是 ACL 受控根接线，不是三档策略（Gate 的用例在 sandbox_test.go）。
	tool := &Tool{}
	res, err := tool.Exec(ctxWithSession("session-no-acl-root"), pwshArgs(t, "Write-Output 'plain=1'", "", nil))
	if err != nil {
		t.Fatalf("未启用受控根时命令应当照常执行: %v", err)
	}
	d, ok := res.Data.(data)
	if !ok {
		t.Fatalf("Data 类型是 %T", res.Data)
	}
	if d.Enforcement != string(sandbox.EnforcementPolicyOnlyNoOSIsolation) {
		t.Fatalf("未启用受控根时 enforcement 必须是 %s，实际 %q",
			sandbox.EnforcementPolicyOnlyNoOSIsolation, d.Enforcement)
	}
	if d.SandboxRoot != "" {
		t.Fatalf("未启用受控根却报了受控根 %q", d.SandboxRoot)
	}
	if d.RunnerNote != "" {
		t.Fatalf("未启用受控根不该有 runner_note（那是启用后失败才有的），实际 %q", d.RunnerNote)
	}
}

func TestACLRootReportsTruthfully(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("ACL 受控根只有 Windows 实现；非 Windows 上 " + sandbox.EnvACLRoot + " 不生效")
	}
	rootParent := filepath.Join(t.TempDir(), "sandbox-roots")
	if err := os.MkdirAll(rootParent, 0o700); err != nil {
		t.Fatal(err)
	}
	tool := &Tool{
		aclRootParent: rootParent,
		homeDir:       t.TempDir(),
	}
	t.Cleanup(func() {
		if err := tool.closeRunners(); err != nil {
			t.Errorf("收工失败（受控根可能残留）: %v", err)
		}
	})

	res, err := tool.Exec(ctxWithSession("session-acl-truth"), pwshArgs(t, "Write-Output ('acl=' + 1)", "", nil))
	if err != nil {
		t.Fatalf("命令本身应当照常执行（受控根用不上不等于不执行）: %v", err)
	}
	d, ok := res.Data.(data)
	if !ok {
		t.Fatalf("Data 类型是 %T", res.Data)
	}

	switch d.Enforcement {
	case string(sandbox.EnforcementWindowsACLWriteOnly):
		// 探针通过：必须真在受控根里跑。
		if strings.TrimSpace(d.SandboxRoot) == "" {
			t.Fatalf("报了 ACL 档却没有受控根: %+v", d)
		}
		if d.RunnerNote != "" {
			t.Fatalf("用上了 ACL runner 却又带失败说明: %+v", d)
		}
		if !strings.Contains(res.Text, "acl=1") {
			t.Fatalf("受控根内命令输出不对: %q", res.Text)
		}
		if _, statErr := os.Stat(d.SandboxRoot); statErr == nil {
			t.Fatalf("命令收工后受控根 %s 还在（该在最后一个使用者退出时删掉）", d.SandboxRoot)
		}
		t.Logf("本机 shell 能在受限身份下运行，已走 ACL 档：root=%s", d.SandboxRoot)
	case string(sandbox.EnforcementPolicyOnlyNoOSIsolation):
		if strings.TrimSpace(d.RunnerNote) == "" {
			t.Fatalf("退回策略层必须写明原因（runner_note 为空）: %+v", d)
		}
		if d.SandboxRoot != "" {
			t.Fatalf("退回策略层却报了受控根: %+v", d)
		}
		if !strings.Contains(res.Text, "acl=1") {
			t.Fatalf("退回后命令应当照常给出输出，实际 %q", res.Text)
		}
		t.Logf("如实退回策略层：enforcement=%s runner_note=%s", d.Enforcement, d.RunnerNote)
	default:
		t.Fatalf("enforcement 只能是 policy-only 或 ACL 档，实际 %q", d.Enforcement)
	}
}

func TestWorkdirInsideRoot(t *testing.T) {
	root := t.TempDir()
	inside, err := workdirInsideRoot("", root)
	if err != nil || !inside {
		t.Fatalf("空 workdir 应当视为在根内（由 Prepare 设成根本身）：inside=%v err=%v", inside, err)
	}
	inside, err = workdirInsideRoot(filepath.Join(root, "sub"), root)
	if err != nil || !inside {
		t.Fatalf("根内子目录应当判为在根内：inside=%v err=%v", inside, err)
	}
	inside, err = workdirInsideRoot(filepath.Dir(root), root)
	if err != nil || inside {
		t.Fatalf("父目录不该判为在根内：inside=%v err=%v", inside, err)
	}
	// 前缀相同但不同目录（workspace-evil）不许混过去。
	sibling := root + "-evil"
	if err := os.MkdirAll(sibling, 0o700); err != nil {
		t.Fatal(err)
	}
	inside, err = workdirInsideRoot(sibling, root)
	if err != nil || inside {
		t.Fatalf("前缀相同的兄弟目录不该判为在根内：inside=%v err=%v", inside, err)
	}
}
