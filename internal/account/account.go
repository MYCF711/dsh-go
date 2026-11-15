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
	"strconv"
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
	// UserCode 是 RFC 8628 device authorization 里**人要在浏览器里输入的那串码**。
	//
	// 为什么放进 attempt：官方 `AccountView.attempt` 是 `additionalProperties: true`
	// （`docs/evidence` 的 rc.2 契约里逐字如此），所以这是**加性字段**，不改官方
	// 形状；而 UI 与 authorization flow 都需要它在**不读内部状态**的前提下显示
	// "去哪个页面、输什么码"。以前它只活在 `Config.Doc.DeviceCode` 旁边没被保存，
	// 于是"人能看见这个码"这件事无从实现。
	UserCode  string `json:"userCode,omitempty"`
	ExpiresAt int64  `json:"expiresAt,omitempty"`
	ErrorCode string `json:"errorCode,omitempty"`
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

	// Diagnostic 承载"本地已完成、但远端没配合"的真实结果（目前只有一种：
	// 登出时服务端撤销重试耗尽）。空串 = 没有任何异常。
	//
	// 为什么必须让调用方看见：官方 `revokeAccount` 把远端失败留在后台（:471-485
	// 「remote failures stay in the background」），用户界面上"登出成功"= 本地凭据
	// 已清。但服务端会话仍可能活着——那是一条真实的安全事实，不能只写在日志里。
	Diagnostic string `json:"diagnostic,omitempty"`
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
	// WatchUpdates 是官方 0.2.2-rc.2 的 `account/watch`（AccountUpdate 流）。
	// Watch 保留为 rc.1 形状（扁平 AccountView），只由兼容路径使用。
	WatchUpdates(ctx context.Context) (<-chan Update, error)
	WatchExpiry(ctx context.Context) (<-chan string, error)

	// 官方 0.2.2-rc.2 新增的通知三件套（数据来自 harnessOrigin 的云端，
	// 没有本地缓存）。不支持的 provider 返回 ErrNoNotificationBackend。
	Notifications(ctx context.Context, client Client) (*NotificationBatch, error)
	NotificationContent(ctx context.Context, id string, client Client) (*Notification, error)
	ReportNotificationEvent(ctx context.Context, id, event string, client Client) (bool, error)
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
	// HarnessOrigin 是**通知专用** origin（官方 `harnessOrigin`，默认
	// https://harness.deepseek.com；见 dsh-deepseek-account/lib/index.js:696-697）。
	// 只用于 notifications 的 list/content/events；登录/资料/余额/登出仍走
	// PlatformOrigin（官方迁移指南 v0.2.2-rc.1/account-notification-fields 第 4 条）。
	// 空 = 先看 EnvHarnessOrigin，再看 DefaultHarnessOrigin。
	HarnessOrigin string
	OIDC          OIDCConfig
	HTTPClient    *http.Client
	LocalLease    time.Duration

	// InferenceOrigin 是官方 `dsh-deepseek-account` 的 `inferenceOrigin`
	// （`src/index.ts:78`：`Schema.string().default('https://api.deepseek.com')`，
	// `:142-147` 校验成"不含凭据/路径/查询/片段的 HTTP(S) origin"）。
	//
	// 为什么必须与 PlatformOrigin 分开：账号令牌**只**发给这个 origin 的推理请求
	// （`resolveToken` 的目的地栅栏，`:457`）。PlatformOrigin 是登录/资料/余额的
	// 计费站，两者在生产上是两个域名，互相推导就会把令牌发给错的站点。
	// 空 = 官方默认。
	InferenceOrigin string

	// LogoutMaxRetries / LogoutRetryDelay 是官方 `logoutMaxRetries`(:515) 与
	// `logoutRetryDelayMs`(:516)。nil / 0 = 走默认（先看环境变量，再看官方默认）。
	//
	// 用 *int 而不是 int：官方 `min(0)` 让"显式不重试"是一个**合法配置**，
	// 用 int 的零值会把"没设"与"设成 0"混成一个东西。
	LogoutMaxRetries *int
	LogoutRetryDelay time.Duration
	// AttemptTimeout 是官方 `attemptTimeoutMs`(:517)：设备登录**单次尝试**的预算
	// （官方默认 600s）。0 = 走默认。
	AttemptTimeout time.Duration
}

