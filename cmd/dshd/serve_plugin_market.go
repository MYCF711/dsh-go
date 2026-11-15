package main

// PARITY-W3-MARKET：pluginMarket 4 方法与 pluginRegistryProbe.fastest 的宿主实现。
// 索引存储/HTTP 在 internal/market；安装状态来自当前 pluginmgr.Manager。
//
// 结果形状照官方 typert 逐字段给（`dsh-plugin-market/lib/typert.host.js` 的
// MarketSearchResult / MarketListingEntry / MarketListingVersion），映射轨迹就是
// 官方 `dsh-plugin-market/src/index.ts` 的 `search` + `listingOf` + `installationOf`
// + `compatibilityOf`：
//
//   - `entries[]` 的每个版本行带 `install{spec,registry?}`、`compatibility`（union
//     对象，不是 bool）、`update`、`publishTimestamp`（Unix 秒）；
//   - `defaultVersion` = 最高兼容且（有稳定版时）不带预发布标签的版本；
//   - `installation.status` 五态里宿主能判的是 not-installed / installed /
//     update-available / different-version；`provided`（发行版随附、未安装）没有
//     数据源，所以不会出现——不是编一个。
//
// 另外两处 rc.2 契约（本文件负责）：
//
//  1. 版本行 `update: boolean`（官方 listingOf）：已装副本来自**完整 commit 钉住的
//     git spec**、这一版更新、且钉住同仓库同 path 目录时才是 true。
//  2. 私有 Git 插件详情：索引没有内联 README 时，按这一版声明的 git 来源读仓库
//     README；https 被拒时由仓库缓存回退 SSH。真实 git 调用在
//     internal/pluginmgr/official 的 RepositoryCache，本文件只做编排与错误投影。

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"dshgo/internal/market"
	"dshgo/internal/pluginmgr"
	officialpluginmgr "dshgo/internal/pluginmgr/official"
	"dshgo/internal/transport"
)

// marketRepositoryReader 是"从 git 仓库读一个文件"的最小接缝：产品实现是
// officialpluginmgr.RepositoryCache（真实 git），测试注入假实现只验编排。
type marketRepositoryReader interface {
	ReadFile(ctx context.Context, url, commit, name string) ([]byte, error)
}

// marketRepositoryReadmeNames 是详情回退时依次尝试的 README 文件名（官方
// dsh-plugin-detail 的 readmeName 覆盖的常见拼写）。
var marketRepositoryReadmeNames = []string{"README.md", "readme.md", "Readme.md", "README"}

// marketRegistryProbeEndpoints 是官方 pluginRegistryProbe 竞速的两个公共 registry
// ping 地址（`dsh-client-ui-plugin-manager/src/index.ts:62`），逐字相同。
var marketRegistryProbeEndpoints = []string{
	"https://registry.npmjs.org/-/ping",
	"https://registry.npmmirror.com/-/ping",
}

// marketRegistryProbeTimeout 是官方 registryProbeTimeoutMs 的默认值（1500ms）。
const marketRegistryProbeTimeout = 1500 * time.Millisecond

type pluginMarketAPI struct {
	store *market.Store
	// bundles 是已装副本的真实投影；nil 表示没有插件管理器（不假装有）。
	bundles func() ([]pluginmgr.BundleState, error)
	// exemptions 是 profile 里的版本豁免（官方 annotator 每页现读一次）。
	exemptions func() map[string][]string
	// repositories 是私有 Git 仓库详情读取（nil = 未接线，回结构化错误而不是假成功）。
	repositories marketRepositoryReader
	// probeClient / probeEndpoints 是 pluginRegistryProbe.fastest 的真实探测面
	//（测试注入本地端点，生产是官方两个公共 registry）。
	probeClient    *http.Client
	probeEndpoints []string
	probeTimeout   time.Duration
}

func newPluginMarketAPI(home string, manager *pluginmgr.Manager) *pluginMarketAPI {
	api := &pluginMarketAPI{
		store:          market.Open(home),
		probeClient:    &http.Client{Timeout: marketRegistryProbeTimeout},
		probeEndpoints: marketRegistryProbeEndpoints,
		probeTimeout:   marketRegistryProbeTimeout,
	}
	if manager != nil {
		api.bundles = manager.Bundles
		api.exemptions = func() map[string][]string {
			exemptions, _ := manager.VersionExemptions()
			return exemptions
		}
	}
	// 仓库缓存落在 home 下：与官方 detailCacheDir 同一职责（可丢、可重建的缓存）。
	api.repositories = officialpluginmgr.NewRepositoryCache(filepath.Join(home, "cache", "plugin-market-repositories"))
	return api
}

