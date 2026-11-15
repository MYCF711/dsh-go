package main

// 四类通知的构造与抑制规则——全部是**行为断言**：
// 断言"写出了什么字节"与"一条都没写"，不看内部字段。

import (
	"bytes"
	"strings"
	"testing"
)

// frame 造一帧（seq 单调递增，模拟 journal 里的真实序号）。
func (r *notifyRig) frame(typ, data string) ServerFrame {
	r.seq++
	return ServerFrame{Seq: r.seq, Type: typ, Data: []byte(data), Time: int64(r.seq)}
}

// deliver 走真实入口（handleFrame）+ 主循环的"这一批处理完再结算"。
func (r *notifyRig) deliver(t *testing.T, frames ...ServerFrame) string {
	t.Helper()
	r.buf.Reset()
	for _, f := range frames {
		r.app.handleFrame(f)
	}
	r.app.drainFrames(nil)
	return r.buf.String()
}

// out 只读上一次投递写出的字节（不再触发结算）。
func (r *notifyRig) out() string { return r.buf.String() }

// ───────────────────────────────────────────────────────────── 正文构造

func TestTaskCompletionPreviewStripsMarkdown(t *testing.T) {
	src := "## 结论\n\n**已完成** 3 个文件，见 [说明](https://example.com/x)。\n\n- 第一项\n- 第二项\n\n```go\nfmt.Println(1)\n```"
	got := taskCompletionPreview(src)
	want := "结论 已完成 3 个文件，见 说明。 第一项 第二项 fmt.Println(1)"
	if got != want {
		t.Fatalf("去 Markdown 的结果不对：\n got=%q\nwant=%q", got, want)
	}
}

func TestTaskCompletionPreviewRemovesImagesTablesAndHTML(t *testing.T) {
	src := "![截图](img.png) 与 <b>粗体</b>\n\n| 名称 | 数量 |\n|---|---|\n| 文件 | 3 |"
	got := taskCompletionPreview(src)
	want := "截图 与 粗体 名称 数量 文件 3"
	if got != want {
		t.Fatalf("图片/表格/HTML 的抽取不对：\n got=%q\nwant=%q", got, want)
	}
}

func TestTaskCompletionPreviewIsSingleLineAndCapped(t *testing.T) {
	src := strings.Repeat("很长的句子。\n", 60)
	got := taskCompletionPreview(src)
	if strings.Contains(got, "\n") {
		t.Fatalf("通知正文必须是单行：%q", got)
	}
	if n := len(graphemeStrings(got)); n > notificationMaxGraphemes {
		t.Fatalf("正文 %d 字素，超过上限 %d", n, notificationMaxGraphemes)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("截断后应有省略号：%q", got)
	}
}

func TestApprovalNotificationMessage(t *testing.T) {
	cases := []struct {
		name    string
		session string
		tool    string
		reason  string
		want    string
	}{
		{"三段齐全", "修登录", "bash", "要跑 go test", "修登录：bash：要跑 go test"},
		{"没有理由", "修登录", "bash", "", "修登录：bash"},
		{"没有会话名给占位", "", "bash", "要跑命令", "未命名会话：bash：要跑命令"},
		{"长会话名按列让位", strings.Repeat("会话", 20), "bash", "", clipToWidthGraphemes(strings.Repeat("会话", 20), notificationSessionNameMaxWidth) + "：bash"},
		{"理由压成一行", "会话", "bash", "第一行\n第二行", "会话：bash：第一行 第二行"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := approvalNotificationMessage(c.session, c.tool, c.reason); got != c.want {
				t.Fatalf("授权正文不对：\n got=%q\nwant=%q", got, c.want)
			}
		})
	}
}

func TestQuestionNotificationMessage(t *testing.T) {
	if got, want := questionNotificationMessage("确认", "要继续吗"), "确认：要继续吗"; got != want {
		t.Fatalf("提问正文 = %q，want %q", got, want)
	}
	if got, want := questionNotificationMessage("", "要继续吗"), "要继续吗"; got != want {
		t.Fatalf("无 header 时正文 = %q，want %q", got, want)
	}
	if got := questionNotificationMessage("", ""); got != "" {
		t.Fatalf("什么都没给时应为空正文，实际 %q", got)
	}
}

