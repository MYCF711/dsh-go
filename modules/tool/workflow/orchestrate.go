// 编排面：官方 workflow 脚本的**数据等价物**。
//
// 官方的 script 是一段 JS，里面的 `agent()/phase()/log()/parallel()/pipeline()`
// 是 VM 里的全局钩子。本仓 R6 不嵌 JS 运行时，所以这里的 script 是**JSON 操作表**——
// 语法是数据，**语义逐条照官方**（发射点、事件顺序、上限、致命码都不变）。
//
//	[{"op":"phase","title":"审查"},
//	 {"op":"agent","prompt":"读改动并列出风险点","label":"读改动","save":"risks"},
//	 {"op":"log","message":"第一轮扇出完成"},
//	 {"op":"parallel","agents":[{"prompt":"跑全量测试"},{"prompt":"跑竞态"}]},
//	 {"op":"pipeline","items":["a.go","b.go"],"stages":[{"prompt":"读 ${item}"},{"prompt":"按 ${prev} 复核 ${item}"}]},
//	 {"op":"tool","tool":"grep","args":{"pattern":"TODO"},"save":"todos"},
//	 {"op":"return","value":{"risks":"${risks}"}}]
//
// 两种写法可以混：没有 `op` 字段的项按本仓既有的步骤表处理（tool/args/foreach/save），
// 已经写好的旧脚本一个字节都不用改。
package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"dshgo/internal/spi"
	"dshgo/internal/workflow"
)

// maxResultChars 是编排结果正文的上限（官方 tool-workflow 的 Config 默认值
// `maxResultChars: 50_000`，dsh-tool-workflow/src/index.ts:61）。
const maxResultChars = 50000

// opScript 是一次编排脚本的执行状态。
type opScript struct {
	tool   *Tool
	ctx    context.Context
	run    *workflow.Run
	items  []json.RawMessage
	vars   map[string]any
	traces []trace
	failed int
	stop   bool
	sub    spi.SubDispatch
	// recordErr 是耐久记录失败（官方 recorder 的 warn，dsh-tool-workflow/src/index.ts:106-112）。
	// 用一个小盒子而不是裸 error：写入发生在 run 执行期间（多个 goroutine），
	// 结算时才读，直接读裸字段会是数据竞争。
	recordErr *recordErrorBox
}

// recordErrorBox 是耐久记录失败的线程安全盒子。
type recordErrorBox struct {
	mu  sync.Mutex
	err error
}

func (b *recordErrorBox) set(err error) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err == nil {
		b.err = err
	}
}

func (b *recordErrorBox) get() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.err
}

// execOrchestrated 执行一段**含编排操作**的脚本。
//
// 前置顺序照官方：meta 校验（META_INVALID）→ 发布 run（AGENT_START）→ 执行。
// 脚本解析已经在调用方做过（SCRIPT_PARSE），因此这里的每个 item 都是合法 JSON。
func (t *Tool) execOrchestrated(ctx context.Context, a args, items []json.RawMessage) (spi.Result, error) {
	meta, err := workflow.ValidateMeta(a.Meta)
	if err != nil {
		return spi.Result{Hint: orchestrationStartHint(err)},
			fmt.Errorf("workflow run failed (%s): %s", workflow.CodeOf(err), err.Error())
	}
	recordErr := &recordErrorBox{}
	params := workflow.Params{
		Limits:          workflow.DefaultLimits(),
		Activator:       t.svc,
		ParentSessionID: spi.SessionFrom(ctx),
		OnRecordError: func(err error) {
			// 只报告、不中断执行（官方同一条纪律：打 warn 并停用记录，
			// dsh-tool-workflow/src/index.ts:106-112）。
			recordErr.set(err)
			fmt.Fprintf(os.Stderr, "tool-workflow: %v\n", err)
		},
	}
	// 只有**顶层**调用记录耐久帧：官方判据是 `exec.parent === undefined`
	// （dsh-tool-workflow/src/index.ts:443）。本仓的等价事实是子调用深度：
	// workflow 里再调 workflow（Depth>0）不落盘——它的 run 属于子调用，
	// 挂在父 run 的节点下面会让界面把两层混成一层。
	if spi.SubDispatchFrom(ctx).Depth == 0 {
		params.Sink = spi.EventSinkFrom(ctx)
	}
	engine := workflow.New(params)
	run, err := engine.Start(ctx, meta, spi.SessionFrom(ctx))
	if err != nil {
		return spi.Result{Hint: orchestrationStartHint(err)},
			fmt.Errorf("workflow run failed (%s): %s", workflow.CodeOf(err), err.Error())
	}
	defer run.Dispose()

	// `args` 是这个工具的入参（官方脚本里的 `args` 全局）：原样塞进变量表，
	// 脚本用 `${args}` 取。**不塞就是文档说谎**——schema 里写着可用，实际取不到。
	script := &opScript{
		tool: t, ctx: ctx, run: run, items: items, recordErr: recordErr,
		vars: map[string]any{"args": a.Args},
	}
	value, execErr := script.execute()
	if execErr != nil {
		result := run.Fail(execErr)
		if result.FatalCode == "" {
			// 取消路径上引擎已经结算成 cancelled（官方也不带码），但脚本抛出来的
			// 那个 *Error 是 CANCELLED——把码补回去，上层才路由得动。
			result.FatalCode = workflow.CodeOf(execErr)
		}
		return spi.Result{Hint: orchestrationFailureHint(meta, result, script)},
			fmt.Errorf("%s", orchestrationFailureMessage(result))
	}
	result := run.Complete(value)
	if result.StopReason != workflow.StopCompleted {
		return spi.Result{Hint: orchestrationFailureHint(meta, result, script)},
			fmt.Errorf("%s", orchestrationFailureMessage(result))
	}
	return orchestrationSuccess(meta, result, script), nil
}

