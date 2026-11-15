// status.go —— 官方「更新状态」的壳服务（官方 `DesktopUpdateBridge` 的 Go 等价物）。
//
// 官方归属（`dsh-client-ui-settings-general/src/types.ts:30-35`）：
//
//	/** Optional carrier API; it cannot select artifacts or authorize installation. */
//	export interface DesktopUpdateBridge {
//	  readonly channels?: DesktopChannelBridge
//	  status(): Promise<DesktopUpdatePresentation>
//	  open(): Promise<void>
//	  subscribe(listener: (state: DesktopUpdatePresentation) => void): () => void
//	}
//
// 它与 channel.go 的 `DesktopChannelBridge` 是**兄弟**：那边管"我选哪个渠道"，
// 这边管"现在有没有新版本、更新入口在哪"。
//
// # 更新来源是我们自己的 GitHub 仓
//
// 用户 2026-11-13 指令「走我们的 go 仓库更新」——即从 `MYCF711/dsh-go` 读 release，
// **不是**官方的 Electron 更新通道（本仓既没有 Electron 主进程，也没有 updater feed）。
// 用的是 GitHub REST 只读接口 `GET /repos/{owner}/{repo}/releases`。
//
// # 本仓只"检查"，不"下载/安装"
//
// 官方 phase 有 8 态，本仓只有前四种能力，所以**只产生 4 态**：
//
//	idle       没有比运行中版本更新的发布（含"还没查过"的冷启动）
//	checking   正在查
//	available  有更新，带 `version`
//	error      查失败，带 `failure`
//
// downloading / verifying / installing / ready **一个都不产生**——本仓没有下载器、
// 没有校验器、没有安装器，返回它们就是假数据。`percent` 同理永远是 nil。
//
// # 失败分类（3 条规则，覆盖官方 14 个 failure 里我们能遇到的 3 个）
//
//	configuration   壳配置坏（`update-channel.json` 读不了/格式错/值非法/清理失败）。
//	                同时把官方 `configurationError` 填成渠道那四个错误码之一——
//	                官方渲染层先判 `configurationError`（DesktopUpdateIndicator.tsx:15），
//	                它给出的文案比 `failure` 那句更具体。两件事同时为真也是官方行为：
//	                启动时渠道文件读不出来，官方同时设 `channelState.error` 与
//	                `suspendConfiguration()`（rc.2 的 app.asar `lib/main.js:20699` 与 `:20759`），
//	                于是 presentation 同时带 `configurationError` 和 `phase:'error'`+`failure:'configuration'`。
//	check-network   拿不到一次**完成的** HTTP 往返：DNS/拨号/TLS/超时/读包中断。
//	                与官方分类器一致——它按 message 是否命中网络正则判定
//	                （rc.2 的 app.asar `lib/main.js:16644` 的 NETWORK_FAILURE：
//	                ERR_CONNECTION_CLOSED|ERR_CONNECTION_RESET|ERR_INTERNET_DISCONNECTED|
//	                ERR_NAME_NOT_RESOLVED|ETIMEDOUT）。
//	check           拿到了确定答复但答案不可用：**任何**非 2xx 状态码（含 5xx）、
//	                body 不是合法 JSON、body 不是数组、body 超长。
//	                5xx 也归这一档而不是 check-network：官方只看 message 命不命中网络正则，
//	                HTTP 状态码本身不算网络失败（`lib/main.js:16654-16655`），
//	                而 `desktop.update.checkFailed`（「检查更新失败，请稍后重试。」）
//	                正好就是 5xx 该说的话。
//
// 其余 11 个 failure（download* / install* / stop-failed / tasks-* / fullscreen-timeout）
// 本仓**不产生**——它们全属于下载与安装阶段，本仓没有那条链路。
//
// # 不许打爆 GitHub
//
//   - **没人观察就不出网**：进程启动不发请求；只有 `status()`（界面在观察）与
//     `open()`（用户点了）才会发起。
//   - **TTL 缓存**：默认 5 分钟。TTL 内无论调多少次 `status()` 都只回缓存。
//   - **单飞**：同一时刻只允许一次在飞的请求，并发调用共用它，不叠加。
//   - **条件请求**：带上一次的 `ETag`，服务端还认就只回 304，我们复用上次的结论。
//   - **失败也进缓存**：失败结果同样带 `at`，所以"自动重试"被 TTL 挡住；
//     真正的重试由用户点 `open()` 触发（`open` 绕过 TTL）。
package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Phase 逐字照官方 `DesktopUpdatePresentation['phase']`（`types.ts:22`）。
type Phase string