// ───────────────────────────────────────────────────────────── 完成通知

func TestTaskCompleteNotificationsEndToEnd(t *testing.T) {
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "ghostty"})
	rig.app.sid = "s1"
	got := rig.deliver(t,
		rig.frame("turn/start", `{"turn":1}`),
		rig.frame("assistant/message", `{"turn":1,"step":1,"message":{"role":"assistant","content":[{"type":"text","text":"**改好了** 两个文件"}]}}`),
		rig.frame("turn/end", `{"turn":1,"reason":{"kind":"completed"}}`),
	)
	want := "\x1b]777;notify;dsh-TUI · Task complete;改好了 两个文件\x07"
	if got != want {
		t.Fatalf("完成通知字节不对：\n got=%q\nwant=%q", got, want)
	}
}

func TestTaskCompleteWithoutReplyShowsTitleOnly(t *testing.T) {
	// 这一轮没有任何已结算回复：正文为空，只显示标题（上游同义）。
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "ghostty"})
	rig.app.sid = "s1"
	got := rig.deliver(t,
		rig.frame("turn/start", `{"turn":1}`),
		rig.frame("turn/end", `{"turn":1,"reason":{"kind":"completed"}}`),
	)
	want := "\x1b]777;notify;dsh-TUI · Task complete;\x07"
	if got != want {
		t.Fatalf("无回复时应只显示标题：%q", got)
	}
}

func TestTaskCompleteSuppressedOnReplay(t *testing.T) {
	// 重放的帧（seq 落在订阅时的水位以内）一条通知都不许发。
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "ghostty"})
	rig.app.sid = "s1"
	rig.app.notify.replayUntil = 3
	got := rig.deliver(t,
		rig.frame("turn/start", `{"turn":1}`),
		rig.frame("assistant/message", `{"turn":1,"step":1,"message":{"role":"assistant","content":[{"type":"text","text":"历史回复"}]}}`),
		rig.frame("turn/end", `{"turn":1,"reason":{"kind":"completed"}}`),
	)
	if got != "" {
		t.Fatalf("重放不该通知，实际写出 %q", got)
	}
}

func TestTaskCompleteSuppressedOnCancelAndFailure(t *testing.T) {
	// 取消 / 失败 / 截断 / 中断 / 分叉都不是"完成"：一条都不通知。
	for _, kind := range []string{"aborted", "error", "interrupted", "max-tokens", "blocked", "forked"} {
		t.Run(kind, func(t *testing.T) {
			rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "ghostty"})
			rig.app.sid = "s1"
			got := rig.deliver(t,
				rig.frame("turn/start", `{"turn":1}`),
				rig.frame("assistant/message", `{"turn":1,"step":1,"message":{"role":"assistant","content":[{"type":"text","text":"半截回复"}]}}`),
				rig.frame("turn/end", `{"turn":1,"reason":{"kind":"`+kind+`"}}`),
			)
			if got != "" {
				t.Fatalf("%s 不该通知，实际写出 %q", kind, got)
			}
		})
	}
}

func TestTaskCompleteIgnoresInterruptedAndSubagentReplies(t *testing.T) {
	// 被打断的回复与子代理的回复都不是"落后的主回复"：正文不带它们。
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "ghostty"})
	rig.app.sid = "s1"
	got := rig.deliver(t,
		rig.frame("turn/start", `{"turn":1}`),
		rig.frame("assistant/message", `{"turn":1,"step":1,"interrupted":true,"message":{"role":"assistant","content":[{"type":"text","text":"被打断的话"}]}}`),
		rig.frame("assistant/message", `{"turn":1,"step":2,"parentCallId":"call-9","message":{"role":"assistant","content":[{"type":"text","text":"子代理的话"}]}}`),
		rig.frame("turn/end", `{"turn":1,"reason":{"kind":"completed"}}`),
	)
	want := "\x1b]777;notify;dsh-TUI · Task complete;\x07"
	if got != want {
		t.Fatalf("子代理/被打断的回复进了正文：%q", got)
	}
}

