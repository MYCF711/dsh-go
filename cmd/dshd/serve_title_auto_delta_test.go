package main

// HS-03 · 自动标题五项 KNOWN-DELTA 的验收（先红）。
//
// 每一项都对着官方 0.2.2 包里的真实实现写断言，而不是对着我们自己的注释：
//
//	① messageSeqs  官方 `[first.seq]`（`dsh-session-title-first-prompt-llm/lib/index.js:105,110`）
//	② source.kind  官方 `{kind:"provider",provider:<titleProvider>,model:{provider,model}}`
//	               （`dsh-session-title/lib/index.js:480-488`；读索引侧仍是 Go 契约 `auto`）
//	③ effort       官方 `selectReasoningEffort = model.reasoning?.efforts[0]?.id`
//	               （`dsh-session-title-first-prompt-llm/lib/index.js:42`）
//	④ 审计帧       官方在流式前 append `session/title-llm-request`
//	               （`dsh-session-title-llm/lib/index.js:199-207`），且它记录的就是真实请求
//	⑤ 超限         官方 `inputBytes > maxInputBytes` 直接抛错（同文件 `:169-170`），不截断
//
// 纪律：只用进程内假网关；期望值从真实 v4/真实请求里取（不硬编码 seq），
// 这样"锚错消息"或"改了原请求"都会真的红。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"dshgo/codecs/v4"
	"dshgo/internal/llm"
)

// autoTitleCatalogFixture 写一份真实形状的模型目录：`owned_by` 是本宿主的 provider
// 身份，`reasoningEfforts` 的**声明顺序**就是官方 `model.reasoning.efforts` 的数组顺序。
func autoTitleCatalogFixture(t *testing.T, home, gatewayBase, modelID, ownedBy, reasoningEfforts string) {
	t.Helper()
	raw := fmt.Sprintf(
		`{"id":%q,"name":"Auto Title Model","owned_by":%q,"api":"openai-completions","baseURL":%q,"contextWindow":1000000,"maxTokens":128000,"input":["text"],"reasoningEfforts":%s}`,
		modelID, ownedBy, gatewayBase, reasoningEfforts)
	catalog := llm.OpenModelCatalog(home)
	revision := int64(0)
	if _, err := catalog.Upsert(json.RawMessage(raw), nil, &revision); err != nil {
		t.Fatalf("写模型目录: %v", err)
	}
}

// autoTitleLastUserSeq 返回日志里最后一条 `user/message` 的 seq。
//
// 首轮触发要求"恰好一条合格人类消息"，所以它同时是首条与最后一条；
// 断言从真实日志取值，锚到别的事件（turn/start、system/message）都会红。
func autoTitleLastUserSeq(t *testing.T, events []v4.Event) int64 {
	t.Helper()
	seq := int64(-1)
	for _, ev := range events {
		if ev.Type == "user/message" {
			seq = ev.Seq
		}
	}
	if seq < 0 {
		t.Fatal("日志里没有 user/message")
	}
	return seq
}

