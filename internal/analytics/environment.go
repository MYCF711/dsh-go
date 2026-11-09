package analytics

// 环境采集：官方 productAnalytics 启动时跑一次的**本地**元数据采集
// （dsh-client-product-analytics/lib/index.js:16-201）。
//
// 官方语义照抄的部分：
//   - 固定 argv、不经 shell（argv 里不可能出现事件内容）；
//   - 每次查询有 `timeout` 与 `maxOutputBytes`（就是本文件的两个官方参数）；
//   - 一个数据源失败只让**依赖它的字段**缺失，原因写进 missing_fields；
//   - 采集结果在 analytics 实例生命周期内复用（不是每条事件采一次）。
//
// 本仓与官方的两处**有意差异**（都写进 parameter-register 的 note）：
//  1. `memory_total_bytes` / `cpu_model` 不采集：前者要读系统内存总量，后者是设备
//     指纹级信息；本仓默认"本地零出网"，这两个字段对本地分析没有价值却扩大本地面
//     指纹面，因此如实进 missing_fields（reason=unavailable），不假装。
//  2. 非 Windows 平台不做 `sw_vers`/`sysctl` 分支：本项目稳定版只支持 Windows
//     （docs 的支持范围裁定），非 Windows 上这些字段如实标 unsupported，且**一个
//     子进程都不起**。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

const (
	// OfficialEnvironmentQueryTimeoutMillis 是官方 `environmentQueryTimeoutMillis`
	// 的默认值（index.js:315）。
	OfficialEnvironmentQueryTimeoutMillis = 5000
	// OfficialEnvironmentQueryMaxOutputBytes 是官方 `environmentQueryMaxOutputBytes`
	// 的默认值（index.js:316）。
	OfficialEnvironmentQueryMaxOutputBytes = 16384

	// EnvEnvironmentQueryTimeoutMS 覆盖环境查询的单次超时（毫秒）。
	EnvEnvironmentQueryTimeoutMS = "DSHD_ANALYTICS_ENVIRONMENT_QUERY_TIMEOUT_MS"
	// EnvEnvironmentQueryMaxOutputBytes 覆盖环境查询的输出上限（字节）。
	EnvEnvironmentQueryMaxOutputBytes = "DSHD_ANALYTICS_ENVIRONMENT_QUERY_MAX_OUTPUT_BYTES"
)

// 采集失败的固定原因（逐字对齐官方 missing_fields 的取值）。
const (
	missingReasonUnavailable = "unavailable"
	missingReasonInvalid     = "invalid-value"
	missingReasonUnsupported = "unsupported"
	missingReasonTimeout     = "timeout"
	missingReasonOutputLimit = "output-limit"
	missingReasonQueryFailed = "query-failed"
)

var (
	// ErrEnvironmentQueryTimeout 表示这次固定 argv 查询超时（官方 "timeout"）。
	ErrEnvironmentQueryTimeout = errors.New("analytics: 环境查询超时")
	// ErrEnvironmentOutputLimit 表示输出超过 maxOutputBytes（官方 "output-limit"）。
	ErrEnvironmentOutputLimit = errors.New("analytics: 环境查询输出超过上限")
)

// EnvironmentQueryConfig 返回**真实生效**的两个官方参数。
//
// 官方 zod 两个字段都是 `min(1)`：0/负数不是"关掉"，是非法值 → 夹到 1
// （夹而不是回默认，是因为"设了 1ms"和"没设"在语义上都合法，回默认会把
// 用户的显式配置悄悄吞掉）。
func EnvironmentQueryConfig(getenv func(string) string) (time.Duration, int) {
	if getenv == nil {
		getenv = os.Getenv
	}
	timeoutMillis := OfficialEnvironmentQueryTimeoutMillis
	if raw := strings.TrimSpace(getenv(EnvEnvironmentQueryTimeoutMS)); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			timeoutMillis = parsed
		}
	}
	if timeoutMillis < 1 {
		timeoutMillis = 1
	}
	maxBytes := OfficialEnvironmentQueryMaxOutputBytes
	if raw := strings.TrimSpace(getenv(EnvEnvironmentQueryMaxOutputBytes)); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			maxBytes = parsed
		}
	}
	if maxBytes < 1 {
		maxBytes = 1
	}
	return time.Duration(timeoutMillis) * time.Millisecond, maxBytes
}

// Environment 是一次采集的结果：字段 + 缺失原因（两者都不许省略）。
type Environment struct {
	Fields  map[string]any
	Missing map[string]string
}

// MissingSummary 按官方口径把缺失原因排成一行：`field=reason,field=reason`
// （index.js:397 先按字段名排序再 join）。没有缺失时为空串。
func (e Environment) MissingSummary() string {
	if len(e.Missing) == 0 {
		return ""
	}
	keys := make([]string, 0, len(e.Missing))
	for key := range e.Missing {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+e.Missing[key])
	}
	return strings.Join(parts, ",")
}

