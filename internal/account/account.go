// Package account 是官方 0.2.2-rc.1 account 组的宿主侧状态机与多提供方实现。
//
// 8 个此前缺失的方法（startSignIn/cancelSignIn/getState/getProfile/getBalance/
// hasRunningAccountTasks/watch/watchExpiry）都走同一个 Provider，而不是每个
// 方法各写一份假状态：
//
//   - local（默认）：本地账号，profile/wallet 落在 <home>/state/account-local.json；
//     结果带 source:"local"，绝不冒充官方账号或官方余额；
//   - official：DeepSeek Platform，复用 <home>/state/account-grant.json；
//     profile/balance/bonuses/ack/signOut 真走 Platform HTTP，startSignIn 走
//     auth_init/auth_exchange（PKCE），没有 Platform 时返回结构化错误，不 501；
//   - self-hosted：自托管 OIDC（device code + token + userinfo），落
//     <home>/state/account-oidc.json，source:"self-hosted"，不伪造钱包。
//
// 持久化是原子的（同目录临时文件 + rename + 0600）；attempt/grant 过期会在
// getState/watch/watchExpiry 里真实推进，重启后从同一份文档恢复。
package account

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Source 是结果来源标签。UI 必须据此区分本地/官方/自托管，绝不能把 local
// 画成官方账号。
type Source string

const (
	SourceLocal      Source = "local"
	SourceOfficial   Source = "official"
	SourceSelfHosted Source = "self-hosted"
)

// Status 是官方 AccountView.status 的两个值。
const (
	StatusSignedOut        = "signed-out"
	StatusCredentialStored = "credential-stored"
)

// Attempt phase，逐字对齐官方枚举。
const (
	PhaseInitializing   = "initializing"
	PhaseWaitingBrowser = "waiting-browser"
	PhaseExchanging     = "exchanging"
	PhaseCommitting     = "committing"
	PhaseSucceeded      = "succeeded"
	PhaseCancelled      = "cancelled"
	PhaseExpired        = "expired"
	PhaseFailed         = "failed"
)

var (
	ErrNoAttempt = errors.New("account: 没有这个登录尝试")
	ErrSignedOut = errors.New("account: 未登录")
	ErrExpired   = errors.New("account: 登录已过期")
)

// Client 是官方 AccountClientMetadata。
type Client struct {
	Version               string
	Locale                string
	TimezoneOffsetSeconds float64
}

type Links struct {
	UsageURL string `json:"usageUrl"`
	TopUpURL string `json:"topUpUrl"`
}

type Attempt struct {
	ID           string `json:"id"`
	Phase        string `json:"phase"`
	AuthorizeURL string `json:"authorizeUrl,omitempty"`
	ExpiresAt    int64  `json:"expiresAt,omitempty"`
	ErrorCode    string `json:"errorCode,omitempty"`
}

type Profile struct {
	ID        *string `json:"id"`
	Name      *string `json:"name"`
	Contact   *string `json:"contact"`
	AvatarURL *string `json:"avatarUrl,omitempty"`
	Source    Source  `json:"source,omitempty"`
}

type Wallet struct {
	Currency string `json:"currency"`
	Balance  string `json:"balance"`
}

type Balance struct {
	Status       string   `json:"status"`
	Value        []Wallet `json:"value,omitempty"`
	BonusWallets []Wallet `json:"bonusWallets,omitempty"`
	Source       Source   `json:"source,omitempty"`
}

type Bonus struct {
	Title     string          `json:"title,omitempty"`
	OrderID   string          `json:"orderId"`
	Campaign  json.RawMessage `json:"campaign,omitempty"`
	Amount    json.RawMessage `json:"amount,omitempty"`
	Currency  json.RawMessage `json:"currency,omitempty"`
	GrantedAt json.RawMessage `json:"grantedAt,omitempty"`
	ExpiresAt string          `json:"expiresAt"`
	Message   string          `json:"message"`
}

type BonusList struct {
	AccountID string  `json:"accountId"`
	Bonuses   []Bonus `json:"bonuses"`
	Source    Source  `json:"source,omitempty"`
}

