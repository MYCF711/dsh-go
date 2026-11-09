// Package liveshare 是自托管 Liveshare relay 的宿主侧客户端与 owner 状态。
//
// 分层：
//   - relay 子包是 relay 服务端（cmd/dsh-relayd 的库形态）；
//   - ws 子包是最小 RFC6455 实现，两端共用；
//   - 本包是 host 侧：owner-shares.json 状态、relay HTTP 客户端、12 个方法的
//     真实语义（No fake ids, no fabricated identity）。
//
// 官方 relay 的部署实现（OAuth/数据库/路由）不在官方包里，其 wire 不可见；
// 本包与 relay 子包的 wire 是**本仓自托管协议**，形状按官方 12 方法的结果类型
// 反推（shareId/link/capabilities/audit/permissions）。切换 relay 只改地址。
package liveshare

import "time"

// Capability 是一个插件声明的端点能力（与官方 ShareCapabilityGroup 同形）。
type Capability struct {
	Plugin   string   `json:"plugin"`
	Readable []string `json:"readable"`
	Writable []string `json:"writable"`
}

// SelectedIdentity 是 selected 分享里被点名的 viewer 身份。
type SelectedIdentity struct {
	ProviderID  string `json:"providerId"`
	TenantID    string `json:"tenantId"`
	SubjectID   string `json:"subjectId"`
	IdentityKey string `json:"identityKey"`
}

// Access 是分享的准入面（与官方 ShareAccess 同形）。
type Access struct {
	Mode                          string             `json:"mode"`
	Selected                      []SelectedIdentity `json:"selected"`
	DeploymentAllowedSubjectIDs   []string           `json:"deploymentAllowedSubjectIds"`
	ShareAllowedSubjectIDs        []string           `json:"shareAllowedSubjectIds"`
	BroadenedByDeploymentSubjects bool               `json:"broadenedByDeploymentSubjects"`
	IdentityKeyACL                bool               `json:"identityKeyAcl"`
}

// CreateRequest 是 create 的入参（字段与官方 CreateShareRequest 同形）。
type CreateRequest struct {
	SessionID         string
	SessionTitle      string
	AccessMode        string // open | selected（空 = open）
	TTLMinutes        *float64
	AllowedSubjectIDs []string
	AllowsFork        bool
	SessionMode       string // view | write（空 = view）
}

// CreatedShare 是 create 的结果（与官方 CreatedShare 同形）。
type CreatedShare struct {
	Link        string `json:"link"`
	ShareID     string `json:"shareId"`
	SessionID   string `json:"sessionId"`
	SessionMode string `json:"sessionMode"`
	Access      Access `json:"access"`
}

// ShareView 是一条 owner 分享（与官方 OwnerShareView 同形）。
type ShareView struct {
	ShareID            string       `json:"shareId"`
	SessionID          string       `json:"sessionId"`
	SessionTitle       string       `json:"sessionTitle"`
	Link               string       `json:"link"`
	CreatedAt          time.Time    `json:"createdAt"`
	ExpiresAt          *time.Time   `json:"expiresAt,omitempty"`
	AllowsFork         bool         `json:"allowsFork"`
	SessionMode        string       `json:"sessionMode"`
	ViewerCapabilities []Capability `json:"viewerCapabilities"`
	Online             bool         `json:"online"`
	Access             *Access      `json:"access,omitempty"`
}

// ShareList 是 list 的结果。
type ShareList struct {
	Shares []ShareView `json:"shares"`
}

// RelayState 是 relay 方法的返回（与官方 OwnerRelayState 同形）。
//
// configured 是部署给的地址（DSH_LIVESHARE_RELAY），override 是 owner 用
// setRelay 保存的地址；effective 是当前真正生效的那一个。
type RelayState struct {
	Configured string `json:"configured,omitempty"`
	Override   string `json:"override,omitempty"`
	Effective  string `json:"effective,omitempty"`
}

// StopResult 是 stop 的返回（与官方 StopShareResult 同形）。
type StopResult struct {
	OK            bool   `json:"ok"`
	Relay         string `json:"relay,omitempty"` // "unreachable" 表示 relay 侧还没删掉，需要重试
	PendingQueued bool   `json:"-"`
}

// Permissions 是 updatePermissions 的返回（与官方 SharePermissions 同形）。
type Permissions struct {
	ShareID     string     `json:"shareId"`
	SessionMode string     `json:"sessionMode"`
	AllowsFork  bool       `json:"allowsFork"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
}

// CapabilityOffers 是 capabilities 的返回（与官方 ShareCapabilityOffers 同形）。
type CapabilityOffers struct {
	Capabilities []Capability `json:"capabilities"`
}

// ShareCapabilities 是 setCapabilities 的返回（与官方 ShareCapabilities 同形）。
type ShareCapabilities struct {
	ShareID            string       `json:"shareId"`
	ViewerCapabilities []Capability `json:"viewerCapabilities"`
}

// IdentityRequest 是 identity 的入参（与官方 OwnerIdentityRequest 同形）。
type IdentityRequest struct {
	Relay string
}

// IdentityStatus 是 identity 的返回（与官方 OwnerIdentityStatus 同形）。
//
// 状态取值：verified / expired / unverified / relay-unsupported。
// self-host relay 没有"过期 token"这条真实状态，所以本实现只会返回
// verified / unverified / relay-unsupported——不编 expired。
type IdentityStatus struct {
	State       string     `json:"state"`
	Relay       string     `json:"relay"`
	DisplayName string     `json:"displayName,omitempty"`
	SubjectID   string     `json:"subjectId,omitempty"`
	AvatarURL   string     `json:"avatarUrl,omitempty"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
	Renewed     *bool      `json:"renewed,omitempty"`
	Claim       *ClaimOut  `json:"claim,omitempty"`
}

// ClaimOut 是 identity 的可选 claim 结果（本实现不跑 claim pass，故为 nil）。
type ClaimOut struct {
	Claimed int `json:"claimed"`
	Skipped int `json:"skipped"`
}

// AuditEntry 是一条审计记录（与官方 ShareAuditEntryView 同形）。
type AuditEntry struct {
	ID        string    `json:"id"`
	Action    string    `json:"action"`
	CreatedAt time.Time `json:"createdAt"`
	Actor     string    `json:"actor,omitempty"`
	Detail    any       `json:"detail,omitempty"`
}

// Audit 是 audit 的返回。
type Audit struct {
	Events []AuditEntry `json:"events"`
}

// Connectivity 是 testRelay 的返回（与官方 RelayConnectivityView 同形）。
type Connectivity struct {
	OK            bool   `json:"ok"`
	HTTPReachable bool   `json:"httpReachable"`
	Blocking      bool   `json:"blocking"`
	Error         string `json:"error,omitempty"`
}

// PublishView 是 testPublish 的返回（与官方 RelayPublishView 同形）。
type PublishView struct {
	OK       bool   `json:"ok"`
	Blocking bool   `json:"blocking"`
	Error    string `json:"error,omitempty"`
}

// UpdatePermissionsRequest 是 updatePermissions 的入参。
type UpdatePermissionsRequest struct {
	ShareID     string
	SessionMode *string
	AllowsFork  *bool
	TTLMinutes  *float64
	ClearTTL    bool // ttlMinutes: null（永不过期）
}

// SetCapabilitiesRequest 是 setCapabilities 的入参。
type SetCapabilitiesRequest struct {
	ShareID      string
	Capabilities []Capability
}
