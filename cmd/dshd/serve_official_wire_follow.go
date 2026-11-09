package main

// `session.follow` snapshot.header 的**真实来源**：v4 会话文件头。
//
// 为什么在 cmd 而不是 transport：header 是 durable v4 头（version/id/createdAt/
// isSeeded/cwd/…），读它必须碰文件系统与 v4 编解码；transport 两条都不允许
// （红线 R7：host 与 UI 的唯一通道是契约，不是日志格式）。
//
// 为什么需要显式挂载：宿主侧 `officialSessionSource` 没有实现
// `transport.OfficialSessionHeaderSource`，而那一侧不在本任务的写作用域内。
// 所以本文件提供真实实现，由 `newOfficialWireServer` 用
// `transport.AttachOfficialSessionHeaderSource` 挂到 Server 上——**不编 header**：
// 读不到就返回错误（follow 如实失败），绝不拿零值冒充 1970 年的会话。
//
// 字段来源逐条对照官方 SessionHeader 声明（dsh-api-session-controller
// typert.host.js:3644）：
//
//	version/id/createdAt/isSeeded  必填 ← v4 header 同名必填字段
//	cwd/parentSession/agentPreset  可选 ← v4 header 同名可选字段
//	origin                         可选，官方是字面量 'subagent'
//	                               ← v4 的 origin 是自由 JSON，只有恰好是
//	                                 "subagent" 时才透传，否则**省略**（省略 = 不知道；
//	                                 塞一个对象进去会让客户端的 zod 直接拒掉整个 snapshot）
//	delegationDepth                可选 ← 只在真实深度 > 0 时给（0 是"顶层"，
//	                                 官方用省略表达，不要把 0 当成信息）

import (
	"encoding/json"
	"fmt"
	"strings"

	"dshgo/codecs/v4"
	"dshgo/internal/transport"
)

// officialWireSessionHeaderSource 读真实 v4 会话头。
type officialWireSessionHeaderSource struct {
	// home 是宿主 home（`<home>/sessions/...` 的上一级）。
	home string
}

var _ transport.OfficialSessionHeaderSource = officialWireSessionHeaderSource{}

// SessionHeader 返回该会话的官方 header。
//
// 会话不存在 → 返回 transport.ErrNoSuchSession（由帧机翻成 session/not-found）。
// 日志坏了 → 返回明确错误，不降级成"空 header"。
func (s officialWireSessionHeaderSource) SessionHeader(sessionID string) (transport.OfficialSessionHeader, error) {
	if strings.TrimSpace(s.home) == "" {
		return transport.OfficialSessionHeader{}, fmt.Errorf("%w: 宿主 home 为空，读不到会话头", transport.ErrNotImplemented)
	}
	// 复用宿主那条解析路径（`session-<uuid>` 与裸 uuid 都认），不另造一套 id 解析。
	source := newSessionSource(s.home, "")
	path, err := source.resolve(sessionID)
	if err != nil {
		return transport.OfficialSessionHeader{}, err
	}
	log, err := v4.ReadFile(path)
	if err != nil {
		return transport.OfficialSessionHeader{}, fmt.Errorf("读会话日志失败: %w", err)
	}
	header := log.Header
	if strings.TrimSpace(header.ID) == "" {
		// v4 的 header 校验已经保证 id 非空；到这里说明日志形状与编解码器不同步，
		// 与其回一个 id="" 的 snapshot（客户端会把它当成另一条会话），不如报错。
		return transport.OfficialSessionHeader{}, fmt.Errorf("会话 %q 的 v4 头没有 id", sessionID)
	}
	out := transport.OfficialSessionHeader{
		Version:       header.Version,
		ID:            header.ID,
		CreatedAt:     header.CreatedAt,
		CWD:           header.Cwd,
		ParentSession: header.ParentSession,
		IsSeeded:      header.IsSeeded,
		AgentPreset:   header.AgentPreset,
	}
	if header.DelegationDepth > 0 {
		depth := header.DelegationDepth
		out.DelegationDepth = &depth
	}
	if origin := officialSessionOrigin(header.Origin); origin != nil {
		out.Origin = origin
	}
	return out, nil
}

// officialSessionOrigin 把 v4 的自由 origin 折成官方的字面量 'subagent'（或省略）。
//
// 官方 zod 是 `origin: z.literal('subagent').optional()`：透传一个对象/别的字符串
// 会让客户端拒绝整帧。所以这里只认那一份**逐字相等**的值。
func officialSessionOrigin(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	if value != "subagent" {
		return nil
	}
	return json.RawMessage(`"subagent"`)
}
