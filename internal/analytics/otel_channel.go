package analytics

// 官方 #37 `otel` 的宿主形状：**每消费者一条独立 OTLP 通道**。
//
// 官方原件（只读）：
//   - `@deepseek-ai/dsh-otel/README.md`:27-40 —— "Each channel has its own exporter,
//     resource, instrumentation scope, and queue"；"Call `ctx.otel.createEventReporter(options)`
//     for ordinary analytics or `ctx.otel.createSessionLogReporter(options)` for complete
//     Session events"；"A Session channel never shares a request or queue with ordinary events"。
//   - `src/index.ts:22` `createEventReporter(options)`（普通事件，SDK 按 count 批）、
//     `:30` `createSessionLogReporter(options)`（会话日志，一条事件一条 record）。
//     options 显式给 endpoint / headers / resourceAttributes / scope / processor / onFailure；
//     服务本身**没有任何部署默认值**，也不替消费者采集。
//   - `src/event-log.ts:53-64` 每个通道一个 `LoggerProvider` + `BatchLogRecordProcessor`
//     （count 批），`:73` `emit()` 入队即返回（不确认送达），`:83` `shutdown(signal)`
//     在 signal abort 时取消请求与重试等待，并在 transport 收尾后 resolve。
//   - `src/session-log.ts:12` `SESSION_LOG_MAX_REQUEST_BYTES = 4_000_000`（未压缩字节，
//     含 OTLP 信封；只允许调低）；`:87-115` 队列满 → 拒收并诊断、单条超限 → 拒收
//     **不截断**；`:126-145` 一次只飞一个请求，等 export 队列清空再发下一批；
//     `:210-218` 一条事件一条 record，`eventName: "session-log"`、`body: "session-log"`、
//     `attributes.sessionId` 与 `attributes.content`（完整事件信封的 JSON **字符串**）。
//   - `src/transport.ts:52-55` headers 是显式的：`Content-Type: application/json`
//     之后再叠消费者自己的头（消费者可覆盖），"does not inherit ambient authorization
//     headers or TLS identity"。
//   - `src/transport.ts:41` "Resolve collector-local headers and agents with shared SDK
//     timeout and **compression** defaults"；`src/event-transport.ts:26,31-32`
//     `const compressed = config.compression === 'gzip'` → 请求体 `gzipSync(data)` + `'content-encoding': 'gzip'`。
//     默认（未配）= `none`，请求体就是明文 JSON；`LogChannelOptions.Compression` 是它的
//     宿主形状（校验见 normalizeLogChannelOptions，编码点见 send）。
//
// 为什么每条通道都要自己的 http.Client/队列/资源/scope：官方 README 那句
// "no state is shared with other channels"。共用一份 client 或队列会让"一个消费者
// 的 collector 挂了拖住另一个消费者"变成结构上可能的事，也会让 scope 归属漂移。
//
// 与官方的**唯一**差异（明写，不藏着）：product analytics 这条消费者在宿主里是
// **同步结算**的（它有自己的 pending-export 队列与 `Exported` 计数，要让
// `Report` 如实回答"这条到底发出去了没有"），所以它用同一条 event 通道的请求面
// 同步发送；异步按 count 批的 `EventReporter` 由 `CreateEventReporter` 提供给
// 其它消费者。两者共用同一套编码/重试/计数/诊断。

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 通道默认值与官方 SDK 对齐（官方 product adapter README 的默认表、
// session-log.ts:65-68 / :195）。
const (
	// DefaultMaxQueueSize 是单通道队列上限（官方 2048）：满了丢新记录并诊断。
	DefaultMaxQueueSize = 2048
	// DefaultMaxExportBatchSize 是单请求记录上限（官方 512），不得大于队列上限。
	DefaultMaxExportBatchSize = 512
	// DefaultScheduledDelay 是"部分批次"最长等待（官方 session 通道 1000ms）。
	DefaultScheduledDelay = 1000 * time.Millisecond
	// DefaultExportTimeout 是单请求看门狗（官方 30000ms）：到点只警告，不谎报成功。
	DefaultExportTimeout = 30000 * time.Millisecond
	// DefaultSessionLogMaxRequestBytes 是会话日志单请求上限（官方 4,000,000，只准调低）。
	DefaultSessionLogMaxRequestBytes = 4_000_000
)

// 请求体压缩方式（官方 `LogExporterOptions.compression` 的两个合法取值）。
const (
	// CompressionNone 是默认值：请求体是明文 OTLP JSON（官方"默认不压缩"）。
	CompressionNone = "none"
	// CompressionGzip 让通道 gzip 请求体并带 `content-encoding: gzip`
	// （官方 `src/event-transport.ts:26,31-32`）。
	CompressionGzip = "gzip"
)

