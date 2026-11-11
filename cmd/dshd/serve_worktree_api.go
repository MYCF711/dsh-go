package main

// forgePanel worktree 生命周期（6 个方法）的宿主实现。
//
// 分层：
//
//	internal/worktree      真实状态机 + git 子进程（无 session 依赖）
//	internal/transport     官方 L1 契约（参数/四字段错误/void 成功）
//	本文件                 会话 v4 日志读写 + 挂到 Server + agent 钩子
//
// 为什么 Store 放在这里：worktree 包不能 import session（那会把 R7 的
// "Transport 不 import 会话日志" 反过来变成 service→session 的隐式耦合）；
// 宿主是唯一允许读 v4 日志的一侧，所以 Load/Append/SetWorkingDirectory 由本
// 文件实现，service 只认接口。
//
// 接线（WIRING）：
//
//	wsHost := attachOfficialWorkspace(srv, *home, src, prompts)
//	worktreeSvc := attachOfficialWorktree(srv, src)
//	// 1) 消息提交前 hold（inbox hold）：
//	//    if err := worktreeHold(ctx, worktreeSvc, sessionID); err != nil { ... }
//	// 2) agent.Options.PreStep 套一层：
//	//    PreStep: worktreePreStep(worktreeSvc, sessionID, prevPreStep)
//
// 两条都不接时，requestWorktree 仍然真实记录请求；只是没有 turn 会去创建
// （pre-step 是唯一创建点），所以 WIRING 不是可选项。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"dshgo/codecs/v4"
	"dshgo/internal/modes"
	"dshgo/internal/sandbox"
	"dshgo/internal/session"
	"dshgo/internal/transport"
	"dshgo/internal/worktree"
)

// worktreeSessionStore 用会话 v4 日志实现 worktree.Store。
type worktreeSessionStore struct {
	src *sessionSource
}

// Load 折叠会话日志里的 worktree 记录 + 有效工作目录。
func (s *worktreeSessionStore) Load(sessionID string) (worktree.Snapshot, error) {
	if s == nil || s.src == nil {
		return worktree.Snapshot{}, fmt.Errorf("worktree: 宿主没有 sessionSource")
	}
	path, err := s.src.resolve(sessionID)
	if err != nil {
		return worktree.Snapshot{}, err
	}
	parsed, err := v4.ReadFile(path)
	if err != nil {
		return worktree.Snapshot{}, fmt.Errorf("worktree: 读会话日志失败: %w", err)
	}
	return worktree.Fold(parsed.Header.Cwd, parsed.Events), nil
}

// AppendWorktree 追加一条 worktree 记录（打开写锁 → append → 关闭）。
func (s *worktreeSessionStore) AppendWorktree(sessionID string, record worktree.Record) error {
	return s.withWriter(sessionID, func(log *session.Log) error {
		return log.Append(worktree.EventType, record)
	})
}

// AppendCreated 写官方 rc.2 的 `plugin:worktree/created` 记录
// （worktree.CreatedAppender 这个**可选**扩展）。
//
// 不实现它 = 生产里没有创建记录 = 归档时永远没有可清理的候选（官方语义：
// 没有事件就不扫，release 前创建的 checkout 永不被清理）。create() 在目录建好、
// 会话已切过去之后回调这里；写失败按官方语义让这次创建报错——checkout 与分支
// 都保留，下一次 retry 还能继续。
func (s *worktreeSessionStore) AppendCreated(sessionID string, creation worktree.Creation) error {
	return s.withWriter(sessionID, func(log *session.Log) error {
		return log.Append(worktree.EventTypeCreated, creation)
	})
}

