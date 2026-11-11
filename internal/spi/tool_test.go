package spi

// per-call 并发分类器与工具错误身份（官方 0.2.2-rc.2）的契约用例。
//
// 官方原件（`tmp/GO8-RC2/official-rc2/dsh/node_modules/@deepseek-ai/dsh-tools/`）：
//   - 声明形状：`isConcurrencySafe?(args: unknown): boolean`（src/contracts.ts:84）
//   - 判定点：`executionMode`（src/pipeline.ts:107-114、lib/pipeline.js:279-291）
//     —— 没有该方法、返回值不是 `true`、或抛错，一律 exclusive（fail-closed）
//   - 并发上限默认值：`maxParallelToolCalls` = 10（dsh-agent-loop/src）
//   - 错误身份：`error.info{name,code}`（lib/types/batch.js:238）
//   - 码表：`lib/types/error-codes.js:6-16`、`lib/types/contracts.js:19-21`

import (
	"context"
	"encoding/json"
	"testing"
)

// safeProbe 是"声明了逐调用分类器"的工具：返回值完全由用例给。
type safeProbe struct {
	classify func(args json.RawMessage) bool
	calls    *int // 可选：记录分类器被调了几次
}

func (p *safeProbe) Name() string            { return "safe" }
func (p *safeProbe) Description() string     { return "用例探针" }
func (p *safeProbe) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (p *safeProbe) Caps() Caps              { return Caps{} }
func (p *safeProbe) Exec(context.Context, json.RawMessage) (Result, error) {
	return Result{}, nil
}

func (p *safeProbe) ConcurrencySafe(args json.RawMessage) bool {
	if p.calls != nil {
		*p.calls++
	}
	if p.classify == nil {
		return false
	}
	return p.classify(args)
}

// undeclaredProbe **没有** ConcurrencySafe 方法：它就是"没接线"的绝大多数工具。
type undeclaredProbe struct{}

func (undeclaredProbe) Name() string                                          { return "undeclared" }
func (undeclaredProbe) Description() string                                   { return "用例探针" }
func (undeclaredProbe) Schema() json.RawMessage                               { return json.RawMessage(`{"type":"object"}`) }
func (undeclaredProbe) Caps() Caps                                            { return Caps{} }
func (undeclaredProbe) Exec(context.Context, json.RawMessage) (Result, error) { return Result{}, nil }

// 没接线 = 独占。这是 fail-safe 的默认值：新增一个工具、忘了接线，它只会变慢，
// 不会跟别人一起写同一个文件。
func TestConcurrencySafeDefaultsToExclusive(t *testing.T) {
	if got := ConcurrencySafe(undeclaredProbe{}, json.RawMessage(`{}`)); got {
		t.Fatal("没有声明分类器的工具必须判独占（官方：!tool?.isConcurrencySafe → exclusive）")
	}
	if got := ConcurrencySafe(nil, json.RawMessage(`{}`)); got {
		t.Fatal("nil 工具必须判独占")
	}
}

// 只有分类器返回 true 才是安全：false 仍然是独占。
func TestConcurrencySafeHonoursClassifier(t *testing.T) {
	if got := ConcurrencySafe(&safeProbe{classify: func(json.RawMessage) bool { return true }},
		json.RawMessage(`{}`)); !got {
		t.Fatal("分类器返回 true 时必须判为可并发")
	}
	if got := ConcurrencySafe(&safeProbe{classify: func(json.RawMessage) bool { return false }},
		json.RawMessage(`{}`)); got {
		t.Fatal("分类器返回 false 时必须判独占")
	}
}

