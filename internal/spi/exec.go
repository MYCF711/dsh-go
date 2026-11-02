package spi

import "time"

// Exec 是命令执行接缝。按 R6：**真实 shell 进程**，不做进程内解释。
// CwdFollower 是 Exec 的可选能力：换掉"新会话的默认工作目录"。
//
// 和 FS 的 RootFollower 同一套思路：客户端带来的 cwd 要两边一起生效，
// 否则同一个会话里 read 在 B、bash 在 A。
type CwdFollower interface {
	SetCwd(dir string) error
}

type Exec interface {
	// Run 在持久 shell 里执行一条命令。
	//
	// idle 是「多久没有任何输出就转为后台任务」。返回的 JobID 非空表示已转后台，
	// 此时 Result 里是到目前为止的输出，任务继续跑，用 Job 服务取后续输出。
	Run(cmd string, idle time.Duration) (ExecResult, error)
}

// ExecResult 是一条命令的结果。
type ExecResult struct {
	Output    string `json:"output"` // stdout 与 stderr 合流（M1 的取舍，见 CHANGELOG）
	ExitCode  int    `json:"exit_code"`
	Cwd       string `json:"cwd"`
	JobID     string `json:"job_id,omitempty"`
	MovedToBg bool   `json:"moved_to_bg,omitempty"`
}

// Job 是后台任务表。
type Job interface {
	Output(id string, wait time.Duration) (JobOutput, error)
	Kill(id string) error
	List() []JobInfo
}

// JobOutput 是一次取输出的结果。
type JobOutput struct {
	ID       string `json:"id"`
	Output   string `json:"output"`
	Done     bool   `json:"done"`
	ExitCode int    `json:"exit_code"`
	Cwd      string `json:"cwd"`
}

// JobInfo 是任务清单里的一行。
type JobInfo struct {
	ID       string `json:"id"`
	Command  string `json:"command"`
	Done     bool   `json:"done"`
	ExitCode int    `json:"exit_code"`
	Bytes    int    `json:"bytes"`
}
