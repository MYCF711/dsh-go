// Package forge 的最小 GitHub GraphQL 客户端（rc.2 的 review threads / viewed
// 标记只能走 GraphQL；REST 没有这两条读的入口）。
//
// # 官方依据（只读，rc.2 解包树）
//
//	dsh-experimental-forge-github/src/provider.ts:402-412（GraphqlReader：一次
//	  GraphQL 请求 → data 或 ForgeFailure；AUTHENTICATION 正则判 unauthenticated）
//	dsh-experimental-forge-github/src/provider.ts:414-420（mutate → sendMutation）
//	dsh-experimental-forge-github/src/mutation.ts:76-107（sendMutation 的
//	  failed/unknown 分类：有 payload 就算成功；4xx / 已知拒绝型 errors → failed；
//	  其余一律 unknown，且**不重试**）
//	dsh-experimental-forge-github/src/index.ts:22-60（maxOutputBytes 默认 8MiB）
//
// # 与官方的差别（有意的）
//
// 官方把 GraphQL 交给 `gh api graphql`（子进程 + stdin）。本宿主的 forge 面本来
// 就是**自己发 HTTP**（REST provider 的 apiBase + Bearer token），所以这里直接
// `POST <graphql endpoint>`：
//
//   - 端点：github.com → https://api.github.com/graphql；GHES → https://<host>/api/graphql；
//     测试/自建用 BaseURL（DSHD_GITHUB_API_URL）推出 `<base>/graphql`，
//     REST base 是 `/api/v3` 时换成 `/api/graphql`。
//   - 凭据：Token（构造时 GITHUB_TOKEN/GH_TOKEN）→ TokenLookup（生产 `gh auth token`）。
//     没有凭据时**在发请求之前**就返回结构化 unsupported，绝不空手出网。
//   - 超时/取消：沿用 provider 的 *http.Client（生产 45s）与请求 ctx；不重试。
//   - 体积：单次响应上限 8MiB（官方 maxOutputBytes 默认）；超了报 too-large。
package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	// graphqlPageSize 是 GitHub connection 的单页上限（官方 PAGE_SIZE=100）。
	graphqlPageSize = 100
	// defaultGraphQLMaxBytes 是官方 maxOutputBytes 的默认值（index.ts:38）。
	defaultGraphQLMaxBytes = 8 << 20
	// defaultGraphQLMaxReviewThreads 是官方 maxReviewThreads 默认值（index.ts:39）。
	defaultGraphQLMaxReviewThreads = 1000
	// defaultGraphQLMaxViewedFiles 是官方 maxViewedFiles 默认值（index.ts:41）。
	defaultGraphQLMaxViewedFiles = 3000
)

// graphqlAuthentication 是官方 provider.ts:31 的 AUTHENTICATION 正则。
var graphqlAuthentication = regexp.MustCompile(`(?i)gh auth login|authentication|bad credentials|HTTP 401`)

// graphqlRefusalTypes 是官方 mutation.ts:17 的 REFUSALS：这些 GraphQL 错误型
// 表示 GitHub 在**应用前**就拒绝了，所以结果是 failed（什么都没改），不是 unknown。
var graphqlRefusalTypes = map[string]bool{
	"FORBIDDEN": true, "INSUFFICIENT_SCOPES": true, "NOT_FOUND": true,
	"RATE_LIMITED": true, "UNPROCESSABLE": true,
}

// graphqlProblem 是一条 GraphQL errors[] 元素。
type graphqlProblem struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// graphqlAnswer 是一次 GraphQL 应答（状态码 + 原始体 + 解析出的字段）。
type graphqlAnswer struct {
	Status   int
	Body     []byte
	Data     json.RawMessage
	Problems []graphqlProblem
	Message  string
}

// graphqlMutation 是一次 GraphQL mutation：文档 + 结果字段名 + 变量。
type graphqlMutation struct {
	Query     string
	Field     string
	Variables map[string]any
}

