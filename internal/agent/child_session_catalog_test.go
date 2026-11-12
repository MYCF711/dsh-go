package agent

// rc.2 `subagent/catalog` v3 写入端的四类拒绝 + 一条成功。
//
// 官方证据：
//   - v3 是 native V4 的唯一写法（`dsh-subagent/src/catalog.ts:40`），`name` 与
//     continuable 的 `label` 都是必填（`names.ts:23-25` 的正则 + 保留名 `lead`）；
//   - 同代唯一：同一父代理下 childId/name 不许重复（官方 `SiblingNameReservations.reserve`
//     与 `dsh-session-format-v3-to-v4/lib/index.js:1037-1046`）。
//
// 校验与判重都由 `internal/session` 的 B3 校验器执行，这里验的是**接线**：
// 四类拒绝必须在**建子会话之前**发生（不留下空子会话），成功时 `subagent/catalog`
// 必须真的落进子会话日志，且 `subagent/spawn` 仍是第一条事件。

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"dshgo/codecs/v4"
	"dshgo/internal/modes"
	"dshgo/internal/session"
)

// seedParentSession 建一份父会话日志（可预置同代 catalog 事实），返回 home。
func seedParentSession(t *testing.T, parentID string, catalogNames ...string) string {
	t.Helper()
	home := t.TempDir()
	lg, err := session.CreateSession(home, t.TempDir(), parentID, modes.DefaultID)
	if err != nil {
		t.Fatalf("建父会话: %v", err)
	}
	for _, name := range catalogNames {
		if err := lg.Append(session.EventSubagentCatalog,
			session.SubagentCatalogV3("child-"+name, 1000, session.CatalogModeContinuable,
				"label-"+name, name)); err != nil {
			t.Fatalf("写父会话 catalog: %v", err)
		}
	}
	if err := lg.Close(); err != nil {
		t.Fatalf("关父会话: %v", err)
	}
	return home
}

// readSessionEvents 读一份会话的全部 v4 事件。
func readSessionEvents(t *testing.T, home, id string) []v4.Event {
	t.Helper()
	path, err := session.FindByID(home, id)
	if err != nil {
		t.Fatalf("找会话 %s: %v", id, err)
	}
	back, err := v4.ReadFile(path)
	if err != nil {
		t.Fatalf("读会话 %s: %v", id, err)
	}
	return back.Events
}

// TestSpawnRejectsInvalidCatalogName：四类拒绝（正则 / 保留名 / 同代重名 / 缺 label），
// 且拒绝发生在建子会话之前——不留下半条子会话。
func TestSpawnRejectsInvalidCatalogName(t *testing.T) {
	cases := []struct {
		name   string
		opts   ChildSpawnOptions
		seed   []string
		want   string
		parent string
	}{
		{
			name: "大写开头不匹配 ^[a-z][a-z0-9-]{0,31}$",
			opts: ChildSpawnOptions{Name: "Helper", Label: "helper 展示名"},
			want: "不合法",
		},
		{
			name: "保留名 lead",
			opts: ChildSpawnOptions{Name: session.CatalogLeadReservedName, Label: "主代理"},
			want: "保留名",
		},
		{
			name: "同代重名",
			opts: ChildSpawnOptions{Name: "helper", Label: "helper 展示名"},
			seed: []string{"helper"},
			want: "duplicate",
		},
		{
			name: "continuable 缺 label",
			opts: ChildSpawnOptions{Name: "helper"},
			want: "label",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			parent := "parent-1"
			home := seedParentSession(t, parent, c.seed...)
			// 先量一遍父会话下的会话数量：拒绝必须一条都不新增。
			before, err := session.ListSessions(home, "")
			if err != nil {
				t.Fatalf("列会话: %v", err)
			}
			_, err = (ChildSpawn{Home: home, ParentSessionID: parent, Cwd: home}).Spawn(c.opts)
			if err == nil {
				t.Fatalf("%s：必须被拒绝", c.name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("%s：错误 %q 里应包含 %q", c.name, err.Error(), c.want)
			}
			after, err := session.ListSessions(home, "")
			if err != nil {
				t.Fatalf("列会话: %v", err)
			}
			if len(after) != len(before) {
				t.Fatalf("%s：拒绝发生在建子会话之前才不留垃圾，会话数 %d → %d",
					c.name, len(before), len(after))
			}
		})
	}
}

// TestSpawnWritesCatalogV3：成功路径——具名委派落一条 v3 事实（带 name/label），
// 且 `subagent/spawn` 仍是子会话的第一条事件（FindChild 的判据不变）。
func TestSpawnWritesCatalogV3(t *testing.T) {
	home := seedParentSession(t, "parent-1")
	const fixedMillis = int64(1757654321000)

	rec, err := (ChildSpawn{Home: home, ParentSessionID: "parent-1", Cwd: home,
		Now: func() time.Time { return time.UnixMilli(fixedMillis) }}).Spawn(ChildSpawnOptions{
		AgentID: "reviewer",
		Prompt:  "看一遍会话",
		Answer:  "看完了",
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	events := readSessionEvents(t, home, rec.ChildSessionID)
	if len(events) == 0 || events[0].Type != EventChildSpawn {
		t.Fatalf("第一条事件必须是 %s：%v", EventChildSpawn, firstTypes(events))
	}

	var fact map[string]json.RawMessage
	found := false
	for _, event := range events {
		if event.Type != session.EventSubagentCatalog {
			continue
		}
		found = true
		if err := json.Unmarshal(event.Data, &fact); err != nil {
			t.Fatalf("解析 catalog: %v", err)
		}
	}
	if !found {
		t.Fatalf("子会话日志里没有 %s 事实：%v", session.EventSubagentCatalog, firstTypes(events))
	}
	parsed, err := session.ValidateSubagentCatalogPayload(mustJSON(t, fact))
	if err != nil {
		t.Fatalf("写下的 catalog 事实过不了官方校验器：%v", err)
	}
	if parsed.Version != session.CatalogVersionV3 {
		t.Fatalf("version = %d，want %d", parsed.Version, session.CatalogVersionV3)
	}
	if parsed.Name != "reviewer" {
		t.Fatalf("name = %q，want reviewer（具名委派用 AgentID）", parsed.Name)
	}
	if parsed.Mode != session.CatalogModeContinuable || parsed.Label == "" {
		t.Fatalf("continuable 事实必须带非空 label：%+v", parsed)
	}
	if parsed.ChildID != rec.ChildSessionID || parsed.ChildCreatedAt != fixedMillis {
		t.Fatalf("childId/childCreatedAt 必须与落盘记录一致：%+v / %+v", parsed, rec)
	}
}

// TestSpawnAnonymousKeepsLegacyLog：匿名委派（没有名字）不写 catalog 事实——
// 既有读者的日志字节与改动前一致。
func TestSpawnAnonymousKeepsLegacyLog(t *testing.T) {
	home := t.TempDir()
	rec, err := (ChildSpawn{Home: home, ParentSessionID: "parent-1", Cwd: home}).Spawn(
		ChildSpawnOptions{Prompt: "帮我看看", Answer: "好"})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	for _, event := range readSessionEvents(t, home, rec.ChildSessionID) {
		if event.Type == session.EventSubagentCatalog {
			t.Fatalf("匿名委派不该写 catalog 事实：%s", event.Data)
		}
	}
}

func firstTypes(events []v4.Event) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		out = append(out, event.Type)
	}
	return out
}

func mustJSON(t *testing.T, value map[string]json.RawMessage) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("重新序列化: %v", err)
	}
	return raw
}
