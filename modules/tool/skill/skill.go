// Package skill 提供 skill 工具：按名字把一份 SKILL.md 全文取进上下文。
//
// 它只做"取文档"这一件事：不执行 skill、不解析 frontmatter、不缓存。
// 内容由人维护在 <dir>/<name>/SKILL.md，本工具只读。
package skill

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
)

// maxBody 是单次返回正文的字节上限；超过就截断，并在末尾留下可见说明。
const maxBody = 32 << 10

// truncatedTail 是截断时追加在正文末尾的说明。
const truncatedTail = "…（内容超过 32KB，已截断）"

var schema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "name": {
      "type": "string",
      "description": "skill 名，即 <dir>/<name>/SKILL.md 里的 <name>"
    }
  },
  "required": ["name"],
  "additionalProperties": false
}`)

// Tool 按名字加载 <dir>/<name>/SKILL.md。
type Tool struct{ dir string }

func (t *Tool) Name() string { return "skill" }

// Caps：只读本地文件，不改盘、不起进程、不联网，因此不需要审批。
func (t *Tool) Caps() spi.Caps { return spi.Caps{} }

func (t *Tool) Schema() json.RawMessage { return schema }

func (t *Tool) Description() string {
	return "按名字加载本机已安装的 skill 正文（<dir>/<name>/SKILL.md）。当某类任务的做法写在 skill 说明里、你需要那份步骤再动手时，先用它取全文。"
}

func (t *Tool) Exec(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	var args struct {
		Name string `json:"name"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return spi.Result{
				Hint: `参数形状是 {"name":"<skill 名>"}；name 必填、必须是字符串。`,
			}, skillErr("skill 参数不是合法 JSON 对象：" + err.Error())
		}
	}

	name := strings.TrimSpace(args.Name)
	if name == "" {
		return spi.Result{Hint: "缺 name：要加载哪个 skill？"}, skillErr("skill 缺少 name 参数")
	}

	dir := t.dir
	slashDir := filepath.ToSlash(dir)

	// 先确认根目录存在：它决定 hint 该说"没装任何 skill"还是"没有这个名字"。
	info, statErr := os.Stat(dir)
	if statErr != nil || !info.IsDir() {
		return spi.Result{
			Hint: "这台机器上没有装任何 skill（" + slashDir + " 不存在）。先建目录并放入 <名字>/SKILL.md。",
		}, skillErr("skill 目录不可用：" + slashDir)
	}

	// 防越界：name 里带 ../ 时 Join 会逃出 dir。这种情况一律按"没有这个 skill"处理，
	// 而不是去读目录外面的文件。
	path := filepath.Join(dir, name, "SKILL.md")
	if !underDir(dir, path) {
		return spi.Result{Hint: notFoundHint(dir, name)}, skillErr("没有名为「" + name + "」的 skill")
	}

	body, readErr := os.ReadFile(path)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return spi.Result{Hint: notFoundHint(dir, name)}, skillErr("没有名为「" + name + "」的 skill")
		}
		return spi.Result{
			Hint: "确认该文件可读（不是目录、没有被权限挡住），或换一个 skill 名。",
		}, skillErr("读不到 " + filepath.ToSlash(path) + "：" + readErr.Error())
	}

	text := string(body)
	chars := len([]rune(text))
	truncated := false
	if len(body) > maxBody {
		// 按字节切 32KB；切点可能落在多字节字符中间，ToValidUTF8 负责丢掉残缺尾字节。
		text = strings.ToValidUTF8(text[:maxBody], "") + truncatedTail
		truncated = true
	}

	return spi.Result{
		Text:    text,
		Summary: "已加载 skill「" + name + "」（" + itoa(chars) + " 字）",
		Data: map[string]any{
			"name":      name,
			"chars":     chars,
			"bytes":     len(body),
			"truncated": truncated,
		},
	}, nil
}

// skillErr 是本包的 error 类型。用它而不是 fmt.Errorf：本文件只许用上面那批 import，
// 而 spi.Result 已经承载了给人/给模型看的说明，error 只需说明"失败在哪"。
type skillErr string

func (e skillErr) Error() string { return string(e) }

// underDir 判断 p 是否落在 base 之内（跨盘符一律判否）。
func underDir(base, p string) bool {
	ab, err := filepath.Abs(base)
	if err != nil {
		return false
	}
	ap, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(ab, ap)
	if err != nil {
		return false // Windows 跨盘符会在这里报错
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// notFoundHint 列出目录里真实存在的 skill 名，让模型下一句能改对参数。
func notFoundHint(dir, name string) string {
	names := existingSkills(dir)
	if len(names) == 0 {
		return "没有名为「" + name + "」的 skill。目录是空的（" + filepath.ToSlash(dir) + "）。"
	}
	return "没有名为「" + name + "」的 skill。现有：" + strings.Join(names, "、")
}

// existingSkills 返回 dir 下含 SKILL.md 的子目录名，已排序。
func existingSkills(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, e.Name(), "SKILL.md")); err != nil {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// itoa 打印十进制整数。不引 strconv：本文件只许用上面那批 import。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// Module 把工具注册进 tools 服务。
func Module(dir string) kernel.Module {
	return kernel.Module{
		ID:       "tool-skill",
		Backend:  kernel.BackendStatic,
		Requires: []string{"tools"},
		Register: func(s *kernel.Scope) error {
			reg, err := kernel.Inject(s, spi.ToolsKey)
			if err != nil {
				return err
			}
			return reg.Register(&Tool{dir: dir})
		},
	}
}
