package bash

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"dshgo/internal/spi"
)

// processIdentity 全部来自 OS 查询；Active=false 与查询错误不是一回事。
type processIdentity struct {
	PID            int
	Image          string
	CreationID     string
	Active         bool
	ProcessTree    string
	ProcessGroupID int
}

type processTreeNative interface {
	// graceful 投递一次**宽限终止请求**（Windows: CTRL_BREAK 到进程组；
	// Unix: SIGTERM 到进程组）。返回投递方式，投递不了就是错误——
	// 调用方据此记录"请求根本没送到"，而不是假装礼数已尽。
	graceful() (string, error)
	terminate() error
	release() error
	terminateOnClose(validRoot bool) bool
}

const (
	// DefaultGrace 是官方 `bash.graceMs` 的默认值（3000ms）。
	DefaultGrace = 3 * time.Second
	// gracePollInterval 是没有退出通道时的身份轮询间隔（PTY 之外的兜底）。
	gracePollInterval = 20 * time.Millisecond

	// DeliveryCtrlBreak / DeliverySIGTERM / DeliveryCtrlCInput 是三种宽限投递方式；
	// DeliveryUnavailable 表示宿主连请求都投不出去（例如 dshd 没有控制台）。
	DeliveryCtrlBreak   = "ctrl-break"
	DeliverySIGTERM     = "sigterm"
	DeliveryCtrlCInput  = "ctrl-c-input"
	DeliveryUnavailable = "unavailable"
)

// StopOutcome 是一次停止的**结局**：宽限内自行退出 vs 强杀必须能区分开——
// 前者保留命令自己的退出码（trap 收尾、写盘、上报都算数），后者才是被杀。
type StopOutcome struct {
	// Grace 是本次使用的宽限预算。
	Grace time.Duration
	// Waited 是实际等待时长（宽限窗口内进程退出就提前结束）。
	Waited time.Duration
	// Delivery 是宽限请求的投递方式（ctrl-break / sigterm / unavailable）。
	Delivery string
	// Graceful 表示 root 在宽限内自行退出（没有强杀）。
	Graceful bool
	// Escalated 表示宽限用完仍存活，走了强杀路径。
	Escalated bool
}

// String 是给日志/工具结果看的一行结局说明。
func (o StopOutcome) String() string {
	switch {
	case o.Graceful:
		return fmt.Sprintf("宽限内退出（%s，等待 %s）", o.Delivery, o.Waited.Round(time.Millisecond))
	case o.Escalated:
		return fmt.Sprintf("强杀（%s，宽限 %s 用完）", o.Delivery, o.Grace.Round(time.Millisecond))
	default:
		return "未停止"
	}
}

// processControl 的锁同时串行化 Stop/Close 与测试中的身份快照。
// native 的句柄只服务本次启动的树，绝不按镜像名或全局枚举杀进程。
type processControl struct {
	mu       sync.Mutex
	identity processIdentity
	native   processTreeNative
	closed   bool
	// stopping 非 nil 时表示已有一次停止在进行：并发 Stop 等它收尾后
	// 按"已结束"拒绝，而不是重复投递/重复强杀。
	stopping chan struct{}
	stopOut  StopOutcome
	stopped  bool
}