// execute 顺序执行操作表，返回脚本的 `return` 值（没有就是 nil）。
func (s *opScript) execute() (any, error) {
	for index, raw := range s.items {
		var op map[string]any
		if err := json.Unmarshal(raw, &op); err != nil {
			return nil, workflow.NewError(workflow.CodeScriptParse,
				fmt.Sprintf("workflow script item %d is not an object: %v", index+1, err))
		}
		// `${var}` 整值替换：与既有步骤表同一套规则（saved 值可以喂给
		// prompt/args/items/schema 等任何位置）。
		expanded := expand(op, s.vars)
		// 第二遍是**子串**模板：`"用 ${files} 干活"` 这类内嵌引用。
		// 第一遍 expand 只处理"整个字符串就是一个 ${名}"（要保留非字符串值的类型），
		// 两遍合起来才是完整的模板语义。
		if templated, ok := expandTemplatesDeep(expanded, s.vars).(map[string]any); ok {
			expanded = templated
		}
		name := ""
		if raw, present := expanded["op"]; present {
			text, ok := raw.(string)
			if !ok || text == "" {
				return nil, workflow.NewError(workflow.CodeScriptParse,
					fmt.Sprintf("workflow script item %d has a non-string \"op\"", index+1))
			}
			name = text
		}
		if name == "" {
			// 无 op 字段 = 既有步骤表的一步（混写）；save 在 runStepItem 里处理。
			if _, err := s.runStepItem(index, expanded); err != nil {
				return nil, err
			}
			continue
		}
		finished, value, err := s.runOp(index, name, expanded)
		if err != nil {
			return nil, err
		}
		if finished {
			return value, nil
		}
	}
	// 官方：脚本没有 return 时 value 是 null（undefined → null）。
	return nil, nil
}

