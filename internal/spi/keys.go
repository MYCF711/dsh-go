package spi

import "dshgo/internal/kernel"

// 服务键集中在这里：模块之间只经 spi 相遇，不互相 import 对方的包。
//
// 这是 R2「SPI 之外无通路」的具体化——如果两个模块需要共享一个键，
// 说明那个键本来就属于契约层，而不是某一方的实现细节。
var (
	ToolsKey      = kernel.NewKey[ToolRegistry]("tools")
	ToolRunnerKey = kernel.NewKey[Runner]("toolrunner")
	FSKey         = kernel.NewKey[FS]("fs")
	ExecKey       = kernel.NewKey[Exec]("exec")
	JobKey        = kernel.NewKey[Job]("jobs")
)
