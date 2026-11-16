package forge

// localDiff 的官方 rc.2 形状：fork point、完整 diff 文件字段、评审分层与未跟踪文件上限。
// 契约：dsh-experimental-client-ui-forge/src/{local-diff,diff,layers,types}.ts。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	defaultMaxDiffBytes      = 8_388_608
	defaultMaxUntrackedFiles = 100
	attributesPath           = ".gitattributes"
)

var (
	ErrDiffTooLarge     = errors.New("forge: diff 超过 maxDiffBytes")
	ErrCommitsMissing   = errors.New("forge: 选定提交不在 branch 范围内或顺序错误")
	ErrCommitsNotLinear = errors.New("forge: 选定提交不是连续的第一父提交链")
	fullCommitPattern   = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	hunkHeaderPattern   = regexp.MustCompile(`^@@ -([0-9]+)(?:,([0-9]+))? \+([0-9]+)(?:,([0-9]+))? @@`)
	binaryIndexPattern  = regexp.MustCompile(`(?m)^index ([0-9a-f]+\.\.[0-9a-f]+)`)
)

var localDiffArgs = []string{
	"-c", "core.quotePath=false", "diff", "--no-ext-diff", "--no-textconv",
	"--submodule=short", "--ignore-submodules=dirty", "--no-color", "-M",
	"--src-prefix=a/", "--dst-prefix=b/",
}

func fullCommit(commit string) bool { return fullCommitPattern.MatchString(commit) }

// LocalDiff 执行真实 git diff。branch 使用官方 forkPoint/chosenForkPoint；
// worktree 为会话创建记录时，自动 fork point 用其 baseCommit 而不是 merge-base。
func (e *Executor) LocalDiff(ctx context.Context, dir string, scope Scope, maxDiffBytes int, worktrees ...*WorktreeFork) (LocalDiff, error) {
	root, err := e.Root(ctx, dir)
	if err != nil {
		return LocalDiff{}, err
	}
	if maxDiffBytes <= 0 {
		maxDiffBytes = defaultMaxDiffBytes
	}
	var worktree *WorktreeFork
	if len(worktrees) > 0 {
		worktree = worktrees[0]
	}
	args := append([]string(nil), localDiffArgs...)
	var base *string
	attributesRev := ""
	switch scope.Kind {
	case "branch":
		commit, label, _, err := e.ForkPoint(ctx, root, scope.Base, worktree)
		if err != nil {
			return LocalDiff{}, err
		}
		if commit == "" {
			return LocalDiff{}, ErrNoForkPoint
		}
		base = &label
		if scope.Commits != nil {
			return e.commitsDiff(ctx, root, commit, label, *scope.Commits, maxDiffBytes)
		}
		args = append(args, commit)
	case "uncommitted":
		head, err := e.headOrEmptyTree(ctx, root)
		if err != nil {
			return LocalDiff{}, err
		}
		args = append(args, head)
	case "unstaged":
		// 空 range：工作树相对 index。
	case "staged":
		head, err := e.headOrEmptyTree(ctx, root)
		if err != nil {
			return LocalDiff{}, err
		}
		args = append(args, "--cached", head)
		attributesRev = ":"
	default:
		return LocalDiff{}, fmt.Errorf("forge: 未知 diff scope %q", scope.Kind)
	}
	args = append(args, "--")
	patch, _, err := e.run(ctx, root, args...)
	if err != nil {
		return LocalDiff{}, err
	}
	if len(patch) > maxDiffBytes {
		return LocalDiff{}, fmt.Errorf("%w: patch", ErrDiffTooLarge)
	}
	files := parseDiffFiles(patch)
	omitted := 0
	if scope.Kind != "staged" {
		untracked, left, err := e.untrackedDiffFiles(ctx, root, maxDiffBytes)
		if err != nil {
			return LocalDiff{}, err
		}
		files = append(files, untracked...)
		omitted = left
	}
	attributes, err := e.diffAttributes(ctx, root, attributesRev, maxDiffBytes)
	if err != nil {
		return LocalDiff{}, err
	}
	e.classifyDiffFiles(files, attributes)
	if files == nil {
		files = []DiffFile{}
	}
	return LocalDiff{Base: base, Files: files, Omitted: omitted}, nil
}