// ArchiveFacts 读一份会话在归档清理里需要的三件事：
//
//  1. 有效工作目录（header.cwd，且叠加 working-directory/change 之后的值）；
//  2. 本会话自己创建的 checkout（`plugin:worktree/created` 折叠出来的 Creations）；
//  3. 子代理父子帧里的父会话 id。
//
// 第 3 项为什么要读日志：本宿主把"这是一条子代理会话"写在**子会话自己的**
// `subagent/spawn` 帧里（internal/agent.ChildSpawn → session.EventChildSpawn），
// header 里没有官方的 `origin`；而 header.parentSession 表达的是 **fork** 的父子
// 关系，不是子代理。两者绝不能混用：fork 出去的会话是独立会话（不会被祖先的
// 归档带走），子代理则会。
func (s *worktreeSessionStore) ArchiveFacts(sessionID string) (worktree.Snapshot, string, error) {
	if s == nil || s.src == nil {
		return worktree.Snapshot{}, "", fmt.Errorf("worktree: 宿主没有 sessionSource")
	}
	path, err := s.src.resolve(sessionID)
	if err != nil {
		return worktree.Snapshot{}, "", err
	}
	parsed, err := v4.ReadFile(path)
	if err != nil {
		return worktree.Snapshot{}, "", fmt.Errorf("worktree: 读会话日志失败: %w", err)
	}
	return worktree.Fold(parsed.Header.Cwd, parsed.Events), worktreeArchiveParent(parsed.Events), nil
}

// worktreeArchiveParent 从 `subagent/spawn` 帧里读父会话 id（没有 = 不是子代理会话）。
//
// 只认**第一条**：子代理会话的日志第一条事件就是它（internal/agent 的 Spawn 先写
// 父子帧）；坏了就按"不是子代理"处理——那只会让它更不容易被跳过，也就是更保守。
func worktreeArchiveParent(events []v4.Event) string {
	for _, event := range events {
		if event.Type != session.EventChildSpawn {
			continue
		}
		var frame struct {
			ParentSessionID string `json:"parentSessionId"`
		}
		if err := json.Unmarshal(event.Data, &frame); err != nil {
			return ""
		}
		return strings.TrimSpace(frame.ParentSessionID)
	}
	return ""
}

// SetWorkingDirectory 把会话有效目录改成 dir（真实 working-directory/change）。
//
// 直接追加官方事件而不是调 session 的 helper：HEAD 基线的 internal/session
// 还没有 cwd 记录器（那是并行任务的 uncommitted 改动），宿主侧只依赖
// v4 事件形状，冲突面最小。
func (s *worktreeSessionStore) SetWorkingDirectory(sessionID, dir string) error {
	return s.withWriter(sessionID, func(log *session.Log) error {
		return log.Append("working-directory/change", map[string]string{"cwd": dir})
	})
}

func (s *worktreeSessionStore) withWriter(sessionID string, fn func(*session.Log) error) error {
	if s == nil || s.src == nil {
		return fmt.Errorf("worktree: 宿主没有 sessionSource")
	}
	path, err := s.src.resolve(sessionID)
	if err != nil {
		return err
	}
	logw, err := session.OpenSessionAt(path)
	if err != nil {
		// 会话写锁被仍在大轮里的子进程短暂占用：按官方四字段回可重试的 409，
		// 不要落成 internal 500（500 会让界面认为 worktree 坏了）。
		if strings.Contains(err.Error(), "正被另一个进程写入") {
			return &worktree.Error{Code: "worktree/session-busy", Status: 409,
				Message: "会话正在写（上一轮可能刚结束）：稍后重试（同一会话的 worktree 操作）"}
		}
		return fmt.Errorf("worktree: 打开会话写入失败: %w", err)
	}
	defer func() { _ = logw.Close() }()
	return fn(logw)
}

// newWorktreeService 造一个绑定到该 home 的 worktree 服务。
//
// 单独抽出来是因为 `cmdRun`（子进程，没有 transport.Server）也要在
// agent 的 pre-step 里创建 worktree；两处必须用同一份 store/git 实现。
func newWorktreeService(src *sessionSource) *worktree.Service {
	return worktree.NewService(&worktreeSessionStore{src: src}, worktree.ExecGit{})
}

// attachOfficialWorktree 挂上 6 个方法的真实实现，返回 service 供钩子使用。
func attachOfficialWorktree(srv *transport.Server, src *sessionSource) *worktree.Service {
	if srv == nil {
		return nil
	}
	svc := newWorktreeService(src)
	srv.AttachOfficialWorktreeAPI(transport.OfficialWorktreeAPI{
		RequestWorktree:   svc.RequestWorktree,
		CancelWorktree:    svc.CancelWorktree,
		RetryWorktree:     svc.RetryWorktree,
		StopCreation:      svc.StopCreation,
		RestoreWorktree:   svc.RestoreWorktree,
		ContinueInProject: svc.ContinueInProject,
	})
	return svc
}