// Service 是 local/official/self-hosted 三合一的真实实现。
type Service struct {
	mu              sync.Mutex
	home            string
	mode            Source
	path            string
	doc             document
	client          *http.Client
	origin          string
	inferenceOrigin string
	harness         string
	oidc            OIDCConfig
	lease           time.Duration
	subs            map[chan State]struct{}
	updates         map[*updateSub]struct{}
	expiry          map[chan string]struct{}
	timer           *time.Timer
	timerAt         int64

	// 官方 account 平台的三条预算（见 officialPolicyDefaults）。
	logoutMaxRetries int
	logoutRetryDelay time.Duration
	attemptTimeout   time.Duration
}

// Update 是官方 0.2.2-rc.2 `AccountUpdate`（见
// dsh-deepseek-account/lib/typert.host.js:640-651 与 types/index.js 的
// `interface AccountUpdate { type: 'snapshot' | 'credentials-changed'; state: AccountView }`）。
//
// 为什么必须区分 type：官方客户端在 `credentials-changed` 时要清掉通知态并
// 重启轮询（官方迁移指南第 1 条），而登录进度（snapshot）要**保留**通知态。
// 把两者混成一种帧，客户端就只能保守地每次都清——那是真实可见的行为差异。
type Update struct {
	Type  string `json:"type"`
	State State  `json:"state"`
}

// 官方 AccountUpdate.type 的两个值（typert.host.js:648 的 enum）。
const (
	UpdateSnapshot           = "snapshot"
	UpdateCredentialsChanged = "credentials-changed"
)

// updateSub 是一个 AccountUpdate 订阅者。
//
// pending 只在 s.mu 下读写；wake 是容量 1 的唤醒信号。官方用同一套结构做
// 合并（observeUpdates：pending 只留一份，且 credentials-changed 优先），
// 这里照抄，避免"高频状态变化把凭据变更挤掉"这种真实丢帧。
type updateSub struct {
	wake    chan struct{}
	pending *Update
}

// 官方默认值（app.asar:dsh/node_modules/@deepseek-ai/dsh-deepseek-account-platform/lib/index.js:515-517）。
const (
	OfficialLogoutMaxRetries = 5
	OfficialLogoutRetryDelay = time.Second
	OfficialAttemptTimeout   = 10 * time.Minute
)

// 环境变量名（宿主统一 DSHD_ 前缀；与 options 同时存在时 options 优先）。
const (
	EnvLogoutMaxRetries   = "DSHD_ACCOUNT_LOGOUT_MAX_RETRIES"
	EnvLogoutRetryDelayMs = "DSHD_ACCOUNT_LOGOUT_RETRY_DELAY_MS"
	EnvAttemptTimeoutMs   = "DSHD_ACCOUNT_ATTEMPT_TIMEOUT_MS"
)

