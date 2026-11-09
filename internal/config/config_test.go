package config

// 先红（HS-14）：基线里没有配置树。`/api/config.set` 只认扁平的
// `map[string]string`（internal/transport/handlers_config.go），嵌套对象在这一层
// 根本无法表达，也没有 revision 概念——于是"两个人同时改配置"必然是后写覆盖先写，
// 而且被覆盖的那一方拿到的是 200。本文件钉住四件事：
//
//  1. 树是**嵌套 + 有类型**的（string / bool / number / object / array），同时能降级成
//     旧协议认的扁平 key→string 表——旧客户端不回归；
//  2. patch 是**全有或全无**的：一条 op 不合法，整批一条都不落；
//  3. 落盘是**原子**的（临时文件 → fsync → rename）：写失败时旧文件一个字节不变，
//     内存里的 revision 也不前进（否则下一次"成功"其实是基于没落盘的树）；
//  4. revision 是**单调**的：过期 revision 一律冲突，绝不静默覆盖。
//
// 为什么 key 里不许带 `.`：一旦允许，`{"a.b": 1}` 与 `{"a": {"b": 1}}` 就是同一份
// 配置的两种拼法，patch 路径再也说不清指的是哪一个。宁可在建树时拒绝。

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func nestedValues() map[string]any {
	return map[string]any{
		"ui": map[string]any{
			"theme":   "dark",
			"zoom":    1.25,
			"compact": true,
		},
		"models": []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "b"},
		},
		"lang": "zh",
	}
}

// TestConfigTreeNestedTyped 是红 A 的绿侧：配置不再是扁平 kv。
func TestConfigTreeNestedTyped(t *testing.T) {
	tree, err := NewTree(nestedValues())
	if err != nil {
		t.Fatalf("建树失败: %v", err)
	}

	if got, ok := tree.Get("ui.theme"); !ok || got != "dark" {
		t.Fatalf("ui.theme = %#v（ok=%v），want \"dark\"", got, ok)
	}
	if got, ok := tree.Get("ui.zoom"); !ok {
		t.Fatal("ui.zoom 读不到")
	} else if number, isNumber := got.(float64); !isNumber || number != 1.25 {
		t.Fatalf("ui.zoom = %#v，want float64(1.25)——基线只能存字符串，这里必须是真数字", got)
	}
	if got, ok := tree.Get("ui.compact"); !ok {
		t.Fatal("ui.compact 读不到")
	} else if flag, isBool := got.(bool); !isBool || !flag {
		t.Fatalf("ui.compact = %#v，want bool(true)", got)
	}
	if got, ok := tree.Get("models"); !ok {
		t.Fatal("models 读不到")
	} else if list, isList := got.([]any); !isList || len(list) != 2 {
		t.Fatalf("models = %#v，want 长度 2 的数组", got)
	}
	if _, ok := tree.Get("ui"); !ok {
		t.Fatal("中间节点 ui 必须可读（配置编辑器要按层展开）")
	}
	if _, ok := tree.Get("ui.missing"); ok {
		t.Fatal("不存在的路径必须回 ok=false，不能回零值")
	}
	if _, ok := tree.Get("ui.theme.deeper"); ok {
		t.Fatal("穿过标量继续往下必须回 ok=false")
	}

	// 快照是深拷贝：改快照不影响树（否则 patch 的原子性无从谈起）。
	snap := tree.Snapshot()
	snap["ui"].(map[string]any)["theme"] = "light"
	if got, _ := tree.Get("ui.theme"); got != "dark" {
		t.Fatalf("Snapshot 泄露了内部 map：树里的 ui.theme 变成 %#v", got)
	}
}

