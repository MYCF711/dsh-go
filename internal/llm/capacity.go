package llm

import (
	"errors"
	"fmt"
)

// 0.2.2 对未声明容量的自定义模型使用十进制默认值，不是二进制 MiB/KiB。
//
// 官方来源（未声明容量的路由，即 pi-ai 的 `request.defaultContextWindow` /
// `request.defaultMaxTokens`）：
//   - `tmp/GO3-PARAMS/_official/dsh-llm-pi-ai/lib/index.js:960`
//     `const DEFAULT_CONTEXT_WINDOW = 1e6;`
//   - `tmp/GO3-PARAMS/_official/dsh-llm-pi-ai/lib/index.js:962`
//     `const DEFAULT_MAX_TOKENS = 128e3;`
//   - `:1158` 把它们摊成 `defaultContextWindow: source.defaultContextWindow ?? 1e6`
//     与 `defaultMaxTokens: source.defaultMaxTokens ?? 128e3`，因此每个 pi-ai 路由
//     **永远**有默认输出上限，不是"默认不发 max_tokens"。
//
// 注意 `1e6` 是十进制 1000000（不是 1048576）。官方 llm-deepseek 的**适配器默认**
// 同为 1e6（`dsh-llm-deepseek/lib/index.js:38`），但它的**内置模型目录**声明
// 1048576（同文件 :17 的 `OFFICIAL_CONTEXT_WINDOW`），两者口径不同，别混。
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
// # 这些数字的官方依据
//
// 官方桌面 profile 对**同一条 magpie 网关**给这两个模型声明了容量
// （`D:/DeepSeek Harness/home/profiles/desktop/cordis.patch.yml.bak-1794253367009`
// 的 `llm-pi-ai.providers.magpie.models[]`）：
//
//	:438-444  group/deepseek-v4.1-flash-1017
//	          contextWindow: 1000000  maxTokens: 128000
//	:446-455  group/kimi-k3.1-pro
//	          contextWindow: 1000000  maxTokens: 128000
//
// 逐字段来源：
//   - `contextWindow` 与网关 `/v1/models` 实测的 `context_length` 一致（1e6），
//     也等于 pi-ai 的默认窗口（`_official/dsh-llm-pi-ai/lib/index.js:960`）。
//   - `maxTokens` 128000 = pi-ai 的默认输出上限（同文件 :962/:1158），
//     **部署里显式写的就是它**（上面 profile :441/:449）。
//
// # 为什么 MaxOutput 不能是 0
//
// 官方 0.2.2 的请求面**永远**带输出上限：适配器把 `defaultMaxTokens` 声明给
// 核心（`_official/dsh-llm-deepseek/lib/index.js:536`、
// `dsh-llm-pi-ai/lib/index.js:1158`），核心在调用配置省略 maxTokens 时把它
// **物化**进有效配置（`_official/dsh-llm/lib/index.js:2188-2193`：
// `config.maxTokens === undefined && info.defaultMaxTokens !== undefined ?
// { ...config, maxTokens: info.defaultMaxTokens } : config`），适配器再原样发出
// （`dsh-llm-deepseek/lib/index.js:1732`
// `max_tokens: options.maxTokens ?? model?.maxTokens ?? connection.maxTokens`；
// `dsh-llm-pi-ai/lib/index.js:2028` 把 `options.maxTokens` 交给 pi-ai 的
// `streamSimple`）。所以"内置模型不发 max_tokens"与官方相反，是漏接，不是保守。
//
// catalog 里为同一模型声明的窗口仍然优先于这张表（见
// cmd/dshd/sessionSource.ContextWindow）；这张表是"目录/网关都没声明时的兜底"。
//
// 规范 id 带 `group/` 前缀（网关口径）；会话/launcher 里的无前缀写法由
// `ResolveModelID` 归一，不在这张表里另抄一份别名——一份清单一个真相源。
var builtInModels = []builtInModel{
	{
		ID: "group/deepseek-v4.1-flash-1017",
		Capacity: ModelCapacity{
			ContextWindow: 1_000_000,
			MaxOutput:     DefaultCustomMaxOutput,
		},
	},
	{
		ID: "group/kimi-k3.1-pro",
		Capacity: ModelCapacity{
			ContextWindow: 1_000_000,
			MaxOutput:     DefaultCustomMaxOutput,
		},
	},
}

// KnownModelCapacity 报告 modelID 是否是内置容量表里的已知模型，并返回其容量。
//
// 这是官方"适配器目录条目"的宿主等价物：pi-ai 解析链是
// `entry.contextWindow ?? base.contextWindow ?? request.defaultContextWindow`
// （`_official/dsh-llm-pi-ai/lib/index.js:695-696`），其中 `base` 就是内置目录。
// 调用方据此把"目录声明"排在"适配器默认窗口"（DSHD_CONTEXT_WINDOW）之前。
func KnownModelCapacity(modelID string) (ModelCapacity, bool) {
	return builtInModelCapacity(modelID)
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
