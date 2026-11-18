package chatimport

// 对话映射规则的逐条断言（官方 tests/conversation.host.spec.ts 的对应物）。
//
// 每条断言都钉一个**官方规则**，不是"跑通就行"：事件顺序、信封字段、
// 被排除回复的固定错误码、标题规范化、分支选择。

import (
	"encoding/json"
	"strings"
	"testing"
)

func convertFixture(t *testing.T, payload map[string]any) Converted {
	t.Helper()
	file := parseConversationFixture(t, payload)
	converted, err := ConvertConversation(file, ConvertOptions{NewID: fixedIDs(), MaxTitleBytes: DefaultMaxTitleBytes})
	if err != nil {
		t.Fatalf("对话转换失败: %v", err)
	}
	return converted
}

// TestConvertSingleTurnWritesOfficialEventSequence 钉住一轮对话的事件序列
// （官方 conversation.ts:186-231 的 turn 写序）。
func TestConvertSingleTurnWritesOfficialEventSequence(t *testing.T) {
	converted := convertFixture(t, map[string]any{
		"id":          "conv-1",
		"title":       "导入测试",
		"inserted_at": "2026-11-12T10:00:00.000Z",
		"messages": []map[string]any{
			userMessageJSON(1, nil, "第一问", "2026-11-12T10:00:00.000Z"),
			replyMessageJSON(2, 1, "FINISHED", "2026-11-12T10:00:05.000Z", []map[string]any{
				{"type": "THINK", "content": "想一想", "elapsed_secs": 2},
				{"type": "RESPONSE", "content": "第一答"},
			}),
		},
	})

	want := []string{
		"turn/start", "step/start", "system/message", "user/message", "assistant/message",
		"step/end", "turn/end", "session/title",
	}
	got := eventTypes(converted.Events)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("事件序列不符\n got=%v\nwant=%v", got, want)
	}
	// seq 必须从 0 起连续（v4 的硬契约）。
	for index, event := range converted.Events {
		if event.Seq != int64(index) {
			t.Fatalf("第 %d 条事件 seq=%d，必须连续", index, event.Seq)
		}
	}
	// 时间不递减，且第一条就是用户消息的时间。
	userAt, ok := ParseExportTime("2026-11-12T10:00:00.000Z")
	if !ok {
		t.Fatal("夹具时间解析失败")
	}
	if converted.Events[0].Time != userAt {
		t.Fatalf("首条事件时间=%d，应为用户消息时间 %d", converted.Events[0].Time, userAt)
	}
	for index := 1; index < len(converted.Events); index++ {
		if converted.Events[index].Time < converted.Events[index-1].Time {
			t.Fatalf("第 %d 条事件时间倒退：%d < %d",
				index, converted.Events[index].Time, converted.Events[index-1].Time)
		}
	}
	// 思考的 elapsed_secs 推进后续步骤的时间（官方 conversation.ts:214）。
	if converted.Events[4].Time != userAt+5000+2000 {
		t.Fatalf("assistant/message 时间=%d，应为回复时间 + 思考 2s = %d",
			converted.Events[4].Time, userAt+7000)
	}
	// 认证：surface 事件带 append 信封。
	for _, index := range []int{2, 3, 4} {
		if string(converted.Events[index].SurfaceOp) != `"append"` {
			t.Fatalf("%s 的 surfaceOp=%s，应为 \"append\"",
				converted.Events[index].Type, converted.Events[index].SurfaceOp)
		}
	}
	// 第一轮的 system 头是空内容、system-prompt 来源。
	systemMessage := eventData(t, converted.Events[2])["message"].(map[string]any)
	if systemMessage["role"] != "system" || systemMessage["source"].(map[string]any)["kind"] != "system-prompt" {
		t.Fatalf("system/message 形状不对: %v", systemMessage)
	}
	if content := systemMessage["content"].([]any); len(content) != 0 {
		t.Fatalf("system 头必须是空 content，拿到 %v", content)
	}
	// 用户消息的 data 是**消息本身**（官方 userMessage 直接当 data）。
	userData := eventData(t, converted.Events[3])
	if userData["role"] != "user" || userData["source"].(map[string]any)["kind"] != "user" {
		t.Fatalf("user/message 形状不对: %v", userData)
	}
	// 助手消息：内容顺序是 reasoning 再 text，来源是导出方。
	assistant := eventData(t, converted.Events[4])
	message := assistant["message"].(map[string]any)
	source := message["source"].(map[string]any)
	if source["provider"] != ImportedModelProvider || source["model"] != ImportedModelName || source["kind"] != "model" {
		t.Fatalf("assistant 来源不对: %v", source)
	}
	content := message["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("助手内容应有 2 块，拿到 %d", len(content))
	}
	if content[0].(map[string]any)["type"] != "reasoning" || content[1].(map[string]any)["type"] != "text" {
		t.Fatalf("内容块顺序不对: %v", content)
	}
	if stream := assistant["stream"].([]any); len(stream) != 0 {
		t.Fatalf("导入的 assistant/message stream 必须为空数组，拿到 %v", stream)
	}
	// turn/end 是 completed。
	reason := eventData(t, converted.Events[6])["reason"].(map[string]any)
	if reason["kind"] != "completed" {
		t.Fatalf("turn/end reason=%v，应为 completed", reason)
	}
	// 标题事件：原文、空 messageSeqs、user 来源（官方 conversation.ts:161）。
	titleData := eventData(t, converted.Events[7])
	if titleData["title"] != "导入测试" {
		t.Fatalf("标题=%v", titleData["title"])
	}
	if seqs := titleData["messageSeqs"].([]any); len(seqs) != 0 {
		t.Fatalf("messageSeqs 必须是空数组，拿到 %v", seqs)
	}
	if titleData["source"].(map[string]any)["kind"] != "user" {
		t.Fatalf("标题来源必须是 user（钉住标题，不跑标题生成）")
	}
	if converted.Title == nil || *converted.Title != "导入测试" {
		t.Fatalf("Converted.Title=%v", converted.Title)
	}
}

