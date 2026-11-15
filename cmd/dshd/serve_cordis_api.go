package main

// PARITY-CORDIS-C3B / HOST-CORDIS-SHAPE: dynamicCordisRunner 12 方法的真实宿主实现。
//
// 数据来源只有两处：
//   - Go 核心：internal/cordis（registry/manifest/生命周期）
//   - 外部受管 node：internal/cordis/extern（真实插件包加载/调用/结算）
// 没有 501、没有静态成功；无 node 时返回结构化不可用 + Hint。
//
// # 官方形状（本文件这 5 个方法的结果字段逐一取自官方 typert 契约，不自造字段）
//
// 官方源：`tmp/GO8-RC2/official-rc2/dsh/node_modules/@deepseek-ai/
// dsh-cordis-host-runner/lib/typert.host.js`
//
//	getClientCode(agent, pluginId, pluginRunId)
//	    -> {code,name,pluginId,packageId,pluginRunId}                                  (:13-19)
//	runHostHalf(agent, pluginId, packageId, mode, requestId, approveFutureVersions)     (:146-170)
//	    -> {ok:true,pluginId,packageId,pluginRunId,waitingFor,startedHere}
//	     | {ok:false,message,stack?}
//	invoke(pluginId, pluginRunId, method, args)                                        (:64-81)
//	    -> {ok:true,value}
//	     | {ok:false,code:'plugin-not-running'|'stale-run'|'method-not-found'|'handler-error',
//	        message,stack?}
//	settleUserRun(agent, pluginId, resolution)                                         (:171-205)
//	    -> {ok:true,status,pluginId,packageId,pluginRunId,waitingFor,clientWaitingFor?,
//	        currentPackageId?,nextPackageId?,mode}
//	     | {ok:false,reason,message,stack?}
//	resolveRequestRun(requestId, resolution) -> {accepted}                             (:127-145)
//	reportClientGuardFailure / reportRenderFailure -> null                             (:82-109)
//
// # 每个字段的真值从哪来
//
//   - `name` / `packageId`：盘上 `cordis-plugins/<id>/package.json` 的 `name` 与
//     `name@version`。本宿主**没有**官方的 define 通道，官方那个不透明 `packageId`
//     也就不存在；派生值满足官方两条硬要求（同版本恒等、不同版本必不同）。
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
//   - 首参 `agent`（wire 名 `agentId`，也认 `sessionId`）：这 5 个方法**不消费**它 ——
//     本宿主没有 define 通道（没有 Plugin↔Session 归属表），也没有把结果 steer 回
//     会话的通道，而且这 5 个结果里本来就没有 agentId 字段。传了就丢，不编归属。
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

type cordisEntry struct {
	ID      string   `json:"id"`
	Version string   `json:"version"`
	Kind    string   `json:"kind"` // go | js
	State   string   `json:"state"`
	Dir     string   `json:"dir,omitempty"`
	Methods []string `json:"methods,omitempty"`
	PID     int      `json:"pid,omitempty"`

	// 官方 5 个方法要的身份/状态值。inventory 行是**另一层**的形状（本分支里它就是
	// 这条结构体本身的 JSON），所以这里全部 json:"-"：加这几个字段**不改变**
	// inventory/syncManifest 既有的输出，只在 5 个方法的回执里用。
	Name          string `json:"-"` // package.json 的 name
	PackageID     string `json:"-"` // 由 name@version 派生（见 cordisPackageID）
	RunID         string `json:"-"` // 每次激活铸的 pluginRunId
	HasHostHalf   bool   `json:"-"`
	HasClientHalf bool   `json:"-"`
	RunStatus     string `json:"-"` // running|client-pending|failed|rejected|stopped
	RunMode       string `json:"-"` // run|update
	ErrorPhase    string `json:"-"` // approval|host-load|host-apply|client-load|client-apply
	ErrorMessage  string `json:"-"`

	// lastOKPackage 是上一次**成功**激活过的包身份，只用来判 run/update。
	lastOKPackage string
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
	nodePath string
	nextRun  int
}