func TestTaskCompleteUsesLatestSettledReplyOfTheTurn(t *testing.T) {
	// 一轮里可能有多条主回复（多步）：用的是**最新一条**，不是拼起来。
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "ghostty"})
	rig.app.sid = "s1"
	got := rig.deliver(t,
		rig.frame("turn/start", `{"turn":1}`),
		rig.frame("assistant/message", `{"turn":1,"step":1,"message":{"role":"assistant","content":[{"type":"text","text":"先看一眼"}]}}`),
		rig.frame("assistant/message", `{"turn":1,"step":3,"message":{"role":"assistant","content":[{"type":"text","text":"最终答复"}]}}`),
		rig.frame("turn/end", `{"turn":1,"reason":{"kind":"completed"}}`),
	)
	if want := "\x1b]777;notify;dsh-TUI · Task complete;最终答复\x07"; got != want {
		t.Fatalf("应取最新一条回复：%q", got)
	}
}

func TestTaskCompleteWaitsForQueuedFollowUp(t *testing.T) {
	// 还有一次提交在飞（= 排队跟进）时不算"任务完成"；结算后也不算，
	// 只有真正空闲下来才发。
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "ghostty"})
	rig.app.sid = "s1"
	rig.buf.Reset()
	rig.app.handleFrame(rig.frame("turn/start", `{"turn":1}`))
	rig.app.handleFrame(rig.frame("turn/end", `{"turn":1,"reason":{"kind":"completed"}}`))
	rig.app.pendingSend = &pendingPrompt{id: 1, sid: "s1", text: "跟一句"}
	rig.app.drainFrames(nil)
	if got := rig.out(); got != "" {
		rig.app.pendingSend = nil
		t.Fatalf("有排队跟进时不该发完成通知，实际 %q", got)
	}
	rig.app.pendingSend = nil
	rig.app.settleCompletion()
	if !strings.Contains(rig.out(), "Task complete") {
		t.Fatalf("排队跟进清掉之后应该发完成通知，实际 %q", rig.out())
	}
}

func TestTaskCompleteDroppedWhenNextTurnStartsInSameBatch(t *testing.T) {
	// 同一批里 turn/end 后面紧跟下一轮的 turn/start：这不是"任务完成"，
	// drain 必须先把这批处理完再判断（否则会发出一条假消息）。
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "ghostty"})
	rig.app.sid = "s1"
	rig.buf.Reset()
	end := rig.frame("turn/end", `{"turn":1,"reason":{"kind":"completed"}}`)
	next := rig.frame("turn/start", `{"turn":2}`)
	ch := make(chan ServerFrame, 4)
	ch <- next
	rig.app.handleFrame(end)
	rig.app.drainFrames(ch)
	if got := rig.out(); got != "" {
		t.Fatalf("同一批里已经开始下一轮，不该发完成通知，实际 %q", got)
	}
}

// ───────────────────────────────────────────────────────────── 授权通知

func TestApprovalNotificationBody(t *testing.T) {
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "ghostty"})
	rig.app.sid = "s1"
	rig.app.notify.sessionTitle = "修登录超时"
	got := rig.deliver(t, rig.frame("approval/request",
		`{"requestId":"req-1","sessionId":"s1","tool":"bash","reason":"要跑一遍 go test","toolCall":{"title":"bash: go test ./...","rawInput":"{}"}}`))
	want := "\x1b]777;notify;dsh-TUI · Approval needed;修登录超时：bash：要跑一遍 go test\x07"
	if got != want {
		t.Fatalf("授权通知字节不对：\n got=%q\nwant=%q", got, want)
	}
}

func TestApprovalNeverReadsToolArguments(t *testing.T) {
	// 工具参数一个字节都不许进正文：宿主把完整命令放在 rawInput/toolCall.title 里。
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "ghostty"})
	rig.app.sid = "s1"
	rig.app.notify.sessionTitle = "会话"
	got := rig.deliver(t, rig.frame("approval/request",
		`{"requestId":"req-2","sessionId":"s1","tool":"bash","reason":"要写文件",`+
			`"toolCall":{"title":"bash: rm -rf SECRET-ARG-7","rawInput":"{\"command\":\"SECRET-ARG-7\"}"}}`))
	for _, leak := range []string{"SECRET-ARG-7", "rm -rf", "command"} {
		if strings.Contains(got, leak) {
			t.Fatalf("工具参数漏进了通知正文：%q", got)
		}
	}
	if !strings.Contains(got, "会话：bash：要写文件") {
		t.Fatalf("正文应只有会话名、工具名与理由：%q", got)
	}
}

