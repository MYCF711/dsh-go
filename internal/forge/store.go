// Package forge 是 `forgePanel.*` 的**真实绑定存储**：Session ↔ change request URL。
//
// # 为什么单独一个包
//
// 官方把"哪条 change request 属于哪个会话"存在 forgeSession 绑定里，`track` /
// `dismiss` / `refresh` / `follow` 四个方法都读写它。本仓不能把这份状态挂在
// Transport 上（R7：Transport 不读盘、不认识 code host），也不能每个方法各造
// 一份——所以这里只有**一份**账本：`<home>/state/forge-sessions.json`。
//
// # 语义（与官方 dsh-experimental-forge 对齐）
//
//	Track    建/复用绑定；同一 (session,url) 幂等，不改 TrackedAt
//	Dismiss  把绑定标成"不再显示"，**保留记录**（不是删——删了下次 refresh
//	         就会以为从没绑过，用户会看到它又冒出来）
//	Refresh  走 Provider 真实拉取远端状态（HTTP），结果落进绑定
//	Follow   由 transport 侧做快照差异（本包只提供一致的 Snapshot）
//
// # 边界
//
// Provider 是可注入的：生产用真实 code host（当前未接 GitHub 凭据时返回
// ErrNoProvider，**不静默回旧值**）；测试用 httptest 真起一个 HTTP 服务。
package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// 结构化错误：调用方用 errors.Is 判断，不去比对字符串。
var (
	// ErrNotFound 表示这条 (session,url) 绑定不存在。
	ErrNotFound = errors.New("forge: 没有这条绑定")
	// ErrInvalidURL 表示 url 不是合法的 http(s) code host URL —— 本地就拒，不发出去。
	ErrInvalidURL = errors.New("forge: 不是合法的 code host URL")
	// ErrNoProvider 表示宿主没配 forge provider（没有凭据 / 没有实现）。
	ErrNoProvider = errors.New("forge: 没有配置 forge provider")
)

// storeVersion 是盘上格式版本。加字段保持向后兼容，不要静默改语义。
const storeVersion = 1

// RemoteState 是 provider 拉回来的远端状态。
type RemoteState struct {
	State   string `json:"state"`
	HeadSHA string `json:"headSha"`
	Title   string `json:"title"`
}

// Provider 是 code host 的只读拉取面。
type Provider interface {
	Fetch(ctx context.Context, url string) (RemoteState, error)
}

// Binding 是一条 forgeSession 绑定。
type Binding struct {
	SessionID   string    `json:"sessionId"`
	URL         string    `json:"url"`
	Provider    string    `json:"provider"`
	Tracked     bool      `json:"tracked"`
	State       string    `json:"state,omitempty"`
	Title       string    `json:"title,omitempty"`
	HeadSHA     string    `json:"headSha,omitempty"`
	TrackedAt   time.Time `json:"trackedAt"`
	DismissedAt time.Time `json:"dismissedAt,omitempty"`
	RefreshedAt time.Time `json:"refreshedAt,omitempty"`
}

type document struct {
	Version  int       `json:"version"`
	Bindings []Binding `json:"bindings"`
}

// Store 是绑定存储。并发安全（一把锁管读-改-写-落盘整段）。
type Store struct {
	mu       sync.Mutex
	home     string
	path     string
	provider Provider
	doc      document
	loaded   bool
}

// Open 打开（或初始化）`<home>/state/forge-sessions.json`。
func Open(home string) (*Store, error) {
	home = strings.TrimSpace(home)
	if home == "" {
		return nil, fmt.Errorf("forge: home 为空，不能开绑定存储")
	}
	return &Store{
		home: home,
		path: filepath.Join(home, "state", "forge-sessions.json"),
		doc:  document{Version: storeVersion},
	}, nil
}

// Path 返回盘上文件路径（诊断/测试用）。
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// SetProvider 注入 code host 拉取面。nil = 没有 provider（Refresh 结构化失败）。
func (s *Store) SetProvider(p Provider) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.provider = p
}