// 传输层的本地常量：与 Service 既有 exporter（service.go 的 postCollector）同口径，
// 本任务不放松任何一条（重试 3 次、退避 100ms 起、响应体最多读 1MiB）。
const (
	otlpChannelExportAttempts = 3
	otlpRetryBackoff          = 100 * time.Millisecond
	otlpResponseBodyLimit     = 1 << 20
)

// 本仓 OTLP resource 的应用身份。版本字面量只允许出现在 cmd/dshd/main.go 与
// cmd/dshtui/brand.go（见 cmd/dshd/version_guard_test.go），所以 analytics 包
// **不带** service.version；`service.name` 用本仓命令 token（AGENTS.md 的命名裁定）。
const analyticsServiceName = "dsh-go"

// LogChannelOptions 是一条 OTLP 日志通道的显式配置（官方 dsh-otel 的 options）。
//
// 与官方一致：**没有**任何部署默认端点、不继承环境里的认证头/TLS 身份；
// endpoint/headers/resource/scope 全由消费者给。
type LogChannelOptions struct {
	// Endpoint 是完整 HTTP(S) logs 目的地；无论有没有 /v1/logs 后缀都如实使用。
	Endpoint string
	// Headers 是该通道自己的请求头（例如 collector 的 `x-channel`）；
	// Content-Type: application/json 由通道设置，这里的同名头可覆盖。
	Headers map[string]string
	// ResourceAttributes 是该通道自己的 OTLP resource 属性（有序）。
	ResourceAttributes []LogAttribute
	// Scope 是该通道自己的 instrumentation scope（名字由消费者负责）。
	Scope InstrumentationScope
	// MaxQueueSize 是队列上限；0 用 DefaultMaxQueueSize。
	MaxQueueSize int
	// MaxExportBatchSize 是单请求记录上限；0 用 DefaultMaxExportBatchSize。
	MaxExportBatchSize int
	// ScheduledDelay 是部分批次最长等待；0 用 DefaultScheduledDelay。
	ScheduledDelay time.Duration
	// ExportTimeout 是单请求看门狗；0 用 DefaultExportTimeout。
	ExportTimeout time.Duration
	// Compression 是请求体压缩方式（官方 `LogExporterOptions.compression`，
	// 取值 `none` | `gzip`；零值/空 = `none`）。
	//
	// 官方默认就是不压缩（`src/event-transport.ts:26` 只认字面量 'gzip'），所以
	// 零值必须逐字节等于"没有这个功能之前"的请求：不设 `Content-Encoding`、
	// 载荷就是明文 OTLP JSON。配 `gzip` 时按官方形状 `gzipSync(data)` 压请求体
	// 并设 `content-encoding: gzip`。
	//
	// 与 `maxRequestBytes` 的关系（官方 `SESSION_LOG_MAX_REQUEST_BYTES = 4_000_000`
	// 是**未压缩**字节）：字节上限始终按**压缩前**的载荷算，压缩不会让一条超大
	// 记录偷偷过闸。
	Compression string
	// OnFailure 是失败诊断回调（官方 onFailure：报"记录被拒/传输失败"，绝不记录内容）。
	// 为 nil 时通道写到 stderr 一次——丢记录必须有声音，静默成功是本仓禁止的形态。
	OnFailure func(message string, err error)
	// HTTPClient 是该通道自己的 client；为 nil 时通道自建一个（含独立 Transport）。
	// 传进来也不会被其它通道共享：官方要求"每条通道自己的 agent/连接池"。
	HTTPClient *http.Client

	// onOutbound 是 exporter 自观测计数点（官方 exporter metrics recorder 的位置），
	// 供宿主把真实 HTTP 尝试次数报到 Service.OutboundRequests（默认 local 必须为 0）。
	onOutbound func()
	// sessionMode 是内部标记：会话日志通道（按字节上限排批 + 单条超限拒收）。
	sessionMode bool
	// maxRequestBytes 是单请求字节上限（0 = 不限，普通事件通道就是 0）。
	maxRequestBytes int
}

// SessionLogChannelOptions 是会话日志通道的配置（官方 SessionLogOptions）。
type SessionLogChannelOptions struct {
	LogChannelOptions
	// MaxRequestBytes 是单请求未压缩字节上限；0 用官方默认 4,000,000，
	// 只允许调低（官方 resolveSessionLogLimits 的硬边界）。
	MaxRequestBytes int
}

