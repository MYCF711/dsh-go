package main

// 参数单源守卫（t7）。
//
// # 它拦什么
//
// 同一个参数在多个入口各写一套默认值，是这一轮扫描里最集中的缺陷形态：
//
//   - `--workspace-bytes`：run/serve 的默认值来自 `defaultWorkspaceBytes`（65536），
//     ACP 却另写一个 `8000`——同一个参数两个默认值，从 serve 侧调好的预算换到 ACP
//     就静默变回 1/8；
//   - `--base`：绝大多数入口是 `envOr("DSHD_LLM_BASE", …)`，doctor 却把 URL 字面量
//     直接写死——**设了环境变量也不生效**；
//   - `--home`：run/acp/doctor 用 cwd 相对的 `tmp/dshd-home`，schedule/analytics 用
//     `defaultHome()`（`~/.dshd`）——同一个旗标两种落点，而 `defaultHome()` 的注释
//     写的就是"默认 DSHD_HOME 或 ~/.dshd"。
//
// 这些都不是"常量值不对"，而是**同一个参数有两个来源**。所以守卫不能断言某个常量
// 等于某个数（那只是把默认值抄第二份），而要**枚举所有入口的默认表达式**，逐个比对
// 是否来自同一个常量/解析函数。谁再写死一套，这里就红。
//
// 守卫是 AST 级的：解析 `cmd/dshd/*.go` 里所有 `fs.Int/String/Bool(...)` 调用，
// 取第一个参数的旗标名与第二个参数的**源码表达式**（`go/types.ExprString`）。
// 这样断言的是源码形状，不是运行期取值——改回字面量必红。

import (
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type flagDefault struct {
	file string
	line int
	expr string
}

// scanFlagDefaults 收集 cmd/dshd 非测试源码里所有 flag 默认表达式（按旗标名分组）。
func scanFlagDefaults(t *testing.T) map[string][]flagDefault {
	t.Helper()
	// go test 的 cwd 就是包目录；用绝对路径避免受 -C 影响。
	pattern := "*.go"
	files, err := filepath.Glob(pattern)
	if err != nil || len(files) == 0 {
		t.Fatalf("扫描 %s 失败: %v（files=%d）", pattern, err, len(files))
	}
	out := map[string][]flagDefault{}
	// 只认这些注册方法：fs.Var 的第二个参数是名字而不是默认值，形状不同。
	typed := map[string]bool{"Int": true, "String": true, "Bool": true, "Duration": true, "Int64": true}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !typed[sel.Sel.Name] || len(call.Args) < 2 {
				return true
			}
			recv, ok := sel.X.(*ast.Ident)
			if !ok || recv.Name != "fs" {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			flagName, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			out[flagName] = append(out[flagName], flagDefault{
				file: filepath.Base(name),
				line: fset.Position(call.Pos()).Line,
				expr: types.ExprString(call.Args[1]),
			})
			return true
		})
	}
	return out
}

// TestGuardFlagDefaultsAreSingleSourced 枚举所有入口，同一个旗标只允许一种默认表达式。
func TestGuardFlagDefaultsAreSingleSourced(t *testing.T) {
	defaults := scanFlagDefaults(t)

	// 每个旗标允许的默认表达式（**源码形状**）。多个入口必须完全一致。
	allowed := map[string][]string{
		// 每轮步数上限：四个入口 + 子代理统一到 defaultTurnMaxSteps（安全阀，唯一常量）。
		"max-steps": {"defaultTurnMaxSteps"},
		// 工具输出外置阈值：run/acp/serve 同一常量。
		"max-chars": {"defaultMaxChars"},
		// AGENTS.md 字节预算：run/serve 是 defaultWorkspaceBytes；ACP 以前写死 8000。
		"workspace-bytes": {"defaultWorkspaceBytes"},
		// 自动压缩阈值：只有一份默认值（defaultAutoCompact，经 autoCompactValue 注册）。
		// 注意 auto-compact 走 fs.Var，不在本守卫的扫描面内——它由
		// TestAutoCompactFlagAcceptsBooleanAndInteger 与 serve 透传测试覆盖。
		// 会话根目录：只允许 defaultHome()（env 或 ~/.dshd）；serve 是必填（空串=让调用方给）。
		"home": {"defaultHome()", `envOr("DSHD_HOME", "")`},
		// 模型端点：所有入口都必须是 envOr(env, 同一个默认常量)，不许再写字面量。
		"base": {`envOr("DSHD_LLM_BASE", defaultLLMBase)`},
	}

	for flag, want := range allowed {
		got := defaults[flag]
		if len(got) == 0 {
			t.Fatalf("旗标 --%s 在所有入口都没找到：旗标被删了，或者注册方式改了（守卫需要同步）", flag)
		}
		for _, def := range got {
			ok := false
			for _, w := range want {
				if def.expr == w {
					ok = true
					break
				}
			}
			if !ok {
				t.Errorf("--%s 在 %s:%d 的默认值是 %q，允许的只有 %v。\n"+
					"同一个参数有两个来源就是缺陷：请改成引用唯一常量/解析函数，不要在这里再写一套默认值。",
					flag, def.file, def.line, def.expr, want)
			}
		}
	}
}

