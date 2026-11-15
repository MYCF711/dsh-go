package session

import (
	"encoding/json"
	"fmt"
	"regexp"

	"dshgo/codecs/v4"
)

// `subagent/catalog` 的版本化事实（v0/1/2 历史 + rc.2 的 v3）。
//
// 官方证据：
//   - 声明：`dsh-session-reference/lib/typert.host.js:649`
//     `{childId, childCreatedAt} & (v0 | v1 | v2 | {version:3, name} & ({mode:'continuable',label} | {mode:'external',label?}))`
//   - 历史版本校验：`dsh-session-format-v3-to-v4/lib/index.js:926-930`（catalogFact）
//   - v3 校验：同文件 `:1055-1056`（nativeCatalogFact）
//   - 同代唯一性：同文件 `:1037-1046`（duplicate catalog child / child name）
//   - 事件词汇表写入端：`dsh-subagent/src/catalog.ts:21,40`；投影 `stateVersion: 6`（`catalog.ts:138`）
//
// **Lead 裁决（2026-11-12）**：接受前向不兼容——rc.2 起按 v3 写（带 `name`），
// 旧版宿主读不了含 v3 的会话；v0/1/2 必须保持可读（旧日志照样能打开）。
const (
	// CatalogVersionV0 是最早的目录事实（有 continuable/one-shot/external，没有 unknown）。
	CatalogVersionV0 = 0
	// CatalogVersionV1 增 `unknown` 模式。
	CatalogVersionV1 = 1
	// CatalogVersionV2 只表达 external。
	CatalogVersionV2 = 2
	// CatalogVersionV3 是 rc.2 起 native V4 唯一写法：带子代理名。
	CatalogVersionV3 = 3

	// CatalogModeContinuable 表示可续问的持久子会话。
	CatalogModeContinuable = "continuable"
	// CatalogModeOneShot 表示一次性子代理。
	CatalogModeOneShot = "one-shot"
	// CatalogModeExternal 表示外部（非本机创建）子会话。
	CatalogModeExternal = "external"
	// CatalogModeUnknown 表示 v1 里"不知道"的历史记录。
	CatalogModeUnknown = "unknown"

	// CatalogLeadReservedName 是保留名：lead 是主代理，不允许当子代理名。
	CatalogLeadReservedName = "lead"
)

// catalogChildNamePattern 是官方正则 `^[a-z][a-z0-9-]{0,31}$`：
// 小写字母开头、只含小写字母/数字/连字符、总长 ≤32。
var catalogChildNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// SubagentCatalogFact 是一条已校验的 `subagent/catalog` 事实。
type SubagentCatalogFact struct {
	ChildID        string
	ChildCreatedAt int64
	Version        int
	Mode           string
	Name           string // 只有 v3 有
	Label          string
}

// CatalogChildNameValid 判断子代理名是否合法（v3）。
func CatalogChildNameValid(name string) bool {
	return catalogChildNamePattern.MatchString(name) && name != CatalogLeadReservedName
}

// ValidateSubagentCatalogPayload 校验一条 `subagent/catalog` 载荷。
//
// 逐条照抄官方两个函数（catalogFact 管 v0/1/2、nativeCatalogFact 管 v3）：
//   - `version` ∈ {0,1,2,3}；
//   - `childId` 必须是非空字符串？官方只要求 string（不查空）——这里同样只查 string，
//     不擅自加严：加严会让官方写下的合法日志打不开；
//   - `childCreatedAt` 必须是整数（官方 `sessionFormatCount`）；
//   - `(version === 2) === (mode === 'external')`：v2 只表达 external，非 v2 不许 external；
//   - `version === 0` 不许 `unknown`；
//   - `mode === 'continuable'` 必须有字符串 `label`；
//   - `label` 出现时必须是字符串；
//   - v3 额外：`mode` 只允许 continuable/external；`name` 必填且匹配正则、不得是 `lead`；
//     continuable 的 `label` 必填。
func ValidateSubagentCatalogPayload(data json.RawMessage) (SubagentCatalogFact, error) {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(data, &value); err != nil || value == nil {
		return SubagentCatalogFact{}, fmt.Errorf("subagent/catalog 载荷必须是对象: %s", truncateJSON(data))
	}

	version, ok, err := catalogInt(value, "version")
	if err != nil || !ok {
		return SubagentCatalogFact{}, fmt.Errorf("subagent/catalog 缺少整数 version: %s", truncateJSON(data))
	}
	if version < CatalogVersionV0 || version > CatalogVersionV3 {
		return SubagentCatalogFact{}, fmt.Errorf("subagent/catalog 版本 %d 不支持（只支持 0/1/2/3）", version)
	}

	childID, err := catalogString(value, "childId", true)
	if err != nil {
		return SubagentCatalogFact{}, err
	}
	createdAt, ok, err := catalogInt(value, "childCreatedAt")
	if err != nil || !ok {
		return SubagentCatalogFact{}, fmt.Errorf("subagent/catalog 缺少整数 childCreatedAt")
	}
	mode, err := catalogString(value, "mode", true)
	if err != nil {
		return SubagentCatalogFact{}, err
	}
	label, err := catalogString(value, "label", false)
	if err != nil {
		return SubagentCatalogFact{}, err
	}

	fact := SubagentCatalogFact{
		ChildID:        childID,
		ChildCreatedAt: createdAt,
		Version:        int(version),
		Mode:           mode,
		Label:          label,
	}

	if version == CatalogVersionV3 {
		if mode != CatalogModeContinuable && mode != CatalogModeExternal {
			return SubagentCatalogFact{}, fmt.Errorf("subagent/catalog v3 的 mode 只允许 %q/%q，收到 %q",
				CatalogModeContinuable, CatalogModeExternal, mode)
		}
		name, err := catalogString(value, "name", true)
		if err != nil {
			return SubagentCatalogFact{}, fmt.Errorf("subagent/catalog v3 必须带 name: %w", err)
		}
		if !catalogChildNamePattern.MatchString(name) {
			return SubagentCatalogFact{}, fmt.Errorf("subagent/catalog 的 name %q 不符合 ^[a-z][a-z0-9-]{0,31}$", name)
		}
		if name == CatalogLeadReservedName {
			return SubagentCatalogFact{}, fmt.Errorf("subagent/catalog 的 name 不得为保留名 %q", CatalogLeadReservedName)
		}
		if mode == CatalogModeContinuable && label == "" {
			return SubagentCatalogFact{}, fmt.Errorf("subagent/catalog v3 continuable 子代理必须带非空 label")
		}
		fact.Name = name
		return fact, nil
	}

	// v0/1/2：官方 catalogFact
	switch mode {
	case CatalogModeContinuable, CatalogModeOneShot, CatalogModeExternal:
	case CatalogModeUnknown:
		if version == CatalogVersionV0 {
			return SubagentCatalogFact{}, fmt.Errorf("subagent/catalog v0 不允许 mode=unknown")
		}
	default:
		return SubagentCatalogFact{}, fmt.Errorf("subagent/catalog 的 mode %q 不受支持", mode)
	}
	if (version == CatalogVersionV2) != (mode == CatalogModeExternal) {
		return SubagentCatalogFact{}, fmt.Errorf("subagent/catalog v%d 与 mode=%q 不自洽（只有 v2 表达 external，v2 只能表达 external）", version, mode)
	}
	if mode == CatalogModeContinuable && label == "" {
		return SubagentCatalogFact{}, fmt.Errorf("subagent/catalog continuable 子代理必须带非空 label")
	}
	return fact, nil
}