func (e *Executor) commitsDiff(ctx context.Context, root, fork, label string, chosen CommitRange, maxDiffBytes int) (LocalDiff, error) {
	if !fullCommit(chosen.From) || !fullCommit(chosen.To) {
		return LocalDiff{}, ErrCommitsMissing
	}
	commits, err := e.allBranchCommits(ctx, root, fork)
	if err != nil {
		return LocalDiff{}, err
	}
	first, last := -1, -1
	for i, commit := range commits {
		if commit.SHA == chosen.From {
			first = i
		}
		if commit.SHA == chosen.To {
			last = i
		}
	}
	if first < 0 || last < first {
		return LocalDiff{}, ErrCommitsMissing
	}
	for i := first + 1; i <= last; i++ {
		if commits[i].Parent == nil || *commits[i].Parent != commits[i-1].SHA {
			return LocalDiff{}, ErrCommitsNotLinear
		}
	}
	parent := ""
	if commits[first].Parent != nil {
		parent = *commits[first].Parent
	} else {
		parent, err = e.emptyTree(ctx, root)
		if err != nil {
			return LocalDiff{}, err
		}
	}
	args := append(append([]string(nil), localDiffArgs...), parent, chosen.To, "--")
	patch, _, err := e.run(ctx, root, args...)
	if err != nil {
		return LocalDiff{}, err
	}
	if len(patch) > maxDiffBytes {
		return LocalDiff{}, fmt.Errorf("%w: patch", ErrDiffTooLarge)
	}
	files := parseDiffFiles(patch)
	attributes, err := e.diffAttributes(ctx, root, chosen.To+":", maxDiffBytes)
	if err != nil {
		return LocalDiff{}, err
	}
	e.classifyDiffFiles(files, attributes)
	if files == nil {
		files = []DiffFile{}
	}
	return LocalDiff{Base: &label, Files: files, Omitted: 0}, nil
}

func (e *Executor) allBranchCommits(ctx context.Context, root, fork string) ([]LocalCommit, error) {
	format := "--format=%H%x00%P%x00%an%x00%aI%x00%s%x1e"
	out, _, err := e.run(ctx, root, "log", "--no-show-signature", "--topo-order", "--reverse", format, fork+"..HEAD", "--")
	if err != nil {
		return nil, err
	}
	return parseLogRecords(out), nil
}

func (e *Executor) headOrEmptyTree(ctx context.Context, root string) (string, error) {
	_, _, err := e.run(ctx, root, "rev-parse", "--verify", "--quiet", "HEAD")
	if err == nil {
		return "HEAD", nil
	}
	return e.emptyTree(ctx, root)
}

func (e *Executor) emptyTree(ctx context.Context, root string) (string, error) {
	out, _, err := e.run(ctx, root, "hash-object", "-t", "tree", "--stdin")
	return strings.TrimSpace(out), err
}

func (e *Executor) diffAttributes(ctx context.Context, root, revision string, maxBytes int) (string, error) {
	if revision == "" {
		data, oversized, err := readBoundedFile(filepath.Join(root, attributesPath), maxBytes)
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		if oversized {
			return "", fmt.Errorf("%w: %s", ErrDiffTooLarge, attributesPath)
		}
		if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
			return "", nil
		}
		return string(data), nil
	}
	shown, _, err := e.run(ctx, root, "show", revision+attributesPath)
	if err != nil {
		// 官方 show 允许 128：该侧没有 .gitattributes 就是空规则。
		if strings.Contains(err.Error(), "exit status 128") {
			return "", nil
		}
		return "", err
	}
	if len(shown) > maxBytes {
		return "", fmt.Errorf("%w: %s", ErrDiffTooLarge, attributesPath)
	}
	return shown, nil
}

