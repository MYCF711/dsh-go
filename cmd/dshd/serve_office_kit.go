package main

// RC2-E7：官方 0.2.2-rc.2 的 `officeKit.*` 面在产品里的宿主接线（只接线，不重写实现）。
//
// 分工：
//   - 引擎发现 / 转换 / 重算 / 光栅 / 并发排队 / 产物仓：internal/officekit（C4 定稿）
//   - 方法分派 + 501/capableWhen 信封：internal/transport/handlers_office_kit_rc2.go
//   - **本文件**：把 transport.OfficialOfficeKitAPI 接到真 provider。
//     适配器形状与错误码表在 handlers_office_kit_rc2_test.go 的
//     `realOfficeKitAdapter` / `toOfficeKitAPIError` 里已经跑通过端到端，这里原样搬成
//     产品代码，只多两件产品才需要的事：① 会话工作区里的相对 path 解析；
//     ② 进程内共享 provider（工具面与 Transport 面必须同一份引擎状态）。
//
// 为什么必须是同一个 provider：officekit.Provider 自己握着引擎句柄、准入队列
// （maxConcurrentConversions=2 / maxBackgroundConversions=1）、产物仓与 generation。
// 建两份 = 两套并发槽（实际并发翻倍）、两个 generation（UI 缓存失效判定错乱）、
// 两个产物仓（保留期淘汰各算各的）。所以 `sharedOfficeKitProvider` 是唯一入口。

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"

	"dshgo/internal/officekit"
	"dshgo/internal/officepdf"
	"dshgo/internal/transport"
)

// officeKitAPI 是 transport.OfficialOfficeKitAPI 的产品实现。
type officeKitAPI struct {
	provider *officekit.Provider
	// home 是相对 path 的兜底解析基准（与旧 officeToPdf 面同语义：
	// <home>/workspaces/<scope>/<path>）。空 = 只接受绝对路径。
	home string
	// cwd 把 workspaceFileScopeId（本仓就是会话 id）解析成该会话的工作目录。
	// serve 注入 src.Cwd：GUI 传的 path 是**工作区相对**的，基准必须是会话 cwd，
	// 不能是宿主进程 cwd（多会话可能属于不同工作区）。nil 时退回 home 兜底。
	cwd func(scope string) (string, error)
}

// newOfficeKitAPI 按 C4 报告 §7.4 的形状构造适配器。
//
// home 是可选参数：`newOfficeKitAPI(provider)` 就是报告里的形状（只处理绝对路径）；
// serve 传入 home 后，相对 path 才能按 <home>/workspaces/<scope> 解析。
func newOfficeKitAPI(provider *officekit.Provider, home ...string) *officeKitAPI {
	api := &officeKitAPI{provider: provider}
	if len(home) > 0 {
		api.home = strings.TrimSpace(home[0])
	}
	return api
}

// withSessionCwd 注入会话工作目录解析器（serve 传 src.Cwd），返回自身便于链式装配。
func (a *officeKitAPI) withSessionCwd(cwd func(scope string) (string, error)) *officeKitAPI {
	a.cwd = cwd
	return a
}

// newOfficePDFAPIWith 让旧 officeToPdf 面复用**同一个** provider：
// 同一引擎、同一准入队列、同一 generation（officepdf.Wrap 只换适配层，不新建引擎）。
func newOfficePDFAPIWith(home string, kit *officekit.Provider) *officePDFAPI {
	return &officePDFAPI{provider: officepdf.Wrap(home, kit)}
}

// Generation 报 provider 生命周期标识与能力（无引擎时给真值 officekit-unavailable，
// 不编造一个能用的 generation）。
func (a *officeKitAPI) Generation(context.Context) (string, transport.OfficialOfficeKitCapability, error) {
	capability := a.provider.Capability()
	return capability.Generation, transport.OfficialOfficeKitCapability{
		Available: capability.Available, Generation: capability.Generation,
		EngineVersion: capability.EngineVersion, EngineRoot: capability.EngineRoot,
		Executable: capability.Executable, ProgramDirectory: capability.ProgramDirectory,
		Source: capability.Source, Tried: capability.Tried, CapableWhen: capability.CapableWhen,
	}, nil
}

// Render 把 Office 文档真转成 PDF（结果形状 = 官方 RenderedDocumentBytes）。
func (a *officeKitAPI) Render(ctx context.Context, scope, path, priority string) (transport.OfficialOfficePDFResult, error) {
	absolute, err := a.absolutePath(scope, path)
	if err != nil {
		return transport.OfficialOfficePDFResult{}, toOfficeKitAPIError(err)
	}
	doc, err := a.provider.Render(ctx, absolute, priority)
	if err != nil {
		return transport.OfficialOfficePDFResult{}, toOfficeKitAPIError(err)
	}
	fonts := doc.MissingFonts
	if fonts == nil {
		fonts = []string{}
	}
	return transport.OfficialOfficePDFResult{MissingFonts: fonts, Generation: doc.Generation, Offset: doc.Offset,
		Data: doc.Data, Eof: doc.Eof, AbsolutePath: doc.AbsolutePath, Version: doc.Version, Bytes: doc.Bytes}, nil
}

