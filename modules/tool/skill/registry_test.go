package skill

// `skill` 的官方**服务面**断言：provider 注册面（list/get/invalidate）、
// invocation 策略的四个组合、同层合并语义、incomplete 观察口径。
//
// 每条都对着官方契约的一处（出处见 registry.go / filesystem.go 顶部）：
//
//   - registerProvider / register（`dsh-skill/src/index.ts:430-494`）
//   - 四个调用组合全部保留（`dsh-skill/README.md` 策略表）
//   - provider 失败 → 日志 + 跳过 + incomplete（README "Observable success and failures"）
//   - 同名 first-wins：(rank → provider 顺序 → provider 内顺序)（:945-951）
//   - 载入回来的 name 与候选不一致 = 过期，让缓存失效（:552-558）
//
// 关键：这里用的是**真实目录**（t.TempDir + SKILL.md）与**真实工具实例**，
// 不断言布尔开关——断言的是"模型面到底看到了哪几个名字"。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// stubProvider 是一个**非文件系统** provider：候选与正文都在内存里。
//
// 它的存在就是为了证明"非文件系统来源能接进 skill 工具"——不是 mock 住工具内部，
// 而是实现官方 Provider 接口后从注册面注册进来，和文件系统 provider 一起合并。
type stubProvider struct {
	name string

	// control 由 RegisterProvider 注入；invalidate() 失效目录缓存。
	control ProviderControl

	mu         sync.Mutex
	candidates []Candidate
	bodies     map[string]string
	// definitionName 非空时 get 返回这个改名后的定义（证明"过期选择"检测）。
	definitionName string
	listErr        error
	complete       bool
	listCalls      int
}

func (p *stubProvider) Name() string { return p.name }

func (p *stubProvider) List(_ context.Context, _ LookupOptions) (Observation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.listCalls++
	if p.listErr != nil {
		return Observation{}, p.listErr
	}
	return Observation{Candidates: append([]Candidate(nil), p.candidates...), Complete: p.complete}, nil
}

func (p *stubProvider) Get(_ context.Context, candidate Candidate, _ LookupOptions) (*Definition, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	body, ok := p.bodies[candidate.Name]
	if !ok {
		return nil, nil
	}
	name := candidate.Name
	if p.definitionName != "" {
		name = p.definitionName
	}
	return &Definition{
		Summary: Summary{
			Name: name, Description: candidate.Description,
			Invocation: candidate.Invocation, Source: candidate.Source,
			Provider: candidate.Provider,
		},
		Content: body,
	}, nil
}

// stubCandidate 造一条 provider 候选（provider 归属自动对齐，rank 由调用方给）。
func stubCandidate(provider, name, description string, rank int, policy InvocationPolicy) Candidate {
	return Candidate{
		Summary: Summary{
			Name: name, Description: description,
			Invocation: policy, Source: "custom", Provider: provider,
		},
		Rank:    rank,
		Locator: name,
	}
}

// registerStub 把 stubProvider 经官方注册面接进工具。
func registerStub(t *testing.T, tool *Tool, stub *stubProvider) *Registry {
	t.Helper()
	registry, err := tool.Registry()
	if err != nil {
		t.Fatalf("取注册表：%v", err)
	}
	if _, err := registry.RegisterProvider(func(control ProviderControl) (Provider, error) {
		stub.control = control
		return stub, nil
	}); err != nil {
		t.Fatalf("注册 provider %q：%v", stub.name, err)
	}
	return registry
}

// execJSON 跑一次 skill 工具。
func execJSON(t *testing.T, tool *Tool, args string) (text string, data map[string]any, hint string, err error) {
	t.Helper()
	res, err := tool.Exec(context.Background(), json.RawMessage(args))
	if res.Data != nil {
		if typed, ok := res.Data.(map[string]any); ok {
			data = typed
		}
	}
	return res.Text, data, res.Hint, err
}

