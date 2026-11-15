package main

// PARITY-CORDIS-C3B / HOST-CORDIS-SHAPE: dynamicCordisRunner 12 方法的真实宿主实现。
//
// 数据来源只有两处：
//   - Go 核心：internal/cordis（registry/manifest/生命周期）
//   - 外部受管 node：internal/cordis/extern（真实插件包加载/调用/结算）
// 没有 501、没有静态成功；无 node 时返回结构化不可用 + Hint。
//
// # 官方形状（这一层为什么长成现在这样）
//
// 官方契约（`dsh-cordis-host-runner/lib/typert.host.js:20-63` 的 inventory 结果
// schema，`src/types.ts:308` 的 stop 回执与 `:254` 的 undefine 回执）规定：
//
//	inventory  -> [{pluginId, agentId,
//	                packages[{packageId,name,purpose,hasHostHalf,hasClientHalf}],
//	                currentPackageId?, nextPackageId?,
//	                activeRun?{pluginRunId,packageId},
//	                latestRun?{pluginRunId,packageId,mode,status,host,client,error}}]
//	stopFromPanel(agent, pluginId)     -> {ok:true} | {ok:false,reason:'plugin-missing'|'not-running',message}
//	undefineFromPanel(agent, pluginId) -> {ok:true,wasRunning} | {ok:false,reason:'plugin-missing',message}
//
// 本宿主能给出的**真值**只有盘上与进程里那几样：pluginId（目录名）、
// package.json 的 name/version、host 入口与 client/client.js 是否存在、每次激活
// 铸的 run 号、以及 stop/undefine 之后的真实状态。所以逐字段是这么填的：
//
//   - `agentId`：官方在 define 时把 Plugin 绑到一个会话上；本宿主**没有 define
//     通道**（官方 rc.2 也不注册任何 cordis 模型工具：`dsh-cordis-host-runner/
//     README.md:119`「This package registers no tool or prompt」），归属只能来自
//     激活/面板调用里带的 `sessionId` 参数。没带过就是空串——**不编一个会话名**，
//     官方 UI 因此把这类行归到「其他会话」。
//   - `packages`：官方是 define 顺序的不可变版本表；本宿主一个插件目录只有一个
//     版本，所以数组恰好一项。`packageId` 由磁盘 package.json 的 `name@version`
//     派生（同 name@version 即同身份，不随加载次数漂移）。`purpose` 在盘上**没有
//     来源**，给空串（官方 UI 渲染成「(未填写用途)」）。
//   - `nextPackageId`：本宿主不做「失败/进行中的版本切换」，该字段**不出现**。
//   - `activeRun`：只有真实激活（外部 node 已加载、进程活着）才出现。
//   - `latestRun`：真实尝试记录（成功=running、加载失败=failed +
//     `error.phase=host-load`、stop 之后=stopped），host/client 两半按真实情况填；
//     没有 Client 半的包 `client.status=absent`。
//   - 4 个 cordis 事件里只有 2 个在本宿主有真实触发点：`cordis/dynamic-package`
//     （激活成功）与 `cordis/dynamic-retract`（stop/undefine 撤下）。
//     另 2 个（`cordis/request-run` / `cordis/request-run-resolved`）要求「待审批的
//     Client 半激活请求」，本宿主没有 Cordis 审批队列，**不发空帧/假帧**。

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

// cordisEntry 是宿主内部的一条真实记录（**不是**官方 wire 形状；wire 形状见
// cordisInventoryRow）。syncManifest 也读它，所以字段保留 id/version/kind 那套。
type cordisEntry struct {
	ID      string   `json:"id"`
	Version string   `json:"version"`
	Kind    string   `json:"kind"` // go | js
	State   string   `json:"state"`
	Dir     string   `json:"dir,omitempty"`
	Methods []string `json:"methods,omitempty"`
	PID     int      `json:"pid,omitempty"`

	// 官方 inventory 需要的真实值。
	Name          string `json:"name,omitempty"`           // package.json 的 name
	PackageID     string `json:"packageId,omitempty"`      // name@version 派生
	RunID         string `json:"pluginRunId,omitempty"`    // 每次激活铸的 run 号
	Owner         string `json:"ownerSessionId,omitempty"` // 激活它的会话；未知为空
	HasHostHalf   bool   `json:"hasHostHalf,omitempty"`
	HasClientHalf bool   `json:"hasClientHalf,omitempty"`
	RunStatus     string `json:"runStatus,omitempty"` // running|failed|stopped
	RunMode       string `json:"runMode,omitempty"`   // run|update
	ErrorPhase    string `json:"errorPhase,omitempty"`
	ErrorMessage  string `json:"errorMessage,omitempty"`

	// lastOKPackage 是上一次**成功**激活的包身份，只用来判 run/update。
	lastOKPackage string
}

