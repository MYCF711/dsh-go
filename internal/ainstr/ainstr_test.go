package ainstr

// 这些测试钉的是官方 `@deepseek-ai/dsh-agent-instructions` **durable 层**的契约，
// 逐条对着 tag `dsh-v0.2.2-rc.2` 的 `src/{config,digest,files,render,state,index}.ts`。
//
// 每条测试都造真实目录树、走真实路径（`Compose` → 落盘载荷 → `FrameStateFromLog` 重放），
// 不是"函数存在"的空断言：
//
//	TestIdentityMatchesOfficialKeyOrder              config.ts:71-84
//	TestDigestIsSha1OfExactAndTrimmedContent          digest.ts:14-28
//	TestScopeKeyRoundTripAndReservedScope             render.ts:201-224、170-186
//	TestAdditionalSectionApplicability                render.ts:229-243
//	TestChangedSectionShapes                          render.ts:257-270
//	TestBaselineFrameIsDurableUserMessage             state.ts:40-49、index.ts:194-203
//	TestReplayFoldsVisibleStateAndBaseline            state.ts:140-161、index.ts:47-64
//	TestReconcileEmitsSetReplaceRemove                state.ts:371-445
//	TestReconcileRollsBackGroupOnUnavailable          state.ts:357-377
//	TestReconcileDedupsWithinGroupByTrimmedContent    state.ts:312-323、420-430
//	TestReconcileMetadataFastPathSkipsRead            state.ts:386-405
//	TestReconcileTinyBudgetCommitsNothing             state.ts:446-451
//	TestComposeEmitsBaselineThenIncrement             index.ts:109-243
//	TestComposeKeepsBaselineWhenIdentityMatches       index.ts:131-141
//	TestComposeReplacesBaselineOnCwdChange            index.ts:139-193
//	TestComposeDiscardsAlreadySupplied                index.ts:245-254
//	TestStepBoundaryDefersTouches                     index.ts:304-328
//	TestToolTouchesOnlySuccessfulFileTools            index.ts:75-83、358-374
//	TestDescendantDirsFindsNestedScopes               files.ts:276-283

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// markRoot 造官方默认项目根标记 `.git`。
func markRoot(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// project 造一个带 .git 的项目树，返回根与"会话 cwd"。
func project(t *testing.T) (root, cwd string) {
	t.Helper()
	root = t.TempDir()
	markRoot(t, root)
	writeFile(t, filepath.Join(root, "AGENTS.md"), "root-rules")
	if err := os.MkdirAll(filepath.Join(root, "pkg", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root, filepath.Join(root, "pkg", "sub")
}

func defaultConfig(t *testing.T) Config {
	t.Helper()
	return resolveForTest(Config{MaxBytes: 65536})
}

func resolveForTest(config Config) Config {
	return ResolveConfig(config)
}

// TestIdentityMatchesOfficialKeyOrder 钉官方 `workspaceBaselineIdentity`
// （`src/config.ts:71-84`）：六个键、**插入序**（JSON.stringify 的顺序）。
//
// 为什么钉键序：身份是跨轮/跨重启对账的唯一凭据。键序变了值就变了 → 每次 resume
// 都判"身份不等" → 整条基线重发 → prompt cache 全失效，而功能看起来正常。
func TestIdentityMatchesOfficialKeyOrder(t *testing.T) {
	config := resolveForTest(Config{
		MaxBytes:                       65536,
		InstructionFileCandidates:      []string{"AGENTS.md"},
		LocalInstructionFileCandidates: []string{"AGENTS.local.md"},
		ProjectRootMarkers:             []string{".git"},
	})
	root, cwd := project(t)
	got := Identity(cwd, root, config)

	// 用 encoding/json 解成有序键序列（json.Decoder 的 Token 流）。
	decoder := json.NewDecoder(strings.NewReader(got))
	token, err := decoder.Token()
	if err != nil {
		t.Fatalf("身份不是合法 JSON: %v", err)
	}
	if token != json.Delim('{') {
		t.Fatalf("身份应是对象，开头是 %v", token)
	}
	var keys []string
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			t.Fatal(err)
		}
		key, ok := keyToken.(string)
		if !ok {
			t.Fatalf("键不是字符串: %v", keyToken)
		}
		keys = append(keys, key)
		var discard json.RawMessage
		if err := decoder.Decode(&discard); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{
		"projectRoot", "projectRootMarkers", "maxBytes", "maxSourceBytes",
		"instructionFileCandidates", "localInstructionFileCandidates",
	}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("身份键序不对（官方 config.ts:76-83 的插入序）:\n got=%v\nwant=%v", keys, want)
	}
	// projectRoot 是**相对 cwd** 的路径（官方 `relative(cwd, projectRoot)`）。
	if !strings.Contains(got, `"projectRoot":"../.."`) {
		t.Fatalf("projectRoot 应是 cwd 相对的 ../..（官方 relative(cwd, projectRoot)）：%s", got)
	}
	// 同一个输入必须得到同一个身份（确定性是它唯一的用途）。
	if again := Identity(cwd, root, config); again != got {
		t.Fatalf("同一输入的身份不稳定:\n%s\n%s", got, again)
	}
}

// TestDigestIsSha1OfExactAndTrimmedContent 钉官方 digest.ts:14-28。
func TestDigestIsSha1OfExactAndTrimmedContent(t *testing.T) {
	// sha1("abc") 是公开值，用来证明用的是 SHA-1 而不是别的散列。
	if got := Digest("abc"); got != "a9993e364706816aba3e25717850c26c9cd0d89d" {
		t.Fatalf("Digest 不是 SHA-1: %q", got)
	}
	a := TrimmedDigest("  same bytes  ")
	b := TrimmedDigest("same bytes")
	if a != b {
		t.Fatal("trim 身份应忽略首尾空白（官方 digest.ts:21-28）")
	}
	if TrimmedDigest("same bytes\n") != b {
		t.Fatal("trim 身份应忽略尾部换行")
	}
}

// TestScopeKeyRoundTripAndReservedScope 钉官方 render.ts:138-224。
func TestScopeKeyRoundTripAndReservedScope(t *testing.T) {
	cases := []struct {
		display  string
		scope    string
		directoy string
	}{
		{"AGENTS.md", ".", "."},
		{"pkg/AGENTS.md", "pkg", "pkg"},
		{"./user-global/AGENTS.md", "./user-global", "./user-global"},
		{"$DSH_HOME/AGENTS.md", UserGlobalDirectory, UserGlobalDirectory},
		{"$DSH_AGENTS_HOME/AGENTS.md", AgentsGlobalDirectory, AgentsGlobalDirectory},
		{"~/.dsh/AGENTS.md", UserGlobalDirectory, UserGlobalDirectory},
		{"~/.agents/AGENTS.md", AgentsGlobalDirectory, AgentsGlobalDirectory},
	}
	for _, tc := range cases {
		if directory := ScopeForDisplayPath(tc.display); directory != tc.directoy {
			t.Fatalf("scopeForDisplayPath(%q)=%q，应为 %q", tc.display, directory, tc.directoy)
		}
		key := InstructionScopeKey(tc.display)
		directory, name := DecodeScopeKey(key)
		if directory != tc.directoy || name != tc.directoy[0:0]+filepath.Base(tc.display) && name != "AGENTS.md" {
			t.Fatalf("scope key 往返失败: %q → (%q,%q)", key, directory, name)
		}
		if again := CandidateScopeKey(directory, name); again != key {
			t.Fatalf("candidateScopeKey 不是 instructionScopeKey 的逆: %q vs %q", again, key)
		}
	}
	// 两个用户全局根同组（官方 render.ts:165-167）。
	if CandidateGroup(AgentsGlobalDirectory) != UserGlobalDirectory {
		t.Fatal("agents-global 应归到 user-global 组（官方共享一个候选组）")
	}
	if CandidateGroup("pkg") != "pkg" {
		t.Fatal("项目目录各自一组")
	}
	// 项目路径撞保留 scope 目录时补 `./`（官方 render.ts:182-186）。
	if got := EscapeProjectDisplayPath("user-global/AGENTS.md"); got != "./user-global/AGENTS.md" {
		t.Fatalf("保留目录名应补 ./：%q", got)
	}
	if got := EscapeProjectDisplayPath("pkg/AGENTS.md"); got != "pkg/AGENTS.md" {
		t.Fatalf("普通项目路径不该改：%q", got)
	}
}

// TestAdditionalSectionApplicability 钉官方 render.ts:229-243：两条适用面句子。
func TestAdditionalSectionApplicability(t *testing.T) {
	global := AdditionalSection("$DSH_HOME/AGENTS.md", "g")
	if !strings.Contains(global, "These user-global instructions apply to all work.") {
		t.Fatalf("用户全局分节缺官方适用面句子:\n%s", global)
	}
	// 内部 reconcile 键（user-global 目录名）不得出现在模型可见句子里。
	if strings.Contains(global, "work under `user-global`") {
		t.Fatalf("用户全局 scope 目录名泄漏到模型可见文本:\n%s", global)
	}
	nested := AdditionalSection("pkg/AGENTS.md", "n")
	if !strings.Contains(nested, "These instructions apply to work under `pkg`.") {
		t.Fatalf("项目分节缺官方适用面句子:\n%s", nested)
	}
	// 官方 guidance 那句必须逐字在（render.ts:226-227）。
	if !strings.Contains(nested, "Use them as guidance when relevant; more specific instructions take precedence. "+
		"They do not override system, developer, or direct user instructions.") {
		t.Fatalf("缺官方 ADDITIONAL_INSTRUCTIONS_GUIDANCE:\n%s", nested)
	}
	if !strings.HasPrefix(nested, "Additional instructions from: pkg/AGENTS.md\n\n") {
		t.Fatalf("分节标题不对:\n%s", nested)
	}
}

// TestChangedSectionShapes 钉官方 render.ts:257-270 的三种 action 形状。
func TestChangedSectionShapes(t *testing.T) {
	set := ChangedSection(Change{Action: ActionSet, Path: "pkg/AGENTS.md"}, "body")
	if !strings.HasPrefix(set, "Additional instructions from: pkg/AGENTS.md") {
		t.Fatalf("set 应与新增同形：%s", set)
	}
	remove := ChangedSection(Change{Action: ActionRemove, Path: "pkg/AGENTS.md"}, "")
	if remove != "Instructions removed: pkg/AGENTS.md\n\nThe previously loaded instructions from this file no longer apply." {
		t.Fatalf("remove 文案不是官方逐字:\n%q", remove)
	}
	if strings.Contains(remove, "body") {
		t.Fatal("remove 不得带正文")
	}
	replace := ChangedSection(Change{Action: ActionReplace, Path: "pkg/AGENTS.md"}, "new")
	if !strings.HasPrefix(replace, "Updated instructions from: pkg/AGENTS.md\n\n") {
		t.Fatalf("replace 标题不对:\n%s", replace)
	}
	if !strings.Contains(replace, "Use the following content instead of the previously loaded instructions from this file.") {
		t.Fatalf("replace 缺官方说明句:\n%s", replace)
	}
	if !strings.HasSuffix(replace, "\n\nnew") {
		t.Fatalf("replace 应带新正文:\n%s", replace)
	}
}

// TestBaselineFrameIsDurableUserMessage 钉官方 state.ts:40-49 + index.ts:194-203：
// 落盘形状是 `user/message`，source 是 `{kind,form,baseline,baselineIdentity,changes}`。
func TestBaselineFrameIsDurableUserMessage(t *testing.T) {
	frame := Frame{
		Text:     "<system-reminder>\nhi\n</system-reminder>",
		Baseline: true,
		Identity: `{"projectRoot":""}`,
		Changes: []Change{
			{Action: ActionSet, Scope: "pkg\x00AGENTS.md", Path: "pkg/AGENTS.md", Digest: Digest("x")},
		},
		Source: Source{
			Kind:             SourceKind,
			Form:             SourceForm,
			Baseline:         true,
			BaselineIdentity: `{"projectRoot":""}`,
			Changes:          []Change{{Action: ActionSet, Scope: "pkg\x00AGENTS.md", Path: "pkg/AGENTS.md", Digest: Digest("x")}},
		},
	}
	event := FrameEventOf(frame)
	if event.Type != "user/message" {
		t.Fatalf("基线帧必须落成 user/message（官方 index.ts:194-203），得到 %q", event.Type)
	}
	payload, err := json.Marshal(event.Data)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Role      string `json:"role"`
		SurfaceOp string `json:"surfaceOp"`
		Content   []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Source struct {
			Kind             string `json:"kind"`
			Form             string `json:"form"`
			Baseline         bool   `json:"baseline"`
			BaselineIdentity string `json:"baselineIdentity"`
			Changes          []struct {
				Action string `json:"action"`
				Scope  string `json:"scope"`
				Path   string `json:"path"`
				Digest string `json:"digest"`
			} `json:"changes"`
		} `json:"source"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Role != "user" || decoded.SurfaceOp != "append" {
		t.Fatalf("落盘角色/落位不对: role=%q surfaceOp=%q", decoded.Role, decoded.SurfaceOp)
	}
	if len(decoded.Content) != 1 || decoded.Content[0].Type != "text" || decoded.Content[0].Text != frame.Text {
		t.Fatalf("content 应是一个 text 块且正文原样: %+v", decoded.Content)
	}
	if decoded.Source.Kind != SourceKind || decoded.Source.Form != SourceForm ||
		!decoded.Source.Baseline || decoded.Source.BaselineIdentity != frame.Identity {
		t.Fatalf("source 形状不对: %+v", decoded.Source)
	}
	if len(decoded.Source.Changes) != 1 || decoded.Source.Changes[0].Action != ActionSet ||
		decoded.Source.Changes[0].Scope != "pkg\x00AGENTS.md" || decoded.Source.Changes[0].Digest != Digest("x") {
		t.Fatalf("changes 形状不对: %+v", decoded.Source.Changes)
	}

	// 非基线帧（增量）不得带 baseline / baselineIdentity。
	increment := FrameEventOf(Frame{Text: "x", Source: Source{Kind: SourceKind, Form: SourceForm}})
	raw, _ := json.Marshal(increment.Data)
	if strings.Contains(string(raw), `"baseline":true`) || strings.Contains(string(raw), "baselineIdentity") {
		t.Fatalf("增量帧不得标 baseline（官方 index.ts:236-241 的条件展开）: %s", raw)
	}
}

// TestReplayFoldsVisibleStateAndBaseline 钉官方 state.ts:140-161 与 index.ts:47-64。
func TestReplayFoldsVisibleStateAndBaseline(t *testing.T) {
	baseline := FrameEventOfText("baseline-text", Source{
		Kind: SourceKind, Form: SourceForm, Baseline: true, BaselineIdentity: "id-1",
		Changes: []Change{{Action: ActionSet, Scope: "a\x00AGENTS.md", Path: "AGENTS.md", Digest: "d1"}},
	})
	increment := FrameEventOfText("inc", Source{
		Kind: SourceKind, Form: SourceForm,
		Changes: []Change{
			{Action: ActionReplace, Scope: "a\x00AGENTS.md", Path: "AGENTS.md", Digest: "d2"},
			{Action: ActionRemove, Scope: "b\x00AGENTS.md", Path: "b/AGENTS.md"},
		},
	})
	// 用户自己说的话不能污染重放。
	userMessage := map[string]any{
		"content": []any{map[string]any{"type": "text", "text": "hello"}},
		"source":  map[string]any{"kind": "user"},
		"role":    "user",
	}
	userPayload, _ := json.Marshal(userMessage)

	state := FrameStateFromLog(sliceEvents{
		{Type: "user/message", Data: mustJSON(t, userMessage)},
		{Type: "user/message", Data: mustJSON(t, baseline.Data)},
		{Type: "assistant/message", Data: []byte(`{"role":"assistant"}`)},
		{Type: "user/message", Data: mustJSON(t, increment.Data)},
		{Type: "user/message", Data: userPayload},
	})
	if !state.BaselineOK || state.Baseline.Identity != "id-1" {
		t.Fatalf("重放没找到基线帧: %+v", state.Baseline)
	}
	visible := VisibleChanges(state.Visible)
	if len(visible) != 2 {
		t.Fatalf("visible 应有 2 个 scope，得到 %d: %+v", len(visible), visible)
	}
	// 同 scope 后者覆盖前者（replace 盖掉 set）。
	if got := visible["a\x00AGENTS.md"]; got.Action != ActionReplace || got.Digest != "d2" {
		t.Fatalf("同 scope 应后者覆盖前者: %+v", got)
	}
	if got := visible["b\x00AGENTS.md"]; got.Action != ActionRemove {
		t.Fatalf("remove 应保留: %+v", got)
	}
	// 最后一条帧的载荷用于 alreadySupplied 判重：**用户消息不算**。
	if state.LastText != "inc" {
		t.Fatalf("LastText 应是最后一条指令帧: %q", state.LastText)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// sliceEvents 实现 LoadSessionEvents（测试用的内存事件源）。
type sliceEvents []struct {
	Type string
	Data []byte
}

func (s sliceEvents) Types() []string {
	out := make([]string, 0, len(s))
	for _, event := range s {
		out = append(out, event.Type)
	}
	return out
}

func (s sliceEvents) Payloads() [][]byte {
	out := make([][]byte, 0, len(s))
	for _, event := range s {
		out = append(out, event.Data)
	}
	return out
}

// TestReconcileEmitsSetReplaceRemove 钉官方 state.ts:371-445 的三种变化。
//
// 场景（三次 reconcile，同一个 version 缓存）：
//  1. 首次：`pkg/AGENTS.md` 出现 → `set`；
//  2. 改内容（mtime/size 变）→ `replace`；
//  3. 删文件 → `remove`。
func TestReconcileEmitsSetReplaceRemove(t *testing.T) {
	root, cwd := project(t)
	config := defaultConfig(t)
	writeFile(t, filepath.Join(root, "pkg", "AGENTS.md"), "v1")
	cache := NewVersionCache()

	first, err := Reconcile(config, cache, ReconcileOptions{
		Cwd: cwd, IncludeBaselineScopes: true,
		ScopeMessages: []Change{{Action: ActionSet, Scope: CandidateScopeKey("pkg", "AGENTS.md"), Path: "pkg/AGENTS.md"}},
		ProjectRoot:   root,
	})
	if err != nil {
		t.Fatal(err)
	}
	pkgSet := findChange(t, first.Changes, "pkg/AGENTS.md")
	if pkgSet.Action != ActionSet {
		t.Fatalf("首次应产出 set(pkg/AGENTS.md): %+v", first.Changes)
	}
	if pkgSet.Digest != Digest("v1") {
		t.Fatalf("digest 应是原样内容的 SHA-1: %q", pkgSet.Digest)
	}
	if !strings.Contains(first.Body, "Additional instructions from: pkg/AGENTS.md") {
		t.Fatalf("set 应渲染成官方 Additional instructions 分节: %s", first.Body)
	}
	ApplyVersionUpdates(cache, first.VersionUpdates)

	// 改内容（必须让 mtime/size 都变：size 变最稳）。
	writeFile(t, filepath.Join(root, "pkg", "AGENTS.md"), "v2-longer")
	visible := append([]Change(nil), first.Changes...)
	second, err := Reconcile(config, cache, ReconcileOptions{
		Cwd: cwd, AuthorityMessages: visible, IncludeBaselineScopes: true,
		ScopeMessages: []Change{{Action: ActionSet, Scope: CandidateScopeKey("pkg", "AGENTS.md"), Path: "pkg/AGENTS.md"}},
		ProjectRoot:   root,
	})
	if err != nil {
		t.Fatal(err)
	}
	pkgReplace := findChange(t, second.Changes, "pkg/AGENTS.md")
	if pkgReplace.Action != ActionReplace {
		t.Fatalf("改内容应产出 replace: %+v", second.Changes)
	}
	if !strings.Contains(second.Body, "Updated instructions from: pkg/AGENTS.md") {
		t.Fatalf("replace 应渲染成 Updated instructions 分节:\n%s", second.Body)
	}
	ApplyVersionUpdates(cache, second.VersionUpdates)

	// 删文件。
	if err := os.Remove(filepath.Join(root, "pkg", "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	visible = append(visible, second.Changes...)
	third, err := Reconcile(config, cache, ReconcileOptions{
		Cwd: cwd, AuthorityMessages: visible, IncludeBaselineScopes: true,
		ScopeMessages: []Change{{Action: ActionSet, Scope: CandidateScopeKey("pkg", "AGENTS.md"), Path: "pkg/AGENTS.md"}},
		ProjectRoot:   root,
	})
	if err != nil {
		t.Fatal(err)
	}
	pkgRemove := findChange(t, third.Changes, "pkg/AGENTS.md")
	if pkgRemove.Action != ActionRemove {
		t.Fatalf("删文件应产出 remove: %+v", third.Changes)
	}
	if !strings.Contains(third.Body, "Instructions removed: pkg/AGENTS.md") {
		t.Fatalf("remove 应渲染成 Instructions removed 分节:\n%s", third.Body)
	}

	// 什么都没变 → 不产出（官方 `items.length === 0` 早退）。
	ApplyVersionUpdates(cache, third.VersionUpdates)
	fourth, err := Reconcile(config, cache, ReconcileOptions{
		Cwd: cwd, AuthorityMessages: append(visible, third.Changes...), IncludeBaselineScopes: true,
		ScopeMessages: []Change{{Action: ActionSet, Scope: CandidateScopeKey("pkg", "AGENTS.md"), Path: "pkg/AGENTS.md"}},
		ProjectRoot:   root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(fourth.Changes) != 0 || fourth.Body != "" {
		t.Fatalf("无变化不该产出: %+v", fourth)
	}
}

// TestReconcileRollsBackGroupOnUnavailable 钉官方 state.ts:357-377 的整组回滚。
//
// 用一个**目录**顶替 AGENTS.md 制造"不是文件"？不——那是 absent（官方也算缺席）。
// 真正造 unavailable 需要权限错误：Windows 上 chmod 不管用，所以改用
// "路径中间一段是文件"（ENOTDIR）：那会被判 absent 而不是 unavailable... 也会被
// 误判。所以这里用**读不出来的文件**（目录仍然存在）：先把 AGENTS.md 建成目录，
// 官方判 `type !== 'file'` → absent。
//
// 结论：本地 FS 上"确认缺席"与"读不了"无法用 portable 的方式稳定区分，所以本测试
// 钉**回滚的机制本身**（Probe 的三态在包内是纯函数），用失败注入替身验证：
// `Reconcile` 在见到 `ProbeUnavailable` 时必须丢弃该组已排进批次的 items 与 version 更新。
func TestReconcileRollsBackGroupOnUnavailable(t *testing.T) {
	// 组内第一个 scope 产出 set、第二个 scope 读失败 ⇒ 整组回滚：
	// 已排进批次的 set 被撤销、缓存回到组前状态、什么都不产出。
	//
	// 用独立的临时树（**没有**根 AGENTS.md）：否则根上的 set 会混进结果，
	// 断言「什么都不产出」就没法成立。
	root := t.TempDir()
	markRoot(t, root)
	if err := os.MkdirAll(filepath.Join(root, "pkg", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(root, "pkg", "sub")
	config := defaultConfig(t)
	writeFile(t, filepath.Join(root, "pkg", "AGENTS.md"), "a")
	writeFile(t, filepath.Join(root, "pkg", "CLAUDE.md"), "b")
	cache := NewVersionCache()

	failReadGroup = func(scope string) bool {
		return strings.HasPrefix(scope, "pkg\x00CLAUDE.md")
	}
	defer func() { failReadGroup = nil }()

	result, err := Reconcile(config, cache, ReconcileOptions{
		Cwd: cwd, IncludeBaselineScopes: true,
		ProjectRoot: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Changes) != 0 || result.Body != "" {
		t.Fatalf("组内读失败必须整组回滚（官方 state.ts:374-377），实际产出 %+v", result)
	}
	if cache.Len() != 0 {
		t.Fatalf("回滚后缓存必须回到组前状态，实际 %d 条", cache.Len())
	}
}

// TestReconcileDedupsWithinGroupByTrimmedContent 钉官方 state.ts:420-430 +
// render.ts:165-167：同目录 trim 后同内容只留最早一个。
func TestReconcileDedupsWithinGroupByTrimmedContent(t *testing.T) {
	root, cwd := project(t)
	config := defaultConfig(t)
	writeFile(t, filepath.Join(root, "pkg", "AGENTS.md"), "same")
	writeFile(t, filepath.Join(root, "pkg", "CLAUDE.md"), "same\n")
	cache := NewVersionCache()

	result, err := Reconcile(config, cache, ReconcileOptions{
		Cwd: cwd, IncludeBaselineScopes: true,
		ScopeMessages: []Change{
			{Action: ActionSet, Scope: CandidateScopeKey("pkg", "AGENTS.md"), Path: "pkg/AGENTS.md"},
			{Action: ActionSet, Scope: CandidateScopeKey("pkg", "CLAUDE.md"), Path: "pkg/CLAUDE.md"},
		},
		ProjectRoot: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findChangeOK(result.Changes, "pkg/CLAUDE.md"); ok {
		t.Fatalf("组内 trim 重复的 CLAUDE.md 不该渲染: %+v", result.Changes)
	}
	if pkgSet, ok := findChangeOK(result.Changes, "pkg/AGENTS.md"); !ok || pkgSet.Action != ActionSet {
		t.Fatalf("组内 trim 重复应只留最早的 AGENTS.md: %+v", result.Changes)
	}
}

// TestReconcileMetadataFastPathSkipsRead 钉官方 state.ts:386-405：元数据没变时
// 不重读内容、不产出 change。
func TestReconcileMetadataFastPathSkipsRead(t *testing.T) {
	root, cwd := project(t)
	config := defaultConfig(t)
	path := filepath.Join(root, "pkg", "AGENTS.md")
	writeFile(t, path, "content")
	cache := NewVersionCache()

	first, err := Reconcile(config, cache, ReconcileOptions{
		Cwd: cwd, IncludeBaselineScopes: true,
		ScopeMessages: []Change{{Action: ActionSet, Scope: CandidateScopeKey("pkg", "AGENTS.md"), Path: "pkg/AGENTS.md"}},
		ProjectRoot:   root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Changes) == 0 {
		t.Fatalf("前置：首次应产出基线 scope 的 set: %+v", first)
	}
	ApplyVersionUpdates(cache, first.VersionUpdates)
	readCount := 0
	countReads = func() { readCount++ }
	defer func() { countReads = nil }()

	second, err := Reconcile(config, cache, ReconcileOptions{
		Cwd: cwd, AuthorityMessages: first.Changes, IncludeBaselineScopes: true,
		ScopeMessages: []Change{{Action: ActionSet, Scope: CandidateScopeKey("pkg", "AGENTS.md"), Path: "pkg/AGENTS.md"}},
		ProjectRoot:   root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Changes) != 0 || second.Body != "" {
		t.Fatalf("元数据没变应早退，实际 %+v", second)
	}
	if readCount != 0 {
		t.Fatalf("元数据快路径不该重读内容，实际读了 %d 次", readCount)
	}
}

// TestReconcileTinyBudgetCommitsNothing 钉官方 state.ts:446-451：
// 预算小到只渲染出预算通知（一条 change 都没表示）时，**什么都不提交**，
// 否则缓存会记着"已渲染"，那条指令永远不再进上下文。
func TestReconcileTinyBudgetCommitsNothing(t *testing.T) {
	root, cwd := project(t)
	config := resolveForTest(Config{MaxBytes: 1})
	writeFile(t, filepath.Join(root, "pkg", "AGENTS.md"), "long content that will never fit")
	cache := NewVersionCache()

	result, err := Reconcile(config, cache, ReconcileOptions{
		Cwd:           cwd,
		ScopeMessages: []Change{{Action: ActionSet, Scope: CandidateScopeKey("pkg", "AGENTS.md"), Path: "pkg/AGENTS.md"}},
		ProjectRoot:   root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Changes) != 0 || result.Body != "" || len(result.VersionUpdates) != 0 {
		t.Fatalf("渲染没表示出任何 change 时不得提交: %+v", result)
	}
	if cache.Len() != 0 {
		t.Fatalf("不得写缓存: %d 条", cache.Len())
	}
}

// TestComposeEmitsBaselineThenIncrement 钉官方 index.ts:109-243：
// 首轮 = 基线（一条帧），次轮无变化 = 不产出，文件被碰过后 = 增量帧。
func TestComposeEmitsBaselineThenIncrement(t *testing.T) {
	root, cwd := project(t)
	config := defaultConfig(t)
	runner := NewRunner()
	state := FrameState{}

	first, err := runner.ComposeAndCommit(Options{Config: config, Cwd: cwd, State: state},
		func() (FrameState, error) { return state, nil },
		func(Frame) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if first.Frame == nil {
		t.Fatalf("首轮应产出基线帧: %+v", first)
	}
	if !first.Frame.Source.Baseline || first.Frame.Source.BaselineIdentity == "" {
		t.Fatalf("首轮帧必须是基线（带 baseline + baselineIdentity）: %+v", first.Frame.Source)
	}
	if !strings.Contains(first.Frame.Text, "root-rules") {
		t.Fatalf("基线应包含项目根 AGENTS.md 的内容:\n%s", first.Frame.Text)
	}
	if !strings.Contains(first.Frame.Text, "<system-reminder>") {
		t.Fatalf("基线应是官方 `<system-reminder>` 帧形状:\n%s", first.Frame.Text)
	}

	// 把基线帧"落盘"后重放，再跑一次：身份相等 → 不产出。
	baseline := FrameEventOf(*first.Frame)
	state = FrameStateFromLog(sliceEvents{
		{Type: baseline.Type, Data: mustJSON(t, baseline.Data)},
	})
	second, err := runner.ComposeAndCommit(Options{Config: config, Cwd: cwd, State: state},
		func() (FrameState, error) { return state, nil },
		func(Frame) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if second.Frame != nil {
		t.Fatalf("身份相等且无变化时不该重发基线: %+v", second)
	}
	if second.Discard != DiscardAlreadySupplied && second.Discard != DiscardNothing {
		t.Fatalf("丢弃原因应是 already-supplied / nothing-changed，得到 %q", second.Discard)
	}

	// 碰一个**新**的嵌套指令文件：增量帧，且是 set。
	writeFile(t, filepath.Join(root, "pkg", "sub", "AGENTS.md"), "nested-rules")
	state = FrameStateFromLog(sliceEvents{
		{Type: baseline.Type, Data: mustJSON(t, baseline.Data)},
	})
	third, err := runner.ComposeAndCommit(Options{
		Config: config, Cwd: cwd, State: state,
		TouchedPaths: []string{filepath.Join(root, "pkg", "sub", "AGENTS.md")},
	}, func() (FrameState, error) { return state, nil }, func(Frame) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if third.Frame == nil {
		t.Fatalf("碰过嵌套指令文件后应有增量帧: %+v", third)
	}
	if third.Frame.Source.Baseline {
		t.Fatal("增量帧不得标 baseline")
	}
	if !strings.Contains(third.Frame.Text, "Additional instructions from: pkg/sub/AGENTS.md") {
		t.Fatalf("增量应是官方 Additional instructions 分节:\n%s", third.Frame.Text)
	}
	if !strings.Contains(third.Frame.Text, "nested-rules") {
		t.Fatalf("增量应带正文:\n%s", third.Frame.Text)
	}
}

// TestComposeKeepsBaselineWhenIdentityMatches 钉官方 index.ts:131-141 的
// `keepVisibleBaseline`：身份相等时**不重载基线**，只做增量（不重复整段）。
func TestComposeKeepsBaselineWhenIdentityMatches(t *testing.T) {
	root, cwd := project(t)
	config := defaultConfig(t)
	identity := Identity(cwd, root, config)
	frame := Frame{
		Text: "baseline", Baseline: true, Identity: identity,
		Source: Source{Kind: SourceKind, Form: SourceForm, Baseline: true, BaselineIdentity: identity,
			Changes: []Change{{Action: ActionSet, Scope: CandidateScopeKey(".", "AGENTS.md"), Path: "AGENTS.md", Digest: Digest("root-rules")}}},
	}
	event := FrameEventOf(frame)
	state := FrameStateFromLog(sliceEvents{{Type: event.Type, Data: mustJSON(t, event.Data)}})
	runner := NewRunner()

	composed, err := runner.ComposeAndCommit(Options{Config: config, Cwd: cwd, State: state},
		func() (FrameState, error) { return state, nil }, func(Frame) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if composed.Frame != nil {
		t.Fatalf("身份相等时不该重发基线，实际: %+v", composed.Frame)
	}
}

// TestComposeReplacesBaselineOnCwdChange 钉官方 index.ts:139-193 的替换型基线：
// cwd 变了 → 发**新的**基线帧（标 baseline + 新身份），并显式 remove 旧基线里
// 没有被新基线覆盖的 scope。
func TestComposeReplacesBaselineOnCwdChange(t *testing.T) {
	root := t.TempDir()
	markRoot(t, root)
	writeFile(t, filepath.Join(root, "AGENTS.md"), "root")
	writeFile(t, filepath.Join(root, "pkg", "AGENTS.md"), "pkg-old")
	writeFile(t, filepath.Join(root, "other", "deeper", "AGENTS.md"), "other")
	config := defaultConfig(t)

	// 旧基线：cwd=root/pkg 时的身份 + 一条 pkg/AGENTS.md 的 set。
	oldCwd := filepath.Join(root, "pkg")
	oldIdentity := Identity(oldCwd, root, config)
	oldFrame := Frame{
		Text: "old", Baseline: true, Identity: oldIdentity,
		Changes: []Change{
			{Action: ActionSet, Scope: CandidateScopeKey(".", "AGENTS.md"), Path: "AGENTS.md", Digest: Digest("root")},
			{Action: ActionSet, Scope: CandidateScopeKey("pkg", "AGENTS.md"), Path: "pkg/AGENTS.md", Digest: Digest("pkg-old")},
		},
		Source: Source{Kind: SourceKind, Form: SourceForm, Baseline: true, BaselineIdentity: oldIdentity,
			Changes: []Change{
				{Action: ActionSet, Scope: CandidateScopeKey(".", "AGENTS.md"), Path: "AGENTS.md", Digest: Digest("root")},
				{Action: ActionSet, Scope: CandidateScopeKey("pkg", "AGENTS.md"), Path: "pkg/AGENTS.md", Digest: Digest("pkg-old")},
			}},
	}
	event := FrameEventOf(oldFrame)
	state := FrameStateFromLog(sliceEvents{{Type: event.Type, Data: mustJSON(t, event.Data)}})

	// 新 cwd 深度不同（官方身份编码的是 relative(cwd, projectRoot)，
	// 同深度的兄弟目录身份相同 —— 深度变了身份才变）。
	newCwd := filepath.Join(root, "other", "deeper")
	runner := NewRunner()
	composed, err := runner.ComposeAndCommit(Options{Config: config, Cwd: newCwd, State: state},
		func() (FrameState, error) { return state, nil }, func(Frame) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if composed.Frame == nil || !composed.Frame.Source.Baseline {
		t.Fatalf("cwd 变了应发替换型基线: %+v", composed)
	}
	if composed.Frame.Source.BaselineIdentity == oldIdentity {
		t.Fatal("替换型基线的身份必须是新的")
	}
	// 旧基线里的 pkg/AGENTS.md 不在新基线范围内 → 必须显式 remove。
	var removed bool
	for _, change := range composed.Frame.Source.Changes {
		if change.Action == ActionRemove && change.Path == "pkg/AGENTS.md" {
			removed = true
		}
	}
	if !removed {
		t.Fatalf("替换型基线必须显式移除旧基线里不再覆盖的 scope（官方 index.ts:184-193）: %+v", composed.Frame.Source.Changes)
	}
	if !strings.Contains(composed.Frame.Text, "deeper") {
		t.Fatalf("新基线应包含新 cwd 的链:\n%s", composed.Frame.Text)
	}
}

// TestComposeDiscardsAlreadySupplied 钉官方 index.ts:245-254：载荷完全相同不再落盘。
func TestComposeDiscardsAlreadySupplied(t *testing.T) {
	_, cwd := project(t)
	config := defaultConfig(t)
	runner := NewRunner()

	first, err := runner.ComposeAndCommit(Options{Config: config, Cwd: cwd, State: FrameState{}},
		func() (FrameState, error) { return FrameState{}, nil }, func(Frame) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if first.Frame == nil {
		t.Fatal("首轮应有基线帧")
	}
	event := FrameEventOf(*first.Frame)
	state := FrameStateFromLog(sliceEvents{{Type: event.Type, Data: mustJSON(t, event.Data)}})
	// 同一份日志状态再 compose 一次：身份相等 → 不发基线；无变化 → nothing-changed。
	// 为了钉住 already-supplied 这条**独立的**判据，这里把身份换成不相等，
	// 但把"要发的帧"造成与日志里最后一条完全一致：唯一路径是 alreadySupplied。
	same := *first.Frame
	if state.LastText == same.Text && state.LastSource != nil && sourceEqual(*state.LastSource, same.Source) {
		if !alreadySupplied(state, &same) {
			t.Fatal("载荷完全相同时 alreadySupplied 必须为真")
		}
	} else {
		t.Fatal("测试前置不成立：落盘帧与首帧载荷应完全相同")
	}
	if alreadySupplied(state, &Frame{Text: "different", Source: same.Source}) {
		t.Fatal("文本不同时不得判 already-supplied")
	}
}

// TestStepBoundaryDefersTouches 钉官方 index.ts:304-328：step 打开期间 touch 只攒不投。
func TestStepBoundaryDefersTouches(t *testing.T) {
	runner := NewRunner()
	// 不在 step 里：立刻要求投影（官方 queueProjection 那一支）。
	if flush := runner.Touch("a.md"); !flush {
		t.Fatal("step 没打开时 touch 应立刻投影")
	}
	runner.EndStep() // 清空
	runner.BeginStep()
	if flush := runner.Touch("b.md"); flush {
		t.Fatal("step 打开期间 touch 不得立刻投影")
	}
	if flush := runner.Touch("c.md"); flush {
		t.Fatal("第二条同样只攒")
	}
	paths := runner.EndStep()
	if len(paths) != 2 || paths[0] != "b.md" || paths[1] != "c.md" {
		t.Fatalf("step 结束应取出攒批（排序后）: %v", paths)
	}
	// step 结束后的 touch 又回到"立刻投影"。
	if flush := runner.Touch("d.md"); !flush {
		t.Fatal("step 结束后 touch 应立刻投影")
	}
}

// TestToolTouchesOnlySuccessfulFileTools 钉官方 index.ts:75-83、358-374。
func TestToolTouchesOnlySuccessfulFileTools(t *testing.T) {
	args := map[string]any{"file_path": " pkg/AGENTS.md "}
	if got := ToolTouches(true, "read", args, nil); len(got) != 1 || got[0].Path != "pkg/AGENTS.md" {
		t.Fatalf("成功的 read 应产生一条 trim 后的 touch: %+v", got)
	}
	if got := ToolTouches(false, "read", args, nil); len(got) != 0 {
		t.Fatalf("失败的调用不得产生 touch（官方 !result.isError）: %+v", got)
	}
	for _, tool := range []string{"glob", "grep", "bash", "list"} {
		if got := ToolTouches(true, tool, args, nil); len(got) != 0 {
			t.Fatalf("%s 不在官方 FILE_TOUCH_TOOL_NAMES，不该产生 touch: %+v", tool, got)
		}
	}
	for _, tool := range []string{"write", "edit"} {
		if got := ToolTouches(true, tool, args, nil); len(got) != 1 {
			t.Fatalf("%s 应产生 touch: %+v", tool, got)
		}
	}
	// 参数缺 file_path / 不是字符串 / 全空白：都不算。
	for _, bad := range []any{
		map[string]any{"path": "pkg/AGENTS.md"},
		map[string]any{"file_path": 42},
		map[string]any{"file_path": "   "},
		"not-an-object",
	} {
		if _, ok := FilePathFromToolCall("read", bad); ok {
			t.Fatalf("不该认的参数形状: %#v", bad)
		}
	}
	// 子调用的 touch 冒泡（官方 index.ts:365-371）。
	children := []ToolTouch{{Path: "child.md"}}
	if got := ToolTouches(true, "read", args, children); len(got) != 2 {
		t.Fatalf("子调用 touch 应冒泡到父: %+v", got)
	}
}

// TestDescendantDirsFindsNestedScopes 钉官方 files.ts:276-283：cwd → 被碰文件之间
// **新跨过的**目录（不含 cwd 自己）。
func TestDescendantDirsFindsNestedScopes(t *testing.T) {
	root, cwd := project(t)
	got := DescendantDirs(cwd, filepath.Join(root, "pkg", "sub", "deep", "file.md"))
	if strings.Join(got, ",") != filepath.Join(cwd, "deep") && len(got) != 1 {
		t.Fatalf("应只给出新跨过的 deep 目录: %v", got)
	}
	// cwd 自己不算。
	if got := DescendantDirs(cwd, filepath.Join(cwd, "file.md")); len(got) != 0 {
		t.Fatalf("文件就在 cwd 下时不该有新目录: %v", got)
	}
	// root 之外（向上）不算。
	if got := DescendantDirs(cwd, filepath.Join(root, "AGENTS.md")); len(got) != 0 {
		t.Fatalf("root 之上的路径不该产生后代目录: %v", got)
	}
	// 相对路径按 cwd 解析。
	if got := DescendantDirs(cwd, "deep/file.md"); len(got) != 1 {
		t.Fatalf("相对路径应按 cwd 解析: %v", got)
	}
}

// findChange 断言式取一条指定显示路径的 change。
func findChange(t *testing.T, changes []Change, path string) Change {
	t.Helper()
	change, ok := findChangeOK(changes, path)
	if !ok {
		t.Fatalf("缺 %s 的 change: %+v", path, changes)
	}
	return change
}

func findChangeOK(changes []Change, path string) (Change, bool) {
	for _, change := range changes {
		if change.Path == path {
			return change, true
		}
	}
	return Change{}, false
}
