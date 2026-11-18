package chatimport

// 导入编排的断言（官方 tests/importer.host.spec.ts 的对应物）：
// 索引选择顺序、失败替补、工作区中途消失、整体失败一个会话都不写。

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"dshgo/codecs/v4"
)

// fakePersistence 记录每次写盘，供断言"写了几个、写了什么"。
type fakePersistence struct {
	headers []ImportedHeader
	events  [][]v4.Event
	failAt  int // >0 时，第 failAt 次写盘返回错误
}

func (f *fakePersistence) persist(_ context.Context, header ImportedHeader, events []v4.Event) error {
	if f.failAt > 0 && len(f.headers)+1 == f.failAt {
		return errors.New("磁盘满了")
	}
	f.headers = append(f.headers, header)
	f.events = append(f.events, events)
	return nil
}

// importFixture 造一份三对话导出（updated_at 递减：c1 最新、c3 最旧）。
func importFixture(t *testing.T, extra ...zipEntry) []byte {
	t.Helper()
	message := []map[string]any{
		userMessageJSON(1, nil, "问", "2026-11-12T10:00:00.000Z"),
		replyMessageJSON(2, 1, "FINISHED", "2026-11-12T10:00:01.000Z",
			[]map[string]any{{"type": "RESPONSE", "content": "答"}}),
	}
	entries := []zipEntry{
		manifestEntry(t, 3),
		indexEntry(t,
			[2]string{"c1", "2026-11-12T12:00:00.000Z"},
			[2]string{"c2", "2026-11-12T11:00:00.000Z"},
			[2]string{"c3", "2026-11-12T10:00:00.000Z"},
		),
		conversationEntryZip(t, "c1", "一", message),
		conversationEntryZip(t, "c2", "二", message),
		conversationEntryZip(t, "c3", "三", message),
	}
	entries = append(entries, extra...)
	return buildZip(t, entries)
}

func runImport(t *testing.T, archive []byte, limits Limits, workspace Workspace, deps Dependencies, progress *[]string) (Result, error) {
	t.Helper()
	if deps.NewID == nil {
		deps.NewID = fixedIDs()
	}
	if deps.Warn == nil {
		deps.Warn = func(string) {}
	}
	return Import(context.Background(), deps, Request{
		Archive:   archive,
		Workspace: workspace,
		Limits:    limits,
		OnProgress: func(done, total int) {
			if progress != nil {
				*progress = append(*progress, itoa(done)+"/"+itoa(total))
			}
		},
	})
}

func twoWorkspace() Workspace {
	return Workspace{
		ID:            "ws-1",
		Path:          "D:/fake/workspace",
		AttachSession: func(context.Context, string) error { return nil },
	}
}

// TestImportSelectsNewestAndRespectsLimit 钉住"按 updated_at 取最新 N 个"
// （官方 importer.ts:174-176、422-439）。
func TestImportSelectsNewestAndRespectsLimit(t *testing.T) {
	store := &fakePersistence{}
	result, err := runImport(t, importFixture(t),
		Limits{MaxSessions: 2, MaxEntryBytes: DefaultMaxEntryBytes, MaxTitleBytes: DefaultMaxTitleBytes},
		twoWorkspace(),
		Dependencies{Persist: store.persist, WorkspaceRegistered: func(string) bool { return true }},
		nil)
	if err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	if len(store.headers) != 2 {
		t.Fatalf("应写 2 个会话，实际 %d", len(store.headers))
	}
	if result.ConversationCount != 3 {
		t.Fatalf("索引对话数=%d，应为 3", result.ConversationCount)
	}
	if result.OmittedByLimit != 1 {
		t.Fatalf("omittedByLimit=%d，应为 1（c3 因条数上限没被尝试）", result.OmittedByLimit)
	}
	if len(result.SessionIDs) != 2 {
		t.Fatalf("sessionIds 应有 2 个，实际 %v", result.SessionIDs)
	}
	// 必须是**最新**的两个（c1「一」、c2「二」），不是任意两个：索引排序写反了
	// 会选到最旧的两个，而"写了 2 个"这个数量断言抓不住。
	titles := make([]string, 0, len(store.events))
	for _, events := range store.events {
		title := ""
		for _, event := range events {
			if event.Type != "session/title" {
				continue
			}
			var data struct {
				Title string `json:"title"`
			}
			if err := json.Unmarshal(event.Data, &data); err != nil {
				t.Fatalf("session/title 解析失败: %v", err)
			}
			title = data.Title
		}
		titles = append(titles, title)
	}
	if titles[0] != "一" || titles[1] != "二" {
		t.Fatalf("必须按 updated_at 取最新的两个（一、二），实际 %v", titles)
	}
	// 每个 header 的 cwd 必须是工作区路径，createdAt 必须等于第一条事件的时间。
	for index, header := range store.headers {
		if header.Cwd != "D:/fake/workspace" {
			t.Fatalf("第 %d 个会话的 cwd=%q，应为工作区路径", index, header.Cwd)
		}
		if header.CreatedAt != store.events[index][0].Time {
			t.Fatalf("第 %d 个会话的 createdAt=%d，应为第一条事件时间 %d",
				index, header.CreatedAt, store.events[index][0].Time)
		}
		if len(store.events[index]) == 0 {
			t.Fatalf("第 %d 个会话没有事件", index)
		}
	}
}

