// Package official 把 `internal/pluginmgr` 的**真实**状态接到官方
// `pluginManager.*`（0.2.2-rc.1）的 L1 契约上。
//
// 为什么它是一个独立包，而不是塞进 `internal/transport` 或 `cmd/dshd`：
//
//   - `internal/transport` 的既有约定是"只做协议翻译，宿主状态靠注入"
//     （见 handlers_agent_official.go 顶部）。把 pluginmgr 的磁盘/事务知识
//     写进 Transport 会破坏那条边界；
//   - `cmd/dshd` 不在本次写作用域内，且放那里就无法用真实管理器做端到端测试；
//   - 于是这一层只做一件事：把官方契约与真实管理器**双向**映射，并让
//     `Cmd/dshd` 的接线退化成一行 `official.New(pluginManager)`。
//
// 真实性边界（每条都在 adapter_test.go 里有真实磁盘断言）：
//
//   - 装/卸走 `pluginmgr` 的既有事务（journal + staging rename + 回滚）；
//   - 启用位与安装写在同一份 `state.json`，重开管理器后仍在；
//   - 取消只在事务检查点生效，收工后 `state.json` 与 `packages/` 都回到原样；
//   - 需要 registry/pnpm/git/tarball 的形态一律 `transport.ErrOfficialNotImplemented`，
//     绝不假装装了一个下载不到的包；
//   - 兼容性模型与版本豁免是真的：`setVersionExemption` 把授权/撤销写进管理器
//     root 下的 `compatibility.json`（与官方同名同形状），install/update 在装之前
//     按 `engines.dsh` / peerDependencies 判一次，不兼容且没有精确豁免就拒装。
package official

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"dshgo/internal/pluginmgr"
	"dshgo/internal/transport"
)

// adapter 把官方契约翻译到真实管理器。它没有自己的状态：每次调用都读
// `pluginmgr` 的当前真实状态。
type adapter struct {
	manager *pluginmgr.Manager
}

// New 返回官方 pluginManager 的真实实现集合。
//
// `SetVersionExemption` 走 `pluginmgr.Manager.SetVersionExemption`：真实持久化
// （compatibility.json，重开管理器后仍可读），并被 install/update 的兼容性闸门
// 消费——这正是"豁免"在官方语义里的作用，不是一份没人读的死数据。
func New(manager *pluginmgr.Manager) transport.OfficialPluginManagerAPI {
	if manager == nil {
		return transport.OfficialPluginManagerAPI{}
	}
	a := &adapter{manager: manager}
	return transport.OfficialPluginManagerAPI{
		Inspect:             a.inspect,
		ListVersions:        a.listVersions,
		InstallBundle:       a.installBundle,
		UpdateBundle:        a.updateBundle,
		RemoveBundle:        a.removeBundle,
		SetBundleEnabled:    a.setEnabled,
		SetPluginEnabled:    a.setEnabled,
		SetVersionExemption: a.setVersionExemption,
		// 读与写是同一份 compatibility.json：listVersionExemptions 是
		// setVersionExemption 的读侧，不是另一份状态。
		ListVersionExemptions: a.listVersionExemptions,
		Registries:            a.registries,
		ReadBundleDetail:      a.readBundleDetail,
		WaitForInstall:        a.waitForInstall,
		CancelInstall:         a.cancelInstall,
	}
}

// docCandidates 是包内长文档的候选名（官方先看 locale 的 meta.detail，再看
// README；本仓 manifest 没有 meta.detail，所以只有 README 这一档）。
var docCandidates = []string{"README.md", "readme.md", "Readme.md", "README.markdown"}

// readmePrefix 是"不是 README"的判定：官方 likewise 只在没有 locale 文档时
// 才回落到 README，回落到 README 时 origin 就是 `readme`。

