package liveshare

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrShareNotFound 表示本地 owner 记录里没有这个 share。
var ErrShareNotFound = errors.New("share 不存在")

// BadRequestError 是"参数形状合法但取值不合法"的错误（宿主回 400）。
type BadRequestError struct{ Message string }

func (e *BadRequestError) Error() string { return e.Message }

// BadRequestf 造一个参数错误。
func BadRequestf(format string, args ...any) error {
	return &BadRequestError{Message: fmt.Sprintf(format, args...)}
}

// IsBadRequest 判断错误是不是参数错误。
func IsBadRequest(err error) bool {
	var target *BadRequestError
	return errors.As(err, &target)
}

// Config 是 owner 服务的构造参数。
type Config struct {
	// Home 是 DSH home；owner-shares.json 落在 <home>/liveshare/。
	Home string
	// RelayURL 是部署配置的 relay 地址（可空；空时只能用 setRelay 保存的地址）。
	RelayURL string
	// OwnerToken 是 owner 凭据（relay 的 owner-token.json 里的 token）。
	OwnerToken string
	// HTTP 可注入测试用客户端。
	HTTP *http.Client
	// Offers 是插件能力注册表；为空时新建空表。
	Offers *Registry
	// Now 让测试钉住时钟。
	Now func() time.Time
}

// Service 是 liveshare 12 个官方方法的宿主实现。
//
// 所有会改 owner-shares.json 的方法都用 s.mu 串行化（同进程内不丢更新）；
// 多个进程共用同一个 home 不在本实现的支持范围内（owner 状态按单宿主设计）。
type Service struct {
	mu              sync.Mutex
	home            string
	configuredRelay string
	token           string
	httpClient      *http.Client
	offers          *Registry
	now             func() time.Time
}

// NewService 构造服务并校验 owner 状态文件可读。
func NewService(cfg Config) (*Service, error) {
	if strings.TrimSpace(cfg.Home) == "" {
		return nil, errors.New("liveshare Home 不能为空")
	}
	configured, err := ValidateRelayURL(cfg.RelayURL)
	if err != nil {
		return nil, err
	}
	if _, err := loadState(cfg.Home); err != nil {
		return nil, err
	}
	offers := cfg.Offers
	if offers == nil {
		offers = NewRegistry()
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		home:            cfg.Home,
		configuredRelay: configured,
		token:           strings.TrimSpace(cfg.OwnerToken),
		httpClient:      cfg.HTTP,
		offers:          offers,
		now:             now,
	}, nil
}

// Offers 返回插件能力注册表（宿主接线时注册实时声明）。
func (s *Service) Offers() *Registry { return s.offers }

// EffectiveRelay 返回当前生效的 relay 地址（override 优先，其次部署配置）。
func (s *Service) EffectiveRelay() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.load()
	if err != nil {
		return s.configuredRelay
	}
	return s.effectiveRelayLocked(state)
}

func (s *Service) effectiveRelayLocked(state State) string {
	if state.RelayOverride != "" {
		return state.RelayOverride
	}
	return s.configuredRelay
}

func (s *Service) client(relay string) *Client {
	return NewClient(relay, s.token, s.httpClient)
}

func (s *Service) requestContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 15*time.Second)
}

// Relay 返回 relay 地址状态（configured/override/effective）。
func (s *Service) Relay() (RelayState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.load()
	if err != nil {
		return RelayState{}, err
	}
	return RelayState{
		Configured: s.configuredRelay,
		Override:   state.RelayOverride,
		Effective:  s.effectiveRelayLocked(state),
	}, nil
}

