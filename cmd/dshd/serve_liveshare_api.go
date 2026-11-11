package main

// liveshare 13 个官方方法在宿主侧的接线（rc.1 的 12 个 + rc.2 新增的 addSessions）。
//
// 设计（与官方 README §Boundaries 一致）：
//   - relay 是单独部署的服务（本仓提供 cmd/dsh-relayd，也可对接官方 relay 地址）；
//   - 宿主只负责 owner 状态（<home>/liveshare/owner-shares.json）、凭据解析与 13 个
//     方法的映射，13 个方法每次都真的访问 relay，**不编 shareId、不伪造 identity**；
//   - 没配 relay / 没配 owner token 时回结构化错误，而不是假成功。
//
// integrator 接线：在 serve.go 组装 srv 之后调用一次
//
//	liveshareSvc, err := attachOfficialLiveshare(srv, *home)
//	if err != nil { /* 已挂"配置坏了"的 API，日志里记一句即可 */ }
//
// 插件加载器可以拿返回的 *liveshare.Service，用 liveshareSvc.Offers().Register(...)
// 把真实插件声明的端点注册进来（capabilities/setCapabilities 读的就是它）。

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"dshgo/internal/liveshare"
	"dshgo/internal/session"
	"dshgo/internal/transport"
)

// attachOfficialLiveshare 给 srv 挂上 13 个 liveshare 方法并返回 owner 服务。
//
// 返回 error 表示 owner 状态文件/配置读不出来；此时已经挂上一个"每次调用都
// 结构化报错"的 API（不会静默 501，也不会假成功），调用方把错误记日志即可。
func attachOfficialLiveshare(srv *transport.Server, home string) (*liveshare.Service, error) {
	offers := liveshare.NewRegistry()
	svc, err := liveshare.NewService(liveshare.Config{
		Home:       home,
		RelayURL:   strings.TrimSpace(os.Getenv("DSH_LIVESHARE_RELAY")),
		OwnerToken: resolveLiveshareOwnerToken(home),
		Offers:     offers,
	})
	if err != nil {
		srv.AttachOfficialLiveshareAPI(brokenLiveshareAPI(err))
		return nil, err
	}
	srv.AttachOfficialLiveshareAPI(liveshareTransportAPI(svc, home))
	return svc, nil
}

// resolveLiveshareOwnerToken 按优先级找 owner 凭据：
//
//  1. DSH_LIVESHARE_OWNER_TOKEN（自托管部署常用）
//  2. DSH_LIVESHARE_OWNER_TOKEN_FILE（文件内容可以是裸 token 或 {"token":…} JSON）
//  3. <home>/liveshare/owner-credential.json（{"token":…,"relay":…}）
//  4. <home>/liveshare/owner-token（裸 token）
//
// 都找不到就返回空串：identity 会如实回 unverified，需要凭据的方法回结构化错误。
func resolveLiveshareOwnerToken(home string) string {
	if token := strings.TrimSpace(os.Getenv("DSH_LIVESHARE_OWNER_TOKEN")); token != "" {
		return token
	}
	if path := strings.TrimSpace(os.Getenv("DSH_LIVESHARE_OWNER_TOKEN_FILE")); path != "" {
		if token, err := readLiveshareTokenFile(path); err == nil {
			return token
		}
	}
	for _, path := range []string{
		filepath.Join(home, "liveshare", "owner-credential.json"),
		filepath.Join(home, "liveshare", "owner-token"),
	} {
		if token, err := readLiveshareTokenFile(path); err == nil && token != "" {
			return token
		}
	}
	return ""
}

// readLiveshareTokenFile 读 token 文件：JSON `{"token":"…"}` 或一行裸 token 都收。
func readLiveshareTokenFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return "", errors.New("token 文件是空的")
	}
	if strings.HasPrefix(trimmed, "{") {
		var doc struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
			return "", fmt.Errorf("解析 %s 失败: %w", path, err)
		}
		return strings.TrimSpace(doc.Token), nil
	}
	return trimmed, nil
}

