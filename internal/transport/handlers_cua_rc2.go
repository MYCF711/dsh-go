package transport

// RC2-C1：官方 0.2.2-rc.2 的电脑操作（CUA）面——9 个 `cuaAccess.*` 方法。
//
// # 契约（唯一权威）
//
// `docs/evidence/official-method-contract-delta-0.2.2-rc.2.md:311-814`，逐条：
//
//	cuaAccess/systemStatus         ()                              → darwin|win32 权限观测
//	cuaAccess/applications         ()                              → {allowedApps, blockedApps, persistentAvailable}
//	cuaAccess/applicationIcons     ()                              → {可执行路径: data:image/png;base64,…}
//	cuaAccess/openSettings         (permission, locale)             → void
//	cuaAccess/ptcConfiguration     ()                              → {persistent, writable}
//	cuaAccess/enablePersistentPtc  ()                              → void
//	cuaAccess/requestPermission    (permission, locale)             → darwin|win32 权限观测
//	cuaAccess/retryApproval        (id)                            → void
//	cuaAccess/revoke               (id)                            → void
//
// # 官方事实（决定了本文件为什么这么分级）
//
// 官方源码 `dsh-cua/src/access.ts:39-142` 是这 9 个方法的唯一实现，它把
// **决策**交给 `ApplicationAccess`（`application-access.ts:41`）、把**原生动作**
// 交给 provider 注入的 `control`（`access.ts:21`、`:155-170`）。而 provider 只在
// `installCua()` 里装配（`index.ts:153-177`），`apply()` 是空函数（`index.ts:415`）。
// rc.2 的 bundle 只挂 macOS provider（`dsh-cua-bundle/cordis.patch.yml:5-8`
// `disabled: !!js process.platform !== 'darwin'`），Windows 安装里那个包
// **根本不存在**——**所以官方 Windows 宿主上这 9 个方法一个都不存在**。
//
// 本仓的分级（任务书要求：能真做的真做，缺 provider 的结构化未实现）：
//
//	真实现（不依赖原生 provider）  systemStatus / applications / applicationIcons
//	                             + ptcConfiguration / enablePersistentPtc
//	                             + revoke / retryApproval / openSettings（Windows 设置页）
//	结构化未实现 + capableWhen     requestPermission（原生同意 UI 不存在）
//	                             + 非 Windows 构建上的 systemStatus
//
// 注意：`applications` 在官方是**读已保存的决策**，不是枚举运行中进程
// （`access.ts:75-80` → `application-access.ts:65-81`）。枚举在官方属于 provider
// 的 native 协议（`protocol.ts:52-53` 的 listApps/findApps），不在 9 方法面内；
// 本仓把 Windows 上真能做的枚举放在 **additive** 方法
// `cuaAccess/runningApplications`（见 `AdditiveCUAMethods`），不改官方语义。
//
// # 与官方 wire 的显式差异
//
//   - void 结果用 `{}` 而不是 `null`：本仓出口不接受 `{"result":null}`（会被当 500），
//     与 `agentTeams/setEnabled` 同一口径。
//   - 501 的错误体多两个兄弟字段：`capableWhen`（补救方式）与 `capability`
//     （平台/提供者事实），不改 error 的四字段契约。
//   - 成功结果里可能多出契约 `additionalProperties: true` 允许的**附加真话**
//     （如 `providerAvailable`/`capableWhen`），必需字段一个不少。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"dshgo/internal/cua"
)

