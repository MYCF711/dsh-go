package ptc

import (
	"fmt"
	"regexp"
)

// portableIdentifier 是**语言可移植**的标识符子集：`[A-Za-z_][A-Za-z0-9_]*`。
//
// 故意比 ECMAScript 窄：JS 允许 `$`，Python 不允许。缝的承诺是"一份 bindings
// 名单在所有后端上都有效"，所以 `$tools` 在任何后端都必须被拒——不是只有
// Python 后端才拒它。
var portableIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// PortableReservedWords 是**每个**可移植目标语言的保留字并集（ECMAScript ∪ Python）。
//
// 官方 `src/index.ts:79-90`，逐字照抄、一个字都别改：改成"顺眼"的写法会让某个
// 名字在一个后端过、在另一个后端炸，而缝的承诺正是"这份名单在所有后端都有效"。
//
// 新增一门语言意味着扩这个并集——那是对既有 binding 名的破坏性评审，按设计如此。
var PortableReservedWords = map[string]bool{
	// ECMAScript 保留字与严格模式保留名。
	"await": true, "break": true, "case": true, "catch": true, "class": true, "const": true,
	"continue": true, "debugger": true, "default": true, "delete": true, "do": true,
	"else": true, "enum": true, "export": true, "extends": true, "false": true, "finally": true,
	"for": true, "function": true, "if": true, "import": true, "in": true,
	"instanceof": true, "new": true, "null": true, "return": true, "super": true, "switch": true,
	"this": true, "throw": true, "true": true, "try": true, "typeof": true,
	"var": true, "void": true, "while": true, "with": true, "yield": true, "let": true,
	"static": true, "implements": true, "interface": true, "package": true,
	"private": true, "protected": true, "public": true, "arguments": true, "eval": true,
	// Python 3.x 关键字，以及已在上表之外的软关键字
	// （`type` 与 `_` 实际合法，为安全起见一并保留）。
	"False": true, "None": true, "True": true, "and": true, "as": true, "assert": true,
	"async": true, "def": true, "del": true, "elif": true, "except": true, "from": true,
	"global": true, "is": true, "lambda": true, "nonlocal": true, "not": true, "or": true,
	"pass": true, "raise": true, "match": true, "type": true, "_": true,
}

// ReservedBindingGlobals 是**每个**后端都会拒绝的全局名，因为**某个**后端占用了
// 程序命名空间里的这个槽位。
//
// 官方 `src/index.ts:43-46`：
//
//	console        Node 的日志捕获
//	__dsh_main__   Python 后端的引导包装
//	__builtins__   Python 预置模块全局
//	__name__       同上
//	__debug__      CPython 把裸 `__debug__` 编译成常量 True 且在**编译期**拒绝赋值，
//	               所以注入同名全局在 Python 侧永远取不到——这与"名字合法"无关，
//	               是"注进去也用不上"，正是共享集合要挡的那一类。
//
// 注意它与 `ReservedErrorMembers` 的差别：错误成员**整类**拒绝 dunder 形态，
// binding 全局只拒这里列出的具体名字。
var ReservedBindingGlobals = map[string]bool{
	"console":      true,
	"__dsh_main__": true, "__builtins__": true, "__name__": true, "__debug__": true,
}

// ReservedErrorMembers 是错误类 `MemberNameProperty` **每个**后端都拒绝的名字。
//
// 官方 `src/index.ts:58-61`：JS `Error` 的 name/message/stack，以及 Python 异常协议
// 的 args/with_traceback/add_note。其余任何非空自有属性名在所有后端都接受。
var ReservedErrorMembers = map[string]bool{
	"name": true, "message": true, "stack": true,
	"args": true, "with_traceback": true, "add_note": true,
}

// DunderMember 匹配 dunder 形态（`__x__`，中间非空）。
//
// 官方 `src/index.ts:67`：这类名字在 Python 里是对象协议槽，且具体集合随解释器
// 版本变（有几个是 setattr 会抛的受限 descriptor），所以整类拒绝而不是逐个列举。
var DunderMember = regexp.MustCompile(`^__.+__$`)

// ValidateGlobalName 校验一个程序可见的全局名（命名空间 global 或错误类名）。
//
// 三条一起过：语法子集 → 保留字 → 后端占用槽位。错误里说清是哪一条，
// 因为调用方（模型或上层）要据此改名，含糊的"名字不合法"帮不上忙。
func ValidateGlobalName(name string) error {
	if !portableIdentifier.MatchString(name) {
		return fmt.Errorf("%w：全局名 %q 不符合语言可移植标识符 `[A-Za-z_][A-Za-z0-9_]*`（JS 专有的 `$` 也不行）", ErrMisuse, name)
	}
	if PortableReservedWords[name] {
		return fmt.Errorf("%w：全局名 %q 是某个可移植目标语言的保留字", ErrMisuse, name)
	}
	if ReservedBindingGlobals[name] {
		return fmt.Errorf("%w：全局名 %q 被某个后端占用（见 ReservedBindingGlobals）", ErrMisuse, name)
	}
	return nil
}

// ValidateErrorClass 校验一个命名空间的有类型拒绝契约。
func ValidateErrorClass(ec BindingErrorClass) error {
	if err := ValidateGlobalName(ec.Name); err != nil {
		return fmt.Errorf("错误类名：%w", err)
	}
	member := ec.MemberNameProperty
	if member == "" {
		return fmt.Errorf("%w：错误类 %q 的 memberNameProperty 不能为空", ErrMisuse, ec.Name)
	}
	if ReservedErrorMembers[member] {
		return fmt.Errorf("%w：错误成员名 %q 被每个后端保留（见 ReservedErrorMembers）", ErrMisuse, member)
	}
	if DunderMember.MatchString(member) {
		return fmt.Errorf("%w：错误成员名 %q 是 dunder 形态，整类拒绝", ErrMisuse, member)
	}
	return nil
}

// ValidateBindings 校验一整份 bindings 名单。
//
// 除了逐项校验，还拒**重复的 global**：两个命名空间抢同一个全局名时，
// 程序里只会看到后一个，前一个的函数静默消失——那是最难查的一类"我明明注入了"。
func ValidateBindings(namespaces []BindingNamespace) error {
	seen := make(map[string]bool, len(namespaces))
	for i, ns := range namespaces {
		if err := ValidateGlobalName(ns.Global); err != nil {
			return fmt.Errorf("bindings[%d]：%w", i, err)
		}
		if seen[ns.Global] {
			return fmt.Errorf("%w：bindings[%d] 的全局名 %q 重复", ErrMisuse, i, ns.Global)
		}
		seen[ns.Global] = true
		if ns.ErrorClass != nil {
			if err := ValidateErrorClass(*ns.ErrorClass); err != nil {
				return fmt.Errorf("bindings[%d]：%w", i, err)
			}
		}
	}
	return nil
}