func (a *adapter) inspect(p transport.OfficialInspectParams) (transport.OfficialPluginSpecInspection, error) {
	spec := strings.TrimSpace(p.Spec)
	kind := pluginmgr.SourceKind(spec)
	switch kind {
	case "path":
		if _, err := os.Stat(spec); err != nil {
			return transport.OfficialInspectionRefused("not-a-package", fmt.Sprintf("路径不存在或不可读: %v", err)), nil
		}
		manifest, err := pluginmgr.ValidateSource(spec)
		if err != nil {
			return transport.OfficialInspectionRefused("not-a-package", err.Error()), nil
		}
		return a.inspectManifest(manifest, "path")
	case "registry", "tarball":
		local, resolved, err := a.materializeRegistrySpec(spec, p.Registry)
		if err != nil {
			return registryInspectionRefused(spec, err), nil
		}
		manifest, err := pluginmgr.ValidateSource(local)
		if err != nil {
			return transport.OfficialInspectionRefused("not-a-package", err.Error()), nil
		}
		// registry 的精确版本必须与 packument 一致；不一致是协议失败，不能
		// 把 tarball 内容当成另一个版本。
		if kind == "registry" && resolved.Version != "" && manifest.Version != resolved.Version {
			return transport.OfficialInspectionRefused("not-a-package",
				fmt.Sprintf("tarball manifest 版本 %s 与 registry 解析版本 %s 不一致", manifest.Version, resolved.Version)), nil
		}
		return a.inspectManifest(manifest, kind)
	default:
		return transport.OfficialInspectionRefused("not-a-package",
			fmt.Sprintf("%s 规格不是可安装的包（本仓支持 path/registry/tarball）", kind)), nil
	}
}

func (a *adapter) inspectManifest(manifest pluginmgr.Manifest, sourceKind string) (transport.OfficialPluginSpecInspection, error) {
	if _, installed, err := a.manager.Bundle(manifest.ID); err != nil {
		return transport.OfficialPluginSpecInspection{}, hostError(err)
	} else if installed {
		return transport.OfficialInspectionRefused("already-installed",
			fmt.Sprintf("%s 已经装过了", manifest.ID)), nil
	}
	return transport.OfficialInspectionAccepted(sourceKind, manifest.ID, manifest.Version, false), nil
}

func (a *adapter) listVersions(p transport.OfficialListVersionsParams) (transport.OfficialVersionList, error) {
	state, ok, err := a.manager.Bundle(p.Name)
	if err != nil {
		return transport.OfficialVersionList{}, hostError(err)
	}
	if !ok {
		return transport.OfficialVersionsRefused("not-updatable",
			fmt.Sprintf("%s 不是已安装的 profile 依赖", p.Name)), nil
	}
	// 从 registry 装的包，Manager 落盘的是本地缓存目录；和 updateBundle 一样
	// 用缓存根识别，继续问同一个 registry。
	fromRegistry := pluginmgr.SourceIsRegistrySpec(state.Source) ||
		strings.HasPrefix(filepath.Clean(state.Source), filepath.Clean(pluginmgr.CacheDir())+string(os.PathSeparator))
	if p.Registry == nil && !fromRegistry {
		return transport.OfficialVersionsRefused("not-updatable",
			fmt.Sprintf("%s@%s 来自%s，不是 registry 依赖", state.Name, state.Version,
				pluginmgr.DescribeSource(state.Source))), nil
	}
	client := pluginmgr.RegistryFromEnv(p.Registry)
	if client == nil {
		return transport.OfficialVersionList{}, transport.OfficialRegistryUnavailable(
			"pluginManager.listVersions 没有配置 registry：设置 DSHD_PLUGIN_REGISTRY 或 options.registry")
	}
	published, err := client.Versions(context.Background(), p.Name)
	if err != nil {
		return transport.OfficialVersionList{}, transport.OfficialRegistryUnavailable(
			"pluginManager.listVersions 读取 registry 失败：%v", err)
	}
	registry, _ := json.Marshal(client.Base())
	versions := make([]transport.OfficialPublishedVersion, 0, len(published))
	for _, v := range published {
		tags := v.Tags
		if tags == nil {
			tags = []string{}
		}
		versions = append(versions, transport.OfficialPublishedVersion{
			Version: v.Version, PublishedAt: v.PublishedAt, Tags: tags,
			Prerelease: v.Prerelease, Deprecated: v.Deprecated,
		})
	}
	return transport.OfficialVersionList{
		Status: "ok", Installed: state.Version, Registry: registry,
		Attempts: []transport.OfficialRegistryAttempt{{Registry: registry}},
		Versions: versions,
	}, nil
}

