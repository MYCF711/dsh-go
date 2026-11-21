//go:build windows

package sandbox

// Windows ACL runner：把命令放进「WRITE_RESTRICTED 受限令牌 + Low 完整性」下跑，
// 只给**受控根**授写 ACE，于是写/删只能在受控根里成立。
//
// 机制与 767 行探针（`tmp/GO8-RC5/_sbxroot/probe/main.go`）逐条同源，并由
// `runner_windows_test.go` 的四条证明守着：
//  1. 受控根之外写不进（`code=5 Access is denied`）；
//  2. 受控根之内写得进（`code=0`）；
//  3. 删掉受控根之后一点不剩（目录消失、父目录 ACL 前后逐字相同）；
//  4. **因果对照**：受控根与对照目录同为 Low 完整性，只差一个 ACE——
//     授上就写得进、撤掉立刻又被拒。所以挡住写入的是「受限 SID + 目录 ACL」，
//     不是完整性级别。
//
// 探针踩过的三个坑直接照抄：
//   - `icacls /grant *<SID>` 报 **1332 NONE_MAPPED**（icacls 要名字映射，不认不可
//     映射 SID）→ 必须走 `SetEntriesInAclW` 直传 SID 指针；
//   - `CreateProcessAsUserW` 是 **11 个参数**，漏 `lpCurrentDirectory` 报
//     **267 ERROR_DIRECTORY** → 这里不自己拼那 11 个参数，交给 `os/exec` 的
//     `syscall.SysProcAttr.Token`（Go 的 `CreateProcessAsUser` 包装，`dirp` 必传）；
//   - 本机 shell 自己就跑在 Low 完整性，从 tmp 继承 `Low Mandatory Level:(OI)(CI)(NW)`
//     → 判「外面写不进」时不能归因于完整性级别，测试里给两个目录**显式**打同一标签。
//
// 本实现**比官方窄一层**（必须写在文档与 enforcement 里）：官方把工作区也授给受限
// SID，本仓只授宿主自有的受控根，绝不往用户工作区写 ACE 或 Low 标签。

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

const (
	// writeRestricted 是 CreateRestrictedToken 的标志位：只对**写**访问做受限 SID 检查。
	writeRestricted = 0x0008

	tokenIntegrityLevel = 25

	// seGroupIntegrity 是 TOKEN_MANDATORY_LABEL 里 label 的 attributes。
	seGroupIntegrity = 0x00000020

	seFileObject             = 1
	daclSecurityInformation  = 0x00000004
	labelSecurityInformation = 0x00000010

	setAccessMode    = 2
	revokeAccessMode = 4
	// subContainersAndObjectsInherit 让 ACE 既作用于目录本身也传播给子项
	// （icacls 里显示成 `(OI)(CI)`，没有 `(IO)`）。
	subContainersAndObjectsInherit = 0x3

	trusteeIsSID = 0

	// grantMask 是授给受限 SID 的访问位，逐位对齐探针：icacls 显示为 `(W,D,Rc)`。
	//   FILE_GENERIC_WRITE = 0x120116、DELETE = 0x10000、READ_CONTROL = 0x20000
	grantMask = 0x120116 | 0x00010000 | 0x00020000

	// SYSTEM_MANDATORY_LABEL_ACE 与「不许向上写」标志。
	aceTypeSystemMandatoryLabel      = 0x11
	aceFlagObjectAndContainerInherit = 0x03
	systemMandatoryLabelNoWriteUp    = 0x1

	// lowILSubAuthority 是 Low 完整性的 SID 编号：S-1-16-4096。
	lowILSubAuthority = 4096

	sddlRevision = 1
)

var (
	advapi32 = syscall.NewLazyDLL("advapi32.dll")

	procOpenProcessToken       = advapi32.NewProc("OpenProcessToken")
	procCreateRestrictedToken  = advapi32.NewProc("CreateRestrictedToken")
	procSetTokenInformation    = advapi32.NewProc("SetTokenInformation")
	procGetNamedSecurityInfoW  = advapi32.NewProc("GetNamedSecurityInfoW")
	procSetNamedSecurityInfoW  = advapi32.NewProc("SetNamedSecurityInfoW")
	procSetEntriesInAclW       = advapi32.NewProc("SetEntriesInAclW")
	procConvertSidToStringSidW = advapi32.NewProc("ConvertSidToStringSidW")
	procConvertSDToStringSDW   = advapi32.NewProc("ConvertSecurityDescriptorToStringSecurityDescriptorW")

	procGetCurrentProcess = kernel32Proc("GetCurrentProcess")
	procCloseHandle       = kernel32Proc("CloseHandle")
	procLocalFree         = kernel32Proc("LocalFree")
)