type State struct {
	Status    string   `json:"status"`
	Links     Links    `json:"links"`
	Attempt   *Attempt `json:"attempt"`
	ExpiresAt int64    `json:"expiresAt,omitempty"`
	Source    Source   `json:"source,omitempty"`
}

type Task struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

// StartOptions 是 startSignIn 的真实输入。
type StartOptions struct {
	Client         Client
	CallbackOrigin string
	LoginSource    string
}

// Provider 是 account 组唯一的状态机接口。
type Provider interface {
	Source() Source
	State(ctx context.Context) (State, error)
	Profile(ctx context.Context, client Client) (*Profile, error)
	Balance(ctx context.Context, client Client) (*Balance, error)
	Bonuses(ctx context.Context, client Client) (*BonusList, error)
	AckBonus(ctx context.Context, accountID, orderID string, client Client) (bool, error)
	StartSignIn(ctx context.Context, opts StartOptions) (State, error)
	CompleteSignIn(ctx context.Context, attemptID, code string) (State, error)
	CancelSignIn(ctx context.Context, attemptID string) (State, error)
	SignOut(ctx context.Context) (State, error)
	RunningTasks(ctx context.Context) ([]Task, error)
	Watch(ctx context.Context) (<-chan State, error)
	WatchExpiry(ctx context.Context) (<-chan string, error)
}

// OIDCConfig 是 self-hosted OIDC 的真实端点/客户端配置。
type OIDCConfig struct {
	Issuer        string
	ClientID      string
	ClientSecret  string
	Scopes        string
	DeviceAuthURL string
	TokenURL      string
	UserInfoURL   string
}

// Options 是构造 Provider 的显式依赖。
type Options struct {
	Home           string
	Mode           Source
	PlatformOrigin string
	OIDC           OIDCConfig
	HTTPClient     *http.Client
	LocalLease     time.Duration
}

// Service 是 local/official/self-hosted 三合一的真实实现。
type Service struct {
	mu      sync.Mutex
	home    string
	mode    Source
	path    string
	doc     document
	client  *http.Client
	origin  string
	oidc    OIDCConfig
	lease   time.Duration
	subs    map[chan State]struct{}
	expiry  map[chan string]struct{}
	timer   *time.Timer
	timerAt int64
}

// document 是三提供方共用的持久形状；不同 mode 只使用相关字段。
type document struct {
	Version      int      `json:"version"`
	Source       Source   `json:"source"`
	Status       string   `json:"status"`
	AccountID    string   `json:"accountId,omitempty"`
	Profile      *Profile `json:"profile,omitempty"`
	Wallets      []Wallet `json:"wallets,omitempty"`
	BonusWallets []Wallet `json:"bonusWallets,omitempty"`
	Bonuses      []Bonus  `json:"bonuses,omitempty"`
	Acked        []string `json:"ackedOrderIds,omitempty"`
	Attempt      *Attempt `json:"attempt,omitempty"`
	ExpiresAt    int64    `json:"expiresAt,omitempty"`
	Token        string   `json:"token,omitempty"`
	Issuer       string   `json:"issuer,omitempty"`
	DeviceCode   string   `json:"deviceCode,omitempty"`
	CodeVerifier string   `json:"codeVerifier,omitempty"`
	State        string   `json:"state,omitempty"`
	RedirectURI  string   `json:"redirectUri,omitempty"`
}

const docVersion = 2

