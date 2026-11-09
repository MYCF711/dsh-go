package bash

import (
	"os"
	"strconv"
	"strings"
)

// 官方 jobs 服务的两条容量参数（`dsh-jobs-local`）：
//
//   - `jobs.maxConcurrentJobsPerOwner`（默认 10）：同一个 owner 的**运行中**后台任务上限；
//   - `jobs.settledRetainBytes`（默认 16384）：任务**结算之后**只保留这么多输出字节。
//
// 宿主原来两条都没有：
//   - `Service` 已经是 owner 的载体（一个 Service = 一个 kernel Scope，`ownerID` 就是它的
//     隔离边界），但 `register` 只登记不配额，想开多少后台任务就开多少；
//   - `task` 只有一份**运行期**窗口（`maxRetainedJobOutput`，1MiB），任务结算后缓冲原样留着，
//     长跑会话里 job 越多、内存越涨（官方则把已结算的砍到 16KiB）。
//
// 与 `jobs.retainBytes` 的分工：那一条是运行中窗口（宿主 1MiB，已在总账里单独登记），
// 这一条只管已结算任务，两条互不覆盖、也不互相改写默认值。
const (
	// DefaultMaxConcurrentJobsPerOwner 是官方 `jobs.maxConcurrentJobsPerOwner` 的默认值。
	DefaultMaxConcurrentJobsPerOwner = 10
	// DefaultSettledRetainBytes 是官方 `jobs.settledRetainBytes` 的默认值。
	//
	// **宿主默认不启用这一档**（见 settledRetainBytes 的说明）：本机的已结算任务保留量
	// 是一条既有验收契约（W69：`TestServeJobsOutIntegrationRealW69RetentionWindow` 要求
	// 结算后仍按 1MiB 运行窗口保留、分页从淘汰起点起读），所以"不设置就零影响"。
	// 要官方语义就显式设 DSHD_SETTLED_RETAIN_BYTES（值取 16384 即官方默认）。
	DefaultSettledRetainBytes = 16384

	// EnvMaxConcurrentJobsPerOwner 覆盖同 owner 运行中任务上限；0 = 不限。
	EnvMaxConcurrentJobsPerOwner = "DSHD_MAX_CONCURRENT_JOBS_PER_OWNER"
	// EnvSettledRetainBytes 覆盖已结算任务保留字节；0 = 不裁剪。
	EnvSettledRetainBytes = "DSHD_SETTLED_RETAIN_BYTES"
)

// maxConcurrentJobsPerOwner 解析同 owner 并发上限：默认 10；0 = 不限；非法值退回默认。
func maxConcurrentJobsPerOwner() int {
	return nonNegativeIntEnv(EnvMaxConcurrentJobsPerOwner, DefaultMaxConcurrentJobsPerOwner)
}

// settledRetainBytes 解析已结算任务的保留字节。
//
// 三种情况，分得很清楚：
//   - **env 未设置 = 不裁剪（0）**：宿主既有契约是"结算后仍按 1MiB 运行窗口保留"
//     （W69 验收用例钉着这条），官方默认 16384 是另一个语义。要求"不设置时零影响"，
//     所以默认不启用，改由 DSHD_SETTLED_RETAIN_BYTES 显式打开。
//   - **env=0 = 显式不裁剪**（显式表达"我要旧行为"，与未设置同效但可自证）。
//   - **env 写错（非法/负数）= 用官方默认 16384**：调用方明显是想启用却写错了，
//     这时退回官方默认比退回"不裁剪"更接近意图。
func settledRetainBytes() int {
	if strings.TrimSpace(os.Getenv(EnvSettledRetainBytes)) == "" {
		return 0
	}
	return nonNegativeIntEnv(EnvSettledRetainBytes, DefaultSettledRetainBytes)
}

// nonNegativeIntEnv 读一个非负整数环境变量；空/非法/负数一律退回默认。
//
// 为什么负数退回默认而不是当 0：负的上限/预算既不是"不限"也不是"不裁"，
// 是配置手滑；退回官方默认比把它解释成"关闭"更安全（关闭会让上限失效）。
func nonNegativeIntEnv(name string, def int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return def
	}
	return n
}

// trimSettledLocked 在任务结算时把保留窗口缩到已结算预算（官方 `jobs.settledRetainBytes`）。
//
// 只动**已结算**任务（调用点都在 done 置位之后），运行中的窗口由
// `maxRetainedJobOutput` 管——这是"裁剪不破坏正在运行的 job"的实现保证。
// total 是全量字节数、base 是已经裁掉的偏移，两者一起让 `page()` 如实报 truncated，
// 而不是把"少给了"伪装成"就这么多"。
func (t *task) trimSettledLocked() {
	budget := settledRetainBytes()
	if budget <= 0 || len(t.buf) <= budget {
		return
	}
	drop := len(t.buf) - budget
	copy(t.buf, t.buf[drop:])
	t.buf = t.buf[:budget]
	t.base += int64(drop)
}
