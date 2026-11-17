package main

// 官方 `session.create` 的宿主实现（基线 0.2.2-rc.2）。
//
// # 官方契约（rc.2 解包树，逐条对照）
//
//	@deepseek-ai/dsh-api-session-controller/src/types.ts:268-284
//	  SessionCreateRequest { workspaceId?; cwd?; sessionId?; agentPreset? }
//	  SessionCreateValue   { sessionId; agentPreset? }
//
//	.../src/commands.ts:121-123
//	  workspaceId 与 cwd 同时给 = gateway/bad-request（transport 层已经按这句拒掉）
//	.../src/commands.ts:125-133
//	  workspaceId → registry 查无此物 = workspace/not-found { workspaceId }
//	  cwd = workspace?.path ?? request.cwd ?? defaultCwd
//	.../src/commands.ts:136-142
//	  reusable = !archivedSessionIds.includes(id)
//	            && (workspace === undefined || workspace.sessionIds.includes(id))
//	.../src/commands.ts:146-157
//	  attach 失败 = session/workspace-attach-failed { sessionId, workspaceId }
//	.../src/client/sessions/manager.ts:855-857（workspaceAttachSessionId）
//	  error.code === 'session/workspace-attach-failed' → 取 error.details.sessionId，
//	  客户端把它当 Ungrouped 会话暴露（"已发布但没挂上"的会话是真实存在的）
//	.../src/agent.ts:229-233 / :331-364
//	  sessionId 指向的会话：(a) 没存过 → 用这个名字建新会话
//	                        (b) 存过且"没开过任何 turn" → 复用它
//	                        (c) 存过但不能复用 → 另铸新 id（结果 id 与请求不同）
//	                        (d) 存过但 header.cwd 与请求不符 → session/conflict
//	                            { sessionId, requestedCwd, existingCwd }
//	.../src/agent.ts:261-274
//	  agentPreset 没点名 → 用 configured default；点名 → 先 resolve，
//	  未知 = agent-preset/not-found { agentPreset, available }
//	  （`@deepseek-ai/dsh-agent-preset-registry/src/index.ts:220-226`）
//
// # 分工
//
// 契约/参数解码/四字段错误信封在 transport（`internal/transport/server.go` 的
// handleSessionCreate 与 writeOfficialError）；本文件只提供 transport 拿不到的
// 真实数据（红线 R7：transport 不读盘、不认识 registry、不认识 v4 会话）。
//
// # 一处**有意的**能力差异（不静默）
//
// 官方复用一个已存在的空白会话时，会把会话的 preset **改**成请求（或默认）的
// preset，做法是往日志追加 `agent-preset/selected`（`agent.ts:403-412`），因为
// 官方的 preset 活在投影里。本仓的 preset 活在 **v4 header** 里，且 header 是
// append-only（R5），runner 读的也是 header（`cmd/dshd/main.go` 的
// `mode := l.Header.AgentPreset`）。所以：
//
//   - 请求**显式点名**了不同的 preset → 回官方 `agent-preset/conflict`
//     { sessionId, requestedPreset }（`commands.ts:550-554`）。
//     宁可响亮失败，也不回一个"成功"然后按旧 preset 跑。
//   - 请求**没点名** → 保持会话 header 里的 preset（那本来就是同一份
//     `selectedDefault ?? default` 解析在创建时写下的），结果里如实回它。

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"dshgo/codecs/v4"
	"dshgo/internal/transport"
)

// officialSessionCreator 是 transport.Options.CreateOfficial 的真实实现。
type officialSessionCreator struct {
	// home 是 `<home>`：会话根目录（真 v4 存储在 <home>/sessions 下）。
	home string
	// src 是生产会话数据源：按 id 找已存会话复用它（sessionSource.resolve，
	// 与 session.load/history 同一个路径解析），不另开第二条读盘入口。
	src *sessionSource
	// workspace 取 registry 宿主。serve 装配顺序是 NewServer → attachOfficialWorkspace，
	// 所以这里存**取值函数**而不是装配时的指针（那个时刻它还是 nil）。
	workspace func() *officialWorkspaceHost
	// settings 是新会话"创建当时有效默认值"的物化源，与老 session.create 路径
	// 是同一份 serveSettingsStore（不是第二份设置）。nil = 未接设置面，没有默认
	// 值可物化（生产 serve 永远非 nil）。
	settings *serveSettingsStore
}