// OTelLogRecord 是一条普通事件日志（官方 dsh-otel `OTelEventRecord` 的宿主形状）。
//
// Body 是"人类可读摘要"：官方明确要求它**永远不是** prompt/回答/凭据/文件内容；
// 本仓产品事件的摘要就是事件名本身，属性只来自白名单（见 redact.go）。
type OTelLogRecord struct {
	// EventName 是事件名（进 OTLP 的 eventName 字段，不混进 attributes）。
	EventName string
	// Body 是摘要文本。
	Body string
	// Timestamp 是事件发生时间，Unix 毫秒（观察时间由通道在入队时补）。
	Timestamp float64
	// SeverityNumber 省略时用 INFO（与官方 emit 默认一致）。
	SeverityNumber int
	// Attributes 是调用方选定、已脱敏的属性（有序；值只能是标量或一层对象）。
	Attributes []LogAttribute
}

// SessionLogRecord 是一条完整会话事件（官方 dsh-otel `SessionLogRecord` 的宿主形状）。
type SessionLogRecord struct {
	// SessionID 是 collector 侧的会话身份（编码进 attributes.sessionId）。
	SessionID string
	// Event 是完整事件信封（data 是采集策略给出的已脱敏副本）；编码成
	// attributes.content 的 JSON 字符串。
	Event any
	// Timestamp 是事件时间，Unix 毫秒；0 表示"现在"。
	Timestamp float64
	// Attributes 是附加采集元数据（sessionId/content 永远由通道自己写）。
	Attributes []LogAttribute
	// SeverityNumber 省略时用 INFO。
	SeverityNumber int
}

// ChannelStats 是一条通道的可观测计数。
//
// 官方把"队列溢出 / 单条超限 / 传输失败丢记录"当可接受行为（dsh-otel README:70-73
// "Queue overflow, oversized Session events, transport failure ... can lose records"），
// 但**丢了必须看得见**：这组计数 + 每类失败一条诊断就是"看得见"的落地形态。
type ChannelStats struct {
	// Accepted 是成功入队（或同步发送成功）的记录数。
	Accepted int64
	// Exported 是真正被 collector 2xx 接受的记录数。
	Exported int64
	// Dropped 是没送达就丢掉的记录数（队列溢出 / 传输失败 / 关闭时放弃）。
	Dropped int64
	// Rejected 是入队即拒收的记录数（单条超限 / 序列化失败 / 通道已停）。
	Rejected int64
	// Failures 是"整条请求重试完仍失败"的次数（异步通道此时还会叠加 Dropped；
	// 同步消费者此时记录仍在待发队列里，下一条 Report 会继续补发）。
	Failures int64
	// Outbound 是真实 HTTP 尝试次数（含重试）。
	Outbound int64
}

// queuedLog 是队列里的一条记录 + 它的保守字节估算（含 resource/scope 信封）。
type queuedLog struct {
	record otlpLogRecord
	bytes  int
}

// otlpChannel 是一条**自有**通道：自己的 endpoint/headers/resource/scope/队列/
// client/发送 goroutine。除显式传给它的参数外，不读任何全局状态。
type otlpChannel struct {
	opts   LogChannelOptions
	client *http.Client
	// transport 只在通道自建 client 时非 nil；关闭时释放空闲连接。
	transport *http.Transport

	// abortCtx 是"取消在飞请求与重试等待"的唯一开关（shutdown 的 deadline 到点 /
	// stopPending 都走它）。发送与重试都从它派生 context。
	abortCtx    context.Context
	abortCancel context.CancelFunc

	// wake 是入队/停止的唤醒信号（缓冲 1：重复唤醒会被合并，不丢唤醒）。
	wake chan struct{}
	// done 在发送 goroutine 退出时关闭（只由 pump 关闭一次）。
	done chan struct{}

	mu            sync.Mutex
	queue         []queuedLog
	queuedBytes   int
	firstQueuedAt time.Time
	stopped       bool
	shutdownDone  bool

	accepted atomic.Int64
	exported atomic.Int64
	dropped  atomic.Int64
	rejected atomic.Int64
	failures atomic.Int64
	outbound atomic.Int64

	warnMu sync.Mutex
	warned map[string]struct{}
}

// newOTLPChannel 构造一条通道并（按需）启动它的发送 goroutine。
//
// startPump=false 用于"只同步发送"的消费者（product analytics 的结算路径）：
// 它不排队、不需要后台发送者，因此也就不多一条 goroutine。
func newOTLPChannel(opts LogChannelOptions, startPump bool) (*otlpChannel, error) {
	channel := &otlpChannel{
		opts:   opts,
		wake:   make(chan struct{}, 1),
		done:   make(chan struct{}),
		warned: map[string]struct{}{},
	}
	if opts.HTTPClient != nil {
		channel.client = opts.HTTPClient
	} else {
		// 每条通道一个独立 Transport：连接池不共享（官方 exporter 自己持有 agent）。
		channel.transport = &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			MaxIdleConns:        4,
			IdleConnTimeout:     30 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
			ForceAttemptHTTP2:   true,
		}
		channel.client = &http.Client{Transport: channel.transport, Timeout: 0}
	}
	channel.abortCtx, channel.abortCancel = context.WithCancel(context.Background())
	if startPump {
		go channel.pump()
	} else {
		// 没有后台发送者（同步消费者）：done 立刻关闭、也不接受入队，
		// shutdown 不会等谁。
		channel.stopped = true
		channel.shutdownDone = true
		close(channel.done)
	}
	return channel, nil
}

