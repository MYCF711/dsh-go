package main

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"dshgo/internal/agent"
	"dshgo/internal/spi"
)

// 压表只许去掉参数的说明文字。类型、enum、required、items 一个都不许动——
// 少一个 required 模型就会漏参数，少一个 enum 就会传非法值。
//
// 这条测试盯着"两种表结构一致"，因为 compact 已经默认开启：
// 一旦有人在某处漏改，模型看到的就是残缺的契约，而单测很难发现。
func TestCompactTableKeepsContract(t *testing.T) {
	table := buildToolTableMust(t)
	var fullBytes, compactBytes int
	for _, e := range table {
		compact := agent.CompactSchema(e.Parameters)
		fullBytes += len(e.Parameters)
		compactBytes += len(compact)

		var a, b any
		if err := json.Unmarshal(e.Parameters, &a); err != nil {
			t.Fatalf("%s: 原 schema 不是合法 JSON: %v", e.Name, err)
		}
		if err := json.Unmarshal(compact, &b); err != nil {
			t.Fatalf("%s: 压完不是合法 JSON: %v", e.Name, err)
		}
		if !reflect.DeepEqual(stripDesc(a), stripDesc(b)) {
			t.Fatalf("%s: 压表改动了结构\n原: %v\n压后: %v", e.Name, a, b)
		}
	}
	if compactBytes >= fullBytes {
		t.Fatalf("压完没有变小（%d → %d），默认开它就没有意义", fullBytes, compactBytes)
	}
	t.Logf("工具表 %d 个：%d → %d 字节（省 %.0f%%）",
		len(table), fullBytes, compactBytes, 100*(1-float64(compactBytes)/float64(fullBytes)))
}

func stripDesc(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if k == "description" {
				continue
			}
			out[k] = stripDesc(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = stripDesc(val)
		}
		return out
	}
	return v
}

// 默认值本身也要钉住：翻默认是经过 20 次 A/B 的决定，不该被顺手改回去。
// flag 的默认值由 scripts/check.sh 里的 `--help` 断言兜住（那才是模型真正看到的）。
func TestCompactIsTheDefault(t *testing.T) {
	if defaultToolSchema != "compact" {
		t.Fatalf("默认工具表 = %q，期望 compact（改它要先跑 scripts/tool-table-ab.py 拿数据）", defaultToolSchema)
	}
}

// ---- merged：同族工具合成"一个工具 + action" ----
//
// 这一档的全部风险都在"模型看到的契约还对不对"上，所以测试不看字节数，
// 看三件事：每个成员的每个参数是否仍然可达、action 枚举是否就是后缀、
// 以及**默认值是否仍然不是它**。

