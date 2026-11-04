// Package schema 是「压缩工具参数 schema」的唯一规则来源。
//
// 它只回答一个问题：一个 `description` 成员，是**注解**（给模型看的说明，
// 压表时删）还是**参数名**（properties 映射里的键，一个字节都不许动）？
//
// 为什么要有这个包：2026-11-04 一天里，同一个错误在两份独立实现里各犯一次——
// internal/agent.CompactSchema 与 cmd/dshd 的 stripDescriptions 都是「见到
// description 键就删」。本仓有 8 个工具真有一个叫 description 的参数，其中
// 5 个还在 required 里；删完之后模型看到「必填、但没有类型」，全链路不报错。
// 两份实现必然漂移，所以判断收在这里，两处都调 IsAnnotationKey。
//
// 为什么不再往上合并成「一个压表函数」：CompactSchema 与 cmd/dshd.tightenSchema
// 是两种**变换**，不是同一个东西的两种写法。差异是实测的（探针见
// docs/silent-success-audit.md §4 D3）：
//
//   - tightenSchema 还删冗余骨架（顶层 type:object / 空 properties / 空 required /
//     additionalProperties:true），CompactSchema 不删；
//   - tightenSchema 用 json.RawMessage 承载子节点，type/enum/数字逐字节不变
//     （9007199254740993 不会变成 ...992，1e+04 不会重排）；CompactSchema 走
//     any，数字会过一遍 float64；
//   - tightenSchema 只递归 properties/items，$defs、anyOf、default 里的东西
//     原样不动；CompactSchema 递归一切对象（因此也会删掉 default 数据里的
//     同名键——这是它既有的行为，不在这条收敛里改）。
//
// 所以两个变换各自保留，只有「谁是注解」这个判断共用。
package schema

import "encoding/json"

// description 是 JSON Schema 的说明注解成员名。
//
// 不导出：按名字比较只允许出现在 IsAnnotationKey 里；外部拿不到这个字面量，
// 也就不容易再写出一份「见键就删」。
const description = "description"

// IsPropertyMapKey 判断某个 schema 关键字下面挂的是不是「名字 → schema」映射。
//
// 只有这四个：properties / patternProperties / $defs / definitions。
// 其余（items / anyOf / additionalProperties…）挂的是 schema 本身，
// 哪怕参数正好叫 items，也不能把它的名字当关键字处理。
func IsPropertyMapKey(k string) bool {
	switch k {
	case "properties", "patternProperties", "$defs", "definitions":
		return true
	}
	return false
}

// IsAnnotationKey 判断 schema 对象上的某个成员是不是**注解**（压表时删）。
//
// inPropertyMap 是这个对象的位置：为真表示它是一张「参数名 → schema」映射，
// 这一层的键就是参数名——哪怕它叫 description，也只是个名字，必须保留。
// 这是两处压表实现唯一需要共同判断的事；收在这里，是为了让「按位置判断」
// 只有一份，而不是每个调用方各自按 key 名写一个删除分支。
func IsAnnotationKey(k string, inPropertyMap bool) bool {
	return !inPropertyMap && k == description
}

// stripAnnotations 去掉 v 里所有 schema 对象上的 description 注解，保留参数名。
//
// v 必须是 json.Unmarshal 进 any 的结果（map[string]any / []any / 标量）。
// 返回新结构，不改入参。
func stripAnnotations(v any) any {
	return stripNode(v, false)
}

// stripNode 递归压一个 schema 节点。
//
// inPropertyMap 为真表示 x 是 `properties` 这类映射的**值**：
// 它的键是参数名（可能就叫 description），它的值是参数自己的 schema。
func stripNode(x any, inPropertyMap bool) any {
	switch t := x.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		if inPropertyMap {
			// 这一层是「参数名 → schema」：键全部保留，值按 schema 走。
			for k, val := range t {
				out[k] = stripNode(val, false)
			}
			return out
		}
		for k, val := range t {
			if IsAnnotationKey(k, false) {
				continue // 这个才是注解
			}
			// properties/patternProperties/$defs 的值是「名字 → schema」的映射。
			out[k] = stripNode(val, IsPropertyMapKey(k))
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = stripNode(val, false)
		}
		return out
	}
	return x
}

// Compact 是 stripAnnotations 的 raw 版：不是合法 JSON 就原样返回。
//
// 这是「只删注解」这一档的全部实现。cmd/dshd 不要另写一份：它的 sameShape
// 需要同一份语义，直接调这里。
func Compact(raw json.RawMessage) json.RawMessage {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	b, err := json.Marshal(stripAnnotations(v))
	if err != nil {
		return raw
	}
	return b
}