// TestImportReplacesFailedConversationWithOlder 钉住"失败的那条由更旧的顶上"
// （官方 importer.ts:268-273、290-311）。
func TestImportReplacesFailedConversationWithOlder(t *testing.T) {
	// c1 的对话文件损坏（JSON 不是对象）→ 应被跳过，c3 顶上。
	entries := []zipEntry{
		manifestEntry(t, 3),
		indexEntry(t,
			[2]string{"c1", "2026-11-12T12:00:00.000Z"},
			[2]string{"c2", "2026-11-12T11:00:00.000Z"},
			[2]string{"c3", "2026-11-12T10:00:00.000Z"},
		),
		{name: "chat_sessions/c1.json", data: []byte(`"不是对象"`)},
		conversationEntryZip(t, "c2", "二", []map[string]any{
			userMessageJSON(1, nil, "问", "2026-11-12T10:00:00.000Z"),
			replyMessageJSON(2, 1, "FINISHED", "2026-11-12T10:00:01.000Z",
				[]map[string]any{{"type": "RESPONSE", "content": "答"}}),
		}),
		conversationEntryZip(t, "c3", "三", []map[string]any{
			userMessageJSON(1, nil, "问", "2026-11-12T10:00:00.000Z"),
			replyMessageJSON(2, 1, "FINISHED", "2026-11-12T10:00:01.000Z",
				[]map[string]any{{"type": "RESPONSE", "content": "答"}}),
		}),
	}
	store := &fakePersistence{}
	var warnings []string
	result, err := runImport(t, buildZip(t, entries),
		Limits{MaxSessions: 2, MaxEntryBytes: DefaultMaxEntryBytes, MaxTitleBytes: DefaultMaxTitleBytes},
		twoWorkspace(),
		Dependencies{
			Persist:             store.persist,
			WorkspaceRegistered: func(string) bool { return true },
			Warn:                func(message string) { warnings = append(warnings, message) },
		}, nil)
	if err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	if len(store.headers) != 2 {
		t.Fatalf("失败替补后应写 2 个会话，实际 %d", len(store.headers))
	}
	if len(result.Skipped) != 1 || result.Skipped[0].ConversationID != "c1" {
		t.Fatalf("c1 必须出现在 skipped 里，实际 %+v", result.Skipped)
	}
	if result.OmittedByLimit != 0 {
		t.Fatalf("c3 顶上之后 omittedByLimit=%d，应为 0", result.OmittedByLimit)
	}
	if len(warnings) == 0 {
		t.Fatal("被跳过的对话必须留下 warn 证据（官方 deps.warn）")
	}
}