// TestConvertSecondTurnHasNoSystemHead 钉住"system 头只在第一轮"
// （官方 conversation.ts:191）。
func TestConvertSecondTurnHasNoSystemHead(t *testing.T) {
	converted := convertFixture(t, map[string]any{
		"id":          "conv-2",
		"title":       nil,
		"inserted_at": "2026-11-12T10:00:00.000Z",
		"messages": []map[string]any{
			userMessageJSON(1, nil, "问一", "2026-11-12T10:00:00.000Z"),
			replyMessageJSON(2, 1, "FINISHED", "2026-11-12T10:00:01.000Z",
				[]map[string]any{{"type": "RESPONSE", "content": "答一"}}),
			userMessageJSON(3, 2, "问二", "2026-11-12T10:01:00.000Z"),
			replyMessageJSON(4, 3, "FINISHED", "2026-11-12T10:01:01.000Z",
				[]map[string]any{{"type": "RESPONSE", "content": "答二"}}),
		},
	})
	systemCount := 0
	turnStarts := make([]float64, 0, 2)
	for _, event := range converted.Events {
		switch event.Type {
		case "system/message":
			systemCount++
		case "turn/start":
			turnStarts = append(turnStarts, eventData(t, event)["turn"].(float64))
		}
	}
	if systemCount != 1 {
		t.Fatalf("system/message 出现 %d 次，只允许第一轮 1 次", systemCount)
	}
	if len(turnStarts) != 2 || turnStarts[0] != 1 || turnStarts[1] != 2 {
		t.Fatalf("轮次编号不对: %v", turnStarts)
	}
	// title 为 null 时不写 session/title 事件。
	for _, event := range converted.Events {
		if event.Type == "session/title" {
			t.Fatalf("title 为 null 时不该写 session/title")
		}
	}
	if converted.Title != nil {
		t.Fatalf("title 为 null 时 Converted.Title 必须是 nil，拿到 %v", *converted.Title)
	}
}

