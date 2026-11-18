package main

// 官方 rc.5 `dsh-deepseek-chat-import` 的**端到端**探针：
// 一份合成导出 ZIP → 真实 v4 会话文件 → 挂进真实工作区 registry → 能被
// `session.ListSessions` 认出来、能按官方历史形状读回。
//
// 为什么这条必须在 `cmd/dshd` 而不是 `internal/chatimport`：引擎只产事件，
// "事件写得进 v4、写出来的会话读得回来、工作区认它"这三件事只有在这里才同时可见。

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"dshgo/codecs/v4"
	"dshgo/internal/chatimport"
	"dshgo/internal/session"
	"dshgo/internal/transport"
)

// chatImportZipEntry 是合成导出的一个条目。
type chatImportZipEntry struct {
	name string
	data []byte
}

// buildChatImportZip 把条目写成 ZIP（全部 deflate，与真实导出一致）。
func buildChatImportZip(t *testing.T, entries []chatImportZipEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, entry := range entries {
		handle, err := writer.Create(entry.name)
		if err != nil {
			t.Fatalf("造归档条目 %s 失败: %v", entry.name, err)
		}
		if _, err := handle.Write(entry.data); err != nil {
			t.Fatalf("写归档条目 %s 失败: %v", entry.name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭合成归档失败: %v", err)
	}
	return buffer.Bytes()
}

func chatImportJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("夹具序列化失败: %v", err)
	}
	return raw
}

// chatImportFixture 造一份两对话的导出（c-old 更旧，c-new 最新）。
func chatImportFixture(t *testing.T) []byte {
	t.Helper()
	conversation := func(id, title, at string) chatImportZipEntry {
		return chatImportZipEntry{name: "chat_sessions/" + id + ".json", data: chatImportJSON(t, map[string]any{
			"id":                 id,
			"title":              title,
			"current_message_id": 2,
			"inserted_at":        at,
			"messages": []map[string]any{
				{
					"id": 1, "parent_id": nil, "role": "USER", "status": "FINISHED", "inserted_at": at,
					"fragments": []map[string]any{{"type": "REQUEST", "content": "第一问"}},
				},
				{
					"id": 2, "parent_id": 1, "role": "ASSISTANT", "status": "FINISHED", "inserted_at": at,
					"fragments": []map[string]any{
						{"type": "THINK", "content": "想", "elapsed_secs": 1},
						{"type": "RESPONSE", "content": "第一答"},
					},
				},
			},
		})}
	}
	return buildChatImportZip(t, []chatImportZipEntry{
		{name: chatimport.ManifestEntry, data: chatImportJSON(t, map[string]any{"export_format_version": 3})},
		{name: chatimport.IndexEntry, data: chatImportJSON(t, []map[string]any{
			{"id": "c-new", "inserted_at": "2026-11-12T12:00:00.000Z", "updated_at": "2026-11-12T12:00:00.000Z"},
			{"id": "c-old", "inserted_at": "2026-11-12T10:00:00.000Z", "updated_at": "2026-11-12T10:00:00.000Z"},
		})},
		conversation("c-new", "新的那个", "2026-11-12T12:00:00.000Z"),
		conversation("c-old", "旧的那个", "2026-11-12T10:00:00.000Z"),
	})
}

