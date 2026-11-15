package main

// PARITY-CORDIS-C3B / HOST-CORDIS-SHAPE: dynamicCordisRunner 12 方法的真实宿主实现。
//
// 数据来源只有两处：
//   - Go 核心：internal/cordis（registry/manifest/生命周期）
//   - 外部受管 node：internal/cordis/extern（真实插件包加载/调用/结算）
// 没有 501 假成功、没有静态桩；无 node 时返回结构化不可用 + Hint。
//
// # 官方形状（这一层为什么长成现在这样）
//
// 官方契约：
//   - inventory 结果 schema：`dsh-cordis-host-runner/lib/typert.host.js:20-63`
//   - stop 回执 `src/types.ts:308`、undefine 回执 `:254`
//   - 另外 5 个方法（getClientCode / runHostHalf / invoke / settleUserRun /
//     resolveRequestRun）与两个 report 的入参/结果：同一份 `typert.host.js`
//     （:13-19 / :146-170 / :64-81 / :171-205 / :127-145 / :82-109）。
//
//	inventory  -> [{pluginId, agentId,
//	                packages[{packageId,name,purpose,hasHostHalf,hasClientHalf}],
//	                currentPackageId?, nextPackageId?,
//	                activeRun?{pluginRunId,packageId},
//	                latestRun?{pluginRunId,packageId,mode,status,host,client,error}}]
//	stopFromPanel(agent, pluginId)     -> {ok:true} | {ok:false,reason:'plugin-missing'|'not-running',message}
//	undefineFromPanel(agent, pluginId) -> {ok:true,wasRunning} | {ok:false,reason:'plugin-missing',message}
//	getClientCode(agent, pluginId, pluginRunId)
//	    -> {code,name,pluginId,packageId,pluginRunId}
//	runHostHalf(agent, pluginId, packageId, mode, requestId, approveFutureVersions)
//	    -> {ok:true,pluginId,packageId,pluginRunId,waitingFor,startedHere}
//	     | {ok:false,message,stack?}
//	invoke(pluginId, pluginRunId, method, args)
//	    -> {ok:true,value}
//	     | {ok:false,code:'plugin-not-running'|'stale-run'|'method-not-found'|'handler-error',
//	        message,stack?}
//	settleUserRun(agent, pluginId, resolution)
//	    -> {ok:true,status,pluginId,packageId,pluginRunId,waitingFor,clientWaitingFor?,
//	        currentPackageId?,nextPackageId?,mode}
//	     | {ok:false,reason,message,stack?}
//	resolveRequestRun(requestId, resolution) -> {accepted}
//	reportClientGuardFailure / reportRenderFailure -> null
//
// # 本宿主能给出的真值，逐字段来自哪里
//
//   - `agentId`：官方在 define 时把 Plugin 绑到一个会话上；本宿主**没有 define
//     通道**（官方 rc.2 也不注册任何 cordis 模型工具：`dsh-cordis-host-runner/
//     README.md:119`「This package registers no tool or prompt」），归属只能来自
//     激活/面板调用里带的 `sessionId`（或 5 个方法的 `agentId`）。没带过就是空串——
//     **不编一个会话名**，官方 UI 因此把这类行归到「其他会话」。
//   - `packages`：官方是 define 顺序的不可变版本表；本宿主一个插件目录只有一个
//     版本，所以数组恰好一项。`packageId` 由磁盘 package.json 的 `name@version`
//     派生（同 name@version 即同身份，不随加载次数漂移；官方那个不透明 packageId
//     依赖 define 通道，本宿主没有那份历史）。`purpose` 在盘上**没有来源**，给
//     空串（官方 UI 渲染成「(未填写用途)」）。
//   - `nextPackageId`：本宿主不做「失败/进行中的版本切换」，该字段**不出现**。
//   - `activeRun`：真实激活（外部 node 已加载、进程活着）才出现；包里有
//     `client/client.js` 时停在 `client-pending`——它同样是"进行中的运行"，所以
//     activeRun 出现，但 `currentPackageId` **不推进**（见下）。
//   - `latestRun`：真实尝试记录（成功=running、加载失败=failed +
//     `error.phase=host-load`、客户端半失败=failed + client-apply、审批被拒=
//     failed + approval、stop 之后=stopped），host/client 两半按真实情况填；
//     没有 Client 半的包 `client.status=absent`。
//   - `pluginRunId`：**每一次真实激活**（外部 node 真把包加载起来）铸一个
//     `run-N`。官方 run 号同样每次激活新铸，语义一致。
//   - `startedHere`：这一次调用**是否真的创建了激活**（新起外部 node = true；
//     挂到已活的那次运行 = false）。官方语义正是「panel 只是挂上来时是 false」。
//   - `waitingFor`（Host 半还缺哪些服务）：本宿主没有 Cordis 的服务/Fiber 依赖图，
//     Host 半就是那个外部 node 进程，"加载成功"即无缺失服务 → 空数组。这是**如实
//     为空**而不是占位：官方 `missingFor()` 在没有缺失服务时同样返回 `[]`。
//   - `clientWaitingFor`：只来自调用方 `resolution.waitingFor`（页面自报）。本进程
//     从不加载 Client 半，不替它编。
//   - `code` / `name`（getClientCode）：外部 node 真实读到的那份 Client 源码与
//     package.json name。`file` 是本仓扩展参数（官方没有这个入参），缺省
//     `client/client.js`。
//   - `accepted`（resolveRequestRun）：本宿主**没有** model 侧 `cordis/request-run`
//     待审批请求表（官方 rc.2 也不注册任何 cordis 模型工具，见该包 README 的
//     "registers no tool or prompt"），所以任何 requestId 都落在官方语义的
//     「unknown」分支 → `accepted:false`。**这是官方对未知请求的定义**，不是假装
//     成功：本宿主永远不会把一次 resolution 认成"结算掉了某个请求"。
//   - 4 个 cordis 事件里只有 2 个在本宿主有真实触发点：`cordis/dynamic-package`
//     （激活成功）与 `cordis/dynamic-retract`（stop/undefine 撤下）。
//     另 2 个（`cordis/request-run` / `cordis/request-run-resolved`）要求「待审批的
//     Client 半激活请求」，本宿主没有 Cordis 审批队列，**不发空帧/假帧**。
//
// # 结算（settleUserRun）为什么不是硬编
//
// 官方把「Host 半已起来、Client 半等页面加载」记成 `client-pending`，页面加载完
// 用页面自己的结果来结算：`{ok:true,pluginRunId,waitingFor?}` 或拒绝/失败
// `{ok:false,reason,...}`。本宿主有那一半**真实状态**：包里有 `client/client.js`
// 时，激活后就是 `client-pending`；结算成功才把该 package 记成 current。所以这里
// 是按真实状态机实现，不是"先编一个 running"。
//
// 审批队列依旧不存在：`resolution.reason='rejected'` 只能记成 rejected 尝试
// （官方那一步是用户点了拒绝），本宿主没有发起过审批，也就没有可"拒绝"的请求。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"dshgo/internal/cordis"
	"dshgo/internal/cordis/extern"
	"dshgo/internal/transport"
)

