package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// ReportResult 是一次 Report 的真实结算。
//
// 官方 `productAnalytics.report` 的结果是 `void`，HTTP 上不回这个结构；它给
// CLI/测试与 RESULT 证据用：stored 表示真的写了本地 JSONL，exported 表示真的
// POST 成功，dropped 表示被 disabled/采样丢弃。
type ReportResult struct {
	Accepted    bool   `json:"accepted"`
	Stored      int    `json:"stored"`
	Exported    int    `json:"exported"`
	Dropped     int    `json:"dropped"`
	ExportError string `json:"exportError,omitempty"`
}

// Aggregate 是本地聚合结果（P1 的 rollup）。
type Aggregate struct {
	Total         int            `json:"total"`
	ByEvent       map[string]int `json:"byEvent"`
	PolicyVersion int64          `json:"policyVersion"`
	Oldest        string         `json:"oldest,omitempty"`
	Newest        string         `json:"newest,omitempty"`
}

// Service 是产品分析管道的唯一实例。
//
// 并发模型：所有状态（policy/lastHash/watchers）都由 mu 保护；网络导出在
// 锁外执行（避免慢 collector 卡住 enabled/report 的其它调用）。
type Service struct {
	home string
	dir  string

	mu          sync.Mutex
	policy      Policy
	policyStamp string
	lastHash    string
	closed      bool
	watchers    map[chan bool]struct{}
	random      *rand.Rand

	// outbound 是真实 HTTP 出站尝试次数（默认 local 模式必须为 0）。
	// 每条 OTLP 通道有自己的 client（见 otel_channel.go），不再共享一个全局 client：
	// 共享会让"一个消费者的 collector 挂了拖住另一个"变成结构上可能的事。
	outbound atomic.Int64

	// eventChannel 是 **product analytics 消费者自己的** OTLP event 通道
	// （官方 #37 `otel.createEventReporter` 的形状）：自己的 endpoint/headers/
	// resource/instrumentation scope/client/队列，与任何其它通道都不共享。
	// 端点是策略的一部分，端点变了就换一条；旧通道按 shutdown 语义收尾。
	eventChannel    *otlpChannel
	eventChannelURL string

	// anonymousUserID 是 `$DSHD_HOME/.anonymous-user-id` 的内容（官方 user.id）。
	// 在 Open 时就落地：身份是"这台机器上的这个人"，不是"这一条事件"。
	anonymousUserID string
	// environmentOptions 是环境采集的输入（两个官方参数 + 平台事实 + 执行器）。
	environmentOptions environmentOptions
	// envOnce/env 只采一次：官方也缓存到 analytics 实例生命周期
	// （index.js:389 `this.environment ??= collectEnvironment(...)`）。
	envOnce sync.Once
	env     Environment
}

// Open 打开（必要时创建）一个 home 下的 analytics 目录。
//
// 默认策略是 enabled=true + mode=local；默认路径不创建任何网络连接。
func Open(home string) (*Service, error) {
	if home == "" {
		return nil, fmt.Errorf("analytics: home 不能为空")
	}
	dir := analyticsDir(home)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("analytics: 创建目录 %s 失败: %w", dir, err)
	}
	anonymousUserID, err := ensureAnonymousUserID(home)
	if err != nil {
		return nil, err
	}
	timeout, maxOutputBytes := EnvironmentQueryConfig(os.Getenv)
	service := &Service{
		home:     home,
		dir:      dir,
		policy:   DefaultPolicy(),
		watchers: map[chan bool]struct{}{},
		random:   rand.New(rand.NewSource(time.Now().UnixNano())),

		anonymousUserID: anonymousUserID,
		environmentOptions: environmentOptions{
			platform:       runtime.GOOS,
			systemRoot:     os.Getenv("SystemRoot"),
			goarch:         runtime.GOARCH,
			timeout:        timeout,
			maxOutputBytes: maxOutputBytes,
			logicalCPUs:    runtime.NumCPU(),
		},
	}
	stamp, err := service.loadPolicyLocked()
	if err != nil {
		return nil, err
	}
	service.policyStamp = stamp
	files, err := listEventFiles(dir)
	if err != nil {
		return nil, err
	}
	lastHash, err := lastHashOfFiles(files)
	if err != nil {
		return nil, err
	}
	service.lastHash = lastHash
	return service, nil
}