// TestChatImportEndToEndWritesResumableSessions 是这条能力的**产品级**证据：
// 导入 → 真实 v4 文件 → 可被列表/工作区认出 → 事件形状符合官方历史。
func TestChatImportEndToEndWritesResumableSessions(t *testing.T) {
	home := t.TempDir()
	workspaceDir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		t.Fatalf("建工作区目录失败: %v", err)
	}
	workspaceHost := newOfficialWorkspaceHost(home, nil, nil)
	created, err := workspaceHost.createWorkspace(workspaceDir)
	if err != nil {
		t.Fatalf("建工作区失败: %v", err)
	}
	host := newChatImportHost(home, nil, func() *officialWorkspaceHost { return workspaceHost })

	workspace, ok := host.api().ResolveWorkspace(created.Workspace.WorkspaceID)
	if !ok {
		t.Fatal("刚建的工作区必须能按 id 解析出来")
	}
	if workspace.Path == "" {
		t.Fatal("解析出来的工作区必须带真实路径")
	}

	var progress []int
	result, err := host.api().Import(context.Background(), transport.ChatImportRequest{
		Archive:   chatImportFixture(t),
		Workspace: workspace,
		OnProgress: func(done, total int) {
			progress = append(progress, done)
		},
	})
	if err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	if len(result.SessionIDs) != 2 {
		t.Fatalf("应导入 2 个会话，实际 %v", result.SessionIDs)
	}
	if result.ConversationCount != 2 || result.OmittedByLimit != 0 {
		t.Fatalf("计数不对: %+v", result)
	}
	if result.WorkspaceID != created.Workspace.WorkspaceID {
		t.Fatalf("结果里的工作区 id 不对: %s", result.WorkspaceID)
	}
	if len(progress) == 0 || progress[0] != 0 {
		t.Fatalf("进度必须从 0/total 开始，实际 %v", progress)
	}

	// 1) 真实 v4 文件必须存在，且 header 形状与官方导入一致。
	for index, sessionID := range result.SessionIDs {
		path := filepath.Join(home, "sessions", v4.ProjectKey(workspace.Path), sessionID, "session.v4.jsonl.zstd")
		log, readErr := v4.ReadFile(path)
		if readErr != nil {
			t.Fatalf("第 %d 个导入会话读不回来（%s）: %v", index, path, readErr)
		}
		if log.Header.Version != 4 || log.Header.IsSeeded {
			t.Fatalf("header 不对: %+v", log.Header)
		}
		if log.Header.Cwd != workspace.Path {
			t.Fatalf("header.cwd=%q，应为工作区路径 %q", log.Header.Cwd, workspace.Path)
		}
		if log.Header.AgentPreset != "" {
			t.Fatalf("导入会话不该带 agentPreset，拿到 %q", log.Header.AgentPreset)
		}
		if len(log.Events) == 0 {
			t.Fatalf("导入会话没有任何事件")
		}
		// createdAt 必须等于第一条事件的时间（官方 importer.ts:562）。
		if log.Header.CreatedAt != log.Events[0].Time {
			t.Fatalf("header.createdAt=%d，应为第一条事件时间 %d", log.Header.CreatedAt, log.Events[0].Time)
		}
		// 事件 seq 连续，时间不递减。
		for position, event := range log.Events {
			if event.Seq != int64(position) {
				t.Fatalf("第 %d 条事件 seq=%d，必须连续", position, event.Seq)
			}
			if position > 0 && event.Time < log.Events[position-1].Time {
				t.Fatalf("第 %d 条事件时间倒退", position)
			}
			// 每条事件都必须是已知类型（写进去读不回来的记录不许出现）。
			if !session.IsKnownEventType(event.Type) {
				t.Fatalf("导入了未知事件类型 %q（v4 词汇表里没有）", event.Type)
			}
			if err := session.ValidateKnownEventPayload(event.Type, event.Data); err != nil {
				t.Fatalf("导入的事件 %s 负载不合法: %v", event.Type, err)
			}
		}
		if log.Events[0].Type != "turn/start" {
			t.Fatalf("首条事件应是 turn/start，拿到 %s", log.Events[0].Type)
		}
		// 导入路径**不写** session/end-seed（官方 README 明示，resume 时补）。
		for _, event := range log.Events {
			if event.Type == "session/end-seed" {
				t.Fatal("导入路径不许写 session/end-seed")
			}
		}
		// 标题以 session/title 落盘，来源是 user（钉住，不跑标题生成）。
		titleSeen := false
		for _, event := range log.Events {
			if event.Type != "session/title" {
				continue
			}
			var data struct {
				Title  string `json:"title"`
				Source struct {
					Kind string `json:"kind"`
				} `json:"source"`
			}
			if err := json.Unmarshal(event.Data, &data); err != nil {
				t.Fatalf("session/title 解析失败: %v", err)
			}
			if data.Title == "" || data.Source.Kind != "user" {
				t.Fatalf("标题事件不对: %s", event.Data)
			}
			titleSeen = true
		}
		if !titleSeen {
			t.Fatal("导入的会话必须带 session/title（官方钉住原标题）")
		}
	}

	// 2) 会话列表（界面侧栏读的就是它）必须认出这两个会话与它们的标题。
	listings, listErr := session.ListSessions(home, workspace.Path)
	if listErr != nil {
		t.Fatalf("列会话失败: %v", listErr)
	}
	byID := make(map[string]session.Listing, len(listings))
	for _, listing := range listings {
		byID[listing.SessionID] = listing
	}
	for _, sessionID := range result.SessionIDs {
		listing, found := byID[sessionID]
		if !found {
			t.Fatalf("导入的会话 %s 没出现在会话列表里", sessionID)
		}
		if listing.Title == nil || *listing.Title == "" {
			t.Fatalf("导入的会话 %s 没有标题（session/title 没被投影出来）", sessionID)
		}
		if !listing.Readable {
			t.Fatalf("导入的会话 %s 读不动: %s", sessionID, listing.Error)
		}
	}

	// 3) 工作区必须把它算成自己的会话（attachSession 的语义：顶到手动顺序最前）。
	registry, registryErr := workspaceHost.registry()
	if registryErr != nil {
		t.Fatalf("读 registry 失败: %v", registryErr)
	}
	var workspaceView *transport.OfficialWorkspace
	for index := range registry.Workspaces {
		if registry.Workspaces[index].WorkspaceID == created.Workspace.WorkspaceID {
			workspaceView = &registry.Workspaces[index]
			break
		}
	}
	if workspaceView == nil {
		t.Fatal("registry 里找不到刚建的工作区")
	}
	attached := make(map[string]bool, len(workspaceView.SessionIDs))
	for _, id := range workspaceView.SessionIDs {
		attached[id] = true
	}
	for _, sessionID := range result.SessionIDs {
		if !attached[sessionID] {
			t.Fatalf("导入的会话 %s 没被挂进工作区 %s", sessionID, created.Workspace.WorkspaceID)
		}
	}
}