func TestApprovalNotifiesOncePerRequest(t *testing.T) {
	// 同一问只通知一次：宿主重连会重发挂起审批（不带 seq），不能弹第二次。
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "ghostty"})
	rig.app.sid = "s1"
	payload := `{"requestId":"req-3","sessionId":"s1","tool":"bash","reason":"要跑命令"}`
	first := rig.deliver(t, rig.frame("approval/request", payload))
	if first == "" {
		t.Fatalf("第一条审批通知应该发出来")
	}
	second := rig.deliver(t, rig.frame("approval/request", payload))
	if second != "" {
		t.Fatalf("同一个 requestId 不该通知第二次：%q", second)
	}
}

func TestApprovalOnlyForCurrentSession(t *testing.T) {
	// 后台会话/其它 agent 的审批保持安静（只留在面板里）。
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "ghostty"})
	rig.app.sid = "s1"
	got := rig.deliver(t, rig.frame("approval/request",
		`{"requestId":"req-4","sessionId":"s2","tool":"bash","reason":"别的会话"}`))
	if got != "" {
		t.Fatalf("别的会话的审批不该通知：%q", got)
	}
	got = rig.deliver(t, rig.frame("approval/request",
		`{"requestId":"req-5","sessionId":"s1","agentId":"sub-1","tool":"bash","reason":"子代理"}`))
	if got != "" {
		t.Fatalf("子代理的审批不该通知：%q", got)
	}
}

func TestApprovalSuppressedOnReplay(t *testing.T) {
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "ghostty"})
	rig.app.sid = "s1"
	rig.app.notify.replayUntil = 2
	got := rig.deliver(t, rig.frame("approval/request",
		`{"requestId":"req-6","sessionId":"s1","tool":"bash","reason":"历史里的审批"}`))
	if got != "" {
		t.Fatalf("重放的审批不该通知：%q", got)
	}
}

// ───────────────────────────────────────────────────────────── 提问与计划评审

func TestQuestionNotificationFromAskTool(t *testing.T) {
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "ghostty"})
	rig.app.sid = "s1"
	args := `{\"questions\":[{\"id\":\"q1\",\"header\":\"确认\",\"question\":\"要继续吗\",` +
		`\"options\":[{\"label\":\"选项甲\"},{\"label\":\"选项乙\"}]}]}`
	got := rig.deliver(t,
		rig.frame("tool/call", `{"callId":"call-1","name":"ask_user_question","arguments":"`+args+`"}`),
		rig.frame("tool/result", `{"callId":"call-1","isError":false,"message":{"isError":false,"content":[{"type":"text","text":"等你回答"}]}}`),
	)
	want := "\x1b]777;notify;dsh-TUI · Waiting for your answer;确认：要继续吗\x07"
	if got != want {
		t.Fatalf("提问通知字节不对：\n got=%q\nwant=%q", got, want)
	}
	for _, leaked := range []string{"选项甲", "选项乙"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("选项不该进正文（留在面板里）：%q", got)
		}
	}
}

func TestQuestionSilentWhenToolFails(t *testing.T) {
	// 工具自己失败了（参数不合法/被取消）：问题没有真的交到人手上，不许通知。
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "ghostty"})
	rig.app.sid = "s1"
	got := rig.deliver(t,
		rig.frame("tool/call", `{"callId":"call-2","name":"ask_user_question","arguments":"{\"questions\":[{\"id\":\"q1\",\"question\":\"问一句\"}]}"}`),
		rig.frame("tool/result", `{"callId":"call-2","isError":true,"message":{"isError":true,"content":[{"type":"text","text":"失败"}]}}`),
	)
	if got != "" {
		t.Fatalf("失败的提问不该通知：%q", got)
	}
}

func TestQuestionSilentWhenNeverSettled(t *testing.T) {
	// 只声明了调用、没有 tool/result（这一轮被取消）：保持安静。
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "ghostty"})
	rig.app.sid = "s1"
	got := rig.deliver(t,
		rig.frame("tool/call", `{"callId":"call-3","name":"ask_user_question","arguments":"{\"questions\":[{\"id\":\"q1\",\"question\":\"问一句\"}]}"}`),
	)
	if got != "" {
		t.Fatalf("没有结算的提问不该通知：%q", got)
	}
}

