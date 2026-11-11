package transport

// 本文件是官方 0.2.2-rc.2 **AgentTeam 远程方法**与**按目标会话授权的子代理中断**
// 在 Transport 层的 L1 实现（`agentTeams/setEnabled`、`subagents/interruptByUser`）。
//
// # 为什么单独一个分派面
//
// hostProtocolVersion 仍是 4，但 rc.2 把方法总数从 193 推到 223，Team 从"实验包"
// 转正：`dsh-agent-team` 只剩**任务板**，远程面只有 `agentTeams/setEnabled` 一个方法；
// 子代理中断从"父授权"改成"网关按**目标会话**授权"（`interruptByUser`，单参数）。
// 这两条与既有 25 个 agent 方法不是同一批语义（一个是 Team 模式选择、一个是
// Gateway 授权），所以按既有"每个官方方法族一个门 + 一个分派函数"的模式新开一面，
// 由 `server.go` 的 `/api` 路由在校验后调用一次（接线行见报告 B2）。
//
// # 契约要点（逐条来自 rc.2 随包源码/文档）
//
//	agentTeams/setEnabled
//	 参数：agent（source=lookup，wire=agentId；**必须正好是树根**）、enabled（json boolean）
//	 结果：void。官方是 `z.void()`；本仓 wire 不接受 `{"result":null}`（会被当 500），
//	       所以用**空结果对象** `{}` 表达"这次调用没有载荷"——与 `commands.execute`
//	       已在用的出口约定一致（该差异登记在报告里）。
//	 语义：选择在 await 持久化**之前**生效；重复设置同值不重复 append
//	       （`if (this.isEnabled(root) !== enabled)`）；非根 → TEAM_LEAD_REQUIRED。
//	subagents/interruptByUser
//	 参数：childSessionId（json string，**唯一参数**）——rc.1 的 parentSessionId/mode
//	       是未知字段，必须被 strict 参数校验拒掉。
//	 结果：{accepted: true}（官方 `accepted` 是 `const true`）。
//	 语义：授权在**目标会话**上由网关完成；「不存在 / 空闲 / 已完成的目标是
//	       accepted no-op」——这是官方的明确语义，不是我们编的。
//
// # 与官方 wire 的显式差异
//
//   - 本仓仍是 `POST /api/<method>` + 裸参数对象（官方是 typert Connection envelope），
//     L2 `remote.mux` 未实现，所以**不**声称 wire 兼容。
//   - void 结果用 `{}` 而不是 `null`（同上，出口不接受 null）。
//   - `subagents.interruptByParent`（rc.1 三参数、父授权）**保留为兼容别名**：
//     能力下限规则要求官方旧形状继续可用；它在既有 agent 分派面里原样保留，
//     本分派面不接管它（见 handlers_subagents_official.go 的 Deprecated 注释）。
//
// # 诚实边界
//
// 宿主没接线（没有 `AttachOfficialAgentTeamRC2API`）时回 **501 not_implemented**
// 并点名缺什么；宿主把 TEAM_LEAD_REQUIRED 顶上来时回 403 `team_lead_required`；
// 持久化失败回 500。任何一条都**不会**被渲染成成功。

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// 官方方法名。字符串必须与官方 typert 描述符逐字对应（`agentTeams/setEnabled`
// → 本仓 `agentTeams.setEnabled`，与既有方法的 `/`→`.` 替换同形）。
const (
	// MethodAgentTeamsSetEnabled 是官方 `agentTeams/setEnabled`
	// （`dsh-agent-team/src/index.ts:111-118`、`lib/typert.host.js:20-70`）。
	MethodAgentTeamsSetEnabled Method = "agentTeams.setEnabled"
	// MethodSubagentsInterruptByUser 是官方 `subagents/interruptByUser`
	// （`dsh-subagent` 的 `interruptByUser`：单参数 childSessionId，结果 {accepted:true}）。
	MethodSubagentsInterruptByUser Method = "subagents.interruptByUser"
)

// OfficialAgentTeamRC2Methods 按固定顺序返回 rc.2 Team 分派面的方法。
//
// 它给快照/文档/门禁用：增删方法必须显式改这里，不能让"方法悄悄多了一个"
// 从路由表漏过去。
func OfficialAgentTeamRC2Methods() []Method {
	return []Method{MethodAgentTeamsSetEnabled, MethodSubagentsInterruptByUser}
}

