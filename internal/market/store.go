// Package market 是 pluginMarket/pluginRegistryProbe 的真实索引存储。
//
// 索引源 URL 持久化在 <home>/state/plugin-market-indexes.json；每次 list/search
// 通过真实 HTTP 取索引文档（不编造条目）。没有配置时调用方回结构化
// registry_unavailable，不把空数组当成功。
//
// # 索引文档格式 = 官方 format 1
//
// 权威是 `@deepseek-ai/dsh-plugin-market-index` 的 `parseMarketIndex`
// （`dsh-plugin-market-index/src/index.ts`）：
//
//	{format:1, title, description?, metadata{categories,labels,sortKeys}, plugins:[…]}
//
// 其中 `metadata.categories/labels/sortKeys` 是**市场声明**的分类/标签/排序键
// （id → 显示标题；排序键另有初始方向），插件自己的
// `metadata{category,keywords,labels,sortKeys}` 只能引用已声明的 id —— 官方
// validator 对未声明的 id 直接判索引非法，这里同一条规则。没有这三张声明表，
// 客户端就画不出分类菜单/标签过滤/排序键（GUI #30 的 A/B 两块正是卡在这里）。
//
// 本文件此前只解析 `{version:1,…}` 的自造形状（插件顶层 `category/labels/keywords`、
// 版本 `publishedAt` 字符串、没有 `source`）。该形状**产不出官方版本行**：
// 官方 `versions[].source` 必填、`install.spec` 由它算出，缺了就只能编造安装来源。
// 所以现在只认 format 1；旧文档按官方口径判"索引非法"并带出原因。
package market

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"dshgo/internal/pluginmgr"
)

// LocalizedText 是官方 LocalizedText：一个字符串，或 `{en:"…", zh:"…"}` 对象。
//
// 官方 union 两种写法都合法；解析时字符串归一成 `{"en": 值}`，下发时单语言回
// 字符串、多语言回对象（客户端两种都收）。
type LocalizedText map[string]string

// UnmarshalJSON 接受官方 union 的两种写法。
func (t *LocalizedText) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*t = nil
		return nil
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return err
		}
		*t = LocalizedText{"en": text}
		return nil
	}
	var values map[string]string
	if err := json.Unmarshal(trimmed, &values); err != nil {
		return err
	}
	*t = values
	return nil
}

// MarshalJSON 按官方 union 下发：单语言回字符串，其余回对象。
func (t LocalizedText) MarshalJSON() ([]byte, error) {
	if len(t) == 1 {
		for _, value := range t {
			return json.Marshal(value)
		}
	}
	return json.Marshal(map[string]string(t))
}

// Declaration 是市场声明的一类 id（分类或标签）：id → 显示标题。
type Declaration struct {
	Title LocalizedText `json:"title"`
}

// SortKeyDeclaration 是市场声明的排序键：显示标题 + 初始方向。
type SortKeyDeclaration struct {
	Title LocalizedText `json:"title"`
	Order string        `json:"order"`
}

// IndexMetadata 是索引文档 `metadata` 的三张声明表；缺省组归一成空表。
type IndexMetadata struct {
	Categories map[string]Declaration        `json:"categories"`
	Labels     map[string]Declaration        `json:"labels"`
	SortKeys   map[string]SortKeyDeclaration `json:"sortKeys"`
}

// PluginMetadata 是插件自己的市场元数据（官方 MarketPluginMetadata）。
type PluginMetadata struct {
	// Category 必须引用 IndexMetadata.Categories 里已声明的 id。
	Category string `json:"category,omitempty"`
	// Keywords 是自由文本（惯例来自 package keywords），缺席归一成空数组。
	Keywords []string `json:"keywords"`
	// Labels 必须引用 IndexMetadata.Labels 里已声明的 id。
	Labels []string `json:"labels"`
	// SortKeys 是已声明排序键的数值；不可用（JSON null/缺席）的键被丢掉，0 保留。
	SortKeys map[string]float64 `json:"sortKeys"`
}

// Repository 是插件源码仓库的展示页与包目录（官方 MarketRepository）。
type Repository struct {
	URL       string `json:"url"`
	Directory string `json:"directory,omitempty"`
}

