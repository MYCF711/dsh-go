package fssearch

// workspaceFiles.search 的行为测试（先红后绿）。
//
// 契约来源（只读官方 0.2.2-rc.2 解包树 tmp/GO8-RC2/official-rc2/dsh）：
//
//	node_modules/@deepseek-ai/dsh-api-workspace-files/lib/typert.host.js:443-497
//	  —— 参数 request 四项 required；结果 {matches,truncated,incomplete}
//	node_modules/@deepseek-ai/dsh-api-workspace-files/lib/index.js:175-232
//	  —— searchWorkspace：rg --files --sort=path --null --path-separator=/、
//	     exclusion() 字面转义、maxEntries 处提前终止、depth→path 排序、切片截断
//	node_modules/@deepseek-ai/dsh-api-workspace-files/lib/index.js:660-695
//	  —— search()：trim query、空拒、root 必须是已存在目录
//
// 这些测试把官方实现里"能被观察到的行为"逐条钉住；与官方 rg 的差异
// （见 doc.go）不在本文件里放宽，差异本身单独登记。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// writeTree 在 root 下按 slash 相对路径建树。
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("建目录 %q: %v", abs, err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatalf("写文件 %q: %v", abs, err)
		}
	}
}

// matchList 把结果拍平成 "type path"（排序即结果顺序）。
func matchList(res Result) []string {
	out := make([]string, 0, len(res.Matches))
	for _, m := range res.Matches {
		out = append(out, string(m.Type)+" "+m.Path)
	}
	return out
}

func assertMatches(t *testing.T, res Result, want ...string) {
	t.Helper()
	got := matchList(res)
	if len(got) != len(want) {
		t.Fatalf("matches 数量 = %d, want %d\n got=%v\nwant=%v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("matches[%d] = %q, want %q\n got=%v\nwant=%v", i, got[i], want[i], got, want)
		}
	}
}

// searchNoIgnore 是多数测试用的默认调用：不读 .gitignore / .ignore，隐藏项按开关。
func searchNoIgnore(t *testing.T, root string, req Request) Result {
	t.Helper()
	res, err := Search(context.Background(), root, req)
	if err != nil {
		t.Fatalf("Search(%q, %+v): %v", root, req, err)
	}
	return res
}

// TestSearchCaseInsensitiveSubstring 钉住"大小写不敏感子串 + 祖先目录推导"。
//
// 官方 index.js:196 `const needle = spec.query.toLowerCase()`、
// :201 `admit` 对 name/path 各做一次 toLowerCase().includes(needle)；
// 目录只由已列出的文件推导（index.js:212-220），所以空目录永不出现在结果里。
func TestSearchCaseInsensitiveSubstring(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"Alpha.txt":       "a",
		"sub/ALPHA.md":    "b",
		"sub/beta.txt":    "c",
		"ALPHA_DEEP/x.md": "d",
		"empty/.keep":     "",
	})
	// empty/.keep 是隐藏文件：includeHidden=false 时不列出，因此 empty/ 也不推导出来。
	res := searchNoIgnore(t, root, Request{Query: "alpha"})
	assertMatches(t, res,
		"directory ALPHA_DEEP", // 深度 1，路径序在 Alpha.txt 之前（'L'<'l'）
		"file Alpha.txt",
		"file sub/ALPHA.md",
	)
	if res.Truncated || res.Incomplete {
		t.Fatalf("小树不该 truncated/incomplete: %+v", res)
	}
}

// TestSearchNameVersusPath 钉住"query 含 `/` 时匹配相对路径，否则只匹配条目名"。
func TestSearchNameVersusPath(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"sub/foo.txt":          "a",
		"deep/er/sub/foo.md":   "b",
		"fsub.txt":             "c",
		"foo/bar.txt":          "d",
		"deep/er/other/x.txt":  "e",
		"deep/other/sub/y.txt": "f",
	})

	byPath := searchNoIgnore(t, root, Request{Query: "sub/foo"})
	assertMatches(t, byPath,
		"file sub/foo.txt",
		"file deep/er/sub/foo.md",
	)

	byName := searchNoIgnore(t, root, Request{Query: "sub"})
	assertMatches(t, byName,
		"file fsub.txt",
		"directory sub",
		"directory deep/er/sub",
		"directory deep/other/sub",
	)

	// "deep/er/other" 与 "deep/other" 的目录名都不含 "sub"，所以只有含 sub 的名字入结果。
}