// 扁平投影必须还能被旧协议认：`/api/config.get` 的 values 是 map[string]string。
func TestConfigTreeFlattenKeepsFlatProtocol(t *testing.T) {
	tree, err := NewTree(nestedValues())
	if err != nil {
		t.Fatal(err)
	}
	flat := tree.Flatten()

	want := map[string]string{
		"ui.theme":   "dark",
		"ui.zoom":    "1.25",
		"ui.compact": "true",
		"lang":       "zh",
		"models":     `[{"id":"a"},{"id":"b"}]`,
	}
	for key, value := range want {
		if got, ok := flat[key]; !ok || got != value {
			t.Fatalf("Flatten()[%q] = %q（ok=%v），want %q；整表=%v", key, got, ok, value, flat)
		}
	}
	if len(flat) != len(want) {
		t.Fatalf("扁平表多出条目：%v", flat)
	}

	// 反过来：旧客户端写回来的扁平表要能长回树里（迁移路径，不是两套语义）。
	rebuilt, err := TreeFromFlat(flat)
	if err != nil {
		t.Fatalf("从扁平表重建失败: %v", err)
	}
	if got, _ := rebuilt.Get("ui.theme"); got != "dark" {
		t.Fatalf("重建后 ui.theme = %#v", got)
	}
}

func TestConfigTreeRejectsInvalidValues(t *testing.T) {
	cases := []struct {
		name   string
		values map[string]any
	}{
		{"键里带点", map[string]any{"a.b": 1}},
		{"空键", map[string]any{"": 1}},
		{"不支持的Go类型", map[string]any{"f": func() {}}},
		{"NaN", map[string]any{"n": math.NaN()}},
		{"嵌套层数超限", deepNest(64)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewTree(tc.values); err == nil {
				t.Fatalf("%s 必须被拒绝（否则同一份配置有多种拼法/无法序列化）", tc.name)
			}
		})
	}
}

func deepNest(depth int) map[string]any {
	root := map[string]any{"leaf": 1}
	for i := 0; i < depth; i++ {
		root = map[string]any{"a": root}
	}
	return root
}

// 同一棵树换个 map 插入顺序，序列化字节必须完全一致——快照/digest 全靠这个。
func TestConfigTreeCanonicalJSONIsStable(t *testing.T) {
	first, err := NewTree(map[string]any{"b": 2, "a": map[string]any{"y": true, "x": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewTree(map[string]any{"a": map[string]any{"x": "1", "y": true}, "b": 2})
	if err != nil {
		t.Fatal(err)
	}
	left, err := first.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	right, err := second.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(left) != string(right) {
		t.Fatalf("同一份树的规范字节不同：\n%s\n%s", left, right)
	}
	if !strings.HasPrefix(string(left), `{"a":{`) {
		t.Fatalf("规范序必须是键升序：%s", left)
	}
}

// 红 B 的绿侧前半：patch 全有或全无。
func TestConfigPatchAppliesAllOrNothing(t *testing.T) {
	base, err := NewTree(map[string]any{
		"ui":   map[string]any{"theme": "dark", "zoom": 1},
		"lang": "zh",
	})
	if err != nil {
		t.Fatal(err)
	}
	patch := Patch{
		ExpectedRevision: 3,
		Ops: []PatchOp{
			{Op: OpSet, Path: "ui.theme", Value: "light"},
			{Op: OpMerge, Path: "ui", Value: map[string]any{"zoom": 2, "density": "compact"}},
			{Op: OpUnset, Path: "lang"},
			{Op: OpSet, Path: "models.active", Value: "a"},
		},
	}
	next, err := ApplyPatch(base, patch)
	if err != nil {
		t.Fatalf("合法 patch 失败: %v", err)
	}
	if got, _ := next.Get("ui.theme"); got != "light" {
		t.Fatalf("ui.theme = %#v", got)
	}
	if got, _ := next.Get("ui.zoom"); got == nil {
		t.Fatal("merge 丢了 ui.zoom")
	} else if number, ok := got.(float64); !ok || number != 2 {
		t.Fatalf("merge 后 ui.zoom = %#v，want 2", got)
	}
	if got, _ := next.Get("ui.density"); got != "compact" {
		t.Fatalf("merge 没写进 ui.density：%#v", got)
	}
	if _, ok := next.Get("lang"); ok {
		t.Fatal("unset 之后 lang 还在")
	}
	if got, _ := next.Get("models.active"); got != "a" {
		t.Fatalf("不存在的中间节点必须自动建：models.active = %#v", got)
	}

	// base 一个字节都不许动。
	if got, _ := base.Get("ui.theme"); got != "dark" {
		t.Fatalf("ApplyPatch 改到了基准树：ui.theme = %#v", got)
	}
	if _, ok := base.Get("lang"); !ok {
		t.Fatal("ApplyPatch 把基准树的 lang 删了")
	}
	if _, ok := base.Get("models"); ok {
		t.Fatal("ApplyPatch 往基准树里加了 models")
	}
}

func TestConfigPatchRejectsBadOps(t *testing.T) {
	base, err := NewTree(map[string]any{"ui": map[string]any{"theme": "dark", "zoom": 1}, "lang": "zh"})
	if err != nil {
		t.Fatal(err)
	}
	baseJSON, err := base.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		p    Patch
	}{
		{"未知 op", Patch{Ops: []PatchOp{{Op: "replace", Path: "lang", Value: "en"}}}},
		{"空路径", Patch{Ops: []PatchOp{{Op: OpSet, Path: "", Value: 1}}}},
		{"路径段为空", Patch{Ops: []PatchOp{{Op: OpSet, Path: "a..b", Value: 1}}}},
		{"unset 不存在的路径", Patch{Ops: []PatchOp{{Op: OpUnset, Path: "nope.deeper"}}}},
		{"merge 到标量上", Patch{Ops: []PatchOp{{Op: OpMerge, Path: "lang", Value: map[string]any{"x": 1}}}}},
		{"set 的值不可序列化", Patch{Ops: []PatchOp{{Op: OpSet, Path: "lang", Value: func() {}}}}},
		{"把对象写进标量路径", Patch{Ops: []PatchOp{{Op: OpSet, Path: "lang.x", Value: 1}}}},
		{"一次太多 op", Patch{Ops: makeOps(257)}},
		{"完全没有 op", Patch{Ops: nil}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ApplyPatch(base, tc.p); err == nil {
				t.Fatalf("%s 必须被拒绝（静默成功会让调用方以为改生效了）", tc.name)
			}
			got, err := base.CanonicalJSON()
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(baseJSON) {
				t.Fatalf("%s 的失败 patch 改到了基准树：\n%s\n%s", tc.name, got, baseJSON)
			}
		})
	}
}

func makeOps(n int) []PatchOp {
	out := make([]PatchOp, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, PatchOp{Op: OpSet, Path: "k" + strings.Repeat("x", i%4), Value: i})
	}
	return out
}