// AnonymousUserID 返回本机匿名身份（官方 Resource.user.id）。
func (s *Service) AnonymousUserID() string {
	if s == nil {
		return ""
	}
	return s.anonymousUserID
}

// environmentSnapshot 返回本次进程的环境维度（懒采集 + 只采一次）。
//
// 采集会起本地子进程（Windows 上最多 2 次），所以：
//   - 绝不在 s.mu 里做（见 Report）；
//   - 采集失败也要返回结果（字段如实进 missing_fields，绝不 panic）。
func (s *Service) environmentSnapshot() Environment {
	s.envOnce.Do(func() {
		opts := s.environmentOptions
		if opts.logicalCPUs <= 0 {
			opts.logicalCPUs = runtime.NumCPU()
		}
		ctx := context.Background()
		if opts.timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, opts.timeout)
			defer cancel()
		}
		s.env = collectEnvironment(ctx, opts)
	})
	return s.env
}

// Dir 返回 analytics 目录（测试/CLI 诊断用；不是公开 wire 形状）。
func (s *Service) Dir() string { return s.dir }

// Policy 返回当前**真实生效**策略（会先刷新 policy.json，外部编辑也能看到）。
func (s *Service) Policy() Policy {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.refreshPolicyLocked()
	return s.policy
}

// Enabled 返回当前是否接受新事件（官方 enabled 的 bool）。
func (s *Service) Enabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.refreshPolicyLocked()
	return s.policy.Enabled
}

// UpdatePolicy 校验并原子落盘策略，同时通知所有 watchPolicy 订阅者。
//
// 环境变量端点在这里同样参与：调用方没给 collectorUrl 时用 env 值补齐
// （显式配置 > env > 默认）。补齐后的值**会落盘**——policy.json 是显式配置的
// 落点，一旦写下就以配置的身份存在，之后即使清掉 env 端点也不会漂移。
// policyVersion 单调递增：调用方给的值 <= 当前值时会自动 +1，避免"改回旧版本"
// 让审计链出现重号。
func (s *Service) UpdatePolicy(next Policy) (Policy, error) {
	next, _, err := withEnvCollectorURL(next)
	if err != nil {
		return Policy{}, err
	}
	normalized, err := next.Normalize()
	if err != nil {
		return Policy{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshPolicyLocked(); err != nil {
		return Policy{}, err
	}
	if normalized.PolicyVersion <= s.policy.PolicyVersion {
		normalized.PolicyVersion = s.policy.PolicyVersion + 1
	}
	if err := s.writePolicyLocked(normalized); err != nil {
		return Policy{}, err
	}
	stamp, err := s.policyFileStampLocked()
	if err != nil {
		return Policy{}, err
	}
	s.policy = normalized
	s.policyStamp = stamp
	s.notifyPolicyLocked()
	return normalized, nil
}

// WatchPolicy 订阅真实策略流：先给当前 enabled，再在每次策略变化时推新值。
//
// 官方 watchPolicy 是 `AsyncIterable<boolean>`；这里返回的 channel 关闭 =
// 订阅结束（ctx 取消或 Service 关闭）。不轮询、不伪造定时帧。
func (s *Service) WatchPolicy(ctx context.Context) (<-chan bool, error) {
	s.mu.Lock()
	if err := s.refreshPolicyLocked(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if s.closed {
		s.mu.Unlock()
		return nil, fmt.Errorf("analytics: service 已关闭")
	}
	ch := make(chan bool, 1)
	s.watchers[ch] = struct{}{}
	ch <- s.policy.Enabled
	s.mu.Unlock()

	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				s.mu.Lock()
				if _, ok := s.watchers[ch]; ok {
					delete(s.watchers, ch)
					close(ch)
				}
				s.mu.Unlock()
				return
			case <-ticker.C:
				s.mu.Lock()
				_ = s.refreshPolicyLocked()
				if s.closed {
					if _, ok := s.watchers[ch]; ok {
						delete(s.watchers, ch)
						close(ch)
					}
					s.mu.Unlock()
					return
				}
				s.mu.Unlock()
			}
		}
	}()
	return ch, nil
}