func kernel32Proc(name string) *syscall.LazyProc {
	return syscall.NewLazyDLL("kernel32.dll").NewProc(name)
}

// ---------------- Win32 结构 ----------------

type sidAndAttributes struct {
	Sid        unsafe.Pointer
	Attributes uint32
}

type tokenMandatoryLabel struct {
	Label sidAndAttributes
}

type trusteeW struct {
	MultipleTrustee          unsafe.Pointer
	MultipleTrusteeOperation uint32
	TrusteeForm              uint32
	TrusteeType              uint32
	Name                     *uint16
}

type explicitAccess struct {
	AccessPermissions uint32
	AccessMode        uint32
	Inheritance       uint32
	Trustee           trusteeW
}

// ---------------- SID / 句柄小工具 ----------------

// buildSID 序列化一个 SID 字节串。
//
// 为什么不调 `AllocateAndInitializeSid`：那样拿到的是 LocalAlloc 的内存，所有权要
// 在包之间传，还要配 FreeSid；而且 defer 链一长就容易漏。SID 的二进制布局是稳定的
// （Revision(1) | Count(1) | IdentifierAuthority(6, 大端) | SubAuthority[n] 小端），
// 自己拼一块 Go 内存更简单也更安全（GC 能看见它）。
func buildSID(authority [6]byte, subs ...uint32) []byte {
	b := make([]byte, 8+4*len(subs))
	b[0] = 1 // SID_REVISION
	b[1] = byte(len(subs))
	copy(b[2:8], authority[:])
	for i, sub := range subs {
		binary.LittleEndian.PutUint32(b[8+4*i:], sub)
	}
	return b
}

func lowSID() []byte {
	return buildSID([6]byte{0, 0, 0, 0, 0, 16}, lowILSubAuthority)
}

// randomRestrictedSID 造一个每会话唯一的「不可映射」SID：S-1-5-21-a-b-c-d。
//
// 故意选一个不属于本机任何账号的 SID：它只出现在令牌的 restricting SIDs 与受控根的
// DACL 里，不会意外匹配到某个真实用户/组。`icacls` 对它报 1332 NONE_MAPPED 是正常的
// ——我们从不按名字解析它。
func randomRestrictedSID() ([]byte, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, fmt.Errorf("随机受限 SID: %w", err)
	}
	subs := make([]uint32, 4)
	for i := 0; i < 4; i++ {
		subs[i] = binary.LittleEndian.Uint32(raw[i*4:i*4+4]) | 1
	}
	return buildSID([6]byte{0, 0, 0, 0, 0, 5}, 21, subs[0], subs[1], subs[2], subs[3]), nil
}

// sidToString 用 ConvertSidToStringSidW 取标准 `S-1-5-…` 文本（只读）。
func sidToString(sid []byte) (string, error) {
	if len(sid) == 0 {
		return "", fmt.Errorf("SID 为空")
	}
	var s *uint16
	r, _, e := procConvertSidToStringSidW.Call(uintptr(unsafe.Pointer(&sid[0])), uintptr(unsafe.Pointer(&s)))
	runtime.KeepAlive(sid)
	if r == 0 {
		return "", fmt.Errorf("ConvertSidToStringSidW: %w", win32Err(e))
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(s)))
	return utf16PtrToString(s), nil
}

func utf16PtrToString(p *uint16) string {
	if p == nil {
		return ""
	}
	base := unsafe.Pointer(p)
	n := 0
	for *(*uint16)(unsafe.Add(base, uintptr(n)*2)) != 0 {
		n++
		if n > 4096 {
			break
		}
	}
	return syscall.UTF16ToString(unsafe.Slice((*uint16)(base), n))
}

func win32Err(e error) error {
	if e == nil {
		return syscall.EINVAL
	}
	if errno, ok := e.(syscall.Errno); ok {
		if errno == 0 {
			return syscall.EINVAL
		}
		return errno
	}
	return e
}