// create 实现官方 `SessionCommandController.create`（commands.ts:120-160）。
func (c *officialSessionCreator) create(req transport.OfficialSessionCreateRequest) (transport.OfficialSessionCreateResult, error) {
	var zero transport.OfficialSessionCreateResult
	if c == nil || strings.TrimSpace(c.home) == "" {
		return zero, transport.OfficialWorkspaceError("workspace/unsupported", "建会话需要会话根目录（--home 是空的）")
	}
	workspaceID := derefOfficialString(req.WorkspaceID)
	// hasWorkspace 用"给了没"判（官方 !== undefined），不是用真值：
	// 空串已经在 cwdFor 里按 workspace/not-found 拒掉，之后 workspaceID 非空
	// 当且仅当请求真的点名了一个工作区。
	hasWorkspace := req.WorkspaceID != nil
	cwd, err := c.cwdFor(req)
	if err != nil {
		return zero, err
	}
	resolvedPreset, explicitPreset, err := c.presetFor(req)
	if err != nil {
		return zero, err
	}
	sessionID, reused, recordedPreset, err := c.claimSession(req.SessionID, workspaceID, cwd, explicitPreset)
	if err != nil {
		return zero, err
	}
	if reused {
		// 官方 attach 对"复用"与"新建"是同一条路径（commands.ts:146-157 的 try
		// 覆盖两条路），所以复用也要走一次 attach，不能只在新建时挂工作区。
		if hasWorkspace {
			if err := c.attach(workspaceID, sessionID); err != nil {
				return zero, err
			}
		}
		return transport.OfficialSessionCreateResult{SessionID: sessionID, AgentPreset: recordedPreset}, nil
	}
	// 新会话：先落盘，再物化"创建当时的默认值"，最后 attach。
	// 顺序照官方：publish 之后的 attach 失败，会话仍然存在，错误体带着它的 id。
	if err := c.publish(sessionID, cwd, resolvedPreset); err != nil {
		return zero, err
	}
	if hasWorkspace {
		if err := c.attach(workspaceID, sessionID); err != nil {
			return zero, err
		}
	}
	return transport.OfficialSessionCreateResult{SessionID: sessionID, AgentPreset: resolvedPreset}, nil
}

// cwdFor 解析会话目录：`workspace?.path ?? request.cwd ?? defaultCwd`（commands.ts:133）。
//
// workspaceId 与 cwd 同时给已经在 transport 层按官方拒掉（commands.ts:121-123），
// 这里只看"给了一个"或"都没给"。**workspaceId 优先**这条官方语义在客户端侧是
// 组合规则（manager.ts:502-504：workspaceId !== undefined 时连 cwd 都不发），
// 在宿主侧表现为"两个都发就是 bad-request"，两边都不替客户端挑一个。
func (c *officialSessionCreator) cwdFor(req transport.OfficialSessionCreateRequest) (string, error) {
	// 判据是 `workspaceId !== undefined`（官方 commands.ts:125-132），不是真值：
	// 给了空串也**必须**走 registry 查无此物，不能被当成省略后在进程 cwd 建会话。
	if req.WorkspaceID != nil {
		host, err := c.workspaceHost()
		if err != nil {
			return "", err
		}
		path, err := host.workspacePath(*req.WorkspaceID)
		if err != nil {
			return "", err
		}
		return v4.NormalizeCwd(path), nil
	}
	if req.Cwd != nil && strings.TrimSpace(*req.Cwd) != "" {
		return v4.NormalizeCwd(*req.Cwd), nil
	}
	// 官方 defaultCwd 是宿主启动时定下的项目目录；本仓老行为就是进程 cwd
	// （serveCreate 对空 cwd 也取 os.Getwd），这里保持同一份判据。
	got, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("取当前目录失败: %w", err)
	}
	return v4.NormalizeCwd(got), nil
}