// Engines 是这一版运行时的要求（官方 MarketEngines，目前只认 dsh）。
type Engines struct {
	DSH string `json:"dsh,omitempty"`
}

// Plugin 是索引里的一个可安装包（官方 MarketPlugin）。
type Plugin struct {
	Name        string         `json:"name"`
	Title       LocalizedText  `json:"title,omitempty"`
	Description LocalizedText  `json:"description,omitempty"`
	Icon        string         `json:"icon,omitempty"`
	Author      string         `json:"author,omitempty"`
	Homepage    string         `json:"homepage,omitempty"`
	Repository  *Repository    `json:"repository,omitempty"`
	Metadata    PluginMetadata `json:"metadata"`
	// Versions 按 SemVer 降序（官方 validator 同样在解析时排序）。
	Versions []Version `json:"versions"`
}

// Version 是一个确切的可安装版本（官方 MarketVersion）。
type Version struct {
	Version          string  `json:"version"`
	PublishTimestamp int64   `json:"publishTimestamp"`
	Source           *Source `json:"source,omitempty"`
	Engines          Engines `json:"engines"`
	// Readme 是索引内联的长文档：官方 detail 走 npm CDN / Git 提交，不读索引内联，
	// 所以这是宿主扩展；索引没写就没有（详情走仓库 README 回退）。
	Readme string `json:"readme,omitempty"`
	// Deprecated 是宿主可读的非官方字段（官方 format 1 不定义）。官方兼容性/弃用
	// 信息来自 registry 的 packument，索引里没有；这里保留读取只为不丢既有信息。
	Deprecated string `json:"deprecated,omitempty"`
}

// Source 是官方索引的每版本安装来源（MarketSource 的宿主形状）。
type Source struct {
	// Type 是 "npm" 或 "git"；其它值判索引非法（官方 validator 同一规则）。
	Type string `json:"type"`
	// Registry 是 npm 索引地址（缺省 = 客户端配置的 registry）。
	Registry string `json:"registry,omitempty"`
	// URL 是 git 仓库的规范 https 地址（无凭据/query/fragment）。
	URL string `json:"url,omitempty"`
	// Commit 是小写 40 位 commit SHA（git 来源必填，官方语义只认完整 commit）。
	Commit string `json:"commit,omitempty"`
	// Path 是包在仓库里的相对 POSIX 目录（缺省 = 仓库根）。
	Path string `json:"path,omitempty"`
}

// Document 是一份通过校验的 format 1 索引。
type Document struct {
	Format      int           `json:"format"`
	Title       LocalizedText `json:"title"`
	Description LocalizedText `json:"description,omitempty"`
	Metadata    IndexMetadata `json:"metadata"`
	Plugins     []Plugin      `json:"plugins"`
}

// Entry 是一个索引源（listIndexes 的一项）。
type Entry struct {
	URL         string        `json:"url"`
	Title       LocalizedText `json:"title,omitempty"`
	Description LocalizedText `json:"description,omitempty"`
	Error       string        `json:"error,omitempty"`
}

type Store struct {
	mu     sync.Mutex
	path   string
	URLs   []string
	Client *http.Client
}

type persisted struct {
	Version int      `json:"version"`
	URLs    []string `json:"urls"`
}

func Open(home string) *Store {
	client := &http.Client{Timeout: 30 * time.Second}
	if os.Getenv("DSHD_PLUGIN_MARKET_INSECURE") == "1" {
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} // 本地 HTTPS 夹具
	}
	s := &Store{path: filepath.Join(home, "state", "plugin-market-indexes.json"), Client: client}
	raw, err := os.ReadFile(s.path)
	if err == nil {
		var doc persisted
		if json.Unmarshal(raw, &doc) == nil && doc.Version == 1 {
			s.URLs = append([]string{}, doc.URLs...)
		}
	}
	return s
}

func (s *Store) ListURLs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.URLs...)
}

func (s *Store) SetURLs(urls []string) error {
	clean := make([]string, 0, len(urls))
	seen := map[string]bool{}
	for _, raw := range urls {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("索引 URL 必须是 http(s) 绝对 URL: %q", raw)
		}
		if seen[u.String()] {
			continue
		}
		seen[u.String()] = true
		clean = append(clean, u.String())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(persisted{Version: 1, URLs: clean}, "", "  ")
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.URLs = clean
	return nil
}

