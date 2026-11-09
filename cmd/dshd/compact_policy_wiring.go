package main

// 压缩策略的三个入口共用解析（run / serve / acp）。
//
// 官方面：`context.compaction.headroomTokens`（默认 65536）与
// `context.compaction.modelPolicies`（按 provider/model 精确覆盖），
// 原件 tmp/GO3-PARAMS/_official/dsh-compaction-basic/lib/index.js:59-150。
//
// 为什么单独抽一个文件：这三个参数以前在宿主**没有任何读取点**（总账 §7 的
// UNREAD-GAP），而 run / serve / acp 各有一份自己的 flag 块。只在一个入口接
// 会造出"同一个模型换个入口阈值就不同"的差异——那种差异最难解释，也最容易
// 在真机上表现为"某条路径的上下文莫名其妙顶满"。
//
// 判据：
//  1. 三个入口解析出**同一个** compact.Policy（同默认值、同校验）；
//  2. 非法配置在**启动期** fail-loud（不是每次压缩时静默退回默认）；
//  3. 不配置时与既有行为逐字节一致（`Resolve` 返回默认策略）。

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"dshgo/internal/compact"
)

const (
	// EnvCompactHeadroomTokens 是官方 `context.compaction.headroomTokens`
	// 的环境变量入口（毫秒之外的 token 数，正整数）。
	EnvCompactHeadroomTokens = "DSHD_COMPACT_HEADROOM_TOKENS"
	// EnvCompactModelPolicies 是官方 `context.compaction.modelPolicies` 的
	// JSON 数组入口（每条形如 {"provider":"…","model":"…","headroomTokens":…}）。
	EnvCompactModelPolicies = "DSHD_COMPACT_MODEL_POLICIES"
)

// compactHeadroomFromEnv 解析 headroom 默认值：未设置 = 官方 65536。
//
// 非法值（非整数、负数）fail-loud：静默回落会让人以为"我关了安全带"，
// 实际跑的还是 64K 预留。
func compactHeadroomFromEnv(getenv func(string) string) (int, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	raw := strings.TrimSpace(getenv(EnvCompactHeadroomTokens))
	if raw == "" {
		return compact.DefaultHeadroomTokens, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s=%q 不是整数（token 数）：%w", EnvCompactHeadroomTokens, raw, err)
	}
	if n < 0 {
		return 0, fmt.Errorf("%s=%d 不能为负（0 = 不留安全带）", EnvCompactHeadroomTokens, n)
	}
	return n, nil
}

// compactPolicyFromFlags 把三个入口的 flag 值解析成一份有效策略。
//
// model 是这个进程固定服务的模型（宿主一条 llm 通道，模型在启动期定下来）；
// retry 是 env 解析出来的重试预算，作为默认值的一部分。
func compactPolicyFromFlags(model string, headroom int, rawPolicies string, retry compact.RetryPolicy) (compact.Policy, []compact.ModelPolicy, error) {
	if headroom < 0 {
		return compact.Policy{}, nil, fmt.Errorf("--compact-headroom-tokens 不能为负：%d（0 = 不留安全带）", headroom)
	}
	policies, err := compact.ParseModelPolicies(rawPolicies)
	if err != nil {
		return compact.Policy{}, nil, err
	}
	defaults := compact.DefaultPolicy().WithRetry(retry)
	defaults.HeadroomTokens = headroom
	return compact.Resolve(policies, defaults, model), policies, nil
}
