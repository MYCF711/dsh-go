// Package plugin 提供 `plugin` 工具：列出、启用、停用本仓的模块（manifest 里的 [[module]]）。
//
// 为什么现在做：官方那条线有插件管理器与插件市场，我们的"插件"目前只有一层——
// 编译进二进制的 static 模块，靠 dsh.toml 白名单开合。**能管理的就是这一层**，
// 所以这里只做这一层，不假装能装卸 wasm/process（那两个后端还没实现，
// 声明它们会被 kernel 当场拒绝，见 internal/kernel/module.go 的 implementedBackends）。
//
// 三条安全约束，都是踩过坑才有的：
//   - 只认 manifest 里已经存在的 id：凭空写一个 id，下次启动就是一个**启动即失败**的配置；
//   - 改之前留 .bak，改完**当场重新解析**一遍，解析不过就回滚——而且回滚**写回后回读校验**，
//     写回失败不许说"已回滚"（回滚失败却报成功，用户以为配置干净、实际下次启动就炸）；
//   - 标明"下次启动生效"——本仓没有热重载，骗人说已生效是最坏的结果。
package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
)

var schema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "action": {"type": "string", "enum": ["list", "enable", "disable"], "description": "list=看有哪些模块；enable/disable=改 manifest（下次启动生效）"},
    "id": {"type": "string", "description": "模块 id，例如 tool-watch。enable/disable 时必填"}
  },
  "required": ["action"],
  "additionalProperties": false
}`)

// Tool 管 manifest 里那一层模块。
type Tool struct {
	manifest string

	// writeFile 是写文件的唯一入口；nil = os.WriteFile。
	// 只有测试会设它（注入"回滚写回失败"这类磁盘故障），生产装配从不设，
	// 行为与直接调 os.WriteFile 完全一致。
	writeFile func(name string, data []byte, perm os.FileMode) error
}

// write 把写动作收在一个入口上，好让测试能注入"写/回滚写失败"。
func (t *Tool) write(name string, data []byte, perm os.FileMode) error {
	if t.writeFile != nil {
		return t.writeFile(name, data, perm)
	}
	return os.WriteFile(name, data, perm)
}

func (t *Tool) Name() string { return "plugin" }

// Caps：list 只读，但 enable/disable 会改 dsh.toml，所以整体按"写"报。
// 不按 action 拆成两个工具：那样工具表多一条，而审批本来就会显示参数。
func (t *Tool) Caps() spi.Caps { return spi.Caps{Writes: true} }

func (t *Tool) Schema() json.RawMessage { return schema }

func (t *Tool) Description() string {
	return "看/开/关本仓的模块（dsh.toml 里的 [[module]]）：list 列出，enable/disable 改配置（下次启动生效）。"
}

type args struct {
	Action string `json:"action"`
	ID     string `json:"id"`
}

// entry 是 manifest 里的一个模块块（保留原始行号，便于原地改）。
type entry struct {
	ID      string
	Enabled bool
	Start   int // [[module]] 所在行
	End     int // 块结束（下一个 [[module]] 或文件尾）
}

func (t *Tool) Exec(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	var a args
	if err := json.Unmarshal(raw, &a); err != nil {
		return spi.Result{Hint: `参数形状：{"action":"list"} 或 {"action":"disable","id":"tool-watch"}`}, err
	}
	switch strings.TrimSpace(a.Action) {
	case "list":
		return t.list()
	case "enable", "disable":
		if strings.TrimSpace(a.ID) == "" {
			return spi.Result{Hint: "enable/disable 要指定 id；先用 {\"action\":\"list\"} 看有哪些"}, fmt.Errorf("缺 id")
		}
		return t.toggle(a.ID, a.Action == "enable")
	default:
		return spi.Result{Hint: `action 只能是 list / enable / disable`}, fmt.Errorf("未知 action: %q", a.Action)
	}
}

func (t *Tool) list() (spi.Result, error) {
	lines, entries, _, err := t.read()
	if err != nil {
		// 没有 manifest **不是错误**：宿主没有白名单时就是"全开"，
		// 这是合法配置。当成错误会让模型以为"插件系统坏了"，
		// 而其实只是这台机器没建过这个文件（真机上就是这种情况）。
		if os.IsNotExist(err) {
			return spi.Result{
				Summary: "没有 manifest 文件（默认全开）",
				Text: "没有找到 " + filepath.ToSlash(t.manifest) +
					"：这种情况下宿主不用白名单，编译进来的模块全开。\n" +
					"要能逐个开关，先建这个文件并在里面写 [[module]] 块。",
			}, nil
		}
		return spi.Result{
			Hint: "读不到 manifest：" + filepath.ToSlash(t.manifest) + "；确认路径可用（--manifest 指向哪里）",
		}, err
	}
	_ = lines

	var b strings.Builder
	on := 0
	for _, e := range entries {
		if e.Enabled {
			on++
		}
		state := "on "
		if !e.Enabled {
			state = "off"
		}
		b.WriteString("  " + state + " " + e.ID + "\n")
	}
	return spi.Result{
		Summary: fmt.Sprintf("manifest 里 %d 个模块，%d 个开着", len(entries), on),
		Text:    strings.TrimRight(b.String(), "\n"),
		Data: map[string]any{
			"manifest": filepath.ToSlash(t.manifest),
			"count":    len(entries),
			"enabled":  on,
		},
	}, nil
}

// toggle 改一个模块的 enabled。**改完必须重新解析**：解析不过就回滚，
// 而且回滚要回读校验——写失败/回滚失败时提示按真实状态说（见 writeHint）。
func (t *Tool) toggle(id string, enable bool) (spi.Result, error) {
	lines, entries, raw, err := t.read()
	if err != nil {
		return spi.Result{Hint: "读不到 manifest：" + filepath.ToSlash(t.manifest)}, err
	}

	var target *entry
	for i := range entries {
		if entries[i].ID == id {
			target = &entries[i]
		}
	}
	if target == nil {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.ID)
		}
		sort.Strings(names)
		return spi.Result{
			Hint: "manifest 里没有这个 id。现有：" + strings.Join(names, "、"),
		}, fmt.Errorf("没有模块 %q", id)
	}
	if target.Enabled == enable {
		state := "开着"
		if !enable {
			state = "已经关着"
		}
		return spi.Result{Summary: id + " " + state + "，没改动"}, nil
	}

	out := applyToggle(lines, *target, enable)
	state, err := t.writeGuarded(out, raw)
	if err != nil {
		return spi.Result{Hint: writeHint(state, t.manifest)}, err
	}

	verb := "已启用"
	if !enable {
		verb = "已停用"
	}
	return spi.Result{
		Summary: verb + " " + id + "（下次启动生效）",
		Text:    verb + " " + id + "。本仓没有热重载，**重启宿主后生效**；manifest：" + filepath.ToSlash(t.manifest),
		Data:    map[string]any{"id": id, "enabled": enable, "restartRequired": true},
	}, nil
}

// read 把文件拆成行 + 模块块。
func (t *Tool) read() ([]string, []entry, []byte, error) {
	raw, err := os.ReadFile(t.manifest)
	if err != nil {
		return nil, nil, nil, err
	}
	lines := strings.Split(string(raw), "\n")
	entries := parseEntries(lines)
	return lines, entries, raw, nil
}

// parseEntries 找出每个 [[module]] 块与它的 enabled。
func parseEntries(lines []string) []entry {
	var out []entry
	cur := -1
	for i, ln := range lines {
		if strings.TrimSpace(ln) == "[[module]]" {
			if cur >= 0 {
				out[len(out)-1].End = i
			}
			out = append(out, entry{Start: i, Enabled: true, End: len(lines)})
			cur = len(out) - 1
			continue
		}
		if cur < 0 {
			continue
		}
		s := strings.TrimSpace(ln)
		if v, ok := strings.CutPrefix(s, "id"); ok {
			v = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "="))
			if e := &out[cur]; e.ID == "" {
				e.ID = strings.Trim(v, `"`)
			}
		}
		if v, ok := strings.CutPrefix(s, "enabled"); ok {
			v = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "="))
			out[cur].Enabled = !strings.HasPrefix(v, "false")
		}
	}
	return out
}

