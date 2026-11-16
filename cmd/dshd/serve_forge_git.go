package main

// PARITY-W2-GIT: forgePanel.branches/localDiff/localText 的宿主实现。
// 所有结果来自真实 git 进程（internal/forge），会话目录由 resolver 提供。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"dshgo/internal/forge"
	"dshgo/internal/transport"
	"dshgo/internal/worktree"
)

type forgeGitAPI struct {
	exec      *forge.Executor
	resolve   func(sessionID string) (string, error)
	worktrees *worktree.Service
}

func newForgeGitAPI(resolve func(string) (string, error), worktrees *worktree.Service) *forgeGitAPI {
	return &forgeGitAPI{exec: forge.NewExecutor(), resolve: resolve, worktrees: worktrees}
}

func (a *forgeGitAPI) repoDir(sessionID string) (string, error) {
	if a.resolve == nil {
		return "", errors.New("forgePanel: 宿主没有会话目录解析器")
	}
	dir, err := a.resolve(sessionID)
	if err != nil {
		return "", err
	}
	if dir == "" {
		return "", &transport.ForgeGitError{Status: 400, Code: "bad_request", Message: "sessionId 找不到目录"}
	}
	return dir, nil
}

func (a *forgeGitAPI) HandleOfficialForgeGit(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	var p map[string]json.RawMessage
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &transport.ForgeGitError{Status: 400, Code: "bad_request", Message: "参数不是 JSON 对象"}
	}
	str := func(k string) string { var v string; _ = json.Unmarshal(p[k], &v); return v }
	sessionID := str("sessionId")
	if sessionID == "" {
		return nil, &transport.ForgeGitError{Status: 400, Code: "bad_request", Message: "sessionId 必填"}
	}
	dir, err := a.repoDir(sessionID)
	if err != nil {
		return nil, &transport.ForgeGitError{Status: 400, Code: "bad_request", Message: err.Error()}
	}
	switch method {
	case string(transport.MethodForgePanelBranches):
		var max int
		_ = json.Unmarshal(p["maxBranches"], &max)
		info, err := a.exec.Branches(ctx, dir, max)
		if err != nil {
			return nil, &transport.ForgeGitError{Status: 422, Code: "not_a_repository", Message: err.Error()}
		}
		return info, nil
	case string(transport.MethodForgePanelLocalDiff):
		var scope forge.Scope
		if len(p["scope"]) == 0 || json.Unmarshal(p["scope"], &scope) != nil || scope.Kind == "" {
			return nil, &transport.ForgeGitError{Status: 400, Code: "bad_request", Message: "scope.kind 必填"}
		}
		var maxBytes int
		_ = json.Unmarshal(p["maxDiffBytes"], &maxBytes)
		diff, err := a.exec.LocalDiff(ctx, dir, scope, maxBytes, a.worktreeFork(sessionID))
		if err != nil {
			return nil, forgeGitFailure(err)
		}
		return diff, nil
	case string(transport.MethodForgePanelLocalText):
		var scope forge.Scope
		if len(p["scope"]) == 0 || json.Unmarshal(p["scope"], &scope) != nil || scope.Kind == "" {
			return nil, &transport.ForgeGitError{Status: 400, Code: "bad_request", Message: "scope.kind 必填"}
		}
		path := str("path")
		if path == "" {
			return nil, &transport.ForgeGitError{Status: 400, Code: "bad_request", Message: "path 必填"}
		}
		text, err := a.exec.LocalText(ctx, dir, scope, path)
		if err != nil {
			return nil, forgeGitFailure(err)
		}
		return text, nil
	default:
		return nil, fmt.Errorf("不是 forgePanel git 方法: %s", method)
	}
}

// worktreeFork 与 rc2 面板共用创建事实；Executor.ForkPoint 会再核对当前 root。
func (a *forgeGitAPI) worktreeFork(sessionID string) *forge.WorktreeFork {
	if a.worktrees == nil {
		return nil
	}
	state, err := a.worktrees.State(sessionID)
	if err != nil || state.Status != worktree.StatusCreated || state.Directory == "" {
		return nil
	}
	return &forge.WorktreeFork{Directory: state.Directory, Base: state.Base, BaseCommit: state.BaseCommit}
}

func forgeGitFailure(err error) error {
	failure := &transport.ForgeGitError{Status: 422, Code: "git_failed", Message: err.Error()}
	switch {
	case errors.Is(err, forge.ErrNoForkPoint):
		failure.Code = transport.CodeForgeNoForkPoint
	case errors.Is(err, forge.ErrBaseMissing):
		failure.Code = transport.CodeForgeBaseMissing
	case errors.Is(err, forge.ErrCommitsMissing):
		failure.Code = transport.CodeForgeCommitsMissing
	case errors.Is(err, forge.ErrCommitsNotLinear):
		failure.Code = transport.CodeForgeCommitsNotLinear
	case errors.Is(err, forge.ErrDiffTooLarge):
		failure.Status, failure.Code = 413, transport.CodeForgeDiffTooLarge
	case errors.Is(err, forge.ErrNotBranchRef):
		failure.Status, failure.Code = 400, "bad_request"
	case errors.Is(err, forge.ErrNotRepository):
		failure.Status, failure.Code = 400, transport.CodeForgeNotACheckout
	}
	return failure
}

var _ transport.OfficialForgeGitAPI = (*forgeGitAPI)(nil)