func (a *pluginMarketAPI) ListIndexes(ctx context.Context) ([]transport.OfficialMarketIndex, error) {
	entries, err := a.store.List(ctx)
	if err == market.ErrNoIndexes {
		return nil, &transport.PluginMarketError{Status: http.StatusServiceUnavailable, Code: "registry_unavailable",
			Message: "pluginMarket.listIndexes 没有配置索引源：设置 plugin-market 索引 URL（DSHD_PLUGIN_MARKET_INDEXES）"}
	}
	if err != nil {
		return nil, err
	}
	out := make([]transport.OfficialMarketIndex, 0, len(entries))
	for _, entry := range entries {
		out = append(out, transport.OfficialMarketIndex{URL: entry.URL, Title: officialLocalizedText(entry.Title),
			Description: officialLocalizedText(entry.Description), Error: entry.Error})
	}
	return out, nil
}

func (a *pluginMarketAPI) SetIndexes(_ context.Context, urls []string) error {
	if len(urls) == 0 {
		// 允许清空；但空索引源在 list/search 时仍会结构化 refused。
		return a.store.SetURLs(nil)
	}
	if err := a.store.SetURLs(urls); err != nil {
		// 索引 URL 不合法是**调用方的参数问题**（官方是 plugin-market/invalid-index-url），
		// 不是上游不可用：回 400，别让客户端按 502 去重试。
		return &transport.PluginMarketError{Status: http.StatusBadRequest, Code: "bad_request", Message: err.Error()}
	}
	return nil
}

// installedBundleMap 读一次已装副本，供搜索/详情判定共用（同一份真值）。
func (a *pluginMarketAPI) installedBundleMap() map[string]pluginmgr.BundleState {
	if a.bundles == nil {
		return nil
	}
	bundles, err := a.bundles()
	if err != nil || len(bundles) == 0 {
		return nil
	}
	out := make(map[string]pluginmgr.BundleState, len(bundles))
	for _, bundle := range bundles {
		out[bundle.Name] = bundle
	}
	return out
}

func (a *pluginMarketAPI) Search(ctx context.Context, rawURL string, q transport.OfficialMarketQuery) (transport.OfficialMarketSearchResult, error) {
	if strings.TrimSpace(rawURL) == "" {
		return transport.OfficialMarketSearchResult{}, &transport.PluginMarketError{Status: 400, Code: "bad_request", Message: "缺 url"}
	}
	result, err := a.store.Search(ctx, rawURL, market.Query{Text: q.Text, Category: q.Category, Labels: q.Labels,
		Sort: q.SortSpec(), Offset: q.Offset, Limit: q.Limit, Refresh: q.Refresh, FetchedAt: q.FetchedAt})
	if err != nil {
		return transport.OfficialMarketSearchResult{}, &transport.PluginMarketError{Status: 502, Code: "index_unavailable", Message: err.Error()}
	}
	installed := a.installedBundleMap()
	entries := make([]transport.OfficialMarketEntry, 0, len(result.Plugins))
	for _, plugin := range result.Plugins {
		entry, err := a.listingEntry(plugin, installed)
		if err != nil {
			return transport.OfficialMarketSearchResult{}, &transport.PluginMarketError{Status: 502, Code: "index_unavailable", Message: err.Error()}
		}
		entries = append(entries, entry)
	}
	return transport.OfficialMarketSearchResult{URL: rawURL,
		Title:       officialLocalizedText(result.Index.Title),
		Description: officialLocalizedText(result.Index.Description),
		Metadata:    officialIndexMetadata(result.Index.Metadata),
		FetchedAt:   result.FetchedAt,
		Entries:     entries,
		Total:       result.Total,
		NextOffset:  result.NextOffset}, nil
}

