package main

// PARITY-W3-MARKET：pluginMarket 4 方法与 pluginRegistryProbe.fastest 的宿主实现。
// 索引存储/HTTP 在 internal/market；安装状态来自当前 pluginmgr.Manager。

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"dshgo/internal/market"
	"dshgo/internal/pluginmgr"
	"dshgo/internal/transport"
)

type pluginMarketAPI struct {
	store   *market.Store
	manager *pluginmgr.Manager
	probe   *pluginmgr.RegistryClient
}

func newPluginMarketAPI(home string, manager *pluginmgr.Manager) *pluginMarketAPI {
	return &pluginMarketAPI{store: market.Open(home), manager: manager, probe: pluginmgr.RegistryFromEnv(nil)}
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

func (a *pluginMarketAPI) Search(ctx context.Context, rawURL string, q transport.OfficialMarketQuery) (transport.OfficialMarketSearchResult, error) {
	if strings.TrimSpace(rawURL) == "" {
		return transport.OfficialMarketSearchResult{}, &transport.PluginMarketError{Status: 400, Code: "bad_request", Message: "缺 url"}
	}
	result, err := a.store.Search(ctx, rawURL, market.Query{Text: q.Text, Category: q.Category, Labels: q.Labels,
		Sort: q.Sort, Offset: q.Offset, Limit: q.Limit, Refresh: q.Refresh, FetchedAt: q.FetchedAt})
	if err != nil {
		return transport.OfficialMarketSearchResult{}, &transport.PluginMarketError{Status: 502, Code: "index_unavailable", Message: err.Error()}
	}
	installed := map[string]string{}
	if a.manager != nil {
		if bundles, err := a.manager.Bundles(); err == nil {
			for _, bundle := range bundles {
				installed[bundle.Name] = bundle.Version
			}
		}
	}
	plugins := make([]transport.OfficialMarketPlugin, 0, len(result.Plugins))
	for _, plugin := range result.Plugins {
		item := transport.OfficialMarketPlugin{Name: plugin.Name, Title: plugin.Title, Description: plugin.Description,
			Author: plugin.Author, Keywords: plugin.Keywords, Category: plugin.Category, Labels: plugin.Labels,
			Installation: map[string]any{"status": "not-installed"}}
		for _, version := range plugin.Versions {
			item.Versions = append(item.Versions, transport.OfficialMarketPluginVersion{Version: version.Version,
				PublishedAt: version.PublishedAt, Deprecated: version.Deprecated, Engines: version.Engines, Compatible: true})
		}
		if len(item.Versions) > 0 {
			item.DefaultVersion = item.Versions[0].Version
		}
		if want, ok := installed[plugin.Name]; ok {
			if len(item.Versions) > 0 && want == item.Versions[0].Version {
				item.Installation = map[string]any{"status": "installed", "installedVersion": want}
			} else {
				item.Installation = map[string]any{"status": "different-version", "installedVersion": want}
			}
		}
		plugins = append(plugins, item)
	}
	return transport.OfficialMarketSearchResult{Index: transport.OfficialMarketIndex{URL: rawURL,
		Title: result.Index.Title, Description: result.Index.Description}, Plugins: plugins,
		Total: result.Total, Offset: result.Offset, NextOffset: result.NextOffset, FetchedAt: result.FetchedAt}, nil
}

func (a *pluginMarketAPI) ReadEntryDetail(ctx context.Context, rawURL, name, version, language string) (transport.OfficialMarketEntryDetail, error) {
	if strings.TrimSpace(rawURL) == "" || strings.TrimSpace(name) == "" || strings.TrimSpace(version) == "" {
		return transport.OfficialMarketEntryDetail{}, &transport.PluginMarketError{
			Status: http.StatusBadRequest, Code: "bad_request",
			Message: "pluginMarket.readEntryDetail 需要 url / name / version（缺一个都读不到真实条目）"}
	}
	text, found, err := a.store.ReadEntryDetail(ctx, rawURL, name, version, language)
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
	return transport.OfficialMarketEntryDetail{Status: "found", Origin: "readme", Markdown: text, Document: name + "@" + version}, nil
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
