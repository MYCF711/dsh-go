package main

// 这组测试钉 `agent-instructions`（官方 #53）的 **durable 层在生产入口（run）的接线**，
// 不是包内单元：`aiInstrRunner` 是 run 入口真正挂的东西（`cmd/dshd/main.go` 的
// `aiInstr.Baseline` / `aiInstr.OnToolCallSettled` / `aiInstr.OnStepEnd`）。
//
// 覆盖：
//
// 挂载条件：`DSHD_INSTRUCTION_CHAIN` 未关 **且** `DSHD_INSTRUCTION_DURABLE=1`
// （durable 开关默认关，理由见 aiInstrDurableEnabled：与系统提示里的 cwd 链重复）。
//
//	TestAIInstrBaselineWritesDurableUserMessage —— 落盘形状是 `user/message` +
//	  `source.kind='agent-instructions'` + `baseline:true` + `baselineIdentity` + `changes`，
//	  而且**真的是那条会话的 v4 日志**里的一条事件（不是内存投影）。
//	TestAIInstrBaselineIsIdempotentWithinSession —— 第二次 Baseline 不再落盘（身份相等）。
//	TestAIInstrStepTouchesProjectNestedInstructions —— read/write/edit 成功触摸
//	  `pkg/sub/AGENTS.md` 后，下一次 step 投影落一条 `Additional instructions from:` 帧。
//	TestAIInstrFailedToolDoesNotTouch —— 失败的工具结果不产生 touch、不落帧。
//	TestAIInstrDisabledByEnvKeepsCurrentBytes —— `DSHD_INSTRUCTION_CHAIN=0` 时整体不挂载。
//	TestAIInstrReplaysAfterRestart —— 新进程（新 runner、同一份日志）身份相等 → 不重发基线，
//	  这是 official resume reconcile 的直接证据。

import (
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
	home, root, cwd, logw := aiInstrFixture(t)
	runner := newAIInstrRunner(home)
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
	_ = root
}

// aliveIntroFragment 是官方 `AGENT_INSTRUCTIONS_INTRO` 的第一句（逐字）。
const aliveIntroFragment = "The following workspace instructions may be relevant to your work."

// TestAIInstrBaselineIsIdempotentWithinSession 钉身份相等时**不重发**基线
// （官方 index.ts:131-141 `keepVisibleBaseline`）。
func TestAIInstrBaselineIsIdempotentWithinSession(t *testing.T) {
	t.Setenv("DSHD_INSTRUCTION_CHAIN", "")
	t.Setenv("DSHD_INSTRUCTION_DURABLE", "1")
	home, _, cwd, logw := aiInstrFixture(t)
	runner := newAIInstrRunner(home)
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
	runner := newAIInstrRunner(home)
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
	runner := newAIInstrRunner(home)
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

// TestAIInstrDisabledByEnvKeepsCurrentBytes 钉 `DSHD_INSTRUCTION_CHAIN=0` 时整体不挂载。
func TestAIInstrDisabledByEnvKeepsCurrentBytes(t *testing.T) {
	for _, value := range []string{"0", "false", "off", "no"} {
		t.Setenv("DSHD_INSTRUCTION_CHAIN", value)
		t.Setenv("DSHD_INSTRUCTION_DURABLE", "1")
		if runner := newAIInstrRunner(t.TempDir()); runner != nil {
			t.Fatalf("DSHD_INSTRUCTION_CHAIN=%q 时不该挂载 durable 层", value)
		}
	}
	// durable 开关默认关：没有显式打开时一个钩子都不接（生产报文逐字节不变）。
	t.Setenv("DSHD_INSTRUCTION_CHAIN", "")
	t.Setenv("DSHD_INSTRUCTION_DURABLE", "")
	if runner := newAIInstrRunner(t.TempDir()); runner != nil {
		t.Fatal("DSHD_INSTRUCTION_DURABLE 默认关，不该挂载（生产字节必须保持改动前）")
	}
}

// TestAIInstrReplaysAfterRestart 钉 resume reconcile：**新进程**（新 runner、同一份日志）
// 身份相等 → 不重发基线；文件被改过 → 发 `Updated instructions from:`。
func TestAIInstrReplaysAfterRestart(t *testing.T) {
	t.Setenv("DSHD_INSTRUCTION_CHAIN", "")
	t.Setenv("DSHD_INSTRUCTION_DURABLE", "1")
	home, root, cwd, logw := aiInstrFixture(t)
	first := newAIInstrRunner(home)
	if _, err := first.Baseline(logw, cwd); err != nil {
		t.Fatal(err)
	}
	baselineFrames := len(aiInstrFramesFromLog(t, logw.Path))

	// "重启"：同一份日志、新的 runner 实例（进程内状态全丢）。
	second := newAIInstrRunner(home)
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
	runner := newAIInstrRunner(home)
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