// absolutePath 把 (workspaceFileScopeId, path) 归一成引擎要的绝对路径。
//
// 优先级：绝对路径原样用 → 会话 cwd（src.Cwd）→ <home>/workspaces/<scope>。
// 相对路径解析后必须仍在基准目录内：`..` 逃逸直接按 invalid-request 拒，
// 与「workspaceFileScope 限定读取范围」的语义一致。
func (a *officeKitAPI) absolutePath(scope, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", &officekit.Error{Code: officekit.CodeInvalidRequest, Message: "path 是空串"}
	}
	if filepath.IsAbs(raw) {
		return filepath.Clean(raw), nil
	}
	var base string
	if a.cwd != nil && strings.TrimSpace(scope) != "" {
		if dir, err := a.cwd(scope); err == nil {
			base = strings.TrimSpace(dir)
		}
	}
	if base == "" && a.home != "" {
		base = filepath.Join(a.home, "workspaces")
		if strings.TrimSpace(scope) != "" {
			base = filepath.Join(base, scope)
		}
	}
	if base == "" {
		absolute, err := filepath.Abs(raw)
		if err != nil {
			return "", &officekit.Error{Code: officekit.CodeInvalidRequest, Message: "路径无法解析：" + err.Error()}
		}
		return absolute, nil
	}
	resolved := filepath.Join(base, raw)
	rel, err := filepath.Rel(base, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", &officekit.Error{Code: officekit.CodeInvalidRequest,
			Message: "path 越出工作区范围：" + raw, Detail: "基准目录 " + base}
	}
	return resolved, nil
}

// toOfficeKitAPIError 是 C4 报告 §4 错误码表的实现（与测试里的同形代码逐行一致）：
//
//	unavailable → 501（附 capableWhen，UI 能给出「去装/去配」的动作）
//	invalid-* / unsupported-format / too-large / invalid-output / source-changed → 400
//	timeout → 504、busy → 429、其余 → 502
func toOfficeKitAPIError(err error) error {
	var kitErr *officekit.Error
	if !errors.As(err, &kitErr) {
		return &transport.OfficeKitAPIError{Status: 502, Code: "failed", Message: err.Error()}
	}
	status := 502
	code := string(kitErr.Code)
	switch kitErr.Code {
	case officekit.CodeUnavailable:
		status = 501
	case officekit.CodeInvalidRequest, officekit.CodeInvalidDocument, officekit.CodeUnsupportedFormat,
		officekit.CodeInputTooLarge, officekit.CodeOutputTooLarge, officekit.CodeInvalidOutput,
		officekit.CodeSourceChanged:
		status = 400
	case officekit.CodeTimeout:
		status = 504
	case officekit.CodeBusy:
		status = 429
	}
	return &transport.OfficeKitAPIError{Status: status, Code: code, Message: kitErr.Error(), CapableWhen: kitErr.CapableWhen}
}

// ---------------------------------------------------------------------------
// 进程内共享 provider：Transport 面与工具面用同一份引擎/队列/generation
// ---------------------------------------------------------------------------

var sharedOfficeKit = struct {
	mu      sync.Mutex
	byHome  map[string]*officekit.Provider
	current *officekit.Provider
}{byHome: map[string]*officekit.Provider{}}

// shareOfficeKitProvider 把本进程唯一的 provider 登记为共享实例（serve 起来后立刻登记）。
//
// 登记这一个动作就是「避免两套引擎状态」的实现：之后同一进程里任何
// `sharedOfficeKitProvider(home)` 都拿到这一份。
func shareOfficeKitProvider(home string, provider *officekit.Provider) {
	if provider == nil {
		return
	}
	sharedOfficeKit.mu.Lock()
	defer sharedOfficeKit.mu.Unlock()
	sharedOfficeKit.current = provider
	if key := strings.TrimSpace(home); key != "" {
		sharedOfficeKit.byHome[key] = provider
	}
}

// sharedOfficeKitProvider 返回共享 provider：已登记过就用那一份，否则按 home 建一份
// 并登记。`run`/`tools`/`exec`/`acp` 这些进程里由工具面（builtinModules）调它；
// serve 进程里则命中 cmdServe 登记的那一份。
func sharedOfficeKitProvider(home string) *officekit.Provider {
	key := strings.TrimSpace(home)
	sharedOfficeKit.mu.Lock()
	defer sharedOfficeKit.mu.Unlock()
	if key != "" {
		if provider := sharedOfficeKit.byHome[key]; provider != nil {
			return provider
		}
	} else if sharedOfficeKit.current != nil {
		return sharedOfficeKit.current
	}
	options := officekit.Options{Home: key}
	if key != "" {
		options.OutputDirectory = filepath.Join(key, "office-kit", "outputs")
	}
	provider := officekit.Open(options)
	if key != "" {
		sharedOfficeKit.byHome[key] = provider
	} else {
		sharedOfficeKit.current = provider
	}
	return provider
}

var _ transport.OfficialOfficeKitAPI = (*officeKitAPI)(nil)
