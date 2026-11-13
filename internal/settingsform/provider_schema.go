package settingsform

// 官方 0.2.2-rc.2 `dsh-llm` 的 retryPolicy：**发布给设置表单的 schema** +
// 宿主读/写路径上的同一套校验。
//
// 权威源（随 rc.2 asar 发布的 TypeScript 原件）：
//
//	dsh-llm/src/error.ts:17     LLM_FAILURE_CODES（52 码，其中 8 个带 retryChoice）
//	dsh-llm/src/error.ts:30     RETRY_CODE_CHOICES = 按定义序筛出的 8 个可选码
//	dsh-llm/src/error.ts:76     RETRY_CODE_CHOICES 的只读导出
//	dsh-llm/src/retry-policy.ts:19-25  DEFAULT_MAX_RETRIES=5 / INITIAL=500 /
//	                                   MAX=10000 / JITTER=0.1 / DEFAULT_CODES
//	dsh-llm/src/retry-policy.ts:84-106 RetryPolicySchema：
//	   backoff.initialDelayMs/.maxDelayMs 都是 `.min(Number.MIN_VALUE).max(MAX_TIMER_DELAY_MS)`
//	   mode 是 `z.const('normal')/z.const('always')`，两分支各自 required
//	   retryableCodes 是 `z.array(z.union([...8 个 z.const, z.string().min(1)])).min(1)`
//	dsh-llm/src/retry-policy.ts:120-215 resolveRetryPolicy 的**跨字段规则**
//	   （initialDelayMs <= maxDelayMs、code 非空/不重复、未知键拒绝）——schema
//	   本身表达不了，读路径必须另做一遍。
//
// 消费端（决定形状不能走样）：
//
//	dsh-client-ui-settings-models/src/client/RetryPolicyEditor.tsx:1-9
//	   "built from the `retryPolicy` schema the owning namespace publishes"
//	:69-71  choicesOf 读 item 的 `list`（只取 type:'const' 的 value）
//	:134-144 numberFailure：`min === Number.MIN_VALUE` 是"必须为正"的 schema 哨兵，
//	         换成别的正数会退化成通用 min 文案
//	dsh-client-ui-settings-models/src/client/ProviderEditor.tsx:273
//	   schema 路径 = `providers.<route>.retryPolicy`
//
// 为什么发布在 `llm` 命名空间：宿主 `llm.listConfigurableProviders` 报的
// settingsNs 就是 "llm"（internal/llm/provider_registry.go:119），UI 按它去
// describe 结果里找 namespace；放别处 UI 解析不到，等于没发布。

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

const (
	// NamespaceLLM 是官方 provider 配置所在的设置命名空间（llm.listConfigurableProviders
	// 的 settingsNs；官方 pi-ai 的 profile 就挂在 `providers.<route>` 下）。
	NamespaceLLM = "llm"

	// RetryModeNormal 与 RetryModeAlways 是官方 RetryPolicySchema 的两个 union 分支。
	RetryModeNormal = "normal"
	RetryModeAlways = "always"

	// 官方默认值（dsh-llm/src/retry-policy.ts:19-22）——rc.2 一个字没变。
	RetryDefaultMaxRetries     = 5
	RetryDefaultInitialDelayMs = 500
	RetryDefaultMaxDelayMs     = 10000

	// RetryMaxTimerDelayMs 是官方 MAX_TIMER_DELAY_MS（dsh-timeout/src/index.ts:25）。
	RetryMaxTimerDelayMs = 2_147_483_647

	// RetryMaxSafeInteger 是官方 maxRetries 的上界（Number.MAX_SAFE_INTEGER）。
	RetryMaxSafeInteger = 9_007_199_254_740_991

	// ProviderProfilesKey 是命名空间根下 provider 路由的字典键。
	ProviderProfilesKey = "providers"
	// RetryPolicyKey 是每个 provider profile 下的重试策略键。
	RetryPolicyKey = "retryPolicy"
)

