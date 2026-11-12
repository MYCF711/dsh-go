package account

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// startOIDC 走 RFC 8628 device code：自托管 OIDC 真实端点，不接受空配置冒充可用。
func (s *Service) startOIDC(ctx context.Context, opts StartOptions) (State, error) {
	issuer := strings.TrimRight(strings.TrimSpace(s.oidc.Issuer), "/")
	if issuer == "" || strings.TrimSpace(s.oidc.ClientID) == "" {
		return State{}, fmt.Errorf("account: self-hosted OIDC 需要 DSHD_ACCOUNT_OIDC_ISSUER 与 DSHD_ACCOUNT_OIDC_CLIENT_ID")
	}
	deviceURL := s.oidc.DeviceAuthURL
	if deviceURL == "" {
		deviceURL = issuer + "/device_authorization"
	}
	form := url.Values{"client_id": {s.oidc.ClientID}, "scope": {defaultScopes(s.oidc.Scopes)}}
	var dev struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int64  `json:"expires_in"`
		Interval        int64  `json:"interval"`
	}
	if err := s.postForm(ctx, deviceURL, form, &dev); err != nil {
		return State{}, err
	}
	if dev.DeviceCode == "" || dev.VerificationURI == "" || dev.ExpiresIn <= 0 {
		return State{}, fmt.Errorf("account: OIDC device authorization 返回不完整")
	}
	id, err := randomID()
	if err != nil {
		return State{}, err
	}
	s.mu.Lock()
	s.doc.DeviceCode = dev.DeviceCode
	s.doc.Issuer = issuer
	s.doc.Attempt = &Attempt{
		ID: id, Phase: PhaseWaitingBrowser, AuthorizeURL: dev.VerificationURI,
		ExpiresAt: time.Now().Add(time.Duration(dev.ExpiresIn) * time.Second).UnixMilli(),
	}
	s.doc.Status = StatusSignedOut
	if err := s.saveLocked(); err != nil {
		s.mu.Unlock()
		return State{}, err
	}
	s.notifyLocked()
	st := s.stateLocked()
	s.mu.Unlock()
	return st, nil
}

func (s *Service) completeOIDC(ctx context.Context) (State, error) {
	s.mu.Lock()
	device := s.doc.DeviceCode
	issuer := s.doc.Issuer
	s.mu.Unlock()
	if device == "" || issuer == "" {
		return State{}, fmt.Errorf("account: 没有在跑的 OIDC device code")
	}
	tokenURL := s.oidc.TokenURL
	if tokenURL == "" {
		tokenURL = issuer + "/token"
	}
	form := url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {device},
		"client_id":   {s.oidc.ClientID},
	}
	if s.oidc.ClientSecret != "" {
		form.Set("client_secret", s.oidc.ClientSecret)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := s.postForm(ctx, tokenURL, form, &tok); err != nil {
		return s.failAttempt(ctx, "network")
	}
	if tok.AccessToken == "" {
		return s.failAttempt(ctx, "protocol")
	}
	userinfo := s.oidc.UserInfoURL
	if userinfo == "" {
		userinfo = issuer + "/userinfo"
	}
	var claims struct {
		Sub     string  `json:"sub"`
		Name    *string `json:"name"`
		Email   *string `json:"email"`
		Picture *string `json:"picture"`
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, userinfo, nil)
	if err != nil {
		return State{}, err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	resp, err := s.client.Do(req)
	if err != nil {
		return s.failAttempt(ctx, "network")
	}
	raw, err := readLimited(resp)
	if err != nil {
		return State{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return s.failAttempt(ctx, "protocol")
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		return s.failAttempt(ctx, "protocol")
	}
	if claims.Sub == "" {
		return s.failAttempt(ctx, "protocol")
	}
	profile := Profile{ID: strptr(claims.Sub), Name: claims.Name, Contact: claims.Email,
		AvatarURL: claims.Picture, Source: SourceSelfHosted}
	expires := time.Now().Add(s.lease).UnixMilli()
	if tok.ExpiresIn > 0 {
		expires = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second).UnixMilli()
	}
	if expires <= time.Now().UnixMilli() {
		expires = time.Now().Add(s.lease).UnixMilli()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.doc.AccountID = claims.Sub
	s.doc.Profile = &profile
	s.doc.Token = tok.AccessToken
	s.doc.Status = StatusCredentialStored
	s.doc.ExpiresAt = expires
	if s.doc.Attempt != nil {
		s.doc.Attempt.Phase = PhaseSucceeded
	}
	if err := s.saveLocked(); err != nil {
		return State{}, err
	}
	s.resetTimerLocked()
	// OIDC 令牌落盘 = 凭据写入 → credentials-changed。
	s.notifyKindLocked(UpdateCredentialsChanged)
	return s.stateLocked(), nil
}

func defaultScopes(scopes string) string {
	if strings.TrimSpace(scopes) == "" {
		return "openid profile email"
	}
	return scopes
}
