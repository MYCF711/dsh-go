package team

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/spi"
)

// GO.7 / t11 #23：官方 `agentTeam.maxMembers`（默认 16）——团队名册的人数上限。
//
// 官方原件（app.asar:dsh/node_modules/@deepseek-ai/dsh-experimental-agent-team/lib/index.js）：
//
//	:1438  const DEFAULT_MAX_MEMBERS = 16;
//	:1442  maxMembers: z.number().step(1).min(1).default(DEFAULT_MAX_MEMBERS)
//	:1457  maxMembers: positiveLimit("maxMembers", config.maxMembers ?? DEFAULT_MAX_MEMBERS)
//	:1026  if (state.members.some((m) => m.name === name))
//	         throw new TeamError(`teammate name "${name}" was already used in this Team`, "TEAM_MEMBER_NAME_TAKEN");
//	:1027  if (state.members.length >= this.maxMembers)
//	         throw new TeamError(`Team member limit ${this.maxMembers} reached`, "TEAM_MEMBER_LIMIT");
//
// 宿主的"名册"就是 spawn_teammate 写的 <home>/state/team/agents.json（具名队友 +
// description + context + created_at）。名册本来就有，缺的是**人数上限**：以前可以
// 无限登记，团队拓扑与消息扇出无界。下面三条先红后绿。

// 默认值必须与官方一致：16（不是 8、不是 64）。
func TestTeamMemberLimitDefaultIs16(t *testing.T) {
	got, err := teamMaxMembers(func(string) string { return "" })
	if err != nil {
		t.Fatalf("未设置时不该报错: %v", err)
	}
	if got != defaultMaxMembers {
		t.Fatalf("默认人数上限 = %d，期望官方默认 %d", got, defaultMaxMembers)
	}
	if defaultMaxMembers != 16 {
		t.Fatalf("官方默认 maxMembers = 16，当前常量 = %d", defaultMaxMembers)
	}
}

// 第 17 个成员被拒：拒绝要有结构化 Hint（点名上限与怎么办），且**不落盘**。
func TestTeamMemberLimitRejects17thMember(t *testing.T) {
	dir := t.TempDir()
	tool := &spawnTool{dir: dir}
	ctx := context.Background()

	spawn := func(i int) (spi.Result, error) {
		args := fmt.Sprintf(`{"name":%q,"description":"第 %d 个队友","prompt":"干活"}`, fmt.Sprintf("member-%02d", i), i)
		return tool.Exec(ctx, json.RawMessage(args))
	}
	for i := 1; i <= defaultMaxMembers; i++ {
		if _, err := spawn(i); err != nil {
			t.Fatalf("第 %d 个队友应当登记得上: %v", i, err)
		}
	}

	res, err := spawn(defaultMaxMembers + 1)
	if err == nil {
		t.Fatalf("第 %d 个队友必须被拒，实际登记成功了", defaultMaxMembers+1)
	}
	if !strings.Contains(res.Hint, "16") {
		t.Fatalf("Hint 要点名上限 16，实际 %q", res.Hint)
	}
	if !strings.Contains(res.Hint, "spawn_teammate") && !strings.Contains(res.Hint, "队友") {
		t.Fatalf("Hint 要指向名册工具（怎么腾位置），实际 %q", res.Hint)
	}

	// 拒绝必须是"没写进去"：名册里仍只有 16 个。
	f, err := loadSpawnFile(filepath.Join(dir, spawnFileName))
	if err != nil {
		t.Fatalf("回读名册: %v", err)
	}
	if len(f.Agents) != defaultMaxMembers {
		t.Fatalf("被拒后名册应是 %d 人，实际 %d 人", defaultMaxMembers, len(f.Agents))
	}
}

// 上限可配置（官方 maxMembers 是配置项），非法值 fail-loud 而不是静默退回默认。
func TestTeamMemberLimitConfig(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	if got, err := teamMaxMembers(env(nil)); err != nil || got != defaultMaxMembers {
		t.Fatalf("未设置 = 官方默认 %d，得到 (%d, %v)", defaultMaxMembers, got, err)
	}
	if got, err := teamMaxMembers(env(map[string]string{envMaxMembers: " 3 "})); err != nil || got != 3 {
		t.Fatalf("%s=3 应生效，得到 (%d, %v)", envMaxMembers, got, err)
	}
	for _, bad := range []string{"0", "-2", "many", "1.5"} {
		if _, err := teamMaxMembers(env(map[string]string{envMaxMembers: bad})); err == nil {
			t.Fatalf("%s=%q 是非法上限，必须 fail-loud", envMaxMembers, bad)
		}
	}

	// 小上限也要真的生效（不是只认 16 这一个数）。
	dir := t.TempDir()
	small := &spawnTool{dir: dir, maxMembers: 1}
	ctx := context.Background()
	if _, err := small.Exec(ctx, json.RawMessage(`{"name":"only-one","description":"唯一队友","prompt":"干活"}`)); err != nil {
		t.Fatalf("上限 1 时第一个应成功: %v", err)
	}
	if _, err := small.Exec(ctx, json.RawMessage(`{"name":"second","description":"应被拒","prompt":"干活"}`)); err == nil {
		t.Fatal("上限 1 时第二个必须被拒")
	}
}
