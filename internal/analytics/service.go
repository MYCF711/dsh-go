package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
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

	client   *http.Client
	outbound atomic.Int64
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
	service := &Service{
		home:     home,
		dir:      dir,
		policy:   DefaultPolicy(),
		watchers: map[chan bool]struct{}{},
		random:   rand.New(rand.NewSource(time.Now().UnixNano())),
		client:   &http.Client{Timeout: 3 * time.Second},
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
// policyVersion 单调递增：调用方给的值 <= 当前值时会自动 +1，避免"改回旧版本"
// 让审计链出现重号。
func (s *Service) UpdatePolicy(next Policy) (Policy, error) {
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

// Close 停止服务：关闭所有策略订阅，后续 Report 返回错误。
func (s *Service) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	for ch := range s.watchers {
		close(ch)
	}
	s.watchers = map[chan bool]struct{}{}
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
	record, err := sanitizeEvent(event, s.policy.PolicyVersion, now)
	if err != nil {
		s.mu.Unlock()
		return ReportResult{}, err
	}
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
	s.policy = DefaultPolicy()
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
func (s *Service) loadPolicyLocked() (string, error) {
	raw, err := os.ReadFile(s.policyPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.policy = DefaultPolicy()
			return "", nil
		}
		return "", err
	}
	policy := DefaultPolicy()
	if err := json.Unmarshal(raw, &policy); err != nil {
		return "", fmt.Errorf("analytics: policy.json 不是合法 JSON: %w", err)
	}
	normalized, err := policy.Normalize()
	if err != nil {
		return "", err
	}
	s.policy = normalized
	return s.policyFileStampLocked()
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
		// 文件被删：回到默认策略（不是沿用旧内存值）。
		if s.policyStamp != "" {
			s.policy = DefaultPolicy()
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
	if err := s.postCollector(ctx, policy.CollectorURL, batch); err != nil {
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

func (s *Service) postCollector(ctx context.Context, collectorURL string, batch ExportBatch) error {
	if collectorURL == "" {
		return fmt.Errorf("analytics: collectorUrl 为空")
	}
	raw, err := json.Marshal(batch)
	if err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, collectorURL, bytesReader(raw))
		if err != nil {
			return err
		}
		request.Header.Set("Content-Type", "application/json")
		s.outbound.Add(1)
		response, err := s.client.Do(request)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
			response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				return nil
			}
			lastErr = fmt.Errorf("analytics: collector 返回 %d", response.StatusCode)
		} else {
			lastErr = err
		}
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(100*(attempt+1)) * time.Millisecond):
			}
		}
	}
	return lastErr
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