func (e *Executor) untrackedDiffFiles(ctx context.Context, root string, maxBytes int) ([]DiffFile, int, error) {
	listed, _, err := e.run(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, 0, err
	}
	if len(listed) > maxBytes {
		return nil, 0, fmt.Errorf("%w: untracked file list", ErrDiffTooLarge)
	}
	paths := make([]string, 0)
	for _, path := range splitZ(listed) {
		if path != "" && !strings.HasSuffix(path, "/") {
			paths = append(paths, path)
		}
	}
	limit := e.MaxUntrackedFiles
	if limit <= 0 {
		limit = defaultMaxUntrackedFiles
	}
	files := make([]DiffFile, 0, min(limit, len(paths)))
	for _, path := range paths[:min(limit, len(paths))] {
		data, oversized, err := readBoundedFile(filepath.Join(root, filepath.FromSlash(path)), maxBytes)
		if err != nil {
			// 官方对 listing 后消失、嵌套仓库等 FS 错误跳过该文件，不伪造成空文件。
			continue
		}
		files = append(files, makeUntrackedDiff(path, data, oversized))
	}
	return files, max(0, len(paths)-limit), nil
}

func readBoundedFile(path string, maxBytes int) ([]byte, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) > maxBytes {
		return nil, true, nil
	}
	return data, false, nil
}

func makeUntrackedDiff(path string, content []byte, oversized bool) DiffFile {
	file := DiffFile{Path: path, Status: "added", Untracked: true, Hunks: []Hunk{}}
	identity := ""
	if oversized {
		file.Oversized = true
		identity = "oversized"
	} else if binaryContent(content) {
		file.Binary = true
		sum := sha256.Sum256(content)
		identity = hex.EncodeToString(sum[:])
	} else if len(content) > 0 {
		lines := strings.Split(string(content), "\n")
		ended := lines[len(lines)-1] == ""
		if ended {
			lines = lines[:len(lines)-1]
		}
		hunk := Hunk{OldStart: 1, OldLines: 0, NewStart: 1, NewLines: len(lines), Lines: make([]string, 0, len(lines)+1)}
		for _, line := range lines {
			hunk.Lines = append(hunk.Lines, "+"+line)
		}
		if !ended {
			hunk.Lines = append(hunk.Lines, `\ No newline at end of file`)
		}
		file.Hunks = append(file.Hunks, hunk)
		file.Additions = len(lines)
	}
	file.Digest = changeDigest(file.Path, nil, file.Status, file.Hunks, identity, "")
	return file
}

func binaryContent(content []byte) bool {
	probe := content
	if len(probe) > 8000 {
		probe = probe[:8000]
	}
	return bytes.IndexByte(probe, 0) >= 0 || !utf8.Valid(content)
}

// parseDiffFiles 把 git patch 解析成官方 ForgeDiffFile（layer 稍后分类）。
func parseDiffFiles(patch string) []DiffFile {
	lines := strings.Split(patch, "\n")
	files := make([]DiffFile, 0)
	for start := 0; start < len(lines); {
		if !strings.HasPrefix(lines[start], "diff --git ") {
			start++
			continue
		}
		end := start + 1
		for end < len(lines) && !strings.HasPrefix(lines[end], "diff --git ") {
			end++
		}
		if file, ok := parseDiffSection(lines[start:end]); ok {
			files = append(files, file)
		}
		start = end
	}
	return files
}