// applyToggle 在块的 End 之前插入或删除 `enabled` 行。
func applyToggle(lines []string, e entry, enable bool) []string {
	if enable {
		out := make([]string, 0, len(lines))
		for i, ln := range lines {
			if i >= e.Start && i < e.End && strings.HasPrefix(strings.TrimSpace(ln), "enabled") {
				continue // 整行删掉 = 回到默认（开）
			}
			out = append(out, ln)
		}
		return out
	}
	// 插在本块**最后一个非空行**之后，而不是块尾。
	// 块尾前面常有一个空行，插过去就变成"空行 + enabled"，读起来像下一块的。
	insertAfter := e.End - 1
	for insertAfter > e.Start && strings.TrimSpace(lines[insertAfter]) == "" {
		insertAfter--
	}
	out := make([]string, 0, len(lines)+1)
	for i, ln := range lines {
		out = append(out, ln)
		if i == insertAfter {
			out = append(out, "enabled = false")
		}
	}
	return out
}

// 写完之后 manifest 在磁盘上的**真实**状态。
//
// 为什么要显式建模：以前写失败只回一句"已回滚"，而回滚写回的错误被 `_ =` 丢掉。
// 回滚真的失败时，模型/用户仍然以为配置是干净的，实际留下一个下次启动会解析
// 失败的 manifest——**没有症状的谎报**比直接失败更糟。
type writeState int

