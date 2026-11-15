package team

// 本文件是官方 DSH `0.2.2-rc.2` 在**团队/子代理命名与配置面**上的模型移植。
//
// # 为什么需要它（rc.1 → rc.2 的三处硬变化）
//
//  1. **AgentTeam 是重写，不是搬家**：rc.1 的 `dsh-experimental-agent-team` 是名册制
//     （`maxMembers=16` + `TEAM_MEMBER_LIMIT` + disposal 超时）；rc.2 的
//     `dsh-agent-team` 只剩任务板，并在加载期**显式拒绝**旧配置键
//     `maxMembers` / `maxMessageBytes` / `disposalTimeoutMs`
//     （`dsh-agent-team/src/index.ts:34-35,62-69`，错误码 `TEAM_INVALID_CONFIG`）。
//     配置只剩 `maxTasks`（默认 256，`src/index.ts:32,49-52`）与
//     `defaultEnabled`（默认 false）。
//  2. **`tool-agent-team` 也不接受配置**：`Config = Record<never, never>`，并拒绝
//     `interrupt` / `freshProvider` / `forkProvider`（`dsh-tool-agent-team/src/index.ts:23-24,95-99`）——
//     "队友就是子代理，用子代理的创建与发货"。
//  3. **子代理中断默认关**：`tool-subagent-control` 的插件默认是 `interrupt: true`，
//     但内置配置（base bundle 与 Web 的 standard/cordis/ptc 三 preset）都写
//     `interrupt: false`（`dsh-base/cordis.patch.yml:386-389`；文档
//     `docs/upgrade-guide/v0.2.2-rc.1/subagent-interrupt-default/guide.md:7-9`）。
//     所以**宿主内置默认必须是 false**（模型默认拿不到 `interrupt_agent`），
//     同时保留 profile 覆盖回来的路。
//
// # 命名模型（`dsh-subagent/src/names.ts`）
//
//	根名固定 `lead`（:13）；子名 `^[a-z][a-z0-9-]{0,31}$` 且不得为 `lead`（:16,23-25）；
//	成员全名 = 从根起的 `/` 路径（`reviewer/checker`，:27-36）；历史未命名子代理
//	保留 Session id 作名字（:38-47）。
//
// 本文件只做**纯函数**（命名、配置解析），不碰磁盘、不建状态：
// 这样"官方默认值"可以被单测逐条钉住，而不是散落在注册逻辑里靠人肉核对。

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ROOTAgentName 是根代理的固定名字；任何子代理都不得占这个名字。
// 官方：`dsh-subagent/src/names.ts:13`。
const ROOTAgentName = "lead"

// agentNamePattern 是官方子代理名正则：1–32 个字符、小写字母开头、
// 只含小写字母/数字/连字符（36 字符的 UUID 因此永远匹配不上）。
// 官方：`dsh-subagent/src/names.ts:16`。
const agentNamePattern = `^[a-z][a-z0-9-]{0,31}$`

var agentNameRE = regexp.MustCompile(agentNamePattern)

// IsAgentName 判断一个字符串是不是合法的**子代理名**（不是根名）。
// 官方：`dsh-subagent/src/names.ts:23-25`。
func IsAgentName(value string) bool {
	return agentNameRE.MatchString(value) && value != ROOTAgentName
}

// AgentNameError 把"名字为什么不合法"说清（模型/人照着改得动）。
// 对应官方 `INVALID_AGENT_NAME` / `DUPLICATE_AGENT_NAME` 两种错误。
func AgentNameError(value string) string {
	if value == ROOTAgentName {
		return fmt.Sprintf("agent name %q is reserved for the root agent; choose another name", value)
	}
	return fmt.Sprintf(
		"agent name %q is invalid; use 1-32 lower-case letters, digits, or hyphens starting with a letter, other than %q",
		value, ROOTAgentName)
}