func parseDiffSection(lines []string) (DiffFile, bool) {
	file := DiffFile{Status: "modified", Hunks: []Hunk{}}
	var before, after *string
	var oldMode, newMode, blobs string
	renamed := false
	var hunk *Hunk
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "old mode "):
			oldMode = strings.TrimPrefix(line, "old mode ")
		case strings.HasPrefix(line, "new mode "):
			newMode = strings.TrimPrefix(line, "new mode ")
		case strings.HasPrefix(line, "new file mode "):
			file.Status = "added"
			newMode = strings.TrimPrefix(line, "new file mode ")
		case strings.HasPrefix(line, "deleted file mode "):
			file.Status = "deleted"
			oldMode = strings.TrimPrefix(line, "deleted file mode ")
		case strings.HasPrefix(line, "rename from "):
			renamed = true
			value := strings.TrimPrefix(line, "rename from ")
			before = &value
		case strings.HasPrefix(line, "rename to "):
			renamed = true
			value := strings.TrimPrefix(line, "rename to ")
			after = &value
		case strings.HasPrefix(line, "--- "):
			before = parsePatchPath(strings.TrimPrefix(line, "--- "), "a/")
		case strings.HasPrefix(line, "+++ "):
			after = parsePatchPath(strings.TrimPrefix(line, "+++ "), "b/")
		case line == "GIT binary patch" || strings.HasPrefix(line, "Binary files "):
			file.Binary = true
		case strings.HasPrefix(line, "index "):
			// index 头在 GIT binary patch 标记之前；先收下，最终只对 binary 计入 digest。
			if match := binaryIndexPattern.FindStringSubmatch(line); len(match) == 2 {
				blobs = match[1]
			}
		case strings.HasPrefix(line, "@@ "):
			match := hunkHeaderPattern.FindStringSubmatch(line)
			if len(match) == 5 {
				h := Hunk{OldStart: atoi(match[1]), OldLines: hunkCount(match[2]), NewStart: atoi(match[3]), NewLines: hunkCount(match[4]), Lines: []string{}}
				file.Hunks = append(file.Hunks, h)
				hunk = &file.Hunks[len(file.Hunks)-1]
			}
		case hunk != nil && line != "" && (line[0] == ' ' || line[0] == '+' || line[0] == '-' || line[0] == '\\'):
			hunk.Lines = append(hunk.Lines, line)
			if line[0] == '+' {
				file.Additions++
			} else if line[0] == '-' {
				file.Deletions++
			}
		}
	}
	if after != nil {
		file.Path = *after
	} else if before != nil {
		file.Path = *before
	} else {
		file.Path = diffHeaderPath(lines[0])
	}
	if file.Path == "" {
		return DiffFile{}, false
	}
	if renamed {
		file.Status = "renamed"
		file.PreviousPath = before
	} else if before == nil && after != nil {
		file.Status = "added"
	} else if after == nil && before != nil {
		file.Status = "deleted"
	}
	// binary patch 常没有 ---/+++；此时保留 new/deleted file mode 给出的状态，
	// modified 也不能因 before==nil 被误判成 added。
	identity := ""
	if file.Binary {
		identity = blobs
	}
	file.Digest = changeDigest(file.Path, file.PreviousPath, file.Status, file.Hunks, identity, oldMode+".."+newMode)
	return file, true
}

func parsePatchPath(raw, prefix string) *string {
	raw = strings.TrimSuffix(raw, "\r")
	if raw == "/dev/null" {
		return nil
	}
	if unquoted, err := strconv.Unquote(raw); err == nil {
		raw = unquoted
	}
	raw = strings.TrimPrefix(raw, prefix)
	return &raw
}

func diffHeaderPath(header string) string {
	raw := strings.TrimPrefix(header, "diff --git ")
	if index := strings.LastIndex(raw, " b/"); index >= 0 {
		return strings.TrimPrefix(raw[index+1:], "b/")
	}
	return ""
}

func atoi(value string) int {
	n, _ := strconv.Atoi(value)
	return n
}

func hunkCount(value string) int {
	if value == "" {
		return 1
	}
	return atoi(value)
}

// changeDigest 与官方 diff.ts:63-67 完全同形：JSON 数组的 SHA-256 前 32 个 hex。
func changeDigest(path string, previous *string, status string, hunks []Hunk, content, modes string) string {
	if hunks == nil {
		hunks = []Hunk{}
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode([]any{path, previous, status, hunks, content, modes})
	payload := bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])[:32]
}

// classifyDiffFiles 按 .gitattributes → 配置 glob → 内置 test/interface → implementation。
func (e *Executor) classifyDiffFiles(files []DiffFile, attributes string) {
	rules := parseAttributeRules(attributes)
	for i := range files {
		path := files[i].Path
		layer := ""
		generated := false
		for _, rule := range rules {
			if !rule.match(path) {
				continue
			}
			if rule.layerSet {
				layer = rule.layer
			}
			if rule.generatedSet {
				generated = rule.generated
			}
		}
		switch {
		case layer != "":
		case generated:
			layer = "test"
		case anyGlob(e.TestPaths, path):
			layer = "test"
		case anyGlob(e.InterfacePaths, path):
			layer = "interface"
		case anyGlob(builtInTestPaths, path):
			layer = "test"
		case anyGlob(builtInInterfacePaths, path):
			layer = "interface"
		default:
			layer = "implementation"
		}
		files[i].Layer = layer
	}
}

