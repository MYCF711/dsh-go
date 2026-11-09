// Package config 是宿主配置的**树**与**补丁**层。
//
// 为什么不是"扁平 key→string"：`/api/config.get|set` 那一层只搬 kv，于是
// `{"ui": {"theme": "dark"}}` 这种"一层套一层"的配置在协议里根本表示不出来——
// 界面要么把整棵子树序列化成一个字符串塞进某个 key（读写两端各解析一次，
// 迟早对不上），要么把层级拍平成 `ui.theme`（那就再也分不清"键名里带点"
// 和"嵌套"）。本包让树成为**唯一真值**，扁平表只是它的投影（`Flatten`）。
//
// 三条硬约束（都有测试钉住）：
//
//  1. **有类型**：string / bool / number / object / array 各是各的，不做字符串化；
//  2. **patch 全有或全无**：一条 op 不合法，整批一条都不落（见 ApplyPatch）；
//  3. **revision 单调**：写入必须带 expectedRevision（见 Store.Apply），
//     过期就是 `*RevisionConflict`，绝不静默覆盖别人的修改。
//
// 落盘是**原子**的：临时文件 → fsync → rename（见 store.go）。写失败时磁盘上
// 一个字节都不变、内存里的 revision 也不前进——"没有落盘却说成功了"比报错更糟。
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

const (
	// FileSchema 是落盘文件的 schema。不认识的 schema 一律拒绝加载：
	// 把别人的格式当自己的读，下一次写就会把它覆盖掉。
	FileSchema = "dsh.config-tree@1"

	// maxDepth / maxKeyLen 是"防呆"上限，不是能力上限：
	// 配置文件是给人看和给人改的，嵌 40 层只会是拼错了。
	maxDepth  = 32
	maxKeyLen = 128
)

var (
	// ErrInvalidValue 是"这个值放不进配置树"（不可 JSON 序列化、NaN、超深）。
	ErrInvalidValue = errors.New("配置值不合法")
	// ErrInvalidKey 是"这个键名不能当配置键"（空、带点、过长）。
	ErrInvalidKey = errors.New("配置键名不合法")
)

// Tree 是一棵配置树。零值不可用——用 NewTree / EmptyTree 建。
//
// 它是**值语义**的：`Snapshot`/`ApplyPatch` 都做深拷贝，调用方手里的快照
// 改了不会影响树。这一点是 patch 原子性的基础。
type Tree struct {
	root map[string]any
}

// EmptyTree 是一棵空树（新库）。
func EmptyTree() *Tree { return &Tree{root: map[string]any{}} }

// NewTree 建树。values 会被深拷贝并逐层校验（见 normalizeValue）。
func NewTree(values map[string]any) (*Tree, error) {
	root, err := normalizeObject(values, 0)
	if err != nil {
		return nil, err
	}
	return &Tree{root: root}, nil
}

// TreeFromFlat 把旧协议的扁平表长回树里。
//
// **它是有损的**：扁平投影把所有叶子都写成了字符串（数组写成 JSON 文本、
// null 写成空串），所以这里只能把 `a.b` 重新拆成层级，值一律按字符串放回。
// 需要精确类型的迁移（比如把 `ui.zoom` 变成数字）必须走 NewTree 或一条明确的
// patch，不能指望这个函数猜。
func TreeFromFlat(flat map[string]string) (*Tree, error) {
	values := map[string]any{}
	paths := make([]string, 0, len(flat))
	for path := range flat {
		paths = append(paths, path)
	}
	// 固定顺序：同样的扁平表永远得到同样的树（也保证"a 与 a.b 冲突"这类
	// 错误可复现，而不是看 map 的随机遍历顺序）。
	sort.Strings(paths)
	for _, path := range paths {
		raw := flat[path]
		segments, err := splitPath(path)
		if err != nil {
			return nil, fmt.Errorf("扁平键 %q 不能还原成树：%w", path, err)
		}
		if err := setPath(values, segments, raw); err != nil {
			return nil, fmt.Errorf("扁平键 %q 写不进树：%w", path, err)
		}
	}
	return NewTree(values)
}

// Snapshot 返回整棵树的深拷贝。
func (t *Tree) Snapshot() map[string]any {
	if t == nil {
		return map[string]any{}
	}
	return deepCopyObject(t.root)
}

// Get 按点分路径取一个节点。路径穿过标量时回 ok=false（不返回零值）。
func (t *Tree) Get(path string) (any, bool) {
	if t == nil {
		return nil, false
	}
	segments, err := splitPath(path)
	if err != nil {
		return nil, false
	}
	var node any = t.root
	for _, segment := range segments {
		object, ok := node.(map[string]any)
		if !ok {
			return nil, false
		}
		value, ok := object[segment]
		if !ok {
			return nil, false
		}
		node = value
	}
	return deepCopyValue(node), true
}

