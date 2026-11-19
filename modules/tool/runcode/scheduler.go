package runcode

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"dshgo/internal/ptc"
	"dshgo/internal/spi"
)

// toolCallErrorName 是程序里"某个工具失败"时 reject 的错误类名（官方同一名字）。
const toolCallErrorName = "ToolCallError"

// toolCallErrorMember 是承载成员名的属性名（官方同一名字）。
const toolCallErrorMember = "toolName"

// limitError 复刻官方 `ToolCallLimitExceededError` 的**正文**。
//
// 正文是模型自纠的唯一依据，所以三件事一个都不能少：上限、被拒的调用序号、
// "可能是死循环"的提示。逐句照官方 `src/errors.ts:16-18`。
type limitError struct {
	limit      int64
	callNumber int64
	perMinute  int
}

func (e *limitError) Error() string {
	return fmt.Sprintf("run_code exceeded its limit of %d tool calls (%d per minute of its timeout); "+
		"call %d was not dispatched. The run_code program may contain an infinite loop. "+
		"Check loop termination or split the work into smaller run_code calls.",
		e.limit, e.perMinute, e.callNumber)
}

// scheduler 是程序内子调用的调度器：计数、并发与准入。
type scheduler struct {
	runner    spi.Runner
	registry  spi.ToolRegistry
	allowed   int64
	perMinute int

	attempts      atomic.Int64
	exhaustedFlag atomic.Bool

	// safe 是"并发安全"子调用的并发闸；exclusive 串行化其余调用（fail-closed）。
	safe      chan struct{}
	exclusive sync.Mutex

	cancel context.CancelFunc

	mu    sync.Mutex
	limit *limitError
}

func newScheduler(runner spi.Runner, registry spi.ToolRegistry, tools []spi.Tool, allowed int64, perMinute, parallel int, cancel context.CancelFunc) *scheduler {
	if parallel < 1 {
		parallel = 1
	}
	return &scheduler{
		runner:    runner,
		registry:  registry,
		allowed:   allowed,
		perMinute: perMinute,
		safe:      make(chan struct{}, parallel),
		cancel:    cancel,
	}
}

// namespace 把注册表里的工具变成程序可见的 `tools.<name>(args)`。
//
// 错误类固定为 ToolCallError/toolName：程序 `try/catch` 时能拿到"哪个工具失败了"，
// 这正是官方承诺的那条类型契约。
func (s *scheduler) namespace(tools []spi.Tool) ptc.BindingNamespace {
	namespace := ptc.BindingNamespace{
		Global:    "tools",
		Functions: make(map[string]ptc.BindingFunction, len(tools)),
		ErrorClass: &ptc.BindingErrorClass{
			Name:               toolCallErrorName,
			MemberNameProperty: toolCallErrorMember,
		},
	}
	for _, tool := range tools {
		name := tool.Name()
		target := tool
		namespace.Functions[name] = func(ctx context.Context, args any) (any, error) {
			return s.call(ctx, target, name, args)
		}
	}
	return namespace
}

// exhausted 报告这次运行有没有因为额度耗尽被终止。
func (s *scheduler) exhausted() bool { return s.exhaustedFlag.Load() }

// limitError 返回这次运行记下的额度错误。
func (s *scheduler) limitError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.limit != nil {
		return s.limit
	}
	return &limitError{limit: s.allowed, callNumber: 0, perMinute: s.perMinute}
}

// call 派发一次子调用。
//
// 顺序是刻意的：**先计数再派发**。官方原文 "All tool bindings share the count,
// including queued calls, invalid arguments, and failed tools"——把计数放在
// 参数校验之后，就等于允许"无限次失败调用"绕过额度。
func (s *scheduler) call(ctx context.Context, tool spi.Tool, name string, args any) (any, error) {
	attempt := s.attempts.Add(1)
	if attempt > s.allowed {
		s.trip(attempt)
		return nil, s.limitError()
	}

	raw, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("参数不是无损 JSON：%v", err)
	}
	release, err := s.acquire(ctx, tool, raw)
	if err != nil {
		return nil, err
	}
	defer release()

	result, runErr := s.runner.Run(ctx, name, raw)
	if runErr != nil {
		// 工具失败 → 绑定 reject（程序可以 catch）。正文保留工具自己的说法。
		return nil, fmt.Errorf("%s", oneLine(runErr.Error()))
	}
	if result.Info != nil && result.Info.Code == spi.CodeAborted {
		return nil, fmt.Errorf("tool call aborted")
	}
	if result.Data != nil {
		return result.Data, nil
	}
	if strings.TrimSpace(result.Text) != "" {
		return result.Text, nil
	}
	if result.Summary != "" {
		return result.Summary, nil
	}
	return nil, nil
}

// acquire 按工具的并发分类取闸。
//
// 分类走 `spi.ConcurrencySafe`（fail-closed：没声明、返回非 true、或分类器抛错都是独占），
// 与原生执行器同一套判据——PTC 不是"另一套并发语义"。
func (s *scheduler) acquire(ctx context.Context, tool spi.Tool, raw json.RawMessage) (func(), error) {
	if spi.ConcurrencySafe(tool, raw) {
		select {
		case s.safe <- struct{}{}:
			return func() { <-s.safe }, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	s.exclusive.Lock()
	return s.exclusive.Unlock, nil
}

// trip 记录额度耗尽并终止整个程序。
//
// 官方："The next attempt terminates the entire program through the runtime abort path,
// cancels active subcalls, and abandons queued calls"——所以这里 cancel 的是运行 ctx，
// 程序里 catch 住这条 reject 也救不回来（那正是"可能死循环"该有的行为）。
func (s *scheduler) trip(attempt int64) {
	s.mu.Lock()
	if s.limit == nil {
		s.limit = &limitError{limit: s.allowed, callNumber: attempt, perMinute: s.perMinute}
	}
	s.mu.Unlock()
	s.exhaustedFlag.Store(true)
	if s.cancel != nil {
		s.cancel()
	}
}

func oneLine(text string) string {
	line := strings.TrimSpace(text)
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = strings.TrimSpace(line[:idx])
	}
	return line
}
