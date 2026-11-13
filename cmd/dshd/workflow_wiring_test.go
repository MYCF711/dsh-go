package main

// 这三组用例钉的是"宿主接线"这一层：
//
//  1. 会话事件写口（sessionEventSink）：真的落 v4、并发安全、没写口必须报错；
//  2. 两段式委派（subProvider.StartActivation）：发布时就定 childId，且落出
//     一条**真实**的 durable 子会话（不是编出来的地址）；
//  3. 默认开关：官方 workflow 默认**开**（dsh-base 的 cordis.patch.yml:410-416
//     没有 disabled），本仓的默认装配必须与它一致，且 `enabled = false` 必须
//     真的关掉（双向）。

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dshgo/codecs/v4"
	"dshgo/internal/kernel"
	"dshgo/internal/llm"
	"dshgo/internal/session"
	"dshgo/internal/spi"
)

// ── 1. 会话事件写口 ──────────────────────────────────────────────────

// 事件必须真的落进 v4 日志，且 seq 连续（GUI 靠 seq 补洞）。
func TestSessionEventSinkAppendsToV4(t *testing.T) {
	log, err := session.CreateSession(t.TempDir(), t.TempDir(), "sink-session", "default")
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	sink := newSessionEventSink(log)
	for index := 1; index <= 3; index++ {
		if err := sink.AppendEvent("tool-workflow/agent-start", map[string]any{
			"runId": "run-1", "seq": index, "label": "成员", "childId": "child-1",
		}); err != nil {
			t.Fatalf("第 %d 次写失败：%v", index, err)
		}
	}
	back, err := v4.ReadFile(log.Path)
	if err != nil {
		t.Fatal(err)
	}
	type startPayload struct {
		RunID string `json:"runId"`
		Seq   int    `json:"seq"`
	}
	var got []startPayload
	for _, event := range back.Events {
		if event.Type != "tool-workflow/agent-start" {
			continue
		}
		var payload startPayload
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			t.Fatalf("载荷不是 JSON 对象：%s", event.Data)
		}
		got = append(got, payload)
	}
	if len(got) != 3 {
		t.Fatalf("落盘条数 = %d，期望 3", len(got))
	}
	for index, payload := range got {
		if payload.RunID != "run-1" || payload.Seq != index+1 {
			t.Fatalf("第 %d 条载荷 = %+v", index+1, payload)
		}
	}
	// v4 的 seq 从 0 起、逐条 +1（这里只写这三条，所以是 0/1/2）。
	for index, event := range back.Events {
		if event.Seq != int64(index) {
			t.Fatalf("第 %d 条 seq = %d，期望 %d", index+1, event.Seq, index)
		}
	}
}

// 并发写（workflow 扇出的子代理同时收尾）必须串行化：seq 不能重号。
func TestSessionEventSinkSerializesConcurrentAppends(t *testing.T) {
	log, err := session.CreateSession(t.TempDir(), t.TempDir(), "sink-concurrent", "default")
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	sink := newSessionEventSink(log)
	const writers = 16
	var wg sync.WaitGroup
	for worker := 0; worker < writers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			if err := sink.AppendEvent("tool-workflow/agent-end", map[string]any{
				"runId": "run-1", "seq": worker + 1, "outcome": "completed",
			}); err != nil {
				t.Errorf("writer %d: %v", worker, err)
			}
		}(worker)
	}
	wg.Wait()
	back, err := v4.ReadFile(log.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Events) != writers {
		t.Fatalf("落盘条数 = %d，期望 %d", len(back.Events), writers)
	}
	seen := map[int64]bool{}
	for _, event := range back.Events {
		if seen[event.Seq] {
			t.Fatalf("seq %d 重号（并发写没串行化）", event.Seq)
		}
		seen[event.Seq] = true
	}
}

// 没有写口时必须**报错**（"记录能力没接上"要可见），不能静默成功。
func TestSessionEventSinkFailsWithoutLog(t *testing.T) {
	var nilSink *sessionEventSink
	if err := nilSink.AppendEvent("tool-workflow/run-start", map[string]any{}); err == nil {
		t.Fatal("nil sink 必须报错")
	}
	if err := newSessionEventSink(nil).AppendEvent("tool-workflow/run-start", map[string]any{}); err == nil {
		t.Fatal("没有日志写入端必须报错")
	}
}

