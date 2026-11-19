package ptc

import (
	"strings"
	"testing"
)

// officialReservedWords 是官方 `@deepseek-ai/dsh-ptc-runtime/src/index.ts:79-90`
// 的**逐字**清单（ECMAScript ∪ Python）。
//
// 把它抄进测试是故意的：这条集合是"一份 bindings 名单在所有后端都有效"这个
// 承诺的全部内容，任何增删都必须是对既有 binding 名的破坏性评审。
// 抄一份死清单，才挡得住"顺手加个名字"。
const officialReservedWords = `
await break case catch class const continue debugger default delete do
else enum export extends false finally for function if import in
instanceof new null return super switch this throw true try typeof
var void while with yield let static implements interface package
private protected public arguments eval
False None True and as assert async def del elif except from
global is lambda nonlocal not or pass raise match type _
`

func splitWords(raw string) []string {
	return strings.Fields(raw)
}

func TestPortableReservedWordsMatchOfficial(t *testing.T) {
	want := splitWords(officialReservedWords)
	wantSet := map[string]bool{}
	for _, w := range want {
		if wantSet[w] {
			t.Fatalf("测试自己的官方清单里重复了 %q", w)
		}
		wantSet[w] = true
	}
	for _, w := range want {
		if !PortableReservedWords[w] {
			t.Errorf("缺少官方保留字 %q —— 缺一个就会让某份 binding 名单在一个后端过、在另一个后端炸", w)
		}
	}
	for w := range PortableReservedWords {
		if !wantSet[w] {
			t.Errorf("多出官方没有的保留字 %q —— 加名字是破坏性变更，必须同步官方", w)
		}
	}
	if len(PortableReservedWords) != len(want) {
		t.Fatalf("保留字数量 %d，官方是 %d", len(PortableReservedWords), len(want))
	}
}

func TestReservedBindingGlobalsMatchOfficial(t *testing.T) {
	want := []string{"console", "__dsh_main__", "__builtins__", "__name__", "__debug__"}
	for _, name := range want {
		if !ReservedBindingGlobals[name] {
			t.Errorf("缺少后端占用槽位 %q", name)
		}
	}
	if len(ReservedBindingGlobals) != len(want) {
		t.Errorf("ReservedBindingGlobals 有 %d 项，官方是 %d 项", len(ReservedBindingGlobals), len(want))
	}
}

func TestReservedErrorMembersMatchOfficial(t *testing.T) {
	want := []string{"name", "message", "stack", "args", "with_traceback", "add_note"}
	for _, name := range want {
		if !ReservedErrorMembers[name] {
			t.Errorf("缺少错误成员保留名 %q", name)
		}
	}
	if len(ReservedErrorMembers) != len(want) {
		t.Errorf("ReservedErrorMembers 有 %d 项，官方是 %d 项", len(ReservedErrorMembers), len(want))
	}
}

func TestDunderMember(t *testing.T) {
	for _, name := range []string{"__x__", "__dict__", "__setattr__"} {
		if !DunderMember.MatchString(name) {
			t.Errorf("%q 应该被判成 dunder 形态", name)
		}
	}
	for _, name := range []string{"_x", "x__", "__", "a__b", "____"} {
		if DunderMember.MatchString(name) {
			t.Errorf("%q 不该被判成 dunder 形态（中间必须非空）", name)
		}
	}
}

func TestValidateGlobalName(t *testing.T) {
	cases := []struct {
		name    string
		wantErr bool
		why     string
	}{
		{"tools", false, "普通标识符"},
		{"_x1", false, "下划线开头合法"},
		{"myTools", false, "驼峰合法"},
		{"$tools", true, "JS 专有的 $ 不属于可移植子集"},
		{"1tools", true, "不能数字开头"},
		{"tools-x", true, "连字符不是标识符字符"},
		{"", true, "空名"},
		{"lambda", true, "Python 保留字"},
		{"await", true, "ECMAScript 保留字"},
		{"console", true, "Node 后端占用槽位"},
		{"__dsh_main__", true, "Python 后端占用槽位"},
		{"__name__", true, "Python 预置全局"},
	}
	for _, tc := range cases {
		err := ValidateGlobalName(tc.name)
		if tc.wantErr && err == nil {
			t.Errorf("ValidateGlobalName(%q) 应该报错（%s）", tc.name, tc.why)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("ValidateGlobalName(%q) 不该报错：%v", tc.name, err)
		}
		if tc.wantErr && err != nil && !IsMisuse(err) {
			t.Errorf("ValidateGlobalName(%q) 的错误必须是误用身份：%v", tc.name, err)
		}
	}
}

func TestValidateErrorClass(t *testing.T) {
	ok := BindingErrorClass{Name: "ToolCallError", MemberNameProperty: "toolName"}
	if err := ValidateErrorClass(ok); err != nil {
		t.Fatalf("合法错误类被判错：%v", err)
	}
	bad := []struct {
		ec  BindingErrorClass
		why string
	}{
		{BindingErrorClass{Name: "ToolCallError", MemberNameProperty: ""}, "空成员属性名"},
		{BindingErrorClass{Name: "ToolCallError", MemberNameProperty: "name"}, "Error.name 保留"},
		{BindingErrorClass{Name: "ToolCallError", MemberNameProperty: "stack"}, "Error.stack 保留"},
		{BindingErrorClass{Name: "ToolCallError", MemberNameProperty: "args"}, "Python 异常协议保留"},
		{BindingErrorClass{Name: "ToolCallError", MemberNameProperty: "__dict__"}, "dunder 整类拒绝"},
		{BindingErrorClass{Name: "lambda", MemberNameProperty: "toolName"}, "类名撞保留字"},
		{BindingErrorClass{Name: "console", MemberNameProperty: "toolName"}, "类名撞后端槽位"},
	}
	for _, tc := range bad {
		if err := ValidateErrorClass(tc.ec); err == nil {
			t.Errorf("错误类 %+v 应该被拒（%s）", tc.ec, tc.why)
		}
	}
}

func TestValidateBindingsDuplicateGlobalIsMisuse(t *testing.T) {
	err := ValidateBindings([]BindingNamespace{
		{Global: "tools", Functions: map[string]BindingFunction{}},
		{Global: "tools", Functions: map[string]BindingFunction{}},
	})
	if err == nil {
		t.Fatal("两个命名空间抢同一个全局名必须被拒：程序里只会看到后一个，前一个的函数静默消失")
	}
	if !IsMisuse(err) {
		t.Fatalf("重复全局名是误用身份：%v", err)
	}
}

func TestValidateBindingsRejectsNestedErrorClass(t *testing.T) {
	err := ValidateBindings([]BindingNamespace{{
		Global:     "tools",
		Functions:  map[string]BindingFunction{},
		ErrorClass: &BindingErrorClass{Name: "lambda", MemberNameProperty: "toolName"},
	}})
	if err == nil {
		t.Fatal("命名空间里非法的错误类必须被拒")
	}
}