// RetryDefaultJitterRatio 是官方默认抖动比例（0.1）。
const RetryDefaultJitterRatio = 0.1

// RetryCodeChoices 是官方 RETRY_CODE_CHOICES，**顺序即定义序**（error.ts:17-76）。
//
// QUOTA / MALFORMED_RESPONSE / STREAM_CLOSED 是 rc.2 新增的候选：表单可以选，
// 但本宿主没有产生点（宿主只产生 RATE_LIMIT / SERVER / TRANSPORT 等），
// 选进去也不会误触发重试——官方语义就是"code 不匹配就不重试"。
var RetryCodeChoices = []string{
	"EMPTY_RESPONSE", "MALFORMED_RESPONSE", "QUOTA", "RATE_LIMIT",
	"SERVER", "STREAM_CLOSED", "TIMEOUT", "TRANSPORT",
}

// DefaultRetryableCodes 是官方 DEFAULT_RETRYABLE_CODES（5 个，顺序一致）。
var DefaultRetryableCodes = []string{"EMPTY_RESPONSE", "RATE_LIMIT", "SERVER", "TIMEOUT", "TRANSPORT"}

// schemaEnvelope 按 schemastery `toJSON()` 的形状拼 envelope：
// `{uid: <root>, refs: {"<uid>": {type, meta, dict/list/inner/...}}}`，
// 其中 `dict` 的值、`list` 的元素、`inner` 都是 **uid**（数字），
// `meta` 承载 default/min/max/step/required。客户端 `new Schema(envelope)`
// 就是按这个形状 rehydrate 的（schemastery/src/index.ts:254-270,311-321）。
type schemaEnvelope struct {
	refs map[string]any
	next int
}

func newSchemaEnvelope() *schemaEnvelope {
	return &schemaEnvelope{refs: map[string]any{}}
}

func (b *schemaEnvelope) node(kind string, meta map[string]any, extra map[string]any) int {
	b.next++
	uid := b.next
	if meta == nil {
		meta = map[string]any{}
	}
	node := map[string]any{"type": kind, "meta": normalizeSchemaMeta(meta)}
	for key, value := range extra {
		node[key] = value
	}
	b.refs[strconv.Itoa(uid)] = node
	return uid
}

func (b *schemaEnvelope) constNode(value string, required bool) int {
	meta := map[string]any{}
	if required {
		meta["required"] = true
	}
	return b.node("const", meta, map[string]any{"value": value})
}

func (b *schemaEnvelope) stringNode(meta map[string]any) int {
	return b.node("string", meta, nil)
}

func (b *schemaEnvelope) numberNode(meta map[string]any) int {
	return b.node("number", meta, nil)
}

func (b *schemaEnvelope) objectNode(dict map[string]any) int {
	return b.node("object", nil, map[string]any{"dict": dict})
}

func (b *schemaEnvelope) arrayNode(inner int, meta map[string]any) int {
	return b.node("array", meta, map[string]any{"inner": inner})
}

func (b *schemaEnvelope) dictNode(inner, sKey int, meta map[string]any) int {
	return b.node("dict", meta, map[string]any{"inner": inner, "sKey": sKey})
}

func (b *schemaEnvelope) unionNode(list []int, meta map[string]any) int {
	// list 存成 []any：与 JSON 解码后的形状一致，测试/客户端都按数组读。
	items := make([]any, len(list))
	for index, uid := range list {
		items[index] = uid
	}
	return b.node("union", meta, map[string]any{"list": items})
}

func (b *schemaEnvelope) envelope(root int) any {
	return map[string]any{"uid": root, "refs": b.refs}
}

// RetryPolicySchema 返回官方 `retryPolicy` 节点的独立 envelope
// （union[normal, always]，供需要单独发布该节点的调用方使用）。
func RetryPolicySchema() any {
	b := newSchemaEnvelope()
	root := b.retryPolicyUnion()
	return b.envelope(root)
}