// logoutPolicy 返回真实生效的登出重试策略（测试与诊断读它，不读私有字段）。
func (s *Service) logoutPolicy() (int, time.Duration) { return s.logoutMaxRetries, s.logoutRetryDelay }

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
		subs: map[chan State]struct{}{}, updates: map[*updateSub]struct{}{},
		expiry: map[chan string]struct{}{},
	}
	s.logoutMaxRetries, s.logoutRetryDelay = resolveLogoutPolicy(opts)
	s.attemptTimeout = resolveAttemptTimeout(opts)
	if s.client == nil {
		s.client = &http.Client{Timeout: 20 * time.Second}
	}
	if s.origin == "" {
		s.origin = "https://platform.deepseek.com"
	}
	// inferenceOrigin 的校验逐条照官方 `src/index.ts:142-147`：必须是 http(s)
	// origin，且不带凭据、路径、查询、片段。**不在解析失败时静默退回默认值**——
	// 退回默认会把"配错的私有部署"变成"把令牌发给官方推理站"，那是最坏的失败方向。
	inference, err := resolveInferenceOrigin(opts.InferenceOrigin)
	if err != nil {
		return nil, err
	}
	s.inferenceOrigin = inference
	// harnessOrigin 只给通知用，默认值与 platformOrigin **不同**
	// （官方 lib/index.js:696-697）。两者刻意不互相推导：私有部署只配了
	// platformOrigin 时，通知仍然走官方默认域名——那是官方行为，
	// 要改就用 DSHD_ACCOUNT_HARNESS_ORIGIN 显式指定。
	s.harness = strings.TrimRight(strings.TrimSpace(opts.HarnessOrigin), "/")
	if s.harness == "" {
		s.harness = strings.TrimRight(strings.TrimSpace(os.Getenv(EnvHarnessOrigin)), "/")
	}
	if s.harness == "" {
		s.harness = DefaultHarnessOrigin
	}
	if s.lease <= 0 {
		s.lease = 30 * 24 * time.Hour
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// resolveLogoutPolicy 解析登出重试策略：显式 options > 环境变量 > 官方默认。
//
// 三条官方约束一起守住：maxRetries 落在 [0,5]（超出夹紧），delay 落在 [1ms,60s]。
// 为什么要夹紧而不是照抄：这两个值直接决定"登出最多卡多久"，一个写成 999999
// 的配置不该让 dshd 在退出路径上等半小时。
func resolveLogoutPolicy(opts Options) (int, time.Duration) {
	retries := OfficialLogoutMaxRetries
	if opts.LogoutMaxRetries != nil {
		retries = *opts.LogoutMaxRetries
	} else if raw := strings.TrimSpace(os.Getenv(EnvLogoutMaxRetries)); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			retries = parsed
		}
	}
	if retries < 0 {
		retries = 0
	}
	if retries > 5 {
		retries = 5
	}
	delay := opts.LogoutRetryDelay
	if delay <= 0 {
		delay = OfficialLogoutRetryDelay
		if raw := strings.TrimSpace(os.Getenv(EnvLogoutRetryDelayMs)); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
				delay = time.Duration(parsed) * time.Millisecond
			}
		}
	}
	if delay < time.Millisecond {
		delay = time.Millisecond
	}
	if delay > 60*time.Second {
		delay = 60 * time.Second
	}
	return retries, delay
}

// resolveAttemptTimeout 解析设备登录的单次尝试预算（官方 min 1ms / max 3600s）。
func resolveAttemptTimeout(opts Options) time.Duration {
	timeout := opts.AttemptTimeout
	if timeout <= 0 {
		timeout = OfficialAttemptTimeout
		if raw := strings.TrimSpace(os.Getenv(EnvAttemptTimeoutMs)); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
				timeout = time.Duration(parsed) * time.Millisecond
			}
		}
	}
	if timeout < time.Millisecond {
		timeout = time.Millisecond
	}
	if timeout > time.Hour {
		timeout = time.Hour
	}
	return timeout
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
	// Chmod 失败不算致命（Windows 上基本是空操作），所以只忽略它；
	// 但 Write/Sync 失败**必须报出来**——旧写法把 `err :=` 写进了 if 的作用域，
	// 内层那次赋值根本没碰到外层 err，写盘失败会被静默吞掉（SA4006 抓的就是它）。
	_ = tmp.Chmod(0o600)
	if _, err = tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if closeErr := tmp.Close(); closeErr != nil {
		return closeErr
	}
	return os.Rename(name, s.path)
}

