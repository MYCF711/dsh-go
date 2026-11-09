package pluginmgr

// 安装作业：官方 `pluginManager.installBundle/updateBundle` 的 `requestId`
// 语义（`waitForInstall` / `cancelInstall`）落到这里。
//
// 真实性边界：
//   - 作业登记的是一次**真实**的安装/切版事务，不是一句"已开始"；
//   - 取消只在事务的检查点生效（开始、复制每个文件、staged→installed、
//     installed→committing 之前）。一旦进入 committing，状态正在落盘，
//     取消返回 `too-late`，绝不把"没停下"报成 `cancelled`；
//   - 取消后由既有事务回滚把已复制的版本目录清掉、journal 收干净，
//     `state.json` 不变——重启 reconcile 不会看到残缺事务；
//   - 作业收工即从登记表移除：官方同样不保留已完成的结果（`waitForInstall`
//     对未知 id 回 null）。

import (
	"errors"
	"sync"
	"sync/atomic"
)

// errInstallCancelled 是取消的哨兵错误：安装路径见到它就回滚并把它翻译成
// `cancelled`，而不是 `failed`（两者在官方结果里是不同的 application）。
var errInstallCancelled = errors.New("安装已被取消")

// InstallOptions 是官方 installBundle/updateBundle 里**本仓能兑现**的那部分选项。
//
// 不能兑现的（registry、approvedBuilds）由适配器在调用前就拒成 501：
// 静默忽略等于假装装了一个带 npm 构建脚本的包。
type InstallOptions struct {
	// RequestID 命名这次安装；空串表示不登记作业（也就不能 wait/cancel）。
	RequestID string
	// Enable 是安装完成后的启用位。它和安装写在同一个事务、同一份 state 里。
	Enable bool
}

// InstallOutcome 是一次安装作业的结果投影（不是 HTTP/JSON 类型）。
type InstallOutcome struct {
	// Changed 表示真实状态变了；cancelled/failed 一律 false。
	Changed bool
	// Application 只取 applied / cancelled / failed。
	//
	// 本仓没有运行时插件加载器（docs/host-plugin-manager.md §1），所以
	// `applied` 的准确含义是"profile 状态已持久化"，调用方必须连同
	// `Warnings` 一起呈现，不能据此声称插件已在进程内跑起来。
	Application string
	// Stage 是最后走到的一步：install / enable / remove。
	Stage string
	// Target 是这次作业的目标（安装是 spec，切版是包名）。
	Target string
	// Enabled 非 nil 表示这次作业动过启用位。
	Enabled *bool
	// Info 是收工后的真实投影；失败/取消时为零值。
	Info PluginInfo
	// Failure 是管理性失败的四字段原因（重复安装、版本冲突、状态损坏…）。
	Failure *Error
}

// InstallJob 是一次在飞安装的句柄。
type InstallJob struct {
	RequestID string

	control *installControl
	done    chan struct{}
	once    sync.Once
	outcome InstallOutcome
}

// installControl 是取消标志与"太晚"的边界。
type installControl struct {
	mu        sync.Mutex
	phase     string
	cancelled bool
}

// checkpoint 是 nil 安全的取消检查点。
func (c *installControl) checkpoint() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancelled {
		return errInstallCancelled
	}
	return nil
}

// enter 记录事务阶段，供"太晚"判定使用。
func (c *installControl) enter(phase string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.phase = phase
	c.mu.Unlock()
}

// requestCancel 置位取消标志。返回 false 表示已经太晚（状态正在落盘）。
func (c *installControl) requestCancel() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch c.phase {
	case "committing", "state_committed":
		return false
	}
	c.cancelled = true
	return true
}

func (c *installControl) currentPhase() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.phase
}

// Wait 等到作业收工，返回真实结果。
func (j *InstallJob) Wait() InstallOutcome {
	<-j.done
	return j.outcome
}

func (j *InstallJob) finish(outcome InstallOutcome) {
	j.once.Do(func() {
		j.outcome = outcome
		close(j.done)
	})
}

// StartInstall 在后台跑一次真实安装，并把它登记到 RequestID 上。
//
// 重复的 RequestID 一律拒绝：两次安装共用同一个进度/取消句柄，会让
// cancelInstall 停错作业。
func (m *Manager) StartInstall(source string, opts InstallOptions) (*InstallJob, error) {
	job, err := m.registerJob(opts.RequestID)
	if err != nil {
		return nil, err
	}
	go func() {
		outcome := m.runInstallJob(job.control, source, opts.Enable, opts.RequestID)
		m.finishJob(opts.RequestID, job, outcome)
	}()
	return job, nil
}

// StartUpdate 在后台跑一次真实切版（目标版本必须已在本机 store 里）。
func (m *Manager) StartUpdate(name, version string, opts InstallOptions) (*InstallJob, error) {
	job, err := m.registerJob(opts.RequestID)
	if err != nil {
		return nil, err
	}
	go func() {
		outcome := m.runUpdateJob(job.control, name, version, opts.RequestID)
		m.finishJob(opts.RequestID, job, outcome)
	}()
	return job, nil
}

