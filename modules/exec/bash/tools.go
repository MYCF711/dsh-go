package bash

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
)

// Options 是执行层的可调参数。
type Options struct {
	// IdleBudget 是「多久没有输出就转后台」。默认 10 分钟（与 DSH 的前台宽限同量级）。
	IdleBudget time.Duration
}

func (o Options) idle() time.Duration {
	if o.IdleBudget > 0 {
		return o.IdleBudget
	}
	return 10 * time.Minute
}

func registerTools(s *kernel.Scope, svc *Service, opts Options) error {
	reg, err := kernel.Inject(s, spi.ToolsKey)
	if err != nil {
		return err
	}
	for _, t := range []spi.Tool{
		&shellTool{svc: svc, budget: opts.idle()},
		&jobOutputTool{svc: svc},
		&jobKillTool{svc: svc},
		&jobListTool{svc: svc},
	} {
		if err := reg.Register(t); err != nil {
			return err
		}
	}
	return nil
}

// ---------- shell ----------

var shellSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "command": {"type": "string", "description": "要执行的 bash 命令；cwd 跨命令保留"},
    "description": {"type": "string", "description": "这段命令干什么，5-10 个字，给用户看的（例如「列出当前目录」）"},
    "idle_seconds": {"type": "integer", "minimum": 1, "maximum": 900, "description": "多久无输出转后台，默认 600"}
  },
  "required": ["command", "description"],
  "additionalProperties": false
}`)

type shellTool struct {
	svc    *Service
	budget time.Duration
}

func (t *shellTool) Name() string   { return "bash" }
func (t *shellTool) Caps() spi.Caps { return spi.Caps{Writes: true, Executes: true} }
func (t *shellTool) Description() string {
	return "在持久 bash 会话里执行命令，cwd 跨命令保留；无输出超时就转后台。"
}
func (t *shellTool) Schema() json.RawMessage { return shellSchema }

type shellArgs struct {
	Command     string `json:"command"`
	Description string `json:"description"`
	IdleSeconds int    `json:"idle_seconds"`
}

type shellData struct {
	Output    string `json:"output"`
	ExitCode  int    `json:"exit_code"`
	Cwd       string `json:"cwd"`
	JobID     string `json:"job_id,omitempty"`
	MovedToBg bool   `json:"moved_to_bg,omitempty"`
}

func (t *shellTool) Exec(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	var a shellArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return spi.Result{Hint: `参数不合法：期望 {"command": "..."}`}, fmt.Errorf("参数解析失败: %w", err)
	}
	if strings.TrimSpace(a.Command) == "" {
		return spi.Result{Hint: "command 不能为空"}, fmt.Errorf("command 为空")
	}

	budget := t.budget
	if a.IdleSeconds > 0 {
		budget = time.Duration(a.IdleSeconds) * time.Second
	}
	if budget > 15*time.Minute {
		budget = 15 * time.Minute
	}

	res, err := t.svc.Run(a.Command, budget)
	if err != nil {
		if errors.Is(err, ErrSessionEnded) {
			return spi.Result{Hint: "命令自己结束了 shell（例如 exit）；下一条命令会自动开新会话，但 cwd 会回到起点"}, err
		}
		return spi.Result{Hint: "命令没能执行：确认 shell 可用（Git Bash）、命令语法正确"}, err
	}

	d := shellData{
		Output:    res.Output,
		ExitCode:  res.ExitCode,
		Cwd:       res.Cwd,
		JobID:     res.JobID,
		MovedToBg: res.MovedToBg,
	}
	summary := fmt.Sprintf("退出码 %d，输出 %d 字节", res.ExitCode, len(res.Output))
	hint := ""
	switch {
	case res.MovedToBg:
		summary = fmt.Sprintf("已转后台 %s（期间无输出）", res.JobID)
		hint = "任务在后台继续跑；用 job_output <id> 取后续输出，job_kill <id> 终止"
	case res.ExitCode != 0:
		hint = "命令失败：先读输出里的报错原文，再决定改命令还是改参数；不要原样重试"
	}
	return spi.Result{
		Summary: summary,
		Data:    d,
		Hint:    hint,
		View: &spi.ViewSpec{
			Kind:  spi.ViewPlain,
			Title: "bash",
			Badge: fmt.Sprintf("exit %d", res.ExitCode),
			Body:  res.Output,
		},
	}, nil
}

// ---------- job_output ----------

var jobOutputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "job_id": {"type": "string", "description": "任务 id，如 job-1"},
    "wait": {"type": "boolean", "description": "是否阻塞等待任务结束，默认 false"},
    "timeout_ms": {"type": "integer", "minimum": 0, "maximum": 120000, "description": "wait=true 时最多等多少毫秒，默认 30000"}
  },
  "required": ["job_id"],
  "additionalProperties": false
}`)

