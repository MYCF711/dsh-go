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
	"fmt"
	"strings"

	"dshgo/codecs/v4"
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
