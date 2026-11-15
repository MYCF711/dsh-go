// Package cua 实现官方 0.2.2-rc.2 的 `cuaAccess` 方法面（9 个方法）在 Go 宿主里的真实能力。
//
// 开工前必须记住的三条官方事实（都带 file:line，别凭印象改）：
//
//  1. **契约的 9 个方法全部带 win32 分支**
//     （`docs/evidence/official-method-contract-delta-0.2.2-rc.2.md:311-814`；
//     源码侧 `dsh-cua/src/access.ts:23-29` 的 `systemPermissions` 就是
//     darwin/win32 的 discriminatedUnion）；
//     但 rc.2 的 `dsh-cua-bundle` **只挂 macOS provider**
//     （`dsh-cua-bundle/cordis.patch.yml:5-8`：`disabled: !!js process.platform !== 'darwin'`），
//     而且 `@deepseek-ai/dsh-experimental-cua-macos` 这个包在 Windows 安装里
//     **根本不存在**（`ls node_modules/@deepseek-ai | grep cua` 只有 cua/cua-bundle/computer-use）。
//     `dsh-cua/src/index.ts:415` 的 `apply()` 是空函数——`cuaAccess` 服务由 provider
//     调 `installCua()`（`index.ts:153-177`）才创建，所以**官方 Windows 宿主上这 9 个方法一个都不存在**。
//  2. 因此本包的定位是「只能比官方多，不能比官方少」：**不依赖原生 AX/输入 provider 的部分做成真实现**
//     （权限/环境观测、图标提取、设置页打开、应用决策状态机与持久化、PTC 状态），
//     **必须有平台 provider 的部分做成结构化未实现 + `capableWhen`**，绝不返回假数据、绝不静默成功。
//  3. 官方 `applications` 读的是**已保存的应用决策**，不是枚举运行中进程：
//     `access.ts:75-80` → `ApplicationAccess.status()`（`application-access.ts:65-81`）→
//     `CuaPermissionStore`（`permission-store.ts:47-129`）。枚举运行中应用属于 provider 的
//     native 协议（`listApps`/`getApp`，`protocol.ts:52-53`），**不在 9 方法面内**。
//     本包按契约实现前者，并把后者做成 additive 方法 `cuaAccess.runningApplications`
//     （`RunningApplications`），因为它在这台机器上**真能枚举**（Win32 EnumWindows）。
package cua

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
)

// ApplicationIDPattern 是官方 CuaApplicationId 的判据：
// `dsh-cua/src/application-access.ts:15`（`z.string().regex(/^[a-f0-9]{64}$/)`）、
// `permission-store.ts:13`、`permission-store.ts:24` 三处一致。
var ApplicationIDPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Grant 是官方 CuaApplicationGrant（`dsh-cua/src/application-types.ts:9-16`）。
//
//	{id: 原生身份摘要, name: 决策时呈现给用户的标签, executablePath?: 用于显示与图标的规范路径}
type Grant struct {
	// ID 是原生身份摘要（64 位小写十六进制）。官方由 provider 计算，本仓由
	// ApplicationIdentity 从规范可执行路径算出来（同一台机器上稳定可复算）。
	ID string `json:"id"`
	// Name 是决策时呈现给用户的标签。空标签没有意义，校验时拒绝。
	Name string `json:"name"`
	// ExecutablePath 只用于显示与取图标；操作系统给不出时整个字段省略。
	ExecutablePath string `json:"executablePath,omitempty"`
}

// Decision 是一次会话级许可决定。官方三态：`dsh-cua/src/permission-store.ts:17`。
type Decision string

const (
	// DecisionAllow 表示本会话已允许。
	DecisionAllow Decision = "allow"
	// DecisionDeny 表示本会话已拒绝（`blockedApps` 就是从这里来的）。
	DecisionDeny Decision = "deny"
	// DecisionAsk 表示本会话还没定（`retryApproval` 把 deny 退回这里）。
	DecisionAsk Decision = "ask"
)

// Valid 判断是不是官方三态之一。
func (d Decision) Valid() bool {
	switch d {
	case DecisionAllow, DecisionDeny, DecisionAsk:
		return true
	}
	return false
}

// SessionDecision 是一条会话级决策（`permission-store.ts:16-18`）。
type SessionDecision struct {
	Identity Grant    `json:"identity"`
	Decision Decision `json:"decision"`
	// Revision 让"等审批期间决策被改过"可被发现（官方 `application-access.ts:194-197`）。
	Revision int `json:"revision"`
}

