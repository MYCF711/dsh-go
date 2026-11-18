package chatimport

// 归档视图的断言（官方 tests/archive.host.spec.ts 的对应物）：
// 中央目录、同名条目、单条目上限、按名读取、按趟选择。

import (
	"context"
	"errors"
	"testing"
)

func TestOpenArchiveListsCentralDirectoryInOrder(t *testing.T) {
	archive, sourceErr := OpenArchive(buildZip(t, []zipEntry{
		manifestEntry(t, 3),
		indexEntry(t, [2]string{"c1", "2026-11-12T10:00:00.000Z"}),
		conversationEntryZip(t, "c1", nil, nil),
	}), DefaultMaxEntryBytes)
	if sourceErr != nil {
		t.Fatalf("打开归档失败: %v", sourceErr)
	}
	names := archive.Names()
	want := []string{ManifestEntry, IndexEntry, "chat_sessions/c1.json"}
	if len(names) != len(want) {
		t.Fatalf("条目数=%d，应为 %d（%v）", len(names), len(want), names)
	}
	for index := range want {
		if names[index] != want[index] {
			t.Fatalf("第 %d 个条目=%q，应为 %q（中央目录顺序必须保持）", index, names[index], want[index])
		}
	}
}

func TestOpenArchiveRejectsDuplicateNames(t *testing.T) {
	data := buildZip(t, []zipEntry{
		manifestEntry(t, 3),
		{name: "chat_sessions/c1.json", data: []byte(`{}`)},
		{name: "chat_sessions/c1.json", data: []byte(`{}`)},
	})
	_, sourceErr := OpenArchive(data, DefaultMaxEntryBytes)
	if sourceErr == nil {
		t.Fatal("同名条目必须让整份归档失败（read 按名选会绕开单条目上限）")
	}
	if sourceErr.Reason != ReasonUnreadableArchive {
		t.Fatalf("原因码=%s，应为 %s", sourceErr.Reason, ReasonUnreadableArchive)
	}
}

func TestOpenArchiveRejectsNonZip(t *testing.T) {
	_, sourceErr := OpenArchive([]byte("这不是 ZIP"), DefaultMaxEntryBytes)
	if sourceErr == nil {
		t.Fatal("不是 ZIP 必须失败")
	}
	if sourceErr.Reason != ReasonUnreadableArchive {
		t.Fatalf("原因码=%s，应为 %s", sourceErr.Reason, ReasonUnreadableArchive)
	}
}

func TestArchiveReadByNameAndMissing(t *testing.T) {
	data := []byte(`{"export_format_version":3}`)
	archive, sourceErr := OpenArchive(buildZip(t, []zipEntry{{name: ManifestEntry, data: data}}), DefaultMaxEntryBytes)
	if sourceErr != nil {
		t.Fatalf("打开归档失败: %v", sourceErr)
	}
	got, entryErr := archive.Read(context.Background(), ManifestEntry, DefaultMaxEntryBytes)
	if entryErr != nil {
		t.Fatalf("按名读取失败: %v", entryErr)
	}
	if string(got) != string(data) {
		t.Fatalf("读到的字节=%q，应为 %q", got, data)
	}
	missing, entryErr := archive.Read(context.Background(), "nope.json", DefaultMaxEntryBytes)
	if entryErr != nil {
		t.Fatalf("不存在的条目不该报错: %v", entryErr)
	}
	if missing != nil {
		t.Fatalf("不存在的条目必须返回 nil，拿到 %v", missing)
	}
}

func TestArchiveReadRejectsOversizedEntry(t *testing.T) {
	archive, sourceErr := OpenArchive(buildZip(t, []zipEntry{{name: ManifestEntry, data: make([]byte, 1024)}}), 64)
	if sourceErr != nil {
		t.Fatalf("打开归档失败: %v", sourceErr)
	}
	_, entryErr := archive.Read(context.Background(), ManifestEntry, 64)
	if entryErr == nil {
		t.Fatal("超过单条目上限的条目必须被拒绝")
	}
	if !entryErr.Oversized {
		t.Fatalf("必须标记为 oversized，拿到 %+v", entryErr)
	}
}

func TestArchiveStreamSelectsOnlyRequestedEntries(t *testing.T) {
	archive, sourceErr := OpenArchive(buildZip(t, []zipEntry{
		manifestEntry(t, 3),
		conversationEntryZip(t, "c1", nil, nil),
		conversationEntryZip(t, "c2", nil, nil),
	}), DefaultMaxEntryBytes)
	if sourceErr != nil {
		t.Fatalf("打开归档失败: %v", sourceErr)
	}
	visited := make([]string, 0, 1)
	rejected := make([]string, 0, 1)
	streamErr := archive.Stream(context.Background(), DefaultMaxEntryBytes, StreamHandler{
		Select: func(entry Entry) bool {
			// 只要 c2：manifest 与 c1 一个字节都不该解压。
			return entry.Name == "chat_sessions/c2.json"
		},
		Entry: func(_ context.Context, entry Entry, data []byte) error {
			visited = append(visited, entry.Name)
			if len(data) == 0 {
				t.Fatal("被选中的条目必须交出字节")
			}
			return nil
		},
		Reject: func(entry Entry, failure *EntryError) {
			rejected = append(rejected, entry.Name)
		},
	})
	if streamErr != nil {
		t.Fatalf("流式趟失败: %v", streamErr)
	}
	if len(visited) != 1 || visited[0] != "chat_sessions/c2.json" {
		t.Fatalf("只应访问 c2，实际 %v", visited)
	}
	if len(rejected) != 0 {
		t.Fatalf("不该有被拒条目: %v", rejected)
	}
}