// JoinAgentPath 把子名接到创建者路径后面。根的路径是空串；
// 任何一环没名字就返回 ok=false（历史记录里名字可能缺）。
// 官方：`dsh-subagent/src/names.ts:33-36`。
func JoinAgentPath(parentPath string, name string, nameKnown bool) (path string, ok bool) {
	if !nameKnown {
		return "", false
	}
	if parentPath == "" {
		return name, true
	}
	return parentPath + "/" + name, true
}

// AgentAddress 选一个树成员的**模型可见名字**：根是 `lead`，有完整路径用路径，
// 否则退回 Session id（历史未命名子代理）。
// 官方：`dsh-subagent/src/names.ts:44-47`。
func AgentAddress(path string, pathKnown bool, sessionID string) string {
	if pathKnown && path == "" {
		return ROOTAgentName
	}
	if pathKnown {
		return path
	}
	return sessionID
}

// ParentAgentPath 去掉路径最后一段，得到创建者路径（创建者是根时返回空串）。
// 官方：`dsh-subagent/src/names.ts:54-57`。
func ParentAgentPath(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[:i]
	}
	return ""
}

// DefaultAgentName 是"没指定名字时"的自动命名（官方 `agent-N`，从 1 开始找空位）。
// 官方：`dsh-subagent/src/names.ts:82-86`。
func DefaultAgentName(taken func(string) bool) string {
	for ordinal := 1; ; ordinal++ {
		candidate := fmt.Sprintf("agent-%d", ordinal)
		if taken == nil || !taken(candidate) {
			return candidate
		}
	}
}

// ---------------------------------------------------------------------------
// 配置模型
// ---------------------------------------------------------------------------

// TeamConfigError 是配置期失败（官方 `TEAM_INVALID_CONFIG`）。
//
// 为什么单独一个类型：配置错误必须在**装配期**炸，而不是等第 257 条任务
// 或第 17 个队友时才报一个看不出源头的错（官方 `positiveLimit` 同此口径）。
type TeamConfigError struct {
	// Code 是稳定错误码（官方 `TEAM_INVALID_CONFIG` / `TEAM_INVALID_ARGUMENT`）。
	Code string
	Msg  string
}

func (e *TeamConfigError) Error() string { return e.Code + ": " + e.Msg }

// 官方错误码（`dsh-agent-team/src/index.ts`，共 16 个里与本文件相关的几个）。
const (
	TeamCodeInvalidConfig     = "TEAM_INVALID_CONFIG"
	TeamCodeInvalidArgument   = "TEAM_INVALID_ARGUMENT"
	TeamCodeInvalidWriteScope = "TEAM_INVALID_WRITE_SCOPE"
)

// DefaultMaxTasks 是官方 `agentTeam.maxTasks` 的默认值。
// 官方：`dsh-agent-team/src/index.ts:32`（`const DEFAULT_MAX_TASKS = 256`）、
// `src/index.ts:49-52`（`maxTasks: z.number().step(1).min(1).default(DEFAULT_MAX_TASKS)`）。
const DefaultMaxTasks = 256

// DefaultTeamEnabled 是官方 `agentTeam.defaultEnabled` 的默认值（false = 新根默认不开 Team）。
// 官方：`src/index.ts:51`（`defaultEnabled: z.boolean().default(false)`）。
const DefaultTeamEnabled = false

// teamRemovedConfigKeys 是 rc.2 **显式拒绝**的旧配置键。
//
// 官方：`dsh-agent-team/src/index.ts:34-35`（`REMOVED_CONFIG_KEYS`），
// `:62-69`（`Object.hasOwn(config, key)` → 抛 `TEAM_INVALID_CONFIG`，
// 原文："teammates are subagents, so subagent limits and inbox delivery apply"）。
var teamRemovedConfigKeys = []string{"maxMembers", "maxMessageBytes", "disposalTimeoutMs"}

// TeamConfig 是官方 `@deepseek-ai/dsh-agent-team` 的配置（rc.2 只有这两项）。
// 官方：`src/types.ts:135-141`、`docs/config-catalog.md:175-192`。
type TeamConfig struct {
	// MaxTasks 是**一个 Team 保留的未删除任务上限**。
	MaxTasks int
	// DefaultEnabled 让没有保存过选择的新根在第一步之前就开 Team（恢复的会话保持原选择）。
	DefaultEnabled bool
}

