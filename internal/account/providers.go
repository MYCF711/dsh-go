package account

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ---- 通用 HTTP -------------------------------------------------------------

func (s *Service) doJSON(ctx context.Context, method, endpoint string, headers map[string]string, body any, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	raw, err := readLimited(resp)
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("account: %s %s 返回 HTTP %d: %s", method, endpoint, resp.StatusCode, truncate(string(raw), 300))
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, fmt.Errorf("account: %s 响应不是合法 JSON: %w", endpoint, err)
		}
	}
	return resp.StatusCode, nil
}

type platformEnvelope struct {
	Code int `json:"code"`
	Data struct {
		BizCode int             `json:"biz_code"`
		BizData json.RawMessage `json:"biz_data"`
	} `json:"data"`
}

// unwrapPlatform 接受官方 envelope 与直接 JSON 两种响应（测试桩/旧端点）。
func unwrapPlatform(raw []byte, out any) error {
	var env platformEnvelope
	if err := json.Unmarshal(raw, &env); err == nil && env.Data.BizData != nil {
		if env.Code != 0 {
			return fmt.Errorf("account: Platform envelope code=%d", env.Code)
		}
		return json.Unmarshal(env.Data.BizData, out)
	}
	return json.Unmarshal(raw, out)
}

func (s *Service) platformJSON(ctx context.Context, method, path string, token string, body any, out any) error {
	headers := map[string]string{}
	if token != "" {
		headers["x-dsh-auth-token"] = token
	}
	var raw json.RawMessage
	if err := s.doJSONRaw(ctx, method, s.origin+path, headers, body, &raw); err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return unwrapPlatform(raw, out)
}

func (s *Service) doJSONRaw(ctx context.Context, method, endpoint string, headers map[string]string, body any, out *json.RawMessage) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	raw, err := readLimited(resp)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("account: %s %s HTTP %d: %s", method, endpoint, resp.StatusCode, truncate(string(raw), 300))
	}
	*out = raw
	return nil
}

func (s *Service) postForm(ctx context.Context, endpoint string, values url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	raw, err := readLimited(resp)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("account: POST %s HTTP %d: %s", endpoint, resp.StatusCode, truncate(string(raw), 300))
	}
	return json.Unmarshal(raw, out)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ---- official Platform -----------------------------------------------------

type officialUser struct {
	ID       *string `json:"id"`
	Email    string  `json:"email"`
	Mobile   string  `json:"mobile"`
	MobileNo string  `json:"mobile_number"`
	Profile  *struct {
		Name    *string `json:"name"`
		Picture *string `json:"picture"`
	} `json:"id_profile"`
}

func projectProfile(u officialUser) Profile {
	contact := u.Mobile
	if contact == "" {
		contact = u.MobileNo
	}
	if contact == "" {
		contact = u.Email
	}
	var name *string
	var avatar *string
	if u.Profile != nil {
		name = u.Profile.Name
		avatar = u.Profile.Picture
	}
	return Profile{ID: u.ID, Name: name, Contact: strptr(contact), AvatarURL: avatar, Source: SourceOfficial}
}

// attemptContext 把官方 `attemptTimeoutMs` 落到"这次登录尝试"的请求上。
//
// 为什么不是靠客户端超时：`s.client` 的 20s 是**单次 HTTP** 的预算
// （官方 requestTimeoutMs 是另一个参数），而一次尝试可能包含等待用户授权后的
// 交换；用 attempt 预算包住本次尝试的请求，才能让"整次尝试"有一个官方口径的
// 天花板，而不是被单请求超时截断或反过来无限期挂着。
func (s *Service) attemptContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if s.attemptTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, s.attemptTimeout)
}

// attemptDeadlineMs 返回本条尝试的绝对截止（毫秒）；0 表示不限。
func (s *Service) attemptDeadlineMs(now time.Time) int64 {
	if s.attemptTimeout <= 0 {
		return 0
	}
	return now.Add(s.attemptTimeout).UnixMilli()
}