func (a *adapter) registries() (transport.OfficialRegistries, error) {
	client := pluginmgr.RegistryFromEnv(nil)
	if client == nil {
		return transport.OfficialRegistries{}, transport.OfficialRegistryUnavailable(
			"pluginManager.registries 没有配置 registry：设置 DSHD_PLUGIN_REGISTRY")
	}
	raw, _ := json.Marshal(client.Base())
	return transport.OfficialRegistries{
		Registry: raw, FallbackRegistries: []string{}, Resolved: raw,
	}, nil
}

func (a *adapter) installBundle(p transport.OfficialInstallBundleParams) (transport.OfficialChangeResult, error) {
	spec := strings.TrimSpace(p.Spec)
	kind := pluginmgr.SourceKind(spec)
	local := spec
	var approvedWarning string
	if len(p.ApprovedBuilds) > 0 {
		approvedWarning = "approvedBuilds 已收到，但本仓插件包没有 package.json 构建脚本；未执行任何构建"
	}
	switch kind {
	case "path":
	case "registry", "tarball":
		downloaded, _, err := a.materializeRegistrySpec(spec, p.Registry)
		if err != nil {
			return registryChangeRefused(spec, "not-a-package", err), nil
		}
		local = downloaded
	default:
		return transport.OfficialChangeResult{}, formUnsupported(kind)
	}
	enable := true
	if p.Enabled != nil {
		enable = *p.Enabled
	}
	requests := pluginmgr.InstallOptions{Enable: enable}
	if p.RequestID != nil {
		requests.RequestID = *p.RequestID
	}
	job, err := a.manager.StartInstall(local, requests)
	if err != nil {
		return transport.OfficialChangeResult{}, hostError(err)
	}
	outcome := job.Wait()
	result := a.changeFromOutcome(outcome, "install", spec)
	if outcome.Application == "applied" {
		result.Bundle = outcome.Info.ID
		result.Version = outcome.Info.Version
		if warning := a.exemptionWarningFor(outcome.Info.ID + "@" + outcome.Info.Version); warning != "" {
			result.Warnings = append(result.Warnings, warning)
		}
	}
	if approvedWarning != "" {
		result.Warnings = append(result.Warnings, approvedWarning)
	}
	return result, nil
}

func (a *adapter) updateBundle(p transport.OfficialUpdateBundleParams) (transport.OfficialUpdateResult, error) {
	state, ok, err := a.manager.Bundle(p.Name)
	if err != nil {
		return transport.OfficialUpdateResult{}, hostError(err)
	}
	if !ok {
		return updateRefused("", p.Name, p.Version, "invalid-spec",
			fmt.Sprintf("%s 不是已安装的 profile 依赖", p.Name)), nil
	}
	from := state.Version
	sourceKind := pluginmgr.SourceKind(state.Source)
	// 从 registry/tarball 装的包，Manager 落盘的是本地缓存目录（真实来源
	// 已下载）；用缓存根识别它，才能继续从同一 registry 解析新版本。
	fromRegistryCache := strings.HasPrefix(filepath.Clean(state.Source), filepath.Clean(pluginmgr.CacheDir())+string(os.PathSeparator))
	if sourceKind == "registry" || sourceKind == "tarball" || p.Registry != nil || fromRegistryCache {
		target := strings.TrimSpace(p.Version)
		spec := p.Name
		if target != "" && target != "latest" {
			spec = p.Name + "@" + target
		} else {
			spec = p.Name + "@latest"
		}
		local, resolved, err := a.materializeRegistrySpec(spec, p.Registry)
		if err != nil {
			return updateRefused(from, p.Name, p.Version, "not-updatable", err.Error()), nil
		}
		if target != "" && target != "latest" && resolved.Version != target {
			return updateRefused(from, p.Name, p.Version, "invalid-spec",
				fmt.Sprintf("registry 解析到 %s，但请求的是 %s", resolved.Version, target)), nil
		}
		info, err := a.manager.Update(p.Name, local)
		if err != nil {
			var apiErr *pluginmgr.Error
			if errors.As(err, &apiErr) {
				return updateRefused(from, p.Name, resolved.Version, mapChangeCode(apiErr.Code), apiErr.Message), nil
			}
			return transport.OfficialUpdateResult{}, hostError(err)
		}
		result := transport.OfficialAppliedChange("install", p.Name, info.Enabled)
		result.Bundle = info.ID
		result.Version = info.Version
		if warning := a.exemptionWarningFor(info.ID + "@" + info.Version); warning != "" {
			result.Warnings = append(result.Warnings, warning)
		}
		return transport.OfficialUpdateResult{From: from, To: info.Version, Step: "install", Result: result}, nil
	}
	compare, err := pluginmgr.CompareVersions(p.Version, from)
	if err != nil {
		return updateRefused(from, p.Name, p.Version, "invalid-spec",
			fmt.Sprintf("%q 不是 SemVer 版本", p.Version)), nil
	}
	if compare <= 0 {
		return updateRefused(from, p.Name, p.Version, "invalid-spec",
			fmt.Sprintf("%s 不新于已安装的 %s", p.Version, from)), nil
	}
	if _, ok := state.VersionSources[p.Version]; !ok {
		return updateRefused(from, p.Name, p.Version, "not-updatable",
			fmt.Sprintf("本机 store 里没有 %s@%s；本地目录包请用 plugin.update 指定目录", p.Name, p.Version)), nil
	}
	requests := pluginmgr.InstallOptions{}
	if p.RequestID != nil {
		requests.RequestID = *p.RequestID
	}
	job, err := a.manager.StartUpdate(p.Name, p.Version, requests)
	if err != nil {
		return transport.OfficialUpdateResult{}, hostError(err)
	}
	outcome := job.Wait()
	result := a.changeFromOutcome(outcome, "install", p.Name)
	if warning := a.exemptionWarningFor(p.Name + "@" + p.Version); warning != "" {
		result.Warnings = append(result.Warnings, warning)
	}
	return transport.OfficialUpdateResult{From: from, To: p.Version, Step: "install", Result: result}, nil
}

