//go:build windows

package bash

// GO.6 先红：官方 `bash.graceMs`（默认 3000）是「先请求退出 → 等宽限 → 再强杀」的
// 升级链；旧宿主是一次性 TerminateJobObject，宽限内会自己收尾的进程也会被当场杀死，
// 退出码记成强杀值（1），而不是它自己的退出码。
//
// 本用例起一个**真实 Windows 进程**（re-exec 本测试二进制），它忽略第一次
// CTRL_BREAK、1 秒后以 42 退出：
//   - 旧实现：立即强杀 → 退出码 1、耗时 ~毫秒；
//   - 新实现：宽限内自行退出 → 退出码 42、耗时 ≈1s，结局记为「宽限内退出」。

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// graceHelperEnv 让同一个测试二进制以 helper 进程身份运行。
const graceHelperEnv = "DSHD_TEST_GRACE_HELPER"

// TestGraceHelperProcess 是 helper 进程本体（被 re-exec 时跑；普通 go test 直接跳过）。
//
// 语义刻意做成「**忽略第一次终止请求**，1 秒后才退出（退出码 42）」：
//   - 计时起点必须是**收到请求的时刻**，不能是 READY 时刻——否则全量并行测试
//     一忙，"父进程还没走到 stop()，helper 自己先睡了 1s 退出"，测的就不是宽限；
//   - 退出码 42 与"被强杀"的 1 区分开，断言才咬得住"宽限内退出"。
func TestGraceHelperProcess(t *testing.T) {
	if os.Getenv(graceHelperEnv) != "1" {
		t.Skip("helper 进程专用")
	}
	// 返回 TRUE = 已处理：第一次 CTRL_BREAK 不退出，1 秒后才以 42 退出。
	handler := syscall.NewCallback(func(ctrlType uint32) uintptr {
		fmt.Printf("HELPER: ignoring ctrl event %d\n", ctrlType)
		go func() {
			time.Sleep(time.Second)
			fmt.Println("HELPER: exiting 42")
			os.Exit(42)
		}()
		return 1
	})
	if ok, _, err := procSetConsoleCtrlHandler.Call(handler, 1); ok == 0 {
		fmt.Println("HELPER: SetConsoleCtrlHandler 失败:", err)
		os.Exit(3)
	}
	fmt.Println("HELPER: READY")
	// 装好 handler 之后一直等请求（60s 是兜底：等不到就说明投递失败，退出码 9 能看出来）。
	time.Sleep(60 * time.Second)
	fmt.Println("HELPER: 60s 内没有收到宽限终止请求")
	os.Exit(9)
}

var procSetConsoleCtrlHandler = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetConsoleCtrlHandler")

// startGraceHelper 起 helper，并返回它的 processControl 与退出通道。
func startGraceHelper(t *testing.T) (*exec.Cmd, *processControl, <-chan struct{}, *helperTrace) {
	t.Helper()
	trace := &helperTrace{}
	cmd := exec.Command(os.Args[0], "-test.run=^TestGraceHelperProcess$")
	cmd.Env = append(os.Environ(), graceHelperEnv+"=1")
	// 独立进程组：CTRL_BREAK 只打这棵树，不会连带测试进程自己。
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("起 helper 失败: %v", err)
	}
	control, err := attachProcessTree(cmd)
	if err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("建立进程树归属失败: %v", err)
	}
	exited := make(chan struct{})
	go func() {
		// 不能持 trace.mu 等 cmd.Wait()：helper 活着的整个窗口里，
		// 读输出那条 goroutine 也要拿这把锁，持锁等退出会把 READY 卡住。
		waitErr := cmd.Wait()
		trace.mu.Lock()
		trace.waitErr = waitErr
		trace.mu.Unlock()
		close(exited)
	}()
	// 等 helper 真正装好 console handler 再发终止请求，否则默认处置会直接杀它。
	ready := make(chan string, 16)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			trace.record(line)
			ready <- line
		}
		close(ready)
	}()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case line, ok := <-ready:
			if !ok {
				t.Fatalf("helper 还没 READY 就退出了：%s", trace.dump())
			}
			if line == "HELPER: READY" {
				return cmd, control, exited, trace
			}
		case <-deadline:
			_ = cmd.Process.Kill()
			t.Fatalf("等 helper READY 超时：%s", trace.dump())
		}
	}
}

// helperTrace 记下 helper 的真实输出与 Wait 错误：失败信息里必须能看见
// 「它到底怎么退的」，否则退出码异常时只能靠猜。
type helperTrace struct {
	mu      sync.Mutex
	lines   []string
	waitErr error
}

func (h *helperTrace) record(line string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lines = append(h.lines, line)
}

func (h *helperTrace) dump() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return fmt.Sprintf("输出=%q waitErr=%v", h.lines, h.waitErr)
}

// 先红：宽限内自行退出的进程必须保留自己的退出码，而不是被强杀。
//
// 走的是生产形状（管道式会话就是 `stopWithGrace(grace, waitDone)`）：
// 退出通道来自 `cmd.Wait()`，所以"宽限内退出"的判定与退出码的读取不会打架——
// 若改用 `control.stop()` 的身份轮询兜底，轮询可能在进程正在退出的瞬间判死，
// 随后 release 的 KILL_ON_JOB_CLOSE 会把退出码改写成强杀值（测试里实测过）。
func TestStopHonorsGraceBeforeEscalation(t *testing.T) {
	cmd, control, exited, trace := startGraceHelper(t)
	defer control.close()

	start := time.Now()
	out, err := control.stopWithGrace(DefaultGrace, exited)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("停止失败（%v 后）: %v；%s", elapsed, err, trace.dump())
	}
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		t.Fatal("停止后 helper 仍未退出")
	}
	rc := cmd.ProcessState.ExitCode()
	if rc != 42 {
		t.Fatalf("宽限内自行退出的进程必须保留退出码 42，实际 %d（说明被当场强杀）；%s", rc, trace.dump())
	}
	if !out.Graceful || out.Escalated || out.Delivery != DeliveryCtrlBreak {
		t.Fatalf("结局必须是「宽限内退出 + CTRL_BREAK 投递」: %+v", out)
	}
	if elapsed < 900*time.Millisecond {
		t.Fatalf("停止只用了 %v：没有给宽限窗口（旧实现立刻强杀）", elapsed)
	}
}
