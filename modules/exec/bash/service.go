package bash

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
)

var fallbackOwnerSeq atomic.Uint64

// Service 同时实现 spi.Exec 与 spi.Job，并实现 additive 的 spi.JobPager/JobController。
// 一个 Service 只属于一个 kernel Scope；OwnerID 是这个实例的隔离边界，不伪造外部 session id。
type Service struct {
	mu        sync.Mutex
	sess      *session
	cwd       string
	jobs      map[string]*job
	seq       int
	order     []string
	ownerID   string
	scopeName string
	closed    bool
	// terms 是**长连接终端**（`terminal.open` 起的那些），与一次性 job 分开。
	terms map[string]*Terminal
	tseq  int
	// grace 是停止时的宽限预算（官方 `bash.graceMs`）；0 表示用默认 3000ms。
	grace time.Duration
	// logf 记录停止结局（宽限内退出 / 强杀）；nil 不输出。
	logf func(format string, args ...any)
}

func newOwnerID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("%016x", uint64(time.Now().UnixNano())^fallbackOwnerSeq.Add(1))
}

// Cwd 返回下一条命令将要使用的 cwd（会话没有建立时就是服务默认 cwd）。
// 策略闸门必须在启动进程**之前**拿到它：命令的 canonical 边界是 cwd 决定的。
func (s *Service) Cwd() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cwd != "" {
		return s.cwd
	}
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

// SetCwd 换掉"新会话的默认工作目录"。已经开着的会话不动。
func (s *Service) SetCwd(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return fmt.Errorf("工作目录不能为空")
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return fmt.Errorf("不是目录: %s", dir)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return spi.ErrServiceClosed
	}
	s.cwd = dir
	return nil
}

// NewService 以 cwd 为初始工作目录。每次调用都有独立 OwnerID。
func NewService(cwd string) *Service {
	return &Service{cwd: cwd, jobs: make(map[string]*job), ownerID: newOwnerID()}
}

func (s *Service) acquireSession() (*session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, spi.ErrServiceClosed
	}
	sess := s.sess
	if sess == nil || sess.busy() || sess.isDead() {
		next, err := startSession(s.cwd, s.grace)
		if err != nil {
			return nil, err
		}
		sess = next
		s.sess = sess
	}
	return sess, nil
}

// Run 实现 spi.Exec。
func (s *Service) Run(cmd string, idle time.Duration) (spi.ExecResult, error) {
	if strings.TrimSpace(cmd) == "" {
		return spi.ExecResult{}, fmt.Errorf("命令为空")
	}
	if idle <= 0 {
		idle = 10 * time.Minute
	}
	sess, err := s.acquireSession()
	if err != nil {
		return spi.ExecResult{}, err
	}
	res, t, err := sess.run(cmd, idle)
	if err != nil {
		return res, err
	}
	if !res.MovedToBg {
		s.mu.Lock()
		if res.Cwd != "" {
			s.cwd = res.Cwd
		}
		s.mu.Unlock()
		return res, nil
	}

	id, err := s.register(cmd, sess, t)
	if err != nil {
		sess.close()
		return spi.ExecResult{}, err
	}
	s.mu.Lock()
	if s.sess == sess {
		s.sess = nil
	}
	s.mu.Unlock()
	res.JobID = id
	return res, nil
}

func (s *Service) register(cmd string, sess *session, t *task) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", spi.ErrServiceClosed
	}
	// 官方 jobs.maxConcurrentJobsPerOwner（默认 10）：同一 owner 的**运行中**任务上限。
	// 已结算的任务不占额度（它们只是保留输出，不占并发）；超限 fail-loud —— 排队会
	// 让调用方以为"已经跑起来了"，静默丢弃更糟。调用方拿到错误会关闭刚起的会话，
	// 所以不会留下没人管的进程树。
	if limit := maxConcurrentJobsPerOwner(); limit > 0 {
		running := 0
		for _, j := range s.jobs {
			if j != nil && j.task != nil && !j.task.finished() {
				running++
			}
		}
		if running >= limit {
			return "", fmt.Errorf("同一 owner 已有 %d 个运行中的后台任务（上限 %d，可用 %s 调整）：先等它们结束，或停掉其中一个",
				running, limit, EnvMaxConcurrentJobsPerOwner)
		}
	}
	s.seq++
	id := fmt.Sprintf("job-%s-%d", s.ownerID, s.seq)
	s.jobs[id] = &job{id: id, cmd: cmd, sess: sess, task: t}
	s.order = append(s.order, id)
	return id, nil
}