func (s *Service) startOfficial(ctx context.Context, opts StartOptions) (State, error) {
	target := strings.TrimSpace(opts.CallbackOrigin)
	if target == "" {
		return State{}, fmt.Errorf("account: official startSignIn 需要 callbackOrigin")
	}
	attemptCtx, cancelAttempt := s.attemptContext(ctx)
	defer cancelAttempt()
	verifier, challenge, state, err := pkce()
	if err != nil {
		return State{}, err
	}
	redirect := strings.TrimRight(target, "/") + "/oauth/callback"
	req := map[string]any{
		"code_challenge": challenge, "code_challenge_method": "S256",
		"state": state, "redirect_uri": redirect, "locale": opts.Client.Locale,
		"login_source": opts.LoginSource,
	}
	var init struct {
		AuthorizeURL string  `json:"authorize_url"`
		AuthorizeID  string  `json:"authorize_id"`
		ExpiresIn    float64 `json:"expires_in"`
	}
	if err := s.platformJSON(attemptCtx, http.MethodPost, "/auth-api/v0/dsh/auth_init", "", req, &init); err != nil {
		return State{}, err
	}
	if init.AuthorizeURL == "" || init.ExpiresIn <= 0 {
		return State{}, fmt.Errorf("account: Platform auth_init 返回缺 authorize_url/expires_in")
	}
	id, err := randomID()
	if err != nil {
		return State{}, err
	}
	expires := time.Now().Add(time.Duration(init.ExpiresIn) * time.Second).UnixMilli()
	// 官方 :981 `expiresAt = Date.now() + attemptTimeout`：服务端说多久都不算数，
	// 尝试窗口的上界是 attemptTimeoutMs。取两者较小值——用户要的"等一会儿"不能被
	// 缩短到一个点不动窗口，服务端给一小时也不能让一次尝试挂一小时。
	if deadline := s.attemptDeadlineMs(time.Now()); deadline > 0 && deadline < expires {
		expires = deadline
	}
	s.mu.Lock()
	s.doc.CodeVerifier = verifier
	s.doc.State = state
	s.doc.RedirectURI = redirect
	s.doc.Attempt = &Attempt{ID: id, Phase: PhaseWaitingBrowser, AuthorizeURL: init.AuthorizeURL, ExpiresAt: expires}
	s.doc.Status = StatusSignedOut
	if err := s.saveLocked(); err != nil {
		s.mu.Unlock()
		return State{}, err
	}
	// 尝试预算到点必须由定时器**主动**推进（官方 :983 setTimeout(deadline.abort)）：
	// 不重新布防的话，attemptTimeout 只能在有人调 State() 时才被看见。
	s.resetTimerLocked()
	s.notifyLocked()
	st := s.stateLocked()
	s.mu.Unlock()
	return st, nil
}

func (s *Service) failAttempt(ctx context.Context, code string) (State, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.doc.Attempt != nil {
		s.doc.Attempt.Phase = PhaseFailed
		s.doc.Attempt.ErrorCode = code
	}
	_ = s.saveLocked()
	s.notifyLocked()
	return s.stateLocked(), nil
}