// graphqlEndpoint 返回这次请求的 GraphQL 端点。
func (p *ChangeRequestProvider) graphqlEndpoint(ref changeRequestRef) string {
	if base := strings.TrimRight(strings.TrimSpace(p.BaseURL), "/"); base != "" {
		// GHES 的 REST base 是 `<host>/api/v3`，GraphQL 在 `<host>/api/graphql`。
		if strings.HasSuffix(base, "/api/v3") {
			return strings.TrimSuffix(base, "/v3") + "/graphql"
		}
		return base + "/graphql"
	}
	if ref.Host == "github.com" || strings.HasSuffix(ref.Host, ".github.com") {
		return "https://api.github.com/graphql"
	}
	return "https://" + ref.Host + "/api/graphql"
}

// graphqlToken 按生产凭据链取令牌：Token → GITHUB_TOKEN → GH_TOKEN →
// TokenLookup（生产是 `gh auth token`）。解析结果缓存一次（`gh` 是子进程）。
func (p *ChangeRequestProvider) graphqlToken() string {
	if token := strings.TrimSpace(p.Token); token != "" {
		return token
	}
	for _, key := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if token := strings.TrimSpace(os.Getenv(key)); token != "" {
			return token
		}
	}
	p.tokenOnce.Do(func() {
		if p.TokenLookup != nil {
			p.tokenValue = strings.TrimSpace(p.TokenLookup())
			return
		}
		if !p.CLICredential && !cliCredentialEnabled() {
			return
		}
		if out, err := exec.Command("gh", "auth", "token").Output(); err == nil {
			p.tokenValue = strings.TrimSpace(string(out))
		}
	})
	return p.tokenValue
}

// cliCredentialEnabled 报告这次进程是否允许用 `gh auth token` 兜底。
//
// 为什么不无条件开：`gh auth token` 读的是**本机登录态**，`go test` 里一旦拿到
// 真令牌就会真的打 api.github.com —— 离线测试是硬规则，而且 `cmd/dshd` 的
// provider 能力测试正是靠"没有凭据"这一支在断言官方码。所以：
//   - 测试二进制（`*_test` 编译出来的 `pkg.test[.exe]`）默认**不**用 gh 兜底；
//   - 生产二进制默认用（官方 github provider 自己也靠 `gh` 的登录态，
//     见 dsh-experimental-forge-github/src/provider.ts:402-420）；
//   - DSHD_FORGE_CLI_CREDENTIAL=1/0 可显式覆盖（部署/调试用）。
func cliCredentialEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("DSHD_FORGE_CLI_CREDENTIAL"))) {
	case "1", "true", "on", "yes":
		return true
	case "0", "false", "off", "no":
		return false
	}
	name := strings.ToLower(filepath.Base(os.Args[0]))
	return !strings.HasSuffix(name, ".test") && !strings.HasSuffix(name, ".test.exe")
}

// graphqlHTTPClient 返回 GraphQL 用的客户端（nil 时退回默认客户端）。
func (p *ChangeRequestProvider) graphqlHTTPClient() *http.Client {
	if p.HTTP != nil {
		return p.HTTP
	}
	return http.DefaultClient
}

// graphqlMaxBytes 是本次读允许的响应体上限（0 = 官方默认）。
func (p *ChangeRequestProvider) graphqlMaxBytes() int {
	if p.MaxReviewBytes > 0 {
		return p.MaxReviewBytes
	}
	return defaultGraphQLMaxBytes
}

// graphqlMaxThreads / graphqlMaxViewed 是两条读的上限（0 = 官方默认）。
func (p *ChangeRequestProvider) graphqlMaxThreads() int {
	if p.MaxReviewThreads > 0 {
		return p.MaxReviewThreads
	}
	return defaultGraphQLMaxReviewThreads
}

func (p *ChangeRequestProvider) graphqlMaxViewed() int {
	if p.MaxViewedFiles > 0 {
		return p.MaxViewedFiles
	}
	return defaultGraphQLMaxViewedFiles
}