// normalizeLogChannelOptions 校验并补齐一条通道的配置。
//
// 非法配置必须 fail-loud（官方 "Invalid byte limits, non-positive queue/timer
// values, maxExportBatchSize > maxQueueSize, invalid endpoints ... fail at load"）：
// 静默回落成默认值等于把用户的显式配置悄悄改掉。
func normalizeLogChannelOptions(kind string, opts LogChannelOptions) (LogChannelOptions, error) {
	endpoint, err := otlpLogsEndpoint(opts.Endpoint)
	if err != nil {
		return LogChannelOptions{}, fmt.Errorf("analytics %s: %w", kind, err)
	}
	opts.Endpoint = endpoint
	if strings.TrimSpace(opts.Scope.Name) == "" {
		return LogChannelOptions{}, fmt.Errorf("analytics %s: instrumentation scope 的 name 不能为空", kind)
	}
	if opts.MaxQueueSize == 0 {
		opts.MaxQueueSize = DefaultMaxQueueSize
	}
	if opts.MaxExportBatchSize == 0 {
		opts.MaxExportBatchSize = DefaultMaxExportBatchSize
	}
	if opts.ScheduledDelay == 0 {
		opts.ScheduledDelay = DefaultScheduledDelay
	}
	if opts.ExportTimeout == 0 {
		opts.ExportTimeout = DefaultExportTimeout
	}
	if opts.MaxQueueSize < 1 || opts.MaxExportBatchSize < 1 {
		return LogChannelOptions{}, fmt.Errorf("analytics %s: 队列/批次上限必须为正（queue=%d batch=%d）",
			kind, opts.MaxQueueSize, opts.MaxExportBatchSize)
	}
	if opts.MaxExportBatchSize > opts.MaxQueueSize {
		return LogChannelOptions{}, fmt.Errorf("analytics %s: MaxExportBatchSize(%d) 不得大于 MaxQueueSize(%d)",
			kind, opts.MaxExportBatchSize, opts.MaxQueueSize)
	}
	if opts.ScheduledDelay < 0 || opts.ExportTimeout < 0 {
		return LogChannelOptions{}, fmt.Errorf("analytics %s: 定时/看门狗必须为正", kind)
	}
	if opts.maxRequestBytes < 0 {
		return LogChannelOptions{}, fmt.Errorf("analytics %s: maxRequestBytes 不能为负", kind)
	}
	// 压缩方式：官方只认 `gzip`（其余按 none），但**静默**把拼错的值当成
	// "不压缩"等于把用户的显式配置悄悄改掉——所以不认识的值 fail-loud。
	switch strings.ToLower(strings.TrimSpace(opts.Compression)) {
	case "", CompressionNone:
		opts.Compression = CompressionNone
	case CompressionGzip:
		opts.Compression = CompressionGzip
	default:
		return LogChannelOptions{}, fmt.Errorf(
			"analytics %s: Compression=%q 不支持（合法值：%q / %q）",
			kind, opts.Compression, CompressionNone, CompressionGzip)
	}
	if opts.onOutbound == nil {
		opts.onOutbound = func() {}
	}
	return opts, nil
}

// otlpLogsEndpoint 校验 OTLP/HTTP 端点并规范路径。
//
// 官方把 endpoint 定义成"完整 HTTP(S) logs URL"（dsh-otel README / transport.ts:16-19），
// 所以这里**原样保留**已给的路径；只有在根本没有路径（或只有 `/`）时才补上
// OTLP/HTTP 的约定路径 `/v1/logs`——否则请求会打到站点的根，被当成网页请求。
func otlpLogsEndpoint(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("endpoint 不能为空")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("endpoint 不是合法 URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("endpoint 必须是 http/https，收到 %q", raw)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("endpoint 缺主机: %q", raw)
	}
	if parsed.Path == "" || parsed.Path == "/" {
		parsed.Path = "/v1/logs"
	}
	return parsed.String(), nil
}

// ---------------------------------------------------------------------------
// 发送 goroutine
// ---------------------------------------------------------------------------