// brokenLiveshareAPI 是 owner 状态损坏时的实现：每次调用都返回带原因的 500。
// 它绝不返回空结果或成功——"状态文件坏了"不能表现成"没有分享"。
func brokenLiveshareAPI(cause error) transport.OfficialLiveshareAPI {
	fail := func() error {
		return fmt.Errorf("liveshare owner 状态不可用（%v）；检查 <home>/liveshare/owner-shares.json 是否是合法 JSON", cause)
	}
	return transport.OfficialLiveshareAPI{
		Create: func(transport.OfficialLiveshareCreateRequest) (transport.OfficialLiveshareCreatedShare, error) {
			return transport.OfficialLiveshareCreatedShare{}, fail()
		},
		AddSessions: func(transport.OfficialLiveshareAddSessionsRequest) (transport.OfficialLiveshareShareSessions, error) {
			return transport.OfficialLiveshareShareSessions{}, fail()
		},
		List: func() (transport.OfficialLiveshareShareList, error) {
			return transport.OfficialLiveshareShareList{}, fail()
		},
		Stop: func(transport.OfficialLiveshareStopRequest) (transport.OfficialLiveshareStopResult, error) {
			return transport.OfficialLiveshareStopResult{}, fail()
		},
		UpdatePermissions: func(transport.OfficialLiveshareUpdatePermissionsRequest) (transport.OfficialLivesharePermissions, error) {
			return transport.OfficialLivesharePermissions{}, fail()
		},
		SetCapabilities: func(transport.OfficialLiveshareSetCapabilitiesRequest) (transport.OfficialLiveshareShareCapabilities, error) {
			return transport.OfficialLiveshareShareCapabilities{}, fail()
		},
		Capabilities: func() (transport.OfficialLiveshareCapabilityOffers, error) {
			return transport.OfficialLiveshareCapabilityOffers{}, fail()
		},
		Relay: func() (transport.OfficialLiveshareRelayState, error) {
			return transport.OfficialLiveshareRelayState{}, fail()
		},
		SetRelay: func(transport.OfficialLiveshareSetRelayRequest) (transport.OfficialLiveshareRelayState, error) {
			return transport.OfficialLiveshareRelayState{}, fail()
		},
		Identity: func(transport.OfficialLiveshareIdentityRequest) (transport.OfficialLiveshareIdentityStatus, error) {
			return transport.OfficialLiveshareIdentityStatus{}, fail()
		},
		Audit: func(transport.OfficialLiveshareAuditRequest) (transport.OfficialLiveshareAudit, error) {
			return transport.OfficialLiveshareAudit{}, fail()
		},
		TestRelay: func() (transport.OfficialLiveshareConnectivity, error) {
			return transport.OfficialLiveshareConnectivity{}, fail()
		},
		TestPublish: func(transport.OfficialLivesharePublishRequest) (transport.OfficialLivesharePublishView, error) {
			return transport.OfficialLivesharePublishView{}, fail()
		},
	}
}