// RunBackground 起一条命令立刻转后台；即使它在首个调度窗口内结束也保留 job，
// 使结束后输出与元数据仍可从同一任务表读取。
func (s *Service) RunBackground(cmd string) (spi.ExecResult, error) {
	if strings.TrimSpace(cmd) == "" {
		return spi.ExecResult{}, fmt.Errorf("命令为空")
	}
	sess, err := s.acquireSession()
	if err != nil {
		return spi.ExecResult{}, err
	}
	res, t, err := sess.run(cmd, time.Millisecond)
	if err != nil {
		return res, err
	}
	id, err := s.register(cmd, sess, t)
	if err != nil {
		sess.close()
		return spi.ExecResult{}, err
	}
	s.mu.Lock()
	if res.MovedToBg && s.sess == sess {
		s.sess = nil
	}
	if !res.MovedToBg && res.Cwd != "" {
		s.cwd = res.Cwd
	}
	s.mu.Unlock()
	res.JobID = id
	return res, nil
}

// Output 实现旧 spi.Job.Output。兼容语义仍是保留输出的全量字符串并去掉末尾换行；
// 新调用方应使用 OutputPage 获取绝对游标、total 与 truncated。
func (s *Service) Output(id string, wait time.Duration) (spi.JobOutput, error) {
	j, err := s.job(id)
	if err != nil {
		return spi.JobOutput{}, err
	}
	if wait < 0 {
		wait = 0
	}
	deadline := time.Now().Add(wait)
	for {
		rc, cwd, done := j.task.result()
		if done || wait <= 0 || time.Now().After(deadline) {
			return spi.JobOutput{ID: id, Output: j.task.text(), Done: done, ExitCode: rc, Cwd: cwd}, nil
		}
		notify := j.task.notification()
		timer := time.NewTimer(time.Until(deadline))
		select {
		case <-notify:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
		}
	}
}

// OutputPage 实现 spi.JobPager。limit>JobMaxPageLimit 会被钳制，wait>JobMaxPageWait 同理。
func (s *Service) OutputPage(id string, cursor int64, limit int, wait time.Duration) (spi.JobOutputPage, error) {
	j, err := s.job(id)
	if err != nil {
		return spi.JobOutputPage{}, err
	}
	if limit < 0 {
		return spi.JobOutputPage{}, fmt.Errorf("limit 不能为负: %d", limit)
	}
	if limit == 0 {
		limit = spi.JobDefaultPageLimit
	}
	if limit > spi.JobMaxPageLimit {
		limit = spi.JobMaxPageLimit
	}
	if wait < 0 {
		wait = 0
	}
	if wait > spi.JobMaxPageWait {
		wait = spi.JobMaxPageWait
	}
	deadline := time.Now().Add(wait)
	for {
		page, notify, err := j.task.page(id, cursor, limit)
		if err != nil {
			return spi.JobOutputPage{}, fmt.Errorf("任务 %s: %w", id, err)
		}
		if page.Output != "" || page.Done || page.Offset != cursor || wait <= 0 {
			return page, nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return page, nil
		}
		timer := time.NewTimer(remaining)
		select {
		case <-notify:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
		}
	}
}

// Stop 实现 spi.JobController：只停止本实例 id 且 root 创建代次/映像仍匹配的进程树。
//
// 停止是「先请求退出 → 等 graceMs → 仍存活才强杀」的升级链；结局记在任务上，
// 用 StopOutcome 取——「宽限内退出」和「强杀」不能糊成同一个 killed。
func (s *Service) Stop(id string) error {
	j, err := s.job(id)
	if err != nil {
		return err
	}
	if j.task.finished() {
		return fmt.Errorf("%w: 任务 %s（退出码 %d）", spi.ErrJobFinished, id, j.task.resultCode())
	}
	out, err := j.sess.stop(j.task)
	if err != nil {
		return fmt.Errorf("停止任务 %s: %w", id, err)
	}
	s.logStop(id, out)
	return nil
}

// logStop 把停止结局写成一条事件：**「宽限内自行退出」与「强杀」必须能区分**，
// 因为两者的下一步完全不同（前者命令已经收尾，后者是被打断）。
// 生产路径落 slog（dshd 在 diag.go 装了写文件的默认 handler）；
// 测试/嵌入方可以用 Options.Logf 收走。
func (s *Service) logStop(id string, out StopOutcome) {
	if s.logf != nil {
		s.logf("bash: 停止任务 %s：%s", id, out)
		return
	}
	slog.Info("bash 停止任务",
		"job", id,
		"outcome", out.String(),
		"delivery", out.Delivery,
		"grace_ms", out.Grace.Milliseconds(),
		"waited_ms", out.Waited.Milliseconds(),
		"graceful", out.Graceful,
		"escalated", out.Escalated,
	)
}

