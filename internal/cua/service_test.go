package cua

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeIcons struct {
	calls int
	got   []string
	out   map[string]string
	err   error
}

func (f *fakeIcons) Icons(paths []string) (map[string]string, error) {
	f.calls++
	f.got = append([]string{}, paths...)
	return f.out, f.err
}

type fakeSystem struct {
	calls  int
	status Win32SystemStatus
	err    error
}

func (f *fakeSystem) Status() (Win32SystemStatus, error) {
	f.calls++
	return f.status, f.err
}

type fakeSettings struct {
	opened []string
	err    error
}

func (f *fakeSettings) Open(permission string) error {
	f.opened = append(f.opened, permission)
	return f.err
}

type fakeApps struct {
	apps []RunningApp
	err  error
}

func (f *fakeApps) Running() ([]RunningApp, error) { return f.apps, f.err }

type fakePTC struct {
	cfg     PTCConfiguration
	enabled int
	err     error
}

func (f *fakePTC) Configuration() PTCConfiguration { return f.cfg }
func (f *fakePTC) Enable() error {
	f.enabled++
	return f.err
}

func win32Status() Win32SystemStatus {
	locked := false
	return Win32SystemStatus{
		Platform: "win32", SessionLocked: &locked, CaptureAvailable: true, Elevated: false,
		Helper:            ProcessInfo{PID: 4242, Name: "dshd.exe", Path: `C:\dsh\dshd.exe`},
		ProviderAvailable: false,
		HelperRole:        "host",
		CapableWhen:       cuaProviderCapableWhen,
	}
}

func TestServiceApplicationsUsesStoredDecisionsOnly(t *testing.T) {
	icons := &fakeIcons{out: map[string]string{}}
	service := NewService(Options{HomeDir: t.TempDir(), Icons: icons})
	always := demoGrant("always")
	denied := demoGrant("denied")

	if err := service.Store().GrantAlways(always); err != nil {
		t.Fatalf("GrantAlways: %v", err)
	}
	if err := service.Store().Decide("s1", denied, DecisionDeny); err != nil {
		t.Fatalf("Decide: %v", err)
	}

	state, err := service.Applications(context.Background())
	if err != nil {
		t.Fatalf("Applications: %v", err)
	}
	if len(state.AllowedApps) != 1 || len(state.BlockedApps) != 1 {
		t.Fatalf("决策投影不对：%+v", state)
	}
	if icons.calls != 0 {
		t.Fatal("applications 是「不发原生请求」的读（官方描述），不该顺手取图标")
	}
}

func TestServiceApplicationIconsSkipsAppsWithoutPath(t *testing.T) {
	icons := &fakeIcons{out: map[string]string{`C:\Apps\with-path\with-path.exe`: "data:image/png;base64,AAAA"}}
	service := NewService(Options{HomeDir: t.TempDir(), Icons: icons})
	withPath := demoGrant("with-path")
	withoutPath := ApplicationIdentity(`C:\Apps\no-path.exe`)
	withoutPath.ExecutablePath = ""

	if err := service.Store().GrantAlways(withPath); err != nil {
		t.Fatalf("GrantAlways: %v", err)
	}
	if err := service.Store().Decide("s1", withoutPath, DecisionDeny); err != nil {
		t.Fatalf("Decide: %v", err)
	}

	got, err := service.ApplicationIcons(context.Background())
	if err != nil {
		t.Fatalf("ApplicationIcons: %v", err)
	}
	if len(got) != 1 || got[withPath.ExecutablePath] != "data:image/png;base64,AAAA" {
		t.Fatalf("图标结果不对：%+v", got)
	}
	if len(icons.got) != 1 || icons.got[0] != withPath.ExecutablePath {
		t.Fatalf("没有 executablePath 的应用必须被跳过，实际请求了 %+v", icons.got)
	}
}