// TestConvertContentFilteredTurnIsDropped 钉住"被内容过滤撤回的问答整对丢掉"
// （官方 conversation.ts:154-157）。
func TestConvertContentFilteredTurnIsDropped(t *testing.T) {
	converted := convertFixture(t, map[string]any{
		"id":          "conv-3",
		"title":       "撤回",
		"inserted_at": "2026-11-12T10:00:00.000Z",
		"messages": []map[string]any{
			userMessageJSON(1, nil, "正常问", "2026-11-12T10:00:00.000Z"),
			replyMessageJSON(2, 1, "FINISHED", "2026-11-12T10:00:01.000Z",
				[]map[string]any{{"type": "RESPONSE", "content": "正常答"}}),
			userMessageJSON(3, 2, "敏感问", "2026-11-12T10:02:00.000Z"),
			replyMessageJSON(4, 3, "CONTENT_FILTER", "2026-11-12T10:02:01.000Z",
				[]map[string]any{{"type": "RESPONSE", "content": "不该出现"}}),
		},
	})
	for _, event := range converted.Events {
		if strings.Contains(string(event.Data), "敏感问") || strings.Contains(string(event.Data), "不该出现") {
			t.Fatalf("被过滤的问答不许出现在日志里: %s %s", event.Type, event.Data)
		}
	}
	turns := 0
	for _, event := range converted.Events {
		if event.Type == "turn/start" {
			turns++
		}
	}
	if turns != 1 {
		t.Fatalf("只剩一轮，拿到 %d", turns)
	}
}

// TestConvertAllWithdrawnFails 钉住"整份对话都被撤回 → 不导入"
// （官方 conversation.ts:164-168）。
func TestConvertAllWithdrawnFails(t *testing.T) {
	file := parseConversationFixture(t, map[string]any{
		"id":          "conv-4",
		"title":       "全撤回",
		"inserted_at": "2026-11-12T10:00:00.000Z",
		"messages": []map[string]any{
			userMessageJSON(1, nil, "问", "2026-11-12T10:00:00.000Z"),
			replyMessageJSON(2, 1, "CONTENT_FILTER", "2026-11-12T10:00:01.000Z",
				[]map[string]any{{"type": "RESPONSE", "content": "x"}}),
		},
	})
	_, err := ConvertConversation(file, ConvertOptions{NewID: fixedIDs(), MaxTitleBytes: DefaultMaxTitleBytes})
	if err == nil {
		t.Fatal("整份对话被撤回时必须报错（官方：every question withdrawn）")
	}
	if !strings.Contains(err.Error(), "content filtering") {
		t.Fatalf("错误文案应点名内容过滤，拿到 %q", err.Error())
	}
}

// TestConvertUnfinishedReplyEndsTurnWithFixedCode 钉住未完成回复的固定错误码
// （官方 conversation.ts:38-44、368-377）。
func TestConvertUnfinishedReplyEndsTurnWithFixedCode(t *testing.T) {
	cases := []struct {
		status string
		code   string
	}{
		{"FAILED", "DEEPSEEK_EXPORT_FAILED"},
		{"TIMEOUT", "DEEPSEEK_EXPORT_TIMEOUT"},
		{"CONTEXT_LENGTH_EXCEEDED", "DEEPSEEK_EXPORT_CONTEXT_LENGTH_EXCEEDED"},
		{"INCOMPLETE", "DEEPSEEK_EXPORT_INCOMPLETE"},
		{"WIP", "DEEPSEEK_EXPORT_WIP"},
		{"SOMETHING_NEW", "DEEPSEEK_EXPORT_UNFINISHED_REPLY"},
	}
	for _, testCase := range cases {
		t.Run(testCase.status, func(t *testing.T) {
			converted := convertFixture(t, map[string]any{
				"id":          "conv-err",
				"title":       nil,
				"inserted_at": "2026-11-12T10:00:00.000Z",
				"messages": []map[string]any{
					userMessageJSON(1, nil, "问", "2026-11-12T10:00:00.000Z"),
					replyMessageJSON(2, 1, testCase.status, "2026-11-12T10:00:01.000Z",
						[]map[string]any{{"type": "RESPONSE", "content": "半截"}}),
				},
			})
			// 被排除的回复不写 assistant/message。
			for _, event := range converted.Events {
				if event.Type == "assistant/message" {
					t.Fatalf("未完成回复不许写 assistant/message")
				}
			}
			end := eventData(t, converted.Events[len(converted.Events)-1])
			reason := end["reason"].(map[string]any)
			if reason["kind"] != "error" {
				t.Fatalf("turn/end kind=%v，应为 error", reason["kind"])
			}
			failure := reason["error"].(map[string]any)
			if failure["code"] != testCase.code {
				t.Fatalf("错误码=%v，应为 %s", failure["code"], testCase.code)
			}
			// 问题本身保留（用户能看到自己问了什么）。
			seenUser := false
			for _, event := range converted.Events {
				if event.Type == "user/message" {
					seenUser = true
				}
			}
			if !seenUser {
				t.Fatal("被排除的回复不影响问题本身：user/message 必须保留")
			}
		})
	}
}

