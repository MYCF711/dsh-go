package pluginmgr

// registry 失败的**结构化**原因。
//
// 为什么需要它：官方 `registryPlan()` 决定"什么时候轮到下一个源"，判据是失败种类
// （`NEXT_REGISTRY_KINDS = {network, timeout, not-found, no-matching-version}`，
// dsh-plugin-manager/lib/index.js:1097-1103）。宿主此前把 registry 失败一律当成
// 一段字符串，没法回答"这次失败换个源能不能好"——于是只能单源、失败即 503。
//
// 边界（不许扩散）：只有"另一个 registry 可能给出不同答案"的失败才换源。
// 包存在但内容坏了（不是 JSON、缺 tarball、sha1 不匹配）不换源：换个源拿到
// 同一份坏数据只是把错误读过一遍。

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
)

// registry 失败种类（逐字对齐官方 NEXT_REGISTRY_KINDS + 兜底 other）。
const (
	RegistryKindNetwork           = "network"
	RegistryKindTimeout           = "timeout"
	RegistryKindNotFound          = "not-found"
	RegistryKindNoMatchingVersion = "no-matching-version"
	RegistryKindOther             = "other"
)

// RegistryError 是一次 registry 请求失败的结构化形态。
type RegistryError struct {
	Kind string
	Err  error
}

func (e *RegistryError) Error() string {
	if e == nil || e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e *RegistryError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// RegistryFailureKind 返回失败种类；不是 RegistryError 的失败一律 "other"
// （未知失败不换源：宁可把真实错误抛给调用方，也不去猜）。
func RegistryFailureKind(err error) string {
	if err == nil {
		return ""
	}
	var registryErr *RegistryError
	if errors.As(err, &registryErr) && registryErr.Kind != "" {
		return registryErr.Kind
	}
	return RegistryKindOther
}

// NextRegistryAfterProblem 报告官方语义下"这个失败要不要问下一个源"。
func NextRegistryAfterProblem(kind string) bool {
	switch kind {
	case RegistryKindNetwork, RegistryKindTimeout, RegistryKindNotFound, RegistryKindNoMatchingVersion:
		return true
	}
	return false
}

// registryTransportError 给传输层失败定性：超时 / 网络 / 其它。
func registryTransportError(err error) *RegistryError {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return &RegistryError{Kind: RegistryKindTimeout, Err: err}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &RegistryError{Kind: RegistryKindTimeout, Err: err}
	}
	return &RegistryError{Kind: RegistryKindNetwork, Err: err}
}

// registryStatusError 给 HTTP 状态码定性。
//
// 404 = not-found（另一个源可能有这个包）；429/5xx = network（重试或换源）；
// 其余 4xx（401/403/400）不换源：换个源大概率同样是凭据/参数问题。
func registryStatusError(status int, err error) *RegistryError {
	kind := RegistryKindOther
	switch {
	case status == http.StatusNotFound:
		kind = RegistryKindNotFound
	case status == http.StatusTooManyRequests || status >= 500:
		kind = RegistryKindNetwork
	}
	return &RegistryError{Kind: kind, Err: err}
}