// ApplicationPermission 是一个应用的决策记录（`permission-store.ts:19-22`）。
//
//	revoked  撤销代数：每 revoke 一次 +1，选中时的快照与它不一致即视为已撤销
//	always   持久许可（只在能持久化的组合里存在）
//	sessions 会话级决策
type ApplicationPermission struct {
	Revoked  int                        `json:"revoked"`
	Always   *Grant                     `json:"always,omitempty"`
	Sessions map[string]SessionDecision `json:"sessions"`
}

// State 是持久化的权威状态，形状与官方 domain storage 一致
// （`permission-store.ts:23-32` 的 `state`），因而本仓写出的文件与官方同构。
type State struct {
	// Migrated 表示"已导入过历史许可"，空库不再重新导入（官方同名字段）。
	Migrated     bool                             `json:"migrated"`
	Applications map[string]ApplicationPermission `json:"applications"`
}

// ApplicationState 是 `cuaAccess/applications` 的 result（契约 :337-403）。
type ApplicationState struct {
	// AllowedApps 是持久许可（只从 always 来，与会话级 allow 无关）。
	AllowedApps []Grant `json:"allowedApps"`
	// BlockedApps 是"任一会话拒绝且没有持久许可"的应用。
	BlockedApps []Grant `json:"blockedApps"`
	// PersistentAvailable 表示这个组合能不能把许可跨重启留住。
	PersistentAvailable bool `json:"persistentAvailable"`
}

// ProcessInfo 是官方 CuaPermissionProcess（`dsh-cua/src/access-types.ts:10-14`）。
//
// 官方刻意说明"helper 与它的父进程都不假定是 TCC 的持有者"——所以这里只报
// **真实进程元数据**，不推断权限归属。
type ProcessInfo struct {
	PID  int    `json:"pid"`
	Path string `json:"path,omitempty"`
	Name string `json:"name,omitempty"`
}

// Win32SystemStatus 是契约 win32 分支的 result（契约 :756-812）。
//
// 必需字段（platform/sessionLocked/captureAvailable/elevated/helper）**一定出现**：
// `sessionLocked` 是 `boolean|null`，观测不到时是真的 null，不是省略。
// 其余字段是契约 `additionalProperties: true` 允许的**附加真话**——它们不替代
// 必需字段，只回答"为什么这台机器没有 AX 能力、什么时候会有"。
type Win32SystemStatus struct {
	Platform string `json:"platform"`
	// SessionLocked：Windows 会话是否锁屏。nil = 观测不到（不是"没锁"）。
	SessionLocked *bool `json:"sessionLocked"`
	// CaptureAvailable：能不能真的抓屏（本包做一次 1×1 BitBlt 实测，不靠猜）。
	CaptureAvailable bool        `json:"captureAvailable"`
	Elevated         bool        `json:"elevated"`
	Helper           ProcessInfo `json:"helper"`

	// —— 以下是契约 additionalProperties 允许的附加真话 ——
	// Parent 是 helper 的父进程（真实枚举；解析不出就省略）。
	Parent *ProcessInfo `json:"parent,omitempty"`
	// ProviderAvailable 恒为 false：rc.2 在 Windows 上没有 CUA provider（见包注释）。
	ProviderAvailable bool `json:"providerAvailable"`
	// HelperRole 说明 helper 报的是谁：本仓没有独立原生 helper，报的是宿主自己。
	HelperRole string `json:"helperRole"`

	// RequestedPermission 只在 requestPermission 的结果里出现（systemStatus 不带）。
	RequestedPermission string `json:"requestedPermission,omitempty"`
	// ConsentEstablished 恒为 false：请求权限不会建立同意（官方描述同义）。
	ConsentEstablished bool `json:"consentEstablished"`

	// PlatformNote 说明 win32 分支为什么没有 accessibility/screenRecording。
	PlatformNote string `json:"platformNote,omitempty"`
	// CapableWhen 说清"什么条件下这项能力才会出现"。
	CapableWhen string `json:"capableWhen,omitempty"`
	// ObservedAt 是这次观测的真实时间（RFC3339，UTC）。
	ObservedAt string `json:"observedAt,omitempty"`
}

// PTCConfiguration 是 `cuaAccess/ptcConfiguration` 的 result（契约 :453-470）。
type PTCConfiguration struct {
	// Persistent：共享运行时是否保留内存状态（官方 `ptcRuntime.retainsState`）。
	Persistent bool `json:"persistent"`
	// Writable：profile 里是否有一个可编辑的 `ptc-runtime` 条目。
	Writable bool `json:"writable"`

	// —— 附加真话 ——
	EntryID    string `json:"entryId,omitempty"`
	EntryFound bool   `json:"entryFound"`
	// CapableWhen 只在 writable=false 时出现，说清"什么条件下能持久化"。
	CapableWhen string `json:"capableWhen,omitempty"`
}

