// 且已经没人用的干净 checkout 用**非 force** 的 `git worktree remove` 清掉；
// 分支与提交永远保留，任何不确定都选择保留。
//
// # 官方实现（逐条对齐，权威）
//
//	%TEMP%/official-rc2/dsh/node_modules/@deepseek-ai/dsh-experimental-worktree/
//	  src/archive.ts    — WorktreeReclamation：retainOnArchive / 后台 inspection /
//	                      revision 单调 / scheduled|retained|removed|failed /
//	                      cleanupOf 等待语义 / unused() 判据 / 只清 owned 创建
//	  src/removal.ts    — inspectRemoval 的判据与 `worktree remove --`（非 force）
//	  src/types.ts      — WorktreeCleanup / WorktreeCreation
//	  README.md         — `plugin:worktree/created` 记录与 retainOnArchive 说明
//
// 升级指南：docs/upgrade-guide/v0.2.2-rc.1/archive-worktrees/guide.md
//
// # 语义（与官方 archive.ts 一致）
//
//  1. retainOnArchive 为真 → 归档时不排期，每个 checkout 都保留；
//  2. 只考虑本会话自己创建的创建记录（Snapshot.Creations）；没有创建 → 不排期；
//  3. 排期后立刻读到 {revision, scheduled}，后台 inspection 结束后变
//     retained / removed / failed；revision 在 Service 生命周期内单调递增；
//  4. 创建者会话不存在 / 创建者只读 / 还有未归档会话引用 / inspection 判据不满足
//     → retained；宿主字段为 nil（拿不到答案）一律按"保留"处理；
//  5. inspection 全过 → `git worktree remove -- <path>`（非 force）成功 → removed；
//  6. inspection 抛错或 remove 报错 → failed（可能留下半个目录），分支与提交保留；
//  7. 同一 path 的移除不重叠；同一会话只有一个 attempt，新 attempt 覆盖旧的
//     （旧 revision 的等待者拿到 nil）。
package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// CleanupStatus 是一次归档清理尝试的状态（官方 `WorktreeCleanup['status']`）。
type CleanupStatus string

const (
	// CleanupScheduled：已排期，后台 inspection 还没结束。
	CleanupScheduled CleanupStatus = "scheduled"
	// CleanupRetained：使用中、策略、或 Git 状态让它留下来。
	CleanupRetained CleanupStatus = "retained"
	// CleanupRemoved：`git worktree remove` 已成功。
	CleanupRemoved CleanupStatus = "removed"
	// CleanupFailed：移除报错，可能留下半个目录；分支与提交保留。
	CleanupFailed CleanupStatus = "failed"
)

// Cleanup 是一次归档清理尝试的进程内状态（官方 `WorktreeCleanup`）。
type Cleanup struct {
	// Revision 是本次 Host 生命周期内的单调尝试序号（从 1 开始）。
	Revision int64
	// Status 是当前状态。
	Status CleanupStatus
}

// ArchiveHost 是宿主（transport/host）注入的运行时能力。
//
// 所有字段都可以为 nil，**nil 一律按"保守保留"处理**：拿不到答案就不删。
// 例外只有 RetainOnArchive——它是官方设置项，缺省即 false（默认不保留）。
type ArchiveHost struct {
	// RetainOnArchive 每次准入时读：true 表示保留每个 checkout（官方设置项，
	// 默认 false）。宿主把它接成 Forge 面板 worktree 行上的开关。
	RetainOnArchive func() bool
	// Unused 报告除了正在归档的会话之外，没有未归档会话再引用这个 checkout。
	// 拿不到答案（error）必须当"仍被使用"→ retained，不许乐观删除。
	Unused func(path, archivingSessionID string) (bool, error)
	// ReadOnly 报告创建者会话的沙箱策略是否是只读；只读不清理。
	// 拿不到答案（nil 回调）同样按只读处理。
	ReadOnly func(sessionID string) bool
	// Exclusive 可选：对同一 path 的清理与目录访问互斥（nil = 用 Service 内
	// 的 per-path 互斥）。body 是同步的，宿主实现里不要起 goroutine。
	Exclusive func(path string, body func() error) error
	// Now 可选，测试注入时间源。
	Now func() time.Time
}