func TestPlanReviewNotificationNeverIncludesThePlan(t *testing.T) {
	// 计划评审：标题是 Plan review，正文只说"在等你评审"，**从不带计划本身**。
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "ghostty"})
	rig.app.sid = "s1"
	got := rig.deliver(t,
		rig.frame("tool/call", `{"callId":"call-4","name":"exit_plan_mode","arguments":"{\"plan\":\"PLAN-SECRET-BODY 第一步\"}"}`),
		rig.frame("tool/result", `{"callId":"call-4","isError":false,"summary":"方案已提交，等待批准","message":{"isError":false,"content":[{"type":"text","text":"PLAN-SECRET-BODY 第一步"}]}}`),
	)
	want := "\x1b]777;notify;dsh-TUI · Plan review;方案已提交，等待批准\x07"
	if got != want {
		t.Fatalf("计划评审通知字节不对：\n got=%q\nwant=%q", got, want)
	}
	if strings.Contains(got, "PLAN-SECRET-BODY") {
		t.Fatalf("计划正文漏进了通知：%q", got)
	}
}

func TestPlanReviewFallsBackToHostQuestionWhenProvided(t *testing.T) {
	// 宿主若给了 question header/question（协议形状），就按上游规则用它们拼正文。
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "ghostty"})
	rig.app.sid = "s1"
	got := rig.deliver(t,
		rig.frame("tool/call", `{"callId":"call-5","name":"exit_plan_mode","arguments":"{\"plan\":\"方案正文\",\"header\":\"计划评审\",\"question\":\"要开始执行吗\"}"}`),
		rig.frame("tool/result", `{"callId":"call-5","isError":false,"summary":"方案已提交，等待批准"}`),
	)
	want := "\x1b]777;notify;dsh-TUI · Plan review;计划评审：要开始执行吗\x07"
	if got != want {
		t.Fatalf("有 header/question 时应优先用它们：%q", got)
	}
}

// ───────────────────────────────────────────────────────────── 开关与抑制

func TestOffSilencesAllFour(t *testing.T) {
	rig := newNotifyRig(t, notifMethodAuto, notifOff, map[string]string{"TERM_PROGRAM": "ghostty"})
	rig.app.sid = "s1"
	rig.buf.Reset()
	rig.app.handleFrame(rig.frame("turn/start", `{"turn":1}`))
	rig.app.handleFrame(rig.frame("turn/end", `{"turn":1,"reason":{"kind":"completed"}}`))
	rig.app.handleFrame(rig.frame("approval/request", `{"requestId":"r1","sessionId":"s1","tool":"bash","reason":"理由"}`))
	rig.app.handleFrame(rig.frame("tool/call", `{"callId":"c1","name":"ask_user_question","arguments":"{\"questions\":[{\"id\":\"q1\",\"header\":\"确认\",\"question\":\"问题\"}]}"}`))
	rig.app.handleFrame(rig.frame("tool/result", `{"callId":"c1","isError":false}`))
	rig.app.handleFrame(rig.frame("tool/call", `{"callId":"c2","name":"exit_plan_mode","arguments":"{\"plan\":\"方案\"}"}`))
	rig.app.handleFrame(rig.frame("tool/result", `{"callId":"c2","isError":false,"summary":"方案已提交，等待批准"}`))
	rig.app.drainFrames(nil)
	if got := rig.out(); got != "" {
		t.Fatalf("off 档四类都不该通知，实际写出 %q", got)
	}
}

