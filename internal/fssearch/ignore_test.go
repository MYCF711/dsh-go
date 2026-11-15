package fssearch

// gitignore / .ignore 语义测试（先红后绿）。
//
// 官方 rg 的忽略规则语义（index.js:209 `--no-ignore`；descriptor 里的描述：
// "including repository detection, nested and parent .gitignore, .git/info/exclude,
// global Git excludes and .ignore. The service does not read core.ignorecase;
// false passes --no-ignore."）：
//
//   - respectGitignore=true  → .ignore / .gitignore（需在 git 仓库内）/ .git/info/exclude
//     / 父级 .gitignore / 全局排除都生效；
//   - respectGitignore=false → 全部失效（但 hidden 仍按 includeHidden 单独判）；
//   - 忽略模式大小写敏感（官方明说不读 core.ignorecase）。
//
// 本文件把能被观察到的部分逐条钉住；读用户级全局配置这一项本包默认关闭
// （见 doc.go 的差异表），需要时由 Config.GitGlobalExcludesFile 显式给路径。

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeFileText(t *testing.T, abs, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("建目录 %q: %v", filepath.Dir(abs), err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("写文件 %q: %v", abs, err)
	}
}

// markRepo 让 root 看起来是一个 git 仓库根（官方 rg 的 require_git 行为需要它）。
func markRepo(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("建 .git: %v", err)
	}
}

// searchGit 用显式配置跑一次（默认配置 + 指定 respect 开关）。
func searchGit(t *testing.T, root string, req Request) Result {
	t.Helper()
	res, err := Search(context.Background(), root, req)
	if err != nil {
		t.Fatalf("Search(%q): %v", root, err)
	}
	return res
}

// TestSearchRespectGitignoreSwitch 钉住 respectGitignore 开关的作用面：
// 打开时 .gitignore（含嵌套）、.ignore 都排除；关闭时等价 `--no-ignore`，一条都不排除。
func TestSearchRespectGitignoreSwitch(t *testing.T) {
	root := t.TempDir()
	markRepo(t, root)
	writeFileText(t, filepath.Join(root, ".gitignore"), "ignored.txt\nignored-dir/\n")
	writeFileText(t, filepath.Join(root, ".ignore"), "dotignored.txt\n")
	writeFileText(t, filepath.Join(root, "sub", ".gitignore"), "nested-ignored.txt\n")
	writeTree(t, root, map[string]string{
		"ignored.txt":            "x",
		"ignored-dir/a.txt":      "x",
		"kept.txt":               "x",
		"sub/nested-ignored.txt": "x",
		"sub/kept2.txt":          "x",
		"dotignored.txt":         "x",
		"sub/ignored.txt":        "x", // 不含 `/` 的模式在任意深度生效
		"sub/ignored-dir/b.txt":  "x",
		"other/kept3.txt":        "x",
	})

	res := searchGit(t, root, Request{Query: "txt", RespectGitignore: true})
	assertMatches(t, res,
		"file kept.txt",
		"file other/kept3.txt",
		"file sub/kept2.txt",
	)

	res = searchGit(t, root, Request{Query: "txt"}) // RespectGitignore=false == --no-ignore
	assertMatches(t, res,
		"file dotignored.txt",
		"file ignored.txt",
		"file kept.txt",
		"file ignored-dir/a.txt",
		"file other/kept3.txt",
		"file sub/ignored.txt",
		"file sub/kept2.txt",
		"file sub/nested-ignored.txt",
		"file sub/ignored-dir/b.txt",
	)
}

// TestSearchGitignoreRequiresGitRepo 钉住 rg 的 require_git：
// `.gitignore` 只在 git 仓库内生效，`.ignore` 与仓库无关、始终生效。
func TestSearchGitignoreRequiresGitRepo(t *testing.T) {
	root := t.TempDir() // 注意：不建 .git
	writeFileText(t, filepath.Join(root, ".gitignore"), "ignored.txt\n")
	writeFileText(t, filepath.Join(root, ".ignore"), "dotignored.txt\n")
	writeTree(t, root, map[string]string{
		"ignored.txt":    "x",
		"kept.txt":       "x",
		"dotignored.txt": "x",
	})
	res := searchGit(t, root, Request{Query: "txt", RespectGitignore: true})
	assertMatches(t, res, "file ignored.txt", "file kept.txt")
}

