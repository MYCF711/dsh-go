package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
)

// serveJobsOut 在**子进程**侧处理父进程的「查某个后台任务的输出」请求。
//
// # 为什么在子进程这一侧做
//
// `spi.Job` 的实例在子进程的 kernel Scope 里（`modules/exec/bash` 提供）。
// 宿主那一侧没有它——所以 `jobs.output` 只能变成「父进程问、子进程答」。
//
// # 为什么读 channel 而不是直接读 stdin
//
// stdin 上还有审批应答（`@@APPROVAL-RESP@@`）。两个读者抢一根流拿不准，
// 所以由 `stdinDispatcher` 统一读、按标签分发，这里只消费自己那一路。
//
// # 为什么 err 也回一条应答
//
// 不回的话父进程只能等超时——而"这个 id 不存在"和"子进程没反应"在界面上
// 长得一样。回一条带 error 的应答，界面能立刻说清是哪个。
func serveJobsOut(root *kernel.Scope) {
	job, err := kernel.Inject(root, spi.JobKey)
	if err != nil || job == nil {
		return // 这个宿主没有 exec 模块：什么都不做，父进程那边的 jobs.output 会回 501
	}
	for line := range jobsOutCh {
		var req struct {
			ID string `json:"id"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, jobsOutReqTag)), &req) != nil || req.ID == "" {
			continue
		}
		resp := map[string]any{"id": req.ID}
		out, oerr := job.Output(req.ID, 0) // wait=0：只取现有的，不等
		if oerr != nil {
			resp["error"] = oerr.Error()
		} else {
			resp["output"] = out.Output
			resp["done"] = out.Done
			resp["exit"] = out.ExitCode
			if out.Cwd != "" {
				resp["cwd"] = out.Cwd
			}
		}
		b, merr := json.Marshal(resp)
		if merr != nil {
			continue
		}
		// **写 stderr**：和 @@JOBS@@ 同一条通道（stderr 通着、stdout 只有 ready 握手）。
		fmt.Fprintf(os.Stderr, "%s%s\n", jobsOutRespTag, b)
	}
}
