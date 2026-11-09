package goal

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	goaldomain "dshgo/internal/goal"
	"dshgo/internal/spi"
)

func ctxWith(id string) context.Context { return spi.WithSession(context.Background(), id) }

func call(t *testing.T, st *store, kind string, ctx context.Context, raw string) (spi.Result, error) {
	t.Helper()
	return (&Tool{st: st, kind: kind}).Exec(ctx, json.RawMessage(raw))
}

// testStore 按生产的接线形状建 store：规范文档是 `<home>/state/session-goal.json`。
func testStore(t *testing.T) (*store, string) {
	t.Helper()
	home := t.TempDir()
	return &store{path: filepath.Join(home, "state", "session-goal.json"), newID: newID}, home
}

// revision 是 CAS：拿旧值来改必须被拒，否则两个调用会互相覆盖。
func TestUpdateRejectsStaleRevision(t *testing.T) {
	st, _ := testStore(t)
	ctx := ctxWith("acp-a")
	if _, err := call(t, st, kindCreate, ctx, `{"objective":"把 demo 跑通"}`); err != nil {
		t.Fatal(err)
	}
	// 第一次：revision=1，成功
	if _, err := call(t, st, kindUpdate, ctx, `{"action":"pause","goal_id":"`+goalID(t, st, ctx)+`","revision":1}`); err != nil {
		t.Fatalf("第一次更新应当成功: %v", err)
	}
	// 第二次还用 1：应当被拒
	if _, err := call(t, st, kindUpdate, ctx, `{"action":"resume","goal_id":"`+goalID(t, st, ctx)+`","revision":1}`); err == nil {
		t.Fatal("旧 revision 必须被拒绝")
	}
}

