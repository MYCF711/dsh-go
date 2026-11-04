package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
)

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
	call := &spi.Call{Name: name, Args: args, Session: spi.SessionFrom(ctx)}
	// 子调用（workflow 里发的）标出来：宿主据此决定"这条要不要额外落 v4"。
	call.Nested = spi.SubDispatchFrom(ctx).Depth > 0
	started := time.Now()
	// 每次工具调用都在日志里留一条：名字、耗时、成败、错误原文。
	// 出事之后人要能回答"它到底跑了什么、跑了多久、成没成"。
	defer func() {
		attrs := []any{
			"tool", name,
			"ms", time.Since(started).Milliseconds(),
			"ok", call.Err == nil,
			"args_bytes", len(args),
		}
		if call.Err != nil {
			attrs = append(attrs, "error", call.Err.Error())
			p.log.Warn("工具调用失败", attrs...)
			return
		}
		p.log.Info("工具调用", attrs...)
	}()

	// 1. pre-execute：waterfall，可 veto。
	if _, err := p.bus.Waterfall(spi.EventToolPreExecute, call); err != nil {
		call.Err = err
		call.Result.Hint = "被策略拒绝：" + err.Error() + "；改用其它工具或先问人"
		return call.Result, call.Err
	}

	// 0. 参数必须是 JSON 对象。
	//
	// 模型偶尔会吐 `{"arguments": "{not json"` 这种。以前直接透传给工具——
	// 大多数工具自己 Unmarshal 会失败，但**不校验参数的工具会拿着垃圾照跑**。
	// 在执行前拦一次，比让每个工具各写一遍可靠。
	if len(call.Args) > 0 {
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(call.Args, &probe); err != nil {
			call.Err = fmt.Errorf("参数不是 JSON 对象: %w", err)
			call.Result.Hint = "tool_call.arguments 必须是 JSON 对象（键值对）；常见原因是引号没转义或写成了字符串"
			return call.Result, call.Err
		}
	}

	tool, ok := p.reg.Get(call.Name)
	if !ok {
		// 措辞对齐 DSH：这是最常见的一类失败（真实语料里 367 个失败样本中 84 个是它，
		// 因为工具集会随模式/配置变）。模型看到的名字和它调用的名字必须一致。
		call.Err = fmt.Errorf(`unknown tool %q`, call.Name)
		call.Result.Hint = "用工具表里真实存在的名字；不确定就先看当前模式给了哪些工具"
		return call.Result, call.Err
	}

	// 2. execute。
	p.bus.Emit(spi.EventToolExecute, call)
	call.Result, call.Err = tool.Exec(ctx, call.Args)

	// 2b. 成功、但什么都没返回 —— 静默成功的工具版。
	//
	// 模型会看到「（无输出）」：它不知道该不该相信这次调用真的做了什么，
	// 下一步只能瞎试。而流水线这边一切正常（ok=true、无 error），没人会去查。
	// 这里补一句人话 + 一条下一步，不改失败语义（err 仍然是 nil）。
	//
	// 判据是"模型看得见的字段全空"，不是"看起来短"：Data = [] 是**有效载荷**
	// （"集合是空的"本身就是答案），Summary 只有一行也算有产出。
	if call.Err == nil && isEmptySuccess(call.Result) {
		call.Result.Summary = fmt.Sprintf("工具 %q 执行成功，但没有返回任何模型可见的输出", call.Name)
		call.Result.Hint = "不要假设它做了什么：需要确认就用只读手段核实（读文件/看状态/查日志）；" +
			"这通常是该工具实现的问题，不是你的参数写错了"
		p.log.Warn("工具成功但没有产出（静默成功）", "tool", call.Name)
	}

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