func New(opts Options) (*Service, error) {
	home := strings.TrimSpace(opts.Home)
	if home == "" {
		return nil, fmt.Errorf("account: 需要显式 home")
	}
	mode := opts.Mode
	if mode == "" {
		mode = SourceLocal
	}
	switch mode {
	case SourceLocal, SourceOfficial, SourceSelfHosted:
	default:
		return nil, fmt.Errorf("account: 未知 provider %q（可选 local/official/self-hosted）", mode)
	}
	var path string
	switch mode {
	case SourceLocal:
		path = filepath.Join(home, "state", "account-local.json")
	case SourceOfficial:
		path = filepath.Join(home, "state", "account-grant.json")
	case SourceSelfHosted:
		path = filepath.Join(home, "state", "account-oidc.json")
	}
	s := &Service{
		home: home, mode: mode, path: path,
		client: opts.HTTPClient, origin: strings.TrimRight(opts.PlatformOrigin, "/"),
		oidc: opts.OIDC, lease: opts.LocalLease,
		subs: map[chan State]struct{}{}, expiry: map[chan string]struct{}{},
	}
	if s.client == nil {
		s.client = &http.Client{Timeout: 20 * time.Second}
	}
	if s.origin == "" {
		s.origin = "https://platform.deepseek.com"
	}
	if s.lease <= 0 {
		s.lease = 30 * 24 * time.Hour
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Service) Source() Source { return s.mode }

func (s *Service) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.doc = document{Version: docVersion, Source: s.mode, Status: StatusSignedOut}
		return nil
	}
	if err != nil {
		return fmt.Errorf("account: 读 %s: %w", s.path, err)
	}
	var doc document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("account: %s 不是合法 JSON: %w", s.path, err)
	}
	if doc.Source != "" && doc.Source != s.mode {
		return fmt.Errorf("account: %s 的 source=%q 与当前 provider=%q 不一致", s.path, doc.Source, s.mode)
	}
	if doc.Status == "" {
		doc.Status = StatusSignedOut
	}
	doc.Source = s.mode
	s.doc = doc
	s.expireLocked(time.Now())
	s.resetTimerLocked()
	return nil
}

func (s *Service) saveLocked() error {
	if s.doc.Source == "" {
		s.doc.Source = s.mode
	}
	raw, err := json.MarshalIndent(s.doc, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".account-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(raw)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, s.path)
}

func (s *Service) notifyLocked() {
	st := s.stateLocked()
	for ch := range s.subs {
		select {
		case ch <- st:
		default:
		}
	}
}

func (s *Service) stateLocked() State {
	return State{
		Status: s.doc.Status, Links: s.linksLocked(), Attempt: s.doc.Attempt,
		ExpiresAt: s.doc.ExpiresAt, Source: s.mode,
	}
}

func (s *Service) linksLocked() Links {
	if s.mode == SourceOfficial {
		return Links{UsageURL: s.origin + "/usage", TopUpURL: s.origin + "/top_up"}
	}
	return Links{}
}

func (s *Service) State(ctx context.Context) (State, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.expireLocked(time.Now()); err != nil {
		return State{}, err
	}
	return s.stateLocked(), nil
}

func (s *Service) expireLocked(now time.Time) error {
	if s.doc.ExpiresAt > 0 && now.UnixMilli() >= s.doc.ExpiresAt {
		s.doc.Status = StatusSignedOut
		s.doc.Token = ""
		s.doc.ExpiresAt = 0
		if s.doc.Attempt != nil && !finalPhase(s.doc.Attempt.Phase) {
			s.doc.Attempt.Phase = PhaseExpired
		}
		s.resetTimerLocked()
		if err := s.saveLocked(); err != nil {
			return err
		}
		s.notifyLocked()
		for ch := range s.expiry {
			select {
			case ch <- "session-expired":
			default:
			}
		}
	}
	if s.doc.Attempt != nil && !finalPhase(s.doc.Attempt.Phase) && s.doc.Attempt.ExpiresAt > 0 &&
		now.UnixMilli() >= s.doc.Attempt.ExpiresAt {
		s.doc.Attempt.Phase = PhaseExpired
		if err := s.saveLocked(); err != nil {
			return err
		}
		s.notifyLocked()
	}
	return nil
}

func (s *Service) resetTimerLocked() {
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	if s.doc.ExpiresAt <= 0 {
		return
	}
	d := time.Until(time.UnixMilli(s.doc.ExpiresAt))
	if d < 0 {
		d = 0
	}
	s.timerAt = s.doc.ExpiresAt
	s.timer = time.AfterFunc(d, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		_ = s.expireLocked(time.Now())
	})
}

func (s *Service) Profile(ctx context.Context, client Client) (*Profile, error) {
	s.mu.Lock()
	doc := s.doc
	s.mu.Unlock()
	if doc.Status != StatusCredentialStored {
		return nil, ErrSignedOut
	}
	switch s.mode {
	case SourceLocal:
		if doc.Profile == nil {
			return nil, ErrSignedOut
		}
		p := *doc.Profile
		p.Source = SourceLocal
		return &p, nil
	case SourceOfficial:
		return s.officialProfile(ctx, doc.Token)
	default:
		if doc.Profile == nil {
			return nil, ErrSignedOut
		}
		p := *doc.Profile
		p.Source = SourceSelfHosted
		return &p, nil
	}
}

