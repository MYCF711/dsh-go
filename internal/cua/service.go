package cua

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// MaxApplicationIconChars 是官方 `ApplicationIcon` 的长度上限
// （`dsh-cua/src/protocol.ts:178`：`z.string().max(65536)`）。
//
// 超限的图标宁可**不给**也不截断：截断的 base64 不是图片，客户端拿到会当成
// 一张坏图去渲染，比"这个应用没图标"更糟。
const MaxApplicationIconChars = 65536

// IconProvider 提取可执行文件图标（官方 native `applicationIcons`）。
//
// 返回 map[可执行路径] = `data:image/png;base64,…`；给不出图标的路径直接不出现。
type IconProvider interface {
	Icons(paths []string) (map[string]string, error)
}

// SystemProber 观测平台权限/会话状态（官方 native `permissionStatus`）。
type SystemProber interface {
	Status() (Win32SystemStatus, error)
}

// SettingsOpener 打开权限设置页（官方 native `openPermissionSettings`）。
type SettingsOpener interface {
	Open(permission string) error
}

// RunningAppLister 枚举运行中应用（本仓 additive；官方把这步放在 provider 的
// native 协议 listApps/findApps，见 `dsh-cua/src/protocol.ts:52-53`）。
type RunningAppLister interface {
	Running() ([]RunningApp, error)
}

// Options 是 Service 的装配参数。
type Options struct {
	// HomeDir 是状态目录根（`<home>/state/cua-permissions.json`）。
	// 空 = 进程内状态：此时 `applications.persistentAvailable=false`，是**真话**。
	HomeDir string
	// 以下四个是平台观测/动作的注入点；nil 时用平台默认实现
	// （Windows 上是真实 Win32；其它平台明确"没有这项能力"）。
	Icons    IconProvider
	System   SystemProber
	Settings SettingsOpener
	Apps     RunningAppLister
	// PTC 是 PTC 执行状态注入点；nil = 本宿主没有 ptc-runtime 条目。
	PTC PTCProbe
	// Now 是时间源（测试可注入）。
	Now func() time.Time
}

// Service 是 `cuaAccess` 面在宿主里的实现。
//
// 它**不做**审批 UI/审批请求通道：那属于宿主既有的 approval 机制（`internal/approval`
// 与 Transport 的 approval 帧）。本类只负责官方方法面本身的四件事：
// 决策持久化（Store）、平台观测（SystemProber）、图标（IconProvider）、
// 设置页（SettingsOpener）——与官方 `CuaAccess`（`dsh-cua/src/access.ts:39-142`）
// 的职责划分一致：官方也是把决策交给 `ApplicationAccess`、把原生动作交给 control。
type Service struct {
	store    *Store
	ptc      PTCProbe
	icons    IconProvider
	system   SystemProber
	settings SettingsOpener
	apps     RunningAppLister
	now      func() time.Time
}

// NewService 造一个 cuaAccess 服务。
func NewService(options Options) *Service {
	service := &Service{
		store:    NewStore(stateDir(options.HomeDir)),
		ptc:      options.PTC,
		icons:    options.Icons,
		system:   options.System,
		settings: options.Settings,
		apps:     options.Apps,
		now:      options.Now,
	}
	if service.ptc == nil {
		service.ptc = NewPTCProbe(PTCProbeOptions{})
	}
	if service.icons == nil {
		service.icons = NewIconProvider()
	}
	if service.system == nil {
		service.system = NewSystemProber()
	}
	if service.settings == nil {
		service.settings = NewSettingsOpener()
	}
	if service.apps == nil {
		service.apps = NewRunningAppLister()
	}
	if service.now == nil {
		service.now = time.Now
	}
	return service
}

// stateDir 把 home 根映射到 `<home>/state`；空 home 保持空（进程内存储）。
func stateDir(homeDir string) string {
	homeDir = strings.TrimSpace(homeDir)
	if homeDir == "" {
		return ""
	}
	return filepath.Join(homeDir, "state")
}

