package bash

// 受管 shell 环境（官方 base 插件 #46 `@deepseek-ai/dsh-shell-env`）在本执行层的落地。
//
// 官方语义（`dsh-shell-env/src/index.ts:93-99`）：命名空间**每一次 shell 工具执行**
// 重建——执行方先把环境里既有的 `DSH_*` 全部丢掉，再注入注册表的当前快照。
//
// 本仓有两处形状不同，必须在这里补上，否则这条语义就落不了地：
//
//  1. **bash 会话是常驻的**（一个进程用到底，`cwd` 跨命令保留），进程环境只在
//     `startSession` 时定一次。所以"每次执行重建"落在**每条命令的脚本**上：
//     命令之前先 `unset` 掉 shell 里既有的 `DSH_*`，再 `export` 这次快照。
//     启动时只做一件事——把**继承环境**里的 `DSH_*` 摘干净。
//  2. **exec 服务是部署级共享的**（一个 kernel 作用域一份 Service，可能服务多个
//     会话）。若把快照钉在进程环境里，第二个会话的命令会读到第一个会话的
//     `DSH_SESSION_ID`——那是编出来的身份，比"没有"更坏。每条命令现算才不串号。
//
// 未接线时（没有 `shellEnv` 能力：测试直接 `NewService`、或宿主没装这个模块）
// 一律退化为**不注入、也不清理**，既有行为一个字节都不变。

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
)

// managedEnv 返回这次执行要注入的受管快照。
//
// ok=false 表示**未接线**（拿不到 shellEnv 能力）：调用方按旧行为不注入、不清理。
// err 非 nil 表示贡献者违规（官方码 SHELL_ENV_CONTRIBUTION_INVALID）——
// 官方语义是"命令不跑"，所以调用方必须把它变成失败，不许降级成少注入几个变量。
func (s *Service) managedEnv(ctx context.Context) (map[string]string, bool, error) {
	p := s.shellEnvProvider()
	if p == nil {
		return nil, false, nil
	}
	env, err := p.Collect(ctx)
	if err != nil {
		return nil, false, err
	}
	return env, true, nil
}

// shellEnvProvider 惰性 inject `shellEnv` 能力。
//
// 惰性的两个理由，与 pwsh 工具那边同源：
//   - R2：模块只拿 `*Scope`，不拿全局单例；
//   - 装配顺序由能力图决定，注册表可能在 Service 之后才 provide，
//     装配期取一次会白白失败（这里也只在**成功**时缓存，失败下次还会再试）。
func (s *Service) shellEnvProvider() spi.ShellEnvProvider {
	s.envMu.Lock()
	defer s.envMu.Unlock()
	if s.envTried {
		return s.envProvider
	}
	if s.scope == nil {
		return nil
	}
	p, err := kernel.Inject(s.scope, spi.ShellEnvKey)
	if err != nil {
		return nil
	}
	s.envProvider = p
	s.envTried = true
	return p
}

// stripManagedEnv 丢掉继承环境里的全部 `DSH_*`。
//
// 大小写不敏感：Windows 的环境名不区分大小写，`dsh_home` 与 `DSH_HOME` 是同一件事。
// `DSHD_*`（本仓宿主自己的变量，如 `DSHD_BASH`）**不受影响**——前缀是 `DSH_`。
func stripManagedEnv(base []string) []string {
	out := make([]string, 0, len(base))
	for _, kv := range base {
		if hasManagedPrefix(kv) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// hasManagedPrefix 判断一条 `KEY=VALUE` 的键名是否受管前缀。
func hasManagedPrefix(kv string) bool {
	name := kv
	if i := strings.IndexByte(kv, '='); i >= 0 {
		name = kv[:i]
	}
	if len(name) < len(spi.ShellEnvPrefix) {
		return false
	}
	return strings.EqualFold(name[:len(spi.ShellEnvPrefix)], spi.ShellEnvPrefix)
}

// managedEnvScript 生成"重建受管命名空间"的那几行脚本（每条命令之前写进 stdin）。
//
// 为什么是 `export` 而不是 `VAR=value cmd` 前缀：命令可能是多行、多条语句，
// 前缀形式只作用于第一条语句。为什么不用子 shell `( … )`：那会丢掉 `cd` 的
// 跨命令保留——那是这个工具的既有契约（`session.go` 的 cwd 语义）。
//
// `${!DSH_@}` 是 bash 的前缀展开：一个匹配都没有时展开为空，循环体不执行，
// 所以空环境也安全。命令里若自己 `export DSH_*`，下一次执行会先被这里清掉——
// 与官方"每次都重建"一致。
func managedEnvScript(env map[string]string) string {
	var b strings.Builder
	b.WriteString("for __dshd_k in ${!DSH_@}; do unset \"$__dshd_k\"; done; unset __dshd_k\n")
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		b.WriteString("export")
		for _, key := range keys {
			// 键名来自注册表，形状已被 `^DSH_[A-Z][A-Z0-9_]*$` 校验过，不用引；
			// 值一律单引号包裹（POSIX 里单引号内不解释任何字符）。
			b.WriteString(" " + key + "=" + shellSingleQuote(env[key]))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// shellScript 组装写进常驻 bash 的一条命令。
//
// 与改造前的唯一区别：`managed` 为真时在命令**之前**插入重建脚本；为假时
// 逐字节保持旧形状（未接线路径不许改变既有行为）。
func shellScript(cmd string, seq int, env map[string]string, managed bool) string {
	var b strings.Builder
	if managed {
		b.WriteString(managedEnvScript(env))
	}
	fmt.Fprintf(&b, "{ %s\n} 2>&1; __dshd_rc=$?\nprintf '\\001DSHD:%d:%%d\\001%%s\\001\\n' \"$__dshd_rc\" \"$PWD\"\n",
		cmd, seq)
	return b.String()
}

// shellSingleQuote 把值转成 POSIX 单引号字面量：外层用单引号包住，
// 值里出现的单引号按 POSIX 规矩断开再转义（闭引号 + 反斜杠转义 + 重新开引号），
// 这样 $、反引号、反斜杠、换行在值里全都是字面量。
func shellSingleQuote(v string) string {
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}

// managedEnvErrorCode 取受管环境失败的码；不是这类失败就返回空串。
//
// 用接口断言而不是 import `internal/shellenv`：模块之间不许互相 import（R2），
// 错误类型只需要暴露一个方法就够了（官方那边是 `err.code`）。
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
