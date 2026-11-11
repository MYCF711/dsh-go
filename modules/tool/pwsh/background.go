package pwsh

// run_in_background：官方语义是"注册进宿主的 jobs 运行时、立刻回 job id、
// 之后用 job_output 取输出、job_kill 终止"，与 bash 工具**同一张表**。
//
// 为什么经 spi.Exec 而不是自己起 goroutine 存一张私表：
//   - 私表里的任务 `job_output` / `job_kill` 看不见，等于假后台（模型拿到一个
//     取不到输出的 id），比"不支持"更坏；
//   - 工具模块不许直接 import 别的模块（AGENTS R2），所以不能拿
//     modules/exec/bash 的 Service；`spi.Exec` 是契约层唯一的执行接缝。
//
// 接缝的用法分两档（fail-loud，不假装）：
//  1. 首选 additive 能力 `RunBackground(cmd)`——就是 bash 工具 run_in_background
//     走的那一个方法，语义逐字一致（毫秒级返回、瞬时结束也保留 job）；
//  2. 接缝只有 `Run(cmd, idle)` 时，用 1ms 的 idle 让它立刻转后台，
//     形状相同（拿得到 job id），只是"首输出前就被判定转后台"。
//
// 命令行本身是给 bash 接缝执行的，所以要做 POSIX 单引号转义：PowerShell 脚本里
// 的引号/换行/$ 一律原样传过去，不让 bash 先解释一遍（否则脚本会被吃掉一层）。

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
)

// backgroundIdle 是"没有 RunBackground 扩展时"的转后台窗口。
// 1ms = 下一个调度窗口就转后台，等价于 RunBackground 内部的实现。
const backgroundIdle = time.Millisecond

// backgroundRunner 是 spi.Exec 的 additive 能力：立刻把命令登记成后台任务并返回
// job id。modules/exec/bash 的 Service 实现它（bash 工具的 run_in_background 同源），
// 但这个接口**刻意不写进 spi**：接缝的旧实现只有 Run，工具按需断言即可。
type backgroundRunner interface {
	RunBackground(cmd string) (spi.ExecResult, error)
}

// backgroundSpec 是一条要转后台的 pwsh 调用。
type backgroundSpec struct {
	shell       string // resolveShell 解出的绝对路径
	script      string // 已带 legacy 序言的脚本文本
	workdir     string // 已校验存在的目录；空 = 用接缝会话的当前目录
	description string // 官方 required 的说明，进 Summary/Data 给 UI
	command     string // 模型给的原文，进 Data 方便回看
}

// jobsSeam 取执行接缝：测试注入优先，否则从装配作用域里 inject spi.Exec。
func (t *Tool) jobsSeam() (spi.Exec, error) {
	if t.execSeam != nil {
		return t.execSeam, nil
	}
	if t.scope == nil {
		return nil, fmt.Errorf("本工具没有装配作用域：无法取到 jobs/exec 能力")
	}
	return kernel.Inject(t.scope, spi.ExecKey)
}

// startBackground 把命令交给 jobs 运行时并返回 job id。
func (t *Tool) startBackground(spec backgroundSpec) (spi.Result, error) {
	d := data{
		Shell:       filepath.Base(spec.shell),
		Description: spec.description,
		Command:     spec.command,
		Workdir:     spec.workdir,
		Background:  true,
	}
	seam, err := t.jobsSeam()
	if err != nil {
		return spi.Result{
			Data: d,
			Hint: "宿主没装配 jobs/exec 能力，run_in_background 无法登记后台任务：去掉这个参数在前台跑，" +
				"或让宿主把 modules/exec/bash（提供 spi.Exec/spi.Job）装进来",
		}, fmt.Errorf("pwsh: run_in_background 缺执行接缝: %w", err)
	}
	line := backgroundCommandLine(spec)
	var res spi.ExecResult
	if bg, ok := seam.(backgroundRunner); ok {
		res, err = bg.RunBackground(line)
	} else {
		// 接缝没有 RunBackground：给它一个 1ms 的 idle 窗口，立刻转后台。
		res, err = seam.Run(line, backgroundIdle)
	}
	if err != nil {
		return spi.Result{
			Data: d,
			Hint: "后台启动失败：先看 job_list 有没有残留（用 job_kill 收掉），或去掉 run_in_background 在前台跑一次看真实报错",
		}, fmt.Errorf("pwsh: 后台启动失败: %w", err)
	}
	d.JobID = res.JobID
	if res.JobID == "" {
		// 1ms 内就跑完的命令（echo/true 这类）：这不是错误，如实说它在后台窗口里跑完了。
		// 官方在"给了 run_in_background"时也一定有 job，但这里拿不到 id 就不能编一个。
		body := res.Output
		if strings.TrimSpace(body) == "" {
			body = "（无输出）"
		}
		summary := "命令瞬间结束（没进后台队列）"
		if spec.description != "" {
			summary = spec.description + "：" + summary
		}
		return spi.Result{Summary: summary, Text: body, Data: d}, nil
	}
	summary := "已在后台启动 " + res.JobID
	if spec.description != "" {
		summary = spec.description + "：已在后台启动 " + res.JobID
	}
	return spi.Result{
		Summary: summary,
		Text:    "已转后台：" + res.JobID + "（用 job_output 取输出，job_kill 终止）",
		Data:    d,
	}, nil
}

// backgroundCommandLine 把 pwsh 调用拼成接缝能执行的一行命令。
//
// 形状：`cd -- '<workdir>' && '<pwsh>' -NoLogo -NoProfile -NonInteractive -Command '<script>'`
//
// 为什么用 basename 而不是绝对路径：接缝执行的是 bash，Windows 绝对路径
// （`C:\Program Files\...`）在单引号里会被 bash 当字面量，能不能跑取决于
// 那个壳的路径转换行为；而 shell 本来就是 exec.LookPath 从 PATH 上找出来的，
// 用 basename 让 bash 用同一套 PATH 再找一次，行为最稳定。
func backgroundCommandLine(spec backgroundSpec) string {
	parts := make([]string, 0, 2)
	if wd := strings.TrimSpace(spec.workdir); wd != "" {
		// 先绝对化：接缝会话的 cwd 可能与我们的进程 cwd 不同（bash 工具里 cd 过），
		// 相对路径在那边会指到别处。
		if abs, err := filepath.Abs(wd); err == nil {
			wd = abs
		}
		parts = append(parts, "cd -- "+shQuote(wd))
	}
	parts = append(parts, shQuote(filepath.Base(spec.shell))+
		" -NoLogo -NoProfile -NonInteractive -Command "+shQuote(spec.script))
	return strings.Join(parts, " && ")
}

// shQuote 把一个字符串安全地嵌进 POSIX shell 的单引号里。
// 规则：' 先闭合、转义一个 \'、再打开——这是 POSIX 唯一无歧义的写法。
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