// Close 停止服务：关闭所有策略订阅，收掉本消费者的 OTLP 通道，后续 Report 返回错误。
//
// 通道收尾在锁外做：shutdown 会等队列 drain（最长 analyticsChannelCloseTimeout），
// 拿在 s.mu 里会把 enabled/policy 这些读路径一起堵住。
func (s *Service) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	for ch := range s.watchers {
		close(ch)
	}
	s.watchers = map[chan bool]struct{}{}
	channel := s.eventChannel
	s.eventChannel = nil
	s.eventChannelURL = ""
	s.mu.Unlock()
	closeChannel(channel)
}

// Report 是一条事件的**真实**处理：策略检查 → 采样 → 脱敏 → append JSONL
// （hash 链）→ 可选 exporter。任何一步失败都返回错误，不静默成功。
func (s *Service) Report(ctx context.Context, event Event) (ReportResult, error) {
	now := time.Now()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ReportResult{}, fmt.Errorf("analytics: service 已关闭")
	}
	if err := s.refreshPolicyLocked(); err != nil {
		s.mu.Unlock()
		return ReportResult{}, err
	}
	if !s.policy.Enabled {
		s.mu.Unlock()
		return ReportResult{Accepted: true, Dropped: 1}, nil
	}
	if s.policy.SamplingRate < 1 && s.random.Float64() > s.policy.SamplingRate {
		s.mu.Unlock()
		return ReportResult{Accepted: true, Dropped: 1}, nil
	}
	anonymousUserID := s.anonymousUserID
	// 环境采集要起本地子进程（每个进程最多一次），**必须在锁外**做：
	// 否则一次冷启动的子进程会把 enabled/watchPolicy 这些读路径一起堵住。
	s.mu.Unlock()
	environment := s.environmentSnapshot()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ReportResult{}, fmt.Errorf("analytics: service 已关闭")
	}
	record, err := sanitizeEvent(event, s.policy.PolicyVersion, now)
	if err != nil {
		s.mu.Unlock()
		return ReportResult{}, err
	}
	// 身份与环境维度只能来自宿主自己（调用方塞的同名 attributes 已被白名单丢弃）。
	record.AnonymousUserID = anonymousUserID
	record.Environment = environment.Fields
	record.MissingFields = environment.MissingSummary()
	stored, err := appendRecordLocked(s.dir, now, record, s.lastHash)
	if err != nil {
		s.mu.Unlock()
		return ReportResult{}, err
	}
	s.lastHash = stored.Hash
	policy := s.policy
	s.mu.Unlock()

	result := ReportResult{Accepted: true, Stored: 1}
	if policy.Mode != ModeLocalExport {
		return result, nil
	}
	exported, exportErr := s.exportRecord(ctx, policy, stored)
	if exportErr != nil {
		result.ExportError = exportErr.Error()
		return result, nil
	}
	if exported {
		result.Exported = 1
	}
	return result, nil
}