// notifyLocked 推一次状态变化，类型按 snapshot 处理（登录进度/一般状态）。
func (s *Service) notifyLocked() {
	s.notifyKindLocked(UpdateSnapshot)
}

// notifyKindLocked 推一次状态变化，并标明官方 AccountUpdate.type。
//
// kind 只有两个合法值：UpdateSnapshot（初始快照与登录进度）与
// UpdateCredentialsChanged（凭据写入/移除）。凭据变更**优先**：若某个订阅者
// 还有未读帧，就把它的类型提到 credentials-changed，绝不因为后来的 snapshot
// 把"凭据变了"这件事挤掉（官方 observeUpdates 的同一口径，lib/index.js:1400-1404）。
func (s *Service) notifyKindLocked(kind string) {
	if kind != UpdateCredentialsChanged {
		kind = UpdateSnapshot
	}
	st := s.stateLocked()
	for ch := range s.subs {
		select {
		case ch <- st:
		default:
		}
	}
	for sub := range s.updates {
		next := kind
		if sub.pending != nil && sub.pending.Type == UpdateCredentialsChanged {
			next = UpdateCredentialsChanged
		}
		sub.pending = &Update{Type: next, State: st}
		select {
		case sub.wake <- struct{}{}:
		default:
		}
	}
}

func (s *Service) stateLocked() State {
	// Attempt 必须**拷一份**再交出去：它是指针，直接给出去等于把服务内部的
	// 活结构交给订阅者——定时器到点时 expireLocked 会在 s.mu 下改 Phase，
	// 而订阅者读的是同一个 struct，`-race` 一跑就是 DATA RACE（真实链上就是
	// "客户端读到半个 Attempt"）。Attempt 全是值字段，浅拷贝即完整快照。
	var attempt *Attempt
	if s.doc.Attempt != nil {
		snapshot := *s.doc.Attempt
		attempt = &snapshot
	}
	return State{
		Status: s.doc.Status, Links: s.linksLocked(), Attempt: attempt,
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
		// 租约到期 = 本地凭据被移除 → 官方 AccountUpdate 的 credentials-changed。
		s.notifyKindLocked(UpdateCredentialsChanged)
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
	// 定时器的触发点取"最早到点的那个"：登录态到期（doc.ExpiresAt）与
	// 登录尝试预算（attempt.ExpiresAt，官方 attemptTimeoutMs）都可能先生效。
	// 只看 doc.ExpiresAt 会让"尝试预算用尽"必须靠调用方轮询 State 才能推进。
	at := s.doc.ExpiresAt
	if s.doc.Attempt != nil && !finalPhase(s.doc.Attempt.Phase) &&
		s.doc.Attempt.ExpiresAt > 0 && (at <= 0 || s.doc.Attempt.ExpiresAt < at) {
		at = s.doc.Attempt.ExpiresAt
	}
	if at <= 0 {
		return
	}
	d := time.Until(time.UnixMilli(at))
	if d < 0 {
		d = 0
	}
	s.timerAt = at
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
	// 这一步真的写入了凭据 → credentials-changed（官方 AccountUpdate 语义）。
	s.notifyKindLocked(UpdateCredentialsChanged)
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
	// 登出 = 凭据被移除 → credentials-changed。
	s.notifyKindLocked(UpdateCredentialsChanged)
	st := s.stateLocked()
	s.mu.Unlock()
	if s.mode == SourceOfficial && token != "" {
		// 本地凭据已经清了（上面整体重置过 document）；这里补的是**服务端**撤销。
		// 失败不改变本地登出结果，但必须让调用方看见（见 State.Diagnostic）。
		if revokeErr := s.revokeOfficialGrant(ctx, token); revokeErr != nil {
			st.Diagnostic = revokeErr.Error()
		}
	}
	return st, nil
}

// revokeOfficialGrant 按官方 `revokeAccount` 的形状撤销服务端会话
// （dsh-deepseek-account-platform/lib/index.js:471-485）：
//
//   - 总共最多 1+maxRetries 次尝试（首次 + maxRetries 次重试）；
//   - 第 n 次重试前等 `delayMs * 2^(n-1)`（指数退避）；
//   - 每次等待都可以被 ctx 取消（官方用 provider 生命周期 signal 做同一件事）；
//   - 成功即返回 nil；耗尽后返回带"试了几次"的错误，由调用方决定怎么呈现。
//
// 与官方的**唯一差异**：官方把整次撤销扔在后台、失败只留在日志里；宿主改成
// 同步 + 把结论写进 State.Diagnostic（有界：默认 6 次尝试 / 累计约 31s）。
// 这样"登出看上去成功、服务端会话其实还在"不会被静默吞掉。
func (s *Service) revokeOfficialGrant(ctx context.Context, token string) error {
	attempts := s.logoutMaxRetries + 1
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			wait := s.logoutRetryDelay * time.Duration(int64(1)<<uint(attempt-1))
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				if lastErr != nil {
					return fmt.Errorf("服务端撤销被取消（已尝试 %d 次）：%w", attempt, lastErr)
				}
				return ctx.Err()
			case <-timer.C:
			}
		}
		if lastErr = s.officialLogout(ctx, token); lastErr == nil {
			return nil
		}
	}
	return fmt.Errorf("登出本地已完成，但服务端撤销 %d 次尝试后仍失败：%w", attempts, lastErr)
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

