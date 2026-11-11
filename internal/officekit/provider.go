package officekit

// Provider 是官方 `officeKit` 服务的宿主实现：一个 provider 生命周期（generation）
// 拥有全部引擎调用、排队、临时目录与转换产物。
//
// 官方对照：
//   - dsh-office-kit/src/index.ts:127-176   OfficeKit（generation / slots / queue / outputs）
//   - dsh-office-kit/src/index.ts:185-250   convert / renderImages / convertTo / recalculate
//   - dsh-office-kit/src/index.ts:278-332   render（Remote 面：转 PDF 后按 RenderedDocumentBytes 返回）
//   - dsh-office-kit/src/index.ts:369-409   runOperation（临时目录、错误分类、清理）
//   - dsh-office-kit/src/queue.ts:105-...   ConversionQueue（准入/并发/取消）

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 官方默认值（dsh-office-kit/src/index.ts:87-109 Config）。
const (
	defaultMaxConcurrent      = 2
	defaultMaxQueued          = 8
	defaultMaxBackground      = 1
	defaultTimeout            = 60 * time.Second
	defaultMaxInputBytes      = 50 * 1024 * 1024
	defaultMaxOutputBytes     = 100 * 1024 * 1024
	defaultMaxImageResolution = 192
	defaultOutputRetention    = 24 * time.Hour
	defaultMaxOutputStoreSize = 512 * 1024 * 1024
)

// Options 是 provider 的部署带宽与引擎配置（字段对照官方 Config，默认值逐项相同）。
type Options struct {
	// Home 是 DSH 的 home 目录；为空时用系统临时目录。转换产物默认落在
	// <Home>/office-kit/outputs（官方默认 $DSH_HOME/office-kit/outputs）。
	Home string
	// EnginePath 是显式引擎路径（设置项）。空则走环境变量与自动发现。
	EnginePath string
	// SearchRoots 覆盖自动发现根（测试注入用）。
	SearchRoots []string
	// OutputDirectory 覆盖产物目录；必须绝对路径。
	OutputDirectory string
	// OutputRetention 是产物保留时长。
	OutputRetention time.Duration
	// MaxOutputStoreBytes 是产物仓总字节上限。
	MaxOutputStoreBytes int64
	// MaxConcurrent 是同时进行的转换数；官方 2。
	MaxConcurrent int
	// MaxQueued 是排队等待准入的调用数；官方 8。
	MaxQueued int
	// MaxBackground 是并发后台转换数；0 表示拒绝后台工作。
	MaxBackground int
	// Timeout 是单次转换的截止时间（不含排队）；官方 60s。
	Timeout time.Duration
	// MaxInputBytes 是源文件上限；官方 50 MiB。
	MaxInputBytes int64
	// MaxOutputBytes 是产物上限；官方 100 MiB。
	MaxOutputBytes int64
	// MaxImageResolution 是 PDF 导出分辨率；官方 192。
	MaxImageResolution int
}

// Capability 是「这个 provider 现在能不能渲染」的可序列化描述。
//
// 它存在的唯一目的：没有引擎时给调用方和 UI 一个**能行动**的答案，
// 而不是一个空错误。
type Capability struct {
	Available        bool     `json:"available"`
	Generation       string   `json:"generation"`
	EngineVersion    string   `json:"engineVersion,omitempty"`
	EngineRoot       string   `json:"engineRoot,omitempty"`
	Executable       string   `json:"executable,omitempty"`
	ProgramDirectory string   `json:"programDirectory,omitempty"`
	Source           string   `json:"source,omitempty"`
	Tried            []string `json:"tried,omitempty"`
	CapableWhen      string   `json:"capableWhen,omitempty"`
}

// Provider 是并发安全的长生命周期对象。
type Provider struct {
	opts Options

	mu         sync.Mutex
	engine     *Engine
	tried      []string
	generation string
	closed     bool

	slots   chan struct{}
	bg      chan struct{}
	pending atomic.Int64

	outputs *outputStore
}

// Open 构造 provider。**没有引擎也返回可用对象**——这是官方 rc.2 的语义
// （office-kit-package 升级指南：「It now registers without Kit」），
// 具体操作各自按 unavailable 失败。
func Open(opts Options) *Provider {
	opts = withDefaults(opts)
	p := &Provider{opts: opts}
	if opts.MaxConcurrent > 0 {
		p.slots = make(chan struct{}, opts.MaxConcurrent)
	}
	if opts.MaxBackground > 0 {
		p.bg = make(chan struct{}, opts.MaxBackground)
	}
	p.outputs = newOutputStore(opts.outputDirectory(), opts.OutputRetention, opts.MaxOutputStoreBytes)
	return p
}