func updateRefused(from, name, version, code, diagnostic string) transport.OfficialUpdateResult {
	return transport.OfficialUpdateResult{
		From: from, To: version, Step: "check",
		Result: transport.OfficialFailedChange("install", name, code, diagnostic),
	}
}

func (a *adapter) removeBundle(p transport.OfficialBundleNameParams) (transport.OfficialChangeResult, error) {
	if _, ok, err := a.manager.Bundle(p.Name); err != nil {
		return transport.OfficialChangeResult{}, hostError(err)
	} else if !ok {
		// 官方把"不可移除"放在结果里（ManagementFailure），不是抛错。
		return transport.OfficialFailedChange("remove", p.Name, "not-removable",
			fmt.Sprintf("没有已安装的包 %s", p.Name)), nil
	}
	if err := a.manager.Uninstall(p.Name); err != nil {
		var apiErr *pluginmgr.Error
		if errors.As(err, &apiErr) {
			return transport.OfficialFailedChange("remove", p.Name, mapChangeCode(apiErr.Code), apiErr.Message), nil
		}
		return transport.OfficialChangeResult{}, hostError(err)
	}
	return transport.OfficialAppliedChange("remove", p.Name, false), nil
}

// setEnabled 同时服务 setBundleEnabled 与 setPluginEnabled：本仓一包一插件，
// 两个官方方法写的是同一个持久化启用位（同一 id 空间）。
func (a *adapter) setEnabled(p transport.OfficialSetEnabledParams) (transport.OfficialChangeResult, error) {
	state, ok, err := a.manager.Bundle(p.Name)
	if err != nil {
		return transport.OfficialChangeResult{}, hostError(err)
	}
	if !ok {
		return transport.OfficialFailedChange("enable", p.Name, "unknown-plugin",
			fmt.Sprintf("没有已安装的包 %s", p.Name)), nil
	}
	result := transport.OfficialAppliedChange("enable", p.Name, p.Enabled)
	if state.Enabled == p.Enabled {
		result.Changed = false
		return result, nil
	}
	var info pluginmgr.PluginInfo
	if p.Enabled {
		info, err = a.manager.Enable(p.Name)
	} else {
		info, err = a.manager.Disable(p.Name)
	}
	if err != nil {
		var apiErr *pluginmgr.Error
		if errors.As(err, &apiErr) {
			return transport.OfficialFailedChange("enable", p.Name, mapChangeCode(apiErr.Code), apiErr.Message), nil
		}
		return transport.OfficialChangeResult{}, hostError(err)
	}
	enabled := info.Enabled
	result.Changed = true
	result.Enabled = &enabled
	return result, nil
}

