package fssearch

// 本文件是 "native ignore rules"（官方 descriptor 语）的 Go 等价物：
// `.ignore` / `.gitignore` / `.git/info/exclude` / 可选全局排除文件。
//
// 官方语义由一个 rg 子进程承担（`rg --files` 默认尊重忽略文件，`--no-ignore`
// 关闭全部）。本文件按 gitignore(5) 的格式与 ripgrep 的可观察行为实现：
//
//   - 注释 `#`、取反 `!`、尾随空格（可用 `\` 转义）；
//   - 尾随 `/` 只匹配目录；
//   - 前导 `/` 或名字中间含 `/` 的模式相对忽略文件所在目录锚定，否则按基名
//     在任意深度匹配；
//   - `**` 的三条语义（前导 / 中缀 / 尾随）；
//   - 同一文件内"最后一条命中的模式胜出"；目录被忽略即整棵子树不列（不扫进去）；
//   - `.gitignore` / `.git/info/exclude` / 全局排除只在 git 仓库内生效
//     （rg 的 require-git 行为）；`.ignore` 与仓库无关、始终生效；
//   - 模式匹配大小写敏感（官方明说不读 `core.ignorecase`）。
//
// 与官方的差异（全局排除默认不读、`a/**` 边界）登记在 doc.go。

