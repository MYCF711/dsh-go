// Package creation 是官方 `create_personalized_page` 动作卡 kind 的宿主实现。
//
// # 官方语义（@deepseek-ai/dsh-creation-recommendations 0.2.2-rc.2）
//
// 用户在卡片上点 Create 之后，后台**真的**生成一个独立插件包、校验、装进
// profile 的 `created-plugins`，把安装结果写回 decision：
//
//	decide(ctx, verb, payload)
//	  create : 校验 location → 立刻记 {phase:'creating', bundleName} → 起 Creator 子代理
//	           （subagents.startActivation，agentPreset:'cordis'，在
//	           <session.cwd>/.dsh-created-plugins/<uuid> 里写 npm 包）→
//	           validateGeneratedBundle → 名字占用检查 → cp 到
//	           <profile>/created-plugins/<uuid> → 再校验 →
//	           记 {phase:'installing'} → pluginManager.installBundle(directory) →
//	           记 {phase:'installed', application} 或 {phase:'failed'}
//	  undo   : 已安装/kept 的卡 → pluginManager.removeBundle → {phase:'removed', application}
//	  cancel : 中止正在跑的 Creator（没有 job 时回已有 decision）
//	  restore: removed 的卡 → 校验保留的 profile 副本 → 重新 installBundle
//	  reconcile: unconfirmed 卡每次动作前按真实 profile 复核（已装 → installed，否则 interrupted）
//
// # 官方每一步 → 宿主怎么落地（逐条，别含糊）
//
//	① location 校验（slot 枚举 + customLocation trim 后 1..1000）
//	    落地：parseLocation，与官方 zod 判据一致（长度按 rune 计，见该函数注释）。
//	② details 校验（{slot, initialContent?}）
//	    落地：validateDetails，propose_action 时就拒绝坏 details。
//	③ 记 {phase:'creating', bundleName} 再后台跑
//	    落地：`creating` 与 `installing` 都真的按官方顺序落盘（creating 在起
//	    Creator 之前、installing 在真装之前），所以"开始过、装到一半"在 v4 里
//	    看得见。
//	    落差：宿主没有卡片后台任务队列（kind 的回调是同步的），Create 会把
//	    生成→校验→复制→安装**同步**执行完再写最终 phase，`accepted` 这个
//	    中间返回在宿主不可达。另一个已知窗口：宿主只在 `unconfirmed` 卡上
//	    触发 Reconcile（actioncards 的既有语义），所以进程若**在 generate 中途
//	    被杀**，留下的 creating/installing 卡不会被自动复核成 interrupted，
//	    下一次动作会回 settled{phase:'creating'}；官方 recover() 会复核
//	    creating/installing/cancelled/failed/unconfirmed 全五档。生产接线没有
//	    Creator，走不到这里（create 在生成前就 refused），只影响注入 Creator 的接线。
//	④ Creator 子代理写插件包（npm + cordis.patch.yml + 已构建 JS 入口）
//	    **做不到**：本仓不嵌 JS 运行时（AGENTS R6），没有 `agentPreset:'cordis'`，
//	    也没有页面渲染 slot（transport 没有任何 slot/视图注册面）。生产接线不注入
//	    Creator，于是 `create` 在**写任何文件之前**返回
//	    refused{reason:'creator-unavailable'}——不写假包、不假装 installed。
//	    生成器由接线方通过 WithCreator 注入；注入后 ⑤⑥⑦ 全部真实执行。
//	⑤ validateGeneratedBundle（包身份、insert-only 补丁、入口必须已构建、包内
//	    路径与链接约束）
//	    落地：ValidateBundle（bundle.go），逐条对应官方判据；两处**更严**的差异
//	    （只收 JSON 形式补丁、完全禁 symlink）写在那个文件里。
//	⑥ cp 到 profile/created-plugins/<uuid> + 再校验
//	    落地：CopyBundle + ValidateBundle（目标必须不存在，不覆盖别人的包）。
//	⑦ pluginManager.installBundle(directory)
//	    落地：真调 internal/pluginmgr 的 Manager.Install。**这一步会真实失败**：
//	    本仓的安装器只接受 `dsh.local-plugin@1` 的 Go/WASM 工具插件，
//	    明确指出 Cordis/npm 形态不支持（internal/pluginmgr/manifest.go）。失败时
//	    记 {phase:'failed'}（官方 catch 分支同样是 failed），绝不记 installed。
//	    保留的 profile 副本不删：官方 restore 就从它重装。
//	⑧ undo / restore / cancel / reconcile
//	    落地：全部真实——undo 调 Manager.Uninstall，restore 先 ValidateBundle
//	    保留副本再真 Install，reconcile 用 Manager.Bundle 复核真实安装态。
//	⑨ 页面渲染（用户最终看到的那个页面）
//	    **做不到**：宿主没有页面渲染运行时，"页面装上了"这件事在本仓无意义；
//	    所以只有真正装进 pluginmgr 的包才会被记 installed，而它一定是被真实
//	    安装器接受的包。
//
// # 不静默成功
//
// 任何一步失败都写回 `failed`（并 logf 真实原因），不会写 `installed`；
// 生成前缺能力就 refused 并带机器可读 reason。`installed` 只在
// `Manager.Bundle(name)` 复核到真实安装态之后才写。
package creation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"dshgo/codecs/v4"
	"dshgo/internal/actioncards"
	"dshgo/internal/pluginmgr"
	"dshgo/internal/session"
)

