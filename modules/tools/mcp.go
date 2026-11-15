// Package tools 的 MCP 客户端桥。
//
// 它解决的是一个**产品级缺口**：`cmd/dshd/mcp.go` 早就能连上 MCP server、也能
// 数出 `tools/list` 有多少个工具，但那些工具**从来没有进过工具注册表**——模型
// 看不到、也调不到，界面上只有一个 `toolCount` 在骗人。
//
// 契约逐条对齐官方 `@deepseek-ai/dsh-mcp-client`（asar 只读解出，
// packages/mcp/mcp-client/lib/index.js）与 `@deepseek-ai/dsh-mcp-resources`：
//
//   - 公开名 `mcp__<serverName>__<rawName>`，按 DeepSeek function-name 约束
//     归一化成 `[A-Za-z0-9_-]` 且 ≤64；一旦归一化真的改了字（替换或截断），
//     追加 12 位十六进制的 `sha256(serverName \x00 rawName)`——两个不同的 MCP
//     身份绝不许塌成同一个公开名。
//   - 服务器没声明 `tools` 能力就**不聚合**工具列表（官方同一判据）。
//   - 同一个 server 把同名工具列两次 = 非法工具列表，**响亮失败**。
//   - `isError: true` 的调用结果是失败，不是成功。
//   - 资源走三个共享工具（list / templates / read），结果以
//     `MCP server: <name>\n<JSON>` 呈现，`blob` 换成字数说明，二进制永不进历史。
//
// 错误策略分两档，这条界线是刻意画的：
//
//   - **装配期**（工具名冲突、schema 不是 JSON、serverName 非法）→ 返回错误，
//     装配失败。这类错误静默下去，模型会看到一张残缺的工具表，而没人能解释为什么。
//   - **连接期**（子进程起不来、握手超时、跑起来又挂了）→ 记 status=error，
//     该 server 的工具不注册，**不**拖垮整个宿主。官方 `failOnStartupError`
//     默认就是 false：一个配错的 MCP server 不该让 `dshd tools` 退出。
package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"dshgo/internal/kernel"
	"dshgo/internal/mcp"
	"dshgo/internal/spi"
)

const (
	// mcpDefaultToolCallTimeout 与官方 DEFAULT_TOOL_CALL_TIMEOUT_MS 同值。
	mcpDefaultToolCallTimeout = 60 * time.Second
	// mcpHandshakeTimeout 是 initialize + 工具发现的总预算。
	mcpHandshakeTimeout = 10 * time.Second
	// mcpMaxToolPages 是分页上限：nextCursor 不前进或页数失控时必须停。
	mcpMaxToolPages = 1024
	// mcpMaxPublicNameLength 是 DeepSeek function-name 契约（wire 常量，不是配置）。
	mcpMaxPublicNameLength = 64
	// mcpNameHashLength 是归一化有损时追加的 SHA-256 十六进制位数。
	mcpNameHashLength = 12
	// mcpClientName 用于 initialize 握手的 clientInfo.name。
	mcpClientName = "dsh-go"
	// mcpClientVersion 与官方一样是静态值：它只影响 server 侧日志，不是产品版本。
	mcpClientVersion = "0.0.1"
)

var (
	// mcpServerNamePattern 与官方 SERVER_NAME_PATTERN 逐字相同。
	mcpServerNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
	// mcpInvalidNameChars 是归一化要替换掉的字符集。
	mcpInvalidNameChars = regexp.MustCompile(`[^A-Za-z0-9_-]`)

	// ErrMCPServerNotFound 让宿主把错误翻译成 transport 层的 404。
	ErrMCPServerNotFound = errors.New("mcp server not found")
	// ErrMCPServerDisconnected 是"配置里有它、但现在没连上"。
	ErrMCPServerDisconnected = errors.New("mcp server disconnected")
	// ErrMCPToolConflict 是命名空间冲突。它必须是可 errors.Is 的：
	// 装配失败时人要能一眼分清"名字撞了"和"连不上"。
	ErrMCPToolConflict = errors.New("MCP 工具名冲突")
	// ErrMCPProtocol 表示 server 违反了 MCP 契约：把同名工具列两次、inputSchema
	// 不是 JSON、nextCursor 不前进、要求本桥不支持的 task-based execution。
	//
	// 这一类**永远致命**，与"子进程起不来"必须分开：后者是环境暂时不通
	// （记 status=error，宿主照常起来），前者是这个 server 说的话不可信——
	// 静默下去模型会拿到一张自己也不知道缺了什么的工具表。
	ErrMCPProtocol = errors.New("MCP 协议契约违规")
)

// MCPReconnectConfig 是重连策略。默认值与官方 RECONNECT_DEFAULTS 逐字相同。
type MCPReconnectConfig struct {
	Enabled        *bool `json:"enabled"`
	InitialDelayMS int   `json:"initialDelayMs"`
	MaxDelayMS     int   `json:"maxDelayMs"`
	MaxAttempts    int   `json:"maxAttempts"`
}

type mcpReconnectPolicy struct {
	enabled      bool
	initialDelay time.Duration
	maxDelay     time.Duration
	maxAttempts  int
}

// mcpServerConfig 是 mcp.json 里一个 server 的配置。
//
// 形状沿用本仓既有契约（Claude Desktop 风格：name → {command,args,env,cwd,enabled}），
// 新增字段全部可选、且与官方同名同默认，未写的字段行为逐字节不变。
type mcpServerConfig struct {
	Command            string              `json:"command"`
	Args               []string            `json:"args"`
	Env                map[string]string   `json:"env"`
	Cwd                string              `json:"cwd"`
	Enabled            *bool               `json:"enabled"`
	ToolCallTimeoutMS  int                 `json:"toolCallTimeoutMs"`
	CloseTimeoutMS     int                 `json:"closeTimeoutMs"`
	FailOnStartupError bool                `json:"failOnStartupError"`
	Reconnect          *MCPReconnectConfig `json:"reconnect"`
	// 官方 0.2.2 的条目带 transport 字段（stdio | streamable-http）。
	// 本宿主只实现 stdio：出现别的取值时**不许**静默当成 stdio 起进程，
	// 而是把它当"结构化未实现"报出来（probe/restart 返回四字段错误）。
	Transport string            `json:"transport"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
}

// closeBudget 是关这一代连接的预算（官方 DEFAULT_CLOSE_TIMEOUT_MS = 5000ms）。
func (c mcpServerConfig) closeBudget() time.Duration {
	if c.CloseTimeoutMS > 0 {
		return time.Duration(c.CloseTimeoutMS) * time.Millisecond
	}
	return mcpCloseWait
}

// transportName 给出工程意义上的传输名：未写 = stdio（本仓历史契约）。
func (c mcpServerConfig) transportName() string {
	if c.Transport == "" {
		return "stdio"
	}
	return c.Transport
}

func (c mcpServerConfig) enabled() bool { return c.Enabled == nil || *c.Enabled }

func (c mcpServerConfig) callTimeout() time.Duration {
	if c.ToolCallTimeoutMS > 0 {
		return time.Duration(c.ToolCallTimeoutMS) * time.Millisecond
	}
	return mcpDefaultToolCallTimeout
}

// reconnectPolicy 补官方默认值。
//
// 三值约定与全仓一致：未写 = 默认；显式 0 = 也用默认（0 表示"没定义过"）；
// 只有 enabled=false 才是明确关掉。
func (c mcpServerConfig) reconnectPolicy() mcpReconnectPolicy {
	policy := mcpReconnectPolicy{
		enabled:      true,
		initialDelay: 500 * time.Millisecond,
		maxDelay:     30 * time.Second,
		maxAttempts:  10,
	}
	cfg := c.Reconnect
	if cfg == nil {
		return policy
	}
	if cfg.Enabled != nil {
		policy.enabled = *cfg.Enabled
	}
	if cfg.InitialDelayMS > 0 {
		policy.initialDelay = time.Duration(cfg.InitialDelayMS) * time.Millisecond
	}
	if cfg.MaxDelayMS > 0 {
		policy.maxDelay = time.Duration(cfg.MaxDelayMS) * time.Millisecond
	}
	if cfg.MaxAttempts > 0 {
		policy.maxAttempts = cfg.MaxAttempts
	}
	if policy.maxDelay < policy.initialDelay {
		policy.maxDelay = policy.initialDelay
	}
	return policy
}

// MCPToolAnnotations 是 MCP 规范里工具的能力自述。
//
// 为什么宿主需要它：Caps 决定审批与沙箱，而第三方 server 的破坏力不能靠猜。
// 没写 readOnlyHint 就按"可能写盘"处理——放宽一条能力声明的代价是一次静默的
// 破坏性副作用；收紧的代价只是多点一次批准。
type MCPToolAnnotations struct {
	ReadOnlyHint    bool  `json:"readOnlyHint"`
	DestructiveHint *bool `json:"destructiveHint"`
}

// MCPToolDef 是一条 `tools/list` 结果。
type MCPToolDef struct {
	Name         string              `json:"name"`
	Description  string              `json:"description"`
	InputSchema  json.RawMessage     `json:"inputSchema"`
	OutputSchema json.RawMessage     `json:"outputSchema,omitempty"`
	Annotations  *MCPToolAnnotations `json:"annotations,omitempty"`
	Execution    *struct {
		TaskSupport string `json:"taskSupport"`
	} `json:"execution,omitempty"`
}

// MCPStatus 是 `mcp.list` 的一行。ToolCount 用指针区分"没连上"与"连上但 0 个"。
type MCPStatus struct {
	Name      string
	Status    string
	ToolCount *int
	LastError string
}

type mcpCapabilities struct {
	Tools     json.RawMessage `json:"tools"`
	Resources json.RawMessage `json:"resources"`
	Prompts   json.RawMessage `json:"prompts"`
}

type mcpInitializeResult struct {
	ProtocolVersion string          `json:"protocolVersion"`
	Capabilities    mcpCapabilities `json:"capabilities"`
	Instructions    string          `json:"instructions"`
	ServerInfo      struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"serverInfo"`
}