// TestSearchExcludedPathsAreLiteral 钉住 excludedPaths 是**字面量**：
// 不含 `/` 的名字在任意深度排除、含 `/` 的是工作区相对精确路径、
// 名字里的 glob 元字符不当模式用（官方 index.js:185 `exclusion()` 逐个转义）。
func TestSearchExcludedPathsAreLiteral(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"build/out.txt":      "a",
		"sub/build/keep.txt": "b",
		"gen/out.txt":        "c",
		"other/gen/out.txt":  "d",
		"a[b].txt":           "e",
		"ab.txt":             "f",
		"!bang.txt":          "g",
	})
	// 1) 不含 `/`：任意深度同名（目录命中即整棵子树排除）。
	res := searchNoIgnore(t, root, Request{Query: "txt", ExcludedPaths: []string{"build"}})
	assertMatches(t, res,
		"file !bang.txt", "file a[b].txt", "file ab.txt",
		"file gen/out.txt", "file other/gen/out.txt")

	// 2) 含 `/`：精确工作区相对路径，其它同名文件不受影响。
	res = searchNoIgnore(t, root, Request{Query: "txt", ExcludedPaths: []string{"gen/out.txt"}})
	assertMatches(t, res,
		"file !bang.txt", "file a[b].txt", "file ab.txt",
		"file build/out.txt", "file other/gen/out.txt", "file sub/build/keep.txt")

	// 3) 元字符是字面量：`a[b].txt` 只排除那个真名文件，不会当成 [b] 字符类。
	res = searchNoIgnore(t, root, Request{Query: "txt", ExcludedPaths: []string{"a[b].txt"}})
	assertMatches(t, res,
		"file !bang.txt", "file ab.txt",
		"file build/out.txt", "file gen/out.txt",
		"file other/gen/out.txt", "file sub/build/keep.txt")

	// 4) 字面量 `*` / `!` 不是通配/取反：`*` 不排除任何东西，`!bang.txt` 是真名。
	res = searchNoIgnore(t, root, Request{Query: "txt", ExcludedPaths: []string{"*"}})
	if len(res.Matches) != 7 {
		t.Fatalf(`excludedPaths=["*"] 是字面量，应一条都不排除，得到 %v`, matchList(res))
	}
	res = searchNoIgnore(t, root, Request{Query: "txt", ExcludedPaths: []string{"!bang.txt"}})
	assertMatches(t, res,
		"file a[b].txt", "file ab.txt",
		"file build/out.txt", "file gen/out.txt",
		"file other/gen/out.txt", "file sub/build/keep.txt")

	// 5) 前导 `/` 与尾部 `/` 只是"根锚定 / 目录"写法：`/build/` 仍是**含 `/`** 的
	// 精确路径（官方 `path.includes("/")` 看的是原始字符串），只排除根下 build/，
	// 不排除 sub/build/；`a[b].txt` 照旧按字面量排除。
	res = searchNoIgnore(t, root, Request{Query: "txt", ExcludedPaths: []string{"/build/", "a[b].txt"}})
	assertMatches(t, res,
		"file !bang.txt", "file ab.txt",
		"file gen/out.txt", "file other/gen/out.txt", "file sub/build/keep.txt")
}

// TestSearchHiddenSwitch 钉住 includeHidden（官方 index.js:209 `--hidden`）：
// 隐藏项默认不列，开关打开后列出；VCS 元数据在两种情况下都排除。
func TestSearchHiddenSwitch(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".git/config":      "t",
		".hidden/hc.txt":   "t",
		".hc.txt":          "t",
		"visible.txt":      "t",
		"not-hidden.tx2":   "t",
		".hidden/sub/d.md": "t",
	})

	off := searchNoIgnore(t, root, Request{Query: "t"})
	assertMatches(t, off, "file not-hidden.tx2", "file visible.txt")

	on := searchNoIgnore(t, root, Request{Query: "t", IncludeHidden: true})
	assertMatches(t, on,
		"file .hc.txt",
		"file not-hidden.tx2",
		"file visible.txt",
		"file .hidden/hc.txt",
	)
	if ext := matchList(on); contains(ext, "file .git/config") || contains(ext, "directory .git") {
		t.Fatalf("VCS 元数据必须始终排除，得到 %v", ext)
	}
}

