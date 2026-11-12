package transport

// 官方 0.2.2 桌面端 liveshareShares.* 全部 13 个方法的 **L1 契约层**
// （rc.1 的 12 个 + rc.2 新增的 addSessions）。
//
// # 契约来源（只读镜像，未改安装目录）
//
//	rc.1（对照基线）%TEMP%/official-api-src/dsh/node_modules/@deepseek-ai/dsh-experimental-liveshare/lib/typert.host.js
//	rc.2（当前基线）tmp/GO8-RC2/official-rc2/dsh/node_modules/@deepseek-ai/dsh-experimental-liveshare/src/host/share-remote.ts
//	契约机器件     tmp/GO8-RC2/contracts-rc2.json → liveshareShares/*
//
// 13 个方法（官方 `service: 'liveshareShares'`，每个都是 `invocation: direct`）：
//
//	create            typert.host.js:279 / share-remote.ts:56  CreateShareRequest → CreatedShare
//	addSessions       share-remote.ts:66-68（契约 sourceLocation line 67）  AddShareSessionsRequest → ShareSessions
//	list              typert.host.js:291 / share-remote.ts:74  () → OwnerShareList
//	stop              typert.host.js:437 / share-remote.ts:80  StopShareRequest → StopShareResult
//	updatePermissions typert.host.js:516 / share-remote.ts:87  UpdateSharePermissionsRequest → SharePermissions
//	setCapabilities   typert.host.js:373 / share-remote.ts:94  SetShareCapabilitiesRequest → ShareCapabilities
//	capabilities      typert.host.js:299 / share-remote.ts:101 () → ShareCapabilityOffers
//	relay             typert.host.js:391 / share-remote.ts:108 () → OwnerRelayState
//	setRelay          typert.host.js:405 / share-remote.ts:115 SetRelayRequest → OwnerRelayState
//	identity          typert.host.js:311 / share-remote.ts:122 OwnerIdentityRequest → OwnerIdentityStatus
//	audit             typert.host.js:232 / share-remote.ts:129 ShareAuditRequest → ShareAudit
//	testRelay         typert.host.js:483 / share-remote.ts:136 () → RelayConnectivityView
//	testPublish       typert.host.js:469 / share-remote.ts:143 TestPublishRequest → RelayPublishView
//
// 旧版 handlers_forge_liveshare_official.go 只把其中 8 个收进"结构化未实现"表，
// 另外 4 个（list/relay/capabilities/testRelay）落到 server.go 的通用 501——
// 这 4 个其实是官方同一个 host face 上的真方法。本文件把 12 个统一成
// "真实现或说清缺什么"，参数仍按官方 wire 名校验（`request` 对象）。
// rc.2 的 addSessions 见本文件 §addSessions。
//
// # 本仓的 L1↔L2 边界
//
//   - 本文件只定义契约与参数/结果校验，不 import 任何服务实现；
//   - 宿主通过 `Server.AttachOfficialLiveshareAPI` 注入 13 个回调；
//   - 未注入时每个方法都回 501 + 点名缺哪个回调（不返回空数组/假成功）；
//   - 结果的枚举/必填字段在这里再校验一次，防止宿主把假数据送上线。
//
// # 两条方法表（为什么不是一张）
//
// `OfficialLiveshareMethods()` 保持 rc.1 的 12 个（官方模型顺序），
// `OfficialLiveshareRC2Methods()` 返回 rc.2 新增的 addSessions。原因：
// `OfficialForgeLiveshareMethods()`（在 handlers_forge_liveshare_official.go）
// 的 31 = 19 forgePanel + 12 liveshareShares 被
// `handlers_forge_liveshare_official_test.go` 的计数与逐名清单钉住，
// 那个文件不在本任务的写作用域里。路由不受影响：`IsOfficialLiveshareMethod`
// 认 13 个，所以 `IsOfficialForgeLiveshareMethod` 会自动把它带进来。
// Lead 集成时把那张测试的 31→32、`want` 清单加一项即可合并成一张表。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// ---------------------------------------------------------------------------
// 13 个方法
// ---------------------------------------------------------------------------

const (
	MethodLiveshareSharesCreate            Method = "liveshareShares.create"
	MethodLiveshareSharesAddSessions       Method = "liveshareShares.addSessions"
	MethodLiveshareSharesList              Method = "liveshareShares.list"
	MethodLiveshareSharesStop              Method = "liveshareShares.stop"
	MethodLiveshareSharesUpdatePermissions Method = "liveshareShares.updatePermissions"
	MethodLiveshareSharesSetCapabilities   Method = "liveshareShares.setCapabilities"
	MethodLiveshareSharesCapabilities      Method = "liveshareShares.capabilities"
	MethodLiveshareSharesRelay             Method = "liveshareShares.relay"
	MethodLiveshareSharesSetRelay          Method = "liveshareShares.setRelay"
	MethodLiveshareSharesIdentity          Method = "liveshareShares.identity"
	MethodLiveshareSharesAudit             Method = "liveshareShares.audit"
	MethodLiveshareSharesTestRelay         Method = "liveshareShares.testRelay"
	MethodLiveshareSharesTestPublish       Method = "liveshareShares.testPublish"
)

// OfficialLiveshareMethods 返回官方 rc.1 liveshareShares face 的 12 个方法
// （官方模型顺序，rc.2 起 13 个：addSessions 紧跟 create）。
func OfficialLiveshareMethods() []Method {
	return []Method{
		MethodLiveshareSharesCreate,
		// rc.2 新增：把源会话加入已发布的 share（官方 share-remote.ts:56/66/74）。
		MethodLiveshareSharesAddSessions,
		MethodLiveshareSharesList,
		MethodLiveshareSharesStop,
		MethodLiveshareSharesUpdatePermissions,
		MethodLiveshareSharesSetCapabilities,
		MethodLiveshareSharesCapabilities,
		MethodLiveshareSharesRelay,
		MethodLiveshareSharesSetRelay,
		MethodLiveshareSharesIdentity,
		MethodLiveshareSharesAudit,
		MethodLiveshareSharesTestRelay,
		MethodLiveshareSharesTestPublish,
	}
}