// 官方方法名。`/` → `.`，与既有 58 个方法同形；字符串必须与官方 typert 描述符
// 逐字对应，改了就是破坏契约。
const (
	// MethodCUASystemStatus 是官方 `cuaAccess/systemStatus`（契约 :666）。
	MethodCUASystemStatus Method = "cuaAccess.systemStatus"
	// MethodCUAApplications 是官方 `cuaAccess/applications`（契约 :329）。
	MethodCUAApplications Method = "cuaAccess.applications"
	// MethodCUAApplicationIcons 是官方 `cuaAccess/applicationIcons`（契约 :311）。
	MethodCUAApplicationIcons Method = "cuaAccess.applicationIcons"
	// MethodCUAOpenSettings 是官方 `cuaAccess/openSettings`（契约 :423）。
	MethodCUAOpenSettings Method = "cuaAccess.openSettings"
	// MethodCUAPTCConfiguration 是官方 `cuaAccess/ptcConfiguration`（契约 :445）。
	MethodCUAPTCConfiguration Method = "cuaAccess.ptcConfiguration"
	// MethodCUAEnablePersistentPTC 是官方 `cuaAccess/enablePersistentPtc`（契约 :405）。
	MethodCUAEnablePersistentPTC Method = "cuaAccess.enablePersistentPtc"
	// MethodCUARequestPermission 是官方 `cuaAccess/requestPermission`（契约 :472）。
	MethodCUARequestPermission Method = "cuaAccess.requestPermission"
	// MethodCUARetryApproval 是官方 `cuaAccess/retryApproval`（契约 :626）。
	MethodCUARetryApproval Method = "cuaAccess.retryApproval"
	// MethodCUARevoke 是官方 `cuaAccess/revoke`（契约 :646）。
	MethodCUARevoke Method = "cuaAccess.revoke"

	// MethodCUARunningApplications 是本仓 **additive** 方法，**不在官方 9 方法内**：
	// 官方把"枚举运行中应用"放在 provider 的 native 协议（`protocol.ts:52-53`），
	// 方法面只有已保存的决策；本仓在 Windows 上用 EnumWindows 真能枚举，
	// 所以单独给一个方法，而不是把 `applications` 的官方语义改掉。
	MethodCUARunningApplications Method = "cuaAccess.runningApplications"
)

// OfficialCUARc2Methods 按官方描述符顺序返回 9 个方法（给快照/文档/门禁用）。
func OfficialCUARc2Methods() []Method {
	return []Method{
		MethodCUAApplicationIcons, MethodCUAApplications, MethodCUAEnablePersistentPTC,
		MethodCUAOpenSettings, MethodCUAPTCConfiguration, MethodCUARequestPermission,
		MethodCUARetryApproval, MethodCUARevoke, MethodCUASystemStatus,
	}
}

// AdditiveCUAMethods 是本仓在官方 9 方法之外新增的方法；绝不混进
// OfficialCUARc2Methods（否则 9 个的计数与快照就错了）。
func AdditiveCUAMethods() []Method {
	return []Method{MethodCUARunningApplications}
}

// IsOfficialCUARc2Method 判断 /api 路径名是否属于 cuaAccess 面（含 additive 方法）。
func IsOfficialCUARc2Method(name string) bool {
	switch Method(name) {
	case MethodCUASystemStatus, MethodCUAApplications, MethodCUAApplicationIcons,
		MethodCUAOpenSettings, MethodCUAPTCConfiguration, MethodCUAEnablePersistentPTC,
		MethodCUARequestPermission, MethodCUARetryApproval, MethodCUARevoke,
		MethodCUARunningApplications:
		return true
	}
	return false
}

// OfficialCUARc2API 是 cuaAccess 面的宿主实现。
//
// 为什么是"函数字段"而不是接口：与 `OfficialAgentTeamRC2API` 同一模式，
// 测试可以只填自己关心的那一个、其余留 nil（nil = 未接线，回 501 并说清）。
type OfficialCUARc2API struct {
	// SystemStatus 返回平台标记的权限观测（win32 分支见 cua.Win32SystemStatus）。
	SystemStatus func(ctx context.Context) (any, error)
	// Applications 读已保存的应用决策（不发原生请求）。
	Applications func(ctx context.Context) (cua.ApplicationState, error)
	// ApplicationIcons 取已保存决策涉及的应用图标。
	ApplicationIcons func(ctx context.Context) (map[string]string, error)
	// OpenSettings 打开权限设置页；成功返回 nil。
	OpenSettings func(ctx context.Context, permission, locale string) error
	// PTCConfiguration 读 PTC 执行状态。
	PTCConfiguration func(ctx context.Context) (cua.PTCConfiguration, error)
	// EnablePersistentPTC 把内存态 PTC 改为持久化；已持久化时 no-op。
	EnablePersistentPTC func(ctx context.Context) error
	// RequestPermission 请求系统权限并返回当前观测（请求不建立同意）。
	RequestPermission func(ctx context.Context, permission, locale string) (any, error)
	// RetryApproval 清掉这个应用在所有会话里的拒绝（deny → ask）。
	RetryApproval func(ctx context.Context, id string) error
	// Revoke 撤销这个应用的已保存与会话许可。
	Revoke func(ctx context.Context, id string) error
	// RunningApplications 是 additive 能力（官方 9 方法外）：枚举运行中应用。
	RunningApplications func(ctx context.Context) ([]cua.RunningApp, error)
}