type mcpCallToolResult struct {
	Content           []json.RawMessage `json:"content"`
	StructuredContent json.RawMessage   `json:"structuredContent"`
	IsError           bool              `json:"isError"`
}

// mcpServer 是一个已配置 server 的运行态。
//
// 锁的粒度是**每个 server 一把**：工具调用是并发的，用管理器的全局锁会把
// 一个慢 server 变成所有 server 的串行瓶颈。
type mcpServer struct {
	name   string
	config mcpServerConfig
	raw    map[string]json.RawMessage
	// reg 是**唯一**的服务状态表（internal/mcp.Registry）。本文件不另存一份
	// 状态字符串：所有状态变化都经 publish() 写进 registry，mcp.list 与
	// mcpManager.watch 读同一份。
	reg *mcp.Registry

	mu           sync.Mutex
	client       *mcpClient
	status       mcp.Status
	tools        []MCPToolDef
	instructions string
	hasTools     bool
	hasResources bool
	reconnecting bool
	// failure 留存第一份清理失败（官方 ManagedRecord.failure）：
	// 它不为空时，重启一律拒绝，直到宿主重启。
	failure *mcp.CloseResult
}

func (s *mcpServer) snapshot() (client *mcpClient, status mcp.Status, tools []MCPToolDef) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client, s.status, s.tools
}

// publish 写一次状态：本地留一份用于诊断文案，registry 是公开口径。
func (s *mcpServer) publish(status mcp.Status) {
	s.mu.Lock()
	s.status = status
	s.mu.Unlock()
	if s.reg != nil {
		// Publish 的错误只可能是"状态组合非法"或"registry 已关闭"，
		// 两种都不该让进程路径崩掉；状态本身仍然留在本地供日志用。
		_ = s.reg.Publish(s.name, status)
	}
}

// publishConnected 是"连上并把这一代工具注册完"的那一个状态。
func (s *mcpServer) publishConnected(toolCount int) {
	s.publish(mcp.Status{State: mcp.StateConnected, ToolCount: toolCount, ConnectedAt: time.Now().UnixMilli()})
}

// publishLoadFailure 是官方 mount 失败的口径：failed/load + 原文。
func (s *mcpServer) publishLoadFailure(err error) {
	s.publish(mcp.Status{State: mcp.StateFailed, Reason: mcp.ReasonLoad, LastError: err.Error()})
}

// publishReconnecting 是官方 reconnecting 口径：连了几次、几次为上限、下次什么时候。
func (s *mcpServer) publishReconnecting(attempt, maxAttempts int, nextRetry time.Time, lastErr string) {
	s.publish(mcp.Status{
		State:       mcp.StateReconnecting,
		Attempt:     attempt,
		MaxAttempts: maxAttempts,
		NextRetryAt: nextRetry.UnixMilli(),
		LastError:   lastErr,
	})
}

// publishGaveUp 是官方 gave-up 口径：本轮预算耗尽，工具保持注册但调用会失败。
func (s *mcpServer) publishGaveUp(lastErr string) {
	s.publish(mcp.Status{State: mcp.StateFailed, Reason: mcp.ReasonGaveUp, LastError: lastErr})
}

// publishClosed 把一次关闭判定摊成官方状态：
//   - 关闭确认且没有清理错误 → 有活实例时是 closed；
//   - 有留存失败 → failed/cleanup（带留存结果）；
//   - 首次出现未确认关闭 → failed/unclosed-generation。
func (s *mcpServer) publishClosed(result mcp.CloseResult) {
	retained, hasFailure := s.retainCleanupFailure(result)
	switch {
	case hasFailure:
		s.publish(mcp.Status{State: mcp.StateFailed, Reason: mcp.ReasonCleanup, Close: retained})
	case !result.CloseConfirmed || result.CleanupError != "":
		s.publish(mcp.Status{State: mcp.StateFailed, Reason: mcp.ReasonUnclosedGeneration, Close: retained})
	default:
		s.publish(mcp.Status{State: mcp.StateClosed, Close: retained})
	}
}