// RetryPolicyNamespaceSchema 返回 `llm` 命名空间的 schema：
// 根 object 下 `providers` 是 `z.dict(profile)`，profile 里发布 `retryPolicy`。
//
// 只发布 retryPolicy 一项：baseURL/apiKeyEnv/models 等在宿主由模型目录承载，
// 不在这里假装它们可写（见报告"未实现项"）。
func RetryPolicyNamespaceSchema() any {
	b := newSchemaEnvelope()
	profile := b.objectNode(map[string]any{RetryPolicyKey: b.retryPolicyUnion()})
	keySchema := b.stringNode(nil)
	providers := b.dictNode(profile, keySchema, map[string]any{"default": map[string]any{}})
	root := b.objectNode(map[string]any{ProviderProfilesKey: providers})
	return b.envelope(root)
}

// retryPolicyUnion 构造官方 RetryPolicySchema 的 union 节点；backoff 两个分支共享
// 同一个 uid（官方就是同一个 `backoffSchema` 实例，toJSON 后也共享引用）。
func (b *schemaEnvelope) retryPolicyUnion() int {
	backoff := b.objectNode(map[string]any{
		"initialDelayMs": b.numberNode(map[string]any{
			"default": RetryDefaultInitialDelayMs,
			"min":     math.SmallestNonzeroFloat64, // Number.MIN_VALUE：UI 的"必须为正"哨兵
			"max":     RetryMaxTimerDelayMs,
		}),
		"maxDelayMs": b.numberNode(map[string]any{
			"default": RetryDefaultMaxDelayMs,
			"min":     math.SmallestNonzeroFloat64,
			"max":     RetryMaxTimerDelayMs,
		}),
		"jitterRatio": b.numberNode(map[string]any{
			"default": RetryDefaultJitterRatio,
			"min":     0,
			"max":     1,
		}),
	})

	codeBranches := make([]int, 0, len(RetryCodeChoices)+1)
	for _, code := range RetryCodeChoices {
		codeBranches = append(codeBranches, b.constNode(code, false))
	}
	codeBranches = append(codeBranches, b.stringNode(map[string]any{"min": 1}))
	codeUnion := b.unionNode(codeBranches, nil)

	defaults := make([]any, 0, len(DefaultRetryableCodes))
	for _, code := range DefaultRetryableCodes {
		defaults = append(defaults, code)
	}
	codes := b.arrayNode(codeUnion, map[string]any{"min": 1, "default": defaults})

	normal := b.objectNode(map[string]any{
		"mode": b.constNode(RetryModeNormal, true),
		"maxRetries": b.numberNode(map[string]any{
			"step":    1,
			"min":     0,
			"max":     RetryMaxSafeInteger,
			"default": RetryDefaultMaxRetries,
		}),
		"retryableCodes": codes,
		"backoff":        backoff,
	})
	always := b.objectNode(map[string]any{
		"mode":    b.constNode(RetryModeAlways, true),
		"backoff": backoff,
	})
	return b.unionNode([]int{normal, always}, nil)
}