// runOp 执行一个编排操作；finished=true 表示脚本在这里 return。
func (s *opScript) runOp(index int, name string, op map[string]any) (finished bool, value any, err error) {
	switch name {
	case "phase":
		if err := assertOpFields(op, "phase", "title"); err != nil {
			return false, nil, err
		}
		title, ok := op["title"].(string)
		if !ok {
			return false, nil, workflow.NewError(workflow.CodeInvalidArgument,
				"phase() requires a non-empty title string")
		}
		if err := s.run.Phase(title); err != nil {
			return false, nil, err
		}
		return false, nil, nil
	case "log":
		if err := assertOpFields(op, "log", "message"); err != nil {
			return false, nil, err
		}
		message, ok := op["message"].(string)
		if !ok {
			return false, nil, workflow.NewError(workflow.CodeInvalidArgument,
				"log() requires a message string")
		}
		s.run.Log(message)
		return false, nil, nil
	case "agent":
		if err := s.saveTarget(op, "agent"); err != nil {
			return false, nil, err
		}
		prompt, present := op["prompt"]
		if !present {
			return false, nil, workflow.NewError(workflow.CodeInvalidArgument,
				"agent() requires a non-empty prompt string")
		}
		options := make(map[string]any, len(op))
		for key, entry := range op {
			if key == "op" || key == "prompt" || key == "save" {
				continue
			}
			options[key] = entry
		}
		result, err := s.run.Agent(s.ctx, prompt, options)
		if err != nil {
			return false, nil, err
		}
		s.save(op, result)
		return false, nil, nil
	case "parallel":
		if err := assertOpFields(op, "parallel", "agents", "save"); err != nil {
			return false, nil, err
		}
		if err := s.saveTarget(op, "parallel"); err != nil {
			return false, nil, err
		}
		agents, present := op["agents"]
		if !present {
			return false, nil, workflow.NewError(workflow.CodeInvalidArgument,
				"parallel() requires an array of agent specs")
		}
		result, err := s.run.Parallel(s.ctx, agents)
		if err != nil {
			return false, nil, err
		}
		s.save(op, result)
		return false, nil, nil
	case "pipeline":
		if err := assertOpFields(op, "pipeline", "items", "stages", "save"); err != nil {
			return false, nil, err
		}
		if err := s.saveTarget(op, "pipeline"); err != nil {
			return false, nil, err
		}
		items, present := op["items"]
		if !present {
			return false, nil, workflow.NewError(workflow.CodeInvalidArgument,
				"pipeline() requires an items array")
		}
		stages, present := op["stages"]
		if !present {
			return false, nil, workflow.NewError(workflow.CodeInvalidArgument,
				"pipeline() requires at least one stage")
		}
		result, err := s.run.Pipeline(s.ctx, items, stages)
		if err != nil {
			return false, nil, err
		}
		s.save(op, result)
		return false, nil, nil
	case "tool":
		if err := assertOpFields(op, "tool", "tool", "args", "foreach", "save"); err != nil {
			return false, nil, err
		}
		bare := make(map[string]any, len(op))
		for key, entry := range op {
			if key == "op" {
				continue
			}
			bare[key] = entry
		}
		if _, err := s.runStepItem(index, bare); err != nil {
			return false, nil, err
		}
		return false, nil, nil
	case "return":
		if err := assertOpFields(op, "return", "value"); err != nil {
			return false, nil, err
		}
		return true, op["value"], nil
	default:
		return false, nil, workflow.NewError(workflow.CodeScriptParse, fmt.Sprintf(
			"workflow script item %d declares unknown op %q "+
				"(supported: phase/log/agent/parallel/pipeline/tool/return)", index+1, name))
	}
}

// runStepItem 跑一步既有步骤表语义（tool/args/foreach/save），并把结果折进 traces。
//
// 与旧实现的语义逐条一致：工具自身失败**不中断**脚本（记 trace、failed++），
// 只有"通道级"失败（没有 SubDispatch / 嵌套过深）才向上抛。
func (s *opScript) runStepItem(index int, raw map[string]any) (any, error) {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, workflow.NewError(workflow.CodeScriptParse,
			fmt.Sprintf("workflow script item %d cannot be re-encoded: %v", index+1, err))
	}
	var st step
	if err := json.Unmarshal(encoded, &st); err != nil {
		return nil, workflow.NewError(workflow.CodeScriptParse,
			fmt.Sprintf("workflow script item %d is not a step: %v", index+1, err))
	}
	if strings.TrimSpace(st.Tool) == "" {
		return nil, workflow.NewError(workflow.CodeScriptParse,
			fmt.Sprintf("workflow script item %d is neither an op nor a step (missing \"tool\")", index+1))
	}
	if err := s.ensureSubDispatch(); err != nil {
		return nil, err
	}
	items, ok := lookupArray(s.vars, st.Foreach)
	if st.Foreach == "" || !ok {
		result, value, hardErr := runOne(s.ctx, s.sub.Runner, index, st, st.Args, s.vars)
		s.traces = append(s.traces, result)
		if hardErr != nil {
			return nil, hardFailure(hardErr)
		}
		s.absorbTrace(result)
		if st.Save != "" {
			s.vars[st.Save] = value
		}
		return value, nil
	}
	var collected []any
	for _, entry := range items {
		callArgs := make(map[string]any, len(st.Args)+1)
		for key, value := range st.Args {
			callArgs[key] = value
		}
		callArgs["item"] = entry
		result, value, hardErr := runOne(s.ctx, s.sub.Runner, index, st, callArgs, s.vars)
		result.Step = index
		result.Tool = st.Tool
		s.traces = append(s.traces, result)
		if hardErr != nil {
			return nil, hardFailure(hardErr)
		}
		s.absorbTrace(result)
		if result.OK {
			collected = append(collected, value)
		}
	}
	if st.Save != "" {
		s.vars[st.Save] = collected
	}
	return collected, nil
}

