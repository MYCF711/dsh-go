package llm

import (
	"errors"
	"fmt"
)

// 0.2.2 对未声明容量的自定义模型使用十进制默认值，不是二进制 MiB/KiB。
const (
	DefaultCustomContextWindow = 1_000_000
	DefaultCustomMaxOutput     = 128_000
)

// ModelCapacity 是模型的上下文窗口与单次最大输出 token 数。
//
// 零值字段表示调用方没有显式声明该字段；它不是 Transport 的容量字段，
// 因而不会把 Transport 的 0=未知改成猜测值。
type ModelCapacity struct {
	ContextWindow int
	MaxOutput     int
}

// builtInModel 是内置容量表的一行：规范模型 id + 容量。
type builtInModel struct {
	ID       string
	Capacity ModelCapacity
}

// builtInModels 只列当前宿主已有的内置已知模型。
//
// 本仓没有上游 pi-ai 模型目录，models.list 也只有 id/owned_by；因此不能把
// 网关返回的任意名字冒充成“已知容量”。其余非空 --model 自由文本在当前
// 声明面里就是未声明自定义模型。这里只保存兼容基线，不做模型路由。
//
// 上下文窗口以**网关实测**为准：magpie 网关 `/v1/models` 对
// `group/deepseek-v4.1-flash-1017` 返回 `context_length: 1000000`。这里原来写的
// 128_000 是早期没有实测时的猜测，会让界面把 1M 窗口的会话画成"已经快撑爆"。
// catalog 里为同一模型声明的窗口仍然优先（见 cmd/dshd/sessionSource.ContextWindow），
// 这张表只是"目录没声明时的兜底"。
//
// 规范 id 带 `group/` 前缀（网关口径）；会话/launcher 里的无前缀写法由
// `ResolveModelID` 归一，不在这张表里另抄一份别名——一份清单一个真相源。
var builtInModels = []builtInModel{
	{
		ID: "group/deepseek-v4.1-flash-1017",
		Capacity: ModelCapacity{
			ContextWindow: 1_000_000,
			MaxOutput:     0, // 保持原行为：常规请求不新增 max_tokens。
		},
	},
}

// builtInModelIDs 是别名归一回看的已知 id 列表，和 builtInModels 同源派生。
func builtInModelIDs() []string {
	ids := make([]string, 0, len(builtInModels))
	for _, entry := range builtInModels {
		ids = append(ids, entry.ID)
	}
	return ids
}

// builtInModelCapacity 按规范 id（或它的唯一无前缀别名）查内置容量。
func builtInModelCapacity(modelID string) (ModelCapacity, bool) {
	canonical, matched := ResolveModelID(modelID, builtInModelIDs())
	if !matched {
		return ModelCapacity{}, false
	}
	for _, entry := range builtInModels {
		if entry.ID == canonical {
			return entry.Capacity, true
		}
	}
	return ModelCapacity{}, false
}

// ErrorCodeInvalidModelContext 是官方错误码：声明了 context 但 contextWindow
// 不是正整数（`tmp/GO3-PARAMS/_official/dsh-llm/lib/types/index.js:629`：
// `context !== undefined && (!Number.isInteger(...) || contextWindow <= 0)` →
// `INVALID_MODEL_CONTEXT`）。
const ErrorCodeInvalidModelContext = "INVALID_MODEL_CONTEXT"

// InvalidModelContextError 是"容量声明写错了"的结构化错误。
//
// 为什么要一个独立类型而不是一句 fmt.Errorf：客户端要能区分"你写的窗口不合法"
// （改参数就能过，不可重试）和"模型调用失败"（重发可能就好）。官方给的是机读码，
// 这里保留同一个码。
type InvalidModelContextError struct {
	ModelID string
	// Field 是出错的声明字段（当前只有 contextWindow；maxOutput 走
	// 官方 INVALID_MODEL_MAX_TOKENS，不在本次范围内）。
	Field string
	Value int
}

func (e *InvalidModelContextError) Error() string {
	model := e.ModelID
	if model == "" {
		model = "（未指定模型）"
	}
	return fmt.Sprintf("模型 %s 声明的 %s=%d 不合法：官方要求正整数（INVALID_MODEL_CONTEXT）；"+
		"宿主不会静默回退默认容量——把窗口改成真实值，或不声明（0 = 按模型容量解析）",
		model, e.Field, e.Value)
}

// Code 返回官方机读码。
func (e *InvalidModelContextError) Code() string { return ErrorCodeInvalidModelContext }

// ModelContextErrorCode 从错误链里取官方错误码；不是这个错误时 ok=false。
func ModelContextErrorCode(err error) (string, bool) {
	var invalid *InvalidModelContextError
	if errors.As(err, &invalid) {
		return invalid.Code(), true
	}
	return "", false
}

// ValidateDeclaredContextWindow 校验一份**显式声明**的上下文窗口。
//
// 只拒负值：宿主声明面是 int（`--context-window` / `ModelCapacity`），零值在本仓
// 的契约里是"未声明"——`--context-window 0` 的帮助文本、`docs/model-capacity.md` §1、
// 以及 `host_run_options.go` 每次给子进程透传的 `--context-window 0` 都依赖这个
// 语义。官方 `undefined` 与"声明 0"的区分在目录面已经成立：目录里写
// `contextWindow: 0` 在读盘时就被拒（model_catalog.go 的正整数校验），不会静默
// 回退。所以这里要补的是**负值**这条真缺口：旧实现把它当成"未声明"，用户看不到
// 自己写错了（docs/parameter-register.md #19）。
func ValidateDeclaredContextWindow(modelID string, contextWindow int) error {
	if contextWindow < 0 {
		return &InvalidModelContextError{ModelID: modelID, Field: "contextWindow", Value: contextWindow}
	}
	return nil
}

// ResolveModelCapacityChecked 是"先校验、再解析"的唯一入口：解析前把非法声明
// 打回去，绝不静默回退到内置/自定义默认值。
func ResolveModelCapacityChecked(modelID string, declared ModelCapacity) (ModelCapacity, error) {
	if err := ValidateDeclaredContextWindow(modelID, declared.ContextWindow); err != nil {
		return ModelCapacity{}, err
	}
	return ResolveModelCapacity(modelID, declared), nil
}

// ResolveModelCapacity 是宿主唯一的模型容量解析点。
//
// 显式声明的正值逐字段优先，包括较小端点的 32K/8K 等覆盖。只有非内置、
// 非空的自由文本模型才补 1M/128K 默认值；内置模型保留原容量，空模型 ID
// 仍表示未知。模型 id 先经 `ResolveModelID` 做唯一别名归一（例如会话里持久化
// 的 `deepseek-v4.1-flash-1017` → 内置表的 `group/deepseek-v4.1-flash-1017`），
// 否则会误判成"未声明自定义模型"并注入 128K 输出上限。
// 函数不缓存任何模型或会话状态，交错解析不会互相串值。
func ResolveModelCapacity(modelID string, declared ModelCapacity) ModelCapacity {
	capacity, known := builtInModelCapacity(modelID)
	if !known {
		if modelID == "" {
			return declared
		}
		capacity = ModelCapacity{
			ContextWindow: DefaultCustomContextWindow,
			MaxOutput:     DefaultCustomMaxOutput,
		}
	}
	if declared.ContextWindow > 0 {
		capacity.ContextWindow = declared.ContextWindow
	}
	if declared.MaxOutput > 0 {
		capacity.MaxOutput = declared.MaxOutput
	}
	return capacity
}
