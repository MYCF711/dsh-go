package bash

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
)

// Service 同时实现 spi.Exec 与 spi.Job：一个 exec 服务必然带一张后台任务表。
type Service struct {
	mu    sync.Mutex
	sess  *session
	cwd   string
	jobs  map[string]*job
	seq   int
	order []string
}

// NewService 以 cwd 为初始工作目录。
func NewService(cwd string) *Service {
	return &Service{cwd: cwd, jobs: make(map[string]*job)}
}

// Run 实现 spi.Exec。
func (s *Service) Run(cmd string, idle time.Duration) (spi.ExecResult, error) {
	if strings.TrimSpace(cmd) == "" {
		return spi.ExecResult{}, fmt.Errorf("命令为空")
	}
	if idle <= 0 {
		idle = 10 * time.Minute
	}

	s.mu.Lock()
	sess := s.sess
	if sess == nil || sess.busy() || sess.isDead() {
		next, err := startSession(s.cwd)
		if err != nil {
			s.mu.Unlock()
			return spi.ExecResult{}, err
		}
		sess = next
		s.sess = sess
	}
	s.mu.Unlock()

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

	// 空闲超时：把会话整体交给后台，下一次调用换新会话。
	id := s.register(cmd, sess, t)
	s.mu.Lock()
	if s.sess == sess {
		s.sess = nil
	}
	s.mu.Unlock()
	res.JobID = id
	return res, nil
}

func (s *Service) register(cmd string, sess *session, t *task) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	id := fmt.Sprintf("job-%d", s.seq)
	s.jobs[id] = &job{id: id, cmd: cmd, sess: sess, task: t}
	s.order = append(s.order, id)
	return id
}

// Output 实现 spi.Job.Output。
func (s *Service) Output(id string, wait time.Duration) (spi.JobOutput, error) {
	j, err := s.job(id)
	if err != nil {
		return spi.JobOutput{}, err
	}
	deadline := time.Now().Add(wait)
	for {
		if j.task.finished() || wait <= 0 || time.Now().After(deadline) {
			break
		}
		select {
		case <-j.task.sig:
		case <-time.After(time.Until(deadline)):
		}
	}
	rc, cwd, done := j.task.result()
	return spi.JobOutput{
		ID:       id,
		Output:   j.task.text(),
		Done:     done,
		ExitCode: rc,
		Cwd:      cwd,
	}, nil
}

// Kill 实现 spi.Job.Kill。
func (s *Service) Kill(id string) error {
	j, err := s.job(id)
	if err != nil {
		return err
	}
	if j.task.finished() {
		return fmt.Errorf("任务 %s 已经结束（退出码 %d），无需终止", id, j.task.resultCode())
	}
	j.sess.close()
	return nil
}

// List 实现 spi.Job.List。
func (s *Service) List() []spi.JobInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]spi.JobInfo, 0, len(s.order))
	for _, id := range s.order {
		j := s.jobs[id]
		rc, _, done := j.task.result()
		text := j.task.text()
		out = append(out, spi.JobInfo{
			ID: j.id, Command: j.cmd, Done: done, ExitCode: rc, Bytes: len(text),
		})
	}
	return out
}

func (s *Service) job(id string) (*job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return nil, fmt.Errorf("没有任务 %q；用 job_list 看有哪些", id)
	}
	return j, nil
}

// Close 关掉所有会话（作用域撤销时调用）。
func (s *Service) Close() {
	s.mu.Lock()
	sess := s.sess
	jobs := make([]*job, 0, len(s.jobs))
	for _, j := range s.jobs {
		jobs = append(jobs, j)
	}
	s.mu.Unlock()
	if sess != nil {
		sess.close()
	}
	for _, j := range jobs {
		j.sess.close()
	}
}

type job struct {
	id   string
	cmd  string
	sess *session
	task *task
}

func (t *task) resultCode() int {
	rc, _, _ := t.result()
	return rc
}

// Module 返回装配用的模块描述：提供 exec 与 jobs 两个服务，并注册四个工具。
func Module(cwd string, opts Options) kernel.Module {
	return kernel.Module{
		ID:       "exec",
		Backend:  kernel.BackendStatic,
		Provides: []string{"exec", "jobs"},
		Register: func(s *kernel.Scope) error {
			svc := NewService(cwd)
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