// TestImportReportsConversationMissingFromArchive 钉住"索引有、归档没有"
// （官方 importer.ts:261-267）。
func TestImportReportsConversationMissingFromArchive(t *testing.T) {
	entries := []zipEntry{
		manifestEntry(t, 3),
		indexEntry(t, [2]string{"c1", "2026-11-12T12:00:00.000Z"}),
	}
	store := &fakePersistence{}
	result, err := runImport(t, buildZip(t, entries),
		Limits{MaxSessions: 5, MaxEntryBytes: DefaultMaxEntryBytes, MaxTitleBytes: DefaultMaxTitleBytes},
		twoWorkspace(),
		Dependencies{Persist: store.persist, WorkspaceRegistered: func(string) bool { return true }}, nil)
	if err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	if len(store.headers) != 0 {
		t.Fatalf("归档里没有对话文件就不该写会话，实际写了 %d", len(store.headers))
	}
	if len(result.Skipped) != 1 || result.Skipped[0].ConversationID != "c1" {
		t.Fatalf("必须报告缺失的对话，实际 %+v", result.Skipped)
	}
}

// TestImportStopsWhenWorkspaceDisappears 钉住"工作区中途消失就停下并保留已写的"
// （官方 importer.ts:187-192、220）。
func TestImportStopsWhenWorkspaceDisappears(t *testing.T) {
	store := &fakePersistence{}
	written := 0
	result, err := runImport(t, importFixture(t),
		Limits{MaxSessions: 3, MaxEntryBytes: DefaultMaxEntryBytes, MaxTitleBytes: DefaultMaxTitleBytes},
		twoWorkspace(),
		Dependencies{
			Persist: store.persist,
			// 第一次检查和第一次写之后工作区就"消失"了。
			WorkspaceRegistered: func(string) bool { return written == 0 },
			NewID: func() string {
				written++
				return "id-" + itoa(written)
			},
		}, nil)
	if err == nil {
		t.Fatalf("工作区消失必须让整次导入失败（已写的会话保留），拿到 result=%+v", result)
	}
	var sourceError *SourceError
	if !errors.As(err, &sourceError) || sourceError.Reason != ReasonWorkspaceNotFound {
		t.Fatalf("原因必须是 workspace-not-found，拿到 %v", err)
	}
}

// TestImportFailsBeforeWritingOnMetadataDefects 钉住"元数据阶段失败一个会话都不写"
// （官方 README「Failures detected before the streaming pass write nothing」）。
func TestImportFailsBeforeWritingOnMetadataDefects(t *testing.T) {
	conversation := conversationEntryZip(t, "c1", nil, []map[string]any{
		userMessageJSON(1, nil, "问", "2026-11-12T10:00:00.000Z"),
		replyMessageJSON(2, 1, "FINISHED", "2026-11-12T10:00:01.000Z",
			[]map[string]any{{"type": "RESPONSE", "content": "答"}}),
	})
	cases := []struct {
		name   string
		zip    []byte
		reason FailureReason
	}{
		{
			name: "没有 manifest",
			zip: buildZip(t, []zipEntry{
				indexEntry(t, [2]string{"c1", "2026-11-12T12:00:00.000Z"}),
				conversation,
			}),
			reason: ReasonMissingManifest,
		},
		{
			name: "版本不对",
			zip: buildZip(t, []zipEntry{
				manifestEntry(t, 2),
				indexEntry(t, [2]string{"c1", "2026-11-12T12:00:00.000Z"}),
				conversation,
			}),
			reason: ReasonUnsupportedVersion,
		},
		{
			name: "没有索引",
			zip: buildZip(t, []zipEntry{
				manifestEntry(t, 3),
				conversation,
			}),
			reason: ReasonMissingIndex,
		},
		{
			name: "索引不是数组",
			zip: buildZip(t, []zipEntry{
				manifestEntry(t, 3),
				{name: IndexEntry, data: []byte(`{"rows":[]}`)},
				conversation,
			}),
			reason: ReasonMissingIndex,
		},
		{
			name: "索引里没有对话",
			zip: buildZip(t, []zipEntry{
				manifestEntry(t, 3),
				indexEntry(t),
				conversation,
			}),
			reason: ReasonNoConversations,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			store := &fakePersistence{}
			_, err := runImport(t, testCase.zip,
				Limits{MaxSessions: 10, MaxEntryBytes: DefaultMaxEntryBytes, MaxTitleBytes: DefaultMaxTitleBytes},
				twoWorkspace(),
				Dependencies{Persist: store.persist, WorkspaceRegistered: func(string) bool { return true }}, nil)
			if err == nil {
				t.Fatal("必须整体失败")
			}
			var sourceError *SourceError
			if !errors.As(err, &sourceError) {
				t.Fatalf("失败必须是 *SourceError，拿到 %T %v", err, err)
			}
			if sourceError.Reason != testCase.reason {
				t.Fatalf("原因码=%s，应为 %s（%s）", sourceError.Reason, testCase.reason, sourceError.Message)
			}
			if len(store.headers) != 0 {
				t.Fatalf("元数据阶段失败时一个会话都不许写，实际 %d", len(store.headers))
			}
		})
	}
}

