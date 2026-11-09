// Package market 是 pluginMarket/pluginRegistryProbe 的真实索引存储。
//
// 索引源 URL 持久化在 <home>/state/plugin-market-indexes.json；每次 list/search
// 通过真实 HTTP 取索引文档（不编造条目）。没有配置时调用方回结构化
// registry_unavailable，不把空数组当成功。
package market

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Document struct {
	Version     int               `json:"version"`
	Title       map[string]string `json:"title"`
	Description map[string]string `json:"description"`
	Plugins     []Plugin          `json:"plugins"`
}

type Plugin struct {
	Name        string            `json:"name"`
	Title       map[string]string `json:"title"`
	Description map[string]string `json:"description"`
	Author      string            `json:"author"`
	Keywords    []string          `json:"keywords"`
	Category    string            `json:"category"`
	Labels      []string          `json:"labels"`
	Versions    []Version         `json:"versions"`
	Repository  map[string]string `json:"repository"`
}

type Version struct {
	Version     string            `json:"version"`
	PublishedAt string            `json:"publishedAt"`
	Deprecated  string            `json:"deprecated"`
	Engines     map[string]string `json:"engines"`
	Readme      string            `json:"readme"`
}

type Entry struct {
	URL         string            `json:"url"`
	Title       map[string]string `json:"title,omitempty"`
	Description map[string]string `json:"description,omitempty"`
	Error       string            `json:"error,omitempty"`
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
	matched := make([]Plugin, 0, len(doc.Plugins))
	for _, plugin := range doc.Plugins {
		if q.Category != "" && plugin.Category != q.Category {
			continue
		}
		if !hasAll(plugin.Labels, q.Labels) {
			continue
		}
		if !fuzzyMatch(plugin, q.Text) {
			continue
		}
		matched = append(matched, plugin)
	}
	sortPlugins(matched, q.Sort)
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
	var doc Document
	if err := json.Unmarshal(body, &doc); err != nil {
		return Document{}, fmt.Errorf("索引 %s 不是合法 JSON: %w", rawURL, err)
	}
	if doc.Version != 1 {
		return Document{}, fmt.Errorf("索引 %s version 必须是 1，实际 %d", rawURL, doc.Version)
	}
	return doc, nil
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

func fuzzyMatch(plugin Plugin, text string) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return true
	}
	fields := []string{plugin.Name, plugin.Author, plugin.Category, strings.Join(plugin.Keywords, " ")}
	for _, m := range []map[string]string{plugin.Title, plugin.Description} {
		for _, v := range m {
			fields = append(fields, v)
		}
	}
	for _, word := range strings.Fields(text) {
		ok := false
		for _, field := range fields {
			if strings.Contains(strings.ToLower(field), word) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func localized(m map[string]string, language string) string {
	if language != "" {
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

func sortPlugins(plugins []Plugin, sortSpec map[string]any) {
	kind, _ := sortSpec["kind"].(string)
	direction, _ := sortSpec["direction"].(string)
	desc := direction != "asc"
	switch kind {
	case "published":
		sort.SliceStable(plugins, func(i, j int) bool {
			vi, vj := firstPublished(plugins[i]), firstPublished(plugins[j])
			if vi == vj {
				return plugins[i].Name < plugins[j].Name
			}
			if desc {
				return vi > vj
			}
			return vi < vj
		})
	case "key":
		id, _ := sortSpec["id"].(string)
		sort.SliceStable(plugins, func(i, j int) bool {
			a, b := pluginKey(plugins[i], id), pluginKey(plugins[j], id)
			if a == b {
				return plugins[i].Name < plugins[j].Name
			}
			if desc {
				return a > b
			}
			return a < b
		})
	default:
		sort.SliceStable(plugins, func(i, j int) bool { return plugins[i].Name < plugins[j].Name })
	}
}

func firstPublished(p Plugin) string {
	if len(p.Versions) == 0 {
		return ""
	}
	return p.Versions[0].PublishedAt
}

func pluginKey(p Plugin, id string) string {
	_ = id
	if len(p.Versions) == 0 {
		return ""
	}
	return p.Versions[0].Version
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