// absorbTrace 把一步结果的软失败/停止信号折进脚本状态。
func (s *opScript) absorbTrace(result trace) {
	if !result.OK {
		s.failed++
	}
	if result.stop {
		s.stop = true
	}
}

// ensureSubDispatch 取受管子调用通道；拿不到就按既有措辞明确失败。
func (s *opScript) ensureSubDispatch() error {
	if s.sub.Runner != nil {
		return nil
	}
	sub := spi.SubDispatchFrom(s.ctx)
	if sub.Runner == nil {
		return fmt.Errorf("这个环境没有编排通道（宿主没注入 SubDispatch）；改用普通工具一次一次调")
	}
	if sub.Depth >= spi.MaxSubDepth {
		return fmt.Errorf("嵌套太深（%d 层）：workflow 里不要再套 workflow，把它拆成两段", sub.Depth)
	}
	s.sub = spi.SubDispatch{Runner: sub.Runner, Depth: sub.Depth + 1}
	return nil
}

// save 把结果存进 vars（`save` 字段给了才存）。
func (s *opScript) save(op map[string]any, value any) {
	name, ok := op["save"].(string)
	if !ok || name == "" {
		return
	}
	s.vars[name] = value
}

// saveTarget 校验 `save` 字段类型（给了就必须是非空字符串）。
func (s *opScript) saveTarget(op map[string]any, hook string) error {
	raw, present := op["save"]
	if !present || raw == nil {
		return nil
	}
	name, ok := raw.(string)
	if !ok || name == "" {
		return workflow.NewError(workflow.CodeInvalidArgument,
			fmt.Sprintf("%s() option \"save\" must be a non-empty string", hook))
	}
	return nil
}

// assertOpFields 拒绝操作上的未知字段。
//
// 为什么必须拒绝：数据语言的拼写错误（`"mesage"`）不会像 JS 那样报
// ReferenceError，它会**静默少一条旁白**。多写一个字段就报错，比"脚本跑完了
// 但少了一半日志"便宜得多。
func assertOpFields(op map[string]any, hook string, allowed ...string) error {
	known := make(map[string]bool, len(allowed)+1)
	known["op"] = true
	for _, field := range allowed {
		known[field] = true
	}
	for key := range op {
		if known[key] {
			continue
		}
		return workflow.NewError(workflow.CodeInvalidArgument, fmt.Sprintf(
			"%s op has unsupported field %q (supported: %s)", hook, key, strings.Join(allowed, ", ")))
	}
	return nil
}

