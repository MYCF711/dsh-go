// Package edit 是逐字替换工具：old_string → new_string，默认要求唯一匹配。
//
// 「唯一匹配」是刻意的：多见于同一文件的重复片段，replace_all 是显式选择，
// 而不是默认行为——默认替换全部会把一次小改动放大成一次大改动。
package edit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
)

var schema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "file_path": {"type": "string", "description": "文件路径（正斜杠，相对工作区根）"},
    "old_string": {"type": "string", "description": "要被替换的原文，必须逐字匹配（含缩进）"},
    "new_string": {"type": "string", "description": "替换后的内容"},
    "replace_all": {"type": "boolean", "description": "默认 false：old_string 在文件里必须唯一"}
  },
  "required": ["file_path", "old_string", "new_string"],
  "additionalProperties": false
}`)

// Tool 实现 spi.Tool。
type Tool struct {
	fs spi.FS
}

func (t *Tool) Name() string   { return "edit" }
func (t *Tool) Caps() spi.Caps { return spi.Caps{Writes: true} }
func (t *Tool) Description() string {
	return "把文件中的 old_string 逐字替换为 new_string；要求唯一匹配，多处须显式 replace_all。"
}
func (t *Tool) Schema() json.RawMessage { return schema }

type args struct {
	FilePath   string `json:"file_path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

type payload struct {
	Path         string `json:"path"`
	Replacements int    `json:"replacements"`
	BytesBefore  int    `json:"bytes_before"`
	BytesAfter   int    `json:"bytes_after"`
}

func (t *Tool) Exec(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	var a args
	if err := json.Unmarshal(raw, &a); err != nil {
		return spi.Result{Hint: `参数不合法：期望 {"file_path","old_string","new_string","replace_all"?}`},
			fmt.Errorf("参数解析失败: %w", err)
	}
	if strings.TrimSpace(a.FilePath) == "" {
		return spi.Result{Hint: "file_path 不能为空"}, fmt.Errorf("file_path 为空")
	}
	if a.OldString == "" {
		return spi.Result{Hint: "old_string 不能为空；新建文件请用 write"}, fmt.Errorf("old_string 为空")
	}
	if a.OldString == a.NewString {
		return spi.Result{Hint: "old_string 与 new_string 相同，这次编辑没有任何效果"}, fmt.Errorf("替换内容相同")
	}

	if err := t.fs.CheckWritable(a.FilePath, "edit"); err != nil {
		return spi.Result{Hint: "改文件之前必须先 read 它，确认当前内容与你以为的一致"}, err
	}

	data, err := t.fs.Read(a.FilePath)
	if err != nil {
		return spi.Result{Hint: "确认路径存在；用 glob/grep 定位，不要猜"}, err
	}
	content := strings.ReplaceAll(string(data), "\r\n", "\n")

	count := strings.Count(content, strings.ReplaceAll(a.OldString, "\r\n", "\n"))
	switch {
	case count == 0:
		return spi.Result{
				Hint: "old_string 没找到：先 read 确认原文（注意缩进、行尾、大小写、空格），或改用 write 整体覆盖",
			},
			fmt.Errorf("在 %s 里找不到 old_string", a.FilePath)
	case count > 1 && !a.ReplaceAll:
		return spi.Result{
				Hint: fmt.Sprintf("old_string 在文件里出现 %d 次；给它更多上下文让它唯一，或显式传 replace_all=true", count),
			},
			fmt.Errorf("old_string 不唯一（%d 处）", count)
	}

	reps := count
	if !a.ReplaceAll {
		reps = 1
	}
	n := -1
	if !a.ReplaceAll {
		n = 1
	}
	updated := strings.Replace(content, strings.ReplaceAll(a.OldString, "\r\n", "\n"), a.NewString, n)
	if err := t.fs.Write(a.FilePath, []byte(updated)); err != nil {
		return spi.Result{Hint: "写入失败；确认路径可写"}, err
	}

	p := payload{
		Path:         strings.ReplaceAll(a.FilePath, "\\", "/"),
		Replacements: reps,
		BytesBefore:  len(data),
		BytesAfter:   len(updated),
	}
	return spi.Result{
		Summary: fmt.Sprintf("%s 替换 %d 处（%d → %d 字节）", p.Path, reps, p.BytesBefore, p.BytesAfter),
		Data:    p,
	}, nil
}

// Module 返回装配用的模块描述。
func Module() kernel.Module {
	return kernel.Module{
		ID:       "tool-edit",
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