// listingEntry 是官方 `listingOf` 的宿主实现：一页里的每个插件补齐
// defaultVersion / installation / 每版本的 install、compatibility、update。
func (a *pluginMarketAPI) listingEntry(plugin market.Plugin, installed map[string]pluginmgr.BundleState) (transport.OfficialMarketEntry, error) {
	bundle, hasBundle := installed[plugin.Name]
	entry := transport.OfficialMarketEntry{
		Name:        plugin.Name,
		Title:       officialLocalizedText(plugin.Title),
		Description: officialLocalizedText(plugin.Description),
		Icon:        plugin.Icon,
		Author:      plugin.Author,
		Homepage:    plugin.Homepage,
		Metadata: transport.OfficialMarketPluginMetadata{
			Category: plugin.Metadata.Category,
			Keywords: plugin.Metadata.Keywords,
			Labels:   plugin.Metadata.Labels,
			SortKeys: plugin.Metadata.SortKeys,
		},
	}
	if plugin.Repository != nil {
		entry.Repository = &transport.OfficialMarketRepository{URL: plugin.Repository.URL, Directory: plugin.Repository.Directory}
	}
	entry.Versions = make([]transport.OfficialMarketListingVersion, 0, len(plugin.Versions))
	stable := false
	for _, version := range plugin.Versions {
		if !marketPrerelease(version.Version) {
			stable = true
			break
		}
	}
	for _, version := range plugin.Versions {
		compatibility, err := a.compatibilityOf(plugin.Name, version)
		if err != nil {
			return transport.OfficialMarketEntry{}, err
		}
		spec, ok := marketInstallSpec(plugin.Name, version)
		if !ok {
			return transport.OfficialMarketEntry{}, wrappedMarketError(
				"索引里 " + plugin.Name + "@" + version.Version + " 的来源算不出 install.spec（官方 format 1 的 source 必须是 npm|git 且字段完整）")
		}
		entry.Versions = append(entry.Versions, transport.OfficialMarketListingVersion{
			Version:          version.Version,
			PublishTimestamp: version.PublishTimestamp,
			Source:           officialMarketSource(version.Source),
			Install:          transport.OfficialMarketInstall{Spec: spec, Registry: marketSourceRegistry(version.Source)},
			Engines:          transport.OfficialMarketEngines{DSH: version.Engines.DSH},
			Compatibility:    compatibility,
			Update:           marketVersionUpdatable(plugin.Name, version, bundle, hasBundle),
		})
	}
	entry.DefaultVersion = marketDefaultVersion(entry.Versions, stable)
	measured := entry.DefaultVersion
	if measured == "" && len(entry.Versions) > 0 {
		measured = entry.Versions[0].Version
	}
	entry.Installation = marketInstallationOf(bundle, hasBundle, measured)
	return entry, nil
}

// marketDefaultVersion 复刻官方 `listingOf` 的 defaultVersion 规则：最高兼容版本，
// 但"这个包有稳定版"时只认不带预发布标签的那些；一个都不兼容 → 空（官方缺席）。
func marketDefaultVersion(versions []transport.OfficialMarketListingVersion, stable bool) string {
	for _, version := range versions {
		if !version.Compatibility.Compatible {
			continue
		}
		if stable && marketPrerelease(version.Version) {
			continue
		}
		return version.Version
	}
	return ""
}

// marketPrerelease 判 SemVer 预发布：核心版本之后的 `-` 标签（build metadata 里
// 的 `-` 不算，所以先切掉 `+` 之后的部分）。
func marketPrerelease(version string) bool {
	version = strings.TrimSpace(version)
	if index := strings.Index(version, "+"); index >= 0 {
		version = version[:index]
	}
	return strings.Contains(version, "-")
}

// marketInstallationOf 复刻官方 `installationOf` 的宿主可判分支。
//
// 官方五态里的 `provided`（发行版随附但未安装）需要"安装提供的 catalog"这一数据源，
// 本仓没有 → 不会出现；除此之外三个判据逐条对齐。
func marketInstallationOf(bundle pluginmgr.BundleState, hasBundle bool, version string) transport.OfficialMarketInstallation {
	if !hasBundle {
		return transport.OfficialMarketInstallation{Status: "not-installed"}
	}
	installedVersion := strings.TrimSpace(bundle.Version)
	if installedVersion == "" {
		return transport.OfficialMarketInstallation{Status: "different-version"}
	}
	if installedVersion == version {
		return transport.OfficialMarketInstallation{Status: "installed", InstalledVersion: installedVersion}
	}
	if comparison, err := pluginmgr.CompareVersions(version, installedVersion); err == nil && comparison > 0 {
		return transport.OfficialMarketInstallation{Status: "update-available", InstalledVersion: installedVersion}
	}
	return transport.OfficialMarketInstallation{Status: "different-version", InstalledVersion: installedVersion}
}

