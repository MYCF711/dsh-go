package account

// 官方 0.2.2-rc.2 的「账号通知三件套」：getNotifications /
// getNotificationContent / reportNotificationEvent。
//
// # 契约来源（只读官方 rc.2 包，逐条可核对）
//
//	dsh-deepseek-account/lib/typert.host.js:333  account/getNotificationContent
//	dsh-deepseek-account/lib/typert.host.js:382  account/getNotifications
//	dsh-deepseek-account/lib/typert.host.js:476  account/reportNotificationEvent
//	dsh-deepseek-account/lib/types/notifications.js:5-89    zod 形状 + 逐条跳过
//	dsh-deepseek-account/lib/types/protocol.js:110-160      三个 HTTP 端点
//	dsh-deepseek-account/lib/types/client-metadata.js:26-58 客户端身份头
//	dsh-deepseek-account/lib/index.js:696-697   platformOrigin / harnessOrigin 默认值
//	dsh-deepseek-account/lib/index.js:1030-1098 三个方法（null/false 语义）
//
// # 数据来源：**云端**，没有本地缓存
//
// 官方实现里没有任何本地通知存储：`getNotifications` 直接 GET
// `<harnessOrigin>/api/v0/users/notifications/list`，content / events 同理
// （lib/index.js:1040/1063/1087）。所以本仓也不做"本地通知队列"——
// 那会是官方没有的语义，而且必然要编造内容。
//
// 因此三条方法的可用性只有两种情况：
//
//   - official provider + 已登录：真打云端，结果原样投影；
//   - 其他（local / self-hosted）：**没有通知后端**，回
//     ErrNoNotificationBackend（上层映射成结构化 not_implemented 并说清缺什么），
//     绝不返回空列表假装"没有通知"。
//
// # 两个 origin 不共用（官方迁移指南第 4 条）
//
// 通知的 list / content / events 走 harnessOrigin（默认
// https://harness.deepseek.com）；登录、资料、余额、登出、外链、内嵌页仍走
// platformOrigin（默认 https://platform.deepseek.com）。这两个默认值在
// lib/index.js:696-697，解析/校验在 lib/index.js:920-921。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// DefaultHarnessOrigin 是官方 `harnessOrigin` 的默认值
// （dsh-deepseek-account/lib/index.js:697）。
//
// 它**只**用于通知的 list / content / events；其余账号端点用 PlatformOrigin。
const DefaultHarnessOrigin = "https://harness.deepseek.com"

// EnvHarnessOrigin 覆盖 harnessOrigin（私有部署/环回测试用；官方迁移指南
// 第 4 条「Private deployments and loopback fixtures set harnessOrigin to
// their notification service origin」）。
const EnvHarnessOrigin = "DSHD_ACCOUNT_HARNESS_ORIGIN"

// 通知交互事件（官方 z.union 三个字面量，typert.host.js:136）。
const (
	NotificationEventDisplayed  = "displayed"
	NotificationEventCTAClicked = "cta_clicked"
	NotificationEventDismissed  = "dismissed"
)

// NotificationEvents 按官方顺序返回事件的三个合法值（给白名单校验用）。
func NotificationEvents() []string {
	return []string{NotificationEventDisplayed, NotificationEventCTAClicked, NotificationEventDismissed}
}

// 通知动作的样式（官方 z.union 两个字面量）。
const (
	NotificationActionStylePrimary   = "primary"
	NotificationActionStyleSecondary = "secondary"
)

// 通知动作的目标（官方 discriminatedUnion('type') 的两个变体）。
const (
	NotificationTargetAppRoute = "app_route"
	NotificationTargetDismiss  = "dismiss"
)

// notificationMaxPollAfterSeconds 是官方 `next_poll_after` 的上界
// （z.number().positive().max(2_147_483_647 / 1_000)，notifications.js:23）。
const notificationMaxPollAfterSeconds = 2147483.647