const (
	// PhaseIdle 已是最新（或还没查过）。
	PhaseIdle Phase = "idle"
	// PhaseChecking 正在查。
	PhaseChecking Phase = "checking"
	// PhaseAvailable 有更新；此时 `Version` 必有值。
	PhaseAvailable Phase = "available"
	// PhaseError 查失败；此时 `Failure` 必有值。
	PhaseError Phase = "error"

	// 下面四态官方有、本仓**不产生**（没有下载器/校验器/安装器）。列在这里是为了
	// 让"官方词表"在代码里可查，而不是靠记忆。
	PhaseDownloading Phase = "downloading"
	PhaseVerifying   Phase = "verifying"
	PhaseInstalling  Phase = "installing"
	PhaseReady       Phase = "ready"
)

// ProduciblePhases 返回本仓真的会产出的 phase（固定顺序）。
//
// 这是"不许伪造能力"的可执行判据：任何一条链路的返回值都必须落在这四个里。
func ProduciblePhases() []Phase {
	return []Phase{PhaseIdle, PhaseChecking, PhaseAvailable, PhaseError}
}

// 官方 `DesktopUpdateFailureKind` 的 14 个逐字取值（`types.ts:4-19`，顺序照 union）。
const (
	FailureConfiguration      = "configuration"
	FailureCheck              = "check"
	FailureCheckNetwork       = "check-network"
	FailureDownload           = "download"
	FailureDownloadNetwork    = "download-network"
	FailureDownloadSpace      = "download-space"
	FailureDownloadPermission = "download-permission"
	FailureDownloadBusy       = "download-busy"
	FailureInstall            = "install"
	FailureInstallNetwork     = "install-network"
	FailureStopFailed         = "stop-failed"
	FailureTasksChanged       = "tasks-changed"
	FailureTasksUnavailable   = "tasks-unavailable"
	FailureFullscreenTimeout  = "fullscreen-timeout"
)

// FailureKinds 按官方 union 顺序返回 14 个 failure 取值。
func FailureKinds() []string {
	return []string{
		FailureConfiguration, FailureCheck, FailureCheckNetwork,
		FailureDownload, FailureDownloadNetwork, FailureDownloadSpace,
		FailureDownloadPermission, FailureDownloadBusy,
		FailureInstall, FailureInstallNetwork, FailureStopFailed,
		FailureTasksChanged, FailureTasksUnavailable, FailureFullscreenTimeout,
	}
}

// ProducibleFailures 返回本仓真的会产出的 failure（固定顺序）。
//
// 其余 11 个都要求"下载/安装"链路，本仓没有——返回它们就是假数据。
func ProducibleFailures() []string {
	return []string{FailureConfiguration, FailureCheck, FailureCheckNetwork}
}

// Presentation 是官方 `DesktopUpdatePresentation`（`types.ts:21-27`）。
//
// 字段与官方逐个对应；本仓永不填 `Percent`（没有下载进度可言）。
type Presentation struct {
	// Phase 见 Phase 的四个取值。
	Phase Phase `json:"phase"`
	// Version 只在 available 时有值（规范化写法，不带前导 v）。
	Version string `json:"version,omitempty"`
	// Percent 官方用于 downloading 的进度；本仓**永远 nil**。
	Percent *int `json:"percent,omitempty"`
	// Failure 只在 error 时有值；取值见 ProducibleFailures。
	Failure string `json:"failure,omitempty"`
	// ConfigurationError 是壳渠道的故障码（read/format/channel/cleanup）。
	ConfigurationError string `json:"configurationError,omitempty"`
}