func TestFocusedTerminalSilencesUnfocusedCondition(t *testing.T) {
	// 默认档（unfocused）在终端聚焦时不发——这是"默认不打扰"的语义。
	rig := newNotifyRig(t, notifMethodAuto, notifWhenUnfocused, map[string]string{"TERM_PROGRAM": "ghostty"})
	rig.app.sid = "s1"
	rig.app.notify.focusKnown = true
	rig.app.notify.focused = true
	got := rig.deliver(t,
		rig.frame("turn/start", `{"turn":1}`),
		rig.frame("turn/end", `{"turn":1,"reason":{"kind":"completed"}}`),
	)
	if got != "" {
		t.Fatalf("聚焦时 unfocused 档不该通知：%q", got)
	}
	// 失焦后同一份设置就该发。
	rig.app.notify.focused = false
	got = rig.deliver(t,
		rig.frame("turn/start", `{"turn":2}`),
		rig.frame("turn/end", `{"turn":2,"reason":{"kind":"completed"}}`),
	)
	if !strings.Contains(got, "Task complete") {
		t.Fatalf("失焦后应该通知，实际 %q", got)
	}
}

func TestSessionTitleFrameFeedsApprovalBody(t *testing.T) {
	// 会话名来自宿主的 session/title 帧（真实值），不是本地编的名字。
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "ghostty"})
	rig.app.sid = "s1"
	got := rig.deliver(t,
		rig.frame("session/title", `{"title":"修复登录超时","messageSeqs":[],"source":{"kind":"llm"}}`),
		rig.frame("approval/request", `{"requestId":"req-7","sessionId":"s1","tool":"bash","reason":"跑测试"}`),
	)
	if want := "\x1b]777;notify;dsh-TUI · Approval needed;修复登录超时：bash：跑测试\x07"; got != want {
		t.Fatalf("会话名没有从 session/title 帧来：%q", got)
	}
}

func TestResetNotificationsDropsCrossFrameState(t *testing.T) {
	// 切会话时跨帧状态必须整批作废：上一个会话的提问不能在切过去之后弹。
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "ghostty"})
	rig.app.sid = "s1"
	rig.buf.Reset()
	rig.app.handleFrame(rig.frame("tool/call", `{"callId":"c9","name":"ask_user_question","arguments":"{\"questions\":[{\"id\":\"q1\",\"question\":\"旧会话的问题\"}]}"}`))
	rig.app.resetNotifications("", 0) // 相当于 /resume 切走
	rig.app.handleFrame(rig.frame("tool/result", `{"callId":"c9","isError":false}`))
	rig.app.drainFrames(nil)
	if got := rig.out(); got != "" {
		t.Fatalf("切会话后旧候选不该结算：%q", got)
	}
}

// ───────────────────────────────────────────────────────────── 降级播报

func TestWindowsDegradationIsReportedInBody(t *testing.T) {
	// auto 在本地 Windows → windows 档：投递必须降级为响铃，并把原因写进屏幕。
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, nil)
	rig.notifier.facts.platform = "win32"
	rig.app.sid = "s1"
	rig.buf.Reset()
	rig.app.handleFrame(rig.frame("turn/start", `{"turn":1}`))
	rig.app.handleFrame(rig.frame("turn/end", `{"turn":1,"reason":{"kind":"completed"}}`))
	rig.app.drainFrames(nil)
	if got := rig.buf.String(); got != "\x07" {
		t.Fatalf("windows 档必须降级为响铃，实际 %q", got)
	}
	joined := strings.Join(rig.app.lines, "\n")
	if !strings.Contains(joined, "未实现") {
		t.Fatalf("降级原因必须出现在正文里：%s", joined)
	}
	if !strings.Contains(rig.app.err, "未实现") {
		t.Fatalf("降级原因必须出现在状态行里：%q", rig.app.err)
	}
}

var _ = bytes.MinRead