// OfficialLiveshareRC2Methods 返回官方 rc.2 在 liveshareShares face 上新增的方法
// （12 → 13）。官方顺序里 addSessions 紧跟 create（share-remote.ts:56/66/74）。
func OfficialLiveshareRC2Methods() []Method {
	return []Method{MethodLiveshareSharesAddSessions}
}

// IsOfficialLiveshareMethod 判断方法名是否属于本族的 13 个（rc.1 的 12 + rc.2 的 1）。
func IsOfficialLiveshareMethod(name string) bool {
	for _, method := range OfficialLiveshareRC2Methods() {
		if string(method) == name {
			return true
		}
	}
	for _, method := range OfficialLiveshareMethods() {
		if string(method) == name {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 契约类型（字段名与官方 zod 逐字对应）
// ---------------------------------------------------------------------------

// OfficialLiveshareCapability 是插件声明的端点能力（官方 ShareCapabilityGroup）。
type OfficialLiveshareCapability struct {
	Plugin   string   `json:"plugin"`
	Readable []string `json:"readable"`
	Writable []string `json:"writable"`
}

// OfficialLiveshareSelectedIdentity 是 selected 分享点名的 viewer 身份。
type OfficialLiveshareSelectedIdentity struct {
	ProviderID  string `json:"providerId"`
	TenantID    string `json:"tenantId"`
	SubjectID   string `json:"subjectId"`
	IdentityKey string `json:"identityKey"`
}

// OfficialLiveshareAccess 是官方 ShareAccess。
type OfficialLiveshareAccess struct {
	Mode                          string                              `json:"mode"`
	Selected                      []OfficialLiveshareSelectedIdentity `json:"selected"`
	DeploymentAllowedSubjectIDs   []string                            `json:"deploymentAllowedSubjectIds"`
	ShareAllowedSubjectIDs        []string                            `json:"shareAllowedSubjectIds"`
	BroadenedByDeploymentSubjects bool                                `json:"broadenedByDeploymentSubjects"`
	IdentityKeyACL                bool                                `json:"identityKeyAcl"`
}

// OfficialLiveshareCreateRequest 是官方 CreateShareRequest。
type OfficialLiveshareCreateRequest struct {
	SessionID         string
	SessionTitle      string
	AccessMode        string
	TTLMinutes        *float64
	AllowedSubjectIDs []string
	AllowsFork        bool
	SessionMode       string
}

// OfficialLiveshareAddSessionsRequest 是官方 AddShareSessionsRequest（rc.2 新增）。
//
// `sessionIds` 是"要加进这条 share 的源会话"；已经在 share 里的 id 按官方语义
// **忽略**（`share-api-types.ts:164-170`：ids already served by the share are
// ignored），因此重复调用是幂等的。
type OfficialLiveshareAddSessionsRequest struct {
	ShareID    string
	SessionIDs []string
}

// OfficialLiveshareShareSessions 是官方 ShareSessions（rc.2 新增）。
//
// 两个字段都是 required、additionalProperties:true。它是"这条 share 现在服务
// 哪些源会话"的**权威清单**，主会话在最前（官方 `sessionIds[0]` 就是
// `sessionId`，见 `source-sessions.ts:primarySessionIdOf`）。
type OfficialLiveshareShareSessions struct {
	ShareID    string   `json:"shareId"`
	SessionIDs []string `json:"sessionIds"`
}

// OfficialLiveshareCreatedShare 是官方 CreatedShare。
//
// rc.2 起 `sessionIds` 是 required：主会话在第一位，`sessionId` 对旧调用方保留
// （官方 `share-api-types.ts:153-163` 的注释说明了这层兼容关系）。
type OfficialLiveshareCreatedShare struct {
	Link        string                  `json:"link"`
	ShareID     string                  `json:"shareId"`
	SessionID   string                  `json:"sessionId"`
	SessionIDs  []string                `json:"sessionIds"`
	SessionMode string                  `json:"sessionMode"`
	Access      OfficialLiveshareAccess `json:"access"`
}

// OfficialLiveshareShareView 是官方 OwnerShareView。
//
// rc.2 起 `sessionIds` 是 required（`contracts-rc2.json` 的
// liveshareShares/list → result.shares[].required 里有它）；`sessionId` 是它的
// 第一项，为旧调用方保留。
type OfficialLiveshareShareView struct {
	ShareID            string                        `json:"shareId"`
	SessionID          string                        `json:"sessionId"`
	SessionIDs         []string                      `json:"sessionIds"`
	SessionTitle       string                        `json:"sessionTitle"`
	Link               string                        `json:"link"`
	CreatedAt          string                        `json:"createdAt"`
	ExpiresAt          string                        `json:"expiresAt,omitempty"`
	AllowsFork         bool                          `json:"allowsFork"`
	SessionMode        string                        `json:"sessionMode"`
	ViewerCapabilities []OfficialLiveshareCapability `json:"viewerCapabilities"`
	Online             bool                          `json:"online"`
	Access             *OfficialLiveshareAccess      `json:"access,omitempty"`
}

// OfficialLiveshareShareList 是官方 OwnerShareList。
type OfficialLiveshareShareList struct {
	Shares []OfficialLiveshareShareView `json:"shares"`
}

// OfficialLiveshareRelayState 是官方 OwnerRelayState。
type OfficialLiveshareRelayState struct {
	Configured string `json:"configured,omitempty"`
	Override   string `json:"override,omitempty"`
	Effective  string `json:"effective,omitempty"`
}

// OfficialLiveshareStopRequest 是官方 StopShareRequest。
type OfficialLiveshareStopRequest struct {
	ShareID string
}

// OfficialLiveshareStopResult 是官方 StopShareResult（ok 是字面量 true）。
type OfficialLiveshareStopResult struct {
	OK    bool   `json:"ok"`
	Relay string `json:"relay,omitempty"`
}

// OfficialLivesharePermissions 是官方 SharePermissions。
type OfficialLivesharePermissions struct {
	ShareID     string `json:"shareId"`
	SessionMode string `json:"sessionMode"`
	AllowsFork  bool   `json:"allowsFork"`
	ExpiresAt   string `json:"expiresAt,omitempty"`
}

// OfficialLiveshareCapabilityOffers 是官方 ShareCapabilityOffers。
type OfficialLiveshareCapabilityOffers struct {
	Capabilities []OfficialLiveshareCapability `json:"capabilities"`
}

// OfficialLiveshareSetCapabilitiesRequest 是官方 SetShareCapabilitiesRequest。
type OfficialLiveshareSetCapabilitiesRequest struct {
	ShareID      string
	Capabilities []OfficialLiveshareCapability
}

// OfficialLiveshareShareCapabilities 是官方 ShareCapabilities。
type OfficialLiveshareShareCapabilities struct {
	ShareID            string                        `json:"shareId"`
	ViewerCapabilities []OfficialLiveshareCapability `json:"viewerCapabilities"`
}

// OfficialLiveshareSetRelayRequest 是官方 SetRelayRequest。
type OfficialLiveshareSetRelayRequest struct {
	RelayURL *string
}

// OfficialLiveshareIdentityRequest 是官方 OwnerIdentityRequest。
type OfficialLiveshareIdentityRequest struct {
	Relay string
}

// OfficialLiveshareIdentityStatus 是官方 OwnerIdentityStatus。
type OfficialLiveshareIdentityStatus struct {
	State       string `json:"state"`
	Relay       string `json:"relay"`
	DisplayName string `json:"displayName,omitempty"`
	SubjectID   string `json:"subjectId,omitempty"`
	AvatarURL   string `json:"avatarUrl,omitempty"`
	ExpiresAt   string `json:"expiresAt,omitempty"`
	Renewed     *bool  `json:"renewed,omitempty"`
	Claim       *struct {
		Claimed int `json:"claimed"`
		Skipped int `json:"skipped"`
	} `json:"claim,omitempty"`
}

// OfficialLiveshareAuditRequest 是官方 ShareAuditRequest。
type OfficialLiveshareAuditRequest struct {
	ShareID string
}

// OfficialLiveshareAuditEntry 是官方 ShareAuditEntryView。
type OfficialLiveshareAuditEntry struct {
	ID        string `json:"id"`
	Action    string `json:"action"`
	CreatedAt string `json:"createdAt"`
	Actor     string `json:"actor,omitempty"`
	Detail    any    `json:"detail,omitempty"`
}

// OfficialLiveshareAudit 是官方 ShareAudit。
type OfficialLiveshareAudit struct {
	Events []OfficialLiveshareAuditEntry `json:"events"`
}

// OfficialLiveshareConnectivity 是官方 RelayConnectivityView。
type OfficialLiveshareConnectivity struct {
	OK            bool   `json:"ok"`
	HTTPReachable bool   `json:"httpReachable"`
	Blocking      bool   `json:"blocking"`
	Error         string `json:"error,omitempty"`
}

// OfficialLivesharePublishRequest 是官方 TestPublishRequest。
type OfficialLivesharePublishRequest struct {
	SessionID string
}

// OfficialLivesharePublishView 是官方 RelayPublishView。
type OfficialLivesharePublishView struct {
	OK       bool   `json:"ok"`
	Blocking bool   `json:"blocking"`
	Error    string `json:"error,omitempty"`
}

// OfficialLiveshareUpdatePermissionsRequest 是官方 UpdateSharePermissionsRequest。
//
// ttlnull 表示显式 `ttlMinutes: null`（永不过期），与"没给这个字段"不同：
// 前者会清掉 deadline，后者保持不变。
type OfficialLiveshareUpdatePermissionsRequest struct {
	ShareID     string
	SessionMode *string
	AllowsFork  *bool
	TTLMinutes  *float64
	TTLNull     bool
}

// ---------------------------------------------------------------------------
// 宿主实现注入
// ---------------------------------------------------------------------------

// OfficialLiveshareAPI 是宿主侧 13 个方法的真实实现。
//
// 每个字段对应一个方法；nil = 该方法没有实现（回 501 并点名缺哪个字段）。
type OfficialLiveshareAPI struct {
	Create            func(req OfficialLiveshareCreateRequest) (OfficialLiveshareCreatedShare, error)
	AddSessions       func(req OfficialLiveshareAddSessionsRequest) (OfficialLiveshareShareSessions, error)
	List              func() (OfficialLiveshareShareList, error)
	Stop              func(req OfficialLiveshareStopRequest) (OfficialLiveshareStopResult, error)
	UpdatePermissions func(req OfficialLiveshareUpdatePermissionsRequest) (OfficialLivesharePermissions, error)
	SetCapabilities   func(req OfficialLiveshareSetCapabilitiesRequest) (OfficialLiveshareShareCapabilities, error)
	Capabilities      func() (OfficialLiveshareCapabilityOffers, error)
	Relay             func() (OfficialLiveshareRelayState, error)
	SetRelay          func(req OfficialLiveshareSetRelayRequest) (OfficialLiveshareRelayState, error)
	Identity          func(req OfficialLiveshareIdentityRequest) (OfficialLiveshareIdentityStatus, error)
	Audit             func(req OfficialLiveshareAuditRequest) (OfficialLiveshareAudit, error)
	TestRelay         func() (OfficialLiveshareConnectivity, error)
	TestPublish       func(req OfficialLivesharePublishRequest) (OfficialLivesharePublishView, error)
}

var officialLiveshareMount = struct {
	mu sync.RWMutex
	m  map[*Server]OfficialLiveshareAPI
}{m: map[*Server]OfficialLiveshareAPI{}}

// AttachOfficialLiveshareAPI 给这个 Server 挂上 13 个方法。
func (s *Server) AttachOfficialLiveshareAPI(api OfficialLiveshareAPI) {
	if s == nil {
		return
	}
	officialLiveshareMount.mu.Lock()
	defer officialLiveshareMount.mu.Unlock()
	officialLiveshareMount.m[s] = api
}

// DetachOfficialLiveshareAPI 摘掉实现（测试收尾用）。
func (s *Server) DetachOfficialLiveshareAPI() {
	if s == nil {
		return
	}
	officialLiveshareMount.mu.Lock()
	defer officialLiveshareMount.mu.Unlock()
	delete(officialLiveshareMount.m, s)
}

func (s *Server) officialLiveshareAPI() (OfficialLiveshareAPI, bool) {
	if s == nil {
		return OfficialLiveshareAPI{}, false
	}
	officialLiveshareMount.mu.RLock()
	defer officialLiveshareMount.mu.RUnlock()
	api, ok := officialLiveshareMount.m[s]
	return api, ok
}

// ---------------------------------------------------------------------------
// 分发
// ---------------------------------------------------------------------------

// serveOfficialLiveshareAPI 是 /api 路由的入口（server.go 的 forge/liveshare 分支
// 已经存在；本文件只负责 12 个 liveshare 方法）。
func (s *Server) serveOfficialLiveshareAPI(w http.ResponseWriter, r *http.Request, name string) {
	// 即使整族没接线也要先做参数校验：参数拼错必须回 400（能改），
	// 而不是一个"等宿主实现"的 501。缺实现的字段在具体分支里回 501。
	api, _ := s.officialLiveshareAPI()
	ServeOfficialLiveshare(w, r, name, api)
}

// ServeOfficialLiveshare 是纯函数形态的分发入口（测试不需要真实 Server）。
func ServeOfficialLiveshare(w http.ResponseWriter, r *http.Request, name string, api OfficialLiveshareAPI) {
	method := Method(name)
	if !IsOfficialLiveshareMethod(name) {
		writeOfficialError(w, officialNotImplemented("不是官方 liveshareShares 族的方法：%q", name))
		return
	}
	params, err := decodeLiveshareParams(r)
	if err != nil {
		writeOfficialError(w, err)
		return
	}

	switch method {
	case MethodLiveshareSharesCreate:
		req, err := parseLiveshareCreate(params)
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		if api.Create == nil {
			writeOfficialError(w, missingLiveshareImpl("Create", name))
			return
		}
		result, err := api.Create(req)
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		if err := validateOfficialCreatedShare(result); err != nil {
			writeOfficialError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)

	case MethodLiveshareSharesAddSessions:
		req, err := parseLiveshareAddSessions(params)
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		if api.AddSessions == nil {
			writeOfficialError(w, missingLiveshareImpl("AddSessions", name))
			return
		}
		result, err := api.AddSessions(req)
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		if err := validateOfficialShareSessions(result, req.SessionIDs); err != nil {
			writeOfficialError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)

	case MethodLiveshareSharesList:
		if err := params.finish(); err != nil {
			writeOfficialError(w, err)
			return
		}
		if api.List == nil {
			writeOfficialError(w, missingLiveshareImpl("List", name))
			return
		}
		result, err := api.List()
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		if err := validateOfficialShareList(result); err != nil {
			writeOfficialError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)

	case MethodLiveshareSharesStop:
		req, err := parseLiveshareStop(params)
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		if api.Stop == nil {
			writeOfficialError(w, missingLiveshareImpl("Stop", name))
			return
		}
		result, err := api.Stop(req)
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		if !result.OK {
			writeOfficialError(w, officialInternal("宿主 stop 返回 ok=false：官方 StopShareResult 的 ok 只能是 true"))
			return
		}
		writeJSON(w, http.StatusOK, result)

	case MethodLiveshareSharesUpdatePermissions:
		req, err := parseLiveshareUpdatePermissions(params)
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		if api.UpdatePermissions == nil {
			writeOfficialError(w, missingLiveshareImpl("UpdatePermissions", name))
			return
		}
		result, err := api.UpdatePermissions(req)
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		if err := validateLiveshareSessionMode(result.SessionMode, "sessionMode"); err != nil {
			writeOfficialError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)

	case MethodLiveshareSharesSetCapabilities:
		req, err := parseLiveshareSetCapabilities(params)
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		if api.SetCapabilities == nil {
			writeOfficialError(w, missingLiveshareImpl("SetCapabilities", name))
			return
		}
		result, err := api.SetCapabilities(req)
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		if err := validateCapabilities(result.ViewerCapabilities); err != nil {
			writeOfficialError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)

	case MethodLiveshareSharesCapabilities:
		if err := params.finish(); err != nil {
			writeOfficialError(w, err)
			return
		}
		if api.Capabilities == nil {
			writeOfficialError(w, missingLiveshareImpl("Capabilities", name))
			return
		}
		result, err := api.Capabilities()
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		if err := validateCapabilities(result.Capabilities); err != nil {
			writeOfficialError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)

	case MethodLiveshareSharesRelay:
		if err := params.finish(); err != nil {
			writeOfficialError(w, err)
			return
		}
		if api.Relay == nil {
			writeOfficialError(w, missingLiveshareImpl("Relay", name))
			return
		}
		result, err := api.Relay()
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)

	case MethodLiveshareSharesSetRelay:
		req, err := parseLiveshareSetRelay(params)
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		if api.SetRelay == nil {
			writeOfficialError(w, missingLiveshareImpl("SetRelay", name))
			return
		}
		result, err := api.SetRelay(req)
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)

	case MethodLiveshareSharesIdentity:
		req, err := parseLiveshareIdentity(params)
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		if api.Identity == nil {
			writeOfficialError(w, missingLiveshareImpl("Identity", name))
			return
		}
		result, err := api.Identity(req)
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		switch result.State {
		case "verified", "expired", "unverified", "relay-unsupported":
		default:
			writeOfficialError(w, officialInternal(
				"宿主 identity 返回了官方枚举外的 state=%q（只能是 verified/expired/unverified/relay-unsupported）", result.State))
			return
		}
		writeJSON(w, http.StatusOK, result)

	case MethodLiveshareSharesAudit:
		req, err := parseLiveshareAudit(params)
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		if api.Audit == nil {
			writeOfficialError(w, missingLiveshareImpl("Audit", name))
			return
		}
		result, err := api.Audit(req)
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		for i, entry := range result.Events {
			if entry.ID == "" || entry.Action == "" || entry.CreatedAt == "" {
				writeOfficialError(w, officialInternal(
					"宿主 audit 第 %d 条缺 id/action/createdAt（官方三者必填）", i))
				return
			}
		}
		writeJSON(w, http.StatusOK, result)

	case MethodLiveshareSharesTestRelay:
		if err := params.finish(); err != nil {
			writeOfficialError(w, err)
			return
		}
		if api.TestRelay == nil {
			writeOfficialError(w, missingLiveshareImpl("TestRelay", name))
			return
		}
		result, err := api.TestRelay()
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)

	case MethodLiveshareSharesTestPublish:
		req, err := parseLiveshareTestPublish(params)
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		if api.TestPublish == nil {
			writeOfficialError(w, missingLiveshareImpl("TestPublish", name))
			return
		}
		result, err := api.TestPublish(req)
		if err != nil {
			writeOfficialError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)

	default:
		writeOfficialError(w, officialNotImplemented("liveshareShares 方法没有分发分支：%q", name))
	}
}