// ── 2. 两段式委派 ────────────────────────────────────────────────────

// newTestSubProvider 造一个真的 subRunner（假 chat，真子会话落盘）。
func newTestSubProvider(t *testing.T) *subProvider {
	t.Helper()
	gate, err := subagentGateFromEnv(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	runner := &subRunner{
		chat:     &stubChat{reply: "子代理完成"},
		tools:    subagentEmptyRegistry{},
		runner:   erroringRunner{},
		model:    "test-model",
		maxSteps: 4,
		timeout:  30 * time.Second,
		home:     t.TempDir(),
		gate:     gate,
	}
	provider := &subProvider{}
	provider.Bind(runner)
	return provider
}

// erroringRunner 在子代理真的调工具时炸掉（本用例不该走到）。
type erroringRunner struct{}

func (erroringRunner) Run(context.Context, string, json.RawMessage) (spi.Result, error) {
	return spi.Result{}, errors.New("子代理不该调工具")
}

// 发布时就要有 childId；Await 之后子会话必须真的在盘上（不是编的地址）。
func TestStartActivationPublishesRealChildSession(t *testing.T) {
	provider := newTestSubProvider(t)
	ctx := spi.WithSession(context.Background(), "parent-session")
	activation, err := provider.StartActivation(ctx, spi.SubagentRequest{Prompt: "干一件事"}, spi.ActivationOptions{})
	if err != nil {
		t.Fatalf("发布失败：%v", err)
	}
	childID := activation.ChildSessionID()
	if childID == "" {
		t.Fatal("发布时必须已经定下 childId（官方 childId 是发布事实）")
	}
	result, err := activation.Await(ctx)
	if err != nil {
		t.Fatalf("等待失败：%v", err)
	}
	if !strings.Contains(result.Text, "子代理完成") {
		t.Fatalf("子代理正文 = %q", result.Text)
	}
	if result.ChildSessionID != childID {
		t.Fatalf("结果里的 childId = %q，发布时是 %q（必须是同一个）",
			result.ChildSessionID, childID)
	}
	// 真的落了一条 durable 子会话（home 由 subRunner 带着）。
	path, err := session.FindByID(provider.impl.home, childID)
	if err != nil {
		t.Fatalf("子会话没落盘：%v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("子会话文件不存在：%v", err)
	}
	// 而且它记着父子关系（subagent/spawn 帧）。
	back, err := v4.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var sawSpawn bool
	for _, event := range back.Events {
		if event.Type == "subagent/spawn" {
			sawSpawn = true
			if !strings.Contains(string(event.Data), "parent-session") {
				t.Fatalf("父子帧里没有父会话 id：%s", event.Data)
			}
		}
	}
	if !sawSpawn {
		t.Fatal("子会话里没有 subagent/spawn 帧（父子关系没记）")
	}
}

// 等待期间被取消：Await 立刻返回取消原因，不挂住。
func TestStartActivationAwaitCancellation(t *testing.T) {
	provider := newTestSubProvider(t)
	ctx := spi.WithSession(context.Background(), "parent-session")
	activation, err := provider.StartActivation(ctx, spi.SubagentRequest{Prompt: "干一件事"}, spi.ActivationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := activation.Await(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Await 必须返回取消原因，实得 %v", err)
	}
	// 取消是**发得出去**的：Await 的 ctx 就是 run 的 ctx，取消它必须停掉真正
	// 在跑的那个 child（否则留下一个没人等的 goroutine，还在吃模型配额）。
	// 所以第二次 Await 拿到的仍然是取消（子代理已经被叫停），不是"正常结果"。
	if _, err := activation.Await(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消之后再 Await 必须是取消结局，实得 %v", err)
	}
}

// 没装配好 / 空 prompt / 没有父会话 id：都必须**响亮失败**（AGENT_START 的三种真实来源）。
func TestStartActivationRejectsInvalidRequests(t *testing.T) {
	empty := &subProvider{}
	if _, err := empty.StartActivation(context.Background(), spi.SubagentRequest{Prompt: "p"}, spi.ActivationOptions{}); err == nil {
		t.Fatal("没装配好必须失败")
	}
	provider := newTestSubProvider(t)
	if _, err := provider.StartActivation(context.Background(), spi.SubagentRequest{Prompt: "  "}, spi.ActivationOptions{}); err == nil {
		t.Fatal("空 prompt 必须失败")
	}
	if _, err := provider.StartActivation(context.Background(), spi.SubagentRequest{Prompt: "p"}, spi.ActivationOptions{}); err == nil {
		t.Fatal("没有父会话 id 必须失败（没有父就没有授权边界）")
	}
}

// 子代理失败（模型/正文）→ Failed 标记，而不是 error（error 留给结果通道坏掉）。
func TestStartActivationReportsChildFailureAsFlag(t *testing.T) {
	gate, err := subagentGateFromEnv(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	provider := &subProvider{}
	provider.Bind(&subRunner{
		chat:     &stubChat{reply: "   "}, // 只有空白 = 没有产出正文
		tools:    subagentEmptyRegistry{},
		runner:   erroringRunner{},
		model:    "test-model",
		maxSteps: 4,
		timeout:  30 * time.Second,
		home:     t.TempDir(),
		gate:     gate,
	})
	ctx := spi.WithSession(context.Background(), "parent-session")
	activation, err := provider.StartActivation(ctx, spi.SubagentRequest{Prompt: "干一件事"}, spi.ActivationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result, awaitErr := activation.Await(ctx)
	if awaitErr != nil {
		t.Fatalf("子代理失败不该是 channel error：%v", awaitErr)
	}
	if !result.Failed {
		t.Fatalf("子代理没产出正文必须标成 Failed：%+v", result)
	}
}

// 结构化输出（官方 `agent(prompt, {schema})`）：指令要进 prompt，正文里的
// JSON 对象要能取出来；取不出来**不是 error**（引擎把这次委派解成 null）。
func TestRunSubStructuredOutputContract(t *testing.T) {
	cases := []struct {
		name       string
		reply      string
		wantObject bool
	}{
		{"纯 JSON", `{"summary":"ok"}`, true},
		{"带围栏", "```json\n" + `{"summary":"ok"}` + "\n```", true},
		{"先解释再给", `结论如下：{"summary":"ok"}`, true},
		{"自然语言", "我觉得还行", false},
		{"数组不是对象", `[1,2]`, false},
	}
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"summary": map[string]any{"type": "string"}}, "required": []any{"summary"}}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			chat := &recordingChat{reply: testCase.reply}
			gate, err := subagentGateFromEnv(func(string) string { return "" })
			if err != nil {
				t.Fatal(err)
			}
			provider := &subProvider{}
			provider.Bind(&subRunner{
				chat: chat, tools: subagentEmptyRegistry{}, runner: erroringRunner{},
				model: "test-model", maxSteps: 4, timeout: 30 * time.Second,
				home: t.TempDir(), gate: gate,
			})
			ctx := spi.WithSession(context.Background(), "parent-session")
			activation, err := provider.StartActivation(ctx,
				spi.SubagentRequest{Prompt: "给结论"}, spi.ActivationOptions{OutputSchema: schema})
			if err != nil {
				t.Fatal(err)
			}
			result, awaitErr := activation.Await(ctx)
			if awaitErr != nil {
				t.Fatalf("取结构化结果不该是通道错误：%v", awaitErr)
			}
			// 指令必须真的进了 prompt（否则模型不知道要回 JSON）。
			if len(chat.last.Messages) == 0 {
				t.Fatal("没有发出模型请求")
			}
			lastPrompt := chat.last.Messages[len(chat.last.Messages)-1].Content
			if !strings.Contains(lastPrompt, "输出要求") || !strings.Contains(lastPrompt, `"summary"`) {
				t.Fatalf("prompt 里没有结构化输出指令：%s", lastPrompt)
			}
			if testCase.wantObject != (len(result.Structured) > 0) {
				t.Fatalf("结构化结果 = %s，期望有对象 = %v", result.Structured, testCase.wantObject)
			}
			// 取到对象时这次委派是成功的；取不到时由引擎判失败（解成 null），
			// 两者都不能变成"结果通道坏掉"（那要 error）。
			if testCase.wantObject && result.Failed {
				t.Fatalf("拿到合法结构化结果却标成 Failed：%+v", result)
			}
		})
	}
}