// Keys 返回顶层键（升序）。
func (t *Tree) Keys() []string {
	if t == nil {
		return nil
	}
	keys := make([]string, 0, len(t.root))
	for key := range t.root {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Flatten 是给旧协议用的**投影**：每个叶子一条 `a.b.c` → 字符串。
//
//   - 标量：字符串原样；bool → "true"/"false"；数字 → strconv 最短表示；
//   - null → 空串（协议里没有 null）；
//   - 数组/空对象 → 规范 JSON 文本（数组整体是一值，不做 `a[0]` 展开：
//     半个数组没有意义，patch 只能整只替换，见 patch.go）。
func (t *Tree) Flatten() map[string]string {
	out := map[string]string{}
	if t == nil {
		return out
	}
	flattenInto("", t.root, out)
	return out
}

func flattenInto(prefix string, node any, out map[string]string) {
	object, ok := node.(map[string]any)
	if !ok {
		return
	}
	for key, value := range object {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		switch typed := value.(type) {
		case map[string]any:
			if len(typed) == 0 {
				out[path] = "{}"
				continue
			}
			flattenInto(path, typed, out)
		case []any:
			encoded, err := canonicalJSON(typed)
			if err != nil {
				// 归一化阶段已经保证可序列化；走到这里是内部 bug，宁可留空也不
				// 把半个数组写给旧客户端。
				out[path] = ""
				continue
			}
			out[path] = string(encoded)
		case nil:
			out[path] = ""
		default:
			out[path] = scalarString(value)
		}
	}
}

func scalarString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64)
	default:
		return fmt.Sprint(value)
	}
}

// CanonicalJSON 是**规范**序列化：键升序、数字最短表示、不转义 HTML。
// 快照 / digest / 落盘都用它，所以同一棵树永远给同样的字节。
func (t *Tree) CanonicalJSON() ([]byte, error) {
	if t == nil {
		return []byte("{}"), nil
	}
	return canonicalJSON(t.root)
}

func canonicalJSON(value any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, fmt.Errorf("%w：%v", ErrInvalidValue, err)
	}
	// Encoder 会补一个换行；规范字节里不要它（否则 digest 白算）。
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// --- 归一化：任何值进树之前都必须先过这里 -------------------------------

func normalizeObject(values map[string]any, depth int) (map[string]any, error) {
	if depth > maxDepth {
		return nil, fmt.Errorf("%w：嵌套超过 %d 层", ErrInvalidValue, maxDepth)
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		if err := validateKey(key); err != nil {
			return nil, err
		}
		normalized, err := normalizeValue(value, depth+1)
		if err != nil {
			return nil, fmt.Errorf("键 %q：%w", key, err)
		}
		out[key] = normalized
	}
	return out, nil
}

func normalizeValue(value any, depth int) (any, error) {
	if depth > maxDepth {
		return nil, fmt.Errorf("%w：嵌套超过 %d 层", ErrInvalidValue, maxDepth)
	}
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case string:
		return typed, nil
	case bool:
		return typed, nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return nil, fmt.Errorf("%w：NaN/Inf 不是 JSON 数字", ErrInvalidValue)
		}
		return typed, nil
	case float32:
		return normalizeValue(float64(typed), depth)
	case int:
		return float64(typed), nil
	case int8:
		return float64(typed), nil
	case int16:
		return float64(typed), nil
	case int32:
		return float64(typed), nil
	case int64:
		return float64(typed), nil
	case uint:
		return float64(typed), nil
	case uint8:
		return float64(typed), nil
	case uint16:
		return float64(typed), nil
	case uint32:
		return float64(typed), nil
	case uint64:
		return float64(typed), nil
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return nil, fmt.Errorf("%w：%v", ErrInvalidValue, err)
		}
		return normalizeValue(parsed, depth)
	case map[string]any:
		return normalizeObject(typed, depth)
	case []any:
		out := make([]any, 0, len(typed))
		for index, element := range typed {
			normalized, err := normalizeValue(element, depth+1)
			if err != nil {
				return nil, fmt.Errorf("第 %d 个元素：%w", index, err)
			}
			out = append(out, normalized)
		}
		return out, nil
	case []string:
		out := make([]any, 0, len(typed))
		for _, element := range typed {
			out = append(out, element)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("%w：类型 %T 不能放进配置树（只能 string/bool/number/object/array/null）", ErrInvalidValue, value)
	}
}

func validateKey(key string) error {
	if key == "" {
		return fmt.Errorf("%w：键名不能为空", ErrInvalidKey)
	}
	if strings.Contains(key, ".") {
		return fmt.Errorf("%w：键名 %q 不能带 '.'（否则 %q 与嵌套 a→b 是同一份配置的两种拼法）", ErrInvalidKey, key, key)
	}
	if len(key) > maxKeyLen {
		return fmt.Errorf("%w：键名超过 %d 字节", ErrInvalidKey, maxKeyLen)
	}
	return nil
}

// --- 深拷贝 -------------------------------------------------------------

func deepCopyObject(object map[string]any) map[string]any {
	out := make(map[string]any, len(object))
	for key, value := range object {
		out[key] = deepCopyValue(value)
	}
	return out
}

func deepCopyValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return deepCopyObject(typed)
	case []any:
		out := make([]any, 0, len(typed))
		for _, element := range typed {
			out = append(out, deepCopyValue(element))
		}
		return out
	default:
		return value
	}
}

// --- 路径 ---------------------------------------------------------------

// splitPath 把 `a.b.c` 拆成段。空段（`a..b`、首尾点）一律拒绝：
// 否则 `a.` 与 `a` 会指向同一个节点，patch 的幂等性就说不清了。
func splitPath(path string) ([]string, error) {
	if path == "" {
		return nil, fmt.Errorf("%w：路径不能为空", ErrInvalidPath)
	}
	segments := strings.Split(path, ".")
	if len(segments) > maxDepth {
		return nil, fmt.Errorf("%w：路径超过 %d 段", ErrInvalidPath, maxDepth)
	}
	for _, segment := range segments {
		if segment == "" {
			return nil, fmt.Errorf("%w：路径 %q 里有空段", ErrInvalidPath, path)
		}
		if err := validateKey(segment); err != nil {
			return nil, fmt.Errorf("%w：路径 %q", err, path)
		}
	}
	return segments, nil
}