// WatchUpdates 订阅官方 0.2.2-rc.2 的 AccountUpdate 流。
//
// 与 `Watch` 的区别不是命名：每帧都带 type（snapshot / credentials-changed），
// 客户端据此决定"要不要清通知态并重启轮询"（官方迁移指南第 1 条）。
//
// 合并口径照官方 observeUpdates（lib/index.js:1395-1418）：订阅者只保留**一帧**
// 待读状态，且 credentials-changed 优先——凭据变更绝不被后来的 snapshot 挤掉。
func (s *Service) WatchUpdates(ctx context.Context) (<-chan Update, error) {
	ch := make(chan Update, 8)
	sub := &updateSub{wake: make(chan struct{}, 1)}
	s.mu.Lock()
	sub.pending = &Update{Type: UpdateSnapshot, State: s.stateLocked()}
	s.updates[sub] = struct{}{}
	s.mu.Unlock()
	select {
	case sub.wake <- struct{}{}:
	default:
	}
	go func() {
		defer close(ch)
		for {
			select {
			case <-ctx.Done():
				s.mu.Lock()
				delete(s.updates, sub)
				s.mu.Unlock()
				return
			case <-sub.wake:
			}
			s.mu.Lock()
			pending := sub.pending
			sub.pending = nil
			s.mu.Unlock()
			if pending == nil {
				continue
			}
			select {
			case ch <- *pending:
			case <-ctx.Done():
				s.mu.Lock()
				delete(s.updates, sub)
				s.mu.Unlock()
				return
			}
		}
	}()
	return ch, nil
}

func (s *Service) WatchExpiry(ctx context.Context) (<-chan string, error) {
	ch := make(chan string, 4)
	s.mu.Lock()
	s.expiry[ch] = struct{}{}
	expired := s.doc.ExpiresAt > 0 && time.Now().UnixMilli() >= s.doc.ExpiresAt
	s.mu.Unlock()
	// **不发基线帧**：官方 `account/watchExpiry` 的结果 schema 是
	// `{"type":"string","const":"session-expired"}`（typert.host.js:658-668），
	// 只有一个合法值。旧实现先发一帧"当前到期时刻/空串"保证客户端能拿到
	// 响应头，但那是**违反官方 const** 的非法 wire（rc.1 的同一个 const 也在）。
	// 现在由 transport 在开流时就写响应头并 flush（见 serveAccountWatchExpiry），
	// 既保证 open 不挂，又不发非法帧。
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