// TestChatImportEndToEndReusesSameArchiveTwice 钉住官方明示的已知行为：
// 同一份导出导入两次会写出第二套会话（不做去重）。
func TestChatImportEndToEndReusesSameArchiveTwice(t *testing.T) {
	home := t.TempDir()
	workspaceDir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		t.Fatalf("建工作区目录失败: %v", err)
	}
	workspaceHost := newOfficialWorkspaceHost(home, nil, nil)
	created, err := workspaceHost.createWorkspace(workspaceDir)
	if err != nil {
		t.Fatalf("建工作区失败: %v", err)
	}
	host := newChatImportHost(home, nil, func() *officialWorkspaceHost { return workspaceHost })
	workspace, ok := host.api().ResolveWorkspace(created.Workspace.WorkspaceID)
	if !ok {
		t.Fatal("工作区解析失败")
	}
	archive := chatImportFixture(t)
	first, err := host.api().Import(context.Background(), transport.ChatImportRequest{Archive: archive, Workspace: workspace})
	if err != nil {
		t.Fatalf("第一次导入失败: %v", err)
	}
	second, err := host.api().Import(context.Background(), transport.ChatImportRequest{Archive: archive, Workspace: workspace})
	if err != nil {
		t.Fatalf("第二次导入失败: %v", err)
	}
	if len(first.SessionIDs) != 2 || len(second.SessionIDs) != 2 {
		t.Fatalf("两次都应导入 2 个: %v / %v", first.SessionIDs, second.SessionIDs)
	}
	seen := make(map[string]bool, 4)
	for _, id := range append(append([]string{}, first.SessionIDs...), second.SessionIDs...) {
		if seen[id] {
			t.Fatalf("重复导入必须写出新会话，id %s 重复了", id)
		}
		seen[id] = true
	}
}