// environmentRunner 执行一次固定 argv 查询。
//
// 契约：输出超过 maxOutputBytes 必须返回 ErrEnvironmentOutputLimit；超时必须返回
// ErrEnvironmentQueryTimeout（用 ctx 判断）。返回值是 stdout 原文。
type environmentRunner func(ctx context.Context, command string, args []string, maxOutputBytes int) (string, error)

// defaultEnvironmentRunner 是生产执行器；测试整体替换它（见 environment_test.go）。
var defaultEnvironmentRunner environmentRunner = execEnvironmentQuery

// environmentOptions 是一次采集的全部输入（全部显式传入，不读全局）。
type environmentOptions struct {
	platform       string // "win32" / "darwin" / 其它（runtime.GOOS 的值域）
	systemRoot     string // Windows：SystemRoot（找 powershell.exe）
	goarch         string
	timeout        time.Duration
	maxOutputBytes int
	run            environmentRunner
	logicalCPUs    int
}

// collectEnvironment 采集官方字段集（可用字段 + 缺失原因）。
func collectEnvironment(ctx context.Context, opts environmentOptions) Environment {
	env := Environment{Fields: map[string]any{}, Missing: map[string]string{}}
	missing := func(fields []string, reason string) {
		for _, field := range fields {
			env.Missing[field] = reason
		}
	}
	set := func(field string, value any) {
		switch typed := value.(type) {
		case string:
			if typed == "" {
				missing([]string{field}, missingReasonUnavailable)
				return
			}
		case int:
			if typed <= 0 {
				missing([]string{field}, missingReasonUnavailable)
				return
			}
		}
		env.Fields[field] = value
	}

	platform := opts.platform
	if platform == "" {
		platform = runtime.GOOS
	}
	goarch := opts.goarch
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	cpus := opts.logicalCPUs
	if cpus <= 0 {
		cpus = runtime.NumCPU()
	}
	run := opts.run
	if run == nil {
		run = defaultEnvironmentRunner
	}

	set("os_name", osName(platform))
	set("app_arch", goarch)
	set("cpu_logical_count", cpus)
	// 见文件头差异 1：这两个字段本仓有意不读。
	missing([]string{"memory_total_bytes", "cpu_model"}, missingReasonUnavailable)

	if platform != "win32" {
		// 见文件头差异 2：稳定版只支持 Windows，不在别的平台起子进程。
		missing([]string{"os_version_full", "os_build", "os_arch", "cpu_physical_core_count"}, missingReasonUnsupported)
		return env
	}
	systemRoot := strings.TrimSpace(opts.systemRoot)
	if systemRoot == "" {
		missing([]string{"os_version_full", "os_build", "os_arch", "cpu_physical_core_count"}, missingReasonUnavailable)
		return env
	}
	powershell := filepath.Join(systemRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")

	versionOut, versionErr := runEncoded(ctx, run, powershell, versionQuery, opts)
	cpuOut, cpuErr := runEncoded(ctx, run, powershell, cpuQuery, opts)

	if versionErr != nil {
		missing([]string{"os_version_full", "os_build"}, reasonFor(versionErr))
	} else if parsed, ok := decodeWindowsVersion(versionOut); !ok {
		missing([]string{"os_version_full", "os_build"}, missingReasonInvalid)
	} else {
		if parsed.build == "" {
			missing([]string{"os_build"}, missingReasonUnavailable)
		} else {
			env.Fields["os_build"] = parsed.build
		}
		if parsed.major == nil || parsed.minor == nil || parsed.build == "" || parsed.revision == nil {
			missing([]string{"os_version_full"}, missingReasonUnavailable)
		} else {
			env.Fields["os_version_full"] = fmt.Sprintf("%d.%d.%s.%d", *parsed.major, *parsed.minor, parsed.build, *parsed.revision)
			// 官方的 os_version 来自 node os.release()；这里用同一个版本查询的
			// major.minor.build 拼出等价形态，读不出来就如实缺失。
			set("os_version", fmt.Sprintf("%s %d.%d.%s", platform, *parsed.major, *parsed.minor, parsed.build))
		}
	}

	if cpuErr != nil {
		missing([]string{"os_arch", "cpu_physical_core_count"}, reasonFor(cpuErr))
		return env
	}
	processors, ok := decodeWindowsProcessors(cpuOut)
	if !ok {
		missing([]string{"os_arch", "cpu_physical_core_count"}, missingReasonInvalid)
		return env
	}
	total := 0
	allKnown := true
	for _, processor := range processors {
		if processor.cores == nil {
			allKnown = false
			break
		}
		total += *processor.cores
	}
	if allKnown && total > 0 {
		env.Fields["cpu_physical_core_count"] = total
	} else {
		missing([]string{"cpu_physical_core_count"}, missingReasonUnavailable)
	}
	if arch, ok := windowsArchitectures[processors[0].architecture]; ok {
		uniform := true
		for _, processor := range processors {
			if processor.architecture != processors[0].architecture {
				uniform = false
				break
			}
		}
		if uniform {
			env.Fields["os_arch"] = arch
		} else {
			missing([]string{"os_arch"}, missingReasonUnavailable)
		}
	} else {
		missing([]string{"os_arch"}, missingReasonUnavailable)
	}
	return env
}

// osName 是官方 `platform === "darwin" ? "macos" : platform === "win32" ? "windows" : platform`
// （index.js:85）。
func osName(platform string) string {
	switch platform {
	case "darwin":
		return "macos"
	case "win32":
		return "windows"
	}
	return platform
}

func reasonFor(err error) string {
	switch {
	case errors.Is(err, ErrEnvironmentQueryTimeout):
		return missingReasonTimeout
	case errors.Is(err, ErrEnvironmentOutputLimit):
		return missingReasonOutputLimit
	}
	return missingReasonQueryFailed
}

// 官方两个固定脚本（index.js:68-76），逐字照抄；用 -EncodedCommand 传，
// 避免任何引号/转义在命令行上被重新解释。
const versionQuery = `
$ErrorActionPreference = 'Stop'
$r = Get-Item -LiteralPath 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion'
@{ major = $r.GetValue('CurrentMajorVersionNumber'); minor = $r.GetValue('CurrentMinorVersionNumber'); build = $r.GetValue('CurrentBuildNumber'); revision = $r.GetValue('UBR') } | ConvertTo-Json -Compress
`

const cpuQuery = `
$ErrorActionPreference = 'Stop'
ConvertTo-Json -Compress -InputObject @(Get-CimInstance -ClassName Win32_Processor -Property NumberOfCores,Architecture | ForEach-Object { @{ cores = $_.NumberOfCores; architecture = $_.Architecture } })
`

// runEncoded 跑一条固定脚本。
//
// 每次查询各自带一个 `opts.timeout` 的截止（官方也是每次 execFile 一个 timeout，
// 不是整次采集一个总预算）：一个慢查询不该吃掉另一条的预算，两条各自有界。
func runEncoded(ctx context.Context, run environmentRunner, powershell, script string, opts environmentOptions) (string, error) {
	queryCtx := ctx
	if opts.timeout > 0 {
		var cancel context.CancelFunc
		queryCtx, cancel = context.WithTimeout(ctx, opts.timeout)
		defer cancel()
	}
	encoded := base64.StdEncoding.EncodeToString(utf16LE(script))
	output, err := run(queryCtx, powershell, []string{"-NoProfile", "-NonInteractive", "-EncodedCommand", encoded}, opts.maxOutputBytes)
	if queryCtx.Err() != nil && errors.Is(queryCtx.Err(), context.DeadlineExceeded) {
		// 超时优先于"执行器返回了什么"：进程被 kill 时错误码不一定是 DeadlineExceeded。
		return "", ErrEnvironmentQueryTimeout
	}
	if err != nil {
		return "", err
	}
	if opts.maxOutputBytes > 0 && len(output) > opts.maxOutputBytes {
		// 执行器没守住上限（自定义 runner）时这里兜底：绝不把超限输出交给解析层。
		return "", ErrEnvironmentOutputLimit
	}
	return output, nil
}

func utf16LE(text string) []byte {
	units := utf16.Encode([]rune(text))
	out := make([]byte, 0, len(units)*2)
	for _, unit := range units {
		out = append(out, byte(unit), byte(unit>>8))
	}
	return out
}

// windowsArchitectures 是官方 architecture → 名字的映射（index.js:152-157）。
var windowsArchitectures = map[int]string{0: "ia32", 5: "arm", 9: "x64", 12: "arm64"}

type windowsVersionInfo struct {
	major    *int
	minor    *int
	build    string
	revision *int
}

// decodeWindowsVersion 解析版本脚本输出；形状不对就是 invalid-value（官方同口径）。
func decodeWindowsVersion(raw string) (windowsVersionInfo, bool) {
	var parsed struct {
		Major    *int    `json:"major"`
		Minor    *int    `json:"minor"`
		Build    *string `json:"build"`
		Revision *int    `json:"revision"`
	}
	if err := decodeJSONOutput(raw, &parsed); err != nil {
		return windowsVersionInfo{}, false
	}
	info := windowsVersionInfo{major: parsed.Major, minor: parsed.Minor, revision: parsed.Revision}
	if parsed.Build != nil {
		// 官方只接受纯数字的 build（`z.string().regex(/^\d+$/)`）。
		if _, err := strconv.Atoi(*parsed.Build); err != nil {
			return windowsVersionInfo{}, false
		}
		info.build = *parsed.Build
	}
	return info, true
}

type windowsProcessor struct {
	cores        *int
	architecture int
}

func decodeWindowsProcessors(raw string) ([]windowsProcessor, bool) {
	var parsed []struct {
		Cores        *int `json:"cores"`
		Architecture *int `json:"architecture"`
	}
	if err := decodeJSONOutput(raw, &parsed); err != nil || len(parsed) == 0 {
		return nil, false
	}
	out := make([]windowsProcessor, 0, len(parsed))
	for _, item := range parsed {
		processor := windowsProcessor{cores: item.Cores}
		if item.Architecture != nil {
			processor.architecture = *item.Architecture
		} else {
			processor.architecture = -1
		}
		out = append(out, processor)
	}
	return out, true
}

// execEnvironmentQuery 是生产执行器：固定 argv、无 shell、有超时与输出上限。
func execEnvironmentQuery(ctx context.Context, command string, args []string, maxOutputBytes int) (string, error) {
	queryCtx, cancel := context.WithTimeout(ctx, timeoutFromContext(ctx))
	defer cancel()

	cmd := exec.CommandContext(queryCtx, command, args...)
	stdout := &limitedWriter{limit: maxOutputBytes}
	cmd.Stdout = stdout
	cmd.Stderr = io.Discard
	// Windows 上不弹窗（官方 `windowsHide: true`）。
	hideWindow(cmd)
	err := cmd.Run()
	if stdout.overflow {
		return "", ErrEnvironmentOutputLimit
	}
	if queryCtx.Err() != nil && errors.Is(queryCtx.Err(), context.DeadlineExceeded) {
		return "", ErrEnvironmentQueryTimeout
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "", ErrEnvironmentQueryTimeout
		}
		return "", fmt.Errorf("%w: %v", ErrEnvironmentQueryFailed, err)
	}
	return stdout.String(), nil
}

