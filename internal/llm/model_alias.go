package llm

import "strings"

// ResolveModelID 把调用方给的模型 id 归一到 `known` 里的规范 id。
//
// 这是宿主**唯一**的模型别名归一入口：容量解析（ResolveModelCapacity）与目录
// 窗口解析（CatalogContextWindow）都走它，不要在调用点各写一份前缀匹配。
//
// 规则（顺序即优先级）：
//  1. 精确命中优先——`known` 里有同名 id 时原样返回；
//  2. 未命中时，若 `known` 里**恰好一条** id 以 `"/"+model` 结尾，用那条；
//  3. 没有候选、候选不唯一（如 `a/x` 与 `b/x` 并存）或模型为空，都不猜：
//     原样返回输入并报 false，调用方按"这张表里没有这个模型"处理。
//
// 为什么需要：TUI2GO launcher 与历史会话持久化的是**无前缀** id
// （`deepseek-v4.1-flash-1017`），而内置容量表与模型 catalog 的键是网关的
// 全限定 id（`group/deepseek-v4.1-flash-1017`）。不归一会精确 miss，落进
// "未声明自定义模型"分支，让**每一轮**请求都带上 `max_tokens=128000`。
//
// 只做"唯一后缀"这一种归一，不引入模糊匹配/大小写折叠：猜错模型比报未知更
// 危险——容量会串到别的模型上，用户看到的是错误的上下文占用。
func ResolveModelID(model string, known []string) (string, bool) {
	if model == "" {
		return "", false
	}
	for _, id := range known {
		if id == model {
			return id, true
		}
	}
	suffix := "/" + model
	match := ""
	for _, id := range known {
		if !strings.HasSuffix(id, suffix) {
			continue
		}
		if match != "" && match != id {
			// 两个不同前缀的同名模型：没有依据选谁，按未命中处理。
			return model, false
		}
		match = id
	}
	if match == "" {
		return model, false
	}
	return match, true
}