// reclamation 是归档清理的进程内状态（官方 `WorktreeReclamation`）。
type reclamation struct {
	mu        sync.Mutex
	host      ArchiveHost
	attempts  map[string]*cleanupAttempt
	revision  int64
	stopped   bool
	pathLocks map[string]*sync.Mutex

	lifetime context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

func newReclamation() *reclamation {
	lifetime, cancel := context.WithCancel(context.Background())
	return &reclamation{
		attempts:  map[string]*cleanupAttempt{},
		pathLocks: map[string]*sync.Mutex{},
		lifetime:  lifetime,
		cancel:    cancel,
	}
}

// cleanupAttempt 是一个会话的清理尝试（官方 `interface Attempt`）。
//
// status/cancelled 由 reclamation.mu 保护；done 在结算后关闭一次。
type cleanupAttempt struct {
	sessionID string
	creation  Creation
	revision  int64
	status    CleanupStatus
	cancelled bool
	done      chan struct{}
	finish    sync.Once
	// startedAt/settledAt 是宿主时钟（ArchiveHost.Now）给出的排期/结算时间，
	// 留在进程内做诊断（官方同样不落盘）。
	startedAt time.Time
	settledAt time.Time
}

// SetArchiveHost 注入/替换宿主能力（可在运行中调用；每次准入都读最新值）。
func (s *Service) SetArchiveHost(host ArchiveHost) {
	rec := s.reclaim
	if rec == nil {
		return
	}
	rec.mu.Lock()
	rec.host = host
	rec.mu.Unlock()
}

// RetainOnArchive 透传宿主设置（没有 host 时 false，即官方默认：不保留）。
func (s *Service) RetainOnArchive() bool {
	host := s.hostSnapshot()
	if host.RetainOnArchive == nil {
		return false
	}
	return host.RetainOnArchive()
}

// ScheduleArchiveCleanup 为归档的会话排一次清理并**立刻返回**，后台做 inspection。
//
// 返回 error 仅表示"没排期"：服务正在停止，或读不到会话日志。retainOnArchive
// 打开、或这个会话没有自己创建的 checkout 时，返回 nil 且不产生 attempt。
func (s *Service) ScheduleArchiveCleanup(sessionID string) error {
	if err := s.requireSession(sessionID); err != nil {
		return err
	}
	rec := s.reclaim
	if rec == nil {
		return errors.New("worktree: 归档清理没有初始化")
	}
	if s.archiveStopped() {
		return errors.New("worktree: 归档清理正在停止，不排期")
	}
	// 1. 保留策略：不排期（官方在准入最前面就返回）。
	if s.RetainOnArchive() {
		return nil
	}
	snapshot, err := s.store.Load(sessionID)
	if err != nil {
		return fmt.Errorf("worktree: 读会话日志失败: %w", err)
	}
	// 2. 只考虑本会话自己创建的 checkout；没有创建 → 不排期。
	if len(snapshot.Creations) == 0 {
		return nil
	}
	host := s.hostSnapshot()
	rec.mu.Lock()
	if rec.stopped {
		rec.mu.Unlock()
		return errors.New("worktree: 归档清理正在停止，不排期")
	}
	rec.revision++
	attempt := &cleanupAttempt{
		sessionID: sessionID,
		creation:  snapshot.Creations[0],
		revision:  rec.revision,
		status:    CleanupScheduled,
		done:      make(chan struct{}),
		startedAt: nowOf(host),
	}
	// 7. 同一会话只有一个 attempt：新 attempt 覆盖旧的，旧 revision 的等待者拿 nil。
	if previous := rec.attempts[sessionID]; previous != nil {
		previous.cancelled = true
	}
	rec.attempts[sessionID] = attempt
	rec.wg.Add(1)
	rec.mu.Unlock()

	go func() {
		defer rec.wg.Done()
		s.runArchiveCleanup(attempt)
	}()
	return nil
}

// CleanupCancellation 是"取消一次在途归档清理"的结果。
//
// 官方 `archive.ts:87-93` 的监听器不返回任何东西（它只是 `await attempt.done`），
// 但宿主必须知道**取消到底来没来得及**：来晚了 checkout 已经没了，得走官方恢复
// 路径；来早了什么都没动，不能乱恢复。所以这里把结算终态如实交回去。
type CleanupCancellation struct {
	// Cancelled 报告是否真的有一个在途尝试被取消（官方 `attempt === undefined →
	// return`：没有 attempt 就什么都不做）。
	Cancelled bool
	// Revision 是被取消尝试的 revision（Cancelled 为 false 时是 0）。
	Revision int64
	// Status 是**结算后**的终态：retained = 取消及时，checkout 一个字都没动；
	// removed = 取消来晚，`git worktree remove` 已经跑完（调用方必须走恢复路径）；
	// failed = 移除报错（可能留半个目录）。
	Status CleanupStatus
}

// Removed 报告这次取消是不是"来晚了"（checkout 真的已经被删掉）。
func (c CleanupCancellation) Removed() bool {
	return c.Cancelled && c.Status == CleanupRemoved
}

// CancelArchiveCleanup 取消这个会话**尚未准入**的归档清理，并 await 结算。
//
// 逐条对齐官方 `archive.ts:87-93`（`workspace/session-unarchiving` 监听器）：
//
//	attempt := this.attempts.get(sessionId)
//	if (attempt === undefined) return          // ① 没有在途尝试 → 什么都不做
//	attempt.cancelled = true                   // ② 置取消位
//	await attempt.done                         // ③ 等它结算（已准入的 git 不打断）
//	this.attempts.delete(sessionId)            // ④ 收走 attempt（cleanupOf 之后为 null）
//
// 为什么置位就够：`inspectAndRemove` 在 inspection 之前、`removeCheckout` 在拿锁
// 之后与 `git worktree remove` 之前各判一次 `archiveAborted`（官方同位的
// `admit()`）。置位与这两处判定共用 `reclaim.mu`，所以**取消与准入不可能同时为
// 真**——两者只可能一前一后：
//
//   - 取消落在准入之前 → 移除根本不执行，结算为 retained：目录、`git worktree
//     list` 注册、分支全都不变；
//   - 取消落在准入之后（`git worktree remove` 已经在跑）→ 我们不打断它（官方
//     也不打断），等它结算；成功即 removed，此时 checkout 真的没了，调用方必须
//     走官方恢复路径 `RestoreWorktree`，不许假装没删。
//
// 返回 error 只表示"没资格取消"（会话 id 为空）；没有在途尝试不是错误。
func (s *Service) CancelArchiveCleanup(sessionID string) (CleanupCancellation, error) {
	if err := s.requireSession(sessionID); err != nil {
		return CleanupCancellation{}, err
	}
	rec := s.reclaim
	if rec == nil {
		return CleanupCancellation{}, nil
	}
	rec.mu.Lock()
	attempt := rec.attempts[sessionID]
	if attempt == nil {
		rec.mu.Unlock()
		return CleanupCancellation{}, nil
	}
	// ② 置取消位：与 inspectAndRemove/removeCheckout 的准入判据在同一把锁下判定。
	attempt.cancelled = true
	revision := attempt.revision
	done := attempt.done
	rec.mu.Unlock()

	// ③ await 结算。已准入的 `git worktree remove` 跑完才关 done，所以这里返回
	// 时终态是**确定的**：要么没删（retained），要么真的删了（removed）。
	<-done

	// ④ 收走 attempt（官方 `this.attempts.delete`）。并发取消者都等同一个 done，
	// 都读到同一个终态，只有第一个能把它删掉——不会出现"一个说 retained、另一个
	// 说 removed"的分裂报告，也不会重复触发恢复。
	rec.mu.Lock()
	status := attempt.status
	if rec.attempts[sessionID] == attempt {
		delete(rec.attempts, sessionID)
	}
	rec.mu.Unlock()
	return CleanupCancellation{Cancelled: true, Revision: revision, Status: status}, nil
}

// CleanupOf 读（或等）一个会话最近一次清理尝试。
//
// revision 为 nil 时立刻读；非 nil 且等于当前尝试时等它结算后再读（不启动新
// 尝试）。没有 attempt、revision 已被超越、或服务正在停止时返回 (nil, false)。
func (s *Service) CleanupOf(sessionID string, revision *int64) (*Cleanup, bool) {
	rec := s.reclaim
	if rec == nil {
		return nil, false
	}
	rec.mu.Lock()
	attempt := rec.attempts[sessionID]
	if attempt == nil || (revision != nil && *revision != attempt.revision) {
		rec.mu.Unlock()
		return nil, false
	}
	if revision == nil {
		rec.mu.Unlock()
		return s.cleanupResult(attempt)
	}
	done := attempt.done
	rec.mu.Unlock()
	<-done
	return s.cleanupResult(attempt)
}

// Close 停止归档清理：作废在途尝试、取消它们 Git 调用并等待其结算；之后
// CleanupOf 一律返回 (nil, false)，ScheduleArchiveCleanup 报"没排期"。
//
// 官方对应 `ctx.effect` 的 drain（`worktree: drain archive cleanup`）。
func (s *Service) Close() {
	rec := s.reclaim
	if rec == nil {
		return
	}
	rec.mu.Lock()
	if !rec.stopped {
		rec.stopped = true
		for _, attempt := range rec.attempts {
			attempt.cancelled = true
		}
		rec.cancel()
	}
	rec.mu.Unlock()
	rec.wg.Wait()
}

// ---------------------------------------------------------------------------
// 后台 inspection → 结算
// ---------------------------------------------------------------------------

func (s *Service) runArchiveCleanup(attempt *cleanupAttempt) {
	// 结算必须发生：CancelArchiveCleanup（取消反归档）与 CleanupOf(&revision) 都在
	// await 这个 attempt（官方 archive.ts:91 同样 await attempt.done）。宿主注入的
	// 回调（Unused/ReadOnly/Exclusive）panic 不许把等待者永远挂住 —— 官方那里是
	// try/catch 结算为 failed，这里同义。
	status := CleanupFailed
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				fmt.Fprintf(os.Stderr, "worktree: 归档清理 %s 崩溃，结算为 failed: %v\n", attempt.sessionID, recovered)
			}
		}()
		status = s.inspectAndRemove(attempt)
	}()
	rec := s.reclaim
	settledAt := nowOf(s.hostSnapshot())
	rec.mu.Lock()
	attempt.status = status
	attempt.settledAt = settledAt
	rec.mu.Unlock()
	attempt.finish.Do(func() { close(attempt.done) })
}