// BundleManager 是本包依赖的真实 pluginManager 子集（*pluginmgr.Manager 满足它）。
type BundleManager interface {
	Bundles() ([]pluginmgr.BundleState, error)
	Bundle(name string) (pluginmgr.BundleState, bool, error)
	Install(source string) (pluginmgr.PluginInfo, error)
	Uninstall(id string) error
}

// CreateRequest 是交给 Creator 的完整计划。
//
// 字段与官方 `subagents.startActivation` 的 prompt 载荷逐项对应
// （task/title/brief/initialContent/location/directory/packageName），
// 只是用 Go 结构体表达，避免接线方自己拼 JSON 时漏字段。
type CreateRequest struct {
	// SessionID 是发起 Create 的会话（官方 Creator 的 parent）。
	SessionID string
	// Title / Brief 是卡片标题与简要说明（官方 prompt 的 title/brief）。
	Title string
	Brief string
	// Details 是官方 proposal.details（{slot, initialContent?}）原样。
	Details map[string]any
	// Slot 是用户最终选定的位置（官方 `card.slot`）。
	Slot string
	// CustomLocation 是用户手写的位置说明；HasCustomLocation 为 false 表示没写。
	CustomLocation    string
	HasCustomLocation bool
	// InitialContent 是"页面的既有内容"（官方 details.initialContent，可省略）。
	// HasInitialContent 区分"没写"与"写了空串"：官方对两者给 Creator 的提示词不同
	// （省略时 prompt 里没有 initialContent/contentPolicy 这两个字段）。
	InitialContent    string
	HasInitialContent bool
	// SourceDirectory 是**唯一**允许写包的地方（官方 sourceDirectory）。
	SourceDirectory string
	// BundleName 是宿主保留名（官方 bundleName）。
	BundleName string
}

// Creator 是官方 Creator 子代理在宿主的接缝：在 SourceDirectory 里写出一份
// 满足官方判据的插件包。
//
// 生产接线**没有**实现（宿主没有页面运行时/Cordis 生成器，见包文档 ④）：
// 没有注入时 create 会在生成前 refused{creator-unavailable}，一个字节都不写。
// 注入实现时 ⑤⑥⑦（校验、复制、真安装）会照官方顺序真的跑。
type Creator interface {
	Create(ctx context.Context, request CreateRequest) error
}

// Option 是 New 的可选接线项。
type Option func(*Service)

// WithCreator 注入一个真实生成器（生产默认没有，见 Creator 的注释）。
func WithCreator(creator Creator) Option {
	return func(service *Service) { service.creator = creator }
}

