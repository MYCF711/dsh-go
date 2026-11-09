package liveshare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"dshgo/internal/liveshare/ws"
)

// ErrNoOwnerToken 表示没有配置 owner 凭据。宿主必须显式报"没凭据"，不能
// 拿匿名请求去撞 relay 再把它说成"relay 不可用"。
var ErrNoOwnerToken = errors.New("没有配置 liveshare owner token")

// ErrRelayNotConfigured 表示还没选 relay 地址（先 setRelay 或部署配置）。
var ErrRelayNotConfigured = errors.New("还没有配置 liveshare relay 地址")

// ErrUnreachable 是 relay 连不上的哨兵（网络层失败，不是 HTTP 错误码）。
var ErrUnreachable = errors.New("liveshare relay 不可达")

// UnreachableError 携带具体 relay 地址与底层错误。
type UnreachableError struct {
	URL string
	Err error
}

func (e *UnreachableError) Error() string {
	return fmt.Sprintf("%s：%s（%v）", ErrUnreachable.Error(), e.URL, e.Err)
}

func (e *UnreachableError) Unwrap() error { return ErrUnreachable }

// IsUnreachable 判断一个错误是不是网络层不可达。
func IsUnreachable(err error) bool { return errors.Is(err, ErrUnreachable) }

// RelayError 是 relay 返回的非 2xx 结构化错误。
type RelayError struct {
	Status  int
	Code    string
	Message string
}

func (e *RelayError) Error() string {
	return fmt.Sprintf("relay 返回 %d %s：%s", e.Status, e.Code, e.Message)
}

// IsUnauthorized / IsNotFound 给调用方按状态分类（不按中文消息猜）。
func IsUnauthorized(err error) bool { return relayStatus(err) == http.StatusUnauthorized }
func IsForbidden(err error) bool    { return relayStatus(err) == http.StatusForbidden }
func IsNotFound(err error) bool     { return relayStatus(err) == http.StatusNotFound }

// RelayStatusCode 返回 relay 错误里的 HTTP 状态（不是 relay 错误返回 0）。
// 宿主用它把"relay 拒绝了这个请求(400)"映射成可修正的 bad_request。
func RelayStatusCode(err error) int { return relayStatus(err) }

func relayStatus(err error) int {
	var relayErr *RelayError
	if errors.As(err, &relayErr) {
		return relayErr.Status
	}
	return 0
}

// Client 是 relay HTTP/WS 客户端。
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// NewClient 造一个客户端；httpClient 为空时用 10s 超时的默认值。
func NewClient(baseURL, token string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{BaseURL: normalizeBaseURL(baseURL), Token: strings.TrimSpace(token), HTTP: httpClient}
}

// normalizeBaseURL 去掉末尾斜杠；不补 scheme（地址必须写全，避免 http/https 猜错）。
func normalizeBaseURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

// ValidateRelayURL 校验 owner 保存的 relay 地址：必须是 http/https 且带 host。
func ValidateRelayURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("relay 地址不是合法 URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("relay 地址只支持 http/https，收到 %q", u.Scheme)
	}
	if u.Host == "" {
		return "", errors.New("relay 地址缺 host")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("relay 地址不能带 query 或 fragment")
	}
	return normalizeBaseURL(raw), nil
}

func (c *Client) url(path string) string { return c.BaseURL + path }

func (c *Client) wsURL(path string) string {
	base := c.BaseURL
	switch {
	case strings.HasPrefix(base, "https://"):
		base = "wss://" + strings.TrimPrefix(base, "https://")
	case strings.HasPrefix(base, "http://"):
		base = "ws://" + strings.TrimPrefix(base, "http://")
	}
	return base + path
}

type relayErrorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// do 发一个请求并解码结果。owner=true 时要求带 token。
func (c *Client) do(ctx context.Context, method, path string, body, out any, owner bool) error {
	if c.BaseURL == "" {
		return ErrRelayNotConfigured
	}
	if owner && c.Token == "" {
		return ErrNoOwnerToken
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.url(path), reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if owner {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return &UnreachableError{URL: c.BaseURL, Err: err}
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if readErr != nil {
		return &UnreachableError{URL: c.BaseURL, Err: readErr}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var parsed relayErrorBody
		if err := json.Unmarshal(raw, &parsed); err != nil || parsed.Error.Code == "" {
			return &RelayError{Status: resp.StatusCode, Code: "http_error",
				Message: strings.TrimSpace(string(raw))}
		}
		return &RelayError{Status: resp.StatusCode, Code: parsed.Error.Code, Message: parsed.Error.Message}
	}
	if out != nil {
		if len(raw) == 0 {
			return errors.New("relay 成功响应没有 body")
		}
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("relay 响应不是合法 JSON: %w", err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// relay wire（本仓自托管协议；字段形状按官方 12 方法结果类型反推）
// ---------------------------------------------------------------------------

// relayShare 是 relay 侧返回的 share 形状。
type relayShare struct {
	ShareID            string       `json:"shareId"`
	SessionID          string       `json:"sessionId"`
	SessionTitle       string       `json:"sessionTitle"`
	Link               string       `json:"link"`
	CreatedAt          time.Time    `json:"createdAt"`
	ExpiresAt          *time.Time   `json:"expiresAt"`
	AllowsFork         bool         `json:"allowsFork"`
	SessionMode        string       `json:"sessionMode"`
	ViewerCapabilities []Capability `json:"viewerCapabilities"`
	Online             bool         `json:"online"`
	AccessMode         string       `json:"accessMode"`
	AllowedSubjectIDs  []string     `json:"allowedSubjectIds"`
}

// RelayConfig 是 relay 的 owner 配置（用于 compose access 对象）。
type RelayConfig struct {
	ProviderID                  string   `json:"providerId"`
	TenantID                    string   `json:"tenantId"`
	DeploymentAllowedSubjectIDs []string `json:"deploymentAllowedSubjectIds"`
	ViewerAuth                  string   `json:"viewerAuth"`
}

// HealthDoc 是 /v1/health 的返回。
type HealthDoc struct {
	OK              bool   `json:"ok"`
	Service         string `json:"service"`
	Version         string `json:"version"`
	OwnerConfigured bool   `json:"ownerConfigured"`
	ProviderID      string `json:"providerId"`
	TenantID        string `json:"tenantId"`
}

func (c *Client) Health(ctx context.Context) (HealthDoc, error) {
	var out HealthDoc
	err := c.do(ctx, http.MethodGet, "/v1/health", nil, &out, false)
	return out, err
}

func (c *Client) OwnerConfig(ctx context.Context) (RelayConfig, error) {
	var out RelayConfig
	err := c.do(ctx, http.MethodGet, "/v1/owner/config", nil, &out, true)
	return out, err
}

// OwnerIdentity 查 relay 验证出的 owner 身份。
func (c *Client) OwnerIdentity(ctx context.Context) (IdentityStatus, error) {
	var raw struct {
		State       string `json:"state"`
		ProviderID  string `json:"providerId"`
		TenantID    string `json:"tenantId"`
		SubjectID   string `json:"subjectId"`
		DisplayName string `json:"displayName"`
		ExpiresAt   string `json:"expiresAt"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/owner/identity", nil, &raw, true); err != nil {
		return IdentityStatus{}, err
	}
	status := IdentityStatus{
		State: raw.State, Relay: c.BaseURL,
		SubjectID: raw.SubjectID, DisplayName: raw.DisplayName,
	}
	if raw.ExpiresAt != "" {
		if ts, err := time.Parse(time.RFC3339, raw.ExpiresAt); err == nil {
			status.ExpiresAt = &ts
		}
	}
	return status, nil
}

type relayCreateRequest struct {
	SessionID         string   `json:"sessionId"`
	SessionTitle      string   `json:"sessionTitle,omitempty"`
	AccessMode        string   `json:"accessMode,omitempty"`
	TTLMinutes        *float64 `json:"ttlMinutes,omitempty"`
	AllowedSubjectIDs []string `json:"allowedSubjectIds,omitempty"`
	AllowsFork        bool     `json:"allowsFork,omitempty"`
	SessionMode       string   `json:"sessionMode,omitempty"`
}

func (c *Client) CreateShare(ctx context.Context, req relayCreateRequest) (relayShare, error) {
	var out relayShare
	err := c.do(ctx, http.MethodPost, "/v1/shares", req, &out, true)
	return out, err
}

func (c *Client) ListShares(ctx context.Context) ([]relayShare, error) {
	var out struct {
		Shares []relayShare `json:"shares"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/shares", nil, &out, true); err != nil {
		return nil, err
	}
	return out.Shares, nil
}

func (c *Client) GetShare(ctx context.Context, shareID string) (relayShare, error) {
	var out relayShare
	err := c.do(ctx, http.MethodGet, "/v1/shares/"+url.PathEscape(shareID), nil, &out, true)
	return out, err
}

// PatchShare 的 body 用 map：ttlMinutes 需要能发 null（= 永不过期），
// 用结构体指针 + omitempty 表达不了"显式 null"。
func (c *Client) PatchShare(ctx context.Context, shareID string, req map[string]any) (relayShare, error) {
	var out relayShare
	err := c.do(ctx, http.MethodPatch, "/v1/shares/"+url.PathEscape(shareID), req, &out, true)
	return out, err
}

func (c *Client) DeleteShare(ctx context.Context, shareID string) error {
	var out struct {
		OK bool `json:"ok"`
	}
	return c.do(ctx, http.MethodDelete, "/v1/shares/"+url.PathEscape(shareID), nil, &out, true)
}

func (c *Client) Audit(ctx context.Context, shareID string) ([]AuditEntry, error) {
	var out struct {
		Events []AuditEntry `json:"events"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/shares/"+url.PathEscape(shareID)+"/audit", nil, &out, true); err != nil {
		return nil, err
	}
	return out.Events, nil
}

func (c *Client) PutCapabilities(ctx context.Context, shareID string, caps []Capability) ([]Capability, error) {
	body := struct {
		Capabilities []Capability `json:"capabilities"`
	}{Capabilities: caps}
	var out struct {
		ViewerCapabilities []Capability `json:"viewerCapabilities"`
	}
	if err := c.do(ctx, http.MethodPut, "/v1/shares/"+url.PathEscape(shareID)+"/capabilities", body, &out, true); err != nil {
		return nil, err
	}
	return out.ViewerCapabilities, nil
}

// DialPublisher 连上 relay 的 owner 发布通道（数据面）。
//
// 宿主网关把会话帧写进这条连接；它不在 12 个方法里，但真实端到端需要它。
func (c *Client) DialPublisher(ctx context.Context, shareID string) (*ws.Conn, error) {
	if c.Token == "" {
		return nil, ErrNoOwnerToken
	}
	header := http.Header{"Authorization": []string{"Bearer " + c.Token}}
	return ws.Dial(ctx, c.wsURL("/v1/publish?share="+url.QueryEscape(shareID)), header)
}

// DialViewer 连上 viewer 通道（测试与最小 viewer 用）。
func (c *Client) DialViewer(ctx context.Context, shareID string, since int64, fork bool, viewerSecret string) (*ws.Conn, error) {
	path := "/v1/connect?share=" + url.QueryEscape(shareID)
	if since > 0 {
		path += fmt.Sprintf("&since=%d", since)
	}
	if fork {
		path += "&fork=1"
	}
	var header http.Header
	if viewerSecret != "" {
		header = http.Header{"Authorization": []string{"Bearer " + viewerSecret}}
	}
	return ws.Dial(ctx, c.wsURL(path), header)
}
