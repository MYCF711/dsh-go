package main

// agentPresets.read 的官方 entry-list YAML 投影。
//
// 旧实现（serve_agent_api.go 的 agentPresetRead）对用户自建模式原样回磁盘
// TOML，内置模式回 `id: ...` 的行内声明；这两者都不是官方 `content` 的
// entry-list YAML。这里新增一条投影路径，由 WIRING 把
// `OfficialAgentAPI.AgentPresetRead` 指到它；旧方法保留给既有直接调用测试，
// 不再作为生产 wire 的正文来源。
//
// 接线（见 tmp/GO2-TEAM/PARITY-W1-PRESETS-ACTIONS/WIRING.md）：
//
//	cmd/dshd/serve_agent_api.go 的 newAgentOfficialAPI 里：
//	    AgentPresetRead: a.agentPresetReadYAML,
//
// 只改这一行，不动共享 handler。

import (
	"fmt"

	"dshgo/internal/agentpresets"
	"dshgo/internal/modes"
	"dshgo/internal/transport"
)

// agentPresetReadYAML 返回真实 preset 的官方 entry-list YAML 正文。
//
// 值来源与 agentPresetRoster 完全同源：内置 modes.Builtins() + 用户
// `<home>/modes/*.toml` 的解析结果；不另读一份 TOML，也不把未知 preset
// 编成一个空列表。
func (a *agentOfficialAPI) agentPresetReadYAML(presetID string) (transport.OfficialAgentPresetDocument, error) {
	roster, err := a.agentPresetRoster()
	if err != nil {
		return transport.OfficialAgentPresetDocument{}, err
	}
	var found *modes.Mode
	for i := range roster {
		if roster[i].ID == presetID {
			found = &roster[i]
			break
		}
	}
	if found == nil {
		return transport.OfficialAgentPresetDocument{}, fmt.Errorf(
			"%w: 没有 id=%q 的 preset（内置：%s）",
			transport.ErrOfficialBadInput, presetID, modes.IDs())
	}
	content, err := agentpresets.Document(*found)
	if err != nil {
		return transport.OfficialAgentPresetDocument{}, err
	}
	return transport.OfficialAgentPresetDocument{
		AgentPreset: found.ID,
		Name:        found.Name,
		Description: found.Description,
		Content:     content,
	}, nil
}
