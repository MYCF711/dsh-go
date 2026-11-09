package main

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"
)

// 官方 cordis 宿主运行器的两个默认预算：
//
//   - `cordis.hostRunner.vmTimeoutMs`（默认 5000）：单次插件脚本执行的 VM 预算；
//   - `cordis.hostRunner.clientInspectTimeoutMs`（默认 10000）：inspect 查询的专用预算。
//
// 宿主原来两处都没有默认预算：`internal/cordis/extern` 只有"起进程 + 握手"的 15s
// 启动超时，真正的脚本调用完全由调用方 ctx 决定。而调用方是 HTTP 请求，
// 只要插件自己死循环，这个请求就一直挂着——官方这两条参数存在的理由正是这个。
//
// 落点选在 `cmd/dshd/serve_cordis_api.go` 的两个真实调用点（Invoke / ResolveInspectQuery）：
// 那是宿主唯一消费这两个能力的地方，预算必须加在"谁在等"那一侧。
const (
	// DefaultCordisVMTimeoutMS 是官方 `cordis.hostRunner.vmTimeoutMs` 的默认值。
	DefaultCordisVMTimeoutMS = 5000
	// DefaultCordisClientInspectTimeoutMS 是官方 `cordis.hostRunner.clientInspectTimeoutMs` 的默认值。
	DefaultCordisClientInspectTimeoutMS = 10000
	// EnvCordisVMTimeoutMS 覆盖脚本执行预算（毫秒）；0 = 不设预算。
	EnvCordisVMTimeoutMS = "DSHD_CORDIS_VM_TIMEOUT_MS"
	// EnvCordisClientInspectTimeoutMS 覆盖 inspect 预算（毫秒）；0 = 不设预算。
	EnvCordisClientInspectTimeoutMS = "DSHD_CORDIS_CLIENT_INSPECT_TIMEOUT_MS"
)

// cordisVMTimeout 解析脚本执行预算：默认 5000ms；0 = 不设预算；非法值退回默认。
func cordisVMTimeout() time.Duration {
	return cordisBudgetFromEnv(EnvCordisVMTimeoutMS, DefaultCordisVMTimeoutMS)
}

// cordisClientInspectTimeout 解析 inspect 预算：默认 10000ms。
func cordisClientInspectTimeout() time.Duration {
	return cordisBudgetFromEnv(EnvCordisClientInspectTimeoutMS, DefaultCordisClientInspectTimeoutMS)
}

func cordisBudgetFromEnv(name string, defMS int) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return time.Duration(defMS) * time.Millisecond
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return time.Duration(defMS) * time.Millisecond
	}
	return time.Duration(n) * time.Millisecond
}

// withCordisBudget 给一次 cordis 调用套上默认预算。
//
// 两个细节：调用方 ctx 已经带更短的 deadline 时**保留更短的那个**（不要把上游的
// 收紧预算放宽）；budget<=0 表示显式不设预算，直接返回原 ctx（配套的 cancel 是空操作，
// 调用方可以无脑 defer）。
func withCordisBudget(ctx context.Context, budget time.Duration) (context.Context, context.CancelFunc) {
	noop := func() {}
	if budget <= 0 {
		return ctx, noop
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= budget {
		return ctx, noop
	}
	return context.WithTimeout(ctx, budget)
}