// graphqlCredentialsMissing 是"没有凭据"的唯一错误：结构化 unsupported（501）。
//
// 官方 github provider 恒有 GraphQL 能力，缺的只是登录态（它回 unauthenticated）；
// 本宿主的 GraphQL 面在当前构造下**确实没有凭据**，所以按官方 `unsupported`
// 语义（能力缺失，不是失败重试）报，并把"怎么能用"写进正文。
func graphqlCredentialsMissing() error {
	return &ForgeHTTPError{Status: http.StatusNotImplemented, Code: FailureUnsupported,
		Message: "GitHub GraphQL 需要凭据：设置 GITHUB_TOKEN/GH_TOKEN，或先 `gh auth login`" +
			"（review threads 与 viewed 标记只能走 GraphQL；本宿主不伪造）"}
}

// graphqlRequireCredential 在发请求前先检查凭据。
//
// 顺序很重要：没有凭据时"这条能力不可用"优先于"参数不合法"——上层
// （cmd/dshd）把 unsupported 折成官方 review-unsupported / null / union
// unsupported，而参数错会变成 400/422。同时这样也能保证没有凭据时一次请求都不发。
func (p *ChangeRequestProvider) graphqlRequireCredential() error {
	if p.graphqlToken() == "" {
		return graphqlCredentialsMissing()
	}
	return nil
}