// NewOfficialCUARc2APIFromService 把 `*cua.Service` 适配成本包要的函数表。
//
// 生产接线只需要这一行（见报告"接线行"）：
//
//	srv.AttachOfficialCUARc2API(transport.NewOfficialCUARc2APIFromService(
//	    cua.NewService(cua.Options{HomeDir: *home})))
func NewOfficialCUARc2APIFromService(service *cua.Service) OfficialCUARc2API {
	if service == nil {
		return OfficialCUARc2API{}
	}
	return OfficialCUARc2API{
		SystemStatus: func(ctx context.Context) (any, error) {
			return service.SystemStatus(ctx)
		},
		Applications: func(ctx context.Context) (cua.ApplicationState, error) {
			return service.Applications(ctx)
		},
		ApplicationIcons: func(ctx context.Context) (map[string]string, error) {
			return service.ApplicationIcons(ctx)
		},
		OpenSettings:        service.OpenSettings,
		PTCConfiguration:    service.PTCConfiguration,
		EnablePersistentPTC: service.EnablePersistentPTC,
		RequestPermission: func(ctx context.Context, permission, locale string) (any, error) {
			return service.RequestPermission(ctx, permission, locale)
		},
		RetryApproval:       service.RetryApproval,
		Revoke:              service.Revoke,
		RunningApplications: service.RunningApplications,
	}
}

// officialCUARc2Mount 把一份实现挂到一个 Server 上（按 *Server 索引，不用 Options：
// Options 在 server.go，本任务的写作用域之外）。
var officialCUARc2Mount = struct {
	mu sync.RWMutex
	m  map[*Server]OfficialCUARc2API
}{m: map[*Server]OfficialCUARc2API{}}

// AttachOfficialCUARc2API 给这个 Server 挂上真实实现（装配阶段调用）。
func (s *Server) AttachOfficialCUARc2API(api OfficialCUARc2API) {
	if s == nil {
		return
	}
	officialCUARc2Mount.mu.Lock()
	defer officialCUARc2Mount.mu.Unlock()
	officialCUARc2Mount.m[s] = api
}

// DetachOfficialCUARc2API 摘掉实现（测试收尾用）。
func (s *Server) DetachOfficialCUARc2API() {
	if s == nil {
		return
	}
	officialCUARc2Mount.mu.Lock()
	defer officialCUARc2Mount.mu.Unlock()
	delete(officialCUARc2Mount.m, s)
}

// officialCUARc2API 取这个 Server 的实现。
//
// **没接线时不回 501，而是现造一个真实的 `cua.Service`（进程内存储 + 平台真实观测）**：
// 任务是"能真做的真做"，把"没接线"当成默认 501 就是把能做的项也糊弄掉了。
// 这时唯一的差别是 `persistentAvailable=false`——那是**真话**（没给状态目录）。
func (s *Server) officialCUARc2API() OfficialCUARc2API {
	if s == nil {
		return OfficialCUARc2API{}
	}
	officialCUARc2Mount.mu.RLock()
	api, ok := officialCUARc2Mount.m[s]
	officialCUARc2Mount.mu.RUnlock()
	if ok {
		return api
	}
	// 一个 Server 只造一次：每次请求新建 Service 会让进程内决策存储每次从空开始，
	// 于是 revoke/retryApproval 变成"改了但下一次看不见"的假动作。
	officialCUARc2Mount.mu.Lock()
	defer officialCUARc2Mount.mu.Unlock()
	if api, ok = officialCUARc2Mount.m[s]; ok {
		return api
	}
	api = NewOfficialCUARc2APIFromService(cua.NewService(cua.Options{}))
	officialCUARc2Mount.m[s] = api
	return api
}