// pump 是通道**唯一**的后台 goroutine：按 count/字节把队列排成请求，串行发送。
//
// 并发约定（见 scripts/race-goroutines.json 的登记）：
//   - 队列、时间戳、stopped 全在 c.mu 下读写；pump 是唯一出队方；
//   - 入队方只 append + signal(wake)，不碰出队；
//   - wake 是缓冲 1 的 channel，重复唤醒被合并（不会丢唤醒，也不会堆积）；
//   - done 只由本 goroutine 关闭；退出路径只有"已 stopped 且队列空"或 abort。
func (c *otlpChannel) pump() {
	defer close(c.done)
	for {
		c.mu.Lock()
		stopped := c.stopped
		batch, wait := c.planLocked(time.Now())
		c.mu.Unlock()
		if len(batch) > 0 {
			c.deliver(batch)
			continue
		}
		if stopped {
			return
		}
		if !c.sleep(wait) {
			return
		}
	}
}

// planLocked 决定"现在能发哪一批"和"否则最多等多久"。
//
// 时长语义与官方 BatchLogRecordProcessor 一致：队列到 MaxExportBatchSize、
// 或单请求字节到上限、或队列里最老那条已等满 ScheduledDelay 就发；
// 关闭中（stopped）不受阈值约束，有多少发多少（官方 shutdown 会 drain）。
func (c *otlpChannel) planLocked(now time.Time) ([]queuedLog, time.Duration) {
	if len(c.queue) == 0 {
		// 空队列：只等唤醒（wait 为负 = 不设定时器）。
		return nil, -1
	}
	due := c.firstQueuedAt.Add(c.opts.ScheduledDelay)
	ready := c.stopped ||
		len(c.queue) >= c.opts.MaxExportBatchSize ||
		(c.opts.maxRequestBytes > 0 && c.queuedBytes >= c.opts.maxRequestBytes) ||
		!now.Before(due)
	if !ready {
		return nil, due.Sub(now)
	}
	limit := c.opts.MaxExportBatchSize
	if c.stopped {
		limit = len(c.queue)
	}
	count := 0
	bytes := 0
	for count < limit && count < len(c.queue) {
		next := c.queue[count]
		if c.opts.maxRequestBytes > 0 && count > 0 && bytes+next.bytes > c.opts.maxRequestBytes {
			break
		}
		bytes += next.bytes
		count++
	}
	batch := make([]queuedLog, count)
	copy(batch, c.queue[:count])
	c.queue = append([]queuedLog(nil), c.queue[count:]...)
	c.queuedBytes -= bytes
	if len(c.queue) == 0 {
		c.firstQueuedAt = time.Time{}
	} else {
		c.firstQueuedAt = now
	}
	return batch, 0
}

// sleep 等"唤醒 / 定时 / abort"。返回 false = 该退出（abort，剩下的队列丢弃并计数）。
func (c *otlpChannel) sleep(wait time.Duration) bool {
	var timer *time.Timer
	var timerC <-chan time.Time
	if wait >= 0 {
		timer = time.NewTimer(wait)
		timerC = timer.C
		defer timer.Stop()
	}
	select {
	case <-c.wake:
		return true
	case <-timerC:
		return true
	case <-c.abortCtx.Done():
		// 取消在飞请求/重试等待之后，队列里剩下的记录不可能再送达：丢掉并计数，
		// 让"丢了多少"在统计里看得见（官方也是"expiry discards the remaining queue"）。
		c.discardQueue()
		return false
	}
}

// discardQueue 清空队列并把丢掉的条数记进 Dropped。
func (c *otlpChannel) discardQueue() {
	c.mu.Lock()
	dropped := len(c.queue)
	c.queue = nil
	c.queuedBytes = 0
	c.firstQueuedAt = time.Time{}
	c.mu.Unlock()
	if dropped > 0 {
		c.dropped.Add(int64(dropped))
		c.failOnce("shutdown", "analytics: OTLP 通道关闭时丢弃了未发出的记录", nil)
	}
}

// signal 唤醒 pump（缓冲 1，重复唤醒合并）。
func (c *otlpChannel) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// deliver 串行发送一批记录：成功计入 Exported，失败整批丢弃并计数 + 一条诊断。
//
// 官方 Session 处理器"一次只飞一个请求、等 export 队列清空再发下一批"的形状由
// 这里的串行 pump 天然满足（pump 只有一条，deliver 返回前不会取下一批）。
func (c *otlpChannel) deliver(batch []queuedLog) {
	records := make([]otlpLogRecord, len(batch))
	for index := range batch {
		records[index] = batch[index].record
	}
	ctx, cancel := context.WithTimeout(c.abortCtx, c.opts.ExportTimeout)
	defer cancel()
	if err := c.send(ctx, records); err != nil {
		c.dropped.Add(int64(len(records)))
		c.failOnce("export", "analytics: OTLP 导出失败，本批记录已丢弃", err)
		return
	}
	c.exported.Add(int64(len(records)))
}