// IsOfficialAgentTeamRC2Method 判断一个 /api 路径名是否属于 rc.2 Team 分派面。
// 路由侧只需要这一次判断（不要在 server.go 摊开 case）。
func IsOfficialAgentTeamRC2Method(name string) bool {
	switch Method(name) {
	case MethodAgentTeamsSetEnabled, MethodSubagentsInterruptByUser:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// 挂载点
// ---------------------------------------------------------------------------

// OfficialAgentTeamSetEnabledRequest 是 `agentTeams.setEnabled` 的宿主入参。
//
// 官方两个参数是位置参数（agent、enabled）；本仓 wire 是裸对象，按**参数名**取。
type OfficialAgentTeamSetEnabledRequest struct {
	// Agent 是官方参数 `agent`（wire 名 agentId）：**正好是树根**的那个活会话。
	Agent string
	// Enabled 是官方参数 `enabled`：用户选择的 Team 模式。
	Enabled bool
}

// OfficialAgentTeamRC2API 是宿主侧的真实实现集合。**每个字段都可以为 nil**：
// 为 nil 表示这个能力在当前宿主上没有真实支撑，调用回 not_implemented 并说清缺什么。
type OfficialAgentTeamRC2API struct {
	// SetEnabled 按会话持久化 Team 选择。非根调用返回 ErrOfficialTeamLeadRequired；
	// 持久化失败返回包裹 ErrStatePersistence 的错误。返回 nil 表示"选择已落盘"。
	SetEnabled func(req OfficialAgentTeamSetEnabledRequest) error
	// InterruptByUser 在**目标会话**上接纳一次取消信号（官方 Gateway 授权口径）。
	// 目标不存在/空闲/已完成 → accepted no-op（官方语义），不是错误。
	InterruptByUser func(childSessionID string) (OfficialAccepted, error)
}

// officialAgentTeamRC2Mount 把一份实现挂到一个 Server 上。
//
// 与 `officialAgentMount` 同一模式（按 *Server 索引，不用 Options——Options 在
// server.go，本任务的写作用域之外）：两个 Server 各挂各的，互不可见。
var officialAgentTeamRC2Mount = struct {
	mu sync.RWMutex
	m  map[*Server]OfficialAgentTeamRC2API
}{m: map[*Server]OfficialAgentTeamRC2API{}}

// AttachOfficialAgentTeamRC2API 给这个 Server 挂上 rc.2 Team 面的真实实现。
// 必须发生在服务开始接受 /api 调用之前（serve 的装配阶段）。
func (s *Server) AttachOfficialAgentTeamRC2API(api OfficialAgentTeamRC2API) {
	if s == nil {
		return
	}
	officialAgentTeamRC2Mount.mu.Lock()
	defer officialAgentTeamRC2Mount.mu.Unlock()
	officialAgentTeamRC2Mount.m[s] = api
}

// DetachOfficialAgentTeamRC2API 摘掉实现（测试收尾用；生产不需要调）。
func (s *Server) DetachOfficialAgentTeamRC2API() {
	if s == nil {
		return
	}
	officialAgentTeamRC2Mount.mu.Lock()
	defer officialAgentTeamRC2Mount.mu.Unlock()
	delete(officialAgentTeamRC2Mount.m, s)
}

func (s *Server) officialAgentTeamRC2API() (OfficialAgentTeamRC2API, bool) {
	if s == nil {
		return OfficialAgentTeamRC2API{}, false
	}
	officialAgentTeamRC2Mount.mu.RLock()
	defer officialAgentTeamRC2Mount.mu.RUnlock()
	api, ok := officialAgentTeamRC2Mount.m[s]
	return api, ok
}

// ---------------------------------------------------------------------------
// 错误契约
// ---------------------------------------------------------------------------

// ErrOfficialTeamLeadRequired 表示"Team 选择只能由树根会话修改"（官方
// `TEAM_LEAD_REQUIRED`，`dsh-agent-team/src/index.ts:114`）。回 403。
var ErrOfficialTeamLeadRequired = errors.New(
	"TEAM_LEAD_REQUIRED: change Team mode from the root Session（Team 模式只能从根会话修改）")

// ---------------------------------------------------------------------------
// 分发
// ---------------------------------------------------------------------------

// ServeOfficialAgentTeamRC2API 处理 rc.2 Team 分派面的方法。
//
// 调用方（server.go 的 /api 路由）只需在校验后判断
// `IsOfficialAgentTeamRC2Method(name)` 并调用它；认证、`POST` 校验、失败日志
// 都已经在 handleAPI 里做过了。
func (s *Server) ServeOfficialAgentTeamRC2API(w http.ResponseWriter, r *http.Request, name string) {
	api, ok := s.officialAgentTeamRC2API()
	if !ok {
		writeJSON(w, http.StatusNotImplemented, errorBodyFor("not_implemented",
			fmt.Sprintf("%s 未实现：宿主没有 AttachOfficialAgentTeamRC2API（rc.2 Team 分派面未接线）", name)))
		return
	}
	params, err := decodeOfficialParams(r)
	if err != nil {
		writeOfficialError(w, err)
		return
	}
	result, err := routeOfficialAgentTeamRC2(api, Method(name), params)
	if err != nil {
		// 树根限制是**稳定的业务码**，不能塌成 internal：界面据此提示
		// "去根会话改"，而不是显示"服务器错误"。
		if errors.Is(err, ErrOfficialTeamLeadRequired) {
			writeJSON(w, http.StatusForbidden, errorBodyFor("team_lead_required", err.Error()))
			return
		}
		writeOfficialError(w, err)
		return
	}
	if result == nil {
		// 官方 void 结果（本仓出口不接受 result:null）。
		result = map[string]any{}
	}
	writeJSON(w, http.StatusOK, result)
}

// routeOfficialAgentTeamRC2 把方法名映射到 handler。
func routeOfficialAgentTeamRC2(api OfficialAgentTeamRC2API, method Method, p *officialParams) (any, error) {
	switch method {
	case MethodAgentTeamsSetEnabled:
		return officialAgentTeamsSetEnabled(api, p)
	case MethodSubagentsInterruptByUser:
		return officialSubagentsInterruptByUser(api, p)
	}
	return nil, officialBadRequest("未知的 rc.2 Team 方法 %q", string(method))
}

// officialAgentTeamsSetEnabled 解析并转发 `agentTeams.setEnabled`。
//
// 官方参数顺序 [agent, enabled]；wire 名分别是 `agentId` 与 `enabled`。
func officialAgentTeamsSetEnabled(api OfficialAgentTeamRC2API, p *officialParams) (any, error) {
	// 官方 wire 名在前，本仓既有 58 方法惯用的 sessionId 也认（口径同 sessionRef）。
	agentID, _, err := p.strAlias("agentId", "agent")
	if err != nil {
		return nil, err
	}
	enabled, has, err := p.boolean("enabled")
	if err != nil {
		return nil, err
	}
	if !has {
		return nil, officialBadRequest("缺 enabled（官方参数是 boolean：true=开 Team，false=关）")
	}
	if err := p.finish(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(agentID) == "" {
		return nil, officialBadRequest("agent 是空串：要的是**树根会话 id**（官方 source=lookup）")
	}
	if api.SetEnabled == nil {
		return nil, officialNotImplemented("agentTeams.setEnabled 未实现：宿主没有 Team 选择持久化")
	}
	if err := api.SetEnabled(OfficialAgentTeamSetEnabledRequest{Agent: agentID, Enabled: enabled}); err != nil {
		return nil, err
	}
	// 官方 void：本仓用空对象表达（见文件头"与官方 wire 的显式差异"）。
	return map[string]any{}, nil
}

// officialSubagentsInterruptByUser 解析并转发 `subagents.interruptByUser`。
//
// 官方**唯一参数**是 `childSessionId`。rc.1 的 `parentSessionId` / `mode` 在这里
// 是未知字段：strict 参数校验会拒掉（`p.finish()`），这正是契约变化本身。
func officialSubagentsInterruptByUser(api OfficialAgentTeamRC2API, p *officialParams) (any, error) {
	childID, err := p.str("childSessionId")
	if err != nil {
		return nil, err
	}
	if err := p.finish(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(childID) == "" {
		return nil, officialBadRequest("childSessionId 是空串：要的是目标子会话 id")
	}
	if api.InterruptByUser == nil {
		return nil, officialNotImplemented(
			"subagents.interruptByUser 未实现：宿主没有按目标会话接纳取消信号的通道")
	}
	return api.InterruptByUser(childID)
}

// boolean 取一个可选布尔参数：ok=false 表示请求里没有这个键。
//
// 类型不对是**参数错误**（官方 strictCodec 会直接拒），不静默转成 false——
// 把 "enabled":"false" 当成 false 会让"关 Team"看起来成功，而模型以为开了。
func (p *officialParams) boolean(name string) (value bool, ok bool, err error) {
	raw, present := p.take(name)
	if !present {
		return false, false, nil
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, true, officialBadRequest("%s 必须是布尔值（收到 %s）", name, strings.TrimSpace(string(raw)))
	}
	return value, true, nil
}