// TestSearchVCSAlwaysExcluded 钉住 6 个 VCS 名在 no-ignore + hidden 下仍排除。
//
// 官方 index.js:171-173 `VCS_NAMES = [".git",".svn",".hg",".bzr",".jj",".sl"]`，
// 它们作为字面 glob 与 excludedPaths 一起传，不受 --hidden / --no-ignore 影响。
func TestSearchVCSAlwaysExcluded(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".git/a.txt":   "t",
		".svn/b.txt":   "t",
		".hg/c.txt":    "t",
		".bzr/d.txt":   "t",
		".jj/e.txt":    "t",
		".sl/f.txt":    "t",
		"normal/g.txt": "t",
	})
	res := searchNoIgnore(t, root, Request{Query: "txt", IncludeHidden: true})
	assertMatches(t, res, "file normal/g.txt")
}

// TestSearchSortDepthThenPath 钉住排序「先深度、再 workspace 相对路径」。
//
// 官方 index.js:227-229：
//
//	matches.sort((a,b) => a.path.split("/").length - b.path.split("/").length
//	                     || (a.path < b.path ? -1 : 1));
func TestSearchSortDepthThenPath(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"b/a/z.txt":   "t",
		"b/z2.txt":    "t",
		"a.txt":       "t",
		"z/y.txt":     "t",
		"alpha/q.txt": "q", // 目录 alpha 只由文件推导，但 "q.txt" 不含 "t"?? 它含 t
	})
	res := searchNoIgnore(t, root, Request{Query: "txt"})
	assertMatches(t, res,
		"file a.txt",
		"file alpha/q.txt",
		"file b/z2.txt",
		"file z/y.txt",
		"file b/a/z.txt",
	)
}

// TestSearchAncestorDirectoriesMatchByName 钉住"目录按自身名字参与匹配，
// 且只有自身命中的目录才出现（匹配文件的祖先不会跟着返回）"。
func TestSearchAncestorDirectoriesMatchByName(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"tree/leaf.txt":       "x",
		"tree/treehouse.md":   "y",
		"treehouse/roof.txt":  "z",
		"other/tree/nest.txt": "w",
	})
	// 不含 `/` 的 query 只比条目名：`roof.txt` / `nest.txt` 的**名字**不含 tree，
	// 所以它们不命中（它们的祖先目录 other/tree 自己命中 query，才作为目录出现）。
	res := searchNoIgnore(t, root, Request{Query: "tree"})
	assertMatches(t, res,
		"directory tree",
		"directory treehouse",
		"directory other/tree",
		"file tree/treehouse.md",
	)
	if contains(matchList(res), "file tree/leaf.txt") {
		t.Fatalf("不命中的文件不该出现在结果里：%v", matchList(res))
	}
}

// TestSearchEmptyDirectoryAbsent 钉住"空目录不存在"（目录只由文件推导）。
func TestSearchEmptyDirectoryAbsent(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"kept.txt": "x"})
	if err := os.MkdirAll(filepath.Join(root, "emptydir", "deeper"), 0o755); err != nil {
		t.Fatalf("建空目录: %v", err)
	}
	res := searchNoIgnore(t, root, Request{Query: "emptydir"})
	assertMatches(t, res)
}

// TestSearchTruncated 钉住 truncated：扫描完有界文件集后，命中数超过 maxResults。
func TestSearchTruncated(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"a.txt": "x", "b.txt": "x", "c.txt": "x",
	})
	cfg := DefaultConfig()
	cfg.MaxResults = 2
	res, err := SearchWithConfig(context.Background(), root, Request{Query: "txt"}, cfg)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	assertMatches(t, res, "file a.txt", "file b.txt")
	if !res.Truncated {
		t.Fatalf("命中 3 条、上限 2，truncated 必须是 true: %+v", res)
	}
	if res.Incomplete {
		t.Fatalf("文件数没超 maxEntries，incomplete 必须 false: %+v", res)
	}

	// 命中数正好等于上限：不截断。
	cfg.MaxResults = 3
	res, err = SearchWithConfig(context.Background(), root, Request{Query: "txt"}, cfg)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Truncated || len(res.Matches) != 3 {
		t.Fatalf("命中 3 条、上限 3，不该 truncated: %+v", res)
	}
}