// liveshareTransportAPI 把 owner 服务映射成 transport 的 13 个回调。
func liveshareTransportAPI(svc *liveshare.Service, home string) transport.OfficialLiveshareAPI {
	host := &liveshareHost{svc: svc, home: home, sessions: newLiveshareSessionStore(home)}
	return transport.OfficialLiveshareAPI{
		Create: func(req transport.OfficialLiveshareCreateRequest) (transport.OfficialLiveshareCreatedShare, error) {
			created, err := svc.Create(liveshare.CreateRequest{
				SessionID:         req.SessionID,
				SessionTitle:      req.SessionTitle,
				AccessMode:        req.AccessMode,
				TTLMinutes:        req.TTLMinutes,
				AllowedSubjectIDs: req.AllowedSubjectIDs,
				AllowsFork:        req.AllowsFork,
				SessionMode:       req.SessionMode,
			})
			if err != nil {
				return transport.OfficialLiveshareCreatedShare{}, mapLiveshareError(err)
			}
			// 新建 share 只有主会话；顺手清掉同 id 的旧侧存（shareId 重复时不能
			// 让上一条分享留下的会话集合挂到新分享上）。
			host.sessions.Forget(created.ShareID)
			return transport.OfficialLiveshareCreatedShare{
				Link: created.Link, ShareID: created.ShareID, SessionID: created.SessionID,
				SessionIDs:  []string{created.SessionID},
				SessionMode: created.SessionMode, Access: toTransportAccess(created.Access),
			}, nil
		},
		AddSessions: func(req transport.OfficialLiveshareAddSessionsRequest) (transport.OfficialLiveshareShareSessions, error) {
			return host.AddSessions(req)
		},
		List: func() (transport.OfficialLiveshareShareList, error) {
			list, err := svc.List()
			if err != nil {
				return transport.OfficialLiveshareShareList{}, mapLiveshareError(err)
			}
			// 侧存读一次（不是每条 share 读一次）：列表要与 addSessions 的返回
			// 用同一份会话集合，否则"加了但 list 看不到"。
			records, err := readLiveshareOwnerShares(home)
			if err != nil {
				return transport.OfficialLiveshareShareList{}, fmt.Errorf("liveshare owner 状态不可读：%w", err)
			}
			byShare := make(map[string]liveshareOwnerShare, len(records))
			for _, record := range records {
				byShare[record.ShareID] = record
			}
			out := transport.OfficialLiveshareShareList{Shares: make([]transport.OfficialLiveshareShareView, 0, len(list.Shares))}
			for _, share := range list.Shares {
				view := transport.OfficialLiveshareShareView{
					ShareID:            share.ShareID,
					SessionID:          share.SessionID,
					SessionIDs:         host.sessions.resolve(share.ShareID, share.SessionID, byShare[share.ShareID].SessionIDs),
					SessionTitle:       share.SessionTitle,
					Link:               share.Link,
					CreatedAt:          formatLiveshareTime(share.CreatedAt),
					AllowsFork:         share.AllowsFork,
					SessionMode:        share.SessionMode,
					ViewerCapabilities: toTransportCapabilities(share.ViewerCapabilities),
					Online:             share.Online,
				}
				if share.ExpiresAt != nil {
					view.ExpiresAt = formatLiveshareTime(*share.ExpiresAt)
				}
				if share.Access != nil {
					access := toTransportAccess(*share.Access)
					view.Access = &access
				}
				out.Shares = append(out.Shares, view)
			}
			return out, nil
		},
		Stop: func(req transport.OfficialLiveshareStopRequest) (transport.OfficialLiveshareStopResult, error) {
			result, err := svc.Stop(req.ShareID)
			if err != nil {
				return transport.OfficialLiveshareStopResult{}, mapLiveshareError(err)
			}
			// share 已经停了：它的会话集合没有意义了，一并清掉（清不掉也不影响
			// 正确性：list 里没有这条 share，侧存条目永远读不到）。
			host.sessions.Forget(req.ShareID)
			return transport.OfficialLiveshareStopResult{OK: result.OK, Relay: result.Relay}, nil
		},
		UpdatePermissions: func(req transport.OfficialLiveshareUpdatePermissionsRequest) (transport.OfficialLivesharePermissions, error) {
			result, err := svc.UpdatePermissions(liveshare.UpdatePermissionsRequest{
				ShareID:     req.ShareID,
				SessionMode: req.SessionMode,
				AllowsFork:  req.AllowsFork,
				TTLMinutes:  req.TTLMinutes,
				ClearTTL:    req.TTLNull,
			})
			if err != nil {
				return transport.OfficialLivesharePermissions{}, mapLiveshareError(err)
			}
			out := transport.OfficialLivesharePermissions{
				ShareID: result.ShareID, SessionMode: result.SessionMode, AllowsFork: result.AllowsFork,
			}
			if result.ExpiresAt != nil {
				out.ExpiresAt = formatLiveshareTime(*result.ExpiresAt)
			}
			return out, nil
		},
		SetCapabilities: func(req transport.OfficialLiveshareSetCapabilitiesRequest) (transport.OfficialLiveshareShareCapabilities, error) {
			result, err := svc.SetCapabilities(liveshare.SetCapabilitiesRequest{
				ShareID:      req.ShareID,
				Capabilities: fromTransportCapabilities(req.Capabilities),
			})
			if err != nil {
				return transport.OfficialLiveshareShareCapabilities{}, mapLiveshareError(err)
			}
			return transport.OfficialLiveshareShareCapabilities{
				ShareID: result.ShareID, ViewerCapabilities: toTransportCapabilities(result.ViewerCapabilities),
			}, nil
		},
		Capabilities: func() (transport.OfficialLiveshareCapabilityOffers, error) {
			offers := svc.Capabilities()
			return transport.OfficialLiveshareCapabilityOffers{
				Capabilities: toTransportCapabilities(offers.Capabilities),
			}, nil
		},
		Relay: func() (transport.OfficialLiveshareRelayState, error) {
			state, err := svc.Relay()
			if err != nil {
				return transport.OfficialLiveshareRelayState{}, mapLiveshareError(err)
			}
			return toTransportRelayState(state), nil
		},
		SetRelay: func(req transport.OfficialLiveshareSetRelayRequest) (transport.OfficialLiveshareRelayState, error) {
			state, err := svc.SetRelay(req.RelayURL)
			if err != nil {
				return transport.OfficialLiveshareRelayState{}, mapLiveshareError(err)
			}
			return toTransportRelayState(state), nil
		},
		Identity: func(req transport.OfficialLiveshareIdentityRequest) (transport.OfficialLiveshareIdentityStatus, error) {
			status, err := svc.Identity(liveshare.IdentityRequest{Relay: req.Relay})
			if err != nil {
				return transport.OfficialLiveshareIdentityStatus{}, mapLiveshareError(err)
			}
			out := transport.OfficialLiveshareIdentityStatus{
				State: status.State, Relay: status.Relay,
				DisplayName: status.DisplayName, SubjectID: status.SubjectID,
				AvatarURL: status.AvatarURL, Renewed: status.Renewed,
			}
			if status.ExpiresAt != nil {
				out.ExpiresAt = formatLiveshareTime(*status.ExpiresAt)
			}
			if status.Claim != nil {
				out.Claim = &struct {
					Claimed int `json:"claimed"`
					Skipped int `json:"skipped"`
				}{Claimed: status.Claim.Claimed, Skipped: status.Claim.Skipped}
			}
			return out, nil
		},
		Audit: func(req transport.OfficialLiveshareAuditRequest) (transport.OfficialLiveshareAudit, error) {
			audit, err := svc.Audit(req.ShareID)
			if err != nil {
				return transport.OfficialLiveshareAudit{}, mapLiveshareError(err)
			}
			out := transport.OfficialLiveshareAudit{Events: make([]transport.OfficialLiveshareAuditEntry, 0, len(audit.Events))}
			for _, event := range audit.Events {
				out.Events = append(out.Events, transport.OfficialLiveshareAuditEntry{
					ID: event.ID, Action: event.Action,
					CreatedAt: formatLiveshareTime(event.CreatedAt),
					Actor:     event.Actor, Detail: event.Detail,
				})
			}
			return out, nil
		},
		TestRelay: func() (transport.OfficialLiveshareConnectivity, error) {
			view, err := svc.TestRelay()
			if err != nil {
				return transport.OfficialLiveshareConnectivity{}, mapLiveshareError(err)
			}
			return transport.OfficialLiveshareConnectivity{
				OK: view.OK, HTTPReachable: view.HTTPReachable, Blocking: view.Blocking, Error: view.Error,
			}, nil
		},
		TestPublish: func(req transport.OfficialLivesharePublishRequest) (transport.OfficialLivesharePublishView, error) {
			view, err := svc.TestPublish(req.SessionID)
			if err != nil {
				return transport.OfficialLivesharePublishView{}, mapLiveshareError(err)
			}
			return transport.OfficialLivesharePublishView{OK: view.OK, Blocking: view.Blocking, Error: view.Error}, nil
		},
	}
}

