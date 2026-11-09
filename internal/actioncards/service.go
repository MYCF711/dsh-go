// Package actioncards 是官方 `actionCards.act` 的宿主运行时。
//
// 官方契约（app.asar 0.2.2-rc.1，@deepseek-ai/dsh-action-cards）：
//
//	propose_action(action,title,brief,details) -> {action,title,invitation}
//	actionCards.act(sessionId,callId,verb,payload) -> ActionCardResult
//	ActionCardResult = accepted | settled{decision} | refused{reason} | unconfirmed{reason}
//
// 本包实现三件事（不是静态成功）：
//  1. **propose_action 工具**：校验 action 已注册、details 通过 kind schema、
//     可选校验 kind skill 在本轮加载过；成功时只返回官方渲染需要的字段。
//  2. **kind 注册表**：实验包/插件通过 Register 注册 action、verbs、details、
//     dismiss/act 回调；同名 action 拒绝重复注册。
//  3. **真实会话投影**：proposal 从 v4 的 `tool/call` + 成功 `tool/result`
//     （name=propose_action）折叠；decision 从 `plugin:action-cards/decision`
//     记录折叠；act/record 都写回 v4，重启后从同一份日志恢复。
//
// 注意：本包不把 proposal 状态放在内存里。卡片的唯一事实源是会话 v4，
// 与官方投影一致；这也保证了 fork/重启后的恢复。
package actioncards

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"dshgo/internal/spi"
)

// ToolName 是官方卡工具名。
const ToolName = "propose_action"

// 官方 ActionCardResult.status 的四个可判别值。
const (
	StatusAccepted    = "accepted"
	StatusSettled     = "settled"
	StatusRefused     = "refused"
	StatusUnconfirmed = "unconfirmed"
)

// 官方共享的 dismiss 动作；每个卡片都提供，不需要 kind 注册。
const VerbDismiss = "dismiss"

// 投影里由日志派生的 phase；任何 kind 都不能把它写进 decision。
var derivedPhases = map[string]bool{
	"replaced": true,
}

// finalPhases 是官方语义里"已经结束、不再接受动作"的 phase。
// kind 自己的 openPhases 另行判断；这几个是共享终态。
var finalPhases = map[string]bool{
	"dismissed": true,
	"removed":   true,
	"replaced":  true,
}

// VerbPolicy 是 kind 对某个动词的访问策略。
type VerbPolicy struct {
	// Global 表示这个动作需要全局 Remote 访问（官方 policy.global）。
	Global bool `json:"global,omitempty"`
	// Writes 表示这个动作会写；read-only 会话拒绝（官方 policy.writes）。
	Writes bool `json:"writes,omitempty"`
}

// Kind 是一个注册到宿主的动作卡类型。
//
// Details 是 details 的校验器；nil 表示接受任意 JSON 对象。Act 非 nil 时
// 才会处理非 dismiss 的动词；返回 settled 的 Decision 会由 Service 写入 v4。
type Kind struct {
	Action     string
	Skill      string
	Invitation string
	Details    func(json.RawMessage) error
	Verbs      map[string]VerbPolicy
	OpenPhases []string
	// Reconcile 在 unconfirmed 卡每次动作（含 dismiss）前重读真实状态；
	// 返回非 nil decision 时直接落盘并 settled，不再执行原动作。
	Reconcile func(Proposal, Decision) (*Decision, error)
	Dismissal func(Proposal, map[string]json.RawMessage) (map[string]any, error)
	Act       func(context.Context, Proposal, string, map[string]json.RawMessage) (KindResult, error)
}

// KindResult 是 kind 执行一个动作后的真实结果。
type KindResult struct {
	Status   string
	Decision *Decision
	Reason   string
}

// Accepted 表示 kind 接受了后台工作；不立即写 decision。
func Accepted() KindResult { return KindResult{Status: StatusAccepted} }

// Settled 表示动作当场完成，并把 decision 写入日志。
func Settled(decision Decision) KindResult {
	return KindResult{Status: StatusSettled, Decision: &decision}
}

// Refused 是结构化拒绝（官方 reason）。
func Refused(reason string) KindResult {
	return KindResult{Status: StatusRefused, Reason: reason}
}

// Unconfirmed 表示动作可能已发生但无法确认。
func Unconfirmed(reason string) KindResult {
	return KindResult{Status: StatusUnconfirmed, Reason: reason}
}

