package goal

// 域层测试：文档编解码、状态机、旧格式迁移、跨进程锁。
//
// 这些用例只碰 `internal/goal` 自己的契约——宿主 store 与工具 store 的
// 一致性由 `cmd/dshd` / `modules/tool/goal` 的真链探针钉住。

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dshgo/internal/transport"
)

func testID(prefix string) func() (string, error) {
	next := 0
	return func() (string, error) {
		next++
		return prefix + "-" + string(rune('a'+next-1)), nil
	}
}

func docPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "state", "session-goal.json")
}

// 文档必须能原样往返：goal/change 帧与 `goals.get` 读的都是这份形状。
func TestDocumentRoundTrip(t *testing.T) {
	path := docPath(t)
	objective := "把 goal 单真值做完"
	rounds := 2
	doc := Document{Version: Version, Goals: map[string]Record{
		"session-a": {
			Goal: &transport.GoalSnapshot{
				ID: "goal-1", Revision: 3, Objective: objective, Phase: "active", MaxGoalRounds: 8,
			},
			RoundsStarted: &rounds,
		},
	}}
	if err := Save(path, doc); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	record, ok := loaded.Goals["session-a"]
	if !ok || record.Goal == nil {
		t.Fatalf("往返丢了记录: %+v", loaded)
	}
	if !Equal(doc.Goals["session-a"], record) {
		t.Fatalf("往返不等: %+v → %+v", doc.Goals["session-a"], record)
	}
	// 落盘是 tmp + rename：不许留下 .tmp。
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".tmp" {
			t.Fatalf("残留临时文件: %s", entry.Name())
		}
	}
}

// 坏文件必须 fail-loud：版本/顶层 goals/记录字段任一处不对都不能当空文档。
func TestDecodeRejectsBrokenDocuments(t *testing.T) {
	cases := map[string]string{
		"version 0":       `{"version":0,"goals":{}}`,
		"goals 缺失":        `{"version":1}`,
		"goals 为 null":    `{"version":1,"goals":null}`,
		"记录缺 objective":   `{"version":1,"goals":{"s":{"goal":{"id":"g","revision":1,"phase":"active","maxGoalRounds":0}}}}`,
		"revision 为 0":    `{"version":1,"goals":{"s":{"goal":{"id":"g","revision":0,"objective":"x","phase":"active","maxGoalRounds":0}}}}`,
		"非 blocked 带理由":   `{"version":1,"goals":{"s":{"goal":{"id":"g","revision":1,"objective":"x","phase":"active","maxGoalRounds":0,"blockedReason":{"code":"c","message":"m"}}}}}`,
		"blocked 缺理由":     `{"version":1,"goals":{"s":{"goal":{"id":"g","revision":1,"objective":"x","phase":"blocked","maxGoalRounds":0}}}}`,
		"phase 非法":        `{"version":1,"goals":{"s":{"goal":{"id":"g","revision":1,"objective":"x","phase":"running","maxGoalRounds":0}}}}`,
		"roundsStarted 负": `{"version":1,"goals":{"s":{"goal":{"id":"g","revision":1,"objective":"x","phase":"active","maxGoalRounds":0},"roundsStarted":-1}}}`,
	}
	for name, raw := range cases {
		if _, err := Decode([]byte(raw)); err == nil {
			t.Fatalf("%s 应当被拒: %s", name, raw)
		}
	}
}

