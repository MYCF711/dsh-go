// Package pwsh 提供 `pwsh` 工具：在 Windows 上（或任何装了 PowerShell 7 的机器上）
// 执行一条 PowerShell 命令，把合并后的输出交回模型。
//
// 与 bash 工具的关系：两者是同一件事的两个壳，选哪个由壳的可用性决定，不由模型猜。
// 本工具优先用 pwsh（PowerShell 7，默认 UTF-8），退回到 powershell.exe
// （Windows PowerShell 5.1），后者要额外把控制台输出编码掰成 UTF-8，否则中文全变乱码。
//
// 三条实现上的注意点（都是踩过的坑）：
//  1. -NoProfile / -NonInteractive：不加载用户 profile、不弹交互提示——否则可能挂死；
//  2. 输出统一限长：命令一跑就是几百 KB，不拦的话截断层之外还多一层浪费；
//  3. 非零退出码是**命令失败**而不是**工具失败**：输出照回，错误照报，Hint 给下一步。
package pwsh

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"dshgo/internal/kernel"
	"dshgo/internal/sandbox"
	"dshgo/internal/spi"
)

// schema 是模型看到的那张表，**逐字对齐官方 rc.2**（`@deepseek-ai/dsh-tool-pwsh`）：
//
//	properties: description / command / timeoutMs / workdir / run_in_background
//	required:   description / command
//
// 两条与官方的差异都是"只加不减"（能力下限规则，见 AGENTS.md）：
//   - `timeout_seconds` 是我们的旧参数，保留为**兼容别名**；同时给时以官方名
//     `timeoutMs` 为准（官方名优先，见 resolveTimeout）。
//   - `sandbox_permissions` + `justification` 是官方 pwsh 工具**本来就有**的一对
//     （`@deepseek-ai/dsh-tool-pwsh/src/index.ts:432-440`，官方在 pwsh 这一侧同样
//     广告升级阶梯；旧注释"官方把它放在 bash 那一侧"是错的），描述逐字取官方
//     `sandboxPermissionsDescription('command')`。
var schema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "description": {"type": "string", "description": "这段命令干什么，5-10 个字，给用户看的（例如「列出当前目录」「查看进程列表」）"},
    "command": {"type": "string", "description": "要执行的 PowerShell 命令（一条或多条，用分号分隔）"},
    "timeoutMs": {"type": "number", "description": "超时毫秒数；省略用默认 120000，上限 600000"},
    "workdir": {"type": "string", "description": "可选：工作目录（相对当前工作目录），默认当前目录"},
    "run_in_background": {"type": "boolean", "description": "立刻转后台并返回 job_id，不等命令结束（长任务用这个）：用 job_output 取输出、job_kill 终止"},
    "timeout_seconds": {"type": "integer", "description": "已废弃：旧版超时秒数；与 timeoutMs 同时给时以 timeoutMs 为准"},
    "sandbox_permissions": {"type": "string", "enum": ["workspace-write", "danger-full-access"], "description": "The narrowest wider sandbox mode for a one-shot retry of the exact command the sandbox just denied; the retry asks the user for approval."},
    "justification": {"type": "string", "description": "Required with sandbox_permissions: one sentence for the user explaining why this exact command needs the wider access. Use the language of the user’s current request."}
  },
  "required": ["description", "command"],
  "additionalProperties": false
}`)

const (
	// DefaultMaxOutputBytes 是单次调用回给模型的输出上限（字节）。命令输出通常
	// 远超这个量，与其让它把上下文撑爆，不如这里就截断并明说。
	//
	// 取值逐字对齐官方：`@deepseek-ai/dsh-pwsh-local/src/index.ts:130` 的
	// `maxOutputBytes: z.number().default(64_000)` —— 是 64000，**不是** 64KiB(65536)。
	DefaultMaxOutputBytes = 64000
	// DefaultTimeout 是"模型没给超时"时实际用的默认值（官方 `timeoutMs` 的出厂值）。
	// 官方 `@deepseek-ai/dsh-pwsh-local/src/index.ts:128`：
	// `timeoutMs: z.number().default(120_000)`。
	DefaultTimeout = 120 * time.Second
	// maxTimeout 是最长超时。官方 `@deepseek-ai/dsh-pwsh-local/src/index.ts:129` 的
	// `maxTimeoutMs: z.number().default(600_000)`；超过这个量级的任务该用后台任务
	// 工具，不是这里。
	maxTimeout = 600 * time.Second
)

// legacyPrologue 只在退回 Windows PowerShell 5.1 时加：它重定向时按控制台代码页
// 写字节，中文会变成乱码。pwsh 7 默认就是 UTF-8，不需要动。
const legacyPrologue = `[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; $OutputEncoding = [System.Text.Encoding]::UTF8; `

// execCommandContext 是包内唯一的进程构造点。
// 测试把它换成一个计数桩，直接证明"策略拒绝时零进程"，而不是只看副作用。
var execCommandContext = exec.CommandContext

type Tool struct {
	// gate 是命令策略闸门；nil = 策略层未接线（旧行为，但拒绝任何一次性升级请求）。
	gate *sandbox.Gate
	// lastDecision 只保留给错误结果的结构化 Data；授权判断本身不读它。
	lastDecision *sandbox.Decision
	// scope 是装配期捕获的作用域：`run_in_background` 要把命令登记进宿主的
	// jobs 运行时，而工具模块不许直接 import 别的模块（R2），只能经 spi.Exec 接缝。
	// 惰性 inject（Exec 时才取）：装配顺序变了也不会把工具装配搞红。
	scope *kernel.Scope
	// execSeam 是测试注入的执行接缝；nil 时按 scope 取 spi.Exec。
	execSeam spi.Exec
	// timeoutBudget 是宿主注入的默认超时，对应官方设置 `pwsh-sandbox.timeoutMs`
	// （`@deepseek-ai/dsh-pwsh-local/src/index.ts:128`）。0 = 未注入 → DefaultTimeout；
	// 模型显式给 timeoutMs / timeout_seconds 时仍以模型为准（见 resolveTimeout）。
	timeoutBudget time.Duration
	// maxOutputBytes 是宿主注入的输出上限（字节），对应官方设置
	// `pwsh-sandbox.maxOutputBytes`（`index.ts:130`）。0 = 未注入 → DefaultMaxOutputBytes。
	maxOutputBytes int
	// envMu / envProvider / envTried 是 `shellEnv` 能力（受管 DSH_* 环境）的惰性缓存，
	// 见 shellenv.go。
	envMu       sync.Mutex
	envProvider spi.ShellEnvProvider
	envTried    bool
	// aclRootParent 是受控根父目录（`sandbox.EnvACLRoot`）。空 = **未启用** ACL runner，
	// 命令仍以宿主用户权限运行，enforcement 如实报 policy-only。
	//
	// 默认关闭是有意的：受控根把命令的可写面收在一个宿主自有的目录里，
	// workspace-write 下就写不进用户工作区了；要启用必须显式配置。
	aclRootParent string
	// homeDir 在默认受控根路径 `<home>/sandbox-roots/<session-id>/` 里用。
	homeDir string
	// runnerMu / runners 是每会话的 ACL runner 缓存（含「启用但用不了」的原因）。
	// 一个会话一个受控根；探测失败会缓存原因，不会每条命令重试一次 pwsh。
	runnerMu sync.Mutex
	runners  map[string]sessionRunnerState
}

// sessionRunnerState 是一个会话的 runner 结论：要么 runner 可用，要么 note 说明为什么退回了。
type sessionRunnerState struct {
	runner *sandbox.Runner
	note   string
}

// Option 是 Tool 的可选项（测试注入与将来宿主接线用）。
type Option func(*Tool)

// WithSandboxGate 注入命令策略闸门。nil 被忽略。
func WithSandboxGate(g *sandbox.Gate) Option {
	return func(t *Tool) {
		if g != nil {
			t.gate = g
		}
	}
}

// WithExecSeam 注入执行接缝（spi.Exec）。只给测试与"宿主自建 scope"用：
// 生产由 Module 把装配作用域交给工具，Exec 时从中取 spi.Exec。
func WithExecSeam(e spi.Exec) Option {
	return func(t *Tool) {
		if e != nil {
			t.execSeam = e
		}
	}
}

// WithTimeoutBudget 注入官方设置 `pwsh-sandbox.timeoutMs` 的生效值
// （`@deepseek-ai/dsh-pwsh-local/src/index.ts:128`）作为"模型没给超时"时的默认值。
//
// d <= 0 忽略，而不是"关掉超时"：0 毫秒会让每条命令立刻失败，官方在构造执行器时
// 也用 `assertPositiveFinite('timeoutMs', ...)` 直接拒（`index.ts:108`）——这里按
// 同一语义处理坏值：不生效、静默保留出厂默认，别把工具变成永远超时。
// 超过 maxTimeout（官方 `maxTimeoutMs` 缺省 600_000，`index.ts:129`）的按上限夹住，
// 与模型显式给 timeoutMs 时"超过 600s 夹到 600s"的既有语义一致。
func WithTimeoutBudget(d time.Duration) Option {
	return func(t *Tool) {
		if d <= 0 {
			return
		}
		if d > maxTimeout {
			d = maxTimeout
		}
		t.timeoutBudget = d
	}
}

// WithMaxOutputBytes 注入官方设置 `pwsh-sandbox.maxOutputBytes`
// （`@deepseek-ai/dsh-pwsh-local/src/index.ts:130`）：输出限长与 `truncated` 标记
// 都跟着它走。
//
// n <= 0 忽略：0 字节上限会让输出永远是空的，官方那边
// `assertPositiveFinite('maxOutputBytes', ...)`（`index.ts:110`）同样直接拒。
func WithMaxOutputBytes(n int) Option {
	return func(t *Tool) {
		if n > 0 {
			t.maxOutputBytes = n
		}
	}
}

// WithSandboxACLRoot 显式启用 ACL 受控根（生产从 `DSHD_SANDBOX_ACL_ROOT` 解析，
// 这里只是让测试与宿主自建 scope 能注入）。
//
// parent 为空表示**不启用**——这是默认值，不是「用默认目录启用」。
func WithSandboxACLRoot(parent, home string) Option {
	return func(t *Tool) {
		t.aclRootParent = strings.TrimSpace(parent)
		if h := strings.TrimSpace(home); h != "" {
			t.homeDir = h
		}
	}
}

func newTool(opts ...Option) (*Tool, error) {
	t := &Tool{}
	for _, opt := range opts {
		opt(t)
	}
	if t.gate == nil {
		wd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("取当前目录失败: %w", err)
		}
		gate, err := sandbox.GateFromEnv(wd)
		if err != nil {
			return nil, fmt.Errorf("pwsh 装配沙箱策略失败: %w", err)
		}
		t.gate = gate
	}
	// 受控根是**显式开关**：没配 `DSHD_SANDBOX_ACL_ROOT` 就完全不起 runner，
	// 命令仍以宿主用户权限运行（enforcement 如实报 policy-only）。
	if t.aclRootParent == "" {
		t.aclRootParent = strings.TrimSpace(os.Getenv(sandbox.EnvACLRoot))
	}
	if t.homeDir == "" {
		t.homeDir = strings.TrimSpace(os.Getenv(sandbox.EnvHomeDir))
		if t.homeDir == "" {
			if home, err := os.UserHomeDir(); err == nil {
				t.homeDir = home
			}
		}
	}
	return t, nil
}

func (t *Tool) Name() string { return "pwsh" }
func (t *Tool) Caps() spi.Caps {
	// 会起子进程，命令里想写文件也拦不住，所以两样都如实声明。
	return spi.Caps{Executes: true, Writes: true}
}
func (t *Tool) Schema() json.RawMessage { return schema }
func (t *Tool) Description() string {
	return "执行 PowerShell 命令（优先 pwsh，退回 powershell）。查系统、跑脚本时用。"
}

type args struct {
	// Description 是官方 required 参数：给人看的一句话（UI 展示）。
	// 缺省不拒绝调用（旧调用方没有它），但 Summary/Data 会如实带上。
	Description        string  `json:"description"`
	Command            string  `json:"command"`
	TimeoutMs          float64 `json:"timeoutMs"` // 官方名（毫秒）
	Workdir            string  `json:"workdir"`
	RunInBackground    bool    `json:"run_in_background"`
	TimeoutSeconds     int     `json:"timeout_seconds"` // 旧别名，官方名优先
	SandboxPermissions string  `json:"sandbox_permissions"`
	// Justification 是官方 `justification`：与 sandbox_permissions 成对，一句给用户看的理由。
	Justification *string `json:"justification"`
}

type data struct {
	Shell       string `json:"shell"`
	Description string `json:"description,omitempty"`
	Command     string `json:"command"`
	Workdir     string `json:"workdir,omitempty"`
	ExitCode    int    `json:"exit_code"`
	DurationMS  int64  `json:"duration_ms"`
	Truncated   bool   `json:"truncated,omitempty"`
	Bytes       int    `json:"bytes"`
	// TimeoutMS 是这次调用**实际生效**的超时（毫秒）。
	// 模型给了 timeoutMs / timeout_seconds 里哪个生效，一眼能看出来。
	TimeoutMS int64 `json:"timeout_ms,omitempty"`
	// JobID 非空表示已转后台：用 job_output 取输出、job_kill 终止。
	JobID      string `json:"job_id,omitempty"`
	Background bool   `json:"background,omitempty"`
	// Enforcement 是这次调用**实际**到达的隔离层级，逐次如实上报：
	//   policy-only-no-os-isolation   = 只有策略闸门（今天的事实）
	//   windows-acl-write-only-partial = 走 Windows ACL 受控根（只限写/删，读/网络/进程不受限）
	Enforcement string `json:"enforcement"`
	// SandboxRoot 非空表示这次调用真的在受控根里跑（`<home>/sandbox-roots/<session-id>/`）。
	SandboxRoot string `json:"sandbox_root,omitempty"`
	// RunnerNote 记「启用了受控根但没能用上」的具体原因。这条**必须有**：
	// 退回策略层是允许的，静默退回不允许。
	RunnerNote string `json:"runner_note,omitempty"`
}

// resolveTimeout 决定这次调用的超时。
//
// 官方名 timeoutMs 优先；旧的 timeout_seconds 保留为兼容别名（能力下限：老调用
// 仍然按秒生效）。两者都没给就用**本工具的默认值**——宿主按 `pwsh-sandbox.timeoutMs`
// 注入的 budget，没注入就是官方出厂 120s（DefaultTimeout）；一律夹到上限 600s。
//
// 必须是方法而不是包级函数：默认值现在跟着 `Tool` 走（见 WithTimeoutBudget），
// 但"模型给了就用模型的、超过 600s 夹到 600s"这两条既有语义一个字没变。
func (t *Tool) resolveTimeout(a args) (time.Duration, int64) {
	def := t.defaultTimeout()
	pick := func(d time.Duration) (time.Duration, int64) {
		if d <= 0 {
			d = def
		}
		if d > maxTimeout {
			d = maxTimeout
		}
		return d, d.Milliseconds()
	}
	if a.TimeoutMs > 0 {
		// 官方类型是 number（可以是小数毫秒）：四舍五入到毫秒，不静默截断成 0。
		ms := math.Round(a.TimeoutMs)
		if ms < 1 {
			ms = 1
		}
		return pick(time.Duration(ms) * time.Millisecond)
	}
	if a.TimeoutSeconds > 0 {
		return pick(time.Duration(a.TimeoutSeconds) * time.Second)
	}
	return pick(def)
}

// defaultTimeout 是"模型没给超时"时的默认值：设置注入的优先，否则官方出厂值。
func (t *Tool) defaultTimeout() time.Duration {
	if t.timeoutBudget > 0 {
		return t.timeoutBudget
	}
	return DefaultTimeout
}

// outputLimit 是这次调用的输出上限（字节），limitWriter 与截断文案都用它，
// 保证"实际生效值"只有一处真源。
func (t *Tool) outputLimit() int {
	if t.maxOutputBytes > 0 {
		return t.maxOutputBytes
	}
	return DefaultMaxOutputBytes
}

func (t *Tool) Exec(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	var a args
	if err := json.Unmarshal(raw, &a); err != nil {
		return spi.Result{Hint: `参数不合法：期望 {"command": "Get-ChildItem"}`}, err
	}
	cmdline := strings.TrimSpace(a.Command)
	if cmdline == "" {
		return spi.Result{Hint: "command 不能为空：给一条要执行的 PowerShell 命令"}, fmt.Errorf("command 为空")
	}
	// description 是官方 required 的"给人看的一句话"。缺失不拒绝（旧调用方本来就没有
	// 这个参数），但 Summary/Data 里如实体现：UI 拿它当标题。
	desc := strings.TrimSpace(a.Description)

	dir := strings.TrimSpace(a.Workdir)
	if dir != "" {
		dir = filepath.FromSlash(dir)
		st, err := os.Stat(dir)
		if err != nil {
			return spi.Result{Hint: "workdir 不存在：先用 glob 或 read 确认目录，再传相对路径"},
				fmt.Errorf("workdir 不存在: %s", a.Workdir)
		}
		if !st.IsDir() {
			return spi.Result{Hint: "workdir 是文件不是目录：指定到父目录"}, fmt.Errorf("workdir 不是目录: %s", a.Workdir)
		}
	}

	permission, err := sandbox.ParsePermission(a.SandboxPermissions)
	if err != nil {
		return spi.Result{Hint: `sandbox_permissions 只能是 workspace-write 或 danger-full-access（或省略）`}, err
	}
	// 官方 validateEscalationArgs（`dsh-sandbox/src/escalation.ts:88-98`）：请求与理由
	// 必须成对，理由必须是非空句子。仍然在起进程之前判。
	if err := sandbox.ValidateEscalationArgs(permission, a.Justification); err != nil {
		return spi.Result{Hint: err.Error()}, err
	}
	// 策略判定必须发生在 resolveShell/exec.Command 之前：deny/needs-approval 时零进程。
	if err := t.authorize(ctx, cmdline, dir, permission); err != nil {
		return spi.Result{
			Summary: "沙箱策略拒绝，命令未执行",
			Data:    map[string]any{"sandbox": t.lastDecision},
			Hint:    sandboxHint(err, t.lastDecision),
		}, err
	}

	// 受管 `DSH_*` 环境：在**任何进程动作之前**收集一次。官方语义是"贡献者不合规
	// → 命令不跑"（`dsh-shell-env/src/index.ts:188-195`），所以它和策略判定一样
	// 必须发生在起进程之前。nil 表示未接线，子进程照旧继承宿主环境。
	//
	// run_in_background 那条路也先收集：命令最终由 jobs 接缝（bash 的 Service）执行，
	// 受管环境由那一侧真正注入；这里收集只是让同样的违规**照样当场挡住**。
	envList, err := t.managedEnviron(ctx)
	if err != nil {
		return spi.Result{Hint: managedEnvHint(err)}, err
	}

	shell, isLegacy, err := resolveShell()
	if err != nil {
		return spi.Result{Hint: "找不到 PowerShell：装 PowerShell 7（提供 pwsh）或改用 bash 工具"}, err
	}
	script := cmdline
	if isLegacy {
		script = legacyPrologue + cmdline
	}

	// ACL 受控根：只在**显式启用且探针通过**时使用。runner 为 nil 表示这次调用
	// 走策略层，runnerNote 说明为什么（未启用时为空，启用但用不上时非空）。
	runner, runnerNote := t.runnerFor(ctx, shell)

	// 立刻转后台：官方语义是"注册进 jobs 运行时、马上回 job id、无超时"。
	// 走的是与 bash 工具**同一个** jobs 表（spi.Exec 接缝），见 background.go。
	if a.RunInBackground {
		if runner != nil {
			// 后台面由 jobs 运行时执行，那里打不了受限令牌；静默降级成"没沙箱"不行。
			return spi.Result{
				Summary: "受控根模式不支持后台任务，命令未执行",
				Data: map[string]any{
					"enforcement":  string(sandbox.EnforcementWindowsACLWriteOnly),
					"sandbox_root": runner.Root(),
					"runner_note":  "run_in_background 走 jobs 运行时，无法打受限令牌",
				},
				Hint: "去掉 run_in_background 再试；受控根模式下每条命令都要一个受限子进程",
			}, fmt.Errorf("受控根模式不支持 run_in_background（无法为后台任务打受限令牌）")
		}
		return t.startBackground(ctx, backgroundSpec{
			shell:       shell,
			script:      script,
			workdir:     dir,
			description: desc,
			command:     cmdline,
		})
	}

	timeout, timeoutMS := t.resolveTimeout(a)
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// 注意：这里一个参数传整条脚本。命令里带双引号时，Go 会按 argv 规则转义，
	// PowerShell 多数情况下能正确还原；极复杂的嵌套引号建议写进 .ps1 再执行。
	cmd := execCommandContext(runCtx, shell, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script)
	if dir != "" {
		cmd.Dir = dir
	}
	if envList != nil {
		cmd.Env = envList
	}
	limit := t.outputLimit()
	out := &limitWriter{max: limit}
	cmd.Stdout = out
	cmd.Stderr = out

	enforcement := sandbox.EnforcementPolicyOnlyNoOSIsolation
	sandboxRoot := ""
	var releaseRoot func() error
	if runner != nil {
		// 受控根模式下 cwd 必须是根内：静默改写 cwd 会让命令在错误目录里产生副作用，
		// 所以这里 fail-loud，而不是替用户换目录。
		inside, inErr := workdirInsideRoot(dir, runner.Root())
		if inErr != nil {
			return spi.Result{Hint: "解析工作目录失败：" + inErr.Error()}, inErr
		}
		if !inside {
			return spi.Result{
				Summary: "受控根模式下 workdir 必须在受控根内，命令未执行",
				Data: map[string]any{
					"enforcement":  string(sandbox.EnforcementWindowsACLWriteOnly),
					"sandbox_root": runner.Root(),
				},
				Hint: fmt.Sprintf("受控根模式把可写面收在 %s 里；把 workdir 指到根内，或关掉 %s 再跑",
					runner.Root(), sandbox.EnvACLRoot),
			}, fmt.Errorf("workdir %q 不在受控根 %q 内", dir, runner.Root())
		}
		release, prepErr := runner.Prepare(cmd)
		if prepErr != nil {
			return spi.Result{
				Summary: "受控根准备失败，命令未执行",
				Data: map[string]any{
					"enforcement": string(sandbox.EnforcementPolicyOnlyNoOSIsolation),
					"runner_note": prepErr.Error(),
				},
				Hint: "受控根建不起来就明确失败，不静默降级成无沙箱执行；检查 " + sandbox.EnvACLRoot,
			}, prepErr
		}
		releaseRoot = release
		enforcement = runner.Enforcement()
		sandboxRoot = runner.Root()
	}

	start := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(start)
	if releaseRoot != nil {
		// 收工失败（受控根残留）要说出来：那是硬问题，不是日志噪音。
		if relErr := releaseRoot(); relErr != nil {
			return spi.Result{
				Summary: "命令已执行但受控根没清干净",
				Data: map[string]any{
					"enforcement":  string(enforcement),
					"sandbox_root": sandboxRoot,
					"runner_note":  relErr.Error(),
				},
				Hint: "受控根残留会把目录留在 " + sandboxRoot + "；报告宿主，别忽略",
			}, relErr
		}
	}

	body := out.String()
	trunc := out.Truncated()
	if trunc {
		body += fmt.Sprintf("\n…（输出超过 %d 字节已截断：缩小范围、加 Select-Object -First N 或写到文件再读）", limit)
	}
	if strings.TrimSpace(body) == "" {
		body = "（无输出）"
	}

	head := filepath.Base(shell)
	if desc != "" {
		head = desc + " · " + head
	}
	res := spi.Result{
		Summary: fmt.Sprintf("%s 退出码 %d（%d 毫秒，%d 字节）", head, exitCodeOf(runErr), elapsed.Milliseconds(), out.Len()),
		Text:    body,
		Data: data{
			Shell:       filepath.Base(shell),
			Description: desc,
			Command:     cmdline,
			Workdir:     dir,
			ExitCode:    exitCodeOf(runErr),
			DurationMS:  elapsed.Milliseconds(),
			Truncated:   trunc,
			Bytes:       out.Len(),
			TimeoutMS:   timeoutMS,
			Enforcement: string(enforcement),
			SandboxRoot: sandboxRoot,
			RunnerNote:  runnerNote,
		},
	}

	if runCtx.Err() == context.DeadlineExceeded || runCtx.Err() == context.Canceled {
		res.Hint = fmt.Sprintf("命令超过 %s 未结束已终止：拆成更小的步骤，或把长任务改成 run_in_background: true", timeout)
		return res, fmt.Errorf("命令超时（%s）: %s", timeout, firstLine(body))
	}
	if runErr != nil {
		res.Hint = "命令失败：读上面的报错，先修掉第一条错误再重试；确认是不是路径或权限问题"
		return res, fmt.Errorf("pwsh 退出码 %d: %s", exitCodeOf(runErr), firstLine(body))
	}
	return res, nil
}

// authorize 在启动任何进程之前做策略判定。
func (t *Tool) authorize(ctx context.Context, command, workdir string, permission sandbox.Permission) error {
	cwd := workdir
	if strings.TrimSpace(cwd) == "" {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("取当前目录失败: %w", err)
		}
		cwd = wd
	}
	cwd, err := filepath.Abs(cwd)
	if err != nil {
		return fmt.Errorf("解析工作目录失败: %w", err)
	}
	if t.gate == nil {
		if permission != "" {
			return fmt.Errorf("%w: 本进程没有 %s；带 sandbox_permissions 的调用一律拒绝",
				sandbox.ErrPolicyUnwired, sandbox.EnvMode)
		}
		return nil
	}
	d, err := t.gate.EvaluateCommand(ctx, command, cwd, permission)
	if err != nil {
		return err
	}
	t.lastDecision = &d
	if d.Action == sandbox.ActionAllow {
		return nil
	}
	return d.Error()
}

// sandboxHint 把策略结论翻成模型能照做的下一步。拒绝/待审批时带**官方标记**
// （`dsh-sandbox/src/escalation.ts:108-123`）。
func sandboxHint(err error, decision *sandbox.Decision) string {
	var escalation *sandbox.EscalationError
	switch {
	case errors.As(err, &escalation):
		return escalation.Error()
	case errors.Is(err, sandbox.ErrNeedsApproval):
		return "这次调用请求了更宽的 sandbox 档位，但审批还没完成；让用户批准这次调用后重试，或改成工作区内的操作" +
			sandbox.EscalationHintMarker("command")
	case errors.Is(err, sandbox.ErrDenied):
		if decision != nil && decision.Reason == sandbox.ReasonEscalationDenied {
			return sandbox.EscalationRejectedMessage(decision.RequestedPermission, "command")
		}
		mode := sandbox.ModeReadOnly
		if decision != nil && decision.EffectiveMode != "" {
			mode = decision.EffectiveMode
		}
		return "当前会话 sandbox 档位拒绝这条命令；把命令限制在工作区 cwd 内，或让用户显式切到更宽档位" +
			sandbox.SandboxDenialMarker(mode)
	case errors.Is(err, sandbox.ErrPolicyUnwired):
		return "宿主没有把 sandbox 策略接进本进程；去掉 sandbox_permissions 参数，或让宿主设置 DSHD_SANDBOX_MODE"
	case errors.Is(err, sandbox.ErrMissingContext):
		return "这次工具调用没有会话上下文，策略无法判定；不要重试同一次调用，报告宿主"
	default:
		return "命令没有启动；按错误里的原因调整后再试"
	}
}

// resolveShell 首选 pwsh（7+），退回 Windows PowerShell 5.1，两者都没有就报错。
// isLegacy 标记退回的那个是老壳，需要补 UTF-8 序言。
func resolveShell() (path string, isLegacy bool, err error) {
	if p, e := exec.LookPath("pwsh"); e == nil {
		return p, false, nil
	}
	if p, e := exec.LookPath("powershell"); e == nil {
		return p, true, nil
	}
	return "", false, fmt.Errorf("PATH 里既没有 pwsh 也没有 powershell")
}

func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	if s == "" {
		s = "（无输出）"
	}
	return s
}

// limitWriter 收集输出并在到达上限后静默丢弃其余部分，只记一个标记。
// 并发安全：exec 在 Stdout/Stderr 是同一个可比较的 Writer 时只用一个 goroutine，
// 但为将来拆开流留个锁，代价可以忽略。
type limitWriter struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (w *limitWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	room := w.max - w.buf.Len()
	if room <= 0 {
		w.truncated = true
		return len(p), nil
	}
	if len(p) > room {
		w.buf.Write(p[:room])
		w.truncated = true
		return len(p), nil
	}
	w.buf.Write(p)
	return len(p), nil
}

func (w *limitWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func (w *limitWriter) Len() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Len()
}

func (w *limitWriter) Truncated() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.truncated
}

// Module 返回装配用的模块描述。Options 只服务测试与将来的宿主接线；
// 生产 cmd/dshd 仍按旧签名调用，闸门从进程环境解析。
func Module(opts ...Option) kernel.Module {
	return kernel.Module{
		ID:       "tool-pwsh",
		Backend:  kernel.BackendStatic,
		Requires: []string{"tools"},
		Register: func(s *kernel.Scope) error {
			reg, err := kernel.Inject(s, spi.ToolsKey)
			if err != nil {
				return err
			}
			tool, err := newTool(opts...)
			if err != nil {
				return err
			}
			// 装配期捕获作用域：Exec 时用它 inject spi.Exec，把 run_in_background
			// 登记进宿主的 jobs 运行时。装配顺序不影响（注入发生在调用时）。
			tool.scope = s
			return reg.Register(tool)
		},
	}
}
