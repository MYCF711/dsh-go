package transport

// 先红（HS-14）：基线的方法面是**编译期常量 + 静态快照**
// （internal/transport/api.go 的 SessionMethods() 与 testdata/api.snapshot.json）。
// 后果有三个，本文件逐条钉住：
//
//  1. 「运行时注册」：没有地方能在进程跑起来之后新增一个方法描述符；
//  2. 「按描述符校验参数」：handleAPI 只按方法名分派，参数合不合法由每个 handler
//     各写一遍（于是同一个错法在 40 个 handler 里有 40 种行为）；
//  3. 「挂载包的描述注入」：没有 typert-loader 那一层，包声明的描述进不来。
//
// 这里实现的是**描述符层**（typert）：注册、冲突检测、按描述符校验参数、稳定快照。
// 它不改变既有 handler 的任何行为——Lead 决定哪些方法接校验（见 $S/WIRING.md）。
// receipt：canonical 25 一个都不许被本层改名或重排，所以有一个用例专门对表。

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

func demoDescriptor() Descriptor {
	return Descriptor{
		Method:  Method("demo.echo"),
		Summary: "回显一段文本（测试用描述符）",
		Source:  "test",
		Params: []ParamSpec{
			{Name: "text", Kind: ParamString, Required: true, MinLength: 1, MaxLength: 64},
			{Name: "upper", Kind: ParamBool},
			{Name: "mode", Kind: ParamString, Enum: []string{"plain", "json"}},
		},
	}
}

// 红 A 的绿侧：进程跑起来之后还能注册，而且立刻对后续请求生效。
func TestDescriptorRegisterAtRuntime(t *testing.T) {
	registry := NewRegistry()
	if registry.Len() != 0 {
		t.Fatalf("新注册表必须是空的：%d", registry.Len())
	}

	if err := registry.Register(demoDescriptor()); err != nil {
		t.Fatalf("运行时注册失败: %v", err)
	}
	if got := registry.Len(); got != 1 {
		t.Fatalf("Register 之后 Len() = %d，want 1", got)
	}
	got, ok := registry.Lookup("demo.echo")
	if !ok {
		t.Fatal("注册完立刻查不到")
	}
	if got.Revision != 1 {
		t.Fatalf("注册代数 = %d，want 1（每次注册 +1，客户端靠它判断描述变了没）", got.Revision)
	}
	if got.Method != Method("demo.echo") || len(got.Params) != 3 {
		t.Fatalf("取回的描述被改了：%+v", got)
	}

	// 注册之后立刻可用——不是"下次启动才生效"。
	if err := registry.ValidateParams("demo.echo", map[string]any{"text": "hi"}); err != nil {
		t.Fatalf("合法参数被拒: %v", err)
	}
	if err := registry.ValidateParams("demo.echo", map[string]any{}); err == nil {
		t.Fatal("缺必填参数必须被拒")
	}

	// 重名必须报错：静默覆盖会让两个模块抢同一个方法名而不自知。
	if err := registry.Register(demoDescriptor()); err == nil {
		t.Fatal("重复注册必须报错")
	}
	if registry.Len() != 1 {
		t.Fatalf("重复注册改动了表大小：%d", registry.Len())
	}

	// 没有描述符的方法：查不到（调用方据此决定"不校验"还是"拒绝"）。
	if _, ok := registry.Lookup("demo.nope"); ok {
		t.Fatal("未注册的方法不该被查到")
	}
}

