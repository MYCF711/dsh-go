package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"unicode/utf8"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
)

// RunKey 是 Runner 的服务键。
var RunKey = kernel.NewKey[spi.Runner]("toolrunner")

// Options 是流水线的可调参数。默认值来自实测：dsc 的 spill 阈值 4000 字符。
type Options struct {
	SpillDir string // 外置目录；空 = <temp>/dshd-spill
	MaxChars int    // 超过就外置；<=0 = 4000
}

func (o Options) withDefaults() Options {
	if o.SpillDir == "" {
		o.SpillDir = filepath.Join(os.TempDir(), "dshd-spill")
	}
	if o.MaxChars <= 0 {
		o.MaxChars = 4000
	}
	return o
}

// Pipeline 实现 spi.Runner：三条硬约束都在这里，工具作者一行都不用写。
//
//	pre-execute（审批/沙箱，可 veto）→ execute → post-execute（外置/裁剪/提醒）→ result（广播）
type Pipeline struct {
	reg  spi.ToolRegistry
	bus  *kernel.Bus
	opts Options
	log  *slog.Logger

	mu         sync.Mutex
	violations []string // R3 违规记录：失败却没给 Hint
	seq        int
}

// NewPipeline 造一条流水线。opts 会被补默认值。
func NewPipeline(reg spi.ToolRegistry, bus *kernel.Bus, opts Options) *Pipeline {
	return &Pipeline{reg: reg, bus: bus, opts: opts.withDefaults(), log: slog.Default()}
}

// Violations 返回 R3 违规（失败路径没有 Hint）的快照，供测试与诊断使用。
func (p *Pipeline) Violations() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.violations...)
}

// Run 是唯一执行入口：绕过它直接调 Tool.Exec 会跳过审批、沙箱、截断与提示。
func (p *Pipeline) Run(ctx context.Context, name string, args json.RawMessage) (spi.Result, error) {
	call := &spi.Call{Name: name, Args: args}

	// 1. pre-execute：waterfall，可 veto。
	if _, err := p.bus.Waterfall(spi.EventToolPreExecute, call); err != nil {
		call.Err = err
		call.Result.Hint = "被策略拒绝：" + err.Error() + "；改用其它工具或先问人"
		return call.Result, call.Err
	}

	tool, ok := p.reg.Get(call.Name)
	if !ok {
		call.Err = fmt.Errorf("工具 %q 不存在", call.Name)
		call.Result.Hint = "用工具目录里真实存在的名字；不确定就先列出已注册的工具"
		return call.Result, call.Err
	}

	// 2. execute。
	p.bus.Emit(spi.EventToolExecute, call)
	call.Result, call.Err = tool.Exec(ctx, call.Args)

	// 3. 提示层：失败必须带下一步。缺失就兜底并记账（CI 应该让这种代码进不来）。
	if call.Err != nil && call.Result.Hint == "" {
		call.Result.Hint = fmt.Sprintf("工具 %q 失败了；先确认参数与环境再决定是否重试", call.Name)
		p.mu.Lock()
		p.violations = append(p.violations, call.Name)
		p.mu.Unlock()
		p.log.Warn("工具失败但没有给 Hint（R3）", "tool", call.Name, "error", call.Err)
	}

	// 4. 序列化层 + 截断层。
	if err := enforceSerialization(&call.Result); err != nil {
		call.Err = err
		call.Result.Hint = "结果无法序列化成 JSON；这是该工具实现的 bug，改用其它工具或修复它"
		return call.Result, call.Err
	}
	p.spill(&call.Result, call.Name)

	// 5. post-execute：可改写结果（裁剪、脱敏、补提示）。
	if _, err := p.bus.Waterfall(spi.EventToolPostExecute, call); err != nil {
		call.Err = err
		if call.Result.Hint == "" {
			call.Result.Hint = "后置策略拒绝了这次结果：" + err.Error()
		}
		return call.Result, call.Err
	}

	// 6. result：广播，不等待。
	p.bus.Emit(spi.EventToolResult, call)
	return call.Result, call.Err
}

// enforceSerialization 保证 Data 是合法 JSON（键名约定靠 review + 快照测试）。
func enforceSerialization(res *spi.Result) error {
	if res.Data == nil {
		return nil
	}
	if _, err := json.Marshal(res.Data); err != nil {
		return fmt.Errorf("结果 Data 不能序列化为 JSON: %w", err)
	}
	return nil
}

// spill 是截断层：超阈值外置成 artifact + 指针。
//
// 尽力而为：存不下去就保留内联，绝不把一次成功的调用变成失败；
// 替换体本身永远不超阈值，防止「取回 → 又被外置」的死循环。
func (p *Pipeline) spill(res *spi.Result, toolName string) {
	if res.Data == nil {
		return
	}
	raw, err := json.Marshal(res.Data)
	if err != nil || len(raw) <= p.opts.MaxChars {
		return
	}

	path, err := p.writeSpill(raw)
	if err != nil {
		p.log.Warn("结果外置失败，保留内联", "error", err)
		return
	}

	budget := p.opts.MaxChars / 4
	preview := truncateUTF8(string(raw), budget)
	res.Artifacts = append(res.Artifacts, spi.Artifact{Name: toolName, Path: path, Bytes: int64(len(raw))})
	res.Truncated = &spi.Truncation{Kept: len(preview), Total: len(raw), Pointer: path}
	res.Data = map[string]any{
		"spilled": true,
		"bytes":   len(raw),
		"preview": preview,
	}
	if res.Hint == "" {
		res.Hint = "结果已外置到 " + path + "；用 read 工具（offset/limit 分段）读全文"
	}
}

func (p *Pipeline) writeSpill(raw []byte) (string, error) {
	if err := os.MkdirAll(p.opts.SpillDir, 0o755); err != nil {
		return "", err
	}
	p.mu.Lock()
	p.seq++
	seq := p.seq
	p.mu.Unlock()

	f, err := os.CreateTemp(p.opts.SpillDir, fmt.Sprintf("spill-%04d-*.json", seq))
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(raw); err != nil {
		return "", err
	}
	return filepath.ToSlash(f.Name()), nil
}

// truncateUTF8 按字节预算截断，但不会切断一个多字节字符。
func truncateUTF8(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	s = s[:max]
	for len(s) > 0 {
		r, size := utf8.DecodeLastRuneInString(s)
		if r != utf8.RuneError || size > 1 {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}