// RunningApp 是本仓 **additive** 的运行中应用条目。
//
// 它不在官方 9 方法面内：官方把枚举放在 provider 的 native 协议
// （`protocol.ts:52-53` 的 listApps/findApps），方法面只有"已保存的决策"。
// 本仓在 Windows 上真能枚举（EnumWindows + 进程可执行路径），所以单独给一个方法，
// 而不是塞进 `applications` 的 result 里假装那是官方语义。
type RunningApp struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	ExecutablePath string `json:"executablePath,omitempty"`
	PID            int    `json:"pid"`
}

// Error 是带 HTTP 语义的结构化错误；transport 按 Status/Code 出四字段错误体，
// `CapableWhen` 作为 error 的兄弟字段出现（不改四字段契约）。
type Error struct {
	Status      int
	Code        string
	Message     string
	CapableWhen string
}

func (e *Error) Error() string { return e.Message }

func badRequest(format string, args ...any) *Error {
	return &Error{Status: http.StatusBadRequest, Code: "bad_request", Message: fmt.Sprintf(format, args...)}
}

func internalError(format string, args ...any) *Error {
	return &Error{Status: http.StatusInternalServerError, Code: "internal", Message: fmt.Sprintf(format, args...)}
}

// unavailable 是"这台机器/这个组合真的没有这项能力"的统一形状：
// 501 + code=unavailable + capableWhen（说清装什么、配什么、什么平台才会有）。
//
// 为什么不是 not_implemented：not_implemented 说的是"宿主这一侧还没接线"，
// 而这里说的是"官方在这个平台上没有 provider / 这个组合没有可写的 profile 条目"——
// 客户端据此给出的动作不同（前者等接线，后者去装/去配）。
func unavailable(capableWhen, format string, args ...any) *Error {
	return &Error{
		Status: http.StatusNotImplemented, Code: "unavailable",
		Message: fmt.Sprintf(format, args...), CapableWhen: capableWhen,
	}
}

func conflict(code, format string, args ...any) *Error {
	return &Error{Status: http.StatusConflict, Code: code, Message: fmt.Sprintf(format, args...)}
}

// validateApplicationID 校验官方 64 位小写十六进制身份摘要。
func validateApplicationID(id string) error {
	if !ApplicationIDPattern.MatchString(id) {
		return badRequest("id 必须是官方 CuaApplicationId（64 位小写十六进制原生身份摘要，收到 %q）", id)
	}
	return nil
}

// ApplicationIdentity 从一条规范可执行路径算出官方形状的应用身份（Grant）。
//
// 官方 id 是"provider 对平台应用元数据算的摘要"（`application-types.ts:5-6`）；
// 本仓拿不到 provider，就在同一台机器上用**规范可执行路径**算一个稳定摘要：
//
//	id = sha256(lower(canonical(path)))，十六进制小写
//
// 小写是必须的：Windows 路径大小写不敏感，`C:\A.exe` 与 `c:\a.exe` 是同一个应用，
// 不归一化就会给同一个 exe 编出两个身份，`revoke` 只撤掉其中一个。
func ApplicationIdentity(executablePath string) Grant {
	clean := CanonicalExecutablePath(executablePath)
	if clean == "" {
		// 空路径造不出身份：sha256("") 会给出一个"看起来合法"的 64 位摘要，
		// 那等于给所有没有路径的东西编了同一个身份（撤一个就撤掉全部）。
		return Grant{}
	}
	sum := sha256.Sum256([]byte(strings.ToLower(clean)))
	name := filepath.Base(clean)
	if name == "." || name == string(filepath.Separator) || name == "" {
		name = clean
	}
	return Grant{ID: hex.EncodeToString(sum[:]), Name: name, ExecutablePath: clean}
}

// CanonicalExecutablePath 把一条路径规范到"同一台机器上稳定"的形态。
//
// 先尽力解析符号链接/联接点（解析不了不算错：很多系统目录本来就解不开），
// 再取绝对路径并清理分隔符。**不去改大小写**——保留真实大小写给界面显示，
// 大小写归一化只发生在算摘要那一步（见 ApplicationIdentity）。
func CanonicalExecutablePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return filepath.Clean(path)
}