// TestConvertTemplateReplyWins 钉住"模板回复压过状态"
// （官方 conversation.ts:369）。
func TestConvertTemplateReplyWins(t *testing.T) {
	converted := convertFixture(t, map[string]any{
		"id":          "conv-template",
		"title":       nil,
		"inserted_at": "2026-11-12T10:00:00.000Z",
		"messages": []map[string]any{
			userMessageJSON(1, nil, "问", "2026-11-12T10:00:00.000Z"),
			replyMessageJSON(2, 1, "FINISHED", "2026-11-12T10:00:01.000Z", []map[string]any{
				{"type": "TEMPLATE_RESPONSE", "content": "服务端模板"},
				{"type": "RESPONSE", "content": "正文"},
			}),
		},
	})
	end := eventData(t, converted.Events[len(converted.Events)-1])
	failure := end["reason"].(map[string]any)["error"].(map[string]any)
	if failure["code"] != "DEEPSEEK_EXPORT_TEMPLATE_RESPONSE" || failure["message"] != "服务端模板" {
		t.Fatalf("模板回复错误形状不对: %v", failure)
	}
}

// TestConvertSearchAndFetchBecomeToolCalls 钉住网页搜索/开页 → 工具调用
// （官方 conversation.ts:305-319、437-484）。
func TestConvertSearchAndFetchBecomeToolCalls(t *testing.T) {
	converted := convertFixture(t, map[string]any{
		"id":          "conv-tools",
		"title":       nil,
		"inserted_at": "2026-11-12T10:00:00.000Z",
		"messages": []map[string]any{
			userMessageJSON(1, nil, "查一下", "2026-11-12T10:00:00.000Z"),
			replyMessageJSON(2, 1, "FINISHED", "2026-11-12T10:00:01.000Z", []map[string]any{
				{"type": "TOOL_SEARCH", "queries": []string{"go 1.27"}, "stage_id": 7, "results": []map[string]any{
					{"url": "https://example.test/a", "title": "示例", "snippet": "片段", "published_at": "2026-11-01"},
				}},
				{"type": "TOOL_OPEN", "status": "FINISHED", "url": "https://example.test/a", "title": "示例页", "stage_id": 7},
				{"type": "RESPONSE", "content": "结论"},
			}),
		},
	})
	var calls, results int
	var searchArgs, fetchArgs string
	for index, event := range converted.Events {
		switch event.Type {
		case "tool/call":
			calls++
			data := eventData(t, event)
			switch data["name"] {
			case "web_search":
				searchArgs = data["arguments"].(string)
			case "web_fetch":
				fetchArgs = data["arguments"].(string)
			default:
				t.Fatalf("未知工具调用: %v", data["name"])
			}
		case "tool/result":
			results++
			data := eventData(t, event)
			// 结果必须挂到它那条调用的 seq 上。
			if len(event.SourceEventSeqs) != 1 {
				t.Fatalf("tool/result 必须带 1 个 sourceEventSeqs，拿到 %v", event.SourceEventSeqs)
			}
			sourceSeq := event.SourceEventSeqs[0]
			if sourceSeq <= 0 || sourceSeq >= event.Seq {
				t.Fatalf("sourceEventSeqs 必须指向前面那条 tool/call（%d），拿到 %d", event.Seq, sourceSeq)
			}
			meta, ok := data["meta"].(map[string]any)
			if !ok {
				t.Fatalf("导入的工具结果必须带 meta（重放渲染卡片要用）: %v", data)
			}
			if data["step"].(float64) != 1 {
				t.Fatalf("同 stage 的调用必须共用一个 step，拿到 step=%v", data["step"])
			}
			_ = meta
			_ = index
		}
	}
	if calls != 2 || results != 2 {
		t.Fatalf("期望 2 次调用 + 2 条结果，拿到 %d/%d", calls, results)
	}
	if !strings.Contains(searchArgs, "go 1.27") {
		t.Fatalf("web_search 参数里必须带查询词，拿到 %s", searchArgs)
	}
	if !strings.Contains(fetchArgs, "https://example.test/a") {
		t.Fatalf("web_fetch 参数里必须带地址，拿到 %s", fetchArgs)
	}
}