// setVersionExemption 授权/撤销一个精确版本豁免。
//
// 官方语义（dsh-plugin-manager/lib/index.js:2085-2090）：先写 profile 的
// compatibility.json，再 reload 活体插件。本仓没有运行时插件加载器，所以第二半
// 如实折成 warning，而不是假装 reload 过。
func (a *adapter) setVersionExemption(p transport.OfficialVersionExemptionParams) (transport.OfficialChangeResult, error) {
	acceptRisk := p.AcceptRisk != nil && *p.AcceptRisk
	changed, err := a.manager.SetVersionExemption(p.PackageVersion, p.RuntimeVersion, p.Enabled, acceptRisk)
	if err != nil {
		return transport.OfficialChangeResult{}, hostError(err)
	}
	result := transport.OfficialAppliedChange("enable", p.PackageVersion, p.Enabled)
	result.Changed = changed.Changed
	result.Warnings = append(result.Warnings, "豁免已持久化到插件管理器的 compatibility.json（重开仍可读）")
	result.Warnings = append(result.Warnings, changed.Warnings...)
	return result, nil
}

// listVersionExemptions 读 profile 里已接受的精确版本豁免。
//
// 官方语义（dsh-plugin-manager/lib/index.js:2071-2077）：直接读
// compatibility.json，把**被接受**的 `name@version → [dsh 版本]` 与读者拒掉的
// 记录/文件问题一起返回。坏文件不抛错：exemptions 为空、warnings 如实带出，
// 让界面提示用户先手工修（本仓 `pluginmgr.readCompatibility` 同一条分支）。
func (a *adapter) listVersionExemptions() (transport.OfficialVersionExemptions, error) {
	exemptions, warnings := a.manager.VersionExemptions()
	return transport.OfficialVersionExemptions{
		Exemptions: exemptions,
		Warnings:   warnings,
	}, nil
}

// exemptionWarningFor 报告"这个精确版本是靠豁免装上的"。
//
// 返回空串表示没有豁免，不加噪音；有豁免就如实带上风险——官方同样把它渲染成
// 一条用户可见的警告（pluginCompatibilityWarning）。
func (a *adapter) exemptionWarningFor(packageVersion string) string {
	exemptions, _ := a.manager.VersionExemptions()
	for _, runtimeVersion := range exemptions[packageVersion] {
		if runtimeVersion == pluginmgr.HostRuntimeVersion {
			return fmt.Sprintf("%s 与 dsh %s 不兼容，是按精确版本豁免安装的：运行它可能崩溃或丢数据",
				packageVersion, pluginmgr.HostRuntimeVersion)
		}
	}
	return ""
}

func (a *adapter) readBundleDetail(p transport.OfficialReadBundleDetailParams) (transport.OfficialBundleDetail, error) {
	path, raw, ok, err := a.manager.ReadDocument(p.Name, docCandidates)
	if err != nil {
		var apiErr *pluginmgr.Error
		if errors.As(err, &apiErr) && apiErr.Code == "not_found" {
			return transport.OfficialBundleDetail{}, transport.OfficialNotFound(
				"没有这个 bundle：%q（先调 installBundle 或核对 id）", p.Name)
		}
		return transport.OfficialBundleDetail{}, hostError(err)
	}
	if !ok {
		// 装上了但没有 README：官方的回答就是 none，不是错误。
		return transport.OfficialBundleDetail{Status: "none"}, nil
	}
	return transport.OfficialBundleDetail{
		Status: "found", Origin: "readme", Path: path,
		Markdown: stripDocumentPrefix(raw),
	}, nil
}

func (a *adapter) waitForInstall(p transport.OfficialRequestIDParams) (transport.OfficialInstallWait, error) {
	outcome, ok := a.manager.WaitInstall(p.RequestID)
	if !ok {
		// 官方的 null：没有（或已经收工）这个 requestId。
		return transport.OfficialInstallWait{Active: false}, nil
	}
	result := a.changeFromOutcome(outcome, outcome.Stage, outcome.Target)
	return transport.OfficialInstallWait{Active: true, Result: &result}, nil
}

