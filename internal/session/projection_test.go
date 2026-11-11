package session

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"dshgo/codecs/v4"
)

func testEvent(t *testing.T, typ string, seq int64, data any) v4.Event {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("事件 %s 序列化失败: %v", typ, err)
	}
	return v4.Event{Type: typ, Seq: seq, Time: seq * 1000, Data: raw}
}

func userPrompt(t *testing.T, seq int64, kind string) v4.Event {
	t.Helper()
	return testEvent(t, "user/message", seq, map[string]any{
		"content":   []map[string]any{{"type": "text", "text": "hi"}},
		"source":    map[string]any{"kind": kind},
		"role":      "user",
		"surfaceOp": "append",
	})
}

func metadataValue(t *testing.T, snapshot ProjectionSnapshot) map[string]any {
	t.Helper()
	value, ok := snapshot.Values[ProjectionKeySessionListMetadata].(map[string]any)
	if !ok {
		t.Fatalf("sessionListMetadata = %#v", snapshot.Values[ProjectionKeySessionListMetadata])
	}
	return value
}

func asInt64(t *testing.T, value any) int64 {
	t.Helper()
	switch typed := value.(type) {
	case nil:
		return -1
	case int64:
		return typed
	case int:
		return int64(typed)
	case float64:
		return int64(typed)
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			t.Fatalf("不是整数: %v", typed)
		}
		return parsed
	default:
		t.Fatalf("不是数字: %#v", value)
		return 0
	}
}

func TestProjectionRegistryRejectsDuplicateAndEmpty(t *testing.T) {
	_, err := NewProjectionRegistry(
		ProjectionUnit{Key: "a", StateVersion: 1},
		ProjectionUnit{Key: "a", StateVersion: 1},
	)
	if err == nil {
		t.Fatal("重复 key 必须拒绝")
	}
	if _, err := NewProjectionRegistry(ProjectionUnit{Key: "", StateVersion: 1}); err == nil {
		t.Fatal("空 key 必须拒绝")
	}
	if _, err := NewProjectionRegistry(ProjectionUnit{Key: "a", StateVersion: -1}); err == nil {
		t.Fatal("负 stateVersion 必须拒绝")
	}
	if _, err := NewProjectionRegistry(ProjectionUnit{Key: "a", StateVersion: 1}); err == nil {
		t.Fatal("没有 schema 的 unit 必须拒绝（否则 checkpoint 读回来没人校验）")
	}
}

// TestProjectionFoldMatchesTransportSemantics 钉住与 Transport 那台折叠
// 完全相同的三个键语义（sessionListMetadata / title / workingDirectory）。
func TestProjectionFoldMatchesTransportSemantics(t *testing.T) {
	registry := DefaultProjectionRegistry()
	cwd := t.TempDir()
	sub := filepath.Join(cwd, "sub")
	header := v4.Header{Version: 4, Cwd: cwd}
	events := []v4.Event{
		testEvent(t, "turn/start", 0, map[string]any{"turn": 1}),
		userPrompt(t, 1, "user"),
		testEvent(t, "session/title", 2, map[string]any{"title": "真实标题"}),
		testEvent(t, EventWorkingDirectoryChange, 3, map[string]any{"cwd": sub}),
		userPrompt(t, 4, "tool"),
	}
	snapshot, rows, err := registry.Restore(nil, events, 0, header, 0)
	if err != nil {
		t.Fatalf("Restore 失败: %v", err)
	}
	if snapshot.AsOfSeq != 4 {
		t.Fatalf("asOfSeq = %d，期望 4", snapshot.AsOfSeq)
	}
	metadata := metadataValue(t, snapshot)
	if metadata["blank"] != false {
		t.Fatalf("blank = %#v（turn/start 之后必须为 false）", metadata["blank"])
	}
	if got := asInt64(t, metadata["lastPromptAt"]); got != 1000 {
		t.Fatalf("lastPromptAt = %d，期望 1000（只有 source.kind=user 才算人类 prompt）", got)
	}
	if snapshot.Values[ProjectionKeyTitle] != "真实标题" {
		t.Fatalf("title = %#v", snapshot.Values[ProjectionKeyTitle])
	}
	if snapshot.Values[ProjectionKeyWorkingDirectory] != sub {
		t.Fatalf("workingDirectory = %#v，期望 %q", snapshot.Values[ProjectionKeyWorkingDirectory], sub)
	}
	// 每个 unit 都刷新到同一个切点。
	for _, key := range []string{ProjectionKeySessionListMetadata, ProjectionKeyTitle, ProjectionKeyWorkingDirectory} {
		row, ok := rows[key]
		if !ok {
			t.Fatalf("rows 缺 %q", key)
		}
		if row.Seq != 4 || row.Ver != 1 {
			t.Fatalf("%s row = %+v，期望 {ver:1 seq:4}", key, row)
		}
	}
}

