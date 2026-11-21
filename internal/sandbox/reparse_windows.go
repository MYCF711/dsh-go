//go:build windows

package sandbox

// Windows 上的「把已存在路径解析到最终目标」。
//
// # 为什么不能只靠 filepath.EvalSymlinks
//
// Go 从 1.23 起把 `winsymlink` 的默认值翻成 1（本仓 `go.mod` 声明 `go 1.27.0`，
// 所以拿到的就是新语义）：**目录 junction（IO_REPARSE_TAG_MOUNT_POINT）不再被
// 当成符号链接**——`os.Lstat` 报 `ModeIrregular`（`?rw-rw-rw-`）而不是
// `ModeSymlink`，`filepath.EvalSymlinks` 也不会跟进它（原样返回）。
//
// 对沙盒是致命的：`canonicalPath` 靠 EvalSymlinks 解析，于是
// `workspace\link\evil.txt`（link 是指向工作区外的 junction）被判成
// `pathWorkspace`，`EvaluateFS(write)` 返回 **allow/workspace-write**，
// 而 `FS.Write` 写的是原始路径——操作系统照样跟着 junction 落到工作区外。
// 实测（`TestEvaluateFSRejectsWriteThroughJunction`）：修复前这条写被放行。
//
// 复现判据（Go 源码 `src/os/types_windows.go` 的 `mode()` / `modePreGo1_23()`）：
// 新语义下只有 IO_REPARSE_TAG_SYMLINK 给 ModeSymlink，MOUNT_POINT 落进
// `default: m |= ModeIrregular`；`winsymlink=0` 才会走老逻辑把 MOUNT_POINT
// 也当成 ModeSymlink。
//
// # 修法
//
// 不赌 Go 的 junction 语义：先按 GetFileAttributes 的属性位**检测**路径上有没有
// 重解析点，有就交给 OS 拿最终路径（GetFinalPathNameByHandleW），没有就还是走
// `filepath.EvalSymlinks`——正常路径（绝大多数）行为一个字节都不变。

import (
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

var procGetFinalPathNameByHandleW = kernel32Proc("GetFinalPathNameByHandleW")

// resolveExistingPath 把一条**已存在**的路径解析成它的最终目标（跟随 junction/
// 符号链接/其它重解析点）。非 Windows 由 reparse_other.go 提供 EvalSymlinks 版本。
func resolveExistingPath(path string) (string, error) {
	reparse, err := pathHasReparsePoint(path)
	if err != nil {
		return "", err
	}
	if !reparse {
		// 没有重解析点：既有行为（含 8.3 短名到长名、大小写的规范化）保持不变。
		return filepath.EvalSymlinks(path)
	}
	return finalPathOf(path)
}

// pathHasReparsePoint 报告 path 的**任一已存在前缀**是不是 reparse point。
//
// 只用 GetFileAttributes 的属性位（FILE_ATTRIBUTE_REPARSE_POINT），不看 Go 给的
// FileMode：后者对 junction 不可靠（见文件头）。
func pathHasReparsePoint(path string) (bool, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	vol := filepath.VolumeName(abs)
	rest := strings.TrimPrefix(abs, vol)
	cur := vol
	for _, part := range strings.Split(strings.Trim(rest, `\/`), `\`) {
		if part == "" {
			continue
		}
		if cur == "" || strings.HasSuffix(cur, `\`) {
			cur += part
		} else {
			cur += `\` + part
		}
		reparse, err := isReparsePoint(cur)
		if err != nil {
			// 前缀不存在（后面才是要建的目标）就到此为止：调用方只对已存在路径问了。
			if isNotExistErr(err) {
				return false, nil
			}
			return false, err
		}
		if reparse {
			return true, nil
		}
	}
	return false, nil
}

// finalPathOf 用 GetFinalPathNameByHandleW 拿一条路径的最终目标。
func finalPathOf(path string) (string, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	h, err := syscall.CreateFile(p, 0,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", err
	}
	defer syscall.CloseHandle(h)

	buf := make([]uint16, 512)
	for {
		n, _, callErr := procGetFinalPathNameByHandleW.Call(
			uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0)
		if n == 0 {
			return "", win32Err(callErr)
		}
		if int(n) < len(buf) {
			out := syscall.UTF16ToString(buf[:n])
			// GetFinalPathNameByHandleW(VOLUME_NAME_DOS) 返回 `\\?\C:\…` 或
			// `\\?\UNC\server\share\…`；剥掉前缀才有可比性。
			switch {
			case strings.HasPrefix(out, `\\?\UNC\`):
				out = `\\` + out[len(`\\?\UNC\`):]
			case strings.HasPrefix(out, `\\?\`):
				out = out[len(`\\?\`):]
			}
			return filepath.Clean(out), nil
		}
		// 缓冲区不够：n 是需要的字符数（含结尾 0）。
		buf = make([]uint16, n+1)
	}
}

// isNotExistErr 判断「这个前缀不存在」类错误。GetFileAttributes 对不存在的路径
// 返回 ERROR_FILE_NOT_FOUND 或 ERROR_PATH_NOT_FOUND。
func isNotExistErr(err error) bool {
	errno, ok := err.(syscall.Errno)
	if !ok {
		return false
	}
	return errno == syscall.ERROR_FILE_NOT_FOUND || errno == syscall.ERROR_PATH_NOT_FOUND
}

// 下面三个是给 internal/sandbox 之外的调用方用的薄包装：判据只此一份，
// 别的包（cmd/dshd、internal/creation …）不要再按 `os.ModeSymlink` 自己判一遍。
//
// 为什么要导出：Go ≥1.23 的 `winsymlink=1` 语义下目录 junction（MOUNT_POINT）
// 既不是 `ModeSymlink` 也不是目录，`os.Lstat` 报 `ModeIrregular`；凡是
// "拒绝链接"的安全判据只要写成 `Mode()&os.ModeSymlink != 0` 就会在 junction 上
// 漏掉。`GetFileAttributes` 的 `FILE_ATTRIBUTE_REPARSE_POINT` 属性位在新旧语义下
// 都是 `0x410`，是唯一稳定的判据（见 isReparsePoint 的注释）。

// IsReparsePoint 报告 path **本身**是不是 reparse point（junction/符号链接/
// 其它重解析点）。path 不存在时返回错误（不是 (false, nil)）。
//
// 用途：拒绝"最后一段是链接"的放行判据（例如"包根必须是真实目录"）。
func IsReparsePoint(path string) (bool, error) { return isReparsePoint(path) }

// PathHasReparsePoint 报告 path 的**任一已存在前缀**是不是 reparse point，
// 即"这条路径是不是经链接进去的"。
//
// 用途：`root\junction\file.txt` 的 `os.Lstat` 是一个正常常规文件，最后一段的
// 判据看不见中间那条 junction；要判"整条路径没有链接"只能用这个。
func PathHasReparsePoint(path string) (bool, error) { return pathHasReparsePoint(path) }

// ResolveExistingPath 把一条**已存在**的路径解析到最终目标（跟随 junction/
// 符号链接/其它重解析点）。没有 reparse point 时就是 filepath.EvalSymlinks，
// 普通路径行为一字节不变。
//
// 用途：边界复核（"解析后还在不在根里"）——Go ≥1.23 的 EvalSymlinks 不跟
// 目录 junction，只靠它会让越界路径看起来还在根内。
func ResolveExistingPath(path string) (string, error) { return resolveExistingPath(path) }