func newCordisService(home string) *cordisService {
	return &cordisService{
		home: home, reg: cordis.NewRegistry(nil),
		entries: map[string]*cordisEntry{}, clients: map[string]*extern.Client{},
	}
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

// cordisPackageID 由盘上真实 package.json 的 name@version 派生包身份。
//
// 官方 packageId 是 define 时铸的不透明 id；本宿主没有 define 通道，也就没有那
// 份历史。派生值至少满足官方两条硬要求：**同一版本恒等**、**不同版本必不同**
// （官方 UI 拿它比 currentPackageId/packageId，派生值稳定才能对上）。
func cordisPackageID(name, version string) string {
	if name == "" && version == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(name + "@" + version))
	return "pkg-" + hex.EncodeToString(sum[:8])
}

// fillMetaFromDisk 用盘上真实存在的 package.json / 入口文件补元数据。
//
// 从未激活过的插件也要能答出身份，所以这条路径不启动 node：它只读文件。读不出
// package.json 就保持空缺，**不猜值**。
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
func (s *cordisService) planActivationLocked(e *cordisEntry) (mode, runID string) {
	s.nextRun++
	runID = fmt.Sprintf("run-%d", s.nextRun)
	mode = "run"
	if e.lastOKPackage != "" && e.PackageID != "" && e.lastOKPackage != e.PackageID {
		mode = "update"
	}
	e.RunID = runID
	e.RunMode = mode
	return mode, runID
}

// registeredLocked 取一条记录；盘上有 `cordis-plugins/<id>/package.json` 但内存
// 里还没有记录时补一条 registered（只读文件，不启动 node）。这样"插件存在但从没
// 跑过"能如实答身份，也能被判成 not-running，而不是被当成"不存在"。
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

// recordFailure 把一次真实加载失败记成 failed 尝试（官方 latestRun 的 error 来源）。
func (s *cordisService) recordFailure(id string, cause error) {
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
func (s *cordisService) ensureJS(ctx context.Context, id string) (c *extern.Client, e *cordisEntry, started bool, err error) {
	s.mu.Lock()
	if live := s.clients[id]; live != nil && live.Alive() {
		e = s.entries[id]
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
	proc, err := extern.Start(ctx, extern.Options{NodePath: s.nodePath})
	if err != nil {
		s.recordFailure(id, err)
		return nil, nil, false, cordisUnavailable(err)
	}
	info, loadErr := proc.LoadPackage(ctx, dir)
	if loadErr != nil {
		_ = proc.Close()
		s.recordFailure(id, loadErr)
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
	s.planActivationLocked(e)
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
	s.clients[id] = proc
	s.mu.Unlock()
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
		if _, err := client.Invoke(vmCtx, reqID, handler, args, &out); err != nil {
			return cordisInvokeFailureOf(err), nil
		}
		return cordisInvokeValue{OK: true, Value: out}, nil
	case string(transport.MethodDynamicCordisRunnerRunHostHalf):
		return s.runHostHalf(ctx, p, str)
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
		client, _, _, err := s.ensureJS(ctx, id)
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
		id := str("pluginId")
		s.mu.Lock()
		c := s.clients[id]
		e := s.entries[id]
		delete(s.clients, id)
		if e != nil {
			e.State = "stopped"
			e.PID = 0
			e.RunStatus = "stopped"
		}
		s.mu.Unlock()
		if c != nil {
			_ = c.Close()
		}
		if e == nil {
			return nil, &transport.CordisAPIError{Status: 404, Code: "not_found", Message: "pluginId 不存在"}
		}
		return e, nil
	case string(transport.MethodDynamicCordisRunnerUndefineFromPanel):
		id := str("pluginId")
		s.mu.Lock()
		c := s.clients[id]
		_, existed := s.entries[id]
		delete(s.clients, id)
		delete(s.entries, id)
		s.mu.Unlock()
		if c != nil {
			_ = c.Close()
		}
		if !existed {
			return nil, &transport.CordisAPIError{Status: 404, Code: "not_found", Message: "pluginId 不存在"}
		}
		return map[string]any{"pluginId": id, "undefined": true}, nil
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
func (s *cordisService) runHostHalf(ctx context.Context, p map[string]json.RawMessage, str func(string) string) (any, error) {
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

	_, _, started, err := s.ensureJS(ctx, id)
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
		return res, true, fmt.Errorf("resolution 必须是对象: %w", err)
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

func (s *cordisService) inventory() []*cordisEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*cordisEntry, 0, len(s.entries))
	for _, e := range s.entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *cordisService) syncManifest() (map[string]any, error) {
	entries := s.inventory()
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
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
