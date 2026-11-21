// Package runcode 提供 `run_code` 工具：把"一次调十个工具"写成一段程序交给
// PTC 运行时跑（对应官方 `@deepseek-ai/dsh-tool-executor-ptc`）。
//
// # 职责分工（照官方的三条线，别混）
//
//   - 本包（executor）：拥有**工具绑定**与程序级限额。它把注册表里的工具变成
//     程序里的 `tools.<name>(args)`，并负责每次子调用的准入、并发与计数。
//   - provider（`internal/ptc/nodeprovider`）：拥有**进程**。它起停外部受管
//     node、做帧式控制通道、管超时与终止。
//   - 缝（`internal/ptc`）：词汇与校验，不认识工具也不认识会话。
//
// # 官方语义里本包必须守住的三条
//
//  1. **限额按"每次运行的总次数"算，不是滑动窗口**：每次 `run_code` 得到
//     `floor(resolved timeoutMs / 60000 × maxSubCallsPerMinute)` 次尝试额度；
//     用完的那一次**不派发**，并终止整个程序（程序里 catch 住也救不回来），
//     结果按 `CODE_RUN_FAILED` 上报，正文带上限、被拒的调用序号与"可能死循环"的提示。
//  2. **失败的子调用 reject 一个有类型的错误**（`ToolCallError`，成员名走
//     `toolName`），程序可以 `try/catch` 继续；这把"工具失败"与"程序出错"分开。
//  3. **子调用必须走 ctx 里的受管通道**（`spi.SubDispatch`）。拿不到就拒绝，
//     不许自己另找后门——否则"我自己编排十次写盘、一次都不问"。
//
// # 与官方默认形状的差别（如实写在代码里）
//
// 官方 base 组合里 `tool-executor-ptc` 是 `disabled: true`，启用时它**取代**
// 原生执行器（模型只看到 `run_code`）。本仓这条是**加法**：只要显式开着
// `DSHD_PTC=1` 就与原生工具并排出现，不做执行器替换、不生成 system prompt 的
// SDK 段、不发 `ptc-dispatch` 事件。所以缺口登记里这条是 PARTIAL，不是 IMPLEMENTED。
package runcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"

	"dshgo/internal/kernel"
	"dshgo/internal/ptc"
	"dshgo/internal/ptc/nodeprovider"
	"dshgo/internal/spi"
)

// 官方 `dsh-tool-executor-ptc/src/errors.ts` 的错误身份。
//
// 码值来自官方 `HarnessError` 常量（`CODE_RUN_FAILED`），类名来自同名导出类。
// 这两个值会落进 v4 的 `tool/result.error.{name,code}`，客户端按 code 分支。
const (
	errorNameCodeRunFailed = "CodeRunFailedError"
	codeRunFailed          = "CODE_RUN_FAILED"
)

// OptInEnv 是本仓启用这条工具面的开关。
//
// 为什么默认关：官方默认也关着（`dsh-base/cordis.patch.yml:523` 的
// `disabled: true`），而且这个 provider **没有文件 confinement**——
// 程序里的 `require('node:fs')` 按进程权限生效，不是按会话档位。默认关、
// 显式开，是让"我知道这次会跑任意 JS"这件事由人决定，而不是装配时替他决定。
const OptInEnv = "DSHD_PTC"

// Config 是执行器配置，字段名与默认值逐条照官方 `src/config.ts`。
type Config struct {
	// MaxSubCallsPerMinute 是"每运行一分钟后允许的工具调用尝试数"，默认 1024。
	MaxSubCallsPerMinute int
	// MaxParallelSubCalls 是并发安全子调用的上限，默认 10；1 表示串行。
	MaxParallelSubCalls int
	// NodePath 透传给 provider；空 = 从 PATH 找。
	NodePath string
}

// DefaultConfig 返回官方默认值。
func DefaultConfig() Config {
	return Config{MaxSubCallsPerMinute: 1024, MaxParallelSubCalls: 10}
}

func (c Config) normalized() (Config, error) {
	if c.MaxSubCallsPerMinute <= 0 {
		c.MaxSubCallsPerMinute = DefaultConfig().MaxSubCallsPerMinute
	}
	if c.MaxParallelSubCalls <= 0 {
		c.MaxParallelSubCalls = DefaultConfig().MaxParallelSubCalls
	}
	return c, nil
}