func TestProjectionFoldDoesNotInventMissingKeys(t *testing.T) {
	registry := DefaultProjectionRegistry()
	cwd := t.TempDir()
	header := v4.Header{Version: 4, Cwd: cwd}
	snapshot, _, err := registry.Restore(nil, nil, 0, header, 0)
	if err != nil {
		t.Fatalf("空日志 Restore 失败: %v", err)
	}
	if snapshot.AsOfSeq != -1 {
		t.Fatalf("空日志 asOfSeq = %d，期望 -1（baseSeq-1）", snapshot.AsOfSeq)
	}
	metadata := metadataValue(t, snapshot)
	if metadata["blank"] != true || metadata["lastPromptAt"] != nil {
		t.Fatalf("空日志 metadata = %#v", metadata)
	}
	if _, ok := snapshot.Values[ProjectionKeyTitle]; ok {
		t.Fatal("没有 session/title 事件时 title 不许出现（不伪造）")
	}
	// workingDirectory 的 init 是 header.cwd：这是官方契约里的兜底真值，不是伪造。
	if snapshot.Values[ProjectionKeyWorkingDirectory] != cwd {
		t.Fatalf("workingDirectory 兜底 = %#v，期望 %q", snapshot.Values[ProjectionKeyWorkingDirectory], cwd)
	}
}

// TestProjectionFoldResumesFromCheckpoint 是"冷 fold 续跑"的核心断言：
// 从 checkpoint 起只 fold 尾部事件，结果必须与全量 fold 逐字段相同。
func TestProjectionFoldResumesFromCheckpoint(t *testing.T) {
	registry := DefaultProjectionRegistry()
	cwd := t.TempDir()
	targetA := filepath.Join(cwd, "a")
	targetB := filepath.Join(cwd, "b")
	header := v4.Header{Version: 4, Cwd: cwd}
	events := []v4.Event{
		testEvent(t, "turn/start", 0, map[string]any{"turn": 1}),
		userPrompt(t, 1, "user"),
		testEvent(t, "session/title", 2, map[string]any{"title": "第一版"}),
		testEvent(t, EventWorkingDirectoryChange, 3, map[string]any{"cwd": targetA}),
		testEvent(t, "session/title", 4, map[string]any{"title": "第二版"}),
		testEvent(t, EventWorkingDirectoryChange, 5, map[string]any{"cwd": targetB}),
	}
	full, fullRows, err := registry.Restore(nil, events, 0, header, 0)
	if err != nil {
		t.Fatal(err)
	}

	// 模拟"上进程存下的 checkpoint"：只 fold 到 seq 2。
	prefix, prefixRows, err := registry.Restore(nil, events[:3], 0, header, 0)
	if err != nil {
		t.Fatal(err)
	}
	if prefix.AsOfSeq != 2 {
		t.Fatalf("prefix asOfSeq = %d", prefix.AsOfSeq)
	}

	resumed, resumedRows, err := registry.Restore(prefixRows, events[3:], 3, header, 0)
	if err != nil {
		t.Fatalf("续跑失败: %v", err)
	}
	if resumed.AsOfSeq != 5 {
		t.Fatalf("续跑 asOfSeq = %d，期望 5", resumed.AsOfSeq)
	}
	if got, want := resumed.Values[ProjectionKeyTitle], full.Values[ProjectionKeyTitle]; got != want {
		t.Fatalf("续跑 title = %#v，全量 = %#v", got, want)
	}
	if got, want := resumed.Values[ProjectionKeyWorkingDirectory], full.Values[ProjectionKeyWorkingDirectory]; got != want {
		t.Fatalf("续跑 cwd = %#v，全量 = %#v", got, want)
	}
	// 尾部没有新 turn/start，但 metadata 的 blank 必须从 checkpoint 继承。
	if metadataValue(t, resumed)["blank"] != false {
		t.Fatal("续跑丢了 blank 状态")
	}
	for _, key := range []string{ProjectionKeySessionListMetadata, ProjectionKeyTitle, ProjectionKeyWorkingDirectory} {
		if resumedRows[key].Seq != fullRows[key].Seq {
			t.Fatalf("%s 续跑 seq = %d，全量 = %d", key, resumedRows[key].Seq, fullRows[key].Seq)
		}
	}
}