// DefaultTeamConfig 是官方默认配置（maxTasks=256、defaultEnabled=false）。
func DefaultTeamConfig() TeamConfig {
	return TeamConfig{MaxTasks: DefaultMaxTasks, DefaultEnabled: DefaultTeamEnabled}
}

// ParseTeamConfig 解析官方 `agent-team` 的配置对象。
//
// 规矩与官方逐条对齐：
//   - 出现任何 `REMOVED_CONFIG_KEYS` → `TEAM_INVALID_CONFIG`（**不是忽略**：
//     静默忽略会让写了一辈子 rc.1 配置的人以为上限还在生效）；
//   - `maxTasks` 必须是正整数（官方 `positiveLimit`：`Number.isSafeInteger` 且 ≥1）；
//   - 未知键 → `TEAM_INVALID_CONFIG`（官方 schemastery 默认拒绝未知键）；
//   - 省略 = 官方默认。
func ParseTeamConfig(raw map[string]any) (TeamConfig, error) {
	cfg := DefaultTeamConfig()
	for key := range raw {
		for _, removed := range teamRemovedConfigKeys {
			if key == removed {
				return cfg, &TeamConfigError{
					Code: TeamCodeInvalidConfig,
					Msg: fmt.Sprintf(
						"agent-team: %s is no longer supported; teammates are subagents, so subagent limits and inbox delivery apply",
						key),
				}
			}
		}
	}
	for key, value := range raw {
		switch key {
		case "maxTasks":
			n, err := positiveSafeInt("maxTasks", value)
			if err != nil {
				return cfg, err
			}
			cfg.MaxTasks = n
		case "defaultEnabled":
			b, ok := value.(bool)
			if !ok {
				return cfg, &TeamConfigError{
					Code: TeamCodeInvalidConfig,
					Msg:  "defaultEnabled must be a boolean",
				}
			}
			cfg.DefaultEnabled = b
		default:
			return cfg, &TeamConfigError{
				Code: TeamCodeInvalidConfig,
				Msg:  fmt.Sprintf("agent-team: unknown configuration key %q", key),
			}
		}
	}
	return cfg, nil
}

// ParseToolAgentTeamConfig 校验官方 `@deepseek-ai/dsh-tool-agent-team` 的配置。
//
// rc.2 该插件**没有配置**（`Config = Record<never, never>`），并且拒绝三个旧键
// （`interrupt` / `freshProvider` / `forkProvider`）——队友的创建与投递归子代理。
// 官方：`dsh-tool-agent-team/src/index.ts:18-24,95-99`。
func ParseToolAgentTeamConfig(raw map[string]any) error {
	for key := range raw {
		switch key {
		case "interrupt", "freshProvider", "forkProvider":
			return &TeamConfigError{
				Code: TeamCodeInvalidConfig,
				Msg:  fmt.Sprintf("tool-agent-team: %s is no longer supported; create teammates with a subagent tool", key),
			}
		default:
			return &TeamConfigError{
				Code: TeamCodeInvalidConfig,
				Msg:  fmt.Sprintf("tool-agent-team: unknown configuration key %q", key),
			}
		}
	}
	return nil
}

// positiveSafeInt 是官方 `positiveLimit` 的 Go 版：正整数且不超过 i64 安全整数。
func positiveSafeInt(name string, value any) (int, error) {
	toInt := func(f float64) (int, bool) {
		n := int(f)
		if float64(n) != f {
			return 0, false
		}
		return n, true
	}
	var n int
	switch v := value.(type) {
	case int:
		n = v
	case int64:
		if v > int64(^uint(0)>>1) {
			return 0, &TeamConfigError{Code: TeamCodeInvalidConfig, Msg: name + " must be a positive safe integer"}
		}
		n = int(v)
	case float64:
		converted, ok := toInt(v)
		if !ok {
			return 0, &TeamConfigError{Code: TeamCodeInvalidConfig, Msg: name + " must be a positive safe integer"}
		}
		n = converted
	default:
		return 0, &TeamConfigError{Code: TeamCodeInvalidConfig, Msg: name + " must be a positive safe integer"}
	}
	if n < 1 {
		return 0, &TeamConfigError{Code: TeamCodeInvalidConfig, Msg: name + " must be a positive safe integer"}
	}
	return n, nil
}