func toTransportAccess(access liveshare.Access) transport.OfficialLiveshareAccess {
	selected := make([]transport.OfficialLiveshareSelectedIdentity, 0, len(access.Selected))
	for _, identity := range access.Selected {
		selected = append(selected, transport.OfficialLiveshareSelectedIdentity{
			ProviderID: identity.ProviderID, TenantID: identity.TenantID,
			SubjectID: identity.SubjectID, IdentityKey: identity.IdentityKey,
		})
	}
	return transport.OfficialLiveshareAccess{
		Mode:                          access.Mode,
		Selected:                      selected,
		DeploymentAllowedSubjectIDs:   append([]string{}, access.DeploymentAllowedSubjectIDs...),
		ShareAllowedSubjectIDs:        append([]string{}, access.ShareAllowedSubjectIDs...),
		BroadenedByDeploymentSubjects: access.BroadenedByDeploymentSubjects,
		IdentityKeyACL:                true,
	}
}

func toTransportCapabilities(caps []liveshare.Capability) []transport.OfficialLiveshareCapability {
	out := make([]transport.OfficialLiveshareCapability, 0, len(caps))
	for _, capability := range caps {
		out = append(out, transport.OfficialLiveshareCapability{
			Plugin:   capability.Plugin,
			Readable: append([]string{}, capability.Readable...),
			Writable: append([]string{}, capability.Writable...),
		})
	}
	return out
}