// win32ErrCode 取一个错误的 Win32 编号（0 表示不是 Errno）。
func win32ErrCode(e error) uint32 {
	if errno, ok := e.(syscall.Errno); ok {
		return uint32(errno)
	}
	return 0
}

func currentProcessHandle() uintptr {
	r, _, _ := procGetCurrentProcess.Call()
	return r
}

// ---------------- 令牌 ----------------

// openProcessToken 取本进程令牌。受限令牌上 TOKEN_ALL_ACCESS 会被拒，所以这里
// 只请求造受限令牌真正需要的那几个位。
func openProcessToken(access uint32) (syscall.Handle, error) {
	var tok syscall.Handle
	r, _, e := procOpenProcessToken.Call(currentProcessHandle(), uintptr(access),
		uintptr(unsafe.Pointer(&tok)))
	if r == 0 {
		return 0, fmt.Errorf("OpenProcessToken: %w", win32Err(e))
	}
	return tok, nil
}

// restrictedLowToken 把调用者令牌复制成「WRITE_RESTRICTED + Low 完整性」的受限令牌。
func restrictedLowToken(base syscall.Handle, restrictedSID []byte) (syscall.Handle, error) {
	rsa := sidAndAttributes{Sid: unsafe.Pointer(&restrictedSID[0])}
	var tok syscall.Handle
	r, _, e := procCreateRestrictedToken.Call(
		uintptr(base), uintptr(writeRestricted),
		0, 0, // SidsToDisable
		0, 0, // PrivilegesToDelete
		1, uintptr(unsafe.Pointer(&rsa)), // RestrictedSids
		uintptr(unsafe.Pointer(&tok)))
	runtime.KeepAlive(restrictedSID)
	if r == 0 {
		return 0, fmt.Errorf("CreateRestrictedToken(WRITE_RESTRICTED): %w", win32Err(e))
	}
	low := lowSID()
	lbl := tokenMandatoryLabel{Label: sidAndAttributes{
		Sid:        unsafe.Pointer(&low[0]),
		Attributes: seGroupIntegrity,
	}}
	r, _, e = procSetTokenInformation.Call(uintptr(tok), tokenIntegrityLevel,
		uintptr(unsafe.Pointer(&lbl)), uintptr(unsafe.Sizeof(lbl)))
	runtime.KeepAlive(low)
	if r == 0 {
		procCloseHandle.Call(uintptr(tok))
		return 0, fmt.Errorf("SetTokenInformation(TokenIntegrityLevel=Low): %w", win32Err(e))
	}
	return tok, nil
}

// ---------------- DACL / 完整性标签 ----------------

// modifyDACL 直接按 SID 指针对一个路径的 DACL 增/删一个 ACE。
//
// 必须这样做的原因（探针实测）：`icacls /grant *<SID>` 要先把 SID 映射成名字，
// 我们造的受限 SID 不可映射，于是报 **1332 ERROR_NONE_MAPPED**。`SetEntriesInAclW`
// 收的是 TRUSTEE_IS_SID，不做名字解析，所以自造 SID 也能用。
func modifyDACL(path string, sid []byte, mask, mode uint32) error {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	var sd unsafe.Pointer
	var dacl uintptr
	rc, _, _ := procGetNamedSecurityInfoW.Call(
		uintptr(unsafe.Pointer(p)), seFileObject, daclSecurityInformation,
		0, 0, uintptr(unsafe.Pointer(&dacl)), 0,
		uintptr(unsafe.Pointer(&sd)))
	if rc != 0 {
		return fmt.Errorf("GetNamedSecurityInfoW(%s): %w", path, syscall.Errno(rc))
	}
	defer procLocalFree.Call(uintptr(sd))

	ea := explicitAccess{
		AccessPermissions: mask,
		AccessMode:        mode,
		Inheritance:       subContainersAndObjectsInherit,
		Trustee: trusteeW{
			TrusteeForm: trusteeIsSID,
			Name:        (*uint16)(unsafe.Pointer(&sid[0])),
		},
	}
	var newACL uintptr
	rc, _, _ = procSetEntriesInAclW.Call(1, uintptr(unsafe.Pointer(&ea)), dacl,
		uintptr(unsafe.Pointer(&newACL)))
	runtime.KeepAlive(sid)
	if rc != 0 {
		return fmt.Errorf("SetEntriesInAclW(%s): %w", path, syscall.Errno(rc))
	}
	defer procLocalFree.Call(newACL)

	rc, _, _ = procSetNamedSecurityInfoW.Call(uintptr(unsafe.Pointer(p)), seFileObject,
		daclSecurityInformation, 0, 0, newACL, 0)
	if rc != 0 {
		return fmt.Errorf("SetNamedSecurityInfoW(DACL %s): %w", path, syscall.Errno(rc))
	}
	return nil
}