// 状态机全程：create → edit → pause → resume → block → complete → clear。
func TestApplyLifecycle(t *testing.T) {
	newID := testID("goal")
	records := map[string]Record{}
	objective := "生命周期"
	maxRounds := 4

	next, created, err := Apply(records, "s", Intent{Kind: "create", Objective: &objective, MaxRounds: &maxRounds}, newID)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Goal.Phase != "active" || created.Goal.Revision != 1 || created.RoundsStarted == nil || *created.RoundsStarted != 0 {
		t.Fatalf("create 记录不对: %+v", created.Goal)
	}

	edited := "改过的目标"
	next, created, err = Apply(next, "s", Intent{Kind: "edit", Objective: &edited}, newID)
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if created.Goal.Objective != edited || created.Goal.Revision != 2 {
		t.Fatalf("edit 记录不对: %+v", created.Goal)
	}

	next, created, err = Apply(next, "s", Intent{Kind: "pause"}, newID)
	if err != nil || created.Goal.Phase != "paused" || created.Goal.Revision != 3 {
		t.Fatalf("pause: %+v err=%v", created.Goal, err)
	}
	next, created, err = Apply(next, "s", Intent{Kind: "resume"}, newID)
	if err != nil || created.Goal.Phase != "active" || created.Goal.Revision != 4 {
		t.Fatalf("resume: %+v err=%v", created.Goal, err)
	}
	reason := &transport.GoalBlockedReason{Code: "ci", Message: "等 CI"}
	next, created, err = Apply(next, "s", Intent{Kind: "block", Reason: reason}, newID)
	if err != nil || created.Goal.Phase != "blocked" || created.Goal.BlockedReason == nil || created.Goal.BlockedReason.Code != "ci" {
		t.Fatalf("block: %+v err=%v", created.Goal, err)
	}
	next, created, err = Apply(next, "s", Intent{Kind: "complete"}, newID)
	if err != nil || created.Goal.Phase != "complete" || created.Goal.BlockedReason != nil {
		t.Fatalf("complete: %+v err=%v", created.Goal, err)
	}
	next, cleared, err := Apply(next, "s", Intent{Kind: "clear"}, newID)
	if err != nil || cleared.Goal != nil {
		t.Fatalf("clear: %+v err=%v", cleared, err)
	}
	if _, ok := next["s"]; ok {
		t.Fatal("clear 之后记录还在")
	}
}

// 非法迁移必须被拒，且不改变输入文档（纯函数）。
func TestApplyRejectsIllegalTransitions(t *testing.T) {
	newID := testID("goal")
	records := map[string]Record{}
	objective := "x"
	records, _, err := Apply(records, "s", Intent{Kind: "create", Objective: &objective}, newID)
	if err != nil {
		t.Fatal(err)
	}
	before := CloneMap(records)

	for name, tc := range map[string]struct {
		session string
		intent  Intent
	}{
		"create 已有未完成目标":     {"s", Intent{Kind: "create", Objective: &objective}},
		"create 空 objective": {"s", Intent{Kind: "create"}},
		"resume active":      {"s", Intent{Kind: "resume"}},
		"clear 不存在":          {"missing", Intent{Kind: "clear"}},
		"未知操作":               {"s", Intent{Kind: "dance"}},
	} {
		if _, _, err := Apply(records, tc.session, tc.intent, newID); !errors.Is(err, ErrTransition) {
			t.Fatalf("%s 应当 ErrTransition，实际 %v", name, err)
		}
	}
	if !EqualMap(records, before) {
		t.Fatalf("非法迁移改了输入文档: %+v → %+v", before, records)
	}

	// CAS：拿旧 revision 来改必须被拒。
	expect := &Ref{ID: records["s"].Goal.ID, Revision: 99}
	if _, _, err := Apply(records, "s", Intent{Kind: "edit", Expect: expect, Objective: &objective}, newID); !errors.Is(err, ErrTransition) {
		t.Fatalf("过期 revision 应当 ErrTransition，实际 %v", err)
	}
}

// 预算用尽的 resume 必须被拒（官方 "increase maxGoalRounds before resuming"）。
func TestApplyResumeRejectsExhaustedBudget(t *testing.T) {
	newID := testID("goal")
	objective := "预算"
	maxRounds := 2
	records, record, err := Apply(map[string]Record{}, "s", Intent{Kind: "create", Objective: &objective, MaxRounds: &maxRounds}, newID)
	if err != nil {
		t.Fatal(err)
	}
	record.Goal.Phase = "paused"
	used := 2
	record.RoundsStarted = &used
	records["s"] = record
	if _, _, err := Apply(records, "s", Intent{Kind: "resume"}, newID); !errors.Is(err, ErrTransition) {
		t.Fatalf("预算用尽 resume 应当 ErrTransition，实际 %v", err)
	}
}