// presetFor 解析 preset，返回 (实际要用的 preset, 请求里**显式**点名的 preset)。
//
// 显式点名（agentPreset 或旧名 mode）与默认值分开返回，是因为"复用一个已存的
// 空白会话"时两者处理不同：显式点名的不一致要响亮拒绝，默认值不参与拒绝
// （见文件头的取舍说明）。
func (c *officialSessionCreator) presetFor(req transport.OfficialSessionCreateRequest) (string, string, error) {
	explicit := strings.TrimSpace(derefOfficialString(req.AgentPreset))
	fromOfficialField := req.AgentPreset != nil
	if explicit == "" {
		// 旧 `mode` 就是 v4 header.agentPreset 的旧名字：官方字段优先，其次旧字段。
		explicit = strings.TrimSpace(req.Mode)
	}
	if explicit == "" {
		// 官方 defaultId = selectedDefault ?? default（agent-preset-registry
		// src/index.ts:82）；本仓唯一的解析点在 selectedAgentPresetDefault，
		// 它对失效值也会回落到内置默认。
		return selectedAgentPresetDefault(c.home), "", nil
	}
	// 官方字段必须校验存在性：不存在的 preset 写进 header，界面会显示一个点不出来的
	// 名字，resume 也恢复不到东西。旧 `mode` 保持老行为（不校验），不删既有能力。
	if fromOfficialField && !agentPresetKnown(c.home, explicit) {
		return "", "", transport.OfficialWorkspaceErrorDetails("agent-preset/not-found",
			map[string]any{"agentPreset": explicit, "available": agentPresetAvailableIDs(c.home)},
			"Unknown agent preset: %s", explicit)
	}
	return explicit, explicit, nil
}

