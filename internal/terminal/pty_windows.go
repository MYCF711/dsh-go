//go:build windows

package terminal

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// jobObjectLimitKillOnJobClose 让 Job 句柄一关就收掉整棵树（与执行层同一语义）。
const jobObjectLimitKillOnJobClose = 0x00002000

// procUpdateProcThreadAttribute 是 kernel32 的 UpdateProcThreadAttribute。
var procUpdateProcThreadAttribute = windows.NewLazySystemDLL("kernel32.dll").NewProc("UpdateProcThreadAttribute")

// updatePseudoConsoleAttribute 把 **HPCON 值本身**写进属性列表。
//
// 为什么不直接调 x/sys 的 `attrs.Update`：它的 value 形参是 `unsafe.Pointer`，
// 而 `PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE` 要的是 HPCON **值**（一个句柄），
// 不是 Go 指针；强转 `unsafe.Pointer(handle)` 会被 `go vet` 判成
// "possible misuse of unsafe.Pointer"。按 Win32 原型直接调，lpValue 就是 PVOID，
// 传句柄值才是正确语义（微软 EchoCon 示例也是 `UpdateProcThreadAttribute(..., hPC, sizeof(hPC), ...)`）。
func updatePseudoConsoleAttribute(list *windows.ProcThreadAttributeList, console windows.Handle) error {
	r1, _, e1 := procUpdateProcThreadAttribute.Call(
		uintptr(unsafe.Pointer(list)),
		0,
		uintptr(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE),
		uintptr(console),
		unsafe.Sizeof(console),
		0,
		0,
	)
	if r1 == 0 {
		return e1
	}
	return nil
}

// windowsPTY 是 ConPTY 会话的真句柄。
//
// 三条管道各管一件事：
//
//	inWrite  宿主 → 伪控制台输入（用户键盘）
//	outRead  伪控制台输出 → 宿主（屏幕字节流，含 ANSI）
//	job      整棵进程树的归属（KILL_ON_JOB_CLOSE）
type windowsPTY struct {
	// mu 保护下面所有句柄字段：Read 在泵协程、Close 在调用方，两者并发。
	// 句柄值本身没有同步语义，`p.out = nil` 与 `p.out.Read` 并发就是 data race。
	mu      sync.Mutex
	console windows.Handle
	job     windows.Handle
	process windows.Handle
	thread  windows.Handle
	pid     uint32

	in  *os.File
	out *os.File

	// writeMu 串行化整次 Write：长输入按字节切片，多个并发 Input 不能交错，
	// 否则字节顺序会被破坏。
	writeMu sync.Mutex

	closeOnce sync.Once
	closeErr  error
}

// ensureConPTYAvailable 探测 kernel32 是否导出 ConPTY（Win10 1809+ / Server 2019+）。
//
// 必须先探测：x/sys 的懒加载 proc 在缺失时会 panic，而不是返回错误。
func ensureConPTYAvailable() error {
	required := []string{"CreatePseudoConsole", "ResizePseudoConsole", "ClosePseudoConsole"}
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	for _, name := range required {
		if err := kernel32.NewProc(name).Find(); err != nil {
			return fmt.Errorf("%w: 本机 kernel32 没有 %s（需要 Windows 10 1809+ / Server 2019+）：%v",
				ErrUnsupported, name, err)
		}
	}
	return nil
}

// buildCommandLine 把可执行文件与参数拼成 CreateProcess 认的整行（按 Windows 引号规则转义）。
func buildCommandLine(path string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, syscall.EscapeArg(path))
	for _, arg := range args {
		parts = append(parts, syscall.EscapeArg(arg))
	}
	return strings.Join(parts, " ")
}