func fromTransportCapabilities(caps []transport.OfficialLiveshareCapability) []liveshare.Capability {
	out := make([]liveshare.Capability, 0, len(caps))
	for _, capability := range caps {
		out = append(out, liveshare.Capability{
			Plugin:   capability.Plugin,
			Readable: append([]string{}, capability.Readable...),
			Writable: append([]string{}, capability.Writable...),
		})
	}
	return out
}

func toTransportRelayState(state liveshare.RelayState) transport.OfficialLiveshareRelayState {
	return transport.OfficialLiveshareRelayState{
		Configured: state.Configured, Override: state.Override, Effective: state.Effective,
	}
}

// formatLiveshareTime 统一输出 RFC3339 UTC（官方 zod 的 string 时间）。
func formatLiveshareTime(ts time.Time) string { return ts.UTC().Format(time.RFC3339) }

// mapLiveshareError 把 owner 服务错误翻译成官方四字段错误。
//
// 分类原则：
//   - 参数/配置可修正（bad_request）：400，提示怎么改；
//   - share 不存在：404 not_found；
//   - 凭据缺失/被拒、relay 内部错误：500 internal（retryable，宿主配置问题不是客户端参数错）；
//   - relay 不可达：500 internal + "不可达"正文（可重试）。
func mapLiveshareError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, liveshare.ErrShareNotFound):
		return transport.OfficialLiveshareNotFound("%v", err)
	case errors.Is(err, liveshare.ErrRelayNotConfigured):
		return fmt.Errorf("%w：%v（先在 Share 设置里保存 relay 地址，或部署时设置 DSH_LIVESHARE_RELAY）", transport.ErrOfficialBadInput, err)
	case errors.Is(err, liveshare.ErrNoOwnerToken):
		return fmt.Errorf("liveshare owner 凭据缺失（不是客户端参数错误）：%v；把 relay 的 owner-token.json 配到 DSH_LIVESHARE_OWNER_TOKEN/DSH_LIVESHARE_OWNER_TOKEN_FILE，或放进 <home>/liveshare/owner-credential.json", err)
	case liveshare.IsBadRequest(err):
		return fmt.Errorf("%w：%v", transport.ErrOfficialBadInput, err)
	case liveshare.IsUnauthorized(err), liveshare.IsForbidden(err):
		return fmt.Errorf("relay 拒绝 owner 凭据（不是客户端参数错误）：%v；检查 DSH_LIVESHARE_OWNER_TOKEN 与 relay 的 owner-token.json 是否一致", err)
	case liveshare.RelayStatusCode(err) == 400:
		return fmt.Errorf("%w：relay 拒绝了请求：%v", transport.ErrOfficialBadInput, err)
	case liveshare.IsUnreachable(err):
		return fmt.Errorf("liveshare relay 不可达：%v；启动 dsh-relayd 或修正 relay 地址后重试", err)
	default:
		return err
	}
}


// ---------------------------------------------------------------------------
// addSessions：一条已发布 share 的"源会话集合"
// ---------------------------------------------------------------------------
//
// # 官方语义（rc.2 新增，`share-remote.ts:66` → `owner-share-operations.ts:408-449`）
//
//	顺序：validate → 解析 share → 算出真正新增的会话 → 持久化 → 装访问层 → accessChanged
//
// 三条硬规则：
//   - **不发 relay 请求**：relay 上的注册只带主会话，加会话不改注册（官方注释：
//     "The Relay registration carries the primary source only and does not change,
//     so no Relay call is made"）；
//   - **幂等**：已经在 share 里的 id 直接忽略，重复调用返回同一份集合；
//   - **同一工作区**：所有源会话必须与主会话在同一个 workspace（`assertOneWorkspace`，
//     否则 `liveshareShares/session-workspace-unknown` / `session-workspace-mismatch`）。
//
// # 本仓的落点
//
// 官方把 `sessionIds` 存进 owner-shares.json 的那条 share 记录（`OwnerShare.sessionIds`），
// `sessionId` 只是第一项的派生视图。本仓的 `internal/liveshare` 目前只建模单个
// `SessionID`，而它不在本任务的写作用域里，所以这里用**同目录的伴随状态**
// `<home>/liveshare/owner-share-sessions.json` 存"每条 share 服务哪些会话"：
//
//	{"version":1,"shares":{"share-abc":["s1","s2"]}}
//
// 这份状态是真实落盘的 owner 状态（原子写、0600），list / addSessions / 重启后的
// 读路径都走它。等 `StoredShare` 长出 `SessionIDs`（一行 JSON tag + Create 里填
// 初值），把 resolve 的取值顺序调成"记录里的 sessionIds 优先"、删掉本文件即可，
// 语义完全一致（resolve 已经认记录里的 sessionIds）。

