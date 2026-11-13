package account

// 账号令牌的**读取**与**作废**两件事，对应官方 `dsh-deepseek-account` 平台的
// 两个公开入口（`src/index.ts`）：
//
//	resolveToken(url)      // :454-473 —— 只给"目的地允许账号认证"的推理请求发令牌
//	rejectToken(token)     // :377-382 —— 只清掉"仍然等于当前登录"的那一份令牌
//
// 为什么两者必须成对：模型请求 401 时若不作废令牌，下一次重试会拿着同一份坏令牌
// 再打一遍（用户看到的是"一直失败但账号页面显示已登录"）；反过来，若 401 时无脑
// 清空凭据，一次**过期请求**就能把用户刚做完的新登录一起清掉——官方的判据是
// "传入的 token 仍等于当前存储的那份"（`:379`），这里逐字照做。

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// DefaultInferenceOrigin 是官方 `inferenceOrigin` 的默认值
// （`dsh-deepseek-account/src/index.ts:78`）。
const DefaultInferenceOrigin = "https://api.deepseek.com"

// resolveInferenceOrigin 校验并归一化 `inferenceOrigin`。
//
// 判据逐条照官方 `:141-147`：必须是合法 URL、scheme 只能是 http/https、无凭据、
// `pathname` 只能是 `/`（空等价于 `/`）、无 search、无 hash。空串 = 官方默认值。
func resolveInferenceOrigin(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return DefaultInferenceOrigin, nil
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("account: inferenceOrigin 不是合法 URL: %q: %w", trimmed, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("account: inferenceOrigin 必须是 http(s) origin，收到 %q", parsed.Scheme)
	}
	if parsed.Host == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("account: inferenceOrigin 必须是**不带凭据/路径/查询/片段的** origin，收到 %q", trimmed)
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

// InferenceOrigin 返回账号令牌允许送达的推理 origin。
//
// 调用方（模型请求层）必须拿它做目的地栅栏：拿不到这个值就不该发令牌。
func (s *Service) InferenceOrigin() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inferenceOrigin
}

// AccessToken 返回当前可用的账号令牌（官方 `resolveToken` 的宿主读取半边）。
//
// 三条"不返回令牌"的情形都是**真实状态**，不是保守估计：
//   - 未登录（Status 不是 credential-stored）；
//   - 令牌为空；
//   - 本地租约已过期（这里顺带推进一次过期，与 getState/watch 同一条路径）。
//
// 目的地栅栏（origin 比较）不在这里做：那是请求层的事，`InferenceOrigin()` 已经有了。
func (s *Service) AccessToken() (string, bool) {
	if s == nil {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.expireLocked(time.Now()); err != nil {
		return "", false
	}
	if s.doc.Status != StatusCredentialStored || s.doc.Token == "" {
		return "", false
	}
	return s.doc.Token, true
}

// RejectToken 作废一份被推理请求拒绝的令牌（官方 `rejectToken`，`:377-382`）。
//
// 判据：**只有传入的 token 仍等于当前存储的那一份**才清。否则是纯 no-op——
// 那说明这份令牌已经被更新的登录取代了，清掉它等于删掉用户刚做的登录。
//
// 清掉之后与 `expireLocked` 的收尾保持一致：状态回 signed-out、令牌清空、
// 登录尝试标记过期，并发一次 `credentials-changed`（官方 `:394-399` 的
// signed-out + snapshot 通知）。
func (s *Service) RejectToken(token string) error {
	if s == nil {
		return fmt.Errorf("account: RejectToken 需要账号服务")
	}
	if strings.TrimSpace(token) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.doc.Token != token {
		return nil
	}
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
	s.notifyKindLocked(UpdateCredentialsChanged)
	for ch := range s.expiry {
		select {
		case ch <- "session-expired":
		default:
		}
	}
	return nil
}