// cordisEntry 是宿主内部的一条真实记录（**不是**官方 wire 形状；inventory 的 wire
// 形状见 cordisInventoryRow）。syncManifest 也读它，所以字段保留 id/version/kind 那套。
type cordisEntry struct {
	ID      string   `json:"id"`
	Version string   `json:"version"`
	Kind    string   `json:"kind"` // go | js
	State   string   `json:"state"`
	Dir     string   `json:"dir,omitempty"`
	Methods []string `json:"methods,omitempty"`
	PID     int      `json:"pid,omitempty"`

	// 官方 inventory / 5 个方法需要的真实值。
	Name          string `json:"name,omitempty"`           // package.json 的 name
	PackageID     string `json:"packageId,omitempty"`      // name@version 派生
	RunID         string `json:"pluginRunId,omitempty"`    // 每次激活铸的 run 号
	Owner         string `json:"ownerSessionId,omitempty"` // 激活它的会话；未知为空
	HasHostHalf   bool   `json:"hasHostHalf,omitempty"`
	HasClientHalf bool   `json:"hasClientHalf,omitempty"`
	RunStatus     string `json:"runStatus,omitempty"` // running|client-pending|failed|rejected|stopped
	RunMode       string `json:"runMode,omitempty"`   // run|update
	ErrorPhase    string `json:"errorPhase,omitempty"`
	ErrorMessage  string `json:"errorMessage,omitempty"`

	// lastOKPackage 是上一次**成功**激活过的包身份，只用来判 run/update。
	lastOKPackage string
}

// —— 官方 inventory / stop / undefine 的 wire 形状（字段名逐一取自 typert.host.js / types.ts，不自造） ——

type cordisInventoryPackage struct {
	PackageID     string `json:"packageId"`
	Name          string `json:"name"`
	Purpose       string `json:"purpose"`
	HasHostHalf   bool   `json:"hasHostHalf"`
	HasClientHalf bool   `json:"hasClientHalf"`
}

type cordisHalfState struct {
	Status     string   `json:"status"` // absent|pending|stopped|running|waiting|failed
	WaitingFor []string `json:"waitingFor"`
	Error      string   `json:"error,omitempty"`
}

type cordisRunDiagnostic struct {
	Phase       string `json:"phase"` // approval|host-load|host-apply|client-load|client-apply|client-render
	Message     string `json:"message"`
	PluginID    string `json:"pluginId"`
	PackageID   string `json:"packageId"`
	PluginRunID string `json:"pluginRunId"`
}

type cordisRunAttempt struct {
	PluginRunID string               `json:"pluginRunId"`
	PackageID   string               `json:"packageId"`
	Mode        string               `json:"mode"`   // run|update
	Status      string               `json:"status"` // 官方 DynamicCordisRunStatus 之一
	Host        cordisHalfState      `json:"host"`
	Client      cordisHalfState      `json:"client"`
	Error       *cordisRunDiagnostic `json:"error,omitempty"`
}

type cordisRunRef struct {
	PluginRunID string `json:"pluginRunId"`
	PackageID   string `json:"packageId"`
}

type cordisInventoryRow struct {
	PluginID         string                   `json:"pluginId"`
	AgentID          string                   `json:"agentId"`
	Packages         []cordisInventoryPackage `json:"packages"`
	CurrentPackageID string                   `json:"currentPackageId,omitempty"`
	NextPackageID    string                   `json:"nextPackageId,omitempty"`
	ActiveRun        *cordisRunRef            `json:"activeRun,omitempty"`
	LatestRun        *cordisRunAttempt        `json:"latestRun,omitempty"`
}

// cordisStopOK 是官方 stop 的成功回执：**只有 ok 一个键**。
type cordisStopOK struct {
	OK bool `json:"ok"`
}

// cordisUndefineOK 是官方 undefine 的成功回执：wasRunning 必填。
type cordisUndefineOK struct {
	OK         bool `json:"ok"`
	WasRunning bool `json:"wasRunning"`
}

// cordisRefusal 是 stop/undefine 的官方失败回执（200 里的值，不是 HTTP 错误）。
type cordisRefusal struct {
	OK      bool   `json:"ok"`
	Reason  string `json:"reason"` // plugin-missing | not-running
	Message string `json:"message"`
}

// —— 官方 5 个方法的 wire 形状（字段名逐一取自 typert.host.js，不自造） ——

type cordisClientCode struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	PluginID    string `json:"pluginId"`
	PackageID   string `json:"packageId"`
	PluginRunID string `json:"pluginRunId"`
}

type cordisRunHostOK struct {
	OK          bool     `json:"ok"`
	PluginID    string   `json:"pluginId"`
	PackageID   string   `json:"packageId"`
	PluginRunID string   `json:"pluginRunId"`
	WaitingFor  []string `json:"waitingFor"`
	StartedHere bool     `json:"startedHere"`
}

type cordisRunHostFailure struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	Stack   string `json:"stack,omitempty"`
}

type cordisInvokeValue struct {
	OK    bool `json:"ok"`
	Value any  `json:"value"`
}

type cordisInvokeFailure struct {
	OK      bool   `json:"ok"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Stack   string `json:"stack,omitempty"`
}

type cordisSettleOK struct {
	OK               bool     `json:"ok"`
	Status           string   `json:"status"` // running|awaiting-approval|starting
	PluginID         string   `json:"pluginId"`
	PackageID        string   `json:"packageId"`
	PluginRunID      string   `json:"pluginRunId"`
	WaitingFor       []string `json:"waitingFor"`
	ClientWaitingFor []string `json:"clientWaitingFor,omitempty"`
	CurrentPackageID string   `json:"currentPackageId,omitempty"`
	NextPackageID    string   `json:"nextPackageId,omitempty"`
	Mode             string   `json:"mode"`
}

type cordisSettleFailure struct {
	OK      bool   `json:"ok"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
	Stack   string `json:"stack,omitempty"`
}

type cordisResolveReceipt struct {
	Accepted bool `json:"accepted"`
}

// cordisResolution 是官方 settleUserRun/resolveRequestRun 的 `resolution` 入参
// （typert.host.js:130-141 的判别联合）：
//
//	{ok:true,  pluginRunId, waitingFor?}
//	{ok:false, reason:'rejected'|'host-half-failed'|'client-half-failed',
//	           pluginRunId?, startedHere?, message?, stack?}
//
// `startedHere` 用指针：官方判的是 `resolution.startedHere !== false`，
// "没给"与"给了 false"是两种意思。
type cordisResolution struct {
	OK          bool     `json:"ok"`
	PluginRunID string   `json:"pluginRunId"`
	WaitingFor  []string `json:"waitingFor"`
	Reason      string   `json:"reason"`
	StartedHere *bool    `json:"startedHere"`
	Message     string   `json:"message"`
	Stack       string   `json:"stack"`
}