const (
	// liveshareSessionsFileName 是与 owner-shares.json 同目录的会话集合状态。
	liveshareSessionsFileName = "owner-share-sessions.json"
	// liveshareSessionsVersion 是这份状态的 schema 版本（与 owner-shares.json 的
	// version=1 独立演进：它是本仓的伴随状态，不是官方文件）。
	liveshareSessionsVersion = 1
	// liveshareOwnerStateFileName 是 internal/liveshare 的 owner 状态文件名
	// （`internal/liveshare/state.go:12` 的 stateFile，那里是包内常量，这里只读）。
	liveshareOwnerStateFileName = "owner-shares.json"
	// liveshareOwnerStateVersion 是 owner-shares.json 当前唯一认的版本
	// （`internal/liveshare/state.go:13` 的 stateVersion）。
	liveshareOwnerStateVersion = 1
)

// liveshareOwnerShare 是 owner-shares.json 里本任务需要的那几个字段。
//
// 只映射用得到的字段：多映一个就多一处"状态文件加了字段我们却没跟上"的面。
// `sessionIds` 是本仓将来把多会话折进记录时的形状（目前写不进去，读得到就用）。
type liveshareOwnerShare struct {
	ShareID    string   `json:"shareId"`
	SessionID  string   `json:"sessionId"`
	SessionIDs []string `json:"sessionIds,omitempty"`
	Relay      string   `json:"relay,omitempty"`
}

type liveshareOwnerStateDoc struct {
	Version int                  `json:"version"`
	Shares  []liveshareOwnerShare `json:"shares"`
}

// readLiveshareOwnerShares 读 owner-shares.json 里的分享记录。
//
// 文件不存在 = 还没有任何分享（空列表，不是错误）；文件坏了 = 错误（不能把
// "状态坏了"表现成"没有分享"，那会让 addSessions 静默失败）。
func readLiveshareOwnerShares(home string) ([]liveshareOwnerShare, error) {
	path := filepath.Join(home, "liveshare", liveshareOwnerStateFileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("读 %s 失败: %w", path, err)
	}
	var doc liveshareOwnerStateDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", path, err)
	}
	if doc.Version != liveshareOwnerStateVersion {
		return nil, fmt.Errorf("%s 的 version=%d，本二进制只认 %d", path, doc.Version, liveshareOwnerStateVersion)
	}
	return doc.Shares, nil
}

// liveshareSessionStore 是"每条 share 服务哪些源会话"的持久状态。
//
// 单进程内用互斥锁串行化（读-改-写是一个整体）；跨进程的并发写由 owner 状态
// 文件本身的前提保证——一个 home 只应该被一个 dshd 进程持有。
type liveshareSessionStore struct {
	path string
	mu   sync.Mutex
}

func newLiveshareSessionStore(home string) *liveshareSessionStore {
	return &liveshareSessionStore{path: filepath.Join(home, "liveshare", liveshareSessionsFileName)}
}

type liveshareSessionsDoc struct {
	Version int                 `json:"version"`
	Shares  map[string][]string `json:"shares"`
}

func (s *liveshareSessionStore) load() (map[string][]string, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string][]string{}, nil
		}
		return nil, fmt.Errorf("读 %s 失败: %w", s.path, err)
	}
	var doc liveshareSessionsDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", s.path, err)
	}
	if doc.Version != liveshareSessionsVersion {
		return nil, fmt.Errorf("%s 的 version=%d，本二进制只认 %d", s.path, doc.Version, liveshareSessionsVersion)
	}
	if doc.Shares == nil {
		return map[string][]string{}, nil
	}
	return doc.Shares, nil
}