func (a *adapter) cancelInstall(p transport.OfficialRequestIDParams) (transport.OfficialInstallCancellation, error) {
	return transport.OfficialInstallCancellation{Status: a.manager.CancelInstall(p.RequestID)}, nil
}

// changeFromOutcome 把真实作业结果翻译成官方 ChangeResult。
func (a *adapter) changeFromOutcome(outcome pluginmgr.InstallOutcome, stage, target string) transport.OfficialChangeResult {
	if stage == "" {
		stage = outcome.Stage
	}
	if target == "" {
		target = outcome.Target
	}
	switch outcome.Application {
	case "applied":
		enabled := false
		if outcome.Enabled != nil {
			enabled = *outcome.Enabled
		} else if outcome.Changed {
			enabled = outcome.Info.Enabled
		}
		result := transport.OfficialAppliedChange(stage, target, enabled)
		result.Bundle = outcome.Info.ID
		result.Version = outcome.Info.Version
		return result
	case "cancelled":
		return transport.OfficialCancelledChange(stage, target)
	default:
		code := "operation-error"
		diagnostic := "安装事务失败"
		if outcome.Failure != nil {
			code = mapChangeCode(outcome.Failure.Code)
			diagnostic = outcome.Failure.Message
		}
		return transport.OfficialFailedChange(stage, target, code, diagnostic)
	}
}

// mapChangeCode 把 pluginmgr 的稳定码映射到官方的 `ChangeResult.error.code` 枚举。
func mapChangeCode(code string) string {
	switch code {
	case "duplicate_id", "already_installed":
		return "already-installed"
	case "invalid_manifest", "hash_mismatch", "bad_request", "version_conflict", "state_corrupt",
		"package_corrupt", "transaction_corrupt":
		return "invalid-spec"
	case "incompatible_version":
		return "incompatible-version"
	case "not_found":
		return "unknown-plugin"
	case "duplicate_request":
		return "operation-error"
	case "document_too_large":
		return "operation-error"
	default:
		return "operation-error"
	}
}

// stripDocumentPrefix 去掉 BOM 与开头的 YAML front matter（官方同样剥这两样：
// 前端不应拿到 `---` 头）。
func stripDocumentPrefix(raw []byte) string {
	text := strings.TrimPrefix(string(raw), "\ufeff")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if !strings.HasPrefix(text, "---") {
		return text
	}
	lines := strings.Split(text, "\n")
	if strings.TrimSpace(lines[0]) != "---" {
		return text
	}
	for index := 1; index < len(lines); index++ {
		if strings.TrimSpace(lines[index]) == "---" {
			return strings.TrimPrefix(strings.Join(lines[index+1:], "\n"), "\n")
		}
	}
	// 没有闭合的 front matter：原样返回（不猜它是不是文档内容）。
	return text
}

// hostError 把 pluginmgr 的四字段错误翻译成 official 错误，保住状态码。
//
// 一律塌成 500 会让客户端把"参数写错/状态冲突"当成"宿主崩了"（它就会重试，
// 或者去改参数却改错地方）。hint 由错误码统一给出，这里不带。
func hostError(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *pluginmgr.Error
	if !errors.As(err, &apiErr) {
		return err
	}
	switch apiErr.Status {
	case http.StatusBadRequest:
		return transport.OfficialBadRequest("%s", apiErr.Message)
	case http.StatusNotFound:
		return transport.OfficialNotFound("%s", apiErr.Message)
	case http.StatusConflict:
		return transport.OfficialConflict("%s", apiErr.Message)
	case http.StatusUnprocessableEntity:
		return transport.OfficialBadRequest("%s", apiErr.Message)
	default:
		return err
	}
}

func formUnsupported(kind string) error {
	return fmt.Errorf("%w: %q 形态的 spec 需要 registry/pnpm/git/tarball 客户端；本仓只接受本地目录包（kind=path）",
		transport.ErrOfficialNotImplemented, kind)
}

func registryUnsupported() error {
	return fmt.Errorf("%w: 本仓没有 registry 客户端（pnpm view 等价物），也没有任何 registry 配置可读",
		transport.ErrOfficialNotImplemented)
}
