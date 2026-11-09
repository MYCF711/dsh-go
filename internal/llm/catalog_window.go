package llm

import (
	"encoding/json"
	"math"
)

// catalogModelEntry 是模型目录里一行条目中容量解析关心的字段。
type catalogModelEntry struct {
	ID            string `json:"id"`
	ContextWindow int64  `json:"contextWindow"`
	ContextLength int64  `json:"context_length"`
}

// CatalogContextWindow 从一份模型目录视图里取 `modelID` 声明的上下文窗口。
//
// # 两种形状都要认
//
// 目录里的模型条目有两类来源，字段拼法不同：
//
//   - 界面/官方 schema 写入的条目用 `contextWindow`（camelCase，见 model_catalog.go
//     的字段校验与 isNullableCatalogField）；
//   - 从网关 `/v1/models` 发现来的条目用 `context_length`（snake_case，见
//     discover.go 的 discoverEntry）。
//
// 两者都是**真实声明**，谁先出现用谁；同一个条目里两个都给时用 `contextWindow`
// （那是宿主/界面明确写入的口径）。
//
// # 别名归一
//
// 精确命中优先。目录里没有同名条目时，用 `ResolveModelID` 做**唯一**后缀归一：
// catalog 的键是网关全限定 id（`group/deepseek-v4.1-flash-1017`），而真实会话里
// 持久化的模型 id 常常没有前缀，精确比较会 miss。只有恰好一条候选时才用；两个
// 不同前缀的候选并存时不猜，按"目录没声明"处理。
//
// 返回 0 表示目录没有为这个模型声明窗口——**不是**"窗口是 0"，调用方要继续往下解析。
func CatalogContextWindow(view ModelCatalogView, modelID string) int {
	if modelID == "" {
		return 0
	}
	// 精确命中：目录里显式写的那一条就是真值，哪怕它声明的是 0（=没声明窗口，
	// 也不该拿别的同名前缀条目来顶替）。精确命中后再找别名，避免把用户明确
	// 录入的条目旁路掉。
	if window, found := catalogWindowFor(view, modelID); found {
		return window
	}
	canonical, matched := ResolveModelID(modelID, catalogModelIDs(view))
	if !matched || canonical == modelID {
		return 0
	}
	window, _ := catalogWindowFor(view, canonical)
	return window
}

// catalogWindowFor 在目录里查一条 id 的声明窗口。
//
// 第二个返回值是"目录里有这个 id 的条目"（不管它声明的窗口是不是 0），
// 调用方据此区分"没有这条"与"有这条但没声明窗口"。
func catalogWindowFor(view ModelCatalogView, modelID string) (int, bool) {
	for _, raw := range view.Models {
		var entry catalogModelEntry
		if err := json.Unmarshal(raw, &entry); err != nil || entry.ID != modelID {
			continue
		}
		if value := positiveIntTokenCount(entry.ContextWindow); value > 0 {
			return value, true
		}
		return positiveIntTokenCount(entry.ContextLength), true
	}
	return 0, false
}

// catalogModelIDs 取目录里的全部条目 id，供别名归一回看用。
func catalogModelIDs(view ModelCatalogView) []string {
	ids := make([]string, 0, len(view.Models))
	for _, raw := range view.Models {
		var entry catalogModelEntry
		if err := json.Unmarshal(raw, &entry); err != nil || entry.ID == "" {
			continue
		}
		ids = append(ids, entry.ID)
	}
	return ids
}

// positiveIntTokenCount 把目录里的 int64 token 数收成 int，并挡住负值/溢出。
//
// 目录校验只保证"正整数"，机制上仍可能被手写文件塞进一个超过 int 上限的数
// （32 位平台上 int 装不下 1M 以上的窗口）；那种值不可能是真实窗口，按
// "没声明"处理比回一个负数或截断值更诚实。
func positiveIntTokenCount(value int64) int {
	if value <= 0 || value > int64(math.MaxInt) {
		return 0
	}
	return int(value)
}