// TestPlanReviewNeverYieldsToExternalNotifier 钉住"计划评审不让位"这条例外：
// 外部通知插件在线时，完成/授权/提问让位，计划评审照发。
func TestPlanReviewNeverYieldsToExternalNotifier(t *testing.T) {
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "ghostty"})
	rig.app.sid = "s1"
	rig.app.notify.externalNotifier = true

	// 完成：让位（一条都不发）。
	got := rig.deliver(t,
		rig.frame("turn/start", `{"turn":1}`),
		rig.frame("turn/end", `{"turn":1,"reason":{"kind":"completed"}}`),
	)
	if got != "" {
		t.Fatalf("外部插件在线时完成通知应让位：%q", got)
	}
	// 授权：让位。
	got = rig.deliver(t, rig.frame("approval/request",
		`{"requestId":"r1","sessionId":"s1","tool":"bash","reason":"理由"}`))
	if got != "" {
		t.Fatalf("外部插件在线时授权通知应让位：%q", got)
	}
	// 提问：让位。
	got = rig.deliver(t,
		rig.frame("tool/call", `{"callId":"c1","name":"ask_user_question","arguments":"{\"questions\":[{\"id\":\"q1\",\"header\":\"确认\",\"question\":\"问题\"}]}"}`),
		rig.frame("tool/result", `{"callId":"c1","isError":false}`),
	)
	if got != "" {
		t.Fatalf("外部插件在线时提问通知应让位：%q", got)
	}
	// 计划评审：**不让位**。
	got = rig.deliver(t,
		rig.frame("tool/call", `{"callId":"c2","name":"exit_plan_mode","arguments":"{\"plan\":\"方案正文\"}"}`),
		rig.frame("tool/result", `{"callId":"c2","isError":false,"summary":"方案已提交，等待批准"}`),
	)
	if !strings.Contains(got, "dsh-TUI · Plan review") {
		t.Fatalf("计划评审绝不让位：%q", got)
	}
}

// TestPlanReviewWithoutHostTextStaysSilent 钉住最要命的一条：
// 宿主只给了计划正文（tool/result 的 content 块）时，**不许**从计划里凑正文——
// 宁可这一条不发，也不能让计划本身进通知（"从不带计划本身"）。
func TestPlanReviewWithoutHostTextStaysSilent(t *testing.T) {
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "ghostty"})
	rig.app.sid = "s1"
	got := rig.deliver(t,
		rig.frame("tool/call", `{"callId":"call-6","name":"exit_plan_mode","arguments":"{\"plan\":\"PLAN-SECRET 第一步 第二步\"}"}`),
		rig.frame("tool/result", `{"callId":"call-6","isError":false,"message":{"isError":false,"content":[{"type":"text","text":"PLAN-SECRET 第一步 第二步"}]}}`),
	)
	if got != "" {
		t.Fatalf("宿主没给问题文本时不该发计划评审通知（会带出计划正文）：%q", got)
	}
	if strings.Contains(got, "PLAN-SECRET") {
		t.Fatalf("计划正文漏进了通知：%q", got)
	}
}

// TestPlanReviewDetectedByQuestionIntent 钉住计划评审的**上游判据**：
// 判别字段是问题对象的 intent.kind=plan-review，不是"哪个工具问的"。
// 本宿主的 ask_user_question 会把问题对象原样带进 tool/call 的 arguments。
func TestPlanReviewDetectedByQuestionIntent(t *testing.T) {
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "ghostty"})
	rig.app.sid = "s1"
	args := `{\"questions\":[{\"id\":\"q1\",\"header\":\"计划评审\",\"question\":\"要开始执行吗\",` +
		`\"intent\":{\"kind\":\"plan-review\"},\"options\":[{\"label\":\"批准\"}]}]}`
	got := rig.deliver(t,
		rig.frame("tool/call", `{"callId":"call-plan","name":"ask_user_question","arguments":"`+args+`"}`),
		rig.frame("tool/result", `{"callId":"call-plan","isError":false}`),
	)
	want := "\x1b]777;notify;dsh-TUI · Plan review;计划评审：要开始执行吗\x07"
	if got != want {
		t.Fatalf("计划评审（intent 判据）字节不对：\n got=%q\nwant=%q", got, want)
	}
	if strings.Contains(got, "批准") {
		t.Fatalf("选项不该进正文：%q", got)
	}
}

// TestQuestionWithoutPlanIntentIsNotPlanReview 确认判别字段真的在起作用：
// 同样的 header/question，没有 intent 时是"等待回答"，不是"计划评审"。
func TestQuestionWithoutPlanIntentIsNotPlanReview(t *testing.T) {
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "ghostty"})
	rig.app.sid = "s1"
	args := `{\"questions\":[{\"id\":\"q1\",\"header\":\"确认\",\"question\":\"要继续吗\"}]}`
	got := rig.deliver(t,
		rig.frame("tool/call", `{"callId":"call-q","name":"ask_user_question","arguments":"`+args+`"}`),
		rig.frame("tool/result", `{"callId":"call-q","isError":false}`),
	)
	if !strings.Contains(got, "dsh-TUI · Waiting for your answer") {
		t.Fatalf("没有 plan-review intent 时应走提问通知：%q", got)
	}
}