// OfficialLiveshareNotFound 是宿主返回"share 不存在/已停止"时用的 404。
// 导出是因为宿主在 cmd/dshd，而 officialError 在本包内。
func OfficialLiveshareNotFound(format string, args ...any) error {
	return &officialError{status: http.StatusNotFound, code: "not_found", msg: fmt.Sprintf(format, args...)}
}

// OfficialLiveshareSessionNotFound 是"要操作的会话在本 home 里不存在"时的 404。
//
// 码用 no_such_session：它的 hint 正好是"先 session.list 拿 id；确认 --home 指向
// 的目录里有这个会话"，比笼统的 not_found 更能告诉调用方下一步做什么。
func OfficialLiveshareSessionNotFound(format string, args ...any) error {
	return &officialError{status: http.StatusNotFound, code: "no_such_session", msg: fmt.Sprintf(format, args...)}
}

// OfficialLiveshareBadRequest 是"形状合法但取值与本宿主的真实状态冲突"时的 400
// （例如要加的会话不在 share 主会话所在的工作区里）。
func OfficialLiveshareBadRequest(format string, args ...any) error {
	return officialBadRequest(format, args...)
}

// OfficialLiveshareForbidden 是"这条 share 不属于当前 owner/relay"时的 403。
//
// 官方把 addSessions 归为 owner-only（`endpoint-classification.ts:241`）。
// 本仓是单 owner 本地宿主（见 cmd/dshd/serve_remote_access_follow.go 的说明：
// 策略表还没落地，经认证的调用者都是 owner），所以 owner 边界的**真实落点**
// 是 owner 状态里的 relay 归属：share 记录上写着哪个 relay 的 owner 发布过它，
// 就不许在另一个 relay（另一个 owner 身份）下改它。
func OfficialLiveshareForbidden(format string, args ...any) error {
	return &officialError{status: http.StatusForbidden, code: "permission_denied", msg: fmt.Sprintf(format, args...)}
}

