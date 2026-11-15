package main

// 官方 4 条设置页的命名空间 → **真实消费方**的唯一解析点。
//
// 设置命名空间由 `internal/settingsform` 声明与校验（schema / sections / base 同源），
// 但"写进去的值到底有没有人读"是另一件事：只发布 schema 不接线，界面保存成功而
// 行为不变就是静默成功。本文件把这些字段接到宿主里**真实生效**的那几个旋钮上。
//
// # 三条判据
//
//  1. **只认用户显式写过的值**（`sections[].fields[].overridden == true`）。
//     base 里的默认值不参与覆盖：它只是"描述"用的官方默认，而宿主的真实默认
//     在各消费方自己身上（agent 默认 10、gate 默认 8、pwsh 默认 120s/64KB）。
//     否则"设置默认值"会把用户已有的环境变量配置整体盖掉——那是另一种静默。
//  2. **优先级：设置 > 环境变量 > 消费方默认**。环境变量是本仓既有部署面，
//     不能被一次界面保存意外清空；界面写了值就压过环境。
//  3. **读不到就不假装读到**：home 为空、文档损坏、命名空间缺失都返回 ok=false，
//     调用方继续走原生路径（env / 常量），不抛错也不写假值。
//
// # 生效时机
//
// 这些解析点在**进程装配期**跑一次：`dshd run` 每一轮是一个新进程，所以下一轮
// 就生效；`dshd serve` 是长驻进程，装配只发生一次——serve 下改设置要重启进程
// （TUI/GUI 走的是 run 子进程路径，所以界面保存后下一轮就是新值）。
//
// # 与官方 applies 的差别（如实登记）
//
// 官方这些命名空间的 `applies` 是 `live`（下一次请求即生效）。本仓是"下一次
// 装配生效"，两者在 run 路径上等价，在 serve 的常驻进程上不等价。这是缺口，
// 不在这里假装成 live。

import (
	"os"
	"strconv"
	"strings"
	"time"

	"dshgo/internal/settings"
	"dshgo/internal/settingsform"
	"dshgo/modules/tool/pwsh"
)

// settingsNamespaceStore 把 `internal/settings` 的持久层适配成 `settingsform.Store`。
//
// 两个 store 本来就指向同一个文件（`<home>/state/settings-namespaces.json`），
// 所以这里只是形状转换：settings 的 `Read` 返回 (value, revision)，
// settingsform 要 (document{User, Revision})——命名空间文档里的 `value`
// **就是** user 段（base 由声明提供，服务层会用声明的 base 补齐）。
type settingsNamespaceStore struct {
	store *settings.Store
}

// Read 返回该命名空间的 user 段与 revision。
func (s settingsNamespaceStore) Read(namespace string) (settingsform.Document, error) {
	value, revision, err := s.store.Read(namespace)
	if err != nil {
		return settingsform.Document{}, err
	}
	return settingsform.Document{User: value, Revision: revision}, nil
}

// Write 与若干写路径共用同一份持久层语义；这里只为满足接口（本文件的解析点全在
// 读路径，任何写都必须经 settings.describe/mutate 那条校验过的通道）。
func (s settingsNamespaceStore) Write(namespace string, user map[string]any, expected *int64) (int64, error) {
	return s.store.Write(namespace, user, expected)
}

// settingsSection 读一个命名空间的机器可读投影（base ∪ user，与 settings.describe
// 同一条合成路径：`settingsform.Service.Get`）。
//
// home 为空（自举/测试）或读失败时返回 ok=false：调用方据此走原生路径。
// 文档损坏在 `settings.describe` 那里是 fail-loud 的；这里只是"取一个可选的覆盖
// 值"，拿不到就用宿主默认，不让一次设置面故障拦住建会话。
func settingsSection(home, ns string) (settingsform.Section, bool) {
	home = strings.TrimSpace(home)
	if home == "" {
		return settingsform.Section{}, false
	}
	store := settingsNamespaceStore{store: settings.Open(home)}
	section, err := settingsform.NewService(store, settingsform.HostRegistry()).Get(ns)
	if err != nil {
		return settingsform.Section{}, false
	}
	return section, true
}

// overriddenField 返回用户**显式写过**的那个字段（union 里的 base 默认不算）。
func overriddenField(section settingsform.Section, name string) (any, bool) {
	for _, field := range section.Fields {
		if field.Name != name {
			continue
		}
		if !field.Overridden || field.Current == nil {
			return nil, false
		}
		return field.Current, true
	}
	return nil, false
}