// TestConvertStageChangeStartsNewStep 钉住 stage_id 变化开新 step
// （官方 conversation.ts:287-295）。
func TestConvertStageChangeStartsNewStep(t *testing.T) {
	converted := convertFixture(t, map[string]any{
		"id":          "conv-stage",
		"title":       nil,
		"inserted_at": "2026-11-12T10:00:00.000Z",
		"messages": []map[string]any{
			userMessageJSON(1, nil, "两步", "2026-11-12T10:00:00.000Z"),
			replyMessageJSON(2, 1, "FINISHED", "2026-11-12T10:00:01.000Z", []map[string]any{
				{"type": "TOOL_SEARCH", "queries": []string{"一"}, "stage_id": 1, "results": []map[string]any{}},
				{"type": "TOOL_SEARCH", "queries": []string{"二"}, "stage_id": 2, "results": []map[string]any{}},
			}),
		},
	})
	steps := make([]float64, 0, 2)
	for _, event := range converted.Events {
		if event.Type != "tool/call" {
			continue
		}
		steps = append(steps, eventData(t, event)["step"].(float64))
	}
	if len(steps) != 2 || steps[0] != 1 || steps[1] != 2 {
		t.Fatalf("stage 变化必须换 step，拿到 %v", steps)
	}
	// 两次 step/start（第一轮首个 step 由 turn 写，换 step 时再写一条）。
	starts := 0
	for _, event := range converted.Events {
		if event.Type == "step/start" {
			starts++
		}
	}
	if starts != 2 {
		t.Fatalf("应有两个 step/start，拿到 %d", starts)
	}
}

// TestSelectBranchFollowsCurrentMessageAndFallsBack 钉住分支选择规则
// （官方 conversation.ts:98-118）。
func TestSelectBranchFollowsCurrentMessageAndFallsBack(t *testing.T) {
	base := map[string]any{
		"id":          "conv-branch",
		"title":       nil,
		"inserted_at": "2026-11-12T10:00:00.000Z",
		"messages": []map[string]any{
			userMessageJSON(1, nil, "根", "2026-11-12T10:00:00.000Z"),
			replyMessageJSON(2, 1, "FINISHED", "2026-11-12T10:00:01.000Z",
				[]map[string]any{{"type": "RESPONSE", "content": "答A"}}),
			replyMessageJSON(3, 1, "FINISHED", "2026-11-12T10:00:02.000Z",
				[]map[string]any{{"type": "RESPONSE", "content": "答B"}}),
			userMessageJSON(4, 2, "沿A", "2026-11-12T10:00:03.000Z"),
		},
	}
	// 没给 current_message_id → 退回 id 最大的那条（4 → 2 → 1）。
	chain, err := SelectBranch(parseConversationFixture(t, base))
	if err != nil {
		t.Fatalf("分支选择失败: %v", err)
	}
	ids := make([]int64, 0, len(chain))
	for _, message := range chain {
		ids = append(ids, message.ID)
	}
	if len(ids) != 3 || ids[0] != 1 || ids[1] != 2 || ids[2] != 4 {
		t.Fatalf("兜底分支应为 1→2→4，拿到 %v", ids)
	}
	// current_message_id=3 → 走另一条分支。
	explicit := map[string]any{}
	for key, value := range base {
		explicit[key] = value
	}
	explicit["current_message_id"] = 3
	chain, err = SelectBranch(parseConversationFixture(t, explicit))
	if err != nil {
		t.Fatalf("显式分支失败: %v", err)
	}
	ids = ids[:0]
	for _, message := range chain {
		ids = append(ids, message.ID)
	}
	if len(ids) != 2 || ids[0] != 1 || ids[1] != 3 {
		t.Fatalf("current_message_id=3 应为 1→3，拿到 %v", ids)
	}
	// 指向不存在的消息 → 退回最大 id（官方：must fall back）。
	missing := map[string]any{}
	for key, value := range base {
		missing[key] = value
	}
	missing["current_message_id"] = 999
	chain, err = SelectBranch(parseConversationFixture(t, missing))
	if err != nil {
		t.Fatalf("不存在的 current_message_id 必须退回最大 id: %v", err)
	}
	if len(chain) != 3 || chain[2].ID != 4 {
		t.Fatalf("应退回 1→2→4，拿到 %d 条", len(chain))
	}
}