func missingLiveshareImpl(field, method string) error {
	return officialNotImplemented(
		"%s 需要 OfficialLiveshareAPI.%s，但宿主没有接线；需要接入 liveshare owner 服务（relay 地址 + owner token 来自进程配置），接线入口是 cmd/dshd 的 attachOfficialLiveshare",
		method, field)
}

// ---------------------------------------------------------------------------
// 参数解析（官方 wire：单参数 `request`，无参方法用 {}）
// ---------------------------------------------------------------------------

// decodeLiveshareParams 读 JSON 对象。无参方法也要求 `{}`（与 /api 的 POST 约定一致）。
func decodeLiveshareParams(r *http.Request) (*officialParams, error) {
	return decodeOfficialParams(r)
}

// liveshareRequest 取 `request` 对象。官方这 12 个方法的参数都是对象，
// strictCodec 是 additionalProperties:false，所以顶层多字段必须拒。
func liveshareRequest(p *officialParams) (map[string]json.RawMessage, error) {
	raw, ok := p.take("request")
	if !ok {
		return nil, officialBadRequest("缺 request")
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, officialBadRequest("request 不能是 null")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, officialBadRequest("request 必须是 JSON 对象")
	}
	if obj == nil {
		return nil, officialBadRequest("request 不能是 null")
	}
	return obj, nil
}