// gzipOTLPPayload 按官方 `gzipSync(data)` 的语义压 OTLP 请求体。
//
// 只有配了 `Compression: gzip` 才会走到这里；默认（none）的请求逐字节不变。
// 用标准库 compress/gzip，不引第三方（go.mod 零第三方依赖的纪律）。
func gzipOTLPPayload(payload []byte) ([]byte, error) {
	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	if _, err := writer.Write(payload); err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("analytics: gzip 压缩 OTLP 请求体失败: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("analytics: gzip 收尾失败: %w", err)
	}
	return buf.Bytes(), nil
}

// send 编码并 POST 一条 OTLP 请求（同步、含重试）。Service 的结算路径也走它。
func (c *otlpChannel) send(ctx context.Context, records []otlpLogRecord) error {
	if len(records) == 0 {
		return nil
	}
	payload, err := marshalOTLPLogs(c.opts.ResourceAttributes, c.opts.Scope, records)
	if err != nil {
		return err
	}
	if c.opts.maxRequestBytes > 0 && len(payload) > c.opts.maxRequestBytes {
		// 排批逻辑已保证单请求不超限；走到这里说明分片算错了，必须报错而不是硬发。
		return fmt.Errorf("analytics: OTLP 请求 %d 字节超过通道上限 %d", len(payload), c.opts.maxRequestBytes)
	}
	// 压缩只做一次：重试重发的必须是**同一份字节**（官方 exporter 也是编码一次、
	// 之后每次尝试复用同一份 buffer）。
	body := payload
	if c.opts.Compression == CompressionGzip {
		compressed, err := gzipOTLPPayload(payload)
		if err != nil {
			return err
		}
		body = compressed
	}
	var lastErr error
	for attempt := 0; attempt < otlpChannelExportAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.opts.Endpoint, bytesReader(body))
		if err != nil {
			return err
		}
		// 官方 transport.ts:52-55：先 Content-Type，再叠消费者自己的头（可覆盖）。
		request.Header.Set("Content-Type", "application/json")
		for name, value := range c.opts.Headers {
			request.Header.Set(name, value)
		}
		// 官方 event-transport.ts:31-32 的顺序相反：`{...headers, 'content-encoding':
		// 'gzip'}` —— 压缩头**压过**消费者同名头，否则会出现"声明 gzip 却发了明文"。
		if c.opts.Compression == CompressionGzip {
			request.Header.Set("Content-Encoding", "gzip")
		}
		c.outbound.Add(1)
		c.opts.onOutbound()
		response, err := c.client.Do(request)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, otlpResponseBodyLimit))
			response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				return nil
			}
			lastErr = fmt.Errorf("analytics: OTLP collector 返回 %d", response.StatusCode)
		} else {
			lastErr = err
		}
		if attempt < otlpChannelExportAttempts-1 {
			select {
			case <-ctx.Done():
				c.failures.Add(1)
				return ctx.Err()
			case <-time.After(otlpRetryBackoff * time.Duration(attempt+1)):
			}
		}
	}
	c.failures.Add(1)
	return lastErr
}

// sendSync 是**同步消费者**的发送面：不经过队列，但共用同一套编码/重试/计数。
//
// product analytics 的结算语义必须是同步的（它有 pending-export 队列与
// `Exported` 计数，要如实回答"这条到底发出去了没有"），所以它用这条路径；
// 异步按 count 批的消费者用 EventReporter.Emit → 队列 → deliver → send。
func (c *otlpChannel) sendSync(ctx context.Context, records []otlpLogRecord) error {
	ctx, cancel := context.WithTimeout(ctx, c.opts.ExportTimeout)
	defer cancel()
	if err := c.send(ctx, records); err != nil {
		c.failOnce("export", "analytics: OTLP 导出失败，本批记录留在待发队列", err)
		return err
	}
	c.accepted.Add(int64(len(records)))
	c.exported.Add(int64(len(records)))
	return nil
}

// failOnce 是"每类失败只记一条日志"的诊断出口（官方 onFailure 的调用点）。
//
// 计数在调用方各自 ++；这里只保证日志不刷屏（官方 session 处理器也是
// "one rejection diagnostic without truncation"）。
func (c *otlpChannel) failOnce(kind, message string, err error) {
	if c.opts.OnFailure == nil {
		return
	}
	c.warnMu.Lock()
	seen := false
	if _, ok := c.warned[kind]; ok {
		seen = true
	} else {
		c.warned[kind] = struct{}{}
	}
	c.warnMu.Unlock()
	if seen {
		return
	}
	c.opts.OnFailure(message, err)
}

// stats 返回当前计数快照。
func (c *otlpChannel) stats() ChannelStats {
	return ChannelStats{
		Accepted: c.accepted.Load(),
		Exported: c.exported.Load(),
		Dropped:  c.dropped.Load(),
		Rejected: c.rejected.Load(),
		Failures: c.failures.Load(),
		Outbound: c.outbound.Load(),
	}
}

