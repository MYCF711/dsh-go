//go:build windows

package sandbox

// 把 767 行探针（`tmp/GO8-RC5/_sbxroot/probe/main.go`）的三条证明与一条因果对照
// 变成 Go 测试。判据逐条照抄探针，不重新发明：
//
//	证明1 受控根**之外**写不进        → 子进程试写失败，Win32 码 5 (Access is denied)，文件未创建
//	证明2 受控根**之内**写得进        → 子进程试写成功，码 0，文件真的出现
//	证明3 删掉受控根之后一点不剩      → 目录消失、父目录 DACL 前后逐字相同、三处都没有我方 SID 的 ACE
//	因果   外面写不进不是完整性级别的锅 → 同令牌同目录同 Low 标签，只差一个 ACE：授上就写得进、撤掉立刻又被拒
//
// 为什么这条「因果」是必须的：本机 shell 自己就跑在 Low 完整性，从 tmp 继承
// `Low Mandatory Level:(OI)(CI)(NW)`。如果不把两个目录的完整性标签显式对齐，
// 「写不进」可能只是完整性级别挡的，跟 ACL 无关——那就等于拿错误的归因去证明机制。
//
// 测试目录一律用 `t.TempDir()`，**绝不在用户工作区里落 ACL 或标签**。

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

// envWriteProbe 让测试二进制变成一个「只会试写一个文件」的子进程。
//
// 为什么不 spawn `cmd.exe /c echo > file`：那只能拿到 exit 1，证不出判据（5 是
// Access is denied，其它码是别的问题）。探针用一个 `--child` 模式回传精确 Win32 码，
// 这里用 re-exec 自己 + 环境变量达到同一目的。
const envWriteProbe = "DSHD_SANDBOX_ACL_WRITE_PROBE"

const (
	probeGenericWrite   = 0x40000000
	probeCreateAlways   = 2
	probeFileAttrNormal = 0x80
	probeInvalidHandle  = ^uintptr(0)
)

var (
	probeKernel32       = syscall.NewLazyDLL("kernel32.dll")
	procProbeCreateFile = probeKernel32.NewProc("CreateFileW")
	procProbeWriteFile  = probeKernel32.NewProc("WriteFile")
)

func TestMain(m *testing.M) {
	if path := strings.TrimSpace(os.Getenv(envWriteProbe)); path != "" {
		os.Exit(aclWriteProbeMain(path))
	}
	os.Exit(m.Run())
}

// aclWriteProbeMain 在受限令牌下只做一次 CreateFileW(GENERIC_WRITE)+WriteFile，
// 用退出码回传 Win32 错误码（0 = 写成功）。
func aclWriteProbeMain(path string) int {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 3
	}
	h, _, e := procProbeCreateFile.Call(uintptr(unsafe.Pointer(p)), probeGenericWrite,
		0, 0, probeCreateAlways, probeFileAttrNormal, 0)
	if h == probeInvalidHandle {
		code := win32ErrCode(win32Err(e))
		os.Stdout.WriteString("write-probe: 拒绝 code=" + itoa(uint64(code)) + "\n")
		return int(code & 0xFF)
	}
	msg := []byte("probe-write-ok\n")
	var written uint32
	procProbeWriteFile.Call(h, uintptr(unsafe.Pointer(&msg[0])), uintptr(len(msg)),
		uintptr(unsafe.Pointer(&written)), 0)
	procCloseHandle.Call(h)
	os.Stdout.WriteString("write-probe: 成功\n")
	return 0
}

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// aclFixture 是一次 ACL 实验的目录布局：`base` 下一对**受控根**与**对照目录**。
type aclFixture struct {
	base    string
	parent  string
	root    string
	outside string
	runner  *Runner
	impl    *windowsACLRunner
	sidStr  string
}