// Store 暴露决策存储（工具路径与宿主接线用；9 方法面只读它的投影）。
func (s *Service) Store() *Store { return s.store }

// SystemStatus 是 `cuaAccess/systemStatus`（契约 :666-813）。
//
// 真话优先：这台机器有没有 CUA provider、helper 是谁、会话锁没锁、能不能抓屏、
// 是否提权，全部来自真实观测；没有 AX/截图 provider 这件事写在
// `providerAvailable=false` + `capableWhen` 里，而不是假装看得到 accessibility。
func (s *Service) SystemStatus(ctx context.Context) (Win32SystemStatus, error) {
	if err := ctxError(ctx); err != nil {
		return Win32SystemStatus{}, err
	}
	status, err := s.system.Status()
	if err != nil {
		return Win32SystemStatus{}, err
	}
	s.stamp(&status)
	return status, nil
}

// Applications 是 `cuaAccess/applications`（契约 :329-403）。
//
// 官方描述「Read fresh stored application decisions without issuing a native request」
// ——**不发原生请求**：这里只读持久化的决策，连图标都不在这里取（官方把图标
// 拆成 `applicationIcons` 正是为了不让权限页被图标拖慢）。
func (s *Service) Applications(ctx context.Context) (ApplicationState, error) {
	if err := ctxError(ctx); err != nil {
		return ApplicationState{}, err
	}
	return s.store.Status()
}

// ApplicationIcons 是 `cuaAccess/applicationIcons`（契约 :311-327）。
//
// 路径只从**已有决策**里来（官方 `access.ts:96-101`），没有 `executablePath`
// 的应用直接跳过；一条路径都没有时必须返回**空对象**，不许编默认图标。
func (s *Service) ApplicationIcons(ctx context.Context) (map[string]string, error) {
	if err := ctxError(ctx); err != nil {
		return nil, err
	}
	state, err := s.store.Status()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	paths := make([]string, 0, len(state.AllowedApps)+len(state.BlockedApps))
	for _, grant := range append(append([]Grant{}, state.AllowedApps...), state.BlockedApps...) {
		path := strings.TrimSpace(grant.ExecutablePath)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	if len(paths) == 0 {
		return map[string]string{}, nil
	}
	sort.Strings(paths)
	icons, err := s.icons.Icons(paths)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(icons))
	for path, icon := range icons {
		if icon == "" || len(icon) > MaxApplicationIconChars {
			continue
		}
		out[path] = icon
	}
	return out, nil
}

// RunningApplications 是本仓 **additive** 方法 `cuaAccess/runningApplications`。
//
// 不在官方 9 方法面内（官方把枚举放在 provider 的 native 协议里）；在这里提供，
// 是因为 Windows 上真能枚举——给出的是真数据，不是把 `applications` 的语义改掉。
func (s *Service) RunningApplications(ctx context.Context) ([]RunningApp, error) {
	if err := ctxError(ctx); err != nil {
		return nil, err
	}
	apps, err := s.apps.Running()
	if err != nil {
		return nil, err
	}
	if apps == nil {
		apps = []RunningApp{}
	}
	return apps, nil
}

// OpenSettings 是 `cuaAccess/openSettings`（契约 :423-443）。
//
// 官方描述是"打开用户选中的 macOS 隐私面板"；本仓在 Windows 上打开与该权限
// **对应**的系统设置页（见 settings_windows.go 的映射与理由）。
// 返回值语义照官方：**只表示系统接受了打开请求**，不表示权限已授予。
func (s *Service) OpenSettings(ctx context.Context, permission, locale string) error {
	if err := ctxError(ctx); err != nil {
		return err
	}
	if err := validatePermission(permission); err != nil {
		return err
	}
	// locale 是官方给 provider 落的本地化文案用的（契约 :430-431）。本仓没有
	// provider 侧文案，不因为 locale 改变行为——**故意忽略**而不是假装做了本地化。
	_ = locale
	return s.settings.Open(permission)
}

