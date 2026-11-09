// Package worktree 是 forgePanel 的 worktree 生命周期服务（官方
// `client-ui-forge` 的 `requestWorktree` / `cancelWorktree` / `retryWorktree` /
// `stopCreation` / `restoreWorktree` / `continueInProject` 六个方法）。
//
// # 官方语义（只读镜像）
//
//	%TEMP%/official-api-src/dsh/node_modules/@deepseek-ai/dsh-experimental-client-ui-forge/README.md
//	§"Worktree option" / §"Creation card" / §"Removed worktree"
//
// 关键事实（逐条对齐）：
//
//   - 分支名固定为 `dsh-<会话 id 前 8 个十六进制位>-<4 位随机十六进制>`，
//     建在**源仓库**的 `.agents/worktrees/<branch>`，checkout 成为会话的工作目录；
//   - 创建分两步（卡片上的两个 stage）：先建分支，再检出文件；
//   - 请求是一条 `plugin:client-ui-forge/worktree` 记录（fold 成 forgeWorktree
//     投影）：request/withdraw 在第一次 turn/start 或创建前生效；
//   - 失败/停止后消息继续等待：`retryWorktree` 用**新分支名**再试，
//     `cancelWorktree` 撤回请求让消息在当前 checkout 继续；
//   - 创建在 agent 的 pre-step（第一步）真正发生，消息的 inbox hold 在
//     创建完成/撤回前不放行；
//   - `restoreWorktree` 在消息等待时重建被删掉的 worktree（分支还在就按分支
//     检出，分支没了就从起始 commit 重建）；`continueInProject` 把会话移回源
//     checkout；
//   - **不删除**已存在的 checkout/分支：失败或取消会留下现场，官方明确如此。
//
// # 本实现的边界
//
//   - 状态持久化复用会话 v4 日志（`plugin:client-ui-forge/worktree` 记录 +
//     `working-directory/change`），重启后从日志折叠，不另建账本；
//   - git 操作用真实 `git` 子进程（仓库里唯一的 git 真值），每个命令都有
//     超时与进程树回收（Windows Job Object）；
//   - 本包不 import `cmd/dshd`：宿主通过 `Store` 注入会话日志读写，
//     这样 transport/host 两层都能测，且 R7 不破。
package worktree

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"dshgo/codecs/v4"
)

// 官方错误码（gateway 词表之外，forge 族自己的 `forgePanel/*`）。
const (
	CodeSessionStarted       = "forgePanel/session-started"
	CodeWorktreesUnavailable = "forgePanel/worktrees-unavailable"
	CodeInvalidBranch        = "forgePanel/invalid-branch"
	CodeNotWaiting           = "forgePanel/not-waiting"
	CodeNotCreated           = "forgePanel/not-created"
	CodeNotACheckout         = "forgePanel/not-a-checkout"
	CodeWorktreeExists       = "forgePanel/worktree-exists"
	CodeWorktreePresent      = "forgePanel/worktree-present"
	CodeCreateFailed         = "forgePanel/create-failed"
	CodeRestoreFailed        = "forgePanel/restore-failed"
)

// Error 是 worktree 服务的结构化错误（transport 层翻成官方四字段）。
type Error struct {
	Code    string
	Status  int
	Message string
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	return e.Code + ": " + e.Message
}

func newError(code string, status int, format string, args ...any) *Error {
	return &Error{Code: code, Status: status, Message: fmt.Sprintf(format, args...)}
}

// EventType 是工作区请求/生命周期记录的事件名（官方 `plugin:` 记录）。
const EventType = "plugin:client-ui-forge/worktree"