// 落盘：revision 单调、重开保留、格式带 schema。
func TestConfigStorePersistsRevision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "values.json")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Revision(); got != 0 {
		t.Fatalf("新库 revision = %d，want 0", got)
	}
	if _, ok := store.Tree().Get("ui.theme"); ok {
		t.Fatal("新库必须是空树")
	}

	result, err := store.Apply(Patch{
		ExpectedRevision: 0,
		Ops:              []PatchOp{{Op: OpSet, Path: "ui.theme", Value: "dark"}, {Op: OpSet, Path: "ui.zoom", Value: 1.25}},
	})
	if err != nil {
		t.Fatalf("首次 apply 失败: %v", err)
	}
	if result.Revision != 1 || result.Applied != 2 {
		t.Fatalf("结果 = %+v，want revision=1 applied=2", result)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("落盘文件读不到: %v", err)
	}
	var onDisk struct {
		Schema   string         `json:"schema"`
		Revision int64          `json:"revision"`
		Values   map[string]any `json:"values"`
	}
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("落盘文件不是合法 JSON: %v\n%s", err, raw)
	}
	if onDisk.Schema != FileSchema {
		t.Fatalf("schema = %q，want %q", onDisk.Schema, FileSchema)
	}
	if onDisk.Revision != 1 {
		t.Fatalf("落盘 revision = %d，want 1", onDisk.Revision)
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("重开失败: %v", err)
	}
	if got := reopened.Revision(); got != 1 {
		t.Fatalf("重开 revision = %d，want 1", got)
	}
	if got, _ := reopened.Tree().Get("ui.zoom"); got == nil {
		t.Fatal("重开后 ui.zoom 丢了")
	}
}