type jobOutputTool struct{ svc *Service }

func (t *jobOutputTool) Name() string   { return "job_output" }
func (t *jobOutputTool) Caps() spi.Caps { return spi.Caps{} }
func (t *jobOutputTool) Description() string {
	return "取后台任务输出（可短等）。任务没结束也能反复取。"
}
func (t *jobOutputTool) Schema() json.RawMessage { return jobOutputSchema }

func (t *jobOutputTool) Exec(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	var a struct {
		JobID     string `json:"job_id"`
		Wait      bool   `json:"wait"`
		TimeoutMS int    `json:"timeout_ms"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return spi.Result{Hint: `参数不合法：期望 {"job_id": "job-1", "wait"?: false, "timeout_ms"?: 30000}`}, err
	}
	wait := time.Duration(0)
	if a.Wait {
		ms := a.TimeoutMS
		if ms <= 0 {
			ms = 30000
		}
		wait = time.Duration(ms) * time.Millisecond
	}
	out, err := t.svc.Output(a.JobID, wait)
	if err != nil {
		return spi.Result{Hint: "用 job_list 看有哪些任务 id"}, err
	}
	summary := fmt.Sprintf("%s 输出 %d 字节（%s）", out.ID, len(out.Output), doneText(out.Done))
	hint := ""
	if !out.Done && out.Output == "" {
		hint = "任务还没吐字：可能是子进程把 stdout 缓冲在管道里；要实时看就别把输出重定向到文件"
	}
	if out.Done && out.ExitCode != 0 {
		hint = fmt.Sprintf("任务已结束，退出码 %d；读输出里的报错再决定下一步", out.ExitCode)
	}
	return spi.Result{Summary: summary, Data: out, Hint: hint}, nil
}

func doneText(done bool) string {
	if done {
		return "已结束"
	}
	return "运行中"
}

// ---------- job_kill ----------

var jobKillSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "job_id": {"type": "string", "description": "任务 id"},
    "reason": {"type": "string", "description": "可选：记录在日志里并转发给任务"}
  },
  "required": ["job_id"],
  "additionalProperties": false
}`)

type jobKillTool struct{ svc *Service }

func (t *jobKillTool) Name() string            { return "job_kill" }
func (t *jobKillTool) Caps() spi.Caps          { return spi.Caps{Destructive: true} }
func (t *jobKillTool) Description() string     { return "终止一个后台任务。" }
func (t *jobKillTool) Schema() json.RawMessage { return jobKillSchema }

func (t *jobKillTool) Exec(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	var a struct {
		JobID  string `json:"job_id"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return spi.Result{Hint: `参数不合法：期望 {"job_id": "job-1", "reason"?: "..."}`}, err
	}
	if err := t.svc.Kill(a.JobID); err != nil {
		return spi.Result{Hint: "用 job_list 确认 id；已结束的任务不需要终止"}, err
	}
	return spi.Result{
		Summary: "已终止 " + a.JobID,
		Data:    map[string]any{"job_id": a.JobID, "killed": true},
		Hint:    "终止只杀掉 bash 进程；它的子进程可能仍在跑，必要时用 bash 手动清理",
	}, nil
}

// ---------- job_list ----------

var jobListSchema = json.RawMessage(`{
  "type": "object",
  "properties": {},
  "additionalProperties": false
}`)

type jobListTool struct{ svc *Service }

func (t *jobListTool) Name() string            { return "job_list" }
func (t *jobListTool) Caps() spi.Caps          { return spi.Caps{} }
func (t *jobListTool) Description() string     { return "列出后台任务及其状态。" }
func (t *jobListTool) Schema() json.RawMessage { return jobListSchema }

func (t *jobListTool) Exec(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	jobs := t.svc.List()
	if len(jobs) == 0 {
		return spi.Result{
			Summary: "没有后台任务",
			Data:    map[string]any{"jobs": []spi.JobInfo{}},
		}, nil
	}
	rows := make([][]string, 0, len(jobs))
	for _, j := range jobs {
		rows = append(rows, []string{j.ID, doneText(j.Done), fmt.Sprintf("%d", j.ExitCode), fmt.Sprintf("%d", j.Bytes), j.Command})
	}
	return spi.Result{
		Summary: fmt.Sprintf("%d 个后台任务", len(jobs)),
		Data:    map[string]any{"jobs": jobs},
		View: &spi.ViewSpec{
			Kind:    spi.ViewTable,
			Title:   "后台任务",
			Columns: []string{"id", "状态", "退出码", "字节", "命令"},
			Rows:    rows,
		},
	}, nil
}