func TestArchiveStreamRejectsOversizedAndContinues(t *testing.T) {
	big := make([]byte, 512)
	archive, sourceErr := OpenArchive(buildZip(t, []zipEntry{
		{name: "big.json", data: big},
		{name: "small.json", data: []byte("ok")},
	}), 64)
	if sourceErr != nil {
		t.Fatalf("打开归档失败: %v", sourceErr)
	}
	rejected := make(map[string]bool)
	visited := make([]string, 0, 1)
	streamErr := archive.Stream(context.Background(), 64, StreamHandler{
		Select: func(entry Entry) bool { return true },
		Entry: func(_ context.Context, entry Entry, _ []byte) error {
			visited = append(visited, entry.Name)
			return nil
		},
		Reject: func(entry Entry, failure *EntryError) {
			rejected[entry.Name] = failure.Oversized
		},
	})
	if streamErr != nil {
		t.Fatalf("流式趟失败: %v", streamErr)
	}
	if !rejected["big.json"] {
		t.Fatalf("超限条目必须以 oversized 被拒: %v", rejected)
	}
	if len(visited) != 1 || visited[0] != "small.json" {
		t.Fatalf("超限条目之后必须继续处理小条目，实际 %v", visited)
	}
}

func TestArchiveStreamStopsOnCancellation(t *testing.T) {
	archive, sourceErr := OpenArchive(buildZip(t, []zipEntry{
		conversationEntryZip(t, "c1", nil, nil),
		conversationEntryZip(t, "c2", nil, nil),
	}), DefaultMaxEntryBytes)
	if sourceErr != nil {
		t.Fatalf("打开归档失败: %v", sourceErr)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	streamErr := archive.Stream(ctx, DefaultMaxEntryBytes, StreamHandler{
		Select: func(Entry) bool { return true },
		Entry: func(_ context.Context, _ Entry, _ []byte) error {
			cancel()
			return nil
		},
	})
	if streamErr == nil {
		t.Fatal("取消后流式趟必须失败，而不是继续跑完")
	}
	if !errors.Is(streamErr, context.Canceled) {
		t.Fatalf("错误必须可 unwrap 到 context.Canceled，拿到 %v", streamErr)
	}
}

// TestEntrySizeErrorRules 钉住官方 `entrySizeError`（archive.ts:276-290）的三条判据。
func TestEntrySizeErrorRules(t *testing.T) {
	const limit int64 = 100
	cases := []struct {
		name      string
		entry     Entry
		wantNil   bool
		oversized bool
	}{
		{"都合规", Entry{Name: "a", Size: 10, OriginalSize: 10, Compression: 8}, true, false},
		{"声明超限", Entry{Name: "a", Size: 10, OriginalSize: 101, Compression: 8}, false, true},
		{"存储超限", Entry{Name: "a", Size: 101, OriginalSize: 10, Compression: 8}, false, true},
		// ZIP_STORED 必须自洽：声明与存储相等。
		{"stored 一致", Entry{Name: "a", Size: 10, OriginalSize: 10, Compression: 0}, true, false},
		{"stored 不一致", Entry{Name: "a", Size: 9, OriginalSize: 10, Compression: 0}, false, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			failure := entrySizeError(testCase.entry, limit)
			if testCase.wantNil {
				if failure != nil {
					t.Fatalf("应该通过，却报 %v", failure)
				}
				return
			}
			if failure == nil {
				t.Fatal("应该被拒绝")
			}
			if failure.Oversized != testCase.oversized {
				t.Fatalf("oversized=%v，应为 %v（%s）", failure.Oversized, testCase.oversized, failure.Message)
			}
		})
	}
}

// TestConversationIDPatterns 钉住官方 importer.ts:32-34 的两个条目名匹配式。
func TestConversationIDPatterns(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"chat_sessions/abc.json", "abc"},
		{"chat_sessions/abc_files/a.png", ""},
		{"chat_sessions/abc.json.bak", ""},
		{"manifest.json", ""},
		{"chat_sessions/x/y.json", ""},
	}
	for _, testCase := range cases {
		if got := conversationIDOf(testCase.name); got != testCase.want {
			t.Fatalf("conversationIDOf(%q)=%q，应为 %q", testCase.name, got, testCase.want)
		}
	}
}