// WaitInstall 等到这次安装收工；没有该 id 的在飞作业时 ok=false。
//
// 与官方一致：**已完成**的作业不保留结果，所以事后重查只会得到 ok=false。
func (m *Manager) WaitInstall(requestID string) (InstallOutcome, bool) {
	m.jobsMu.Lock()
	job := m.jobs[requestID]
	m.jobsMu.Unlock()
	if job == nil {
		return InstallOutcome{}, false
	}
	return job.Wait(), true
}

// CancelInstall 取消一次安装并等到它真的收工，返回官方状态：
// cancelled / too-late / not-running。
func (m *Manager) CancelInstall(requestID string) string {
	_, job := m.requestCancel(requestID)
	if job == nil {
		return "not-running"
	}
	// 官方在 abort 之后等结果落定才回报 cancelled；本仓同样等作业收工，
	// 这样 cancelled 才等于"文件已经回滚回去了"。作业没能停下（已经进入
	// committing，或者抢先提交完了）就是 too-late——绝不报 cancelled。
	outcome := job.Wait()
	if outcome.Application == "cancelled" {
		return "cancelled"
	}
	return "too-late"
}

// requestCancel 只置位取消，不等待；测试用它做确定性同步。
//
// 返回 `cancelling` 表示标志已置位（作业会在下一个检查点停下），
// `too-late` 表示状态已经在落盘，`not-running` 表示没有这个 id。
func (m *Manager) requestCancel(requestID string) (string, *InstallJob) {
	m.jobsMu.Lock()
	job := m.jobs[requestID]
	m.jobsMu.Unlock()
	if job == nil {
		return "not-running", nil
	}
	if !job.control.requestCancel() {
		return "too-late", job
	}
	return "cancelling", job
}

func (m *Manager) registerJob(requestID string) (*InstallJob, error) {
	job := &InstallJob{RequestID: requestID, control: &installControl{}, done: make(chan struct{})}
	if requestID == "" {
		return job, nil
	}
	m.jobsMu.Lock()
	defer m.jobsMu.Unlock()
	if m.jobs == nil {
		m.jobs = map[string]*InstallJob{}
	}
	if _, exists := m.jobs[requestID]; exists {
		return nil, errorf("duplicate_request", 409, false,
			"换一个 requestId；同一次安装的进度与取消必须一一对应",
			"requestId %q 已有在飞安装", requestID)
	}
	m.jobs[requestID] = job
	return job, nil
}

func (m *Manager) finishJob(requestID string, job *InstallJob, outcome InstallOutcome) {
	if requestID == "" {
		// 空 id 不登记，也就没有注销动作；结果仍要发布给 job.Wait。
		job.finish(outcome)
		return
	}
	// 完成发布与注销必须在**同一个 jobsMu 临界区**：先 close(done) 再删 map
	// 会让"installBundle 返回后立刻 WaitInstall"看到 active:true + 已完成结果。
	// 反过来先删再 finish 又会在真正完成前暴露 not-running。锁内一次做完，
	// WaitInstall 的 map 查询要么在完成前拿到 job 并等结果（在飞语义），
	// 要么在释放锁后拿不到 job（not-running）；没有中间态。
	m.jobsMu.Lock()
	defer m.jobsMu.Unlock()
	job.finish(outcome)
	if hook := finishJobPublishedHook.Load(); hook != nil {
		(*hook)(requestID)
	}
	if m.jobs[requestID] == job {
		delete(m.jobs, requestID)
	}
}

// finishJobPublishedHook 是**仅供确定性并发测试**的观察点：在 jobsMu 内、
// job.finish(outcome) 之后、注销 map 之前调用。生产路径恒为 nil。
//
// 有了它，测试可以在"结果已发布"这一刻停住并证明此时 jobsMu 仍被持有：
// 任何 WaitInstall 都不可能越过临界区看到 active:true；去掉它测试仍然
// 语义正确（只是无法稳定复现旧实现的窗口）。
var finishJobPublishedHook atomic.Pointer[func(requestID string)]

func (m *Manager) runInstallJob(control *installControl, source string, enable bool, target string) InstallOutcome {
	m.mu.Lock()
	defer m.mu.Unlock()
	outcome := InstallOutcome{Application: "applied", Stage: "install", Target: target}
	info, err := m.installLocked(source, enable, control)
	if err != nil {
		return installFailure(outcome, err)
	}
	enabled := info.Enabled
	outcome.Changed = true
	outcome.Info = info
	outcome.Enabled = &enabled
	return outcome
}

func (m *Manager) runUpdateJob(control *installControl, name, version, target string) InstallOutcome {
	m.mu.Lock()
	defer m.mu.Unlock()
	if target == "" {
		target = name
	}
	outcome := InstallOutcome{Application: "applied", Stage: "install", Target: target}
	info, err := m.restoreLocked(name, version, control)
	if err != nil {
		return installFailure(outcome, err)
	}
	enabled := info.Enabled
	outcome.Changed = true
	outcome.Info = info
	outcome.Enabled = &enabled
	return outcome
}

func installFailure(outcome InstallOutcome, err error) InstallOutcome {
	if errors.Is(err, errInstallCancelled) {
		outcome.Application = "cancelled"
		return outcome
	}
	outcome.Application = "failed"
	outcome.Failure = AsError(err)
	return outcome
}
