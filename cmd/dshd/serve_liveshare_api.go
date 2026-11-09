package main

// liveshare 12 个官方方法在宿主侧的接线。
//
// 设计（与官方 README §Boundaries 一致）：
//   - relay 是单独部署的服务（本仓提供 cmd/dsh-relayd，也可对接官方 relay 地址）；
//   - 宿主只负责 owner 状态（<home>/liveshare/owner-shares.json）、凭据解析与 12 个
//     方法的映射，12 个方法每次都真的访问 relay，**不编 shareId、不伪造 identity**；
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
	"time"

	"dshgo/internal/liveshare"
	"dshgo/internal/transport"
)

// attachOfficialLiveshare 给 srv 挂上 12 个 liveshare 方法并返回 owner 服务。
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
	srv.AttachOfficialLiveshareAPI(liveshareTransportAPI(svc))
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

// liveshareTransportAPI 把 owner 服务映射成 transport 的 12 个回调。
func liveshareTransportAPI(svc *liveshare.Service) transport.OfficialLiveshareAPI {
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
			return transport.OfficialLiveshareCreatedShare{
				Link: created.Link, ShareID: created.ShareID, SessionID: created.SessionID,
				SessionMode: created.SessionMode, Access: toTransportAccess(created.Access),
			}, nil
		},
		List: func() (transport.OfficialLiveshareShareList, error) {
			list, err := svc.List()
			if err != nil {
				return transport.OfficialLiveshareShareList{}, mapLiveshareError(err)
			}
			out := transport.OfficialLiveshareShareList{Shares: make([]transport.OfficialLiveshareShareView, 0, len(list.Shares))}
			for _, share := range list.Shares {
				view := transport.OfficialLiveshareShareView{
					ShareID:            share.ShareID,
					SessionID:          share.SessionID,
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
