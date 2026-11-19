package ptc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"

	"dshgo/internal/sandbox"
)

// ErrMisuse 是"调用方用错了这条缝"的错误身份。
//
// 官方的分界：程序自身的、预算的、取消的、底座的失败都进 `RunResult.Error`
// **字段**；只有 Service Definition 的**误用**才 reject。Go 里 reject 就是
// 返回 error，所以这里给它一个可识别的身份，让上层能分开处理。
var ErrMisuse = errors.New("ptc: 调用方误用")

// Misuse 造一个带上下文的误用错误。
func Misuse(format string, args ...any) error {
	return fmt.Errorf("%w："+format, append([]any{ErrMisuse}, args...)...)
}

// IsMisuse 报告 err 是不是误用。
func IsMisuse(err error) bool { return errors.Is(err, ErrMisuse) }

// TimeoutSpec 是 provider 对外声明的数字预算默认值与上限。
//
// 官方 `src/index.ts:147` 的 `timeout: { defaultMs, maxMs } | undefined`。
type TimeoutSpec struct {
	DefaultMs int64
	MaxMs     int64
}

// Runtime 是 PTC 执行缝：一个跑"模型写的程序 + 宿主给的异步绑定"的服务。
//
// 官方的分工必须照抄，否则容易把工具/会话的知识漏进 runtime：
// runtime 不认识工具、不认识会话；它只收 program、named async bindings 和已解析的
// 执行选项，回**捕获到的输出、结果、以及适用的沙盒事实**。
//
// 实现者义务（官方 `src/index.ts:99-103`）：
//   - 把 bindings 桥过序列化边界（无损 JSON）；
//   - 为每个声明了 errorClass 的命名空间物化一个真的拒绝构造器；
//   - 把程序当敌对方（hostile peer）对待；
//   - 隔离不同的 stateOwner；
//   - disposal 时终止并等待在途运行。
type Runtime interface {
	// Language 是 run 期望 program 用的源语言（小写标识符，信息性、不作门禁）。
	Language() string
	// Isolation 是执行底座（小写标识符：`process` / `worker-thread` / `container`）。
	// 它是**诊断描述**，不是安全承诺。
	Isolation() string
	// ProgramFormat 是源码求值形态，与是否保留状态无关。
	ProgramFormat() ProgramFormat
	// RetainsState 报告这个 provider 是否按 stateOwner 保留程序内存。
	RetainsState() bool
	// Release 释放某个 owner 的保留环境并等待其执行资源。
	// 不保留状态的 provider 没有可释放的东西，直接返回 nil。
	Release(ctx context.Context, owner any) error
	// ExecutionInstructions 是 provider 自有的用法说明（消费者原样呈现给模型）。
	ExecutionInstructions() string
	// SandboxMode 是部署的文件策略档位；没有 confinement 能力时 ok=false。
	SandboxMode() (mode sandbox.Mode, ok bool)
	// Timeout 是配置的数字预算默认值与上限；ok=false 表示不支持逐次数字覆盖。
	Timeout() (spec TimeoutSpec, ok bool)
	// Resolve 校验支持的选项并补上 provider 缺省值，返回 run 可直接吃的输入。
	// 显式但非法/不受支持的选项在这里报错（误用），不留到 run。
	Resolve(req RunRequest) (RunSpec, error)
	// Run 执行已解析的输入。程序层面的失败写进 RunResult.Error，不返回 error。
	Run(ctx context.Context, spec RunSpec) (RunResult, error)
}

// ResolveTimeout 把三态选择解析成 run 看到的 `timeoutMs: number | null`。
//
// 规则（逐条对应官方 README 的 "A provider rejects choices it does not support"）：
//
//   - 没给 → 用 provider 默认；provider 没有默认（ok=false）时结果是"无期限"；
//   - 显式 null → 无期限（任何 provider 都能满足"不定闹钟"）；
//   - 正数 → 必须 > 0 且有限；provider 不支持数字覆盖时**拒绝**（不许默默忽略），
//     支持时按 MaxMs 封顶。
//
// 返回 nil 表示这次运行没有 elapsed deadline。
func ResolveTimeout(choice TimeoutChoice, supported bool, spec TimeoutSpec) (*int64, error) {
	if !choice.Set {
		if !supported {
			return nil, nil
		}
		if spec.DefaultMs <= 0 {
			return nil, nil
		}
		ms := spec.DefaultMs
		return &ms, nil
	}
	if choice.NoDeadline {
		return nil, nil
	}
	if !supported {
		return nil, Misuse("这个 provider 不支持逐次 timeoutMs 覆盖，但调用方给了 %d ms；去掉该字段或改用 TimeoutNone()", choice.Ms)
	}
	if choice.Ms <= 0 {
		return nil, Misuse("timeoutMs 必须是正数毫秒（收到 %d）；要「没有期限」请用 TimeoutNone()", choice.Ms)
	}
	ms := choice.Ms
	if spec.MaxMs > 0 && ms > spec.MaxMs {
		ms = spec.MaxMs
	}
	return &ms, nil
}