// graphqlPost 发一次 GraphQL 请求，返回应答（HTTP 非 2xx 也照实返回，
// 由调用方按读/写语义分类）。
//
// 只在**请求没发出去**或**答复不可解析为 GraphQL**时返回 error；HTTP 状态码、
// GraphQL errors[] 都留在应答里。
func (p *ChangeRequestProvider) graphqlPost(ctx context.Context, ref changeRequestRef, query string, variables map[string]any) (graphqlAnswer, error) {
	token := p.graphqlToken()
	if token == "" {
		return graphqlAnswer{}, graphqlCredentialsMissing()
	}
	if variables == nil {
		variables = map[string]any{}
	}
	payload, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return graphqlAnswer{}, &ForgeHTTPError{Status: http.StatusBadRequest, Code: "invalid_spec",
			Message: "GraphQL 请求无法序列化：" + err.Error()}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.graphqlEndpoint(ref), bytes.NewReader(payload))
	if err != nil {
		return graphqlAnswer{}, &ForgeHTTPError{Status: http.StatusBadRequest, Code: "invalid_spec",
			Message: "GraphQL 请求构造失败：" + err.Error()}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "dsh-go/0.2.2")
	resp, err := p.graphqlHTTPClient().Do(req)
	if err != nil {
		// 取消/超时原样交给上层（transport 会翻成 499 canceled）。
		if ctx.Err() != nil {
			return graphqlAnswer{}, ctx.Err()
		}
		return graphqlAnswer{}, &ForgeHTTPError{Status: http.StatusBadGateway, Code: "provider_unavailable",
			Message: "GitHub GraphQL 网络失败：" + err.Error()}
	}
	defer resp.Body.Close()
	limit := int64(p.graphqlMaxBytes())
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if readErr != nil {
		return graphqlAnswer{}, &ForgeHTTPError{Status: http.StatusBadGateway, Code: "provider_unavailable",
			Message: "读取 GitHub GraphQL 响应失败：" + readErr.Error()}
	}
	if int64(len(body)) > limit {
		return graphqlAnswer{}, &ForgeHTTPError{Status: http.StatusRequestEntityTooLarge, Code: "too_large",
			Message: fmt.Sprintf("GitHub GraphQL 响应超过 %d 字节上限", limit)}
	}
	answer := graphqlAnswer{Status: resp.StatusCode, Body: body}
	var envelope struct {
		Data    json.RawMessage  `json:"data"`
		Errors  []graphqlProblem `json:"errors"`
		Message string           `json:"message"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return graphqlAnswer{}, &ForgeHTTPError{Status: http.StatusBadGateway, Code: "provider_error",
			Message: "GitHub GraphQL 响应不是 JSON: " + truncateForge(string(body), 300)}
	}
	answer.Data = envelope.Data
	answer.Problems = envelope.Errors
	answer.Message = envelope.Message
	return answer, nil
}

// graphqlRead 发一次只读查询，返回 data。
//
// 错误口径与官方 provider.ts:402-412 的 GraphqlReader 一致：
//   - GraphQL errors[] → unavailable（认证字样 → unauthenticated）；
//   - data 缺失/null → unavailable（绝不把"没读到"说成"没有数据"）；
//   - HTTP 非 2xx → 本包统一的 status → 内部码映射。
func (p *ChangeRequestProvider) graphqlRead(ctx context.Context, ref changeRequestRef, query string, variables map[string]any) (json.RawMessage, error) {
	data, _, err := p.graphqlReadSized(ctx, ref, query, variables)
	return data, err
}

// graphqlReadSized 与 graphqlRead 同义，并交回这次应答占用的字节数
// （官方按 JSON 字节给 review 读记预算）。
func (p *ChangeRequestProvider) graphqlReadSized(ctx context.Context, ref changeRequestRef, query string, variables map[string]any) (json.RawMessage, int, error) {
	answer, err := p.graphqlPost(ctx, ref, query, variables)
	if err != nil {
		return nil, 0, err
	}
	if answer.Status < 200 || answer.Status >= 300 {
		return nil, 0, forgeHTTPError(answer.Status, "POST /graphql", answer.Body)
	}
	if len(answer.Problems) > 0 {
		return nil, 0, graphqlReadProblem(answer.Problems)
	}
	if len(bytes.TrimSpace(answer.Data)) == 0 || string(bytes.TrimSpace(answer.Data)) == "null" {
		return nil, 0, &ForgeHTTPError{Status: http.StatusBadGateway, Code: "provider_error",
			Message: "GitHub GraphQL 应答没有 data（不把空应答当成没有数据）"}
	}
	return answer.Data, len(answer.Body), nil
}

// graphqlReadProblem 把 GraphQL errors[] 折成读侧错误（官方 responseFailure）。
func graphqlReadProblem(problems []graphqlProblem) error {
	message := graphqlProblemMessage(problems)
	if graphqlAuthentication.MatchString(message) {
		return &ForgeHTTPError{Status: http.StatusUnauthorized, Code: "auth_required", Message: message}
	}
	return &ForgeHTTPError{Status: http.StatusBadGateway, Code: "provider_error", Message: message}
}

// graphqlProblemMessage 把 errors[] 拼成一行（官方取第一条有内容的行）。
func graphqlProblemMessage(problems []graphqlProblem) string {
	parts := make([]string, 0, len(problems))
	for _, problem := range problems {
		if text := strings.TrimSpace(problem.Message); text != "" {
			parts = append(parts, text)
		}
	}
	if len(parts) == 0 {
		return "GitHub GraphQL 请求失败（没有给出原因）"
	}
	return strings.Join(parts, "; ")
}

// graphqlSendMutation 发一次 mutation，返回 payload 或官方 ActionFailure。
//
// 分类严格按官方 mutation.ts:76-107：
//   - data.<field> 非 null → 应用成功（即使旁边还有关于所选字段的 errors）；
//   - HTTP 4xx 或 errors[].type 全在 REFUSALS → failed（GitHub 在应用前拒绝）；
//   - 其它（5xx / 网络 / 不确认）→ unknown（可能已生效，必须先到 host 核对）；
//   - 没有凭据（请求没发出去）→ failed，不是 unknown。
//
// **绝不重试**：同一次写只发一次。
func (p *ChangeRequestProvider) graphqlSendMutation(ctx context.Context, ref changeRequestRef, mutation graphqlMutation) (json.RawMessage, *ActionFailure) {
	answer, err := p.graphqlPost(ctx, ref, mutation.Query, mutation.Variables)
	if err != nil {
		var httpErr *ForgeHTTPError
		if errors.As(err, &httpErr) && httpErr.Code == FailureUnsupported {
			// 请求根本没发出去：确定什么都没改。
			return nil, &ActionFailure{Code: ActionFailed, Message: httpErr.Message}
		}
		if ctx.Err() != nil {
			return nil, &ActionFailure{Code: ActionUnknown,
				Message: "请求在答复前被取消（远端是否已写入未知）：" + ctx.Err().Error()}
		}
		return nil, &ActionFailure{Code: ActionUnknown, Message: err.Error()}
	}
	if payload := graphqlPayloadOf(answer.Data, mutation.Field); payload != nil {
		return payload, nil
	}
	refused := answer.Status == http.StatusBadRequest || answer.Status == http.StatusUnauthorized ||
		answer.Status == http.StatusForbidden || answer.Status == http.StatusNotFound ||
		answer.Status == http.StatusConflict || answer.Status == http.StatusUnprocessableEntity
	if len(answer.Problems) > 0 && graphqlAllRefusals(answer.Problems) {
		refused = true
	}
	message := graphqlProblemMessage(answer.Problems)
	if message == "" {
		message = strings.TrimSpace(answer.Message)
	}
	if message == "" {
		message = fmt.Sprintf("GitHub GraphQL 写入返回 HTTP %d", answer.Status)
	}
	if refused {
		return nil, &ActionFailure{Code: ActionFailed, Message: message}
	}
	return nil, &ActionFailure{Code: ActionUnknown, Message: message}
}

// graphqlAllRefusals 报告 errors[] 是否都是"应用前拒绝"的已知类型。
func graphqlAllRefusals(problems []graphqlProblem) bool {
	if len(problems) == 0 {
		return false
	}
	for _, problem := range problems {
		if !graphqlRefusalTypes[problem.Type] {
			return false
		}
	}
	return true
}

// graphqlPayloadOf 取出 mutation 的结果字段；缺失或 null → nil（官方同义）。
func graphqlPayloadOf(data json.RawMessage, field string) json.RawMessage {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil
	}
	payload, ok := fields[field]
	if !ok || len(bytes.TrimSpace(payload)) == 0 || string(bytes.TrimSpace(payload)) == "null" {
		return nil
	}
	return payload
}

// actionFailureError 把官方 ActionFailure 折回本包的错误类型，保证
// ActionFailureOf(err) 能原样还原（cmd/dshd 的 action-failed/unknown 映射靠它）。
func actionFailureError(failure *ActionFailure) error {
	if failure == nil {
		return nil
	}
	switch failure.Code {
	case ActionStale:
		return &ForgeHTTPError{Status: http.StatusConflict, Code: "stale_head", Message: failure.Message}
	case ActionUnauthenticated:
		return &ForgeHTTPError{Status: http.StatusUnauthorized, Code: "auth_required", Message: failure.Message}
	case ActionUnknown:
		return &ForgeHTTPError{Status: http.StatusBadGateway, Code: "provider_unavailable", Message: failure.Message}
	}
	return &ForgeHTTPError{Status: http.StatusUnprocessableEntity, Code: "invalid_spec", Message: failure.Message}
}

// graphqlUnexpected 是"GitHub 的答复形状不认识"（官方 unexpected ... response）。
func graphqlUnexpected(what string) error {
	return &ForgeHTTPError{Status: http.StatusBadGateway, Code: "provider_error",
		Message: "unexpected GitHub response to the " + what}
}

// graphqlNotFound 是"这条 change request 在 host 上不存在"（官方 not-found）。
func graphqlNotFound(number int) error {
	return &ForgeHTTPError{Status: http.StatusNotFound, Code: "not_found",
		Message: fmt.Sprintf("pull request %d was not found", number)}
}

// graphqlStale 在 head 已移动时返回官方 stale_head，否则 nil。
func graphqlStale(want, got string) *ForgeHTTPError {
	if strings.EqualFold(strings.TrimSpace(want), strings.TrimSpace(got)) {
		return nil
	}
	return staleHead(want, got)
}