// 判据是**每次调用的参数**，不是工具名：同一个工具换个参数就该换个分类。
func TestConcurrencySafeReceivesPerCallArguments(t *testing.T) {
	var calls int
	tool := &safeProbe{
		calls: &calls,
		classify: func(args json.RawMessage) bool {
			// 只认"只读"的那一种参数：这正是官方 `isConcurrencySafe(args)` 的形状。
			var a struct {
				Mode string `json:"mode"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return false
			}
			return a.Mode == "read"
		},
	}
	if !ConcurrencySafe(tool, json.RawMessage(`{"mode":"read"}`)) {
		t.Fatal("mode=read 必须判为可并发")
	}
	if ConcurrencySafe(tool, json.RawMessage(`{"mode":"write"}`)) {
		t.Fatal("mode=write 必须判独占")
	}
	if calls != 2 {
		t.Fatalf("分类器被调用 %d 次，期望 2 次：判定必须逐调用做", calls)
	}
}

// 分类器 panic → 独占（官方 `try { ... } catch { return {kind:'exclusive'} }`）。
// 不能让它把整批工具调度带崩：一个工具的分类器写错，最坏只该是"这条变慢"。
func TestConcurrencySafePanicIsExclusive(t *testing.T) {
	tool := &safeProbe{classify: func(json.RawMessage) bool { panic("分类器炸了") }}
	if got := ConcurrencySafe(tool, json.RawMessage(`{}`)); got {
		t.Fatal("分类器 panic 时必须判独占（官方 catch 归 exclusive）")
	}
}

// 官方错误身份原词逐条钉死。
//
// 为什么连字符串都要断言：这些码是**持久化契约**——它们会落进 v4 `tool/result`
// 的 `error.{name,code}`，客户端按码分支。拼错一个字符不会有人报错，
// 只会让模型永远分不清"人拒绝"和"没有审批通道"。
func TestOfficialErrorIdentities(t *testing.T) {
	for _, tc := range []struct {
		name, got, want string
	}{
		{"审批错误类名", ErrorNameApproval, "ApprovalError"},
		{"用户拒绝", CodeApprovalRejected, "APPROVAL_REJECTED"},
		{"审批被取消", CodeApprovalCancelled, "APPROVAL_CANCELLED"},
		{"没有审批通道", CodeApprovalUnavailable, "APPROVAL_UNAVAILABLE"},
		{"缺上下文类名", ErrorNameToolContextUnavailable, "ToolContextUnavailableError"},
		{"缺上下文", CodeToolContextUnavailable, "TOOL_CONTEXT_UNAVAILABLE"},
		{"取消类名", ErrorNameAbort, "AbortError"},
		// 注意：官方**常量名**是 TOOL_ABORTED，**值**是 "ABORTED"
		// （lib/types/contracts.js:19 `export const TOOL_ABORTED = 'ABORTED'`）。
		// 消费方比较的是值，所以这里必须是 ABORTED。
		{"工具体已被调用后取消", CodeAborted, "ABORTED"},
		{"派发前取消", CodeAbortedBeforeDispatch, "ABORTED_BEFORE_DISPATCH"},
	} {
		if tc.got != tc.want {
			t.Fatalf("%s = %q，官方原词是 %q", tc.name, tc.got, tc.want)
		}
	}
}

// 失败结果补身份时，原有字段一个都不能动：
//   - `Hint` 是 R3 的"下一步"契约，丢了就是退回"只说错、不说怎么办"；
//   - `Text` / `Data` / `Summary` 是工具真实产出（模型判断下一步的唯一依据）；
//   - `Truncated` / `Artifacts` 是外置与裁剪证据。
func TestResultWithErrorInfoKeepsExistingFields(t *testing.T) {
	base := Result{
		Summary:   "一行摘要",
		Data:      map[string]any{"k": "v"},
		Text:      "正文",
		Artifacts: []Artifact{{Name: "a", Path: "p", Bytes: 3}},
		Truncated: &Truncation{Kept: 1, Total: 2, Pointer: "p"},
		Hint:      "下一步",
	}
	got := base.WithErrorInfo(ErrorNameApproval, CodeApprovalRejected)
	if got.Info == nil || got.Info.Name != ErrorNameApproval || got.Info.Code != CodeApprovalRejected {
		t.Fatalf("Info = %+v，期望 ApprovalError/APPROVAL_REJECTED", got.Info)
	}
	if got.Hint != base.Hint || got.Text != base.Text || got.Summary != base.Summary {
		t.Fatalf("补身份改动了原有字段：%+v", got)
	}
	if got.Truncated == nil || got.Truncated.Pointer != "p" || len(got.Artifacts) != 1 {
		t.Fatalf("补身份丢了截断/外置证据：%+v", got)
	}
	// 成功结果不该凭空长出身份。
	if base.Info != nil {
		t.Fatal("未失败的 Result 不应带 Info")
	}
}