func parseLiveshareCreate(p *officialParams) (OfficialLiveshareCreateRequest, error) {
	obj, err := liveshareRequest(p)
	if err != nil {
		return OfficialLiveshareCreateRequest{}, err
	}
	var req OfficialLiveshareCreateRequest
	req.SessionID, err = requiredStringField(obj, "sessionId")
	if err != nil {
		return req, err
	}
	if req.SessionTitle, err = optionalStringField(obj, "sessionTitle"); err != nil {
		return req, err
	}
	if req.AccessMode, err = optionalEnumField(obj, "accessMode", "open", "selected"); err != nil {
		return req, err
	}
	if req.SessionMode, err = optionalEnumField(obj, "sessionMode", "view", "write"); err != nil {
		return req, err
	}
	if req.TTLMinutes, err = optionalPositiveNumberField(obj, "ttlMinutes"); err != nil {
		return req, err
	}
	if req.AllowedSubjectIDs, err = optionalStringArrayField(obj, "allowedSubjectIds"); err != nil {
		return req, err
	}
	if req.AllowsFork, err = optionalBoolField(obj, "allowsFork"); err != nil {
		return req, err
	}
	return req, p.finish()
}

// parseLiveshareAddSessions 解析官方 AddShareSessionsRequest。
//
// 官方实现（`owner-share-operations.ts:408-449`）的三条前置校验在这里就已经
// 落成 400，宿主拿到的一定是"有 shareId + 至少一个非空 sessionId"的请求：
//
//   - `shareId === ”` → `liveshareShares/missing-share-id`；
//   - 去重后为空 → `liveshareShares/missing-session-ids`；
//   - 任一项是空串 → 同上。
//
// 去重按官方 `[...new Set(...)]` 的语义做（保持首次出现顺序），这样宿主收到
// 的 id 列表与官方完全一致，重复调用天然幂等。
func parseLiveshareAddSessions(p *officialParams) (OfficialLiveshareAddSessionsRequest, error) {
	obj, err := liveshareRequest(p)
	if err != nil {
		return OfficialLiveshareAddSessionsRequest{}, err
	}
	var req OfficialLiveshareAddSessionsRequest
	if req.ShareID, err = requiredStringField(obj, "shareId"); err != nil {
		return req, err
	}
	ids, err := requiredNonEmptyStringArrayField(obj, "sessionIds")
	if err != nil {
		return req, err
	}
	req.SessionIDs = dedupeStrings(ids)
	if len(req.SessionIDs) == 0 {
		return req, officialBadRequest("sessionIds 去重后是空的：addSessions 至少要加一个非空 sessionId")
	}
	return req, p.finish()
}