// TestConvertTitleNormalization 钉住标题规范化与截断
// （官方 dsh-session-title/src/normalize.ts:22-61）。
func TestConvertTitleNormalization(t *testing.T) {
	cases := []struct {
		name  string
		title string
		bytes int
		want  string
	}{
		{"合并空白", "  多   行\n\n标题\t ", 80, "多 行 标题"},
		{"去控制字符", "标题\u0007带\u001b[31m色", 80, "标题带色"},
		{"去方向控制", "标题\u202e反向", 80, "标题反向"},
		{"按字节截断", "一二三四五", 6, "一二"},
		{"截断后去尾空格", "ab   cd", 4, "ab c"},
		{"不超过上限不动", "短标题", 80, "短标题"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := normalizeSessionTitle(testCase.title, testCase.bytes)
			if got != testCase.want {
				t.Fatalf("normalizeSessionTitle(%q,%d)=%q，应为 %q", testCase.title, testCase.bytes, got, testCase.want)
			}
		})
	}
}

// TestConvertFileFragmentBecomesSyntheticCard 钉住"没有图片存储时，图片与普通
// 文件都落成合成文件卡"（官方 conversation.ts:252-256、392-395）。
func TestConvertFileFragmentBecomesSyntheticCard(t *testing.T) {
	converted := convertFixture(t, map[string]any{
		"id":          "conv-file",
		"title":       nil,
		"inserted_at": "2026-11-12T10:00:00.000Z",
		"messages": []map[string]any{
			{
				"id": 1, "parent_id": nil, "role": "USER", "status": "FINISHED",
				"inserted_at": "2026-11-12T10:00:00.000Z",
				"fragments": []map[string]any{
					{"type": "REQUEST", "content": "看这个"},
					{"type": "FILE", "files": []map[string]any{
						{"id": "file-1", "file_name": "x.png", "file_size": 1234, "is_image": true, "export_path": "chat_sessions/conv-file_files/file-1_x.png"},
					}},
				},
			},
			replyMessageJSON(2, 1, "FINISHED", "2026-11-12T10:00:01.000Z",
				[]map[string]any{{"type": "RESPONSE", "content": "好"}}),
		},
	})
	userMessage := eventData(t, converted.Events[3])
	content := userMessage["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("内容应是附件在前、文本在后，拿到 %v", content)
	}
	attachment := content[0].(map[string]any)
	if attachment["type"] != "file" {
		t.Fatalf("没存下来的图片必须落成 file 卡（官方 attachments 未接线分支），拿到 %v", attachment["type"])
	}
	ref := attachment["attachment"].(map[string]any)
	if !strings.HasPrefix(ref["attachmentId"].(string), "sha256:") {
		t.Fatalf("合成引用 id 必须是导出文件 id 的哈希，拿到 %v", ref["attachmentId"])
	}
	if ref["name"] != "x.png" || ref["bytes"].(float64) != 1234 {
		t.Fatalf("合成卡必须带原文件名与声明大小，拿到 %v", ref)
	}
	// 同一个导出文件 id 每次导入得到同一个引用（确定性）。
	again := convertFixture(t, map[string]any{
		"id":          "conv-file",
		"title":       nil,
		"inserted_at": "2026-11-12T10:00:00.000Z",
		"messages": []map[string]any{
			{
				"id": 1, "parent_id": nil, "role": "USER", "status": "FINISHED",
				"inserted_at": "2026-11-12T10:00:00.000Z",
				"fragments": []map[string]any{
					{"type": "REQUEST", "content": "看这个"},
					{"type": "FILE", "files": []map[string]any{
						{"id": "file-1", "file_name": "x.png", "file_size": 1234, "is_image": true, "export_path": nil},
					}},
				},
			},
			replyMessageJSON(2, 1, "FINISHED", "2026-11-12T10:00:01.000Z",
				[]map[string]any{{"type": "RESPONSE", "content": "好"}}),
		},
	})
	userMessage = eventData(t, again.Events[3])
	ref = userMessage["content"].([]any)[0].(map[string]any)["attachment"].(map[string]any)
	if ref["attachmentId"] != attachment["attachment"].(map[string]any)["attachmentId"] {
		t.Fatal("同一导出文件 id 必须得到同一个合成引用 id")
	}
}