// Proposal 是从会话日志折叠出来的卡片。
type Proposal struct {
	CallID  string         `json:"callId"`
	Turn    int64          `json:"turn"`
	Action  string         `json:"action"`
	Title   string         `json:"title"`
	Brief   string         `json:"brief"`
	Details map[string]any `json:"details"`
}

// Decision 是写进会话日志的卡片决定。
type Decision struct {
	CallID string         `json:"callId"`
	Action string         `json:"action"`
	Phase  string         `json:"phase"`
	Data   map[string]any `json:"data,omitempty"`
}

// View 是 session.projections 的 actionCards 值（官方 projection wire）。
type View struct {
	Proposals map[string]Proposal `json:"proposals"`
	Decisions map[string]Decision `json:"decisions"`
}

// Result 是 actionCards.act 的宿主内部结果；cmd 适配成 transport 的四变体。
type Result struct {
	Status   string    `json:"status"`
	Decision *Decision `json:"decision,omitempty"`
	Reason   string    `json:"reason,omitempty"`
}

// Service 是 actionCards 的宿主服务。
//
// home 是会话根目录；kinds 是进程内注册表；writeMu 串行化"读日志 + 追加
// decision"，保证并发动作不会写出两条互相覆盖的同一 phase。
type Service struct {
	home string

	mu    sync.RWMutex
	kinds map[string]*Kind
	// retired 记录曾经注册过的 action：unload 后的 in-flight decision 仍可写。
	retired map[string]bool

	writeMu sync.Mutex
}

// New 创建一个不与其它 home 共享的 Service（测试/嵌入用）。
func New(home string) *Service {
	return &Service{
		home:    home,
		kinds:   map[string]*Kind{},
		retired: map[string]bool{},
	}
}

// Register 注册一个 kind；同名 action 已存在时返回错误（官方 same-action 冲突）。
func (s *Service) Register(kind Kind) (func(), error) {
	kind.Action = strings.TrimSpace(kind.Action)
	if kind.Action == "" {
		return nil, fmt.Errorf("actioncards: kind.action 不能为空")
	}
	if kind.Action == VerbDismiss {
		return nil, fmt.Errorf("actioncards: %q 是共享动作，不能注册为 kind", VerbDismiss)
	}
	if kind.Verbs == nil {
		kind.Verbs = map[string]VerbPolicy{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.kinds[kind.Action]; exists {
		return nil, fmt.Errorf("actioncards: action %q 已经注册", kind.Action)
	}
	copyKind := kind
	s.kinds[kind.Action] = &copyKind
	dispose := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.kinds, kind.Action)
		s.retired[kind.Action] = true
	}
	return dispose, nil
}

// Actions 返回当前注册的 action（排序，便于诊断/错误文案）。
func (s *Service) Actions() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.kinds))
	for action := range s.kinds {
		out = append(out, action)
	}
	sort.Strings(out)
	return out
}

func (s *Service) kind(action string) (*Kind, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	kind, ok := s.kinds[action]
	return kind, ok
}

// Tool 返回官方 `propose_action` 工具实现。
func (s *Service) Tool() spi.Tool { return &proposeActionTool{service: s} }

// View 读取一个会话的真实卡片投影。
func (s *Service) View(sessionID string) (View, error) {
	events, err := s.readEvents(sessionID)
	if err != nil {
		return View{}, err
	}
	return Project(events), nil
}

// ViewMap 是 transport/session.projections hook 需要的 JSON 形状。
func (s *Service) ViewMap(sessionID string) (map[string]any, error) {
	view, err := s.View(sessionID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"proposals": view.Proposals,
		"decisions": view.Decisions,
	}, nil
}