// ServeOfficialCUARc2API 处理 cuaAccess 面的 10 个方法（9 官方 + 1 additive）。
//
// 调用方（server.go 的 /api 路由）只需要：
//
//	if transport.IsOfficialCUARc2Method(name) {
//	    srv.ServeOfficialCUARc2API(w, r, name)
//	    return
//	}
//
// 认证与 POST 校验已经在 handleAPI 里做过。
func (s *Server) ServeOfficialCUARc2API(w http.ResponseWriter, r *http.Request, name string) {
	api := s.officialCUARc2API()
	params, err := decodeCUAParams(r)
	if err != nil {
		writeCUARc2Error(w, err)
		return
	}
	result, err := routeCUARc2(api, Method(name), r.Context(), params)
	if err != nil {
		writeCUARc2Error(w, err)
		return
	}
	if result == nil {
		// 官方 void 结果：本仓用空对象表达（出口不接受 result:null）。
		result = map[string]any{}
	}
	writeJSON(w, http.StatusOK, result)
}

// routeCUARc2 把方法名映射到实现，并做**严格**参数校验。
func routeCUARc2(api OfficialCUARc2API, method Method, ctx context.Context, p *officialParams) (any, error) {
	switch method {
	case MethodCUASystemStatus:
		if err := p.finish(); err != nil {
			return nil, err
		}
		if api.SystemStatus == nil {
			return nil, cuaMethodUnwired(method)
		}
		return api.SystemStatus(ctx)

	case MethodCUAApplications:
		if err := p.finish(); err != nil {
			return nil, err
		}
		if api.Applications == nil {
			return nil, cuaMethodUnwired(method)
		}
		state, err := api.Applications(ctx)
		if err != nil {
			return nil, err
		}
		return normalizeApplicationState(state), nil

	case MethodCUAApplicationIcons:
		if err := p.finish(); err != nil {
			return nil, err
		}
		if api.ApplicationIcons == nil {
			return nil, cuaMethodUnwired(method)
		}
		icons, err := api.ApplicationIcons(ctx)
		if err != nil {
			return nil, err
		}
		if icons == nil {
			// 官方结果要求是对象；nil map 会序列化成 null，客户端读不到"没有图标"。
			icons = map[string]string{}
		}
		for path, icon := range icons {
			if len(icon) > cua.MaxApplicationIconChars {
				return nil, fmt.Errorf("图标超长（%s：%d > %d）", path, len(icon), cua.MaxApplicationIconChars)
			}
		}
		return icons, nil

	case MethodCUAOpenSettings:
		permission, locale, err := cuaPermissionParams(p)
		if err != nil {
			return nil, err
		}
		if api.OpenSettings == nil {
			return nil, cuaMethodUnwired(method)
		}
		return nil, api.OpenSettings(ctx, permission, locale)

	case MethodCUAPTCConfiguration:
		if err := p.finish(); err != nil {
			return nil, err
		}
		if api.PTCConfiguration == nil {
			return nil, cuaMethodUnwired(method)
		}
		return api.PTCConfiguration(ctx)

	case MethodCUAEnablePersistentPTC:
		if err := p.finish(); err != nil {
			return nil, err
		}
		if api.EnablePersistentPTC == nil {
			return nil, cuaMethodUnwired(method)
		}
		return nil, api.EnablePersistentPTC(ctx)

	case MethodCUARequestPermission:
		permission, locale, err := cuaPermissionParams(p)
		if err != nil {
			return nil, err
		}
		if api.RequestPermission == nil {
			return nil, cuaMethodUnwired(method)
		}
		return api.RequestPermission(ctx, permission, locale)

	case MethodCUARetryApproval:
		id, err := cuaApplicationIDParam(p)
		if err != nil {
			return nil, err
		}
		if api.RetryApproval == nil {
			return nil, cuaMethodUnwired(method)
		}
		return nil, api.RetryApproval(ctx, id)

	case MethodCUARevoke:
		id, err := cuaApplicationIDParam(p)
		if err != nil {
			return nil, err
		}
		if api.Revoke == nil {
			return nil, cuaMethodUnwired(method)
		}
		return nil, api.Revoke(ctx, id)

	case MethodCUARunningApplications:
		if err := p.finish(); err != nil {
			return nil, err
		}
		if api.RunningApplications == nil {
			return nil, cuaMethodUnwired(method)
		}
		apps, err := api.RunningApplications(ctx)
		if err != nil {
			return nil, err
		}
		if apps == nil {
			apps = []cua.RunningApp{}
		}
		return apps, nil
	}
	return nil, officialBadRequest("未知的 cuaAccess 方法 %q", string(method))
}