// TestSearchGitInfoExclude 钉住 `.git/info/exclude`。
func TestSearchGitInfoExclude(t *testing.T) {
	root := t.TempDir()
	markRepo(t, root)
	writeFileText(t, filepath.Join(root, ".git", "info", "exclude"), "excluded-by-info.txt\n")
	writeTree(t, root, map[string]string{
		"excluded-by-info.txt": "x",
		"kept.txt":             "x",
	})
	res := searchGit(t, root, Request{Query: "txt", RespectGitignore: true})
	assertMatches(t, res, "file kept.txt")

	res = searchGit(t, root, Request{Query: "txt"})
	assertMatches(t, res, "file excluded-by-info.txt", "file kept.txt")
}

// TestSearchParentGitignore 钉住"父级 .gitignore"：工作区根在仓库子目录里时，
// 仓库根（及以上）的 .gitignore 同样生效，且带 `/` 的模式相对仓库根锚定。
func TestSearchParentGitignore(t *testing.T) {
	repo := t.TempDir()
	markRepo(t, repo)
	writeFileText(t, filepath.Join(repo, ".gitignore"),
		"from-parent.txt\nws/parent-anchored.txt\n")
	ws := filepath.Join(repo, "ws")
	writeTree(t, ws, map[string]string{
		"from-parent.txt":      "x",
		"parent-anchored.txt":  "x",
		"kept.txt":             "x",
		"deep/from-parent.txt": "x",
	})
	res := searchGit(t, ws, Request{Query: "txt", RespectGitignore: true})
	assertMatches(t, res, "file kept.txt")

	res = searchGit(t, ws, Request{Query: "txt"})
	assertMatches(t, res,
		"file from-parent.txt",
		"file kept.txt",
		"file parent-anchored.txt",
		"file deep/from-parent.txt",
	)
}

// TestSearchGitdirFileRepo 钉住 `.git` 是**文件**（git worktree / submodule）
// 时的仓库检测：`gitdir:` 指向的目录里的 `info/exclude` 生效。
func TestSearchGitdirFileRepo(t *testing.T) {
	base := t.TempDir()
	gitdir := filepath.Join(base, "real-git-dir")
	writeFileText(t, filepath.Join(gitdir, "info", "exclude"), "worktree-ignored.txt\n")
	ws := filepath.Join(base, "ws")
	writeTree(t, ws, map[string]string{"worktree-ignored.txt": "x", "kept.txt": "x"})
	writeFileText(t, filepath.Join(ws, ".git"), "gitdir: "+filepath.ToSlash(gitdir)+"\n")

	res := searchGit(t, ws, Request{Query: "txt", RespectGitignore: true})
	assertMatches(t, res, "file kept.txt")
}

// TestSearchGlobalGitExcludesFile 钉住可选的全局排除源：显式给了路径就生效。
func TestSearchGlobalGitExcludesFile(t *testing.T) {
	root := t.TempDir()
	markRepo(t, root)
	global := filepath.Join(t.TempDir(), "global-ignore")
	writeFileText(t, global, "glob-ignored.txt\n")
	writeTree(t, root, map[string]string{
		"glob-ignored.txt": "x",
		"kept.txt":         "x",
	})
	cfg := DefaultConfig()
	cfg.GitGlobalExcludesFile = global
	res, err := SearchWithConfig(context.Background(), root,
		Request{Query: "txt", RespectGitignore: true}, cfg)
	if err != nil {
		t.Fatalf("SearchWithConfig: %v", err)
	}
	assertMatches(t, res, "file kept.txt")
}

