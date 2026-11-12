package main

// PARITY-W3-MARKET：pluginMarket 4 方法与 pluginRegistryProbe.fastest 的宿主实现。
// 索引存储/HTTP 在 internal/market；安装状态来自当前 pluginmgr.Manager。
//
// 0.2.2-rc.2 的两处契约（本文件负责）：
//
//  1. 版本行 `update: boolean`（`dsh-plugin-market/src/index.ts:481-487`、
//     `types.ts` 的 MarketListingVersion.update）：
//     已装副本来自**完整 commit 钉住的 git spec**（`pluginManager` 把它列成
//     `update: 'git'`）、这一版更新、且这一版钉住**同仓库同 path 目录**的另一个
//     commit 时才是 true。判据逐条复刻官方 `listingOf`，不做近似。
//  2. 私有 Git 插件详情：索引没有内联 README 时，按这一版声明的 git 来源读仓库
//     README；https 被拒时由仓库缓存回退 SSH（release note「只有 SSH 凭据也能看
//     私有 Git 插件」）。真实 git 调用在 internal/pluginmgr/official 的
//     RepositoryCache，本文件只做编排与错误投影。

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"sort"
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

type pluginMarketAPI struct {
	store   *market.Store
	manager *pluginmgr.Manager
	probe   *pluginmgr.RegistryClient
	// bundles 是已装副本的真实投影；nil 表示没有插件管理器（不假装有）。
	bundles func() ([]pluginmgr.BundleState, error)
	// repositories 是私有 Git 仓库详情读取（nil = 未接线，回结构化错误而不是假成功）。
	repositories marketRepositoryReader
}

func newPluginMarketAPI(home string, manager *pluginmgr.Manager) *pluginMarketAPI {
	api := &pluginMarketAPI{
		store:   market.Open(home),
		manager: manager,
		probe:   pluginmgr.RegistryFromEnv(nil),
	}
	if manager != nil {
		api.bundles = manager.Bundles
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
		out = append(out, transport.OfficialMarketIndex{URL: entry.URL, Title: entry.Title, Description: entry.Description, Error: entry.Error})
	}
	return out, nil
}

func (a *pluginMarketAPI) SetIndexes(_ context.Context, urls []string) error {
	if len(urls) == 0 {
		// 允许清空；但空索引源在 list/search 时仍会结构化 refused。
		return a.store.SetURLs(nil)
	}
	return a.store.SetURLs(urls)
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
		Sort: q.Sort, Offset: q.Offset, Limit: q.Limit, Refresh: q.Refresh, FetchedAt: q.FetchedAt})
	if err != nil {
		return transport.OfficialMarketSearchResult{}, &transport.PluginMarketError{Status: 502, Code: "index_unavailable", Message: err.Error()}
	}
	installed := a.installedBundleMap()
	plugins := make([]transport.OfficialMarketPlugin, 0, len(result.Plugins))
	for _, plugin := range result.Plugins {
		item := transport.OfficialMarketPlugin{Name: plugin.Name, Title: plugin.Title, Description: plugin.Description,
			Author: plugin.Author, Keywords: plugin.Keywords, Category: plugin.Category, Labels: plugin.Labels,
			Installation: map[string]any{"status": "not-installed"}}
		bundle, hasBundle := installed[plugin.Name]
		for _, version := range plugin.Versions {
			item.Versions = append(item.Versions, transport.OfficialMarketPluginVersion{Version: version.Version,
				PublishedAt: version.PublishedAt, Deprecated: version.Deprecated, Engines: version.Engines, Compatible: true,
				Update: marketVersionUpdatable(plugin.Name, version, bundle, hasBundle)})
		}
		if len(item.Versions) > 0 {
			item.DefaultVersion = item.Versions[0].Version
		}
		if wantsVersion, ok := installedVersionOf(installed, plugin.Name); ok {
			if len(item.Versions) > 0 && wantsVersion == item.Versions[0].Version {
				item.Installation = map[string]any{"status": "installed", "installedVersion": wantsVersion}
			} else {
				item.Installation = map[string]any{"status": "different-version", "installedVersion": wantsVersion}
			}
		}
		plugins = append(plugins, item)
	}
	return transport.OfficialMarketSearchResult{Index: transport.OfficialMarketIndex{URL: rawURL,
		Title: result.Index.Title, Description: result.Index.Description}, Plugins: plugins,
		Total: result.Total, Offset: result.Offset, NextOffset: result.NextOffset, FetchedAt: result.FetchedAt}, nil
}

func installedVersionOf(installed map[string]pluginmgr.BundleState, name string) (string, bool) {
	bundle, ok := installed[name]
	if !ok || bundle.Version == "" {
		return "", false
	}
	return bundle.Version, true
}

// marketInstallSpec 复刻官方 `marketInstallTarget`（dsh-plugin-market-index/src/index.ts:329-334）：
// npm → `name@version`；git → `git+<url>.git#<commit>[&path:/<dir>]`。
//
// 索引没声明 source（rc.2 之前的老索引）时返回 false：没有来源信息就判不了
// "同仓库另一 commit"，`update` 只能是 false——这是索引缺字段，不是官方不支持。
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
	for _, text := range []map[string]string{plugin.Description, plugin.Title} {
		if value := localizedMarketText(text, language); value != "" {
			return transport.OfficialMarketEntryDetail{Status: "found", Origin: "readme", Markdown: value, Document: document}, nil
		}
	}
	return transport.OfficialMarketEntryDetail{Status: "none"}, nil
}

// localizedMarketText 与 internal/market 的 localized 同一条口径：
// 精确语言 → 语言主标签 → en。
func localizedMarketText(text map[string]string, language string) string {
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

func (a *pluginMarketAPI) Fastest(ctx context.Context, urls []string) (transport.OfficialRegistryProbeResult, error) {
	if len(urls) == 0 && a.probe != nil && a.probe.Base() != "" {
		urls = []string{a.probe.Base()}
	}
	if len(urls) == 0 {
		return transport.OfficialRegistryProbeResult{}, &transport.PluginMarketError{Status: 503, Code: "registry_unavailable",
			Message: "pluginRegistryProbe.fastest 没有 registry URL 可探测（设置 DSHD_PLUGIN_REGISTRY 或传 urls）"}
	}
	type measured struct {
		url string
		d   time.Duration
	}
	results := make([]measured, 0, len(urls))
	for _, raw := range urls {
		start := time.Now()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(raw, "/")+"/-/ping", nil)
		if err != nil {
			continue
		}
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err == nil {
			resp.Body.Close()
		}
		results = append(results, measured{url: raw, d: time.Since(start)})
	}
	if len(results) == 0 {
		return transport.OfficialRegistryProbeResult{}, &transport.PluginMarketError{Status: 502, Code: "provider_unavailable", Message: "pluginRegistryProbe.fastest 所有 URL 都不可达"}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].d < results[j].d })
	return transport.OfficialRegistryProbeResult{Registry: results[0].url, LatencyMs: results[0].d.Milliseconds()}, nil
}

var _ transport.OfficialPluginMarketAPI = (*pluginMarketAPI)(nil)
var _ transport.OfficialPluginRegistryProbeAPI = (*pluginMarketAPI)(nil)