// Tool 是 `run_code` 工具本身。
type Tool struct {
	cfg      Config
	registry spi.ToolRegistry

	mu       sync.Mutex
	provider *nodeprovider.Provider
	provErr  error
}

// New 建一个执行器。
func New(cfg Config, registry spi.ToolRegistry) (*Tool, error) {
	cfg, err := cfg.normalized()
	if err != nil {
		return nil, err
	}
	if registry == nil {
		return nil, errors.New("runcode: 没有工具目录（ToolsKey 未注入）")
	}
	return &Tool{cfg: cfg, registry: registry}, nil
}

// Name 是模型看到的工具名（官方 `RUN_CODE_NAME`）。
func (t *Tool) Name() string { return "run_code" }

// Description 逐字对齐官方 `TYPESCRIPT_FUNCTION_FLAVOR.description`
// （`src/ptc.ts:50-56` 的模板与 `:76-80` 的 function-body 变异）。
func (t *Tool) Description() string {
	return "Execute a TypeScript program against the available tools. Takes two required arguments: " +
		"`code`, the BODY of an async function (erasable syntax only; top-level `await` and `return` work), " +
		"and `description`, a short summary of what the program does. " +
		"Call tools as `await tools.name(args)`, where `name` is any tool available to you. " +
		"Only what you print or return is program output — curate it. " +
		"Image-bearing subtool results are attached after the run."
}

