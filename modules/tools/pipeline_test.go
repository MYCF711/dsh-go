package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
)

var errTestBoom = errors.New("炸了")

type stubTool struct {
	name string
	res  spi.Result
	err  error
	ran  *bool
}

func (s *stubTool) Name() string        { return s.name }
func (s *stubTool) Description() string { return "stub" }

func (s *stubTool) Caps() spi.Caps          { return spi.Caps{} }
func (s *stubTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (s *stubTool) Exec(context.Context, json.RawMessage) (spi.Result, error) {
	if s.ran != nil {
		*s.ran = true
	}
	return s.res, s.err
}

func newPipe(t *testing.T, opts Options, list ...spi.Tool) (*Pipeline, *kernel.Kernel) {
	t.Helper()
	k := kernel.New("t")
	reg := New()
	for _, tl := range list {
		if err := reg.Register(tl); err != nil {
			t.Fatal(err)
		}
	}
	return NewPipeline(reg, k.Bus(), opts), k
}

func TestUnknownToolHasHint(t *testing.T) {
	p, k := newPipe(t, Options{})
	defer k.Root().Dispose()
	res, err := p.Run(context.Background(), "nope", nil)
	if err == nil {
		t.Fatal("未知工具必须报错")
	}
	if res.Hint == "" {
		t.Fatal("失败必须带 Hint（R3）")
	}
}

func TestPreExecuteCanVeto(t *testing.T) {
	ran := false
	tl := &stubTool{name: "x", ran: &ran, res: spi.Result{Summary: "不该跑到这里"}}
	p, k := newPipe(t, Options{}, tl)
	defer k.Root().Dispose()

	kernel.OnTyped(k.Bus(), k.Root(), spi.EventToolPreExecute, func(*spi.Call) error {
		return errors.New("沙箱拒绝")
	})
	res, err := p.Run(context.Background(), "x", nil)
	if err == nil || !strings.Contains(err.Error(), "沙箱拒绝") {
		t.Fatalf("veto 应中断并返回原因，得到 %v", err)
	}
	if ran {
		t.Fatal("被 veto 的工具不应执行")
	}
	if !strings.Contains(res.Hint, "策略拒绝") {
		t.Fatalf("veto 后的 Hint 不可行动: %q", res.Hint)
	}
}

func TestMissingHintIsPatchedAndRecorded(t *testing.T) {
	tl := &stubTool{name: "bad", err: errors.New("炸了")}
	p, k := newPipe(t, Options{}, tl)
	defer k.Root().Dispose()

	res, err := p.Run(context.Background(), "bad", nil)
	if err == nil {
		t.Fatal("应返回错误")
	}
	if res.Hint == "" {
		t.Fatal("流水线必须兜底 Hint")
	}
	v := p.Violations()
	if len(v) != 1 || v[0] != "bad" {
		t.Fatalf("R3 违规未记账: %v", v)
	}
}

func TestLargeDataIsSpilled(t *testing.T) {
	big := strings.Repeat("x", 500)
	tl := &stubTool{name: "big", res: spi.Result{Summary: "大结果", Data: map[string]any{"body": big}}}
	// 阈值要够装下"指针体"（含 artifact 路径）。64 字节连路径都放不下，
	// 那条"替换体不许超阈值"的规则在物理上就不可能满足。
	p, k := newPipe(t, Options{MaxChars: 400}, tl)
	defer k.Root().Dispose()

	res, err := p.Run(context.Background(), "big", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Truncated == nil {
		t.Fatal("超阈值必须标记 Truncated")
	}
	if len(res.Artifacts) != 1 {
		t.Fatalf("应有 1 个 artifact，得到 %d", len(res.Artifacts))
	}
	m, ok := res.Data.(map[string]any)
	if !ok || m["spilled"] != true {
		t.Fatalf("Data 应替换成指针体，得到 %#v", res.Data)
	}
	if res.Hint == "" || !strings.Contains(res.Hint, "read") {
		t.Fatalf("外置后必须告诉模型怎么取回，得到 %q", res.Hint)
	}

	// 原始全文必须完整落盘。
	raw, err := os.ReadFile(res.Truncated.Pointer)
	if err != nil {
		t.Fatalf("外部文件不存在: %v", err)
	}
	if !strings.Contains(string(raw), big) {
		t.Fatal("外置文件里不是完整原文")
	}
	// 替换体本身必须小于阈值，否则取回又会被外置。
	repl, _ := json.Marshal(res.Data)
	if len(repl) > p.opts.MaxChars {
		t.Fatalf("替换体 %d 字节超过阈值 %d", len(repl), p.opts.MaxChars)
	}
}

func TestSmallDataStaysInline(t *testing.T) {
	tl := &stubTool{name: "small", res: spi.Result{Summary: "小", Data: map[string]any{"n": 1}}}
	p, k := newPipe(t, Options{MaxChars: 4000}, tl)
	defer k.Root().Dispose()

	res, err := p.Run(context.Background(), "small", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Truncated != nil || len(res.Artifacts) != 0 {
		t.Fatal("小结果不该外置")
	}
}

func TestPostExecuteCanRewrite(t *testing.T) {
	tl := &stubTool{name: "x", res: spi.Result{Summary: "原始"}}
	p, k := newPipe(t, Options{}, tl)
	defer k.Root().Dispose()

	kernel.OnTyped(k.Bus(), k.Root(), spi.EventToolPostExecute, func(c *spi.Call) error {
		c.Result.Summary = "已脱敏"
		return nil
	})
	res, err := p.Run(context.Background(), "x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary != "已脱敏" {
		t.Fatalf("post-execute 的改写没生效: %q", res.Summary)
	}
}

// 调用工具表里没有的名字：措辞必须和 DSH 一致（真实语料里这是最常见的失败类，
// 367 个失败样本里 84 个是 `Error: unknown tool "bash"`）。
func TestUnknownToolWording(t *testing.T) {
	p, _ := newPipe(t, Options{}, &stubTool{name: "read"})
	_, err := p.Run(context.Background(), "no_such_tool", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("不存在的工具必须报错")
	}
	if err.Error() != `unknown tool "no_such_tool"` {
		t.Fatalf("措辞不一致: %q", err.Error())
	}
}

// 参数不是 JSON 对象时，**在执行前**就拦下。
//
// 让每个工具自己 Unmarshal 也行，但不校验参数的工具会拿着垃圾照跑。
func TestPipelineRejectsNonJSONArgs(t *testing.T) {
	ran := false
	p, k := newPipe(t, Options{}, &stubTool{name: "loose", ran: &ran})
	defer k.Root().Dispose()

	res, err := p.Run(context.Background(), "loose", json.RawMessage(`"{not json"`))
	if err == nil {
		t.Fatal("非 JSON 参数必须报错")
	}
	if ran {
		t.Fatal("参数不合法时不该执行工具")
	}
	if !strings.Contains(res.Hint, "JSON") {
		t.Fatalf("Hint 要说清是 JSON 的问题: %q", res.Hint)
	}
}

// 每次工具调用都要在日志里留一条：名字、耗时、成败、错误原文。
// 出事之后人要能回答"它到底跑了什么、跑了多久、成没成"。
func TestPipelineLogsEveryToolCall(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(prev)

	ran := false
	p, k := newPipe(t, Options{},
		&stubTool{name: "ok_tool", ran: &ran, res: spi.Result{Summary: "fine"}},
		&stubTool{name: "bad_tool", err: errTestBoom, res: spi.Result{Hint: "换个办法"}},
	)
	defer k.Root().Dispose()

	if _, err := p.Run(context.Background(), "ok_tool", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Run(context.Background(), "bad_tool", json.RawMessage(`{}`)); err == nil {
		t.Fatal("bad_tool 应该失败")
	}

	var okRec, errRec map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		switch rec["tool"] {
		case "ok_tool":
			okRec = rec
		case "bad_tool":
			errRec = rec
		}
	}
	if okRec == nil || errRec == nil {
		t.Fatalf("两次调用都该有日志，实际:\n%s", buf.String())
	}
	if okRec["ok"] != true || errRec["ok"] != false {
		t.Fatalf("成败要写清: ok=%v err=%v", okRec["ok"], errRec["ok"])
	}
	if _, has := okRec["ms"]; !has {
		t.Fatal("要记耗时")
	}
	if msg, _ := errRec["msg"].(string); !strings.Contains(msg, "失败") {
		t.Fatalf("失败那条的 msg 要说失败: %q", msg)
	}
	if e, _ := errRec["error"].(string); e == "" {
		t.Fatal("失败那条要带错误原文")
	}
}