// cuaMethodUnwired 是"这个 Server 上这一项没接线"的统一错误（不是平台没有能力）。
func cuaMethodUnwired(method Method) error {
	return &cua.Error{
		Status: http.StatusNotImplemented, Code: "not_implemented",
		Message: fmt.Sprintf("%s 未实现：宿主没有给这个 Server 挂上该能力（AttachOfficialCUARc2API 少了这一项）", method),
	}
}

// normalizeApplicationState 保证两个集合是 `[]` 而不是 null。
//
// 官方 result 里 allowedApps/blockedApps 是 required 的数组；序列化成 null 时
// 客户端要么崩要么当异常处理——"没有授权"与"读不到"必须能分开。
func normalizeApplicationState(state cua.ApplicationState) cua.ApplicationState {
	if state.AllowedApps == nil {
		state.AllowedApps = []cua.Grant{}
	}
	if state.BlockedApps == nil {
		state.BlockedApps = []cua.Grant{}
	}
	return state
}

// cuaPermissionParams 解析官方 [permission, locale] 两个位置参数。
//
// 两个都必填（官方 `z.string()` 与 `z.string().enum(...)`，都不是可选）；
// `permission` 先做枚举校验，错误里给出两个合法值——界面上是单选按钮，传错就是 bug。
func cuaPermissionParams(p *officialParams) (permission, locale string, err error) {
	permission, err = p.str("permission")
	if err != nil {
		return "", "", err
	}
	switch permission {
	case "accessibility", "screenRecording":
	default:
		return "", "", officialBadRequest(
			"permission 只能是 accessibility / screenRecording（官方 CuaSystemPermission，收到 %q）", permission)
	}
	locale, err = p.str("locale")
	if err != nil {
		return "", "", err
	}
	if err := p.finish(); err != nil {
		return "", "", err
	}
	return permission, locale, nil
}

// cuaApplicationIDParam 解析官方 [id]，并**在 transport 层**校验官方身份摘要形状。
//
// 两层都校验（这里 + `cua.validateApplicationID`）不是冗余：这里保证
// `/api` 的 400 语义正确，`cua` 包保证任何调用方（含工具路径）都过同一道闸。
func cuaApplicationIDParam(p *officialParams) (string, error) {
	// 官方参数名是 `id`；本仓既有方法也有用 `applicationId` 的，两种都认。
	id, which, err := p.strAlias("id", "applicationId")
	if err != nil {
		return "", err
	}
	if err := p.finish(); err != nil {
		return "", err
	}
	_ = which
	if !cua.ApplicationIDPattern.MatchString(id) {
		return "", officialBadRequest(
			"id 必须是官方 CuaApplicationId（64 位小写十六进制原生身份摘要，收到 %q）", id)
	}
	return id, nil
}

// decodeCUAParams 读请求体。
//
// 与 `decodeOfficialParams` 的唯一差别：**空请求体按 `{}` 处理**。这 9 个方法里
// 5 个没有参数，官方客户端在没参数时不发正文是常态；把"没发正文"判成 400
// 只会让无参方法每次都被拒。
func decodeCUAParams(r *http.Request) (*officialParams, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, officialBadRequest("读请求体失败: %v", err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		raw = []byte("{}")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, officialBadRequest("请求体不是 JSON 对象: %v", err)
	}
	if m == nil {
		return nil, officialBadRequest("请求体是 null：这些方法要一个 JSON 对象")
	}
	return &officialParams{m: m, used: map[string]bool{}}, nil
}

// writeCUARc2Error 是 cuaAccess 面的唯一错误出口。
//
// `*cua.Error` 自带 HTTP 状态与码；`capableWhen` 作为 error 的兄弟字段出现
// （说清"装什么/配什么就能用"），绝不污染 error 的四字段契约。
func writeCUARc2Error(w http.ResponseWriter, err error) {
	var typed *cua.Error
	if errors.As(err, &typed) {
		status := typed.Status
		if status == 0 {
			status = http.StatusInternalServerError
		}
		code := typed.Code
		if code == "" {
			code = "internal"
		}
		extras := map[string]any{}
		if typed.CapableWhen != "" {
			extras["capableWhen"] = typed.CapableWhen
		}
		if len(extras) > 0 {
			writeJSON(w, status, errorBodyFor(code, typed.Message, extras))
			return
		}
		writeJSON(w, status, errorBodyFor(code, typed.Message))
		return
	}
	// 其余错误交给官方方法的统一出口（bad_request / not_implemented / internal）。
	writeOfficialError(w, err)
}