// save 原子写（临时文件 + fsync + rename，0600），与 owner-shares.json 同规矩。
func (s *liveshareSessionStore) save(shares map[string][]string) error {
	if shares == nil {
		shares = map[string][]string{}
	}
	body, err := json.MarshalIndent(liveshareSessionsDoc{Version: liveshareSessionsVersion, Shares: shares}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, liveshareSessionsFileName+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(body, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path)
}

// resolve 给出"这条 share 现在服务哪些会话"，主会话永远在第一位。
//
// 取值顺序（先到先用，后面的只是兜底）：
//  1. 侧存里记的集合（addSessions 写的就是它）；
//  2. owner 记录里的 `sessionIds`（本仓将来把多会话折进记录时用）；
//  3. 只有主会话（rc.1 的老记录 / 从没 addSessions 过）。
//
// 读不了侧存时退回 2/3 而不是报错：list 是读路径，读不了伴随状态不该让整份
// 列表变成 500——但 addSessions（写路径）读不了侧存必须报错，否则会丢状态。
func (s *liveshareSessionStore) resolve(shareID, primary string, recordIDs []string) []string {
	primary = strings.TrimSpace(primary)
	if primary == "" {
		// 记录本身坏了：不编 id，交给调用方决定（transport 的结果校验会拦住）。
		return []string{}
	}
	s.mu.Lock()
	shares, err := s.load()
	s.mu.Unlock()
	if err != nil {
		shares = map[string][]string{}
	}
	return normalizeLiveshareSessionIDs(shares[shareID], recordIDs, primary)
}

// forget 删掉一条 share 的会话集合（share 停了 / 重建了）。
func (s *liveshareSessionStore) forget(shareID string) {
	if strings.TrimSpace(shareID) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	shares, err := s.load()
	if err != nil || len(shares) == 0 {
		return
	}
	if _, ok := shares[shareID]; !ok {
		return
	}
	delete(shares, shareID)
	_ = s.save(shares)
}

// set 写入一条 share 的完整会话集合（调用方已算好，含主会话）。
func (s *liveshareSessionStore) set(shareID string, ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	shares, err := s.load()
	if err != nil {
		return err
	}
	shares[shareID] = append([]string{}, ids...)
	return s.save(shares)
}

// Forget 是导出给同一包内其它接线点的薄封装（store 的字段保持小写）。
func (s *liveshareSessionStore) Forget(shareID string) { s.forget(shareID) }

// normalizeLiveshareSessionIDs 把候选集合归一成"主会话在第一位、去重、非空"。
func normalizeLiveshareSessionIDs(sideStore, recordIDs []string, primary string) []string {
	out := make([]string, 0, 1+len(sideStore)+len(recordIDs))
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		for _, seen := range out {
			if seen == id {
				return
			}
		}
		out = append(out, id)
	}
	add(primary)
	for _, id := range sideStore {
		add(id)
	}
	for _, id := range recordIDs {
		add(id)
	}
	return out
}

// liveshareHost 是 addSessions 需要的宿主侧状态（owner 服务 + home 布局 + 侧存）。
type liveshareHost struct {
	svc      *liveshare.Service
	home     string
	sessions *liveshareSessionStore
}