// TestProviderRegistrationFeedsSkillTool 证明"非文件系统 provider 真接上了"：
// 注册后模型面的 list 能看到它的两个 skill，Exec 能取到它的正文；
// provider 调 invalidate() 之后目录**立刻**刷新（不必等 TTL）。
func TestProviderRegistrationFeedsSkillTool(t *testing.T) {
	// TTL 拉得很长：刷新只能来自 invalidate() 的 revision 变化，不是 TTL 到期。
	t.Setenv("DSHD_SKILLS_CACHE_TTL_MS", "600000")
	t.Setenv("DSHD_SKILLS_EXTRA", "")
	dir := t.TempDir()
	writeSkill(t, dir, "local-skill", "# 本地 skill\n")

	tool := New(dir)
	stub := &stubProvider{
		name:     "remote-stub",
		complete: true,
		bodies: map[string]string{
			"remote-alpha": "REMOTE-ALPHA-BODY\n",
			"remote-beta":  "REMOTE-BETA-BODY\n",
		},
	}
	stub.candidates = []Candidate{
		stubCandidate("remote-stub", "remote-alpha", "远端来源的第一个", 300, BothInvocable()),
		stubCandidate("remote-stub", "remote-beta", "远端来源的第二个", 300, BothInvocable()),
	}
	registerStub(t, tool, stub)

	text, data, _, err := execJSON(t, tool, `{}`)
	if err != nil {
		t.Fatalf("列清单失败：%v（hint=%s）", err, data)
	}
	for _, want := range []string{"remote-alpha", "remote-beta", "local-skill"} {
		if !strings.Contains(text, want) {
			t.Fatalf("模型面清单缺 %q：\n%s", want, text)
		}
	}
	if count, _ := data["count"].(int); count != 3 {
		t.Fatalf("清单 count=%v，期望 3（两个远端 + 一个本地）", data["count"])
	}
	if incomplete, _ := data["incomplete"].(bool); incomplete {
		t.Fatal("所有 provider 都成功，incomplete 必须是 false")
	}

	// Exec 能加载**非文件系统** provider 的正文。
	body, _, _, err := execJSON(t, tool, `{"name":"remote-alpha"}`)
	if err != nil {
		t.Fatalf("加载 remote-alpha 失败：%v", err)
	}
	if !strings.Contains(body, "REMOTE-ALPHA-BODY") {
		t.Fatalf("正文没取到：\n%s", body)
	}
	// 非文件系统 skill 没有目录基准：官方渲染成 "managed by provider"。
	if !strings.Contains(body, `Resources for this skill are managed by provider "remote-stub".`) {
		t.Fatalf("没有 ResourceBase 时应渲染 provider 托管文案：\n%s", body)
	}

	// invalidate() → 目录立刻刷新（revision 变化让缓存失效）。
	stub.mu.Lock()
	stub.candidates = append(stub.candidates,
		stubCandidate("remote-stub", "remote-gamma", "invalidate 之后才有的", 300, BothInvocable()))
	stub.bodies["remote-gamma"] = "REMOTE-GAMMA-BODY\n"
	stub.mu.Unlock()
	before := stub.listCalls
	stub.control.Invalidate()

	text, _, _, err = execJSON(t, tool, `{}`)
	if err != nil {
		t.Fatalf("invalidate 之后列清单失败：%v", err)
	}
	if !strings.Contains(text, "remote-gamma") {
		t.Fatalf("invalidate() 之后目录没刷新（TTL 是 600s，所以只能是 revision 没生效）：\n%s", text)
	}
	if stub.listCalls == before {
		t.Fatal("invalidate() 之后没有重新收集：provider 的 list() 一次都没再被调用")
	}
}