// TestSearchIncompleteFromEntryCap 钉住 incomplete：rg 列出的文件数超过 maxEntries 被提前终止。
//
// 官方 index.js:204-220 的 callback 在 `listed === spec.maxEntries` 时 **先**返回 false
// （不再 admit 这一条），因此"被计入"的正好是排序后的前 maxEntries 个文件。
func TestSearchIncompleteFromEntryCap(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"a/t1.txt": "x", "b/t2.txt": "x", "c/t3.txt": "x",
	})
	cfg := DefaultConfig()
	cfg.MaxEntries = 2
	cfg.MaxResults = 100
	res, err := SearchWithConfig(context.Background(), root, Request{Query: "txt"}, cfg)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	assertMatches(t, res, "file a/t1.txt", "file b/t2.txt")
	if !res.Incomplete {
		t.Fatalf("文件数 3 > maxEntries 2，incomplete 必须是 true: %+v", res)
	}
	if res.Truncated {
		t.Fatalf("命中 2 条没超 maxResults，truncated 必须 false: %+v", res)
	}

	// 文件数正好等于 maxEntries：不提前终止。
	cfg.MaxEntries = 3
	res, err = SearchWithConfig(context.Background(), root, Request{Query: "txt"}, cfg)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Incomplete || res.Truncated || len(res.Matches) != 3 {
		t.Fatalf("文件数 3 == maxEntries 3，不该 incomplete/truncated: %+v", res)
	}
}

// TestSearchBlankQueryRefused 钉住空 query（去空白后）必须拒绝，不能返回空结果冒充成功。
//
// 官方 index.js:663 `const query = request.query.trim(); if (query.length === 0)
// throw new RemoteError("gateway/bad-request", "query must not be blank")`。
func TestSearchBlankQueryRefused(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a.txt": "x"})
	for _, q := range []string{"", " ", "   ", "\t", "\r\n ", "\u3000"} {
		_, err := Search(context.Background(), root, Request{Query: q})
		if !errors.Is(err, ErrBlankQuery) {
			t.Fatalf("query=%q 应回 ErrBlankQuery，得到 %v", q, err)
		}
	}
}

// TestSearchQueryIsTrimmed 钉住 query 会被 trim 后再匹配（官方 `request.query.trim()`）。
func TestSearchQueryIsTrimmed(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"alpha.txt": "x"})
	res := searchNoIgnore(t, root, Request{Query: "  alpha  "})
	assertMatches(t, res, "file alpha.txt")
}

// TestSearchRootValidation 钉住工作区根的边界：相对路径与 `..` 一律拒绝，
// 不存在 → ErrRootNotFound，不是目录 → ErrRootNotDirectory。
//
// 官方 index.js:670-679：fs.resolve(root) 后 stat，undefined → workspace-file/not-found，
// type!=='directory' → workspace-file/not-directory。本包对路径形状额外收紧（见 doc.go）。
func TestSearchRootValidation(t *testing.T) {
	base := t.TempDir()
	writeTree(t, base, map[string]string{"ws/x.txt": "x", "afile.txt": "x"})
	ws := filepath.Join(base, "ws")

	// 相对路径。
	if _, err := Search(context.Background(), "ws", Request{Query: "x"}); !errors.Is(err, ErrInvalidRoot) {
		t.Fatalf("相对根应回 ErrInvalidRoot，得到 %v", err)
	}
	// `..` 逃逸（未清理的绝对路径）。
	// 注意不能用 filepath.Join：它会把 `..` 清理掉。这里手工拼一个未清理路径。
	escape := ws + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "ws"
	if _, err := Search(context.Background(), escape, Request{Query: "x"}); !errors.Is(err, ErrInvalidRoot) {
		t.Fatalf("含 `..` 的根应回 ErrInvalidRoot，得到 %v", err)
	}
	// 空路径。
	if _, err := Search(context.Background(), "", Request{Query: "x"}); !errors.Is(err, ErrInvalidRoot) {
		t.Fatalf("空根应回 ErrInvalidRoot，得到 %v", err)
	}
	// 不存在。
	if _, err := Search(context.Background(), filepath.Join(base, "nope"), Request{Query: "x"}); !errors.Is(err, ErrRootNotFound) {
		t.Fatalf("不存在的根应回 ErrRootNotFound，得到 %v", err)
	}
	// 不是目录。
	if _, err := Search(context.Background(), filepath.Join(base, "afile.txt"), Request{Query: "x"}); !errors.Is(err, ErrRootNotDirectory) {
		t.Fatalf("文件当根应回 ErrRootNotDirectory，得到 %v", err)
	}
	// 尾部带分隔符的合法绝对路径是可接受的（Clean 后等价）。
	res, err := Search(context.Background(), ws+string(os.PathSeparator), Request{Query: "x"})
	if err != nil {
		t.Fatalf("尾部带分隔符的根应可接受: %v", err)
	}
	assertMatches(t, res, "file x.txt")
}