// TestIgnoreRuleMatching 单测忽略模式匹配（gitignore 语法子集）。
//
// 规则来源：gitignore(5) 的 pattern format；`**` 的三条语义照抄。
func TestIgnoreRuleMatching(t *testing.T) {
	cases := []struct {
		name    string
		line    string
		rel     string
		isDir   bool
		matched bool
		ignore  bool
	}{
		{name: "裸名字任意深度", line: "foo", rel: "a/b/foo", isDir: false, matched: true, ignore: true},
		{name: "裸名字不是后缀子串", line: "foo", rel: "a/b/foobar", isDir: false, matched: false, ignore: false},
		{name: "通配后缀任意深度", line: "*.log", rel: "x/y/a.log", isDir: false, matched: true, ignore: true},
		{name: "通配只匹配基名", line: "*.log", rel: "a.log/x", isDir: false, matched: false, ignore: false},
		{name: "前导 / 锚定根", line: "/build", rel: "build", isDir: true, matched: true, ignore: true},
		{name: "前导 / 不匹配子目录同名", line: "/build", rel: "sub/build", isDir: true, matched: false, ignore: false},
		{name: "无前导 / 的名字任意深度", line: "build", rel: "sub/build", isDir: true, matched: true, ignore: true},
		{name: "尾随 / 只匹配目录", line: "build/", rel: "a/build", isDir: true, matched: true, ignore: true},
		{name: "尾随 / 不匹配同名文件", line: "build/", rel: "a/build", isDir: false, matched: false, ignore: false},
		{name: "中缀 / 锚定相对", line: "doc/frotz", rel: "doc/frotz", isDir: true, matched: true, ignore: true},
		{name: "中缀 / 不从更深层匹配", line: "doc/frotz", rel: "x/doc/frotz", isDir: true, matched: false, ignore: false},
		{name: "前导 ** 匹配任意前缀", line: "**/foo", rel: "foo", isDir: false, matched: true, ignore: true},
		{name: "前导 ** 匹配多层", line: "**/foo", rel: "a/b/foo", isDir: false, matched: true, ignore: true},
		{name: "中缀 ** 匹配零层", line: "a/**/b", rel: "a/b", isDir: false, matched: true, ignore: true},
		{name: "中缀 ** 匹配多层", line: "a/**/b", rel: "a/x/y/b", isDir: false, matched: true, ignore: true},
		{name: "尾随 ** 匹配全部子孙", line: "a/**", rel: "a/x/y", isDir: false, matched: true, ignore: true},
		{name: "尾随 ** 不匹配目录自身", line: "a/**", rel: "a", isDir: true, matched: false, ignore: false},
		{name: "? 单字符", line: "a?c", rel: "abc", isDir: false, matched: true, ignore: true},
		{name: "? 不跨分隔符", line: "a?c", rel: "a/c", isDir: false, matched: false, ignore: false},
		{name: "字符类", line: "[abc].txt", rel: "b.txt", isDir: false, matched: true, ignore: true},
		{name: "模式大小写敏感", line: "*.log", rel: "A.LOG", isDir: false, matched: false, ignore: false},
		{name: "注释行不生效", line: "#comment", rel: "#comment", isDir: false, matched: false, ignore: false},
		{name: "转义 # 当字面量", line: `\#hash`, rel: "#hash", isDir: false, matched: true, ignore: true},
		{name: "尾部空格被裁掉", line: "foo   ", rel: "foo", isDir: false, matched: true, ignore: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rules := parseIgnoreLines([]byte(c.line + "\n"))
			decided, ignored := decideRules(rules, c.rel, c.isDir)
			if decided != c.matched {
				t.Fatalf("模式 %q 对 %q (isDir=%v): 命中=%v, want %v", c.line, c.rel, c.isDir, decided, c.matched)
			}
			if decided && ignored != c.ignore {
				t.Fatalf("模式 %q 对 %q: ignored=%v, want %v", c.line, c.rel, ignored, c.ignore)
			}
		})
	}
}

// TestIgnoreLastMatchWins 钉住同一文件内"最后一条命中的模式胜出"（`!` 取反）。
func TestIgnoreLastMatchWins(t *testing.T) {
	rules := parseIgnoreLines([]byte("*.txt\n!keep.txt\n"))
	if _, ignored := decideRules(rules, "a.txt", false); !ignored {
		t.Fatalf("a.txt 应被 *.txt 忽略")
	}
	if decided, ignored := decideRules(rules, "keep.txt", false); decided && ignored {
		t.Fatalf("keep.txt 应被 !keep.txt 重新纳入")
	}
	// 取反只影响它自己命中过的路径，不认识的路径仍然是"未命中"。
	if decided, _ := decideRules(rules, "other.md", false); decided {
		t.Fatalf("other.md 不该被 *.txt 命中")
	}
}

// TestSkipIgnoredDirectoryPrunesChildren 钉住"目录被忽略即整棵子树不列"。
func TestSkipIgnoredDirectoryPrunesChildren(t *testing.T) {
	root := t.TempDir()
	markRepo(t, root)
	writeFileText(t, filepath.Join(root, ".gitignore"), "vendor/\n")
	writeTree(t, root, map[string]string{
		"vendor/a.txt":      "x",
		"vendor/deep/b.txt": "x",
		"kept.txt":          "x",
	})
	res := searchGit(t, root, Request{Query: "txt", RespectGitignore: true})
	assertMatches(t, res, "file kept.txt")
}
