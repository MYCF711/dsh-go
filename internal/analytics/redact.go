package analytics

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Event 是进入管道的原始产品事件（对应官方 ProductEvent 的判别字段）。
//
// Attributes 保留原始 JSON，由 sanitizeAttributes 做白名单与脱敏；调用方不能
// 把 prompt/正文/路径直接塞进来——即使塞了也会在入库前被丢弃/替换。
type Event struct {
	EventName  string
	Timestamp  float64
	Attributes json.RawMessage
}

// Record 是本地 JSONL 的一行（也是 exporter 的脱敏载荷）。
//
// Hash 链：Hash = sha256(PrevHash + "\n" + canonical(record without Hash))。
// 第一个记录 PrevHash 为空串。canonical 由 encoding/json 的确定性 map 排序保证。
type Record struct {
	EventName     string         `json:"eventName"`
	Timestamp     float64        `json:"timestamp"`
	Attributes    map[string]any `json:"attributes"`
	PolicyVersion int64          `json:"policyVersion"`
	ReceivedAt    string         `json:"receivedAt"`

	// AnonymousUserID 是 `$DSHD_HOME/.anonymous-user-id` 的内容（官方把它挂在
	// OTel Resource 的 user.id 上）。omitempty：没有身份时该字段不出现，**旧记录
	// 的 hash 因此逐字节不变**（hash 是对整条记录做规范化 JSON）。
	AnonymousUserID string `json:"anonymousUserId,omitempty"`
	// Environment / MissingFields 是官方 report() 并入 attributes 的环境维度
	// （os/arch/版本/CPU）。放在 Record 顶层而不是 Attributes 里，是为了守住
	// "attributes 只来自调用方白名单"这条隐私边界：环境维度**只能**来自采集器。
	Environment   map[string]any `json:"environment,omitempty"`
	MissingFields string         `json:"missingFields,omitempty"`

	PrevHash string `json:"prevHash"`
	Hash     string `json:"hash"`
}

// allowedAttributeKeys 是官方 ProductEventMap（app.asar 0.2.2-rc.1 的
// dsh-client-product-analytics/lib/typert.host.js 第 488 行）所有变体的属性键
// 并集。白名单之外一律丢弃：prompt/正文/路径/密钥不可能因为调用方多塞一个键
// 就进入本地库。
var allowedAttributeKeys = map[string]struct{}{
	"button_name":            {},
	"page_name":              {},
	"selected_content":       {},
	"popup_name":             {},
	"is_success":             {},
	"error_reason":           {},
	"session_id":             {},
	"model_name":             {},
	"thinking_effort":        {},
	"run_mode":               {},
	"msg_type":               {},
	"is_new_session":         {},
	"agent_preset":           {},
	"switch_from":            {},
	"switch_to":              {},
	"parent_session_id":      {},
	"parent_message_id":      {},
	"click_position":         {},
	"menu_name":              {},
	"position":               {},
	"region":                 {},
	"action":                 {},
	"code_work_view_enabled": {},
	"is_enabled":             {},
	"plugin_name":            {},
	"plugin_type":            {},
	"is_builtin":             {},
	"input_value":            {},
	"duration":               {},
	"trigger_type":           {},
}

