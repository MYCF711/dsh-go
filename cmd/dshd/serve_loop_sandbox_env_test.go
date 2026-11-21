package main

// 一轮 `dshd run` 子进程必须**无论从哪条路起来**都带上会话的沙盒档位。
//
// 为什么这值得一条守卫：父进程有两条起轮子进程的路——
//   - `loopRunner.run`（serve_tools.go）：继承环境 + 增量；
//   - `runProviderLoop`（serve_loop.go）：provider 路由那一轮，环境由
//     `providerLoopEnvironment` 整份重建。
//
// 档位只写在其中一条上时，另一条上的子进程 `GateFromEnv` 见不到
// `DSHD_SANDBOX_MODE` 就返回 nil 闸门，工具层于是**直接放行**：`read-only`
// 会话在那条路上照样能跑 bash，而界面上档位显示得好好的。这种漏法没有报错、
// 没有日志，只能靠"两条路都必须调同一个函数"这条形状守卫拦住。
//
// 守卫是 AST 级的（与 parameter_guard_test.go 同一思路）：断言的是源码形状，
// 把调用删掉必红。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"dshgo/internal/sandbox"
)

// callsTurnChildSandboxEnv 报某个文件里名为 funcName 的函数体有没有调用
// `*.turnChildSandboxEnv(...)`。
func callsTurnChildSandboxEnv(t *testing.T, file, funcName string) bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", file, err)
	}
	found := false
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name == nil || fn.Name.Name != funcName || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if ok && sel.Sel != nil && sel.Sel.Name == "turnChildSandboxEnv" {
				found = true
				return false
			}
			return true
		})
		return found
	}
	t.Fatalf("%s 里找不到函数 %s", file, funcName)
	return false
}

func TestBothTurnSpawnPathsApplySandboxEnv(t *testing.T) {
	// go test 的 cwd 就是包目录。
	for _, c := range []struct{ file, fn string }{
		{"serve_tools.go", "run"},
		{"serve_loop.go", "runProviderLoop"},
	} {
		if _, err := os.Stat(c.file); err != nil {
			t.Fatalf("%s 不存在: %v", c.file, err)
		}
		if !callsTurnChildSandboxEnv(t, c.file, c.fn) {
			t.Fatalf("%s 的 %s 没有走 turnChildSandboxEnv：这条 spawn 路径上的会话沙盒档位会静默失效", c.file, c.fn)
		}
	}
}

// turnChildSandboxEnv 的内容：档位 + 三个策略根，缺一个都会让"会话的工作区"
// 与"策略判定的工作区"变成两条不同的 canonical 路径。
func TestTurnChildSandboxEnvCarriesSessionMode(t *testing.T) {
	home := t.TempDir()
	reg := newSandboxTestRegistry(t)
	store := mustSandboxStore(t, home)
	id := createSandboxSession(t, home, "read-only")
	r := &loopRunner{home: home, sandboxPresets: reg, sandboxStore: store}

	env, err := r.turnChildSandboxEnv(id)
	if err != nil {
		t.Fatalf("turnChildSandboxEnv 失败：%v", err)
	}
	want := map[string]bool{}
	wd, wdErr := os.Getwd()
	if wdErr != nil {
		t.Fatal(wdErr)
	}
	userHome, homeErr := os.UserHomeDir()
	if homeErr != nil {
		t.Fatal(homeErr)
	}
	want[sandbox.EnvMode+"=read-only"] = false
	want[sandbox.EnvWorkspaceRoot+"="+wd] = false
	want[sandbox.EnvHomeDir+"="+userHome] = false
	want[sandbox.EnvExtraReadRoots+"="+filepath.Join(home, "artifacts")] = false
	for _, kv := range env {
		if _, ok := want[kv]; ok {
			want[kv] = true
		}
	}
	for kv, seen := range want {
		if !seen {
			t.Fatalf("子进程策略环境缺 %q（实际 %v）", kv, env)
		}
	}
	// 绝不许写进父进程环境：A/B 会话会互相污染。
	if os.Getenv(sandbox.EnvMode) != "" {
		t.Fatalf("把档位写进了父进程环境（%s=%q）", sandbox.EnvMode, os.Getenv(sandbox.EnvMode))
	}

	// store/presets 未接线时返回 nil：旧行为不变（子进程按继承环境判定），
	// 而不是凭空写一个默认档。
	bare := &loopRunner{home: home}
	if got, err := bare.turnChildSandboxEnv(id); err != nil || got != nil {
		t.Fatalf("未接线时应返回 (nil,nil)，实际 (%v,%v)", got, err)
	}
}