// dedupeStrings 保序去重（官方 `[...new Set(ids)]`）。
func dedupeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func parseLiveshareStop(p *officialParams) (OfficialLiveshareStopRequest, error) {
	obj, err := liveshareRequest(p)
	if err != nil {
		return OfficialLiveshareStopRequest{}, err
	}
	shareID, err := requiredStringField(obj, "shareId")
	if err != nil {
		return OfficialLiveshareStopRequest{}, err
	}
	return OfficialLiveshareStopRequest{ShareID: shareID}, p.finish()
}

func parseLiveshareAudit(p *officialParams) (OfficialLiveshareAuditRequest, error) {
	obj, err := liveshareRequest(p)
	if err != nil {
		return OfficialLiveshareAuditRequest{}, err
	}
	shareID, err := requiredStringField(obj, "shareId")
	if err != nil {
		return OfficialLiveshareAuditRequest{}, err
	}
	return OfficialLiveshareAuditRequest{ShareID: shareID}, p.finish()
}

func parseLiveshareUpdatePermissions(p *officialParams) (OfficialLiveshareUpdatePermissionsRequest, error) {
	obj, err := liveshareRequest(p)
	if err != nil {
		return OfficialLiveshareUpdatePermissionsRequest{}, err
	}
	var req OfficialLiveshareUpdatePermissionsRequest
	if req.ShareID, err = requiredStringField(obj, "shareId"); err != nil {
		return req, err
	}
	if raw, ok := obj["sessionMode"]; ok {
		mode, err := stringField(raw, "sessionMode")
		if err != nil {
			return req, err
		}
		if mode != "view" && mode != "write" {
			return req, officialBadRequest("sessionMode 只能是 view 或 write，收到 %q", mode)
		}
		req.SessionMode = &mode
	}
	if raw, ok := obj["allowsFork"]; ok {
		var val bool
		if err := json.Unmarshal(raw, &val); err != nil {
			return req, officialBadRequest("allowsFork 必须是布尔值")
		}
		req.AllowsFork = &val
	}
	if raw, ok := obj["ttlMinutes"]; ok {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			req.TTLNull = true
		} else {
			req.TTLMinutes, err = optionalPositiveNumberField(obj, "ttlMinutes")
			if err != nil {
				return req, err
			}
		}
	}
	if req.SessionMode == nil && req.AllowsFork == nil && req.TTLMinutes == nil && !req.TTLNull {
		return req, officialBadRequest("updatePermissions 至少要改 sessionMode/allowsFork/ttlMinutes 之一")
	}
	return req, p.finish()
}

func parseLiveshareSetCapabilities(p *officialParams) (OfficialLiveshareSetCapabilitiesRequest, error) {
	obj, err := liveshareRequest(p)
	if err != nil {
		return OfficialLiveshareSetCapabilitiesRequest{}, err
	}
	var req OfficialLiveshareSetCapabilitiesRequest
	if req.ShareID, err = requiredStringField(obj, "shareId"); err != nil {
		return req, err
	}
	raw, ok := obj["capabilities"]
	if !ok {
		// capabilities 可选：缺省 = 选择空集合（官方 z.array(...).optional()）。
		req.Capabilities = nil
		return req, p.finish()
	}
	caps, err := parseCapabilityArray(raw, "capabilities")
	if err != nil {
		return req, err
	}
	req.Capabilities = caps
	return req, p.finish()
}

func parseLiveshareSetRelay(p *officialParams) (OfficialLiveshareSetRelayRequest, error) {
	obj, err := liveshareRequest(p)
	if err != nil {
		return OfficialLiveshareSetRelayRequest{}, err
	}
	raw, ok := obj["relayUrl"]
	if !ok {
		return OfficialLiveshareSetRelayRequest{}, officialBadRequest("缺 relayUrl")
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return OfficialLiveshareSetRelayRequest{RelayURL: nil}, p.finish()
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return OfficialLiveshareSetRelayRequest{}, officialBadRequest("relayUrl 必须是字符串或 null")
	}
	return OfficialLiveshareSetRelayRequest{RelayURL: &value}, p.finish()
}

func parseLiveshareIdentity(p *officialParams) (OfficialLiveshareIdentityRequest, error) {
	obj, err := liveshareRequest(p)
	if err != nil {
		return OfficialLiveshareIdentityRequest{}, err
	}
	var req OfficialLiveshareIdentityRequest
	if raw, ok := obj["relay"]; ok {
		value, err := stringField(raw, "relay")
		if err != nil {
			return req, err
		}
		req.Relay = value
	}
	return req, p.finish()
}

