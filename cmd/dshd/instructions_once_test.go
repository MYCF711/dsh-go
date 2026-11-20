package main

// 本文件是官方 #53 `agent-instructions` **唯一载体**的硬证据（task-126 的核心证明）：
// 一次模型请求里，每一层 AGENTS.md 的正文**恰好出现一次**，而且只在 durable
// `user/message` 帧里 —— 不在 system prompt。
//
// 官方契约（`@deepseek-ai/dsh-agent-instructions/README.md:34`，逐字）：
//
//	"The first request includes one durable baseline message with the user-global chain —
//	 `$DSH_HOME/AGENTS.md`, then `<agentsHome>/AGENTS.md` — followed by the project chain:
//	 every existing candidate file from the project root down to the session working
//	 directory, in broad-to-specific order."
//
// 四条硬判据全部**在真实请求体上**判（不是看中间变量，也不是看渲染函数的返回值）：
//
//  1. 不重复：五层正文标记在请求 JSON 里各自出现次数 == 1；
//  2. 不丢：五层（`$DSH_HOME`、`<agentsHome>`、project root、pkg、pkg/sub）全在，
//     而且全在**同一条**消息里（= 一次注入，不是分几次注）；
//  3. 位置对：system 消息里 0 处；携带指令的那条消息**就是** v4 日志里那条 durable
//     `user/message`（source.kind='agent-instructions' + baseline:true）的正文，
//     逐字节相等；日志里这样的帧恰好 1 条；
//  4. resume 后仍 1 份：同一会话再跑一轮（身份相等 → 不重发基线），新请求里仍然各 1 处，
//     日志里仍然恰好 1 条基线帧。
//
// 为什么起点是"真实请求"而不是"渲染结果"：唯一载体这件事，只有"发出去的报文里
// 出现几次"能证明。渲染层可能渲染得对，而装配层同时把同一份内容又放进 system（那正是
// 本单修掉的病：默认关的 durable 层 + 默认开的系统提示 cwd 链）。任何一边单独测都是绿的。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"dshgo/codecs/v4"
	"dshgo/internal/agent"
	"dshgo/internal/ainstr"
	"dshgo/internal/modes"
	"dshgo/internal/session"
	"dshgo/internal/spi"
)

// 五层指令的正文标记。**互不为子串**：计数判据才有意义
// （否则 `pkg` 的正文会顺带命中 `pkg/sub` 的内容，1 处也会数成 2 处）。
const (
	instrOnceRoot   = "ROOT-RULES-ONCE-4b1c"
	instrOncePkg    = "PKG-RULES-ONCE-4b1c"
	instrOnceSub    = "SUB-RULES-ONCE-4b1c"
	instrOnceHome   = "HARNESS-HOME-RULES-ONCE-4b1c"
	instrOnceAgents = "SHARED-AGENTS-RULES-ONCE-4b1c"
)

// instrOnceLayers 是"每一层一个标记"，顺序 = 官方 broad-to-specific（用户全局在前）。
var instrOnceLayers = []struct {
	name    string
	marker  string
	display string // 官方帧里那条 `Instructions from: <display>` 的显示路径
}{
	{"$DSH_HOME", instrOnceHome, "$DSH_HOME/AGENTS.md"},
	{"<agentsHome>", instrOnceAgents, "$DSH_AGENTS_HOME/AGENTS.md"},
	{"project root", instrOnceRoot, "AGENTS.md"},
	{"pkg", instrOncePkg, "pkg/AGENTS.md"},
	{"pkg/sub", instrOnceSub, "pkg/sub/AGENTS.md"},
}

// instructionsOnceFixture 是一个"每层都有 AGENTS.md"的真实工作区。
type instructionsOnceFixture struct {
	home        string
	projectRoot string
	cwd         string
	manifest    string
	sessionID   string
}

