package relay

import (
	"encoding/json"
	"strings"
	"time"
)

// Version 是 self-host relay 的 wire 版本。host 侧只接受这个前缀的主版本。
const Version = "1"

// Capability 是一个插件声明的端点能力。
//
// 字段与官方 ShareCapability 同形（plugin/readable/writable）；官方是只读快照，
// relay 不做能力探测——它只存宿主通过 setCapabilities 交来的真实选择。
type Capability struct {
	Plugin   string   `json:"plugin"`
	Readable []string `json:"readable"`
	Writable []string `json:"writable"`
}

// Share 是 relay 侧的一条分享记录（对外视图）。
type Share struct {
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
	AccessMode         string       `json:"accessMode"`
	AllowedSubjectIDs  []string     `json:"allowedSubjectIds,omitempty"`
}

// AuditEvent 是 relay 持久化的一条审计记录。字段与官方 ShareAuditEvent 同形。
type AuditEvent struct {
	ID        string    `json:"id"`
	Action    string    `json:"action"`
	CreatedAt time.Time `json:"createdAt"`
	Actor     string    `json:"actor,omitempty"`
	Detail    any       `json:"detail,omitempty"`
}

// marshalEvent 用固定的时间格式（RFC3339Nano UTC），保证审计文件里的
// createdAt 与 API 返回值逐字一致。
func marshalEvent(e AuditEvent) ([]byte, error) {
	clone := e
	clone.CreatedAt = e.CreatedAt.UTC()
	return json.Marshal(clone)
}

// Fork 是一条 fork 记录。fork 不单独持久化：relay 重启后所有连接都断了，
// 旧的 fork 也随之失效（这是真实状态，不是丢失数据）。
type Fork struct {
	ID        string
	ShareID   string
	CreatedAt time.Time
	Viewers   int
}

// ViewerCredential 是 relay 已知的一个 viewer 身份。
//
// self-host 没有 OIDC：身份由 relay 配置的共享密钥验证（每个 subject 一条），
// relay 是唯一的验证者。host 侧只转发 relay 的结论，绝不本地编身份。
type ViewerCredential struct {
	SubjectID string `json:"subjectId"`
	Secret    string `json:"secret"`
}

// Config 是 relay 的启动配置。
type Config struct {
	// Home 是 relay 的持久目录（owner token / 审计 / share 记录）。
	Home string
	// OwnerToken 为空时从 Home 读取或生成；非空时用作本次运行的 owner token。
	OwnerToken string
	// ProviderID / TenantID 是本 relay 部署的身份命名空间（self-host 默认值）。
	ProviderID string
	TenantID   string
	// DeploymentAllowedSubjectIDs 是部署级允许列表（可空）。
	DeploymentAllowedSubjectIDs []string
	// Viewers 是 viewer 身份目录；为空表示 relay 不支持 selected 分享。
	Viewers []ViewerCredential
	// OwnerSubjectID / OwnerDisplayName 是 self-host relay 验证出的 owner 身份。
	// 它们由 relay 部署配置给出，不是客户端本地编造；默认值明确带 self-hosted 标记。
	OwnerSubjectID   string
	OwnerDisplayName string
	// PublicBaseURL 覆盖分享链接里的 scheme://host（默认用创建请求的 Host）。
	PublicBaseURL string
	// ExpireInterval 是清理过期 share 的扫描间隔；<=0 用 1s。
	ExpireInterval time.Duration
	// Now 让测试钉住时钟。
	Now func() time.Time
}

func (c Config) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c Config) providerID() string {
	if c.ProviderID == "" {
		return "self-hosted"
	}
	return c.ProviderID
}

func (c Config) ownerSubjectID() string {
	if strings.TrimSpace(c.OwnerSubjectID) == "" {
		return "owner"
	}
	return strings.TrimSpace(c.OwnerSubjectID)
}

func (c Config) ownerDisplayName() string {
	if strings.TrimSpace(c.OwnerDisplayName) == "" {
		return "relay owner"
	}
	return strings.TrimSpace(c.OwnerDisplayName)
}

func (c Config) tenantID() string {
	if c.TenantID == "" {
		return "local"
	}
	return c.TenantID
}

func (c Config) expireInterval() time.Duration {
	if c.ExpireInterval > 0 {
		return c.ExpireInterval
	}
	return time.Second
}