// Dump 返回 since 之后（按 receivedAt）的记录；since 为零值表示全部。
func (s *Service) Dump(since time.Time) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := readAllRecords(s.dir)
	if err != nil {
		return nil, err
	}
	if since.IsZero() {
		return records, nil
	}
	out := make([]Record, 0, len(records))
	for _, record := range records {
		received, err := time.Parse(time.RFC3339Nano, record.ReceivedAt)
		if err != nil {
			return nil, fmt.Errorf("analytics: 记录 receivedAt 非法: %w", err)
		}
		if received.After(since) {
			out = append(out, record)
		}
	}
	return out, nil
}

// Verify 复算整条 JSONL hash 链；成功返回记录数。
func (s *Service) Verify() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return verifyChain(s.dir)
}

// Aggregate 做本地 rollup（按事件名计数 + 时间范围）。
func (s *Service) Aggregate() (Aggregate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := readAllRecords(s.dir)
	if err != nil {
		return Aggregate{}, err
	}
	aggregate := Aggregate{Total: len(records), ByEvent: map[string]int{}, PolicyVersion: s.policy.PolicyVersion}
	for _, record := range records {
		aggregate.ByEvent[record.EventName]++
		if aggregate.Oldest == "" || record.ReceivedAt < aggregate.Oldest {
			aggregate.Oldest = record.ReceivedAt
		}
		if aggregate.Newest == "" || record.ReceivedAt > aggregate.Newest {
			aggregate.Newest = record.ReceivedAt
		}
	}
	return aggregate, nil
}

// Purge 清空本地分析库（事件 + 待发队列 + 策略文件），返回删除的事件数。
//
// 清空后策略回到默认（enabled=true, mode=local）；这是"一键清空"的语义，
// 不是把目录留一半。
func (s *Service) Purge() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count, err := countRecords(s.dir)
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	for _, entry := range entries {
		target := filepath.Join(s.dir, entry.Name())
		if err := os.RemoveAll(target); err != nil {
			return 0, err
		}
	}
	policy, _, err := withEnvCollectorURL(DefaultPolicy())
	if err != nil {
		return 0, err
	}
	normalized, err := policy.Normalize()
	if err != nil {
		return 0, err
	}
	s.policy = normalized
	s.policyStamp = ""
	s.lastHash = ""
	s.notifyPolicyLocked()
	return count, nil
}

// OutboundRequests 是真实 HTTP 出站次数（默认 local 模式必须为 0）。
//
// 它不是"声明"，是可被测试读取的计数器；本地模式测试同时用 httptest 桩
// 证伪（桩收到请求就失败）。
func (s *Service) OutboundRequests() int64 { return s.outbound.Load() }

// ---------------------------------------------------------------------------
// policy 文件
// ---------------------------------------------------------------------------

func (s *Service) policyPath() string { return filepath.Join(s.dir, policyFileName) }

func (s *Service) policyFileStampLocked() (string, error) {
	info, err := os.Stat(s.policyPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return fmt.Sprintf("%d:%d", info.ModTime().UnixNano(), info.Size()), nil
}

// loadPolicyLocked 读策略文件；不存在时保持 DefaultPolicy（不写盘）。
//
// 两条分支出口都过 withEnvCollectorURL：环境变量只在**策略里没有显式端点**时
// 补 CollectorURL（显式配置 > env > 默认），非法端点在 Open 阶段就报错。
func (s *Service) loadPolicyLocked() (string, error) {
	raw, err := os.ReadFile(s.policyPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			policy, _, err := withEnvCollectorURL(DefaultPolicy())
			if err != nil {
				return "", err
			}
			normalized, err := policy.Normalize()
			if err != nil {
				return "", err
			}
			s.policy = normalized
			return "", nil
		}
		return "", err
	}
	policy := DefaultPolicy()
	if err := json.Unmarshal(raw, &policy); err != nil {
		return "", fmt.Errorf("analytics: policy.json 不是合法 JSON: %w", err)
	}
	policy, _, err = withEnvCollectorURL(policy)
	if err != nil {
		return "", err
	}
	normalized, err := policy.Normalize()
	if err != nil {
		return "", err
	}
	s.policy = normalized
	return s.policyFileStampLocked()
}