var (
	eventNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	windowsPath      = regexp.MustCompile(`[A-Za-z]:[\\/][^\s"'<>|]*`)
	uncPath          = regexp.MustCompile(`\\\\[^\s"'<>|]+`)
	posixPath        = regexp.MustCompile(`(^|[\s"'(=])(/[^\s"'<>|]+)`)
	urlPattern       = regexp.MustCompile(`(?i)\b(?:https?|ftp)://[^\s"'<>]+`)
	secretPatterns   = []*regexp.Regexp{
		regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{6,}\b`),
		regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
		regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]{8,}`),
		regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
		regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{8,}\b`),
	}
)

// sanitizeEvent 把一次上报变成可入库的 Record；任何非法形状都返回错误，
// 不静默写一条垃圾。
func sanitizeEvent(event Event, policyVersion int64, receivedAt time.Time) (Record, error) {
	name := strings.TrimSpace(event.EventName)
	if !eventNamePattern.MatchString(name) {
		return Record{}, fmt.Errorf("analytics event: eventName %q 不是合法事件名", event.EventName)
	}
	if math.IsNaN(event.Timestamp) || math.IsInf(event.Timestamp, 0) || event.Timestamp <= 0 {
		return Record{}, fmt.Errorf("analytics event: timestamp 必须是有限正数（毫秒）")
	}
	attrs, err := sanitizeAttributes(event.Attributes)
	if err != nil {
		return Record{}, err
	}
	return Record{
		EventName:     name,
		Timestamp:     event.Timestamp,
		Attributes:    attrs,
		PolicyVersion: policyVersion,
		ReceivedAt:    receivedAt.UTC().Format(time.RFC3339Nano),
		PrevHash:      "",
		Hash:          "",
	}, nil
}

func sanitizeAttributes(raw json.RawMessage) (map[string]any, error) {
	out := map[string]any{}
	if len(strings.TrimSpace(string(raw))) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return out, nil
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("analytics event: attributes 必须是 JSON 对象: %w", err)
	}
	for key, value := range values {
		if _, allowed := allowedAttributeKeys[key]; !allowed {
			// 静默丢弃：这不是"参数错"，而是隐私边界。事件本身仍然可入库，
			// 只是不允许把不允许的字段带进来。
			continue
		}
		var decoded any
		if err := json.Unmarshal(value, &decoded); err != nil {
			return nil, fmt.Errorf("analytics event: attribute %s 不是合法 JSON: %w", key, err)
		}
		switch typed := decoded.(type) {
		case string:
			sanitized := sanitizeString(typed)
			if sanitized != "" {
				out[key] = sanitized
			}
		case bool:
			out[key] = typed
		case float64:
			if math.IsNaN(typed) || math.IsInf(typed, 0) {
				return nil, fmt.Errorf("analytics event: attribute %s 是非法数字", key)
			}
			out[key] = typed
		case json.Number:
			out[key] = typed.String()
		default:
			// 嵌套对象/数组全部丢弃：官方所有属性都是标量；保留嵌套结构会
			// 给"正文/路径藏在子对象里"开口子。
		}
	}
	return out, nil
}

// sanitizeString 是最后的防线：路径、URL、密钥、控制字符全部处理掉。
func sanitizeString(value string) string {
	if value == "" {
		return ""
	}
	out := value
	out = windowsPath.ReplaceAllString(out, "<path>")
	out = uncPath.ReplaceAllString(out, "<path>")
	out = posixPath.ReplaceAllString(out, "${1}<path>")
	out = urlPattern.ReplaceAllString(out, "[url]")
	for _, pattern := range secretPatterns {
		out = pattern.ReplaceAllString(out, "[redacted]")
	}
	out = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, out)
	out = strings.Join(strings.Fields(out), " ")
	if len(out) > 256 {
		out = out[:256] + "…"
	}
	return out
}

// canonicalPayload 是 hash 链的规范化字节：字段排序由 json.Marshal 对 map 的
// 确定性排序保证；Hash 自身置空后参与计算。
func canonicalPayload(record Record) ([]byte, error) {
	record.Hash = ""
	return json.Marshal(record)
}

// hashRecord 计算 Hash = sha256(PrevHash + "\n" + canonical(record))。
func hashRecord(record Record) (string, error) {
	payload, err := canonicalPayload(record)
	if err != nil {
		return "", err
	}
	return sha256Hex([]byte(record.PrevHash + "\n" + string(payload))), nil
}

// ExportBatch 是 exporter POST 的载荷；导出前会再次脱敏并校验 policyVersion。
type ExportBatch struct {
	PolicyVersion int64    `json:"policyVersion"`
	Events        []Record `json:"events"`
}

// sanitizeExportBatch 是导出前的**第二次**脱敏与白名单复检。
//
// 即使本地文件被人手工塞进了一条带路径/密钥的记录，导出前也会被重新清洗；
// 这是"导出前二次脱敏"的可执行实现，不是注释。
func sanitizeExportBatch(batch ExportBatch) (ExportBatch, error) {
	if batch.PolicyVersion <= 0 {
		return ExportBatch{}, fmt.Errorf("analytics export: policyVersion 必须为正")
	}
	out := ExportBatch{PolicyVersion: batch.PolicyVersion, Events: make([]Record, 0, len(batch.Events))}
	for _, record := range batch.Events {
		rebuilt, err := sanitizeEvent(Event{
			EventName:  record.EventName,
			Timestamp:  record.Timestamp,
			Attributes: mustMarshal(record.Attributes),
		}, batch.PolicyVersion, time.Now())
		if err != nil {
			return ExportBatch{}, err
		}
		// 导出载荷不带本地链字段（hash/prevHash/receivedAt 是本地审计信息）。
		rebuilt.PrevHash = ""
		rebuilt.Hash = ""
		rebuilt.ReceivedAt = ""
		// 身份与环境维度是 Resource 级属性（官方同一口径），导出时必须跟着走；
		// 环境维度按自己的白名单再复检一次（不是重新信任本地文件）。
		rebuilt.AnonymousUserID = record.AnonymousUserID
		rebuilt.Environment = sanitizeEnvironment(record.Environment)
		rebuilt.MissingFields = record.MissingFields
		out.Events = append(out.Events, rebuilt)
	}
	return out, nil
}

func mustMarshal(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return raw
}

// SortedAttributeKeys 只是给测试/CLI 一个确定性顺序（不影响 hash）。
func SortedAttributeKeys(attributes map[string]any) []string {
	keys := make([]string, 0, len(attributes))
	for key := range attributes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