func (s *Service) Balance(ctx context.Context, client Client) (*Balance, error) {
	s.mu.Lock()
	doc := s.doc
	s.mu.Unlock()
	if doc.Status != StatusCredentialStored {
		return nil, nil
	}
	switch s.mode {
	case SourceLocal:
		return &Balance{Status: "ready", Value: cloneWallets(doc.Wallets), BonusWallets: cloneWallets(doc.BonusWallets), Source: SourceLocal}, nil
	case SourceOfficial:
		return s.officialBalance(ctx, doc.Token)
	default:
		// OIDC 没有绑定计费系统；不伪造 0 余额，也不冒充 ready。
		return &Balance{Status: "failed", Source: SourceSelfHosted}, nil
	}
}

func (s *Service) Bonuses(ctx context.Context, client Client) (*BonusList, error) {
	s.mu.Lock()
	doc := s.doc
	s.mu.Unlock()
	if doc.Status != StatusCredentialStored {
		return nil, nil
	}
	switch s.mode {
	case SourceLocal:
		return &BonusList{AccountID: doc.AccountID, Bonuses: []Bonus{}, Source: SourceLocal}, nil
	case SourceOfficial:
		return s.officialBonuses(ctx, doc.Token)
	default:
		return &BonusList{AccountID: doc.AccountID, Bonuses: []Bonus{}, Source: SourceSelfHosted}, nil
	}
}

func (s *Service) AckBonus(ctx context.Context, accountID, orderID string, client Client) (bool, error) {
	s.mu.Lock()
	doc := s.doc
	s.mu.Unlock()
	if doc.Status != StatusCredentialStored || accountID == "" || accountID != doc.AccountID {
		return false, nil
	}
	switch s.mode {
	case SourceOfficial:
		return s.officialAck(ctx, doc.Token, orderID)
	default:
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.doc.AccountID != accountID {
			return false, nil
		}
		for _, id := range s.doc.Acked {
			if id == orderID {
				return true, nil
			}
		}
		s.doc.Acked = append(s.doc.Acked, orderID)
		if err := s.saveLocked(); err != nil {
			return false, err
		}
		s.notifyLocked()
		return true, nil
	}
}

func (s *Service) StartSignIn(ctx context.Context, opts StartOptions) (State, error) {
	s.mu.Lock()
	if s.doc.Attempt != nil && !finalPhase(s.doc.Attempt.Phase) {
		st := s.stateLocked()
		s.mu.Unlock()
		return st, nil
	}
	if s.doc.Status == StatusCredentialStored {
		st := s.stateLocked()
		s.mu.Unlock()
		return st, nil
	}
	s.mu.Unlock()
	switch s.mode {
	case SourceLocal:
		return s.startLocal()
	case SourceOfficial:
		return s.startOfficial(ctx, opts)
	default:
		return s.startOIDC(ctx, opts)
	}
}

func (s *Service) startLocal() (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, err := randomID()
	if err != nil {
		return State{}, err
	}
	accountID := "local-" + id[:12]
	name := "Local account"
	profile := &Profile{ID: strptr(accountID), Name: &name, Contact: strptr("local"), Source: SourceLocal}
	s.doc.AccountID = accountID
	s.doc.Profile = profile
	s.doc.Status = StatusCredentialStored
	s.doc.ExpiresAt = time.Now().Add(s.lease).UnixMilli()
	s.doc.Attempt = &Attempt{ID: id, Phase: PhaseSucceeded, ExpiresAt: s.doc.ExpiresAt}
	s.doc.Wallets = []Wallet{{Currency: "CNY", Balance: "0.00"}}
	if err := s.saveLocked(); err != nil {
		return State{}, err
	}
	s.resetTimerLocked()
	s.notifyLocked()
	return s.stateLocked(), nil
}