func (s *Store) ensureLoadedLocked() error {
	if s.loaded {
		return nil
	}
	s.loaded = true
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // 空 home：还没有账本，正常
		}
		return fmt.Errorf("forge: 读绑定存储失败: %w", err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}
	var doc document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("forge: 绑定存储损坏（%s）: %w", s.path, err)
	}
	if doc.Version == 0 {
		doc.Version = storeVersion
	}
	s.doc = doc
	return nil
}

// saveLocked 原子落盘（同目录临时文件 + rename）。
func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("forge: 建 state 目录失败: %w", err)
	}
	s.doc.Version = storeVersion
	raw, err := json.MarshalIndent(s.doc, "", "  ")
	if err != nil {
		return fmt.Errorf("forge: 编码绑定存储失败: %w", err)
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(s.path), "forge-sessions-*.tmp")
	if err != nil {
		return fmt.Errorf("forge: 建临时文件失败: %w", err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("forge: 写临时文件失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("forge: 关临时文件失败: %w", err)
	}
	if err := os.Rename(name, s.path); err != nil {
		return fmt.Errorf("forge: 落盘失败: %w", err)
	}
	return nil
}

func indexOf(list []Binding, sessionID, rawURL string) int {
	for i := range list {
		if list[i].SessionID == sessionID && list[i].URL == rawURL {
			return i
		}
	}
	return -1
}

// ValidateURL 只接受 http(s) 的绝对 URL（code host 的 change request 地址）。
func ValidateURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("%w：url 是空串", ErrInvalidURL)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w：%v", ErrInvalidURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w：只支持 http/https，得到 %q", ErrInvalidURL, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("%w：缺 host", ErrInvalidURL)
	}
	return nil
}

// Track 建/复用绑定。同一 (session,url) 幂等：不改 TrackedAt，不清 DismissedAt 之外的字段。
func (s *Store) Track(sessionID, rawURL, provider string) (Binding, error) {
	sessionID = strings.TrimSpace(sessionID)
	rawURL = strings.TrimSpace(rawURL)
	if sessionID == "" {
		return Binding{}, fmt.Errorf("forge: sessionId 是空串")
	}
	if err := ValidateURL(rawURL); err != nil {
		return Binding{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoadedLocked(); err != nil {
		return Binding{}, err
	}
	if i := indexOf(s.doc.Bindings, sessionID, rawURL); i >= 0 {
		// 幂等：已经是 tracked 就原样返回；被 dismiss 过则重新 track（清 DismissedAt）。
		if !s.doc.Bindings[i].Tracked {
			s.doc.Bindings[i].Tracked = true
			s.doc.Bindings[i].DismissedAt = time.Time{}
			if err := s.saveLocked(); err != nil {
				return Binding{}, err
			}
		}
		return s.doc.Bindings[i], nil
	}
	b := Binding{
		SessionID: sessionID,
		URL:       rawURL,
		Provider:  strings.TrimSpace(provider),
		Tracked:   true,
		TrackedAt: time.Now().UTC(),
	}
	s.doc.Bindings = append(s.doc.Bindings, b)
	if err := s.saveLocked(); err != nil {
		return Binding{}, err
	}
	return b, nil
}

// Dismiss 把绑定标成"不再显示"，**保留记录**（官方语义：隐藏一条绑定）。
func (s *Store) Dismiss(sessionID, rawURL string) (Binding, error) {
	sessionID = strings.TrimSpace(sessionID)
	rawURL = strings.TrimSpace(rawURL)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoadedLocked(); err != nil {
		return Binding{}, err
	}
	i := indexOf(s.doc.Bindings, sessionID, rawURL)
	if i < 0 {
		return Binding{}, fmt.Errorf("%w：session=%q url=%q", ErrNotFound, sessionID, rawURL)
	}
	s.doc.Bindings[i].Tracked = false
	s.doc.Bindings[i].DismissedAt = time.Now().UTC()
	if err := s.saveLocked(); err != nil {
		return Binding{}, err
	}
	return s.doc.Bindings[i], nil
}

// Get 读一条绑定。
func (s *Store) Get(sessionID, rawURL string) (Binding, error) {
	sessionID = strings.TrimSpace(sessionID)
	rawURL = strings.TrimSpace(rawURL)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoadedLocked(); err != nil {
		return Binding{}, err
	}
	i := indexOf(s.doc.Bindings, sessionID, rawURL)
	if i < 0 {
		return Binding{}, fmt.Errorf("%w：session=%q url=%q", ErrNotFound, sessionID, rawURL)
	}
	return s.doc.Bindings[i], nil
}

// List 返回按 (TrackedAt, URL) 排好的绑定快照（顺序确定，供 follow 做差异）。
func (s *Store) List() ([]Binding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureLoadedLocked(); err != nil {
		return nil, err
	}
	out := make([]Binding, len(s.doc.Bindings))
	copy(out, s.doc.Bindings)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TrackedAt.Equal(out[j].TrackedAt) {
			return out[i].URL < out[j].URL
		}
		return out[i].TrackedAt.Before(out[j].TrackedAt)
	})
	return out, nil
}