// retainCleanupFailure 留存第一份清理诊断与任何未确认的关闭（官方同一手法）：
// 后到的"确认关闭"不许掩盖先前的未确认。
func (s *mcpServer) retainCleanupFailure(result mcp.CloseResult) (mcp.CloseResult, bool) {
	if result.CloseConfirmed && result.CleanupError == "" {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.failure == nil {
			return result, false
		}
		return *s.failure, true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure == nil {
		s.failure = &result
		return result, true
	}
	merged := mcp.CloseResult{
		CloseConfirmed: result.CloseConfirmed && s.failure.CloseConfirmed,
		CleanupError:   s.failure.CleanupError,
	}
	if merged.CleanupError == "" {
		merged.CleanupError = result.CleanupError
	}
	s.failure = &merged
	return merged, true
}

// retainedFailure 报告是否有留存的清理失败（重启的硬门）。
func (s *mcpServer) retainedFailure() (mcp.CloseResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure == nil {
		return mcp.CloseResult{CloseConfirmed: true}, false
	}
	return *s.failure, true
}

// call 在 server 的连接上发一条请求。没连上时给的是**可操作**的失败。
func (s *mcpServer) call(ctx context.Context, method string, params any, out any) error {
	client, status, _ := s.snapshot()
	if client == nil {
		return fmt.Errorf("%w: server %q 当前状态 %s；先 mcp.reconnect 或检查它的 stderr",
			ErrMCPServerDisconnected, s.name, status.State)
	}
	return client.call(ctx, method, params, out)
}

// stop 收掉当前一代连接，返回可判定的关闭结果（官方 closeConfirmed/cleanupError）。
//
// "没有活连接"也算确认关闭：官方 closeConfirmed 的判据就是"传输关闭已确认，
// 或压根没建 transport"。把它算成未确认会让每一次全新启动都被判成清理失败。
func (s *mcpServer) stop() mcp.CloseResult {
	s.mu.Lock()
	client := s.client
	budget := s.config.closeBudget()
	s.client = nil
	s.tools = nil
	s.mu.Unlock()
	if client == nil {
		return mcp.CloseResult{CloseConfirmed: true}
	}
	return client.CloseWithBudget(budget, nil)
}

// MCPManager 管全部配置好的 server。
type MCPManager struct {
	mu      sync.Mutex
	path    string
	root    map[string]json.RawMessage
	servers map[string]*mcpServer
	order   []string
	loadErr error
	// reg 是全部 server 状态的唯一去处（mcp.list / mcpManager.watch 共用）。
	reg *mcp.Registry
	// lifecycleMu 串行化"会替换活实例"的操作（restart/reconnect/toggle），
	// 对齐官方那条"settled operation tail serializing every lifecycle operation"。
	lifecycleMu sync.Mutex

	closed   atomic.Bool
	closedCh chan struct{}
	wg       sync.WaitGroup
}

// OpenMCPManager 读 <home>/mcp.json。**不**启动任何子进程。
func OpenMCPManager(home string) *MCPManager {
	m := &MCPManager{
		path:     filepath.Join(home, "mcp.json"),
		servers:  map[string]*mcpServer{},
		reg:      mcp.NewRegistry(),
		closedCh: make(chan struct{}),
	}
	raw, err := os.ReadFile(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			m.root = map[string]json.RawMessage{"mcpServers": json.RawMessage(`{}`)}
			return m
		}
		m.loadErr = fmt.Errorf("读 MCP 配置 %s: %w", m.path, err)
		return m
	}
	if err := json.Unmarshal(raw, &m.root); err != nil || m.root == nil {
		m.loadErr = fmt.Errorf("解析 MCP 配置 %s: %w", m.path, err)
		return m
	}
	rawServers, exists := m.root["mcpServers"]
	servers := map[string]json.RawMessage{}
	if exists {
		if err := json.Unmarshal(rawServers, &servers); err != nil || servers == nil {
			m.loadErr = fmt.Errorf("MCP 配置 mcpServers 必须是对象: %w", err)
			return m
		}
	}
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		rawServer := servers[name]
		var fields map[string]json.RawMessage
		var config mcpServerConfig
		if name == "" || json.Unmarshal(rawServer, &fields) != nil || json.Unmarshal(rawServer, &config) != nil || config.Command == "" {
			m.loadErr = fmt.Errorf("MCP server %q 配置无效：需要非空 name/command 与合法参数", name)
			return m
		}
		// 命名空间是公开契约的一部分：serverName 里出现 "__" 会让
		// `mcp__a__b__c` 既能读成 (a, b__c) 也能读成 (a__b, c)。
		// 这种歧义不许留到运行时——那时它表现为"两个工具随机互相覆盖"。
		if !mcpServerNamePattern.MatchString(name) {
			m.loadErr = fmt.Errorf("MCP server %q 名字非法：必须匹配 %s（1-32 位 [A-Za-z0-9_-]）", name, mcpServerNamePattern)
			return m
		}
		server := &mcpServer{name: name, config: config, raw: fields, reg: m.reg}
		initial := mcp.Status{State: mcp.StateConnecting}
		if !config.enabled() {
			initial = mcp.Status{State: mcp.StateDisabled}
		}
		// 登记失败只可能是状态组合非法，这里的状态是常量，出错即编程错误。
		if err := m.reg.Track(name, true, initial); err != nil {
			m.loadErr = fmt.Errorf("MCP server %q 初始状态非法: %w", name, err)
			return m
		}
		m.servers[name] = server
		m.order = append(m.order, name)
	}
	return m
}

// StartAll 按配置顺序连接所有 enabled 的 server。
//
// 同步而不是异步：工具注册要求"装配完成时工具表已经是最终形态"，否则
// 模型会在头几个请求里看到一张逐渐变长的表（前缀缓存全废）。
// StartAll 连接所有 enabled 的 server。
//
// 返回值只携带**契约违规**（ErrMCPProtocol）：那是配置/服务器自身的 bug，
// 必须让装配失败。传输层失败（起不来、握手超时）只记 status=error——一个
// 临时挂掉的 MCP server 不该让整个宿主启不来（官方 failOnStartupError 默认 false）。
func (m *MCPManager) StartAll() error {
	m.mu.Lock()
	if m.loadErr != nil || m.closed.Load() {
		m.mu.Unlock()
		return nil
	}
	enabled := make([]*mcpServer, 0, len(m.order))
	for _, name := range m.order {
		server := m.servers[name]
		switch {
		case !server.config.enabled():
			server.publish(mcp.Status{State: mcp.StateDisabled})
		case server.config.transportName() != "stdio":
			// 结构化未实现：本宿主只起 stdio 子进程。静默跳过会让 mcp.list
			// 显示一个永远 connecting 的行，管理面看不出真正原因。
			server.publishLoadFailure(mcp.UnsupportedTransport(server.name, server.config.transportName()))
			slog.Default().Warn("MCP server 的 transport 本宿主未实现，跳过",
				"server", server.name, "transport", server.config.transportName())
		default:
			enabled = append(enabled, server)
		}
	}
	m.mu.Unlock()
	var contractErrs []error
	for _, server := range enabled {
		err := m.connect(server)
		if err == nil {
			continue
		}
		if errors.Is(err, ErrMCPProtocol) {
			// 契约违规是装配期错误：必须让装配失败，不许当成"临时连不上"。
			server.publishLoadFailure(err)
			contractErrs = append(contractErrs, fmt.Errorf("MCP server %q: %w", server.name, err))
			continue
		}
		server.publishLoadFailure(err)
		slog.Default().Warn("MCP server 连接失败：它的工具不会进工具表",
			"server", server.name, "error", err)
	}
	return errors.Join(contractErrs...)
}

// connect 起新一代连接并做工具发现。
//
// 状态口径与官方一致：开始 attempt 时 connecting{attempt:0}，成功后
// connected{toolCount, connectedAt}；失败的状态由调用方按场景发布
// （首次挂载失败 = failed/load，重连预算耗尽 = failed/gave-up）。
func (m *MCPManager) connect(server *mcpServer) error {
	server.stop()
	server.publish(mcp.Status{State: mcp.StateConnecting})

	client, init, tools, err := mcpHandshake(server.config)
	if err != nil {
		return err
	}
	server.mu.Lock()
	server.client = client
	server.tools = tools
	server.instructions = strings.TrimSpace(init.Instructions)
	server.hasTools = len(init.Capabilities.Tools) > 0 && string(init.Capabilities.Tools) != "null"
	server.hasResources = len(init.Capabilities.Resources) > 0 && string(init.Capabilities.Resources) != "null"
	server.reconnecting = false
	server.mu.Unlock()
	server.publishConnected(len(tools))

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.watch(server, client)
	}()
	return nil
}

