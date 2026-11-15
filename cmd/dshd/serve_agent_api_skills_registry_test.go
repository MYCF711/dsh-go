package main

// `skills.list`（人类面目录）的**注册面**断言：
//
//   - 人类面只取 `userInvocable=true`（官方 skill-catalog.ts:78 的 `.filter(isUserInvocable)`）；
//   - 非文件系统 provider 经官方 `registerProvider` 接进来后，人类面能看到它的 skill；
//   - provider 失败 → 记日志 + 跳过 + 观察标 incomplete，其它来源的候选仍然在；
//   - 同名去重按 rank（官方 precedence），人类面与模型面看同一份合并结果。
//
// 全部用**真实目录**（t.TempDir + SKILL.md）与真实 API 入口，不断言布尔开关。

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"dshgo/modules/tool/skill"
)

// TestAgentOfficialSkillsHumanSurfacePolicy 四个 invocation 组合里，人类面只该看到
// `userInvocable=true` 的两个（{true,true} 与 {false,true}）。
//
// 另两个组合由模型面挑走——模型面那一半在
// modules/tool/skill/registry_test.go 的 TestInvocationPolicyFourCombinations 里用
// 同一批 frontmatter 键断言。
func TestAgentOfficialSkillsHumanSurfacePolicy(t *testing.T) {
	f := newAgentAPIFixture(t)
	// 显式给一个没有 .git 的 cwd：项目根就是这个目录本身，不会扫到仓库里的 skill。
	project := t.TempDir()
	sessionID := f.newSession(project, nil)
	root := filepath.Join(f.home, "skills")
	writeAgentSkillDir(t, root, "policy-both",
		"name: policy-both\ndescription: 两面都宣传\n", "BOTH\n")
	writeAgentSkillDir(t, root, "policy-model-only",
		"name: policy-model-only\ndescription: 只给模型\nuser-invocable: false\n", "MODEL-ONLY\n")
	writeAgentSkillDir(t, root, "policy-user-only",
		"name: policy-user-only\ndescription: 只给人类\ndisable-model-invocation: true\n", "USER-ONLY\n")
	writeAgentSkillDir(t, root, "policy-neither",
		"name: policy-neither\ndescription: 两面都不宣传\ndisable-model-invocation: true\nuser-invocable: false\n", "NEITHER\n")

	catalog, err := f.api.SkillsList(sessionID)
	if err != nil {
		t.Fatalf("skills.list: %v", err)
	}
	want := []string{"policy-both", "policy-user-only"}
	if got := skillNames(catalog); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("人类面清单 %v，期望 %v（{true,false} 与 {false,false} 不该出现）", got, want)
	}
	// 目录里 `modelInvocable` 仍如实反映模型面（user-only 的该字段为 false）。
	if skillEntryByName(t, catalog, "policy-user-only").ModelInvocable {
		t.Fatal("disable-model-invocation 的 skill 在人类面里 modelInvocable 必须是 false")
	}
	if !skillEntryByName(t, catalog, "policy-both").ModelInvocable {
		t.Fatal("{true,true} 的 skill 在人类面里 modelInvocable 必须是 true")
	}
}

// humanSurfaceProvider 是一个**非文件系统** provider，用来证明人类面接得上：
// 候选与正文都在内存里，不碰任何目录。
type humanSurfaceProvider struct {
	name       string
	candidates []skill.Candidate
	bodies     map[string]string
	listErr    error
	complete   bool
}

func (p *humanSurfaceProvider) Name() string { return p.name }

func (p *humanSurfaceProvider) List(_ context.Context, _ skill.LookupOptions) (skill.Observation, error) {
	if p.listErr != nil {
		return skill.Observation{}, p.listErr
	}
	return skill.Observation{Candidates: p.candidates, Complete: p.complete}, nil
}

func (p *humanSurfaceProvider) Get(_ context.Context, candidate skill.Candidate, _ skill.LookupOptions) (*skill.Definition, error) {
	body, ok := p.bodies[candidate.Name]
	if !ok {
		return nil, nil
	}
	return &skill.Definition{
		Summary: skill.Summary{
			Name: candidate.Name, Description: candidate.Description,
			Invocation: candidate.Invocation, Source: candidate.Source, Provider: candidate.Provider,
		},
		Content: body,
	}, nil
}