// WithLogf 注入日志回调（缺省丢弃）。
//
// 为什么不是 log 包直达：本包被 cmd 与测试共用，真实失败原因（例如安装器返回的
// unsupported_plugin）必须能进接线方的日志，也必须能被测试断言到。
func WithLogf(logf func(format string, args ...any)) Option {
	return func(service *Service) {
		if logf != nil {
			service.logf = logf
		}
	}
}

// Service 是 create_personalized_page kind 的宿主实现。
type Service struct {
	home    string
	bundles BundleManager
	cards   *actioncards.Service
	creator Creator
	logf    func(format string, args ...any)

	mu       sync.Mutex
	closing  bool
	disposed bool
	dispose  func()
}

// New 注册官方 kind 并返回服务。
//
// kind 注册失败（例如同名 action 已注册）返回错误，调用方不接线：
// 两张卡抢同一个 action 名会让 propose_action 与 act 看到不同的 schema。
func New(home string, bundles BundleManager, cards *actioncards.Service, opts ...Option) (*Service, error) {
	home = strings.TrimSpace(home)
	if home == "" {
		return nil, errors.New("creation: home 为空")
	}
	if bundles == nil {
		return nil, errors.New("creation: 缺 pluginManager")
	}
	if cards == nil {
		cards = actioncards.Open(home)
	}
	service := &Service{
		home: home, bundles: bundles, cards: cards,
		logf: func(string, ...any) {},
	}
	for _, option := range opts {
		if option != nil {
			option(service)
		}
	}
	dispose, err := cards.Register(actioncards.Kind{
		Action:     Action,
		Skill:      Skill,
		Invitation: Invitation,
		Details:    validateDetails,
		Verbs:      Verbs(),
		OpenPhases: []string{PhaseCancelled, PhaseFailed, PhaseInterrupted, PhaseUnconfirmed},
		Reconcile:  service.reconcile,
		Dismissal:  service.dismissal,
		Act:        service.act,
		// 官方四个动词都按 phase 分派（undo 在 installed/kept、restore 在 removed、
		// cancel 无后台任务、create 只在未完成 phase 上重试），所以四个动词全部
		// 交给 kind 自己判断 phase，不套宿主的默认闸门（见 actioncards.Kind.DelegateVerbs）。
		DelegateVerbs: map[string]bool{
			VerbCreate: true, VerbUndo: true, VerbCancel: true, VerbRestore: true,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("creation: 注册 %s kind 失败: %w", Action, err)
	}
	service.dispose = dispose
	return service, nil
}

// Verbs 返回官方的动词策略（src/index.ts `verbs:`，逐字）：
//
//	create: {global:true, writes:true}  undo: {global:true}
//	cancel: {}                          restore: {global:true}
func Verbs() map[string]actioncards.VerbPolicy {
	return map[string]actioncards.VerbPolicy{
		VerbCreate:  {Global: true, Writes: true},
		VerbUndo:    {Global: true},
		VerbCancel:  {},
		VerbRestore: {Global: true},
	}
}

// OpenPhases 返回官方 openPhases（src/index.ts，逐字）：这些 phase 的卡可以重试。
func OpenPhases() []string {
	return []string{PhaseCancelled, PhaseFailed, PhaseInterrupted, PhaseUnconfirmed}
}

// Close 停用服务并摘掉 kind（测试/关停用）。
func (s *Service) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.disposed {
		s.mu.Unlock()
		return
	}
	s.closing = true
	s.disposed = true
	dispose := s.dispose
	s.mu.Unlock()
	if dispose != nil {
		dispose()
	}
}

func (s *Service) isClosing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closing
}

// DetailsOf 从 proposal.details 里读官方字段（给接线方/诊断用，不做校验）。
func DetailsOf(details map[string]any) (slot string, initialContent string, hasInitialContent bool) {
	if details == nil {
		return "", "", false
	}
	if text, ok := details["slot"].(string); ok {
		slot = strings.TrimSpace(text)
	}
	if text, ok := details["initialContent"].(string); ok {
		initialContent = text
		hasInitialContent = true
	}
	return slot, initialContent, hasInitialContent
}

// ---------------------------------------------------------------------------
// details / location 校验（官方 zod schema 的 Go 落地）
// ---------------------------------------------------------------------------