// TestRuntimeRegistrationFirstWinsWithProvider 同一名字同时由 runtime register
// 与 provider 提供：按官方 rank（runtime=250）runtime 赢，provider 那条被遮掉，
// **日志与计数都要可观测**（不许静默）。
func TestRuntimeRegistrationFirstWinsWithProvider(t *testing.T) {
	t.Setenv("DSHD_SKILLS_CACHE_TTL_MS", "600000")
	t.Setenv("DSHD_SKILLS_EXTRA", "")
	dir := t.TempDir()

	tool := New(dir)
	registry, err := tool.Registry()
	if err != nil {
		t.Fatal(err)
	}
	var warnings []string
	var warnMu sync.Mutex
	registry.SetWarningSink(func(msg string) {
		warnMu.Lock()
		warnings = append(warnings, msg)
		warnMu.Unlock()
	})

	stub := &stubProvider{
		name: "embedded-stub", complete: true,
		bodies: map[string]string{
			"dup":      "PROVIDER-BODY\n",
			"only-rem": "ONLY-REMOTE\n",
		},
		candidates: []Candidate{
			// rank 600 > runtime 的 250：同层去重时 runtime 先赢。
			stubCandidate("embedded-stub", "dup", "provider 版本", 600, BothInvocable()),
			stubCandidate("embedded-stub", "only-rem", "只有 provider 有", 600, BothInvocable()),
		},
	}
	registerStub(t, tool, stub)

	// runtime 内嵌 skill：省略 invocation = 官方默认 {modelInvocable:true,userInvocable:true}。
	dispose, err := registry.Register(Registration{
		Name: "dup", Description: "runtime 版本", Source: "runtime", Content: "RUNTIME-BODY\n",
	})
	if err != nil {
		t.Fatalf("runtime 注册失败：%v", err)
	}
	defer dispose()

	text, _, _, err := execJSON(t, tool, `{}`)
	if err != nil {
		t.Fatalf("列清单失败：%v", err)
	}
	if !strings.Contains(text, "dup — runtime 版本") {
		t.Fatalf("同名时 runtime（rank 250）应当赢 provider（rank 600）：\n%s", text)
	}
	if strings.Contains(text, "provider 版本") {
		t.Fatalf("被遮掉的 provider 候选不该出现在目录里：\n%s", text)
	}
	if stats := registry.Stats(); stats.ShadowedCandidates != 1 {
		t.Fatalf("被遮掉的候选数 = %d，期望 1（要可计数，不许静默）", stats.ShadowedCandidates)
	}
	warnMu.Lock()
	joined := strings.Join(warnings, "\n")
	warnMu.Unlock()
	if !strings.Contains(joined, "dup") || !strings.Contains(joined, "被更高优先级") {
		t.Fatalf("first-wins 必须有日志：%q", joined)
	}

	// 加载走赢家（runtime 的正文）。
	body, _, _, err := execJSON(t, tool, `{"name":"dup"}`)
	if err != nil {
		t.Fatalf("加载 dup 失败：%v", err)
	}
	if !strings.Contains(body, "RUNTIME-BODY") || strings.Contains(body, "PROVIDER-BODY") {
		t.Fatalf("同名应当载入赢家（runtime）的正文：\n%s", body)
	}

	// 同名 runtime 再注册一次：官方是 first-wins + warning + no-op disposer。
	secondDispose, err := registry.Register(Registration{
		Name: "dup", Description: "runtime 第二个", Source: "runtime", Content: "SECOND-BODY\n",
	})
	if err != nil {
		t.Fatalf("同名 runtime 注册官方是告警不是错误：%v", err)
	}
	if stats := registry.Stats(); stats.DuplicateRuntimeRegistrations != 1 {
		t.Fatalf("重复 runtime 注册计数 = %d，期望 1", stats.DuplicateRuntimeRegistrations)
	}
	secondDispose() // no-op：不能把赢家删掉
	text, _, _, err = execJSON(t, tool, `{}`)
	if err != nil {
		t.Fatalf("重复注册后列清单失败：%v", err)
	}
	if !strings.Contains(text, "dup — runtime 版本") {
		t.Fatalf("第二个同名注册的 no-op disposer 删掉了赢家：\n%s", text)
	}
}