// normalizeSchemaMeta 把元数据里的整数统一成 float64：wire 上（JSON 解码后）
// 数字就是 float64，builder 产出的形状必须与客户端看到的逐字节同形。
func normalizeSchemaMeta(meta map[string]any) map[string]any {
	out := make(map[string]any, len(meta))
	for key, value := range meta {
		switch typed := value.(type) {
		case int:
			out[key] = float64(typed)
		case int64:
			out[key] = float64(typed)
		default:
			out[key] = value
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// 校验：读路径与写路径共用同一份 schema（envelope 走一遍 + 跨字段规则再走一遍）
// ---------------------------------------------------------------------------

// ProviderNamespaceValidator 是 `llm` 命名空间的声明式校验入口：
// `providers` 下每个 profile 的 `retryPolicy` 按官方 resolveRetryPolicy 校验。
func ProviderNamespaceValidator(value any) error {
	if value == nil {
		return nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return newInvalidTypeError([]string{NamespaceLLM}, "object", value)
	}
	providers, ok := object[ProviderProfilesKey]
	if !ok || providers == nil {
		return nil
	}
	profiles, ok := providers.(map[string]any)
	if !ok {
		return newInvalidTypeError([]string{NamespaceLLM, ProviderProfilesKey}, "object", providers)
	}
	for route, raw := range profiles {
		profile, ok := raw.(map[string]any)
		if !ok {
			return newInvalidTypeError([]string{NamespaceLLM, ProviderProfilesKey, route}, "object", raw)
		}
		policy, ok := profile[RetryPolicyKey]
		if !ok {
			continue
		}
		if err := ValidateRetryPolicyValue(policy, []string{NamespaceLLM, ProviderProfilesKey, route, RetryPolicyKey}); err != nil {
			return err
		}
	}
	return nil
}

// ValidateRetryPolicyValue 按官方 resolveRetryPolicy 的规则校验一份策略值：
// 未知键拒绝、maxRetries 非负安全整数、retryableCodes 非空/非空串/不重复、
// backoff 三项都在界内且 initialDelayMs <= maxDelayMs。
//
// 注意 always 分支：官方 ALWAYS_POLICY_KEYS 与 normal 同集合（切模式后残留的
// normal-only 字段被**忽略**而不是报错），所以这里对 always 只校验 backoff。
func ValidateRetryPolicyValue(value any, path []string) error {
	if value == nil {
		return nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return newInvalidTypeError(path, "object", value)
	}
	mode, ok := object["mode"].(string)
	if !ok || (mode != RetryModeNormal && mode != RetryModeAlways) {
		return &Error{
			Code:    "invalid_enum",
			Message: fmt.Sprintf("%s.mode 必须是 %q 或 %q，收到 %v", joinPath(path), RetryModeNormal, RetryModeAlways, object["mode"]),
			Hint:    "按 settings.describe schema 里 union 分支的 mode 常量提交",
			Status:  400,
		}
	}
	allowed := map[string]bool{"mode": true, "maxRetries": true, "retryableCodes": true, "backoff": true}
	for key := range object {
		if !allowed[key] {
			return &Error{
				Code:    "unknown_field",
				Message: fmt.Sprintf("%s 有未知键 %q", joinPath(path), key),
				Hint:    "官方 RetryPolicySchema 只接受 mode / maxRetries / retryableCodes / backoff",
				Status:  400,
			}
		}
	}
	if mode == RetryModeNormal {
		if raw, present := object["maxRetries"]; present && raw != nil {
			number, ok := numericValue(raw)
			if !ok || math.Trunc(number) != number {
				return newInvalidTypeError(append(path, "maxRetries"), "non-negative safe integer", raw)
			}
			if number < 0 || number > RetryMaxSafeInteger {
				return newOutOfRangeError(append(path, "maxRetries"), raw, "0", strconv.FormatInt(RetryMaxSafeInteger, 10))
			}
		}
		if raw, present := object["retryableCodes"]; present && raw != nil {
			if err := validateRetryableCodes(raw, append(path, "retryableCodes")); err != nil {
				return err
			}
		}
	}
	if raw, present := object["backoff"]; present && raw != nil {
		if err := validateRetryBackoff(raw, append(path, "backoff")); err != nil {
			return err
		}
	}
	return nil
}

func validateRetryableCodes(value any, path []string) error {
	list, ok := value.([]any)
	if !ok {
		return newInvalidTypeError(path, "array of non-empty strings", value)
	}
	if len(list) == 0 {
		return &Error{
			Code:    "out_of_range",
			Message: fmt.Sprintf("%s 不能为空（官方 .min(1)）", joinPath(path)),
			Hint:    "至少选一个可重试错误码；省略该字段表示用默认 5 个",
			Status:  400,
		}
	}
	seen := map[string]bool{}
	for index, raw := range list {
		code, ok := raw.(string)
		if !ok || strings.TrimSpace(code) == "" {
			return newInvalidTypeError(append(path, strconv.Itoa(index)), "non-empty string", raw)
		}
		if seen[code] {
			return &Error{
				Code:    "invalid_value",
				Message: fmt.Sprintf("%s 有重复码 %q（官方 resolveRetryPolicy 拒绝重复）", joinPath(path), code),
				Hint:    "每个错误码只列一次",
				Status:  400,
			}
		}
		seen[code] = true
	}
	return nil
}

func validateRetryBackoff(value any, path []string) error {
	object, ok := value.(map[string]any)
	if !ok {
		return newInvalidTypeError(path, "object", value)
	}
	for key := range object {
		switch key {
		case "initialDelayMs", "maxDelayMs", "jitterRatio":
		default:
			return &Error{
				Code:    "unknown_field",
				Message: fmt.Sprintf("%s 有未知键 %q", joinPath(path), key),
				Hint:    "backoff 只接受 initialDelayMs / maxDelayMs / jitterRatio",
				Status:  400,
			}
		}
	}
	delay := func(field string) (float64, bool, error) {
		raw, present := object[field]
		if !present || raw == nil {
			return 0, false, nil
		}
		number, ok := numericValue(raw)
		if !ok {
			return 0, false, newInvalidTypeError(append(path, field), "positive finite number", raw)
		}
		if math.IsInf(number, 0) || math.IsNaN(number) || number <= 0 || number > RetryMaxTimerDelayMs {
			return 0, false, newOutOfRangeError(append(path, field), raw, "positive", strconv.Itoa(RetryMaxTimerDelayMs))
		}
		return number, true, nil
	}
	initial, hasInitial, err := delay("initialDelayMs")
	if err != nil {
		return err
	}
	maximum, hasMaximum, err := delay("maxDelayMs")
	if err != nil {
		return err
	}
	if hasInitial && hasMaximum && initial > maximum {
		return &Error{
			Code:    "out_of_range",
			Message: fmt.Sprintf("%s.initialDelayMs=%v 必须不大于 %s.maxDelayMs=%v", joinPath(path), initial, joinPath(path), maximum),
			Hint:    "把初始延迟调小，或把最大延迟调大",
			Status:  400,
		}
	}
	if raw, present := object["jitterRatio"]; present && raw != nil {
		number, ok := numericValue(raw)
		if !ok {
			return newInvalidTypeError(append(path, "jitterRatio"), "number between 0 and 1", raw)
		}
		if math.IsInf(number, 0) || math.IsNaN(number) || number < 0 || number > 1 {
			return newOutOfRangeError(append(path, "jitterRatio"), raw, "0", "1")
		}
	}
	return nil
}

// validateDeclaredValue 把一份声明式命名空间的值按 envelope 走一遍（类型/边界），
// 再交给该命名空间自己的语义校验（跨字段规则）。任何不合都 fail-loud。
func validateDeclaredValue(spec sectionSpec, value any) error {
	if spec.schema == nil {
		return nil
	}
	// 顺序是"先语义、后 envelope"：官方 RetryPolicySchema 是 union，而 union
	// 分支失败在 envelope 里只能汇总成一条 `invalid_value`（把
	// `backoff.initialDelayMs 越界` 这类精确原因埋进长串里）。语义校验
	// （ValidateRetryPolicyValue，对齐 resolveRetryPolicy）能把原因说成
	// out_of_range/unknown_field/invalid_enum + 精确字段路径。两层都跑，
	// envelope 继续兜语义规则没表达的类型/边界（例如数值 vs 字符串）。
	if spec.validate != nil {
		if err := spec.validate(value); err != nil {
			return err
		}
	}
	return validateEnvelopeValue(spec.schema, value, []string{spec.ns})
}

// validateEnvelopeValue 是 schemastery envelope 的最小 Go 侧解释器：只覆盖本仓
// 会发布的节点类型（const/string/number/boolean/array/object/union/dict），
// 语义与 schemastery 的 resolve 一致（缺省取 meta.default、required 缺失报错、
// object 允许额外键、dict 的值走 inner）。
func validateEnvelopeValue(envelope any, value any, path []string) error {
	root, ok := envelope.(map[string]any)
	if !ok {
		return fmt.Errorf("%s: schema envelope 不是对象", joinPath(path))
	}
	refs, _ := root["refs"].(map[string]any)
	resolver := envelopeResolver{refs: refs, root: root["uid"]}
	return resolver.validate(resolver.resolve(root["uid"]), value, path)
}

type envelopeResolver struct {
	refs map[string]any
	root any
}

func (r envelopeResolver) resolve(node any) map[string]any {
	if object, ok := node.(map[string]any); ok {
		return object
	}
	key := ""
	switch typed := node.(type) {
	case int:
		key = strconv.Itoa(typed)
	case int64:
		key = strconv.FormatInt(typed, 10)
	case float64:
		key = strconv.FormatInt(int64(typed), 10)
	case string:
		key = typed
	case json.Number:
		key = typed.String()
	}
	object, _ := r.refs[key].(map[string]any)
	return object
}

func (r envelopeResolver) validate(node map[string]any, value any, path []string) error {
	if node == nil {
		return fmt.Errorf("%s: schema 节点为空", joinPath(path))
	}
	meta, _ := node["meta"].(map[string]any)
	if value == nil {
		if required, _ := meta["required"].(bool); required {
			return &Error{Code: "bad_request", Message: fmt.Sprintf("%s 缺失（schema required）", joinPath(path)), Hint: "补上这个字段后重试", Status: 400}
		}
		if fallback, ok := meta["default"]; ok {
			return r.validate(node, fallback, append(path, "(default)"))
		}
		return nil
	}
	switch kind, _ := node["type"].(string); kind {
	case "const":
		if !valuesEqual(value, node["value"]) {
			return &Error{Code: "invalid_enum", Message: fmt.Sprintf("%s 必须是 %v，收到 %v", joinPath(path), node["value"], value), Status: 400}
		}
		return nil
	case "string":
		text, ok := value.(string)
		if !ok {
			return newInvalidTypeError(path, "string", value)
		}
		if err := checkPatternMeta(meta, text, path); err != nil {
			return err
		}
		return checkLengthBounds(meta, len([]byte(text)), path)
	case "boolean":
		if _, ok := value.(bool); !ok {
			return newInvalidTypeError(path, "boolean", value)
		}
		return nil
	case "number":
		number, ok := numericValue(value)
		if !ok {
			return newInvalidTypeError(path, "number", value)
		}
		if minimum, ok := numericMeta(meta, "min"); ok && number < minimum {
			return newOutOfRangeError(path, value, formatNumber(minimum), "")
		}
		if maximum, ok := numericMeta(meta, "max"); ok && number > maximum {
			return newOutOfRangeError(path, value, "", formatNumber(maximum))
		}
		if step, ok := numericMeta(meta, "step"); ok && step != 0 {
			base := 0.0
			if minimum, ok := numericMeta(meta, "min"); ok {
				base = minimum
			}
			if quotient := (number - base) / step; math.Abs(quotient-math.Round(quotient)) > 1e-9 {
				return &Error{Code: "invalid_value", Message: fmt.Sprintf("%s 必须是 %v 的整数倍（step）", joinPath(path), formatNumber(step)), Status: 400}
			}
		}
		return nil
	case "array":
		list, ok := value.([]any)
		if !ok {
			return newInvalidTypeError(path, "array", value)
		}
		if err := checkLengthBounds(meta, len(list), path); err != nil {
			return err
		}
		inner := r.resolve(node["inner"])
		for index, item := range list {
			if err := r.validate(inner, item, append(path, strconv.Itoa(index))); err != nil {
				return err
			}
		}
		return nil
	case "dict":
		object, ok := value.(map[string]any)
		if !ok {
			return newInvalidTypeError(path, "object", value)
		}
		inner := r.resolve(node["inner"])
		for key, item := range object {
			if err := r.validate(inner, item, append(path, key)); err != nil {
				return err
			}
		}
		return nil
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return newInvalidTypeError(path, "object", value)
		}
		dict, _ := node["dict"].(map[string]any)
		for key, raw := range dict {
			if err := r.validate(r.resolve(raw), object[key], append(path, key)); err != nil {
				return err
			}
		}
		return nil
	case "union":
		list, _ := node["list"].([]any)
		var failures []string
		for _, branch := range list {
			err := r.validate(r.resolve(branch), value, path)
			if err == nil {
				return nil
			}
			failures = append(failures, err.Error())
		}
		return &Error{
			Code:    "invalid_value",
			Message: fmt.Sprintf("%s 不匹配任何 schema 分支：%s", joinPath(path), strings.Join(failures, " | ")),
			Hint:    "按 settings.describe 返回的 schema 提交",
			Status:  400,
		}
	}
	return fmt.Errorf("%s: 未支持的 schema 节点类型 %v", joinPath(path), node["type"])
}

// checkPatternMeta 解释官方 schemastery 的 `meta.pattern`：`z.string().pattern(/…/)`
// 在 toJSON 里就是 `{source, flags}`（schemastery/src/index.ts:415-419、670-674
// 的 string 分支）。没有 pattern 的节点直接通过。
func checkPatternMeta(meta map[string]any, text string, path []string) error {
	raw, ok := meta["pattern"]
	if !ok || raw == nil {
		return nil
	}
	node, ok := raw.(map[string]any)
	if !ok {
		// 形状不认识 = 宿主自己造的 envelope 坏了，不是用户输入问题。
		return fmt.Errorf("%s: schema 的 meta.pattern 不是 {source,flags} 对象", joinPath(path))
	}
	source, _ := node["source"].(string)
	if source == "" {
		return nil
	}
	flags, _ := node["flags"].(string)
	return matchPattern(source, flags, text, path)
}

// matchPattern 按 JS 的 flags 语义跑一遍正则：`u` 在 Go 里是默认行为（正则本身就是
// UTF-8 语义，忽略即可），`i`/`m`/`s` 走内联前缀。别的 flag 属于宿主 schema 的
// 装配 bug，fail-loud。
func matchPattern(source, flags, text string, path []string) error {
	prefix := ""
	for _, flag := range flags {
		switch flag {
		case 'u':
		case 'i', 'm', 's':
			prefix += string(flag)
		default:
			return fmt.Errorf("%s: schema 的 meta.pattern 用了不支持的 flag %q", joinPath(path), string(flag))
		}
	}
	compiled, err := regexp.Compile(prefix + source)
	if err != nil {
		return fmt.Errorf("%s: schema 的 meta.pattern 无法编译: %w", joinPath(path), err)
	}
	if compiled.MatchString(text) {
		return nil
	}
	return &Error{
		Code:    "invalid_value",
		Message: fmt.Sprintf("%s 的值 %q 不匹配 %s", joinPath(path), text, source),
		Hint:    "按 schema 的 meta.pattern 提交：语言标识形如 zh / en / zh-Hans / en-US",
		Status:  400,
	}
}

func checkLengthBounds(meta map[string]any, length int, path []string) error {
	if minimum, ok := numericMeta(meta, "min"); ok && float64(length) < minimum {
		return &Error{Code: "out_of_range", Message: fmt.Sprintf("%s 长度 %d 小于下限 %v", joinPath(path), length, formatNumber(minimum)), Status: 400}
	}
	if maximum, ok := numericMeta(meta, "max"); ok && float64(length) > maximum {
		return &Error{Code: "out_of_range", Message: fmt.Sprintf("%s 长度 %d 大于上限 %v", joinPath(path), length, formatNumber(maximum)), Status: 400}
	}
	return nil
}

func numericMeta(meta map[string]any, key string) (float64, bool) {
	raw, ok := meta[key]
	if !ok {
		return 0, false
	}
	return numericValue(raw)
}

func numericValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	}
	return 0, false
}

