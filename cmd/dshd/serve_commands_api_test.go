package main

// 官方 `commands.*` / `subagents.*` / `userQuestions.attachWait` 的**宿主侧**
// 真实语义测试。
//
// 与 internal/transport 的分工：那边只测形状与协议翻译，这里测"到底有没有
// 真的动到真实状态"——命令真的执行了、消息真的投进了子会话、等待真的有时钟。
// 任何一条拿不到真实支撑，就必须是响亮的未实现（ErrOfficialNotImplemented），
// 不许静默成功，也不许编一个回执。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"dshgo/codecs/v4"
	"dshgo/internal/session"
	"dshgo/internal/spi"
	"dshgo/internal/transport"
)

// ---------------------------------------------------------------------------
// commands.*
// ---------------------------------------------------------------------------

// commands.list 必须列出**真实注册过**的命令（不是空数组、不是 TUI 命令表）。
func TestOfficialCommandListIsReal(t *testing.T) {
	f := newAgentAPIFixture(t)
	sessionID := f.newSession("", nil)

	list, err := f.api.CommandsList(sessionID)
	if err != nil {
		t.Fatalf("commands.list: %v", err)
	}
	if len(list) == 0 {
		t.Fatal("宿主没有列出任何命令：命令注册表必须真实可列出")
	}
	names := make([]string, 0, len(list))
	for _, cmd := range list {
		if cmd.Name == "" || strings.HasPrefix(cmd.Name, "/") {
			t.Fatalf("命令名必须是不带斜杠的非空名字：%q", cmd.Name)
		}
		if strings.TrimSpace(cmd.Description) == "" {
			t.Fatalf("命令 %q 没有 description", cmd.Name)
		}
		names = append(names, cmd.Name)
	}
	if !containsString(names, "help") {
		t.Fatalf("命令清单里没有 help（真实注册表至少要有它）：%v", names)
	}
	// 排序口径：官方按 name 升序。
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Fatalf("命令清单没有按 name 升序：%v", names)
		}
	}
}

// commands.execute 必须真的执行，并回真实的 commandId + 结果。
func TestOfficialCommandExecuteIsReal(t *testing.T) {
	f := newAgentAPIFixture(t)
	sessionID := f.newSession("", nil)

	exec, err := f.api.CommandsExecute(sessionID, "/help", nil)
	if err != nil {
		t.Fatalf("commands.execute /help: %v", err)
	}
	if exec == nil {
		t.Fatal("commands.execute 回了 undefined：/help 是注册过的命令，必须真执行")
	}
	if !strings.HasPrefix(exec.CommandID, "cmd-") {
		t.Fatalf("commandId 形状不对：%q", exec.CommandID)
	}
	if exec.Result.Kind != "success" {
		t.Fatalf("/help 应该是 success，实际 %+v", exec.Result)
	}
	if !strings.Contains(exec.Result.Text, "help") {
		t.Fatalf("/help 的正文没列出命令：%q", exec.Result.Text)
	}
}

// 语法/名字不解析 → 官方 undefined（本仓 nil,nil），**不记账**。
func TestOfficialCommandExecuteUnresolvedIsUndefined(t *testing.T) {
	f := newAgentAPIFixture(t)
	sessionID := f.newSession("", nil)

	for _, line := range []string{"/nope", "help", "/", "/HELP", "/help!", ""} {
		exec, err := f.api.CommandsExecute(sessionID, line, nil)
		if err != nil {
			t.Fatalf("%q 应该回 undefined，实际错误 %v", line, err)
		}
		if exec != nil {
			t.Fatalf("%q 应该回 undefined，实际 %+v", line, exec)
		}
	}
}