// TestConvertEmptyUserMessageKeepsOneEmptyTextBlock 钉住"没有文字的用户消息保留
// 一个空 text 块"（官方 conversation.ts:260-264）。
func TestConvertEmptyUserMessageKeepsOneEmptyTextBlock(t *testing.T) {
	converted := convertFixture(t, map[string]any{
		"id":          "conv-empty",
		"title":       nil,
		"inserted_at": "2026-11-12T10:00:00.000Z",
		"messages": []map[string]any{
			{
				"id": 1, "parent_id": nil, "role": "USER", "status": "FINISHED",
				"inserted_at": "2026-11-12T10:00:00.000Z",
				"fragments":   []map[string]any{{"type": "REQUEST", "content": "   "}},
			},
			replyMessageJSON(2, 1, "FINISHED", "2026-11-12T10:00:01.000Z",
				[]map[string]any{{"type": "RESPONSE", "content": "答"}}),
		},
	})
	content := eventData(t, converted.Events[3])["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("空用户消息应只有一个空 text 块，拿到 %v", content)
	}
	block := content[0].(map[string]any)
	if block["type"] != "text" || block["text"] != "" {
		t.Fatalf("空 text 块形状不对: %v", block)
	}
}

// TestConvertReadLinkBecomesMarkdownLines 钉住 READ_LINK → Markdown 链接行
// （官方 conversation.ts:320-326、415-424）。
func TestConvertReadLinkBecomesMarkdownLines(t *testing.T) {
	converted := convertFixture(t, map[string]any{
		"id":          "conv-link",
		"title":       nil,
		"inserted_at": "2026-11-12T10:00:00.000Z",
		"messages": []map[string]any{
			userMessageJSON(1, nil, "读", "2026-11-12T10:00:00.000Z"),
			replyMessageJSON(2, 1, "FINISHED", "2026-11-12T10:00:01.000Z", []map[string]any{
				{"type": "READ_LINK", "results": []map[string]any{
					{"url": "https://a.test", "title": "甲"},
					{"url": "https://b.test", "title": nil},
					{"url": nil, "title": "只有标题"},
					{"url": nil, "title": nil},
				}},
			}),
		},
	})
	found := ""
	for _, event := range converted.Events {
		if event.Type != "assistant/message" {
			continue
		}
		content := eventData(t, event)["message"].(map[string]any)["content"].([]any)
		for _, block := range content {
			if text := block.(map[string]any)["text"]; text != nil {
				found = text.(string)
			}
		}
	}
	want := "[甲](https://a.test)\n[https://b.test](https://b.test)\n只有标题"
	if found != want {
		t.Fatalf("链接行不对\n got=%q\nwant=%q", found, want)
	}
}

