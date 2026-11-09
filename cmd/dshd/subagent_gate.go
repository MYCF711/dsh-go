package main

// 官方 `subagent.maxActiveSubagents` 的宿主落点：**真并发闸门**。
//
// 官方原文（tmp/GO7-RELEASE/gaps-agentloop/official-ref/dsh-subagent__README.md:54）：
//
//	Set `maxActiveSubagents` on the host plugin to limit live and materializing
//	children in each activation tree. It defaults to `8` and accepts positive
//	safe integers. A top-level parent starts a pool shared by its local and
//	external descendants; the root parent consumes no slot.
//
// 宿主的形状：一次委派 = 一次 `subRunner.RunSub`（同步跑到底）。所以"在飞的孩子"
// 就是"正在 RunSub 的 goroutine"，闸门 = 一个带缓冲 channel 的信号量：拿不到槽就
// **等**（可被 ctx 取消），而不是先全部跑起来再指望机器扛住。
//
// 局限（写进 docs/tool-parity.md 与 RESULT）：
// `dshd serve` 每一轮是独立的 `dshd run` 子进程，池是**进程级**的——多个会话
// 各自开子进程时各有各的 8 个额度。要做到全机共享额度，得有一个跨进程的租约
// （文件锁/本地守护进程），那是另一个量级的改动，不在这一条缺口的范围内。

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

const (
	// officialDefaultMaxActiveSubagents 是官方默认值（README:54）。
	officialDefaultMaxActiveSubagents = 8

	// SubagentMaxActiveEnv 是宿主侧的覆盖项（正整数）。
	//
	// 本机 desktop profile 把同一条参数覆盖成 20（官方 live profile 的
	// `subagent.maxActiveSubagents: 20`）。两个域共用这一处默认值解析，
	// 不各写一份：否则"agent-loop 域说 8、live-profile 域说 20"会变成两套真值。
	SubagentMaxActiveEnv = "DSHD_SUBAGENT_MAX_ACTIVE_SUBAGENTS"
)

// subagentGate 是子代理并发池。
type subagentGate struct {
	slots  chan struct{}
	limitN int
	// inFlight 只用于诊断与用例断言（"现在有几个在跑"），不参与判断。
	inFlight int32
}

// newSubagentGate 造一个上限为 limit 的池；<=0 时退回官方默认值。
func newSubagentGate(limit int) *subagentGate {
	if limit <= 0 {
		limit = officialDefaultMaxActiveSubagents
	}
	return &subagentGate{slots: make(chan struct{}, limit), limitN: limit}
}

// limit 返回池的容量。
func (g *subagentGate) limit() int {
	if g == nil {
		return 0
	}
	return g.limitN
}

// active 返回当前在飞的子代理数。
func (g *subagentGate) active() int {
	if g == nil {
		return 0
	}
	return int(atomic.LoadInt32(&g.inFlight))
}

// acquire 拿一个槽；槽满就等（ctx 取消即返回）。
//
// 返回的 release 是可重复调用的：收尾路径上 defer 与显式调用同时存在也不会
// 把额度还两次（还两次会让限额悄悄变大）。
func (g *subagentGate) acquire(ctx context.Context) (func(), error) {
	if g == nil {
		return func() {}, nil
	}
	select {
	case g.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	atomic.AddInt32(&g.inFlight, 1)
	var once sync.Once
	return func() {
		once.Do(func() {
			atomic.AddInt32(&g.inFlight, -1)
			<-g.slots
		})
	}, nil
}

// subagentGateFromEnv 按环境变量造池：不设 = 官方默认 8；非法 = 响亮失败。
//
// 非法值不许静默回退：配置写着 0 或 `eight` 却跑在 8 上，用户会以为"我调过了"。
func subagentGateFromEnv(env func(string) string) (*subagentGate, error) {
	if env == nil {
		return newSubagentGate(officialDefaultMaxActiveSubagents), nil
	}
	raw := strings.TrimSpace(env(SubagentMaxActiveEnv))
	if raw == "" {
		return newSubagentGate(officialDefaultMaxActiveSubagents), nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 {
		return nil, fmt.Errorf("%s=%q 不是正整数：子代理并发上限必须 > 0（官方默认 %d）",
			SubagentMaxActiveEnv, raw, officialDefaultMaxActiveSubagents)
	}
	return newSubagentGate(limit), nil
}