// isEmptySuccess 判断一次**成功**的工具调用是不是什么载荷都没留下。
//
// 判据是"**模型**能不能看到东西"：Summary/Text/Data 是给模型的正文，
// Artifacts/Truncated 里有取回路径，Hint 是下一步，Images 会另起一条 user 消息发过去。
// 任何一样存在，这次调用就有东西可看。
//
// **View 不算**：那是给客户端画卡片的，模型看不到它。一个只返回 View 的工具
// 在界面上有内容、在模型眼里却是一行「（无输出）」——正是要抓的形状。
//
// 特别注意 Data 的 nil / 空数组之别：nil = 没有数据；[] = "集合是空的"，
// 后者是答案，不能当空结果处理（否则 glob 搜不到文件时会被说成工具坏了）。
func isEmptySuccess(res spi.Result) bool {
	return res.Summary == "" && res.Text == "" && res.Data == nil &&
		len(res.Artifacts) == 0 && res.Truncated == nil && res.Hint == "" &&
		len(res.Images) == 0
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
//
// **Text 也必须一起截**。只换 Data 的话，ProjectResult 会优先用 Text，
// 模型照样看到全文——那样 spill 等于没做（实测 9000 字符的输出，
// 阈值 4000，模型仍然看到 9361 字符）。
func (p *Pipeline) spill(res *spi.Result, toolName string) {
	// 先把 Text 截掉：无论走不走 Data 外置，模型看到的那段文本都要有界。
	textSpilled := false
	if len(res.Text) > p.opts.MaxChars {
		textSpilled = true
	}

	if res.Data == nil {
		// 没有结构化 Data 也不能放过：Text 本身超了就直接截。
		if textSpilled {
			p.spillText(res, textSpilled, "")
		}
		return
	}
	raw, err := json.Marshal(res.Data)
	if err != nil || len(raw) <= p.opts.MaxChars {
		if textSpilled {
			p.spillText(res, textSpilled, "")
		}
		return
	}

	path, err := p.writeSpill(raw)
	if err != nil {
		p.log.Warn("结果外置失败，保留内联", "error", err)
		if textSpilled {
			p.spillText(res, textSpilled, "")
		}
		return
	}

	// Data 里**不再放 preview**：模型看到的正文由 Text 承担（见 spillText），
	// 再塞一份预览等于同一段内容出现两次，而且预览是 JSON 串（多一层转义）。
	res.Artifacts = append(res.Artifacts, spi.Artifact{Name: toolName, Path: path, Bytes: int64(len(raw))})
	res.Truncated = &spi.Truncation{
		Kept:    len([]rune(res.Text)),
		Total:   len(raw),
		Pointer: path,
	}
	res.Data = map[string]any{
		"spilled":  true,
		"bytes":    len(raw),
		"artifact": path,
	}
	if textSpilled {
		p.spillText(res, textSpilled, path)
	}
	// 正文已经被截过（里面带着指针），就别再补一条同样意思的 Hint：
	// 投影层还会再加一条"已截断"说明，三条消息说同一件事纯属浪费。
	if res.Hint == "" && !textSpilled {
		res.Hint = "结果已外置到 " + path + "；用 read 工具（offset/limit 分段）读全文"
	}
}

// spillText 把模型可见正文压到预算内：留头留尾，中间换一行指针。
//
// 留尾巴是因为很多输出（日志、报错、命令结果）的关键信息在最后。
func (p *Pipeline) spillText(res *spi.Result, over bool, pointer string) {
	budget := p.opts.MaxChars * 3 / 4 // 留出指针/提示的位置，正文本身不超阈值
	head := budget * 3 / 4
	tail := budget - head

	orig := res.Text
	total := len([]rune(orig))
	r := []rune(orig)
	keepHead := r[:minInt(head, len(r))]
	keepTail := []rune{}
	if len(r) > head+tail {
		keepTail = r[len(r)-tail:]
	}

	var b strings.Builder
	b.WriteString(string(keepHead))
	b.WriteString("\n" + omittedNotice(string(keepHead), string(keepTail), total-head-tail, pointer) + "\n")
	b.WriteString(string(keepTail))
	res.Text = b.String()
	if res.Truncated == nil {
		res.Truncated = &spi.Truncation{Kept: head + tail, Total: total, Pointer: pointer}
	}
}

// omittedNotice 写省略说明。
//
// 带行号的输出（read/grep 渲染成 "N: 内容"）要说清**省略了哪几行、怎么一次取回**。
// 不然模型只知道"中间少了东西"，要么再试探一次，要么干脆放弃。
// 一次精确调用 vs 两次试探，差 5–6k token（见 docs/token-budget.md §6）。
func omittedNotice(head, tail string, omitted int, pointer string) string {
	from, to, ok := omittedLineRange(head, tail)
	if ok && to >= from && from > 0 {
		return fmt.Sprintf("…（第 %d–%d 行已省略；取回：read offset=%d limit=%d%s）…",
			from, to, from, to-from+1, pointerSuffix(pointer))
	}
	if pointer != "" {
		return fmt.Sprintf("…（中间省略 %d 字符，全文在 %s）…", omitted, pointer)
	}
	return fmt.Sprintf("…（中间省略 %d 字符）…", omitted)
}

// omittedLineRange 从头部最后一行号与尾部第一行号，推出被省略的行区间。
func omittedLineRange(head, tail string) (int, int, bool) {
	lastHead := lastLineNumber(head)
	firstTail := firstLineNumber(tail)
	if lastHead == 0 || firstTail == 0 {
		return 0, 0, false
	}
	return lastHead + 1, firstTail - 1, true
}

func lastLineNumber(s string) int {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if n, ok := lineNumber(lines[i]); ok {
			return n
		}
	}
	return 0
}

func firstLineNumber(s string) int {
	for _, ln := range strings.Split(s, "\n") {
		if n, ok := lineNumber(ln); ok {
			return n
		}
	}
	return 0
}

// lineNumber 认 "123: 内容" 这种渲染格式（行号最多 9 位）。
func lineNumber(line string) (int, bool) {
	i := strings.Index(line, ": ")
	if i <= 0 || i > 9 {
		return 0, false
	}
	n, err := strconv.Atoi(line[:i])
	if err != nil {
		return 0, false
	}
	return n, true
}

func pointerSuffix(pointer string) string {
	if pointer == "" {
		return ""
	}
	return "；或从 " + pointer + " 取全文"
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func itoa(n int) string {
	if n <= 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
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