// TestInvocationPolicyFourCombinations 官方四个组合全部保留，模型面只选
// `modelInvocable=true`：真实目录里造 4 个 skill，模型面只该看到 2 个。
//
// （人类面的另一半在 cmd/dshd/serve_agent_api_skills_test.go 的
// TestAgentOfficialSkillsHumanSurfacePolicy 里断言——两个面看同一批真实目录。）
func TestInvocationPolicyFourCombinations(t *testing.T) {
	t.Setenv("DSHD_SKILLS_CACHE_TTL_MS", "0")
	t.Setenv("DSHD_SKILLS_EXTRA", "")
	dir := t.TempDir()
	// {true,true}：两面都宣传
	writeSkill(t, dir, "policy-both", "---\nname: policy-both\ndescription: 两面都宣传\n---\nBOTH\n")
	// {true,false}：只给模型
	writeSkill(t, dir, "policy-model-only",
		"---\nname: policy-model-only\ndescription: 只给模型\nuser-invocable: false\n---\nMODEL-ONLY\n")
	// {false,true}：只给人类
	writeSkill(t, dir, "policy-user-only",
		"---\nname: policy-user-only\ndescription: 只给人类\ndisable-model-invocation: true\n---\nUSER-ONLY\n")
	// {false,false}：两面都不宣传
	writeSkill(t, dir, "policy-neither",
		"---\nname: policy-neither\ndescription: 两面都不宣传\ndisable-model-invocation: true\nuser-invocable: false\n---\nNEITHER\n")

	tool := New(dir)
	text, _, hint, err := execJSON(t, tool, `{}`)
	if err != nil {
		t.Fatalf("列清单失败：%v（hint=%s）", err, hint)
	}
	if !strings.Contains(text, "policy-both") || !strings.Contains(text, "policy-model-only") {
		t.Fatalf("模型面应当看到 {true,true} 与 {true,false}：\n%s", text)
	}
	if strings.Contains(text, "policy-user-only") || strings.Contains(text, "policy-neither") {
		t.Fatalf("模型面不该看到 modelInvocable=false 的两个：\n%s", text)
	}

	// 模型面也不许**加载** modelInvocable=false 的 skill（官方 tool-skill:142-150）。
	if _, _, loadHint, err := execJSON(t, tool, `{"name":"policy-user-only"}`); err == nil {
		t.Fatalf("加载 user-only skill 必须失败（hint=%s）", loadHint)
	}
	if _, _, _, err := execJSON(t, tool, `{"name":"policy-model-only"}`); err != nil {
		t.Fatalf("加载 model-only skill 不该失败：%v", err)
	}
}

// TestProviderListFailureMarksIncomplete 官方口径：provider 失败 → 记日志 + 跳过，
// 其它 provider 的候选仍然在，观察标为 incomplete（与"确认没有"分开）。
func TestProviderListFailureMarksIncomplete(t *testing.T) {
	t.Setenv("DSHD_SKILLS_CACHE_TTL_MS", "0")
	t.Setenv("DSHD_SKILLS_EXTRA", "")
	dir := t.TempDir()
	writeSkill(t, dir, "local-skill", "# 本地\n")

	tool := New(dir)
	registry, err := tool.Registry()
	if err != nil {
		t.Fatal(err)
	}
	var warnings []string
	var warnMu sync.Mutex
	registry.SetWarningSink(func(msg string) {
		warnMu.Lock()
		warnings = append(warnings, msg)
		warnMu.Unlock()
	})

	broken := &stubProvider{name: "broken-stub", listErr: fmt.Errorf("远端 503")}
	healthy := &stubProvider{
		name: "healthy-stub", complete: true,
		bodies: map[string]string{"healthy-skill": "HEALTHY-BODY\n"},
		candidates: []Candidate{
			stubCandidate("healthy-stub", "healthy-skill", "健康 provider 的 skill", 400, BothInvocable()),
		},
	}
	registerStub(t, tool, broken)
	registerStub(t, tool, healthy)

	text, data, _, err := execJSON(t, tool, `{}`)
	if err != nil {
		t.Fatalf("一个 provider 失败不该让整份目录失败：%v", err)
	}
	if !strings.Contains(text, "healthy-skill") || !strings.Contains(text, "local-skill") {
		t.Fatalf("失败 provider 之外的候选必须还在：\n%s", text)
	}
	if incomplete, _ := data["incomplete"].(bool); !incomplete {
		t.Fatalf("有 provider 失败时 incomplete 必须是 true：%v", data)
	}
	if stats := registry.Stats(); stats.ProviderListFailures != 1 {
		t.Fatalf("provider 失败计数 = %d，期望 1", stats.ProviderListFailures)
	}
	warnMu.Lock()
	joined := strings.Join(warnings, "\n")
	warnMu.Unlock()
	if !strings.Contains(joined, "broken-stub") || !strings.Contains(joined, "失败") {
		t.Fatalf("provider 失败必须有日志（不许静默）：%q", joined)
	}

	// "确认没有"与"不完整"必须分开：空目录 + 失败 provider ≠ 没装任何 skill。
	empty := New(t.TempDir())
	emptyRegistry, err := empty.Registry()
	if err != nil {
		t.Fatal(err)
	}
	emptyRegistry.SetWarningSink(func(string) {})
	registerStub(t, empty, &stubProvider{name: "broken-stub", listErr: fmt.Errorf("远端 503")})
	_, _, hint, err := execJSON(t, empty, `{}`)
	if err == nil {
		t.Fatal("空目录 + 失败 provider 也该报错（但要说不完整）")
	}
	if !strings.Contains(hint, "不完整") {
		t.Fatalf("Hint 必须说清是「不完整」，不能像「确认没有」：%s", hint)
	}
	if strings.Contains(hint, "没装任何 skill") {
		t.Fatalf("有 provider 失败时不能说「没装任何 skill」：%s", hint)
	}
}