// newInstructionsOnceFixture 造 fixture：project root（带 `.git`）→ pkg → pkg/sub，
// 加上 harness home 与 shared agents root。cwd 取最深的 `pkg/sub`，这样一次基线
// 必须把五层**全部**带上（少一层就是"丢"，多一层就是"重复"）。
func newInstructionsOnceFixture(t *testing.T, sessionID string) instructionsOnceFixture {
	t.Helper()
	base := t.TempDir()
	project := filepath.Join(base, "project")
	home := filepath.Join(base, "home")
	agents := filepath.Join(base, "agents")
	cwd := filepath.Join(project, "pkg", "sub")
	for _, dir := range []string{
		filepath.Join(project, ".git"), filepath.Join(project, "pkg", "sub"),
		home, agents,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(project, "AGENTS.md"), instrOnceRoot+"\n")
	write(filepath.Join(project, "pkg", "AGENTS.md"), instrOncePkg+"\n")
	write(filepath.Join(cwd, "AGENTS.md"), instrOnceSub+"\n")
	write(filepath.Join(home, "AGENTS.md"), instrOnceHome+"\n")
	write(filepath.Join(agents, "AGENTS.md"), instrOnceAgents+"\n")
	manifest := filepath.Join(project, "dsh.toml")
	write(manifest, "# 指令唯一载体探针的最小 manifest：cmdRun 只需要 tools 能力\n[[module]]\nid = \"tools\"\n")

	// 两个根都是**进程策略**（官方 README.md:40）：这里显式钉住，断言才不随机器漂。
	t.Setenv(sharedAgentsHomeEnv, agents)
	t.Setenv(defaultWorkDirEnv, cwd)
	// 宿主的三条出口都保持默认（链开、durable 开、没有 --agents 单手册）：
	// 这是**默认形状**，探针要证明的就是它。
	t.Setenv("DSHD_INSTRUCTION_CHAIN", "")
	t.Setenv("DSHD_INSTRUCTION_DURABLE", "")
	oldRules := *rulesPath
	*rulesPath = ""
	t.Cleanup(func() { *rulesPath = oldRules })

	// 会话先建好（真实会话文件）：`--session` 续的是**同一个**会话，
	// resume 那条判据才有意义。
	lg, err := session.CreateSession(home, cwd, sessionID, "default")
	if err != nil {
		t.Fatalf("预建会话: %v", err)
	}
	if err := lg.Close(); err != nil {
		t.Fatalf("关预建会话: %v", err)
	}
	return instructionsOnceFixture{home: home, projectRoot: project, cwd: cwd, manifest: manifest, sessionID: sessionID}
}

// runOnceForWire 用**真实 run 入口**（cmdRun）跑一轮，返回假网关收到的请求体。
//
// 走 cmdRun 而不是自己拼 messages：装配点（系统提示 + durable 基线 + hist 顺序）全在
// 那条路上，任何一处漏改都会在这里现形。
func (f instructionsOnceFixture) runOnceForWire(t *testing.T, prompt string) string {
	t.Helper()
	var mu sync.Mutex
	var bodies []string
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("读请求体: %v", err)
		}
		mu.Lock()
		bodies = append(bodies, string(raw))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, runPlainText)
	}))
	defer gateway.Close()

	err := cmdRun([]string{
		"--home", f.home, "--session", f.sessionID,
		"--prompt", prompt, "--policy", "allow", "--manifest", f.manifest,
		"--base", gateway.URL, "--model", "instructions-once-model",
	})
	if err != nil {
		t.Fatalf("run 入口这一轮失败: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("这一轮应恰好 1 次模型请求，实际 %d 次", len(bodies))
	}
	return bodies[0]
}

// wireRequestMessage 是请求体里的一条消息（`content` 保持原始 JSON：
// 纯文本是 string，带图/分段时是数组——两种都要能数）。
type wireRequestMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// wireRequest 只解我们关心的字段。
type wireRequest struct {
	Messages []wireRequestMessage `json:"messages"`
}

// parseWireRequest 解出消息列表；解不开就当场失败（报文格式变了必须让人看见）。
func parseWireRequest(t *testing.T, body string) wireRequest {
	t.Helper()
	var parsed wireRequest
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("请求体不是可解析的 JSON: %v", err)
	}
	if len(parsed.Messages) == 0 {
		t.Fatalf("请求体里没有 messages: %s", body)
	}
	return parsed
}