// openPlatform 用 ConPTY 起一条真伪终端。
func openPlatform(cfg Config) (platformProcess, error) {
	if err := ensureConPTYAvailable(); err != nil {
		return nil, err
	}

	var inRead, inWrite, outRead, outWrite windows.Handle
	// 句柄**不需要**可继承：CreatePseudoConsole 会把它们复制给 conhost，
	// 子进程靠 PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE 拿到控制台，不靠句柄继承。
	// 反过来，可继承句柄会把宿主的管道泄漏给每个子进程。
	if err := windows.CreatePipe(&inRead, &inWrite, nil, 1<<20); err != nil {
		return nil, fmt.Errorf("%w: CreatePipe(输入): %v", ErrStart, err)
	}
	if err := windows.CreatePipe(&outRead, &outWrite, nil, 0); err != nil {
		windows.CloseHandle(inRead)
		windows.CloseHandle(inWrite)
		return nil, fmt.Errorf("%w: CreatePipe(输出): %v", ErrStart, err)
	}
	// 失败路径统一收口：已创建的句柄一个都不留。
	fail := func(format string, args ...any) (platformProcess, error) {
		for _, h := range []windows.Handle{inRead, inWrite, outRead, outWrite} {
			if h != 0 {
				windows.CloseHandle(h)
			}
		}
		return nil, fmt.Errorf(format, args...)
	}

	var console windows.Handle
	if err := windows.CreatePseudoConsole(
		windows.Coord{X: int16(cfg.Cols), Y: int16(cfg.Rows)},
		inRead, outWrite, 0, &console,
	); err != nil {
		return fail("%w: CreatePseudoConsole(%dx%d): %v", ErrUnsupported, cfg.Cols, cfg.Rows, err)
	}
	// 伪控制台自己持有了输入读端/输出写端的副本；父进程不再需要它们。
	windows.CloseHandle(inRead)
	windows.CloseHandle(outWrite)
	inRead, outWrite = 0, 0
	releaseConsole := func() {
		if console != 0 {
			windows.ClosePseudoConsole(console)
		}
	}

	// 属性列表只有一个属性：把伪控制台绑给子进程。
	//
	// **坑**：PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE 的 lpValue 是 **HPCON 值本身**，
	// 不是"HPCON 的地址"。传 &console 会让 CreateProcess 收到一个栈地址当控制台，
	// 结果是"进程起得来、控制台没连上、输出管道永远读不到字节"——
	// 静默失败，最难查的那种。这里必须传 handle 值（见 updatePseudoConsoleAttribute）。
	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		releaseConsole()
		return fail("%w: NewProcThreadAttributeList: %v", ErrStart, err)
	}
	defer attrs.Delete()
	if err := updatePseudoConsoleAttribute(attrs.List(), console); err != nil {
		releaseConsole()
		return fail("%w: 绑定伪控制台属性: %v", ErrStart, err)
	}

	appPtr, err := syscall.UTF16PtrFromString(cfg.Path)
	if err != nil {
		releaseConsole()
		return fail("%w: shell 路径不是合法 UTF-16: %v", ErrStart, err)
	}
	cmdPtr, err := syscall.UTF16PtrFromString(buildCommandLine(cfg.Path, cfg.Args))
	if err != nil {
		releaseConsole()
		return fail("%w: 命令行不是合法 UTF-16: %v", ErrStart, err)
	}
	var cwdPtr *uint16
	if cfg.Cwd != "" {
		if cwdPtr, err = syscall.UTF16PtrFromString(cfg.Cwd); err != nil {
			releaseConsole()
			return fail("%w: 工作目录不是合法 UTF-16: %v", ErrStart, err)
		}
	}

	startup := &windows.StartupInfoEx{}
	startup.StartupInfo.Cb = uint32(unsafe.Sizeof(*startup))
	// **必须**声明"标准句柄由 STARTUPINFO 给定"（这里全为 NULL）：
	// 不声明的话子进程会继承宿主进程的 stdin/stdout（管道/别的控制台），
	// 结果是"console 连上了、shell 也在跑，但它的 I/O 不走伪控制台"——
	// 界面上就是永远没有提示符、敲 `exit` 也退不掉。
	startup.StartupInfo.Flags = windows.STARTF_USESTDHANDLES
	startup.ProcThreadAttributeList = attrs.List()
	var pi windows.ProcessInformation
	if err := windows.CreateProcess(
		appPtr, cmdPtr, nil, nil, false,
		windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT, nil, cwdPtr,
		&startup.StartupInfo, &pi,
	); err != nil {
		releaseConsole()
		return fail("%w: CreateProcess(%s): %v", ErrStart, cfg.Path, err)
	}

	// 进程树归属：Job Object + KILL_ON_JOB_CLOSE。
	// 任何一步失败都要先收掉刚起的进程，不能留孤儿。
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		_ = windows.TerminateProcess(pi.Process, 1)
		windows.CloseHandle(pi.Thread)
		windows.CloseHandle(pi.Process)
		releaseConsole()
		return fail("%w: CreateJobObject: %v", ErrStart, err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.TerminateJobObject(job, 1)
		windows.CloseHandle(job)
		windows.CloseHandle(pi.Thread)
		windows.CloseHandle(pi.Process)
		releaseConsole()
		return fail("%w: SetInformationJobObject: %v", ErrStart, err)
	}
	if err := windows.AssignProcessToJobObject(job, pi.Process); err != nil {
		_ = windows.TerminateJobObject(job, 1)
		windows.CloseHandle(job)
		windows.CloseHandle(pi.Thread)
		windows.CloseHandle(pi.Process)
		releaseConsole()
		return fail("%w: AssignProcessToJobObject: %v", ErrStart, err)
	}

	return &windowsPTY{
		console: console,
		job:     job,
		process: pi.Process,
		thread:  pi.Thread,
		pid:     pi.ProcessId,
		in:      os.NewFile(uintptr(inWrite), "conpty-in"),
		out:     os.NewFile(uintptr(outRead), "conpty-out"),
	}, nil
}