// enqueue 入队一条普通事件记录（不确认送达）。
func (c *otlpChannel) enqueue(record otlpLogRecord) {
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		c.rejected.Add(1)
		c.failOnce("stopped", "analytics: 通道已停止，记录被拒收", nil)
		return
	}
	if len(c.queue) >= c.opts.MaxQueueSize {
		c.mu.Unlock()
		c.dropped.Add(1)
		c.failOnce("overflow", "analytics: OTLP 通道队列已满，记录被丢弃", nil)
		return
	}
	c.mu.Unlock()

	// 会话通道要按"含信封的单条请求字节"量一次（官方用 SDK 序列化器量同一件事）。
	bytes := 0
	if c.opts.sessionMode && c.opts.maxRequestBytes > 0 {
		payload, err := marshalOTLPLogs(c.opts.ResourceAttributes, c.opts.Scope, []otlpLogRecord{record})
		if err != nil {
			c.rejected.Add(1)
			c.failOnce("serialize", "analytics: OTLP 记录序列化失败，记录被拒收", err)
			return
		}
		if len(payload) > c.opts.maxRequestBytes {
			// 官方：单条超限拒收，**不截断**（截断会把会话内容切成半截）。
			c.rejected.Add(1)
			c.failOnce("oversize", "analytics: OTLP 单条记录超过通道字节上限，记录被拒收（不截断）", fmt.Errorf(
				"记录 %d 字节 > 上限 %d", len(payload), c.opts.maxRequestBytes))
			return
		}
		bytes = len(payload)
	}

	c.mu.Lock()
	if c.stopped || len(c.queue) >= c.opts.MaxQueueSize {
		c.mu.Unlock()
		c.dropped.Add(1)
		c.failOnce("overflow", "analytics: OTLP 通道队列已满，记录被丢弃", nil)
		return
	}
	if len(c.queue) == 0 {
		c.firstQueuedAt = time.Now()
	}
	c.queue = append(c.queue, queuedLog{record: record, bytes: bytes})
	c.queuedBytes += bytes
	c.accepted.Add(1)
	c.mu.Unlock()
	c.signal()
}

// stopPending 停止排队发送并清空队列（官方 stopPending：外层 deadline 到点用）。
func (c *otlpChannel) stopPending() {
	c.mu.Lock()
	c.stopped = true
	c.mu.Unlock()
	c.abortCancel()
	c.signal()
}

// shutdown 是通道的收尾：先把队列 drain 完，deadline 到点则取消在飞请求与重试等待。
func (c *otlpChannel) shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	alreadyShutdown := c.shutdownDone
	if !alreadyShutdown {
		c.stopped = true
		c.shutdownDone = true
	}
	c.mu.Unlock()
	if alreadyShutdown {
		<-c.done
		c.release()
		return nil
	}
	c.signal()
	select {
	case <-c.done:
		c.release()
		return nil
	case <-ctx.Done():
		c.abortCancel()
		<-c.done
		c.release()
		return ctx.Err()
	}
}

// release 释放通道自建 client 的空闲连接（官方 exporter shutdown 销毁自己的 agent）。
func (c *otlpChannel) release() {
	if c.transport != nil {
		c.transport.CloseIdleConnections()
	}
}

// ---------------------------------------------------------------------------
// 消费者通道（官方两个 createReporter）
// ---------------------------------------------------------------------------

// EventReporter 是一条调用方自有的**普通事件**通道（官方 EventLogReporter）。
//
// 生命周期归消费者：用 `Shutdown(ctx)` 收尾，deadline 到点会取消在飞请求与重试等待。
type EventReporter struct {
	channel *otlpChannel
}

// Emit 入队一条普通事件（不确认送达，官方语义）。
func (r *EventReporter) Emit(record OTelLogRecord) {
	if r == nil || r.channel == nil {
		return
	}
	r.channel.enqueue(otlpLogRecordOf(record))
}

// Shutdown 排空队列并释放传输资源；ctx 取消时取消在飞请求与重试等待。
func (r *EventReporter) Shutdown(ctx context.Context) error {
	if r == nil || r.channel == nil {
		return nil
	}
	return r.channel.shutdown(ctx)
}

// Stats 返回这条通道的计数（丢记录必须看得见）。
func (r *EventReporter) Stats() ChannelStats {
	if r == nil || r.channel == nil {
		return ChannelStats{}
	}
	return r.channel.stats()
}

// SessionLogReporter 是一条调用方自有的**会话日志**通道（官方 SessionLogReporter）。
//
// 一条事件一条 record，单请求字节上限默认 4,000,000（只允许调低）；与普通事件
// 通道不共享任何请求或队列。
type SessionLogReporter struct {
	channel *otlpChannel
}