// 旧扁平格式（map[sessionID]objective）迁移：id 可重放，roundsStarted 保持未知。
func TestLoadOrMigrateFlatMap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state", "session-goal.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"session-a":"旧 A","session-b":"旧 B"}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, migrated, err := LoadOrMigrate(path, testID("goal"))
	if err != nil {
		t.Fatalf("LoadOrMigrate: %v", err)
	}
	if !migrated {
		t.Fatal("旧扁平格式必须报告迁移")
	}
	if doc.Version != Version || len(doc.Goals) != 2 {
		t.Fatalf("迁移文档不对: %+v", doc)
	}
	if doc.Goals["session-a"].Goal.ID != "goal-a" || doc.Goals["session-b"].Goal.ID != "goal-b" {
		t.Fatalf("id 分配顺序不稳定: %+v", doc.Goals)
	}
	if doc.Goals["session-a"].RoundsStarted != nil {
		t.Fatal("旧扁平格式没有 roundsStarted：迁移不许伪造 0")
	}
	// 落盘之后重放是幂等的。
	if err := Save(path, doc); err != nil {
		t.Fatal(err)
	}
	again, migrated, err := LoadOrMigrate(path, testID("goal"))
	if err != nil || migrated {
		t.Fatalf("重放不该再迁移: migrated=%v err=%v", migrated, err)
	}
	if again.Goals["session-a"].Goal.ID != "goal-a" {
		t.Fatalf("重放换了 id: %+v", again.Goals)
	}
	// 顶层 null 不是"空文档"：那是坏文件。
	nullPath := filepath.Join(t.TempDir(), "state", "session-goal.json")
	if err := os.MkdirAll(filepath.Dir(nullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nullPath, []byte("null"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadOrMigrate(nullPath, testID("goal")); err == nil {
		t.Fatal("顶层 null 必须报错")
	}
}

// 旧按会话拆分目录：导入 + 归档；同一个 goal 幂等；不同 goal 冲突不覆盖。
func TestMigrateLegacyPerSessionDir(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	legacyDir := filepath.Join(stateDir, "goals")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) string {
		t.Helper()
		path := filepath.Join(legacyDir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("session-a.json", `{"id":"goal-a","objective":"旧 A","phase":"paused","rounds_started":3,"max_goal_rounds":5,"revision":2}`)

	doc := Empty()
	migrated, imported, err := MigrateLegacy(stateDir, doc, testID("goal"))
	if err != nil || imported != 1 {
		t.Fatalf("import: imported=%d err=%v", imported, err)
	}
	record := migrated.Goals["session-a"]
	if record.Goal == nil || record.Goal.ID != "goal-a" || record.Goal.Phase != "paused" ||
		record.Goal.MaxGoalRounds != 5 || record.Goal.Revision != 2 || Rounds(record) != 3 {
		t.Fatalf("迁移字段丢了: %+v", record.Goal)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "goals.migrated", "session-a.json")); err != nil {
		t.Fatalf("旧文件没归档: %v", err)
	}
	// 重放（旧文件已归档）：无操作、不重复导入。
	again, imported, err := MigrateLegacy(stateDir, migrated, testID("goal"))
	if err != nil || imported != 0 || !EqualMap(again.Goals, migrated.Goals) {
		t.Fatalf("重放不是幂等: imported=%d err=%v", imported, err)
	}

	// 冲突：规范文档里是另一个 goal → 两份都留、明确报错。
	write("session-a.json", `{"id":"goal-other","objective":"另一个","phase":"active","rounds_started":0,"max_goal_rounds":1,"revision":1}`)
	before := CloneMap(migrated.Goals)
	if _, _, err := MigrateLegacy(stateDir, migrated, testID("goal")); err == nil {
		t.Fatal("冲突必须 fail-loud")
	}
	if _, err := os.Stat(filepath.Join(legacyDir, "session-a.json")); err != nil {
		t.Fatalf("冲突时动了旧文件: %v", err)
	}
	if !EqualMap(migrated.Goals, before) {
		t.Fatalf("冲突时改了规范文档: %+v → %+v", before, migrated.Goals)
	}
}

// 跨进程锁：同一时刻只有一个持有者；崩溃遗留的锁文件不会挡住后来者。
func TestLockSerializesAndSurvivesCrash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state", "session-goal.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	unlock, err := Lock(path)
	if err != nil {
		t.Fatalf("取锁: %v", err)
	}

	acquired := make(chan struct{})
	go func() {
		unlock2, err := Lock(path)
		if err != nil {
			t.Errorf("第二个持有者取锁失败: %v", err)
			close(acquired)
			return
		}
		unlock2()
		close(acquired)
	}()
	select {
	case <-acquired:
		t.Fatal("锁没有串行化：第二个持有者立刻拿到了")
	case <-time.After(30 * time.Millisecond):
	}
	unlock()
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("释放后第二个持有者没拿到锁")
	}
	// 解锁幂等：第二次调用不能 panic、也不能影响后来者。
	unlock()

	// 崩溃遗留：进程被杀时文件句柄随进程消失，锁**自动释放**——
	// 后来者必须立刻拿到（历史上锁目录实现会卡满 10s 再报超时）。
	leftover := path + ".lock"
	if _, err := os.Stat(leftover); err != nil {
		t.Fatalf("锁文件应当留在盘上（由内核释放锁，不靠删除）: %v", err)
	}
	done := make(chan struct{})
	go func() {
		unlock, err := Lock(path)
		if err != nil {
			t.Errorf("崩溃遗留的锁文件挡住了后来者: %v", err)
			close(done)
			return
		}
		unlock()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("崩溃遗留的锁文件挡住了后来者")
	}
}