// withEnvCollectorURL 是环境变量端点的唯一入口（薄封装，便于测试与阅读）。
func withEnvCollectorURL(policy Policy) (Policy, string, error) {
	return WithEnvCollectorURL(policy, nil)
}

// refreshPolicyLocked 只在文件 stamp 变化时重读，避免每次 report 都读盘。
func (s *Service) refreshPolicyLocked() error {
	stamp, err := s.policyFileStampLocked()
	if err != nil {
		return err
	}
	if stamp == s.policyStamp && stamp != "" {
		return nil
	}
	if stamp == "" {
		// 文件被删：回到默认策略（不是沿用旧内存值），但环境变量端点仍然生效。
		if s.policyStamp != "" {
			policy, _, err := withEnvCollectorURL(DefaultPolicy())
			if err != nil {
				return err
			}
			normalized, err := policy.Normalize()
			if err != nil {
				return err
			}
			s.policy = normalized
			s.policyStamp = ""
			s.notifyPolicyLocked()
		}
		return nil
	}
	old := s.policy
	loadedStamp, err := s.loadPolicyLocked()
	if err != nil {
		return err
	}
	s.policyStamp = loadedStamp
	if s.policy != old {
		s.notifyPolicyLocked()
	}
	return nil
}

func (s *Service) writePolicyLocked(policy Policy) error {
	raw, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return err
	}
	path := s.policyPath()
	temp := path + ".tmp"
	file, err := os.OpenFile(temp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(raw, '\n')); err != nil {
		file.Close()
		os.Remove(temp)
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		os.Remove(temp)
		return err
	}
	if err := file.Close(); err != nil {
		os.Remove(temp)
		return err
	}
	return os.Rename(temp, path)
}

func (s *Service) notifyPolicyLocked() {
	enabled := s.policy.Enabled
	for ch := range s.watchers {
		select {
		case ch <- enabled:
		default:
			// 订阅者来不及读时保留最新值：先丢掉旧值再塞新值。
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- enabled:
			default:
			}
		}
	}
}

// ---------------------------------------------------------------------------
// exporter
// ---------------------------------------------------------------------------

func (s *Service) pendingPath() string { return filepath.Join(s.dir, pendingFileName) }