// compatibilityOf 复刻官方 `compatibilityOf`：只有 `engines.dsh` 一个要求面
// （官方构造的 manifest 也只放 name/version/engines.dsh），runtime 版本取宿主注入值。
func (a *pluginMarketAPI) compatibilityOf(name string, version market.Version) (transport.OfficialMarketCompatibility, error) {
	manifest := pluginmgr.Manifest{ID: name, Version: version.Version}
	if rangeExpr := strings.TrimSpace(version.Engines.DSH); rangeExpr != "" {
		manifest.Engines = &pluginmgr.Engines{DSH: rangeExpr}
	}
	var exemptions map[string][]string
	if a.exemptions != nil {
		exemptions = a.exemptions()
	}
	issue, err := pluginmgr.EvaluateCompatibility(manifest, exemptions, pluginmgr.HostRuntimeVersion)
	if err != nil {
		return transport.OfficialMarketCompatibility{}, wrappedMarketError(
			"判定 " + name + "@" + version.Version + " 的 engines.dsh 兼容性失败: " + err.Error())
	}
	if issue == nil {
		return transport.OfficialMarketCompatibility{Compatible: true}, nil
	}
	exempted := issue.Exempted
	return transport.OfficialMarketCompatibility{Compatible: false, RuntimeVersion: issue.RuntimeVersion,
		Requirements: issue.Requirements, Exempted: &exempted}, nil
}

// wrappedMarketError 把内部失败投影成 502：索引/兼容性判定失败是上游数据问题，
// 不是客户端的参数问题。
func wrappedMarketError(message string) error {
	return &transport.PluginMarketError{Status: http.StatusBadGateway, Code: "index_unavailable", Message: message}
}

func officialLocalizedText(text market.LocalizedText) transport.OfficialLocalizedText {
	if len(text) == 0 {
		return nil
	}
	out := make(transport.OfficialLocalizedText, len(text))
	for language, value := range text {
		out[language] = value
	}
	return out
}

func officialIndexMetadata(metadata market.IndexMetadata) transport.OfficialMarketIndexMetadata {
	out := transport.OfficialMarketIndexMetadata{
		Categories: make(map[string]transport.OfficialMarketDeclaration, len(metadata.Categories)),
		Labels:     make(map[string]transport.OfficialMarketDeclaration, len(metadata.Labels)),
		SortKeys:   make(map[string]transport.OfficialMarketSortKey, len(metadata.SortKeys)),
	}
	for id, declaration := range metadata.Categories {
		out.Categories[id] = transport.OfficialMarketDeclaration{Title: officialLocalizedText(declaration.Title)}
	}
	for id, declaration := range metadata.Labels {
		out.Labels[id] = transport.OfficialMarketDeclaration{Title: officialLocalizedText(declaration.Title)}
	}
	for id, declaration := range metadata.SortKeys {
		out.SortKeys[id] = transport.OfficialMarketSortKey{Title: officialLocalizedText(declaration.Title), Order: declaration.Order}
	}
	return out
}

func officialMarketSource(source *market.Source) transport.OfficialMarketSource {
	if source == nil {
		return transport.OfficialMarketSource{}
	}
	return transport.OfficialMarketSource{Type: source.Type, Registry: source.Registry,
		URL: source.URL, Commit: source.Commit, Path: source.Path}
}

func marketSourceRegistry(source *market.Source) string {
	if source == nil {
		return ""
	}
	return source.Registry
}

// marketInstallSpec 复刻官方 `marketInstallTarget`（dsh-plugin-market-index/src/index.ts）：
// npm → `name@version`；git → `git+<url>.git#<commit>[&path:/<dir>]`。
//
// 索引没声明 source（rc.2 之前的旧索引形状）时返回 false：没有来源信息就判不了
// "同仓库另一 commit"，也装不了，调用方必须报错而不是发一个空 spec。
func marketInstallSpec(name string, version market.Version) (string, bool) {
	source := version.Source
	if source == nil {
		return "", false
	}
	switch strings.ToLower(strings.TrimSpace(source.Type)) {
	case "npm":
		if strings.TrimSpace(version.Version) == "" {
			return "", false
		}
		return name + "@" + version.Version, true
	case "git":
		repository := strings.TrimSpace(source.URL)
		commit := strings.TrimSpace(source.Commit)
		if repository == "" || commit == "" {
			return "", false
		}
		if !strings.HasSuffix(repository, ".git") {
			repository += ".git"
		}
		spec := "git+" + repository + "#" + commit
		if path := strings.Trim(strings.TrimSpace(source.Path), "/"); path != "" {
			spec += "&path:/" + path
		}
		return spec, true
	}
	return "", false
}

