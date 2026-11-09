package main

import (
	"dshgo/internal/acp"
	"dshgo/internal/pluginmgr"
)

// 宿主 runtime 版本的**注入点**。
//
// 版本号只有一个出处：`cmd/dshd/main.go` 的 `version` 变量（`scripts/build.sh` 也从它
// `sed` 出来并 `-ldflags -X main.version` 注入两个 main 包）。但"当前 runtime 版本"这件事
// 有两个下游需要知道，而库包不能 import main：
//
//   - `internal/pluginmgr`：判插件 `engines.dsh` / peer 范围，以及"只能为当前 runtime 授权"；
//   - `internal/acp`：`initialize` 握手里报的 `serverInfo.version`。
//
// 所以在这里注入一次，两个下游拿到**同一个值**。以前它们各自留了一份默认值字面量，
// 于是 2026-11-09 出现了分叉：`--version` 报的是新版本，插件兼容性却按那份没人更新的
// 旧字面量判（防复发守卫见 cmd/dshd/version_guard_test.go）。
//
// 调用点：`main()` 的第一条语句（每个子命令都经过它），以及 cmd/dshd 的测试 init
// （测试进程不走 main()）。两边调的是同一个函数、用的是同一个变量。
func wireHostVersion() {
	pluginmgr.SetHostRuntimeVersion(version)
	acp.SetFallbackVersion(version)
}

// acpFallbackVersion 暴露 ACP 兜底版本给守卫测试（生产代码不读它）。
func acpFallbackVersion() string { return acp.FallbackVersion() }