func valuesEqual(left, right any) bool {
	leftNumber, leftOK := numericValue(left)
	rightNumber, rightOK := numericValue(right)
	if leftOK && rightOK {
		return leftNumber == rightNumber
	}
	return fmt.Sprint(left) == fmt.Sprint(right)
}

func joinPath(path []string) string {
	return strings.Join(path, ".")
}

func formatNumber(value float64) string {
	if value == math.Trunc(value) && math.Abs(value) < 1e15 {
		return strconv.FormatInt(int64(value), 10)
	}
	return strconv.FormatFloat(value, 'g', -1, 64)
}

func newInvalidTypeError(path []string, want string, got any) *Error {
	return &Error{
		Code:    "invalid_type",
		Message: fmt.Sprintf("%s 必须是 %s，收到 %T(%v)", joinPath(path), want, got, got),
		Hint:    "按 settings.describe 的 schema 类型提交",
		Status:  400,
	}
}

func newOutOfRangeError(path []string, value any, minimum, maximum string) *Error {
	bounds := ""
	switch {
	case minimum != "" && maximum != "":
		bounds = minimum + ".." + maximum
	case minimum != "":
		bounds = ">= " + minimum
	case maximum != "":
		bounds = "<= " + maximum
	}
	return &Error{
		Code:    "out_of_range",
		Message: fmt.Sprintf("%s 的值 %v 超出范围 %s", joinPath(path), value, bounds),
		Hint:    "按 schema 的 meta.min / meta.max 提交",
		Status:  400,
	}
}

