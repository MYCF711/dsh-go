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

// 压表只许去掉两类东西：参数的说明文字（compact），以及 schema 里
// "接口已经规定 / 省略等于默认"的冗余骨架（tighten）。类型、enum、required、
// additionalProperties:false、参数名，一个都不许动——
// 少一个 required 模型就会漏参数，少一个 enum 就会传非法值。
//
// 这条测试盯着"模型看到的那张表结构不变"，因为 compact 已经默认开启：
// 一旦有人在某处漏改，模型看到的就是残缺的契约，而单测很难发现。
//
// 判据不是"字节少了"，是**归一化后逐工具相等**：把"顶层 type:object / 空
// properties / 空 required / additionalProperties:true"这四条等价写法抹平之后，
// 完整表与投影表必须一模一样。
func TestCompactTableKeepsContract(t *testing.T) {
	reg := assembleRegistry(t)
	// 走投影表本身（而不是另调一次 tightenSchema）：模型收到的就是这里的 Schema()。
	view := tightenTable(reg)
	got := map[string]spi.Tool{}
	for _, tl := range view.Tools() {
		got[tl.Name()] = tl
	}

	var fullBytes, compactBytes, tightBytes, restored int
	var descDropped int
	n := 0
	for _, e := range buildToolTableMust(t) {
		n++
		compact := agent.CompactSchema(e.Parameters)
		tl, ok := got[e.Name]
		if !ok {
			t.Fatalf("%s 不在投影表里：压表把工具弄丢了", e.Name)
		}
		tight := tl.Schema()
		fullBytes += len(e.Parameters)
		compactBytes += len(compact)
		tightBytes += len(tight)

		// 名叫 description 的**参数**不许丢：agent.CompactSchema 会连它一起删，
		// 投影必须补回来（补回来的字节 = 投影比"压过的表再收紧"多出来的那块）。
		want, err := countDescParams(e.Parameters)
		if err != nil {
			t.Fatalf("%s: %v", e.Name, err)
		}
		have, err := countDescParams(tight)
		if err != nil {
			t.Fatalf("%s: %v", e.Name, err)
		}
		if have != want {
			t.Fatalf("%s：完整表有 %d 个名叫 description 的参数，投影后只剩 %d 个",
				e.Name, want, have)
		}
		old, err := countDescParams(compact)
		if err != nil {
			t.Fatalf("%s: %v", e.Name, err)
		}
		descDropped += want - old
		restored += len(tight) - len(tightenSchema(compact))

		if !json.Valid(tight) {
			t.Fatalf("%s 压完不是合法 JSON: %s", e.Name, tight)
		}
		// 再压一遍必须一字不差：还有 annotation 没删干净的话，第二遍就会把它删掉。
		if again := tightenSchema(tight); string(again) != string(tight) {
			t.Fatalf("%s 的投影不是不动点（还有该删的没删）:\n一遍: %s\n二遍: %s",
				e.Name, tight, again)
		}
		// 收紧只会删东西，绝不该比完整表更大。
		if len(tight) > len(e.Parameters) {
			t.Fatalf("%s 收紧之后比完整表还大（%d → %d）", e.Name, len(e.Parameters), len(tight))
		}

		a, err := canonSchema(e.Parameters, true)
		if err != nil {
			t.Fatalf("%s: 原 schema 解析失败: %v", e.Name, err)
		}
		b, err := canonSchema(tight, true)
		if err != nil {
			t.Fatalf("%s: 投影不是合法 JSON: %v", e.Name, err)
		}
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("%s: 压表改动了结构\n原: %v\n压后: %v", e.Name, a, b)
		}
	}
	if n == 0 {
		t.Fatal("一个工具都没有")
	}
	if tightBytes >= compactBytes {
		t.Fatalf("收紧后反而比上一代 compact 大（%d → %d）——白改", compactBytes, tightBytes)
	}
	t.Logf("工具表 %d 个：参数区 %d → %d（去说明文字）→ %d（再去骨架），净省 %d 字节；"+
		"其中 compact 原本会删掉 %d 个名叫 description 的参数，投影把它们补了回来（+%d 字节）",
		n, fullBytes, compactBytes, tightBytes, fullBytes-tightBytes,
		descDropped, restored)
}