// settingsInt 读一个整数覆盖值（JSON 解码出来可能是 float64 / int / json.Number）。
func settingsInt(section settingsform.Section, name string) (int, bool) {
	raw, ok := overriddenField(section, name)
	if !ok {
		return 0, false
	}
	switch typed := raw.(type) {
	case float64:
		if typed != float64(int(typed)) {
			return 0, false
		}
		return int(typed), true
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

// settingsString 读一个字符串覆盖值（全空白视为"没写"）。
//
// 为什么空白算"没写"：这些字段的值会作为环境变量交给 modules/search 的 provider，
// 一个只含空格的 provider id / 端点不可能被解析成功，那等于把部署配置改成
// "配了一个不存在的后端"。保持"没写"= 回落环境变量/官方默认更接近意图。
func settingsString(section settingsform.Section, name string) (string, bool) {
	raw, ok := overriddenField(section, name)
	if !ok {
		return "", false
	}
	text, isString := raw.(string)
	if !isString {
		return "", false
	}
	trimmed := strings.TrimSpace(text)
	if len(trimmed) == 0 {
		return "", false
	}
	return trimmed, true
}

// settingsBool 读一个布尔覆盖值。
func settingsBool(section settingsform.Section, name string) (bool, bool) {
	raw, ok := overriddenField(section, name)
	if !ok {
		return false, false
	}
	value, ok := raw.(bool)
	return value, ok
}

// effectiveAgentLoopMaxParallel 解析本轮的并发工具调用上限。
//
// 消费方：`agent.Loop.MaxParallelToolCalls`（internal/agent/parallel.go:99-105：
// 字段优先，否则 env `DSHD_MAX_PARALLEL_TOOL_CALLS`，再否则官方默认 10）。
// 返回 0 = 设置里没写过，调用方保持 nil 字段走 env/默认。
func effectiveAgentLoopMaxParallel(home string) int {
	section, ok := settingsSection(home, settingsform.NamespaceAgentLoop)
	if !ok {
		return 0
	}
	value, ok := settingsInt(section, settingsform.FieldMaxParallelToolCalls)
	if !ok || value < settingsform.MinMaxParallelToolCalls {
		return 0
	}
	return value
}

// effectiveSubagentMaxActive 解析子代理并发上限。
//
// 消费方：cmd/dshd/subagent_gate.go 的 `subagentGate`（官方默认 8）。
// 返回 (0,false) = 设置里没写过，调用方继续走 env `DSHD_SUBAGENT_MAX_ACTIVE_SUBAGENTS`。
func effectiveSubagentMaxActive(home string) (int, bool) {
	section, ok := settingsSection(home, settingsform.NamespaceSubagent)
	if !ok {
		return 0, false
	}
	value, ok := settingsInt(section, settingsform.FieldMaxActiveSubagents)
	if !ok || value < settingsform.MinMaxActiveSubagents {
		return 0, false
	}
	return value, true
}

// effectiveSubagentMaxDepth 解析嵌套委派深度上限。
//
// 消费方：cmd/dshd/subagent.go 的 `subRunner.depthLimit`（原为硬编码常量 1，
// 正好是官方默认值）。返回 nil = 设置里没写过，调用方保持 nil 走官方默认；
// 返回非 nil 指针是因为官方 `maxDepth` **允许 0**（连主代理都不许再委派），
// 0 不能当"没设置"的哨兵。
func effectiveSubagentMaxDepth(home string) *int {
	section, ok := settingsSection(home, settingsform.NamespaceSubagent)
	if !ok {
		return nil
	}
	value, ok := settingsInt(section, settingsform.FieldMaxDepth)
	if !ok || value < settingsform.MinMaxDepth {
		return nil
	}
	return &value
}

// shellBudgets 是一份 shell 执行器的生效预算。
type shellBudgets struct {
	// Timeout 是前台命令超时；0 = 由执行器自己的默认值决定。
	Timeout time.Duration
	// MaxOutputBytes 是每路输出上限；0 = 由执行器自己的默认值决定。
	MaxOutputBytes int
}

// effectiveShellBudgets 解析 bash-sandbox / pwsh-sandbox 的生效预算。
//
// 只返回**用户写过**的字段（没写就是 0，"不覆盖"）。字段存在性按命名空间分开：
// 两个执行器是两条独立命名空间，官方页面也是"绑当前平台那一个"。
func effectiveShellBudgets(home, ns string) shellBudgets {
	section, ok := settingsSection(home, ns)
	if !ok {
		return shellBudgets{}
	}
	budgets := shellBudgets{}
	if ms, ok := settingsInt(section, settingsform.FieldShellTimeoutMs); ok && ms > 0 {
		budgets.Timeout = time.Duration(ms) * time.Millisecond
	}
	if bytes, ok := settingsInt(section, settingsform.FieldShellMaxOutputBytes); ok && bytes > 0 {
		budgets.MaxOutputBytes = bytes
	}
	return budgets
}

// pwshBudgetOptions 把 `pwsh-sandbox` 的预算翻成 pwsh 工具的 Option。
//
// 只翻**用户显式写过**的字段：没写过就一个 Option 都不传，工具保持自己的官方
// 默认值（DefaultTimeout=120s / DefaultMaxOutputBytes=64000）。传 0 也没用——
// 工具侧的 Option 对 <=0 一律忽略，那是有意的双保险（装配点写错不该把预算
// 变成 0 毫秒/0 字节）。
//
// 与官方的一处已知差异：官方 `maxTimeoutMs` 默认 600_000，工具侧把注入值夹到
// 600s（`WithTimeoutBudget`），所以设置页写 900000 时实际生效 600000。
func pwshBudgetOptions(home string) []pwsh.Option {
	budgets := effectiveShellBudgets(home, settingsform.NamespacePwshSandbox)
	options := make([]pwsh.Option, 0, 2)
	if budgets.Timeout > 0 {
		options = append(options, pwsh.WithTimeoutBudget(budgets.Timeout))
	}
	if budgets.MaxOutputBytes > 0 {
		options = append(options, pwsh.WithMaxOutputBytes(budgets.MaxOutputBytes))
	}
	return options
}

// settingsEnvBinding 是"设置字段 → 环境变量"的一条映射。
//
// 为什么走环境变量这一层：`modules/search` 的每个 provider 已经**只**从同一个
// getenv 接缝读配置（modules/search/search.go:83-105、
// modules/search/webapi_config.go:132-160、modules/search/deepseek.go:64-84）。
// 在宿主这一层把"设置优先的 getenv"传进去，消费方一行都不用改——不是给每个
// 字段在模块里再开一条私有通道。
type settingsEnvBinding struct {
	// env 是消费方读的环境变量名（逐字，与 modules/search 的常量一致）。
	env string
	// ns / field 是设置文档里的落点。
	ns    string
	field string
	// kind 是取值形状：int / bool / string。
	kind string
}

// settingsEnvBindings 是 4 条设置页里**有真实消费方**的字段映射表。
//
// 没进这张表的字段（subagent-model-selection-settings 的两个、
// web-search-deepseek.apiKey/apiKeyEnv/baseURL 之外的部分）在各自 schema 文件的
// 文件头写清了为什么没有消费方。
var settingsEnvBindings = []settingsEnvBinding{
	// web：两个 provider 选择（modules/search/search.go:92-101 的 pin）。
	{env: "DSHD_WEB_SEARCH_PROVIDER", ns: settingsform.NamespaceWeb, field: settingsform.FieldSearchProvider, kind: "string"},
	{env: "DSHD_WEB_FETCH_PROVIDER", ns: settingsform.NamespaceWeb, field: settingsform.FieldFetchProvider, kind: "string"},
	// web-deepseek-api：九个字段（modules/search/webapi_config.go:132-160）。
	{env: "DSHD_WEB_SEARCH_COUNT", ns: settingsform.NamespaceWebDeepSeekAPI, field: settingsform.FieldWebCount, kind: "int"},
	{env: "DSHD_WEB_SEARCH_REQUESTS_PER_SECOND", ns: settingsform.NamespaceWebDeepSeekAPI, field: settingsform.FieldWebRequestsPerSecond, kind: "int"},
	{env: "DSHD_WEB_SEND_ORIGINAL_QUESTION", ns: settingsform.NamespaceWebDeepSeekAPI, field: settingsform.FieldWebSendOriginalQuestion, kind: "bool"},
	{env: "DSHD_WEB_SEARCH_BILLING", ns: settingsform.NamespaceWebDeepSeekAPI, field: settingsform.FieldWebSearchBilling, kind: "string"},
	{env: "DSHD_WEB_FETCH_BILLING", ns: settingsform.NamespaceWebDeepSeekAPI, field: settingsform.FieldWebFetchBilling, kind: "string"},
	{env: "DSHD_WEB_SEARCH_API_BASE_URL", ns: settingsform.NamespaceWebDeepSeekAPI, field: settingsform.FieldWebSearchBaseURL, kind: "string"},
	{env: "DSHD_WEB_SEARCH_API_KEY_ENV", ns: settingsform.NamespaceWebDeepSeekAPI, field: settingsform.FieldWebSearchAPIKeyEnv, kind: "string"},
	{env: "DSHD_WEB_FETCH_API_BASE_URL", ns: settingsform.NamespaceWebDeepSeekAPI, field: settingsform.FieldWebFetchBaseURL, kind: "string"},
	{env: "DSHD_WEB_FETCH_API_KEY_ENV", ns: settingsform.NamespaceWebDeepSeekAPI, field: settingsform.FieldWebFetchAPIKeyEnv, kind: "string"},
	// web-search-deepseek：旧 Messages 搜索的端点 / 模型 / 请求参数
	// （modules/search/deepseek.go:64-84）。
	{env: "DEEPSEEK_SEARCH_BASE_URL", ns: settingsform.NamespaceWebSearchDeepSeek, field: settingsform.FieldWebLegacyBaseURL, kind: "string"},
	{env: "DEEPSEEK_SEARCH_MODEL", ns: settingsform.NamespaceWebSearchDeepSeek, field: settingsform.FieldWebLegacyModel, kind: "string"},
	{env: "DSHD_SEARCH_MAX_USES", ns: settingsform.NamespaceWebSearchDeepSeek, field: settingsform.FieldWebLegacyMaxUses, kind: "int"},
	{env: "DSHD_SEARCH_MAX_TOKENS", ns: settingsform.NamespaceWebSearchDeepSeek, field: settingsform.FieldWebLegacyMaxTokens, kind: "int"},
	{env: "DSHD_SEARCH_API_VERSION", ns: settingsform.NamespaceWebSearchDeepSeek, field: settingsform.FieldWebLegacyAPIVersion, kind: "string"},
	// agent-loop / subagent：这两个消费方走的是**类型化**解析（见上面三个
	// effective* 函数），不经过 getenv；列在这里只是让"哪条环境变量被设置覆盖"
	// 这件事能被一个测试统一断言。
	{env: "DSHD_MAX_PARALLEL_TOOL_CALLS", ns: settingsform.NamespaceAgentLoop, field: settingsform.FieldMaxParallelToolCalls, kind: "int"},
	{env: "DSHD_SUBAGENT_MAX_ACTIVE_SUBAGENTS", ns: settingsform.NamespaceSubagent, field: settingsform.FieldMaxActiveSubagents, kind: "int"},
	{env: "DSHD_SUBAGENT_MAX_DEPTH", ns: settingsform.NamespaceSubagent, field: settingsform.FieldMaxDepth, kind: "int"},
}

// settingsEnvOverlay 造一个"设置文档优先"的 getenv。
//
// 语义：**用户显式写过的字段**优先，其余一律回落 base(name)（通常是 os.Getenv）。
// 一次预读把涉及的命名空间各读一遍（每个 ns 一次磁盘读），返回的闭包只查内存。
func settingsEnvOverlay(home string, base func(string) string) func(string) string {
	if base == nil {
		base = os.Getenv
	}
	sections := make(map[string]settingsform.Section, 8)
	for _, binding := range settingsEnvBindings {
		if _, ok := sections[binding.ns]; ok {
			continue
		}
		if section, ok := settingsSection(home, binding.ns); ok {
			sections[binding.ns] = section
		}
	}
	return func(name string) string {
		for _, binding := range settingsEnvBindings {
			if binding.env != name {
				continue
			}
			section, ok := sections[binding.ns]
			if !ok {
				break
			}
			if value, ok := binding.overlayValue(section); ok {
				return value
			}
			break
		}
		return base(name)
	}
}

// overlayValue 把一个设置字段渲染成环境变量字符串（= 消费方期望的写法）。
func (b settingsEnvBinding) overlayValue(section settingsform.Section) (string, bool) {
	switch b.kind {
	case "int":
		value, ok := settingsInt(section, b.field)
		if !ok {
			return "", false
		}
		return strconv.Itoa(value), true
	case "bool":
		value, ok := settingsBool(section, b.field)
		if !ok {
			return "", false
		}
		if value {
			return "true", true
		}
		return "false", true
	default:
		return settingsString(section, b.field)
	}
}