// marketVersionUpdatable 是官方 `listingOf` 的 `update` 判据（逐条对齐）：
//
//	const origin = bundle?.update === 'git' && bundle.source !== undefined
//	  ? pinnedGitOrigin(bundle.source) : undefined
//	update = origin !== undefined && installedVersion !== undefined
//	  && semver.gt(version.version, installedVersion)
//	  && pinnedGitOrigin(install.spec) === origin
//
// 注意：**registry 装的副本永远是 false**（官方只在副本是 git commit 时算
// origin；"发行版有新版本"由 installation.status=different-version 表达）。
func marketVersionUpdatable(name string, version market.Version, bundle pluginmgr.BundleState, hasBundle bool) bool {
	origin := installedGitOrigin(bundle, hasBundle)
	if origin == "" {
		return false
	}
	if strings.TrimSpace(bundle.Version) == "" {
		return false
	}
	if comparison, err := pluginmgr.CompareVersions(version.Version, bundle.Version); err != nil || comparison <= 0 {
		return false
	}
	spec, ok := marketInstallSpec(name, version)
	if !ok {
		return false
	}
	target, ok := pluginmgr.PinnedGitOrigin(spec)
	if !ok || target != origin {
		return false
	}
	return true
}

// installedGitOrigin 只在"已装副本来自完整 commit 钉住的 git spec"时返回 origin。
func installedGitOrigin(bundle pluginmgr.BundleState, hasBundle bool) string {
	if !hasBundle {
		return ""
	}
	if pluginmgr.UpdateKind(bundle.Source, bundle.Name, bundle.Version) != pluginmgr.UpdateGit {
		return ""
	}
	origin, ok := pluginmgr.PinnedGitOrigin(bundle.Source)
	if !ok {
		return ""
	}
	return origin
}

func (a *pluginMarketAPI) ReadEntryDetail(ctx context.Context, rawURL, name, version, language string) (transport.OfficialMarketEntryDetail, error) {
	if strings.TrimSpace(rawURL) == "" || strings.TrimSpace(name) == "" || strings.TrimSpace(version) == "" {
		return transport.OfficialMarketEntryDetail{}, &transport.PluginMarketError{
			Status: http.StatusBadRequest, Code: "bad_request",
			Message: "pluginMarket.readEntryDetail 需要 url / name / version（缺一个都读不到真实条目）"}
	}
	plugin, entry, found, err := a.store.FindVersion(ctx, rawURL, name, version)
	if err != nil {
		// 没配置索引源 = 不可用（503），不是上游详情 502：结构化"配置边界"，
		// 与 listIndexes/registries 同一口径。
		if errors.Is(err, market.ErrNoIndexes) {
			return transport.OfficialMarketEntryDetail{}, &transport.PluginMarketError{
				Status: 503, Code: "registry_unavailable", Message: err.Error()}
		}
		return transport.OfficialMarketEntryDetail{}, &transport.PluginMarketError{Status: 502, Code: "detail_unavailable", Message: err.Error()}
	}
	if !found {
		return transport.OfficialMarketEntryDetail{Status: "none"}, nil
	}
	document := name + "@" + version
	// 官方顺序（dsh-plugin-market/src/detail.ts）：索引内联文档优先，其次才是
	// 这一版声明的仓库 README。
	if entry.Readme != "" {
		return transport.OfficialMarketEntryDetail{Status: "found", Origin: "readme", Markdown: entry.Readme, Document: document}, nil
	}
	if entry.Source != nil && strings.EqualFold(entry.Source.Type, "git") {
		detail, repoErr := a.readRepositoryDetail(ctx, entry.Source, document)
		if repoErr != nil {
			return transport.OfficialMarketEntryDetail{}, repoErr
		}
		if detail != nil {
			return *detail, nil
		}
	}
	// 老索引的兜底：语言化 description / title（不是仓库内容，original 仍标 readme）。
	for _, text := range []market.LocalizedText{plugin.Description, plugin.Title} {
		if value := localizedMarketText(text, language); value != "" {
			return transport.OfficialMarketEntryDetail{Status: "found", Origin: "readme", Markdown: value, Document: document}, nil
		}
	}
	return transport.OfficialMarketEntryDetail{Status: "none"}, nil
}