// TestMergedTableIsAProjection 钉住"合并只改模型看到的表"：
//  1. 四个族名各出现一次，17 个成员一个不剩；
//  2. action 枚举 = 原工具名的后缀，一个不多一个不少；
//  3. 每个成员的每个参数都能在合并 schema 里找到，且结构一字不差；
//  4. 不发明参数：合并 schema 里除 action（和改名的那几个）之外，
//     每个 property 都必须能在某个成员的定义里找到出处。
func TestMergedTableIsAProjection(t *testing.T) {
	reg := assembleRegistry(t)
	mt, warn, err := newMergedTable(reg)
	if err != nil {
		t.Fatal(err)
	}
	if len(warn) != 0 {
		t.Fatalf("本仓的装配不该跳过任何族（跳过了就说明 schema 合不进来）: %v", warn)
	}

	got := map[string]spi.Tool{}
	for _, tool := range mt.Tools() {
		got[tool.Name()] = tool
	}
	// 成员必须全部消失：留下一个，模型就有两条路可走，合并等于没做。
	for _, fam := range mergedFamilies() {
		for _, a := range fam.Actions {
			if _, ok := got[a.Tool]; ok {
				t.Fatalf("%s 还在表里：合并后成员名不该对模型可见", a.Tool)
			}
		}
	}
	// 例外要写死：watch_doctor 的 schema 是 watch_ack 的复制（required=numbers），
	// 合进来只能靠"无视 schema"或"逼模型编编号"，所以它留在表外，名字不变。
	if _, ok := got["watch_doctor"]; !ok {
		t.Fatal("watch_doctor 应当仍是独立工具（它的 schema 不可信，不能合并）")
	}
	members := 0
	for _, fam := range mergedFamilies() {
		members += len(fam.Actions)
	}
	if want := len(reg.Tools()) - members + len(mergedFamilies()); len(mt.Tools()) != want {
		t.Fatalf("合并后 %d 个工具，期望 %d（%d 个成员 → %d 个族名工具）",
			len(mt.Tools()), want, members, len(mergedFamilies()))
	}

	for _, fam := range mergedFamilies() {
		tool, ok := got[fam.Name]
		if !ok {
			t.Fatalf("合并结果里没有 %s", fam.Name)
		}
		var schema struct {
			Type       string                     `json:"type"`
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		}
		if err := json.Unmarshal(tool.Schema(), &schema); err != nil {
			t.Fatalf("%s 的 schema 不是合法 JSON: %v", fam.Name, err)
		}
		if schema.Type != "object" {
			t.Fatalf("%s 顶层不是 object", fam.Name)
		}
		// action 枚举：就是后缀。
		var act struct {
			Type string   `json:"type"`
			Enum []string `json:"enum"`
		}
		if err := json.Unmarshal(schema.Properties["action"], &act); err != nil {
			t.Fatalf("%s 的 action 没有 enum: %v", fam.Name, err)
		}
		if act.Type != "string" || !sameStrings(act.Enum, actionNames(fam)) {
			t.Fatalf("%s 的 action 枚举 = %v，期望 %v", fam.Name, act.Enum, actionNames(fam))
		}
		if len(schema.Required) != 1 || schema.Required[0] != "action" {
			t.Fatalf("%s 的 required = %v，只该有 action（别的参数属于具体 action）", fam.Name, schema.Required)
		}

		// 成员参数逐个核对：可达 + 结构一致。
		for _, a := range fam.Actions {
			member, ok := reg.Get(a.Tool)
			if !ok {
				t.Fatalf("装配里没有 %s", a.Tool)
			}
			var ms struct {
				Properties map[string]json.RawMessage `json:"properties"`
			}
			if err := json.Unmarshal(member.Schema(), &ms); err != nil {
				t.Fatal(err)
			}
			for name, raw := range ms.Properties {
				vis := name
				for from, to := range a.Rename {
					if to == name {
						vis = from
					}
				}
				if vis == "action" {
					t.Fatalf("%s 自带 action 却没在 Rename 里改名，会和外层 action 撞车", a.Tool)
				}
				mergedRaw, ok := schema.Properties[vis]
				if !ok {
					t.Fatalf("%s 的参数 %q（→%q）在合并 schema 里没有了：模型填不了它", a.Tool, name, vis)
				}
				if !sameShape(raw, mergedRaw) {
					t.Fatalf("%s 的参数 %q 在合并 schema 里变了形状：\n原: %s\n合: %s",
						a.Tool, vis, raw, mergedRaw)
				}
			}
		}
		// 反向：不发明参数。
		for vis := range schema.Properties {
			if vis == "action" {
				continue
			}
			found := false
			for _, a := range fam.Actions {
				member, _ := reg.Get(a.Tool)
				var ms struct {
					Properties map[string]json.RawMessage `json:"properties"`
				}
				if err := json.Unmarshal(member.Schema(), &ms); err != nil {
					t.Fatal(err)
				}
				for name := range ms.Properties {
					if name == vis {
						found = true
					}
					for from, to := range a.Rename {
						if to == name && from == vis {
							found = true
						}
					}
				}
			}
			if !found {
				t.Fatalf("%s 的合并 schema 凭空多了参数 %q：没有成员认得它", fam.Name, vis)
			}
		}
	}
}

