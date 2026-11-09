package main

// PLAY-WORKSPACE-HOST：workspace registry 的宿主实现。
//
// # 为什么不新建账本
//
// 这 5 个动词全部读写**同一份** `<home>/state/workspaces.json`
// （`officialWorkspaceDocument`：Order / Workspaces / DefaultWorkspaceID /
// archived / pinned），也就是 `workspace.create/rename/delete/insertBefore/…`
// 已经在用的那份。**没有第二个 registry**，所以不存在"两套顺序/两套默认工作区"
// 对不上的问题。
//
// # resolve 的引用形状
//
// TUI 的 provider 会传四种东西，这里都认（见 `workspaceRefAliases`）：
//
//	workspace://<id>     官方 URI 形状
//	file:///C:/path      file URI（Windows 盘符也吃）
//	<绝对路径>            直接按路径匹配（大小写不敏感、清理过 .. 与分隔符）
//	<id> / <title>       先按 id，再按标题
//
// 匹配不到一律 `workspace/not-found`（HTTP 404），**不**回空工作区。

import (
	"net/url"
	"path/filepath"
	"runtime"
	"strings"

	"dshgo/internal/transport"
)

// registry 列出现有 registry 的完整视图。
func (h *officialWorkspaceHost) registry() (transport.OfficialWorkspaceRegistry, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.ensureLoadedLocked(); err != nil {
		return transport.OfficialWorkspaceRegistry{}, err
	}
	order := make([]string, 0, len(h.doc.Order))
	workspaces := make([]transport.OfficialWorkspace, 0, len(h.doc.Order))
	for _, id := range h.doc.Order {
		rec := h.doc.Workspaces[id]
		if rec == nil {
			continue
		}
		order = append(order, id)
		workspaces = append(workspaces, h.viewLocked(rec))
	}
	return transport.OfficialWorkspaceRegistry{
		Version:            h.doc.Version,
		DefaultWorkspaceID: h.doc.DefaultWorkspaceID,
		Order:              order,
		Workspaces:         workspaces,
		ArchivedSessionIDs: append([]string{}, h.doc.ArchivedSessionIDs...),
		PinnedSessionIDs:   append([]string{}, h.doc.PinnedSessionIDs...),
		RegistryPath:       h.path,
	}, nil
}

// resolveWorkspaceRef 把任意引用解析成 registry 条目。
func (h *officialWorkspaceHost) resolveWorkspaceRef(ref string) (transport.OfficialWorkspace, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.ensureLoadedLocked(); err != nil {
		return transport.OfficialWorkspace{}, err
	}
	rec := h.resolveLocked(ref)
	if rec == nil {
		return transport.OfficialWorkspace{}, transport.OfficialWorkspaceError(
			"workspace/not-found", "没有这个工作区：%q", ref)
	}
	return h.viewLocked(rec), nil
}

// switchWorkspace 把 defaultWorkspaceId 指到解析出来的那条，并落盘。
func (h *officialWorkspaceHost) switchWorkspace(ref string) (transport.OfficialWorkspaceSwitchValue, error) {
	var out transport.OfficialWorkspaceSwitchValue
	err := h.withDocumentWrite(func() error {
		rec := h.resolveLocked(ref)
		if rec == nil {
			return transport.OfficialWorkspaceError("workspace/not-found", "没有这个工作区：%q", ref)
		}
		out.PreviousWorkspaceID = h.doc.DefaultWorkspaceID
		h.doc.DefaultWorkspaceID = rec.WorkspaceID
		out.Workspace = h.viewLocked(rec)
		return nil
	})
	if err != nil {
		return transport.OfficialWorkspaceSwitchValue{}, err
	}
	return out, nil
}

// removeWorkspace 只摘注册，**不删任何文件**。
//
// 与 `workspace.delete` 的区别：delete 是官方语义（连会话归属一起处理）；
// remove 只回答"这条注册不要再出现在列表里"。`defaultWorkspaceId` 指针按官方
// "deleting that registration permanently disables automatic creation" 的
// 语义**保留原值**（记录已不存在，于是自动创建不会再把同一个目录拉回来）。
func (h *officialWorkspaceHost) removeWorkspace(ref string) (transport.OfficialWorkspaceRemoveValue, error) {
	var out transport.OfficialWorkspaceRemoveValue
	err := h.withDocumentWrite(func() error {
		rec := h.resolveLocked(ref)
		if rec == nil {
			return transport.OfficialWorkspaceError("workspace/not-found", "没有这个工作区：%q", ref)
		}
		id := rec.WorkspaceID
		delete(h.doc.Workspaces, id)
		kept := h.doc.Order[:0]
		for _, existing := range h.doc.Order {
			if existing != id {
				kept = append(kept, existing)
			}
		}
		h.doc.Order = kept
		out = transport.OfficialWorkspaceRemoveValue{
			Removed:            true,
			WorkspaceID:        id,
			DefaultWorkspaceID: h.doc.DefaultWorkspaceID,
		}
		return nil
	})
	if err != nil {
		return transport.OfficialWorkspaceRemoveValue{}, err
	}
	return out, nil
}