func withDefaults(opts Options) Options {
	if opts.MaxConcurrent <= 0 {
		opts.MaxConcurrent = defaultMaxConcurrent
	}
	if opts.MaxQueued <= 0 {
		opts.MaxQueued = defaultMaxQueued
	}
	if opts.MaxBackground < 0 {
		opts.MaxBackground = 0
	}
	if opts.Timeout <= 0 {
		opts.Timeout = defaultTimeout
	}
	if opts.MaxInputBytes <= 0 {
		opts.MaxInputBytes = defaultMaxInputBytes
	}
	if opts.MaxOutputBytes <= 0 {
		opts.MaxOutputBytes = defaultMaxOutputBytes
	}
	if opts.MaxImageResolution <= 0 {
		opts.MaxImageResolution = defaultMaxImageResolution
	}
	if opts.OutputRetention <= 0 {
		opts.OutputRetention = defaultOutputRetention
	}
	if opts.MaxOutputStoreBytes <= 0 {
		opts.MaxOutputStoreBytes = defaultMaxOutputStoreSize
	}
	return opts
}

func (o Options) tempBase() string {
	if strings.TrimSpace(o.Home) != "" {
		return ""
	}
	return ""
}

func (o Options) outputDirectory() string {
	if strings.TrimSpace(o.OutputDirectory) != "" {
		return o.OutputDirectory
	}
	if strings.TrimSpace(o.Home) != "" {
		return filepath.Join(o.Home, "office-kit", "outputs")
	}
	return filepath.Join(os.TempDir(), "dsh-office-kit-outputs")
}

// Close 回收 provider：等待在途转换结束，清掉过期产物。
func (p *Provider) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.mu.Unlock()
	if p.outputs != nil {
		p.outputs.sweepExpired()
	}
}

// Engine 返回已验证引擎；没有则 nil。
//
// 会触发（并缓存成功结果的）引擎发现——官方语义是“每次需要转换时重新定位 Kit，
// 装上之后不必重载 provider”（dsh-office-kit/src/index.ts:334-344），
// 状态面板与工具都要这条。
func (p *Provider) Engine() *Engine {
	engine, _, _ := p.ensureEngine(context.Background())
	return engine
}

// Capability 报告当前能力与「怎么才能用」。
func (p *Provider) Capability() Capability {
	engine, tried, _ := p.ensureEngine(context.Background())
	capability := Capability{Generation: p.generationID(engine), Tried: tried}
	if engine == nil {
		capability.CapableWhen = CapableWhen()
		return capability
	}
	capability.Available = true
	capability.EngineVersion = engine.Version
	capability.EngineRoot = engine.Root
	capability.Executable = engine.Executable
	capability.ProgramDirectory = engine.ProgramDirectory
	capability.Source = engine.Source
	return capability
}

// CapableWhen 是「引擎缺失时怎么补救」的一句话，写进错误与 UI。
func CapableWhen() string {
	return "设置 " + EngineEnvVar + " 指向 LibreOfficeKit 包根（含 prebuilds.json，内有 bin/dsoffice.exe 与 program/program），" +
		"或把官方 Electron 的 resources/app.asar.unpacked/dsh/node_modules 保持可读（自动发现会扫到它）"
}

// GenerationID 是 provider 生命周期标识：引擎一变就变。
func (p *Provider) GenerationID() string {
	engine, _, _ := p.ensureEngine(context.Background())
	return p.generationID(engine)
}

func (p *Provider) generationID(engine *Engine) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if engine == nil {
		return "officekit-unavailable"
	}
	if p.generation == "" {
		sum := sha256.Sum256([]byte(strings.Join([]string{"officekit", engine.Executable, engine.ProgramDirectory,
			engine.Version, engine.Platform, "v1"}, "|")))
		p.generation = "officekit-" + hex.EncodeToString(sum[:8])
	}
	return p.generation
}

// ensureEngine 返回引擎；缺引擎时**每次操作都重试发现**（官方 index.ts:334-344：
// Kit 装好之后不必重载 provider）。发现结果（含失败）缓存供诊断。
func (p *Provider) ensureEngine(ctx context.Context) (*Engine, []string, error) {
	p.mu.Lock()
	if p.engine != nil {
		engine, tried := p.engine, p.tried
		p.mu.Unlock()
		return engine, tried, nil
	}
	p.mu.Unlock()
	engine, tried := Discover(DiscoverOptions{Explicit: p.opts.EnginePath, SearchRoots: p.opts.SearchRoots})
	p.mu.Lock()
	p.tried = tried
	if engine != nil {
		p.engine = engine
	}
	p.mu.Unlock()
	if engine == nil {
		return nil, tried, &Error{Code: CodeUnavailable, Message: unsatisfiedMessage, CapableWhen: CapableWhen()}
	}
	return engine, tried, nil
}

