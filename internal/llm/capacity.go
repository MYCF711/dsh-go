package llm

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