func parseLiveshareTestPublish(p *officialParams) (OfficialLivesharePublishRequest, error) {
	obj, err := liveshareRequest(p)
	if err != nil {
		return OfficialLivesharePublishRequest{}, err
	}
	sessionID, err := requiredStringField(obj, "sessionId")
	if err != nil {
		return OfficialLivesharePublishRequest{}, err
	}
	return OfficialLivesharePublishRequest{SessionID: sessionID}, p.finish()
}

// ---------------------------------------------------------------------------
// 字段级校验
// ---------------------------------------------------------------------------

func requiredStringField(obj map[string]json.RawMessage, name string) (string, error) {
	raw, ok := obj[name]
	if !ok {
		return "", officialBadRequest("缺 %s", name)
	}
	return stringField(raw, name)
}

func stringField(raw json.RawMessage, name string) (string, error) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", officialBadRequest("%s 必须是字符串", name)
	}
	if strings.TrimSpace(value) == "" && name != "sessionTitle" && name != "relay" {
		return "", officialBadRequest("%s 是空串", name)
	}
	return value, nil
}

func optionalStringField(obj map[string]json.RawMessage, name string) (string, error) {
	raw, ok := obj[name]
	if !ok {
		return "", nil
	}
	return stringField(raw, name)
}

func optionalEnumField(obj map[string]json.RawMessage, name string, allowed ...string) (string, error) {
	raw, ok := obj[name]
	if !ok {
		return "", nil
	}
	value, err := stringField(raw, name)
	if err != nil {
		return "", err
	}
	for _, want := range allowed {
		if value == want {
			return value, nil
		}
	}
	return "", officialBadRequest("%s 只能是 %s，收到 %q", name, strings.Join(allowed, "/"), value)
}

func optionalPositiveNumberField(obj map[string]json.RawMessage, name string) (*float64, error) {
	raw, ok := obj[name]
	if !ok {
		return nil, nil
	}
	var value float64
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, officialBadRequest("%s 必须是数字", name)
	}
	if value <= 0 {
		return nil, officialBadRequest("%s 必须是正数（要永不过期请省略该字段或传 null）", name)
	}
	return &value, nil
}

func optionalStringArrayField(obj map[string]json.RawMessage, name string) ([]string, error) {
	raw, ok := obj[name]
	if !ok {
		return nil, nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, officialBadRequest("%s 必须是字符串数组", name)
	}
	if values == nil {
		return nil, officialBadRequest("%s 不能是 null", name)
	}
	return values, nil
}

// requiredNonEmptyStringArrayField 取一个必填的"非空字符串数组"。
//
// 官方 addSessions 的 `sessionIds` 是 `z.array(z.string())` 且实现里再拒空串
// （`owner-share-operations.ts:414-420`），所以这里一次校验到位：
// 缺字段 / null / 非数组 / 元素非字符串 / 元素空串 全部 400。
func requiredNonEmptyStringArrayField(obj map[string]json.RawMessage, name string) ([]string, error) {
	raw, ok := obj[name]
	if !ok {
		return nil, officialBadRequest("缺 %s", name)
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, officialBadRequest("%s 不能是 null（要给至少一个会话 id 的数组）", name)
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, officialBadRequest("%s 必须是字符串数组", name)
	}
	if len(values) == 0 {
		return nil, officialBadRequest("%s 是空数组：addSessions 至少要加一个会话", name)
	}
	for i, value := range values {
		if strings.TrimSpace(value) == "" {
			return nil, officialBadRequest("%s[%d] 是空串：会话 id 不能为空", name, i)
		}
	}
	return values, nil
}

func optionalBoolField(obj map[string]json.RawMessage, name string) (bool, error) {
	raw, ok := obj[name]
	if !ok {
		return false, nil
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, officialBadRequest("%s 必须是布尔值", name)
	}
	return value, nil
}

// parseCapabilityArray 校验官方 ShareCapabilityGroup 数组。
func parseCapabilityArray(raw json.RawMessage, name string) ([]OfficialLiveshareCapability, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, officialBadRequest("%s 不能是 null（空选择用空数组）", name)
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, officialBadRequest("%s 必须是对象数组", name)
	}
	out := make([]OfficialLiveshareCapability, 0, len(items))
	for i, item := range items {
		field := func(key string) (json.RawMessage, bool) {
			value, ok := item[key]
			return value, ok
		}
		pluginRaw, ok := field("plugin")
		if !ok {
			return nil, officialBadRequest("%s[%d] 缺 plugin", name, i)
		}
		plugin, err := stringField(pluginRaw, name+"["+liveshareIndexString(i)+"].plugin")
		if err != nil {
			return nil, err
		}
		readable, err := capabilityEndpointList(item, "readable", name, i)
		if err != nil {
			return nil, err
		}
		writable, err := capabilityEndpointList(item, "writable", name, i)
		if err != nil {
			return nil, err
		}
		out = append(out, OfficialLiveshareCapability{Plugin: plugin, Readable: readable, Writable: writable})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Plugin < out[j].Plugin })
	return out, nil
}

func capabilityEndpointList(item map[string]json.RawMessage, key, owner string, index int) ([]string, error) {
	raw, ok := item[key]
	if !ok {
		return nil, officialBadRequest("%s[%d] 缺 %s", owner, index, key)
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, officialBadRequest("%s[%d].%s 必须是字符串数组", owner, index, key)
	}
	if values == nil {
		return nil, officialBadRequest("%s[%d].%s 不能是 null", owner, index, key)
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return nil, officialBadRequest("%s[%d].%s 里有空串", owner, index, key)
		}
	}
	return values, nil
}

func liveshareIndexString(i int) string { return fmt.Sprintf("%d", i) }

// ---------------------------------------------------------------------------
// 结果校验（宿主回包必须符合官方 zod，不允许假数据上线）
// ---------------------------------------------------------------------------