// TestChatImportEndToEndReportsWorkspaceGone 钉住"工作区在导入过程中消失"
// （官方 importer.ts:187-192）。
func TestChatImportEndToEndReportsWorkspaceGone(t *testing.T) {
	home := t.TempDir()
	workspaceDir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		t.Fatalf("建工作区目录失败: %v", err)
	}
	workspaceHost := newOfficialWorkspaceHost(home, nil, nil)
	created, err := workspaceHost.createWorkspace(workspaceDir)
	if err != nil {
		t.Fatalf("建工作区失败: %v", err)
	}
	host := newChatImportHost(home, nil, func() *officialWorkspaceHost { return workspaceHost })
	workspace, ok := host.api().ResolveWorkspace(created.Workspace.WorkspaceID)
	if !ok {
		t.Fatal("工作区解析失败")
	}
	// 解析之后把目录删掉：导入必须在动任何会话之前失败。
	if err := os.RemoveAll(workspace.Path); err != nil {
		t.Fatalf("删工作区目录失败: %v", err)
	}
	_, err = host.api().Import(context.Background(), transport.ChatImportRequest{
		Archive:   chatImportFixture(t),
		Workspace: workspace,
	})
	if err == nil {
		t.Fatal("工作区目录消失必须让导入失败")
	}
	var typed *transport.ChatImportError
	if !errors.As(err, &typed) {
		t.Fatalf("失败必须带原因码，拿到 %T %v", err, err)
	}
	if typed.Reason != transport.ChatImportWorkspaceNotFound {
		t.Fatalf("原因码=%s，应为 workspace-not-found", typed.Reason)
	}
	// 一个会话都不许写。
	listings, listErr := session.ListSessions(home, "")
	if listErr != nil && !os.IsNotExist(listErr) {
		t.Fatalf("列会话失败: %v", listErr)
	}
	if len(listings) != 0 {
		t.Fatalf("失败时不许写会话，实际写了 %d 个", len(listings))
	}
}

// TestChatImportEndToEndRejectsBadArchive 钉住 README 的失败表里那几条能被
// 端到端复现的（版本不对 / 不是 ZIP）。
func TestChatImportEndToEndRejectsBadArchive(t *testing.T) {
	home := t.TempDir()
	workspaceDir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		t.Fatalf("建工作区目录失败: %v", err)
	}
	workspaceHost := newOfficialWorkspaceHost(home, nil, nil)
	created, err := workspaceHost.createWorkspace(workspaceDir)
	if err != nil {
		t.Fatalf("建工作区失败: %v", err)
	}
	host := newChatImportHost(home, nil, func() *officialWorkspaceHost { return workspaceHost })
	workspace, ok := host.api().ResolveWorkspace(created.Workspace.WorkspaceID)
	if !ok {
		t.Fatal("工作区解析失败")
	}

	cases := []struct {
		name    string
		archive []byte
		reason  transport.ChatImportFailureReason
	}{
		{
			name:    "不是 ZIP",
			archive: []byte("肯定不是 ZIP"),
			reason:  transport.ChatImportUnreadableArchive,
		},
		{
			name: "版本不对",
			archive: buildChatImportZip(t, []chatImportZipEntry{
				{name: chatimport.ManifestEntry, data: chatImportJSON(t, map[string]any{"export_format_version": 9})},
			}),
			reason: transport.ChatImportUnsupportedVersion,
		},
		{
			name: "缺 manifest",
			archive: buildChatImportZip(t, []chatImportZipEntry{
				{name: chatimport.IndexEntry, data: chatImportJSON(t, []map[string]any{})},
			}),
			reason: transport.ChatImportMissingManifest,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, importErr := host.api().Import(context.Background(), transport.ChatImportRequest{
				Archive:   testCase.archive,
				Workspace: workspace,
			})
			var typed *transport.ChatImportError
			if !errors.As(importErr, &typed) {
				t.Fatalf("必须带原因码，拿到 %T %v", importErr, importErr)
			}
			if typed.Reason != testCase.reason {
				t.Fatalf("原因码=%s，应为 %s（%s）", typed.Reason, testCase.reason, typed.Message)
			}
		})
	}
	if listings, listErr := session.ListSessions(home, workspace.Path); listErr == nil && len(listings) != 0 {
		t.Fatalf("失败的导入不许留下会话，实际 %d 个", len(listings))
	}
}

// TestChatImportLimitsAreOfficialDefaults 钉住 limits 的默认值（官方
// src/index.ts:57-66 的 Config 默认）。
func TestChatImportLimitsAreOfficialDefaults(t *testing.T) {
	home := t.TempDir()
	host := newChatImportHost(home, nil, nil)
	limits := host.api().Limits()
	if limits.MaxUploadBytes != 512*1024*1024 {
		t.Fatalf("maxUploadBytes=%d，官方默认是 536870912", limits.MaxUploadBytes)
	}
	if host.maxEntryBytes != 256*1024*1024 {
		t.Fatalf("maxEntryBytes=%d，官方默认是 268435456", host.maxEntryBytes)
	}
	if host.maxSessions != 1000 || host.maxTitleBytes != 80 {
		t.Fatalf("maxSessions/maxTitleBytes=%d/%d，官方默认是 1000/80", host.maxSessions, host.maxTitleBytes)
	}
}