// SetRelay 保存（或清空）owner 选的 relay 地址。
func (s *Service) SetRelay(raw *string) (RelayState, error) {
	value := ""
	if raw != nil {
		normalized, err := ValidateRelayURL(*raw)
		if err != nil {
			return RelayState{}, BadRequestf("%v", err)
		}
		value = normalized
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.load()
	if err != nil {
		return RelayState{}, err
	}
	state.RelayOverride = value
	if err := s.save(state); err != nil {
		return RelayState{}, err
	}
	return RelayState{
		Configured: s.configuredRelay,
		Override:   value,
		Effective:  s.effectiveRelayLocked(state),
	}, nil
}

// Create 在 relay 上真实注册一条分享，并把 owner 记录写进 owner-shares.json。
//
// shareId/link 全部来自 relay 回包；relay 不可达/拒绝时直接报错，**绝不本地编**。
func (s *Service) Create(req CreateRequest) (CreatedShare, error) {
	req.SessionID = strings.TrimSpace(req.SessionID)
	if req.SessionID == "" {
		return CreatedShare{}, BadRequestf("sessionId 不能为空")
	}
	req.SessionTitle = strings.TrimSpace(req.SessionTitle)
	if req.SessionTitle == "" {
		req.SessionTitle = req.SessionID
	}
	req.AccessMode = strings.TrimSpace(req.AccessMode)
	if req.AccessMode == "" {
		req.AccessMode = "open"
	}
	if req.AccessMode != "open" && req.AccessMode != "selected" {
		return CreatedShare{}, BadRequestf("accessMode 只能是 open 或 selected")
	}
	req.SessionMode = strings.TrimSpace(req.SessionMode)
	if req.SessionMode == "" {
		req.SessionMode = "view"
	}
	if req.SessionMode != "view" && req.SessionMode != "write" {
		return CreatedShare{}, BadRequestf("sessionMode 只能是 view 或 write")
	}
	allowed, err := normalizeIDs(req.AllowedSubjectIDs)
	if err != nil {
		return CreatedShare{}, BadRequestf("%v", err)
	}
	if req.AccessMode == "selected" && len(allowed) == 0 {
		return CreatedShare{}, BadRequestf("selected 分享必须给 allowedSubjectIds")
	}
	if req.TTLMinutes != nil {
		ttl := *req.TTLMinutes
		if math.IsNaN(ttl) || math.IsInf(ttl, 0) || ttl <= 0 || ttl > 525600 {
			return CreatedShare{}, BadRequestf("ttlMinutes 必须在 (0, 525600] 之间")
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.load()
	if err != nil {
		return CreatedShare{}, err
	}
	relay := s.effectiveRelayLocked(state)
	if relay == "" {
		return CreatedShare{}, ErrRelayNotConfigured
	}
	client := s.client(relay)
	ctx, cancel := s.requestContext()
	defer cancel()
	cfg, err := client.OwnerConfig(ctx)
	if err != nil {
		return CreatedShare{}, err
	}
	live, err := client.CreateShare(ctx, relayCreateRequest{
		SessionID: req.SessionID, SessionTitle: req.SessionTitle,
		AccessMode: req.AccessMode, TTLMinutes: req.TTLMinutes,
		AllowedSubjectIDs: allowed, AllowsFork: req.AllowsFork,
		SessionMode: req.SessionMode,
	})
	if err != nil {
		return CreatedShare{}, err
	}
	if live.ShareID == "" || live.Link == "" {
		return CreatedShare{}, errors.New("relay 回包里没有 shareId/link（relay 协议被破坏）")
	}
	access := composeAccess(req, allowed, cfg)
	created := CreatedShare{
		Link: live.Link, ShareID: live.ShareID, SessionID: live.SessionID,
		SessionMode: live.SessionMode, Access: access,
	}
	state.Shares = append(state.Shares, StoredShare{
		ShareID: live.ShareID, SessionID: live.SessionID, SessionTitle: live.SessionTitle,
		Link: live.Link, CreatedAt: live.CreatedAt, ExpiresAt: live.ExpiresAt,
		AllowsFork: live.AllowsFork, SessionMode: live.SessionMode,
		ViewerCapabilities: append([]Capability{}, live.ViewerCapabilities...),
		Access:             access,
		Relay:              relay,
	})
	if err := s.save(state); err != nil {
		// 本地记录写不进去时，把 relay 上刚注册的 share 尽力删掉：否则会留下
		// 一条 owner 不知道、也无法通过 list 看到的分享（真实泄露面）。
		cleanupCtx, cleanupCancel := s.requestContext()
		_ = client.DeleteShare(cleanupCtx, live.ShareID)
		cleanupCancel()
		return CreatedShare{}, err
	}
	return created, nil
}

// List 刷新 relay 权威并返回本 home 的全部分享。
//
// relay 不可达/凭据无效时**不伪装成功**：本地记录照样返回，但 online=false
// 表示"没能向 relay 复核"。不在同一 relay 上的旧记录也保留（不跨 relay 混用）。
func (s *Service) List() (ShareList, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.load()
	if err != nil {
		return ShareList{}, err
	}
	effective := s.effectiveRelayLocked(state)
	online := map[string]bool{}
	if effective != "" && s.token != "" {
		ctx, cancel := s.requestContext()
		defer cancel()
		live, err := s.client(effective).ListShares(ctx)
		switch {
		case err == nil:
			byID := make(map[string]relayShare, len(live))
			for _, share := range live {
				byID[share.ShareID] = share
			}
			kept := state.Shares[:0]
			changed := false
			for _, stored := range state.Shares {
				if stored.Relay != effective {
					kept = append(kept, stored)
					continue
				}
				current, ok := byID[stored.ShareID]
				if !ok {
					changed = true // relay 上已经没有了（stop/过期）→ 本地记录删除
					continue
				}
				if current.SessionTitle != "" {
					stored.SessionTitle = current.SessionTitle
				}
				stored.SessionMode = current.SessionMode
				stored.AllowsFork = current.AllowsFork
				stored.ExpiresAt = current.ExpiresAt
				stored.ViewerCapabilities = append([]Capability{}, current.ViewerCapabilities...)
				if current.Link != "" {
					stored.Link = current.Link
				}
				online[stored.ShareID] = current.Online
				kept = append(kept, stored)
			}
			state.Shares = kept
			changed = s.retryPendingStopsLocked(&state) || changed
			if changed {
				if err := s.save(state); err != nil {
					return ShareList{}, err
				}
			}
		case IsUnreachable(err), IsUnauthorized(err), IsForbidden(err):
			// 保留本地记录，online=false。
		default:
			return ShareList{}, err
		}
	}
	views := make([]ShareView, 0, len(state.Shares))
	for _, stored := range state.Shares {
		view := stored.view()
		view.Online = online[stored.ShareID]
		views = append(views, view)
	}
	sort.Slice(views, func(i, j int) bool { return views[i].CreatedAt.After(views[j].CreatedAt) })
	return ShareList{Shares: views}, nil
}

// retryPendingStopsLocked 重试上次 stop 时没删掉的 relay 行；返回是否改动 state。
func (s *Service) retryPendingStopsLocked(state *State) bool {
	if len(state.PendingStops) == 0 || s.token == "" {
		return false
	}
	relay := s.effectiveRelayLocked(*state)
	if relay == "" {
		return false
	}
	client := s.client(relay)
	remaining := make([]string, 0, len(state.PendingStops))
	for _, shareID := range state.PendingStops {
		ctx, cancel := s.requestContext()
		err := client.DeleteShare(ctx, shareID)
		cancel()
		if err == nil || IsNotFound(err) {
			continue
		}
		remaining = append(remaining, shareID)
	}
	if len(remaining) == len(state.PendingStops) {
		return false
	}
	state.PendingStops = remaining
	return true
}

// Stop 本地删除一条分享，并尽力删除 relay 上的行。
//
// relay 不可达时按官方 StopShareResult 语义返回 {ok:true, relay:"unreachable"}，
// 同时把 shareId 记进 pendingStops，下一次 List 会真实重试。
func (s *Service) Stop(shareID string) (StopResult, error) {
	shareID = strings.TrimSpace(shareID)
	if shareID == "" {
		return StopResult{}, BadRequestf("shareId 不能为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.load()
	if err != nil {
		return StopResult{}, err
	}
	idx := findShare(state.Shares, shareID)
	if idx < 0 {
		// 本地没有记录，但 relay 上可能有残留（例如本地状态被删过）。stop 的语义是
		// "目标状态已达成"，所以仍然向 relay 发一次幂等 DELETE；删不掉就排 pending。
		relay := s.effectiveRelayLocked(state)
		if relay == "" || s.token == "" {
			return StopResult{OK: true}, nil
		}
		ctx, cancel := s.requestContext()
		err := s.client(relay).DeleteShare(ctx, shareID)
		cancel()
		if err != nil && !IsNotFound(err) {
			if IsUnreachable(err) {
				if !containsString(state.PendingStops, shareID) {
					state.PendingStops = append(state.PendingStops, shareID)
					if saveErr := s.save(state); saveErr != nil {
						return StopResult{}, saveErr
					}
				}
				return StopResult{OK: true, Relay: "unreachable", PendingQueued: true}, nil
			}
			return StopResult{}, err
		}
		return StopResult{OK: true}, nil
	}
	stored := state.Shares[idx]
	relay := stored.Relay
	if relay == "" {
		relay = s.effectiveRelayLocked(state)
	}
	if relay == "" {
		state.Shares = append(state.Shares[:idx], state.Shares[idx+1:]...)
		if err := s.save(state); err != nil {
			return StopResult{}, err
		}
		return StopResult{OK: true, Relay: "unreachable"}, nil
	}
	ctx, cancel := s.requestContext()
	err = s.client(relay).DeleteShare(ctx, shareID)
	cancel()
	switch {
	case err == nil, IsNotFound(err):
		state.Shares = append(state.Shares[:idx], state.Shares[idx+1:]...)
		state.PendingStops = removeString(state.PendingStops, shareID)
		if err := s.save(state); err != nil {
			return StopResult{}, err
		}
		return StopResult{OK: true}, nil
	case IsUnreachable(err):
		state.Shares = append(state.Shares[:idx], state.Shares[idx+1:]...)
		if !containsString(state.PendingStops, shareID) {
			state.PendingStops = append(state.PendingStops, shareID)
		}
		if err := s.save(state); err != nil {
			return StopResult{}, err
		}
		return StopResult{OK: true, Relay: "unreachable", PendingQueued: true}, nil
	default:
		return StopResult{}, err
	}
}

// UpdatePermissions 改一条分享的 tier / fork / 过期时间。
func (s *Service) UpdatePermissions(req UpdatePermissionsRequest) (Permissions, error) {
	req.ShareID = strings.TrimSpace(req.ShareID)
	if req.ShareID == "" {
		return Permissions{}, BadRequestf("shareId 不能为空")
	}
	if req.SessionMode == nil && req.AllowsFork == nil && req.TTLMinutes == nil && !req.ClearTTL {
		return Permissions{}, BadRequestf("至少要改 sessionMode / allowsFork / ttlMinutes 之一")
	}
	body := map[string]any{}
	if req.SessionMode != nil {
		if *req.SessionMode != "view" && *req.SessionMode != "write" {
			return Permissions{}, BadRequestf("sessionMode 只能是 view 或 write")
		}
		body["sessionMode"] = *req.SessionMode
	}
	if req.AllowsFork != nil {
		body["allowsFork"] = *req.AllowsFork
	}
	if req.ClearTTL {
		body["ttlMinutes"] = nil
	} else if req.TTLMinutes != nil {
		ttl := *req.TTLMinutes
		if math.IsNaN(ttl) || math.IsInf(ttl, 0) || ttl <= 0 || ttl > 525600 {
			return Permissions{}, BadRequestf("ttlMinutes 必须在 (0, 525600] 之间（要永不过期用 null）")
		}
		body["ttlMinutes"] = ttl
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.load()
	if err != nil {
		return Permissions{}, err
	}
	idx := findShare(state.Shares, req.ShareID)
	if idx < 0 {
		return Permissions{}, fmt.Errorf("%w：%s", ErrShareNotFound, req.ShareID)
	}
	relay := state.Shares[idx].Relay
	if relay == "" {
		relay = s.effectiveRelayLocked(state)
	}
	if relay == "" {
		return Permissions{}, ErrRelayNotConfigured
	}
	ctx, cancel := s.requestContext()
	live, err := s.client(relay).PatchShare(ctx, req.ShareID, body)
	cancel()
	if err != nil {
		if IsNotFound(err) {
			return Permissions{}, fmt.Errorf("%w：%s", ErrShareNotFound, req.ShareID)
		}
		return Permissions{}, err
	}
	state.Shares[idx].SessionMode = live.SessionMode
	state.Shares[idx].AllowsFork = live.AllowsFork
	state.Shares[idx].ExpiresAt = live.ExpiresAt
	if err := s.save(state); err != nil {
		return Permissions{}, err
	}
	return Permissions{
		ShareID: live.ShareID, SessionMode: live.SessionMode,
		AllowsFork: live.AllowsFork, ExpiresAt: live.ExpiresAt,
	}, nil
}

// Capabilities 返回插件实时声明的端点清单。
func (s *Service) Capabilities() CapabilityOffers {
	return CapabilityOffers{Capabilities: s.offers.List()}
}

// SetCapabilities 用一份真实选择替换一条分享暴露的端点。
func (s *Service) SetCapabilities(req SetCapabilitiesRequest) (ShareCapabilities, error) {
	req.ShareID = strings.TrimSpace(req.ShareID)
	if req.ShareID == "" {
		return ShareCapabilities{}, BadRequestf("shareId 不能为空")
	}
	if err := s.offers.ValidateSelection(req.Capabilities); err != nil {
		return ShareCapabilities{}, BadRequestf("%v", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.load()
	if err != nil {
		return ShareCapabilities{}, err
	}
	idx := findShare(state.Shares, req.ShareID)
	if idx < 0 {
		return ShareCapabilities{}, fmt.Errorf("%w：%s", ErrShareNotFound, req.ShareID)
	}
	relay := state.Shares[idx].Relay
	if relay == "" {
		relay = s.effectiveRelayLocked(state)
	}
	if relay == "" {
		return ShareCapabilities{}, ErrRelayNotConfigured
	}
	ctx, cancel := s.requestContext()
	caps, err := s.client(relay).PutCapabilities(ctx, req.ShareID, req.Capabilities)
	cancel()
	if err != nil {
		if IsNotFound(err) {
			return ShareCapabilities{}, fmt.Errorf("%w：%s", ErrShareNotFound, req.ShareID)
		}
		return ShareCapabilities{}, err
	}
	state.Shares[idx].ViewerCapabilities = append([]Capability{}, caps...)
	if err := s.save(state); err != nil {
		return ShareCapabilities{}, err
	}
	return ShareCapabilities{ShareID: req.ShareID, ViewerCapabilities: caps}, nil
}

// Identity 报告 relay 验证出的 owner 身份。没有凭据时如实回 unverified。
func (s *Service) Identity(req IdentityRequest) (IdentityStatus, error) {
	explicit := strings.TrimSpace(req.Relay)
	if explicit != "" {
		normalized, err := ValidateRelayURL(explicit)
		if err != nil {
			return IdentityStatus{}, BadRequestf("%v", err)
		}
		explicit = normalized
	}
	s.mu.Lock()
	state, err := s.load()
	if err != nil {
		s.mu.Unlock()
		return IdentityStatus{}, err
	}
	relay := explicit
	if relay == "" {
		relay = s.effectiveRelayLocked(state)
	}
	s.mu.Unlock()
	if relay == "" {
		return IdentityStatus{State: "unverified"}, nil
	}
	if s.token == "" {
		return IdentityStatus{State: "unverified", Relay: relay}, nil
	}
	ctx, cancel := s.requestContext()
	defer cancel()
	status, err := s.client(relay).OwnerIdentity(ctx)
	if err != nil {
		switch {
		case IsNotFound(err), relayStatus(err) == http.StatusMethodNotAllowed:
			return IdentityStatus{State: "relay-unsupported", Relay: relay}, nil
		case IsUnauthorized(err), IsForbidden(err):
			return IdentityStatus{State: "unverified", Relay: relay}, nil
		default:
			return IdentityStatus{}, err
		}
	}
	status.Relay = relay
	switch status.State {
	case "verified":
		return status, nil
	default:
		return IdentityStatus{State: "unverified", Relay: relay}, nil
	}
}

// Audit 读 relay 上一条分享的持久审计记录。
func (s *Service) Audit(shareID string) (Audit, error) {
	shareID = strings.TrimSpace(shareID)
	if shareID == "" {
		return Audit{}, BadRequestf("shareId 不能为空")
	}
	s.mu.Lock()
	state, err := s.load()
	if err != nil {
		s.mu.Unlock()
		return Audit{}, err
	}
	relay := ""
	if idx := findShare(state.Shares, shareID); idx >= 0 {
		relay = state.Shares[idx].Relay
	}
	if relay == "" {
		relay = s.effectiveRelayLocked(state)
	}
	s.mu.Unlock()
	if relay == "" {
		return Audit{}, ErrRelayNotConfigured
	}
	ctx, cancel := s.requestContext()
	defer cancel()
	events, err := s.client(relay).Audit(ctx, shareID)
	if err != nil {
		if IsNotFound(err) {
			return Audit{}, fmt.Errorf("%w：relay 上没有 %s（可能已 stop 或过期）", ErrShareNotFound, shareID)
		}
		return Audit{}, err
	}
	return Audit{Events: events}, nil
}

// TestRelay 做匿名连通性探测（不需要 owner token）。
func (s *Service) TestRelay() (Connectivity, error) {
	s.mu.Lock()
	state, err := s.load()
	if err != nil {
		s.mu.Unlock()
		return Connectivity{}, err
	}
	relay := s.effectiveRelayLocked(state)
	s.mu.Unlock()
	if relay == "" {
		return Connectivity{OK: false, HTTPReachable: false, Blocking: true,
			Error: "还没有配置 relay 地址：先 setRelay 或设置 DSH_LIVESHARE_RELAY"}, nil
	}
	client := NewClient(relay, "", s.httpClient)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	health, err := client.Health(ctx)
	if err != nil {
		if IsUnreachable(err) {
			return Connectivity{OK: false, HTTPReachable: false, Blocking: false, Error: err.Error()}, nil
		}
		return Connectivity{OK: false, HTTPReachable: true, Blocking: true, Error: err.Error()}, nil
	}
	if !health.OK {
		return Connectivity{OK: false, HTTPReachable: true, Blocking: true,
			Error: "relay 健康检查返回 ok=false"}, nil
	}
	return Connectivity{OK: true, HTTPReachable: true, Blocking: false}, nil
}

// TestPublish 在 relay 上注册一条 throwaway share 再删掉，验证本机真有发布权。
//
// 成功 = relay 接受注册；删不掉只写进 error，不影响"能发布"的结论，
// 但绝不把注册失败说成成功。
func (s *Service) TestPublish(sessionID string) (PublishView, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return PublishView{}, BadRequestf("sessionId 不能为空")
	}
	s.mu.Lock()
	state, err := s.load()
	if err != nil {
		s.mu.Unlock()
		return PublishView{}, err
	}
	relay := s.effectiveRelayLocked(state)
	s.mu.Unlock()
	if relay == "" {
		return PublishView{OK: false, Blocking: true,
			Error: "还没有配置 relay 地址：先 setRelay 或设置 DSH_LIVESHARE_RELAY"}, nil
	}
	ttl := 1.0 // 一分钟，删不掉也会自动过期，不留长期垃圾
	client := s.client(relay)
	ctx, cancel := s.requestContext()
	defer cancel()
	live, err := client.CreateShare(ctx, relayCreateRequest{
		SessionID: sessionID, SessionTitle: "[probe] liveshare publish test",
		SessionMode: "view", AccessMode: "open", TTLMinutes: &ttl,
	})
	if err != nil {
		blocking := IsUnauthorized(err) || IsForbidden(err) || relayStatus(err) == http.StatusBadRequest
		if IsUnreachable(err) {
			blocking = false
		}
		return PublishView{OK: false, Blocking: blocking, Error: err.Error()}, nil
	}
	deleteCtx, deleteCancel := s.requestContext()
	defer deleteCancel()
	if err := client.DeleteShare(deleteCtx, live.ShareID); err != nil {
		return PublishView{OK: true, Blocking: false,
			Error: "发布验证成功，但 throwaway share " + live.ShareID + " 未删除：" + err.Error()}, nil
	}
	return PublishView{OK: true}, nil
}

// composeAccess 把请求与 relay 配置拼成官方 ShareAccess 形状。
func composeAccess(req CreateRequest, allowed []string, cfg RelayConfig) Access {
	deployment := append([]string{}, cfg.DeploymentAllowedSubjectIDs...)
	selected := make([]SelectedIdentity, 0, len(allowed))
	for _, subjectID := range allowed {
		selected = append(selected, SelectedIdentity{
			ProviderID: cfg.ProviderID, TenantID: cfg.TenantID,
			SubjectID: subjectID, IdentityKey: identityKey(cfg.ProviderID, cfg.TenantID, subjectID),
		})
	}
	broadened := false
	for _, subjectID := range deployment {
		if !containsString(allowed, subjectID) {
			broadened = true
			break
		}
	}
	return Access{
		Mode:                          req.AccessMode,
		Selected:                      selected,
		DeploymentAllowedSubjectIDs:   deployment,
		ShareAllowedSubjectIDs:        allowed,
		BroadenedByDeploymentSubjects: broadened,
		IdentityKeyACL:                true,
	}
}

// identityKey 是 provider/tenant/subject 的稳定派生键（官方 identityKey 语义：
// 同一个身份跨会话稳定、不可逆）。用 SHA-256 十六进制，不编随机值。
func identityKey(providerID, tenantID, subjectID string) string {
	sum := sha256.Sum256([]byte(providerID + "\x00" + tenantID + "\x00" + subjectID))
	return hex.EncodeToString(sum[:])
}

func normalizeIDs(ids []string) ([]string, error) {
	out := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, errors.New("allowedSubjectIds 里有空串")
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func removeString(list []string, remove string) []string {
	out := make([]string, 0, len(list))
	for _, item := range list {
		if item != remove {
			out = append(out, item)
		}
	}
	return out
}