// inspectAndRemove 按官方 archive.ts::run 的顺序判：创建者会话是否存在 →
// 只读策略 → unused() → inspection + 非 force 移除。
func (s *Service) inspectAndRemove(attempt *cleanupAttempt) CleanupStatus {
	// 4.1 创建者会话不存在：宿主 store 读不到这个会话的日志 → 保留。
	if _, err := s.store.Load(attempt.sessionID); err != nil {
		return CleanupRetained
	}
	if s.archiveAborted(attempt) {
		return CleanupRetained
	}
	host := s.hostSnapshot()
	// 4.2 创建者沙箱策略只读（或拿不到答案）→ 保留。
	if host.ReadOnly == nil || host.ReadOnly(attempt.sessionID) {
		return CleanupRetained
	}
	// 4.3 还有未归档会话引用（false 或 error）→ 保留。
	if !s.checkUnused(host, attempt.creation.Path, attempt.sessionID) {
		return CleanupRetained
	}
	removed, err := s.removeCheckout(host, attempt)
	if err != nil {
		return CleanupFailed
	}
	if !removed {
		return CleanupRetained
	}
	return CleanupRemoved
}

// removeCheckout 在 path 锁内做二次 unused 判断 → inspection → 非 force 移除。
//
// 返回 (false, nil) 表示"判据保留 / 被取消"；(false, err) 表示 Git 或
// inspection 抛错（结算为 failed，分支保留）。
func (s *Service) removeCheckout(host ArchiveHost, attempt *cleanupAttempt) (bool, error) {
	creation := attempt.creation
	ctx := s.reclaim.lifetime
	removed := false
	err := s.exclusiveCheckout(host, creation.Path, func() error {
		// 锁内二次判断：竞争的操作（目录访问/restore）已经结算完了。
		if s.archiveAborted(attempt) {
			return nil
		}
		if !s.checkUnused(host, creation.Path, attempt.sessionID) {
			return nil
		}
		eligible, inspectErr := s.inspectRemoval(ctx, creation)
		if inspectErr != nil {
			return inspectErr
		}
		if !eligible {
			return nil
		}
		// 官方的 admit()：inspection 之后、真正移除之前再判一次取消。
		if s.archiveAborted(attempt) {
			return nil
		}
		if _, runErr := s.git.Run(ctx, creation.RepositoryRoot, "worktree", "remove", "--", creation.Path); runErr != nil {
			return runErr
		}
		removed = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return removed, nil
}

// ---------------------------------------------------------------------------
// inspection：对齐官方 removal.ts::inspectRemoval 的 6 条判据
// ---------------------------------------------------------------------------

// inspectRemoval 在移除前保守检查这个 checkout。任一判据不满足 → (false, nil)
// 保留；Git 命令本身失败 → (false, err)（结算为 failed）。
func (s *Service) inspectRemoval(ctx context.Context, creation Creation) (bool, error) {
	path, root, branch := creation.Path, creation.RepositoryRoot, creation.Branch

	// 判据 1（身份）：path 必须在 <repositoryRoot>/.agents/worktrees/ 池内（不是
	// 池根自己，也不在别处），且 repositoryRoot 现在仍是那个仓库的顶层。
	pool := filepath.Join(filepath.Clean(root), ".agents", "worktrees")
	if !withinDirectory(pool, path) {
		return false, nil
	}
	top, err := s.git.Run(ctx, root, "rev-parse", "--show-toplevel")
	if err != nil {
		return false, err
	}
	if !samePath(strings.TrimSpace(top), root) {
		return false, nil
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return false, nil
	}

	// 判据 2：`worktree list --porcelain -z` 里有 path 的条目、条目带
	// `branch refs/heads/<branch>`，且不含 detached / locked* / prunable*。
	fields, err := s.worktreeFields(ctx, root, path)
	if err != nil {
		return false, err
	}
	if fields == nil {
		return false, nil
	}
	if !hasField(fields, "branch refs/heads/"+branch) {
		return false, nil
	}
	for _, field := range fields {
		if field == "detached" || strings.HasPrefix(field, "locked") || strings.HasPrefix(field, "prunable") {
			return false, nil
		}
	}

	// 判据 3：checkout 的 HEAD 就是这条分支。
	head, err := s.git.Run(ctx, path, "symbolic-ref", "HEAD")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(head) != "refs/heads/"+branch {
		return false, nil
	}

	// 判据 4：tracked 改动与未跟踪文件都必须为空。ignored 文件（node_modules 等）
	// 和 skip-worktree/assume-unchanged 文件的改动 `git status` 看不见，Git 会
	// 连它们一起丢掉——官方明确如此。
	changed, err := s.git.Run(ctx, path, "status", "--porcelain=v1", "-z",
		"--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return false, err
	}
	if strings.Trim(changed, "\x00 \t\r\n") != "" {
		return false, nil
	}

	// 判据 5：管理目录里的 `modules` 路径不存在（存在 = 初始化过 submodule，
	// Git 会拒绝移除）。
	modules, err := s.git.Run(ctx, path, "rev-parse", "--git-path", "modules")
	if err != nil {
		return false, err
	}
	modulesPath := strings.TrimSpace(modules)
	if modulesPath == "" {
		return false, nil
	}
	if !filepath.IsAbs(modulesPath) {
		modulesPath = filepath.Join(path, filepath.FromSlash(modulesPath))
	}
	if _, statErr := os.Lstat(modulesPath); statErr == nil {
		return false, nil
	}

	// 判据 6：全过 → 允许移除（调用方再执行 `worktree remove --`，非 force）。
	return true, nil
}

// worktreeFields 在源仓库的 `worktree list --porcelain -z` 里找 path 的条目，
// 返回除 `worktree <path>` 之外的字段；找不到返回 nil。
//
// -z 的每条目以 `worktree <path>` 开始、以空字段结束。
func (s *Service) worktreeFields(ctx context.Context, root, path string) ([]string, error) {
	out, err := s.git.Run(ctx, root, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	var entries [][]string
	for _, field := range strings.Split(out, "\x00") {
		switch {
		case strings.HasPrefix(field, "worktree "):
			entries = append(entries, []string{field})
		case field == "":
			// 空字段是条目分隔符。
		default:
			if len(entries) == 0 {
				continue
			}
			last := len(entries) - 1
			entries[last] = append(entries[last], field)
		}
	}
	for _, entry := range entries {
		if samePath(strings.TrimPrefix(entry[0], "worktree "), path) {
			return entry[1:], nil
		}
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

func (s *Service) hostSnapshot() ArchiveHost {
	rec := s.reclaim
	if rec == nil {
		return ArchiveHost{}
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.host
}

func (s *Service) archiveStopped() bool {
	rec := s.reclaim
	if rec == nil {
		return true
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.stopped
}

func (s *Service) archiveAborted(attempt *cleanupAttempt) bool {
	rec := s.reclaim
	if rec == nil {
		return true
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.stopped || attempt.cancelled
}

// cleanupResult 读取一个 attempt 的终态；被超越或服务停止时返回 (nil, false)。
func (s *Service) cleanupResult(attempt *cleanupAttempt) (*Cleanup, bool) {
	rec := s.reclaim
	if rec == nil {
		return nil, false
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.stopped || rec.attempts[attempt.sessionID] != attempt {
		return nil, false
	}
	return &Cleanup{Revision: attempt.revision, Status: attempt.status}, true
}

// checkUnused 问宿主"除了正在归档的会话，还有没有未归档会话引用这个 checkout"。
// nil 回调、false、error 都算"仍被使用"→ 保留。
func (s *Service) checkUnused(host ArchiveHost, path, archivingSessionID string) bool {
	if host.Unused == nil {
		return false
	}
	unused, err := host.Unused(path, archivingSessionID)
	if err != nil {
		// 拿不到答案 = 保守保留，但**不能静默**：否则"为什么这个 checkout 一直
		// 没被清理"永远查不出来（官方同样只把它落成 retained/failed + 一条日志）。
		fmt.Fprintf(os.Stderr, "worktree: 无法判断 %s 是否仍被引用，按保留处理: %v\n", path, err)
		return false
	}
	return unused
}

// exclusiveCheckout 对同一 path 的清理与目录访问互斥：优先用宿主注入的
// Exclusive，没有就用 Service 内的 per-path 互斥。
func (s *Service) exclusiveCheckout(host ArchiveHost, path string, body func() error) error {
	if host.Exclusive != nil {
		return host.Exclusive(path, body)
	}
	rec := s.reclaim
	key := pathKey(path)
	rec.mu.Lock()
	lock := rec.pathLocks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		rec.pathLocks[key] = lock
	}
	rec.mu.Unlock()
	lock.Lock()
	defer lock.Unlock()
	return body()
}

func nowOf(host ArchiveHost) time.Time {
	if host.Now != nil {
		return host.Now()
	}
	return time.Now()
}

// samePath 报告两个路径是否指向同一目录（规范化后大小写不敏感比较）。
func samePath(a, b string) bool {
	first, second := canonicalPath(a), canonicalPath(b)
	if first == "" || second == "" {
		return false
	}
	return strings.EqualFold(filepath.Clean(first), filepath.Clean(second))
}

// withinDirectory 报告 path 是否**严格**位于 root 之下（不含 root 自身）。
func withinDirectory(root, path string) bool {
	rootKey, pathKeyValue := pathKey(root), pathKey(path)
	if rootKey == "" || pathKeyValue == "" || rootKey == pathKeyValue {
		return false
	}
	return strings.HasPrefix(pathKeyValue, rootKey+string(filepath.Separator))
}

func hasField(fields []string, want string) bool {
	for _, field := range fields {
		if field == want {
			return true
		}
	}
	return false
}