func humanCandidate(provider, name, description string, rank int, policy skill.InvocationPolicy) skill.Candidate {
	return skill.Candidate{
		Summary: skill.Summary{
			Name: name, Description: description,
			Invocation: policy, Source: "custom", Provider: provider,
		},
		Rank: rank, Locator: name,
	}
}

// TestAgentSkillCatalogMergesProviderSources 人类面目录**真的**走注册表：
// 追加一个非文件系统 provider 后，它的 skill 出现在 `skills.list` 里；
// 同名按 rank 去重（文件系统 rank 400 vs 追加来源 rank 100 → 追加来源赢）。
func TestAgentSkillCatalogMergesProviderSources(t *testing.T) {
	f := newAgentAPIFixture(t)
	project := t.TempDir()
	root := filepath.Join(f.home, "skills")
	writeAgentSkillDir(t, root, "fs-only",
		"name: fs-only\ndescription: 只在文件系统里\n", "FS\n")
	writeAgentSkillDir(t, root, "shared-skill",
		"name: shared-skill\ndescription: 文件系统版本\n", "FS-SHARED\n")

	provider := &humanSurfaceProvider{
		name:     "plugin-stub",
		complete: true,
		bodies: map[string]string{
			"virtual-skill": "VIRTUAL-BODY\n",
			"shared-skill":  "VIRTUAL-SHARED\n",
		},
		candidates: []skill.Candidate{
			humanCandidate("plugin-stub", "virtual-skill", "插件内嵌的 skill", 100, skill.BothInvocable()),
			// rank 100 < 文件系统的 400：同名时插件版本赢（官方 precedence）。
			humanCandidate("plugin-stub", "shared-skill", "插件版本", 100, skill.BothInvocable()),
			// 只给人类的组合也必须保留：人类面看得到。
			humanCandidate("plugin-stub", "virtual-user-only", "只给人类", 100, skill.UserOnlyInvocable()),
		},
	}

	skills, stats, err := f.core.agentSkillCatalogWith(context.Background(), project, true, provider)
	if err != nil {
		t.Fatalf("人类面合并失败：%v", err)
	}
	if stats.Incomplete {
		t.Fatalf("所有 provider 都成功，不该标 incomplete：%+v", stats)
	}
	names := make([]string, 0, len(skills))
	byName := map[string]string{}
	for _, entry := range skills {
		names = append(names, entry.Name)
		byName[entry.Name] = entry.Description
	}
	sort.Strings(names)
	want := []string{"fs-only", "shared-skill", "virtual-skill", "virtual-user-only"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("人类面清单 %v，期望 %v", names, want)
	}
	if byName["shared-skill"] != "插件版本" {
		t.Fatalf("同名应当按 rank 去重（插件 rank 100 赢文件系统 400），实际 description=%q", byName["shared-skill"])
	}
	if byName["virtual-skill"] != "插件内嵌的 skill" {
		t.Fatalf("非文件系统来源的 description 没带过来：%q", byName["virtual-skill"])
	}
}

