package llm

import (
	"bytes"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// Usage 的 JSON 形状有两层硬约束：
//
//  1. 没有扩展字段时，marshal 出来的字节必须与历史实现**逐字节一致**
//     （prompt_tokens / completion_tokens / total_tokens，顺序固定）；
//  2. provider 回的扩展计数必须**无损保留**，不能因为 Usage 只声明了三个整数
//     就把 prompt_cache_hit_tokens、prompt_tokens_details.cached_tokens、
//     completion_tokens_details.reasoning_tokens 这些官方字段整块吞掉。
//
// 扩展值在内存里是"点号路径 → 整数"的扁平表（如
// "prompt_tokens_details.cached_tokens"），marshal 时再还原成嵌套对象，
// 这样反序列化-再序列化仍保持 provider 原来的形状。

// usageStandardFields 是三个标准计数字段，永远排在最前。
var usageStandardFields = []string{"prompt_tokens", "completion_tokens", "total_tokens"}

// Extension 取一个 provider 扩展计数；没有就返回 0, false。
func (u Usage) Extension(path string) (int64, bool) {
	value, ok := u.Extensions[path]
	return value, ok
}

// MarshalJSON 手写：标准三字段在前，扩展字段按名字排序，点号路径还原成嵌套对象。
func (u Usage) MarshalJSON() ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteString(`{"prompt_tokens":`)
	buffer.WriteString(strconv.Itoa(u.PromptTokens))
	buffer.WriteString(`,"completion_tokens":`)
	buffer.WriteString(strconv.Itoa(u.CompletionTokens))
	buffer.WriteString(`,"total_tokens":`)
	buffer.WriteString(strconv.Itoa(u.TotalTokens))

	if len(u.Extensions) > 0 {
		nested := map[string]any{}
		names := make([]string, 0, len(u.Extensions))
		for path := range u.Extensions {
			names = append(names, path)
		}
		sort.Strings(names)
		for _, path := range names {
			parts := strings.Split(path, ".")
			insertUsageExtension(nested, parts, u.Extensions[path])
		}
		top := make([]string, 0, len(nested))
		for name := range nested {
			top = append(top, name)
		}
		sort.Strings(top)
		for _, name := range top {
			key, err := json.Marshal(name)
			if err != nil {
				return nil, err
			}
			value, err := json.Marshal(nested[name])
			if err != nil {
				return nil, err
			}
			buffer.WriteByte(',')
			buffer.Write(key)
			buffer.WriteByte(':')
			buffer.Write(value)
		}
	}
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}

// insertUsageExtension 把点号路径写进嵌套 map；同名标量与对象冲突时以后者为准
// （usage 里不会出现这种输入，这里只是不让病态 JSON 覆盖内存）。
func insertUsageExtension(dst map[string]any, parts []string, value int64) {
	if len(parts) == 1 {
		dst[parts[0]] = value
		return
	}
	child, ok := dst[parts[0]].(map[string]any)
	if !ok {
		child = map[string]any{}
		dst[parts[0]] = child
	}
	insertUsageExtension(child, parts[1:], value)
}

// UnmarshalJSON 解析标准三字段，并把其余数值叶收进 Extensions（点号路径）。
// 非数值（字符串/布尔/null）不是计数，忽略；未知对象继续向下展开。
func (u *Usage) UnmarshalJSON(raw []byte) error {
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	*u = Usage{}
	_ = json.Unmarshal(fields["prompt_tokens"], &u.PromptTokens)
	_ = json.Unmarshal(fields["completion_tokens"], &u.CompletionTokens)
	_ = json.Unmarshal(fields["total_tokens"], &u.TotalTokens)
	extensions := map[string]int64{}
	for name, value := range fields {
		if isStandardUsageField(name) {
			continue
		}
		flattenUsageExtension(name, value, extensions)
	}
	if len(extensions) > 0 {
		u.Extensions = extensions
	}
	return nil
}

func isStandardUsageField(name string) bool {
	for _, standard := range usageStandardFields {
		if name == standard {
			return true
		}
	}
	return false
}

// flattenUsageExtension 递归收集数值叶：对象展开成 parent.child，其它类型忽略。
func flattenUsageExtension(prefix string, raw json.RawMessage, out map[string]int64) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return
	}
	switch trimmed[0] {
	case '{':
		var children map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &children); err != nil {
			return
		}
		for name, child := range children {
			flattenUsageExtension(prefix+"."+name, child, out)
		}
	case '"', 't', 'f', 'n':
		return
	default:
		number := json.Number(trimmed)
		if value, err := number.Int64(); err == nil {
			out[prefix] = value
		}
	}
}