type cordisService struct {
	home string
	reg  *cordis.Registry

	mu       sync.Mutex
	entries  map[string]*cordisEntry
	clients  map[string]*extern.Client
	runs     map[string]string // requestId -> pluginId
	nodePath string
	nextRun  int

	// 事件的真实出口：与 agent 生命周期帧同一套（每会话 Transport journal + WS）。
	journals *transport.Journals
	pub      func(topic string, frame transport.ServerFrame)
}

func newCordisService(home string) *cordisService {
	return &cordisService{
		home: home, reg: cordis.NewRegistry(nil),
		entries: map[string]*cordisEntry{}, clients: map[string]*extern.Client{},
		runs: map[string]string{},
	}
}

// withEvents 接上事件出口。没接上时事件不发（不是发空帧）——事件要落到
// **真实归属会话**的 journal 里，没有出海口就什么都不做。
func (s *cordisService) withEvents(journals *transport.Journals,
	pub func(string, transport.ServerFrame)) *cordisService {
	if s == nil {
		return nil
	}
	s.journals, s.pub = journals, pub
	return s
}

func (s *cordisService) RegisterGo(id, version string) error {
	if err := s.reg.Register(&goCordisPlugin{id: id, version: version}); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[id] = &cordisEntry{ID: id, Version: version, Kind: "go", State: "registered", Name: id}
	return nil
}

type goCordisPlugin struct{ id, version string }

func (p *goCordisPlugin) Meta() cordis.Meta { return cordis.Meta{ID: p.id, Version: p.version} }
func (p *goCordisPlugin) Apply(*cordis.Context) error {
	return nil
}
func (p *goCordisPlugin) Dispose(*cordis.Context) error { return nil }

func (s *cordisService) pluginDir(id string) string {
	return filepath.Join(s.home, "cordis-plugins", id)
}

// cordisPackageID 由磁盘上真实的 package.json name@version 派生包身份。
//
// 官方 packageId 是 define 时铸的不透明 id；本宿主没有 define 通道，也就没有那
// 份历史。派生值至少满足官方的两条硬要求：**同一版本恒等**、**不同版本必不同**
// （官方 UI 用它比 currentPackageId/activeRun.packageId，派生值稳定才能对上）。
func cordisPackageID(name, version string) string {
	if name == "" && version == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(name + "@" + version))
	return "pkg-" + hex.EncodeToString(sum[:8])
}

// fillMetaFromDisk 用盘上真实存在的 package.json / 入口文件补元数据。
//
// 从未激活过的插件也要能出现在 inventory 里，所以这条路径不启动 node：
// 它只读文件。读不出 package.json 就保持空缺（packages 为空数组），**不猜值**。
func (e *cordisEntry) fillMetaFromDisk(dir string) {
	if dir == "" {
		return
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		var pkg struct {
			Name    string `json:"name"`
			Version string `json:"version"`
			Main    string `json:"main"`
		}
		if json.Unmarshal(raw, &pkg) == nil {
			if pkg.Name != "" {
				e.Name = pkg.Name
			}
			if pkg.Version != "" {
				e.Version = pkg.Version
			}
			main := pkg.Main
			if main == "" {
				main = "host.cjs"
			}
			if st, err := os.Stat(filepath.Join(dir, main)); err == nil && !st.IsDir() {
				e.HasHostHalf = true
			}
			e.PackageID = cordisPackageID(e.Name, e.Version)
		}
	}
	if st, err := os.Stat(filepath.Join(dir, "client", "client.js")); err == nil && !st.IsDir() {
		e.HasClientHalf = true
	}
}

// planActivationLocked 铸 run 号并决定这次是 run 还是 update。
//
// 官方语义：`run` = 启动当前版本，`update` = 换成另一个版本。本宿主只认"上一次
// **成功**激活过的是哪个包"——同一个包再激活是 run，换成另一个包是 update。
// 调用方必须已持有 s.mu。
func (s *cordisService) planActivationLocked(e *cordisEntry, sessionID string) (mode, runID string) {
	s.nextRun++
	runID = fmt.Sprintf("run-%d", s.nextRun)
	mode = "run"
	if e.lastOKPackage != "" && e.PackageID != "" && e.lastOKPackage != e.PackageID {
		mode = "update"
	}
	e.RunID = runID
	e.RunMode = mode
	if sessionID != "" {
		e.Owner = sessionID
	}
	return mode, runID
}

// cordisFrameTarget 是一次待发事件：**没有真实归属会话就不发**。
type cordisFrameTarget struct {
	sessionID string
	typ       string
	data      map[string]any
}

// publish 只发有真实触发点的事件。没有出海口或没有归属会话时静默跳过：
// 发一条没有接收方、也没有对应激活的帧比不发更糟（空帧/假帧）。
func (s *cordisService) publish(targets ...cordisFrameTarget) {
	if s == nil || (s.journals == nil && s.pub == nil) {
		return
	}
	for _, t := range targets {
		if t.sessionID == "" || t.typ == "" {
			continue
		}
		raw, err := json.Marshal(t.data)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cordis 事件序列化失败（type=%s）：%v\n", t.typ, err)
			continue
		}
		emitFrameAt(s.journals, s.pub, t.sessionID, t.typ, raw, time.Now().UnixMilli())
	}
}

// packageEvent 是官方 `cordis/dynamic-package` 的负载
// （`DynamicCordisPackage`：pluginId / packageId / pluginRunId / name）。
func (s *cordisService) packageEvent(pluginID, packageID, runID, name string) cordisFrameTarget {
	return cordisFrameTarget{typ: "cordis/dynamic-package", data: map[string]any{
		"pluginId": pluginID, "packageId": packageID, "pluginRunId": runID, "name": name,
	}}
}

// retractEvent 是官方 `cordis/dynamic-retract` 的负载
// （`DynamicCordisRetracted`：pluginId / packageId / pluginRunId，没有 name）。
func (s *cordisService) retractEvent(pluginID, packageID, runID, sessionID string) cordisFrameTarget {
	return cordisFrameTarget{sessionID: sessionID, typ: "cordis/dynamic-retract", data: map[string]any{
		"pluginId": pluginID, "packageId": packageID, "pluginRunId": runID,
	}}
}

// registeredLocked 取一条记录；盘上有 `cordis-plugins/<id>/package.json` 但内存
// 里还没有记录时补一条 registered。这样 "插件存在但从没跑过" 能如实回
// `not-running`（官方语义），而不是被当成 `plugin-missing`。
// 调用方必须已持有 s.mu。
func (s *cordisService) registeredLocked(id string) *cordisEntry {
	if e := s.entries[id]; e != nil {
		return e
	}
	dir := s.pluginDir(id)
	e := &cordisEntry{ID: id, Kind: "js", State: "registered", Dir: dir}
	e.fillMetaFromDisk(dir)
	if e.Name == "" && e.Version == "" {
		return nil // 读不出 package.json：这不是插件
	}
	s.entries[id] = e
	return e
}

// entrySnapshot 是这条记录在某一刻的真实值（锁内拷贝），调用方在锁外读它。
func (s *cordisService) entrySnapshot(id string) (cordisEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[id]
	if e == nil {
		return cordisEntry{}, false
	}
	return *e, true
}