const (
	writeOK        writeState = iota // 改动已完整落盘（正常路径）
	writeUntouched                   // manifest 逐字节仍是原样：没有需要回滚的东西
	writeRestored                    // 改动落过盘，已写回原文并**回读校验**一致
	writeDirty                       // 改动可能仍在盘上，回滚没成功：绝不能说"已回滚"
)

// errWriteNotRestored 标记"manifest 已不是原样、而且没还原回去"。
// 用 errors.Is 判它决定提示；错误文本里也必须写明原文备份在哪。
var errWriteNotRestored = errors.New("manifest 未能还原")

// writeHint 按 writeState 说话：只有回读校验过才敢说"已回滚"。
func writeHint(state writeState, manifest string) string {
	slash := filepath.ToSlash(manifest)
	bak := slash + ".bak"
	switch state {
	case writeRestored:
		return "写入失败，manifest 已回滚到原样（回读校验一致；原样另存 " + bak + "）；检查文件是否只读或被占用"
	case writeDirty:
		return "写入失败，**回滚也没成功**：manifest 现在可能已不是原样（解析不过会让宿主下次启动失败）。" +
			"先把 " + bak + " 拷回 " + slash + "，再重试"
	default:
		return "写入失败，manifest 原样未改（改动没有落盘）；检查文件是否只读或被占用"
	}
}

// writeGuarded 写回并**当场重新解析**；解析不过就把原样写回去。
//
// 返回的 writeState 是写完之后磁盘上的真实状态，调用方的提示照着它说。
// 回滚（写回原文）本身也可能失败，所以写回之后要**回读比对**：只有逐字节
// 与原文相同才算回滚成功；写回报错但内容已对，也按成功算（内容才是判据）。
func (t *Tool) writeGuarded(lines []string, original []byte) (writeState, error) {
	body := strings.Join(lines, "\n")
	if err := t.write(t.manifest+".bak", original, 0o644); err != nil {
		// 备份都没写成：真正的 manifest 还没被碰过，没有需要回滚的东西。
		return writeUntouched, fmt.Errorf("留备份失败（manifest 原样未改）: %w", err)
	}
	if err := t.write(t.manifest, []byte(body), 0o644); err != nil {
		cause := fmt.Errorf("写 manifest 失败: %w", err)
		// os.WriteFile 先截断再写：报错不代表文件没变。**回读**才知道要不要
		// 回滚——不许凭"写报错了"就假设文件还干净。
		got, readErr := os.ReadFile(t.manifest)
		switch {
		case readErr == nil && bytes.Equal(got, original):
			return writeUntouched, fmt.Errorf("%v；回读确认文件仍与原样一致，没有需要回滚的改动", cause)
		case readErr != nil:
			return writeDirty, fmt.Errorf("%w：%v；回读 %s 也失败（%v），无法确认它还是原样；原文在 %s",
				errWriteNotRestored, cause, filepath.ToSlash(t.manifest), readErr, filepath.ToSlash(t.manifest+".bak"))
		default:
			return t.rollback(original, cause)
		}
	}
	if _, err := kernel.LoadManifest(t.manifest); err != nil {
		return t.rollback(original, fmt.Errorf("改完的 manifest 解析不过: %w", err))
	}
	return writeOK, nil
}

// rollback 把原文写回并**回读校验**；只有逐字节一致才算回滚成功。
//
// 以前这里是 `_ = os.WriteFile(...)`：写回失败没人知道，函数照样返回
// "…已回滚"。现在回滚失败返回 writeDirty + errWriteNotRestored，并把
// 原文备份位置、后果（下次启动失败）都写进错误里。
func (t *Tool) rollback(original []byte, cause error) (writeState, error) {
	slash := filepath.ToSlash(t.manifest)
	bak := slash + ".bak"
	writeErr := t.write(t.manifest, original, 0o644)
	got, readErr := os.ReadFile(t.manifest)
	if readErr == nil && bytes.Equal(got, original) {
		return writeRestored, fmt.Errorf("%v；已写回原文并回读校验一致（原样另存 %s）", cause, bak)
	}
	why := "回读内容与原样不一致"
	if readErr != nil {
		why = "回读失败: " + readErr.Error()
	}
	if writeErr != nil {
		why = fmt.Sprintf("写回失败: %v；%s", writeErr, why)
	}
	return writeDirty, fmt.Errorf(
		"%w：%v（%s；原文只保留在 %s。这份 manifest 现在可能解析不过，宿主下次启动会失败，需要人工把 .bak 拷回去）",
		errWriteNotRestored, cause, why, bak)
}

// Module 装配这个工具。
func Module(manifest string) kernel.Module {
	return kernel.Module{
		ID:       "tool-plugin",
		Backend:  kernel.BackendStatic,
		Requires: []string{"tools"},
		Register: func(s *kernel.Scope) error {
			reg, err := kernel.Inject(s, spi.ToolsKey)
			if err != nil {
				return err
			}
			return reg.Register(&Tool{manifest: manifest})
		},
	}
}