// TestProviderRejectsInvalidCandidates 非法数据必须 fail-loud：官方 validateCandidate
// （`dsh-skill/src/index.ts:956-1006`）对空名/非 kebab-case/归属不符/缺策略一律抛错。
func TestProviderRejectsInvalidCandidates(t *testing.T) {
	t.Setenv("DSHD_SKILLS_EXTRA", "")
	cases := []struct {
		name      string
		candidate Candidate
		wantErr   string
	}{
		{
			name:      "空名",
			candidate: stubCandidate("valid-stub", "", "没有名字", 100, BothInvocable()),
			wantErr:   "空名字",
		},
		{
			name:      "非 kebab-case",
			candidate: stubCandidate("valid-stub", "Bad_Name", "大写名字", 100, BothInvocable()),
			wantErr:   "kebab-case",
		},
		{
			name:      "provider 归属不符",
			candidate: stubCandidate("other-provider", "fine-name", "别人的候选", 100, BothInvocable()),
			wantErr:   "归属",
		},
		{
			name: "没给 invocation 策略",
			candidate: Candidate{
				Summary: Summary{Name: "no-policy", Description: "缺策略", Source: "custom", Provider: "valid-stub"},
				Rank:    100, Locator: "no-policy",
			},
			wantErr: "invocation",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registry := NewRegistry()
			registry.SetWarningSink(func(string) {})
			if _, err := registry.RegisterProvider(func(ProviderControl) (Provider, error) {
				return &stubProvider{
					name: "valid-stub", complete: true,
					candidates: []Candidate{tc.candidate},
					bodies:     map[string]string{},
				}, nil
			}); err != nil {
				t.Fatal(err)
			}
			_, err := registry.Snapshot(context.Background(), LookupOptions{})
			if err == nil {
				t.Fatalf("非法候选必须报错（%s）", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("错误信息不含 %q：%v", tc.wantErr, err)
			}
		})
	}
}