// systemText 拼出所有 system 消息的文本（正常只有一条）。
func systemText(parsed wireRequest) string {
	var b strings.Builder
	for _, message := range parsed.Messages {
		if message.Role == "system" {
			b.Write(message.Content)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// instructionBearingMessages 返回"正文里带指令标记"的 user 消息（下标 + 文本）。
//
// 判据用**文本**而不是角色：官方把指令放在 `user/message` 里，但那只是位置约定；
// 这里要证明的是"这些正文只在一条消息里出现过"，角色断言单独再做一次。
func instructionBearingMessages(t *testing.T, parsed wireRequest) []string {
	t.Helper()
	var out []string
	for _, message := range parsed.Messages {
		var text string
		if err := json.Unmarshal(message.Content, &text); err != nil {
			continue // 非纯文本消息（数组形态）不在本探针范围
		}
		for _, layer := range instrOnceLayers {
			if strings.Contains(text, layer.marker) {
				out = append(out, text)
				break
			}
		}
	}
	return out
}

// baselineFrames 从 v4 日志里取出所有 agent-instructions 帧（类型 + 正文 + source）。
func baselineFrames(t *testing.T, path string) []struct {
	EventType string
	Text      string
	Source    ainstr.Source
} {
	t.Helper()
	log, err := v4.ReadFile(path)
	if err != nil {
		t.Fatalf("读会话日志: %v", err)
	}
	var out []struct {
		EventType string
		Text      string
		Source    ainstr.Source
	}
	for _, event := range log.Events {
		text, source, ok := ainstr.FrameFromData(event.Data)
		if !ok {
			continue
		}
		out = append(out, struct {
			EventType string
			Text      string
			Source    ainstr.Source
		}{EventType: event.Type, Text: text, Source: source})
	}
	return out
}

// TestInstructionsInjectedExactlyOnce 是 task-126 的核心证明（首轮）。
func TestInstructionsInjectedExactlyOnce(t *testing.T) {
	f := newInstructionsOnceFixture(t, "instr-once-first")
	body := f.runOnceForWire(t, "先看看这个工作区的规矩")
	parsed := parseWireRequest(t, body)

	// ① 不重复：每一层正文在**整条请求**里恰好出现一次。
	for _, layer := range instrOnceLayers {
		if got := strings.Count(body, layer.marker); got != 1 {
			t.Fatalf("%s 的正文在请求里出现 %d 次，期望恰好 1 次（%s）\n请求体:\n%s",
				layer.name, got, layer.marker, body)
		}
		if !strings.Contains(body, "Instructions from: "+layer.display) {
			t.Fatalf("%s 的官方式标题 `Instructions from: %s` 不在请求里（= 这一层丢了）",
				layer.name, layer.display)
		}
	}

	// ② 不丢 + 一次注入：五层全在**同一条**消息里。
	carriers := instructionBearingMessages(t, parsed)
	if len(carriers) != 1 {
		t.Fatalf("带指令正文的消息有 %d 条，期望恰好 1 条（多一条 = 注了两次）", len(carriers))
	}
	for _, layer := range instrOnceLayers {
		if strings.Count(carriers[0], layer.marker) != 1 {
			t.Fatalf("唯一载体里 %s 出现次数不是 1：\n%s", layer.name, carriers[0])
		}
	}

	// ③ 位置对：system 里 0 处。
	if sys := systemText(parsed); sys != "" {
		for _, layer := range instrOnceLayers {
			if strings.Contains(sys, layer.marker) {
				t.Fatalf("system 消息里出现了 %s 的正文（指令必须在 durable 帧里）：\n%s", layer.name, sys)
			}
		}
		for _, forbidden := range []string{"Instructions from:", "<system-reminder>", "工作区约定（", "全局约定（"} {
			if strings.Contains(sys, forbidden) {
				t.Fatalf("system 消息里出现了指令容器 %q（系统提示不该携带任何指令段）：\n%s", forbidden, sys)
			}
		}
	}

	// ③b 载体就是那条 durable 帧：日志里恰好 1 条基线帧，且正文与请求里那条逐字节相等。
	path, err := session.ResolvePath(f.home, f.cwd, f.sessionID)
	if err != nil {
		t.Fatalf("解析会话文件: %v", err)
	}
	frames := baselineFrames(t, path)
	if len(frames) != 1 {
		t.Fatalf("v4 日志里 agent-instructions 帧有 %d 条，期望恰好 1 条", len(frames))
	}
	frame := frames[0]
	if frame.EventType != "user/message" {
		t.Fatalf("指令帧的事件类型是 %q，官方是 durable user/message", frame.EventType)
	}
	if !frame.Source.Baseline || frame.Source.BaselineIdentity == "" {
		t.Fatalf("指令帧不是基线帧: %+v", frame.Source)
	}
	if frame.Text != carriers[0] {
		t.Fatalf("请求里的载体与 durable 帧正文不一致（说明请求带的不是那条帧）\n--- 帧 ---\n%s\n--- 请求 ---\n%s",
			frame.Text, carriers[0])
	}
}

// TestInstructionsStillOnceAfterResume 是硬判据 4：**resume 后仍 1 份**。
//
// resume 的正确性来自官方的身份对账（`workspaceBaselineIdentity`）：第二轮身份相等 ⇒
// 不重发基线，历史里的那条帧继续当载体。所以新请求里仍各 1 处、日志里仍恰好 1 条基线帧。
// 这条同时否掉两个常见退化：每轮重发基线（prompt 翻倍）与 resume 后指令消失。
func TestInstructionsStillOnceAfterResume(t *testing.T) {
	f := newInstructionsOnceFixture(t, "instr-once-resume")
	first := f.runOnceForWire(t, "第一轮：先看看规矩")
	if got := strings.Count(first, instrOnceRoot); got != 1 {
		t.Fatalf("首轮就已经不对：root 正文出现 %d 次", got)
	}

	second := f.runOnceForWire(t, "第二轮：接着干")
	for _, layer := range instrOnceLayers {
		if got := strings.Count(second, layer.marker); got != 1 {
			t.Fatalf("resume 后 %s 的正文出现 %d 次，期望恰好 1 次（%s）\n请求体:\n%s",
				layer.name, got, layer.marker, second)
		}
	}
	if carriers := instructionBearingMessages(t, parseWireRequest(t, second)); len(carriers) != 1 {
		t.Fatalf("resume 后带指令正文的消息有 %d 条，期望恰好 1 条", len(carriers))
	}
	if sys := systemText(parseWireRequest(t, second)); strings.Contains(sys, instrOnceRoot) {
		t.Fatalf("resume 后 system 消息里又出现了指令正文：\n%s", sys)
	}

	path, err := session.ResolvePath(f.home, f.cwd, f.sessionID)
	if err != nil {
		t.Fatalf("解析会话文件: %v", err)
	}
	frames := baselineFrames(t, path)
	if len(frames) != 1 {
		t.Fatalf("两轮之后 agent-instructions 帧有 %d 条，期望仍然恰好 1 条（身份相等不该重发基线）", len(frames))
	}
	if !frames[0].Source.Baseline {
		t.Fatalf("唯一那条指令帧不是基线帧: %+v", frames[0].Source)
	}
}

// TestInstructionsInjectedExactlyOnceACPEntry 把同一份证明钉在**另一个生产入口**（ACP）。
//
// 为什么两个入口都要钉：run 与 acp 各自建系统提示、各自建 durable 投影器
// （`aiInstrFor` 是每会话一份）。只在 run 上证明"恰好一次"，acp 那条路完全可能
// 一边开一边关——而 GUI/TUI 之外的客户端走的正是 acp。判据与 run 那条逐条相同：
// 五层各 1 次、system 0 处、日志里恰好 1 条基线帧。
func TestInstructionsInjectedExactlyOnceACPEntry(t *testing.T) {
	f := newInstructionsOnceFixture(t, "instr-once-acp")

	var mu sync.Mutex
	var bodies []string
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("读请求体: %v", err)
		}
		mu.Lock()
		bodies = append(bodies, string(raw))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, runPlainText)
	}))
	defer gateway.Close()

	runner := newACPRunner(
		&fakeReg{tools: map[string]spi.Tool{"w": &fakeTool{name: "w", caps: spi.Caps{Writes: true}}}},
		&recorder{}, gateway.URL, "m", f.home, 2)
	t.Cleanup(func() { _ = runner.Shutdown() })
	runner.ctxWindow = 0
	runner.modeID = modes.DefaultID
	runner.ask = func(context.Context, string, string, string) (bool, error) { return true, nil }

	if _, _, err := runner.Prompt(context.Background(), "instr-once-acp-session", f.cwd,
		"先看看这个工作区的规矩", func(agent.Event) error { return nil }); err != nil {
		t.Fatalf("ACP 这一轮失败: %v", err)
	}
	mu.Lock()
	captured := append([]string(nil), bodies...)
	mu.Unlock()
	if len(captured) != 1 {
		t.Fatalf("ACP 这一轮应恰好 1 次模型请求，实际 %d 次", len(captured))
	}
	body := captured[0]
	parsed := parseWireRequest(t, body)

	for _, layer := range instrOnceLayers {
		if got := strings.Count(body, layer.marker); got != 1 {
			t.Fatalf("ACP 请求里 %s 的正文出现 %d 次，期望恰好 1 次\n请求体:\n%s", layer.name, got, body)
		}
	}
	if carriers := instructionBearingMessages(t, parsed); len(carriers) != 1 {
		t.Fatalf("ACP 请求里带指令正文的消息有 %d 条，期望恰好 1 条", len(carriers))
	}
	sys := systemText(parsed)
	for _, layer := range instrOnceLayers {
		if strings.Contains(sys, layer.marker) {
			t.Fatalf("ACP 的 system 消息里出现了 %s 的正文：\n%s", layer.name, sys)
		}
	}

	path, err := session.ResolvePath(f.home, f.cwd, "instr-once-acp-session")
	if err != nil {
		t.Fatalf("解析 ACP 会话文件: %v", err)
	}
	if frames := baselineFrames(t, path); len(frames) != 1 {
		t.Fatalf("ACP 日志里 agent-instructions 帧有 %d 条，期望恰好 1 条", len(frames))
	}
}