// Release 是 GitHub `GET /repos/{owner}/{repo}/releases` 一条记录里我们用到的字段。
//
// 只要这五个：本仓不做下载，所以 `assets` 一个都不解析（解析了也没人用，
// 而"解析了没人用"的字段迟早会被当成"我们有下载能力"）。
type Release struct {
	TagName    string `json:"tag_name"`
	Name       string `json:"name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	HTMLURL    string `json:"html_url"`
}

// ChannelStateProvider 提供壳侧渠道的当前状态。
//
// 同一个包里的 `*Service`（channel.go）就是它；抽成接口是为了测试能塞一个假的，
// 而不是为了"将来可能换实现"。
type ChannelStateProvider interface {
	State() State
}

const (
	// defaultStatusTTL 是缓存有效期：TTL 内不再出网。
	defaultStatusTTL = 5 * time.Minute
	// defaultStatusTimeout 是单次 HTTP 往返的上限。
	defaultStatusTimeout = 10 * time.Second
	// defaultStatusRepo 是我们自己的发布仓（用户指定"走我们的 go 仓库更新"）。
	defaultStatusRepo = "MYCF711/dsh-go"
	// defaultGitHubAPIBase 是 GitHub REST 的正式地址（测试注入 httptest 的 URL）。
	defaultGitHubAPIBase = "https://api.github.com"
	// defaultWebBase 是网页地址前缀，用于没有具体 release 时的兜底入口。
	defaultWebBase = "https://github.com"
	// releasePageSize 是每页条数：只取最新一页就够（默认 30，我们不需要更早的）。
	releasePageSize = 30
	// maxReleaseBody 是响应体上限；超过就不是我们认识的形状，判 check。
	maxReleaseBody = 4 << 20
	// statusUserAgent 是必需的 UA（GitHub 对无 UA 的请求直接 403）。
	statusUserAgent = "dsh-go-updater"
)

// StatusOptions 是 NewStatusService 的参数。
type StatusOptions struct {
	// Version 是**运行中**的版本号。唯一出处是 `cmd/dshd/main.go` 的 version，
	// 由宿主启动时注入；本包不许自己抄一份字面量。
	Version string
	// Channel 是壳渠道来源（`desktopUpdate.channel.get` 的那份 Service）。必填。
	Channel ChannelStateProvider
	// HTTP 是出网客户端；nil = 10s 超时的新客户端。
	HTTP *http.Client
	// APIBase 是 REST 基址；空 = https://api.github.com（测试指向 httptest）。
	APIBase string
	// WebBase 是网页基址；空 = https://github.com。
	WebBase string
	// Repo 是 `owner/name`；空 = MYCF711/dsh-go。
	Repo string
	// Token 是可选只读凭据，用来抬 GitHub 的速率上限（匿名 60 次/小时/IP）。
	Token string
	// TTL 是缓存有效期；<=0 = 5 分钟。**不允许用 0 表示"不缓存"**：
	// 那会让"每调一次 status() 就打一次 GitHub"，正是要避免的形状。
	TTL time.Duration
	// Now 是可注入时钟（测试用）；nil = time.Now。
	Now func() time.Time
}

// StatusService 是更新状态的壳服务。
type StatusService struct {
	mu sync.Mutex

	version string
	channel ChannelStateProvider
	http    *http.Client
	apiBase string
	webBase string
	repo    string
	token   string
	ttl     time.Duration
	now     func() time.Time

	// cache 是上一次检查的结论；nil = 还没查过。
	cache *statusCache
	// inflight 是当前在飞的那一次检查；nil = 没有。单飞的唯一依据。
	inflight *statusFlight
	// subs 是订阅者；值是带缓冲的通道，发布时**不阻塞**（丢中间态而不是拖住别人）。
	subs    map[int]chan map[string]any
	nextSub int
}

// statusCache 是一次检查的完整结论。
type statusCache struct {
	at         time.Time
	pres       Presentation
	etag       string
	releaseURL string
}

// statusFlight 是一次在飞的检查：等它的人只看 done，结果从缓存里取。
type statusFlight struct{ done chan struct{} }

// NewStatusService 造一个更新状态服务（**不同步出网**）。
func NewStatusService(opts StatusOptions) (*StatusService, error) {
	version := strings.TrimSpace(opts.Version)
	if version == "" {
		return nil, errors.New("updater: 更新状态服务没有运行中版本——唯一出处是 cmd/dshd/main.go 的 version，由宿主启动注入")
	}
	if _, ok := ParseVersion(NormalizeTag(version)); !ok {
		return nil, fmt.Errorf("updater: 运行中版本 %q 解析不了（发布形态是 <上游主版本>-go.<N>）", version)
	}
	if opts.Channel == nil {
		return nil, errors.New("updater: 更新状态服务没有壳渠道来源（desktopUpdate.channel 的那份 Service）")
	}
	repo := strings.TrimSpace(opts.Repo)
	if repo == "" {
		repo = defaultStatusRepo
	}
	if strings.Count(repo, "/") != 1 {
		return nil, fmt.Errorf("updater: 仓库名 %q 不是 owner/name 形状", repo)
	}
	apiBase := strings.TrimRight(strings.TrimSpace(opts.APIBase), "/")
	if apiBase == "" {
		apiBase = defaultGitHubAPIBase
	}
	webBase := strings.TrimRight(strings.TrimSpace(opts.WebBase), "/")
	if webBase == "" {
		webBase = defaultWebBase
	}
	client := opts.HTTP
	if client == nil {
		client = &http.Client{Timeout: defaultStatusTimeout}
	}
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = defaultStatusTTL
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &StatusService{
		version: version,
		channel: opts.Channel,
		http:    client,
		apiBase: apiBase,
		webBase: webBase,
		repo:    repo,
		token:   strings.TrimSpace(opts.Token),
		ttl:     ttl,
		now:     now,
		subs:    map[int]chan map[string]any{},
	}, nil
}

// RunningVersion 返回注入的运行中版本（诊断用）。
func (s *StatusService) RunningVersion() string { return s.version }

// Status 是官方 `status()`：**不阻塞**。
//
// 为什么要这样：官方渲染层的 `status()` 拿的是"当前已知的状态"，真正的检查在后台跑，
// 结果通过 `subscribe` 推（`desktop-update-source.ts:22-31`）。所以：
//
//	缓存新鲜 -> 直接回缓存的结论（idle / available / error）
//	缓存过期且没人在查 -> 起一次检查，回 checking；下次 `status()` 就能拿到结论
//	缓存过期且已有人在查 -> 回 checking（**不叠加第二次请求**）
func (s *StatusService) Status() Presentation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.observeLocked()
}

// Refresh 是阻塞版：拿到**确定**的结论（或 ctx 取消时回当前已知状态）。
//
// `force` 为 true 时绕过 TTL——那是"用户点了"（`open()`）的语义；
// 即便如此也仍然走单飞，并发点击不会打出多个请求。
func (s *StatusService) Refresh(ctx context.Context, force bool) Presentation {
	s.mu.Lock()
	if p, blocked := s.configErrorLocked(); blocked {
		s.mu.Unlock()
		return p
	}
	if !force && s.cache != nil && s.now().Sub(s.cache.at) < s.ttl {
		p := s.cache.pres
		s.mu.Unlock()
		return p
	}
	if s.inflight == nil {
		s.startLocked()
	}
	flight := s.inflight
	s.mu.Unlock()

	select {
	case <-flight.done:
	case <-ctx.Done():
		// 调用方自己放弃了：如实回"还在查"，不编一个结论。
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.currentLocked()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentLocked()
}

// OpenResult 是 `open()` 的结果。
//
// 官方 `open(): Promise<void>` 不回值——它只是"打开更新入口"。本仓是 Go 宿主，
// 没有窗口可以自己弹，所以**把入口交出去**：URL 一定非空（最差是 releases 列表页）。
// 顺带回一份当前 presentation，界面不用再问一次。
type OpenResult struct {
	// URL 是更新入口：有更新时是那条 release 的页面，否则是 releases 列表页。
	URL string `json:"url"`
	// Channel 是本次检查实际用的渠道（用户选择，没选过则是打包渠道）。
	Channel Channel `json:"channel"`
	// Presentation 是这次检查的结论。
	Presentation Presentation `json:"presentation"`
}

// Open 是官方 `open()`：用户点了，所以**绕过 TTL 真查一次**，然后交出入口。
//
// 它不下载、不安装、不写盘——"打开入口"这件事本身是只读的。
func (s *StatusService) Open(ctx context.Context) OpenResult {
	pres := s.Refresh(ctx, true)
	s.mu.Lock()
	defer s.mu.Unlock()
	url := s.releasesPageURL()
	if pres.Phase == PhaseAvailable && s.cache != nil && s.cache.releaseURL != "" {
		url = s.cache.releaseURL
	}
	channel, _ := s.channelLocked()
	if channel == "" {
		channel = ChannelStable
	}
	return OpenResult{URL: url, Channel: channel, Presentation: pres}
}

// Invalidate 丢掉缓存：渠道变了（stable<->nightly 的过滤规则不同），结论就过期了。
//
// 谁调用：宿主在壳渠道变更时挂一个 `channel.Subscribe`（见 cmd/dshd/serve.go）。
func (s *StatusService) Invalidate() {
	s.mu.Lock()
	s.cache = nil
	s.mu.Unlock()
}

// ---------- 内部：状态推导 ----------

// observeLocked 是 `status()` 的内核。
func (s *StatusService) observeLocked() Presentation {
	if p, blocked := s.configErrorLocked(); blocked {
		return p
	}
	if s.cache != nil && s.now().Sub(s.cache.at) < s.ttl {
		return s.cache.pres
	}
	if s.inflight != nil {
		return Presentation{Phase: PhaseChecking}
	}
	s.startLocked()
	return Presentation{Phase: PhaseChecking}
}

// currentLocked 返回"此刻已知的状态"，不触发任何请求。
func (s *StatusService) currentLocked() Presentation {
	if p, blocked := s.configErrorLocked(); blocked {
		return p
	}
	if s.inflight != nil {
		return Presentation{Phase: PhaseChecking}
	}
	if s.cache != nil {
		return s.cache.pres
	}
	return Presentation{Phase: PhaseIdle}
}

// configErrorLocked 把壳渠道故障翻成配置失败态。
//
// 返回 true 表示"壳配置坏了，连查都不用查"——那是 `update-channel.json` 的问题，
// 不是网络的问题。
func (s *StatusService) configErrorLocked() (Presentation, bool) {
	_, code := s.channelLocked()
	if code == "" {
		return Presentation{}, false
	}
	return Presentation{
		Phase:              PhaseError,
		Failure:            FailureConfiguration,
		ConfigurationError: code,
	}, true
}

// channelLocked 解析"本次检查按哪个渠道过滤"，顺带给出壳配置故障码。
//
// 没选过渠道时用**打包渠道**（本构建自带的那个）：更新检查总得按某个渠道走，
// 而打包渠道是这个构建自己的事实（`desktopUpdate.channel.get` 那边照旧回 null，
// 界面仍然显示"配置不可用"——两个面各说各的事实，不互相伪造）。
func (s *StatusService) channelLocked() (Channel, string) {
	state := s.channel.State()
	if state.Error != "" {
		return "", state.Error
	}
	if state.Channel != nil && state.Channel.Valid() {
		return *state.Channel, ""
	}
	if state.PackagedChannel.Valid() {
		return state.PackagedChannel, ""
	}
	return ChannelStable, ""
}

// startLocked 起一次检查（调用方持锁；只在 inflight == nil 时调用）。
func (s *StatusService) startLocked() {
	prev := s.cache
	etag := ""
	if prev != nil {
		etag = prev.etag
	}
	flight := &statusFlight{done: make(chan struct{})}
	s.inflight = flight
	s.publishLocked(Presentation{Phase: PhaseChecking})
	go func() {
		pres, nextETag, releaseURL := s.check(etag, prev)
		s.mu.Lock()
		s.cache = &statusCache{at: s.now(), pres: pres, etag: nextETag, releaseURL: releaseURL}
		s.inflight = nil
		s.publishLocked(pres)
		s.mu.Unlock()
		close(flight.done)
	}()
}

// publishLocked 把一帧推给所有订阅者（持锁调用）。
//
// 与 channel.go 的 notifyLocked 不同，这里**不起 goroutine**：订阅者拿的是带缓冲的
// 通道，塞不进去就丢这一帧。这是状态不是事件流——只需保证"最后一帧是真的"。
func (s *StatusService) publishLocked(p Presentation) {
	if len(s.subs) == 0 {
		return
	}
	frame := presentationWire(p)
	for _, ch := range s.subs {
		select {
		case ch <- frame:
		default:
		}
	}
}

// check 出网查一次，把结果翻成 presentation。
//
// 返回的 etag/releaseURL 直接写进缓存；失败时 etag 清空（否则下一次的 304 会让我们
// 复用一份**失败**的结论），releaseURL 保留（`open()` 的入口还指向那条 release）。
func (s *StatusService) check(prevETag string, prev *statusCache) (Presentation, string, string) {
	keepURL := ""
	if prev != nil {
		keepURL = prev.releaseURL
	}
	channel, code := s.channelLocked()
	if code != "" {
		// 检查途中渠道坏了：如实报配置失败，别把这条算成"检查失败"。
		return Presentation{Phase: PhaseError, Failure: FailureConfiguration, ConfigurationError: code}, prevETag, keepURL
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.httpTimeout())
	defer cancel()
	endpoint := s.apiBase + "/repos/" + s.repo + "/releases?per_page=" + strconv.Itoa(releasePageSize)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Presentation{Phase: PhaseError, Failure: FailureCheck}, "", keepURL
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", statusUserAgent)
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	if prevETag != "" {
		req.Header.Set("If-None-Match", prevETag)
	}

	resp, err := s.http.Do(req)
	if err != nil {
		// DNS / 拨号 / TLS / 超时 / 重定向环：一次往返都没走完 -> 网络那一档。
		return Presentation{Phase: PhaseError, Failure: FailureCheckNetwork}, "", keepURL
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusNotModified:
		if prev == nil {
			// 我们没带校验器却收到 304：服务端行为不可信，当检查失败。
			return Presentation{Phase: PhaseError, Failure: FailureCheck}, "", ""
		}
		return prev.pres, prev.etag, prev.releaseURL
	case resp.StatusCode != http.StatusOK:
		// 4xx/5xx：拿得到答复，但拿不到可用的答案。官方分类器只看 message 是否命中
		// 网络正则，HTTP 状态码本身不算网络失败——所以这一档归 `check`。
		return Presentation{Phase: PhaseError, Failure: FailureCheck}, "", keepURL
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxReleaseBody+1))
	if err != nil {
		return Presentation{Phase: PhaseError, Failure: FailureCheckNetwork}, "", keepURL
	}
	if len(raw) > maxReleaseBody {
		return Presentation{Phase: PhaseError, Failure: FailureCheck}, "", keepURL
	}
	var releases []Release
	if err := json.Unmarshal(raw, &releases); err != nil {
		// 解析不了：官方把这一档归 `check`（不是网络问题）。
		return Presentation{Phase: PhaseError, Failure: FailureCheck}, "", keepURL
	}

	pres, releaseURL := s.present(releases, channel)
	return pres, resp.Header.Get("ETag"), releaseURL
}

// present 从 release 列表算出状态。
func (s *StatusService) present(releases []Release, channel Channel) (Presentation, string) {
	var best *Release
	for i := range releases {
		candidate := &releases[i]
		if !s.accepts(candidate, channel) {
			continue
		}
		if best == nil {
			best = candidate
			continue
		}
		if cmp, ok := CompareVersions(NormalizeTag(candidate.TagName), NormalizeTag(best.TagName)); ok && cmp > 0 {
			best = candidate
		}
	}
	if best == nil {
		return Presentation{Phase: PhaseIdle}, ""
	}
	bestVersion := NormalizeTag(best.TagName)
	cmp, ok := CompareVersions(bestVersion, NormalizeTag(s.version))
	if !ok {
		// release 的 tag 解析不了：这不是"已是最新"，是件说不清的事，如实报检查失败。
		return Presentation{Phase: PhaseError, Failure: FailureCheck}, ""
	}
	if cmp > 0 {
		return Presentation{Phase: PhaseAvailable, Version: bestVersion}, best.HTMLURL
	}
	return Presentation{Phase: PhaseIdle}, ""
}

// accepts 判断一条 release 算不算本渠道的候选。
func (s *StatusService) accepts(r *Release, channel Channel) bool {
	if r.Draft {
		// draft 在官方语义里**还没发布**，任何人都看不见，绝不能拿来当更新。
		return false
	}
	if !IsOurReleaseTag(r.TagName) {
		// 不是我们自己的发布命名（例如上游的 `-rc.N`）。本仓的版本线由用户
		// 2026-11-13 裁定：`.N` 是我们的发版序号、不因上游换 rc 而重置，
		// 所以拿一个非我们命名的 tag 来比版本会把上游 rc 误判成"新版本"。
		return false
	}
	if channel == ChannelStable {
		// 稳定通道只看正式版：GitHub 的 prerelease 标记与 `-gobeta.N` 命名都排除。
		if r.Prerelease || IsBetaTag(r.TagName) {
			return false
		}
	}
	return true
}

// httpTimeout 取客户端自己的超时（0 表示没设，退回默认，别让一次请求永远挂着）。
func (s *StatusService) httpTimeout() time.Duration {
	if s.http != nil && s.http.Timeout > 0 {
		return s.http.Timeout
	}
	return defaultStatusTimeout
}

// releasesPageURL 是没有具体 release 时的兜底入口。
func (s *StatusService) releasesPageURL() string {
	return s.webBase + "/" + s.repo + "/releases"
}

// ---------- 下面三个方法是 transport.UpdateStatusService 的适配层 ----------

// StatusView 是 `status()` 的线上形状（官方 `DesktopUpdatePresentation`）。
func (s *StatusService) StatusView() map[string]any { return presentationWire(s.Status()) }

// OpenView 是 `open()` 的线上形状。
func (s *StatusService) OpenView(ctx context.Context) map[string]any {
	result := s.Open(ctx)
	return map[string]any{
		"url":          result.URL,
		"channel":      string(result.Channel),
		"presentation": presentationWire(result.Presentation),
	}
}

// WireSubscribe 订阅状态变化，返回"帧通道 + 取消函数"。
//
// 为什么返回通道而不是回调（与 channel.go 的 `WireSubscribe` 不同）：这里能省掉一个
// 扇出 goroutine——发布方在锁内做一次非阻塞投递即可，而回调形状必须出锁起 goroutine
// 才能避免"持锁调用别人的代码"。通道形状下丢的是中间帧，不是结论（最后一帧一定送到）。
//
// 首帧是当前状态：官方 `subscribe` 的消费者拿到的是"当前值 + 后续变更"，
// 界面上来就要能画（与 desktopUpdate.channel.subscribe 同一条规矩）。
//
// 注意：订阅**不触发检查**。只有 `status()` / `open()` 会出网，
// 这样"谁在观察"与"谁发起请求"是两件事，容易核对。
func (s *StatusService) WireSubscribe() (<-chan map[string]any, func()) {
	ch := make(chan map[string]any, 8)
	s.mu.Lock()
	id := s.nextSub
	s.nextSub++
	s.subs[id] = ch
	first := presentationWire(s.currentLocked())
	s.mu.Unlock()

	select {
	case ch <- first:
	default:
	}

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			s.mu.Lock()
			delete(s.subs, id)
			s.mu.Unlock()
		})
	}
}

// presentationWire 把 Presentation 转成线上形状。
//
// 空字段**不出现**（官方那几个字段都是可选的），但 `phase` 永远在。
func presentationWire(p Presentation) map[string]any {
	out := map[string]any{"phase": string(p.Phase)}
	if p.Version != "" {
		out["version"] = p.Version
	}
	if p.Percent != nil {
		out["percent"] = *p.Percent
	}
	if p.Failure != "" {
		out["failure"] = p.Failure
	}
	if p.ConfigurationError != "" {
		out["configurationError"] = p.ConfigurationError
	}
	return out
}

// 编译期确认：`*Service`（渠道服务）就是状态服务要的渠道来源。
var _ ChannelStateProvider = (*Service)(nil)
