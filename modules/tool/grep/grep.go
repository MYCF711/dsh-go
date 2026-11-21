// Package grep 是内容搜索工具：正则匹配，返回「文件:行:文本」的结构化结果。
//
// 与 shell 里的 grep 的差别全在返回侧：只回匹配行（不回上下文噪声）、
// 每条带行号、总量有界、二进制与大文件直接跳过并说明。
package grep

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"dshgo/internal/fsutil"
	"dshgo/internal/kernel"
	"dshgo/internal/spi"
)

var schema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "pattern": {"type": "string", "description": "Go 正则语法"},
    "path": {"type": "string", "description": "起始目录，默认工作区根"},
    "include": {"type": "string", "description": "只搜匹配该 glob 的文件，如 **/*.go"},
    "ignore_case": {"type": "boolean", "description": "默认 false"},
    "limit": {"type": "integer", "minimum": 1, "description": "最多返回多少条匹配，默认 100"}
  },
  "required": ["pattern"],
  "additionalProperties": false
}`)

const (
	defaultLimit  = 100
	maxLimit      = 500
	maxFileBytes  = 2 << 20
	maxLineBytes  = 400
	scannerBufMax = 1 << 20

	// lineTruncMarker 是截断行的标记，取自官方 `previewLine` 的同一句话
	// （`dsh-tool-fs-search/src/search-core.ts:301`）。不标的话，模型会把
	// 半截行当成完整行直接引用。
	lineTruncMarker = " (line truncated)"
)

// previewLine 把一条匹配行裁到 maxBytes 字节以内：**裁在 UTF-8 字符边界上**，并标出被裁过。
//
// 官方原件（`dsh-tool-fs-search/src/search-core.ts:297-302`）：
//
//	export function previewLine(line: string, maxBytes: number): string {
//	  const retainer = new TextRetainer({ kind: 'head', maxBytes })
//	  retainer.push(line)
//	  const kept = retainer.finish()
//	  return kept.truncated ? `${kept.text} (line truncated)` : kept.text
//	}
//
// 它的 `finish()` 在出口把尾部半个码位削掉（`trimTrailingPartialUtf8`，
// `dsh-output-retention/src/index.ts`），`GREP_MAX_LINE_BYTES` 的注释写明
// "the cut preserves UTF-8 boundaries"，官方 grep 侧默认 2000 字节。
//
// 为什么非这么裁：这里是 `line[:400]` 直接切**字节**。中文/emoji 行被切成半个字符后，
// 字符串里就带着非法 UTF-8；而 grep 的结果没有 Text，投影层退回 `Summary + JSON(Data)`
// 给模型（`internal/session/project.go:31`），`encoding/json` 会把非法字节换成 U+FFFD ——
// 模型看到的是「这一行有乱码」，真相是「这一行被截断了，后面还有」。
// 这与已经修过的 bash 输出裁剪（官方 `dsh-subprocess-local/src/output.ts:174-179`）
// 是同一个病，只是入口不同：那处我们已经按 `utf8.RuneStart` 前推，这里当时漏了。
func previewLine(line string, maxBytes int) string {
	if maxBytes <= 0 || len(line) <= maxBytes {
		return line
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(line[cut]) {
		cut--
	}
	return line[:cut] + lineTruncMarker
}

// Tool 实现 spi.Tool。
type Tool struct {
	fs spi.FS
}

func (t *Tool) Name() string   { return "grep" }
func (t *Tool) Caps() spi.Caps { return spi.Caps{} }
func (t *Tool) Description() string {
	// 说明里必须点出「跳过 .git/refs/node_modules 等目录」：
	// 模型默认爱用 bash 的 grep -r，那会把 vendored 代码一起扫进来。
	// 实测一次这样的事故：refs/ 里 3,569 个文件让答案从 252 变成 9,852，
	// 模型为了消除这个歧义多花了 6 次调用、多烧 22k token。
	return "按 Go 正则搜内容，返回 文件:行:文本；自动跳过 .git/refs/node_modules，比 bash grep -r 准。"
}
func (t *Tool) Schema() json.RawMessage { return schema }

// ConcurrencySafe 声明 grep 可以和同批其它调用并发（官方 rc.2：
// `dsh-tool-fs-search/src/grep.ts:1095` `isConcurrencySafe: () => true`）。
//
// 为什么敢声明：grep 只读文件系统（`Caps` 为空），每次调用自带 pattern/path/include/limit，
// 不写任何共享状态。一次响应里"搜 6 个关键词"是最常见的形状，串行跑要白等 6 份耗时。
func (t *Tool) ConcurrencySafe(args json.RawMessage) bool {
	_ = args // 逐调用签名保留：以后若加了"会写"的参数，在这里按 args 判
	return true
}

type args struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	Include    string `json:"include"`
	IgnoreCase bool   `json:"ignore_case"`
	Limit      int    `json:"limit"`
}

// Match 是一条命中。
type Match struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

type payload struct {
	Pattern      string `json:"pattern"`
	Base         string `json:"base"`
	Count        int    `json:"count"`
	FilesScanned int    `json:"files_scanned"`
	FilesSkipped int    `json:"files_skipped"`
	Truncated    bool   `json:"truncated"`
	// PartialFiles 是「扫到中途就停下」的文件数：某一行比扫描缓冲（scannerBufMax）还长时
	// `bufio.Scanner` 会直接 `Scan()==false`，该文件**后面的行一条都没扫**。
	//
	// 为什么要单独一个数、不再静默：这个洞的症状不是报错，而是"0 处命中 + 提示你去检查
	// pattern 和路径"（改前实测：1.2 MiB 单行 + 下一行 NEEDLE → count=0、skipped=0、
	// 提示「没有命中：确认 base 路径对不对」）。模型据此得出"代码里没有这个符号"这个
	// **事实错误**，而真相是文件只扫了一行。同类病在本仓已有判例（官方 #12 是"长行让
	// 整次搜索失败"，我们不会失败，但会静默漏扫——两种都让答案变成错的）。
	PartialFiles int     `json:"partial_files,omitempty"`
	Matches      []Match `json:"matches"`
}

var errLimitReached = errors.New("limit reached")

func (t *Tool) Exec(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	var a args
	if err := json.Unmarshal(raw, &a); err != nil {
		return spi.Result{Hint: `参数不合法：期望 {"pattern": "...", "path"?: ".", "include"?: "**/*.go"}`},
			fmt.Errorf("参数解析失败: %w", err)
	}
	if a.Pattern == "" {
		return spi.Result{Hint: "pattern 不能为空；这是 Go 正则，特殊字符要转义"}, fmt.Errorf("pattern 为空")
	}
	if a.Limit <= 0 {
		a.Limit = defaultLimit
	}
	if a.Limit > maxLimit {
		a.Limit = maxLimit
	}

	expr := a.Pattern
	if a.IgnoreCase {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return spi.Result{Hint: "正则不合法：检查括号/转义；要搜字面量就把特殊字符转义"},
			fmt.Errorf("正则编译失败: %w", err)
	}

	base := a.Path
	if strings.TrimSpace(base) == "" {
		base = "."
	}
	absBase, err := t.fs.Resolve(base)
	if err != nil {
		return spi.Result{Hint: "path 必须在工作区内"}, err
	}
	// 目录/文件不存在**必须报错**：返回"0 处命中"会让模型以为
	// "这个目录里没有这个符号"，而真实情况是路径就错了。
	if _, statErr := os.Stat(absBase); statErr != nil {
		return spi.Result{
				Hint: "确认 path 拼写；先用 glob 列出目录，或把 path 去掉（默认搜当前工作区）",
			},
			fmt.Errorf("grep 搜索失败：%q 不存在", base)
	}

	var (
		matches []Match
		scanned int
		skipped int
		partial int // 扫到中途停下（某行超扫描缓冲）的文件数，见 payload.PartialFiles
		patSegs []string
	)
	if a.Include != "" {
		patSegs = strings.Split(strings.ReplaceAll(a.Include, "\\", "/"), "/")
	}

	// .dshdignore 生效：仓库自己声明的"别扫我"（例如 vendored 参考代码）。
	ig, _ := fsutil.LoadIgnore(t.fs.Root())
	var ignoredDirs int
	walkErr := fsutil.WalkFilesWith(absBase, fsutil.WalkOptions{
		Root: t.fs.Root(), Ignore: ig, Ignored: &ignoredDirs,
	}, func(abs string) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		relBase, err := filepath.Rel(absBase, abs)
		if err != nil {
			return nil
		}
		relBase = filepath.ToSlash(relBase)
		if patSegs != nil && !matchSegments(patSegs, strings.Split(relBase, "/")) {
			return nil
		}
		if st, err := os.Stat(abs); err != nil || st.Size() > maxFileBytes {
			skipped++
			return nil
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			skipped++
			return nil
		}
		head := data
		if len(head) > 8192 {
			head = head[:8192]
		}
		if fsutil.IsBinary(head) {
			skipped++
			return nil
		}
		scanned++

		rootRel, err := filepath.Rel(t.fs.Root(), abs)
		if err != nil {
			rootRel = relBase
		}
		rootRel = filepath.ToSlash(rootRel)

		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 0, 64*1024), scannerBufMax)
		lineNo := 0
		for sc.Scan() {
			lineNo++
			line := sc.Text()
			if !re.MatchString(line) {
				continue
			}
			line = previewLine(line, maxLineBytes)
			matches = append(matches, Match{Path: rootRel, Line: lineNo, Text: line})
			if len(matches) >= a.Limit {
				return errLimitReached
			}
		}
		// `bufio.Scanner` 遇到比缓冲还长的行会直接停：`Scan()` 返回 false、`Err()` 非 nil，
		// 而循环体一次都没进到那一行——**该文件后面的行一条都没扫**。
		//
		// 改前这里没有 `Err()` 检查，于是"文件只扫了 1 行"这件事在返回值里一点痕迹都没有：
		// count=0、skipped=0，提示还在说"确认 base 路径对不对"。那是把"没扫到"报成了
		// "不存在"，比报错贵得多。现在只加一个数与一句话，不改任何跳过语义。
		if serr := sc.Err(); serr != nil {
			partial++
			return nil
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, errLimitReached) {
		return spi.Result{Hint: "遍历失败；换个小一点的范围（path）再试"}, walkErr
	}

	truncated := errors.Is(walkErr, errLimitReached)
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Path != matches[j].Path {
			return matches[i].Path < matches[j].Path
		}
		return matches[i].Line < matches[j].Line
	})
	if matches == nil {
		matches = []Match{}
	}

	p := payload{
		Pattern:      a.Pattern,
		Base:         strings.ReplaceAll(base, "\\", "/"),
		Count:        len(matches),
		FilesScanned: scanned,
		FilesSkipped: skipped,
		Truncated:    truncated,
		PartialFiles: partial,
		Matches:      matches,
	}
	// 跳过的数字要分成两截说：目录名规则 + 仓库自己的 .dshdignore。
	// 混成一个数字的话，模型没法判断"是不是这一层把结果挡掉了"。
	skipNote := fmt.Sprintf("%d 个噪声目录", skipped)
	if ignoredDirs > 0 {
		skipNote += fmt.Sprintf(" + %d 个 .dshdignore 目录", ignoredDirs)
	}
	summary := fmt.Sprintf("%s 下 /%s/ 命中 %d 处（扫描 %d 文件，跳过 %s）",
		p.Base, a.Pattern, p.Count, scanned, skipNote)
	hint := ""
	switch {
	case len(matches) == 0 && ignoredDirs > 0:
		// 和 glob 同一个坑：0 命中可能是"被 .dshdignore 挡住了"，不是"真的没有"。
		hint = fmt.Sprintf("0 处命中，但有 %d 个目录被 .dshdignore 挡掉了；"+
			"确认要找的东西不在那些目录里，或把它从 .dshdignore 去掉", ignoredDirs)
	case len(matches) == 0:
		hint = "没有命中：确认 base 路径对不对，或把 pattern 放宽（这是正则，不是通配符）"
	case truncated:
		hint = fmt.Sprintf("已到 %d 条上限；用 include 收窄文件类型，或换更精确的 pattern", a.Limit)
	}
	// 有文件只扫了一半时，上面那句"没有命中：确认 base 路径对不对"就是**误导**：
	// 路径是对的，是那个文件后面的行根本没被扫。所以这句必须顶到最前面说。
	if partial > 0 {
		note := fmt.Sprintf("%d 个文件里有一行超过 %d KiB 的扫描上限，那些文件后面的行没扫到（结果可能不全，别据此断定「不存在」）",
			partial, scannerBufMax>>10)
		if hint == "" {
			hint = note
		} else {
			hint = note + "；" + hint
		}
	}
	return spi.Result{
		Summary: summary,
		Data:    p,
		Hint:    hint,
		View: &spi.ViewSpec{
			Kind:    spi.ViewTable,
			Title:   "grep " + a.Pattern,
			Columns: []string{"文件", "行", "内容"},
			Rows:    tableRows(matches),
		},
	}, nil
}

func tableRows(ms []Match) [][]string {
	rows := make([][]string, 0, len(ms))
	for _, m := range ms {
		rows = append(rows, []string{m.Path, fmt.Sprintf("%d", m.Line), m.Text})
	}
	return rows
}

// matchSegments 与 glob 工具同一套语义（** 跨目录）。
func matchSegments(pat, parts []string) bool {
	if len(pat) == 0 {
		return len(parts) == 0
	}
	if pat[0] == "**" {
		for i := 0; i <= len(parts); i++ {
			if matchSegments(pat[1:], parts[i:]) {
				return true
			}
		}
		return false
	}
	if len(parts) == 0 {
		return false
	}
	ok, err := path.Match(pat[0], parts[0])
	if err != nil || !ok {
		return false
	}
	return matchSegments(pat[1:], parts[1:])
}

// Module 返回装配用的模块描述。
func Module() kernel.Module {
	return kernel.Module{
		ID:       "tool-grep",
		Backend:  kernel.BackendStatic,
		Requires: []string{"tools", "fs"},
		Register: func(s *kernel.Scope) error {
			reg, err := kernel.Inject(s, spi.ToolsKey)
			if err != nil {
				return err
			}
			fsv, err := kernel.Inject(s, spi.FSKey)
			if err != nil {
				return err
			}
			return reg.Register(&Tool{fs: fsv})
		},
	}
}
