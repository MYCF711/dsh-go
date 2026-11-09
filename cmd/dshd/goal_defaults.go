package main

import (
	"os"
	"strconv"
	"strings"
)

// 官方 goal 服务的 `goal.defaultMaxGoalRounds`：新建 goal 时的默认轮次预算 256。
//
// 官方 `resolveMaxGoalRounds` 要求正整数并在 create 时物化，所以"没给上限"在官方
// 那里不是"无限"，而是 256。宿主原来在 `session.setGoal` 建 goal 时落 0（= 无上限），
// 结果是自动续跑要么靠人盯、要么靠调用方显式给上限——一个默认值都没有。
//
// 为什么默认值是 256 而不是别的：那是官方逐值给出的默认，改它就是改契约。想退回
// 旧行为（无上限）有两个显式出口：给 create 传 maxGoalRounds=0，或设
// DSHD_GOAL_DEFAULT_MAX_ROUNDS=0。
const (
	// DefaultGoalMaxRounds 是官方 `goal.defaultMaxGoalRounds` 的默认值。
	DefaultGoalMaxRounds = 256
	// EnvGoalDefaultMaxRounds 覆盖"新建 goal 时没给上限"的默认预算；0 = 显式不限。
	EnvGoalDefaultMaxRounds = "DSHD_GOAL_DEFAULT_MAX_ROUNDS"
)

// goalDefaultMaxRounds 返回新建 goal 的默认轮次上限。
//
// env 为负数/非数字时退回官方默认（一个手滑的 env 不该把预算变成负数——
// 负数在轮次判定里既不是"不限"也不是"有上限"，只会让语义含糊）。
func goalDefaultMaxRounds() int {
	raw := strings.TrimSpace(os.Getenv(EnvGoalDefaultMaxRounds))
	if raw == "" {
		return DefaultGoalMaxRounds
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return DefaultGoalMaxRounds
	}
	return n
}