// exportRecord 把待发队列 + 当前记录一起导出；失败时把当前记录留在待发队列。
//
// 载荷是 OTLP/HTTP JSON（logs，见 otlp.go），不再是宿主自有的 ExportBatch JSON；
// 发送走本消费者的 event 通道（自己的 endpoint/headers/resource/scope/client，
// 见 otel_channel.go），重试/待发队列/字节上限与改动前逐条一致：
//   - 失败时记录落到 pending-export.jsonl，下一次 report 连同队列一起补发；
//   - 只有 HTTP 2xx 才删队列并计入 Exported。
func (s *Service) exportRecord(ctx context.Context, policy Policy, record Record) (bool, error) {
	pending, err := readPending(s.pendingPath())
	if err != nil {
		return false, err
	}
	batch, err := sanitizeExportBatch(ExportBatch{
		PolicyVersion: policy.PolicyVersion,
		Events:        append(pending, record),
	})
	if err != nil {
		return false, err
	}
	channel, err := s.eventChannelFor(policy)
	if err != nil {
		return false, err
	}
	records, err := otlpRecordsForBatch(batch)
	if err != nil {
		return false, err
	}
	if err := channel.sendSync(ctx, records); err != nil {
		// 失败的记录留在队列里，下一次 report 继续尝试；不把错误写进事件。
		if appendErr := appendPending(s.pendingPath(), record); appendErr != nil {
			return false, fmt.Errorf("%v；写入待发队列失败: %w", err, appendErr)
		}
		return false, err
	}
	if err := os.Remove(s.pendingPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return true, nil
}

// eventChannelFor 取（必要时建）当前策略端点对应的 event 通道。
//
// 为什么端点变了要换一条通道：官方通道的 endpoint 是**创建时**显式给的常量，
// 而本仓的策略可以在运行期改 collectorUrl（`dshd analytics policy`）。复用旧
// 通道会把记录发到上一个端点——那是"用户改了配置但没生效"的静默错误。
func (s *Service) eventChannelFor(policy Policy) (*otlpChannel, error) {
	s.mu.Lock()
	if channel := s.eventChannel; channel != nil && s.eventChannelURL == policy.CollectorURL {
		s.mu.Unlock()
		return channel, nil
	}
	stale := s.eventChannel
	s.eventChannel = nil
	s.eventChannelURL = ""
	s.mu.Unlock()

	if stale != nil {
		ctx, cancel := context.WithTimeout(context.Background(), analyticsChannelCloseTimeout)
		_ = stale.shutdown(ctx)
		cancel()
	}

	// resource 维度在锁外采（环境采集会起本地子进程；只采一次，见 environmentSnapshot）。
	options, err := normalizeLogChannelOptions("product analytics event 通道",
		productAnalyticsChannelOptions(policy.CollectorURL, s.otlpResourceAttributes(), func() {
			// exporter 自观测计数：真实 HTTP 尝试次数报回 Service.OutboundRequests，
			// 默认 local 模式下这条路径根本不会走到（一个字节都不出网）。
			s.outbound.Add(1)
		}))
	if err != nil {
		return nil, err
	}
	channel, err := newOTLPChannel(options, false)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		closeChannel(channel)
		return nil, fmt.Errorf("analytics: service 已关闭")
	}
	// 并发 Report 里别的调用先建好了同端点通道：用它的，自己这条收掉。
	// 注意 shutdown 一律在锁外做（它会等 drain，拿在 s.mu 里会堵住读路径）。
	if existing := s.eventChannel; existing != nil && s.eventChannelURL == policy.CollectorURL {
		s.mu.Unlock()
		closeChannel(channel)
		return existing, nil
	}
	existing := s.eventChannel
	s.eventChannel = channel
	s.eventChannelURL = policy.CollectorURL
	s.mu.Unlock()
	if existing != nil {
		closeChannel(existing)
	}
	return channel, nil
}

// closeChannel 在有限时间内收掉一条通道（不把调用方无限期挂住）。
func closeChannel(channel *otlpChannel) {
	if channel == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), analyticsChannelCloseTimeout)
	defer cancel()
	_ = channel.shutdown(ctx)
}

// 本消费者的通道身份。官方 product adapter 用包名做 scope、用 `x-channel` 做
// collector 路由头（`dsh-host-product-telemetry-otel/README.md` 的配置表：
// `channel` 默认 `dsh_otel_report`；`src/index.ts` 的 `scope: { name: '<包名>' }`）。
const (
	// productAnalyticsScopeName 是本消费者在 OTLP instrumentation scope 上的身份。
	productAnalyticsScopeName = "dshgo/internal/analytics"
	// productAnalyticsChannelHeader 是 collector 的 `x-channel` 路由值（官方默认值）。
	productAnalyticsChannelHeader = "dsh_otel_report"
	// analyticsChannelCloseTimeout 是 Service 收尾时给通道 drain 的上界。
	// 官方 product adapter 的 shutdownTimeoutMillis 默认 21s；本仓 Close 是同步收尾
	// 路径（enabled/policy 读路径在等），取一个更短但足够 drain 已入队记录的值。
	analyticsChannelCloseTimeout = 5 * time.Second
)

