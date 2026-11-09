package main

import (
	"errors"
	"fmt"

	"dshgo/internal/transport"
	"dshgo/modules/tools"
)

// mcpManager 是**宿主侧**对 MCP 桥的薄适配层，本身不含协议逻辑。
//
// 为什么协议不在这一层：`modules/tools` 才是工具面的 owner——工具注册、命名
// 空间、caps、resources 工具都在那里。宿主这一层只负责一件事：把中立视图
// 翻译成 transport 的形状，并把"没有这个 server"翻成 transport 的哨兵错误，
// 好让 HTTP 层回 404 而不是 500。
//
// 早先这一层自己实现了整个 stdio 客户端，却**只数工具不注册**——于是
// `dshd tools` 里看不到任何 MCP 工具，模型也调不到。现在协议的唯实现搬到了
// modules/tools/mcp*.go，这里只剩转译。
type mcpManager struct{ *tools.MCPManager }

func openMCPManager(home string) *mcpManager {
	return &mcpManager{MCPManager: tools.OpenMCPManager(home)}
}

// List 对应 `mcp.list`。
func (m *mcpManager) List() ([]transport.MCPServerView, error) {
	rows, err := m.MCPManager.Statuses()
	if err != nil {
		return nil, err
	}
	out := make([]transport.MCPServerView, 0, len(rows))
	for _, row := range rows {
		view := transport.MCPServerView{Name: row.Name, Status: row.Status}
		if row.ToolCount != nil {
			count := *row.ToolCount
			view.ToolCount = &count
		}
		out = append(out, view)
	}
	return out, nil
}

// Reconnect 对应 `mcp.reconnect`。
func (m *mcpManager) Reconnect(name string) error {
	return translateMCPNotFound(m.MCPManager.Reconnect(name))
}

// Toggle 对应 `mcp.toggle`。
func (m *mcpManager) Toggle(name string, enabled bool) error {
	return translateMCPNotFound(m.MCPManager.Toggle(name, enabled))
}

// translateMCPNotFound 把中立的 not-found 翻成 transport 的哨兵。
//
// 两个包都不该 import 对方：modules/tools 不能依赖 internal/transport（分层红线），
// 而 transport 也不该认识 MCP 的实现细节。翻译点放在宿主这一层，各拿各的哨兵。
func translateMCPNotFound(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, tools.ErrMCPServerNotFound) {
		return fmt.Errorf("%w: %w", transport.ErrNoSuchMCPServer, err)
	}
	return err
}