// TestMergedRoutesToOriginalTool 钉住执行侧：action 必须还原成原工具名，
// 参数也要还原（撞名的那一处尤其不能漏）。
func TestMergedRoutesToOriginalTool(t *testing.T) {
	reg := assembleRegistry(t)
	mt, _, err := newMergedTable(reg)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		args string
		want string // 空 = 不该被合并层改写（透传给原 Runner）
		argv string
	}{
		{"watch", `{"action":"ack","numbers":[7,8]}`, "watch_ack", `{"numbers":[7,8]}`},
		{"watch", `{"action":"status"}`, "watch_status", `{}`},
		// watch_doctor 没并进来（它的 schema 是 watch_ack 的复制），原样透传。
		{"watch_doctor", `{"repo":"o/n"}`, "", ""},
		{"schedule", `{"action":"list"}`, "schedule_list", `{}`},
		{"schedule", `{"action":"delete","id":"sch-0001-3f9a"}`, "schedule_delete", `{"id":"sch-0001-3f9a"}`},
		{"hindsight", `{"action":"search_knowledge_pages","query":"x"}`, "hindsight_search_knowledge_pages", `{"query":"x"}`},
		{"team_task", `{"action":"get","task_id":"task-1"}`, "team_task_get", `{"task_id":"task-1"}`},
		// 撞名的那一处：update_action → action，外层 action 自己消失。
		{"team_task", `{"action":"update","task_id":"task-1","update_action":"claim","owner":"me"}`,
			"team_task_update", `{"action":"claim","owner":"me","task_id":"task-1"}`},
		// 不是合并名：原样交给原来的 Runner（合并不该碰别的工具）。
		{"bash", `{"cmd":"ls"}`, "", ""},
		{"read", `{"path":"README.md"}`, "", ""},
	}
	for _, c := range cases {
		real, args, err := mt.rewrite(c.name, json.RawMessage(c.args))
		if err != nil {
			t.Fatalf("%s %s: %v", c.name, c.args, err)
		}
		if c.want == "" {
			if real != "" || args != nil {
				t.Fatalf("%s 不是合并工具，却被改写成 %q %s", c.name, real, args)
			}
			continue
		}
		if real != c.want {
			t.Fatalf("%s %s → %s，期望 %s", c.name, c.args, real, c.want)
		}
		var gotV, wantV any
		if err := json.Unmarshal(args, &gotV); err != nil {
			t.Fatalf("%s 改写后的参数不是 JSON: %v", c.name, err)
		}
		if err := json.Unmarshal([]byte(c.argv), &wantV); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(gotV, wantV) {
			t.Fatalf("%s 改写后的参数 = %s，期望 %s", c.name, args, c.argv)
		}
	}
}

// 选错 action / 忘了 action 是 merged 最可能的失败方式：必须报错且 Hint 里
// 直接列出合法值（R3：失败要给下一步），不能静默透传给一个不存在的工具。
func TestMergedBadActionGivesHint(t *testing.T) {
	reg := assembleRegistry(t)
	mt, _, err := newMergedTable(reg)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range []string{`{"action":"nope"}`, `{}`, `{"action":""}`} {
		if _, _, err := mt.rewrite("watch", json.RawMessage(args)); err == nil {
			t.Fatalf("%s 应该报错（没有 action 或 action 不认识）", args)
		} else if !strings.Contains(err.Error(), "action=") {
			t.Fatalf("报错里没列合法 action：%v", err)
		}
	}
	// 只有合并名的调用才可能出这种错：其它工具的参数原样透传，不做校验。
	if _, _, err := mt.rewrite("bash", json.RawMessage(`{"nope":1}`)); err != nil {
		t.Fatalf("非合并工具不该被校验: %v", err)
	}
}

// 合并后的 required 只能写交集，所以"create 必填 description"这类约束
// 必须在路由里补回来。这条测试钉的就是"别静默收下缺参数的任务"——
// 实测漏了它，team_task_create 会建成一条没有描述的任务，然后模型花四步去补。
func TestMergedEnforcesPerActionRequired(t *testing.T) {
	reg := assembleRegistry(t)
	mt, _, err := newMergedTable(reg)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := mt.rewrite("team_task", json.RawMessage(`{"action":"create","subject":"A"}`)); err == nil {
		t.Fatal("create 缺 description 必须报错（team_task_create 自己不校验）")
	} else if !strings.Contains(err.Error(), "description") {
		t.Fatalf("报错里要点名缺哪个参数：%v", err)
	}
	if _, _, err := mt.rewrite("team_task", json.RawMessage(
		`{"action":"create","subject":"A","description":"B"}`)); err != nil {
		t.Fatalf("参数齐了不该报错: %v", err)
	}
	// 成员自己没写 required 的，就照旧不校验（schedule_delete 的 id 就是这种）。
	if _, _, err := mt.rewrite("schedule", json.RawMessage(`{"action":"delete"}`)); err != nil {
		t.Fatalf("schedule_delete 原本就没 required，路由不该比原表更严: %v", err)
	}
	// 容错：action 填了**全名**也认（全名唯一确定成员，没有歧义）。
	real, _, err := mt.rewrite("hindsight", json.RawMessage(
		`{"action":"hindsight_ingest_document","title":"t","content":"c"}`))
	if err != nil {
		t.Fatalf("action 用全名应当被接受（否则为拼写白跑一步）: %v", err)
	}
	if real != "hindsight_ingest_document" {
		t.Fatalf("action 全名应路由到 hindsight_ingest_document，实际 %s", real)
	}
}