func (s *Store) List(ctx context.Context) ([]Entry, error) {
	urls := s.ListURLs()
	if len(urls) == 0 {
		return nil, ErrNoIndexes
	}
	out := make([]Entry, 0, len(urls))
	for _, raw := range urls {
		doc, err := s.fetch(ctx, raw)
		entry := Entry{URL: raw}
		if err != nil {
			entry.Error = err.Error()
		} else {
			entry.Title = doc.Title
			entry.Description = doc.Description
		}
		out = append(out, entry)
	}
	return out, nil
}

type Query struct {
	Text      string
	Category  string
	Labels    []string
	Sort      map[string]any
	Offset    int
	Limit     int
	Refresh   bool
	FetchedAt int64
}

type SearchResult struct {
	Index      Document
	Plugins    []Plugin
	Total      int
	Offset     int
	NextOffset *int
	FetchedAt  int64
}

func (s *Store) Search(ctx context.Context, rawURL string, q Query) (SearchResult, error) {
	doc, err := s.fetch(ctx, rawURL)
	if err != nil {
		return SearchResult{}, err
	}
	if q.Limit <= 0 || q.Limit > 100 {
		q.Limit = 50
	}
	if q.Offset < 0 {
		q.Offset = 0
	}
	words := searchWords(q.Text)
	matched := make([]Plugin, 0, len(doc.Plugins))
	matchedScores := make([]float64, 0, len(doc.Plugins))
	for _, plugin := range doc.Plugins {
		if q.Category != "" && plugin.Metadata.Category != q.Category {
			continue
		}
		if !hasAll(plugin.Metadata.Labels, q.Labels) {
			continue
		}
		score, ok := fuzzyMatch(plugin, words, doc.Metadata)
		if !ok {
			continue
		}
		matched = append(matched, plugin)
		matchedScores = append(matchedScores, score)
	}
	matched = sortPlugins(matched, matchedScores, q.Sort)
	total := len(matched)
	start := q.Offset
	if start > total {
		start = total
	}
	end := start + q.Limit
	if end > total {
		end = total
	}
	var next *int
	if end < total {
		n := end
		next = &n
	}
	return SearchResult{Index: doc, Plugins: matched[start:end], Total: total, Offset: q.Offset, NextOffset: next, FetchedAt: time.Now().UnixMilli()}, nil
}

// FindVersion 返回索引里**确切版本**的插件与版本条目（含每版本 Source）。
// 与 ReadEntryDetail 的区别：详情回退到真实 Git 仓库时需要的正是 Source，
// 而 ReadEntryDetail 只回答"有没有内联文档"。找不到时 found=false。
func (s *Store) FindVersion(ctx context.Context, rawURL, name, version string) (Plugin, Version, bool, error) {
	doc, err := s.fetch(ctx, rawURL)
	if err != nil {
		return Plugin{}, Version{}, false, err
	}
	for _, plugin := range doc.Plugins {
		if plugin.Name != name {
			continue
		}
		for _, entry := range plugin.Versions {
			if entry.Version == version {
				return plugin, entry, true, nil
			}
		}
		return Plugin{}, Version{}, false, nil
	}
	return Plugin{}, Version{}, false, nil
}

func (s *Store) ReadEntryDetail(ctx context.Context, rawURL, name, version, language string) (string, bool, error) {
	doc, err := s.fetch(ctx, rawURL)
	if err != nil {
		return "", false, err
	}
	for _, plugin := range doc.Plugins {
		if plugin.Name != name {
			continue
		}
		for _, v := range plugin.Versions {
			if v.Version != version {
				continue
			}
			if v.Readme != "" {
				return v.Readme, true, nil
			}
			if text := localized(plugin.Description, language); text != "" {
				return text, true, nil
			}
			if text := localized(plugin.Title, language); text != "" {
				return text, true, nil
			}
		}
	}
	return "", false, nil
}