import (
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ignoreRule 是编译后的一条忽略模式。
type ignoreRule struct {
	negated bool
	dirOnly bool
	// anchored 为 true 表示模式相对忽略文件所在目录整体匹配；
	// 为 false 表示只按基名在任意深度匹配。
	anchored bool
	segs     []string
}

// ignoreScope 是一份"在某个目录下生效的规则集"。
type ignoreScope struct {
	// baseAbs 是忽略文件所在目录的绝对路径（可以是工作区根之上的祖先目录）。
	baseAbs string
	rules   []ignoreRule
}

// ignoreMatcher 持有一次搜索里所有与仓库相关的忽略状态。
type ignoreMatcher struct {
	haveGit    bool
	repoRoot   string
	baseScopes []ignoreScope
}

// newIgnoreMatcher 在工作区根上做一次仓库检测并读入"根之上/根级"的规则。
//
// 规则优先级（低 → 高）与 ripgrep 的 ignore crate 一致：
// 全局排除 < .git/info/exclude < 祖先 .gitignore（浅→深）< 目录内 .gitignore
// < 目录内 .ignore。本结构体只放前三级；后两级在遍历时按目录追加。
func newIgnoreMatcher(rootAbs, globalExcludesFile string) *ignoreMatcher {
	m := &ignoreMatcher{}
	repoRoot, gitDir, ok := findRepoRoot(rootAbs)
	m.haveGit, m.repoRoot = ok, repoRoot
	if !ok {
		return m
	}
	if globalExcludesFile != "" {
		if rules := readIgnoreFile(globalExcludesFile); len(rules) > 0 {
			m.baseScopes = append(m.baseScopes, ignoreScope{baseAbs: repoRoot, rules: rules})
		}
	}
	if gitDir != "" {
		if rules := readIgnoreFile(filepath.Join(gitDir, "info", "exclude")); len(rules) > 0 {
			m.baseScopes = append(m.baseScopes, ignoreScope{baseAbs: repoRoot, rules: rules})
		}
	}
	for _, dir := range ancestorChain(repoRoot, filepath.Dir(rootAbs)) {
		if rules := readIgnoreFile(filepath.Join(dir, ".gitignore")); len(rules) > 0 {
			m.baseScopes = append(m.baseScopes, ignoreScope{baseAbs: dir, rules: rules})
		}
	}
	return m
}

// dirScopes 读一个目录自己的 `.gitignore`（仓库内才读）与 `.ignore`。
//
// 顺序即优先级：`.ignore` 在 `.gitignore` 之后 → 更优先（ripgrep 的次序）。
func (m *ignoreMatcher) dirScopes(dirAbs string) []ignoreScope {
	if m == nil {
		return nil
	}
	var out []ignoreScope
	if m.haveGit {
		if rules := readIgnoreFile(filepath.Join(dirAbs, ".gitignore")); len(rules) > 0 {
			out = append(out, ignoreScope{baseAbs: dirAbs, rules: rules})
		}
	}
	if rules := readIgnoreFile(filepath.Join(dirAbs, ".ignore")); len(rules) > 0 {
		out = append(out, ignoreScope{baseAbs: dirAbs, rules: rules})
	}
	return out
}

// ignored 判断一个条目是否被忽略：从最深的作用域往浅看，第一个给出结论的胜出。
func (m *ignoreMatcher) ignored(scopes []ignoreScope, entryAbs string, isDir bool) bool {
	if m == nil {
		return false
	}
	for i := len(scopes) - 1; i >= 0; i-- {
		target, ok := relUnder(scopes[i].baseAbs, entryAbs)
		if !ok || target == "" {
			continue
		}
		if decided, ignored := decideRules(scopes[i].rules, target, isDir); decided {
			return ignored
		}
	}
	return false
}

// relUnder 求 entryAbs 相对 baseAbs 的 `/` 连接路径；不在 baseAbs 之下则 ok=false。
func relUnder(baseAbs, entryAbs string) (string, bool) {
	if baseAbs == "" {
		return "", false
	}
	if entryAbs == baseAbs {
		return "", true
	}
	prefix := baseAbs
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	if len(entryAbs) > len(prefix) && strings.HasPrefix(entryAbs, prefix) {
		return filepath.ToSlash(entryAbs[len(prefix):]), true
	}
	rel, err := filepath.Rel(baseAbs, entryAbs)
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return rel, true
}

// findRepoRoot 从 rootAbs 向上找 `.git`（目录，或 worktree/submodule 的
// `gitdir:` 指针文件）。找到返回仓库根与 git 目录；找不到返回 ok=false。
func findRepoRoot(rootAbs string) (repoRoot, gitDir string, ok bool) {
	dir := rootAbs
	for {
		gitPath := filepath.Join(dir, ".git")
		if info, err := os.Stat(gitPath); err == nil {
			if info.IsDir() {
				return dir, gitPath, true
			}
			data, rerr := os.ReadFile(gitPath)
			if rerr != nil {
				return dir, "", true
			}
			if target := parseGitdir(string(data)); target != "" {
				if !filepath.IsAbs(target) {
					target = filepath.Join(dir, filepath.FromSlash(target))
				}
				return dir, filepath.Clean(target), true
			}
			return dir, "", true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", false
		}
		dir = parent
	}
}

// parseGitdir 解析 `.git` 文件里的 `gitdir: <path>` 行。
func parseGitdir(data string) string {
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if strings.HasPrefix(line, "gitdir:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
		}
	}
	return ""
}

// ancestorChain 返回从 from 到 to 的目录链（含两端，浅→深）；to 不在 from 之下则空。
func ancestorChain(from, to string) []string {
	if from == "" || to == "" {
		return nil
	}
	var reversed []string
	dir := to
	for {
		reversed = append(reversed, dir)
		if samePath(dir, from) {
			out := make([]string, 0, len(reversed))
			for i := len(reversed) - 1; i >= 0; i-- {
				out = append(out, reversed[i])
			}
			return out
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
		dir = parent
	}
}

func samePath(a, b string) bool {
	if a == b {
		return true
	}
	// Windows 上大小写不敏感；filepath.Clean 已消除尾部分隔符差异。
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// readIgnoreFile 读一个忽略文件；不存在/读不了都返回 nil（忽略文件不可读不是搜索失败）。
func readIgnoreFile(abs string) []ignoreRule {
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil
	}
	return parseIgnoreLines(data)
}

// parseIgnoreLines 把忽略文件内容编译成规则列表（跳过注释与空行）。
func parseIgnoreLines(data []byte) []ignoreRule {
	var rules []ignoreRule
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if rule, ok := parseIgnoreLine(line); ok {
			rules = append(rules, rule)
		}
	}
	return rules
}