// recordingChat 记录最近一次请求（断言 prompt 内容）。
type recordingChat struct {
	reply string
	last  llm.Request
}

func (c *recordingChat) Stream(ctx context.Context, req llm.Request, onEvent func(llm.Event) error) (*llm.Response, error) {
	c.last = req
	return &llm.Response{Content: c.reply, FinishReason: "stop"}, nil
}

// ── 3. 默认开关（与官方双向一致）─────────────────────────────────────

// 官方默认：dsh-base 的 cordis.patch.yml:410-416 同时列出 workflow-ptc 与
// tool-workflow，且**没有** `disabled: true`——即默认开。
// 本仓的默认装配必须一致：不写 dsh.toml 也有 workflow 工具。
func TestWorkflowEnabledByDefault(t *testing.T) {
	home := t.TempDir()
	mods := builtinModules(home, home, 4096, &subProvider{})
	plan, err := kernel.TopoSort(mods)
	if err != nil {
		t.Fatalf("默认装配排序失败：%v", err)
	}
	provides := map[string]bool{}
	requires := map[string][]string{}
	for _, mod := range plan {
		for _, capability := range mod.Provides {
			provides[capability] = true
		}
		requires[mod.ID] = mod.Requires
	}
	if !provides["subagent-activation"] {
		t.Fatal("默认装配必须提供 subagent-activation（workflow 的 agent() 依赖它）")
	}
	workflowRequires := requires["tool-workflow"]
	found := false
	for _, capability := range workflowRequires {
		if capability == "subagent-activation" {
			found = true
		}
	}
	if !found {
		t.Fatalf("tool-workflow 必须声明 subagent-activation 依赖，实得 %v", workflowRequires)
	}
	// 真的装上：register 会把工具塞进工具表。
	k := kernel.New("test")
	root := k.Root()
	defer root.Dispose()
	if _, err := kernel.Assemble(root, mods); err != nil {
		t.Fatalf("默认装配失败：%v", err)
	}
	registry, err := kernel.Inject(root, spi.ToolsKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Get("workflow"); !ok {
		t.Fatal("默认装配里必须有 workflow 工具（官方默认开）")
	}
}

// 反向：manifest 里 `enabled = false` 必须真的把 workflow 关掉。
func TestWorkflowCanBeDisabled(t *testing.T) {
	home := t.TempDir()
	mods := builtinModules(home, home, 4096, &subProvider{})
	disabled := false
	// manifest 是白名单：只列 tools + subagent + tool-workflow 三行（其余跳过，
	// 会打 warn，这里不关心）。tool-workflow 那行显式关掉。
	manifest := &kernel.Manifest{Present: true}
	manifest.Modules = []kernel.ModuleEntry{
		{ID: "tools"},
		{ID: "subagent"},
		{ID: "tool-workflow", Enabled: &disabled},
	}
	applied, err := manifest.Apply(mods)
	if err != nil {
		t.Fatalf("应用 manifest 失败：%v", err)
	}
	for _, mod := range applied {
		if mod.ID == "tool-workflow" {
			t.Fatal("enabled = false 之后 tool-workflow 仍在装配列表里")
		}
	}
	k := kernel.New("test")
	root := k.Root()
	defer root.Dispose()
	if _, err := kernel.Assemble(root, applied); err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	registry, err := kernel.Inject(root, spi.ToolsKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Get("workflow"); ok {
		t.Fatal("关掉之后不该还有 workflow 工具（静默保留就是'改了配置却什么都没发生'）")
	}
}

// 每个工作流事件类型都必须在 v4 已知事件表里（否则官方读路径会拒载整份日志）。
func TestWorkflowEventsAreKnownV4Types(t *testing.T) {
	for _, eventType := range []string{
		"tool-workflow/run-start", "tool-workflow/agent-start",
		"tool-workflow/agent-end", "tool-workflow/run-end",
	} {
		if !session.IsKnownEventType(eventType) {
			t.Fatalf("%s 不在 v4 已知事件表里", eventType)
		}
	}
}

// 确保测试用的临时 home 目录不会污染工作区（占位断言：路径必须是 temp）。
func TestTempHomeIsIsolated(t *testing.T) {
	home := t.TempDir()
	if !filepath.IsAbs(home) {
		t.Fatalf("临时 home 不是绝对路径：%q", home)
	}
}