// decodeJSONOutput 解析固定脚本的 JSON 输出（空输出/坏 JSON 都是 invalid-value）。
func decodeJSONOutput(raw string, out any) error {
	trimmed := strings.TrimSpace(strings.TrimPrefix(raw, "\ufeff"))
	if trimmed == "" {
		return errors.New("analytics: 环境查询没有输出")
	}
	return json.Unmarshal([]byte(trimmed), out)
}

// ErrEnvironmentQueryFailed 是"跑了但失败"的底层原因（会映射成 query-failed）。
var ErrEnvironmentQueryFailed = errors.New("analytics: 环境查询执行失败")

// timeoutFromContext 取 ctx 的剩余预算；没有截止时间时给一个保守的 5s
// （调用方 collectEnvironment 永远会给截止时间，这里只是兜底）。
func timeoutFromContext(ctx context.Context) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining > 0 {
			return remaining
		}
		return time.Nanosecond
	}
	return 5 * time.Second
}

// limitedWriter 在写超上限时立刻记下 overflow 并停止写入（不无限吃内存）。
type limitedWriter struct {
	limit    int
	written  int
	overflow bool
	buffer   strings.Builder
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if w.written+len(p) > w.limit {
		w.overflow = true
		return len(p), nil
	}
	w.written += len(p)
	w.buffer.Write(p)
	return len(p), nil
}

func (w *limitedWriter) String() string { return w.buffer.String() }

// allowedEnvironmentKeys 是导出前的**第二次**白名单：环境维度只允许官方字段集，
// 其余键（以及任何嵌套结构）在导出一律丢弃。
var allowedEnvironmentKeys = map[string]struct{}{
	"os_name":                 {},
	"app_arch":                {},
	"os_version":              {},
	"os_version_full":         {},
	"os_build":                {},
	"os_arch":                 {},
	"cpu_logical_count":       {},
	"cpu_physical_core_count": {},
	"memory_total_bytes":      {},
	"cpu_model":               {},
}

// sanitizeEnvironment 复检环境维度（导出与读回都用它）。
func sanitizeEnvironment(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		if _, allowed := allowedEnvironmentKeys[key]; !allowed {
			continue
		}
		switch typed := value.(type) {
		case string:
			if sanitized := sanitizeString(typed); sanitized != "" {
				out[key] = sanitized
			}
		case bool:
			out[key] = typed
		case float64:
			out[key] = typed
		case int:
			out[key] = float64(typed)
		case json.Number:
			out[key] = typed.String()
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