type attributeRule struct {
	match        func(string) bool
	layer        string
	layerSet     bool
	generated    bool
	generatedSet bool
}

func parseAttributeRules(attributes string) []attributeRule {
	rules := make([]attributeRule, 0)
	for _, raw := range strings.Split(attributes, "\n") {
		fields := strings.Fields(raw)
		if len(fields) == 0 {
			continue
		}
		pattern := fields[0]
		if strings.HasPrefix(pattern, "#") || strings.HasPrefix(pattern, "[attr]") || strings.HasPrefix(pattern, "\"") ||
			strings.HasPrefix(pattern, "!") || strings.HasSuffix(pattern, "/") {
			continue
		}
		rule := attributeRule{}
		for _, field := range fields[1:] {
			switch field {
			case "linguist-generated", "linguist-generated=true":
				rule.generated, rule.generatedSet = true, true
			case "-linguist-generated", "linguist-generated=false":
				rule.generated, rule.generatedSet = false, true
			case "!linguist-generated":
				rule.generated, rule.generatedSet = false, true
			case "-dsh-review-layer", "!dsh-review-layer":
				rule.layer, rule.layerSet = "", true
			default:
				if strings.HasPrefix(field, "dsh-review-layer=") {
					value := strings.TrimPrefix(field, "dsh-review-layer=")
					if value == "interface" || value == "implementation" || value == "test" {
						rule.layer = value
					} else {
						rule.layer = ""
					}
					rule.layerSet = true
				}
			}
		}
		if !rule.layerSet && !rule.generatedSet {
			continue
		}
		matcher := compileGlob(strings.TrimPrefix(pattern, "/"), false)
		if matcher == nil {
			continue
		}
		if strings.Contains(pattern, "/") {
			rule.match = matcher
		} else {
			rule.match = func(path string) bool {
				return matcher(path[strings.LastIndex(path, "/")+1:])
			}
		}
		rules = append(rules, rule)
	}
	return rules
}

var builtInTestPaths = []string{
	"**/test/**", "**/tests/**", "**/Tests/**", "**/__tests__/**", "**/spec/**", "**/e2e/**", "**/testdata/**", "**/__mocks__/**",
	"**/__fixtures__/**", "**/*.Tests/**", "**/*.spec.*", "**/*.test.*", "**/*.e2e.*", "**/*_test.go", "**/test_*.py", "**/*_test.py",
	"**/conftest.py", "**/*_spec.rb", "**/__snapshots__/**", "**/*.snap", "**/locales/**", "**/*.po", "**/*.pot", "**/*.lproj/*.strings",
	"**/res/values-*/strings.xml", "**/package-lock.json", "**/npm-shrinkwrap.json", "**/pnpm-lock.yaml", "**/yarn.lock", "**/bun.lock",
	"**/bun.lockb", "**/Cargo.lock", "**/go.sum", "**/poetry.lock", "**/uv.lock", "**/Pipfile.lock", "**/Gemfile.lock", "**/composer.lock",
	"**/Package.resolved", "**/packages.lock.json", "**/gradle.lockfile", "**/pubspec.lock", "**/mix.lock", "**/flake.lock",
}

var builtInInterfacePaths = []string{
	"**/package.json", "**/go.mod", "**/Cargo.toml", "**/pyproject.toml", "**/setup.py", "**/setup.cfg", "**/Gemfile", "**/*.gemspec",
	"**/pom.xml", "**/build.gradle", "**/build.gradle.kts", "**/*.csproj", "**/Package.swift", "**/composer.json", "**/pubspec.yaml",
	"**/mix.exs", "**/*.d.ts", "**/*.d.mts", "**/*.d.cts", "**/*.pyi", "**/*.h", "**/*.hpp", "**/*.proto", "**/*.graphql",
	"**/*.graphqls", "**/*.thrift", "**/openapi.{json,yaml,yml}", "**/swagger.{json,yaml,yml}",
}

func anyGlob(patterns []string, path string) bool {
	for _, pattern := range patterns {
		if match := compileGlob(pattern, true); match != nil && match(path) {
			return true
		}
	}
	return false
}