// canonSchema 把"等价写法"归一，供上面的结构比对使用。
//
// 归一规则**就是这一档的契约**，多一条都算越界：
//   - description：说明文字，compact 的正当范围；
//   - 顶层 type:"object"：function calling 的 parameters 必须是对象，接口规定；
//   - 空的 properties / required：省略与空等价；
//   - additionalProperties:true：JSON Schema 的默认值。
//
// 别的键一律原样保留并参与比较——所以少一个 enum 值、少一个 required 项、
// 把 additionalProperties:false 删了，都会红。
func canonSchema(raw json.RawMessage, root bool) (any, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return v, nil
	}
	return canonObject(obj, root), nil
}

// canonObject 归一一个 **schema 对象**。
//
// 注意 properties 单独处理：那一层的键是参数名，名叫 description 也只是
// 参数名，不是注解（agent.CompactSchema 就是在这里丢了 6 个工具的参数）。
func canonObject(m map[string]any, root bool) map[string]any {
	out := make(map[string]any, len(m))
	for k, val := range m {
		switch k {
		case "description":
			continue
		case "type":
			if root && val == "object" {
				continue
			}
		case "additionalProperties":
			if val == true {
				continue
			}
		case "properties":
			props := make(map[string]any)
			if pm, ok := val.(map[string]any); ok {
				for name, sub := range pm {
					if so, ok := sub.(map[string]any); ok {
						props[name] = canonObject(so, false)
						continue
					}
					props[name] = sub
				}
			}
			if len(props) == 0 {
				continue
			}
			out[k] = props
			continue
		case "required":
			arr, _ := val.([]any)
			if len(arr) == 0 {
				continue
			}
			sort.Slice(arr, func(i, j int) bool { return arr[i].(string) < arr[j].(string) })
			out[k] = arr
			continue
		}
		out[k] = canonValue(val)
	}
	return out
}

func canonValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		return canonObject(x, false)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = canonValue(e)
		}
		return out
	}
	return v
}

// countDescParams 数一个 schema 里"名字叫 description 的参数"有几个（任意深度）。
//
// 只数 properties 映射里的键——那是模型要填的参数名；schema 对象上的
// description 是说明注解，不算。两者同名，正是这档要分清楚的东西。
func countDescParams(raw json.RawMessage) (int, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, err
	}
	var walk func(any) int
	walk = func(x any) int {
		obj, ok := x.(map[string]any)
		if !ok {
			return 0
		}
		n := 0
		for k, val := range obj {
			if k == "properties" {
				if pm, ok := val.(map[string]any); ok {
					for name, sub := range pm {
						if name == "description" {
							n++
						}
						n += walk(sub)
					}
					continue
				}
			}
			n += walk(val)
		}
		return n
	}
	return walk(v), nil
}