// Read 从伪终端输出管道读（阻塞，EOF 表示控制台已关）。
func (p *windowsPTY) Read(b []byte) (int, error) {
	p.mu.Lock()
	f := p.out
	p.mu.Unlock()
	if f == nil {
		return 0, os.ErrClosed
	}
	return f.Read(b)
}

// conptyInputPace 是“交互短输入”切片之间的节流。
//
// Windows ConPTY 的输入线程把一次 WriteFile 里的字节批量转成 console input
// event；Git/MSYS bash 的 readline 对批量事件消费能力差（T10 实测：一次写
// 250 字节整行会连回显都没有，且后续短命令一起坏掉）。对 17..512 字节的
// 交互输入仍按 1 字节切片、2ms 节流；这不适用于大块粘贴（见 conptyBulkChunk）。
const conptyInputPace = 2 * time.Millisecond

// conptyBulkChunk 是大块粘贴/字节流的有界块长。
//
// T13 实测：整块 4096 字节一次 WriteFile 会被 conhost/readline 截断或极慢；
// 512 字节块在真实 ConPTY `head -c` 字节汇上可逐字节完整到达；块间 1ms
// 足以让 conhost 排空，65536 字节多轮实测约 8-12s（目标 ≤15s）。这是块级
// 吞吐路径，不做无条件每字节 sleep；短写仍然每次检查。
const (
	conptyBulkChunk = 512
	conptyBulkPause = time.Millisecond
)

// Write 把字节写进伪终端输入管道。
//
// 分三档，全部在 Windows PTY 的 Write 内完成，调用方（Session.Input /
// 官方 terminal.input）无感：
//   - ≤16 字节：正常键盘输入，直接 WriteFile（最低延迟）；
//   - 17..512 字节：1 字节有界切片 + 2ms 节流，供 bash readline 逐事件消费；
//   - >512 字节：conptyBulkChunk 大块写，供粘贴/字节流高速传输。
//
// 每一档都检查 WriteFile 的 n/err；短写/错误立即返回带定位信息的错误。
func (p *windowsPTY) Write(b []byte) (int, error) {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()

	p.mu.Lock()
	f := p.in
	p.mu.Unlock()
	if f == nil {
		return 0, ErrClosed
	}
	if len(b) == 0 {
		return 0, nil
	}
	if len(b) <= 16 {
		wn, err := f.Write(b)
		if err != nil {
			return wn, fmt.Errorf("写 ConPTY 输入失败: %w", err)
		}
		if wn != len(b) {
			return wn, fmt.Errorf("写 ConPTY 输入短写（%d/%d）: %w", wn, len(b), io.ErrShortWrite)
		}
		return wn, nil
	}
	if len(b) > 512 {
		total := 0
		for total < len(b) {
			end := total + conptyBulkChunk
			if end > len(b) {
				end = len(b)
			}
			wn, err := f.Write(b[total:end])
			if err != nil {
				return total, fmt.Errorf("写 ConPTY 输入失败（已写 %d/%d 字节）: %w", total+wn, len(b), err)
			}
			if wn != end-total {
				return total + wn, fmt.Errorf("写 ConPTY 输入短写（%d/%d）: %w", wn, end-total, io.ErrShortWrite)
			}
			total = end
			if total < len(b) {
				time.Sleep(conptyBulkPause)
			}
		}
		return total, nil
	}
	total := 0
	for total < len(b) {
		wn, err := f.Write(b[total : total+1])
		if err != nil {
			return total, fmt.Errorf("写 ConPTY 输入失败（已写 %d/%d 字节）: %w", total+wn, len(b), err)
		}
		if wn != 1 {
			return total + wn, fmt.Errorf("写 ConPTY 输入短写（%d/1）: %w", wn, io.ErrShortWrite)
		}
		total += wn
		if total < len(b) {
			time.Sleep(conptyInputPace)
		}
	}
	return total, nil
}