// setLowLabel 把目录的强制完整性标签设成 Low（`NW` = 不许向上写）。
//
// 为什么必须显式做而不是靠继承：宿主的 home 可能是 Medium 标签，而子进程被降成
// Low；那种情况下「写不进」是完整性级别挡的，不是 ACL 挡的——判据就失去意义了。
// 显式打同一标签之后，受控根与对照目录的完整性条件相同，差异只剩 ACL。
func setLowLabel(path string) error {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	low := lowSID()
	sidLen := len(low)
	// ACL 头 8 字节 + ACE 头 4 字节 + mask 4 字节 + SID
	aclSize := 8 + 4 + 4 + sidLen
	acl := make([]byte, aclSize)
	acl[0] = 2 // ACL_REVISION
	binary.LittleEndian.PutUint16(acl[2:], uint16(aclSize))
	binary.LittleEndian.PutUint16(acl[4:], 1) // AceCount
	ace := acl[8:]
	ace[0] = aceTypeSystemMandatoryLabel
	ace[1] = aceFlagObjectAndContainerInherit
	binary.LittleEndian.PutUint16(ace[2:], uint16(4+4+sidLen))
	binary.LittleEndian.PutUint32(ace[4:], systemMandatoryLabelNoWriteUp)
	copy(ace[8:], low)

	rc, _, _ := procSetNamedSecurityInfoW.Call(uintptr(unsafe.Pointer(p)), seFileObject,
		labelSecurityInformation, 0, 0, 0, uintptr(unsafe.Pointer(&acl[0])))
	runtime.KeepAlive(acl)
	if rc != 0 {
		return fmt.Errorf("SetNamedSecurityInfoW(LABEL %s): %w", path, syscall.Errno(rc))
	}
	return nil
}

// sddlOf 读一个路径的安全描述符并转成 SDDL 文本（**只读**，供证据与测试断言用）。
//
// 用 SDDL 而不是 icacls：icacls 的输出受控制台代码页影响（本机跑出来是乱码），而
// SDDL 是稳定 ASCII/UTF-16 文本，「前后逐字相同」可以直接比。
func sddlOf(path string, info uint32) (string, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	var sd unsafe.Pointer
	var dacl, sacl uintptr
	rc, _, _ := procGetNamedSecurityInfoW.Call(
		uintptr(unsafe.Pointer(p)), seFileObject, uintptr(info),
		0, 0,
		uintptr(unsafe.Pointer(&dacl)), uintptr(unsafe.Pointer(&sacl)),
		uintptr(unsafe.Pointer(&sd)))
	if rc != 0 {
		return "", fmt.Errorf("GetNamedSecurityInfoW(0x%x %s): %w", info, path, syscall.Errno(rc))
	}
	defer procLocalFree.Call(uintptr(sd))

	var out *uint16
	var n uint32
	rc, _, _ = procConvertSDToStringSDW.Call(
		uintptr(sd), sddlRevision, uintptr(info),
		uintptr(unsafe.Pointer(&out)), uintptr(unsafe.Pointer(&n)))
	if rc == 0 {
		return "", fmt.Errorf("ConvertSecurityDescriptorToStringSecurityDescriptorW(%s): %w",
			path, syscall.Errno(rc))
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out)))
	return utf16PtrToString(out), nil
}

// ---------------- runner 实现 ----------------

type windowsACLRunner struct {
	token  syscall.Handle
	sid    []byte
	sidStr string
}

func platformSupportsRunner() bool { return true }