func (s *Store) fetch(ctx context.Context, rawURL string) (Document, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return Document{}, err
	}
	req.Header.Set("Accept", "application/json")
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Document{}, fmt.Errorf("取索引 %s 失败: %w", rawURL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return Document{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Document{}, fmt.Errorf("索引 %s 返回 HTTP %d: %s", rawURL, resp.StatusCode, string(body[:min(len(body), 300)]))
	}
	return decodeDocument(body, rawURL)
}

// rawDocument / rawPlugin / rawVersion 是"还没按 format 1 归一"的解码层。
//
// 分成两层的原因：官方 validator 会**规范化**文档（缺省 metadata 组填空表、
// 版本按 SemVer 降序、相对 icon 解析成绝对 URL、null 排序键丢掉），而规范化
// 要看得见"字段是缺席还是零值"，所以这里用指针区分缺席。
type rawDocument struct {
	Format      *int              `json:"format"`
	Title       LocalizedText     `json:"title"`
	Description LocalizedText     `json:"description"`
	Metadata    *rawIndexMetadata `json:"metadata"`
	Plugins     []rawPlugin       `json:"plugins"`
}

type rawIndexMetadata struct {
	Categories map[string]Declaration        `json:"categories"`
	Labels     map[string]Declaration        `json:"labels"`
	SortKeys   map[string]SortKeyDeclaration `json:"sortKeys"`
}

type rawPlugin struct {
	Name        string             `json:"name"`
	Title       LocalizedText      `json:"title"`
	Description LocalizedText      `json:"description"`
	Icon        string             `json:"icon"`
	Author      string             `json:"author"`
	Homepage    string             `json:"homepage"`
	Repository  *Repository        `json:"repository"`
	Metadata    *rawPluginMetadata `json:"metadata"`
	Versions    []rawVersion       `json:"versions"`
}

type rawPluginMetadata struct {
	Category string              `json:"category"`
	Keywords []string            `json:"keywords"`
	Labels   []string            `json:"labels"`
	SortKeys map[string]*float64 `json:"sortKeys"`
}

type rawVersion struct {
	Version          string  `json:"version"`
	PublishTimestamp *int64  `json:"publishTimestamp"`
	Source           *Source `json:"source"`
	Engines          Engines `json:"engines"`
	Readme           string  `json:"readme"`
	Deprecated       string  `json:"deprecated"`
}

// decodeDocument 按官方 format 1 校验并归一一份索引文档。
//
// 校验面与 `parseMarketIndex` 对齐（不多要求、也不少要求）：format 必须 1；
// 标题必填；每个包必须有 name 与非空 versions；每版必须有确切 SemVer、整数
// publishTimestamp、合法 source；插件 category/labels/sortKeys 只能引用已声明 id。
func decodeDocument(body []byte, indexURL string) (Document, error) {
	var raw rawDocument
	if err := json.Unmarshal(body, &raw); err != nil {
		return Document{}, fmt.Errorf("索引 %s 不是合法 JSON: %w", indexURL, err)
	}
	if raw.Format == nil {
		return Document{}, fmt.Errorf("索引 %s 缺 format：官方索引必须声明 format: 1", indexURL)
	}
	if *raw.Format != 1 {
		return Document{}, fmt.Errorf("索引 %s 的 format 必须是 1，实际 %d", indexURL, *raw.Format)
	}
	if !hasLocalizedText(raw.Title) {
		return Document{}, fmt.Errorf("索引 %s 缺 title（format 1 必填，且必须有非空文本）", indexURL)
	}
	if raw.Plugins == nil {
		return Document{}, fmt.Errorf("索引 %s 缺 plugins 数组", indexURL)
	}
	metadata := indexMetadataOf(raw.Metadata)
	base, baseErr := url.Parse(indexURL)
	if baseErr != nil {
		base = nil
	}
	doc := Document{Format: 1, Title: raw.Title, Description: raw.Description, Metadata: metadata,
		Plugins: make([]Plugin, 0, len(raw.Plugins))}
	seen := make(map[string]bool, len(raw.Plugins))
	for position, rawPlugin := range raw.Plugins {
		plugin, err := pluginOf(rawPlugin, position, metadata, base)
		if err != nil {
			return Document{}, fmt.Errorf("索引 %s: %w", indexURL, err)
		}
		if seen[plugin.Name] {
			return Document{}, fmt.Errorf("索引 %s: $.plugins[%d].name 重复 %q", indexURL, position, plugin.Name)
		}
		seen[plugin.Name] = true
		doc.Plugins = append(doc.Plugins, plugin)
	}
	return doc, nil
}