// TestGuardNoDuplicatedGatewayURLLiteral 网关端点字面量每个包只允许出现一次（定义处）。
func TestGuardNoDuplicatedGatewayURLLiteral(t *testing.T) {
	const literal = `"http://127.0.0.1:3425/v1"`
	wantExactlyOnce := []string{".", "../../internal/transport"}
	for _, dir := range wantExactlyOnce {
		count, where := countInPackage(t, dir, literal)
		if count != 1 {
			t.Errorf("%s 里 %s 出现 %d 次（期望 1 次，定义在 defaultLLMBase）；位置：%v",
				dir, literal, count, where)
		}
	}
}

// TestGuardNoCwdRelativeHomeDefault 生产代码不许再把会话根目录默认到 cwd 相对路径。
func TestGuardNoCwdRelativeHomeDefault(t *testing.T) {
	if count, where := countInPackage(t, ".", `"tmp/dshd-home"`); count != 0 {
		t.Fatalf(`生产代码里仍有 "tmp/dshd-home"（%v）：会话根目录必须走 defaultHome()，`+
			`否则 dshd run 会在当前目录建库、schedule 在 ~/.dshd 建库，同一个产品两套落点`, where)
	}
}

// countInPackage 数一个包的非测试源码里某个**字符串字面量**出现几次。
//
// 用词法扫描而不是 `strings.Count`：注释里引用旧写法（例如说明"以前是
// envOr(..., \"http://...\")"）不该算进"定义了几份"里——否则守卫会逼着人删注释。
func countInPackage(t *testing.T, dir, literal string) (int, []string) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("扫描 %s 失败: %v", dir, err)
	}
	count := 0
	var where []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("读 %s 失败: %v", name, err)
		}
		fset := token.NewFileSet()
		file := fset.AddFile(name, fset.Base(), len(raw))
		var sc scanner.Scanner
		sc.Init(file, raw, nil, 0)
		n := 0
		for {
			_, tok, lit := sc.Scan()
			if tok == token.EOF {
				break
			}
			if tok == token.STRING && lit == literal {
				n++
			}
		}
		if n == 0 {
			continue
		}
		count += n
		where = append(where, filepath.Base(name))
	}
	return count, where
}

// TestGuardHostRunOptionsFieldsAreAllForwarded hostRunOptions 的每个字段都必须出现在
// 子进程 argv 里：新增字段忘了接线时，生产 serve 链会静默退回 run 的写死默认值。
func TestGuardHostRunOptionsFieldsAreAllForwarded(t *testing.T) {
	opts := hostRunOptions{
		autoCompact:     40001,
		maxChars:        4001,
		maxInlineTokens: 4002,
		workspaceBytes:  65537,
		contextWindow:   2000000,
		maxOutput:       128001,
		agents:          "docs/WORKER.md",
	}
	argv := opts.args()
	joined := strings.Join(argv, " ")
	for _, flagName := range []string{
		"--auto-compact", "--max-chars", "--max-inline-tokens", "--workspace-bytes",
		"--context-window", "--max-output", "--agents",
	} {
		if !strings.Contains(joined, flagName) {
			t.Errorf("hostRunOptions.args() 漏了 %s：serve 侧解析出来也传不到子进程（argv=%v）", flagName, argv)
		}
	}
	for _, value := range []string{"40001", "4001", "4002", "65537", "2000000", "128001", "docs/WORKER.md"} {
		if !strings.Contains(joined, value) {
			t.Errorf("hostRunOptions.args() 没把值 %s 传下去（argv=%v）", value, argv)
		}
	}
}
