package session

import (
	"encoding/json"
	"errors"
	"testing"
)

// 官方 rc.2 把 `team/mode` 加进事件词汇表
// （`dsh-session/lib/types/known-event-types.js:68`，另有两份内嵌副本：
// `dsh-session/lib/index.js:126`、`dsh-session-persistence-jsonl/lib/worker.cjs:3232`，
// 声明见 `dsh-session-reference/lib/typert.host.js:573` 的 `'team/mode': { enabled: boolean; }`）。
//
// 宿主事件表必须同步：少了它，官方 rc.2 写下的 team 开关既校验不了也投影不出来。
func TestKnownEventTypesCarryTeamMode(t *testing.T) {
	if !IsOfficialEventType(EventTeamMode) {
		t.Fatalf("%s 必须是官方词汇表成员（rc.2 known-event-types.js:68）", EventTeamMode)
	}
	if !IsKnownEventType(EventTeamMode) {
		t.Fatalf("%s 必须是宿主已知事件", EventTeamMode)
	}
	// 抽样钉住官方词汇表：少一条 = 读官方日志时把已知类型误判成未知。
	for _, typ := range []string{
		"turn/start", "turn/end", "step/start", "step/end",
		"user/message", "assistant/message", "assistant/attempt",
		"tool/call", "tool/result",
		"subagent/catalog", "subagent/descriptor",
		"team/member", "team/task", "team/message/queued", "team/message/delivered",
		"working-directory/change", "session/title", "plan/mode",
		"model/selection", "sandbox/mode", "compaction/start", "compaction/end",
		"request/header", "request/context", "system/message",
	} {
		if !IsOfficialEventType(typ) {
			t.Fatalf("%s 应在官方词汇表里", typ)
		}
	}
	for _, typ := range []string{"team/unknown", "turn/unknown", "team/"} {
		if IsOfficialEventType(typ) {
			t.Fatalf("%s 不在官方词汇表里", typ)
		}
	}
}

// 宿主自有耐久事件（官方词汇表里没有）：必须仍然算"已知"，
// 否则读自己的日志会把自己的记录判成未知。
//
// `subagent/spawn`：`internal/agent/child_session.go:49,221`（子会话日志第一条事件）。
// `assistant/text`：宿主流式增量落盘（`internal/transport/handlers_session_official_test.go:726` 而是真实语料形状）。
func TestKnownEventTypesCarryHostDurableEvents(t *testing.T) {
	for _, typ := range []string{EventChildSpawn, "assistant/text"} {
		if IsOfficialEventType(typ) {
			t.Fatalf("%s 不是官方类型", typ)
		}
		if !IsKnownEventType(typ) {
			t.Fatalf("%s 是宿主耐久事件，必须算已知", typ)
		}
	}
}

// 未知类型**不得被静默吞掉**：非 ignorable 必须报错；
// ignorable 按官方语义保留未解释载荷（`dsh-session-format-v3-to-v4/lib/index.js:1022-1024`：
// "The owning admission stage rejects unknown required events; unknown ignorable records
// retain their uninterpreted payloads."）。
func TestAdmitEventDoesNotSwallowUnknownTypes(t *testing.T) {
	if err := AdmitEvent("shop/unknown-line", false); !errors.Is(err, ErrUnknownEventType) {
		t.Fatalf("未知且非 ignorable 必须报 ErrUnknownEventType，得到 %v", err)
	}
	if err := AdmitEvent("shop/unknown-line", true); err != nil {
		t.Fatalf("未知但 ignorable 应保留不解释（官方语义），不该报错，得到 %v", err)
	}
	for _, typ := range []string{EventTeamMode, "turn/start", EventChildSpawn} {
		if err := AdmitEvent(typ, false); err != nil {
			t.Fatalf("已知类型 %s 不该被拒：%v", typ, err)
		}
	}
}

// 官方 `teamModeSchema = z.object({ enabled: z.boolean() }).strict()`
// （`dsh-agent-team/lib/types/projection.js:52`）—— enabled 必填、必须是布尔、不得有多余键。
func TestValidateTeamModePayload(t *testing.T) {
	cases := []struct {
		name    string
		data    string
		wantErr bool
	}{
		{"enabled=true", `{"enabled":true}`, false},
		{"enabled=false", `{"enabled":false}`, false},
		{"缺 enabled", `{}`, true},
		{"enabled 是字符串", `{"enabled":"true"}`, true},
		{"enabled 是数字", `{"enabled":1}`, true},
		{"多余键（strict 拒绝）", `{"enabled":true,"teamId":"t1"}`, true},
		{"null", `null`, true},
		{"不是对象", `[]`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateKnownEventPayload(EventTeamMode, json.RawMessage(c.data))
			if c.wantErr && err == nil {
				t.Fatalf("载荷 %s 必须被拒", c.data)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("载荷 %s 必须通过，得到 %v", c.data, err)
			}
		})
	}
}

// ValidateKnownEventPayload 对 subagent/catalog 必须委派到版本校验（不然两处各判一套）。
func TestValidateKnownEventPayloadDelegatesCatalog(t *testing.T) {
	bad := json.RawMessage(`{"version":3,"childId":"c1","childCreatedAt":1,"mode":"continuable","name":"lead"}`)
	if err := ValidateKnownEventPayload(EventSubagentCatalog, bad); err == nil {
		t.Fatalf("保留名 lead 必须被拒")
	}
	good := json.RawMessage(`{"version":3,"childId":"c1","childCreatedAt":1,"mode":"continuable","label":"L","name":"worker-1"}`)
	if err := ValidateKnownEventPayload(EventSubagentCatalog, good); err != nil {
		t.Fatalf("合法 v3 载荷应通过，得到 %v", err)
	}
}

// team/mode 的写入端构造器与读取端解析必须同形（`{enabled:boolean}`）。
func TestTeamModeEventRoundTrip(t *testing.T) {
	raw, err := json.Marshal(TeamModeEvent(true))
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	if string(raw) != `{"enabled":true}` {
		t.Fatalf("team/mode 载荷 = %s，want {\"enabled\":true}", raw)
	}
	payload, err := ParseTeamModePayload(raw)
	if err != nil {
		t.Fatalf("解析自己写出的载荷失败: %v", err)
	}
	if !payload.Enabled {
		t.Fatalf("enabled 应为 true")
	}
	if _, err := ParseTeamModePayload(json.RawMessage(`{"enabled":false,"extra":1}`)); err == nil {
		t.Fatalf("strict 载荷多键必须被拒")
	}
}
