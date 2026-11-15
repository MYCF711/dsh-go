package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// v4 事件词汇表：官方 rc.2 `KNOWN_SESSION_EVENT_TYPES`
// （`dsh-session/lib/types/known-event-types.js:21-85`，同一份表还有两个内嵌副本：
// `dsh-session/lib/index.js:126`、`dsh-session-persistence-jsonl/lib/worker.cjs:3232`）
// 加上宿主自有事件。
//
// 为什么要有这张表：官方读取路径的规则是"**已知类型才解释**；不在表里的事件，
// 只有带 `ignorable: true` 才允许保留不解释，否则整份日志拒绝载入"——
// 理由写在该文件 :7-19：静默跳过一条 required 事件会重建出一个**错误**的会话。
// 宿主此前没有任何类型表，未知类型在投影/翻译的 switch 里被静默吞掉，
// 正是官方要防的那种情况。
const (
	// EventTeamMode 是 rc.2 新增的 `team/mode`（`{enabled:boolean}`）。
	EventTeamMode = "team/mode"
	// EventSubagentCatalog 是 `subagent/catalog`（v0/1/2 历史版本 + rc.2 的 v3）。
	EventSubagentCatalog = "subagent/catalog"
	// EventChildSpawn 是宿主自有：子会话日志的第一条 durable 事件
	// （`internal/agent/child_session.go:49,221`）。官方用 `subagent/descriptor`
	// 表达同一件事，因此它不在官方词汇表里。
	EventChildSpawn = "subagent/spawn"
	// EventAssistantText 是宿主自有的流式增量落盘事件；官方把增量放在
	// `assistant/message.stream` 里，没有独立类型。
	EventAssistantText = "assistant/text"
)

// ErrUnknownEventType 表示事件类型既不在官方词汇表、也不在宿主自有表里。
//
// 调用方**必须**返回它（或据此报错），不得静默跳过：跳过一条 required 事件
// 会重建出一个错的会话，而错的会话比打不开更难发现。
var ErrUnknownEventType = errors.New("未知的 v4 事件类型")

// officialEventTypes 是官方 rc.2 词汇表的逐条副本（63 条）。
//
// 它必须与官方文件**逐条一致**：删一条 = 读官方日志时把已知类型判成未知；
// 加一条 = 把官方不认识的类型当已知（会解释出错的形状）。
var officialEventTypes = map[string]struct{}{
	"agent-preset/selected":                  {},
	"agent/inbox/spliced":                    {},
	"approval/asked":                         {},
	"approval/decided":                       {},
	"approval/policy":                        {},
	"assistant/attempt":                      {},
	"assistant/message":                      {},
	"command/done":                           {},
	"command/run":                            {},
	"compaction/end":                         {},
	"compaction/prune":                       {},
	"compaction/start":                       {},
	"compaction/summary":                     {},
	"deliverables/presented":                 {},
	"developer/message":                      {},
	"feedback/message-delete":                {},
	"feedback/message-put":                   {},
	"feedback/record":                        {},
	"goal/change":                            {},
	"hook/invoked":                           {},
	"hook/result":                            {},
	"image/offload":                          {},
	"job/registered":                         {},
	"job/settled":                            {},
	"llm/retry":                              {},
	"llm/retry-started":                      {},
	"model/selection":                        {},
	"permission/preset":                      {},
	"plan/mode":                              {},
	"request/context":                        {},
	"request/header":                         {},
	"sandbox/mode":                           {},
	"schedule/change":                        {},
	"session-log-deepseek/delivery-accepted": {},
	"session/end-seed":                       {},
	"session/title":                          {},
	"session/title-llm-request":              {},
	"step/end":                               {},
	"step/start":                             {},
	"subagent/catalog":                       {},
	"subagent/descriptor":                    {},
	"subagent/model-selection-policy":        {},
	"system/message":                         {},
	"team/member":                            {},
	"team/message/delivered":                 {},
	"team/message/queued":                    {},
	"team/mode":                              {},
	"team/task":                              {},
	"todo/write":                             {},
	"tool-workflow/agent-end":                {},
	"tool-workflow/agent-start":              {},
	"tool-workflow/run-end":                  {},
	"tool-workflow/run-start":                {},
	"tool/call":                              {},
	"tool/ptc-dispatch":                      {},
	"tool/ptc-dispatch-start":                {},
	"tool/result":                            {},
	"turn/end":                               {},
	"turn/start":                             {},
	"user/message":                           {},
	"web/deepseek-search-llm-request":        {},
	"working-directory/change":               {},
	"workspace/changes":                      {},
}

// hostEventTypes 是宿主自有、官方词汇表里没有的**耐久**事件。
//
// 加条目要有真实写入点：这张表是"我们确实会写它，所以读自己的日志时不许报未知"，
// 不是"顺手把不认识的都收进来"。journal（WS）层面的类型不在这张表里——
// 那是 `internal/transport` 的 journal 白名单，与 v4 词汇表是两件事。
var hostEventTypes = map[string]struct{}{
	EventChildSpawn:    {},
	EventAssistantText: {},
}