// claimSession 决定这次 create 用哪个会话身份。返回 (会话 id, 是否复用, 复用时 header 里的 preset)。
//
// 四种情形逐条来自官方 agent.ts:229-233 / :331-364：
//   - 没点名 sessionId                  → 铸新 id
//   - 点名了、这个名字下没存过会话      → 用这个名字建新会话
//   - 点名了、存过、cwd 不符            → session/conflict
//   - 点名了、存过、不能复用或有 turn   → 另铸新 id（结果 id 与请求不同）
//   - 点名了、存过、空白且可复用        → 复用它
func (c *officialSessionCreator) claimSession(sessionID *string, workspaceID, cwd, explicitPreset string) (string, bool, string, error) {
	requested := strings.TrimSpace(derefOfficialString(sessionID))
	if sessionID != nil && requested == "" {
		// 官方只有 `undefined` 才是"没给"（commands.ts:140-141 的 reusable 谓词、
		// agent.ts:230 的 `named === undefined`）。空串不是合法 SessionId，
		// 当"没给"处理会静默建一个新会话，客户端会以为它复用了 ""。
		return "", false, "", transport.OfficialWorkspaceError("gateway/bad-request",
			"session.create 的 sessionId 不能是空串")
	}
	if requested == "" {
		id, err := serveID()
		if err != nil {
			return "", false, "", err
		}
		return id, false, "", nil
	}
	path, err := c.source().resolve(requested)
	switch {
	case errors.Is(err, transport.ErrNoSuchSession):
		// (a) 没有存过这个名字：就用这个名字建（官方 "the identity of a new Session"）。
		return requested, false, "", nil
	case err != nil:
		return "", false, "", fmt.Errorf("按 id 找会话 %q 失败: %w", requested, err)
	}
	log, err := v4.ReadFile(path)
	if err != nil {
		return "", false, "", fmt.Errorf("读会话 %q 失败: %w", requested, err)
	}
	// (d) 目录不符先判：官方在 claim 里先比 header.cwd，再谈复用
	//（agent.ts:338-341 live 分支、:358-360 已存分支）。
	if log.Header.Cwd != cwd {
		return "", false, "", transport.OfficialWorkspaceErrorDetails("session/conflict",
			map[string]any{"sessionId": requested, "requestedCwd": cwd, "existingCwd": log.Header.Cwd},
			"session %q belongs to %q, not the requested %q", requested, log.Header.Cwd, cwd)
	}
	host, err := c.workspaceHost()
	if err != nil {
		return "", false, "", err
	}
	reusable, err := host.sessionReusable(workspaceID, requested)
	if err != nil {
		return "", false, "", err
	}
	blank := true
	for _, ev := range log.Events {
		// 官方空白判据是"没有任何 turn/start"（agent.ts:361
		// `!observed.events.some(event => event.type === 'turn/start')`），
		// 不是"没有消息"：只有标题/元数据的会话仍然是空白的。
		if ev.Type == "turn/start" {
			blank = false
			break
		}
	}
	if !reusable || !blank {
		// (c) 不能复用 → 另铸新 id。官方两处都写明"返回的 id 可能与请求的不同"：
		// types.ts:281 的 SessionCreateValue 注释与 manager.ts:512-513 的
		// publishedSessionId 注释（后者讲的是"已发布未挂上"那份会话）。
		id, err := serveID()
		if err != nil {
			return "", false, "", err
		}
		return id, false, "", nil
	}
	// 复用：显式点名了与 header 不同的 preset 时拒绝（理由见文件头）。
	if explicitPreset != "" && explicitPreset != log.Header.AgentPreset {
		return "", false, "", transport.OfficialWorkspaceErrorDetails("agent-preset/conflict",
			map[string]any{"sessionId": requested, "requestedPreset": explicitPreset},
			"session %q runs preset %q, not the requested %q (the v4 header is append-only)",
			requested, log.Header.AgentPreset, explicitPreset)
	}
	return requested, true, log.Header.AgentPreset, nil
}

// publish 落一份新会话并把"创建当时的有效默认"物化到它的 override 键上。
//
// 默认值只给新会话写（老 session.create 路径的语义不变）；复用不 retroactive 改。
func (c *officialSessionCreator) publish(sessionID, cwd, preset string) error {
	if err := serveCreateWithID(c.home, cwd, sessionID, preset); err != nil {
		return err
	}
	if c.settings == nil {
		return nil
	}
	return applySessionSettingDefaults(c.settings, sessionID)
}

// source 给出会话数据源；nil 时按同一个 home 建一份（路径解析只有一份实现）。
func (c *officialSessionCreator) source() *sessionSource {
	if c.src != nil {
		return c.src
	}
	c.src = newSessionSource(c.home, "")
	return c.src
}

// workspaceHost 取 registry 宿主。它由 serve 在装配后挂上；没挂就是真实的
// 未接线，回响亮 501 而不是把 workspaceId 当装饰参数（也不 panic：一个请求
// 参数不该让整个进程倒掉）。
func (c *officialSessionCreator) workspaceHost() (*officialWorkspaceHost, error) {
	if c.workspace != nil {
		if host := c.workspace(); host != nil {
			return host, nil
		}
	}
	return nil, transport.OfficialWorkspaceError("workspace/unsupported", "workspace registry 未接线")
}

// attach 把会话挂进工作区（官方 attachSession 语义；失败形状由 attachSession 给）。
func (c *officialSessionCreator) attach(workspaceID, sessionID string) error {
	host, err := c.workspaceHost()
	if err != nil {
		return err
	}
	return host.attachSession(workspaceID, sessionID)
}

// derefOfficialString 读可选字符串参数（nil = 官方语义里的 undefined）。
func derefOfficialString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