// worktreePreStep 把 worktree 的"第一步创建"套在既有 PreStep 之前。
//
// 顺序：先跑既有 PreStep（checkpoint 落盘等），再做 worktree 创建；任一步
// 失败都让本轮结束（官方：失败结束 turn、请求保留）。
func worktreePreStep(svc *worktree.Service, sessionID string, prev func(context.Context, int) error) func(context.Context, int) error {
	return func(ctx context.Context, step int) error {
		if prev != nil {
			if err := prev(ctx, step); err != nil {
				return err
			}
		}
		if svc == nil {
			return nil
		}
		return svc.PreStep(ctx, sessionID, step)
	}
}

// worktreeHold 是消息提交前的 inbox hold（WIRING）：worktree 创建完成或请求
// 被撤回前不放行；失败/停止继续等（官方语义）。
func worktreeHold(ctx context.Context, svc *worktree.Service, sessionID string) error {
	if svc == nil {
		return nil
	}
	return svc.Wait(ctx, sessionID)
}

// ---------------------------------------------------------------------------
// rc.2「归档会话时清理工作树」的宿主接线
// ---------------------------------------------------------------------------

// worktreeArchiveSettings 是"归档时保留 worktree"的**最小设置读取点**。
//
// 官方把这个开关放在 Plugins 页（Keep worktrees when archiving），**默认 false**
// ——归档就把本会话自己建的干净 checkout 清掉。本仓宿主现有设置面只有 model /
// permission 两个 namespace（internal/settingsform），没有 worktree 这一栏；
// 与其伪造一个设置面板，这里只加"读一个可选布尔值"的最小读取点：
//
//	<home>/state/worktree-settings.json   {"retainOnArchive": true}
//
// 文件不存在 / 没有这个键 = 官方默认 false（归档时清理）。文件**读不动或坏了**
// 属于"拿不到答案"：按 true（保留）处理并往 stderr 打一行——worktree 包的铁律是
// 任何不确定都选择保留，删掉的 checkout 恢复不回来（分支与提交倒是永远在）。
type worktreeArchiveSettings struct{ path string }

func newWorktreeArchiveSettings(home string) worktreeArchiveSettings {
	if strings.TrimSpace(home) == "" {
		return worktreeArchiveSettings{}
	}
	return worktreeArchiveSettings{path: filepath.Join(home, "state", "worktree-settings.json")}
}