func newRunnerImpl() (runnerImpl, error) {
	sid, err := randomRestrictedSID()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRunnerUnavailable, err)
	}
	sidStr, err := sidToString(sid)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRunnerUnavailable, err)
	}
	// TOKEN_QUERY | TOKEN_DUPLICATE | TOKEN_ASSIGN_PRIMARY | TOKEN_ADJUST_DEFAULT |
	// TOKEN_ADJUST_SESSIONID —— CreateRestrictedToken + 降完整性级别需要这几个。
	const access = 0x0008 | 0x0002 | 0x0001 | 0x0080 | 0x0100
	self, err := openProcessToken(access)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRunnerUnavailable, err)
	}
	defer procCloseHandle.Call(uintptr(self))
	tok, err := restrictedLowToken(self, sid)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRunnerUnavailable, err)
	}
	return &windowsACLRunner{token: tok, sid: sid, sidStr: sidStr}, nil
}

func (w *windowsACLRunner) enforcement() Enforcement {
	return EnforcementWindowsACLWriteOnly
}

func (w *windowsACLRunner) describe() string {
	return "windows-acl：WRITE_RESTRICTED 受限令牌 + Low 完整性(S-1-16-4096) + 受控根 DACL 直授 SID " + w.sidStr
}

// sidPointer 给测试/诊断用：受限 SID 的裸指针（生命周期由 w 持有）。
func (w *windowsACLRunner) sidPointer() []byte { return w.sid }

func (w *windowsACLRunner) close() error {
	if w.token != 0 {
		procCloseHandle.Call(uintptr(w.token))
		w.token = 0
	}
	return nil
}

func (w *windowsACLRunner) prepare(root string) error {
	if err := ensureControlledRoot(root); err != nil {
		return err
	}
	if err := modifyDACL(root, w.sid, grantMask, setAccessMode); err != nil {
		return fmt.Errorf("给受控根授写 ACE: %w", err)
	}
	if err := setLowLabel(root); err != nil {
		return fmt.Errorf("给受控根设 Low 完整性标签: %w", err)
	}
	// 子进程的 TMP/TEMP 指到这里（见 confine）：系统 temp 通常不在受控根里，
	// 不重定向的话连 PowerShell 自己都会因为写不了 temp 而失败。
	if err := os.MkdirAll(filepath.Join(root, sandboxRootTempDirName), 0o700); err != nil {
		return fmt.Errorf("建受控根临时目录: %w", err)
	}
	return nil
}

// fileAttributeReparsePoint 是 `GetFileAttributes` 的 FILE_ATTRIBUTE_REPARSE_POINT。
const fileAttributeReparsePoint = 0x00000400

// isReparsePoint 报告一个**已存在**的路径是不是 reparse point（junction / symlink /
// 其它重解析点一律算）。
//
// 为什么不用 `os.Lstat(...).Mode()&os.ModeSymlink`：那个判据在本机不可靠。实测
// （`runner_windows_junction_test.go`）同一个目录 junction，在一种进程里被 Go 报成
// `Lrw-rw-rw-`（ModeSymlink），在另一种进程里被报成 `?rw-rw-rw-`（ModeIrregular，
// 即重解析标签没读出来）。用 ModeSymlink 判会得出"这不是链接"的结论，而
// `GetFileAttributes` 的属性位两种情形下都是 `0x410`（DIRECTORY|REPARSE_POINT）。
// 判机制本身，不判 Go 的类型推断。
func isReparsePoint(path string) (bool, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return false, err
	}
	attrs, err := syscall.GetFileAttributes(p)
	if err != nil {
		return false, err
	}
	return attrs&fileAttributeReparsePoint != 0, nil
}