// productAnalyticsChannelOptions 是 product analytics 消费者的通道配置。
//
// 显式给全（官方 "The service does not add a channel header or inherit ambient
// authorization headers"）：endpoint 来自策略；头只有 collector 路由头；
// resource 是本机匿名身份 + 环境维度 + 应用身份；scope 是本包。
func productAnalyticsChannelOptions(endpoint string, resource []LogAttribute, onOutbound func()) LogChannelOptions {
	return LogChannelOptions{
		Endpoint:           endpoint,
		Headers:            map[string]string{"x-channel": productAnalyticsChannelHeader},
		ResourceAttributes: resource,
		Scope:              InstrumentationScope{Name: productAnalyticsScopeName},
		OnFailure:          stderrOnFailure,
		onOutbound:         onOutbound,
	}
}

// otlpResourceAttributes 是 OTLP resource 维度：应用身份 + 匿名用户身份 + 环境。
//
// 三条都来自宿主自己（调用方塞不进资源维度）：
//   - `service.name`：本仓应用身份（版本字面量被 version_guard 独占，这里不带版本）；
//   - `user.id`：官方 Resource 的匿名用户身份（`$DSHD_HOME/.anonymous-user-id`）；
//   - os/arch/版本/CPU 等环境维度：官方把它当 Resource 级维度（见 redact.go 的说明），
//     只来自采集器，键名按字节序排列以保证编码确定性。
func (s *Service) otlpResourceAttributes() []LogAttribute {
	attributes := make([]LogAttribute, 0, 8)
	attributes = append(attributes, LogAttribute{Key: "service.name", Value: analyticsServiceName})
	if s.anonymousUserID != "" {
		attributes = append(attributes, LogAttribute{Key: "user.id", Value: s.anonymousUserID})
	}
	attributes = append(attributes, sortedLogAttributes(s.environmentSnapshot().Fields)...)
	return attributes
}

// otlpRecordsForBatch 把**已二次脱敏**的导出批次翻成 OTLP 日志记录。
//
// 字段映射（都是本仓已有语义，不新造）：
//   - eventName ← Record.EventName；body ← 事件名（官方 body 只要"人类可读摘要"，
//     绝不能放 prompt/正文）；
//   - timeUnixNano ← Record.Timestamp（Unix 毫秒）；observedTimeUnixNano ← Record.ReceivedAt；
//   - attributes ← 白名单属性（已脱敏）+ `policyVersion` + `missingFields`（非空时）。
//     后两者是本地审计维度，导出前一直带着（旧 ExportBatch 的等价信息，不改语义）；
//   - 身份与环境维度不进 attributes：它们是 Resource 级的（见 otlpResourceAttributes）。
func otlpRecordsForBatch(batch ExportBatch) ([]otlpLogRecord, error) {
	records := make([]otlpLogRecord, 0, len(batch.Events))
	for _, event := range batch.Events {
		attributes := make(map[string]any, len(event.Attributes)+2)
		for key, value := range event.Attributes {
			attributes[key] = value
		}
		attributes["policyVersion"] = batch.PolicyVersion
		if event.MissingFields != "" {
			attributes["missingFields"] = event.MissingFields
		}
		observed := logTimestampUnixNano(event.Timestamp)
		if received, err := time.Parse(time.RFC3339Nano, event.ReceivedAt); err == nil {
			observed = received.UnixNano()
		}
		records = append(records, otlpLogRecord{
			EventName:        event.EventName,
			Body:             event.EventName,
			TimeUnixNano:     logTimestampUnixNano(event.Timestamp),
			ObservedTimeNano: observed,
			SeverityNumber:   SeverityNumberInfo,
			SeverityText:     severityTextFor(SeverityNumberInfo),
			Attributes:       sortedLogAttributes(attributes),
		})
	}
	return records, nil
}