// autoTitleEventAt 取指定类型的唯一一条事件。
func autoTitleEventAt(t *testing.T, events []v4.Event, typ string) (v4.Event, []v4.Event) {
	t.Helper()
	var found []v4.Event
	for _, ev := range events {
		if ev.Type == typ {
			found = append(found, ev)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%s 事件数=%d，要恰好 1 条", typ, len(found))
	}
	return found[0], found
}

// TestAutoTitleDelta1MessageSeqsAnchorUserMessage：① `messageSeqs` 必须是来源消息的真实 seq，
// 且追加标题**不得改写任何更早的事件**（append-only 前缀逐字节相同）。
func TestAutoTitleDelta1MessageSeqsAnchorUserMessage(t *testing.T) {
	home := t.TempDir()
	const sessionID = "session-auto-title-delta1"
	path := autoTitleSeed(t, home, sessionID, "please fix the login redirect")
	before := autoTitleEvents(t, path)
	wantSeq := autoTitleLastUserSeq(t, before)

	gateway := newAutoTitleGateway(t, nil)
	service, base := autoTitleDirect(home, gateway.base(), 3*time.Second)
	if _, err := service.Run(context.Background(), sessionID, "auto-title-model", base, ""); err != nil {
		t.Fatalf("首轮收口后的标题调用失败: %v", err)
	}

	after := autoTitleEvents(t, path)
	titleEvent, _ := autoTitleEventAt(t, after, "session/title")
	var data struct {
		Title       string  `json:"title"`
		MessageSeqs []int64 `json:"messageSeqs"`
	}
	if err := json.Unmarshal(titleEvent.Data, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.MessageSeqs) != 1 || data.MessageSeqs[0] != wantSeq {
		t.Fatalf("messageSeqs=%v，要 [%d]（日志里最后一条 user/message 的 seq）", data.MessageSeqs, wantSeq)
	}

	// 前缀逐字节不变：标题追加没有重命名/改写任何更早的消息。
	if len(after) < len(before) {
		t.Fatalf("事件数从 %d 掉到 %d", len(before), len(after))
	}
	for i, ev := range before {
		if after[i].Type != ev.Type || after[i].Seq != ev.Seq || after[i].Time != ev.Time || string(after[i].Data) != string(ev.Data) {
			t.Fatalf("第 %d 条事件被改写: 前=%+v 后=%+v", i, ev, after[i])
		}
	}

	// 重试幂等：再跑一次不新增模型调用，也不新增标题。
	calls := gateway.count()
	if again, err := service.Run(context.Background(), sessionID, "auto-title-model", base, ""); err != nil || again != "" {
		t.Fatalf("已有标题后再跑=(%q,%v)，要静默跳过", again, err)
	}
	if got := gateway.count(); got != calls {
		t.Fatalf("第二次跑又发了 %d 次请求", got-calls)
	}
	if got := autoTitleTitleEvents(t, path); len(got) != 1 {
		t.Fatalf("session/title 事件数=%d，要 1", len(got))
	}
}

// TestAutoTitleDelta2SourceKindIsOfficialProvider：② v4 事件用官方 `source.kind=provider`
// （titleProvider + model 路由）；Go 读索引仍是 `auto`，且真的落盘。
func TestAutoTitleDelta2SourceKindIsOfficialProvider(t *testing.T) {
	home := t.TempDir()
	const sessionID = "session-auto-title-delta2"
	path := autoTitleSeed(t, home, sessionID, "please fix the login redirect")
	gateway := newAutoTitleGateway(t, nil)
	autoTitleCatalogFixture(t, home, gateway.base(), "auto-title-model", "wps-lingxi", `{"xhigh":"xhigh","max":"max"}`)

	service, base := autoTitleDirect(home, gateway.base(), 3*time.Second)
	if _, err := service.Run(context.Background(), sessionID, "auto-title-model", base, ""); err != nil {
		t.Fatalf("标题调用失败: %v", err)
	}

	titleEvent, _ := autoTitleEventAt(t, autoTitleEvents(t, path), "session/title")
	var data struct {
		Title  string `json:"title"`
		Source struct {
			Kind     string `json:"kind"`
			Provider string `json:"provider"`
			Model    struct {
				Provider string `json:"provider"`
				Model    string `json:"model"`
			} `json:"model"`
		} `json:"source"`
	}
	if err := json.Unmarshal(titleEvent.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.Source.Kind != "provider" {
		t.Fatalf("source.kind=%q，官方是 provider/fallback/user 闭集，自动标题走 provider", data.Source.Kind)
	}
	if data.Source.Provider != "session-title-first-prompt-llm" {
		t.Fatalf("source.provider=%q，要是 first-prompt provider 的注册 id", data.Source.Provider)
	}
	if data.Source.Model.Provider != "wps-lingxi" || data.Source.Model.Model != "auto-title-model" {
		t.Fatalf("source.model=%+v，要是本格真实路由", data.Source.Model)
	}

	// 读索引：Go 侧契约的 source=auto 必须真的在盘上。
	raw, err := os.ReadFile(autoTitleIndexPath(path))
	if err != nil {
		t.Fatalf("读标题索引: %v", err)
	}
	var index struct {
		EventSeq *int64  `json:"eventSeq"`
		Title    *string `json:"title"`
		Source   string  `json:"source"`
	}
	if err := json.Unmarshal(raw, &index); err != nil {
		t.Fatal(err)
	}
	if index.Source != "auto" || index.Title == nil || *index.Title != data.Title {
		t.Fatalf("标题索引=%s", raw)
	}
	if index.EventSeq == nil || *index.EventSeq != titleEvent.Seq {
		t.Fatalf("索引 eventSeq=%v，要 %d", index.EventSeq, titleEvent.Seq)
	}

	// 重启后的读路径（session.list）仍报 auto。
	row := autoTitleListRow(t, home, sessionID)
	if row.Title != data.Title || row.TitleSource != "auto" || !row.TitleExplicit {
		t.Fatalf("list 行=%+v", row)
	}
}

// TestAutoTitleDelta3ReasoningEffortMatchesOfficialDefault：③ 标题调用发官方默认 effort
// = 目录声明顺序里的第一个 level（官方 `efforts[0].id`）；未声明时不发。
func TestAutoTitleDelta3ReasoningEffortMatchesOfficialDefault(t *testing.T) {
	t.Run("declared", func(t *testing.T) {
		home := t.TempDir()
		const sessionID = "session-auto-title-delta3"
		path := autoTitleSeed(t, home, sessionID, "please fix the login redirect")
		gateway := newAutoTitleGateway(t, nil)
		// 声明顺序即官方 efforts 数组顺序：xhigh 是 efforts[0].id（max 是更贵的一档）。
		autoTitleCatalogFixture(t, home, gateway.base(), "auto-title-model", "wps-lingxi", `{"xhigh":"xhigh","max":"max"}`)

		service, base := autoTitleDirect(home, gateway.base(), 3*time.Second)
		if _, err := service.Run(context.Background(), sessionID, "auto-title-model", base, ""); err != nil {
			t.Fatalf("标题调用失败: %v", err)
		}
		req := gateway.request(0)
		if req.ReasoningEffort != "xhigh" {
			t.Fatalf("标题请求 reasoning_effort=%q，官方默认是 efforts[0]=xhigh", req.ReasoningEffort)
		}
		audit, _ := autoTitleEventAt(t, autoTitleEvents(t, path), "session/title-llm-request")
		var frame struct {
			ReasoningEffort string `json:"reasoningEffort"`
		}
		if err := json.Unmarshal(audit.Data, &frame); err != nil {
			t.Fatal(err)
		}
		if frame.ReasoningEffort != "xhigh" {
			t.Fatalf("审计帧 reasoningEffort=%q，要与真实请求一致", frame.ReasoningEffort)
		}
	})

	t.Run("undeclared", func(t *testing.T) {
		home := t.TempDir()
		const sessionID = "session-auto-title-delta3-none"
		path := autoTitleSeed(t, home, sessionID, "please fix the login redirect")
		gateway := newAutoTitleGateway(t, nil)
		// 目录里没有这个模型：官方 route 取不到 reasoning 元数据 → 不发 effort。
		service, base := autoTitleDirect(home, gateway.base(), 3*time.Second)
		if _, err := service.Run(context.Background(), sessionID, "auto-title-model", base, ""); err != nil {
			t.Fatalf("标题调用失败: %v", err)
		}
		if got := gateway.request(0).ReasoningEffort; got != "" {
			t.Fatalf("未声明 reasoningEfforts 时不该发 effort，实际 %q", got)
		}
		audit, _ := autoTitleEventAt(t, autoTitleEvents(t, path), "session/title-llm-request")
		var frame map[string]json.RawMessage
		if err := json.Unmarshal(audit.Data, &frame); err != nil {
			t.Fatal(err)
		}
		if _, present := frame["reasoningEffort"]; present {
			t.Fatalf("未声明 effort 时审计帧不该写 reasoningEffort：%s", audit.Data)
		}
	})
}

// TestAutoTitleDelta4RequestFramePreservesOriginalRequest：④ 流式前先落
// `session/title-llm-request`，且它逐字节等于真实发出的那次请求（system / user 文本 / maxTokens）。
func TestAutoTitleDelta4RequestFramePreservesOriginalRequest(t *testing.T) {
	home := t.TempDir()
	const sessionID = "session-auto-title-delta4"
	path := autoTitleSeed(t, home, sessionID, "please fix the login redirect")
	before := autoTitleEvents(t, path)
	wantSeq := autoTitleLastUserSeq(t, before)

	gateway := newAutoTitleGateway(t, nil)
	autoTitleCatalogFixture(t, home, gateway.base(), "auto-title-model", "wps-lingxi", `{"xhigh":"xhigh","max":"max"}`)
	service, base := autoTitleDirect(home, gateway.base(), 3*time.Second)
	if _, err := service.Run(context.Background(), sessionID, "auto-title-model", base, ""); err != nil {
		t.Fatalf("标题调用失败: %v", err)
	}

	events := autoTitleEvents(t, path)
	auditEvent, _ := autoTitleEventAt(t, events, "session/title-llm-request")
	titleEvent, _ := autoTitleEventAt(t, events, "session/title")
	if auditEvent.Seq >= titleEvent.Seq {
		t.Fatalf("审计帧 seq=%d 必须早于标题帧 seq=%d（官方先记请求再流式）", auditEvent.Seq, titleEvent.Seq)
	}

	var audit struct {
		TitleProvider string  `json:"titleProvider"`
		MessageSeqs   []int64 `json:"messageSeqs"`
		Route         struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
		} `json:"route"`
		System    string `json:"system"`
		MaxTokens int    `json:"maxTokens"`
		Messages  []struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Source struct {
				Kind string `json:"kind"`
			} `json:"source"`
			ID string `json:"id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(auditEvent.Data, &audit); err != nil {
		t.Fatal(err)
	}

	req := gateway.request(0)
	if audit.TitleProvider != "session-title-first-prompt-llm" {
		t.Fatalf("titleProvider=%q", audit.TitleProvider)
	}
	if len(audit.MessageSeqs) != 1 || audit.MessageSeqs[0] != wantSeq {
		t.Fatalf("messageSeqs=%v，要 [%d]", audit.MessageSeqs, wantSeq)
	}
	if audit.Route.Provider != "wps-lingxi" || audit.Route.Model != "auto-title-model" {
		t.Fatalf("route=%+v，要是真实路由", audit.Route)
	}
	if audit.MaxTokens != autoTitleMaxOutputTokens || req.MaxTokens != autoTitleMaxOutputTokens {
		t.Fatalf("maxTokens 审计帧=%d 真实请求=%d，要 %d", audit.MaxTokens, req.MaxTokens, autoTitleMaxOutputTokens)
	}
	// 原 request frame 保持：审计帧里的 system / user 文本与真实请求**逐字节相同**。
	if audit.System != req.text(0) {
		t.Fatalf("审计帧 system 与真实请求不一致:\n帧=%q\n请求=%q", audit.System, req.text(0))
	}
	if len(audit.Messages) != 1 {
		t.Fatalf("审计帧 messages 有 %d 条，要 1 条", len(audit.Messages))
	}
	message := audit.Messages[0]
	if message.Role != "user" || message.Source.Kind != "dsh-session-title-llm" {
		t.Fatalf("审计帧消息=%+v，要 role=user / source.kind=dsh-session-title-llm", message)
	}
	if len(message.Content) != 1 || message.Content[0].Type != "text" {
		t.Fatalf("审计帧消息 content=%+v，要 1 个 text 块", message.Content)
	}
	if message.Content[0].Text != req.text(1) {
		t.Fatalf("审计帧 user 文本与真实请求不一致:\n帧=%q\n请求=%q", message.Content[0].Text, req.text(1))
	}
	if message.ID == "" {
		t.Fatal("审计帧消息缺 id（官方 createUserMessage 一定带 id）")
	}
}

// TestAutoTitleDelta5RejectsOversizedInput：⑤ 输入 > maxInputBytes **响亮失败**，不截断：
// 恰好 4096 字节要能成功，4097 字节必须拒绝且一次模型调用都不发。
func TestAutoTitleDelta5RejectsOversizedInput(t *testing.T) {
	// frameOverhead 是"不含用户文本时"的整帧字节数；用它反推恰好压线的文本长度。
	frameOverhead := func(seq int64) int {
		return len(autoTitleInput([]autoTitleMessage{{Seq: seq, Text: ""}}))
	}

	t.Run("exactly-at-limit", func(t *testing.T) {
		home := t.TempDir()
		const sessionID = "session-auto-title-delta5-fit"
		// 先造一份空文本会话拿到真实 seq，再按它算压线长度。
		path := autoTitleSeed(t, home, sessionID, "placeholder")
		seq := autoTitleLastUserSeq(t, autoTitleEvents(t, path))
		prompt := strings.Repeat("a", autoTitleMaxInputBytes-frameOverhead(seq))
		if got := len(autoTitleInput([]autoTitleMessage{{Seq: seq, Text: prompt}})); got != autoTitleMaxInputBytes {
			t.Fatalf("夹具没压线：输入 %d 字节", got)
		}
		os.RemoveAll(home)
		home = t.TempDir()
		path = autoTitleSeed(t, home, sessionID, prompt)

		gateway := newAutoTitleGateway(t, nil)
		service, base := autoTitleDirect(home, gateway.base(), 3*time.Second)
		if _, err := service.Run(context.Background(), sessionID, "auto-title-model", base, ""); err != nil {
			t.Fatalf("恰好 %d 字节不该被拒: %v", autoTitleMaxInputBytes, err)
		}
		if got := gateway.count(); got != 1 {
			t.Fatalf("压线输入该发 1 次请求，实际 %d", got)
		}
	})

	t.Run("over-limit", func(t *testing.T) {
		home := t.TempDir()
		const sessionID = "session-auto-title-delta5-over"
		path := autoTitleSeed(t, home, sessionID, "placeholder")
		seq := autoTitleLastUserSeq(t, autoTitleEvents(t, path))
		prompt := strings.Repeat("a", autoTitleMaxInputBytes-frameOverhead(seq)+1)
		if got := len(autoTitleInput([]autoTitleMessage{{Seq: seq, Text: prompt}})); got != autoTitleMaxInputBytes+1 {
			t.Fatalf("夹具没越界：输入 %d 字节", got)
		}
		os.RemoveAll(home)
		home = t.TempDir()
		path = autoTitleSeed(t, home, sessionID, prompt)

		gateway := newAutoTitleGateway(t, nil)
		service, base := autoTitleDirect(home, gateway.base(), 3*time.Second)
		title, err := service.Run(context.Background(), sessionID, "auto-title-model", base, "")
		if err == nil {
			t.Fatalf("输入 %d 字节 > %d 必须拒绝，却写入了 %q", autoTitleMaxInputBytes+1, autoTitleMaxInputBytes, title)
		}
		if !strings.Contains(err.Error(), "maxInputBytes") || !strings.Contains(err.Error(), "4097") {
			t.Fatalf("拒绝理由要写明字节数与上限，实际：%v", err)
		}
		if got := gateway.count(); got != 0 {
			t.Fatalf("超限不该发模型调用，实际 %d 次", got)
		}
		if got := autoTitleTitleEvents(t, path); len(got) != 0 {
			t.Fatalf("超限仍写了 session/title：%+v", got)
		}
		if _, statErr := os.Stat(autoTitleIndexPath(path)); !os.IsNotExist(statErr) {
			t.Fatalf("超限仍写了索引：%v", statErr)
		}
		for _, ev := range autoTitleEvents(t, path) {
			if ev.Type == "session/title-llm-request" {
				t.Fatalf("超限仍写了审计帧：%s", ev.Data)
			}
		}
	})
}

// TestAutoTitleDeltaNoModelDegradesLoudly：没有可用模型 id 时明确降级，不伪装成功、不发请求。
func TestAutoTitleDeltaNoModelDegradesLoudly(t *testing.T) {
	home := t.TempDir()
	const sessionID = "session-auto-title-no-model"
	path := autoTitleSeed(t, home, sessionID, "please fix the login redirect")
	gateway := newAutoTitleGateway(t, func(autoTitleGatewayRequest) (string, int, time.Duration) {
		return "Should Never Be Used", http.StatusOK, 0
	})
	service, base := autoTitleDirect(home, gateway.base(), 3*time.Second)
	title, err := service.Run(context.Background(), sessionID, "", base, "")
	if err == nil || title != "" {
		t.Fatalf("没有模型时 Run=(%q,%v)，要明确报错", title, err)
	}
	if !strings.Contains(err.Error(), "模型") {
		t.Fatalf("错误要说清是模型缺失：%v", err)
	}
	if got := gateway.count(); got != 0 {
		t.Fatalf("没有模型仍发了 %d 次请求", got)
	}
	if got := autoTitleTitleEvents(t, path); len(got) != 0 {
		t.Fatalf("没有模型仍写了 session/title：%+v", got)
	}
}