// ResolveCwd 解析工作目录：显式值优先，否则用 provider 默认；必须能被解析成
// **绝对路径**，并且真实存在性由 provider 在执行前判断（这里只看形状）。
func ResolveCwd(requested, fallback string) (string, error) {
	candidate := requested
	if candidate == "" {
		candidate = fallback
	}
	if candidate == "" {
		return "", Misuse("没有工作目录：请求里没给 cwd，provider 也没有默认值")
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", Misuse("cwd %q 无法解析成绝对路径：%v", candidate, err)
	}
	return abs, nil
}

// ResolveSandbox 解析这次运行的文件档位。
//
// 官方：没有 confinement 的 provider **拒绝显式策略**。这里把它写成一条不变量：
// 无 confinement 却收到非 nil 档位 = 误用；把档位悄悄吞掉才是真正危险的那种"顺从"。
func ResolveSandbox(requested *sandbox.Mode, providerSupports bool) (*sandbox.Mode, error) {
	if requested == nil {
		return nil, nil
	}
	if !providerSupports {
		return nil, Misuse("这个 provider 没有文件 confinement，拒绝显式沙盒档位 %q（不许默默忽略）", string(*requested))
	}
	mode := *requested
	return &mode, nil
}

// ValidateProgramFormat 校验一个 provider 声明的形态是否在官方三值之内。
func ValidateProgramFormat(format ProgramFormat) error {
	switch format {
	case FormatFunctionBody, FormatREPL, FormatScript:
		return nil
	}
	return fmt.Errorf("未知的 programFormat %q（官方只有 function-body / repl / script）", string(format))
}

// LosslessJSONProblem 检查 v 能否无损穿过 JSON 边界；能就返回空串，
// 不能就返回一句**能指导模型改代码**的原因。
//
// 这条检查在两个地方用：provider 返回完成值时（不能 → `invalid-output`），
// 以及绑定函数的入参/返回值过桥时。判据照官方 README 逐条：函数、symbol、
// undefined、循环引用、稀疏数组之外的非 JSON 类型、非有限数、嵌套 undefined。
func LosslessJSONProblem(v any) string {
	return losslessProblem(v, map[uintptr]bool{}, 0)
}

func losslessProblem(v any, seen map[uintptr]bool, depth int) string {
	if depth > 256 {
		return "值嵌套超过 256 层，无法无损序列化"
	}
	switch typed := v.(type) {
	case nil:
		return ""
	case bool, string:
		return ""
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return fmt.Sprintf("数值 %v 不是有限数（NaN / Infinity 不是 JSON）", typed)
		}
		return ""
	case float32:
		if math.IsNaN(float64(typed)) || math.IsInf(float64(typed), 0) {
			return fmt.Sprintf("数值 %v 不是有限数（NaN / Infinity 不是 JSON）", typed)
		}
		return ""
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return ""
	case json.Number:
		// 宿主侧经常用 json.Number 保住整数精度（解开 `1` 不想变成 1.0）。
		// 它当然能无损穿过 JSON——只要它真的解析成有限数。
		parsed, err := typed.Float64()
		if err != nil {
			return fmt.Sprintf("json.Number %q 不是合法数值：%v", typed.String(), err)
		}
		if math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return fmt.Sprintf("json.Number %q 不是有限数", typed.String())
		}
		return ""
	case []any:
		for i, item := range typed {
			if problem := losslessProblem(item, seen, depth+1); problem != "" {
				return fmt.Sprintf("数组第 %d 项：%s", i, problem)
			}
		}
		return ""
	case map[string]any:
		for key, item := range typed {
			if problem := losslessProblem(item, seen, depth+1); problem != "" {
				return fmt.Sprintf("对象键 %q：%s", key, problem)
			}
		}
		return ""
	}
	return fmt.Sprintf("类型 %T 不在无损 JSON 值域内（函数 / symbol / undefined / BigInt / 自定义类都不行）", v)
}