var (
	// ErrNoNotificationBackend 表示当前 provider 没有通知后端（local /
	// self-hosted）。上层必须把它映射成结构化"未实现并说清缺什么"，
	// **不许**转成空列表。
	ErrNoNotificationBackend = errors.New("account: 当前 provider 没有账号通知后端")

	// ErrNotificationProtocol 表示云端响应不符合通知协议（信封坏、字段缺、
	// next_poll_after 越界、content 的 id 不符）。这类错误一定伴随"这次
	// 查询失败"，绝不允许被降级成空结果。
	ErrNotificationProtocol = errors.New("account: 通知响应不符合平台协议")

	// ErrNotificationUnauthorized 表示令牌被平台拒绝（HTTP 401 或业务码
	// 40003）。它**不是**调用方错误：官方语义是"清掉凭据并回 null/false"。
	ErrNotificationUnauthorized = errors.New("account: 通知请求的授权已失效")

	// ErrNotificationBadInput 表示调用参数本身不合法（空 id / 未知事件名）。
	ErrNotificationBadInput = errors.New("account: 通知调用参数不合法")
)

// NotificationAction 是通知上的一个动作。
//
// Target 用 RawMessage 原样保留：官方把 `value.actions` 直接透传
// （notifications.js:41-48 只做校验、不重写），两种合法形状是
// `{"type":"app_route","name":"account"}` 与 `{"type":"dismiss"}`。
type NotificationAction struct {
	Label  string          `json:"label"`
	Style  string          `json:"style"`
	Target json.RawMessage `json:"target"`
}

// Notification 是一条已本地化的通知（官方 `AccountNotification`）。
//
// DisplayUntil 是 `string | null`：**null = 永不过期**（官方 describe），
// 所以必须用指针区分"null"与"空串"。
type Notification struct {
	ID           string               `json:"id"`
	CreatedAt    string               `json:"createdAt"`
	DisplayUntil *string              `json:"displayUntil"`
	Title        string               `json:"title"`
	Body         string               `json:"body"`
	Actions      []NotificationAction `json:"actions"`
	// Data 是可选业务数据（赠金元数据搬到这里，官方迁移指南第 1 条）。
	Data json.RawMessage `json:"data,omitempty"`
}

// NotificationBatch 是官方 `AccountNotificationBatch`。
//
// 官方**移除**了 batch 的 accountId（账号由当前令牌决定），所以这里没有它。
type NotificationBatch struct {
	NextPollAfterSeconds float64        `json:"nextPollAfterSeconds"`
	Notifications        []Notification `json:"notifications"`
	// Source 是本仓的额外字段（官方 schema 是 additionalProperties:true）：
	// 让调用方能确认数据真的来自官方 Platform，而不是本地伪造。
	Source Source `json:"source,omitempty"`
}

// noNotificationBackend 造一个带 provider 名字的结构化错误。
func (s *Service) noNotificationBackend() error {
	return fmt.Errorf(
		"%w：provider=%s。官方通知只来自 harnessOrigin 的 "+
			"/api/v0/users/notifications/{list,content,events}（DeepSeek Platform），"+
			"本宿主在 %s 模式下没有这个后端；要用通知请切到 official provider "+
			"（DSHD_ACCOUNT_PROVIDER=official + DSHD_ACCOUNT_PLATFORM_ORIGIN/"+
			"%s）",
		ErrNoNotificationBackend, s.mode, s.mode, EnvHarnessOrigin)
}

// notificationGrant 取当前令牌。ok=false 表示"没有可用授权"（未登录）——
// 按官方语义这是 `null` / `false`，不是错误。
func (s *Service) notificationGrant() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.doc.Status != StatusCredentialStored || strings.TrimSpace(s.doc.Token) == "" {
		return "", false
	}
	return s.doc.Token, true
}

// Notifications 读未消费的通知列表（官方 `account/getNotifications`）。
//
// 返回 (nil, nil) 表示未登录或授权在查询中变了（官方结果类型是
// `AccountNotificationBatch | null`）；任何云端/协议故障都返回错误。
func (s *Service) Notifications(ctx context.Context, client Client) (*NotificationBatch, error) {
	if s.mode != SourceOfficial {
		return nil, s.noNotificationBackend()
	}
	token, ok := s.notificationGrant()
	if !ok {
		return nil, nil
	}
	raw, err := s.notificationRequest(ctx, http.MethodGet,
		"/api/v0/users/notifications/list", token, nil, client)
	if err != nil {
		return nil, s.notificationAuthFailure(err)
	}
	batch, err := parseNotificationList(raw)
	if err != nil {
		return nil, err
	}
	batch.Source = SourceOfficial
	return batch, nil
}