func (c *processControl) metadata() processIdentity {
	if c == nil {
		return processIdentity{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.identity
}

func sameProcessImage(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func validateProcessIdentity(want, got processIdentity) error {
	if !got.Active {
		return spi.ErrJobProcessExited
	}
	if want.PID != got.PID || want.CreationID == "" || want.CreationID != got.CreationID ||
		want.Image == "" || !sameProcessImage(want.Image, got.Image) {
		return fmt.Errorf("%w: pid=%d", spi.ErrJobProcessReused, want.PID)
	}
	return nil
}

// stop 用默认宽限停止（默认 3000ms，官方 `bash.graceMs`）。
func (c *processControl) stop() error {
	_, err := c.stopWithGrace(DefaultGrace, nil)
	return err
}

// stopWithGrace 是「先礼后兵」的升级链：
//
//	身份复核 → 投递宽限终止请求 → 等 grace（exited 或身份轮询）→ 仍存活才强杀
//
// 两条结局都返回 StopOutcome（并发安全地记在 processControl 上）：
// 宽限内退出**不**放弃 Job 归属——收尾仍走 native.release()，Windows 的
// KILL_ON_JOB_CLOSE 会把残留子进程一起收掉，只是 root 自己已经先走了。
func (c *processControl) stopWithGrace(grace time.Duration, exited <-chan struct{}) (StopOutcome, error) {
	if c == nil || c.native == nil {
		return StopOutcome{}, spi.ErrProcessTreeUnsafe
	}
	if grace <= 0 {
		grace = DefaultGrace
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return StopOutcome{}, spi.ErrJobFinished
	}
	if c.stopping != nil {
		inFlight := c.stopping
		c.mu.Unlock()
		<-inFlight
		return StopOutcome{}, spi.ErrJobFinished
	}
	got, err := queryProcessIdentity(c.identity.PID)
	if err != nil {
		c.mu.Unlock()
		if errors.Is(err, spi.ErrJobProcessExited) {
			return StopOutcome{}, err
		}
		return StopOutcome{}, fmt.Errorf("%w: 查询 root PID %d: %v", spi.ErrProcessTreeUnsafe, c.identity.PID, err)
	}
	if err := validateProcessIdentity(c.identity, got); err != nil {
		c.mu.Unlock()
		return StopOutcome{}, err
	}
	inFlight := make(chan struct{})
	c.stopping = inFlight
	rootPID := c.identity.PID
	c.mu.Unlock()

	out := StopOutcome{Grace: grace, Delivery: DeliveryUnavailable}
	if delivery, deliveryErr := c.native.graceful(); deliveryErr == nil {
		out.Delivery = delivery
	}
	start := time.Now()
	if waitForExit(exited, rootPID, grace) {
		out.Graceful = true
	} else {
		// 宽限用完仍存活：强杀仍走原路径（Windows: TerminateJobObject）。
		out.Escalated = true
		if err := c.native.terminate(); err != nil {
			out.Waited = time.Since(start)
			c.finishStop(inFlight, out)
			_ = c.native.release()
			return out, fmt.Errorf("终止已核对的进程树: %w", err)
		}
	}
	out.Waited = time.Since(start)
	c.finishStop(inFlight, out)
	if err := c.native.release(); err != nil {
		return out, fmt.Errorf("进程树已终止，释放归属句柄: %w", err)
	}
	return out, nil
}

// finishStop 记下结局并放开并发停止的等待者。
func (c *processControl) finishStop(inFlight chan struct{}, out StopOutcome) {
	c.mu.Lock()
	c.closed = true
	c.stopOut = out
	c.stopped = true
	if c.stopping == inFlight {
		c.stopping = nil
	}
	c.mu.Unlock()
	close(inFlight)
}

// outcome 返回上次停止的结局（没停过 ok=false）。
func (c *processControl) outcome() (StopOutcome, bool) {
	if c == nil {
		return StopOutcome{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopOut, c.stopped
}

// waitForExit 在宽限窗口内等 root 退出；有退出通道就用通道，没有就轮询身份。
// 返回 true 表示宽限内退出（正常收尾），false 表示需要强杀。
func waitForExit(exited <-chan struct{}, rootPID int, grace time.Duration) bool {
	timer := time.NewTimer(grace)
	defer timer.Stop()
	if exited != nil {
		select {
		case <-exited:
			return true
		case <-timer.C:
			// 收尾竞态：刚好在窗口边缘退出也算宽限内。
			select {
			case <-exited:
				return true
			default:
				return !processAlive(rootPID)
			}
		}
	}
	ticker := time.NewTicker(gracePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if !processAlive(rootPID) {
				return true
			}
		case <-timer.C:
			return !processAlive(rootPID)
		}
	}
}

func processAlive(pid int) bool {
	got, err := queryProcessIdentity(pid)
	if err != nil {
		return false
	}
	return got.Active
}

// close 是 Dispose 的强制收工。Windows 的 Job 句柄不依赖 PID，可在 root 已退出时
// 仍终止该 Job 里残留的自有子进程；Unix 只有 root 身份仍匹配时才对 PGID 发信号，
// 避免一个已经消失的 PGID 被复用后误伤别人。
func (c *processControl) close() {
	if c == nil || c.native == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	valid := false
	if got, err := queryProcessIdentity(c.identity.PID); err == nil {
		valid = validateProcessIdentity(c.identity, got) == nil
	}
	if c.native.terminateOnClose(valid) {
		_ = c.native.terminate()
	}
	_ = c.native.release()
	c.closed = true
}