// —— 官方 wire 形状（字段名逐一取自 typert.host.js，不自造） ——

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

func (s *cordisService) ensureJS(ctx context.Context, id, sessionID string) (*extern.Client, *cordisEntry, error) {
	s.mu.Lock()
	if c := s.clients[id]; c != nil && c.Alive() {
		e := s.entries[id]
		if e != nil && sessionID != "" && e.Owner == "" {
			e.Owner = sessionID
		}
		s.mu.Unlock()
		if e == nil {
			return nil, nil, fmt.Errorf("cordis: 插件 %q 无记录", id)
		}
		return c, e, nil
	}
	// 先按住一条占位记录，好让并发的失败尝试也落在同一个条目上。
	e := s.entries[id]
	if e == nil {
		e = &cordisEntry{ID: id, Kind: "js", State: "registered", Dir: s.pluginDir(id)}
		s.entries[id] = e
	}
	s.mu.Unlock()

	c, err := extern.Start(ctx, extern.Options{NodePath: s.nodePath})
	if err != nil {
		s.recordFailure(id, sessionID, err)
		return nil, nil, err
	}
	// 锁外不读共享条目的字段：目录恒是 pluginDir(id)，直接用。
	dir := s.pluginDir(id)
	info, err := c.LoadPackage(ctx, dir)
	if err != nil {
		_ = c.Close()
		s.recordFailure(id, sessionID, err)
		return nil, nil, err
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
	e.lastOKPackage = e.PackageID
	e.RunStatus = "running"
	owner := e.Owner
	pkgID, name := e.PackageID, e.Name
	s.clients[id] = c
	s.mu.Unlock()

	if owner != "" {
		ev := s.packageEvent(id, pkgID, runID, name)
		ev.sessionID = owner
		s.publish(ev)
	}
	return c, e, nil
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

func (s *cordisService) findClientFor(requestID string) (*extern.Client, *cordisEntry, bool) {
	s.mu.Lock()
	id, ok := s.runs[requestID]
	c := s.clients[id]
	e := s.entries[id]
	s.mu.Unlock()
	return c, e, ok && c != nil
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
	// 官方方法的第一个参数是 agent（会话身份）。本仓 wire 是自定义的，所以用
	// 命名参数 `sessionId` 承载它；没带就没有归属，事件也不发（见文件头）。
	session := str("sessionId")
	switch method {
	case string(transport.MethodDynamicCordisRunnerInventory):
		return s.inventory(), nil
	case string(transport.MethodDynamicCordisRunnerGetClientCode):
		id := str("pluginId")
		if id == "" {
			return nil, errors.New("pluginId 必填")
		}
		c, _, err := s.ensureJS(ctx, id, session)
		if err != nil {
			return nil, err
		}
		file := str("file")
		if file == "" {
			file = "client/client.js"
		}
		code, err := c.GetClientCode(ctx, s.pluginDir(id), file)
		if err != nil {
			return nil, err
		}
		return map[string]any{"pluginId": id, "file": file, "code": string(code), "bytes": len(code)}, nil
	case string(transport.MethodDynamicCordisRunnerInvoke):
		id, reqID := str("pluginId"), str("requestId")
		if id == "" || str("method") == "" {
			return nil, errors.New("pluginId/method 必填")
		}
		c, _, err := s.ensureJS(ctx, id, session)
		if err != nil {
			return nil, err
		}
		var args any
		if rawArgs := p["args"]; len(rawArgs) > 0 {
			_ = json.Unmarshal(rawArgs, &args)
		}
		var out any
		// 官方 cordis.hostRunner.vmTimeoutMs（默认 5000ms）：脚本执行不能靠上游 ctx
		// 兜底，否则一个死循环的插件会把这次 HTTP 请求按到永远。
		vmCtx, cancel := withCordisBudget(ctx, cordisVMTimeout())
		defer cancel()
		rec, err := c.Invoke(vmCtx, reqID, str("method"), args, &out)
		s.mu.Lock()
		s.runs[reqID] = id
		s.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return map[string]any{"result": out, "run": rec}, nil
	case string(transport.MethodDynamicCordisRunnerRunHostHalf):
		id := str("pluginId")
		if id == "" {
			return nil, errors.New("pluginId 必填")
		}
		_, e, err := s.ensureJS(ctx, id, session)
		if err != nil {
			return nil, err
		}
		// 返回快照：调用方稍后才会 marshal，而这张记录还会被别的请求改。
		snap := *e
		return &snap, nil
	case string(transport.MethodDynamicCordisRunnerSettleUserRun):
		reqID := str("requestId")
		c, e, ok := s.findClientFor(reqID)
		if !ok {
			return nil, &transport.CordisAPIError{Status: 404, Code: "not_found", Message: "requestId 没有运行记录"}
		}
		rec, err := c.SettleUserRun(ctx, reqID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"requestId": reqID, "pluginId": e.ID, "run": rec}, nil
	case string(transport.MethodDynamicCordisRunnerResolveRequestRun):
		reqID := str("requestId")
		c, _, ok := s.findClientFor(reqID)
		if !ok {
			return nil, &transport.CordisAPIError{Status: 404, Code: "not_found", Message: "requestId 没有运行记录"}
		}
		rec, found := c.ResolveRequestRun(reqID)
		if !found {
			return nil, &transport.CordisAPIError{Status: 404, Code: "not_found", Message: "requestId 没有运行记录"}
		}
		return rec, nil
	case string(transport.MethodDynamicCordisRunnerResolveInspectQuery):
		id := str("pluginId")
		if id == "" {
			return s.inventory(), nil
		}
		c, _, err := s.ensureJS(ctx, id, session)
		if err != nil {
			return nil, err
		}
		// 官方 cordis.hostRunner.clientInspectTimeoutMs（默认 10000ms）。
		inspectCtx, cancel := withCordisBudget(ctx, cordisClientInspectTimeout())
		defer cancel()
		info, err := c.ResolveInspectQuery(inspectCtx)
		if err != nil {
			return nil, err
		}
		return info, nil
	case string(transport.MethodDynamicCordisRunnerSyncInspectManifest):
		return s.syncManifest()
	case string(transport.MethodDynamicCordisRunnerReportClientGuardFailure),
		string(transport.MethodDynamicCordisRunnerReportRenderFailure):
		return s.report(method, str("pluginId"), str("failure"), raw)
	case string(transport.MethodDynamicCordisRunnerStopFromPanel):
		return s.stopFromPanel(str("pluginId"), session)
	case string(transport.MethodDynamicCordisRunnerUndefineFromPanel):
		return s.undefineFromPanel(str("pluginId"), session)
	default:
		return nil, fmt.Errorf("不是 dynamicCordisRunner 方法: %s", method)
	}
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
	wasRunning := (c != nil && c.Alive()) || e.RunStatus == "running"
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
		if e.RunStatus == "failed" && e.ErrorMessage != "" {
			row.LatestRun.Error = &cordisRunDiagnostic{
				Phase:       cordisErrorPhaseOrHostLoad(e.ErrorPhase),
				Message:     e.ErrorMessage,
				PluginID:    e.ID,
				PackageID:   e.PackageID,
				PluginRunID: e.RunID,
			}
		}
	}
	if e.RunStatus == "running" && e.RunID != "" && e.PackageID != "" {
		row.ActiveRun = &cordisRunRef{PluginRunID: e.RunID, PackageID: e.PackageID}
	}
	return row
}

func cordisModeOrRun(mode string) string {
	if mode == "update" {
		return "update"
	}
	return "run"
}

// cordisAttemptStatus 映射到官方 DynamicCordisRunStatus 词表。
func cordisAttemptStatus(status string) string {
	switch status {
	case "failed":
		return "failed"
	case "stopped":
		return "stopped"
	case "running":
		return "running"
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
	case e.RunStatus == "failed":
		return cordisHalfState{Status: "failed", WaitingFor: []string{}, Error: e.ErrorMessage}
	case e.RunStatus == "running":
		return cordisHalfState{Status: "running", WaitingFor: []string{}}
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
	if e.RunStatus == "running" {
		return cordisHalfState{Status: "pending", WaitingFor: []string{}}
	}
	return cordisHalfState{Status: "stopped", WaitingFor: []string{}}
}

// entries 是内部视图（syncManifest 用），不是官方 wire 形状。
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

func (s *cordisService) report(method, pluginID, failure string, raw json.RawMessage) (map[string]any, error) {
	dir := filepath.Join(s.home, "state", "cordis")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	ev := map[string]any{
		"id":     fmt.Sprintf("%s-%d", method, time.Now().UnixNano()),
		"method": method, "pluginId": pluginID, "failure": failure,
		"at": time.Now().UTC().Format(time.RFC3339Nano), "payload": json.RawMessage(raw),
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return nil, err
	}
	return map[string]any{"eventId": ev["id"], "recorded": true, "path": filepath.ToSlash(filepath.Join(dir, "events.jsonl"))}, nil
}

// 编译期证明实现满足 transport 接口；也帮助 C3 的路由接线。
var _ transport.OfficialCordisAPI = (*cordisService)(nil)

func (s *cordisService) String() string { return strings.TrimSpace(filepath.Base(s.home)) }
