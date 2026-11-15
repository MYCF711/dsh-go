package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestStatusJSONMatchesOfficialUnion 逐条钉住官方
// `mcpManager/watch` 的 `status` oneOf 九个分支（契约：
// docs/evidence/official-method-contract-delta-0.2.2-rc.2.md 的
// `### mcpManager/watch`；源码：dsh-mcp-client/src/types.ts + dsh-mcp-manager/src/types.ts）。
//
// 每个分支的**必填字段**与**可省字段**都必须逐字节对上：多一个键会让官方
// 客户端把状态读成未知形状，少一个键会让它在该状态上取到 undefined。
func TestStatusJSONMatchesOfficialUnion(t *testing.T) {
	cases := []struct {
		name string
		st   Status
		want string
	}{
		{
			name: "closed 只带 closeConfirmed",
			st:   Status{State: StateClosed, Close: CloseResult{CloseConfirmed: true}},
			want: `{"state":"closed","result":{"closeConfirmed":true}}`,
		},
		{
			name: "closed 带 cleanupError",
			st:   Status{State: StateClosed, Close: CloseResult{CloseConfirmed: false, CleanupError: "kill 超时"}},
			want: `{"state":"closed","result":{"closeConfirmed":false,"cleanupError":"kill 超时"}}`,
		},
		{
			name: "waiting-credentials 必带 lastError",
			st:   Status{State: StateWaitingCredentials, LastError: "缺 DSH_MCP_TOKEN"},
			want: `{"state":"waiting-credentials","lastError":"缺 DSH_MCP_TOKEN"}`,
		},
		{
			name: "connecting 带 attempt",
			st:   Status{State: StateConnecting, Attempt: 3},
			want: `{"state":"connecting","attempt":3}`,
		},
		{
			name: "connected 带 toolCount/connectedAt",
			st:   Status{State: StateConnected, ToolCount: 7, ConnectedAt: 1700000000123},
			want: `{"state":"connected","toolCount":7,"connectedAt":1700000000123}`,
		},
		{
			name: "reconnecting 四字段齐全",
			st: Status{State: StateReconnecting, Attempt: 2, MaxAttempts: 10,
				NextRetryAt: 1700000009999, LastError: "EOF"},
			want: `{"state":"reconnecting","attempt":2,"maxAttempts":10,"nextRetryAtMs":1700000009999,"lastError":"EOF"}`,
		},
		{
			name: "failed/gave-up 的 lastError 可选",
			st:   Status{State: StateFailed, Reason: ReasonGaveUp},
			want: `{"state":"failed","reason":"gave-up"}`,
		},
		{
			name: "failed/gave-up 带 lastError",
			st:   Status{State: StateFailed, Reason: ReasonGaveUp, LastError: "10 次都失败"},
			want: `{"state":"failed","reason":"gave-up","lastError":"10 次都失败"}`,
		},
		{
			name: "failed/reconnect-disabled 没有 lastError 时不许补空串",
			st:   Status{State: StateFailed, Reason: ReasonReconnectDisabled},
			want: `{"state":"failed","reason":"reconnect-disabled"}`,
		},
		{
			name: "failed/unclosed-generation 带 result",
			st: Status{State: StateFailed, Reason: ReasonUnclosedGeneration,
				Close: CloseResult{CloseConfirmed: false, CleanupError: "stderr 未读完"}},
			want: `{"state":"failed","reason":"unclosed-generation","result":{"closeConfirmed":false,"cleanupError":"stderr 未读完"}}`,
		},
		{
			name: "failed/load 必带 lastError（空串也要出现）",
			st:   Status{State: StateFailed, Reason: ReasonLoad, LastError: "spawn ENOENT"},
			want: `{"state":"failed","reason":"load","lastError":"spawn ENOENT"}`,
		},
		{
			name: "failed/cleanup 必带 result",
			st: Status{State: StateFailed, Reason: ReasonCleanup,
				Close: CloseResult{CloseConfirmed: false}},
			want: `{"state":"failed","reason":"cleanup","result":{"closeConfirmed":false}}`,
		},
		{
			name: "disabled 只有 state",
			st:   Status{State: StateDisabled},
			want: `{"state":"disabled"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.st)
			if err != nil {
				t.Fatalf("Marshal 失败: %v", err)
			}
			if string(raw) != tc.want {
				t.Fatalf("JSON 形状不对\n got: %s\nwant: %s", raw, tc.want)
			}
		})
	}
}

// TestStatusRejectsInvalidCombinations 钉住"宁可不输出，也不输出非法状态"：
// 契约里不存在的组合必须响亮失败，不许悄悄编一个出来。
func TestStatusRejectsInvalidCombinations(t *testing.T) {
	cases := map[string]Status{
		"未知 state":         {State: "bogus"},
		"空 state":          {},
		"failed 没有 reason": {State: StateFailed},
		"failed 未知 reason": {State: StateFailed, Reason: "meltdown"},
	}
	for name, st := range cases {
		t.Run(name, func(t *testing.T) {
			if err := st.Validate(); err == nil {
				t.Fatalf("Validate 应该拒绝 %+v", st)
			}
			if _, err := json.Marshal(st); err == nil {
				t.Fatalf("Marshal 应该拒绝 %+v", st)
			}
		})
	}
}

// TestViewJSONShape 是 watch 数组元素的最小形状：name/configured/status 三个都必填。
func TestViewJSONShape(t *testing.T) {
	raw, err := json.Marshal([]View{{
		Name:       "files",
		Configured: true,
		Status:     Status{State: StateConnected, ToolCount: 2, ConnectedAt: 42},
	}})
	if err != nil {
		t.Fatalf("Marshal 失败: %v", err)
	}
	want := `[{"name":"files","configured":true,"status":{"state":"connected","toolCount":2,"connectedAt":42}}]`
	if string(raw) != want {
		t.Fatalf("View JSON 不对\n got: %s\nwant: %s", raw, want)
	}
}

// TestProbeResultJSONSuccessAndFailure 钉住官方 probe 的两支结果（success/failure）。
func TestProbeResultJSONSuccessAndFailure(t *testing.T) {
	ok := ProbeResult{
		OK:         true,
		ServerInfo: &ServerInfo{Name: "fixture", Version: "9.9.9"},
		ToolNames:  []string{"echo", "slow"},
		Truncated:  false,
		DurationMs: 12,
		Close:      CloseResult{CloseConfirmed: true},
	}
	raw, err := json.Marshal(ok)
	if err != nil {
		t.Fatalf("Marshal 失败: %v", err)
	}
	wantOK := `{"ok":true,"serverInfo":{"name":"fixture","version":"9.9.9"},"toolNames":["echo","slow"],"truncated":false,"durationMs":12,"closeConfirmed":true}`
	if string(raw) != wantOK {
		t.Fatalf("成功结果不对\n got: %s\nwant: %s", raw, wantOK)
	}

	// 没有 serverInfo 时不许出现 null：官方它是可选字段，缺省即省。
	ok.ServerInfo = nil
	ok.ToolNames = nil
	raw, err = json.Marshal(ok)
	if err != nil {
		t.Fatalf("Marshal 失败: %v", err)
	}
	wantOK = `{"ok":true,"toolNames":[],"truncated":false,"durationMs":12,"closeConfirmed":true}`
	if string(raw) != wantOK {
		t.Fatalf("无 serverInfo 的成功结果不对\n got: %s\nwant: %s", raw, wantOK)
	}

	bad := ProbeResult{
		OK:         false,
		Stage:      StageList,
		Message:    "tools/list 失败",
		DurationMs: 34,
		Close:      CloseResult{CloseConfirmed: false, CleanupError: "kill 超时"},
	}
	raw, err = json.Marshal(bad)
	if err != nil {
		t.Fatalf("Marshal 失败: %v", err)
	}
	wantBad := `{"ok":false,"stage":"list","message":"tools/list 失败","durationMs":34,"closeConfirmed":false,"cleanupError":"kill 超时"}`
	if string(raw) != wantBad {
		t.Fatalf("失败结果不对\n got: %s\nwant: %s", raw, wantBad)
	}

	// stage 只允许 connect/list；编一个第三值必须响亮失败。
	if _, err := json.Marshal(ProbeResult{OK: false, Stage: "handshake"}); err == nil {
		t.Fatal("非法 stage 必须让 Marshal 失败")
	}
	if _, err := json.Marshal(ProbeResult{OK: false, Stage: StageConnect, Message: ""}); err == nil {
		t.Fatal("失败结果没有 message 必须让 Marshal 失败")
	}
}

// TestProbeResultNeverEmitsNullToolNames 钉住"数组字段永远不是 null"：
// 客户端 `for (const n of toolNames)` 在 null 上直接抛。
func TestProbeResultNeverEmitsNullToolNames(t *testing.T) {
	raw, err := json.Marshal(ProbeResult{OK: true, ToolNames: []string{}, Close: CloseResult{CloseConfirmed: true}})
	if err != nil {
		t.Fatalf("Marshal 失败: %v", err)
	}
	if strings.Contains(string(raw), "null") {
		t.Fatalf("toolNames 不许是 null: %s", raw)
	}
}
