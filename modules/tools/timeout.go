package tools

import (
	"context"
	"time"

	"dshgo/internal/spi"
)

// per-call deadline（官方 0.2.2 base #75 `timeout-policy`）的宿主实现。
//
// 官方语义（research/23-dsh-plugin-surface.md §3）：工具定义上的 `timeoutMs` 只是
// **声明**，注册表从不强制；真正按调用生效的是 `dsh-tool-call-timeout-policy`
// 包的一层包装，把"这一次调用超时"映射成模型可见的错误。
//
// 宿主对应物有两层，缺一不可：
//   - **统一预算**（`Options.ToolTimeout`，默认 30 分钟）：谁都没说的时候的兜底，
//     比各工具自带的超时都宽，只对真正挂死的调用生效；
//   - **单次预算**（`spi.WithToolDeadline`，这次调用在 ctx 上声明的）：调用方
//     常常知道得更多——"这条命令我只等 90 秒，超了就换后台"。它覆盖统一预算，
//     但**不改**统一预算的默认值（默认 30 分钟一个字节都不变）。
const (
	// timeoutSourcePipeline 表示这次的预算是流水线统一预算（env 或默认 30 分钟）。
	timeoutSourcePipeline = "pipeline"
	// timeoutSourcePerCall 表示这次的预算是调用方在 ctx 上为**这一次**声明的。
	timeoutSourcePerCall = "per_call"
)

// resolveToolBudget 决定"这一次调用"的预算，并说清它是谁定的。
//
// 三值约定与 Options.ToolTimeout 完全一致（两套语义会漂）：
//   - 没绑定 / 0 ⇒ 流水线统一预算（0 是"未定义"，绝不能因为一次笔误把兜底关掉）；
//   - > 0        ⇒ 这一次就用它；
//   - < 0        ⇒ 这一次明确关掉 deadline 层（只看各工具自己的超时）。
//
// 来源要一起返回：日志和结构化错误需要回答"是谁给的这么个数"，否则一次
// 90 秒的超时到底是有人设的还是默认值，事后只能靠猜。
func resolveToolBudget(ctx context.Context, pipeline time.Duration) (time.Duration, string) {
	if budget, ok := spi.ToolDeadlineFrom(ctx); ok && budget != 0 {
		return budget, timeoutSourcePerCall
	}
	return pipeline, timeoutSourcePipeline
}