// ValidateSubagentCatalogGeneration 校验**本代**（`seq >= inheritedEventCount`）的
// 目录事实唯一性：`childId` 恒唯一；写了 `name` 的（v3）同代不许重名。
//
// 继承代（fork 来的前缀）不参与：祖代与本代出现同名子代理是合法的两条独立事实。
func ValidateSubagentCatalogGeneration(events []v4.Event, inheritedEventCount int64) error {
	ids := make(map[string]struct{})
	names := make(map[string]struct{})
	for _, event := range events {
		if event.Type != EventSubagentCatalog {
			continue
		}
		if event.Seq < inheritedEventCount {
			continue
		}
		fact, err := ValidateSubagentCatalogPayload(event.Data)
		if err != nil {
			return fmt.Errorf("seq %d: %w", event.Seq, err)
		}
		if _, dup := ids[fact.ChildID]; dup {
			return fmt.Errorf("seq %d: duplicate catalog child %s", event.Seq, fact.ChildID)
		}
		ids[fact.ChildID] = struct{}{}
		if fact.Name == "" {
			continue
		}
		if _, dup := names[fact.Name]; dup {
			return fmt.Errorf("seq %d: duplicate catalog child name %s", event.Seq, fact.Name)
		}
		names[fact.Name] = struct{}{}
	}
	return nil
}

// SubagentCatalogV3 构造 rc.2 形状的 `subagent/catalog` 载荷（写入端唯一构造点）。
//
// `label` 为空时不出现在载荷里（官方 v3 external 的 `label?` 可选）；
// continuable 必须有 label——空 label 会被校验拒绝，这里不偷偷补默认值。
func SubagentCatalogV3(childID string, childCreatedAt int64, mode, label, name string) map[string]any {
	payload := map[string]any{
		"version":        CatalogVersionV3,
		"childId":        childID,
		"childCreatedAt": childCreatedAt,
		"mode":           mode,
		"name":           name,
	}
	if label != "" {
		payload["label"] = label
	}
	return payload
}

func catalogString(value map[string]json.RawMessage, key string, required bool) (string, error) {
	raw, ok := value[key]
	if !ok {
		if required {
			return "", fmt.Errorf("subagent/catalog 缺少字符串 %s", key)
		}
		return "", nil
	}
	var out string
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("subagent/catalog 的 %s 必须是字符串（收到 %s）", key, raw)
	}
	return out, nil
}

// catalogInt 读整数字段；JSON 里 `1.0` 与 `1` 都接受（官方 sessionFormatCount 也接受整值）。
func catalogInt(value map[string]json.RawMessage, key string) (int64, bool, error) {
	raw, ok := value[key]
	if !ok {
		return 0, false, nil
	}
	var asFloat float64
	if err := json.Unmarshal(raw, &asFloat); err != nil {
		return 0, false, nil
	}
	if asFloat != float64(int64(asFloat)) {
		return 0, false, fmt.Errorf("subagent/catalog 的 %s 必须是整数（收到 %s）", key, raw)
	}
	return int64(asFloat), true, nil
}

func truncateJSON(data []byte) string {
	const limit = 120
	if len(data) <= limit {
		return string(data)
	}
	return string(data[:limit]) + "…"
}