// ensureControlledRoot 建受控根，并拒绝「这个路径其实是 reparse point/symlink」。
//
// 为什么单独判：ACL 打在**解析后的目标**上。如果受控根是个 junction 指到用户工作区，
// 给 root 授 ACE 就等于给用户工作区授 ACE——那正是本单的硬红线。
func ensureControlledRoot(root string) error {
	if st, err := os.Lstat(root); err == nil {
		reparse, rerr := isReparsePoint(root)
		if rerr != nil {
			return fmt.Errorf("%w: 读 %s 的属性: %v", ErrRunnerRootInvalid, root, rerr)
		}
		if reparse || st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s 是符号链接/reparse point，拒绝在其上打 ACL",
				ErrRunnerRootInvalid, root)
		}
		if !st.IsDir() {
			return fmt.Errorf("%w: %s 已存在且不是目录", ErrRunnerRootInvalid, root)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	// **父目录也不许是 reparse point。**
	//
	// 为什么单列一条：ACL 与 Low 标签打在**解析后的目标**上，而 `<home>/sandbox-roots`
	// 这个父目录在本机是**可被同用户任何进程预先创建**的。如果它先被造成一个指向
	// 用户目录树的 junction，`MkdirAll(root)` 就会把受控根建在那个目标里，随后
	// `modifyDACL`/`setLowLabel` 就落到了用户的目录上——正好是本单硬红线
	// （"绝不往用户/共享工作区写驻留 ACE 或 Low 标签"）。
	//
	// 下面那条 canonical 父子关系检查挡不住它：它是拿**解析后**的父目录去比解析后的
	// 受控根，只要根确实建在解析后的父目录里，它就恒真。所以必须在建之前先看父目录本身。
	//
	// 只查紧邻的父目录，不往上追：`<home>` 自己可能就是被重定向的 junction，
	// 那是用户的合法布局，不应该因此把 runner 判死。
	if parent := filepath.Dir(root); parent != root {
		if _, err := os.Lstat(parent); err == nil {
			reparse, rerr := isReparsePoint(parent)
			if rerr != nil {
				return fmt.Errorf("%w: 读受控根父目录 %s 的属性: %v", ErrRunnerRootInvalid, parent, rerr)
			}
			if reparse {
				return fmt.Errorf("%w: 受控根父目录 %s 是符号链接/reparse point，"+
					"在那里建根会把 ACE 与 Low 标签打到它解析后的目标上",
					ErrRunnerRootInvalid, parent)
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("建受控根 %s: %w", root, err)
	}
	// 父目录与受控根的 canonical 结果必须仍是父子关系：路径中间若有链接把受控根
	// 指到别处（例如用户工作区），这里就当场拒绝，而不是把 ACL 打过去。
	parent, err := canonicalPath(filepath.Dir(root))
	if err != nil {
		return fmt.Errorf("%w: 解析受控根父目录: %v", ErrRunnerRootInvalid, err)
	}
	canonical, err := canonicalPath(root)
	if err != nil {
		return fmt.Errorf("%w: 解析受控根: %v", ErrRunnerRootInvalid, err)
	}
	if !withinRoot(parent, canonical) {
		return fmt.Errorf("%w: %s 解析后成了 %s，越出父目录 %s（中间有 reparse point/symlink）",
			ErrRunnerRootInvalid, root, canonical, parent)
	}
	return nil
}

func (w *windowsACLRunner) teardown(root string) error {
	if strings.TrimSpace(root) == "" {
		return fmt.Errorf("%w: 受控根为空", ErrRunnerRootInvalid)
	}
	if _, err := os.Lstat(root); os.IsNotExist(err) {
		return nil
	}
	if err := os.RemoveAll(root); err == nil {
		return nil
	} else if _, statErr := os.Lstat(root); os.IsNotExist(statErr) {
		// 并发下别的 goroutine 已经删干净了，RemoveAll 的报错不算失败。
		return nil
	} else {
		// 兜底：先撤掉我们自己加的 ACE 再删。只碰这个受控根，绝不往上走一层。
		if revErr := modifyDACL(root, w.sid, 0, revokeAccessMode); revErr != nil {
			return fmt.Errorf("删受控根 %s 失败(%v)，撤 ACE 也失败(%v)", root, err, revErr)
		}
		if retryErr := os.RemoveAll(root); retryErr != nil {
			return fmt.Errorf("撤 ACE 后仍删不掉受控根 %s: %w", root, retryErr)
		}
		return nil
	}
}

func (w *windowsACLRunner) confine(cmd *exec.Cmd, root string) error {
	if w.token == 0 {
		return fmt.Errorf("%w: 受限令牌未创建", ErrRunnerUnavailable)
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// os/exec 看见 Token 非 0 就走 CreateProcessAsUser（11 个参数，含
	// lpCurrentDirectory），所以 267 ERROR_DIRECTORY 那个坑不会再踩。
	cmd.SysProcAttr.Token = syscall.Token(w.token)
	cmd.Dir = root
	cmd.Env = tempEnv(cmd.Env, filepath.Join(root, sandboxRootTempDirName))
	return nil
}