// Report 入队一条完整会话事件；单条超限拒收（不截断）。
func (r *SessionLogReporter) Report(record SessionLogRecord) {
	if r == nil || r.channel == nil {
		return
	}
	converted, err := otlpSessionLogRecordOf(record)
	if err != nil {
		r.channel.rejected.Add(1)
		r.channel.failOnce("session-log", "analytics: 会话日志记录无法编码，被拒收", err)
		return
	}
	r.channel.enqueue(converted)
}

// StopPending 丢弃尚未发送的排队记录（外层 deadline 到点用）。
func (r *SessionLogReporter) StopPending() {
	if r == nil || r.channel == nil {
		return
	}
	r.channel.stopPending()
}

// Shutdown 排空队列并释放传输资源；ctx 取消时取消在飞请求与重试等待。
func (r *SessionLogReporter) Shutdown(ctx context.Context) error {
	if r == nil || r.channel == nil {
		return nil
	}
	return r.channel.shutdown(ctx)
}

// Stats 返回这条通道的计数。
func (r *SessionLogReporter) Stats() ChannelStats {
	if r == nil || r.channel == nil {
		return ChannelStats{}
	}
	return r.channel.stats()
}

// otlpLogRecordOf 把普通事件翻成编码用的记录。
//
// 官方 event-log.ts:73-76：severityNumber 缺省用 INFO、severityText 取枚举反查名、
// observedTimestamp 在**入队时**补（"Observation time is assigned on enqueue"）。
func otlpLogRecordOf(record OTelLogRecord) otlpLogRecord {
	severity := record.SeverityNumber
	if severity == SeverityNumberUnspecified {
		severity = SeverityNumberInfo
	}
	body := record.Body
	if body == "" {
		body = record.EventName
	}
	return otlpLogRecord{
		EventName:        record.EventName,
		Body:             body,
		TimeUnixNano:     logTimestampUnixNano(record.Timestamp),
		ObservedTimeNano: time.Now().UnixNano(),
		SeverityNumber:   severity,
		SeverityText:     severityTextFor(severity),
		Attributes:       record.Attributes,
	}
}

// otlpSessionLogRecordOf 把一条会话事件翻成编码用的记录（官方 session-log.ts:210-218）。
//
// 关键点：**一条事件一条 record**；`eventName`/`body` 都是字面量 "session-log"；
// `attributes.sessionId` 是会话身份；`attributes.content` 是完整事件信封的
// **JSON 字符串**（不是对象：collector 侧按字符串再解析，官方就是这么定的）。
func otlpSessionLogRecordOf(record SessionLogRecord) (otlpLogRecord, error) {
	sessionID := strings.TrimSpace(record.SessionID)
	if sessionID == "" {
		return otlpLogRecord{}, fmt.Errorf("会话日志记录缺 sessionId")
	}
	content, err := json.Marshal(record.Event)
	if err != nil {
		return otlpLogRecord{}, fmt.Errorf("会话事件信封无法序列化: %w", err)
	}
	severity := record.SeverityNumber
	if severity == SeverityNumberUnspecified {
		severity = SeverityNumberInfo
	}
	timestamp := record.Timestamp
	if timestamp <= 0 {
		timestamp = float64(time.Now().UnixMilli())
	}
	attributes := make([]LogAttribute, 0, len(record.Attributes)+2)
	// 官方顺序：先附加采集元数据，再固定写 sessionId/content（后写的同名键覆盖前者）。
	attributes = append(attributes, record.Attributes...)
	attributes = append(attributes,
		LogAttribute{Key: "sessionId", Value: sessionID},
		LogAttribute{Key: "content", Value: string(content)},
	)
	return otlpLogRecord{
		EventName:        "session-log",
		Body:             "session-log",
		TimeUnixNano:     logTimestampUnixNano(timestamp),
		ObservedTimeNano: logTimestampUnixNano(timestamp),
		SeverityNumber:   severity,
		SeverityText:     severityTextFor(severity),
		Attributes:       attributes,
	}, nil
}

// sortedLogAttributes 把 map 折成**确定性顺序**的属性列表（键按字节序）。
//
// 用途：产品分析的 attributes 与资源维度来自 map；不排序会让同一批事件每次
// 编码出不同字节（字节预算与断言都会抖），而事件字节顺序本身没有语义。
func sortedLogAttributes(values map[string]any) []LogAttribute {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]LogAttribute, 0, len(keys))
	for _, key := range keys {
		out = append(out, LogAttribute{Key: key, Value: values[key]})
	}
	return out
}

// stderrOnFailure 是通道默认的失败诊断出口（消费者没给 OnFailure 时）：
// 丢记录必须有声音，但只说一次（failOnce 保证）。
func stderrOnFailure(message string, err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", message, err)
		return
	}
	fmt.Fprintf(os.Stderr, "%s\n", message)
}