// NotificationContent 按 id 读本地化内容（官方 `account/getNotificationContent`）。
//
// **不改变消费状态**（官方 "without changing consumption"）：所以这里只发
// 一个 GET，宿主侧不写任何本地状态。
func (s *Service) NotificationContent(ctx context.Context, id string, client Client) (*Notification, error) {
	if s.mode != SourceOfficial {
		return nil, s.noNotificationBackend()
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("%w：通知 id 不能是空串", ErrNotificationBadInput)
	}
	token, ok := s.notificationGrant()
	if !ok {
		return nil, nil
	}
	raw, err := s.notificationRequest(ctx, http.MethodGet,
		"/api/v0/users/notifications/content?id="+urlQueryEscape(id), token, nil, client)
	if err != nil {
		return nil, s.notificationAuthFailure(err)
	}
	var wire wireNotification
	payload, err := unwrapNotificationEnvelope(raw)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(payload, &wire); err != nil {
		return nil, fmt.Errorf("%w：content 响应不是 JSON 对象: %v", ErrNotificationProtocol, err)
	}
	content, err := projectNotification(wire, "content")
	if err != nil {
		return nil, err
	}
	// 官方：回来的 id 必须与请求一致，否则算协议错（notifications.js:99-101）。
	if content.ID != id {
		return nil, fmt.Errorf("%w：content 返回的 id=%q 与请求的 %q 不一致",
			ErrNotificationProtocol, content.ID, id)
	}
	return content, nil
}

// ReportNotificationEvent 上报一次通知交互（官方 `account/reportNotificationEvent`）。
//
// 返回 true = 平台接受了这次事件。官方对 `acked:true` 与 `acked:false` **都**
// 视为本次上报完成（迁移指南第 2 条「valid acked: true and acked: false
// responses both finish the attempt」），是否消费由后端决定；失败不回滚本地
// 卡片、也不重试——那是调用方的事。
func (s *Service) ReportNotificationEvent(ctx context.Context, id, event string, client Client) (bool, error) {
	if s.mode != SourceOfficial {
		return false, s.noNotificationBackend()
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return false, fmt.Errorf("%w：通知 id 不能是空串", ErrNotificationBadInput)
	}
	if !validNotificationEvent(event) {
		return false, fmt.Errorf("%w：event=%q 不在 %v 里",
			ErrNotificationBadInput, event, NotificationEvents())
	}
	token, ok := s.notificationGrant()
	if !ok {
		return false, nil
	}
	body := map[string]any{"notification_id": id, "type": event}
	raw, err := s.notificationRequest(ctx, http.MethodPost,
		"/api/v0/users/notifications/events", token, body, client)
	if err != nil {
		return false, s.notificationAuthFailure(err)
	}
	payload, err := unwrapNotificationEnvelope(raw)
	if err != nil {
		return false, err
	}
	var ack struct {
		Acked *bool `json:"acked"`
	}
	if err := json.Unmarshal(payload, &ack); err != nil {
		return false, fmt.Errorf("%w：events 响应不是 JSON 对象: %v", ErrNotificationProtocol, err)
	}
	if ack.Acked == nil {
		return false, fmt.Errorf("%w：events 响应缺 acked（官方 z.object({acked: boolean})）",
			ErrNotificationProtocol)
	}
	// acked 的**值**不重要：两种都表示平台收下了这次上报。
	return true, nil
}