// StopOutcome 返回某次停止的结局（宽限内退出 / 强杀）；没停过 ok=false。
func (s *Service) StopOutcome(id string) (StopOutcome, bool) {
	j, err := s.job(id)
	if err != nil || j.sess == nil || j.sess.control == nil {
		return StopOutcome{}, false
	}
	return j.sess.control.outcome()
}

// Kill 保留旧 spi.Job.Kill 签名；不再是 root Process.Kill，而是同一安全树 Stop。
func (s *Service) Kill(id string) error { return s.Stop(id) }

func (s *Service) jobInfo(j *job) spi.JobInfo {
	rc, _, done := j.task.result()
	_, total, _ := j.task.retention()
	identity := j.sess.control.metadata()
	return spi.JobInfo{
		ID: j.id, Command: j.cmd, Done: done, ExitCode: rc, Bytes: int(total),
		OwnerID: s.ownerID, SessionScope: s.scopeName,
		RootPID: identity.PID, RootImage: identity.Image, CreationID: identity.CreationID,
		ProcessTree: identity.ProcessTree, ProcessGroupID: identity.ProcessGroupID,
	}
}

// Metadata 实现 spi.JobController。
func (s *Service) Metadata(id string) (spi.JobMetadata, error) {
	j, err := s.job(id)
	if err != nil {
		return spi.JobMetadata{}, err
	}
	_, cwd, _ := j.task.result()
	base, total, truncated := j.task.retention()
	return spi.JobMetadata{JobInfo: s.jobInfo(j), Cwd: cwd,
		RetainedOffset: base, Total: total, Truncated: truncated}, nil
}

// List 实现 spi.Job.List；每一行都是对应 job 的同源真实元数据快照。
func (s *Service) List() []spi.JobInfo {
	s.mu.Lock()
	jobs := make([]*job, 0, len(s.order))
	for _, id := range s.order {
		jobs = append(jobs, s.jobs[id])
	}
	s.mu.Unlock()
	out := make([]spi.JobInfo, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, s.jobInfo(j))
	}
	return out
}

func (s *Service) job(id string) (*job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j, ok := s.jobs[id]; ok {
		return j, nil
	}
	parts := strings.Split(id, "-")
	if len(parts) == 3 && parts[0] == "job" && parts[1] != s.ownerID {
		return nil, fmt.Errorf("%w: %s", spi.ErrJobWrongInstance, id)
	}
	return nil, fmt.Errorf("%w %q；用 job_list 看有哪些", spi.ErrJobNotFound, id)
}

// Close 是 Scope Dispose 的收工口：去重关闭当前、后台与终端会话，任务记录保留可读。
func (s *Service) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	unique := make(map[*session]struct{})
	if s.sess != nil {
		unique[s.sess] = struct{}{}
	}
	for _, j := range s.jobs {
		unique[j.sess] = struct{}{}
	}
	for _, t := range s.terms {
		t.mu.Lock()
		t.dead = true
		if t.sess != nil {
			unique[t.sess] = struct{}{}
		}
		t.mu.Unlock()
	}
	sessions := make([]*session, 0, len(unique))
	for sess := range unique {
		sessions = append(sessions, sess)
	}
	s.mu.Unlock()
	for _, sess := range sessions {
		sess.close()
	}
}

type job struct {
	id   string
	cmd  string
	sess *session
	task *task
}

// Module 返回装配用的模块描述：提供 exec 与 jobs 两个服务，并注册四个工具。
func Module(cwd string, opts Options) kernel.Module {
	return kernel.Module{
		ID:       "exec",
		Backend:  kernel.BackendStatic,
		Provides: []string{"exec", "jobs"},
		Register: func(s *kernel.Scope) error {
			svc := NewService(cwd)
			svc.scopeName = s.Name()
			svc.grace = opts.grace()
			svc.logf = opts.Logf
			s.Effect(svc.Close)
			if err := kernel.Provide(s, spi.ExecKey, spi.Exec(svc)); err != nil {
				return err
			}
			if err := kernel.Provide(s, spi.JobKey, spi.Job(svc)); err != nil {
				return err
			}
			return registerTools(s, svc, opts)
		},
	}
}

var _ spi.Exec = (*Service)(nil)
var _ spi.Job = (*Service)(nil)
var _ spi.JobPager = (*Service)(nil)
var _ spi.JobController = (*Service)(nil)