// 合并工具自己的 Exec 不能是个"看着能用、实际没实现"的壳：
// 没绑定 Router 时必须给出 Hint，绑定了就必须走同一条路由。
func TestMergedToolExecNeedsRouter(t *testing.T) {
	reg := assembleRegistry(t)
	mt, _, err := newMergedTable(reg)
	if err != nil {
		t.Fatal(err)
	}
	tool, ok := mt.Get("watch")
	if !ok {
		t.Fatal("Get(watch) 应该有投影工具")
	}
	res, err := tool.Exec(context.Background(), json.RawMessage(`{"action":"status"}`))
	if err == nil {
		t.Fatal("没绑定路由时 Exec 应该失败")
	}
	if res.Hint == "" {
		t.Fatal("失败没给 Hint（R3）")
	}
	called := ""
	mt.bindRunner(func(ctx context.Context, name string, args json.RawMessage) (spi.Result, error) {
		called = name + " " + string(args)
		return spi.Result{Summary: "ok"}, nil
	})
	if _, err := tool.Exec(context.Background(), json.RawMessage(`{"action":"status"}`)); err != nil {
		t.Fatal(err)
	}
	if called != "watch "+`{"action":"status"}` {
		t.Fatalf("Exec 应经 Router 路由（并带上 action），实际 %q", called)
	}
}

// merged 只能是显式打开：默认值仍是 compact，旧名 --tool-schema 仍然可用。
func TestMergedNeedsExplicitFlag(t *testing.T) {
	for _, c := range []struct{ mode, legacy, want string }{
		{"", "", toolsModeCompact},
		{"", "full", toolsModeFull},
		{"", "compact", toolsModeCompact},
		{"merged", "", toolsModeMerged},
		{"merged", "compact", toolsModeMerged}, // 新名优先
		{"compact", "merged", toolsModeCompact},
	} {
		got, err := resolveToolsMode(c.mode, c.legacy)
		if err != nil {
			t.Fatalf("mode=%q legacy=%q: %v", c.mode, c.legacy, err)
		}
		if got != c.want {
			t.Fatalf("mode=%q legacy=%q → %q，期望 %q", c.mode, c.legacy, got, c.want)
		}
	}
	if _, err := resolveToolsMode("merge", ""); err == nil {
		t.Fatal("拼错的 --tools-mode 必须报错，不能静默退回某个形态")
	}
	if defaultToolSchema == toolsModeMerged {
		t.Fatal("merged 不能是默认值：它的正确率还没被 A/B 证实")
	}
}

// 省下来的字节是这一档存在的理由，数字掉了就说明合并退化了。
func TestMergedTableIsSmallerThanCompact(t *testing.T) {
	reg := assembleRegistry(t)
	mt, _, err := newMergedTable(reg)
	if err != nil {
		t.Fatal(err)
	}
	mergedEntries, err := toolTableOf(mt.Tools())
	if err != nil {
		t.Fatal(err)
	}
	compactEntries, err := toolTableOf(reg.Tools())
	if err != nil {
		t.Fatal(err)
	}
	var mergedBytes, compactBytes int
	for i := range mergedEntries {
		mergedEntries[i].Parameters = agent.CompactSchema(mergedEntries[i].Parameters)
		mergedEntries[i].Caps = spi.Caps{} // 只比模型看到的东西
	}
	for i := range compactEntries {
		compactEntries[i].Parameters = agent.CompactSchema(compactEntries[i].Parameters)
		compactEntries[i].Caps = spi.Caps{}
	}
	for _, e := range mergedEntries {
		b, _ := json.Marshal(e)
		mergedBytes += len(b)
	}
	for _, e := range compactEntries {
		b, _ := json.Marshal(e)
		compactBytes += len(b)
	}
	if mergedBytes >= compactBytes {
		t.Fatalf("merged 没比 compact 小（%d ≥ %d）——那它就没有存在的理由", mergedBytes, compactBytes)
	}
	t.Logf("merged 比 compact 省 %d 字节（%d → %d，%.1f%%）",
		compactBytes-mergedBytes, compactBytes, mergedBytes,
		100*(1-float64(mergedBytes)/float64(compactBytes)))
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x := append([]string(nil), a...)
	y := append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}