// 一个会话只能有一个目标；重复 create 要给下一步而不是默默覆盖。
func TestCreateTwiceRejected(t *testing.T) {
	st, _ := testStore(t)
	ctx := ctxWith("acp-b")
	if _, err := call(t, st, kindCreate, ctx, `{"objective":"第一个"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, st, kindCreate, ctx, `{"objective":"第二个"}`); err == nil {
		t.Fatal("已有目标时再 create 必须被拒")
	}
}

// blocked 必须带理由，否则复盘时没人知道卡在哪。
func TestBlockedRequiresReason(t *testing.T) {
	st, _ := testStore(t)
	ctx := ctxWith("acp-c")
	call(t, st, kindCreate, ctx, `{"objective":"x"}`)
	id := goalID(t, st, ctx)
	if _, err := call(t, st, kindUpdate, ctx, `{"action":"blocked","goal_id":"`+id+`","revision":1}`); err == nil {
		t.Fatal("blocked 不带 reason 必须被拒")
	}
	if _, err := call(t, st, kindUpdate, ctx, `{"action":"blocked","goal_id":"`+id+`","revision":1,"blocked_reason":"等 CI"}`); err != nil {
		t.Fatalf("带 reason 应当成功: %v", err)
	}
}

// 没有会话上下文时必须拒绝（不然目标写到谁头上都不知道）。
func TestGoalWithoutSessionRejected(t *testing.T) {
	st, _ := testStore(t)
	if _, err := call(t, st, kindCreate, context.Background(), `{"objective":"x"}`); err == nil {
		t.Fatal("没有会话 id 必须拒绝")
	}
}

// 单真值：工具写的就是 serve 的 `state/session-goal.json`。
//
// 这是 HS-GOAL-TRUTH 的最小物证——过去这里写 `state/goals/<session>.json`，
// 于是驱动（只读 session-goal.json）永远看不到模型建的目标。
func TestCreateGoalWritesCanonicalDocumentOnly(t *testing.T) {
	st, home := testStore(t)
	ctx := ctxWith("acp-single-truth")
	if _, err := call(t, st, kindCreate, ctx, `{"objective":"单一真值","max_goal_rounds":7}`); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(home, "state", "session-goal.json"))
	if err != nil {
		t.Fatalf("规范文档没落盘: %v", err)
	}
	var doc goaldomain.Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("规范文档不是合法 JSON: %v\n%s", err, raw)
	}
	record, ok := doc.Goals["acp-single-truth"]
	if !ok || record.Goal == nil {
		t.Fatalf("规范文档里没有这个会话的目标: %s", raw)
	}
	if record.Goal.Objective != "单一真值" || record.Goal.MaxGoalRounds != 7 || record.Goal.Phase != "active" {
		t.Fatalf("目标字段不对: %+v", record.Goal)
	}
	if record.RoundsStarted == nil || *record.RoundsStarted != 0 {
		t.Fatalf("新建目标 roundsStarted 必须是真实的 0: %v", record.RoundsStarted)
	}

	// 旧按会话拆分目录必须**不再被写**。
	if _, err := os.Stat(filepath.Join(home, "state", "goals", "acp-single-truth.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("旧 state/goals 目录又被写了: %v", err)
	}
}

// 旧格式迁移：已有 `state/goals/<session>.json` 的用户目标必须被识别，且归档旧文件。
func TestLegacyPerSessionFilesMigrateAndArchive(t *testing.T) {
	st, home := testStore(t)
	legacyDir := filepath.Join(home, "state", "goals")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{
  "id": "goal-legacy01",
  "objective": "旧格式里的目标",
  "phase": "paused",
  "rounds_started": 2,
  "max_goal_rounds": 9,
  "revision": 3,
  "blocked_reason": ""
}`
	legacyPath := filepath.Join(legacyDir, "acp-legacy.json")
	if err := os.WriteFile(legacyPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := call(t, st, kindGet, ctxWith("acp-legacy"), `{}`)
	if err != nil {
		t.Fatalf("迁移后必须读得到旧目标: %v", err)
	}
	goal := goalOf(t, res)
	if goal.ID != "goal-legacy01" || goal.Objective != "旧格式里的目标" || goal.Phase != "paused" ||
		goal.Revision != 3 || goal.RoundsStarted != 2 || goal.MaxGoalRounds != 9 {
		t.Fatalf("迁移丢了字段: %+v", goal)
	}
	// 旧文件被归档（移动）而不是继续留在读路径上：清掉目标之后不能再复活。
	if _, err := os.Stat(legacyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("旧文件仍在读路径上: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "state", "goals.migrated", "acp-legacy.json")); err != nil {
		t.Fatalf("旧文件没有归档备份: %v", err)
	}
	// 幂等：再读一次仍然是同一个目标（不会因为迁移重放换 id）。
	again, err := call(t, st, kindGet, ctxWith("acp-legacy"), `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if id := goalOf(t, again).ID; id != "goal-legacy01" {
		t.Fatalf("迁移重放换了 id: %q", id)
	}
}

// 冲突不覆盖：规范文档与旧文件是两份不同的目标时，fail-loud，两份都保留。
func TestLegacyConflictKeepsBothFiles(t *testing.T) {
	st, home := testStore(t)
	ctx := ctxWith("acp-conflict")
	if _, err := call(t, st, kindCreate, ctx, `{"objective":"规范文档里的目标"}`); err != nil {
		t.Fatal(err)
	}
	canonical := filepath.Join(home, "state", "session-goal.json")
	before, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatal(err)
	}

	legacyDir := filepath.Join(home, "state", "goals")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(legacyDir, "acp-conflict.json")
	legacy := `{"id":"goal-other","objective":"旧文件里的另一个目标","phase":"active","rounds_started":0,"max_goal_rounds":5,"revision":1}`
	if err := os.WriteFile(legacyPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := call(t, st, kindGet, ctx, `{}`); err == nil {
		t.Fatal("两份不同目标必须 fail-loud，不能按 revision 猜赢")
	}
	after, err := os.ReadFile(canonical)
	if err != nil || string(after) != string(before) {
		t.Fatalf("冲突时改了规范文档: %q → %q err=%v", before, after, err)
	}
	if _, err := os.Stat(legacyPath); err != nil {
		t.Fatalf("冲突时动了旧文件: %v", err)
	}
}

func goalOf(t *testing.T, res spi.Result) *Goal {
	t.Helper()
	m, _ := res.Data.(map[string]any)
	goal, ok := m["goal"].(*Goal)
	if !ok || goal == nil {
		t.Fatalf("读不到目标: %#v", res.Data)
	}
	return goal
}

func goalID(t *testing.T, st *store, ctx context.Context) string {
	t.Helper()
	res, err := call(t, st, kindGet, ctx, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	return goalOf(t, res).ID
}
