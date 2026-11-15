package main

// R3 Hint 守卫的自测：**弄坏必须红**。
//
// 为什么需要它：`TestEveryToolErrorHasHint` 是扫源码的启发式守卫，放宽形状识别时
// 很容易顺手放宽到"什么都算有 Hint"，那这条守卫就退化成永远绿。
// 这里用合成源码把四种形状各钉一遍——两种必须过、两种必须被拦——
// 守卫逻辑改坏时这条先红，而不是等到某天真的漏了 Hint 才有人发现。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestR3HintGuardSelfTest(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantMiss bool
		why      string
	}{
		{
			name: "直接字面量",
			body: `return spi.Result{Hint: "补参数"}, err`,
		},
		{
			name: "链式补错误身份",
			body: `return spi.Result{Hint: "补参数"}.WithErrorInfo(name, code), err`,
		},
		{
			name: "helper 包一层再返回",
			body: `return withJobErrorInfo(spi.Result{Hint: "补参数"}, err), err`,
		},
		{
			name:     "先赋 .Hint 再返回变量",
			body:     "var res spi.Result\nres.Hint = \"补参数\"\nreturn res, err",
			wantMiss: false,
		},
		{
			name:     "没有 Hint 直接返回变量（必须被拦）",
			body:     "var res spi.Result\nres.Data = 1\nreturn res, err",
			wantMiss: true,
			why:      "这条是守卫存在的理由：结果里没 Hint，模型下一句没有可执行的话",
		},
		{
			name:     "返回裸 error（必须被拦）",
			body:     `return spi.Result{}, err`,
			wantMiss: true,
			why:      "空 Result 等于丢了 Hint（R3 原文）",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := "package p\n\nimport \"x/spi\"\n\nfunc f() (spi.Result, error) {\n" + c.body + "\n}\n"
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, "synthetic.go", src, 0)
			if err != nil {
				t.Fatalf("合成源码解析失败：%v\n%s", err, src)
			}
			var got []string
			ast.Inspect(f, func(n ast.Node) bool {
				if fd, ok := n.(*ast.FuncDecl); ok && fd.Body != nil {
					got = append(got, scanFunc(fset, fd.Body)...)
				}
				return true
			})
			if c.wantMiss && len(got) == 0 {
				t.Fatalf("守卫漏报：这段代码不该通过（%s）", c.why)
			}
			if !c.wantMiss && len(got) > 0 {
				t.Fatalf("守卫误报：这段代码是合规的，却被判成 %s", strings.Join(got, " "))
			}
		})
	}
}