func TestDescriptorRejectsInvalidSpec(t *testing.T) {
	cases := []struct {
		name string
		d    Descriptor
	}{
		{"空方法名", Descriptor{Params: nil}},
		{"方法名带空白", Descriptor{Method: Method(" demo.echo ")}},
		{"未知参数类型", Descriptor{Method: Method("demo.a"), Params: []ParamSpec{{Name: "x", Kind: ParamKind("float128")}}}},
		{"参数名为空", Descriptor{Method: Method("demo.b"), Params: []ParamSpec{{Name: "", Kind: ParamString}}}},
		{"参数名重复", Descriptor{Method: Method("demo.c"), Params: []ParamSpec{{Name: "x", Kind: ParamString}, {Name: "x", Kind: ParamString}}}},
		{"必填参数排在可选之后", Descriptor{Method: Method("demo.d"), Params: []ParamSpec{{Name: "a", Kind: ParamString}, {Name: "b", Kind: ParamString, Required: true}}}},
		{"enum 用在非字符串上", Descriptor{Method: Method("demo.e"), Params: []ParamSpec{{Name: "a", Kind: ParamBool, Enum: []string{"x"}}}}},
		{"min 大于 max", Descriptor{Method: Method("demo.f"), Params: []ParamSpec{{Name: "a", Kind: ParamString, MinLength: 8, MaxLength: 2}}}},
		{"数组缺 items", Descriptor{Method: Method("demo.g"), Params: []ParamSpec{{Name: "a", Kind: ParamArray}}}},
		{"items 不是元素类型", Descriptor{Method: Method("demo.h"), Params: []ParamSpec{{Name: "a", Kind: ParamString, Items: ParamString}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registry := NewRegistry()
			if err := registry.Register(tc.d); err == nil {
				t.Fatalf("%s 必须被拒绝（描述符是契约，坏描述符比没有描述符更糟）", tc.name)
			}
			if registry.Len() != 0 {
				t.Fatalf("%s 被拒绝后表里却有 %d 条", tc.name, registry.Len())
			}
		})
	}
}

// 红 B 的绿侧：参数校验按描述符走，错误里说清"哪个参数、错在哪、下一步做什么"。
func TestDescriptorValidatesParams(t *testing.T) {
	registry := NewRegistry()
	base := demoDescriptor()
	base.Params = append(base.Params, ParamSpec{Name: "list", Kind: ParamArray, Items: ParamInteger, MaxLength: 3})
	if err := registry.Register(base); err != nil {
		t.Fatal(err)
	}

	ok := []struct {
		name string
		body map[string]any
	}{
		{"只给必填", map[string]any{"text": "hi"}},
		{"全给", map[string]any{"text": "hi", "upper": true, "mode": "json", "list": []any{1, 2.0}}},
		{"整数写成浮点形式", map[string]any{"text": "hi", "list": []any{1.0, 2.0}}},
	}
	for _, tc := range ok {
		t.Run("ok/"+tc.name, func(t *testing.T) {
			if err := registry.ValidateParams("demo.echo", tc.body); err != nil {
				t.Fatalf("合法参数被拒: %v", err)
			}
		})
	}

	bad := []struct {
		name      string
		body      map[string]any
		wantCode  string
		wantParam string
	}{
		{"缺必填", map[string]any{"upper": true}, "missing", "text"},
		{"类型不对", map[string]any{"text": 42}, "type", "text"},
		{"bool 给字符串", map[string]any{"text": "hi", "upper": "yes"}, "type", "upper"},
		{"整数给了小数", map[string]any{"text": "hi", "list": []any{1.5}}, "items", "list"},
		{"enum 之外的值", map[string]any{"text": "hi", "mode": "yaml"}, "enum", "mode"},
		{"未知参数", map[string]any{"text": "hi", "typo": 1}, "unknown", "typo"},
		{"空串违反 minLength", map[string]any{"text": ""}, "length", "text"},
		{"超长", map[string]any{"text": strings.Repeat("x", 65)}, "length", "text"},
		{"数组元素类型不对", map[string]any{"text": "hi", "list": []any{"a"}}, "items", "list"},
		{"数组超长", map[string]any{"text": "hi", "list": []any{1, 2, 3, 4}}, "length", "list"},
	}
	for _, tc := range bad {
		t.Run("bad/"+tc.name, func(t *testing.T) {
			err := registry.ValidateParams("demo.echo", tc.body)
			if err == nil {
				t.Fatalf("%s 必须被拒", tc.name)
			}
			var paramErr *ParamError
			if !errors.As(err, &paramErr) {
				t.Fatalf("错误类型 = %T，want *ParamError：%v", err, err)
			}
			if paramErr.Code != tc.wantCode || paramErr.Param != tc.wantParam {
				t.Fatalf("code/param = %q/%q，want %q/%q（%v）", paramErr.Code, paramErr.Param, tc.wantCode, tc.wantParam, err)
			}
			if strings.TrimSpace(paramErr.Hint) == "" {
				t.Fatal("参数错误必须带下一步（本仓错误契约要求每个错误都有 hint）")
			}
		})
	}

	// 没有描述符的方法：校验器不许假装"校验过了"。
	err := registry.ValidateParams("demo.unregistered", map[string]any{"anything": 1})
	var paramErr *ParamError
	if !errors.As(err, &paramErr) || paramErr.Code != "unregistered" {
		t.Fatalf("未注册方法的错误 = %v，want code=unregistered", err)
	}
}

// Decode 是 handler 的入口：坏 JSON 与坏参数都必须是 400 级错误。
func TestDescriptorDecodeBindsJSON(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(demoDescriptor()); err != nil {
		t.Fatal(err)
	}
	body, err := registry.Decode("demo.echo", []byte(`{"text":"hi","upper":true}`))
	if err != nil {
		t.Fatalf("合法 JSON 失败: %v", err)
	}
	if body["text"] != "hi" {
		t.Fatalf("body = %v", body)
	}
	if _, err := registry.Decode("demo.echo", []byte(`{"text":`)); err == nil {
		t.Fatal("坏 JSON 必须报错")
	}
	if _, err := registry.Decode("demo.echo", nil); err == nil {
		t.Fatal("空 body 缺必填必须报错")
	}
	if _, err := registry.Decode("demo.echo", []byte(`{"text":"hi","extra":1}`)); err == nil {
		t.Fatal("未知参数必须报错")
	}
}

// canonical 25 一个都不许被这一层改动/重排。
func TestDescriptorCanonicalCoversSessionMethods(t *testing.T) {
	descriptors := CanonicalDescriptors()
	if len(descriptors) != len(SessionMethods()) {
		t.Fatalf("描述符 %d 条 vs canonical %d 条", len(descriptors), len(SessionMethods()))
	}
	want := make(map[Method]bool, len(SessionMethods()))
	for _, m := range SessionMethods() {
		want[m] = true
	}
	seen := make(map[Method]bool, len(descriptors))
	for _, d := range descriptors {
		if !want[d.Method] {
			t.Fatalf("描述符里有 canonical 之外的方法 %q（additive 要单独一组）", d.Method)
		}
		if seen[d.Method] {
			t.Fatalf("描述符重复：%q", d.Method)
		}
		seen[d.Method] = true
		if len(d.Summary) == 0 {
			t.Fatalf("%q 缺 summary：快照是给 Lead 审查用的，空 summary 等于没描述", d.Method)
		}
		for _, p := range d.Params {
			if p.Kind == "" {
				t.Fatalf("%q 的参数 %q 没有类型", d.Method, p.Name)
			}
		}
	}
	for _, m := range SessionMethods() {
		if !seen[m] {
			t.Fatalf("canonical 方法 %q 没有描述符", m)
		}
	}
	// 注册进注册表之后也必须全部通过校验（描述符自身合法）。
	registry := NewRegistry()
	if err := registry.RegisterAll(descriptors); err != nil {
		t.Fatalf("canonical 描述符自身不合法: %v", err)
	}
}

func TestDescriptorSnapshotIsStable(t *testing.T) {
	first, err := DescriptorSnapshot()
	if err != nil {
		t.Fatalf("快照失败: %v", err)
	}
	second, err := DescriptorSnapshot()
	if err != nil {
		t.Fatalf("快照失败: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("两次快照不一致（快照要能进 golden）：\n%s\n%s", first, second)
	}
	var doc struct {
		Schema  string       `json:"schema"`
		Methods []Descriptor `json:"methods"`
	}
	if err := json.Unmarshal(first, &doc); err != nil {
		t.Fatalf("快照不是合法 JSON: %v\n%s", err, first)
	}
	if doc.Schema != DescriptorSnapshotSchema {
		t.Fatalf("快照 schema = %q，want %q", doc.Schema, DescriptorSnapshotSchema)
	}
	if len(doc.Methods) < len(SessionMethods()) {
		t.Fatalf("快照只有 %d 个方法，canonical 就有 %d 个", len(doc.Methods), len(SessionMethods()))
	}

	// 注册表自己导出的 JSON 也必须稳定（同一顺序、同一字节）。
	registry := NewRegistry()
	if err := registry.RegisterAll(CanonicalDescriptors()); err != nil {
		t.Fatal(err)
	}
	left, err := registry.JSON()
	if err != nil {
		t.Fatal(err)
	}
	right, err := registry.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(left) != string(right) {
		t.Fatalf("注册表 JSON 不稳定：\n%s\n%s", left, right)
	}
}

// 挂载包的描述注入（typert-loader 形状）：一批要么全进、要么全不进。
func TestDescriptorPackageLoadIsAtomic(t *testing.T) {
	registry := NewRegistry()
	good := `{
	  "schema": "dsh.typert-package@1",
	  "name": "acme.demo",
	  "version": "1.0.0",
	  "methods": [
	    {"method": "acme.demo.echo", "summary": "回显", "params": [{"name": "text", "kind": "string", "required": true}]},
	    {"method": "acme.demo.ping", "summary": "探活", "params": []}
	  ]
	}`
	added, err := registry.RegisterPackage("acme.demo", strings.NewReader(good))
	if err != nil {
		t.Fatalf("包注入失败: %v", err)
	}
	if added != 2 || registry.Len() != 2 {
		t.Fatalf("注入 %d 条 / 表 %d 条，want 2/2", added, registry.Len())
	}
	if d, ok := registry.Lookup("acme.demo.echo"); !ok || d.Source != "package:acme.demo" {
		t.Fatalf("包来源没标上：%+v（ok=%v）", d, ok)
	}

	// 第二条不合法 → 一条都不许进。
	bad := `{
	  "schema": "dsh.typert-package@1",
	  "name": "acme.bad",
	  "methods": [
	    {"method": "acme.bad.ok", "summary": "合法", "params": []},
	    {"method": "acme.bad.broken", "summary": "坏", "params": [{"name": "x", "kind": "nonsense"}]}
	  ]
	}`
	if _, err := registry.RegisterPackage("acme.bad", strings.NewReader(bad)); err == nil {
		t.Fatal("坏描述符必须拒绝")
	}
	if registry.Len() != 2 {
		t.Fatalf("失败的注入进了半批：表里 %d 条", registry.Len())
	}
	if _, ok := registry.Lookup("acme.bad.ok"); ok {
		t.Fatal("失败的注入留下了 acme.bad.ok（半批可见）")
	}

	// schema / name 不对也必须拒绝。
	if _, err := registry.RegisterPackage("acme.demo", strings.NewReader(`{"schema":"x","name":"acme.demo","methods":[]}`)); err == nil {
		t.Fatal("schema 不认识必须拒绝")
	}
	if _, err := registry.RegisterPackage("acme.other", strings.NewReader(good)); err == nil {
		t.Fatal("包名与入参不一致必须拒绝")
	}
	// 空包是调用方的 bug，不该静默成功。
	if _, err := registry.RegisterPackage("acme.empty", strings.NewReader(`{"schema":"dsh.typert-package@1","name":"acme.empty","methods":[]}`)); err == nil {
		t.Fatal("空方法表必须报错")
	}
}

func TestDescriptorConcurrentRegistration(t *testing.T) {
	registry := NewRegistry()
	var wg sync.WaitGroup
	var mu sync.Mutex
	failures := 0
	const n = 32
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			d := Descriptor{Method: Method("conc." + string(rune('a'+index%26)) + "." + itoaSmall(index)), Summary: "并发", Source: "test"}
			if err := registry.Register(d); err != nil {
				mu.Lock()
				failures++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if failures != 0 || registry.Len() != n {
		t.Fatalf("并发注册：失败 %d 次，表 %d 条，want 0/%d", failures, registry.Len(), n)
	}
	// 注册代数必须是 1..n 的一个排列（没有两条抢到同一个号）。
	seen := map[int64]bool{}
	for _, d := range registry.List() {
		if seen[d.Revision] {
			t.Fatalf("注册代数重复：%d", d.Revision)
		}
		seen[d.Revision] = true
	}
}

func itoaSmall(v int) string {
	if v == 0 {
		return "0"
	}
	var digits []byte
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	return string(digits)
}