var schema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "code": {
      "type": "string",
      "description": "The program: the body of an async TypeScript function."
    },
    "description": {
      "type": "string",
      "description": "A short summary of what the program does."
    },
    "timeoutMs": {
      "type": "number",
      "description": "Positive elapsed-time budget in milliseconds, capped by the deployment maximum."
    }
  },
  "required": ["code", "description"],
  "additionalProperties": false
}`)

// Schema 是模型看到的参数契约。
func (t *Tool) Schema() json.RawMessage { return schema }

// Caps 声明影响面。
//
// 这里只声明 Executes：这条工具**真正新增**的能力是"起一个外部 node 进程跑程序"。
// 它可能造成的文件/网络影响全部走子调用通道（那些工具各自带着自己的 Caps 与审批），
// 所以这里不重复声明 Writes/Network——重复声明只会让每次 run_code 多问一遍人，
// 而真正该问的那一次在子调用上照问。
func (t *Tool) Caps() spi.Caps { return spi.Caps{Executes: true} }

// ConcurrencySafe：官方 "Root run_code calls are exclusive and execute serially"。
func (t *Tool) ConcurrencySafe(args json.RawMessage) bool { return false }

// args 是入参。
type args struct {
	Code        string   `json:"code"`
	Description string   `json:"description"`
	TimeoutMs   *float64 `json:"timeoutMs"`
}

func parseArgs(raw json.RawMessage) (args, error) {
	var a args
	if len(strings.TrimSpace(string(raw))) == 0 {
		return a, errors.New("没有参数：至少要给 code 与 description")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&a); err != nil {
		return a, fmt.Errorf("参数不合法：%w", err)
	}
	if strings.TrimSpace(a.Code) == "" {
		return a, errors.New("code 不能为空：这里要的是 async 函数体，不是一句描述")
	}
	if strings.TrimSpace(a.Description) == "" {
		return a, errors.New("description 不能为空：官方要求它是一句人话摘要，会进会话记录")
	}
	if a.TimeoutMs != nil {
		if math.IsNaN(*a.TimeoutMs) || math.IsInf(*a.TimeoutMs, 0) || *a.TimeoutMs <= 0 {
			return a, fmt.Errorf("timeoutMs 必须是正数有限毫秒（收到 %v）", *a.TimeoutMs)
		}
	}
	return a, nil
}

// ensureProvider 懒建 provider：起进程要 node，装配时不该因为"这台机器没装 node"
// 就让整个宿主起不来（工具存在，用到时如实报 node 不可用）。
func (t *Tool) ensureProvider() (*nodeprovider.Provider, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.provider != nil || t.provErr != nil {
		return t.provider, t.provErr
	}
	cfg := nodeprovider.DefaultConfig()
	cfg.NodePath = t.cfg.NodePath
	if wd, err := os.Getwd(); err == nil {
		cfg.WorkspaceRoot = wd
	}
	provider, err := nodeprovider.New(cfg)
	if err != nil {
		t.provErr = err
		return nil, err
	}
	t.provider = provider
	return provider, nil
}

// Exec 跑一段程序。
func (t *Tool) Exec(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	a, err := parseArgs(raw)
	if err != nil {
		return spi.Result{Hint: "run_code 需要 code（async 函数体）与 description（一句话摘要）；" +
			"示例：{code: 'return await tools.read({path: \"a.txt\"})', description: '读 a.txt'}"}, err
	}

	dispatch := spi.SubDispatchFrom(ctx)
	if dispatch.Runner == nil {
		return spi.Result{Hint: "这个环境没有编排通道（宿主没注入 SubDispatch）；改用原生工具一次一次调"},
			errors.New("run_code 需要宿主注入的子调用通道")
	}

	provider, err := t.ensureProvider()
	if err != nil {
		return spi.Result{Hint: "PTC provider 建不起来：" + err.Error() + "；原生工具不受影响，可以照常一次一次调"},
			err
	}

	choice := ptc.TimeoutDefault()
	if a.TimeoutMs != nil {
		choice = ptc.TimeoutMs(int64(*a.TimeoutMs))
	}
	timeoutSpec, _ := provider.Timeout()
	resolved, resolveErr := ptc.ResolveTimeout(choice, true, timeoutSpec)
	if resolveErr != nil {
		return spi.Result{Hint: "timeoutMs 不合法：给正数毫秒，或干脆不给（用默认值）"}, resolveErr
	}
	allowance := allowanceFor(resolved, t.cfg.MaxSubCallsPerMinute)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	tools := t.bindableTools()
	sched := newScheduler(dispatch.Runner, t.registry, tools, allowance, t.cfg.MaxSubCallsPerMinute, t.cfg.MaxParallelSubCalls, cancel)

	cwd, _ := os.Getwd()
	spec, err := provider.Resolve(ptc.RunRequest{
		Program:  a.Code,
		Bindings: []ptc.BindingNamespace{sched.namespace(tools)},
		Cwd:      cwd,
		Timeout:  choice,
	})
	if err != nil {
		return spi.Result{Hint: "这次运行没能成立（参数/上下文问题），改完再发起；本轮没有产生任何副作用"}, err
	}

	result, runErr := provider.Run(runCtx, spec)
	if runErr != nil {
		return spi.Result{Hint: "PTC 运行没起来（宿主侧问题，不是程序写法问题）；可以先确认这台机器上 `node --version` 可用"}, runErr
	}
	return t.render(ctx, result, sched, a)
}

// bindableTools 是程序里 `tools.*` 能看到的工具：注册表里除自己之外的全部。
//
// 排除自己是为了不让程序递归调 `run_code`（官方也只有 `run_code` 本身直接可调，
// 嵌套程序会另起一层执行器语义，本仓没实现）。
func (t *Tool) bindableTools() []spi.Tool {
	all := t.registry.Tools()
	out := make([]spi.Tool, 0, len(all))
	for _, tool := range all {
		if tool == nil || tool.Name() == t.Name() {
			continue
		}
		out = append(out, tool)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// allowanceFor 复刻官方算式：`floor(resolved timeoutMs / 60000 × perMinute)`。
// 没有期限（nil）时没有有限额度（官方："no deadline means no finite call allowance"）。
func allowanceFor(resolvedMs *int64, perMinute int) int64 {
	if resolvedMs == nil {
		return math.MaxInt64
	}
	return int64(math.Floor(float64(*resolvedMs) / 60000.0 * float64(perMinute)))
}

// render 把运行结果翻成模型看到的东西。
func (t *Tool) render(ctx context.Context, result ptc.RunResult, sched *scheduler, a args) (spi.Result, error) {
	if !result.Failed() {
		return successResult(result)
	}
	failure := result.Error

	// 1) 额度耗尽：官方把它报成 CODE_RUN_FAILED，正文带上限与被拒序号。
	if sched.exhausted() {
		limit := sched.limitError()
		text := limit.Error()
		if len(result.Logs) > 0 {
			text += "\n" + strings.Join(result.Logs, "\n")
		}
		return spi.Result{
				Text: text,
				Data: map[string]any{"logs": result.Logs},
				Hint: "程序里的循环没有终止，或者活儿太大；把循环收敛，或拆成几次更小的 run_code。" +
					"已完成的工具副作用不会回滚，重试前先检查它们。",
			}.WithErrorInfo(errorNameCodeRunFailed, codeRunFailed),
			fmt.Errorf("%s", text)
	}

	// 2) 调用方取消：走共享的 ABORTED 身份（官方："Caller cancellation uses the
	//    shared AbortError / ABORTED tool outcome"）。
	if failure.Kind == ptc.FailureAbort && ctx.Err() != nil {
		return spi.Result{
				Text: "Error: tool call aborted",
				Data: map[string]any{"logs": result.Logs},
				Hint: "这轮被取消了；需要的话重新发起。保留状态（如果有）已被丢弃，已完成的工具副作用仍在。",
			}.WithErrorInfo(spi.ErrorNameAbort, spi.CodeAborted),
			errors.New("tool call aborted")
	}

	// 3) 其它运行失败：CODE_RUN_FAILED，正文带失败种类 + 已捕获输出。
	text := string(failure.Kind) + ": " + failure.Message
	if len(result.Logs) > 0 {
		text += "\n" + strings.Join(result.Logs, "\n")
	}
	return spi.Result{
			Text: text,
			Data: map[string]any{"logs": result.Logs},
			Hint: failureHint(failure.Kind),
		}.WithErrorInfo(errorNameCodeRunFailed, codeRunFailed),
		fmt.Errorf("%s", text)
}

// failureHint 给每一类失败一句"下一步怎么做"（R3：失败必须带 Hint）。
func failureHint(kind ptc.FailureKind) string {
	switch kind {
	case ptc.FailureException:
		return "程序抛了异常：看正文里的行内信息改代码；工具失败会 reject ToolCallError，可以 try/catch 后继续"
	case ptc.FailureTimeout:
		return "执行超预算：把活儿拆小，或给更大的 timeoutMs（有部署上限）"
	case ptc.FailureInvalidOutput:
		return "完成值不是无损 JSON：别 return 函数/undefined/NaN/类实例，改成普通对象或数组"
	case ptc.FailureOutputLimit:
		return "输出太多：少打印，只 return 需要的那几个字段"
	case ptc.FailureWorkerExit:
		return "外部 node 进程没结算就退出了：可能是 OOM 或进程被杀；缩小数据量再试"
	case ptc.FailureProtocol:
		return "控制通道上出现了非法帧：这通常是宿主侧问题，报给维护者，不要靠改程序绕"
	case ptc.FailureSandboxUnavailable:
		return "要求的文件隔离建不起来：去掉沙盒档位请求，或改用原生工具"
	}
	return "看正文里的失败原因再决定下一步"
}

// successResult 组装成功结果。
//
// 模型可见结构照官方 `src/ptc.ts:320-331` 的输出 schema：`logs` 必填、
// `result` 是完成值、`sandbox` 只在 provider 有 confinement 时才有（本 provider 没有）。
func successResult(result ptc.RunResult) (spi.Result, error) {
	data := map[string]any{"logs": result.Logs}
	parts := make([]string, 0, 2)
	if len(result.Logs) > 0 {
		parts = append(parts, strings.Join(result.Logs, "\n"))
	}
	if result.HasValue {
		data["result"] = result.Value
		if encoded, err := json.MarshalIndent(result.Value, "", "  "); err == nil {
			parts = append(parts, string(encoded))
		}
	}
	text := strings.Join(parts, "\n")
	if text == "" {
		text = "(run_code completed with no output)"
	}
	return spi.Result{Summary: "run_code 完成", Text: text, Data: data}, nil
}

// Module 返回装配用的模块描述。
func Module() kernel.Module {
	return ModuleWithConfig(DefaultConfig())
}

// ModuleWithConfig 允许指定配置（测试与将来接设置面用）。
func ModuleWithConfig(cfg Config) kernel.Module {
	return kernel.Module{
		ID:       "tool-runcode",
		Backend:  kernel.BackendStatic,
		Requires: []string{"tools"},
		Register: func(s *kernel.Scope) error {
			registry, err := kernel.Inject(s, spi.ToolsKey)
			if err != nil {
				return err
			}
			tool, err := New(cfg, registry)
			if err != nil {
				return err
			}
			return registry.Register(tool)
		},
	}
}