// Refresh 走 provider 真实拉取远端状态，并把结果落进绑定。
func (s *Store) Refresh(ctx context.Context, sessionID, rawURL string) (Binding, error) {
	sessionID = strings.TrimSpace(sessionID)
	rawURL = strings.TrimSpace(rawURL)
	s.mu.Lock()
	provider := s.provider
	loadedErr := s.ensureLoadedLocked()
	i := -1
	if loadedErr == nil {
		i = indexOf(s.doc.Bindings, sessionID, rawURL)
	}
	s.mu.Unlock()

	if loadedErr != nil {
		return Binding{}, loadedErr
	}
	if i < 0 {
		return Binding{}, fmt.Errorf("%w：session=%q url=%q", ErrNotFound, sessionID, rawURL)
	}
	if provider == nil {
		return Binding{}, fmt.Errorf("%w（session=%q url=%q）", ErrNoProvider, sessionID, rawURL)
	}
	// 网络调用在锁外：慢 code host 不能把 track/dismiss 一起卡住。
	state, err := provider.Fetch(ctx, rawURL)
	if err != nil {
		return Binding{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if j := indexOf(s.doc.Bindings, sessionID, rawURL); j >= 0 {
		s.doc.Bindings[j].State = state.State
		s.doc.Bindings[j].Title = state.Title
		s.doc.Bindings[j].HeadSHA = state.HeadSHA
		s.doc.Bindings[j].RefreshedAt = time.Now().UTC()
		if err := s.saveLocked(); err != nil {
			return Binding{}, err
		}
		return s.doc.Bindings[j], nil
	}
	return Binding{}, fmt.Errorf("%w：session=%q url=%q", ErrNotFound, sessionID, rawURL)
}

// HTTPProvider 是最小可用的 code host 拉取面：GET url，读 JSON（state/headSha/title）。
//
// 生产接真实 provider 时替换它即可；本仓当前的 GitHub 凭据接线在 WIRING 里。
type HTTPProvider struct {
	Client *http.Client
	// Token 非空时加 Authorization: Bearer（GitHub fine-grained token）。
	Token string
}

// Fetch 实现 Provider。
func (p *HTTPProvider) Fetch(ctx context.Context, rawURL string) (RemoteState, error) {
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return RemoteState{}, fmt.Errorf("forge: 造请求失败: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "dsh-go-forge/1")
	if p.Token != "" {
		req.Header.Set("Authorization", "Bearer "+p.Token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return RemoteState{}, fmt.Errorf("forge: 拉取 %s 失败: %w", rawURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return RemoteState{}, fmt.Errorf("forge: 拉取 %s 返回 HTTP %d", rawURL, resp.StatusCode)
	}
	var state RemoteState
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		return RemoteState{}, fmt.Errorf("forge: 解析 %s 的响应失败: %w", rawURL, err)
	}
	return state, nil
}