func TestProjectionRestoreRejectsTailReadBelowCheckpoint(t *testing.T) {
	registry := DefaultProjectionRegistry()
	header := v4.Header{Version: 4, Cwd: t.TempDir()}
	events := []v4.Event{
		testEvent(t, "turn/start", 0, map[string]any{"turn": 1}),
		userPrompt(t, 1, "user"),
		testEvent(t, "session/title", 2, map[string]any{"title": "x"}),
	}
	_, rows, err := registry.Restore(nil, events, 0, header, 0)
	if err != nil {
		t.Fatal(err)
	}
	// checkpoint 声称折叠过 seq 2，但尾部读从 seq 3 开始且没有事件：
	// 日志末端（2）在 watermark（2）之下…… 这里仍然接得上；
	// 真正接不上的是"行声称的事件在窗口末端之后"（日志被截短）。
	shrunken := map[string]ProjectionRow{}
	for key, row := range rows {
		row.Seq = 9 // 声称折叠到 seq 9
		shrunken[key] = row
	}
	if _, _, err := registry.Restore(shrunken, nil, 3, header, 0); err == nil {
		t.Fatal("checkpoint 声称的事件超出日志末端时必须报错（要求从 0 重读）")
	} else if !errors.Is(err, ErrProjectionCheckpoint) {
		t.Fatalf("错误必须可分类，得到 %v", err)
	}
}

func TestProjectionRestoreDetectsSeqGap(t *testing.T) {
	registry := DefaultProjectionRegistry()
	header := v4.Header{Version: 4, Cwd: t.TempDir()}
	events := []v4.Event{
		testEvent(t, "turn/start", 0, map[string]any{"turn": 1}),
		testEvent(t, "turn/start", 2, map[string]any{"turn": 2}), // 缺 seq 1
	}
	if _, _, err := registry.Restore(nil, events, 0, header, 0); err == nil {
		t.Fatal("事件序列有洞时必须报错（不许在一个断了的日志上 fold）")
	} else if !errors.Is(err, ErrProjectionCheckpoint) {
		t.Fatalf("错误必须可分类，得到 %v", err)
	}
}

func TestProjectionViewCheckpointGuard(t *testing.T) {
	registry := DefaultProjectionRegistry()
	cwd := t.TempDir()
	rows := map[string]ProjectionRow{
		ProjectionKeyTitle: {Ver: 1, Seq: 9, Val: "cached"},
		// ver 不匹配必须整行丢弃（cache 语义：只可能过期，不可能错）。
		ProjectionKeyWorkingDirectory: {Ver: 99, Seq: 9, Val: cwd},
		ProjectionKeySessionListMetadata: {Ver: 1, Seq: 9, Val: map[string]any{
			"blank": false, "lastPromptAt": nil,
		}},
	}
	values := registry.View(rows, nil)
	if values[ProjectionKeyTitle] != "cached" {
		t.Fatalf("title = %#v", values[ProjectionKeyTitle])
	}
	if _, ok := values[ProjectionKeyWorkingDirectory]; ok {
		t.Fatal("ver 不匹配的行必须当作不存在")
	}
	// 只取指定键。
	only := registry.View(rows, []string{ProjectionKeyTitle})
	if len(only) != 1 || only[ProjectionKeyTitle] != "cached" {
		t.Fatalf("按 keys 过滤 = %#v", only)
	}
	// 坏 val 也要丢弃而不是 panic。
	broken := map[string]ProjectionRow{ProjectionKeyTitle: {Ver: 1, Seq: 1, Val: 42}}
	if _, ok := registry.View(broken, nil)[ProjectionKeyTitle]; ok {
		t.Fatal("schema 不合法的行必须丢弃")
	}
}

