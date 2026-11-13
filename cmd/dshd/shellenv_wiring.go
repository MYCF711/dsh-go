package main

// 官方 base 插件 #46 `shell-env` 的装配。
//
// 官方形状（`@deepseek-ai/dsh-shell-env/src/index.ts:225-227`）：插件 `apply` 只是
// new 一个 `ShellEnvRegistry`，服务名 `shellEnv`（index.ts:112），dshHome 由
// `Config.dshHome` 给，默认 `$DSH_HOME` 或 `~/.dsh`（index.ts:35-37）。
//
// 本仓对应的三件事：
//   - 注册表在 `internal/shellenv`（R2：没有 init()，装配必须显式）；
//   - 能力经 `spi.ShellEnvKey` 供出（模块间只经契约层相遇）；
//   - 真正的消费者是 shell 工具自己：`modules/exec/bash` 与 `modules/tool/pwsh`
//     在**起进程之前**惰性 inject 这个键取快照（见两个包的 shellenv.go）。

import (
	"dshgo/internal/kernel"
	"dshgo/internal/shellenv"
	"dshgo/internal/spi"
)

// shellEnvModule 把受管 `DSH_*` 环境注册表装配成部署级能力 `shellEnv`。
//
// home 由各入口解析后一路带下来（本仓的 `--home` / `DSHD_HOME`），直接传给注册表：
// 官方那边 `resolveDshHome` 的兜底顺序（显式 > `$DSH_HOME` > `~/.dsh`）在
// `shellenv.ResolveHome` 里同样成立。
func shellEnvModule(home string) kernel.Module {
	return kernel.Module{
		ID:       "shell-env",
		Backend:  kernel.BackendStatic,
		Provides: []string{"shellEnv"},
		Register: func(s *kernel.Scope) error {
			registry := shellenv.NewRegistry(home)
			return kernel.Provide(s, spi.ShellEnvKey, spi.ShellEnvProvider(registry))
		},
	}
}