// TestSearchCancellation 钉住取消：context 取消后按 context.Canceled 返回，
// 不把"扫到一半的结果"当成功交出去。
//
// 官方 master 语义：signal 取消时 searchWorkspace 里 `combined.throwIfAborted()`
// 会抛回取消原因，`search()` 的 rejection 保留原始 reason。
func TestSearchCancellation(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a.txt": "x"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Search(ctx, root, Request{Query: "txt"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("已取消的 context 应回 context.Canceled，得到 %v", err)
	}
}

// TestSearchTimeout 钉住 search.timeoutMs：超时不是 incomplete，而是明确失败。
//
// 官方 index.js:190-196 超时抛 RemoteError("workspace-file/search-failed", …)。
func TestSearchTimeout(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a.txt": "x"})
	// 首检可能落在同一个时钟 tick 上，所以让遍历先干一会儿活（几百个目录），
	// 这样超时判定不再依赖"1ns 比一次 Now() 还短"这种不可论证的假设。
	for i := 0; i < 60; i++ {
		dir := filepath.Join(root, "d"+strconv.Itoa(i))
		for j := 0; j < 6; j++ {
			writeTree(t, dir, map[string]string{"f" + strconv.Itoa(j) + ".txt": "x"})
		}
	}
	cfg := DefaultConfig()
	cfg.Timeout = time.Nanosecond
	if _, err := SearchWithConfig(context.Background(), root, Request{Query: "txt"}, cfg); !errors.Is(err, ErrTimeout) {
		t.Fatalf("超时应回 ErrTimeout，得到 %v", err)
	}
}

// TestSearchSymlinksAbsent 钉住"符号链接不出现、也不被跟随"。
// Windows 上建链接可能因权限失败，跳过而不是降低断言。
func TestSearchSymlinksAbsent(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"real/target.txt": "x", "plain.txt": "y"})
	if err := os.Symlink(filepath.Join(root, "plain.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("本机不能建符号链接（%v），跳过", err)
	}
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "linkdir")); err != nil {
		t.Skipf("本机不能建目录符号链接（%v），跳过", err)
	}
	res := searchNoIgnore(t, root, Request{Query: "txt", IncludeHidden: true})
	// link.txt（指向普通文件的符号链接）名字含 txt，若被跟随就会出现在结果里。
	assertMatches(t, res,
		"file plain.txt",
		"file real/target.txt",
	)
}

// TestSearchEmptyTree 钉住空工作区：matches 是空数组（不是 null），两个标志都 false。
func TestSearchEmptyTree(t *testing.T) {
	root := t.TempDir()
	res := searchNoIgnore(t, root, Request{Query: "anything"})
	if len(res.Matches) != 0 {
		t.Fatalf("空工作区应无命中，得到 %v", matchList(res))
	}
	if res.Truncated || res.Incomplete {
		t.Fatalf("空工作区不该 truncated/incomplete: %+v", res)
	}
}

// TestSearchPathSeparatorIsSlash 钉住 path 是 `/` 连接的 workspace 相对路径（永不以 `/` 开头）。
func TestSearchPathSeparatorIsSlash(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a/b/c.txt": "x"})
	res := searchNoIgnore(t, root, Request{Query: "c.txt"})
	assertMatches(t, res, "file a/b/c.txt")
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