func (s *Service) completeOfficial(ctx context.Context, code string) (State, error) {
	attemptCtx, cancelAttempt := s.attemptContext(ctx)
	defer cancelAttempt()
	ctx = attemptCtx
	s.mu.Lock()
	verifier := s.doc.CodeVerifier
	redirect := s.doc.RedirectURI
	s.mu.Unlock()
	req := map[string]any{
		"code": code, "code_verifier": verifier, "redirect_uri": redirect,
		"device_id": "dsh-go", "device_model": "dsh-go", "os_version": "windows",
	}
	var exchange struct {
		Token string      `json:"token"`
		User  interface{} `json:"user"`
	}
	if err := s.platformJSON(ctx, http.MethodPost, "/auth-api/v0/dsh/auth_exchange", "", req, &exchange); err != nil {
		return s.failAttempt(ctx, "network")
	}
	if exchange.Token == "" {
		return s.failAttempt(ctx, "protocol")
	}
	var profile *Profile
	if raw, err := json.Marshal(exchange.User); err == nil && len(raw) > 4 {
		var u officialUser
		if json.Unmarshal(raw, &u) == nil {
			p := projectProfile(u)
			profile = &p
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.doc.Token = exchange.Token
	s.doc.Status = StatusCredentialStored
	s.doc.ExpiresAt = time.Now().Add(s.lease).UnixMilli()
	if s.doc.Attempt != nil {
		s.doc.Attempt.Phase = PhaseSucceeded
	}
	if profile != nil {
		s.doc.Profile = profile
		s.doc.AccountID = deref(profile.ID)
	}
	if err := s.saveLocked(); err != nil {
		return State{}, err
	}
	s.resetTimerLocked()
	s.notifyLocked()
	return s.stateLocked(), nil
}

func (s *Service) officialProfile(ctx context.Context, token string) (*Profile, error) {
	if token == "" {
		return nil, ErrSignedOut
	}
	var u officialUser
	if err := s.platformJSON(ctx, http.MethodGet, "/auth-api/v0/users/current", token, nil, &u); err != nil {
		if strings.Contains(err.Error(), "HTTP 401") {
			_ = s.clearToken()
			return nil, ErrSignedOut
		}
		return nil, err
	}
	p := projectProfile(u)
	return &p, nil
}

func (s *Service) officialBalance(ctx context.Context, token string) (*Balance, error) {
	if token == "" {
		return nil, nil
	}
	var summary struct {
		Normal []Wallet `json:"normal_wallets"`
		Bonus  []Wallet `json:"bonus_wallets"`
	}
	if err := s.platformJSON(ctx, http.MethodGet, "/api/v0/users/get_user_summary", token, nil, &summary); err != nil {
		if strings.Contains(err.Error(), "HTTP 401") {
			_ = s.clearToken()
			return nil, nil
		}
		return &Balance{Status: "failed", Source: SourceOfficial}, nil
	}
	return &Balance{Status: "ready", Value: summary.Normal, BonusWallets: summary.Bonus, Source: SourceOfficial}, nil
}

func (s *Service) officialBonuses(ctx context.Context, token string) (*BonusList, error) {
	if token == "" {
		return nil, nil
	}
	var list []struct {
		OrderID   string          `json:"order_id"`
		Title     *string         `json:"title"`
		Campaign  json.RawMessage `json:"campaign"`
		Amount    json.RawMessage `json:"amount"`
		Currency  json.RawMessage `json:"currency"`
		GrantedAt json.RawMessage `json:"granted_at"`
		ExpiresAt *string         `json:"expires_at"`
		Msg       string          `json:"msg"`
	}
	if err := s.platformJSON(ctx, http.MethodGet, "/api/v0/users/get_unnotified_bonuses", token, nil, &list); err != nil {
		if strings.Contains(err.Error(), "HTTP 401") {
			_ = s.clearToken()
			return nil, nil
		}
		return nil, err
	}
	s.mu.Lock()
	accountID := s.doc.AccountID
	s.mu.Unlock()
	out := make([]Bonus, 0, len(list))
	for _, b := range list {
		title := ""
		if b.Title != nil {
			title = *b.Title
		}
		expires := ""
		if b.ExpiresAt != nil {
			expires = *b.ExpiresAt
		}
		out = append(out, Bonus{Title: title, OrderID: b.OrderID, Campaign: b.Campaign,
			Amount: b.Amount, Currency: b.Currency, GrantedAt: b.GrantedAt, ExpiresAt: expires, Message: b.Msg})
	}
	return &BonusList{AccountID: accountID, Bonuses: out, Source: SourceOfficial}, nil
}

func (s *Service) officialAck(ctx context.Context, token, orderID string) (bool, error) {
	if token == "" || orderID == "" {
		return false, nil
	}
	if err := s.platformJSON(ctx, http.MethodPost, "/api/v0/users/ack_bonus_notified", token, map[string]any{"order_id": orderID}, nil); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.doc.Acked = append(s.doc.Acked, orderID)
	if err := s.saveLocked(); err != nil {
		return false, err
	}
	s.notifyLocked()
	return true, nil
}

func (s *Service) officialLogout(ctx context.Context, token string) error {
	return s.platformJSON(ctx, http.MethodPost, "/auth-api/v0/users/logout", token, nil, nil)
}

func (s *Service) clearToken() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.doc.Status = StatusSignedOut
	s.doc.Token = ""
	s.doc.ExpiresAt = 0
	if err := s.saveLocked(); err != nil {
		return err
	}
	s.resetTimerLocked()
	s.notifyLocked()
	return nil
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