// expandTemplatesDeep 对操作里的**所有字符串**做子串模板替换。
//
// 为什么要递归：stage prompt 是嵌套的（`stages[].prompt`），只替换顶层
// `prompt` 会让 pipeline 里的 `${args}` 保持字面量——脚本"看起来跑了"，
// 但子代理收到的是 `${args}` 这段文本，没有任何地方会报错。
func expandTemplatesDeep(value any, vars map[string]any) any {
	switch typed := value.(type) {
	case string:
		return workflow.ExpandTemplates(typed, vars)
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, entry := range typed {
			out[key] = expandTemplatesDeep(entry, vars)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, entry := range typed {
			out[index] = expandTemplatesDeep(entry, vars)
		}
		return out
	}
	return value
}

// hardFailure 把通道级失败包成普通错误（不是致命码：它的原因是宿主没接线，
// 不是脚本写错）。
func hardFailure(cause error) error {
	return fmt.Errorf("%s", hardHint(cause))
}

// orchestrationSuccess 渲染成功结果。
//
// 正文照官方 `renderResult`（dsh-tool-workflow/src/index.ts:239-246）：
//
//	workflow "<name>" completed (<n> agents).
//	Return value:
//	<值 JSON>
func orchestrationSuccess(meta workflow.Meta, result workflow.Result, script *opScript) spi.Result {
	rendered := renderReturnValue(result.Value)
	var builder strings.Builder
	fmt.Fprintf(&builder, "workflow %q completed (%d agent%s).\nReturn value:\n",
		meta.Name, result.AgentsStarted, pluralAgents(result.AgentsStarted))
	builder.WriteString(rendered)
	if len(script.traces) > 0 {
		builder.WriteString("\nSteps:\n")
		builder.WriteString(renderTraceLines(script.traces))
	}
	text := clampText(builder.String(), maxResultChars)
	data := orchestrationData(meta, result, script)
	data["value"] = result.Value
	summary := fmt.Sprintf("workflow %q 完成（%d 个子代理）", meta.Name, result.AgentsStarted)
	if script.failed > 0 {
		summary += fmt.Sprintf("，%d 步失败", script.failed)
	}
	return spi.Result{Summary: summary, Text: text, Stop: script.stop, Data: data}
}

// orchestrationFailureHint 渲染失败时的 Hint。
//
// 失败路径上 agent 循环会丢 Text/Data（R3 已知行为），所以关键事实全部塞进
// Hint：致命码、错误原文、已启动的子代理数、以及"前面的步骤已经执行过"。
func orchestrationFailureHint(meta workflow.Meta, result workflow.Result, script *opScript) string {
	code := string(result.FatalCode)
	if code == "" {
		code = "NO_CODE"
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "workflow %q 失败（%s）：%s", meta.Name, code, result.Error)
	if result.AgentsStarted > 0 {
		fmt.Fprintf(&builder, "；已启动 %d 个子代理（它们的事件已经落盘，界面能看到）", result.AgentsStarted)
	}
	if len(script.traces) > 0 {
		fmt.Fprintf(&builder, "；已经执行过 %d 步：别当没发生，要重跑只把没跑完的部分写进 script", len(script.traces))
	}
	if recordErr := script.recordErr.get(); recordErr != nil {
		fmt.Fprintf(&builder, "；注意：耐久记录失败（%v）", recordErr)
	}
	return builder.String()
}

// orchestrationFailureMessage 是返回给 agent 循环的 error 文本（带码，供机器路由）。
func orchestrationFailureMessage(result workflow.Result) string {
	if result.FatalCode != "" {
		return fmt.Sprintf("workflow run failed (%s): %s", result.FatalCode, result.Error)
	}
	return "workflow run failed: " + result.Error
}

// orchestrationStartHint 是"run 都没发布"时的 Hint（META_INVALID / AGENT_START）。
func orchestrationStartHint(err error) string {
	code := string(workflow.CodeOf(err))
	if code == "" {
		return err.Error()
	}
	return fmt.Sprintf("workflow 脚本没有启动（%s）：%s", code, err.Error())
}

// orchestrationData 是编排结果的 Data 载荷。
func orchestrationData(meta workflow.Meta, result workflow.Result, script *opScript) map[string]any {
	data := map[string]any{
		"kind":          "workflow",
		"runId":         script.run.ID(),
		"name":          meta.Name,
		"description":   meta.Description,
		"stopReason":    result.StopReason,
		"agentsStarted": result.AgentsStarted,
	}
	if result.FatalCode != "" {
		data["fatalCode"] = result.FatalCode
	}
	if len(script.traces) > 0 {
		data["steps"] = script.traces
		data["failed"] = script.failed
		data["vars"] = script.vars
	}
	if recordErr := script.recordErr.get(); recordErr != nil {
		data["recordError"] = recordErr.Error()
	}
	return data
}

// renderReturnValue 把返回值渲染成 JSON（官方 renderResult 的同一步）。
//
// 编不出来就退回它的 Go 表示：run 已经判过 RESULT_UNSERIALIZABLE，能走到这里
// 说明 `json.Marshal` 失败只可能是"值在结算之后又被改了"这类宿主 bug，
// 这时让模型看到实际形状比什么都不给强。
func renderReturnValue(value any) string {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(encoded)
}

// renderTraceLines 把步骤 trace 折成几行（复用既有 buildResult 的格式）。
func renderTraceLines(traces []trace) string {
	var builder strings.Builder
	for _, entry := range traces {
		mark := "ok"
		if !entry.OK {
			mark = "ERR"
		}
		fmt.Fprintf(&builder, "%d. %s [%s] %s\n", entry.Step+1, entry.Tool, mark, entry.Brief)
	}
	return builder.String()
}

// clampText 按**字符**截断（官方按 length 截，会切坏代理对；本仓按 rune）。
func clampText(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + fmt.Sprintf("\n… [truncated: %d more characters]", len(runes)-limit)
}

// pluralAgents 是官方 `agent${n === 1 ? ” : 's'}` 的等价物。
func pluralAgents(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}