// 参数名恰好叫 description 时不许被当注解删掉（本仓真有 6 个工具这样）。
//
// 这是 agent.CompactSchema 的老毛病：它见到 description 键就删，分不清
// "schema 上的说明" 和 "叫 description 的参数"。删掉之后 required 里还点着它，
// 模型看到一个"必填但没类型、也没说明"的名字——等于凭空少了一条契约。
func TestTightSchemaKeepsDescriptionParam(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{` +
		`"description":{"type":"string","description":"给用户看的短描述"},` +
		`"command":{"type":"string"}},"required":["command","description"],"additionalProperties":false}`)
	got := tightenSchema(raw)

	var s struct {
		Type       string                           `json:"type"`
		Properties map[string]struct{ Type string } `json:"properties"`
		Required   []string                         `json:"required"`
		Additional any                              `json:"additionalProperties"`
	}
	if err := json.Unmarshal(got, &s); err != nil {
		t.Fatalf("收紧后的 schema 不是合法 JSON: %v（%s）", err, got)
	}
	if s.Properties["description"].Type != "string" {
		t.Fatalf("名叫 description 的参数被删了：模型只看到 required 里一个没类型的名字\n%s", got)
	}
	for _, r := range s.Required {
		if _, ok := s.Properties[r]; !ok {
			t.Fatalf("required 点名了 %q，properties 里却没有：%s", r, got)
		}
	}
	if s.Additional != false {
		t.Fatalf("additionalProperties:false 是有内容的约束，不许删：%s", got)
	}
	if strings.Contains(string(got), "给用户看的短描述") {
		t.Fatalf("参数的说明文字还在：%s", got)
	}
	if strings.Contains(string(got), `"object"`) {
		t.Fatalf("顶层 type:object 还在：%s", got)
	}
}

// 表里每个 required 的名字都必须真在 properties 里。这条不依赖"怎么压"，
// 是契约本身的完整性：模型看不到的参数，不该被标成必填。
func TestRequiredParamsAreDeclared(t *testing.T) {
	check := func(what, name string, schema json.RawMessage) {
		var s struct {
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		}
		if err := json.Unmarshal(schema, &s); err != nil {
			t.Fatalf("%s %s 的 schema 不是合法 JSON: %v", what, name, err)
		}
		for _, r := range s.Required {
			if _, ok := s.Properties[r]; !ok {
				t.Errorf("%s %s：required 点名了 %q，properties 里没有它", what, name, r)
			}
		}
	}
	for _, e := range buildToolTableMust(t) {
		check("完整表", e.Name, e.Parameters)
	}
	for _, tl := range tightenTable(assembleRegistry(t)).Tools() {
		check("投影表", tl.Name(), tl.Schema())
	}
}

// 收紧层不能只是个"看着小"的壳：名字、描述、Caps 必须原样透传
// （审批按 Caps 判定），Exec 必须落到原工具，Get 与 Tools 必须给同一份 schema。
func TestTightTableIsAThinWrapper(t *testing.T) {
	stub := &stubTool{
		name:   "stub",
		desc:   "一个假工具",
		schema: json.RawMessage(`{"type":"object","properties":{"n":{"type":"string"}},"required":["n"],"additionalProperties":false}`),
		caps:   spi.Caps{Writes: true},
	}
	reg := &stubRegistry{tools: map[string]spi.Tool{"stub": stub}}
	view := tightenTable(reg)

	got, ok := view.Get("stub")
	if !ok {
		t.Fatal("Get(stub) 取不到")
	}
	if got.Name() != "stub" || got.Description() != "一个假工具" {
		t.Fatalf("名字/描述被改了：%q %q", got.Name(), got.Description())
	}
	if got.Caps() != stub.caps {
		t.Fatalf("Caps 被改了：%+v（审批会跟着跑偏）", got.Caps())
	}
	if len(view.Tools()) != 1 || string(view.Tools()[0].Schema()) != string(got.Schema()) {
		t.Fatal("Tools() 与 Get() 给的 schema 不是同一份")
	}
	want := `{"additionalProperties":false,"properties":{"n":{"type":"string"}},"required":["n"]}`
	if string(got.Schema()) != want {
		t.Fatalf("投影结果 = %s，期望 %s", got.Schema(), want)
	}
	if _, err := got.Exec(context.Background(), json.RawMessage(`{"n":"x"}`)); err != nil {
		t.Fatalf("Exec 没有落到原工具: %v", err)
	}
	if stub.calls != 1 {
		t.Fatalf("原工具 Exec 被调了 %d 次，期望 1", stub.calls)
	}
	if _, ok := view.Get("没有这个工具"); ok {
		t.Fatal("不存在的工具不该被投影层编出来")
	}
	if err := view.Register(stub); err == nil {
		t.Fatal("投影层不能注册工具")
	}
}

type stubTool struct {
	name   string
	desc   string
	schema json.RawMessage
	caps   spi.Caps
	calls  int
}

func (s *stubTool) Name() string            { return s.name }
func (s *stubTool) Description() string     { return s.desc }
func (s *stubTool) Schema() json.RawMessage { return s.schema }
func (s *stubTool) Caps() spi.Caps          { return s.caps }
func (s *stubTool) Exec(context.Context, json.RawMessage) (spi.Result, error) {
	s.calls++
	return spi.Result{Summary: "ok"}, nil
}

type stubRegistry struct{ tools map[string]spi.Tool }

func (r *stubRegistry) Register(t spi.Tool) error {
	if r.tools == nil {
		r.tools = map[string]spi.Tool{}
	}
	r.tools[t.Name()] = t
	return nil
}
func (r *stubRegistry) Tools() []spi.Tool {
	out := make([]spi.Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}
func (r *stubRegistry) Get(name string) (spi.Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
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