// CreateEventReporter 建一条**调用方自有**的普通事件 OTLP 通道
// （官方 #37 `otel.createEventReporter(options)`：SDK 按 count 批、入队即返回）。
//
// 通道不共享任何状态：endpoint/headers/resource/scope/队列/client 全是这一份
// options 给的。收尾由调用方用自己的 deadline 调 `Shutdown(ctx)`。
func (s *Service) CreateEventReporter(opts LogChannelOptions) (*EventReporter, error) {
	normalized, err := normalizeLogChannelOptions("event 通道", opts)
	if err != nil {
		return nil, err
	}
	if normalized.OnFailure == nil {
		normalized.OnFailure = stderrOnFailure
	}
	channel, err := newOTLPChannel(normalized, true)
	if err != nil {
		return nil, err
	}
	return &EventReporter{channel: channel}, nil
}

// CreateSessionLogReporter 建一条**调用方自有**的会话日志 OTLP 通道
// （官方 #37 `otel.createSessionLogReporter(options)`）。
//
// 语义：一条事件一条 record（`eventName: "session-log"`、`attributes.sessionId`、
// `attributes.content` 为事件信封的 JSON 字符串）；单请求未压缩字节上限默认
// 4,000,000（只允许调低，官方硬边界）；单条超限**拒收不截断**。
//
// 与普通事件通道**不共享任何请求或队列**（官方 "A Session channel never shares
// a request or queue with ordinary events"）。
//
// 谁消费它：官方是 #38 `session-telemetry-otel`（feedback 授权后的会话记录，
// FEEDBACK_ONLY 语义）。本仓 #38 仍 MISSING（审计 plugin-face.md 主表 #38），
// 所以这条通道现在**没有生产消费者**——接口按官方形状提供、并被测试真实调用
// （一条事件一条 record / 超限拒收 / 字节上限 / shutdown 取消在飞请求）。
// 这里**不**造一个"没人调用的假 feedback 流"：#38 接上时直接用这两个入口。
func (s *Service) CreateSessionLogReporter(opts SessionLogChannelOptions) (*SessionLogReporter, error) {
	limit := opts.MaxRequestBytes
	if limit == 0 {
		limit = DefaultSessionLogMaxRequestBytes
	}
	if limit < 1 || limit > DefaultSessionLogMaxRequestBytes {
		return nil, fmt.Errorf("analytics 会话日志通道: MaxRequestBytes 必须在 [1,%d]，收到 %d",
			DefaultSessionLogMaxRequestBytes, limit)
	}
	channelOptions := opts.LogChannelOptions
	channelOptions.sessionMode = true
	channelOptions.maxRequestBytes = limit
	normalized, err := normalizeLogChannelOptions("会话日志通道", channelOptions)
	if err != nil {
		return nil, err
	}
	if normalized.OnFailure == nil {
		normalized.OnFailure = stderrOnFailure
	}
	channel, err := newOTLPChannel(normalized, true)
	if err != nil {
		return nil, err
	}
	return &SessionLogReporter{channel: channel}, nil
}

func readPending(path string) ([]Record, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var records []Record
	for _, line := range splitLines(raw) {
		if len(line) == 0 {
			continue
		}
		var record Record
		if err := json.Unmarshal(line, &record); err != nil {
			return nil, fmt.Errorf("analytics: pending-export.jsonl 损坏: %w", err)
		}
		records = append(records, record)
	}
	return records, nil
}

func appendPending(path string, record Record) error {
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(raw, '\n')); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

// 小工具（避免多一个 bytes 依赖写在文件顶部）。
func bytesReader(raw []byte) io.Reader { return &byteReader{raw: raw} }

type byteReader struct {
	raw []byte
	pos int
}

func (r *byteReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.raw) {
		return 0, io.EOF
	}
	n := copy(p, r.raw[r.pos:])
	r.pos += n
	return n, nil
}

func splitLines(raw []byte) [][]byte {
	var lines [][]byte
	start := 0
	for index, value := range raw {
		if value == '\n' {
			lines = append(lines, raw[start:index])
			start = index + 1
		}
	}
	if start < len(raw) {
		lines = append(lines, raw[start:])
	}
	return lines
}
