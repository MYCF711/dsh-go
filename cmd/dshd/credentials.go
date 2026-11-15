package main

import (
	"strings"

	"dshgo/internal/kernel"
	"dshgo/internal/llm"
	"dshgo/internal/spi"
)

// credentialsModule 把宿主的**独立凭据存储**接成 `spi.CredentialKey` 服务，
// 供模块按 ref 取值（目前是 modules/search 的 DeepSeek 原生搜索）。
//
// 真值只有一个：`<home>/state/model-catalog.json` 的 `credentials` 段 ——
// 官方 `credentials.describe/set/unset` 的后端（见 internal/llm）。这里**不
// 缓存任何值**，每次解析都重新打开这个文件：
//
//   - serve 的每一轮是**另一个进程**（`dshd run` 子进程），父进程内存里的目录
//     对子进程不可见，文件是两边唯一的通道；
//   - 于是「界面上刚存了 key，下一步搜索就生效」只能靠现读。
//     缓存一次就成了"存了要等重启"，正是这个缺口原来的症状。
//
// 为什么放在 Apply **之后**追加（而不是列进 builtinModules）：它不是可配置
// 能力，是宿主基础设施。放进 builtinModules 会被 dsh.toml 的白名单静默丢掉
// （Apply 对没列出的模块只打一行 Warn），症状同样回到「存了凭据却不生效」。
// plugin-runtime 模块用的是同一条理由。
func credentialsModule(home string) kernel.Module {
	return kernel.Module{
		ID:       "credentials",
		Backend:  kernel.BackendStatic,
		Provides: []string{"credentials"},
		Register: func(s *kernel.Scope) error {
			return kernel.Provide(s, spi.CredentialKey, spi.CredentialResolver(catalogCredentials{home: home}))
		},
	}
}

// catalogCredentials 是独立凭据存储上的只读取值实现。
//
// 它只回答"**库**里有没有这个 ref"，环境变量一概不问：一个凭据常有多个别名
// ref（DEEPSEEK_API_KEY / DSHD_SEARCH_API_KEY），环境回落必须留在模块侧，
// 否则"库里的别名 B"会被"环境里的别名 A"抢过去，把官方的「文件优先于环境」
// 反过来。语义见 spi.CredentialResolver。
type catalogCredentials struct{ home string }

// ResolveCredential 现读一次目录文件，返回 ref 在库里的值。
//
// 目录损坏（loadErr）时按"没有这个值"回答，调用方退化成只读环境 ——
// 目录损坏本身已经在 catalog API 上 fail-loud，不靠这条路径兜。
// 返回值只交给需要它的调用方（放进请求头），**不进日志/错误串**。
func (c catalogCredentials) ResolveCredential(ref string) (string, bool) {
	home := strings.TrimSpace(c.home)
	if home == "" || strings.TrimSpace(ref) == "" {
		return "", false
	}
	return llm.OpenModelCatalog(home).StoredCredential(ref)
}