// acquire 是一次操作的准入：并发上限 + 排队上限 + 后台配额，三者都可被 ctx 取消。
//
// 官方 queue.ts 的 ConversionQueue 语义里，排队中的调用方必须还能取消；
// 这里用 ctx 选择实现同一条。
func (p *Provider) acquire(ctx context.Context, priority string) (release func(), err error) {
	if priority != "foreground" && priority != "background" {
		return nil, errf(CodeInvalidRequest, "priority 只能是 foreground / background")
	}
	if pending := p.pending.Add(1); pending > int64(p.opts.MaxQueued)+int64(p.opts.MaxConcurrent) {
		p.pending.Add(-1)
		return nil, errf(CodeBusy, "转换队列已满（上限 %d），请稍后重试", p.opts.MaxQueued)
	}
	defer p.pending.Add(-1)
	release = func() {}
	if priority == "background" {
		if p.bg == nil {
			return nil, errf(CodeBusy, "maxBackgroundConversions=0 时不接受后台转换")
		}
		select {
		case p.bg <- struct{}{}:
			release = func() { <-p.bg }
		case <-ctx.Done():
			return nil, ctxError(ctx)
		}
	}
	select {
	case p.slots <- struct{}{}:
		inner := release
		release = func() { <-p.slots; inner() }
		return release, nil
	case <-ctx.Done():
		release()
		return nil, ctxError(ctx)
	}
}

func ctxError(ctx context.Context) error {
	cause := context.Cause(ctx)
	if cause == nil {
		cause = ctx.Err()
	}
	return &Error{Code: CodeTimeout, Message: "Office 操作被取消或超时", Detail: cause.Error()}
}

// OperationTimeout 返回本次操作实际使用的截止时间。
func (p *Provider) OperationTimeout() time.Duration { return p.opts.Timeout }

// ---------------------------------------------------------------------------
// 产物仓：转换/重算结果落到 provider 私有目录（官方 outputs.ts 的收敛版）
// ---------------------------------------------------------------------------

type outputStore struct {
	root      string
	retention time.Duration
	maxBytes  int64
	mu        sync.Mutex
	entries   []outputEntry
}

type outputEntry struct {
	path    string
	dir     string
	bytes   int64
	created time.Time
}

func newOutputStore(root string, retention time.Duration, maxBytes int64) *outputStore {
	return &outputStore{root: root, retention: retention, maxBytes: maxBytes}
}

// save 把一次转换/重算产物写进 owner 私有目录并返回绝对路径。
//
// 目录名用 owner 的哈希（官方 outputs.ts:34-36 ownerDirectory 同形），
// 文件名只取 basename，避免模型给的名字带路径穿越。
func (s *outputStore) save(owner, name string, data []byte) (string, error) {
	if s == nil {
		return "", errf(CodeInvalidOutput, "产物仓不可用")
	}
	name = filepath.Base(strings.TrimSpace(name))
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return "", errf(CodeInvalidRequest, "产物文件名不合法")
	}
	ownerDir := filepath.Join(s.root, ownerDirectoryName(owner))
	outDir := filepath.Join(ownerDir, randomName())
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return "", errf(CodeFailed, "无法创建产物目录")
	}
	path := filepath.Join(outDir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		_ = os.RemoveAll(outDir)
		return "", errf(CodeFailed, "无法写入产物")
	}
	s.mu.Lock()
	s.entries = append(s.entries, outputEntry{path: path, dir: outDir, bytes: int64(len(data)), created: time.Now()})
	s.mu.Unlock()
	s.evict()
	return path, nil
}

func (s *outputStore) evict() {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-s.retention)
	var kept []outputEntry
	var total int64
	for _, entry := range s.entries {
		if entry.created.Before(cutoff) {
			removeEntry(entry)
			continue
		}
		kept = append(kept, entry)
		total += entry.bytes
	}
	for len(kept) > 0 && total > s.maxBytes {
		oldest := kept[0]
		kept = kept[1:]
		total -= oldest.bytes
		removeEntry(oldest)
	}
	s.entries = kept
}

func (s *outputStore) sweepExpired() {
	s.evict()
}

func removeEntry(entry outputEntry) {
	_ = os.Remove(entry.path)
	_ = os.Remove(entry.dir)
}

func ownerDirectoryName(owner string) string {
	sum := sha256.Sum256([]byte(owner))
	return "session-" + hex.EncodeToString(sum[:12])
}

func randomName() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("out-%d", time.Now().UnixNano())
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	h := hex.EncodeToString(buf[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