// mcpHandshake 做 initialize → initialized → tools/list 全套。
func mcpHandshake(config mcpServerConfig) (*mcpClient, mcpInitializeResult, []MCPToolDef, error) {
	var init mcpInitializeResult
	client, err := startMCPClient(config)
	if err != nil {
		return nil, init, nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), mcpHandshakeTimeout)
	defer cancel()
	if err := client.call(ctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": mcpClientName, "version": mcpClientVersion},
	}, &init); err != nil {
		client.Close()
		return nil, init, nil, fmt.Errorf("MCP initialize 失败: %w", err)
	}
	if err := client.notify("notifications/initialized", map[string]any{}); err != nil {
		client.Close()
		return nil, init, nil, fmt.Errorf("MCP notifications/initialized 失败: %w", err)
	}
	hasTools := len(init.Capabilities.Tools) > 0 && string(init.Capabilities.Tools) != "null"
	tools, err := listMCPTools(ctx, client, hasTools)
	if err != nil {
		client.Close()
		return nil, init, nil, err
	}
	return client, init, tools, nil
}

// listMCPTools 聚合全部页。
func listMCPTools(ctx context.Context, client *mcpClient, hasTools bool) ([]MCPToolDef, error) {
	if !hasTools {
		// 官方判据：没声明 tools 能力就不聚合工具列表。反向做的话，
		// 一个"只提供资源"的 server 会被我们当成"提供了 0 个工具"，
		// 而它其实压根没打算提供工具——两者的运维含义完全不同。
		return nil, nil
	}
	var defs []MCPToolDef
	seen := map[string]struct{}{}
	cursor := ""
	for page := 0; page < mcpMaxToolPages; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var result struct {
			Tools      []MCPToolDef `json:"tools"`
			NextCursor string       `json:"nextCursor"`
		}
		if err := client.call(ctx, "tools/list", params, &result); err != nil {
			return nil, err
		}
		for _, def := range result.Tools {
			if def.Name == "" {
				return nil, fmt.Errorf("%w: tools/list 含无名工具", ErrMCPProtocol)
			}
			if _, dup := seen[def.Name]; dup {
				// 官方原话：server listed tool "<name>" more than once — invalid tool list
				return nil, fmt.Errorf("%w: server 把工具 %q 列了不止一次 —— 非法工具列表", ErrMCPProtocol, def.Name)
			}
			seen[def.Name] = struct{}{}
			if def.Execution != nil && def.Execution.TaskSupport == "required" {
				// 官方：要求 task-based execution 的工具在调用时拒绝，这里提前拒绝，
				// 免得模型以为它在工具表里就能用。
				return nil, fmt.Errorf("%w: 工具 %q 要求 task-based execution，本桥不支持", ErrMCPProtocol, def.Name)
			}
			if len(def.InputSchema) > 0 && !json.Valid(def.InputSchema) {
				return nil, fmt.Errorf("%w: 工具 %q 的 inputSchema 不是合法 JSON", ErrMCPProtocol, def.Name)
			}
			defs = append(defs, def)
		}
		if result.NextCursor == "" {
			return defs, nil
		}
		if result.NextCursor == cursor {
			return nil, fmt.Errorf("%w: tools/list nextCursor 没有前进", ErrMCPProtocol)
		}
		cursor = result.NextCursor
	}
	return nil, fmt.Errorf("%w: tools/list 超过 %d 页", ErrMCPProtocol, mcpMaxToolPages)
}

// watch 盯着这一代连接；断了就交给重连策略。
func (m *MCPManager) watch(server *mcpServer, client *mcpClient) {
	select {
	case <-client.done:
	case <-m.closedCh:
		return
	}
	server.mu.Lock()
	if server.client != client {
		// 已经被 stop()/connect() 换掉了：这是预期内的换代，不是掉线。
		server.mu.Unlock()
		return
	}
	server.client = nil
	server.reconnecting = false
	lastErr := "MCP 子进程已退出"
	if err := client.WaitError(); err != nil {
		lastErr = err.Error()
	}
	stderr := client.Stderr()
	server.mu.Unlock()
	policy := server.config.reconnectPolicy()
	switch {
	case m.closed.Load():
		return
	case !policy.enabled:
		// 重连关掉 = 这一代到此为止（官方 reconnect-disabled）。
		server.publish(mcp.Status{State: mcp.StateFailed, Reason: mcp.ReasonReconnectDisabled, LastError: lastErr})
		return
	default:
		server.publishReconnecting(1, policy.maxAttempts, time.Now().Add(policy.initialDelay), lastErr)
	}
	if stderr != "" {
		slog.Default().Warn("MCP server 退出前的 stderr", "server", server.name, "stderr", stderr)
	}
	if m.closed.Load() {
		return
	}
	m.scheduleReconnect(server)
}

func (m *MCPManager) scheduleReconnect(server *mcpServer) {
	policy := server.config.reconnectPolicy()
	if !policy.enabled {
		return
	}
	server.mu.Lock()
	if server.reconnecting || m.closed.Load() {
		server.mu.Unlock()
		return
	}
	server.reconnecting = true
	server.mu.Unlock()

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer func() {
			server.mu.Lock()
			server.reconnecting = false
			server.mu.Unlock()
		}()
		delay := policy.initialDelay
		lastErr := ""
		for attempt := 1; attempt <= policy.maxAttempts; attempt++ {
			// 每一次退避都是一次对外可见的状态（官方 reconnecting 带下次时间）。
			server.publishReconnecting(attempt, policy.maxAttempts, time.Now().Add(delay), lastErr)
			select {
			case <-m.closedCh:
				return
			case <-time.After(delay):
			}
			if m.closed.Load() {
				return
			}
			server.mu.Lock()
			needConnect := server.client == nil
			server.mu.Unlock()
			if !needConnect {
				return
			}
			if err := m.connect(server); err == nil {
				slog.Default().Info("MCP server 重连成功", "server", server.name, "attempt", attempt)
				return
			} else {
				lastErr = err.Error()
				slog.Default().Warn("MCP server 重连失败", "server", server.name, "attempt", attempt, "error", err)
			}
			delay *= 2
			if delay > policy.maxDelay {
				delay = policy.maxDelay
			}
		}
		// 官方：耗尽预算后工具保持可用名字但调用失败，只有重新加载插件才能回来。
		server.publishGaveUp(lastErr)
		slog.Default().Error("MCP server 重连预算耗尽；它的工具仍注册着，但调用会失败（用 mcp.reconnect 手动恢复）",
			"server", server.name, "maxAttempts", policy.maxAttempts)
	}()
}

// Statuses 返回 `mcp.list` 需要的行（按配置名排序）。
func (m *MCPManager) Statuses() ([]MCPStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	// 读 registry（不是 server 上的副本）：这是"mcp.list 与 mcpManager.watch
	// 共用同一份状态"的落点，两条面永远不会出现不同的 status。
	views := m.reg.List()
	out := make([]MCPStatus, 0, len(views))
	for _, view := range views {
		row := MCPStatus{Name: view.Name, Status: legacyStatusName(view.Status), LastError: view.Status.LastError}
		if view.Status.State == mcp.StateConnected {
			count := view.Status.ToolCount
			row.ToolCount = &count
		}
		out = append(out, row)
	}
	return out, nil
}

