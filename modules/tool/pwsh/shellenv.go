package pwsh

// 受管 shell 环境（官方 base 插件 #46 `@deepseek-ai/dsh-shell-env`）在本工具里的落地。
//
// 与 bash 那边不同，pwsh 是**一次调用一个进程**，所以这里天然就是官方形状：
// 起进程之前 collect 一次快照（官方 `dsh-shell-env/src/index.ts:164` `collect`），
// 把继承环境里的 `DSH_*` 全部摘掉，再叠加这份快照——这就是官方
// "每次 shell 工具执行重建命名空间"的直接对应物（index.ts:93-99）。
//
// 未接线时（没有 `shellEnv` 能力：测试直接构造工具、或宿主没装这个模块）
// 退化为**不注入**：`cmd.Env` 保持 nil，子进程原样继承宿主环境，旧行为不变。

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
)

// managedEnviron 返回这次子进程的完整环境列表；nil 表示**未接线**（沿用继承环境）。
//
// 非 nil 时它是**完整**列表而不是增量：`exec.Cmd.Env` 一旦非 nil 就整体替换继承
// 环境，所以必须从 `os.Environ()` 出发摘掉 `DSH_*`，再叠加快照。
func (t *Tool) managedEnviron(ctx context.Context) ([]string, error) {
	p := t.shellEnvProvider()
	if p == nil {
		return nil, nil
	}
	snapshot, err := p.Collect(ctx)
	if err != nil {
		return nil, err
	}
	return managedEnvironList(os.Environ(), snapshot), nil
}

// shellEnvProvider 惰性 inject `shellEnv` 能力。
//
// 与 `execSeam` 同源的两个理由：R2（模块只拿 `*Scope`）+ 装配顺序无关
// （服务可能在工具模块之后才 provide；只缓存成功的结果，失败下次还会再试）。
func (t *Tool) shellEnvProvider() spi.ShellEnvProvider {
	t.envMu.Lock()
	defer t.envMu.Unlock()
	if t.envTried {
		return t.envProvider
	}
	if t.scope == nil {
		return nil
	}
	p, err := kernel.Inject(t.scope, spi.ShellEnvKey)
	if err != nil {
		return nil
	}
	t.envProvider = p
	t.envTried = true
	return p
}

// managedEnvironList 把继承环境里的 `DSH_*` 摘掉，再按键名排序叠加快照。
//
// 大小写不敏感：Windows 的环境名不区分大小写，`dsh_shell` 与 `DSH_SHELL` 是同一件事。
// `DSHD_*`（本仓宿主自己的变量）不受影响——前缀是 `DSH_`。
func managedEnvironList(base []string, snapshot map[string]string) []string {
	out := make([]string, 0, len(base)+len(snapshot))
	for _, kv := range base {
		if hasShellEnvPrefix(kv) {
			continue
		}
		out = append(out, kv)
	}
	keys := make([]string, 0, len(snapshot))
	for key := range snapshot {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		out = append(out, key+"="+snapshot[key])
	}
	return out
}

// hasShellEnvPrefix 判断一条 `KEY=VALUE` 的键名是否受管前缀。
func hasShellEnvPrefix(kv string) bool {
	name := kv
	if i := strings.IndexByte(kv, '='); i >= 0 {
		name = kv[:i]
	}
	if len(name) < len(spi.ShellEnvPrefix) {
		return false
	}
	return strings.EqualFold(name[:len(spi.ShellEnvPrefix)], spi.ShellEnvPrefix)
}

// managedEnvErrorCode 取受管环境失败的码；不是这类失败返回空串。
//
// 接口断言而不是 import `internal/shellenv`：模块之间不许互相 import（R2）。
func managedEnvErrorCode(err error) string {
	var coder interface{ ShellEnvCode() string }
	if errors.As(err, &coder) {
		return coder.ShellEnvCode()
	}
	return ""
}

// managedEnvHint 给受管环境失败一句能行动的下一步（R3：失败必须带 Hint）。
func managedEnvHint(err error) string {
	if code := managedEnvErrorCode(err); code != "" {
		return "受管 shell 环境（DSH_*）构建失败（" + code + "）：某个插件贡献的变量不合规——" +
			err.Error() + "；让用户修好或下线那个插件再试，重试同一次调用不会好"
	}
	return "受管 shell 环境（DSH_*）构建失败：" + err.Error() + "；先查 shellEnv 服务是否装好，重试同一次调用不会好"
}
