package shellenv

// CodeContributionInvalid 是唯一的错误码（官方 `SHELL_ENV_ERROR_CODES`，
// `dsh-shell-env/src/error.ts:10`）：某个已注册的贡献者返回了未声明的键，
// 或者返回的值不是字符串。此时**命令不跑**——贡献者得先修好。
const CodeContributionInvalid = "SHELL_ENV_CONTRIBUTION_INVALID"

// ShellEnvError 是带稳定错误码的 shell 环境失败（官方 `ShellEnvError extends HarnessError`）。
//
// 与注册期错误的区别（对齐官方）：注册期的六条失败是普通 error（官方就是
// `new Error(...)`，没有码），只有 collect 期这两条带码——因为只有它们会让
// 一条**已经在跑的命令**停下来，调用方需要按码分支而不是读中文。
type ShellEnvError struct {
	// Message 指明是哪个贡献者的哪个键出的问题（文案逐字对齐官方）。
	Message string
	// Code 是消费方路由用的失败类别。
	Code string
}

func (e *ShellEnvError) Error() string { return e.Message }

// ShellEnvCode 返回错误码。
//
// 存在的理由是 R2：真实 shell 工具（bash / pwsh）不许 import 本包，但又需要按码
// 分支给模型一句能行动的 Hint。有了这个方法，消费方只需接口断言
// `interface{ ShellEnvCode() string }`，不必认识 `ShellEnvError` 这个类型
// （官方那边是 `err.code`，形状等价）。
func (e *ShellEnvError) ShellEnvCode() string { return e.Code }