// notificationAuthFailure 把"授权失效"翻译成官方语义：清凭据 + 回 nil/false。
//
// 官方 `getNotifications` 在 AccountUnauthorizedError 上调
// `expireCredential(token, lifetime)` 然后 return null（lib/index.js:1044-1047）；
// 清凭据本身失败时**必须**把错误抛出去（否则磁盘上还留着一个已被平台拒绝的
// 令牌，用户会以为还登着）。
func (s *Service) notificationAuthFailure(err error) error {
	if !errors.Is(err, ErrNotificationUnauthorized) {
		return err
	}
	if clearErr := s.clearToken(); clearErr != nil {
		return fmt.Errorf("%w（清理失效凭据失败：%v）", ErrNotificationProtocol, clearErr)
	}
	return nil
}

// notificationRequest 发一次通知请求，返回平台 biz_data 的原始字节。
func (s *Service) notificationRequest(
	ctx context.Context, method, path, token string, body any, client Client,
) (json.RawMessage, error) {
	headers := notificationClientHeaders(client)
	headers["x-dsh-auth-token"] = token
	var raw json.RawMessage
	status, err := s.doJSONRaw(ctx, method, s.harness+path, headers, body, &raw)
	if err != nil {
		if status == http.StatusUnauthorized {
			return nil, ErrNotificationUnauthorized
		}
		return nil, fmt.Errorf("%w：%s %s 失败（HTTP %d）：%v", ErrNotificationProtocol,
			method, path, status, err)
	}
	if isNotificationUnauthorized(raw) {
		return nil, ErrNotificationUnauthorized
	}
	return raw, nil
}

// notificationClientHeaders 是官方 `platformClientHeaders`
// （client-metadata.js:38-58）逐字段照抄。
//
// 为什么必须发：平台用 `x-client-locale` 选服务端文案（官方 describe：
// "only its primary subtag selects the Platform wire locale"），用
// `x-client-timezone-offset` 判 display_until。漏了它们，客户端拿到的
// title/body 就不是用户语言——这是**真实可见**的语义差异。
func notificationClientHeaders(client Client) map[string]string {
	return map[string]string{
		"x-client-bundle-id":       "",
		"x-client-platform":        notificationClientPlatform(),
		"x-client-version":         client.Version,
		"x-client-locale":          platformWireLocale(client.Locale),
		"x-client-timezone-offset": strconv.FormatFloat(client.TimezoneOffsetSeconds, 'f', -1, 64),
	}
}

// platformWireLocale 把 UI 语言收敛成平台 wire locale（client-metadata.js:60-70）：
// 只看主语言子标签，中文 → zh_CN，其余 → en_US。
func platformWireLocale(locale string) string {
	primary := strings.ToLower(strings.TrimSpace(locale))
	if idx := strings.IndexAny(primary, "-_"); idx >= 0 {
		primary = primary[:idx]
	}
	if primary == "zh" {
		return "zh_CN"
	}
	return "en_US"
}

// notificationClientPlatform 是官方 desktopClientHeaders 的对应物
// （client-metadata.js:16-23）：桌面端明确标注，非桌面对话保持 web。
//
// 官方桌面只出 win/mac 两版，所以非 win32 一律 desktop-mac；本仓在
// Windows 以外没有桌面宿主，就老实报 web（不冒充 mac 桌面）。
func notificationClientPlatform() string {
	switch runtime.GOOS {
	case "windows":
		return "desktop-win"
	case "darwin":
		return "desktop-mac"
	default:
		return "web"
	}
}

// wireNotification 是平台下发的**线上**形状（snake_case）。
//
// 它和 Notification 的差别是真实存在的：平台发 created_at / display_until，
// 官方投影成 createdAt / displayUntil（notifications.js:29-48）。
type wireNotification struct {
	ID           *string                  `json:"id"`
	CreatedAt    *string                  `json:"created_at"`
	DisplayUntil *string                  `json:"display_until"`
	Title        *string                  `json:"title"`
	Body         *string                  `json:"body"`
	Actions      []wireNotificationAction `json:"actions"`
	Data         json.RawMessage          `json:"data"`
}

type wireNotificationAction struct {
	Label  *string         `json:"label"`
	Style  *string         `json:"style"`
	Target json.RawMessage `json:"target"`
}