// DeriveOperation：外部写入（serve 子进程）被父进程观察到时，靠它补帧。
func TestDeriveOperation(t *testing.T) {
	active := Record{Goal: &transport.GoalSnapshot{ID: "g1", Revision: 1, Objective: "x", Phase: "active"}}
	paused := Record{Goal: &transport.GoalSnapshot{ID: "g1", Revision: 2, Objective: "x", Phase: "paused"}}
	rounds := 1
	advanced := Record{Goal: &transport.GoalSnapshot{ID: "g1", Revision: 2, Objective: "x", Phase: "active"}, RoundsStarted: &rounds}
	replaced := Record{Goal: &transport.GoalSnapshot{ID: "g2", Revision: 1, Objective: "y", Phase: "active"}}

	cases := []struct {
		name    string
		prev    Record
		existed bool
		next    Record
		want    string
	}{
		{"首次见到 = create", Record{}, false, active, "create"},
		{"换 id = create", active, true, replaced, "create"},
		{"phase paused = pause", active, true, paused, "pause"},
		{"phase active = resume", paused, true, active, "resume"},
		{"轮次前进 = round", active, true, advanced, "round"},
		{"没有目标 = clear", active, true, Record{}, "clear"},
	}
	for _, tc := range cases {
		if got := DeriveOperation(tc.prev, tc.existed, tc.next); got != tc.want {
			t.Fatalf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

// Encode/Decode 与 json 标签必须与 transport 的形状一致（官方 goal/change）。
func TestDocumentJSONShape(t *testing.T) {
	doc := Empty()
	raw, err := Encode(doc)
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	if generic["version"].(float64) != 1 {
		t.Fatalf("version 字段不对: %s", raw)
	}
	if _, ok := generic["goals"].(map[string]any); !ok {
		t.Fatalf("goals 必须是对象: %s", raw)
	}
}

func EqualMap(a, b map[string]Record) bool {
	if len(a) != len(b) {
		return false
	}
	for key, record := range a {
		other, ok := b[key]
		if !ok || !Equal(record, other) {
			return false
		}
	}
	return true
}
