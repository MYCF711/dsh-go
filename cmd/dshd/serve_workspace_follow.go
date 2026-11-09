package main

// workspace.follow 的宿主数据源：**同一份**真实 registry（`<home>/state/workspaces.json`），
// 不新建账本、不缓存副本。
//
// 接线（WIRING）：
//
//	wsHost := attachOfficialWorkspace(srv, *home, src, prompts)
//	attachOfficialWorkspaceFollow(srv, wsHost)
//
// 快照按 registry 的 `Order` 排好（顺序真相只有一份），Archived/Pinned 用
// registry 的全局集合；transport 侧负责把它们编成官方 WorkspaceFollowFrame。

import (
	"dshgo/internal/transport"
)

// workspaceFollowSource 把 officialWorkspaceHost 适配成 transport 的快照源。
type workspaceFollowSource struct {
	host *officialWorkspaceHost
}

// WorkspaceFollowSnapshot 读一次真实 registry。
func (s workspaceFollowSource) WorkspaceFollowSnapshot() (transport.OfficialWorkspaceFollowSnapshot, error) {
	host := s.host
	if host == nil {
		return transport.OfficialWorkspaceFollowSnapshot{}, transport.OfficialWorkspaceError(
			"workspace/internal", "宿主没有工作区 registry（workspace.follow 需要它）")
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if err := host.ensureLoadedLocked(); err != nil {
		return transport.OfficialWorkspaceFollowSnapshot{}, transport.OfficialWorkspaceError(
			"workspace/internal", "读工作区注册表失败：%v", err)
	}
	items := make([]transport.OfficialWorkspace, 0, len(host.doc.Order))
	for _, id := range host.doc.Order {
		rec := host.doc.Workspaces[id]
		if rec == nil {
			continue
		}
		items = append(items, host.viewLocked(rec))
	}
	return transport.OfficialWorkspaceFollowSnapshot{
		Items:              items,
		ArchivedSessionIDs: append([]string{}, host.doc.ArchivedSessionIDs...),
		PinnedSessionIDs:   append([]string{}, host.doc.PinnedSessionIDs...),
	}, nil
}

// attachOfficialWorkspaceFollow 把 registry 快照源挂到 Server 上。
//
// host 为 nil 时不挂：workspace.follow 会如实 501（"宿主没有工作区快照源"），
// 而不是返回空 baseline 假装"没有任何工作区"。
func attachOfficialWorkspaceFollow(srv *transport.Server, host *officialWorkspaceHost) {
	if srv == nil {
		return
	}
	if host == nil {
		srv.DetachOfficialWorkspaceFollowSource()
		return
	}
	srv.AttachOfficialWorkspaceFollowSource(workspaceFollowSource{host: host})
}