// parseNotificationList 解析官方列表信封并逐条投影。
//
// 口径严格照官方（notifications.js:66-84）：
//   - **信封**坏（缺 next_poll_after、越界、notifications 不是数组）→ 整体失败；
//   - **单条**畸形/不支持 → 跳过那一条（官方 console.error + skip），
//     其余照服务端顺序保留。
func parseNotificationList(raw json.RawMessage) (*NotificationBatch, error) {
	payload, err := unwrapNotificationEnvelope(raw)
	if err != nil {
		return nil, err
	}
	var wire struct {
		NextPollAfter *float64           `json:"next_poll_after"`
		Notifications *[]json.RawMessage `json:"notifications"`
	}
	if err := json.Unmarshal(payload, &wire); err != nil {
		return nil, fmt.Errorf("%w：list 响应不是 JSON 对象: %v", ErrNotificationProtocol, err)
	}
	if wire.NextPollAfter == nil {
		return nil, fmt.Errorf("%w：list 响应缺 next_poll_after", ErrNotificationProtocol)
	}
	if *wire.NextPollAfter <= 0 || *wire.NextPollAfter > notificationMaxPollAfterSeconds {
		return nil, fmt.Errorf("%w：next_poll_after=%v 越界（官方要求 0 < x <= %v）",
			ErrNotificationProtocol, *wire.NextPollAfter, notificationMaxPollAfterSeconds)
	}
	if wire.Notifications == nil {
		return nil, fmt.Errorf("%w：list 响应缺 notifications 数组", ErrNotificationProtocol)
	}
	out := &NotificationBatch{
		NextPollAfterSeconds: *wire.NextPollAfter,
		// 必填数组：没有条目时必须是 []，不是 null。
		Notifications: make([]Notification, 0, len(*wire.Notifications)),
	}
	for i, entry := range *wire.Notifications {
		var item wireNotification
		if err := json.Unmarshal(entry, &item); err != nil {
			continue // 单条畸形 → 跳过（官方行为），不是整体失败
		}
		parsed, err := projectNotification(item, fmt.Sprintf("notifications[%d]", i))
		if err != nil {
			continue // 同上：不支持的条目逐条跳过
		}
		out.Notifications = append(out.Notifications, *parsed)
	}
	return out, nil
}

// projectNotification 把线上形状投影成官方字段，并做官方同等强度的校验。
//
// where 只用于错误信息（"notifications[3]" / "content"），因为在"逐条跳过"
// 的场景里错误会被丢掉，但在 content 场景里它会成为协议错的一部分。
func projectNotification(w wireNotification, where string) (*Notification, error) {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w：%s %s", ErrNotificationProtocol, where, fmt.Sprintf(format, args...))
	}
	if w.ID == nil {
		return nil, bad("缺 id")
	}
	if w.CreatedAt == nil {
		return nil, bad("缺 created_at")
	}
	if !isISOWithOffset(*w.CreatedAt) {
		return nil, bad("created_at=%q 不是带时区的 ISO 时间", *w.CreatedAt)
	}
	if w.DisplayUntil != nil && !isISOWithOffset(*w.DisplayUntil) {
		return nil, bad("display_until=%q 不是带时区的 ISO 时间", *w.DisplayUntil)
	}
	if w.Title == nil || w.Body == nil {
		return nil, bad("缺 title/body")
	}
	if w.Actions == nil {
		return nil, bad("缺 actions 数组")
	}
	out := &Notification{
		ID:        *w.ID,
		CreatedAt: *w.CreatedAt,
		Title:     *w.Title,
		Body:      *w.Body,
		Actions:   make([]NotificationAction, 0, len(w.Actions)),
	}
	if w.DisplayUntil != nil {
		until := *w.DisplayUntil
		out.DisplayUntil = &until
	}
	if len(w.Data) > 0 && string(w.Data) != "null" {
		out.Data = w.Data
	}
	for i, a := range w.Actions {
		if a.Label == nil || a.Style == nil || len(a.Target) == 0 {
			return nil, bad("actions[%d] 缺 label/style/target", i)
		}
		if *a.Style != NotificationActionStylePrimary && *a.Style != NotificationActionStyleSecondary {
			return nil, bad("actions[%d] 的 style=%q 越界", i, *a.Style)
		}
		if err := validateNotificationTarget(a.Target); err != nil {
			return nil, bad("actions[%d] 的 target 非法: %v", i, err)
		}
		out.Actions = append(out.Actions, NotificationAction{
			Label: *a.Label, Style: *a.Style, Target: a.Target,
		})
	}
	return out, nil
}