// compileGlob 覆盖官方 wildmatch 的 * / ** / ? / 字符集 / 转义；picomatch 的
// 内置规则另需简单 {a,b} 展开。无效模式不匹配，不升级为静默 implementation 假分类。
func compileGlob(pattern string, braces bool) func(string) bool {
	var source strings.Builder
	for i := 0; i < len(pattern); i++ {
		char := pattern[i]
		switch char {
		case '\\':
			if i+1 >= len(pattern) {
				return nil
			}
			i++
			source.WriteString(regexp.QuoteMeta(pattern[i : i+1]))
		case '?':
			source.WriteString("[^/]")
		case '*':
			last := i
			for last+1 < len(pattern) && pattern[last+1] == '*' {
				last++
			}
			var after byte
			if last+1 < len(pattern) {
				after = pattern[last+1]
			}
			crossing := last > i && (i == 0 || pattern[i-1] == '/') && (after == 0 || after == '/')
			if crossing && after == '/' {
				source.WriteString("(?:.*/)?")
				i = last + 1
			} else if crossing {
				source.WriteString(".*")
				i = last
			} else {
				source.WriteString("[^/]*")
				i = last
			}
		case '[':
			set, end, ok := globBracket(pattern, i)
			if !ok {
				return nil
			}
			source.WriteString(set)
			i = end
		case '{':
			if !braces {
				source.WriteString(`\{`)
				continue
			}
			close := strings.IndexByte(pattern[i+1:], '}')
			if close < 0 {
				source.WriteString(`\{`)
				continue
			}
			close += i + 1
			parts := strings.Split(pattern[i+1:close], ",")
			source.WriteString("(?:")
			for j, part := range parts {
				if j > 0 {
					source.WriteByte('|')
				}
				source.WriteString(regexp.QuoteMeta(part))
			}
			source.WriteByte(')')
			i = close
		default:
			source.WriteString(regexp.QuoteMeta(pattern[i : i+1]))
		}
	}
	expression, err := regexp.Compile("(?s)^" + source.String() + "$")
	if err != nil {
		return nil
	}
	return expression.MatchString
}

var posixClasses = map[string]string{
	"alnum": "a-zA-Z0-9", "alpha": "a-zA-Z", "blank": ` \t`, "cntrl": `\x00-\x1f\x7f`,
	"digit": "0-9", "graph": `\x21-\x7e`, "lower": "a-z", "print": `\x20-\x7e`,
	"punct": `\x21-\x2f\x3a-\x40\x5b-\x60\x7b-\x7e`, "space": `\t-\r `, "upper": "A-Z", "xdigit": "0-9a-fA-F",
}

func globBracket(pattern string, start int) (string, int, bool) {
	i := start + 1
	negated := i < len(pattern) && (pattern[i] == '!' || pattern[i] == '^')
	if negated {
		i++
	}
	var members strings.Builder
	for first := true; i < len(pattern) && (first || pattern[i] != ']'); first = false {
		char := pattern[i]
		if char == '[' && i+1 < len(pattern) && pattern[i+1] == ':' {
			close := strings.IndexByte(pattern[i+2:], ']')
			if close < 0 {
				return "", 0, false
			}
			close += i + 2
			if close > i+2 && pattern[close-1] == ':' {
				class, ok := posixClasses[pattern[i+2:close-1]]
				if !ok {
					return "", 0, false
				}
				members.WriteString(class)
				i = close + 1
				continue
			}
		}
		if char == '\\' {
			i++
			if i >= len(pattern) {
				return "", 0, false
			}
			char = pattern[i]
		}
		if i+2 < len(pattern) && pattern[i+1] == '-' && pattern[i+2] != ']' {
			end := pattern[i+2]
			members.WriteString(regexp.QuoteMeta(string(char)) + "-" + regexp.QuoteMeta(string(end)))
			i += 3
			continue
		}
		members.WriteString(regexp.QuoteMeta(string(char)))
		i++
	}
	if i >= len(pattern) || pattern[i] != ']' {
		return "", 0, false
	}
	if negated {
		return "[^/" + members.String() + "]", i, true
	}
	return "[" + members.String() + "]", i, true
}
