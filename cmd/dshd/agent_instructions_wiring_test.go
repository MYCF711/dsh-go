package main

// 这组测试钉 `agent-instructions`（官方 #53）的 **durable 层在生产入口（run）的接线**，
// 不是包内单元：`aiInstrRunner` 是 run 入口真正挂的东西（`cmd/dshd/main.go` 的
// `aiInstr.Baseline` / `aiInstr.OnToolCallSettled` / `aiInstr.OnStepEnd`）。
//
// 覆盖：
//
// 挂载条件（默认形状）：`instructionsCarriedByDurableFrame(home)` —— 有 harness home、
// `DSHD_INSTRUCTION_CHAIN` 未关、没给 `--agents`、`DSHD_INSTRUCTION_DURABLE` 未显式关。
// **默认就是挂载**（durable 帧是工作区指令的唯一载体，官方 README.md:34）。
//
//	TestAIInstrBaselineWritesDurableUserMessage —— 落盘形状是 `user/message` +
//	  `source.kind='agent-instructions'` + `baseline:true` + `baselineIdentity` + `changes`，
//	  而且**真的是那条会话的 v4 日志**里的一条事件（不是内存投影）。
//	TestAIInstrBaselineIsIdempotentWithinSession —— 第二次 Baseline 不再落盘（身份相等）。
//	TestAIInstrStepTouchesProjectNestedInstructions —— read/write/edit 成功触摸
//	  `pkg/sub/AGENTS.md` 后，下一次 step 投影落一条 `Additional instructions from:` 帧。
//	TestAIInstrFailedToolDoesNotTouch —— 失败的工具结果不产生 touch、不落帧。
//	TestAIInstrDisabledByEnvKeepsCurrentBytes —— 三条显式出口（链关 / durable 关 /
//	  `--agents`）都**成对**让位：durable 不挂载 **且** 系统提示回到旧形状。
//	TestAIInstrDefaultIsMountedAndPromptCarriesNoInstructions —— 默认形状：挂载 + 系统提示
//	  里一个指令段都没有（位置唯一性的单元面）。
//	TestAIInstrReplaysAfterRestart —— 新进程（新 runner、同一份日志）身份相等 → 不重发基线，
//	  这是 official resume reconcile 的直接证据。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/codecs/v4"
	"dshgo/internal/ainstr"
	"dshgo/internal/session"
)