// validateDetails 校验 propose_action 的 details（官方 detailsSchema）。
//
// 官方是 `z.object({slot: z.enum([...]), initialContent: z.string().optional()})`
// ——非 strict，所以未知键允许（客户端会用它们判断是否重新提案）。
func validateDetails(raw json.RawMessage) error {
	var object map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&object); err != nil || object == nil {
		return errors.New(`details 必须是对象，形如 {"slot":"sidebar-right","initialContent":"…"}`)
	}
	slot, ok := object["slot"].(string)
	if !ok || !knownSlot(slot) {
		return fmt.Errorf("details.slot 必填，且只能是 %s", strings.Join(Slots(), " / "))
	}
	if value, present := object["initialContent"]; present {
		if _, ok := value.(string); !ok {
			return errors.New("details.initialContent 必须省略或是一个字符串")
		}
	}
	return nil
}

// Location 是一次 Create / Not now 选定的位置（官方 PageLocation）。
type Location struct {
	Slot              string
	CustomLocation    string
	HasCustomLocation bool
}

// parseLocation 校验 Create / Not now 的 payload（官方 locationSchema =
// `z.object({slot: slotSchema, customLocation: customLocationSchema.optional()})`）。
//
// customLocationSchema = `z.string().trim().min(1).max(1000)`：先 trim 再量长度，
// 所以"全空白"等于空串（不合法），而不是合法但无意义。长度按 rune 计
// （官方 JS 按 UTF-16 码元；中文场景下 rune 更接近"字符"，差异写在测试里）。
func parseLocation(payload map[string]json.RawMessage) (Location, bool) {
	if payload == nil {
		return Location{}, false
	}
	rawSlot, ok := payload["slot"]
	if !ok {
		return Location{}, false
	}
	var slot string
	if err := json.Unmarshal(rawSlot, &slot); err != nil || !knownSlot(slot) {
		return Location{}, false
	}
	location := Location{Slot: slot}
	if rawCustom, ok := payload["customLocation"]; ok {
		var text string
		if err := json.Unmarshal(rawCustom, &text); err != nil {
			return Location{}, false
		}
		trimmed := strings.TrimSpace(text)
		if trimmed == "" || len([]rune(trimmed)) > 1000 {
			return Location{}, false
		}
		location.CustomLocation = trimmed
		location.HasCustomLocation = true
	}
	return location, true
}