// isNamespaceNotFound 判断 Store 是否"没有这个命名空间"（声明式命名空间遇到
// 不支持它的 Store 时就是这种情形；生产 serve 已为 llm 接线，不会再命中）。
func isNamespaceNotFound(err error) bool {
	var apiErr *Error
	return errors.As(err, &apiErr) && apiErr.Code == "not_found"
}

// buildProviderNamespaceSpec 是 HostRegistry 里 llm 命名空间的声明。
func buildProviderNamespaceSpec() NamespaceSpec {
	return NamespaceSpec{
		NS:      NamespaceLLM,
		Title:   "LLM providers",
		Applies: "live",
		Schema:  RetryPolicyNamespaceSchema(),
		Base:    map[string]any{ProviderProfilesKey: map[string]any{}},
		Validate: func(value any) error {
			return ProviderNamespaceValidator(value)
		},
	}
}

// readOnlyProviderNamespaceError 是**注入的 Store 不支持该命名空间**时写路径的
// 结构化错误：明确"没写进去"和"该去哪写"，不静默丢弃、不假成功。
//
// 生产路径不再触发它：serve 的 settings 存储已为 `llm` 接上真实读写面
// （cmd/dshd/serve_settings.go，落 <home>/state/settings-namespaces.json）。
// 出现这条说明注入的 Store（测试替身或漏接线的装配）没有该 ns 的写面。
func readOnlyProviderNamespaceError(namespace string) *Error {
	return &Error{
		Code:    "read_only",
		Message: fmt.Sprintf("%s 是声明式命名空间：注入的设置存储没有 %q 的持久段，写入没有落点", namespace, namespace),
		Hint:    "检查注入的 Store 是否实现了该 ns 的读写；生产入口见 cmd/dshd/serve_settings.go 的 llm 分支",
		Status:  405,
	}
}