// 环境变量覆盖项（宿主是 Go 进程，没有 JS 侧那套 loader 配置对象）。
const (
	// EnvAgentTeamMaxTasks 覆盖 `agent-team.maxTasks`（官方默认 256）。
	EnvAgentTeamMaxTasks = "DSHD_TEAM_MAX_TASKS"
	// EnvAgentTeamDefaultEnabled 覆盖 `agent-team.defaultEnabled`（官方默认 false）。
	EnvAgentTeamDefaultEnabled = "DSHD_AGENT_TEAM_DEFAULT_ENABLED"
)

// TeamConfigFromEnv 从环境变量解出 `agent-team` 配置；空 = 官方默认，非法值 fail-loud。
//
// 为什么不等价于"读 profile 配置行"：本仓 Go 宿主的模块装配签名由
// `cmd/dshd/main.go` 固定，profile 的配置行还没有解析器；环境变量是同一份语义的
// 可测替代（与 `modules/tool/skill` 的 DSHD_* 做法一致）。profile 解析接上之后，
// 只需把它解出的 map 交给 ParseTeamConfig，默认值仍以这里为准。
func TeamConfigFromEnv(getenv func(string) string) (TeamConfig, error) {
	cfg := DefaultTeamConfig()
	if getenv == nil {
		return cfg, nil
	}
	if raw := strings.TrimSpace(getenv(EnvAgentTeamMaxTasks)); raw != "" {
		n, convErr := strconv.Atoi(raw)
		if convErr != nil {
			return cfg, fmt.Errorf("%s=%q 不是正整数（官方 maxTasks 要求正整数，默认 %d）",
				EnvAgentTeamMaxTasks, raw, DefaultMaxTasks)
		}
		parsed, err := ParseTeamConfig(map[string]any{"maxTasks": n})
		if err != nil {
			return cfg, fmt.Errorf("%s=%q 不是正整数（官方 maxTasks 要求正整数，默认 %d）",
				EnvAgentTeamMaxTasks, raw, DefaultMaxTasks)
		}
		cfg.MaxTasks = parsed.MaxTasks
	}
	if raw := strings.TrimSpace(getenv(EnvAgentTeamDefaultEnabled)); raw != "" {
		switch strings.ToLower(raw) {
		case "1", "true", "yes", "on":
			cfg.DefaultEnabled = true
		case "0", "false", "no", "off":
			cfg.DefaultEnabled = false
		default:
			return cfg, fmt.Errorf("%s=%q 不是布尔值（官方 defaultEnabled 只接受 true/false，默认 %v）",
				EnvAgentTeamDefaultEnabled, raw, DefaultTeamEnabled)
		}
	}
	return cfg, nil
}

// ---------------------------------------------------------------------------
// 子代理控制工具的 interrupt 开关
// ---------------------------------------------------------------------------

// SubagentControlConfig 是官方 `@deepseek-ai/dsh-tool-subagent-control` 的配置。
// 官方：`src/index.ts:23-32`（`interrupt?: boolean`，插件默认 true）。
type SubagentControlConfig struct {
	// Interrupt 为真才注册 `interrupt_agent`；false 时**服务 API 仍然可用**，
	// 只是模型看不到那个工具。
	Interrupt bool
}

// SubagentControlPluginDefaultInterrupt 是**插件**自己的默认值（true）。
// 官方：`dsh-tool-subagent-control/src/index.ts:30-32`。
const SubagentControlPluginDefaultInterrupt = true

