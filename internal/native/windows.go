//go:build windows

package native

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// commandRunner 只做"启动一个进程"这一件事，测试可注入。
type commandRunner interface {
	Start(name string, args ...string) error
}

type execRunner struct{}

func (execRunner) Start(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// folderDialog 是目录选择器的注入点（真实实现走 IFileOpenDialog）。
type folderDialog interface {
	Pick(ctx context.Context) (string, error)
}

type windowsService struct {
	dialog folderDialog
	run    commandRunner
}

// NewService 返回 Windows 原生实现。
func NewService() Service {
	return &windowsService{dialog: comFolderDialog{}, run: execRunner{}}
}

func (s *windowsService) CanOpen() bool { return true }

func (s *windowsService) FileManager() string { return "explorer" }

func (s *windowsService) PickFolder(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return s.dialog.Pick(ctx)
}

func (s *windowsService) OpenDocument(ctx context.Context, path string) error {
	if err := ValidatePath(path); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return shellExecute("open", path, "")
}

func (s *windowsService) Reveal(ctx context.Context, path string) error {
	if err := ValidatePath(path); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return shellExecute("open", "explorer.exe", "/select,"+`"`+path+`"`)
}

func (s *windowsService) OpenWith(ctx context.Context, path, applicationID string) error {
	if err := ValidatePath(path); err != nil {
		return err
	}
	if strings.TrimSpace(applicationID) == "" {
		return fmt.Errorf("%w: application 为空", ErrInvalidPath)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// applicationID 是 Applications() 返回的 OS 应用标识（Windows 上是可执行
	// 文件路径）。打开前重新确认它存在，避免拿缓存里的旧 ID 启动已卸载程序。
	if _, err := os.Stat(applicationID); err != nil {
		// UWP 应用 ID（如 Microsoft.WindowsNotepad_...!App）不是文件路径：
		// 用 explorer 的 shell:AppsFolder 启动；仍然校验 ID 形状，不猜。
		if strings.Contains(applicationID, "!") {
			return s.run.Start("explorer.exe", `shell:AppsFolder\`+applicationID)
		}
		return fmt.Errorf("%w: 应用不存在：%q", ErrInvalidPath, applicationID)
	}
	return s.run.Start(applicationID, path)
}

func (s *windowsService) Applications(ctx context.Context, path string) ([]Application, error) {
	if err := ValidatePath(path); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// 文件关联枚举是 Shell COM API：必须先初始化 COM（RPC_E_CHANGED_MODE 表示
	// 本线程已经用别的套间初始化过，照样可以继续用）。
	if err := windows.CoInitializeEx(0, coinitApartmentThreaded); err == nil {
		defer windows.CoUninitialize()
	}
	ext := filepath.Ext(path)
	if ext == "" {
		return []Application{}, nil
	}
	return enumerateHandlers(ext, defaultExecutable(ext))
}

// ---------------------------------------------------------------------------
// ShellExecuteEx
// ---------------------------------------------------------------------------

type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         windows.HWND
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     windows.Handle
	lpIDList     unsafe.Pointer
	lpClass      *uint16
	hkeyClass    windows.Handle
	dwHotKey     uint32
	hIcon        windows.Handle
	hProcess     windows.Handle
}

const (
	seeMaskNoCloseProcess = 0x00000040
	seeMaskFlagNoUI       = 0x00000400
	swShowNormal          = 1
)

var procShellExecuteExW = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

func shellExecute(verb, file, parameters string) error {
	verbPtr, err := windows.UTF16PtrFromString(verb)
	if err != nil {
		return err
	}
	filePtr, err := windows.UTF16PtrFromString(file)
	if err != nil {
		return err
	}
	var paramsPtr *uint16
	if parameters != "" {
		paramsPtr, err = windows.UTF16PtrFromString(parameters)
		if err != nil {
			return err
		}
	}
	info := shellExecuteInfo{
		cbSize:       uint32(unsafe.Sizeof(shellExecuteInfo{})),
		fMask:        seeMaskNoCloseProcess | seeMaskFlagNoUI,
		lpVerb:       verbPtr,
		lpFile:       filePtr,
		lpParameters: paramsPtr,
		nShow:        swShowNormal,
	}
	r1, _, callErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info)))
	if r1 == 0 {
		if callErr != nil && callErr != syscall.Errno(0) {
			return fmt.Errorf("ShellExecuteExW(%q): %w", file, callErr)
		}
		return fmt.Errorf("ShellExecuteExW(%q) 失败", file)
	}
	if info.hProcess != 0 {
		_ = windows.CloseHandle(info.hProcess)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 文件关联（SHAssocEnumHandlers + AssocQueryStringW）
// ---------------------------------------------------------------------------

var (
	procSHAssocEnumHandlers = windows.NewLazySystemDLL("shell32.dll").NewProc("SHAssocEnumHandlers")
	procAssocQueryStringW   = windows.NewLazySystemDLL("shlwapi.dll").NewProc("AssocQueryStringW")
)

const (
	assocStrExecutable     = 2
	assocFilterRecommended = 0x1
)

// defaultExecutable 查该扩展当前的默认处理程序可执行文件（小写、去引号）。
func defaultExecutable(ext string) string {
	extPtr, err := windows.UTF16PtrFromString(ext)
	if err != nil {
		return ""
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buf))
	r1, _, _ := procAssocQueryStringW.Call(
		0, // ASSOCF_NONE
		assocStrExecutable,
		uintptr(unsafe.Pointer(extPtr)),
		0,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
	)
	if int32(r1) != 0 {
		return ""
	}
	return strings.ToLower(strings.Trim(windows.UTF16ToString(buf), `" `))
}

// enumerateHandlers 枚举该扩展注册的处理程序。
//
// 用注册表（HKCR/HKCU 的 OpenWithList / OpenWithProgids）而不是
// SHAssocEnumHandlers：后者要求当前线程已初始化 COM 且返回的接口在部分
// 无人值守环境里会 AV（实测 0xc0000005）。注册表是这些处理程序的**真值来源**，
// 与资源管理器"打开方式"列表同源；差异是名字取 exe/ProgID 而不是本地化
// UIName（官方允许 icon=null，本实现同样不猜图标）。
func enumerateHandlers(ext, defaultExe string) ([]Application, error) {
	type candidate struct {
		name string
		path string
	}
	var candidates []candidate
	seen := map[string]bool{}

	add := func(exeName, progID string) {
		path := resolveApplicationPath(exeName, progID)
		if path == "" {
			return
		}
		key := strings.ToLower(path)
		if seen[key] {
			return
		}
		seen[key] = true
		name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		if progID != "" && name == "" {
			name = progID
		}
		candidates = append(candidates, candidate{name: name, path: path})
	}

	// 1) HKCR\<ext>\OpenWithList 的值（a/b/c…）
	addOpenWithList := func(root registry.Key, keyPath string) {
		key, err := registry.OpenKey(root, keyPath, registry.READ)
		if err != nil {
			return
		}
		defer key.Close()
		names, _ := key.ReadValueNames(-1)
		for _, name := range names {
			if strings.EqualFold(name, "MRUList") {
				continue
			}
			exe, _, err := key.GetStringValue(name)
			if err != nil || strings.TrimSpace(exe) == "" {
				// 有些机器把 exe 名放在值名上（a=notepad.exe 的变体）
				exe = name
			}
			add(exe, "")
		}
	}
	addOpenWithList(registry.CLASSES_ROOT, ext+`\OpenWithList`)
	addOpenWithList(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Explorer\FileExts\`+ext+`\OpenWithList`)

	// 2) HKCR\<ext>\OpenWithProgids 的 ProgID
	progKey, err := registry.OpenKey(registry.CLASSES_ROOT, ext+`\OpenWithProgids`, registry.READ)
	if err == nil {
		names, _ := progKey.ReadValueNames(-1)
		progKey.Close()
		for _, progID := range names {
			add("", progID)
		}
	}

	out := make([]Application, 0, len(candidates))
	defaultBase := strings.ToLower(filepath.Base(strings.Trim(defaultExe, `" `)))
	for _, candidate := range candidates {
		out = append(out, Application{
			ID:      candidate.path,
			Name:    candidate.name,
			Default: defaultBase != "" && strings.EqualFold(filepath.Base(candidate.path), defaultBase),
			Icon:    nil,
		})
	}
	// 默认处理程序排最前（界面把它当选中项）。
	sort.SliceStable(out, func(i, j int) bool { return out[i].Default && !out[j].Default })
	return out, nil
}

// resolveApplicationPath 把 OpenWithList 的 exe 名 / ProgID 解析成真实可执行文件。
func resolveApplicationPath(exeName, progID string) string {
	if progID != "" {
		key, err := registry.OpenKey(registry.CLASSES_ROOT, progID+`\shell\open\command`, registry.READ)
		if err == nil {
			command, _, _ := key.GetStringValue("")
			key.Close()
			if exe := firstCommandToken(command); exe != "" && fileExists(exe) {
				return exe
			}
		}
	}
	exeName = strings.Trim(strings.TrimSpace(exeName), `"`)
	if exeName == "" {
		return ""
	}
	// 已经是绝对路径
	if filepath.IsAbs(exeName) && fileExists(exeName) {
		return exeName
	}
	// App Paths（HKLM/HKCU）——Notepad3、msedge 这类第三方/系统应用在这里。
	for _, root := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
		if key, err := registry.OpenKey(root, `Software\Microsoft\Windows\CurrentVersion\App Paths\`+exeName, registry.READ); err == nil {
			if path, _, err := key.GetStringValue(""); err == nil && fileExists(path) {
				key.Close()
				return path
			}
			key.Close()
		}
	}
	// HKCR\Applications\<exe>\shell\open\command
	if key, err := registry.OpenKey(registry.CLASSES_ROOT, `Applications\`+exeName+`\shell\open\command`, registry.READ); err == nil {
		command, _, _ := key.GetStringValue("")
		key.Close()
		if exe := firstCommandToken(command); exe != "" && fileExists(exe) {
			return exe
		}
	}
	// System32 / Windows
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	for _, candidate := range []string{
		filepath.Join(root, "System32", exeName),
		filepath.Join(root, exeName),
	} {
		if fileExists(candidate) {
			return candidate
		}
	}
	return ""
}

// firstCommandToken 从 `"C:\pathpp.exe" --arg` 里取可执行文件路径。
func firstCommandToken(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	if command[0] == '"' {
		if end := strings.Index(command[1:], `"`); end >= 0 {
			return command[1 : 1+end]
		}
		return command
	}
	if idx := strings.IndexAny(command, " 	"); idx >= 0 {
		return command[:idx]
	}
	return command
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// comCall 调 vtable 上的一个方法（this 作为第一个参数）。
func comCall(this unsafe.Pointer, index uintptr, args ...uintptr) (uintptr, error) {
	if this == nil {
		return 0, fmt.Errorf("COM 对象是 nil")
	}
	vtbl := *(*unsafe.Pointer)(this)
	method := *(*uintptr)(unsafe.Add(vtbl, index*unsafe.Sizeof(uintptr(0))))
	callArgs := make([]uintptr, 0, len(args)+1)
	callArgs = append(callArgs, uintptr(this))
	callArgs = append(callArgs, args...)
	r1, _, _ := syscall.SyscallN(method, callArgs...)
	if int32(r1) < 0 {
		return r1, syscall.Errno(r1)
	}
	return r1, nil
}

func comRelease(this unsafe.Pointer) {
	if this == nil {
		return
	}
	_, _ = comCall(this, 2) // IUnknown::Release
}

// ---------------------------------------------------------------------------
// IFileOpenDialog（目录选择）
// ---------------------------------------------------------------------------

var (
	clsidFileOpenDialog = windows.GUID{Data1: 0xDC1C5A9C, Data2: 0xE88A, Data3: 0x4DDE,
		Data4: [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7}}
	iidFileOpenDialog = windows.GUID{Data1: 0xD57C7288, Data2: 0xD4AD, Data3: 0x40BE,
		Data4: [8]byte{0x8A, 0x6A, 0x95, 0x9D, 0xDD, 0x32, 0xD9, 0x70}}
	procCoCreateInstance = windows.NewLazySystemDLL("ole32.dll").NewProc("CoCreateInstance")
)

const (
	coinitApartmentThreaded = 0x2
	clsctxInprocServer      = 0x1
	fosPickFolders          = 0x00000020
	fosForceFileSystem      = 0x00000040
	fosPathMustExist        = 0x00000800
	sigdnFileSysPath        = 0x80058000
	errorCancelled          = 0x800704C7
)

type comFolderDialog struct{}

// Pick 在**锁定的 OS 线程**上初始化 COM 并弹目录选择框。
//
// 取消语义：IFileOpenDialog 没有编程式取消（Show 是模态的），所以：
//   - ctx 在 Show 之前取消 → 直接返回 ctx.Err()，不弹框；
//   - 用户在框里取消 → Show 返回 ERROR_CANCELLED → ErrCancelled（官方 null）；
//   - Show 期间 ctx 取消不会被观察（与官方 native picker 一致：窗口关闭后
//     才返回）。这是显式差异，登记在 RESULT。
func (comFolderDialog) Pick(ctx context.Context) (string, error) {
	type result struct {
		path string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := windows.CoInitializeEx(0, coinitApartmentThreaded); err != nil {
			done <- result{err: fmt.Errorf("CoInitializeEx: %w", err)}
			return
		}
		defer windows.CoUninitialize()
		path, err := pickFolderOnThread(ctx)
		done <- result{path: path, err: err}
	}()
	select {
	case <-ctx.Done():
		// 不能安全关闭另一个线程上的模态框；等它自己关（与官方一致）。
		<-done
		return "", ctx.Err()
	case res := <-done:
		return res.path, res.err
	}
}

func pickFolderOnThread(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var dialog unsafe.Pointer
	r1, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidFileOpenDialog)),
		0,
		clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidFileOpenDialog)),
		uintptr(unsafe.Pointer(&dialog)),
	)
	if int32(r1) != 0 || dialog == nil {
		return "", fmt.Errorf("CoCreateInstance(FileOpenDialog) 失败: 0x%x", r1)
	}
	defer comRelease(dialog)
	if _, err := comCall(dialog, 9, uintptr(fosPickFolders|fosForceFileSystem|fosPathMustExist)); err != nil {
		return "", fmt.Errorf("IFileDialog::SetOptions: %w", err)
	}
	hr, _ := comCall(dialog, 3, 0) // Show(nil)
	if uint32(hr) == uint32(errorCancelled) {
		return "", ErrCancelled
	}
	if int32(hr) != 0 {
		return "", fmt.Errorf("IFileOpenDialog::Show 失败: 0x%x", hr)
	}
	var item unsafe.Pointer
	hr, _ = comCall(dialog, 20, uintptr(unsafe.Pointer(&item))) // GetResult
	if int32(hr) != 0 || item == nil {
		return "", fmt.Errorf("IFileOpenDialog::GetResult 失败: 0x%x", hr)
	}
	defer comRelease(item)
	var name *uint16
	hr, _ = comCall(item, 5, sigdnFileSysPath, uintptr(unsafe.Pointer(&name))) // GetDisplayName
	if int32(hr) != 0 || name == nil {
		return "", fmt.Errorf("IShellItem::GetDisplayName 失败: 0x%x", hr)
	}
	defer windows.CoTaskMemFree(unsafe.Pointer(name))
	path := windows.UTF16ToString(unsafe.Slice(name, windows.MAX_LONG_PATH))
	if path == "" {
		return "", fmt.Errorf("目录选择器返回了空路径")
	}
	return path, nil
}