// aiInstrFixture 造一个临时 home + 带 .git 的项目树 + 一份已打开的会话日志。
//
// `$DSH_AGENTS_HOME` 显式指到一个空临时目录：不钉住的话，第二根会落到**跑测试那台
// 机器**的 `~/.agents/AGENTS.md` 上，断言就随机器漂（同一个病
// `TestPromptWithoutHarnessHomeIgnoresSharedRoot` 在系统提示侧守着）。
func aiInstrFixture(t *testing.T) (home, root, cwd string, logw *session.Log) {
	t.Helper()
	base := t.TempDir()
	home = filepath.Join(base, "home")
	root = filepath.Join(base, "project")
	for _, dir := range []string{home, root, filepath.Join(root, ".git"), filepath.Join(root, "pkg", "sub")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(sharedAgentsHomeEnv, filepath.Join(base, "agents"))
	writeFixtureFile(t, filepath.Join(root, "AGENTS.md"), "root-rules\n")
	logw, err := session.CreateSession(home, root, "sess-ai", "")
	if err != nil {
		t.Fatalf("建会话: %v", err)
	}
	t.Cleanup(func() { _ = logw.Close() })
	return home, root, filepath.Join(root, "pkg", "sub"), logw
}

func writeFixtureFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// aiInstrFramesFromLog 从 v4 日志里取出所有 `source.kind='agent-instructions'` 的帧。
func aiInstrFramesFromLog(t *testing.T, path string) []struct {
	Text   string
	Source ainstr.Source
} {
	t.Helper()
	log, err := v4.ReadFile(path)
	if err != nil {
		t.Fatalf("读日志: %v", err)
	}
	var out []struct {
		Text   string
		Source ainstr.Source
	}
	for _, event := range log.Events {
		if event.Type != "user/message" {
			continue
		}
		text, source, ok := ainstr.FrameFromData(event.Data)
		if !ok {
			continue
		}
		out = append(out, struct {
			Text   string
			Source ainstr.Source
		}{Text: text, Source: source})
	}
	return out
}

// TestAIInstrBaselineWritesDurableUserMessage 钉"基线真的落进这条会话的 v4 日志"。
func TestAIInstrBaselineWritesDurableUserMessage(t *testing.T) {
	t.Setenv("DSHD_INSTRUCTION_CHAIN", "")
	t.Setenv("DSHD_INSTRUCTION_DURABLE", "1")
	home, _, cwd, logw := aiInstrFixture(t)
	runner := newAIInstrRunner(home, defaultWorkspaceBytes)
	if runner == nil {
		t.Fatal("链与 durable 开关都显式打开时，runner 不该是 nil")
	}
	if _, err := runner.Baseline(logw, cwd); err != nil {
		t.Fatalf("基线投影: %v", err)
	}
	frames := aiInstrFramesFromLog(t, logw.Path)
	if len(frames) != 1 {
		t.Fatalf("应落一条基线帧，实际 %d 条", len(frames))
	}
	frame := frames[0]
	if !frame.Source.Baseline || frame.Source.BaselineIdentity == "" {
		t.Fatalf("基线帧缺 baseline/baselineIdentity: %+v", frame.Source)
	}
	if frame.Source.Kind != ainstr.SourceKind || frame.Source.Form != ainstr.SourceForm {
		t.Fatalf("source 形状不对: %+v", frame.Source)
	}
	if !strings.Contains(frame.Text, "root-rules") {
		t.Fatalf("基线正文应包含项目链内容:\n%s", frame.Text)
	}
	if !strings.HasPrefix(frame.Text, "<system-reminder>\n") {
		t.Fatalf("基线应是官方 `<system-reminder>` 帧:\n%s", frame.Text)
	}
	if !strings.Contains(frame.Text, aliveIntroFragment) {
		t.Fatalf("基线帧缺官方总说明:\n%s", frame.Text)
	}
	// changes 里必须有 root AGENTS.md 的 set，且带 digest。
	var found bool
	for _, change := range frame.Source.Changes {
		if change.Action == ainstr.ActionSet && change.Path == "AGENTS.md" && change.Digest == ainstr.Digest("root-rules\n") {
			found = true
		}
	}
	if !found {
		t.Fatalf("changes 缺 root AGENTS.md 的 set+digest: %+v", frame.Source.Changes)
	}
}

// aliveIntroFragment 是官方 `AGENT_INSTRUCTIONS_INTRO` 的第一句（逐字）。
const aliveIntroFragment = "The following workspace instructions may be relevant to your work."

// TestAIInstrBaselineIsIdempotentWithinSession 钉身份相等时**不重发**基线
// （官方 index.ts:131-141 `keepVisibleBaseline`）。
func TestAIInstrBaselineIsIdempotentWithinSession(t *testing.T) {
	t.Setenv("DSHD_INSTRUCTION_CHAIN", "")
	t.Setenv("DSHD_INSTRUCTION_DURABLE", "1")
	home, _, cwd, logw := aiInstrFixture(t)
	runner := newAIInstrRunner(home, defaultWorkspaceBytes)
	if _, err := runner.Baseline(logw, cwd); err != nil {
		t.Fatal(err)
	}
	before := len(aiInstrFramesFromLog(t, logw.Path))
	if _, err := runner.Baseline(logw, cwd); err != nil {
		t.Fatal(err)
	}
	after := len(aiInstrFramesFromLog(t, logw.Path))
	if after != before {
		t.Fatalf("第二次基线不该落盘（身份相等），%d → %d", before, after)
	}
}

// TestAIInstrStepTouchesProjectNestedInstructions 钉 fs touch → 嵌套发现 →
// `Additional instructions from:` 增量帧（官方 index.ts:358-374 + state.ts:302-304）。
func TestAIInstrStepTouchesProjectNestedInstructions(t *testing.T) {
	t.Setenv("DSHD_INSTRUCTION_CHAIN", "")
	t.Setenv("DSHD_INSTRUCTION_DURABLE", "1")
	home, root, cwd, logw := aiInstrFixture(t)
	runner := newAIInstrRunner(home, defaultWorkspaceBytes)
	if _, err := runner.Baseline(logw, cwd); err != nil {
		t.Fatal(err)
	}
	before := len(aiInstrFramesFromLog(t, logw.Path))

	// 模型 read 了 pkg 下的一个新指令文件（`sub` 的父目录是 chain 上的目录，
	// 所以这次 touch 会让 `pkg/` 这一级的候选进入 reconcile）。
	writeFixtureFile(t, filepath.Join(root, "pkg", "AGENTS.md"), "pkg-rules\n")
	runner.PreStep(nil)
	runner.OnToolCallSettled("read", json.RawMessage(`{"file_path":"pkg/AGENTS.md"}`), true)
	runner.OnStepEnd(logw, cwd)

	frames := aiInstrFramesFromLog(t, logw.Path)
	if len(frames) != before+1 {
		t.Fatalf("step 后应落一条增量帧，%d → %d", before, len(frames))
	}
	increment := frames[len(frames)-1]
	if increment.Source.Baseline {
		t.Fatal("增量帧不得标 baseline")
	}
	if !strings.Contains(increment.Text, "Additional instructions from: pkg/AGENTS.md") {
		t.Fatalf("增量应是官方 Additional instructions 分节:\n%s", increment.Text)
	}
	if !strings.Contains(increment.Text, "pkg-rules") {
		t.Fatalf("增量应带正文:\n%s", increment.Text)
	}
}

// TestAIInstrFailedToolDoesNotTouch 钉失败的调用不算 touch（官方 `!result.isError`）。
func TestAIInstrFailedToolDoesNotTouch(t *testing.T) {
	t.Setenv("DSHD_INSTRUCTION_CHAIN", "")
	t.Setenv("DSHD_INSTRUCTION_DURABLE", "1")
	home, root, cwd, logw := aiInstrFixture(t)
	runner := newAIInstrRunner(home, defaultWorkspaceBytes)
	if _, err := runner.Baseline(logw, cwd); err != nil {
		t.Fatal(err)
	}
	before := len(aiInstrFramesFromLog(t, logw.Path))
	writeFixtureFile(t, filepath.Join(root, "pkg", "AGENTS.md"), "pkg-rules\n")
	runner.PreStep(nil)
	runner.OnToolCallSettled("read", json.RawMessage(`{"file_path":"pkg/AGENTS.md"}`), false)
	runner.OnStepEnd(logw, cwd)
	if after := len(aiInstrFramesFromLog(t, logw.Path)); after != before {
		t.Fatalf("失败的调用不得触发投影，%d → %d", before, after)
	}
	// 非文件工具（glob）同样不算。
	runner.PreStep(nil)
	runner.OnToolCallSettled("glob", json.RawMessage(`{"pattern":"**/*.md"}`), true)
	runner.OnStepEnd(logw, cwd)
	if after := len(aiInstrFramesFromLog(t, logw.Path)); after != before {
		t.Fatalf("glob 不在 FILE_TOUCH_TOOL_NAMES，不该触发投影: %d → %d", before, after)
	}
}

// TestAIInstrDisabledByEnvKeepsCurrentBytes 钉**三条显式出口的成对性**。
//
// 原来钉的是什么：`DSHD_INSTRUCTION_CHAIN=0` 不挂载；而 durable 开关**默认关**，
// 所以"生产报文逐字节不变"成立（那些字节当时由系统提示的 cwd 链负责）。
// 为什么它跟着变了：durable 帧现在是**唯一载体**，默认必须挂载。开关一旦只影响
// durable 一侧，"关掉"就会让指令一次都不注（比注两次更糟）；所以每一处让位都必须是
// **成对**的：durable 不挂载 ＋ 系统提示回到旧形状。这条测试因此变强了而不是变弱。
// 现在钉的是什么：三个显式出口各自 —— (a) 不建投影器；(b) 系统提示里**确实**有旧形状
// 的指令段（`DSHD_INSTRUCTION_CHAIN=0` → 单点 `工作区约定（`；
// `DSHD_INSTRUCTION_DURABLE=0` → 官方 project 链帧；`--agents` → 那本单手册），
// 也就是"关掉 durable 不等于丢掉指令"。
func TestAIInstrDisabledByEnvKeepsCurrentBytes(t *testing.T) {
	home, root, cwd, _ := aiInstrFixture(t)

	// ① 链整体关：与 cwd 链同一个开关，durable 跟着让位。
	// 用项目根当 cwd：旧的单点 `WorkspaceRules` 只读 **cwd** 的 AGENTS.md。
	for _, value := range []string{"0", "false", "off", "no"} {
		t.Setenv("DSHD_INSTRUCTION_CHAIN", value)
		t.Setenv("DSHD_INSTRUCTION_DURABLE", "")
		oldRules := *rulesPath
		*rulesPath = ""
		if runner := newAIInstrRunner(home, defaultWorkspaceBytes); runner != nil {
			*rulesPath = oldRules
			t.Fatalf("DSHD_INSTRUCTION_CHAIN=%q 时不该挂载 durable 层", value)
		}
		prompt, err := buildSystemPromptWithHome(emptyRegistry{}, "m", root, home, defaultWorkspaceBytes)
		*rulesPath = oldRules
		if err != nil {
			t.Fatalf("装配系统提示: %v", err)
		}
		if !strings.Contains(prompt, "工作区约定（") || !strings.Contains(prompt, "root-rules") {
			t.Fatalf("链关掉后系统提示必须回到旧形状（单点 工作区约定 + 正文）：\n%s", prompt)
		}
	}

	// ② 只关 durable 层（回滚开关）：系统提示必须把指令接回去，否则等于一次都不注。
	for _, value := range []string{"0", "false", "off", "no"} {
		t.Setenv("DSHD_INSTRUCTION_CHAIN", "")
		t.Setenv("DSHD_INSTRUCTION_DURABLE", value)
		oldRules := *rulesPath
		*rulesPath = ""
		if runner := newAIInstrRunner(home, defaultWorkspaceBytes); runner != nil {
			*rulesPath = oldRules
			t.Fatalf("DSHD_INSTRUCTION_DURABLE=%q 时不该挂载 durable 层", value)
		}
		prompt, err := buildSystemPromptWithHome(emptyRegistry{}, "m", cwd, home, defaultWorkspaceBytes)
		*rulesPath = oldRules
		if err != nil {
			t.Fatalf("装配系统提示: %v", err)
		}
		if !strings.Contains(prompt, "root-rules") {
			t.Fatalf("durable 关掉后系统提示必须重新携带项目链（否则指令一次都不注）：\n%s", prompt)
		}
	}

	// ③ `--agents` 显式单手册：自动发现链让位，durable 不挂载，那本手册照旧进系统提示。
	t.Setenv("DSHD_INSTRUCTION_CHAIN", "")
	t.Setenv("DSHD_INSTRUCTION_DURABLE", "")
	manual := filepath.Join(t.TempDir(), "WORKER.md")
	writeFixtureFile(t, manual, "single-manual-rules\n")
	oldRules := *rulesPath
	*rulesPath = manual
	defer func() { *rulesPath = oldRules }()
	if runner := newAIInstrRunner(home, defaultWorkspaceBytes); runner != nil {
		t.Fatal("给了 --agents 单手册时不该挂 durable 自动发现链（两本手册会同时出现）")
	}
	prompt, err := buildSystemPromptWithHome(emptyRegistry{}, "m", cwd, home, defaultWorkspaceBytes)
	if err != nil {
		t.Fatalf("装配系统提示: %v", err)
	}
	if !strings.Contains(prompt, "single-manual-rules") {
		t.Fatalf("--agents 指定的手册没进系统提示：\n%s", prompt)
	}
}

// TestAIInstrDefaultIsMountedAndPromptCarriesNoInstructions 钉**默认形状**（这条以前钉的是
// 反面：默认不挂载、生产字节不变）。
//
// 原来钉的是什么：`DSHD_INSTRUCTION_DURABLE` 未设 ⇒ 不挂载。
// 为什么它跟着变了：durable 帧是官方形状的唯一载体（README.md:34 "one durable baseline
// message"），默认必须挂载；同时系统提示里必须**一个指令段都没有**——否则同一份
// AGENTS.md 在 system 与 user 两个角色里各一份，计数就是 2。
// 现在钉的是什么：默认（两个开关都不设、没有 `--agents`）⇒ 挂载，且系统提示里
// 0 处 `root-rules` / 0 处 `工作区约定（` / 0 处 `全局约定（` / 0 处 `Instructions from:`。
func TestAIInstrDefaultIsMountedAndPromptCarriesNoInstructions(t *testing.T) {
	home, _, cwd, _ := aiInstrFixture(t)
	if err := os.WriteFile(filepath.Join(home, "AGENTS.md"), []byte("home-rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DSHD_INSTRUCTION_CHAIN", "")
	t.Setenv("DSHD_INSTRUCTION_DURABLE", "")
	oldRules := *rulesPath
	*rulesPath = ""
	t.Cleanup(func() { *rulesPath = oldRules })

	if runner := newAIInstrRunner(home, defaultWorkspaceBytes); runner == nil {
		t.Fatal("默认形状下 durable 层必须挂载（它是指令的唯一载体）")
	}
	prompt, err := buildSystemPromptWithHome(emptyRegistry{}, "m", cwd, home, defaultWorkspaceBytes)
	if err != nil {
		t.Fatalf("装配系统提示: %v", err)
	}
	for _, marker := range []string{"root-rules", "home-rules", "工作区约定（", "全局约定（", "Instructions from:", "<system-reminder>"} {
		if strings.Contains(prompt, marker) {
			t.Fatalf("系统提示里出现了指令内容 %q（指令必须只在 durable 帧里）：\n%s", marker, prompt)
		}
	}
	if !strings.Contains(prompt, "Runtime: dsh-go") {
		t.Fatalf("抑制指令段把运行时事实一起吃掉了：\n%s", prompt)
	}
}

// TestAIInstrReplaysAfterRestart 钉 resume reconcile：**新进程**（新 runner、同一份日志）
// 身份相等 → 不重发基线；文件被改过 → 发 `Updated instructions from:`。
func TestAIInstrReplaysAfterRestart(t *testing.T) {
	t.Setenv("DSHD_INSTRUCTION_CHAIN", "")
	t.Setenv("DSHD_INSTRUCTION_DURABLE", "1")
	home, root, cwd, logw := aiInstrFixture(t)
	first := newAIInstrRunner(home, defaultWorkspaceBytes)
	if _, err := first.Baseline(logw, cwd); err != nil {
		t.Fatal(err)
	}
	baselineFrames := len(aiInstrFramesFromLog(t, logw.Path))

	// "重启"：同一份日志、新的 runner 实例（进程内状态全丢）。
	second := newAIInstrRunner(home, defaultWorkspaceBytes)
	if _, err := second.Baseline(logw, cwd); err != nil {
		t.Fatal(err)
	}
	if after := len(aiInstrFramesFromLog(t, logw.Path)); after != baselineFrames {
		t.Fatalf("重启后身份相等，不该重发基线：%d → %d", baselineFrames, after)
	}

	// 文件内容变了：增量帧必须是 replace 形状。
	writeFixtureFile(t, filepath.Join(root, "AGENTS.md"), "root-rules-v2\n")
	second.PreStep(nil)
	second.OnToolCallSettled("edit", json.RawMessage(`{"file_path":"AGENTS.md"}`), true)
	second.OnStepEnd(logw, cwd)
	frames := aiInstrFramesFromLog(t, logw.Path)
	last := frames[len(frames)-1]
	if !strings.Contains(last.Text, "Updated instructions from: AGENTS.md") {
		t.Fatalf("改内容应落 Updated instructions 分节:\n%s", last.Text)
	}
	if !strings.Contains(last.Text, "root-rules-v2") {
		t.Fatalf("更新帧应带新正文:\n%s", last.Text)
	}
}

// TestAIInstrPromptInjectionIsUserRole 钉"注入位置/格式"：官方把指令作为 **user 角色**
// 消息插进会话（`createUserMessage`），不是 system 消息、也不是 tool 消息。
func TestAIInstrPromptInjectionIsUserRole(t *testing.T) {
	t.Setenv("DSHD_INSTRUCTION_CHAIN", "")
	t.Setenv("DSHD_INSTRUCTION_DURABLE", "1")
	home, _, cwd, logw := aiInstrFixture(t)
	runner := newAIInstrRunner(home, defaultWorkspaceBytes)
	if _, err := runner.Baseline(logw, cwd); err != nil {
		t.Fatal(err)
	}
	log, err := v4.ReadFile(logw.Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range log.Events {
		if event.Type != "user/message" {
			continue
		}
		text, source, ok := ainstr.FrameFromData(event.Data)
		if !ok {
			continue
		}
		var payload struct {
			Role string `json:"role"`
		}
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Role != "user" {
			t.Fatalf("指令帧必须是 user 角色（官方 createUserMessage），得到 %q", payload.Role)
		}
		if !strings.Contains(text, "<system-reminder>") {
			t.Fatalf("指令帧正文必须包官方 `<system-reminder>`：\n%s", text)
		}
		if !source.Baseline {
			t.Fatal("首帧应是基线")
		}
		return
	}
	t.Fatal("日志里没有 agent-instructions 帧")
}

// TestAIInstrFinishTurnClosesAbandonedStep 钉"一轮结束时 step 状态必须对账"。
//
// 背景（真实退路，不是假想）：`agent.Loop` 只在**正常收工**的两条分支发 `step/end`
// （`internal/agent/loop.go` 的 `len(resp.ToolCalls)==0` 与工具批次成功之后）。
// 模型调用失败、空响应、`finish=length` 当失败、工具参数不是 JSON object、取消——
// 这些出口都直接 return，没有 `step/end`。于是 `BeginStep` 置上的 stepOpen 留在 true，
// 此后本会话每次 `Touch` 都在攒批而**永不投影**，直到下一次成功的 step/end。
// 表现是"改了 AGENTS.md，界面上一直不刷新"，日志里一句错都没有。
//
// 变异点：把 `FinishTurn` 里的 `AbortStep` 换成 `EndStep` 之外的任何"什么都不做"，
// 或删掉两个生产入口上的调用，本测试在"StepOpen 必须复位"与"补一次投影"两处红。
func TestAIInstrFinishTurnClosesAbandonedStep(t *testing.T) {
	t.Setenv("DSHD_INSTRUCTION_CHAIN", "")
	t.Setenv("DSHD_INSTRUCTION_DURABLE", "1")
	home, root, cwd, logw := aiInstrFixture(t)
	runner := newAIInstrRunner(home, defaultWorkspaceBytes)
	if runner == nil {
		t.Fatal("链与 durable 开关都显式打开时，runner 不该是 nil")
	}
	if _, err := runner.Baseline(logw, cwd); err != nil {
		t.Fatal(err)
	}
	before := len(aiInstrFramesFromLog(t, logw.Path))

	// 走一步：读了一个嵌套指令文件，然后这一轮**异常收尾**（没有 step/end）。
	writeFixtureFile(t, filepath.Join(root, "pkg", "AGENTS.md"), "pkg-rules\n")
	// PreStep 返回的是包装后的钩子，必须**调用**它才算"一步开始"（生产由 Loop 调）。
	preStep := runner.PreStep(nil)
	if err := preStep(context.Background(), 1); err != nil {
		t.Fatalf("PreStep: %v", err)
	}
	runner.OnToolCallSettled("read", json.RawMessage(`{"file_path":"pkg/AGENTS.md"}`), true)
	if !runner.runner.StepOpen() {
		t.Fatal("PreStep 之后 step 必须是打开状态（这是 AbortStep 要收的那一位）")
	}
	if during := len(aiInstrFramesFromLog(t, logw.Path)); during != before {
		t.Fatalf("step 打开期间不该投影，%d → %d", before, during)
	}

	runner.FinishTurn(logw, cwd)
	if runner.runner.StepOpen() {
		t.Fatal("FinishTurn 必须复位 stepOpen：不复位就是本会话之后再也不刷新指令投影（静默卡死）")
	}
	after := aiInstrFramesFromLog(t, logw.Path)
	if len(after) != before+1 {
		t.Fatalf("退路收口必须把这批 touch 投影掉（不丢工作），%d → %d", before, len(after))
	}
	if !strings.Contains(after[len(after)-1].Text, "pkg-rules") {
		t.Fatalf("补投影的帧应带真实正文:\n%s", after[len(after)-1].Text)
	}

	// 幂等：正常收尾后再兜一次不能多写一条。
	runner.FinishTurn(logw, cwd)
	if again := len(aiInstrFramesFromLog(t, logw.Path)); again != len(after) {
		t.Fatalf("FinishTurn 必须幂等（正常收尾后是空操作），%d → %d", len(after), again)
	}
}

// TestAIInstrFinishTurnNoOpOnCleanStepEnd 钉正常路径不受影响：
// `step/end` 已经收口过时，turn 收尾不能再补一条帧（否则每轮多一条重复投影）。
func TestAIInstrFinishTurnNoOpOnCleanStepEnd(t *testing.T) {
	t.Setenv("DSHD_INSTRUCTION_CHAIN", "")
	t.Setenv("DSHD_INSTRUCTION_DURABLE", "1")
	home, root, cwd, logw := aiInstrFixture(t)
	runner := newAIInstrRunner(home, defaultWorkspaceBytes)
	if _, err := runner.Baseline(logw, cwd); err != nil {
		t.Fatal(err)
	}
	before := len(aiInstrFramesFromLog(t, logw.Path))
	writeFixtureFile(t, filepath.Join(root, "pkg", "AGENTS.md"), "pkg-rules\n")
	preStep := runner.PreStep(nil)
	if err := preStep(context.Background(), 1); err != nil {
		t.Fatalf("PreStep: %v", err)
	}
	runner.OnToolCallSettled("read", json.RawMessage(`{"file_path":"pkg/AGENTS.md"}`), true)
	runner.OnStepEnd(logw, cwd)
	clean := len(aiInstrFramesFromLog(t, logw.Path))
	if clean != before+1 {
		t.Fatalf("正常 step/end 应落一条，%d → %d", before, clean)
	}
	runner.FinishTurn(logw, cwd)
	if after := len(aiInstrFramesFromLog(t, logw.Path)); after != clean {
		t.Fatalf("step/end 已收口时 turn 收尾必须是空操作，%d → %d", clean, after)
	}
}

// TestACPClosedSessionForgetsAIInstrRunner 钉"会话关闭后不再持有它的指令投影器"。
//
// 两个后果都不是"多占一点内存"：
//   - 长跑的 ACP 进程给每个开过的会话留一份 Runner，只增不减（`Close` 已经摘
//     logs/hist/turns，唯独漏了这一张）；
//   - 同一个 session id 关掉再打开（Resume 用原 id 是常态）会命中旧 Runner，
//     上一轮异常收尾留下的 `stepOpen=true` 与 `pending` 直接带进新会话——
//     新会话从第一拍起就不刷新指令投影，而且没有任何日志。
//
// 变异点：删掉 `acpRunner.Close` 里的 `forgetAIInstr` 调用（或把它写成空操作），
// 本测试在"表里必须摘掉""再打开必须是新 runner"两处红。
func TestACPClosedSessionForgetsAIInstrRunner(t *testing.T) {
	t.Setenv("DSHD_INSTRUCTION_CHAIN", "")
	t.Setenv("DSHD_INSTRUCTION_DURABLE", "1")
	runner := &acpRunner{home: t.TempDir(), workspaceBytes: defaultWorkspaceBytes}
	first := runner.aiInstrFor("sess-1")
	if first == nil {
		t.Fatal("链与 durable 都显式打开时 aiInstrFor 不该返回 nil")
	}
	if again := runner.aiInstrFor("sess-1"); again != first {
		t.Fatal("同一会话必须复用同一份投影器（否则攒批与 version 缓存各说各话）")
	}
	if err := runner.Close("sess-1"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, ok := runner.aiInstr["sess-1"]; ok {
		t.Fatal("Close 之后必须摘掉这张表的条目（否则长跑只增不减）")
	}
	reopened := runner.aiInstrFor("sess-1")
	if reopened == first {
		t.Fatal("重新打开的会话必须是新投影器：旧 Runner 的 stepOpen/pending 会跨生命周期泄漏")
	}
}