func TestProjectionRestoreFloor(t *testing.T) {
	registry := DefaultProjectionRegistry()
	if floor := registry.RestoreFloor(nil); floor != 0 {
		t.Fatalf("空 checkpoint floor = %d，期望 0（全量重读）", floor)
	}
	cwd := t.TempDir()
	rows := map[string]ProjectionRow{
		ProjectionKeyTitle:               {Ver: 1, Seq: 7, Val: "x"},
		ProjectionKeySessionListMetadata: {Ver: 1, Seq: 2, Val: map[string]any{"blank": true, "lastPromptAt": nil}},
		ProjectionKeyWorkingDirectory:    {Ver: 1, Seq: 2, Val: cwd},
		// rc.2 的新单元也必须有可用行，否则 floor 被它拖到 0（全量重读）。
		ProjectionKeyConversationContinuation: {Ver: 4, Seq: 2, Val: map[string]any{
			"admitted": false, "failedAttempt": false, "ending": nil,
		}},
	}
	// 最低可用 watermark = 2 → 尾部读从 2 开始（那一格是锚点：Restore 会
	// 把它当已折叠事件跳过，但它证明日志还延伸到这里）。
	if floor := registry.RestoreFloor(rows); floor != 2 {
		t.Fatalf("floor = %d，期望 2", floor)
	}
	// 任一 unit 没有可用行 → floor 归 0（那个键必须全量重读）。
	delete(rows, ProjectionKeyWorkingDirectory)
	if floor := registry.RestoreFloor(rows); floor != 0 {
		t.Fatalf("缺行的 floor = %d，期望 0", floor)
	}
}

func TestProjectionIdentityMatches(t *testing.T) {
	header := v4.Header{Version: 4, ID: "s", CreatedAt: 1000, Cwd: "/p", IsSeeded: true}
	identity := IdentityOfHeader(header, 3)
	if identity.FormatVersion != 4 || identity.CreatedAt != 1000 || identity.Cwd != "/p" || !identity.IsSeeded || identity.InheritedEventCount != 3 {
		t.Fatalf("identity = %+v", identity)
	}
	same := identity
	if !identity.Matches(same) || !identity.LifecycleMatches(same) {
		t.Fatal("同一份 identity 必须互相匹配")
	}

	// inheritedEventCount 只影响完整匹配（fold 起点），不影响 lifecycle 匹配（列表只读）。
	other := identity
	other.InheritedEventCount = 99
	if identity.Matches(other) {
		t.Fatal("完整匹配必须比较 inheritedEventCount")
	}
	if !identity.LifecycleMatches(other) {
		t.Fatal("lifecycle 匹配不看 inheritedEventCount")
	}

	// 任何一个不可变 header 字段不同 = 另一段生命周期，必须拒绝。
	for _, mutate := range []func(*ProjectionIdentity){
		func(i *ProjectionIdentity) { i.CreatedAt++ },
		func(i *ProjectionIdentity) { i.Cwd = "/other" },
		func(i *ProjectionIdentity) { i.IsSeeded = false },
		func(i *ProjectionIdentity) { i.FormatVersion = 3 },
	} {
		candidate := identity
		mutate(&candidate)
		if identity.Matches(candidate) || identity.LifecycleMatches(candidate) {
			t.Fatalf("identity %+v 不该匹配 %+v", identity, candidate)
		}
	}

	// 缺失 formatVersion 的记录不可用：没有格式代次就证明不了 fold 语义。
	absent := identity
	absent.FormatVersion = 0
	if identity.Matches(absent) {
		t.Fatal("缺失 formatVersion 必须拒绝")
	}
}

func TestProjectionCheckpointClonesValues(t *testing.T) {
	registry := DefaultProjectionRegistry()
	state := map[string]any{"blank": false, "lastPromptAt": int64(5)}
	rows := registry.Checkpoint(map[string]ProjectionState{
		ProjectionKeySessionListMetadata: {Value: state, Seq: 5},
	})
	state["blank"] = true // 改动调用方状态
	stored := rows[ProjectionKeySessionListMetadata].Val.(map[string]any)
	if stored["blank"] != false {
		t.Fatal("checkpoint 必须存 detached 克隆，不能持有调用方可变引用")
	}
	if _, ok := rows[ProjectionKeyTitle]; ok {
		t.Fatal("没有 cell 的 unit 不该产生行")
	}
}

func TestProjectionRestoreRejectsMalformedCheckpoint(t *testing.T) {
	registry := DefaultProjectionRegistry()
	header := v4.Header{Version: 4, Cwd: t.TempDir()}
	rows := map[string]ProjectionRow{
		ProjectionKeyTitle: {Ver: 1, Seq: 0, Val: 42}, // 不合法 state
	}
	events := []v4.Event{testEvent(t, "turn/start", 0, map[string]any{"turn": 1})}
	if _, _, err := registry.Restore(rows, events, 0, header, 0); err == nil {
		t.Fatal("坏 checkpoint 必须报错（宁可全量重读，不许拿坏值当现状）")
	} else if !errors.Is(err, ErrProjectionCheckpoint) {
		t.Fatalf("错误必须可分类，得到 %v", err)
	}
}