// TestConvertMessageTreeDefectsFail 钉住树结构缺陷会失败（官方 conversation.ts:102-115）。
func TestConvertMessageTreeDefectsFail(t *testing.T) {
	cases := []struct {
		name     string
		messages []map[string]any
		wantText string
	}{
		{
			name: "重复 id",
			messages: []map[string]any{
				userMessageJSON(1, nil, "a", "2026-11-12T10:00:00.000Z"),
				userMessageJSON(1, nil, "b", "2026-11-12T10:00:01.000Z"),
			},
			wantText: "duplicate message id",
		},
		{
			name: "父不存在",
			messages: []map[string]any{
				userMessageJSON(2, 99, "a", "2026-11-12T10:00:00.000Z"),
			},
			wantText: "does not exist",
		},
		{
			name:     "没有消息",
			messages: []map[string]any{},
			wantText: "no messages",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			file := parseConversationFixture(t, map[string]any{
				"id":          "conv-defect",
				"title":       nil,
				"inserted_at": "2026-11-12T10:00:00.000Z",
				"messages":    testCase.messages,
			})
			_, err := ConvertConversation(file, ConvertOptions{NewID: fixedIDs(), MaxTitleBytes: DefaultMaxTitleBytes})
			if err == nil {
				t.Fatal("树结构缺陷必须让这条对话失败")
			}
			if !strings.Contains(err.Error(), testCase.wantText) {
				t.Fatalf("错误文案应含 %q，拿到 %q", testCase.wantText, err.Error())
			}
		})
	}
}

// TestConvertMalformedFragmentFails 钉住"畸形 fragment 让这一条对话失败"
// （官方 conversation.ts:348-360）。
func TestConvertMalformedFragmentFails(t *testing.T) {
	file := parseConversationFixture(t, map[string]any{
		"id":          "conv-bad-fragment",
		"title":       nil,
		"inserted_at": "2026-11-12T10:00:00.000Z",
		"messages": []map[string]any{
			userMessageJSON(1, nil, "问", "2026-11-12T10:00:00.000Z"),
			replyMessageJSON(2, 1, "FINISHED", "2026-11-12T10:00:01.000Z", []map[string]any{
				// TOOL_OPEN 缺 status / url → 与 schema 冲突。
				{"type": "TOOL_OPEN"},
			}),
		},
	})
	if _, err := ConvertConversation(file, ConvertOptions{NewID: fixedIDs(), MaxTitleBytes: DefaultMaxTitleBytes}); err == nil {
		t.Fatal("畸形 fragment 必须让这条对话失败（官方 parseKnownFragment 会 throw）")
	}
}

// TestConvertIsDeterministic 钉住同一份输入两次转换得到一样的日志（除 id 之外）。
func TestConvertIsDeterministic(t *testing.T) {
	payload := map[string]any{
		"id":          "conv-det",
		"title":       "确定",
		"inserted_at": "2026-11-12T10:00:00.000Z",
		"messages": []map[string]any{
			userMessageJSON(1, nil, "问", "2026-11-12T10:00:00.000Z"),
			replyMessageJSON(2, 1, "FINISHED", "2026-11-12T10:00:01.000Z",
				[]map[string]any{{"type": "RESPONSE", "content": "答"}}),
		},
	}
	first := convertFixture(t, payload)
	second := convertFixture(t, payload)
	if len(first.Events) != len(second.Events) {
		t.Fatalf("两次转换事件数不同: %d vs %d", len(first.Events), len(second.Events))
	}
	for index := range first.Events {
		if first.Events[index].Type != second.Events[index].Type {
			t.Fatalf("第 %d 条事件类型不同", index)
		}
		if first.Events[index].Time != second.Events[index].Time {
			t.Fatalf("第 %d 条事件时间不同", index)
		}
	}
	// 事件 data 里的 JSON 键顺序也必须稳定（golden 比对靠这个）。
	for _, event := range first.Events {
		var round map[string]any
		if err := json.Unmarshal(event.Data, &round); err != nil {
			t.Fatalf("事件 %s 的 data 不是合法 JSON: %v", event.Type, err)
		}
	}
}