// SubagentControlBuiltinInterrupt 是**内置配置**的默认值（false）。
//
// 官方：base bundle `dsh-base/cordis.patch.yml:386-389` 写 `interrupt: false`；
// Web 的 `standard`/`cordis`/`ptc` 三 preset 同样写 false
// （`dsh-web-app/presets/standard.patch.yml:98` 等）。文档原文：
// "Models using these configurations no longer receive `interrupt_agent`，
//
//	including in Team mode."（`docs/upgrade-guide/v0.2.2-rc.1/subagent-interrupt-default/guide.md:7-9`）
//
// 宿主是 base 形态（不是 Web preset），所以内置默认取 **false**。
const SubagentControlBuiltinInterrupt = false

// EnvSubagentControlInterrupt 是宿主侧覆盖 `interrupt` 的开关名。
//
// 官方用 profile 的配置行（`$DSH_HOME/profiles/<profile>/cordis.patch.yml` 里
// `tool-subagent-control.config.interrupt`）覆盖；本仓 Go 宿主的模块接线签名由
// `cmd/dshd/main.go` 固定（`team.Module(dir)`），所以等价覆盖项走环境变量：
// 与 `modules/tool/skill` 既有的 `DSHD_SKILLS_*` 是同一套做法。
const EnvSubagentControlInterrupt = "DSHD_TOOL_SUBAGENT_CONTROL_INTERRUPT"

// DefaultSubagentControlConfig 是宿主内置默认（与官方内置配置一致：关）。
func DefaultSubagentControlConfig() SubagentControlConfig {
	return SubagentControlConfig{Interrupt: SubagentControlBuiltinInterrupt}
}

// ResolveSubagentControlConfig 解析生效配置，优先级（高 → 低）：
//
//  1. profile 显式配置行（官方 `tool-subagent-control.config.interrupt`）；
//  2. 环境变量 EnvSubagentControlInterrupt；
//  3. 内置默认 SubagentControlBuiltinInterrupt（false）。
//
// 非法值 fail-loud（写错一个布尔不该悄悄变成"另一套工具面"），
// 与官方 schemastery 在加载期拒绝非法 `interrupt` 同义。
func ResolveSubagentControlConfig(profile *bool, getenv func(string) string) (SubagentControlConfig, error) {
	cfg := DefaultSubagentControlConfig()
	if profile != nil {
		cfg.Interrupt = *profile
		return cfg, nil
	}
	if getenv == nil {
		return cfg, nil
	}
	raw := strings.TrimSpace(getenv(EnvSubagentControlInterrupt))
	if raw == "" {
		return cfg, nil
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "on":
		cfg.Interrupt = true
		return cfg, nil
	case "0", "false", "no", "off":
		cfg.Interrupt = false
		return cfg, nil
	}
	return cfg, fmt.Errorf(
		"%s=%q 不是布尔值：官方 interrupt 只接受 true/false（内置默认 %v）",
		EnvSubagentControlInterrupt, raw, SubagentControlBuiltinInterrupt)
}

// ParseSubagentControlConfigRow 把 profile 里的一行 `config` 解成 `interrupt` 覆盖值。
//
// 存在的理由：官方 patch 的 `config` 是**整份替换**语义，所以"只写了 interrupt"和
// "没写这一行"必须能区分——返回 nil 指针表示"没有覆盖"，交回内置默认。
// 未知键拒掉（官方 schemastery 默认拒绝未知键）。
func ParseSubagentControlConfigRow(raw map[string]any) (*bool, error) {
	var interrupt *bool
	for key, value := range raw {
		switch key {
		case "interrupt":
			b, ok := value.(bool)
			if !ok {
				return nil, &TeamConfigError{
					Code: TeamCodeInvalidConfig,
					Msg:  "tool-subagent-control: interrupt must be a boolean",
				}
			}
			interrupt = &b
		default:
			return nil, &TeamConfigError{
				Code: TeamCodeInvalidConfig,
				Msg:  fmt.Sprintf("tool-subagent-control: unknown configuration key %q", key),
			}
		}
	}
	return interrupt, nil
}