// AddSessions 把源会话加进一条**已发布**的 share，返回它现在服务的全部会话。
//
// 与官方 `owner-share-operations.ts:408-449` 逐步对齐：
//  1. 参数（shareId / sessionIds 形状）已由 transport 层校验并去重；
//  2. share 必须在本宿主的 owner 记录里，否则 404 `not_found`；
//  3. 已在集合里的 id 忽略（幂等），全都已存在时不写盘直接返回；
//  4. 跨 owner/relay 的 share 一律 403（官方把它归 owner-only，
//     `endpoint-classification.ts:241`；本仓 owner 边界的真实落点是 relay 归属）；
//  5. 新增的每个会话必须真实存在（本 home 的 <home>/sessions 下），否则 404
//     `no_such_session`；且必须与主会话在同一个工作区（同一 projectKey），
//     否则 400 —— 对应官方 workspace 两条错误；
//  6. 落盘（原子写）后才回成功；写失败回 500 且状态不变（内存里没有缓存，
//     所以失败天然"没变"）。
func (h *liveshareHost) AddSessions(req transport.OfficialLiveshareAddSessionsRequest) (transport.OfficialLiveshareShareSessions, error) {
	shareID := strings.TrimSpace(req.ShareID)
	if shareID == "" {
		return transport.OfficialLiveshareShareSessions{}, transport.OfficialLiveshareBadRequest("shareId 不能为空")
	}
	records, err := readLiveshareOwnerShares(h.home)
	if err != nil {
		return transport.OfficialLiveshareShareSessions{}, fmt.Errorf("liveshare owner 状态不可读：%w", err)
	}
	record, ok := findLiveshareOwnerShare(records, shareID)
	if !ok {
		return transport.OfficialLiveshareShareSessions{}, transport.OfficialLiveshareNotFound(
			"shareId %q 不在本宿主的 owner 记录里（liveshareShares.list 看还有哪些 share；未发布的 share 不能加会话）", shareID)
	}
	primary := strings.TrimSpace(record.SessionID)
	if primary == "" {
		return transport.OfficialLiveshareShareSessions{}, transport.OfficialLiveshareBadRequest(
			"owner 记录里 share %q 的主会话是空的（状态坏了）；先 liveshareShares.stop 再重建这条分享", shareID)
	}
	current := h.sessions.resolve(shareID, primary, record.SessionIDs)

	// 4) owner 边界：这条 share 是另一个 relay（另一个 owner 身份）发布的，不许改。
	if effective := strings.TrimSpace(h.svc.EffectiveRelay()); record.Relay != "" && effective != "" && record.Relay != effective {
		return transport.OfficialLiveshareShareSessions{}, transport.OfficialLiveshareForbidden(
			"share %q 由 relay %s 的 owner 发布，本宿主当前 relay 是 %s：跨 owner 的分享不允许在本宿主改（越权）；"+
				"要改就先用 liveshareShares.setRelay 切回该地址，或在发布它的那台宿主上操作",
			shareID, record.Relay, effective)
	}

	additions := make([]string, 0, len(req.SessionIDs))
	for _, id := range req.SessionIDs {
		id = strings.TrimSpace(id)
		if id == "" || containsLiveshareID(current, id) || containsLiveshareID(additions, id) {
			continue
		}
		additions = append(additions, id)
	}
	// 3) 全部已经在 share 里：幂等返回当前集合，不碰磁盘。
	if len(additions) == 0 {
		return transport.OfficialLiveshareShareSessions{ShareID: shareID, SessionIDs: current}, nil
	}

	// 5) 会话存在 + 同一工作区。
	primaryProject, err := liveshareSessionProject(h.home, primary)
	if err != nil {
		return transport.OfficialLiveshareShareSessions{}, transport.OfficialLiveshareBadRequest(
			"share %q 的主会话 %q 在本 home 里找不到，判定不了工作区：%v；先确认 --home 指向的目录（或 stop 这条分享重建）",
			shareID, primary, err)
	}
	for _, id := range additions {
		project, err := liveshareSessionProject(h.home, id)
		if err != nil {
			return transport.OfficialLiveshareShareSessions{}, transport.OfficialLiveshareSessionNotFound(
				"会话 %q 不存在（本 home 的 sessions 目录里没有它）：%v；先 session.list 拿真实 id", id, err)
		}
		if project != primaryProject {
			return transport.OfficialLiveshareShareSessions{}, transport.OfficialLiveshareBadRequest(
				"会话 %q 不在 share 主会话 %q 所在的工作区（%s ≠ %s）：官方 addSessions 只允许加入同一 workspace 的会话；"+
					"先在该工作区里开/续这个会话，或为它单独建一条分享",
				id, primary, project, primaryProject)
		}
	}

	// 6) 落盘。
	next := append(append([]string{}, current...), additions...)
	if err := h.sessions.set(shareID, next); err != nil {
		return transport.OfficialLiveshareShareSessions{}, fmt.Errorf("写 %s 的 liveshare 会话集合失败：%w", h.home, err)
	}
	return transport.OfficialLiveshareShareSessions{ShareID: shareID, SessionIDs: next}, nil
}

func findLiveshareOwnerShare(records []liveshareOwnerShare, shareID string) (liveshareOwnerShare, bool) {
	for _, record := range records {
		if record.ShareID == shareID {
			return record, true
		}
	}
	return liveshareOwnerShare{}, false
}

func containsLiveshareID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// liveshareSessionProject 返回会话所在的项目目录（v4 布局下的 projectKey）。
//
// 会话文件布局是 `<home>/sessions/<projectKey>/<encoded id>/session.v4.jsonl.zstd`
// （`internal/session/history.go:251-257`），所以"同一工作区"就是同一个
// projectKey——这正是官方 workspace 概念在本仓的对应物。
func liveshareSessionProject(home, sessionID string) (string, error) {
	path, err := session.FindByID(home, sessionID)
	if err != nil {
		return "", err
	}
	return filepath.Base(filepath.Dir(filepath.Dir(path))), nil
}
