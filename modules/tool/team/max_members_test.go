package team

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// rc.1→rc.2：`agentTeam.maxMembers` 已被官方**删除**。
//
// 官方 rc.1 实验包（`dsh-experimental-agent-team/lib/index.js`）是名册制：
//
//	:1438  const DEFAULT_MAX_MEMBERS = 16;
//	:1442  maxMembers: z.number().step(1).min(1).default(DEFAULT_MAX_MEMBERS)
//	:1027  if (state.members.length >= this.maxMembers) throw TeamError(..., "TEAM_MEMBER_LIMIT")
//
// 官方 rc.2（`dsh-agent-team/src/index.ts:34-35,62-69`）把它进了
// `REMOVED_CONFIG_KEYS`，加载期直接抛 `TEAM_INVALID_CONFIG`：
// "teammates are subagents, so subagent limits and inbox delivery apply"。
// 配置面只剩 `maxTasks`（默认 256）与 `defaultEnabled`（默认 false）。
//
// Lead 裁决：**读取保留、标 deprecated、不再当闸门**。所以这里钉三件事：
//  1. 默认值仍能读出来（16），老配置看得懂；
//  2. **第 17 个成员不再被拒**（不再比官方少能力）；
//  3. 非法值仍然 fail-loud（写错一个数不该悄悄换一套数）。

func TestTeamMemberLimitDefaultIsReadable(t *testing.T) {
	got, err := teamMaxMembers(func(string) string { return "" })
	if err != nil {
		t.Fatalf("未设置时不该报错: %v", err)
	}
	if got != defaultMaxMembers {
		t.Fatalf("默认值 = %d，期望 %d（rc.1 官方默认，保留为只读诊断）", got, defaultMaxMembers)
	}
	if defaultMaxMembers != 16 {
		t.Fatalf("rc.1 官方默认 maxMembers = 16，当前常量 = %d", defaultMaxMembers)
	}
}

// rc.2：人数上限**不再是闸门**——第 17、18 个成员都能登记上。
func TestTeamMemberLimitIsNotAGateInRC2(t *testing.T) {
	dir := t.TempDir()
	tool := &spawnTool{dir: dir}
	ctx := context.Background()

	spawn := func(i int) error {
		args := fmt.Sprintf(`{"name":%q,"description":"第 %d 个队友","prompt":"干活"}`, fmt.Sprintf("member-%02d", i), i)
		res, err := tool.Exec(ctx, json.RawMessage(args))
		if err != nil {
			return fmt.Errorf("第 %d 个队友被拒（rc.2 已删除 maxMembers，不该有人数闸门）: %v / hint=%q", i, err, res.Hint)
		}
		return nil
	}
	for i := 1; i <= defaultMaxMembers+2; i++ {
		if err := spawn(i); err != nil {
			t.Fatal(err)
		}
	}

	// 旧上限仍然作为**诊断值**报出来（老配置的人看得见它，但不再拦人）。
	f, err := loadSpawnFile(filepath.Join(dir, spawnFileName))
	if err != nil {
		t.Fatalf("回读名册: %v", err)
	}
	if len(f.Agents) != defaultMaxMembers+2 {
		t.Fatalf("名册应有 %d 人，实际 %d 人", defaultMaxMembers+2, len(f.Agents))
	}
	res, err := tool.Exec(ctx, json.RawMessage(
		fmt.Sprintf(`{"name":"member-%02d","description":"再来一个","prompt":"干活"}`, defaultMaxMembers+3)))
	if err != nil {
		t.Fatalf("再登记一个: %v / %q", err, res.Hint)
	}
	data, _ := res.Data.(map[string]any)
	if data["max_members_deprecated"] != defaultMaxMembers {
		t.Fatalf("废弃上限应作为诊断值回给调用方，实际 %#v", res.Data)
	}
}

// 上限可配置（读），非法值 fail-loud 而不是静默退回默认。
func TestTeamMemberLimitConfig(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	if got, err := teamMaxMembers(env(nil)); err != nil || got != defaultMaxMembers {
		t.Fatalf("未设置 = 默认 %d，得到 (%d, %v)", defaultMaxMembers, got, err)
	}
	if got, err := teamMaxMembers(env(map[string]string{envMaxMembers: " 3 "})); err != nil || got != 3 {
		t.Fatalf("%s=3 应生效，得到 (%d, %v)", envMaxMembers, got, err)
	}
	for _, bad := range []string{"0", "-2", "many", "1.5"} {
		if _, err := teamMaxMembers(env(map[string]string{envMaxMembers: bad})); err == nil {
			t.Fatalf("%s=%q 是非法值，必须 fail-loud", envMaxMembers, bad)
		}
	}
}

// rc.2 的配置面：`agent-team` 显式拒绝三个旧键（官方原件逐条）。
func TestAgentTeamConfigRejectsRemovedKeys(t *testing.T) {
	for _, key := range []string{"maxMembers", "maxMessageBytes", "disposalTimeoutMs"} {
		_, err := ParseTeamConfig(map[string]any{key: 16})
		if err == nil {
			t.Fatalf("rc.2 必须拒绝旧配键 %q（TEAM_INVALID_CONFIG）", key)
		}
		cfgErr, ok := err.(*TeamConfigError)
		if !ok || cfgErr.Code != TeamCodeInvalidConfig {
			t.Fatalf("%q 的错误码应是 %s，实际 %v", key, TeamCodeInvalidConfig, err)
		}
		if !strings.Contains(cfgErr.Msg, "no longer supported") {
			t.Fatalf("错误信息要说清「不再支持」，实际 %q", cfgErr.Msg)
		}
	}
}

// `tool-agent-team` 也不接受配置，并拒绝三个旧键。
func TestToolAgentTeamConfigRejectsRemovedKeys(t *testing.T) {
	for _, key := range []string{"interrupt", "freshProvider", "forkProvider"} {
		if err := ParseToolAgentTeamConfig(map[string]any{key: true}); err == nil {
			t.Fatalf("rc.2 必须拒绝 tool-agent-team 旧配键 %q", key)
		}
	}
	if err := ParseToolAgentTeamConfig(map[string]any{}); err != nil {
		t.Fatalf("空配置应当合法: %v", err)
	}
}