// ServerPID 返回某个 server 当前子进程的 PID；没连上时是 0。
// 只给测试与诊断用——生产路径不该按 PID 认进程。
func (m *MCPManager) ServerPID(name string) int {
	m.mu.Lock()
	server := m.servers[name]
	m.mu.Unlock()
	if server == nil {
		return 0
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.client == nil || server.client.cmd == nil || server.client.cmd.Process == nil {
		return 0
	}
	return server.client.cmd.Process.Pid
}

// Disconnected 返回某个 server 是否已经没有活连接（测试用：等子进程真退出）。
func (m *MCPManager) Disconnected(name string) bool {
	m.mu.Lock()
	server := m.servers[name]
	m.mu.Unlock()
	if server == nil {
		return true
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	return server.client == nil
}

// Reconnect 手动重连一个 server。
func (m *MCPManager) Reconnect(name string) error {
	m.mu.Lock()
	loadErr := m.loadErr
	server := m.servers[name]
	m.mu.Unlock()
	if loadErr != nil {
		return loadErr
	}
	if server == nil {
		return fmt.Errorf("%w: %s", ErrMCPServerNotFound, name)
	}
	if m.closed.Load() {
		return errors.New("MCP manager 已关闭")
	}
	if !server.config.enabled() {
		return fmt.Errorf("MCP server %s 已 disabled，不能 reconnect", name)
	}
	// 会替换活实例：与 restart/toggle 串行，避免两代实例互相覆盖。
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	if err := m.connect(server); err != nil {
		server.publishLoadFailure(err)
		return err
	}
	return nil
}

// Toggle 打开/关闭一个 server，并把 enabled 写回 mcp.json。
func (m *MCPManager) Toggle(name string, enabled bool) error {
	m.mu.Lock()
	if m.loadErr != nil {
		err := m.loadErr
		m.mu.Unlock()
		return err
	}
	server := m.servers[name]
	if server == nil {
		m.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrMCPServerNotFound, name)
	}
	if m.closed.Load() {
		m.mu.Unlock()
		return errors.New("MCP manager 已关闭")
	}
	oldRaw := append(json.RawMessage(nil), m.root["mcpServers"]...)
	newRawServer := make(map[string]json.RawMessage, len(server.raw)+1)
	for key, value := range server.raw {
		newRawServer[key] = value
	}
	encodedEnabled, _ := json.Marshal(enabled)
	newRawServer["enabled"] = encodedEnabled
	rawServer, _ := json.Marshal(newRawServer)
	servers := map[string]json.RawMessage{}
	for _, key := range m.order {
		if key == name {
			servers[key] = rawServer
		} else {
			servers[key], _ = json.Marshal(m.servers[key].raw)
		}
	}
	rawServers, _ := json.Marshal(servers)
	m.root["mcpServers"] = rawServers
	if err := m.flushLocked(); err != nil {
		m.root["mcpServers"] = oldRaw
		m.mu.Unlock()
		return err
	}
	server.raw = newRawServer
	server.config.Enabled = &enabled
	m.mu.Unlock()

	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	if !enabled {
		// 关掉一个 server 也是一次 unmount：关闭没确认 / 清理报错都要留存，
		// 否则"关不干净"会被 disabled 这个状态掩掉，之后重启还会被同一个
		// 幽灵实例抢 namespace。
		closed := server.stop()
		retained, hasFailure := server.retainCleanupFailure(closed)
		if hasFailure {
			server.publish(mcp.Status{State: mcp.StateFailed, Reason: mcp.ReasonCleanup, Close: retained})
			return nil
		}
		server.publish(mcp.Status{State: mcp.StateDisabled})
		return nil
	}
	if err := m.connect(server); err != nil {
		// 磁盘与内存都保持 enabled=true + error；不得回滚成用户没有请求过的状态。
		server.publishLoadFailure(err)
		return err
	}
	return nil
}

func (m *MCPManager) flushLocked() error {
	raw, err := json.MarshalIndent(m.root, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(m.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".mcp-*.tmp")
	if err != nil {
		return err
	}
	path := tmp.Name()
	defer os.Remove(path)
	if _, err = tmp.Write(append(raw, '\n')); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(path, m.path)
}

// Close 收掉全部子进程并等重连协程退出。幂等。
func (m *MCPManager) Close() {
	m.mu.Lock()
	if m.closed.Load() {
		m.mu.Unlock()
		return
	}
	m.closed.Store(true)
	close(m.closedCh)
	servers := make([]*mcpServer, 0, len(m.order))
	for _, name := range m.order {
		servers = append(servers, m.servers[name])
	}
	m.mu.Unlock()
	for _, server := range servers {
		closed := server.stop()
		if !closed.CloseConfirmed || closed.CleanupError != "" {
			// 已释放的管理器不再发事件（官方同一取舍），但最后的未确认关闭
			// 要在 registry 关掉之前留痕，否则"关不干净"永远查不出来。
			server.publishClosed(closed)
		}
	}
	m.wg.Wait()
	m.reg.Close()
}

// MCPPublicToolName 是公开名的**唯一**实现（官方 publicToolName 的逐条移植）。
//
// 干净情形就是 `mcp__<server>__<raw>` 原样。一旦归一化真的改了字（替换非法字符
// 或截断到 64 位），追加 12 位 SHA-256——否则 `bad/name` 与 `bad:name` 会塌成
// 同一个公开名，而被静默覆盖的那个工具永远不会有人发现。
//
// 划界：哈希是纯函数，看不见别的 server 叫什么，所以它兜的是"归一化塌陷"，
// 兜不住 `("a","b__c")` 与 `("a__b","c")` 这种 `__` 歧义。后者由
// RegisterInto 的冲突检查**响亮失败**（ErrMCPToolConflict）——官方也这么分。
//
// 归一化之后字符串只含 `[A-Za-z0-9_-]`，所以按字节切片等于按字符切片。
func MCPPublicToolName(serverName, rawName string) string {
	joined := "mcp__" + serverName + "__" + rawName
	normalized := mcpInvalidNameChars.ReplaceAllString(joined, "_")
	if normalized == joined && len(normalized) <= mcpMaxPublicNameLength {
		return normalized
	}
	sum := sha256.Sum256([]byte(serverName + "\x00" + rawName))
	digest := hex.EncodeToString(sum[:])[:mcpNameHashLength]
	keep := mcpMaxPublicNameLength - mcpNameHashLength - 1
	if keep > len(normalized) {
		keep = len(normalized)
	}
	return normalized[:keep] + "_" + digest
}

// mcpTool 是 MCP 工具在本仓 SPI 里的适配器。
type mcpTool struct {
	publicName  string
	rawName     string
	description string
	schema      json.RawMessage
	server      *mcpServer
	timeout     time.Duration
	readOnly    bool
	destructive bool
}

func (t *mcpTool) Name() string { return t.publicName }

func (t *mcpTool) Description() string { return t.description }

func (t *mcpTool) Schema() json.RawMessage { return t.schema }

// Caps 把"第三方代码能干什么"翻译成本仓的能力声明。
//
// Executes/Network 恒为 true：它一定起了一个子进程，而这个子进程会连什么我们
// 无法证明。Writes/Destructive 只能采信 server 自报的 annotations——没报就当
// 它会写（fail-closed 的方向是收紧，不是放宽）。
func (t *mcpTool) Caps() spi.Caps {
	return spi.Caps{
		Executes:    true,
		Network:     true,
		Writes:      !t.readOnly,
		Destructive: t.destructive && !t.readOnly,
	}
}

func (t *mcpTool) Exec(ctx context.Context, args json.RawMessage) (spi.Result, error) {
	arguments, err := mcpArguments(args)
	if err != nil {
		return spi.Result{Hint: "tool_call.arguments 必须是 JSON 对象；常见原因是引号没转义或写成了字符串"},
			fmt.Errorf("MCP 工具 %s 的参数不是 JSON 对象: %w", t.publicName, err)
	}
	callCtx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	var result mcpCallToolResult
	if err := t.server.call(callCtx, "tools/call", map[string]any{
		"name":      t.rawName,
		"arguments": arguments,
	}, &result); err != nil {
		return spi.Result{Hint: mcpCallHint(t.server.name, err)},
			fmt.Errorf("MCP 工具 %s 调用失败: %w", t.publicName, err)
	}
	text := mcpExtractText(result.Content, t.rawName)
	data := mcpCanonicalValue(result)
	if result.IsError {
		// 官方：isError 结果在图片持久化之前就抛。这里同样——失败就是失败，
		// 不能因为 content 里有文字就当成功。
		return spi.Result{
				Summary: fmt.Sprintf("MCP 工具 %s 返回错误", t.publicName),
				Text:    text,
				Data:    data,
				Hint:    "这是 MCP server 自己报告的失败（isError），不是宿主的参数校验；先读错误原文再决定改参数还是换工具",
			},
			fmt.Errorf("MCP 工具 %s 返回 isError: %s", t.publicName, text)
	}
	return spi.Result{
		Summary: fmt.Sprintf("MCP %s/%s 调用成功", t.server.name, t.rawName),
		Text:    text,
		Data:    data,
	}, nil
}

func mcpArguments(args json.RawMessage) (map[string]any, error) {
	if len(args) == 0 {
		return map[string]any{}, nil
	}
	var value any
	if err := json.Unmarshal(args, &value); err != nil {
		return nil, err
	}
	if value == nil {
		return map[string]any{}, nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("必须是 JSON 对象")
	}
	return object, nil
}

// mcpExtractText 是官方 extractText + projectContent 的移植。
//
// 规则只有一条：**模型只看得见文字**。二进制（图片/音频/嵌入资源）在历史里
// 只能是字数说明——把 base64 灌进 append-only 日志，等于每次调用都把会话文件
// 撑大几百 KB，而且模型也读不懂。
func mcpExtractText(content []json.RawMessage, rawName string) string {
	parts := make([]string, 0, len(content))
	for _, raw := range content {
		var block map[string]any
		if err := json.Unmarshal(raw, &block); err != nil || block == nil {
			parts = append(parts, "[unsupported MCP content block: expected an object]")
			continue
		}
		kind, _ := block["type"].(string)
		switch kind {
		case "text":
			if text, ok := block["text"].(string); ok {
				parts = append(parts, text)
			}
		case "image":
			parts = append(parts, "[image result not admitted: this build does not prove image routing for MCP results; raw base64 remains available to programmatic callers]")
		case "resource_link":
			name, _ := block["name"].(string)
			uri, _ := block["uri"].(string)
			if name == "" || uri == "" {
				parts = append(parts, "[resource link unavailable: the MCP block is missing its name or URI]")
			} else {
				parts = append(parts, fmt.Sprintf("Resource link: %s (%s)", name, uri))
			}
		case "audio":
			mime, _ := block["mimeType"].(string)
			if mime == "" {
				mime = "unknown media type"
			}
			parts = append(parts, fmt.Sprintf("[audio result unsupported: %s; raw audio data remains available to programmatic callers]", mime))
		case "resource":
			parts = append(parts, "[embedded resource unsupported; raw resource data remains available to programmatic callers]")
		default:
			parts = append(parts, fmt.Sprintf("[unsupported MCP content type: %s]", kind))
		}
	}
	if len(parts) == 0 {
		return fmt.Sprintf("(%s returned no model-visible content)", rawName)
	}
	return strings.Join(parts, "\n")
}

// mcpCanonicalValue 是官方规范值 `{content, structuredContent?}` 的 Go 投影。
// 二进制同样换成说明文字，理由同 mcpExtractText。
func mcpCanonicalValue(result mcpCallToolResult) map[string]any {
	content := make([]any, 0, len(result.Content))
	for _, raw := range result.Content {
		var value any
		if json.Unmarshal(raw, &value) != nil {
			continue
		}
		content = append(content, sanitizeMCPValue(value))
	}
	value := map[string]any{"content": content}
	if len(result.StructuredContent) > 0 {
		var structured any
		if json.Unmarshal(result.StructuredContent, &structured) == nil {
			value["structuredContent"] = sanitizeMCPValue(structured)
		}
	}
	return value
}

// sanitizeMCPValue 递归把 blob 换成字数说明（官方 renderResourceResult 的同一手法）。
func sanitizeMCPValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			if key == "blob" {
				if text, ok := item.(string); ok {
					out[key] = fmt.Sprintf("[binary resource: %d base64 characters; available to programmatic callers]", len(text))
					continue
				}
			}
			out[key] = sanitizeMCPValue(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = sanitizeMCPValue(item)
		}
		return out
	default:
		return value
	}
}