// TestRegistryFailsLoudOnBadRegistration 注册面自身的非法输入：保留名/重名/非法名/
// 非法 policy 都必须返回错误，不许静默成功。
func TestRegistryFailsLoudOnBadRegistration(t *testing.T) {
	registry := NewRegistry()
	registry.SetWarningSink(func(string) {})

	// `runtime` 是保留 provider 名（官方 :22 + :437-439）。
	if _, err := registry.RegisterProvider(func(ProviderControl) (Provider, error) {
		return &stubProvider{name: "runtime"}, nil
	}); err == nil || !strings.Contains(err.Error(), "保留名") {
		t.Fatalf("用保留名 runtime 注册 provider 必须报错：%v", err)
	}

	// 同层重名。
	if _, err := registry.RegisterProvider(func(ProviderControl) (Provider, error) {
		return &stubProvider{name: "same-stub"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.RegisterProvider(func(ProviderControl) (Provider, error) {
		return &stubProvider{name: "same-stub"}, nil
	}); err == nil || !strings.Contains(err.Error(), "已经注册") {
		t.Fatalf("同层重名 provider 必须报错：%v", err)
	}

	// runtime 注册的非法输入。
	if _, err := registry.Register(Registration{Name: "Bad_Name", Description: "x"}); err == nil {
		t.Fatal("非 kebab-case 的 runtime skill 名必须报错")
	}
	if _, err := registry.Register(Registration{Name: "no-desc"}); err == nil {
		t.Fatal("缺 description 的 runtime skill 必须报错")
	}
	empty := InvocationPolicy{} // 零值 = 没给（不是 {false,false}）
	if _, err := registry.Register(Registration{
		Name: "zero-policy", Description: "零值策略", Invocation: &empty,
	}); err == nil || !strings.Contains(err.Error(), "invocation") {
		t.Fatalf("零值（没给）的 invocation 必须报错：%v", err)
	}

	// 非 bool 的原始 policy（远端 JSON / YAML 解出来的形状）同样 fail-loud。
	if _, err := ParseInvocationPolicy(map[string]any{
		"modelInvocable": "yes", "userInvocable": true,
	}); err == nil || !strings.Contains(err.Error(), "不是 bool") {
		t.Fatalf("非 bool 的 modelInvocable 必须报错：%v", err)
	}
	if _, err := ParseInvocationPolicy(map[string]any{"modelInvocable": true}); err == nil {
		t.Fatal("缺 userInvocable 必须报错")
	}
	if _, err := ParseInvocationPolicy(map[string]any{
		"modelInvocable": true, "userInvocable": false,
	}); err != nil {
		t.Fatalf("合法组合不该报错：%v", err)
	}
}

// TestStaleDefinitionInvalidatesCatalog 载入回来的定义名与选中候选不一致 = 过期
// （官方 :552-558）：不返回别人的正文、让缓存失效、下次重扫能拿到新名字。
func TestStaleDefinitionInvalidatesCatalog(t *testing.T) {
	t.Setenv("DSHD_SKILLS_CACHE_TTL_MS", "600000")
	t.Setenv("DSHD_SKILLS_EXTRA", "")
	tool := New(t.TempDir())
	registry, err := tool.Registry()
	if err != nil {
		t.Fatal(err)
	}
	registry.SetWarningSink(func(string) {})

	stub := &stubProvider{
		name: "stale-stub", complete: true, definitionName: "renamed-skill",
		bodies: map[string]string{"stale-skill": "STALE-BODY\n"},
		candidates: []Candidate{
			stubCandidate("stale-stub", "stale-skill", "发现之后改名了", 300, BothInvocable()),
		},
	}
	registerStub(t, tool, stub)
	revisionBefore := registry.Revision()

	definition, err := registry.Get(context.Background(), "stale-skill", LookupOptions{})
	if err != nil {
		t.Fatalf("过期检测不该变成 error：%v", err)
	}
	if definition != nil {
		t.Fatalf("定义名与候选不一致时必须当作过期（nil），实际：%+v", definition)
	}
	if stats := registry.Stats(); stats.StaleDefinitionInvalidations != 1 {
		t.Fatalf("过期失效计数 = %d，期望 1", stats.StaleDefinitionInvalidations)
	}
	if registry.Revision() <= revisionBefore {
		t.Fatalf("过期必须让缓存失效（revision %d → %d）", revisionBefore, registry.Revision())
	}
}

// TestObservationDistinguishesIncomplete 官方 `snapshot` 的 complete 口径：
// provider 自报 complete=false 时候选照算，但观察标 incomplete。
func TestObservationDistinguishesIncomplete(t *testing.T) {
	registry := NewRegistry()
	registry.SetWarningSink(func(string) {})
	stub := &stubProvider{
		name: "partial-stub", complete: false,
		bodies: map[string]string{"partial-skill": "PARTIAL\n"},
		candidates: []Candidate{
			stubCandidate("partial-stub", "partial-skill", "部分成功的结果", 300, BothInvocable()),
		},
	}
	if _, err := registry.RegisterProvider(func(ProviderControl) (Provider, error) { return stub, nil }); err != nil {
		t.Fatal(err)
	}
	catalog, err := registry.Snapshot(context.Background(), LookupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Complete {
		t.Fatal("provider 自报 complete=false 时观察必须是 incomplete")
	}
	if len(catalog.Skills) != 1 || catalog.Skills[0].Name != "partial-skill" {
		t.Fatalf("不完整观察里的候选仍然算数：%+v", catalog.Skills)
	}
	if stats := registry.Stats(); stats.IncompleteObservations != 1 {
		t.Fatalf("不完整观察计数 = %d，期望 1", stats.IncompleteObservations)
	}
}