// TestImportReportsProgress 钉住进度回调的形状（官方 importer.ts:176、215）。
func TestImportReportsProgress(t *testing.T) {
	var progress []string
	store := &fakePersistence{}
	if _, err := runImport(t, importFixture(t),
		Limits{MaxSessions: 2, MaxEntryBytes: DefaultMaxEntryBytes, MaxTitleBytes: DefaultMaxTitleBytes},
		twoWorkspace(),
		Dependencies{Persist: store.persist, WorkspaceRegistered: func(string) bool { return true }},
		&progress); err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	if len(progress) != 3 {
		t.Fatalf("进度回调应有 3 次（选完后 1 次 + 每写一个 1 次），实际 %v", progress)
	}
	if progress[0] != "0/2" || progress[1] != "1/2" || progress[2] != "2/2" {
		t.Fatalf("进度序列不对: %v", progress)
	}
}

// TestImportReportsErrorsForDetail 钉住"细节错误进 skipped 且不影响别的对话"。
func TestImportReportsErrorsForDetail(t *testing.T) {
	entries := []zipEntry{
		manifestEntry(t, 3),
		indexEntry(t,
			[2]string{"c1", "2026-11-12T12:00:00.000Z"},
			[2]string{"c2", "2026-11-12T11:00:00.000Z"},
		),
		// 全部问题都被撤回 → 该对话失败。
		conversationEntryZip(t, "c1", "撤回", []map[string]any{
			userMessageJSON(1, nil, "问", "2026-11-12T10:00:00.000Z"),
			replyMessageJSON(2, 1, "CONTENT_FILTER", "2026-11-12T10:00:01.000Z",
				[]map[string]any{{"type": "RESPONSE", "content": "x"}}),
		}),
		conversationEntryZip(t, "c2", "正常", []map[string]any{
			userMessageJSON(1, nil, "问", "2026-11-12T10:00:00.000Z"),
			replyMessageJSON(2, 1, "FINISHED", "2026-11-12T10:00:01.000Z",
				[]map[string]any{{"type": "RESPONSE", "content": "答"}}),
		}),
	}
	store := &fakePersistence{}
	result, err := runImport(t, buildZip(t, entries),
		Limits{MaxSessions: 2, MaxEntryBytes: DefaultMaxEntryBytes, MaxTitleBytes: DefaultMaxTitleBytes},
		twoWorkspace(),
		Dependencies{Persist: store.persist, WorkspaceRegistered: func(string) bool { return true }}, nil)
	if err != nil {
		t.Fatalf("导入不该整体失败: %v", err)
	}
	if len(store.headers) != 1 {
		t.Fatalf("只应写入正常的那条，实际 %d", len(store.headers))
	}
	if len(result.Skipped) != 1 {
		t.Fatalf("被撤回的对话必须在 skipped 里，实际 %+v", result.Skipped)
	}
	if result.Skipped[0].Title == nil || *result.Skipped[0].Title != "撤回" {
		t.Fatalf("skipped 项应带解析出来的标题，拿到 %v", result.Skipped[0].Title)
	}
}