func mcpCallHint(server string, err error) string {
	var rpcErr *mcpRPCError
	switch {
	case errors.As(err, &rpcErr):
		return "MCP server 自己拒绝了这次调用（JSON-RPC error）；按错误原文改参数，不要把同样的参数再发一次"
	case errors.Is(err, context.DeadlineExceeded):
		return "这次调用超过了该 server 的 toolCallTimeoutMs；把任务拆小，或调大 mcp.json 里这个 server 的 toolCallTimeoutMs 再试"
	case errors.Is(err, context.Canceled):
		return "调用被取消，MCP 侧是否已经执行未知；要结果就重新发起一次，不要假设它没做"
	case errors.Is(err, ErrMCPServerDisconnected):
		return "MCP server " + server + " 当前没有活连接；先 mcp.reconnect，再看它的 stderr"
	default:
		return "MCP server " + server + " 的这次调用没有成功；先确认它还活着（mcp.list 里的 status），再看 stderr 定位"
	}
}

// RegisterInto 把已发现的工具注册进工具目录。
//
// 这是**唯一**的注册入口，也是 fail-loud 的落点：名字冲突、schema 非法、
// serverName 歧义都在这里返回错误，装配因此失败。静默跳过任何一个，
// 模型就会拿到一张它自己也不知道缺了什么的表。
func (m *MCPManager) RegisterInto(reg spi.ToolRegistry) error {
	m.mu.Lock()
	if m.loadErr != nil {
		err := m.loadErr
		m.mu.Unlock()
		return err
	}
	type pending struct {
		public string
		tool   *mcpTool
	}
	items := make([]pending, 0, len(m.order))
	seen := map[string]string{}
	enabledServers := make([]string, 0, len(m.order))
	for _, name := range m.order {
		server := m.servers[name]
		if !server.config.enabled() {
			continue
		}
		enabledServers = append(enabledServers, name)
		server.mu.Lock()
		defs := append([]MCPToolDef(nil), server.tools...)
		server.mu.Unlock()
		for _, def := range defs {
			public := MCPPublicToolName(server.name, def.Name)
			identity := server.name + "\x00" + def.Name
			if prev, dup := seen[public]; dup {
				m.mu.Unlock()
				return fmt.Errorf("%w: 公开名 %q 同时来自两个 MCP 身份（%s）；"+
					"server 名或工具名里不要用 \"__\" 制造歧义，或停用其中一个",
					ErrMCPToolConflict, public, strings.ReplaceAll(prev+" | "+identity, "\x00", "/"))
			}
			seen[public] = identity
			readOnly := def.Annotations != nil && def.Annotations.ReadOnlyHint
			destructive := def.Annotations != nil && def.Annotations.DestructiveHint != nil && *def.Annotations.DestructiveHint
			schema := def.InputSchema
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
			}
			description := def.Description
			if description == "" {
				description = fmt.Sprintf("MCP server %q 提供的工具 %q（server 没给描述）", server.name, def.Name)
			}
			items = append(items, pending{public: public, tool: &mcpTool{
				publicName:  public,
				rawName:     def.Name,
				description: description,
				schema:      schema,
				server:      server,
				timeout:     server.config.callTimeout(),
				readOnly:    readOnly,
				destructive: destructive,
			}})
		}
	}
	m.mu.Unlock()

	// 先整体查重，再动手注册：注册到一半失败会留下半张表，
	// 而工具表是缓存前缀——半张表的代价比一次响亮的装配失败大得多。
	for _, item := range items {
		if _, exists := reg.Get(item.public); exists {
			return fmt.Errorf("%w: 公开名 %q 已被一个非 MCP 工具占用", ErrMCPToolConflict, item.public)
		}
	}
	for _, item := range items {
		if err := reg.Register(item.tool); err != nil {
			return fmt.Errorf("注册 MCP 工具 %q 失败: %w", item.public, err)
		}
	}
	if len(enabledServers) == 0 {
		// 官方：没有可见提供方时本包不增加任何工具与提示词。
		return nil
	}
	return registerMCPResourceTools(reg, m, enabledServers)
}