// RetainOnArchive 每次归档准入时读一次（与官方 `retainOnArchive()` 同语义）。
func (s worktreeArchiveSettings) RetainOnArchive() bool {
	if s.path == "" {
		return false // 没有 home = 没设过 = 官方默认（归档时清理）
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return false
		}
		fmt.Fprintf(os.Stderr, "worktree: 读设置 %s 失败，按保留处理（不清理 checkout）: %v\n", s.path, err)
		return true
	}
	var doc struct {
		RetainOnArchive *bool `json:"retainOnArchive"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		fmt.Fprintf(os.Stderr, "worktree: 设置 %s 不是合法 JSON，按保留处理（不清理 checkout）: %v\n", s.path, err)
		return true
	}
	if doc.RetainOnArchive == nil {
		return false
	}
	return *doc.RetainOnArchive
}

// worktreeArchiveDeps 是归档清理需要的宿主能力（全部真实，不缓存副本）。
type worktreeArchiveDeps struct {
	// Home 是 <home>：列会话、读 registry、解析沙箱策略、读设置都用它。
	Home string
	// Workspace 是 workspace registry 的真实宿主（归档集合的唯一真相）。
	Workspace *officialWorkspaceHost
	// Service 是**与 worktree 面板同一个**实例：清理状态与 revision 必须同源。
	Service *worktree.Service
	// Sessions 读会话日志（和 Service 内部那份 store 是同一个实现，无状态）。
	Sessions *worktreeSessionStore
	// Presets / Modes 是"创建者会话沙箱策略"的真实解析链。
	Presets *modes.Registry
	Modes   *sandboxModeStore
}

// archiveHost 组装 ArchiveHost：三个回调都是真实判断，nil 一个都不留。
func (d worktreeArchiveDeps) archiveHost() worktree.ArchiveHost {
	settings := newWorktreeArchiveSettings(d.Home)
	return worktree.ArchiveHost{
		RetainOnArchive: settings.RetainOnArchive,
		Unused:          d.unused,
		ReadOnly:        d.readOnly,
	}
}

// readOnly 报告创建者会话的沙箱策略是否是只读（官方
// `ctx.sandboxPolicy.resolve({ session }).mode === 'read-only'`）。
//
// 真值来源就是宿主自己的解析链（`state/session-preset.json` 的显式选择 →
// v4 header 的 agentPreset → modes 内置/用户 preset → sandbox 档位），不另建一份。
// 解析不出来（会话日志没了 / preset 未知 / 状态文件坏了）= **拿不到答案** →
// 按只读处理（保留），并打 stderr：ArchiveHost.ReadOnly 只有 bool，没有 error 通道。
func (d worktreeArchiveDeps) readOnly(sessionID string) bool {
	source, err := resolveSandboxModeForSession(d.Home, sessionID, d.Presets, d.Modes)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"worktree: 归档清理读不到会话 %s 的沙箱策略，按只读处理（保留 checkout）: %v\n", sessionID, err)
		return true
	}
	return source.Effective == sandbox.ModeReadOnly
}

// archivedSessionIDs 读 workspace registry 的归档集合。
//
// 与 `workspace.follow` 读的是**同一份** registry（不新建账本、不缓存副本），
// 只是这里只需要那个集合，不必把每个工作区都投影一遍。
func (d worktreeArchiveDeps) archivedSessionIDs() ([]string, error) {
	host := d.Workspace
	if host == nil {
		return nil, fmt.Errorf("worktree: 没有工作区 registry，无法判断哪些会话已经归档")
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if err := host.ensureLoadedLocked(); err != nil {
		return nil, fmt.Errorf("worktree: 读工作区 registry 失败: %w", err)
	}
	return append([]string(nil), host.doc.ArchivedSessionIDs...), nil
}

// worktreeArchiveSessionFacts 是一份未归档会话在 unused() 里的判据素材。
type worktreeArchiveSessionFacts struct {
	snapshot worktree.Snapshot
	parent   string
}

// unused 实现官方 `archive.ts::unused` 的判据：除**正在归档的这个会话**之外，
// 没有未归档会话还引用这个 checkout。
//
// 引用 = 下面任一条（与官方逐条一致）：
//  1. 会话的有效工作目录就是该 checkout，或在它里面（看 working-directory/change
//     之后的值，不是 header 里的原始 cwd——建完 worktree 的会话 header 仍指向源仓库）；
//  2. 会话自己的创建记录里有这个 path（fork 继承来的记录也算"引用"）。
//
// 归档 = registry 的 archivedSessionIds + 正在归档的这个 id；**子代理会话跟着已
// 归档的祖先一起算已归档**（官方 ancestorArchived：子代理没有自己的归档准入，
// 父会话归档时它跟着走）。
//
// 拿不到答案一律返回 error（worktree 服务把 error 当"仍被使用" → retained，不删）：
// 列会话失败、registry 读不了、某份会话日志读不动，都交回调用方做保守决定，
// 不许"没查到 = 可以删"。代价是要读一遍所有未归档会话的日志（官方走
// sessionQuery + 投影缓存；本仓没有那份缓存），所以它只在后台 inspection 里跑。
func (d worktreeArchiveDeps) unused(path, archivingSessionID string) (bool, error) {
	if d.Service == nil {
		return false, fmt.Errorf("worktree: 没有 worktree 服务，无法判断 checkout 是否仍被引用")
	}
	if d.Sessions == nil {
		return false, fmt.Errorf("worktree: 没有会话读取口，无法判断 checkout 是否仍被引用")
	}
	if strings.TrimSpace(d.Home) == "" {
		return false, fmt.Errorf("worktree: 没有 home，无法列会话判断 checkout 是否仍被引用")
	}
	archivedList, err := d.archivedSessionIDs()
	if err != nil {
		return false, err
	}
	archived := make(map[string]bool, len(archivedList)+1)
	for _, id := range archivedList {
		archived[id] = true
	}
	if id := strings.TrimSpace(archivingSessionID); id != "" {
		archived[id] = true
	}
	listings, err := session.ListSessions(d.Home, "")
	if err != nil {
		return false, fmt.Errorf("worktree: 列会话失败: %w", err)
	}
	facts := make(map[string]worktreeArchiveSessionFacts, len(listings))
	for _, listing := range listings {
		id := strings.TrimSpace(listing.SessionID)
		if id == "" || archived[id] {
			continue
		}
		snapshot, parent, factErr := d.Sessions.ArchiveFacts(id)
		if factErr != nil {
			return false, fmt.Errorf(
				"worktree: 读未归档会话 %s 的日志失败（无法判断它是否还引用 %s）: %w", id, path, factErr)
		}
		facts[id] = worktreeArchiveSessionFacts{snapshot: snapshot, parent: parent}
	}
	for id, fact := range facts {
		if worktreeArchiveInheritedArchived(id, facts, archived) {
			continue
		}
		if cwd := strings.TrimSpace(fact.snapshot.Cwd); cwd != "" && officialPathWithin(path, cwd) {
			return false, nil
		}
		for _, creation := range fact.snapshot.Creations {
			if officialSamePath(creation.Path, path) {
				return false, nil
			}
		}
	}
	return true, nil
}

// worktreeArchiveInheritedArchived 报告这条会话是否因**子代理祖先已归档**而算已归档
// （官方 `ancestorArchived`）。本宿主的子代理关系在子会话自己的 `subagent/spawn`
// 帧里（facts[id].parent）；链断了（父会话不在这批未归档会话里）就按"没归档"
// 处理——宁可多保留，也不多删。
func worktreeArchiveInheritedArchived(sessionID string, facts map[string]worktreeArchiveSessionFacts, archived map[string]bool) bool {
	seen := map[string]bool{sessionID: true}
	current := sessionID
	for {
		fact, ok := facts[current]
		if !ok || fact.parent == "" || seen[fact.parent] {
			return false
		}
		if archived[fact.parent] {
			return true
		}
		seen[fact.parent] = true
		current = fact.parent
	}
}

// archiveSessionWithCleanup 在归档**成功之后**排一次 checkout 清理。
//
// 官方是 `workspace/session-archived` 事件的监听者（archive.ts 构造函数），这里
// 用包装达到同一位置：先跑原来的归档（它在 registry 里落盘），成功了才排期。
// 清理失败**不改变归档结果**（归档已经生效，清理是托管动作），但不能静默——
// 打到 stderr，客户端也能从 `forgePanel.worktreeCleanup` 看到 scheduled/retained/failed。
func archiveSessionWithCleanup(prev func(sessionID string, stopActivity bool) ([]string, error), svc *worktree.Service) func(string, bool) ([]string, error) {
	return func(sessionID string, stopActivity bool) ([]string, error) {
		out, err := prev(sessionID, stopActivity)
		if err != nil || svc == nil {
			return out, err
		}
		if cleanupErr := svc.ScheduleArchiveCleanup(sessionID); cleanupErr != nil {
			fmt.Fprintf(os.Stderr, "worktree: 会话 %s 已归档，但没有排期清理 checkout: %v\n", sessionID, cleanupErr)
		}
		return out, nil
	}
}

// attachWorktreeArchive 把 rc.2「归档会话时清理工作树」接进真实宿主（WIRING）。
//
// 两件事：
//  1. 给 worktree 服务注入 ArchiveHost（保留策略 / 是否仍被引用 / 创建者沙箱策略）；
//  2. 在 `workspace.archiveSession` **成功之后**排一次清理（立刻返回，inspection
//     在后台；结果由 `forgePanel.worktreeCleanup` 读/等）。
//
// 第 2 步为什么用"装配期包装"而不是直接改 archiveSession：archiveSession 在
// serve_workspace_api.go（同轮另一条线在改），这里在装配期把同一个函数换成
// "归档成功后多排一次清理"的版本；transport 存的就是这份函数表（Attach 覆盖同一
// key），行为与直接加一行等价，但写作用域不交叉。
func attachWorktreeArchive(srv *transport.Server, deps worktreeArchiveDeps) {
	if deps.Service == nil {
		return
	}
	deps.Service.SetArchiveHost(deps.archiveHost())
	if srv == nil || deps.Workspace == nil {
		return
	}
	api := deps.Workspace.workspaceAPI()
	api.ArchiveSession = archiveSessionWithCleanup(api.ArchiveSession, deps.Service)
	srv.AttachOfficialWorkspaceAPI(api)
}