func newACLFixture(t *testing.T) *aclFixture {
	t.Helper()

	// 受控根与对照目录都建在测试自己的临时目录里：**绝不碰用户真实工作区**。
	base := t.TempDir()
	parent := filepath.Join(base, "sandbox-roots")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatalf("建受控根父目录: %v", err)
	}
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatalf("建对照目录: %v", err)
	}

	runner, err := NewRunner(RunnerSpec{
		SessionID: "acl-test-session",
		ParentDir: parent,
		Mode:      ModeWorkspaceWrite,
	})
	if err != nil {
		if errors.Is(err, ErrRunnerUnsupported) || errors.Is(err, ErrRunnerUnavailable) {
			t.Skipf("本环境用不了 ACL runner，**明确跳过而不是静默绿**：%v", err)
		}
		t.Fatalf("NewRunner: %v", err)
	}
	impl, ok := runner.impl.(*windowsACLRunner)
	if !ok {
		t.Fatalf("runner 实现类型是 %T", runner.impl)
	}
	f := &aclFixture{
		base: base, parent: parent, root: runner.Root(), outside: outside,
		runner: runner, impl: impl, sidStr: impl.sidStr,
	}
	t.Cleanup(func() { _ = runner.Close() })

	// 整个用例期间持有一份引用计数，否则每次 writeAsRestricted 的 release 都会
	// 把受控根删掉（那是设计：最后一个使用者负责收工）。
	if err := runner.acquire(); err != nil {
		t.Skipf("本环境建不了受控根（多半是权限不足），**明确跳过**：%v", err)
	}
	// 因果对照的前提：两个目录必须是**同一个**完整性级别。显式打同一标签，
	// 不让「从 tmp 继承来的 Low」替 ACL 背锅。
	if err := setLowLabel(f.outside); err != nil {
		t.Skipf("给对照目录打 Low 完整性标签失败，无法把完整性变量固定住，**明确跳过**：%v", err)
	}
	return f
}

// writeAsRestricted 用 runner 的真实令牌起一个子进程去试写 path，
// 返回 Win32 错误码（0 = 写成功）。
func (f *aclFixture) writeAsRestricted(t *testing.T, path string) int {
	t.Helper()

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("取测试二进制路径: %v", err)
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), envWriteProbe+"="+path)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	release, err := f.runner.Prepare(cmd)
	if err != nil {
		t.Fatalf("Prepare(受控根 %s): %v", f.root, err)
	}
	runErr := cmd.Run()
	relErr := release()
	if runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			t.Fatalf("受限子进程根本起不来（这一步失败就不是 ACL 语义问题了）: %v", runErr)
		}
	}
	if relErr != nil {
		t.Fatalf("release 受控根失败: %v", relErr)
	}
	code := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		_ = errors.As(runErr, &exitErr)
		code = exitErr.ExitCode()
	}
	t.Logf("受限子进程试写 %s → code=%d %s（子进程输出 %q）",
		path, code, syscall.Errno(code), strings.TrimSpace(buf.String()))
	return code
}

// 证明 2：受控根之内写得进。
func TestRunnerAllowsWriteInsideControlledRoot(t *testing.T) {
	f := newACLFixture(t)
	target := filepath.Join(f.root, "inside.txt")
	if code := f.writeAsRestricted(t, target); code != 0 {
		t.Fatalf("受控根内写入应当成功（code=0），实际 code=%d (%v)", code, syscall.Errno(code))
	}
	st, err := os.Stat(target)
	if err != nil {
		t.Fatalf("受控根内写入报成功后文件却不在: %v", err)
	}
	t.Logf("证明2：受控根内写入 code=0，文件 %d 字节；受限 SID=%s", st.Size(), f.sidStr)
}

// 证明 1：受控根之外写不进。
func TestRunnerDeniesWriteOutsideControlledRoot(t *testing.T) {
	f := newACLFixture(t)
	target := filepath.Join(f.outside, "outside.txt")
	code := f.writeAsRestricted(t, target)
	if code != 5 {
		t.Fatalf("受控根外写入应当被拒为 5 (Access is denied)，实际 code=%d (%v) —— "+
			"若本机返回别的拒绝码，说明判据变了，先看 docs/sandbox-policy.md §4",
			code, syscall.Errno(code))
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatalf("被拒的写入竟然留下了文件 %s", target)
	}
	t.Logf("证明1：受控根外写入 code=%d (%v)，文件未创建", code, syscall.Errno(code))
}