// Record 是一条 worktree 记录（落进会话 v4 日志的 data）。
type Record struct {
	Type       string `json:"type"`
	Branch     string `json:"branch,omitempty"`
	Base       string `json:"base,omitempty"`
	BaseCommit string `json:"baseCommit,omitempty"`
	Directory  string `json:"directory,omitempty"`
	SourceRoot string `json:"sourceRoot,omitempty"`
	Step       string `json:"step,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// 记录类型。
const (
	RecordRequest  = "request"
	RecordRunning  = "running"
	RecordCreated  = "created"
	RecordFailed   = "failed"
	RecordStopped  = "stopped"
	RecordRestored = "restored"
	RecordWithdraw = "withdraw"
)

// Status 是折叠后的状态（面板/测试读）。
type Status string

const (
	StatusNone      Status = "none"
	StatusOpen      Status = "open"
	StatusRunning   Status = "running"
	StatusCreated   Status = "created"
	StatusFailed    Status = "failed"
	StatusStopped   Status = "stopped"
	StatusWithdrawn Status = "withdrawn"
)

// Snapshot 是会话日志折叠出的 worktree 状态。
type Snapshot struct {
	// Cwd 是会话的**有效**工作目录（header.cwd + working-directory/change）。
	Cwd string
	// HeaderCwd 是 header 里的原始 cwd（源 checkout 的候选）。
	HeaderCwd string
	// SourceRoot 是创建 worktree 时记录的源仓库根。
	SourceRoot string

	Requested   bool
	Withdrawn   bool
	TurnStarted bool

	Branch     string
	Base       string
	BaseCommit string
	Directory  string

	Created *Record
	Failure *Record
	Stopped *Record
}

// Store 是会话日志的读写口（宿主实现；测试用内存实现）。
type Store interface {
	// Load 折叠一个会话的 worktree 状态。
	Load(sessionID string) (Snapshot, error)
	// AppendWorktree 追加一条 worktree 记录。
	AppendWorktree(sessionID string, record Record) error
	// SetWorkingDirectory 把会话有效目录改成 dir（会追加 working-directory/change）。
	SetWorkingDirectory(sessionID, dir string) error
}

// Fold 把会话事件折叠成 Snapshot。
//
// 规则与官方投影一致：request/withdraw 在第一次 turn/start 或创建前生效；
// 第一条 created 生效；failed/stopped 保留最近一次；restored 把状态恢复成
// created；working-directory/change 覆盖有效目录。
func Fold(headerCwd string, events []v4.Event) Snapshot {
	snapshot := Snapshot{HeaderCwd: headerCwd, Cwd: headerCwd}
	createdSeen := false
	for _, event := range events {
		switch event.Type {
		case "turn/start":
			snapshot.TurnStarted = true
		case "working-directory/change":
			var data struct {
				Cwd string `json:"cwd"`
			}
			if err := json.Unmarshal(event.Data, &data); err == nil && strings.TrimSpace(data.Cwd) != "" {
				snapshot.Cwd = data.Cwd
			}
		case EventType:
			var record Record
			if err := json.Unmarshal(event.Data, &record); err != nil {
				continue
			}
			switch record.Type {
			case RecordRequest:
				if createdSeen || snapshot.Withdrawn {
					continue
				}
				snapshot.Requested = true
				snapshot.Branch = record.Branch
				snapshot.Base = record.Base
				snapshot.Directory = record.Directory
				if record.SourceRoot != "" {
					snapshot.SourceRoot = record.SourceRoot
				}
				snapshot.Failure = nil
				snapshot.Stopped = nil
			case RecordRunning:
				if createdSeen || snapshot.Withdrawn {
					continue
				}
				snapshot.Requested = true
				if record.Branch != "" {
					snapshot.Branch = record.Branch
				}
				if record.Directory != "" {
					snapshot.Directory = record.Directory
				}
				if record.SourceRoot != "" {
					snapshot.SourceRoot = record.SourceRoot
				}
			case RecordCreated:
				if createdSeen {
					continue
				}
				createdSeen = true
				snapshot.Created = &record
				snapshot.Requested = true
				snapshot.Withdrawn = false
				snapshot.Failure = nil
				snapshot.Stopped = nil
				snapshot.Branch = record.Branch
				snapshot.Base = record.Base
				snapshot.BaseCommit = record.BaseCommit
				snapshot.Directory = record.Directory
				if record.SourceRoot != "" {
					snapshot.SourceRoot = record.SourceRoot
				}
			case RecordFailed:
				if createdSeen {
					continue
				}
				copied := record
				snapshot.Failure = &copied
				snapshot.Stopped = nil
			case RecordStopped:
				if createdSeen {
					continue
				}
				copied := record
				snapshot.Stopped = &copied
			case RecordRestored:
				createdSeen = true
				copied := record
				snapshot.Created = &copied
				snapshot.Withdrawn = false
				snapshot.Failure = nil
				snapshot.Stopped = nil
				snapshot.Branch = record.Branch
				snapshot.Directory = record.Directory
				if record.SourceRoot != "" {
					snapshot.SourceRoot = record.SourceRoot
				}
			case RecordWithdraw:
				snapshot.Withdrawn = true
			}
		}
	}
	return snapshot
}

// State 是对外（测试/投影）的状态视图。
type State struct {
	Status     Status
	Branch     string
	Base       string
	Directory  string
	SourceRoot string
	Step       string
	Reason     string
	Cwd        string
}

// Git 是 git 命令执行口（真实实现是 ExecGit；测试可注入）。
type Git interface {
	Run(ctx context.Context, dir string, args ...string) (string, error)
}

// ExecGit 用真实 git 子进程执行命令。
type ExecGit struct {
	// Binary 默认 "git"。
	Binary string
	// Timeout 默认 60s（worktree add 在大会话上可能慢，但必须有界）。
	Timeout time.Duration
}

// Run 执行一条 git 命令并返回合并输出。
func (g ExecGit) Run(ctx context.Context, dir string, args ...string) (string, error) {
	binary := g.Binary
	if binary == "" {
		binary = "git"
	}
	timeout := g.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return runCommand(runCtx, dir, binary, args...)
}

// Service 是 worktree 生命周期服务。
type Service struct {
	store Store
	git   Git
	now   func() time.Time

	mu       sync.Mutex
	sessions map[string]*sessionState
}

type sessionState struct {
	mu      sync.Mutex
	attempt *attempt
	waiters map[chan struct{}]struct{}
}

type attempt struct {
	cancel context.CancelFunc
	done   chan struct{}
	branch string
}

// NewService 造一个 worktree 服务。
func NewService(store Store, git Git) *Service {
	return &Service{store: store, git: git, now: time.Now, sessions: map[string]*sessionState{}}
}

func (s *Service) sessionState(sessionID string) *sessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.sessions[sessionID]
	if state == nil {
		state = &sessionState{waiters: map[chan struct{}]struct{}{}}
		s.sessions[sessionID] = state
	}
	return state
}

// notify 唤醒所有 hold 的等待者（状态已经落到 created/withdrawn 时）。
func (s *Service) notify(state *sessionState) {
	state.mu.Lock()
	defer state.mu.Unlock()
	for ch := range state.waiters {
		close(ch)
	}
	state.waiters = map[chan struct{}]struct{}{}
}

// State 返回折叠后的状态（测试/投影用）。
func (s *Service) State(sessionID string) (State, error) {
	snapshot, err := s.store.Load(sessionID)
	if err != nil {
		return State{}, err
	}
	return stateOf(snapshot), nil
}

func stateOf(snapshot Snapshot) State {
	state := State{
		Status:     StatusNone,
		Branch:     snapshot.Branch,
		Base:       snapshot.Base,
		Directory:  snapshot.Directory,
		SourceRoot: snapshot.SourceRoot,
		Cwd:        snapshot.Cwd,
	}
	switch {
	case snapshot.Withdrawn:
		state.Status = StatusWithdrawn
	case snapshot.Created != nil:
		state.Status = StatusCreated
	case snapshot.Failure != nil:
		state.Status = StatusFailed
		state.Step = snapshot.Failure.Step
		state.Reason = snapshot.Failure.Reason
	case snapshot.Stopped != nil:
		state.Status = StatusStopped
	case snapshot.Requested:
		state.Status = StatusOpen
	}
	if snapshot.Failure == nil && snapshot.Stopped == nil && snapshot.Requested && snapshot.Created == nil && !snapshot.Withdrawn {
		state.Status = StatusOpen
	}
	return state
}

// ---------------------------------------------------------------------------
// 六个官方方法
// ---------------------------------------------------------------------------

// RequestWorktree 记录一次 worktree 请求（真正的创建在 pre-step/hold 里）。
func (s *Service) RequestWorktree(sessionID string, from *string) error {
	if err := s.requireSession(sessionID); err != nil {
		return err
	}
	if from != nil && strings.TrimSpace(*from) == "" {
		return newError(CodeInvalidBranch, 400, "from 不能是空串（要么给 base 分支，要么给 null 用 HEAD）")
	}
	state := s.sessionState(sessionID)
	state.mu.Lock()
	running := state.attempt != nil
	state.mu.Unlock()

	snapshot, err := s.store.Load(sessionID)
	if err != nil {
		return err
	}
	if snapshot.TurnStarted || snapshot.Created != nil {
		return newError(CodeSessionStarted, 400,
			"会话已经开始了 turn 或 worktree 已创建（requestWorktree 只能在第一条消息前请求）")
	}
	if snapshot.Requested && !snapshot.Withdrawn {
		// 幂等：同一条请求再发一次不改变状态。
		return nil
	}
	if running {
		return newError(CodeNotWaiting, 409, "worktree 正在创建，不能重复请求")
	}
	root, _, _, infoErr := s.repoInfo(snapshot.Cwd)
	if infoErr != nil {
		return infoErr
	}
	base := "HEAD"
	if from != nil {
		base = strings.TrimSpace(*from)
	}
	branch, err := newBranchName(sessionID)
	if err != nil {
		return newError(CodeCreateFailed, 500, "生成 worktree 分支名失败: %v", err)
	}
	directory := worktreeDirectory(root, branch)
	if _, statErr := os.Stat(directory); statErr == nil {
		return newError(CodeWorktreeExists, 409, "worktree 目录已存在：%s（官方不删除已有 checkout，请先清理或换一个）", directory)
	}
	record := Record{
		Type: RecordRequest, Branch: branch, Base: base,
		Directory: directory, SourceRoot: root,
	}
	if err := s.store.AppendWorktree(sessionID, record); err != nil {
		return err
	}
	return nil
}

// CancelWorktree 撤回请求（消息在当前 checkout 继续）；运行中的创建会被停止。
func (s *Service) CancelWorktree(sessionID string) error {
	if err := s.requireSession(sessionID); err != nil {
		return err
	}
	snapshot, err := s.store.Load(sessionID)
	if err != nil {
		return err
	}
	if snapshot.TurnStarted || snapshot.Created != nil {
		return newError(CodeSessionStarted, 400, "会话已经开始 turn 或 worktree 已创建，不能撤回")
	}
	state := s.sessionState(sessionID)
	state.mu.Lock()
	running := state.attempt
	state.mu.Unlock()
	if running != nil {
		running.cancel()
		<-running.done
	}
	if !snapshot.Requested || snapshot.Withdrawn {
		// 没有待撤回的请求：官方只拒绝 session-started，这里幂等成功。
		return nil
	}
	if err := s.store.AppendWorktree(sessionID, Record{Type: RecordWithdraw}); err != nil {
		return err
	}
	s.notify(state)
	return nil
}

// RetryWorktree 在失败/停止后用新分支名再试一次（异步，hold 继续等待）。
func (s *Service) RetryWorktree(sessionID string) error {
	if err := s.requireSession(sessionID); err != nil {
		return err
	}
	snapshot, err := s.store.Load(sessionID)
	if err != nil {
		return err
	}
	if !snapshot.Requested || snapshot.Withdrawn || snapshot.Created != nil {
		return newError(CodeNotWaiting, 409, "没有等待中的 worktree 请求（retryWorktree 只用于失败/停止后的重试）")
	}
	state := s.sessionState(sessionID)
	state.mu.Lock()
	if state.attempt != nil {
		state.mu.Unlock()
		return newError(CodeNotWaiting, 409, "worktree 正在创建，不能重试")
	}
	state.mu.Unlock()
	if snapshot.Failure == nil && snapshot.Stopped == nil {
		return newError(CodeNotWaiting, 409, "没有失败/停止的 worktree 尝试")
	}
	root, _, _, infoErr := s.repoInfo(snapshot.Cwd)
	if infoErr != nil {
		return infoErr
	}
	branch, err := newBranchName(sessionID)
	if err != nil {
		return newError(CodeCreateFailed, 500, "生成 worktree 分支名失败: %v", err)
	}
	directory := worktreeDirectory(root, branch)
	if _, statErr := os.Stat(directory); statErr == nil {
		return newError(CodeWorktreeExists, 409, "worktree 目录已存在：%s", directory)
	}
	record := Record{Type: RecordRequest, Branch: branch, Base: snapshot.Base, Directory: directory, SourceRoot: root}
	if record.Base == "" {
		record.Base = "HEAD"
	}
	if err := s.store.AppendWorktree(sessionID, record); err != nil {
		return err
	}
	// 异步创建：UI 的 spinner 走 running 状态，hold 等 created/failed。
	go func() {
		_ = s.create(context.Background(), sessionID, record)
	}()
	return nil
}

// StopCreation 停止正在进行的创建（消息继续等待重试/撤回）。
func (s *Service) StopCreation(sessionID string) error {
	if err := s.requireSession(sessionID); err != nil {
		return err
	}
	snapshot, err := s.store.Load(sessionID)
	if err != nil {
		return err
	}
	if !snapshot.Requested || snapshot.Withdrawn || snapshot.Created != nil {
		return newError(CodeNotWaiting, 409, "没有等待中的 worktree 创建")
	}
	state := s.sessionState(sessionID)
	state.mu.Lock()
	running := state.attempt
	state.mu.Unlock()
	if running != nil {
		running.cancel()
		<-running.done
	}
	if snapshot.Stopped != nil && running == nil {
		return nil
	}
	if err := s.store.AppendWorktree(sessionID, Record{Type: RecordStopped, Branch: snapshot.Branch}); err != nil {
		return err
	}
	return nil
}

// RestoreWorktree 重建被删掉的 worktree 并把会话目录切过去。
func (s *Service) RestoreWorktree(sessionID string) error {
	if err := s.requireSession(sessionID); err != nil {
		return err
	}
	snapshot, err := s.store.Load(sessionID)
	if err != nil {
		return err
	}
	if snapshot.Created == nil || snapshot.Withdrawn {
		return newError(CodeNotCreated, 409, "这个会话没有已创建的 worktree，不能 restore")
	}
	if snapshot.Directory == "" {
		return newError(CodeNotCreated, 409, "worktree 记录缺少目录")
	}
	root := snapshot.SourceRoot
	if root == "" {
		root = snapshot.HeaderCwd
	}
	if root == "" {
		return newError(CodeNotACheckout, 400, "worktree 记录缺少源 checkout 路径")
	}
	ctx := context.Background()
	info := s.inspectWorktree(ctx, snapshot.Directory)
	switch info.presence {
	case presenceValid:
		// 上一次 restore 已经把目录建好（同分支 + 同一 base commit），只是记录
		// 没写成（例如会话写锁 409）：补齐记录，幂等成功，而不是永远 409 present。
		//
		// 这不是"放宽 present 判据"：HEAD 已经移动（用户在用这个 checkout）或
		// 分支不同时仍然按官方语义 409。
		if snapshot.Created.BaseCommit != "" && info.head == snapshot.Created.BaseCommit && info.branch == snapshot.Branch {
			return s.finishRestore(ctx, sessionID, snapshot, root, snapshot.Directory, info.head)
		}
		return newError(CodeWorktreePresent, 409, "worktree 目录仍然存在：%s", snapshot.Directory)
	case presenceStale:
		// 空目录/没有 .git/损坏的 index 不是"用户在用的 worktree"，是上一次
		// 失败留下的现场：清掉注册与目录，重建。
		if err := s.cleanupStaleWorktree(ctx, root, snapshot.Directory); err != nil {
			return err
		}
	}
	return s.restore(ctx, sessionID, snapshot, root)
}

// worktreePresence 是"目录现在是什么"的三分类。
//
// 为什么不是 os.Stat 一票：Windows 上被文件监听/杀毒持有句柄的目录在删除后
// 仍可能 Stat 成功（删除挂起）；而一次 restore 在"目录已建、记录未写"时失败
// 也会留下一个看起来 present 的目录。只按 Stat 判会把这两种情况都误判成
// "目录还在"，于是 restore 永远 409。这里按**能不能当 worktree 用**分类。
type worktreePresence int

const (
	presenceMissing worktreePresence = iota
	presenceStale
	presenceValid
)

type worktreeInfo struct {
	presence worktreePresence
	head     string
	branch   string
}

// inspectWorktree 判断目录是缺失、有效 checkout，还是"看起来存在但已经不是
// 一个可用 worktree"。
func (s *Service) inspectWorktree(ctx context.Context, directory string) worktreeInfo {
	info, err := os.Lstat(directory)
	if err != nil {
		if os.IsNotExist(err) {
			return worktreeInfo{presence: presenceMissing}
		}
		return worktreeInfo{presence: presenceStale}
	}
	if !info.IsDir() {
		return worktreeInfo{presence: presenceStale}
	}
	// worktree 的 .git 是文件（gitdir: ...）；主 checkout 的 .git 是目录。
	if _, err := os.Lstat(filepath.Join(directory, ".git")); err != nil {
		return worktreeInfo{presence: presenceStale}
	}
	if _, err := s.git.Run(ctx, directory, "rev-parse", "--show-toplevel"); err != nil {
		return worktreeInfo{presence: presenceStale}
	}
	head, err := s.git.Run(ctx, directory, "rev-parse", "HEAD")
	if err != nil {
		return worktreeInfo{presence: presenceStale}
	}
	branch, err := s.git.Run(ctx, directory, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return worktreeInfo{presence: presenceStale}
	}
	return worktreeInfo{presence: presenceValid, head: strings.TrimSpace(head), branch: strings.TrimSpace(branch)}
}

// cleanupStaleWorktree 清掉"已经不存在/不完整"的目录与注册，给重建让路。
func (s *Service) cleanupStaleWorktree(ctx context.Context, root, directory string) error {
	// 先让 git 摘掉注册（目录无效时 remove 会失败，忽略后统一 prune）。
	_, _ = s.git.Run(ctx, root, "worktree", "remove", "--force", directory)
	if _, err := s.git.Run(ctx, root, "worktree", "prune"); err != nil {
		return newError(CodeRestoreFailed, 500, "清理 worktree 注册失败：%v", err)
	}
	if err := removeDirWithRetry(directory, 20, 100*time.Millisecond); err != nil {
		return newError(CodeRestoreFailed, 500,
			"worktree 目录无法清理（可能被文件监听/杀毒占用）：%s；关闭占用后重试：%v", directory, err)
	}
	return nil
}

// removeDirWithRetry 处理 Windows 上"删除已标记但仍能 Lstat 到"的目录。
func removeDirWithRetry(directory string, attempts int, delay time.Duration) error {
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if _, err := os.Lstat(directory); os.IsNotExist(err) {
			return nil
		}
		if err := os.RemoveAll(directory); err != nil {
			lastErr = err
		} else if _, err := os.Lstat(directory); os.IsNotExist(err) {
			return nil
		}
		time.Sleep(delay)
	}
	if _, err := os.Lstat(directory); os.IsNotExist(err) {
		return nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("目录仍然存在")
	}
	return lastErr
}

// finishRestore 写 restore 记录并把会话目录切到 worktree（可重入）。
func (s *Service) finishRestore(ctx context.Context, sessionID string, snapshot Snapshot, root, directory, baseCommit string) error {
	record := Record{
		Type: RecordRestored, Branch: snapshot.Branch, Base: snapshot.Base,
		BaseCommit: baseCommit, Directory: directory, SourceRoot: root,
	}
	if err := s.store.AppendWorktree(sessionID, record); err != nil {
		return s.wrapRestoreError("写 restore 记录失败", err)
	}
	if err := s.store.SetWorkingDirectory(sessionID, directory); err != nil {
		return s.wrapRestoreError("切换会话目录失败", err)
	}
	s.notify(s.sessionState(sessionID))
	return nil
}

func (s *Service) wrapRestoreError(what string, err error) error {
	var typed *Error
	if errors.As(err, &typed) {
		return typed
	}
	return newError(CodeRestoreFailed, 500, "%s：%v", what, err)
}

// ContinueInProject 把会话移回源 checkout 并结束 worktree 记录。
func (s *Service) ContinueInProject(sessionID string) error {
	if err := s.requireSession(sessionID); err != nil {
		return err
	}
	snapshot, err := s.store.Load(sessionID)
	if err != nil {
		return err
	}
	if snapshot.Created == nil || snapshot.Withdrawn {
		return newError(CodeNotCreated, 409, "这个会话没有已创建的 worktree，不能 continueInProject")
	}
	root := snapshot.SourceRoot
	if root == "" {
		root = snapshot.HeaderCwd
	}
	if root == "" {
		return newError(CodeNotACheckout, 400, "worktree 记录缺少源 checkout 路径")
	}
	if err := s.store.SetWorkingDirectory(sessionID, root); err != nil {
		return err
	}
	if err := s.store.AppendWorktree(sessionID, Record{Type: RecordWithdraw}); err != nil {
		return err
	}
	s.notify(s.sessionState(sessionID))
	return nil
}

// ---------------------------------------------------------------------------
// pre-step / hold
// ---------------------------------------------------------------------------

// PreStep 是 agent 的 `agent/pre-step` 钩子：第 1 步创建请求中的 worktree。
//
// 失败返回 error 让本轮结束（官方：失败结束 turn 且保留请求）。
func (s *Service) PreStep(ctx context.Context, sessionID string, step int) error {
	if step != 1 {
		return nil
	}
	if err := s.requireSession(sessionID); err != nil {
		return err
	}
	snapshot, err := s.store.Load(sessionID)
	if err != nil {
		return err
	}
	if !snapshot.Requested || snapshot.Withdrawn || snapshot.Created != nil {
		return nil
	}
	// 失败/停止后的消息由 hold 拦住，等 retryWorktree 或 cancelWorktree；
	// pre-step 不自动重试（否则会拿同一个已存在的分支再撞一次）。
	if snapshot.Failure != nil || snapshot.Stopped != nil {
		return nil
	}
	state := s.sessionState(sessionID)
	state.mu.Lock()
	if state.attempt != nil {
		state.mu.Unlock()
		return nil
	}
	state.mu.Unlock()
	record := Record{
		Type: RecordRunning, Branch: snapshot.Branch, Base: snapshot.Base,
		Directory: snapshot.Directory, SourceRoot: snapshot.SourceRoot,
	}
	if record.Directory == "" || record.Branch == "" {
		return newError(CodeCreateFailed, 500, "worktree 请求记录缺少分支/目录")
	}
	return s.create(ctx, sessionID, record)
}

// Wait 是消息的 inbox hold：创建完成或请求被撤回前阻塞。
//
// 失败/停止不会放行——官方语义就是"消息继续等重试或撤回"。
func (s *Service) Wait(ctx context.Context, sessionID string) error {
	state := s.sessionState(sessionID)
	for {
		snapshot, err := s.store.Load(sessionID)
		if err != nil {
			return err
		}
		if !snapshot.Requested || snapshot.Withdrawn || snapshot.Created != nil {
			return nil
		}
		state.mu.Lock()
		ch := make(chan struct{})
		state.waiters[ch] = struct{}{}
		state.mu.Unlock()
		select {
		case <-ctx.Done():
			state.mu.Lock()
			delete(state.waiters, ch)
			state.mu.Unlock()
			return ctx.Err()
		case <-ch:
		}
	}
}

// ---------------------------------------------------------------------------
// 创建 / 恢复
// ---------------------------------------------------------------------------

func (s *Service) create(ctx context.Context, sessionID string, record Record) error {
	state := s.sessionState(sessionID)
	runCtx, cancel := context.WithCancel(ctx)
	current := &attempt{cancel: cancel, done: make(chan struct{}), branch: record.Branch}
	state.mu.Lock()
	if state.attempt != nil {
		state.mu.Unlock()
		cancel()
		return newError(CodeNotWaiting, 409, "worktree 正在创建")
	}
	state.attempt = current
	state.mu.Unlock()
	defer func() {
		state.mu.Lock()
		if state.attempt == current {
			state.attempt = nil
		}
		state.mu.Unlock()
		close(current.done)
	}()

	root := record.SourceRoot
	if root == "" {
		root = record.Directory
	}
	baseCommit, err := s.createWorktree(runCtx, sessionID, record, root)
	if err != nil {
		if runCtx.Err() != nil {
			// 被 stopCreation/cancel 主动取消：记录 stopped，不记 failed。
			_ = s.store.AppendWorktree(sessionID, Record{Type: RecordStopped, Branch: record.Branch})
			return nil
		}
		failed := Record{Type: RecordFailed, Branch: record.Branch, Step: stepOf(err), Reason: err.Error()}
		_ = s.store.AppendWorktree(sessionID, failed)
		s.notify(state)
		return err
	}
	created := Record{
		Type: RecordCreated, Branch: record.Branch, Base: record.Base,
		BaseCommit: baseCommit, Directory: record.Directory, SourceRoot: record.SourceRoot,
	}
	if err := s.store.AppendWorktree(sessionID, created); err != nil {
		return err
	}
	if err := s.store.SetWorkingDirectory(sessionID, record.Directory); err != nil {
		return err
	}
	s.notify(state)
	return nil
}

// createWorktree 跑官方两步：建分支 → 检出文件；返回 base commit。
func (s *Service) createWorktree(ctx context.Context, sessionID string, record Record, root string) (string, error) {
	base := record.Base
	if base == "" {
		base = "HEAD"
	}
	if _, err := s.git.Run(ctx, root, "worktree", "add", "--no-checkout", "-b", record.Branch, record.Directory, base); err != nil {
		return "", &stepError{step: "creating-branch", err: err}
	}
	commit, err := s.git.Run(ctx, root, "rev-parse", record.Branch)
	if err != nil {
		return "", &stepError{step: "creating-branch", err: err}
	}
	commit = strings.TrimSpace(commit)
	if err := s.store.AppendWorktree(sessionID, Record{
		Type: RecordRunning, Branch: record.Branch, Base: record.Base,
		Directory: record.Directory, SourceRoot: record.SourceRoot, Step: "checking-out-files",
	}); err != nil {
		return "", err
	}
	if _, err := s.git.Run(ctx, record.Directory, "reset", "--hard", commit); err != nil {
		return "", &stepError{step: "checking-out-files", err: err}
	}
	return commit, nil
}

// restore 重建 worktree（分支还在就检出，没了就从起始 commit 建分支）。
//
// 幂等 + 可回滚：
//   - 目录有残留（空目录/损坏 checkout）先清掉；
//   - git add 前后任何一步失败都清掉现场，避免下一次被误判 present；
//   - 记录写不进去（会话写锁 409）时回滚刚建的目录，下一次重试从干净状态开始。
func (s *Service) restore(ctx context.Context, sessionID string, snapshot Snapshot, root string) error {
	directory := snapshot.Directory
	branch := snapshot.Branch
	baseCommit := ""
	if snapshot.Created != nil {
		baseCommit = snapshot.Created.BaseCommit
	}
	// 目录可能在上一次失败里留下半成品：先清干净再建（不能靠 add -f 硬来）。
	if _, err := os.Lstat(directory); err == nil {
		if err := s.cleanupStaleWorktree(ctx, root, directory); err != nil {
			return err
		}
	}
	// 分支还在吗？
	branchExists := false
	if branch != "" {
		if _, err := s.git.Run(ctx, root, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
			branchExists = true
		}
	}
	_ = s.store.AppendWorktree(sessionID, Record{
		Type: RecordRunning, Branch: branch, Directory: directory, SourceRoot: root, Step: "preparing-worktree",
	})
	// 目录没了但注册还在：官方语义是"只清掉缺失的注册"，否则 add 会被拒。
	if _, pruneErr := s.git.Run(ctx, root, "worktree", "prune"); pruneErr != nil {
		return newError(CodeRestoreFailed, 500, "清理缺失的 worktree 注册失败：%v", pruneErr)
	}
	var err error
	if branchExists {
		_, err = s.git.Run(ctx, root, "worktree", "add", directory, branch)
	} else {
		if baseCommit == "" {
			baseCommit = "HEAD"
		}
		if _, err = s.git.Run(ctx, root, "worktree", "add", "-b", branch, directory, baseCommit); err == nil {
			if commit, revErr := s.git.Run(ctx, root, "rev-parse", branch); revErr == nil {
				baseCommit = strings.TrimSpace(commit)
			}
		}
	}
	if err != nil {
		// add 失败可能留下空目录/半个 checkout：一并清掉，否则下一次 restore
		// 会把它当成"目录仍然存在"。
		_ = s.cleanupStaleWorktree(ctx, root, directory)
		failed := Record{Type: RecordFailed, Branch: branch, Step: "restoring", Reason: err.Error()}
		_ = s.store.AppendWorktree(sessionID, failed)
		return newError(CodeRestoreFailed, 500, "重建 worktree 失败：%v", err)
	}
	// 建完必须真的是一个可用 checkout，否则清掉重来（不把假现场当成功）。
	info := s.inspectWorktree(ctx, directory)
	if info.presence != presenceValid || info.branch != branch {
		_ = s.cleanupStaleWorktree(ctx, root, directory)
		reason := fmt.Sprintf("重建后的 checkout 不可用（presence=%d branch=%q）", info.presence, info.branch)
		_ = s.store.AppendWorktree(sessionID, Record{Type: RecordFailed, Branch: branch, Step: "restoring", Reason: reason})
		return newError(CodeRestoreFailed, 500, "%s", reason)
	}
	if info.head != "" {
		baseCommit = info.head
	}
	if err := s.finishRestore(ctx, sessionID, snapshot, root, directory, baseCommit); err != nil {
		// 目录是刚建的，记录写不进去就回滚，避免下一次被误判 present。
		_ = s.cleanupStaleWorktree(ctx, root, directory)
		_ = s.store.AppendWorktree(sessionID, Record{Type: RecordFailed, Branch: branch, Step: "restoring", Reason: err.Error()})
		return s.wrapRestoreError("重建 worktree 后写记录失败（已回滚）", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// git 辅助
// ---------------------------------------------------------------------------

// repoInfo 解析源仓库根与 git 目录；项目本身是 linked worktree 时拒绝。
func (s *Service) repoInfo(cwd string) (root, gitDir, commonDir string, err error) {
	if strings.TrimSpace(cwd) == "" {
		return "", "", "", newError(CodeNotACheckout, 400, "会话没有工作目录，无法确定源 checkout")
	}
	if _, statErr := os.Stat(cwd); statErr != nil || !isDir(cwd) {
		return "", "", "", newError(CodeNotACheckout, 400, "会话工作目录不存在：%s", cwd)
	}
	root, err = s.git.Run(context.Background(), cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", "", newError(CodeNotACheckout, 400, "会话目录不在 git checkout 里：%s", cwd)
	}
	root = canonicalPath(strings.TrimSpace(root))
	if root == "" {
		return "", "", "", newError(CodeNotACheckout, 400, "git 没有给出仓库根：%s", cwd)
	}
	gitDir, err = s.git.Run(context.Background(), root, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		return "", "", "", newError(CodeNotACheckout, 400, "读 .git 目录失败：%v", err)
	}
	commonDir, err = s.git.Run(context.Background(), root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", "", "", newError(CodeNotACheckout, 400, "读公共 .git 目录失败：%v", err)
	}
	gitDir = filepath.Clean(strings.TrimSpace(gitDir))
	commonDir = filepath.Clean(strings.TrimSpace(commonDir))
	if !strings.EqualFold(gitDir, commonDir) {
		return "", "", "", newError(CodeWorktreesUnavailable, 400,
			"项目本身是一个 linked worktree（.git 与公共 .git 不同），官方在这种情况下禁用 worktree 选项")
	}
	return root, gitDir, commonDir, nil
}

func (s *Service) requireSession(sessionID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return newError(CodeSessionStarted, 400, "sessionId 不能为空")
	}
	return nil
}

// canonicalPath 把 8.3 短名/符号链接解析成稳定长路径（git 在 Windows 上会给
// `ADMINI~1` 这种短名，直接拿来比路径会把同一个目录判成两个）。
func canonicalPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	return filepath.Clean(path)
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func worktreeDirectory(root, branch string) string {
	return filepath.Join(filepath.Clean(root), ".agents", "worktrees", branch)
}

// newBranchName 生成官方命名：dsh-<session 前 8 个十六进制>-<4 位随机十六进制>。
func newBranchName(sessionID string) (string, error) {
	compact := strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(sessionID))
	if !isHex(compact) || len(compact) < 8 {
		sum := sha256.Sum256([]byte(sessionID))
		compact = hex.EncodeToString(sum[:])
	}
	var random [2]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return "dsh-" + compact[:8] + "-" + hex.EncodeToString(random[:]), nil
}

func isHex(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}

// stepError 标记失败发生在哪个官方 stage。
type stepError struct {
	step string
	err  error
}

func (e *stepError) Error() string { return e.step + " 失败：" + e.err.Error() }
func (e *stepError) Unwrap() error { return e.err }

func stepOf(err error) string {
	var step *stepError
	if errors.As(err, &step) {
		return step.step
	}
	return "creating-branch"
}