// Act 是 actionCards.act 的真实实现。
func (s *Service) Act(ctx context.Context, sessionID, callID, verb string, payload map[string]json.RawMessage) (Result, error) {
	if strings.TrimSpace(sessionID) == "" {
		return Result{}, fmt.Errorf("actioncards: sessionId 不能为空")
	}
	if strings.TrimSpace(callID) == "" {
		return Result{}, fmt.Errorf("actioncards: callId 不能为空")
	}
	if strings.TrimSpace(verb) == "" {
		return Result{}, fmt.Errorf("actioncards: verb 不能为空")
	}
	events, err := s.readEvents(sessionID)
	if err != nil {
		return Result{}, err
	}
	view := Project(events)
	proposal, ok := view.Proposals[callID]
	if !ok {
		return Result{Status: StatusRefused, Reason: "unknown"}, nil
	}
	existing, hasDecision := view.Decisions[callID]
	kind, hasKind := s.kind(proposal.Action)
	if hasKind && kind.Reconcile != nil && hasDecision && existing.Phase == "unconfirmed" {
		reconciled, err := kind.Reconcile(proposal, existing)
		if err != nil {
			return Result{}, err
		}
		if reconciled != nil {
			decision := *reconciled
			if decision.CallID == "" {
				decision.CallID = proposal.CallID
			}
			if decision.Action == "" {
				decision.Action = proposal.Action
			}
			if derivedPhases[decision.Phase] {
				return Result{}, fmt.Errorf("actioncards: phase %q 由日志派生，kind 不能直接记录", decision.Phase)
			}
			if err := s.appendDecision(sessionID, decision); err != nil {
				return Result{}, err
			}
			return Result{Status: StatusSettled, Decision: &decision}, nil
		}
	}

	if verb == VerbDismiss {
		return s.dismiss(sessionID, proposal, kind, hasKind, existing, hasDecision, payload)
	}
	if !hasKind || kind.Act == nil {
		return Result{Status: StatusRefused, Reason: "unavailable"}, nil
	}
	if _, allowed := kind.Verbs[verb]; !allowed {
		return Result{Status: StatusRefused, Reason: "unavailable"}, nil
	}
	if hasDecision && (finalPhases[existing.Phase] || !openPhase(kind, existing.Phase)) {
		return Result{Status: StatusSettled, Decision: &existing}, nil
	}
	result, err := kind.Act(ctx, proposal, verb, payload)
	if err != nil {
		return Result{}, err
	}
	switch result.Status {
	case StatusAccepted:
		return Result{Status: StatusAccepted}, nil
	case StatusRefused:
		return Result{Status: StatusRefused, Reason: nonEmpty(result.Reason, "unavailable")}, nil
	case StatusUnconfirmed:
		if result.Decision != nil {
			if err := s.appendDecision(sessionID, *result.Decision); err != nil {
				return Result{}, err
			}
		}
		return Result{Status: StatusUnconfirmed, Decision: result.Decision, Reason: nonEmpty(result.Reason, "unconfirmed")}, nil
	case StatusSettled:
		if result.Decision == nil {
			return Result{}, fmt.Errorf("actioncards: kind %q 返回 settled 但 decision 为 nil", proposal.Action)
		}
		decision := *result.Decision
		if decision.CallID == "" {
			decision.CallID = proposal.CallID
		}
		if decision.Action == "" {
			decision.Action = proposal.Action
		}
		if derivedPhases[decision.Phase] {
			return Result{}, fmt.Errorf("actioncards: phase %q 由日志派生，kind 不能直接记录", decision.Phase)
		}
		if err := s.appendDecision(sessionID, decision); err != nil {
			return Result{}, err
		}
		return Result{Status: StatusSettled, Decision: &decision}, nil
	default:
		return Result{}, fmt.Errorf("actioncards: kind 返回未知 status %q", result.Status)
	}
}

func (s *Service) dismiss(sessionID string, proposal Proposal, kind *Kind, hasKind bool, existing Decision, hasDecision bool, payload map[string]json.RawMessage) (Result, error) {
	if hasDecision && finalPhases[existing.Phase] {
		return Result{Status: StatusSettled, Decision: &existing}, nil
	}
	if hasKind {
		if hasDecision && !openPhase(kind, existing.Phase) {
			return Result{Status: StatusSettled, Decision: &existing}, nil
		}
	} else if hasDecision {
		// 没有 kind 时只有"未启动"的卡可以被 Not now 关掉；已经记录过
		// 任何 phase 的卡拒绝（官方 unavailable）。
		return Result{Status: StatusRefused, Reason: "unavailable"}, nil
	}
	decision := Decision{CallID: proposal.CallID, Action: proposal.Action, Phase: "dismissed"}
	if hasKind && kind.Dismissal != nil {
		data, err := kind.Dismissal(proposal, payload)
		if err != nil {
			return Result{}, err
		}
		decision.Data = data
	}
	if err := s.appendDecision(sessionID, decision); err != nil {
		return Result{}, err
	}
	return Result{Status: StatusSettled, Decision: &decision}, nil
}

func openPhase(kind *Kind, phase string) bool {
	for _, open := range kind.OpenPhases {
		if open == phase {
			return true
		}
	}
	return false
}

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