// 证明 3：删掉受控根之后一点不剩（父目录 ACL 前后逐字相同）。
func TestRunnerRemovalLeavesNoResidue(t *testing.T) {
	f := newACLFixture(t)

	// 先在受控根里留下内容，再删根：证明带着内容的目录也能干净删掉。
	if code := f.writeAsRestricted(t, filepath.Join(f.root, "keep.txt")); code != 0 {
		t.Fatalf("准备受控根内文件失败: code=%d", code)
	}

	baseBefore, err := sddlOf(f.base, daclSecurityInformation)
	if err != nil {
		t.Fatalf("读 %s 的 DACL: %v", f.base, err)
	}
	parentBefore, err := sddlOf(f.parent, daclSecurityInformation)
	if err != nil {
		t.Fatalf("读 %s 的 DACL: %v", f.parent, err)
	}

	if err := f.runner.Close(); err != nil {
		t.Fatalf("Close（删受控根）: %v", err)
	}
	if _, err := os.Lstat(f.root); !os.IsNotExist(err) {
		t.Fatalf("受控根 %s 还在（Lstat err=%v）", f.root, err)
	}

	baseAfter, err := sddlOf(f.base, daclSecurityInformation)
	if err != nil {
		t.Fatalf("复读 %s 的 DACL: %v", f.base, err)
	}
	if baseAfter != baseBefore {
		t.Fatalf("%s 的 DACL 变了：\n前 %s\n后 %s", f.base, baseBefore, baseAfter)
	}
	parentAfter, err := sddlOf(f.parent, daclSecurityInformation)
	if err != nil {
		t.Fatalf("复读 %s 的 DACL: %v", f.parent, err)
	}
	if parentAfter != parentBefore {
		t.Fatalf("%s 的 DACL 变了：\n前 %s\n后 %s", f.parent, parentBefore, parentAfter)
	}

	// 三处都不许留下我们受限 SID 的 ACE。
	for _, dir := range []string{f.base, f.parent, f.outside} {
		sddl, err := sddlOf(dir, daclSecurityInformation)
		if err != nil {
			t.Fatalf("读 %s 的 DACL: %v", dir, err)
		}
		if strings.Contains(sddl, f.sidStr) {
			t.Fatalf("%s 上残留了我们受限 SID 的 ACE: %s", dir, sddl)
		}
	}
	t.Logf("证明3：受控根已消失；%s 与 %s 的 DACL 前后逐字相同；三处均无 %s 的 ACE",
		f.base, f.parent, f.sidStr)
}

// 因果对照：同为 Low 完整性，只差一个 ACE。
func TestRunnerDenialIsACLEffectNotIntegrityLevel(t *testing.T) {
	f := newACLFixture(t)

	rootLabel, err := sddlOf(f.root, labelSecurityInformation)
	if err != nil {
		t.Fatalf("读受控根完整性标签: %v", err)
	}
	outsideLabel, err := sddlOf(f.outside, labelSecurityInformation)
	if err != nil {
		t.Fatalf("读对照目录完整性标签: %v", err)
	}
	if rootLabel != outsideLabel {
		t.Skipf("受控根与对照目录的完整性标签不一致（%q vs %q），"+
			"那就证不出「只差一个 ACE」，**明确跳过**", rootLabel, outsideLabel)
	}
	t.Logf("前提确认：受控根与对照目录的完整性标签都是 %q", rootLabel)

	withoutACE := f.writeAsRestricted(t, filepath.Join(f.outside, "causal-before.txt"))
	if withoutACE != 5 {
		t.Fatalf("对照目录（无 ACE）应当被拒为 5，实际 %d", withoutACE)
	}

	// 给对照目录授**同一个**受限 SID 的同一个 ACE。
	if err := modifyDACL(f.outside, f.impl.sidPointer(), grantMask, setAccessMode); err != nil {
		t.Skipf("给对照目录临时授 ACE 失败，**明确跳过**：%v", err)
	}
	revoked := false
	revoke := func() error {
		if revoked {
			return nil
		}
		revoked = true
		return modifyDACL(f.outside, f.impl.sidPointer(), 0, revokeAccessMode)
	}
	// 收尾必须把对照目录还原：这条测试本身不许留痕迹。
	t.Cleanup(func() {
		if err := revoke(); err != nil {
			t.Errorf("撤销对照目录的 ACE 失败（会留下痕迹）: %v", err)
		}
	})

	withACE := f.writeAsRestricted(t, filepath.Join(f.outside, "causal-with-ace.txt"))
	if withACE != 0 {
		t.Fatalf("授了同一 ACE 之后对照目录应当写得进（code=0），实际 %d", withACE)
	}

	// 立刻撤掉再试：拒绝必须马上回来，才说明挡写入的就是那个 ACE。
	if err := revoke(); err != nil {
		t.Fatalf("撤销对照目录的 ACE: %v", err)
	}
	afterRevoke := f.writeAsRestricted(t, filepath.Join(f.outside, "causal-after-revoke.txt"))
	if afterRevoke != 5 {
		t.Fatalf("撤掉 ACE 后应当立刻又被拒（code=5），实际 %d", afterRevoke)
	}

	os.Remove(filepath.Join(f.outside, "causal-with-ace.txt"))
	t.Logf("因果对照：同令牌、同目录、同 Low 完整性(%s) —— 无 ACE code=%d；授同一 ACE code=%d；撤销 code=%d",
		rootLabel, withoutACE, withACE, afterRevoke)
}