// workspaceCommands 列出本宿主**真实**支持的 workspace 命令。
//
// 不可用的一定给 reason：客户端据此把菜单项灰掉并显示原因，而不是点了没反应。
func (h *officialWorkspaceHost) workspaceCommands() (transport.OfficialWorkspaceCommandList, error) {
	h.mu.Lock()
	count := 0
	if err := h.ensureLoadedLocked(); err == nil {
		for _, id := range h.doc.Order {
			if h.doc.Workspaces[id] != nil {
				count++
			}
		}
	}
	h.mu.Unlock()

	cmds := []transport.OfficialWorkspaceCommand{
		{ID: "registry", Title: "列出工作区", Available: true},
		{ID: "resolve", Title: "解析工作区引用", Available: true},
		{ID: "create", Title: "新建工作区", Available: true},
	}
	if count > 0 {
		cmds = append(cmds,
			transport.OfficialWorkspaceCommand{ID: "switch", Title: "切换工作区", Available: true},
			transport.OfficialWorkspaceCommand{ID: "remove", Title: "从列表移除", Available: true},
		)
	} else {
		cmds = append(cmds,
			transport.OfficialWorkspaceCommand{ID: "switch", Title: "切换工作区", Available: false,
				Reason: "registry 里还没有工作区"},
			transport.OfficialWorkspaceCommand{ID: "remove", Title: "从列表移除", Available: false,
				Reason: "registry 里还没有工作区"},
		)
	}
	// 官方桌面端能在文件管理器/编辑器里打开工作区；本宿主没有这层集成，
	// 明确标不可用而不是画一个点了没反应的菜单项。
	cmds = append(cmds, transport.OfficialWorkspaceCommand{
		ID: "open-in-editor", Title: "在编辑器打开", Available: false,
		Reason: "宿主没有编辑器/文件管理器集成（桌面壳能力，非本层职责）",
	})
	return transport.OfficialWorkspaceCommandList{Commands: cmds}, nil
}

// resolveLocked 是引用解析的唯一实现（调用方必须已持锁且已 ensureLoadedLocked）。
func (h *officialWorkspaceHost) resolveLocked(ref string) *officialWorkspaceRecord {
	raw := strings.TrimSpace(ref)
	if raw == "" {
		return nil
	}
	// 1) 官方 URI：workspace://<id>
	if rest, ok := cutPrefixFold(raw, "workspace://"); ok {
		id := strings.Trim(strings.TrimSpace(rest), "/")
		if rec := h.doc.Workspaces[id]; rec != nil {
			return rec
		}
		return nil
	}
	// 2) file URI → 本地路径
	candidate := raw
	if strings.HasPrefix(strings.ToLower(raw), "file://") {
		if u, err := url.Parse(raw); err == nil {
			p := u.Path
			if u.Host != "" && !strings.HasPrefix(p, "/") {
				p = "/" + p
			}
			// /C:/x 与 /home/x 都去掉一个前导 '/'：Windows 上是盘符，
			// 其它平台是根目录的相对写法（后续 normalize 会再清理）。
			candidate = strings.TrimPrefix(p, "/")
			if decoded, err := url.PathUnescape(candidate); err == nil {
				candidate = decoded
			}
		}
	}
	// 3) id
	if rec := h.doc.Workspaces[strings.TrimSpace(raw)]; rec != nil {
		return rec
	}
	// 4) 路径（清理后大小写不敏感；Windows 盘符/分隔符差异都吃掉）
	wantPath := normalizeWorkspacePath(candidate)
	if wantPath != "" {
		for _, id := range h.doc.Order {
			rec := h.doc.Workspaces[id]
			if rec == nil {
				continue
			}
			if normalizeWorkspacePath(rec.Path) == wantPath {
				return rec
			}
		}
	}
	// 5) 标题（精确匹配，不做模糊——模糊会把"改错名字"变成"改到别的目录"）
	for _, id := range h.doc.Order {
		rec := h.doc.Workspaces[id]
		if rec != nil && rec.Title == raw {
			return rec
		}
	}
	return nil
}

// normalizeWorkspacePath 把所有等价写法归一成一个比较键。
func normalizeWorkspacePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = filepath.Clean(filepath.FromSlash(strings.ReplaceAll(p, `\`, "/")))
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return p
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) {
		return "", false
	}
	if strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):], true
	}
	return "", false
}