// 生命周期必须真的落到 durable 日志：command/run + command/done。
func TestOfficialCommandExecuteAppendsLifecycle(t *testing.T) {
	f := newAgentAPIFixture(t)
	sessionID := f.newSession("", nil)

	exec, err := f.api.CommandsExecute(sessionID, "/help", nil)
	if err != nil || exec == nil {
		t.Fatalf("commands.execute: %v %+v", err, exec)
	}
	events, err := f.core.agentReadEvents(sessionID)
	if err != nil {
		t.Fatalf("读会话事件: %v", err)
	}
	var sawRun, sawDone bool
	for _, ev := range events {
		switch ev.Type {
		case "command/run":
			if !strings.Contains(string(ev.Data), exec.CommandID) {
				t.Fatalf("command/run 没带本次 commandId %q：%s", exec.CommandID, ev.Data)
			}
			sawRun = true
		case "command/done":
			if !strings.Contains(string(ev.Data), exec.CommandID) {
				t.Fatalf("command/done 没带本次 commandId %q：%s", exec.CommandID, ev.Data)
			}
			sawDone = true
		}
	}
	if !sawRun || !sawDone {
		t.Fatalf("生命周期没落盘：run=%v done=%v", sawRun, sawDone)
	}
}

// 命令不接受附件时，带附件要结算成 error **结果**（不是 transport 错误）。
func TestOfficialCommandExecuteRejectsAttachments(t *testing.T) {
	f := newAgentAPIFixture(t)
	sessionID := f.newSession("", nil)

	exec, err := f.api.CommandsExecute(sessionID, "/help", []transport.OfficialSubmittedAttachment{
		{Type: "file", ReceiptID: "r-1"},
	})
	if err != nil {
		t.Fatalf("带附件应该是 error **结果**，不是 transport 错误：%v", err)
	}
	if exec == nil || exec.Result.Kind != "error" {
		t.Fatalf("应该结算成 error 结果，实际 %+v", exec)
	}
	if !strings.Contains(exec.Result.Text, "does not accept attachments") {
		t.Fatalf("错误正文没说明不接受附件：%q", exec.Result.Text)
	}
}

// ---------------------------------------------------------------------------
// /compact（官方 base 插件 #65 `command-compact`）
// ---------------------------------------------------------------------------

// compactCommandTextRE 是官方成功文案的**精确**形状：
// `Compacted <条数> history items (~<token 数> tokens).`
var compactCommandTextRE = regexp.MustCompile(`^Compacted (\d+) history items \(~(\d+) tokens\)\.$`)