// liveClient 只在**真的活着**的外部进程上返回 true：官方 invoke/getClientCode
// 都要求"当前激活"，本宿主不把"从没跑过"当成在跑。
func (s *cordisService) liveClient(id string) (*extern.Client, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.clients[id]
	return c, c != nil && c.Alive()
}

// dropClient 撤下一次活着的运行（外部进程已 Close 之后调用）。
func (s *cordisService) dropClient(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.clients, id)
	if e := s.entries[id]; e != nil {
		e.State, e.PID = "stopped", 0
	}
}

// recordFailure 把一次真实加载失败记成官方 latestRun 里的 failed 尝试。
func (s *cordisService) recordFailure(id, sessionID string, cause error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[id]
	if e == nil {
		e = &cordisEntry{ID: id, Kind: "js", Dir: s.pluginDir(id)}
		s.entries[id] = e
	}
	e.fillMetaFromDisk(e.Dir)
	s.nextRun++
	e.RunID = fmt.Sprintf("run-%d", s.nextRun)
	if e.RunMode == "" {
		e.RunMode = "run"
	}
	if sessionID != "" {
		e.Owner = sessionID
	}
	e.State, e.PID = "failed", 0
	e.RunStatus = "failed"
	e.ErrorPhase, e.ErrorMessage = "host-load", cause.Error()
}

// cordisUnavailable 把"这台机器上没有可用的 node"翻成 wire 上的结构化不可用：
// 这不是插件加载失败（那是 ok:false 回执），而是这条能力在当前环境里跑不起来，
// 必须带 Hint 一路传到用户面前，不能被折成 ok:false 或空结果。
func cordisUnavailable(err error) error {
	var unavail *extern.UnavailableError
	if !errors.As(err, &unavail) {
		return err
	}
	return &transport.CordisAPIError{
		Status:  501,
		Code:    "not_implemented",
		Message: unavail.Error(),
		Hint:    unavail.Hint,
	}
}

// ensureJS 拿到（必要时启动）插件的外部 node 进程，并完成一次**真实激活**。
//
// started 表示这次调用是否真的创建了激活（官方 runHostHalf 的 startedHere）：
// 挂到已活的那次运行是 false，新起进程并加载成功是 true。
// sessionID 是归属会话（官方首参 agent）；带上它才发 `cordis/dynamic-package`。
func (s *cordisService) ensureJS(ctx context.Context, id, sessionID string,
) (c *extern.Client, e *cordisEntry, started bool, err error) {
	s.mu.Lock()
	if live := s.clients[id]; live != nil && live.Alive() {
		e = s.entries[id]
		if e != nil && sessionID != "" && e.Owner == "" {
			e.Owner = sessionID
		}
		s.mu.Unlock()
		if e == nil {
			return nil, nil, false, fmt.Errorf("cordis: 插件 %q 无记录", id)
		}
		return live, e, false, nil
	}
	// 先按住一条占位记录，好让并发的失败尝试也落在同一个条目上。
	if s.entries[id] == nil {
		s.entries[id] = &cordisEntry{ID: id, Kind: "js", State: "registered", Dir: s.pluginDir(id)}
	}
	s.mu.Unlock()

	dir := s.pluginDir(id)
	proc, startErr := extern.Start(ctx, extern.Options{NodePath: s.nodePath})
	if startErr != nil {
		s.recordFailure(id, sessionID, startErr)
		return nil, nil, false, cordisUnavailable(startErr)
	}
	info, loadErr := proc.LoadPackage(ctx, dir)
	if loadErr != nil {
		_ = proc.Close()
		s.recordFailure(id, sessionID, loadErr)
		return nil, nil, false, cordisUnavailable(loadErr)
	}

	s.mu.Lock()
	e = s.entries[id]
	if e == nil {
		e = &cordisEntry{ID: id, Kind: "js"}
		s.entries[id] = e
	}
	e.Kind, e.State, e.Dir, e.Methods, e.PID = "js", "running", dir, info.Methods, info.PID
	if info.Name != "" {
		e.Name = info.Name
	}
	if info.Version != "" {
		e.Version = info.Version
	}
	e.fillMetaFromDisk(dir)
	if e.Name == "" {
		e.Name = id
	}
	_, runID := s.planActivationLocked(e, sessionID)
	if e.HasClientHalf {
		// 官方 startFresh：包里有 Client 半 → 停在 client-pending，等页面结算。
		// currentPackageId 这时**不能**推进：官方只在结算成功（commitActivation）
		// 才把包记成 current，未结算的包不算"上一次成功版本"。
		e.RunStatus = "client-pending"
	} else {
		// 官方：没有 Client 半就原地 commit，本次激活即 current。
		e.RunStatus = "running"
		e.lastOKPackage = e.PackageID
	}
	e.ErrorPhase, e.ErrorMessage = "", ""
	owner := e.Owner
	pkgID, name := e.PackageID, e.Name
	s.clients[id] = proc
	s.mu.Unlock()

	if owner != "" {
		ev := s.packageEvent(id, pkgID, runID, name)
		ev.sessionID = owner
		s.publish(ev)
	}
	return proc, e, true, nil
}