// TestAgentSkillCatalogProviderFailureIsIncomplete provider 失败时：候选缺失、其它
// provider 的候选仍在、观察标 incomplete（stats.Incomplete），并有日志（stderr）。
func TestAgentSkillCatalogProviderFailureIsIncomplete(t *testing.T) {
	f := newAgentAPIFixture(t)
	project := t.TempDir()
	writeAgentSkillDir(t, filepath.Join(f.home, "skills"), "fs-only",
		"name: fs-only\ndescription: 文件系统的 skill\n", "FS\n")

	broken := &humanSurfaceProvider{name: "broken-stub", listErr: errors.New("远端 503")}
	healthy := &humanSurfaceProvider{
		name:     "healthy-stub",
		complete: true,
		bodies:   map[string]string{"healthy-skill": "HEALTHY\n"},
		candidates: []skill.Candidate{
			humanCandidate("healthy-stub", "healthy-skill", "健康来源的 skill", 300, skill.BothInvocable()),
		},
	}

	skills, stats, err := f.core.agentSkillCatalogWith(context.Background(), project, true, broken, healthy)
	if err != nil {
		t.Fatalf("单个 provider 失败不该让整份目录 500：%v", err)
	}
	if !stats.Incomplete {
		t.Fatal("有 provider 失败时 stats.Incomplete 必须是 true（与「确认没有」分开）")
	}
	names := make([]string, 0, len(skills))
	for _, entry := range skills {
		names = append(names, entry.Name)
	}
	sort.Strings(names)
	want := []string{"fs-only", "healthy-skill"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("失败 provider 之外的候选必须仍然在：%v，期望 %v", names, want)
	}

	// 反过来：没有 provider 失败时 Incomplete 必须是 false。
	skills, stats, err = f.core.agentSkillCatalogWith(context.Background(), project, true, healthy)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Incomplete {
		t.Fatal("provider 全成功时 Incomplete 必须是 false")
	}
	if len(skills) != 2 {
		t.Fatalf("清单 %v，期望 2 条", skills)
	}
}

// TestAgentSkillCatalogRejectsInvalidProviderData 非文件系统 provider 交回非法数据时
// 整个目录 fail-loud（官方 validateCandidate 抛错 → `skill listing failed: …`）。
func TestAgentSkillCatalogRejectsInvalidProviderData(t *testing.T) {
	f := newAgentAPIFixture(t)
	project := t.TempDir()
	provider := &humanSurfaceProvider{
		name:     "bad-stub",
		complete: true,
		bodies:   map[string]string{},
		candidates: []skill.Candidate{
			// 非 kebab-case 名字：官方 validateCandidate 直接抛错。
			humanCandidate("bad-stub", "Bad_Name", "非法名字", 100, skill.BothInvocable()),
		},
	}
	_, _, err := f.core.agentSkillCatalogWith(context.Background(), project, true, provider)
	if err == nil {
		t.Fatal("provider 交回非法候选必须响亮失败，不许静默丢掉")
	}
	if !strings.Contains(err.Error(), "kebab-case") {
		t.Fatalf("错误信息要指出非法原因：%v", err)
	}
	if !strings.Contains(err.Error(), "skill listing failed") {
		t.Fatalf("错误形状要沿用官方原文 `skill listing failed: …`：%v", err)
	}
}

// TestAgentSkillRootProviderLoadsBody 文件系统 provider 的 get 真的能载入正文
// （人类面目录不载正文，但 provider 契约要求它可用；这条把它钉住，避免空壳实现）。
func TestAgentSkillRootProviderLoadsBody(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "skills")
	path := writeAgentSkillDir(t, root, "loadable-skill",
		"name: loadable-skill\ndescription: 可载入\n", "LOADABLE-BODY\n")
	provider := &agentSkillRootProvider{
		roots: []agentSkillRoot{{Path: root, Source: agentSkillSourceUserDsh, Rank: agentSkillRankUserDsh}},
		stats: &agentSkillScanStats{},
	}
	observation, err := provider.List(context.Background(), skill.LookupOptions{})
	if err != nil {
		t.Fatalf("provider list: %v", err)
	}
	if !observation.Complete || len(observation.Candidates) != 1 {
		t.Fatalf("provider list 结果 %+v", observation)
	}
	definition, err := provider.Get(context.Background(), observation.Candidates[0], skill.LookupOptions{})
	if err != nil {
		t.Fatalf("provider get: %v", err)
	}
	if definition == nil || !strings.Contains(definition.Content, "LOADABLE-BODY") {
		t.Fatalf("正文没载入：%+v", definition)
	}
	if definition.Path != path {
		t.Fatalf("定义里的 path %q，期望 %q", definition.Path, path)
	}
	if definition.Invocation.ModelInvocable() != true || definition.Invocation.UserInvocable() != true {
		t.Fatalf("缺省策略应当是两面都可调用：%+v", definition.Invocation)
	}
}