// RequestPermission 是 `cuaAccess/requestPermission`（契约 :472-624）。
//
// 官方语义：「requesting does not establish consent」——请求**不**建立同意。
// 本仓在 Windows 上没有可弹的同意 UI（没有 provider），所以这里做的是
// **真实再观测**：返回 win32 分支的真话，并显式带上
// `providerAvailable=false` / `consentEstablished=false` / `capableWhen`，
// 绝不谎称"已经问过用户"。
func (s *Service) RequestPermission(ctx context.Context, permission, locale string) (Win32SystemStatus, error) {
	if err := ctxError(ctx); err != nil {
		return Win32SystemStatus{}, err
	}
	if err := validatePermission(permission); err != nil {
		return Win32SystemStatus{}, err
	}
	_ = locale
	status, err := s.system.Status()
	if err != nil {
		return Win32SystemStatus{}, err
	}
	status.RequestedPermission = permission
	status.ConsentEstablished = false
	s.stamp(&status)
	return status, nil
}

// PTCConfiguration 是 `cuaAccess/ptcConfiguration`（契约 :445-470）。
func (s *Service) PTCConfiguration(ctx context.Context) (PTCConfiguration, error) {
	if err := ctxError(ctx); err != nil {
		return PTCConfiguration{}, err
	}
	return s.ptc.Configuration(), nil
}

// EnablePersistentPTC 是 `cuaAccess/enablePersistentPtc`（契约 :405-421）。
//
// 官方：已经持久化的运行时**原样不动**（`access.ts:68` 的早返回）；
// 没有可编辑条目时抛错（`:71`）。本仓照此：成功返回 nil，做不到返回 501 + capableWhen。
func (s *Service) EnablePersistentPTC(ctx context.Context) error {
	if err := ctxError(ctx); err != nil {
		return err
	}
	if s.ptc.Configuration().Persistent {
		return nil
	}
	return s.ptc.Enable()
}

// Revoke 是 `cuaAccess/revoke`（契约 :646-664）。
func (s *Service) Revoke(ctx context.Context, id string) error {
	if err := ctxError(ctx); err != nil {
		return err
	}
	return s.store.Revoke(id)
}

// RetryApproval 是 `cuaAccess/retryApproval`（契约 :626-644）。
func (s *Service) RetryApproval(ctx context.Context, id string) error {
	if err := ctxError(ctx); err != nil {
		return err
	}
	return s.store.RetryApproval(id)
}

// stamp 补上观测时间（provider 自报时间就尊重它，不自作主张覆盖）。
func (s *Service) stamp(status *Win32SystemStatus) {
	if status.ObservedAt == "" {
		status.ObservedAt = s.now().UTC().Format(time.RFC3339)
	}
}

// ctxError 把"调用方已经取消"变成带语义的错误，而不是让它静默变成一次成功。
func ctxError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return internalError("请求已取消：%v", err)
	}
	return nil
}

// validatePermission 校验官方 CuaSystemPermission 的两个值
// （`dsh-cua/src/access-types.ts:6`）。
func validatePermission(permission string) error {
	switch permission {
	case "accessibility", "screenRecording":
		return nil
	}
	return badRequest("permission 只能是 accessibility / screenRecording（官方 CuaSystemPermission，收到 %q）", permission)
}

// cuaProviderCapableWhen 是"这台机器没有 CUA provider"的统一补救说明。
//
// 它必须能回答"那我该怎么办"：官方 rc.2 在 Windows 上没有任何 CUA provider，
// 所以答案不是"重试"，而是"装/等一个提供 AX 与截图的平台 provider"。
const cuaProviderCapableWhen = "当平台提供 dsh-cua 原生 provider（官方 macOS 包 " +
	"@deepseek-ai/dsh-experimental-cua-macos，rc.2 的 dsh-cua-bundle 只在 darwin 挂载：" +
	"cordis.patch.yml:5-8）并随包提供原生 helper 时，accessibility/screenRecording、" +
	"AX 读树与截图才存在；官方 Windows 安装里没有任何 CUA provider"