func (s *cordisService) HandleOfficialCordis(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	var p map[string]json.RawMessage
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("参数不是 JSON 对象: %w", err)
	}
	str := func(key string) string {
		var v string
		_ = json.Unmarshal(p[key], &v)
		return v
	}
	// 官方方法的第一个参数是 agent（会话身份）。本仓 wire 是自定义的，所以用命名
	// 参数承载它：面板/事件路径用 `sessionId`，另外 5 个方法用 `agentId`；两个都
	// 没有就没有归属，事件也不发（见文件头）。
	session := str("sessionId")
	if session == "" {
		session = str("agentId")
	}
	switch method {
	case string(transport.MethodDynamicCordisRunnerInventory):
		return s.inventory(), nil
	case string(transport.MethodDynamicCordisRunnerGetClientCode):
		id := str("pluginId")
		if id == "" {
			return nil, errors.New("pluginId 必填")
		}
		wantRun := str("pluginRunId")
		// 官方 getClientCode 只对**当前那次激活**发 Client 源码，且它自己不激活。
		client, live := s.liveClient(id)
		if !live {
			return nil, &transport.CordisAPIError{Status: 404, Code: "not_found",
				Message: fmt.Sprintf("插件 %q 没有活着的运行：官方只给当前激活发 Client 源码，先 runHostHalf", id)}
		}
		snap, ok := s.entrySnapshot(id)
		if !ok || snap.RunID == "" {
			return nil, &transport.CordisAPIError{Status: 404, Code: "not_found",
				Message: fmt.Sprintf("插件 %q 没有当前的激活记录", id)}
		}
		if wantRun != "" && wantRun != snap.RunID {
			return nil, &transport.CordisAPIError{Status: 404, Code: "not_found",
				Message: fmt.Sprintf("插件 %q 当前的激活是 %q，不是 %q", id, snap.RunID, wantRun)}
		}
		file := str("file")
		if file == "" {
			file = "client/client.js"
		}
		code, err := client.GetClientCode(ctx, s.pluginDir(id), file)
		if err != nil {
			return nil, cordisUnavailable(err)
		}
		return cordisClientCode{
			Code:        string(code),
			Name:        snap.Name,
			PluginID:    id,
			PackageID:   snap.PackageID,
			PluginRunID: snap.RunID,
		}, nil
	case string(transport.MethodDynamicCordisRunnerInvoke):
		id, handler := str("pluginId"), str("method")
		if id == "" || handler == "" {
			return nil, errors.New("pluginId/method 必填")
		}
		// 官方 invoke 不激活插件：没在跑就是 plugin-not-running，不是"顺手帮你跑起来"。
		client, live := s.liveClient(id)
		if !live {
			return cordisInvokeFailure{Code: "plugin-not-running",
				Message: fmt.Sprintf("插件 %q 没有活着的运行", id)}, nil
		}
		snap, ok := s.entrySnapshot(id)
		if !ok || snap.RunID == "" {
			return cordisInvokeFailure{Code: "plugin-not-running",
				Message: fmt.Sprintf("插件 %q 没有活着的运行", id)}, nil
		}
		if wantRun := str("pluginRunId"); wantRun != "" && wantRun != snap.RunID {
			return cordisInvokeFailure{Code: "stale-run",
				Message: fmt.Sprintf("激活 %q 已不再是插件 %q 的当前运行（当前是 %q）", wantRun, id, snap.RunID)}, nil
		}
		reqID := str("requestId")
		var args any
		if rawArgs := p["args"]; len(rawArgs) > 0 {
			_ = json.Unmarshal(rawArgs, &args)
		}
		var out any
		// 官方 cordis.hostRunner.vmTimeoutMs（默认 5000ms）：脚本执行不能靠上游 ctx
		// 兜底，否则一个死循环的插件会把这次 HTTP 请求按到永远。
		vmCtx, cancel := withCordisBudget(ctx, cordisVMTimeout())
		defer cancel()
		_, err := client.Invoke(vmCtx, reqID, handler, args, &out)
		if reqID != "" {
			s.mu.Lock()
			s.runs[reqID] = id
			s.mu.Unlock()
		}
		if err != nil {
			return cordisInvokeFailureOf(err), nil
		}
		return cordisInvokeValue{OK: true, Value: out}, nil
	case string(transport.MethodDynamicCordisRunnerRunHostHalf):
		return s.runHostHalf(ctx, p, str, session)
	case string(transport.MethodDynamicCordisRunnerSettleUserRun):
		id := str("pluginId")
		if id == "" {
			return nil, errors.New("pluginId 必填（官方 settleUserRun(agent, pluginId, resolution) 的首参之后是 pluginId，不是 requestId）")
		}
		res, given, err := cordisResolutionOf(p)
		if err != nil {
			return nil, err
		}
		if !given {
			return nil, errors.New("resolution 必填（官方 settleUserRun 第三参）")
		}
		return s.settleUserRun(id, res)
	case string(transport.MethodDynamicCordisRunnerResolveRequestRun):
		if str("requestId") == "" {
			return nil, errors.New("requestId 必填")
		}
		// 本宿主没有 model 侧待审批的 run 请求表（见文件头），任何 requestId 都落在
		// 官方语义的 unknown 分支。这正是官方对"迟到的/不存在的请求"的定义：不结算
		// 任何东西，回 false。绝不假装 accepted。
		return cordisResolveReceipt{Accepted: false}, nil
	case string(transport.MethodDynamicCordisRunnerResolveInspectQuery):
		id := str("pluginId")
		if id == "" {
			return s.inventory(), nil
		}
		// 这个方法**不在本轮形状对齐的 5 个之内**：保持它原来的行为（无活进程时
		// 按需启动），只跟着 ensureJS 的新签名走，不顺手改它的形状。
		client, _, _, err := s.ensureJS(ctx, id, session)
		if err != nil {
			return nil, err
		}
		// 官方 cordis.hostRunner.clientInspectTimeoutMs（默认 10000ms）。
		inspectCtx, cancel := withCordisBudget(ctx, cordisClientInspectTimeout())
		defer cancel()
		info, err := client.ResolveInspectQuery(inspectCtx)
		if err != nil {
			return nil, cordisUnavailable(err)
		}
		return info, nil
	case string(transport.MethodDynamicCordisRunnerSyncInspectManifest):
		return s.syncManifest()
	case string(transport.MethodDynamicCordisRunnerReportClientGuardFailure),
		string(transport.MethodDynamicCordisRunnerReportRenderFailure):
		// 官方这两个方法的 zod 结果就是 z.literal(null)（typert.host.js:94 / :109）：
		// 真正的交付是"把它 steer 进 agent 会话"这个副作用，回执本身没有载荷。
		// 本宿主没有该注入通道，但真实副作用（落盘诊断）保留；回执照官方给 null。
		if err := s.report(method, str("pluginId"), str("pluginRunId"), raw); err != nil {
			return nil, err
		}
		return transport.NullCordisResult(), nil
	case string(transport.MethodDynamicCordisRunnerStopFromPanel):
		return s.stopFromPanel(str("pluginId"), session)
	case string(transport.MethodDynamicCordisRunnerUndefineFromPanel):
		return s.undefineFromPanel(str("pluginId"), session)
	default:
		return nil, fmt.Errorf("不是 dynamicCordisRunner 方法: %s", method)
	}
}