// localizedMarketText 与 internal/market 的 localized 同一条口径：
// 精确语言 → 语言主标签 → en。
func localizedMarketText(text market.LocalizedText, language string) string {
	if text == nil {
		return ""
	}
	if language != "" {
		if value := text[language]; value != "" {
			return value
		}
		if index := strings.Index(language, "-"); index > 0 {
			if value := text[language[:index]]; value != "" {
				return value
			}
		}
	}
	return text["en"]
}

// readRepositoryDetail 从这一版的 git 来源读 README；README 不存在时返回
// (nil, nil) 让调用方继续走兜底（不是错误）。
func (a *pluginMarketAPI) readRepositoryDetail(ctx context.Context, source *market.Source, document string) (*transport.OfficialMarketEntryDetail, error) {
	if a.repositories == nil {
		return nil, &transport.PluginMarketError{
			Status: http.StatusNotImplemented, Code: "not_implemented",
			Message: "pluginMarket.readEntryDetail：这一版只声明了 git 来源，但宿主没有仓库读取器（私有 Git 插件详情不可用）"}
	}
	for _, readme := range marketRepositoryReadmeNames {
		content, readErr := a.repositories.ReadFile(ctx, source.URL, source.Commit, readme)
		if readErr == nil {
			return &transport.OfficialMarketEntryDetail{Status: "found", Origin: "repository",
				Markdown: string(content), Document: document}, nil
		}
		var failure *officialpluginmgr.RepositoryFailure
		if errors.As(readErr, &failure) && failure.Reason == "invalid" {
			// 这个文件名不存在：试下一个拼写。
			continue
		}
		return nil, marketRepositoryError(source, readErr)
	}
	return nil, nil
}

// marketRepositoryError 把仓库失败投影成结构化四字段错误：reason 进 message
// （hint 由 Transport 的 errorHint 按码兜底，专属文案随报告交接）。
func marketRepositoryError(source *market.Source, err error) *transport.PluginMarketError {
	var failure *officialpluginmgr.RepositoryFailure
	status := http.StatusBadGateway
	reason := "unknown"
	message := err.Error()
	if errors.As(err, &failure) {
		reason = failure.Reason
		message = failure.Message
		switch failure.Reason {
		case "git-unavailable":
			status = http.StatusServiceUnavailable
		case "too-large":
			status = http.StatusBadGateway
		}
	}
	repository := ""
	if source != nil {
		repository = source.URL
	}
	return &transport.PluginMarketError{
		Status: status,
		Code:   "detail_unavailable",
		Message: "私有 Git 仓库详情读取失败（" + reason + "）：" + repository + " — " + message +
			"；已按官方顺序先试 https、被拒后再用 ssh 重试一次",
	}
}

// Fastest 是官方 `pluginRegistryProbe.fastest`：**无参数**，竞速公共 registry 的
// HTTPS ping，回"最先成功那个的 origin URL"，都失败回空串（官方 null）。
// 官方结果缓存 300s（registryProbeCacheTtlMs）；宿主不缓存——这不是形状问题，
// 只是多打两次 ping。
func (a *pluginMarketAPI) Fastest(ctx context.Context) (string, error) {
	endpoints := a.probeEndpoints
	if len(endpoints) == 0 {
		return "", nil
	}
	timeout := a.probeTimeout
	if timeout <= 0 {
		timeout = marketRegistryProbeTimeout
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	winner := make(chan string, len(endpoints))
	for _, endpoint := range endpoints {
		go func(endpoint string) {
			winner <- a.pingRegistry(probeCtx, endpoint)
		}(endpoint)
	}
	for range endpoints {
		select {
		case registry := <-winner:
			if registry != "" {
				return registry, nil
			}
		case <-probeCtx.Done():
			return "", nil
		}
	}
	return "", nil
}

// pingRegistry 打一次 `/-/ping`；成功回 `<scheme>://<host>/`（官方
// `new URL('/', endpoint).href` 同一形状），失败回空串。
func (a *pluginMarketAPI) pingRegistry(ctx context.Context, endpoint string) string {
	client := a.probeClient
	if client == nil {
		client = &http.Client{Timeout: marketRegistryProbeTimeout}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ""
	}
	response, err := client.Do(request)
	if err != nil {
		return ""
	}
	response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ""
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host + "/"
}

var _ transport.OfficialPluginMarketAPI = (*pluginMarketAPI)(nil)
var _ transport.OfficialPluginRegistryProbeAPI = (*pluginMarketAPI)(nil)