// IsOfficialEventType 判断类型是否在官方 rc.2 词汇表里。
func IsOfficialEventType(typ string) bool {
	_, ok := officialEventTypes[typ]
	return ok
}

// IsKnownEventType 判断宿主是否认识这个类型（官方词汇表 ∪ 宿主自有）。
func IsKnownEventType(typ string) bool {
	if IsOfficialEventType(typ) {
		return true
	}
	_, ok := hostEventTypes[typ]
	return ok
}

// OfficialEventTypes 返回官方词汇表的排序副本（取证/比对用）。
func OfficialEventTypes() []string {
	out := make([]string, 0, len(officialEventTypes))
	for typ := range officialEventTypes {
		out = append(out, typ)
	}
	sort.Strings(out)
	return out
}

// KnownEventTypes 返回宿主已知事件（官方 ∪ 宿主自有）的排序副本。
func KnownEventTypes() []string {
	seen := make(map[string]struct{}, len(officialEventTypes)+len(hostEventTypes))
	out := make([]string, 0, len(officialEventTypes)+len(hostEventTypes))
	for typ := range officialEventTypes {
		seen[typ] = struct{}{}
		out = append(out, typ)
	}
	for typ := range hostEventTypes {
		if _, dup := seen[typ]; dup {
			continue
		}
		out = append(out, typ)
	}
	sort.Strings(out)
	return out
}

// AdmitEvent 是读取路径的**准入**判定（官方 `assertReleasedV4Relationships` 前置条件）。
//
//   - 已知类型 → nil；
//   - 未知 + `ignorable: true` → nil（官方语义：保留未解释载荷，兼容更新版本写下的日志）；
//   - 未知 + 非 ignorable → ErrUnknownEventType（**不得吞错**）。
func AdmitEvent(typ string, ignorable bool) error {
	if IsKnownEventType(typ) {
		return nil
	}
	if ignorable {
		return nil
	}
	return fmt.Errorf("%w: %q（既不在官方 rc.2 词汇表，也不是宿主自有事件；"+
		"若这条事件来自更新版本的 harness 且可以不解释，信封上必须带 ignorable:true）", ErrUnknownEventType, typ)
}

// TeamModePayload 是 `team/mode` 的载荷
// （`dsh-agent-team/lib/types/projection.js:52`：`z.object({ enabled: z.boolean() }).strict()`）。
type TeamModePayload struct {
	Enabled bool `json:"enabled"`
}

// TeamModeEvent 构造 `team/mode` 的载荷（写入端唯一构造点）。
func TeamModeEvent(enabled bool) map[string]any {
	return map[string]any{"enabled": enabled}
}

// ParseTeamModePayload 严格解析 `team/mode`：`enabled` 必填、必须是布尔、不得有多余键。
func ParseTeamModePayload(data json.RawMessage) (TeamModePayload, error) {
	var payload TeamModePayload
	if err := validateTeamModePayload(data); err != nil {
		return TeamModePayload{}, err
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return TeamModePayload{}, fmt.Errorf("team/mode 载荷解析失败: %w", err)
	}
	return payload, nil
}

func validateTeamModePayload(data json.RawMessage) error {
	raw := bytes.TrimSpace(data)
	if len(raw) == 0 || raw[0] != '{' {
		return fmt.Errorf("team/mode 载荷必须是对象（收到 %.40s）", raw)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("team/mode 载荷不是合法 JSON 对象: %w", err)
	}
	extra := make([]string, 0, len(fields))
	for key := range fields {
		if key != "enabled" {
			extra = append(extra, key)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		return fmt.Errorf("team/mode 载荷有多余键 %v（官方 schema 是 strict）", extra)
	}
	enabled, ok := fields["enabled"]
	if !ok {
		return fmt.Errorf("team/mode 载荷缺少 enabled")
	}
	var value bool
	if err := json.Unmarshal(enabled, &value); err != nil {
		return fmt.Errorf("team/mode.enabled 必须是布尔（收到 %s）", enabled)
	}
	return nil
}

// ValidateKnownEventPayload 对"宿主有形状契约"的类型做载荷校验。
//
// 目前两类：`team/mode`（rc.2 新增）与 `subagent/catalog`（v0/1/2/3 版本化事实）。
// 其余类型返回 nil——不是"它们没形状"，而是宿主还没有逐字段契约；
// 假校验（只看是不是对象）比不校验更危险。
func ValidateKnownEventPayload(typ string, data json.RawMessage) error {
	if err := AdmitEvent(typ, false); err != nil {
		return err
	}
	switch typ {
	case EventTeamMode:
		return validateTeamModePayload(data)
	case EventSubagentCatalog:
		_, err := ValidateSubagentCatalogPayload(data)
		return err
	default:
		return nil
	}
}