// runHostHalf 回官方 DynamicCordisHostRunResponse：
//
//	{ok:true,pluginId,packageId,pluginRunId,waitingFor,startedHere}
//	{ok:false,message,stack?}
//
// 失败是 **200 里的值**（官方 runHostHalf 从不抛），只有"这台机器没有 node"
// 那种环境性不可用才走结构化错误（带 Hint）。
//
// 入参按官方语义校验（官方 resolvePlan）：packageId 必须是这个插件真实有的包；
// mode 必须与真实状态一致（同一包=run，换包=update）；requestId 非 null 时官方要求
// 它对应一条**待审批的** run 请求——本宿主没有那张表，所以非 null 一律拒绝，绝不
// 偷偷把一次"审批请求"当成直接激活放行。
func (s *cordisService) runHostHalf(ctx context.Context, p map[string]json.RawMessage,
	str func(string) string, sessionID string) (any, error) {
	id := str("pluginId")
	if id == "" {
		return nil, errors.New("pluginId 必填")
	}
	// 官方 runHostHalf 的 requestId 是"model 驱动的待审批请求"，null 才是面板直接操作。
	if reqID := str("requestId"); reqID != "" {
		return cordisRunHostFailure{Message: fmt.Sprintf(
			"run request %q 不存在：本宿主没有 model 侧待审批的 cordis run 请求表，无法授权这次激活（官方语义要求它对应一条 pending 请求）", reqID)}, nil
	}
	// approveFutureVersions 只对"审批覆盖后续版本"有意义；本宿主没有审批，参数被忽略。

	s.mu.Lock()
	e := s.registeredLocked(id)
	if e == nil {
		s.mu.Unlock()
		return cordisRunHostFailure{Message: fmt.Sprintf("插件 %q 不存在：盘上 %s 没有 package.json", id, s.pluginDir(id))}, nil
	}
	kind, pkgID, lastOK := e.Kind, e.PackageID, e.lastOKPackage
	s.mu.Unlock()

	if kind == "go" {
		return cordisRunHostFailure{Message: fmt.Sprintf(
			"插件 %q 是 Go 原生插件，没有 Host 半可跑（官方 runHostHalf 只作用在 JS Package 上）", id)}, nil
	}
	if pkgID == "" {
		return cordisRunHostFailure{Message: fmt.Sprintf(
			"插件 %q 的 package.json 里读不出 name@version，无法确定 packageId", id)}, nil
	}
	if wantPkg := str("packageId"); wantPkg != "" && wantPkg != pkgID {
		return cordisRunHostFailure{Message: fmt.Sprintf("插件 %q 没有包 %q（当前包是 %q）", id, wantPkg, pkgID)}, nil
	}
	if mode := str("mode"); mode != "" {
		switch mode {
		case "run", "update":
		default:
			return cordisRunHostFailure{Message: fmt.Sprintf("mode 只能是 run / update，得到 %q", mode)}, nil
		}
		if mode == "run" && lastOK != "" && lastOK != pkgID {
			return cordisRunHostFailure{Message: fmt.Sprintf(
				"包 %q 与当前 %q 不同；换版本请用 mode=update", pkgID, lastOK)}, nil
		}
		if mode == "update" && lastOK == "" {
			return cordisRunHostFailure{Message: fmt.Sprintf(
				"插件 %q 还没有成功的版本可换；首跑 %q 请用 mode=run", id, pkgID)}, nil
		}
		if mode == "update" && lastOK == pkgID {
			return cordisRunHostFailure{Message: fmt.Sprintf(
				"包 %q 已是当前版本；跑当前版本请用 mode=run", pkgID)}, nil
		}
	}

	_, _, started, err := s.ensureJS(ctx, id, sessionID)
	if err != nil {
		var api *transport.CordisAPIError
		if errors.As(err, &api) {
			return nil, err // 环境性不可用：结构化错误 + Hint，不折成 ok:false
		}
		return cordisRunHostFailure{Message: err.Error()}, nil
	}
	snap, ok := s.entrySnapshot(id)
	if !ok || snap.RunID == "" {
		return cordisRunHostFailure{Message: fmt.Sprintf("插件 %q 激活后没有运行记录", id)}, nil
	}
	return cordisRunHostOK{
		OK:          true,
		PluginID:    id,
		PackageID:   snap.PackageID,
		PluginRunID: snap.RunID,
		WaitingFor:  []string{},
		StartedHere: started,
	}, nil
}

// settleUserRun 回官方 DynamicCordisSettleResponse（typert.host.js:188-205）。
//
//	ok:true  -> {status,pluginId,packageId,pluginRunId,waitingFor,clientWaitingFor?,
//	             currentPackageId?,nextPackageId?,mode}
//	ok:false -> {reason,message,stack?}
//
// 值的来源：激活记录（pluginId/packageId/pluginRunId/mode）是真的；`waitingFor`
// 是 Host 半缺的服务（本宿主没有服务依赖图 → 空数组，见文件头）；`clientWaitingFor`
// 只来自页面自报的 resolution。
func (s *cordisService) settleUserRun(pluginID string, res cordisResolution) (any, error) {
	s.mu.Lock()
	e := s.registeredLocked(pluginID)
	if e == nil {
		s.mu.Unlock()
		return cordisSettleFailure{OK: false, Reason: "plugin-missing",
			Message: fmt.Sprintf("插件 %q 不存在", pluginID)}, nil
	}
	client := s.clients[pluginID]
	alive := client != nil && client.Alive()
	snap := *e
	s.mu.Unlock()

	if !res.OK {
		reason := strings.TrimSpace(res.Reason)
		msg := res.Message
		if msg == "" {
			msg = reason
		}
		switch reason {
		case "rejected":
			s.failRun(pluginID, snap.RunID, "rejected", "approval", msg)
			return cordisSettleFailure{OK: false, Reason: "rejected", Message: msg, Stack: res.Stack}, nil
		case "host-half-failed", "client-half-failed":
			// 官方：run 号对得上、且不是"页面只是挂上来"时撤下那次运行。
			startedHere := res.StartedHere == nil || *res.StartedHere
			if alive && snap.RunID != "" && (res.PluginRunID == "" || res.PluginRunID == snap.RunID) && startedHere {
				if err := client.Close(); err != nil {
					return nil, fmt.Errorf("撤下插件 %q 的 Host 半失败: %w", pluginID, err)
				}
				s.dropClient(pluginID)
			}
			phase := "client-apply"
			if reason == "host-half-failed" {
				phase = "host-apply"
			}
			s.failRun(pluginID, snap.RunID, "failed", phase, msg)
			return cordisSettleFailure{OK: false, Reason: reason, Message: msg, Stack: res.Stack}, nil
		default:
			return nil, &transport.CordisAPIError{Status: 400, Code: "bad_request",
				Message: fmt.Sprintf("resolution.reason 只能是 rejected / host-half-failed / client-half-failed，得到 %q", res.Reason)}
		}
	}

	// ok 分支：官方只接受"当前那次激活"的结算（run 号必须对上，否则那次激活已不在了）。
	if !alive || snap.RunID == "" || snap.PackageID == "" || res.PluginRunID != snap.RunID {
		return cordisSettleFailure{OK: false, Reason: "client-half-failed",
			Message: fmt.Sprintf("激活 %q 已不再是插件 %q 的当前运行", res.PluginRunID, pluginID)}, nil
	}
	s.commitActivation(pluginID)
	after, ok := s.entrySnapshot(pluginID)
	if !ok {
		return cordisSettleFailure{OK: false, Reason: "client-half-failed",
			Message: fmt.Sprintf("插件 %q 在结算期间被移除", pluginID)}, nil
	}
	out := cordisSettleOK{
		OK:               true,
		Status:           "running",
		PluginID:         pluginID,
		PackageID:        after.PackageID,
		PluginRunID:      after.RunID,
		WaitingFor:       []string{},
		CurrentPackageID: after.PackageID,
		Mode:             cordisModeOrRun(after.RunMode),
	}
	// 官方：只有页面报了 clientWaitingFor 才带这个键；没报就不出现（不是空数组）。
	if res.WaitingFor != nil {
		out.ClientWaitingFor = append([]string{}, res.WaitingFor...)
	}
	return out, nil
}

// commitActivation 把一次结算成功的激活记成 current（官方 commitActivation）。
func (s *cordisService) commitActivation(pluginID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[pluginID]
	if e == nil {
		return
	}
	e.RunStatus = "running"
	e.lastOKPackage = e.PackageID
	e.ErrorPhase, e.ErrorMessage = "", ""
}