// compactCommandGateway 是脚本化的 OpenAI 兼容网关：只回答摘要调用。
//
// 为什么必须用真 HTTP 而不是替身对象：`/compact` 走的是 CLI 同一条 `autoCompact`，
// 而那条路径自己 `llm.New(base, key)` 建客户端，没有注入点。喂它一段真 SSE
// 也正是产品里发生的事——宿主真的去调模型拿摘要。
// status != 200 时网关直接失败，用来造"摘要生成不出来"这条真实失败路径。
func compactCommandGateway(t *testing.T, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":[]}`)
		case "/v1/chat/completions":
			if status != http.StatusOK {
				http.Error(w, "摘要服务挂了", status)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: "+`{"choices":[{"delta":{"content":"已压缩的历史摘要"},"finish_reason":"stop"}]}`+"\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		default:
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newCompactableSession 造一条"确实有可压前缀"的会话：10 组一问一答，
// 每组约 450 token（宿主估算器口径），远超默认 2000 token 的保留预算。
func (f *agentAPIFixture) newCompactableSession(t *testing.T) string {
	t.Helper()
	return f.newSession("", func(lg *session.Log) {
		for i := 0; i < 10; i++ {
			if err := lg.Append("user/message", session.UserMessage([]spi.Block{
				{Type: "text", Text: strings.Repeat("撑大上下文的消息", 50)},
			})); err != nil {
				t.Fatalf("追加 user/message: %v", err)
			}
			if err := lg.Append("assistant/message", session.AssistantMessageEvent(1, 1,
				[]map[string]any{session.TextBlock("收到")}, "stop", nil)); err != nil {
				t.Fatalf("追加 assistant/message: %v", err)
			}
		}
	})
}

// sessionFilePath 是这条会话的 v4 文件（压缩直接改它，断言要读同一个文件）。
func (f *agentAPIFixture) sessionFilePath(t *testing.T, sessionID string) string {
	t.Helper()
	path, err := session.ResolvePath(f.home, "", sessionID)
	if err != nil {
		t.Fatalf("定位会话文件: %v", err)
	}
	return path
}

// compactFrames 数一遍日志里的压缩帧：摘要帧的 seq、被遮蔽的 seq 条数。
func compactFrames(t *testing.T, path string) (summarySeq int64, shadowed int, count int) {
	t.Helper()
	events, err := v4.ReadFile(path)
	if err != nil {
		t.Fatalf("读会话事件: %v", err)
	}
	summarySeq = -1
	for _, ev := range events.Events {
		if ev.Type != "compaction/summary" {
			continue
		}
		var data struct {
			ShadowedSeqs []int64 `json:"shadowedSeqs"`
		}
		if err := json.Unmarshal(ev.Data, &data); err != nil {
			t.Fatalf("解析 compaction/summary: %v", err)
		}
		summarySeq, shadowed, count = ev.Seq, len(data.ShadowedSeqs), count+1
	}
	return summarySeq, shadowed, count
}

// /compact 必须真的压一次历史：文案里的数字来自真实压缩，两帧生命周期落盘。
func TestOfficialCommandCompactCompactsRealHistory(t *testing.T) {
	f := newAgentAPIFixture(t)
	gateway := compactCommandGateway(t, http.StatusOK)
	t.Setenv("DSHD_LLM_BASE", gateway.URL+"/v1")
	t.Setenv("DSHD_LLM_KEY", "test-key")
	sessionID := f.newCompactableSession(t)

	// 发现面：注册表里必须有 compact，而且是**官方那份身份**。
	list, err := f.api.CommandsList(sessionID)
	if err != nil {
		t.Fatalf("commands.list: %v", err)
	}
	var row *transport.OfficialCommand
	names := make([]string, 0, len(list))
	for i := range list {
		names = append(names, list[i].Name)
		if list[i].Name == hostCommandCompact {
			row = &list[i]
		}
	}
	if row == nil {
		t.Fatalf("commands.list 里没有 compact：%v", names)
	}
	if row.DefinitionID != officialCompactDefinitionID {
		t.Fatalf("compact 的 definitionId 不是官方那个：%q", row.DefinitionID)
	}
	if row.Description != officialCompactDescription {
		t.Fatalf("compact 的 description 不是官方原文：%q", row.Description)
	}

	exec, err := f.api.CommandsExecute(sessionID, "/compact", nil)
	if err != nil {
		t.Fatalf("commands.execute /compact: %v", err)
	}
	if exec == nil {
		t.Fatal("commands.execute 回了 undefined：compact 注册过，必须真执行")
	}
	if exec.Result.Kind != "success" {
		t.Fatalf("/compact 应该是 success，实际 %+v", exec.Result)
	}
	m := compactCommandTextRE.FindStringSubmatch(exec.Result.Text)
	if m == nil {
		t.Fatalf("成功文案不是官方形状：%q", exec.Result.Text)
	}
	t.Logf("真实压缩文案：%s", exec.Result.Text)
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("条数不是整数：%q", m[1])
	}
	tokens, err := strconv.Atoi(m[2])
	if err != nil {
		t.Fatalf("token 数不是整数：%q", m[2])
	}

	// 数字必须与日志里的真实遮蔽对得上：条数 = 这次压缩真的遮蔽了多少条事件。
	path := f.sessionFilePath(t, sessionID)
	summarySeq, shadowed, frames := compactFrames(t, path)
	if frames != 1 {
		t.Fatalf("compaction/summary 帧 %d 条，期望 1 条", frames)
	}
	if n == 0 || n != shadowed {
		t.Fatalf("文案说压了 %d 条，日志里遮蔽了 %d 条：文案的数字必须是真实值", n, shadowed)
	}
	// token 数说的是"被压掉的那段历史有多大"，不是摘要字数：摘要只有十来个字，
	// 被压掉的 10 条消息按宿主自己的估算器是几千 token。这条断言把
	// "拿摘要字数冒充实测 token"钉死。
	if tokens < 1000 {
		t.Fatalf("token 数 %d 太小，像是把摘要字数（%d 字）当 token 报了出来", tokens, len([]rune("已压缩的历史摘要")))
	}
	// 官方 sourceEventSeq 就是摘要帧自己的 seq。
	if exec.Result.SourceEventSeq == nil {
		t.Fatal("成功结果没有 sourceEventSeq（官方是 result.summarySeq）")
	}
	if *exec.Result.SourceEventSeq != summarySeq {
		t.Fatalf("sourceEventSeq=%d，日志里的摘要帧 seq=%d", *exec.Result.SourceEventSeq, summarySeq)
	}

	// 生命周期两帧必须被追加（command/run 在 handler 前、command/done 在结算后）。
	events, err := f.core.agentReadEvents(sessionID)
	if err != nil {
		t.Fatalf("读会话事件: %v", err)
	}
	var sawRun, sawDone bool
	for _, ev := range events {
		switch ev.Type {
		case "command/run":
			sawRun = sawRun || strings.Contains(string(ev.Data), exec.CommandID)
		case "command/done":
			if strings.Contains(string(ev.Data), exec.CommandID) {
				sawDone = true
				if !strings.Contains(string(ev.Data), `"kind":"success"`) {
					t.Fatalf("command/done 没记成 success：%s", ev.Data)
				}
			}
		}
	}
	if !sawRun || !sawDone {
		t.Fatalf("生命周期没落盘：run=%v done=%v", sawRun, sawDone)
	}
}

// 带参数 → 官方 usage 错误，**一个字都不许差**，而且不许去压。
func TestOfficialCommandCompactRejectsArguments(t *testing.T) {
	f := newAgentAPIFixture(t)
	// 指向死地址：这条路径一旦真的去压缩就会变成"生成摘要失败"，
	// 于是"回的是 usage 而不是别的错误"本身就是"没有去压缩"的证据。
	t.Setenv("DSHD_LLM_BASE", "http://127.0.0.1:1/v1")
	sessionID := f.newCompactableSession(t)

	exec, err := f.api.CommandsExecute(sessionID, "/compact foo", nil)
	if err != nil {
		t.Fatalf("带参数是 error **结果**，不是 transport 错误：%v", err)
	}
	if exec == nil || exec.Result.Kind != "error" {
		t.Fatalf("应该结算成 error 结果，实际 %+v", exec)
	}
	if exec.Result.Text != officialCompactUsage {
		t.Fatalf("usage 文案必须逐字是 %q，实际 %q", officialCompactUsage, exec.Result.Text)
	}
	if _, _, frames := compactFrames(t, f.sessionFilePath(t, sessionID)); frames != 0 {
		t.Fatalf("带参数时居然压了一次：compaction/summary 有 %d 条", frames)
	}
}

// 没有可压的历史 → 官方回 null 那一支：success 的 `No compactable history yet.`。
func TestOfficialCommandCompactWithoutCompactableHistory(t *testing.T) {
	f := newAgentAPIFixture(t)
	// 同样指向死地址：这一支**不该**调模型。
	t.Setenv("DSHD_LLM_BASE", "http://127.0.0.1:1/v1")
	sessionID := f.newSession("", nil)

	// 第二个写法只带空白：官方是 `rawInput.trim()`，所以它同样算"没参数"。
	for _, line := range []string{"/compact", "/compact   "} {
		exec, err := f.api.CommandsExecute(sessionID, line, nil)
		if err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		if exec == nil || exec.Result.Kind != "success" || exec.Result.Text != "No compactable history yet." {
			t.Fatalf("%q 应该回 success 的 No compactable history yet.，实际 %+v", line, exec)
		}
	}
	if _, _, frames := compactFrames(t, f.sessionFilePath(t, sessionID)); frames != 0 {
		t.Fatalf("空会话居然压出了一条摘要：%d 条", frames)
	}
}

// 会话正被别的 writer 占着 → 映射官方 busy 文案（不是编一句别的错）。
//
// 这里直接调 handler，而**不走**整条 commands.execute：那条路上宿主自己也要
// 往**同一个会话**写 command/run 与 command/done 两帧，写锁被占时它先在记账
// 那一步响亮失败（这是对的：官方对生命周期追加失败就是 fail-loud），于是
// busy 这条分支根本轮不到 handler 被调用。busy 真正会发生的情形是"记账时锁还
// 空着、等压缩真要写盘时被别的 writer 抢走"——窗口很窄，但真实存在，
// 所以这里把 handler 单独喂一次，让它可复核。
func TestOfficialCommandCompactBusyMapsOfficialText(t *testing.T) {
	f := newAgentAPIFixture(t)
	gateway := compactCommandGateway(t, http.StatusOK)
	t.Setenv("DSHD_LLM_BASE", gateway.URL+"/v1")
	t.Setenv("DSHD_LLM_KEY", "test-key")
	sessionID := f.newCompactableSession(t)
	path := f.sessionFilePath(t, sessionID)

	// 另一个进程正持有这份会话：锁里写一个不是本进程的 pid。
	// claimWriter 对"别的进程的锁"会等满 sessionLockWait（2s）再拒绝，
	// 所以这条用例慢 2 秒——换来的是真的走到了宿主那条 busy 分支。
	if err := os.WriteFile(path+".lock", []byte("pid=999999\n"), 0o644); err != nil {
		t.Fatalf("造写锁: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path + ".lock") })

	registry, err := newHostCommandRegistry(f.core)
	if err != nil {
		t.Fatalf("装配注册表: %v", err)
	}
	def, ok := registry.Find(hostCommandCompact)
	if !ok {
		t.Fatal("注册表里没有 compact")
	}
	outcome, err := def.Handler(context.Background(), transport.OfficialCommandInvocation{
		CommandID: "cmd-busy", SessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("忙是 error **结果**，不是 transport 错误：%v", err)
	}
	if outcome.Kind != "error" {
		t.Fatalf("应该结算成 error 结果，实际 %+v", outcome)
	}
	if outcome.Text != officialCompactErrorBusy {
		t.Fatalf("忙应该映射官方 busy 文案 %q，实际 %q", officialCompactErrorBusy, outcome.Text)
	}
	if _, _, frames := compactFrames(t, path); frames != 0 {
		t.Fatalf("没抢到写锁却记了摘要：%d 条", frames)
	}
}

// 调用被取消 → 映射官方 cancelled 文案（不是含混的"摘要失败"）。
//
// 直接喂 handler 一个已取消的 ctx：`commands.execute` 自己固定用
// `context.Background()`，走它注入不了"调用被取消"；而 handler 的 ctx 本来就
// 来自调用方，传一个真取消掉的 ctx 就是它在生产里的形态。
func TestOfficialCommandCompactCancelledMapsOfficialText(t *testing.T) {
	f := newAgentAPIFixture(t)
	// 死地址：取消应当在**摘要调用之前**就生效，一个字都不该发出去。
	t.Setenv("DSHD_LLM_BASE", "http://127.0.0.1:1/v1")
	sessionID := f.newCompactableSession(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	registry, err := newHostCommandRegistry(f.core)
	if err != nil {
		t.Fatalf("装配注册表: %v", err)
	}
	def, ok := registry.Find(hostCommandCompact)
	if !ok {
		t.Fatal("注册表里没有 compact")
	}
	outcome, err := def.Handler(ctx, transport.OfficialCommandInvocation{
		CommandID: "cmd-cancel", SessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("取消是 error **结果**，不是 transport 错误：%v", err)
	}
	if outcome.Kind != "error" {
		t.Fatalf("应该结算成 error 结果，实际 %+v", outcome)
	}
	if outcome.Text != officialCompactErrorCancelled {
		t.Fatalf("取消应该映射官方 cancelled 文案 %q，实际 %q", officialCompactErrorCancelled, outcome.Text)
	}
	if _, _, frames := compactFrames(t, f.sessionFilePath(t, sessionID)); frames != 0 {
		t.Fatalf("已经取消了却记了摘要：%d 条", frames)
	}
}

// 摘要生成不出来 → 映射官方 summary 文案。
func TestOfficialCommandCompactSummaryFailureMapsOfficialText(t *testing.T) {
	f := newAgentAPIFixture(t)
	gateway := compactCommandGateway(t, http.StatusInternalServerError)
	t.Setenv("DSHD_LLM_BASE", gateway.URL+"/v1")
	t.Setenv("DSHD_LLM_KEY", "test-key")
	sessionID := f.newCompactableSession(t)

	exec, err := f.api.CommandsExecute(sessionID, "/compact", nil)
	if err != nil {
		t.Fatalf("摘要是 error **结果**，不是 transport 错误：%v", err)
	}
	if exec == nil || exec.Result.Kind != "error" {
		t.Fatalf("应该结算成 error 结果，实际 %+v", exec)
	}
	if exec.Result.Text != officialCompactErrorSummary {
		t.Fatalf("应该映射官方 summary 文案 %q，实际 %q", officialCompactErrorSummary, exec.Result.Text)
	}
	if _, _, frames := compactFrames(t, f.sessionFilePath(t, sessionID)); frames != 0 {
		t.Fatalf("摘要失败却记了摘要：%d 条", frames)
	}
}

// 映射的边界：分不出的失败一律说真实原因，**不许**套官方那几条"说不清"的文案。
func TestOfficialCommandCompactFailureTextMapping(t *testing.T) {
	// 宿主分不出 changed / commit / persistence：那三条文案对应的成因在这条
	// 路径上既不会发生也无从判定，硬套等于编一个没发生过的原因。
	raw := errors.New("会话能读、但打不开写入：文件只读")
	got := compactFailureText(context.Background(), raw)
	if !strings.Contains(got, "打不开写入") {
		t.Fatalf("确定不了时必须说清真实原因，实际 %q", got)
	}
	for _, official := range []string{
		officialCompactErrorChanged, officialCompactErrorCommit,
		officialCompactErrorPersistence, officialCompactErrorBusy,
		officialCompactErrorSummary, officialCompactErrorCancelled,
	} {
		if got == official {
			t.Fatalf("把分不出的失败硬套成了官方文案：%q", got)
		}
	}
}

// ---------------------------------------------------------------------------
// subagents.*
// ---------------------------------------------------------------------------

// 没有子会话时 subagents.prompt 必须响亮失败，**绝不**回一个编的 messageId。
func TestOfficialSubagentPromptUnknownChildIsLoud(t *testing.T) {
	f := newAgentAPIFixture(t)
	parentID := f.newSession("", nil)

	receipt, err := f.api.SubagentsPrompt(transport.OfficialSubagentPromptRequest{
		RequestID: "req-1", ParentSessionID: parentID, ChildSessionID: "child-nope",
		Mode: "continuable", Delivery: "queue",
		Content: []transport.OfficialSubagentPromptContent{{Type: "text", Text: "继续"}},
	})
	if err == nil {
		t.Fatalf("没有子会话竟然回了回执：%+v", receipt)
	}
	if receipt.MessageID != "" {
		t.Fatalf("失败时不许带回执：%+v", receipt)
	}
}

// 真实子会话：投递要真的落进子会话并能被父中断。
func TestOfficialSubagentPromptAndInterrupt(t *testing.T) {
	f := newAgentAPIFixture(t)
	parentID := f.newSession("", nil)

	// 真实投递通道：宿主的 promptService.Submit 在测试里换成记录器。
	var delivered []string
	f.core.deps.submit = func(sessionID, text string) (transport.PromptResult, error) {
		delivered = append(delivered, sessionID+":"+text)
		return transport.PromptResult{Accepted: true, TurnID: "turn-1"}, nil
	}
	f.core.deps.interrupt = func(string) error { return nil }
	f.core.children = nil // 让注册表用新 deps（含 submit / interrupt）重建

	child, err := f.core.childRegistry().Spawn("child-1", parentID, f.home)
	if err != nil {
		t.Fatalf("spawn 子会话: %v", err)
	}
	if child.ID != "child-1" {
		t.Fatalf("子会话地址不对：%+v", child)
	}

	receipt, err := f.api.SubagentsPrompt(transport.OfficialSubagentPromptRequest{
		RequestID: "req-1", ParentSessionID: parentID, ChildSessionID: "child-1",
		Mode: "continuable", Delivery: "queue",
		Content: []transport.OfficialSubagentPromptContent{{Type: "text", Text: "继续"}},
	})
	if err != nil {
		t.Fatalf("subagents.prompt: %v", err)
	}
	if !strings.HasPrefix(receipt.MessageID, "msg-") {
		t.Fatalf("messageId 形状不对：%q", receipt.MessageID)
	}
	if got := len(child.Inbox()); got != 1 {
		t.Fatalf("inbox 应该有 1 条，实际 %d", got)
	}

	accepted, err := f.api.SubagentsInterruptByParent("child-1", parentID)
	if err != nil {
		t.Fatalf("interruptByParent: %v", err)
	}
	if !accepted.Accepted {
		t.Fatalf("interrupt 应该被接受：%+v", accepted)
	}
}

// 父 id 不匹配 = 跨会话，403，不是静默成功。
func TestOfficialSubagentCrossSessionIsRejected(t *testing.T) {
	f := newAgentAPIFixture(t)
	parentA := f.newSession("", nil)
	parentB := f.newSession("", nil)

	if _, err := f.core.childRegistry().Spawn("child-x", parentA, f.home); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	_, err := f.api.SubagentsPrompt(transport.OfficialSubagentPromptRequest{
		RequestID: "req-x", ParentSessionID: parentB, ChildSessionID: "child-x",
		Mode: "continuable", Delivery: "queue",
		Content: []transport.OfficialSubagentPromptContent{{Type: "text", Text: "偷看"}},
	})
	if err == nil {
		t.Fatal("跨会话投递竟然成功了")
	}
	if !strings.Contains(err.Error(), "cross_session") && !errors.Is(err, transport.ErrOfficialBadInput) {
		// cross_session 由 officialError 承载，错误文本里带码。
		if !strings.Contains(strings.ToLower(err.Error()), "属于父会话") {
			t.Fatalf("错误没说清是跨会话：%v", err)
		}
	}
}

// ---------------------------------------------------------------------------
// userQuestions.attachWait
// ---------------------------------------------------------------------------

// 问题还开着时，attachWait 必须给出**真实**剩余毫秒（单调递减）。
func TestOfficialQuestionAttachWaitIsReal(t *testing.T) {
	f := newAgentAPIFixture(t)

	sessionID := f.newSession("", func(lg *session.Log) {
		if err := lg.Append("tool/call", session.ToolCallEvent(1, 1, "call-ask", "ask_user_question", `{"questions":[]}`)); err != nil {
			t.Fatalf("追加 tool/call: %v", err)
		}
		if err := lg.Append("tool/result", session.ToolResultEvent(1, 1, "call-ask", nil, false, "", nil)); err != nil {
			t.Fatalf("追加 tool/result: %v", err)
		}
	})

	// 真实时钟 + 可拨动的 now：deadline 从 durable 的 tool/result 帧时间起算，
	// 所以起点必须是**真实**时刻（编一个过去的时间只会让窗口早就过期）。
	now := time.Now()
	f.core.questionWaits = transport.NewOfficialQuestionWaits(transport.OfficialQuestionWaitOptions{
		Timeout: 10 * time.Second,
		Now:     func() time.Time { return now },
	})

	first, err := f.api.UserQuestionsAttachWait(sessionID, "call-ask")
	if err != nil {
		t.Fatalf("attachWait: %v", err)
	}
	if first == nil {
		t.Fatal("问题开着的时候 attachWait 不该是空帧")
	}
	if first.RemainingMs <= 0 || first.RemainingMs > 10_000 {
		t.Fatalf("remainingMs 不在 (0, 10000]：%d", first.RemainingMs)
	}

	now = now.Add(4 * time.Second)
	second, err := f.api.UserQuestionsAttachWait(sessionID, "call-ask")
	if err != nil || second == nil {
		t.Fatalf("第二次 attachWait: %v %+v", err, second)
	}
	if second.RemainingMs >= first.RemainingMs {
		t.Fatalf("剩余时间没有递减：%d → %d", first.RemainingMs, second.RemainingMs)
	}

	// 窗口过去之后：官方不再发帧（L1 = 空帧），不是回一个 0 骗 UI 继续转。
	now = now.Add(200 * time.Second)
	third, err := f.api.UserQuestionsAttachWait(sessionID, "call-ask")
	if err != nil {
		t.Fatalf("超时后 attachWait: %v", err)
	}
	if third != nil {
		t.Fatalf("窗口结束后不该再发帧，实际 %+v", third)
	}
}

// 没有这个 question 通道时必须响亮失败（不是编一个 remainingMs）。
func TestOfficialQuestionAttachWaitUnknownCallIsLoud(t *testing.T) {
	f := newAgentAPIFixture(t)
	sessionID := f.newSession("", nil)

	if _, err := f.api.UserQuestionsAttachWait(sessionID, "call-nope"); err == nil {
		t.Fatal("没有这个 question 调用竟然成功了")
	}
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