// Resize 真的改伪控制台缓冲尺寸（子进程下一次 ioctl 就能读到）。
func (p *windowsPTY) Resize(cols, rows int) error {
	p.mu.Lock()
	console := p.console
	p.mu.Unlock()
	if console == 0 {
		return ErrClosed
	}
	return windows.ResizePseudoConsole(console, windows.Coord{X: int16(cols), Y: int16(rows)})
}

// PID 是 shell 子进程的 PID。
func (p *windowsPTY) PID() int { return int(p.pid) }

// Kill 终止整棵 Job Object（进程树），幂等。
func (p *windowsPTY) Kill() error {
	p.mu.Lock()
	job := p.job
	p.mu.Unlock()
	if job == 0 {
		return nil
	}
	if err := windows.TerminateJobObject(job, 1); err != nil && err != windows.ERROR_ACCESS_DENIED {
		return fmt.Errorf("终止 PTY 进程树: %w", err)
	}
	return nil
}

// Wait 阻塞到子进程退出并返回真实退出码，然后释放进程/线程句柄。
//
// 句柄只在这里释放：Close 若并发关掉它们，WaitForSingleObject 会踩到复用的句柄。
func (p *windowsPTY) Wait() (int, error) {
	p.mu.Lock()
	process, thread := p.process, p.thread
	p.mu.Unlock()
	if process == 0 {
		return 0, nil
	}
	event, err := windows.WaitForSingleObject(process, windows.INFINITE)
	code := 0
	if err == nil && event == windows.WAIT_OBJECT_0 {
		var raw uint32
		if err := windows.GetExitCodeProcess(process, &raw); err == nil {
			code = int(raw)
		}
	}
	windows.CloseHandle(process)
	if thread != 0 {
		windows.CloseHandle(thread)
	}
	p.mu.Lock()
	p.process, p.thread = 0, 0
	p.mu.Unlock()
	return code, err
}

// Close 回收会话：先杀树，再关管道，最后关伪控制台与 Job 句柄。幂等。
func (p *windowsPTY) Close() error {
	if p == nil {
		return nil
	}
	p.closeOnce.Do(func() {
		// 先在锁里摘下所有句柄，再在锁外关闭：
		// 关句柄可能阻塞（TerminateJobObject / ClosePseudoConsole），
		// 不能在持锁时做，否则泵协程的 Read 会被卡死。
		p.mu.Lock()
		job, in, out, console := p.job, p.in, p.out, p.console
		p.job, p.in, p.out, p.console = 0, nil, nil, 0
		p.mu.Unlock()

		// 1. 先杀整棵树：之后 conhost 一定没有活着的客户端。
		if job != 0 {
			_ = windows.TerminateJobObject(job, 1)
		}
		// 2. 关输入：conhost 立刻看到 EOF。
		if in != nil {
			_ = in.Close()
		}
		// 3. 关输出：读泵立刻返回。
		if out != nil {
			_ = out.Close()
		}
		// 4. 关伪控制台（此时不会阻塞）。
		if console != 0 {
			windows.ClosePseudoConsole(console)
		}
		// 5. 释放 Job 句柄（KILL_ON_JOB_CLOSE 已无意义，树已经空了）。
		if job != 0 {
			p.closeErr = windows.CloseHandle(job)
		}
	})
	return p.closeErr
}