// parseIgnoreLine 解析一行（gitignore(5) pattern format 的子集 + `**`）。
func parseIgnoreLine(line string) (ignoreRule, bool) {
	line = trimTrailingSpaces(line)
	if line == "" {
		return ignoreRule{}, false
	}
	var r ignoreRule
	// 转义的 `#` / `!` 是字面量，不是注释/取反。
	if strings.HasPrefix(line, `\#`) || strings.HasPrefix(line, `\!`) {
		line = line[1:]
	} else if strings.HasPrefix(line, "#") {
		return ignoreRule{}, false
	} else if strings.HasPrefix(line, "!") {
		r.negated = true
		line = line[1:]
		if line == "" {
			return ignoreRule{}, false
		}
	}
	if strings.HasSuffix(line, "/") {
		r.dirOnly = true
		line = strings.TrimSuffix(line, "/")
	}
	if strings.HasPrefix(line, "/") {
		r.anchored = true
		line = strings.TrimPrefix(line, "/")
	} else if strings.Contains(line, "/") {
		r.anchored = true
	}
	if line == "" {
		return ignoreRule{}, false
	}
	r.segs = strings.Split(line, "/")
	return r, true
}

// trimTrailingSpaces 按 gitignore(5)：尾部空格被忽略，除非用 `\` 转义。
func trimTrailingSpaces(line string) string {
	for len(line) > 0 && line[len(line)-1] == ' ' {
		// 数一下这个空格前面连续的反斜杠个数：偶数 = 空格是分隔符（可裁），
		// 奇数 = 空格被转义（保留，并去掉那个反斜杠）。
		backslashes := 0
		for i := len(line) - 2; i >= 0 && line[i] == '\\'; i-- {
			backslashes++
		}
		if backslashes%2 == 1 {
			return line[:len(line)-2] + " "
		}
		line = line[:len(line)-1]
	}
	return line
}

// matches 判断一条规则是否命中 rel（相对忽略文件所在目录的 `/` 路径）。
func (r ignoreRule) matches(rel string, isDir bool) bool {
	if r.dirOnly && !isDir {
		return false
	}
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		return false
	}
	segs := strings.Split(rel, "/")
	if !r.anchored {
		// 无 `/` 的模式：按基名在任意深度匹配（git 的 basename 语义）。
		if len(r.segs) == 0 {
			return false
		}
		ok, err := path.Match(r.segs[0], segs[len(segs)-1])
		return err == nil && ok
	}
	return matchSegments(r.segs, segs)
}

// matchSegments 逐段匹配，`**` 可匹配零到多段。
func matchSegments(pat, segs []string) bool {
	if len(pat) == 0 {
		return len(segs) == 0
	}
	if pat[0] == "**" {
		if len(pat) == 1 {
			// 尾随 `**`（含单独 `**`）：匹配"内部一切"，即至少消费一段。
			// 这样 `a/**` 不会连目录 `a` 本身一起忽略（git 语义）。
			return len(segs) >= 1
		}
		if matchSegments(pat[1:], segs) {
			return true
		}
		for i := 1; i <= len(segs); i++ {
			if matchSegments(pat[1:], segs[i:]) {
				return true
			}
		}
		return false
	}
	if len(segs) == 0 {
		return false
	}
	ok, err := path.Match(pat[0], segs[0])
	if err != nil || !ok {
		return false
	}
	return matchSegments(pat[1:], segs[1:])
}

// decideRules 返回 (是否有规则命中, 命中后是否被忽略)：同一规则集里最后命中的胜出。
func decideRules(rules []ignoreRule, rel string, isDir bool) (matched bool, ignored bool) {
	for _, r := range rules {
		if !r.matches(rel, isDir) {
			continue
		}
		matched = true
		ignored = !r.negated
	}
	return matched, ignored
}