func validateOfficialCreatedShare(share OfficialLiveshareCreatedShare) error {
	if strings.TrimSpace(share.ShareID) == "" || strings.TrimSpace(share.Link) == "" {
		return officialInternal("宿主 create 回包缺 shareId/link：share 必须先在 relay 上真实注册")
	}
	if strings.TrimSpace(share.SessionID) == "" {
		return officialInternal("宿主 create 回包缺 sessionId")
	}
	// rc.2 起 CreatedShare.sessionIds 是 required：主会话必须是第一项。
	if err := validateShareSessionIDs(share.SessionID, share.SessionIDs, "create 回包"); err != nil {
		return err
	}
	if err := validateLiveshareSessionMode(share.SessionMode, "sessionMode"); err != nil {
		return err
	}
	if share.Access.Mode != "open" && share.Access.Mode != "selected" {
		return officialInternal("宿主 create 回包的 access.mode=%q 不是 open/selected", share.Access.Mode)
	}
	if !share.Access.IdentityKeyACL {
		return officialInternal("宿主 create 回包的 access.identityKeyAcl 必须是 true（官方字面量）")
	}
	if share.Access.Selected == nil || share.Access.DeploymentAllowedSubjectIDs == nil || share.Access.ShareAllowedSubjectIDs == nil {
		return officialInternal("宿主 create 回包的 access 数组字段不能是 nil（编码后会变 null）")
	}
	for _, identity := range share.Access.Selected {
		if identity.ProviderID == "" || identity.TenantID == "" || identity.SubjectID == "" || identity.IdentityKey == "" {
			return officialInternal("宿主 create 回包的 selected 身份缺 provider/tenant/subject/identityKey")
		}
	}
	return nil
}

func validateOfficialShareList(list OfficialLiveshareShareList) error {
	if list.Shares == nil {
		return officialInternal("宿主 list 回包的 shares 是 nil（编码后会变 null；空列表应为 []）")
	}
	for _, share := range list.Shares {
		if strings.TrimSpace(share.ShareID) == "" || strings.TrimSpace(share.SessionID) == "" ||
			strings.TrimSpace(share.Link) == "" {
			return officialInternal("宿主 list 回包有条目缺 shareId/sessionId/link")
		}
		if err := validateLiveshareSessionMode(share.SessionMode, "sessionMode"); err != nil {
			return err
		}
		if share.CreatedAt == "" {
			return officialInternal("宿主 list 回包的条目缺 createdAt")
		}
		if err := validateShareSessionIDs(share.SessionID, share.SessionIDs, "list 回包"); err != nil {
			return err
		}
	}
	return nil
}

// validateShareSessionIDs 校验"一主多源"的会话清单（rc.2 的 ShareSessions 形状）。
//
// 官方对这份清单的硬约束（`source-sessions.ts:primarySessionIdOf`）：
// 非空、主会话在第一位、`sessionId`（旧字段）必须等于第一项。
func validateShareSessionIDs(primary string, ids []string, owner string) error {
	if ids == nil {
		return officialInternal("宿主 %s：sessionIds 是 nil（编码后会变 null；官方 rc.2 是 required 数组）", owner)
	}
	if len(ids) == 0 {
		return officialInternal("宿主 %s：sessionIds 是空数组：一条 share 至少服务一个源会话", owner)
	}
	seen := make(map[string]struct{}, len(ids))
	for i, id := range ids {
		if strings.TrimSpace(id) == "" {
			return officialInternal("宿主 %s：sessionIds[%d] 是空串", owner, i)
		}
		if _, dup := seen[id]; dup {
			return officialInternal("宿主 %s：sessionIds 里 %q 重复", owner, id)
		}
		seen[id] = struct{}{}
	}
	if ids[0] != primary {
		return officialInternal("宿主 %s：sessionIds[0]=%q 与 sessionId=%q 不一致（官方约定主会话在第一位）",
			owner, ids[0], primary)
	}
	return nil
}

// validateOfficialShareSessions 校验 addSessions 的结果。
//
// 这里不只查形状，还查**语义**：官方回包是"这条 share 现在服务的全部源会话"
// （`owner-share-operations.ts:448`），所以请求里每个 id 都必须在结果里——
// 少了就是宿主没真的加进去（假成功），必须 500 而不是把半个状态送上线。
func validateOfficialShareSessions(result OfficialLiveshareShareSessions, requested []string) error {
	if strings.TrimSpace(result.ShareID) == "" {
		return officialInternal("宿主 addSessions 回包缺 shareId")
	}
	if result.SessionIDs == nil {
		return officialInternal("宿主 addSessions 回包的 sessionIds 是 nil（编码后会变 null）")
	}
	if len(result.SessionIDs) == 0 {
		return officialInternal("宿主 addSessions 回包的 sessionIds 是空数组：share 至少服务一个会话")
	}
	seen := make(map[string]struct{}, len(result.SessionIDs))
	for i, id := range result.SessionIDs {
		if strings.TrimSpace(id) == "" {
			return officialInternal("宿主 addSessions 回包的 sessionIds[%d] 是空串", i)
		}
		if _, dup := seen[id]; dup {
			return officialInternal("宿主 addSessions 回包的 sessionIds 里 %q 重复（幂等语义被破坏）", id)
		}
		seen[id] = struct{}{}
	}
	for _, id := range requested {
		if _, ok := seen[id]; !ok {
			return officialInternal("宿主 addSessions 回包没有包含刚加进去的 %q：状态没真的变（假成功）", id)
		}
	}
	return nil
}

func validateLiveshareSessionMode(mode, name string) error {
	if mode != "view" && mode != "write" {
		return officialInternal("宿主回包的 %s=%q 不是 view/write", name, mode)
	}
	return nil
}

func validateCapabilities(caps []OfficialLiveshareCapability) error {
	if caps == nil {
		return officialInternal("宿主回包的 capabilities 是 nil（编码后会变 null；空清单应为 []）")
	}
	for _, capability := range caps {
		if strings.TrimSpace(capability.Plugin) == "" {
			return officialInternal("宿主回包的 capability 缺 plugin")
		}
		if capability.Readable == nil || capability.Writable == nil {
			return officialInternal("宿主回包 capability 的 readable/writable 不能是 nil")
		}
	}
	return nil
}