func (s *Service) CompleteSignIn(ctx context.Context, attemptID, code string) (State, error) {
	s.mu.Lock()
	attempt := s.doc.Attempt
	s.mu.Unlock()
	if attempt == nil || attempt.ID != attemptID {
		return State{}, ErrNoAttempt
	}
	if finalPhase(attempt.Phase) {
		return State{}, fmt.Errorf("%w: phase=%s", ErrNoAttempt, attempt.Phase)
	}
	switch s.mode {
	case SourceLocal:
		return s.State(ctx)
	case SourceOfficial:
		return s.completeOfficial(ctx, code)
	default:
		return s.completeOIDC(ctx)
	}
}

func (s *Service) CancelSignIn(ctx context.Context, attemptID string) (State, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.doc.Attempt == nil || s.doc.Attempt.ID != attemptID || finalPhase(s.doc.Attempt.Phase) {
		return State{}, ErrNoAttempt
	}
	s.doc.Attempt.Phase = PhaseCancelled
	s.doc.Status = StatusSignedOut
	if err := s.saveLocked(); err != nil {
		return State{}, err
	}
	s.notifyLocked()
	return s.stateLocked(), nil
}

func (s *Service) SignOut(ctx context.Context) (State, error) {
	s.mu.Lock()
	token := s.doc.Token
	s.doc = document{Version: docVersion, Source: s.mode, Status: StatusSignedOut}
	if err := s.saveLocked(); err != nil {
		s.mu.Unlock()
		return State{}, err
	}
	s.resetTimerLocked()
	s.notifyLocked()
	st := s.stateLocked()
	s.mu.Unlock()
	if s.mode == SourceOfficial && token != "" {
		_ = s.officialLogout(ctx, token)
	}
	return st, nil
}

func (s *Service) RunningTasks(ctx context.Context) ([]Task, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.doc.Attempt != nil && !finalPhase(s.doc.Attempt.Phase) {
		return []Task{{ID: s.doc.Attempt.ID, Kind: "sign-in"}}, nil
	}
	return []Task{}, nil
}

func (s *Service) Watch(ctx context.Context) (<-chan State, error) {
	ch := make(chan State, 8)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	select {
	case ch <- s.stateLocked():
	default:
	}
	s.mu.Unlock()
	go func() {
		<-ctx.Done()
		s.mu.Lock()
		delete(s.subs, ch)
		close(ch)
		s.mu.Unlock()
	}()
	return ch, nil
}

func (s *Service) WatchExpiry(ctx context.Context) (<-chan string, error) {
	ch := make(chan string, 4)
	s.mu.Lock()
	s.expiry[ch] = struct{}{}
	expired := s.doc.ExpiresAt > 0 && time.Now().UnixMilli() >= s.doc.ExpiresAt
	baseline := ""
	if s.doc.ExpiresAt > 0 {
		baseline = time.UnixMilli(s.doc.ExpiresAt).UTC().Format(time.RFC3339)
	}
	s.mu.Unlock()
	// 订阅即给**基线**（当前到期时刻；未登录为空串）：否则 HTTP/mux 客户端
	// 在首个到期事件前连响应头都拿不到（open 挂到超时）。基线是真实状态。
	select {
	case ch <- baseline:
	default:
	}
	if expired {
		select {
		case ch <- "session-expired":
		default:
		}
	}
	go func() {
		<-ctx.Done()
		s.mu.Lock()
		delete(s.expiry, ch)
		close(ch)
		s.mu.Unlock()
	}()
	return ch, nil
}

func finalPhase(phase string) bool {
	switch phase {
	case PhaseSucceeded, PhaseCancelled, PhaseExpired, PhaseFailed:
		return true
	}
	return false
}

func strptr(v string) *string { return &v }

func cloneWallets(in []Wallet) []Wallet {
	out := make([]Wallet, len(in))
	copy(out, in)
	return out
}

func randomID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", raw), nil
}

func pkce() (verifier, challenge, state string, err error) {
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", "", "", err
	}
	stateRaw := make([]byte, 16)
	if _, err = rand.Read(stateRaw); err != nil {
		return "", "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	state = base64.RawURLEncoding.EncodeToString(stateRaw)
	return verifier, challenge, state, nil
}

func readLimited(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

var _ = url.Values{}
