package spi

import "dshgo/internal/kernel"

// CredentialResolver 是"某个 credential ref 在**可写凭据存储**里现在是什么值"
// 的唯一取回入口。
//
// 为什么这条接缝必须存在：官方 `credentials.set` 写的是 provider 管理的可写
// 文件，解析时**文件优先于继承的进程环境**。宿主侧的凭据真值（独立凭据存储）
// 在 internal/llm，而 modules/ 不许反向 import 宿主业务包（R1 内核不识业务），
// 所以只能由宿主在装配时把它注册成服务，模块按 ref 问值。
//
// 语义钉死为"**只**答可写存储里的值"：查不到返回 ("", false)。
//
// 为什么环境变量不在这里兜底（官方 provider 的 get() 是带 env 回落的）：
// 同一个凭据在宿主里常有两三个别名 ref（如 DEEPSEEK_API_KEY /
// DSHD_SEARCH_API_KEY）。若这里按 ref 逐个回落 env，调用方"先问库、再问 env"
// 的顺序就退化成"第一个 ref 的 env 赢过第二个 ref 的库值"——那正好违反官方的
// "文件优先于环境"。把回落留在调用方，别名集合与优先级才由同一处决定：
// 任意一个 ref 在库里，就赢过任意一个环境变量。
type CredentialResolver interface {
	// ResolveCredential 返回 ref 在凭据存储里的当前值。
	// 返回值只给需要它的调用方（放进请求头一类），**绝不进日志/错误串**。
	ResolveCredential(ref string) (string, bool)
}

// CredentialKey 提供服务：独立凭据存储的只读取值。
//
// 没有这个服务时模块必须退化成"只读环境变量"（与接入前一致），
// 不许因此装配失败：干净 home、dsh.toml 白名单、测试装配都不保证它在。
var CredentialKey = kernel.NewKey[CredentialResolver]("credentials")