// failRun 把一次真实结算失败记到官方 latestRun 会读的那几个字段上。
func (s *cordisService) failRun(pluginID, runID, status, phase, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[pluginID]
	if e == nil {
		return
	}
	if runID != "" && e.RunID != "" && e.RunID != runID {
		return // 报的是另一次激活，不覆盖当前那条
	}
	e.RunStatus = status
	e.ErrorPhase, e.ErrorMessage = phase, message
}

func cordisModeOrRun(mode string) string {
	if mode == "update" {
		return "update"
	}
	return "run"
}

// cordisResolutionOf 读官方的第三/第二参 `resolution`（对象）。
func cordisResolutionOf(p map[string]json.RawMessage) (cordisResolution, bool, error) {
	var res cordisResolution
	raw, ok := p["resolution"]
	if !ok || len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return res, false, nil
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return res, false, fmt.Errorf("resolution 必须是对象: %w", err)
	}
	return res, true, nil
}

// cordisInvokeFailureOf 把一次外部调用失败翻成官方 invoke 的 4 个 code
// （typert.host.js:78）：plugin-not-running | stale-run | method-not-found | handler-error。
//
// 分类只认**结构化**信息：extern 的哨兵错误和 JSON-RPC 码，不比对错误文本。
func cordisInvokeFailureOf(err error) cordisInvokeFailure {
	fail := cordisInvokeFailure{Code: "handler-error", Message: err.Error()}
	if errors.Is(err, extern.ErrNotRunning) {
		fail.Code = "plugin-not-running"
		return fail
	}
	var rpcErr *extern.RPCError
	if errors.As(err, &rpcErr) {
		if rpcErr.Code == extern.CodeMethodNotFound {
			fail.Code = "method-not-found"
		}
		fail.Message = rpcErr.Message
	}
	return fail
}

// stopFromPanel 回官方 DynamicCordisStopResponse：
//
//	{ok:true}                                  真的停掉了一次活着的运行
//	{ok:false,reason:"not-running",message}    插件在、但没有活着的运行
//	{ok:false,reason:"plugin-missing",message} 插件不存在
//
// 三个都是 **200 里的值**，不是 HTTP 错误——官方 UI 自己按 reason 分支
// （`dsh-client-ui-cordis/src/client/index.ts:79-85`），把 not-running 当失败弹错。
func (s *cordisService) stopFromPanel(id, sessionID string) (any, error) {
	if id == "" {
		return nil, errors.New("pluginId 必填")
	}
	s.mu.Lock()
	e := s.registeredLocked(id)
	if e == nil {
		s.mu.Unlock()
		return cordisRefusal{Reason: "plugin-missing", Message: fmt.Sprintf("插件 %q 不存在", id)}, nil
	}
	c := s.clients[id]
	if c == nil || !c.Alive() {
		s.mu.Unlock()
		return cordisRefusal{Reason: "not-running", Message: fmt.Sprintf("插件 %q 没有活着的运行", id)}, nil
	}
	delete(s.clients, id)
	e.State, e.PID = "stopped", 0
	e.RunStatus = "stopped"
	owner, pkgID, runID := e.Owner, e.PackageID, e.RunID
	s.mu.Unlock()

	closeErr := c.Close()
	// 撤下是既成事实，撤帧不能因为关闭报错就吞掉；关闭失败单独回给调用方。
	s.publish(s.retractEvent(id, pkgID, runID, owner))
	if closeErr != nil {
		return nil, fmt.Errorf("停止插件 %q 时关闭外部进程失败: %w", id, closeErr)
	}
	_ = sessionID // 官方用它决定把结果 message 注入哪个会话；本宿主没有该注入通道
	return cordisStopOK{OK: true}, nil
}

// undefineFromPanel 回官方 DynamicCordisUndefineReceipt：
//
//	{ok:true,wasRunning}                        删掉了；wasRunning 说它删之前是否在跑
//	{ok:false,reason:"plugin-missing",message}  插件不存在
//
// 与 stop 的区别照官方：stop 只撤下运行、Package 还在；undefine 连 Package 一起删
// （本宿主就是删 `cordis-plugins/<id>/` 目录）。
func (s *cordisService) undefineFromPanel(id, sessionID string) (any, error) {
	if id == "" {
		return nil, errors.New("pluginId 必填")
	}
	s.mu.Lock()
	e := s.registeredLocked(id)
	if e == nil {
		s.mu.Unlock()
		return cordisRefusal{Reason: "plugin-missing", Message: fmt.Sprintf("插件 %q 不存在", id)}, nil
	}
	c := s.clients[id]
	wasRunning := (c != nil && c.Alive()) || e.RunStatus == "running" || e.RunStatus == "client-pending"
	owner, pkgID, runID := e.Owner, e.PackageID, e.RunID
	delete(s.clients, id)
	delete(s.entries, id)
	s.mu.Unlock()

	if c != nil {
		if err := c.Close(); err != nil {
			return nil, fmt.Errorf("移除插件 %q 时关闭外部进程失败: %w", id, err)
		}
	}
	// 本宿主的"定义"就是 `cordis-plugins/<id>/` 这个目录：内存里删掉却把目录留着，
	// inventory 下一拍又会把它读回来（那就成了"移除了还在"）。所以真的删目录——
	// 与官方 undefine「删除插件及其全部 Package」同一语义，也同一样不可撤销。
	dir := s.pluginDir(id)
	if err := os.RemoveAll(dir); err != nil {
		return nil, fmt.Errorf("移除插件 %q 的目录 %s 失败: %w", id, dir, err)
	}
	if wasRunning {
		s.publish(s.retractEvent(id, pkgID, runID, owner))
	}
	_ = sessionID
	return cordisUndefineOK{OK: true, WasRunning: wasRunning}, nil
}

func (s *cordisService) inventory() []cordisInventoryRow {
	// 锁里只抄值，读盘与装配都在锁外：inventory 是面板的读路径，不该让文件系统
	// 占着会话级的锁。
	s.mu.Lock()
	byID := make(map[string]cordisEntry, len(s.entries))
	for id, e := range s.entries {
		byID[id] = *e
	}
	s.mu.Unlock()

	// 盘上的 `cordis-plugins/<id>/` 也是真实定义：本宿主没有 define 通道，
	// 这个目录就是"已定义插件"的落点。没有 package.json 的目录不是插件，跳过。
	if dirs, err := os.ReadDir(filepath.Join(s.home, "cordis-plugins")); err == nil {
		for _, d := range dirs {
			if !d.IsDir() {
				continue
			}
			id := d.Name()
			if _, ok := byID[id]; ok {
				continue
			}
			byID[id] = cordisEntry{ID: id, Kind: "js", State: "registered",
				Dir: filepath.Join(s.home, "cordis-plugins", id)}
		}
	}

	rows := make([]cordisInventoryRow, 0, len(byID))
	for id, e := range byID {
		dir := e.Dir
		if dir == "" && e.Kind == "js" {
			dir = s.pluginDir(id)
		}
		if e.PackageID == "" && dir != "" {
			e.fillMetaFromDisk(dir)
		}
		if e.Kind == "js" && e.Name == "" && e.Version == "" {
			continue // 读不出 package.json：那不是插件，别造一行
		}
		rows = append(rows, e.officialRow())
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].PluginID < rows[j].PluginID })
	return rows
}

