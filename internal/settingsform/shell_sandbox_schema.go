package settingsform

// 官方 shell 执行器的两个设置命名空间：`bash-sandbox` 与 `pwsh-sandbox`。
//
// # 为什么这两条必须存在
//
// 官方 UI 包 `dsh-client-ui-settings-shell` 的页面在
// `src/client/index.ts:73` 挂了 `ctx.configForms.whileServed([BASH_NS, PWSH_NS], ...)`，
// 两个命名空间**一个都没有**时整页不出现（GUI 侧 4 条 MISSING 设置页之一）。
//
// 注意命名空间名**不是** `shell`：官方客户端常量是
//
//	dsh-client-ui-settings-shell/src/client/shell-card-controller.ts:11,13
//	  /** Profile entry id of the POSIX shell executor */
//	  export const BASH_NS = 'bash-sandbox'
//	  /** Profile entry id of the PowerShell executor */
//	  export const PWSH_NS = 'pwsh-sandbox'
//
// 因为页面编辑的其实是**执行器条目**的 Config，而不是某个叫 shell 的插件。
//
// # 权威源（随 rc.2 包发布的 TypeScript 原件）
//
//	<official-rc2>/dsh/node_modules/@deepseek-ai/
//	  dsh-bash-sandbox/src/index.ts:36,48-49
//	    export type Config = LocalConfig          // 继承 dsh-bash-local 的 Config
//	  dsh-bash-local/src/index.ts:100-107
//	    static Config = z.object({
//	      cwd: z.string().volatile(),
//	      timeoutMs: z.number().default(120_000).volatile(),
//	      maxTimeoutMs: z.number().default(600_000).volatile(),
//	      maxOutputBytes: z.number().default(64_000).volatile(),
//	      maxSpillBytes: z.number().default(DEFAULT_MAX_SPILL_BYTES).volatile(),
//	      graceMs: z.number().default(DEFAULT_GRACE_MS).volatile(),
//	    })
//	  dsh-pwsh-local/src/index.ts:126-133（同上，另加 pwshPath）
//	  dsh-client-ui-settings-shell/src/client/shell-card-controller.ts:21-27,58
//	    interface ShellSettings { timeoutMs?: number; maxOutputBytes?: number }
//	    // "The shell fields this page edits — a subset of the served schema by design."
//
// 页面只编辑 `timeoutMs` / `maxOutputBytes` 两个字段——官方自己把这条写成
// "a subset of the served schema by design"，所以这里发的就是这两个；执行器
// Config 里另外四条（cwd / maxTimeoutMs / maxSpillBytes / graceMs，pwsh 还有
// pwshPath）是**本仓没有对应消费方**的执行器级旋钮，见文件末尾的缺口登记。
//
// # 线上 envelope（实跑取证）
//
// 取证脚本 tmp/GO8-RC2/_hs4/dump-hs4.mjs 直接 require 官方编译产物
// `dsh-bash-local/lib/index.js` / `dsh-pwsh-local/lib/index.js` 的 `Config.toJSON()`
// 再走官方 plainSchema/volatileForm（删 meta.volatile），得到：
//
//	{"type":"number","meta":{"default":120000}}   // timeoutMs
//	{"type":"number","meta":{"default":64000}}    // maxOutputBytes
//
// 也就是**裸 number、没有 step/min/max**。宿主在 envelope 上照抄这个形状
// （uiKindNumber），但**写路径仍只接受整数**：本仓 L1 表单只画整数输入框，
// 而这两个字段的真实语义就是整数毫秒 / 整数字节。
//
// # 本仓的消费方（真生效）
//
//   - `pwsh-sandbox`：两个字段都**真生效**。modules/tool/pwsh 的官方对齐常量是
//     `DefaultTimeout`（120s）与 `DefaultMaxOutputBytes`（64000，与官方
//     `maxOutputBytes` 默认值逐字一致；此前宿主写 64<<10=65536），
//     `WithTimeoutBudget` / `WithMaxOutputBytes` 覆盖它们；cmd/dshd 装配期用
//     `pwshBudgetOptions`（cmd/dshd/settings_effective.go）只注入**用户显式
//     写过**的值。
//   - `bash-sandbox`：两个字段**没有消费方**（可读写、有官方默认值、能往返，
//     但不改变行为）。宿主 bash 执行器（modules/exec/bash）的旋钮是
//     IdleBudget（多久无输出转后台，默认 10 分钟）、GraceBudget（停止宽限，
//     DSHD_BASH_GRACE_MS，默认 3000ms）与 retention 保留量，**没有**"前台命令
//     总超时"与"每路输出内存上限"这两个语义。把 timeoutMs 硬接到 ctx 超时上
//     会改变真实行为（长命令被 120s 掐掉），把 maxOutputBytes 接到 retention 上
//     会把 1MiB 运行窗口改成 64KB——两者都不是"接线"，是**换语义**，所以不做。
//     这是真实缺口，登记在交付报告与 docs 里，不假装接上。