func TestProjectionUnitsAreOfficialShapes(t *testing.T) {
	registry := DefaultProjectionRegistry()
	keys := registry.Keys()
	// 默认注册表 = 宿主真有事件来源的键。rc.2 起多一个 conversationContinuation
	// （官方 stateVersion 4，事件来源 turn/start、user/message、assistant/attempt、turn/end
	// 全是宿主真会写的事件）：dsh-api-session-controller/lib/index.js:479-513。
	want := []string{
		ProjectionKeySessionListMetadata,
		ProjectionKeyTitle,
		ProjectionKeyWorkingDirectory,
		ProjectionKeyConversationContinuation,
	}
	if len(keys) != len(want) {
		t.Fatalf("units = %v，期望 %v", keys, want)
	}
	for index, key := range want {
		if keys[index] != key {
			t.Fatalf("units[%d] = %q，期望 %q", index, keys[index], key)
		}
	}
	// 官方 working-directory 事件名与 data 形状是外部契约，钉死。
	if EventWorkingDirectoryChange != "working-directory/change" {
		t.Fatalf("cwd 事件名 = %q", EventWorkingDirectoryChange)
	}
}

// TestProjectionTrackerReportsChangedKeys 钉住增量折叠的变更集：
// observeProjections 每帧只报"这个事件真的改了哪些键"，没改就空数组。
func TestProjectionTrackerReportsChangedKeys(t *testing.T) {
	registry := DefaultProjectionRegistry()
	cwd := t.TempDir()
	target := filepath.Join(cwd, "next")
	header := v4.Header{Version: 4, Cwd: cwd}

	tracker := NewProjectionTracker(registry, nil, -1, header, 0)
	if changes := tracker.Apply(testEvent(t, "turn/start", 0, map[string]any{"turn": 1})); len(changes) != 1 || changes[0].Key != ProjectionKeySessionListMetadata {
		t.Fatalf("turn/start 的变更 = %#v", changes)
	}
	if changes := tracker.Apply(userPrompt(t, 1, "user")); len(changes) != 1 || changes[0].Key != ProjectionKeySessionListMetadata {
		t.Fatalf("人类 prompt 的变更 = %#v", changes)
	}
	if changes := tracker.Apply(testEvent(t, "session/title", 2, map[string]any{"title": "标题"})); len(changes) != 1 || changes[0].Key != ProjectionKeyTitle || changes[0].Val != "标题" {
		t.Fatalf("标题的变更 = %#v", changes)
	}
	if changes := tracker.Apply(testEvent(t, EventWorkingDirectoryChange, 3, map[string]any{"cwd": target})); len(changes) != 1 || changes[0].Key != ProjectionKeyWorkingDirectory || changes[0].Val != target {
		t.Fatalf("cwd 的变更 = %#v", changes)
	}
	// 与投影无关的事件不改任何键。
	if changes := tracker.Apply(userPrompt(t, 4, "tool")); len(changes) != 0 {
		t.Fatalf("无关事件的变更 = %#v，期望空数组", changes)
	}

	snapshot := tracker.Snapshot()
	if snapshot.AsOfSeq != 4 {
		t.Fatalf("asOfSeq = %d", snapshot.AsOfSeq)
	}
	if snapshot.Values[ProjectionKeyTitle] != "标题" || snapshot.Values[ProjectionKeyWorkingDirectory] != target {
		t.Fatalf("snapshot = %#v", snapshot.Values)
	}
	rows := tracker.Checkpoint()
	for _, key := range []string{ProjectionKeySessionListMetadata, ProjectionKeyTitle, ProjectionKeyWorkingDirectory} {
		if rows[key].Seq != 4 {
			t.Fatalf("%s row = %+v", key, rows[key])
		}
	}

	// 用 checkpoint 行续跑：已折叠的前缀必须跳过，不重复发变更。
	resumed := NewProjectionTracker(registry, rows, 4, header, 0)
	if changes := resumed.Apply(userPrompt(t, 2, "tool")); len(changes) != 0 {
		t.Fatalf("已折叠前缀不该再发变更: %#v", changes)
	}
	if changes := resumed.Apply(testEvent(t, "session/title", 5, map[string]any{"title": "新标题"})); len(changes) != 1 || changes[0].Val != "新标题" {
		t.Fatalf("续跑变更 = %#v", changes)
	}
}