// officialRow 把一条内部记录翻成官方行。
func (e *cordisEntry) officialRow() cordisInventoryRow {
	row := cordisInventoryRow{PluginID: e.ID, AgentID: e.Owner}
	if e.PackageID != "" {
		row.Packages = []cordisInventoryPackage{{
			PackageID:     e.PackageID,
			Name:          e.Name,
			Purpose:       "", // 盘上没有来源；不编
			HasHostHalf:   e.HasHostHalf,
			HasClientHalf: e.HasClientHalf,
		}}
	} else {
		row.Packages = []cordisInventoryPackage{}
	}
	// currentPackageId 只在**结算成功**（或没有 Client 半的原位 commit）之后出现：
	// 停在 client-pending 的那次运行还没有被页面确认，不能算"上一次成功版本"。
	if e.RunStatus == "running" && e.PackageID != "" {
		row.CurrentPackageID = e.PackageID
	}
	if e.RunID != "" && e.RunStatus != "" {
		row.LatestRun = &cordisRunAttempt{
			PluginRunID: e.RunID,
			PackageID:   e.PackageID,
			Mode:        cordisModeOrRun(e.RunMode),
			Status:      cordisAttemptStatus(e.RunStatus),
			Host:        cordisHostHalf(e),
			Client:      cordisClientHalf(e),
		}
		if (e.RunStatus == "failed" || e.RunStatus == "rejected") && e.ErrorMessage != "" {
			row.LatestRun.Error = &cordisRunDiagnostic{
				Phase:       cordisErrorPhaseOrHostLoad(e.ErrorPhase),
				Message:     e.ErrorMessage,
				PluginID:    e.ID,
				PackageID:   e.PackageID,
				PluginRunID: e.RunID,
			}
		}
	}
	// activeRun：进行中的那次运行。client-pending 同样是"Host 半活着、还没结算"的
	// 进行中运行，官方 activeRun 对它是成立的（currentPackageId 则不然）。
	if (e.RunStatus == "running" || e.RunStatus == "client-pending") && e.RunID != "" && e.PackageID != "" {
		row.ActiveRun = &cordisRunRef{PluginRunID: e.RunID, PackageID: e.PackageID}
	}
	return row
}

// cordisAttemptStatus 映射到官方 DynamicCordisRunStatus 词表。
func cordisAttemptStatus(status string) string {
	switch status {
	case "failed":
		return "failed"
	case "rejected":
		// 官方那一步是用户点了拒绝：这次尝试没有成功。
		return "failed"
	case "stopped":
		return "stopped"
	case "running":
		return "running"
	case "client-pending":
		// Host 半起来了、等 Client 半结算：官方这叫 waiting。
		return "waiting"
	default:
		return "waiting"
	}
}

func cordisErrorPhaseOrHostLoad(phase string) string {
	if phase == "" {
		return "host-load"
	}
	return phase
}

func cordisHostHalf(e *cordisEntry) cordisHalfState {
	switch {
	case e.Kind == "go":
		return cordisHalfState{Status: "running", WaitingFor: []string{}}
	case e.RunStatus == "client-pending", e.RunStatus == "running":
		// Host 半就是那个外部 node 进程：已经加载成功，只是 Client 半还没结算。
		return cordisHalfState{Status: "running", WaitingFor: []string{}}
	case e.RunStatus == "failed":
		return cordisHalfState{Status: "failed", WaitingFor: []string{}, Error: e.ErrorMessage}
	case e.RunStatus == "stopped":
		return cordisHalfState{Status: "stopped", WaitingFor: []string{}}
	default:
		return cordisHalfState{Status: "absent", WaitingFor: []string{}}
	}
}

// cordisClientHalf 说真话：本宿主从不加载 Client 半（那是浏览器的事），
// 所以这里只有"没有 Client 半"（absent）或"有但本进程没在跑它"（pending）。
func cordisClientHalf(e *cordisEntry) cordisHalfState {
	if !e.HasClientHalf {
		return cordisHalfState{Status: "absent", WaitingFor: []string{}}
	}
	if e.RunStatus == "client-pending" || e.RunStatus == "running" {
		return cordisHalfState{Status: "pending", WaitingFor: []string{}}
	}
	return cordisHalfState{Status: "stopped", WaitingFor: []string{}}
}

// entriesSnapshot 是内部视图（syncManifest 用），不是官方 wire 形状。
func (s *cordisService) entriesSnapshot() []*cordisEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*cordisEntry, 0, len(s.entries))
	for _, e := range s.entries {
		snap := *e
		out = append(out, &snap)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// findClientFor 用 requestId 找回那次调用所在的活进程与条目。
//
// 官方 invoke/settleUserRun 都以 requestId 定位一次运行；本宿主的 model 侧没有
// 待审批请求表（见文件头），所以这张表只承接 invoke 真实登记过的 requestId，
// 用来如实回答"这个 requestId 属于哪个插件的哪次运行"，**不冒充审批队列**。
func (s *cordisService) findClientFor(requestID string) (*extern.Client, *cordisEntry, bool) {
	s.mu.Lock()
	id, ok := s.runs[requestID]
	c := s.clients[id]
	e := s.entries[id]
	s.mu.Unlock()
	return c, e, ok && c != nil
}

func (s *cordisService) syncManifest() (map[string]any, error) {
	entries := s.entriesSnapshot()
	doc := map[string]any{"version": 1, "plugins": entries}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	doc["hash"] = hash
	dir := filepath.Join(s.home, "state", "cordis")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	out, _ := json.MarshalIndent(doc, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), append(out, '\n'), 0o644); err != nil {
		return nil, err
	}
	return map[string]any{"path": filepath.ToSlash(filepath.Join(dir, "manifest.json")), "hash": hash, "plugins": entries}, nil
}

// report 落地一条真实的失败诊断。**回执不在这里**：官方那两个 report 方法的结果是
// `z.literal(null)`，载荷在副作用里（官方把它 steer 进 agent 会话；本宿主落盘）。
func (s *cordisService) report(method, pluginID, pluginRunID string, raw json.RawMessage) error {
	dir := filepath.Join(s.home, "state", "cordis")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	ev := map[string]any{
		"id":     fmt.Sprintf("%s-%d", method, time.Now().UnixNano()),
		"method": method, "pluginId": pluginID, "pluginRunId": pluginRunID,
		"at": time.Now().UTC().Format(time.RFC3339Nano), "payload": json.RawMessage(raw),
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}
	return nil
}

// 编译期证明实现满足 transport 接口；也帮助 C3 的路由接线。
var _ transport.OfficialCordisAPI = (*cordisService)(nil)

func (s *cordisService) String() string { return strings.TrimSpace(filepath.Base(s.home)) }