// 原子写：写失败时旧文件一个字节不变，且内存 revision 不前进。
func TestConfigStoreAtomicWriteKeepsOldFileOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "values.json")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Apply(Patch{Ops: []PatchOp{{Op: OpSet, Path: "lang", Value: "zh"}}}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	boom := errors.New("磁盘满了")
	store.writeFile = func(string, []byte) error { return boom }

	if _, err := store.Apply(Patch{ExpectedRevision: 1, Ops: []PatchOp{{Op: OpSet, Path: "lang", Value: "en"}}}); !errors.Is(err, boom) {
		t.Fatalf("写入失败没有被原样返回：%v", err)
	}
	if got := store.Revision(); got != 1 {
		t.Fatalf("写失败后内存 revision = %d，want 1（前进就等于谎报已落盘）", got)
	}
	if got, _ := store.Tree().Get("lang"); got != "zh" {
		t.Fatalf("写失败后内存树被改了：lang = %#v", got)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("写失败后磁盘文件变了：\n%s\n%s", before, after)
	}
	// 没有留下写到一半的临时文件。
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("目录里残留了临时文件：%v", names)
	}
}

// 红 B 的绿侧后半：过期 revision 必须冲突，且冲突时**不写盘**。
func TestConfigStoreRejectsStaleRevision(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "values.json")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Apply(Patch{ExpectedRevision: 0, Ops: []PatchOp{{Op: OpSet, Path: "lang", Value: "zh"}}}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	_, err = store.Apply(Patch{ExpectedRevision: 0, Ops: []PatchOp{{Op: OpSet, Path: "lang", Value: "en"}}})
	if err == nil {
		t.Fatal("过期 revision 必须冲突，不能静默覆盖别人的修改")
	}
	var conflict *RevisionConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("错误类型 = %T，want *RevisionConflict：%v", err, err)
	}
	if conflict.Expected != 0 || conflict.Actual != 1 {
		t.Fatalf("冲突细节 = %+v，want expected=0 actual=1", conflict)
	}
	if !strings.Contains(err.Error(), "revision") {
		t.Fatalf("错误消息要能让人知道是 revision 冲突：%v", err)
	}
	if got, _ := store.Tree().Get("lang"); got != "zh" {
		t.Fatalf("冲突时树被改了：lang = %#v", got)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("冲突时磁盘被写了：\n%s\n%s", before, after)
	}
}

// 并发写者：读-改-写重试的人必须全部落盘，谁也不许静默丢更新。
func TestConfigStoreConcurrentWritersDoNotLoseUpdates(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "values.json"))
	if err != nil {
		t.Fatal(err)
	}
	const writers = 8
	var wg sync.WaitGroup
	errCh := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			key := "w" + string(rune('a'+index))
			for attempt := 0; attempt < 200; attempt++ {
				revision := store.Revision()
				_, err := store.Apply(Patch{
					ExpectedRevision: revision,
					Ops:              []PatchOp{{Op: OpSet, Path: key, Value: index}},
				})
				if err == nil {
					return
				}
				var conflict *RevisionConflict
				if !errors.As(err, &conflict) {
					errCh <- err
					return
				}
			}
			errCh <- errors.New("200 次重试都没抢到 revision")
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("并发写失败: %v", err)
	}

	tree := store.Tree()
	for i := 0; i < writers; i++ {
		key := "w" + string(rune('a'+i))
		if _, ok := tree.Get(key); !ok {
			t.Fatalf("丢了 %s 的更新（整树=%v）", key, tree.Snapshot())
		}
	}
	if got := store.Revision(); got != writers {
		t.Fatalf("revision = %d，want %d（每次成功写入 +1）", got, writers)
	}
}

func TestConfigStoreRejectsCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "values.json")
	if err := os.WriteFile(path, []byte("{不是 JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(path); err == nil {
		t.Fatal("坏文件必须报错，不能当成空配置继续跑（那会让下一次写把用户配置清掉）")
	}

	if err := os.WriteFile(path, []byte(`{"schema":"其他 schema","revision":1,"values":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(path); err == nil {
		t.Fatal("schema 不认识的配置必须拒绝")
	}
}
