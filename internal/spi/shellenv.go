package spi

import (
	"context"

	"dshgo/internal/kernel"
)

// ShellEnvPrefix 是受管 shell 环境变量的前缀（官方 `DSH_ENV_PREFIX`，
// 来自 `@deepseek-ai/dsh-shell`）。
//
// 放在契约层是为了让"谁该被清掉"只有一个来源：`internal/shellenv` 用它拼键名，
// 真实 shell 工具（bash / pwsh）用它把继承环境里的 `DSH_*` 摘干净——
// 两边各写一份字面量迟早会漂成两个前缀。
const ShellEnvPrefix = "DSH_"

// ShellEnvProvider 提供服务：本次 shell 工具执行的受管 `DSH_*` 快照。
//
// 官方形状（`@deepseek-ai/dsh-shell-env/src/index.ts:164` `ShellEnvRegistry.collect`）：
// 注册表在**每次**执行时现算一份环境，返回的映射就是这次要注入子进程的那一份。
// Go 侧一样的形状，只是 `execution` 换成本仓的执行上下文——会话身份在
// `spi.SessionFrom(ctx)` 里（没有会话就如实不给 `DSH_SESSION_ID`）。
//
// 谁提供：`internal/shellenv` 的 Registry（装配期经 `kernel.Provide(scope, ShellEnvKey, …)`）。
// 谁消费：真实的 shell 工具（`modules/exec/bash`、`modules/tool/pwsh`）在起进程之前取一次。
//
// 为什么放在 spi：工具模块不许 import 别的模块（R2，见 tool.go 包注释），
// 共享能力只能经中立契约层相遇——与 `ExecKey` / `JobKey` 同一套路。
type ShellEnvProvider interface {
	// Collect 返回这次执行的受管环境快照：内置事实与当前贡献者合入后的结果。
	//
	// 键名一律 `DSH_` 前缀；返回值是**独立副本**，调用方改它不影响注册表。
	// 有贡献者声明了非法内容时返回 error（官方码 SHELL_ENV_CONTRIBUTION_INVALID），
	// 调用方必须让命令**不跑**，而不是降级成"少注入几个变量"。
	Collect(ctx context.Context) (map[string]string, error)
}

// ShellEnvKey 是 `shellEnv` 能力的服务键；名字与官方服务名逐字一致
// （`index.ts:112` `super(ctx, 'shellEnv')`）。
var ShellEnvKey = kernel.NewKey[ShellEnvProvider]("shellEnv")