// validateNotificationTarget 校验官方 target 的两个变体
// （notifications.js:8-13 的 discriminatedUnion）。
func validateNotificationTarget(raw json.RawMessage) error {
	var target struct {
		Type *string `json:"type"`
		Name *string `json:"name"`
	}
	if err := json.Unmarshal(raw, &target); err != nil {
		return fmt.Errorf("不是对象: %v", err)
	}
	if target.Type == nil {
		return errors.New("缺 type")
	}
	switch *target.Type {
	case NotificationTargetAppRoute:
		if target.Name == nil || *target.Name != "account" {
			return errors.New(`app_route 必须带 name:"account"`)
		}
	case NotificationTargetDismiss:
	default:
		return fmt.Errorf("type=%q 不在 app_route/dismiss 里", *target.Type)
	}
	return nil
}

// isISOWithOffset 近似官方 `z.iso.datetime({offset:true})`：必须是带时区
// （Z 或 ±hh:mm）的 ISO 8601 时间。
func isISOWithOffset(value string) bool {
	if !strings.Contains(value, "T") {
		return false
	}
	if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
		return false
	}
	return true
}

// unwrapNotificationEnvelope 取平台信封里的 biz_data。
//
// 官方只认 `{code:0,data:{biz_code:0,biz_data}}`（protocol.js:71-73、:206-236）；
// 本仓沿用 `unwrapPlatform` 的既有约定：**裸 payload 也接受**（测试桩与旧
// 端点是裸 JSON），但两者都畸形时一定报协议错，绝不静默成空结果。
func unwrapNotificationEnvelope(raw json.RawMessage) (json.RawMessage, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, fmt.Errorf("%w：响应体是空的", ErrNotificationProtocol)
	}
	var envelope struct {
		Code *int `json:"code"`
		Data *struct {
			BizCode *int            `json:"biz_code"`
			BizData json.RawMessage `json:"biz_data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("%w：响应不是 JSON: %v", ErrNotificationProtocol, err)
	}
	if envelope.Data == nil {
		// 没有信封 → 当作裸 payload 交给上层继续校验。
		return raw, nil
	}
	if envelope.Code != nil && *envelope.Code != 0 {
		return nil, fmt.Errorf("%w：信封 code=%d（官方要求 0）", ErrNotificationProtocol, *envelope.Code)
	}
	if envelope.Data.BizCode != nil && *envelope.Data.BizCode != 0 {
		return nil, fmt.Errorf("%w：biz_code=%d（官方要求 0）", ErrNotificationProtocol, *envelope.Data.BizCode)
	}
	if len(envelope.Data.BizData) == 0 {
		return nil, fmt.Errorf("%w：信封缺 biz_data", ErrNotificationProtocol)
	}
	return envelope.Data.BizData, nil
}

// isNotificationUnauthorized 识别官方"授权失效"的第二种形状：
// HTTP 200 + `{"code":40003}`（protocol.js:227-231）。
func isNotificationUnauthorized(raw json.RawMessage) bool {
	var probe struct {
		Code *int `json:"code"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	return probe.Code != nil && *probe.Code == 40003
}

// validNotificationEvent 判断事件名是否合法。
func validNotificationEvent(event string) bool {
	for _, known := range NotificationEvents() {
		if event == known {
			return true
		}
	}
	return false
}

// urlQueryEscape 用 net/url 的查询转义（官方用 encodeURIComponent）。
//
// 唯一的差异处理：Go 的 QueryEscape 把空格转成 `+`，而 encodeURIComponent
// 转成 `%20`——多转义一点在 query 里永远安全，所以统一成 %20。
func urlQueryEscape(value string) string {
	return strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
}