func TestServiceApplicationIconsWithoutPathsIsEmptyObject(t *testing.T) {
	icons := &fakeIcons{}
	service := NewService(Options{Icons: icons}) // 进程内空存储
	got, err := service.ApplicationIcons(context.Background())
	if err != nil {
		t.Fatalf("ApplicationIcons: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("没有路径时应返回空对象（不是 nil），得到 %#v", got)
	}
	if icons.calls != 0 {
		t.Fatal("没有路径时不该调用平台取图标")
	}
}

func TestServiceApplicationIconsDropsOversize(t *testing.T) {
	huge := "data:image/png;base64," + strings.Repeat("A", MaxApplicationIconChars)
	icons := &fakeIcons{out: map[string]string{demoGrant("huge").ExecutablePath: huge}}
	service := NewService(Options{HomeDir: t.TempDir(), Icons: icons})
	if err := service.Store().GrantAlways(demoGrant("huge")); err != nil {
		t.Fatalf("GrantAlways: %v", err)
	}
	got, err := service.ApplicationIcons(context.Background())
	if err != nil {
		t.Fatalf("ApplicationIcons: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("超过官方 65536 上限的图标必须丢掉（截断的 base64 不是图片），得到 %d 项", len(got))
	}
}

func TestServiceSystemStatusIsObservationNotClaim(t *testing.T) {
	system := &fakeSystem{status: win32Status()}
	service := NewService(Options{System: system})
	got, err := service.SystemStatus(context.Background())
	if err != nil {
		t.Fatalf("SystemStatus: %v", err)
	}
	if got.Platform != "win32" || got.Helper.PID != 4242 {
		t.Fatalf("观测没透传：%+v", got)
	}
	if got.ProviderAvailable {
		t.Fatal("rc.2 在 Windows 上没有 CUA provider，providerAvailable 不许为 true")
	}
	if got.CapableWhen == "" {
		t.Fatal("没有 provider 时必须给出 capableWhen（补救方式）")
	}
	if got.ObservedAt == "" {
		t.Fatal("观测时间应被补上")
	}
	if got.RequestedPermission != "" || got.ConsentEstablished {
		t.Fatalf("systemStatus 不该带请求权限字段：%+v", got)
	}
}

func TestServiceSystemStatusPropagatesUnavailable(t *testing.T) {
	want := unavailable(cuaProviderCapableWhen, "没有观测实现")
	service := NewService(Options{System: &fakeSystem{err: want}})
	if _, err := service.SystemStatus(context.Background()); err == nil {
		t.Fatal("观测失败必须报错，不许返回零值假成功")
	} else {
		var typed *Error
		if !errors.As(err, &typed) || typed.Status != 501 || typed.CapableWhen == "" {
			t.Fatalf("应把 501 + capableWhen 透出去，得到 %v", err)
		}
	}
}

func TestServiceOpenSettingsValidatesAndForwards(t *testing.T) {
	settings := &fakeSettings{}
	service := NewService(Options{Settings: settings})

	for _, permission := range []string{"accessibility", "screenRecording"} {
		if err := service.OpenSettings(context.Background(), permission, "zh-CN"); err != nil {
			t.Fatalf("OpenSettings(%s): %v", permission, err)
		}
	}
	if len(settings.opened) != 2 {
		t.Fatalf("应把两次请求都转给平台打开器，得到 %+v", settings.opened)
	}
	for _, bad := range []string{"", "screenCapture", "Accessibility", "camera"} {
		if err := service.OpenSettings(context.Background(), bad, "en"); err == nil {
			t.Fatalf("OpenSettings(%q) 应被拒（官方枚举只有 accessibility/screenRecording）", bad)
		}
	}
}

func TestServiceRequestPermissionDoesNotEstablishConsent(t *testing.T) {
	system := &fakeSystem{status: win32Status()}
	service := NewService(Options{HomeDir: t.TempDir(), System: system})

	got, err := service.RequestPermission(context.Background(), "screenRecording", "ja")
	if err != nil {
		t.Fatalf("RequestPermission: %v", err)
	}
	if got.RequestedPermission != "screenRecording" {
		t.Fatalf("结果里应带上被请求的权限（可审计），得到 %q", got.RequestedPermission)
	}
	if got.ConsentEstablished {
		t.Fatal("请求权限不建立同意（官方描述），consentEstablished 不许为 true")
	}
	if got.Platform != "win32" {
		t.Fatalf("结果必须是平台标记的观测，得到 %+v", got)
	}
	state, err := service.Applications(context.Background())
	if err != nil {
		t.Fatalf("Applications: %v", err)
	}
	if len(state.AllowedApps) != 0 || len(state.BlockedApps) != 0 {
		t.Fatal("请求系统权限不该写任何应用决策")
	}
	if _, err := service.RequestPermission(context.Background(), "camera", "en"); err == nil {
		t.Fatal("非法 permission 应被拒")
	}
}

func TestServicePTCConfigurationTruthfulWithoutRuntime(t *testing.T) {
	service := NewService(Options{})
	cfg, err := service.PTCConfiguration(context.Background())
	if err != nil {
		t.Fatalf("PTCConfiguration: %v", err)
	}
	if cfg.Persistent || cfg.Writable {
		t.Fatalf("本宿主没有 ptc-runtime 条目，两个字段都必须是 false（真话）：%+v", cfg)
	}
	if cfg.CapableWhen == "" {
		t.Fatal("writable=false 时要说清什么条件下能持久化")
	}
	err = service.EnablePersistentPTC(context.Background())
	if err == nil {
		t.Fatal("没有可写条目时 enablePersistentPtc 必须报错，不许静默成功")
	}
	var typed *Error
	if !errors.As(err, &typed) || typed.Status != 501 || typed.CapableWhen == "" {
		t.Fatalf("应为 501 + capableWhen，得到 %v", err)
	}
}

// ptcR6Keywords 是"没有 PTC 运行时"这一族错误体里**必须同时出现**的关键词。
//
// 这里刻意写**字面量**而不是引用生产常量：生产常量改名时测试必须红，
// 否则断言就退化成"自己和自己比"，"点名 R6 与两个官方模块"的要求会静默失效。
func ptcR6Keywords() []string {
	return []string{"R6", "不嵌 JS 运行时", "@deepseek-ai/dsh-ptc-runtime-node", "@deepseek-ai/dsh-tool-executor-ptc"}
}

// assertPTCBlockerNamesR6AndModules 钉住三件事同一段里都说了：
// (a) R6「不嵌 JS 运行时」是设计约束；(b) 缺 #74/#94 两个官方模块；
// (c) 官方默认同样是关着的（file:line 可核）。
func assertPTCBlockerNamesR6AndModules(t *testing.T, what, text string) {
	t.Helper()
	for _, want := range ptcR6Keywords() {
		if !strings.Contains(text, want) {
			t.Fatalf("%s 必须点名 %q（R6 与缺的两个官方模块都要出现）：%s", what, want, text)
		}
	}
	for _, want := range []string{"cordis.patch.yml:521", "disabled: true"} {
		if !strings.Contains(text, want) {
			t.Fatalf("%s 必须说明官方默认也关着（缺 %q）：%s", what, want, text)
		}
	}
}

// TestServicePTCConfigurationReadsRealStateWithoutEntry 钉住只读探针**如实读**：
// 没有 ptc-runtime 条目时返回的是真读到的状态（EntryID/EntryFound/Persistent/Writable），
// 不是编的，也**不是一律报错**——官方 `ptcConfiguration` 就是只读探针
// （`dsh-cua/src/access.ts:55-61`），必须能读到"没装配"这个事实本身。
func TestServicePTCConfigurationReadsRealStateWithoutEntry(t *testing.T) {
	service := NewService(Options{})
	cfg, err := service.PTCConfiguration(context.Background())
	if err != nil {
		t.Fatalf("只读探针不该因为没有 ptc-runtime 条目而报错（读不到就如实报未装配）：%v", err)
	}
	if cfg.EntryID != PTCEntryID {
		t.Fatalf("条目 id 必须报官方 id %q，得到 %q", PTCEntryID, cfg.EntryID)
	}
	if cfg.EntryFound {
		t.Fatalf("没有条目时 entryFound 必须是 false（真话）：%+v", cfg)
	}
	if cfg.Persistent || cfg.Writable {
		t.Fatalf("没有 ptc-runtime 条目时必须如实报 persistent=false/writable=false：%+v", cfg)
	}
	if cfg.CapableWhen == "" {
		t.Fatal("writable=false 时必须给出 capableWhen（说清被什么拦住、缺什么）")
	}
	assertPTCBlockerNamesR6AndModules(t, "capableWhen", cfg.CapableWhen)
}

// TestProfilePTCProbeEnableUnavailableNamesR6AndMissingModules 钉住写路径的形状与文案：
// 码仍是既有的 501 + unavailable（错误码契约不许动），message 与 capableWhen
// **同时**点名 R6、不嵌 JS 运行时、#74/#94 两个官方模块，以及官方默认 disabled: true。
func TestProfilePTCProbeEnableUnavailableNamesR6AndMissingModules(t *testing.T) {
	probe := NewPTCProbe(PTCProbeOptions{})
	err := probe.Enable()
	var typed *Error
	if !errors.As(err, &typed) {
		t.Fatalf("没有 PTC 运行时必须返回结构化错误（不是含糊的 error），得到 %v", err)
	}
	if typed.Status != 501 || typed.Code != "unavailable" {
		t.Fatalf("码仍是既有的 501 + unavailable，得到 %d/%s", typed.Status, typed.Code)
	}
	assertPTCBlockerNamesR6AndModules(t, "message", typed.Message)
	assertPTCBlockerNamesR6AndModules(t, "capableWhen", typed.CapableWhen)
	if !strings.Contains(typed.Message, PTCEntryID) {
		t.Fatalf("message 要说清找的是哪个 profile 条目：%s", typed.Message)
	}
}

func TestServiceEnablePersistentPTCNoOpWhenAlreadyPersistent(t *testing.T) {
	probe := &fakePTC{cfg: PTCConfiguration{Persistent: true, Writable: true}}
	service := NewService(Options{PTC: probe})
	if err := service.EnablePersistentPTC(context.Background()); err != nil {
		t.Fatalf("已持久化时应 no-op 成功: %v", err)
	}
	if probe.enabled != 0 {
		t.Fatal("已持久化时不该再次写配置（官方：already-persistent runtimes are left intact）")
	}
}

func TestProfilePTCProbeWritesThroughInjectedEditor(t *testing.T) {
	entries := []PTCEntry{{ID: PTCEntryID, Config: map[string]any{"persistent": false}}}
	var written map[string]any
	probe := NewPTCProbe(PTCProbeOptions{
		Entries: func() []PTCEntry { return entries },
		Write: func(_ PTCEntry, values map[string]any) error {
			written = values
			entries = []PTCEntry{{ID: PTCEntryID, Config: values}}
			return nil
		},
	})
	cfg := probe.Configuration()
	if !cfg.Writable || cfg.Persistent {
		t.Fatalf("有条目 + 有写入通道 = 可写但未持久化，得到 %+v", cfg)
	}
	if err := probe.Enable(); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if written["persistent"] != true {
		t.Fatalf("应写入持久化标记，实际写入 %+v", written)
	}
	if again := probe.Configuration(); !again.Persistent {
		t.Fatalf("写完之后应读到 persistent=true，得到 %+v", again)
	}
	if err := probe.Enable(); err != nil {
		t.Fatalf("已持久化后 Enable 应为 no-op: %v", err)
	}
}

func TestProfilePTCProbeHonoursRetainsStateKey(t *testing.T) {
	// 官方运行时叫 retainsState、配置文件里叫 persistent；两个都认，
	// 否则换装配时会把"已经持久化"读成 false 并重复写配置。
	probe := NewPTCProbe(PTCProbeOptions{
		Entries: func() []PTCEntry {
			return []PTCEntry{{ID: PTCEntryID, Config: map[string]any{"retainsState": true}}}
		},
	})
	if !probe.Configuration().Persistent {
		t.Fatal("retainsState=true 应被读成已持久化")
	}
}

func TestServiceRunningApplicationsPassesThrough(t *testing.T) {
	apps := &fakeApps{apps: []RunningApp{{ID: demoGrant("x").ID, Name: "x", PID: 7}}}
	service := NewService(Options{Apps: apps})
	got, err := service.RunningApplications(context.Background())
	if err != nil {
		t.Fatalf("RunningApplications: %v", err)
	}
	if len(got) != 1 || got[0].PID != 7 {
		t.Fatalf("枚举结果没透传：%+v", got)
	}
}

func TestServiceRevokeAndRetryChangeStoredState(t *testing.T) {
	service := NewService(Options{HomeDir: t.TempDir()})
	grant := demoGrant("roundtrip")
	if err := service.Store().Decide("s1", grant, DecisionDeny); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if err := service.Revoke(context.Background(), grant.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	state, err := service.Applications(context.Background())
	if err != nil {
		t.Fatalf("Applications: %v", err)
	}
	if len(state.BlockedApps) != 1 {
		t.Fatalf("撤销后该应用应仍在 blockedApps 里：%+v", state)
	}
	if err := service.RetryApproval(context.Background(), grant.ID); err != nil {
		t.Fatalf("RetryApproval: %v", err)
	}
	state, err = service.Applications(context.Background())
	if err != nil {
		t.Fatalf("Applications: %v", err)
	}
	if len(state.BlockedApps) != 0 {
		t.Fatalf("重试后不该再算 blocked：%+v", state)
	}
}