// hasLocalizedText 判一段 LocalizedText 有没有非空文本（官方 stringAt 要求
// 至少一个非空白的字符）。
func hasLocalizedText(text LocalizedText) bool {
	for _, value := range text {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func indexMetadataOf(raw *rawIndexMetadata) IndexMetadata {
	out := IndexMetadata{Categories: map[string]Declaration{}, Labels: map[string]Declaration{},
		SortKeys: map[string]SortKeyDeclaration{}}
	if raw == nil {
		return out
	}
	for id, declaration := range raw.Categories {
		out.Categories[id] = declaration
	}
	for id, declaration := range raw.Labels {
		out.Labels[id] = declaration
	}
	for id, declaration := range raw.SortKeys {
		out.SortKeys[id] = declaration
	}
	return out
}

func pluginOf(raw rawPlugin, position int, metadata IndexMetadata, base *url.URL) (Plugin, error) {
	name := strings.TrimSpace(raw.Name)
	if name == "" {
		return Plugin{}, fmt.Errorf("$.plugins[%d].name 必须是非空字符串", position)
	}
	if len(raw.Versions) == 0 {
		return Plugin{}, fmt.Errorf("$.plugins[%d].versions 必须是非空数组", position)
	}
	plugin := Plugin{Name: name, Title: raw.Title, Description: raw.Description,
		Icon: resolveIcon(raw.Icon, base), Author: strings.TrimSpace(raw.Author),
		Homepage: strings.TrimSpace(raw.Homepage), Repository: raw.Repository}
	pluginMetadata, err := pluginMetadataOf(raw.Metadata, metadata, position)
	if err != nil {
		return Plugin{}, err
	}
	plugin.Metadata = pluginMetadata
	plugin.Versions = make([]Version, 0, len(raw.Versions))
	for index, rawVersion := range raw.Versions {
		version, err := versionOf(name, rawVersion, position, index)
		if err != nil {
			return Plugin{}, err
		}
		plugin.Versions = append(plugin.Versions, version)
	}
	sortVersionsDescending(plugin.Versions)
	return plugin, nil
}

// pluginMetadataOf 归一插件元数据，并复刻官方"只能引用已声明 id"的校验：
// 未声明的分类/标签/排序键判索引非法（官方 validator 同一规则），而不是静默丢掉。
func pluginMetadataOf(raw *rawPluginMetadata, metadata IndexMetadata, position int) (PluginMetadata, error) {
	out := PluginMetadata{Keywords: []string{}, Labels: []string{}, SortKeys: map[string]float64{}}
	if raw == nil {
		return out, nil
	}
	out.Category = strings.TrimSpace(raw.Category)
	if out.Category != "" {
		if _, declared := metadata.Categories[out.Category]; !declared {
			return PluginMetadata{}, fmt.Errorf("$.plugins[%d].metadata.category 引用了未声明的分类 %q", position, out.Category)
		}
	}
	if raw.Keywords != nil {
		out.Keywords = append([]string{}, raw.Keywords...)
	}
	for _, label := range raw.Labels {
		label = strings.TrimSpace(label)
		if label == "" {
			continue
		}
		if _, declared := metadata.Labels[label]; !declared {
			return PluginMetadata{}, fmt.Errorf("$.plugins[%d].metadata.labels 引用了未声明的标签 %q", position, label)
		}
		out.Labels = append(out.Labels, label)
	}
	for id, value := range raw.SortKeys {
		if value == nil {
			// 官方：null 排序值省略，0 保留（0 是测出来的值）。
			continue
		}
		if _, declared := metadata.SortKeys[id]; !declared {
			return PluginMetadata{}, fmt.Errorf("$.plugins[%d].metadata.sortKeys 引用了未声明的排序键 %q", position, id)
		}
		if math.IsNaN(*value) || math.IsInf(*value, 0) {
			return PluginMetadata{}, fmt.Errorf("$.plugins[%d].metadata.sortKeys[%q] 必须是有限数字", position, id)
		}
		out.SortKeys[id] = *value
	}
	return out, nil
}

func versionOf(name string, raw rawVersion, position, index int) (Version, error) {
	version := strings.TrimSpace(raw.Version)
	if version == "" {
		return Version{}, fmt.Errorf("$.plugins[%d].versions[%d].version 必须是非空字符串", position, index)
	}
	if raw.PublishTimestamp == nil {
		return Version{}, fmt.Errorf("$.plugins[%d].versions[%d].publishTimestamp 必须是 Unix 秒整数（format 1 必填）", position, index)
	}
	if *raw.PublishTimestamp < 0 {
		return Version{}, fmt.Errorf("$.plugins[%d].versions[%d].publishTimestamp 不能为负", position, index)
	}
	source, err := sourceOf(name, version, raw.Source)
	if err != nil {
		return Version{}, fmt.Errorf("$.plugins[%d].versions[%d]: %w", position, index, err)
	}
	return Version{Version: version, PublishTimestamp: *raw.PublishTimestamp, Source: source,
		Engines: Engines{DSH: strings.TrimSpace(raw.Engines.DSH)}, Readme: raw.Readme,
		Deprecated: strings.TrimSpace(raw.Deprecated)}, nil
}

// sourceOf 校验官方 MarketSource union。source 必填：install.spec 由它算出，
// 缺了就只剩编造安装来源这一条路。
func sourceOf(name, version string, raw *Source) (*Source, error) {
	if raw == nil {
		return nil, fmt.Errorf("version %q 缺 source（官方 format 1 必有 npm|git 来源，缺了算不出 install.spec）", version)
	}
	source := &Source{Registry: strings.TrimSpace(raw.Registry), URL: strings.TrimSpace(raw.URL),
		Commit: strings.TrimSpace(raw.Commit), Path: strings.Trim(strings.TrimSpace(raw.Path), "/")}
	switch strings.ToLower(strings.TrimSpace(raw.Type)) {
	case "npm":
		source.Type = "npm"
		return source, nil
	case "git":
		source.Type = "git"
		if source.URL == "" || source.Commit == "" {
			return nil, fmt.Errorf("version %q 的 git source 必须有 url 与 commit（%s）", version, name)
		}
		return source, nil
	default:
		return nil, fmt.Errorf("version %q 的 source.type 必须是 npm 或 git，实际 %q", version, raw.Type)
	}
}

// resolveIcon 复刻官方"相对 icon 引用按索引 URL 解析成绝对 URL"。
func resolveIcon(icon string, base *url.URL) string {
	icon = strings.TrimSpace(icon)
	if icon == "" || strings.HasPrefix(icon, "data:") || base == nil {
		return icon
	}
	ref, err := url.Parse(icon)
	if err != nil || ref.IsAbs() {
		return icon
	}
	return base.ResolveReference(ref).String()
}

// sortVersionsDescending 按 SemVer 优先级降序（官方 validator 同一行为）。
// 解析不了版本号时保持原顺序（stable），不猜。
func sortVersionsDescending(versions []Version) {
	sort.SliceStable(versions, func(i, j int) bool {
		comparison, err := pluginmgr.CompareVersions(versions[i].Version, versions[j].Version)
		if err != nil {
			return false
		}
		return comparison > 0
	})
}

var ErrNoIndexes = errors.New("没有配置 plugin market 索引源")

func hasAll(have, want []string) bool {
	for _, w := range want {
		found := false
		for _, h := range have {
			if h == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// searchWords 切词：官方按 NFKC 归一后按"字母/数字串"切分（CJK 另有二元组规则，
// 这里按同一取向只切非空词）。没有字母数字的查询匹配所有插件。
func searchWords(text string) []string {
	return strings.Fields(strings.ToLower(strings.TrimSpace(text)))
}

// searchField 是一个可搜索字段及其相关度权重。
type searchField struct {
	text   string
	weight float64
}

// fuzzyFields 是官方可搜字段：名字、作者、关键词、标题/描述的每种语言、
// 以及分类标题与标签标题的每种语言（官方 MiniSearch 的 fields 表）。
func fuzzyFields(plugin Plugin, metadata IndexMetadata) []searchField {
	fields := []searchField{{plugin.Name, 3}, {plugin.Author, 1}, {strings.Join(plugin.Metadata.Keywords, " "), 2}}
	for _, text := range []LocalizedText{plugin.Title, plugin.Description} {
		for _, value := range text {
			fields = append(fields, searchField{value, 3})
		}
	}
	for _, value := range taxonomyTitle(metadata.Categories, plugin.Metadata.Category) {
		fields = append(fields, searchField{value, 1})
	}
	for _, label := range plugin.Metadata.Labels {
		for _, value := range taxonomyTitle(metadata.Labels, label) {
			fields = append(fields, searchField{value, 1})
		}
	}
	return fields
}

func taxonomyTitle(declarations map[string]Declaration, id string) LocalizedText {
	if id == "" {
		return nil
	}
	if declaration, ok := declarations[id]; ok {
		return declaration.Title
	}
	return nil
}

// fuzzyMatch 返回"每个查询词都在某个字段里出现过"以及一个真实的相关度分
// （命中字段的权重和）。前缀/编辑距离匹配是官方 MiniSearch 的细节，宿主这里用
// "子串命中"实现：宁可少命中，也不假装能和官方同一排名。
func fuzzyMatch(plugin Plugin, words []string, metadata IndexMetadata) (float64, bool) {
	if len(words) == 0 {
		return 0, true
	}
	fields := fuzzyFields(plugin, metadata)
	score := 0.0
	for _, word := range words {
		hit := 0.0
		for _, field := range fields {
			if strings.Contains(strings.ToLower(field.text), word) && field.weight > hit {
				hit = field.weight
			}
		}
		if hit == 0 {
			return 0, false
		}
		score += hit
	}
	return score, true
}

func localized(m LocalizedText, language string) string {
	if m != nil {
		if v := m[language]; v != "" {
			return v
		}
		if i := strings.Index(language, "-"); i > 0 {
			if v := m[language[:i]]; v != "" {
				return v
			}
		}
	}
	return m["en"]
}

// sortPlugins 按官方 MarketSort 排序；同分/缺失值都回到索引原序（稳定）。
//
//   - index：索引原序（不排）；
//   - relevance：命中分降序；没有 text 时就是索引原序；
//   - published：每包**最新**发布时刻（Unix 秒）；
//   - key：已声明排序键的值，没有值的包在两个方向上都在最后。
func sortPlugins(plugins []Plugin, scores []float64, sortSpec map[string]any) []Plugin {
	kind, _ := sortSpec["kind"].(string)
	direction, _ := sortSpec["direction"].(string)
	desc := direction != "asc"
	switch kind {
	case "published":
		sort.SliceStable(plugins, func(i, j int) bool {
			left, right := latestPublished(plugins[i]), latestPublished(plugins[j])
			if left == right {
				return false
			}
			if desc {
				return left > right
			}
			return left < right
		})
	case "key":
		id, _ := sortSpec["id"].(string)
		sort.SliceStable(plugins, func(i, j int) bool {
			left, leftOK := plugins[i].Metadata.SortKeys[id]
			right, rightOK := plugins[j].Metadata.SortKeys[id]
			if leftOK != rightOK {
				return leftOK
			}
			if !leftOK || left == right {
				return false
			}
			if desc {
				return left > right
			}
			return left < right
		})
	case "relevance":
		if len(scores) != len(plugins) {
			return plugins
		}
		type scored struct {
			plugin Plugin
			score  float64
		}
		ranked := make([]scored, len(plugins))
		for index := range plugins {
			ranked[index] = scored{plugin: plugins[index], score: scores[index]}
		}
		sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
		for index := range ranked {
			plugins[index] = ranked[index].plugin
		}
	}
	return plugins
}

func latestPublished(plugin Plugin) int64 {
	var latest int64
	for _, version := range plugin.Versions {
		if version.PublishTimestamp > latest {
			latest = version.PublishTimestamp
		}
	}
	return latest
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