func knownSlot(slot string) bool {
	for _, candidate := range Slots() {
		if slot == candidate {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// kind 回调
// ---------------------------------------------------------------------------

// act 分发官方四个动词。
//
// 走到这里说明 Service 已经确认：这张卡存在、verb 在 Verbs 里。四个动词都声明了
// DelegateVerbs（官方 kind 的 act 自己按 phase 分派），所以 phase 判断在本文件里
// 逐个阶段写清。当前 decision 由 Service 通过 ctx 传下来
// （actioncards.DecisionFrom），不需要本包自己再读一遍投影。
func (s *Service) act(ctx context.Context, proposal actioncards.Proposal, verb string, payload map[string]json.RawMessage) (actioncards.KindResult, error) {
	if s.isClosing() {
		return actioncards.Refused(ReasonClosing), nil
	}
	actCtx, _ := actioncards.ActContextFrom(ctx)
	decision, hasDecision := actioncards.DecisionFrom(ctx)
	switch verb {
	case VerbCreate:
		return s.create(ctx, actCtx, proposal, decision, hasDecision, payload)
	case VerbUndo:
		return s.undo(ctx, actCtx, decision, hasDecision)
	case VerbCancel:
		return s.cancel(decision, hasDecision), nil
	case VerbRestore:
		return s.restore(ctx, actCtx, decision, hasDecision)
	default:
		return actioncards.Refused(ReasonUnavailable), nil
	}
}

func (s *Service) create(ctx context.Context, actCtx actioncards.ActContext, proposal actioncards.Proposal, decision actioncards.Decision, hasDecision bool, payload map[string]json.RawMessage) (actioncards.KindResult, error) {
	location, ok := parseLocation(payload)
	if !ok {
		// 官方：Create 必须有合法 location，否则 refused{invalid-location}，
		// 卡片保持原样（不写 decision、不算已开始）。
		return actioncards.Refused(ReasonInvalidLocation), nil
	}
	if hasDecision {
		// 官方只允许在"没完成/可重试"的 phase 上重新 Create；
		// 其它 phase（installed/kept/creating/installing/removed/dismissed）
		// 直接回已记录的 decision。
		switch decision.Phase {
		case PhaseCancelled, PhaseFailed, PhaseInterrupted, PhaseUnconfirmed:
		default:
			return actioncards.Settled(decision), nil
		}
	}
	header, err := s.liveRoot(actCtx.SessionID)
	if err != nil {
		s.logf("create_personalized_page: 会话 %q 不是可建页面的 live root: %v", actCtx.SessionID, err)
		return actioncards.Refused(ReasonNotLiveRoot), nil
	}
	if s.creator == nil {
		// 官方这一步起 Creator 子代理写包。宿主没有这条能力（包文档 ④），
		// 所以在**写任何文件之前**如实拒绝：不写假包、不记 installed。
		s.logf("create_personalized_page: 拒绝 create（%s）：宿主没有 Creator 生成器与页面渲染运行时，"+
			"无法生成官方要求的 Cordis/npm 插件包（AGENTS R6：不嵌 JS 运行时）", ReasonCreatorUnavailable)
		return actioncards.Refused(ReasonCreatorUnavailable), nil
	}
	return s.generate(ctx, actCtx, proposal, header, location)
}

// generate 是官方 generate() 的宿主落地：真实落盘 → 真实校验 → 真实安装。
func (s *Service) generate(ctx context.Context, actCtx actioncards.ActContext, proposal actioncards.Proposal, header v4.Header, location Location) (actioncards.KindResult, error) {
	id, err := NewID()
	if err != nil {
		return actioncards.KindResult{}, err
	}
	bundleName := BundleName(id)
	staging := StagingDir(header.Cwd, s.home, id)
	profile := ProfileCopyDir(s.home, id)
	card := actioncards.Decision{
		CallID: proposal.CallID, Action: proposal.Action, Phase: PhaseCreating,
		Data: dataOf(location, bundleName, ""),
	}
	fail := func(stage string, cause error) (actioncards.KindResult, error) {
		s.logf("create_personalized_page: %s 失败（%s）: %v", stage, bundleName, cause)
		return s.settle(card, PhaseFailed), nil
	}
	// ⓪ 先记 {phase:'creating', bundleName}（官方 decide() 在起 Creator **之前**就落这条，
	// 进程若在生成中途死掉，这张卡在 v4 里仍然是"已开始"而不是"没点过"）。
	if err := s.record(ctx, actCtx.SessionID, card); err != nil {
		return actioncards.KindResult{}, err
	}
	// 官方 generate() 的第一步：mkdir(sourceDirectory, {recursive:true, mode:0o700})。
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return fail("建暂存目录", err)
	}
	_, initialContent, hasInitialContent := DetailsOf(proposal.Details)
	request := CreateRequest{
		SessionID: actCtx.SessionID, Title: proposal.Title, Brief: proposal.Brief,
		Details: proposal.Details,
		// 官方把**用户在卡上选定的** location 交给 Creator（`card.slot` /
		// `card.customLocation`），proposal.details.slot 只是推荐值，不覆盖用户选择。
		Slot:           location.Slot,
		CustomLocation: location.CustomLocation, HasCustomLocation: location.HasCustomLocation,
		InitialContent: initialContent, HasInitialContent: hasInitialContent,
		SourceDirectory: staging, BundleName: bundleName,
	}
	// ④ Creator 生成（官方 subagents.startActivation + structured ready:true）
	if err := s.creator.Create(ctx, request); err != nil {
		return fail("Creator 生成", err)
	}
	// ⑤ 校验暂存目录（官方 validateGeneratedBundle(sourceDirectory, bundleName)）
	if err := ValidateBundle(staging, bundleName); err != nil {
		return fail("校验生成包", err)
	}
	// 保留名占用检查（官方在复制前查 listBundles().some(bundle => bundle.name === bundleName)）
	if _, exists, err := s.bundles.Bundle(bundleName); err != nil {
		return fail("查询保留名", err)
	} else if exists {
		return fail("保留名占用", fmt.Errorf("包 %s 已经在 pluginManager 里，拒绝同名生成", bundleName))
	}
	// ⑥ 复制到 profile 的 created-plugins（官方 cp(..., {errorOnExist:true})）
	if err := CopyBundle(staging, profile); err != nil {
		return fail("复制到 profile", err)
	}
	// ⑥ 再校验复制件（官方对**复制件**再跑一次 validateGeneratedBundle）
	if err := ValidateBundle(profile, bundleName); err != nil {
		return fail("校验 profile 副本", err)
	}
	// ⑦ 记 {phase:'installing'}，再真装（官方 installProfileCopy）
	card.Phase = PhaseInstalling
	if err := s.record(ctx, actCtx.SessionID, card); err != nil {
		return actioncards.KindResult{}, err
	}
	if _, err := s.bundles.Install(profile); err != nil {
		// 真实安装器的拒绝（本仓只接受 dsh.local-plugin@1 的 Go/WASM 工具插件）
		// 就是真实结果：记 failed，绝不记 installed。
		return fail("pluginManager 安装", err)
	}
	// ⑦ 装完按真实安装态确认（官方 listBundles().some(bundle => name && installed)）
	state, installed, err := s.bundles.Bundle(bundleName)
	if err != nil {
		return fail("复核安装态", err)
	}
	if !installed {
		return fail("复核安装态", fmt.Errorf("安装器返回成功但 pluginManager 里没有 %s", bundleName))
	}
	application := ApplicationApplied
	if !state.Enabled {
		// 装上了但没启用：官方把这种情况记成 application:failed（
		// recover() 的 `installed.error === undefined && installed.enabled ? {} : {application:'failed'}`）。
		application = ApplicationFailed
	}
	card.Phase = PhaseInstalled
	card.Data = dataOf(location, bundleName, application)
	return s.settle(card, PhaseInstalled), nil
}

func (s *Service) undo(ctx context.Context, actCtx actioncards.ActContext, decision actioncards.Decision, hasDecision bool) (actioncards.KindResult, error) {
	// 官方：只有 installed/kept 的卡能真卸载；removed 的卡再点 undo 回已有决定
	// （官方 `if (previous?.phase === 'removed' …) return settled(previous)`），
	// 其它 phase 明确 unavailable。
	if !hasDecision {
		return actioncards.Refused(ReasonUnavailable), nil
	}
	if decision.Phase == PhaseRemoved {
		return actioncards.Settled(decision), nil
	}
	if decision.Phase != PhaseInstalled && decision.Phase != PhaseKept {
		return actioncards.Refused(ReasonUnavailable), nil
	}
	bundleName := bundleNameOf(decision.Data)
	if bundleName == "" {
		return actioncards.Refused(ReasonUnavailable), nil
	}
	base := receiptOf(decision)
	if s.isClosing() {
		return actioncards.Refused(ReasonClosing), nil
	}
	_, installed, err := s.bundles.Bundle(bundleName)
	if err != nil {
		s.logf("create_personalized_page: 查询 %s 安装态失败: %v", bundleName, err)
		base.Data["application"] = ApplicationFailed
		return s.settle(base, PhaseFailed), nil
	}
	if !installed {
		// 官方：没有已安装包时记 removed + applied（restore 仍可从保留副本重装）。
		base.Data["application"] = ApplicationApplied
		return s.settle(base, PhaseRemoved), nil
	}
	if err := s.bundles.Uninstall(bundleName); err != nil {
		s.logf("create_personalized_page: 卸载 %s 失败: %v", bundleName, err)
		base.Data["application"] = ApplicationFailed
		return s.settle(base, PhaseFailed), nil
	}
	base.Data["application"] = ApplicationApplied
	return s.settle(base, PhaseRemoved), nil
}

func (s *Service) cancel(decision actioncards.Decision, hasDecision bool) actioncards.KindResult {
	// 宿主没有后台 Creator 任务：create 要么同步做完、要么在生成前拒绝，
	// 所以这里没有"正在跑的构建"可中止。官方在没有 job 时的语义就是回已记录的
	// decision（src/index.ts 的 `settled(previous)`）；连 decision 都没有就
	// refused{unavailable}（官方对非 create 动词的兜底分支）。
	if hasDecision {
		return actioncards.Settled(decision)
	}
	return actioncards.Refused(ReasonUnavailable)
}

func (s *Service) restore(ctx context.Context, actCtx actioncards.ActContext, decision actioncards.Decision, hasDecision bool) (actioncards.KindResult, error) {
	// 官方：restore 只作用于 removed 的卡。
	if !hasDecision || decision.Phase != PhaseRemoved {
		return actioncards.Refused(ReasonUnavailable), nil
	}
	bundleName := bundleNameOf(decision.Data)
	if bundleName == "" {
		return actioncards.Refused(ReasonUnavailable), nil
	}
	id, err := IDOfBundleName(bundleName)
	if err != nil {
		return actioncards.Refused(ReasonUnavailable), nil
	}
	directory := ProfileCopyDir(s.home, id)
	// 官方：先用同一个校验器确认保留副本还在、还是好的。
	if err := ValidateBundle(directory, bundleName); err != nil {
		s.logf("create_personalized_page: 保留副本不能恢复（%s）: %v", directory, err)
		return actioncards.Refused(ReasonSourceMissing), nil
	}
	if s.isClosing() {
		return actioncards.Refused(ReasonClosing), nil
	}
	// 官方：上一次的 application 描述的是"移除"，不是这次安装，重装前去掉。
	base := receiptOf(decision)
	delete(base.Data, "application")
	if _, installed, err := s.bundles.Bundle(bundleName); err == nil && installed {
		return s.settle(base, PhaseInstalled), nil
	}
	base.Phase = PhaseInstalling
	if err := s.record(ctx, actCtx.SessionID, base); err != nil {
		return actioncards.KindResult{}, err
	}
	if _, err := s.bundles.Install(directory); err != nil {
		s.logf("create_personalized_page: 恢复 %s 时安装被拒: %v", bundleName, err)
		return s.settle(base, PhaseFailed), nil
	}
	// 与 create 同一套判据：只有 pluginManager 里真的有了，才记 installed。
	state, installed, err := s.bundles.Bundle(bundleName)
	if err != nil || !installed {
		s.logf("create_personalized_page: 恢复 %s 后复核不到安装态: installed=%v err=%v", bundleName, installed, err)
		return s.settle(base, PhaseFailed), nil
	}
	base.Data["application"] = ApplicationApplied
	if !state.Enabled {
		base.Data["application"] = ApplicationFailed
	}
	return s.settle(base, PhaseInstalled), nil
}

// reconcile 是官方 recover() 的宿主落地：unconfirmed 卡在下一次动作前按真实
// profile 复核——真的装上了就记 installed，否则记 interrupted（可重试）。
func (s *Service) reconcile(proposal actioncards.Proposal, decision actioncards.Decision) (*actioncards.Decision, error) {
	bundleName := bundleNameOf(decision.Data)
	if bundleName == "" {
		return nil, nil
	}
	state, installed, err := s.bundles.Bundle(bundleName)
	if err != nil {
		return nil, err
	}
	if !installed {
		out := actioncards.Decision{
			CallID: decision.CallID, Action: decision.Action, Phase: PhaseInterrupted,
			Data: cloneData(decision.Data),
		}
		return &out, nil
	}
	data := map[string]any{"bundleName": bundleName}
	if slot, ok := decision.Data["slot"].(string); ok && slot != "" {
		data["slot"] = slot
	}
	if custom, ok := decision.Data["customLocation"].(string); ok && custom != "" {
		data["customLocation"] = custom
	}
	if !state.Enabled {
		data["application"] = ApplicationFailed
	}
	out := actioncards.Decision{
		CallID: decision.CallID, Action: decision.Action, Phase: PhaseInstalled, Data: data,
	}
	return &out, nil
}

// dismissal 是官方 dismissal 回调的落地：Not now 也把用户选的位置记下来。
//
// 官方判据：slot 从不合法就退回 proposal.details.slot；customLocation 只在
// 合法时带上（两者独立判定）。唯一偏差：proposal.details 里连合法 slot 都没有时
// 我们**省略** slot，而不是写一个 undefined（JSON 里没有 undefined）。
func (s *Service) dismissal(proposal actioncards.Proposal, payload map[string]json.RawMessage) (map[string]any, error) {
	data := map[string]any{}
	slot := ""
	if payload != nil {
		if raw, ok := payload["slot"]; ok {
			var text string
			if json.Unmarshal(raw, &text) == nil && knownSlot(text) {
				slot = text
			}
		}
	}
	if slot == "" {
		if text, ok := proposal.Details["slot"].(string); ok && knownSlot(text) {
			slot = text
		}
	}
	if slot != "" {
		data["slot"] = slot
	}
	if payload != nil {
		if raw, ok := payload["customLocation"]; ok {
			var text string
			if json.Unmarshal(raw, &text) == nil {
				if trimmed := strings.TrimSpace(text); trimmed != "" && len([]rune(trimmed)) <= 1000 {
					data["customLocation"] = trimmed
				}
			}
		}
	}
	return data, nil
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

// liveRoot 确认会话是**live root**（存在且不是子代理会话），并返回真实头部。
//
// 官方卡片服务只把 live root 交给 kind；这里复查一次真实 v4 头，是因为
// 本宿主的 ActionCardAct 直接由 transport 调用，没有 agent 对象可问。
func (s *Service) liveRoot(sessionID string) (v4.Header, error) {
	if strings.TrimSpace(sessionID) == "" {
		return v4.Header{}, errors.New("缺会话 id")
	}
	path, err := session.FindByID(s.home, sessionID)
	if err != nil {
		return v4.Header{}, err
	}
	log, err := v4.ReadFile(path)
	if err != nil {
		return v4.Header{}, err
	}
	if strings.TrimSpace(log.Header.ParentSession) != "" {
		return v4.Header{}, fmt.Errorf("会话 %s 是子代理会话（parentSession=%s）", sessionID, log.Header.ParentSession)
	}
	return log.Header, nil
}

func (s *Service) record(ctx context.Context, sessionID string, decision actioncards.Decision) error {
	if strings.TrimSpace(sessionID) == "" {
		return errors.New("creation: 缺会话 id，无法写回 decision")
	}
	return s.cards.Record(ctx, sessionID, decision)
}

// settle 返回官方 settled。
//
// 这里**不自己写盘**：actioncards.Service 收到 `settled` 时会把它写进 v4
// （与官方一致：kind 只回决定，落盘由卡片服务负责）。中间态（installing）
// 才需要 kind 自己用 record 落一次。
func (s *Service) settle(decision actioncards.Decision, phase string) actioncards.KindResult {
	decision.Phase = phase
	return actioncards.Settled(decision)
}

// dataOf 组官方 PageData（只写官方字段：slot / customLocation / bundleName / application）。
func dataOf(location Location, bundleName, application string) map[string]any {
	data := map[string]any{"slot": location.Slot}
	if location.HasCustomLocation {
		data["customLocation"] = location.CustomLocation
	}
	if bundleName != "" {
		data["bundleName"] = bundleName
	}
	if application != "" {
		data["application"] = application
	}
	return data
}

// receiptOf 复制一张卡的 data 作为下一次记录的收据（不共享 map，避免改到旧值）。
func receiptOf(decision actioncards.Decision) actioncards.Decision {
	return actioncards.Decision{
		CallID: decision.CallID, Action: decision.Action,
		Phase: decision.Phase, Data: cloneData(decision.Data),
	}
}

func cloneData(data map[string]any) map[string]any {
	out := make(map[string]any, len(data)+1)
	for key, value := range data {
		out[key] = value
	}
	return out
}

func bundleNameOf(data map[string]any) string {
	value, ok := data["bundleName"].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}