import "net/http"

const (
	// NamespaceBashSandbox 是 POSIX shell 执行器条目的命名空间（逐字）。
	NamespaceBashSandbox = "bash-sandbox"
	// NamespacePwshSandbox 是 PowerShell 执行器条目的命名空间（逐字）。
	NamespacePwshSandbox = "pwsh-sandbox"
	// FieldShellTimeoutMs 是前台命令超时（毫秒）。
	FieldShellTimeoutMs = "timeoutMs"
	// FieldShellMaxOutputBytes 是每路输出在内存里的字节上限。
	FieldShellMaxOutputBytes = "maxOutputBytes"
)

const (
	// DefaultShellTimeoutMs 是官方 `timeoutMs` 默认值（bash-local:102 / pwsh-local:128 的 120_000）。
	DefaultShellTimeoutMs = 120000
	// DefaultShellMaxOutputBytes 是官方 `maxOutputBytes` 默认值（bash-local:104 / pwsh-local:130 的 64_000）。
	DefaultShellMaxOutputBytes = 64000
)

// shellSandboxNamespace 造一份 shell 执行器命名空间的声明（两个平台同源）。
func shellSandboxNamespace(ns, title string) uiNamespace {
	return uiNamespace{
		ns:    ns,
		title: title,
		fields: []uiField{
			// 官方是裸 `z.number().default(...)`：没有 step、没有 min/max。
			uiBareNumberField(FieldShellTimeoutMs, DefaultShellTimeoutMs),
			uiBareNumberField(FieldShellMaxOutputBytes, DefaultShellMaxOutputBytes),
		},
		base: map[string]any{
			FieldShellTimeoutMs:      DefaultShellTimeoutMs,
			FieldShellMaxOutputBytes: DefaultShellMaxOutputBytes,
		},
		check: func(value map[string]any) error { return checkShellSandboxBudgets(ns, value) },
	}
}

// checkShellSandboxBudgets 是官方执行器构造函数里那两行运行时判据的等价物：
//
//	dsh-bash-local/src/index.ts:80-82
//	  assertPositiveFinite('timeoutMs', config.timeoutMs.get())
//	  assertPositiveFinite('maxOutputBytes', config.maxOutputBytes.get())
//
// 官方 schema 上不设界（envelope 就没有 min），非法值是在**构造执行器**时抛错的；
// 本仓在**写路径**就拒（`invalid_value`），否则写进去要等到下次装配才炸，
// 而炸的时候用户早忘了自己写过什么。0 / 负数都不是"关闭"，是坏值：
// 0 毫秒超时会让每条命令立刻失败，0 字节上限会让输出永远是空的。
func checkShellSandboxBudgets(ns string, value map[string]any) error {
	for _, field := range []string{FieldShellTimeoutMs, FieldShellMaxOutputBytes} {
		raw, present := value[field]
		if !present || raw == nil {
			continue
		}
		number, ok := integerValue(raw)
		if !ok {
			// 类型错误已由字段校验先报过；这里只兜一次，保持 fail-loud。
			return newInvalidTypeError([]string{ns, field}, "integer", raw)
		}
		if number <= 0 {
			return &Error{
				Code: "invalid_value",
				Message: joinPath([]string{ns, field}) + " 必须是正整数（毫秒/字节），收到 " +
					formatInt(number) + "；官方执行器构造期同样只接受正的有限数",
				Hint:   "取一个正整数：官方默认 timeoutMs=120000、maxOutputBytes=64000",
				Status: http.StatusBadRequest,
			}
		}
	}
	return nil
}

// buildShellSandboxNamespaceSpecs 返回两条 shell 执行器命名空间（顺序与官方页面
// 的服务顺序一致：先 bash 后 pwsh）。
func buildShellSandboxNamespaceSpecs() []NamespaceSpec {
	return []NamespaceSpec{
		uiNamespaceSpec(shellSandboxNamespace(NamespaceBashSandbox, "Bash shell")),
		uiNamespaceSpec(shellSandboxNamespace(NamespacePwshSandbox, "PowerShell shell")),
	}
}