// ---- MCP 资源（官方 @deepseek-ai/dsh-mcp-resources 的三个共享工具） ----

const (
	mcpListResourcesToolName    = "list_mcp_resources"
	mcpListTemplatesToolName    = "list_mcp_resource_templates"
	mcpReadResourceToolName     = "read_mcp_resource"
	mcpResourceServerParamDescr = "Configured MCP server name."
)

type mcpResourceTool struct {
	name        string
	description string
	schema      json.RawMessage
	manager     *MCPManager
}

func (t *mcpResourceTool) Name() string { return t.name }

func (t *mcpResourceTool) Description() string { return t.description }

func (t *mcpResourceTool) Schema() json.RawMessage { return t.schema }

// Caps：资源工具只走 server 的 resources/* 请求。它一定起了子进程、一定联网，
// 但**不**写盘、**不**具破坏性——读资源是这个工具存在的全部意义。
func (t *mcpResourceTool) Caps() spi.Caps {
	return spi.Caps{Executes: true, Network: true}
}

func (t *mcpResourceTool) Exec(ctx context.Context, args json.RawMessage) (spi.Result, error) {
	var in struct {
		Server string `json:"server"`
		Cursor string `json:"cursor"`
		URI    string `json:"uri"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return spi.Result{Hint: "tool_call.arguments 必须是 JSON 对象"}, fmt.Errorf("%s 参数非法: %w", t.name, err)
	}
	server := strings.TrimSpace(in.Server)
	if server == "" {
		return spi.Result{Hint: "必须给 server 参数；可用名字见 mcp.list 或本工具的参数说明"},
			fmt.Errorf("%s 缺少必填参数 server", t.name)
	}
	var (
		value any
		err   error
	)
	switch t.name {
	case mcpListResourcesToolName:
		value, err = t.manager.resourceRequest(ctx, server, "resources/list", in.Cursor, "")
	case mcpListTemplatesToolName:
		value, err = t.manager.resourceRequest(ctx, server, "resources/templates/list", in.Cursor, "")
	default:
		if strings.TrimSpace(in.URI) == "" {
			return spi.Result{Hint: "必须给 uri 参数；先用 list_mcp_resources 或 list_mcp_resource_templates 拿到一个"},
				fmt.Errorf("%s 缺少必填参数 uri", t.name)
		}
		value, err = t.manager.resourceRequest(ctx, server, "resources/read", "", in.URI)
	}
	if err != nil {
		return spi.Result{Hint: "MCP 资源请求失败：先确认 server 在 mcp.list 里是 connected，再确认它声明了 resources 能力"},
			fmt.Errorf("%s(%s) 失败: %w", t.name, server, err)
	}
	text := renderMCPResourceResult(server, value)
	return spi.Result{
		Summary: fmt.Sprintf("MCP %s 资源请求成功", server),
		Text:    text,
		Data:    map[string]any{"server": server, "result": sanitizeMCPValue(value)},
	}, nil
}

// renderMCPResourceResult 是官方 renderResourceResult 的移植：
// 以 `MCP server: <name>` 起头，随后是 JSON，二进制换成字数说明。
func renderMCPResourceResult(server string, value any) string {
	raw, err := json.Marshal(sanitizeMCPValue(value))
	if err != nil {
		return "MCP server: " + server + "\n" + fmt.Sprintf("%v", value)
	}
	return "MCP server: " + server + "\n" + string(raw)
}

// resourceRequest 在指定 server 上发一条 resources/* 请求。
func (m *MCPManager) resourceRequest(ctx context.Context, server, method, cursor, uri string) (any, error) {
	m.mu.Lock()
	state := m.servers[server]
	m.mu.Unlock()
	if state == nil {
		return nil, fmt.Errorf("%w: MCP resource server %q 未配置", ErrMCPServerNotFound, server)
	}
	state.mu.Lock()
	connected := state.client != nil
	hasResources := state.hasResources
	timeout := state.config.callTimeout()
	state.mu.Unlock()
	if !connected {
		return nil, fmt.Errorf("%w: MCP resource server %q 当前不可用", ErrMCPServerDisconnected, server)
	}
	// 官方：没声明 resources 能力的 server，列表/模板直接返回空页而**不联系**它。
	// 为什么：某些 server 的兜底实现会往 stdout 写诊断，而那上面跑的是 JSON-RPC。
	if !hasResources && method != "resources/read" {
		switch method {
		case "resources/list":
			return map[string]any{"resources": []any{}}, nil
		case "resources/templates/list":
			return map[string]any{"resourceTemplates": []any{}}, nil
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	params := map[string]any{}
	switch method {
	case "resources/read":
		params["uri"] = uri
	case "resources/list", "resources/templates/list":
		if cursor != "" {
			params["cursor"] = cursor
		}
	default:
		return nil, fmt.Errorf("未知 MCP 资源方法 %q", method)
	}
	var out any
	if err := state.call(callCtx, method, params, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func registerMCPResourceTools(reg spi.ToolRegistry, manager *MCPManager, servers []string) error {
	names := append([]string(nil), servers...)
	sort.Strings(names)
	serverDescription := mcpResourceServerParamDescr
	if len(names) > 0 {
		serverDescription = fmt.Sprintf("%s One of: %s.", mcpResourceServerParamDescr, strings.Join(names, ", "))
	}
	listSchema := mcpResourceListSchema(serverDescription)
	readSchema := mcpResourceReadSchema(serverDescription)
	tools := []*mcpResourceTool{
		{
			name:        mcpListResourcesToolName,
			description: "List resources available from an MCP server.",
			schema:      listSchema,
			manager:     manager,
		},
		{
			name:        mcpListTemplatesToolName,
			description: "List parameterized resource URI templates from an MCP server.",
			schema:      listSchema,
			manager:     manager,
		},
		{
			name:        mcpReadResourceToolName,
			description: "Read an MCP resource by URI from the named server. Use a listed URI or an expanded resource template.",
			schema:      readSchema,
			manager:     manager,
		},
	}
	for _, tool := range tools {
		if _, exists := reg.Get(tool.name); exists {
			return fmt.Errorf("%w: 资源工具名 %q 已被占用", ErrMCPToolConflict, tool.name)
		}
	}
	for _, tool := range tools {
		if err := reg.Register(tool); err != nil {
			return fmt.Errorf("注册 MCP 资源工具 %q 失败: %w", tool.name, err)
		}
	}
	return nil
}

func mcpResourceListSchema(serverDescription string) json.RawMessage {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"server": map[string]any{"type": "string", "description": serverDescription},
			"cursor": map[string]any{"type": "string", "description": "Continuation cursor returned by this server."},
		},
		"required":             []string{"server"},
		"additionalProperties": false,
	}
	raw, _ := json.Marshal(schema)
	return raw
}

func mcpResourceReadSchema(serverDescription string) json.RawMessage {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"server": map[string]any{"type": "string", "description": serverDescription},
			"uri":    map[string]any{"type": "string", "description": "Resource URI to read."},
		},
		"required":             []string{"server", "uri"},
		"additionalProperties": false,
	}
	raw, _ := json.Marshal(schema)
	return raw
}

// ---- 官方 0.2.2 MCP 管理面：mcpManager.probe / restart / watch ----
//
// 这三个方法**不新建状态机**：它们读写的都是上面那一个 mcp.Registry
// （mcp.list / mcp.reconnect / mcp.toggle 也写它）。probe 用一条独立的
// 一次性连接（不占 serverName、不注册工具、不碰活实例），restart 走官方
// "清理未确认就禁止替换"的门，watch 直接订 registry 的唤醒流。

// mcpProbeClientName 是官方 probe 的 clientInfo.name（probe.ts:66）。
// 它只是 server 侧日志，不是产品版本。
const (
	mcpProbeClientName    = "dsh-mcp-probe"
	mcpProbeClientVersion = "0.0.1"
)

// legacyStatusName 是 mcp.list 的旧字符串口径（**不变**：界面还在读它）。
//
// 官方状态 richer，但既有 wire 已经开始被人消费；把新词汇直接塞进去会让
// 老客户端 switch 到未知值。新的官方形状只从 mcpManager.watch/list 出去。
func legacyStatusName(status mcp.Status) string {
	switch status.State {
	case mcp.StateConnected:
		return "connected"
	case mcp.StateDisabled:
		return "disabled"
	case mcp.StateConnecting, mcp.StateReconnecting:
		return "connecting"
	default:
		// closed / failed / waiting-credentials 对旧口径都是"现在不可用"。
		return "error"
	}
}

// Views 返回官方 `McpManagedServerView[]`（watch 的元素、list 的行）。
func (m *MCPManager) Views() []mcp.View { return m.reg.List() }

// List 是 mcpManager.list 的等价入口（本任务只要求三个方法，list 一并给出，
// 保证管理面能先拿一次性快照再决定要不要开流）。
func (m *MCPManager) List() ([]mcp.View, error) {
	m.mu.Lock()
	loadErr := m.loadErr
	m.mu.Unlock()
	if loadErr != nil {
		return nil, loadErr
	}
	return m.reg.List(), nil
}

// Subscribe 开一条**真实**状态流：首帧是当前列表，之后每次状态/条目变化
// 再给一份完整列表（Registry 唤醒，不是定时轮询出来的假流）。
func (m *MCPManager) Subscribe(ctx context.Context) (<-chan []mcp.View, error) {
	m.mu.Lock()
	loadErr := m.loadErr
	m.mu.Unlock()
	if loadErr != nil {
		return nil, loadErr
	}
	if m.closed.Load() {
		return nil, mcp.ErrDisposed
	}
	return m.reg.Subscribe(ctx), nil
}

// Probe 是官方 mcpManager.probe：探一个 server 的管理面能力，**不动**活实例。
//
// 门与官方 manager.probe 一致：已释放、未知 server、清理未完成都直接报错；
// 本宿主另有两条：transport 不是 stdio（结构化未实现）、配置读取失败。
func (m *MCPManager) Probe(ctx context.Context, name string) (mcp.ProbeResult, error) {
	server, err := m.requireServer(name)
	if err != nil {
		return mcp.ProbeResult{}, err
	}
	if transport := server.config.transportName(); transport != "stdio" {
		return mcp.ProbeResult{}, mcp.UnsupportedTransport(name, transport)
	}
	if _, hasFailure := server.retainedFailure(); hasFailure {
		return mcp.ProbeResult{}, mcp.CleanupIncomplete(name)
	}
	target := mcp.ClientProbeTarget{
		Starter:       stdioProbeStarter{config: server.config},
		ClientName:    mcpProbeClientName,
		ClientVersion: mcpProbeClientVersion,
	}
	return mcp.ProbeServer(ctx, target, mcp.ProbeOptions{
		// 官方 mcpManager.probe 不传选项：整场用 10s、翻页 16、工具名 512，
		// 清理预算取这个条目自己的 closeTimeoutMs。
		CloseTimeoutMs: int(server.config.closeBudget() / time.Millisecond),
	})
}

// Restart 是官方 mcpManager.restart：dispose 活着的实例，再挂一个全新的，
// 带上全新的重连预算。清理未确认时**拒绝替换**（ErrCleanupIncomplete）。
func (m *MCPManager) Restart(ctx context.Context, name string) error {
	if _, err := m.requireServer(name); err != nil {
		return err
	}
	// 会替换活实例：与 reconnect/toggle 串行（官方那条 settle 的 tail）。
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	return mcp.Restart(ctx, mcpRestController{manager: m}, name)
}

// requireServer 取 server 并做三条门（loadErr / 已释放 / 未知）。
func (m *MCPManager) requireServer(name string) (*mcpServer, error) {
	server := m.server(name)
	m.mu.Lock()
	loadErr := m.loadErr
	m.mu.Unlock()
	if loadErr != nil {
		return nil, loadErr
	}
	if m.closed.Load() {
		return nil, mcp.ErrDisposed
	}
	if server == nil {
		return nil, mcp.UnknownServer(name)
	}
	return server, nil
}

func (m *MCPManager) server(name string) *mcpServer {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.servers[name]
}

// stdioProbeStarter 把探针要的那条独立连接接到本仓既有的 stdio 客户端上：
// 进程控制（Windows Job Object、有界回收）只有这一份实现。
type stdioProbeStarter struct{ config mcpServerConfig }

func (s stdioProbeStarter) Start(context.Context) (mcp.Conn, error) {
	client, err := startMCPClient(s.config)
	if err != nil {
		return nil, err
	}
	return &stdioProbeConn{client: client, budget: s.config.closeBudget()}, nil
}

type stdioProbeConn struct {
	client *mcpClient
	budget time.Duration
}

func (c *stdioProbeConn) Call(ctx context.Context, method string, params any, out any) error {
	return c.client.call(ctx, method, params, out)
}

func (c *stdioProbeConn) Notify(method string, params any) error {
	return c.client.notify(method, params)
}

func (c *stdioProbeConn) Close(ctx context.Context) mcp.CloseResult {
	return c.client.CloseWithBudget(c.budget, ctx)
}

// mcpRestController 是 mcp.Restart 的动作面：决策在 internal/mcp，动作在这里。
type mcpRestController struct{ manager *MCPManager }

func (c mcpRestController) Disposed() bool { return c.manager.closed.Load() }

func (c mcpRestController) Exists(name string) bool { return c.manager.server(name) != nil }

func (c mcpRestController) Enabled(name string) bool {
	server := c.manager.server(name)
	return server != nil && server.config.enabled()
}

func (c mcpRestController) Stop(name string) (mcp.CloseResult, bool) {
	server := c.manager.server(name)
	if server == nil {
		return mcp.CloseResult{CloseConfirmed: true}, true
	}
	closed := server.stop()
	retained, hasFailure := server.retainCleanupFailure(closed)
	if hasFailure {
		return retained, false
	}
	return closed, true
}

func (c mcpRestController) Mount(name string) error {
	server := c.manager.server(name)
	if server == nil {
		return mcp.UnknownServer(name)
	}
	if err := c.manager.connect(server); err != nil {
		// 官方 mount 失败的口径：failed/load + 原文。
		server.publishLoadFailure(err)
		return err
	}
	return nil
}

func (c mcpRestController) Publish(name string, status mcp.Status) error {
	server := c.manager.server(name)
	if server == nil {
		return mcp.UnknownServer(name)
	}
	server.publish(status)
	return nil
}

// MCPModule 把 MCP 工具接进内核。
//
// Requires("tools")：装配顺序由能力依赖决定，不靠模块列表的行顺序。
func MCPModule(home string) kernel.Module {
	return kernel.Module{
		ID:       "mcp-client",
		Backend:  kernel.BackendStatic,
		Requires: []string{"tools"},
		Register: func(s *kernel.Scope) error {
			reg, err := kernel.Inject(s, spi.ToolsKey)
			if err != nil {
				return err
			}
			manager := OpenMCPManager(home)
			// 逆序撤销：scope 释放时先收子进程，再取消 context。
			s.Effect(manager.Close)
			if err := manager.StartAll(); err != nil {
				return err
			}
			return manager.RegisterInto(reg)
		},
	}
}