// TestTaskCompleteAfterApprovalStillNotifies 钉住"一轮里有审批时完成通知照发"。
// 真机 2026-11-13 抓到过这一条**没发**（同轮有 approval/request → 答完 → 工具失败
// → 模型答复 → turn/end），所以这里把那条帧序列按顺序复现，防止再退化。
func TestTaskCompleteAfterApprovalStillNotifies(t *testing.T) {
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "ghostty"})
	rig.app.sid = "s1"
	got := rig.deliver(t,
		rig.frame("turn/start", `{"turn":2}`),
		rig.frame("approval/request", `{"requestId":"req-9","sessionId":"s1","tool":"write","reason":"","toolCall":{"title":"write: a.txt"}}`),
		rig.frame("approval/answered", `{"requestId":"req-9","decision":"allow_once"}`),
		rig.frame("tool/call", `{"turn":2,"callId":"call-w","name":"write","arguments":"{\"file_path\":\"a.txt\",\"content\":\"x\"}"}`),
		rig.frame("tool/result", `{"turn":2,"callId":"call-w","isError":true,"message":{"isError":true,"content":[{"type":"text","text":"ERROR: file has not been read"}]}}`),
		rig.frame("assistant/message", `{"turn":2,"step":2,"message":{"role":"assistant","content":[{"type":"text","text":"这次 write 被拒了，文件没动"}]}}`),
		rig.frame("turn/end", `{"turn":2,"reason":{"kind":"completed"}}`),
	)
	if !strings.Contains(got, "dsh-TUI · Task complete") {
		t.Fatalf("同轮有审批时完成通知也必须发：%q", got)
	}
	if !strings.Contains(got, "这次 write 被拒了") {
		t.Fatalf("完成通知应带最新回复预览：%q", got)
	}
}

// TestTaskCompleteForEveryTurnWhenHostNumbersTurnsFromOne 钉住真机的**轮次形状**：
// 本仓宿主是"一轮一个 serve 子进程"，帧里的 `turn` 每个 prompt 都从 1 开始
// （真机取证 evidence/probe-report.json：三轮全是 {"turn":1}）。
// 所以轮次身份必须由 turn/start 自己数（epoch），不能拿 `turn` 当单调序号——
// 那样第二轮及以后的完成通知永远不发（2026-11-13 真机抓包抓到的就是这条）。
func TestTaskCompleteForEveryTurnWhenHostNumbersTurnsFromOne(t *testing.T) {
	rig := newNotifyRig(t, notifMethodAuto, notifAlways, map[string]string{"TERM_PROGRAM": "ghostty"})
	rig.app.sid = "s1"

	first := rig.deliver(t,
		rig.frame("turn/start", `{"turn":1}`),
		rig.frame("assistant/message", `{"turn":1,"step":1,"message":{"role":"assistant","content":[{"type":"text","text":"第一轮答复"}]}}`),
		rig.frame("turn/end", `{"turn":1,"reason":{"kind":"completed"}}`),
	)
	if !strings.Contains(first, "第一轮答复") {
		t.Fatalf("第一轮完成通知没发或正文不对：%q", first)
	}
	second := rig.deliver(t,
		rig.frame("turn/start", `{"turn":1}`),
		rig.frame("assistant/message", `{"turn":1,"step":1,"message":{"role":"assistant","content":[{"type":"text","text":"第二轮答复"}]}}`),
		rig.frame("turn/end", `{"turn":1,"reason":{"kind":"completed"}}`),
	)
	if !strings.Contains(second, "第二轮答复") {
		t.Fatalf("第二轮完成通知没发（宿主 turn 恒为 1 时被误判成同一轮）：%q", second)
	}
	// 同一轮的 turn/end 重复到达（重放/重复帧）不许发第二条。
	again := rig.deliver(t, rig.frame("turn/end", `{"turn":1,"reason":{"kind":"completed"}}`))
	if again != "" {
		t.Fatalf("同一轮的重复 turn/end 不该再发通知：%q", again)
	}
}
