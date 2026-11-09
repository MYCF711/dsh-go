package analytics

// GO.7 / T9：analytics 线三条官方参数的真实现（先红后绿）。
//
// 官方原件：
//   - `environmentQueryTimeoutMillis`     @ dsh-client-product-analytics/lib/index.js:315（默认 5000）
//   - `environmentQueryMaxOutputBytes`    @ 同上 :316（默认 16384）
//   - `telemetry.anonymousUserId`         @ dsh-base/cordis.patch.yml:210
//     （`$DSH_HOME/.anonymous-user-id`，随机 UUID，删文件即重置身份）
//
// 官方用法：
//   - `queryEnvironment()`（:16-54）用 `execFile` 跑**固定 argv**（无 shell），
//     `timeout`/`maxBuffer` 就是上面两个参数；失败只让"依赖它的字段"缺失，
//     并在 `missing_fields` 里带固定原因（output-limit / timeout / query-failed /
//     invalid-value / unavailable / unsupported）；
//   - `report()`（:386-409）把环境元数据并入 attributes，并把 `.anonymous-user-id`
//     作为 Resource 的 user.id。
//
// 红侧事实（GO.7 之前）：`internal/analytics` 里 `grep -rn 'environment|runtime.GOOS'` = 0 命中，
// `grep -rn anonymous` = 0 命中——既没有环境采集，也没有匿名身份。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf16"
)

// withEnvRunner 把生产执行器换成桩，并在测试结束时还原。
func withEnvRunner(t *testing.T, run environmentRunner) {
	t.Helper()
	previous := defaultEnvironmentRunner
	defaultEnvironmentRunner = run
	t.Cleanup(func() { defaultEnvironmentRunner = previous })
}

func TestEnvironmentQueryConfigMatchesOfficialDefaults(t *testing.T) {
	timeout, maxBytes := EnvironmentQueryConfig(func(string) string { return "" })
	if timeout != 5*time.Second || OfficialEnvironmentQueryTimeoutMillis != 5000 {
		t.Fatalf("默认超时 = %v（常量 %d），want 5000ms（官方 :315）", timeout, OfficialEnvironmentQueryTimeoutMillis)
	}
	if maxBytes != 16384 || OfficialEnvironmentQueryMaxOutputBytes != 16384 {
		t.Fatalf("默认输出上限 = %d（常量 %d），want 16384（官方 :316）", maxBytes, OfficialEnvironmentQueryMaxOutputBytes)
	}

	t.Setenv(EnvEnvironmentQueryTimeoutMS, "900")
	t.Setenv(EnvEnvironmentQueryMaxOutputBytes, "2048")
	timeout, maxBytes = EnvironmentQueryConfig(func(key string) string { return os.Getenv(key) })
	if timeout != 900*time.Millisecond {
		t.Fatalf("%s=900 → %v，want 900ms", EnvEnvironmentQueryTimeoutMS, timeout)
	}
	if maxBytes != 2048 {
		t.Fatalf("%s=2048 → %d", EnvEnvironmentQueryMaxOutputBytes, maxBytes)
	}

	// 非法/越界值：官方 min(1) —— 不能变成 0（那等于"立刻超时/永远读不到输出"）。
	for _, raw := range []string{"0", "-5", "abc", "  "} {
		timeout, maxBytes = EnvironmentQueryConfig(func(string) string { return "" })
		if raw != "  " {
			timeout, maxBytes = queryConfigWith(t, EnvEnvironmentQueryTimeoutMS, raw, EnvEnvironmentQueryMaxOutputBytes, raw)
		}
		if timeout < time.Millisecond {
			t.Fatalf("%q 解析出 %v，必须夹到 >= 1ms（官方 min(1)）", raw, timeout)
		}
		if maxBytes < 1 {
			t.Fatalf("%q 解析出 %d 字节上限，必须夹到 >= 1", raw, maxBytes)
		}
	}
}

// decodeEncodedCommand 把 `-EncodedCommand <base64-utf16le>` 还原成脚本原文，
// 用来确认宿主发出去的是**官方那两条固定脚本**（而不是任何拼接了事件内容的串）。
func decodeEncodedCommand(t *testing.T, args []string) string {
	t.Helper()
	for index, arg := range args {
		if arg != "-EncodedCommand" || index+1 >= len(args) {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(args[index+1])
		if err != nil || len(raw)%2 != 0 {
			t.Fatalf("-EncodedCommand 不是合法 base64：%v", err)
		}
		units := make([]uint16, 0, len(raw)/2)
		for i := 0; i+1 < len(raw); i += 2 {
			units = append(units, uint16(raw[i])|uint16(raw[i+1])<<8)
		}
		return string(utf16.Decode(units))
	}
	t.Fatalf("参数里没有 -EncodedCommand：%v", args)
	return ""
}

func queryConfigWith(t *testing.T, keyA, valueA, keyB, valueB string) (time.Duration, int) {
	t.Helper()
	getenv := func(key string) string {
		switch key {
		case keyA:
			return valueA
		case keyB:
			return valueB
		}
		return ""
	}
	return EnvironmentQueryConfig(getenv)
}

func TestCollectEnvironmentUsesFixedArgvAndMarksMissingFields(t *testing.T) {
	var calls atomic.Int32
	var sawTimeout, sawMaxBytes atomic.Int64
	withEnvRunner(t, func(ctx context.Context, command string, args []string, maxOutputBytes int) (string, error) {
		calls.Add(1)
		sawMaxBytes.Store(int64(maxOutputBytes))
		if deadline, ok := ctx.Deadline(); ok {
			sawTimeout.Store(int64(time.Until(deadline).Seconds() * 1000))
		}
		script := decodeEncodedCommand(t, args)
		switch {
		case strings.Contains(script, "Win32_Processor"):
			return `[{"cores":4,"architecture":9},{"cores":4,"architecture":9}]`, nil
		case strings.Contains(script, "CurrentBuildNumber"):
			return `{"major":10,"minor":0,"build":"22631","revision":4317}`, nil
		default:
			t.Fatalf("固定脚本里没认出这两条官方查询之一：%q", script)
			return "", nil
		}
	})

	env := collectEnvironment(context.Background(), environmentOptions{
		platform: "win32", systemRoot: `C:\Windows`, goarch: "amd64",
		timeout: 5 * time.Second, maxOutputBytes: 16384,
		run: defaultEnvironmentRunner,
	})
	if env.Fields["os_name"] != "windows" {
		t.Fatalf("os_name = %v，want windows（官方把 win32 映射成 windows）", env.Fields["os_name"])
	}
	if env.Fields["app_arch"] != "amd64" {
		t.Fatalf("app_arch = %v，want amd64", env.Fields["app_arch"])
	}
	if env.Fields["cpu_logical_count"] != runtime.NumCPU() {
		t.Fatalf("cpu_logical_count = %v，want %d", env.Fields["cpu_logical_count"], runtime.NumCPU())
	}
	if env.Fields["os_version_full"] != "10.0.22631.4317" {
		t.Fatalf("os_version_full = %v，want 10.0.22631.4317", env.Fields["os_version_full"])
	}
	if env.Fields["os_build"] != "22631" {
		t.Fatalf("os_build = %v，want 22631", env.Fields["os_build"])
	}
	if env.Fields["os_arch"] != "x64" {
		t.Fatalf("os_arch = %v，want x64（官方 architecture 9 → x64）", env.Fields["os_arch"])
	}
	if env.Fields["cpu_physical_core_count"] != 8 {
		t.Fatalf("cpu_physical_core_count = %v，want 8（两路各 4 核）", env.Fields["cpu_physical_core_count"])
	}
	// 本仓有意不读的两个字段（见 environment.go 文件头差异 1）：内存总量与 CPU 型号
	// 是设备指纹级信息，对本地分析没价值。它们必须**如实**进 missing，而不是缺失即静默。
	wantMissing := map[string]string{"memory_total_bytes": "unavailable", "cpu_model": "unavailable"}
	if len(env.Missing) != len(wantMissing) {
		t.Fatalf("缺失集合 = %v，want %v", env.Missing, wantMissing)
	}
	for field, reason := range wantMissing {
		if env.Missing[field] != reason {
			t.Fatalf("%s 的缺失原因 = %q，want %q", field, env.Missing[field], reason)
		}
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("固定 argv 查询跑了 %d 次，want 2（版本 + CPU）", got)
	}
	if sawMaxBytes.Load() != 16384 {
		t.Fatalf("maxOutputBytes 没传给执行器：%d", sawMaxBytes.Load())
	}
	if sawTimeout.Load() <= 0 {
		t.Fatalf("执行器拿到的 ctx 没有截止时间（超时参数没接线）")
	}
}

func TestCollectEnvironmentDegradesPerField(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		output string
		reason string
		fields []string
	}{
		{name: "超时", err: ErrEnvironmentQueryTimeout, reason: "timeout", fields: []string{"os_version_full", "os_build"}},
		{name: "输出超限", err: ErrEnvironmentOutputLimit, reason: "output-limit", fields: []string{"os_version_full", "os_build"}},
		{name: "执行失败", err: errors.New("boom"), reason: "query-failed", fields: []string{"os_version_full", "os_build"}},
		{name: "非法输出", output: "not json", reason: "invalid-value", fields: []string{"os_version_full", "os_build"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withEnvRunner(t, func(context.Context, string, []string, int) (string, error) {
				return tc.output, tc.err
			})
			env := collectEnvironment(context.Background(), environmentOptions{
				platform: "win32", systemRoot: `C:\Windows`, goarch: "amd64",
				timeout: time.Second, maxOutputBytes: 16384, run: defaultEnvironmentRunner,
			})
			for _, field := range tc.fields {
				if got := env.Missing[field]; got != tc.reason {
					t.Fatalf("%s 的缺失原因 = %q，want %q（整个 missing：%v）", field, got, tc.reason, env.Missing)
				}
			}
			// 一个数据源失败不能把整次采集变成"什么都没有"：与它无关的字段照常。
			if env.Fields["os_name"] != "windows" || env.Fields["app_arch"] != "amd64" {
				t.Fatalf("失败的数据源连带丢了基础字段：%+v", env.Fields)
			}
		})
	}
}

func TestCollectEnvironmentSkipsSubprocessOffWindows(t *testing.T) {
	var calls atomic.Int32
	withEnvRunner(t, func(context.Context, string, []string, int) (string, error) {
		calls.Add(1)
		return "", nil
	})
	env := collectEnvironment(context.Background(), environmentOptions{
		platform: "linux", goarch: "amd64",
		timeout: time.Second, maxOutputBytes: 1024, run: defaultEnvironmentRunner,
	})
	if calls.Load() != 0 {
		t.Fatalf("非 Windows 平台不该起子进程：跑了 %d 次", calls.Load())
	}
	if env.Missing["os_version_full"] != "unsupported" {
		t.Fatalf("非 Windows 的 os_version_full 缺失原因 = %q，want unsupported", env.Missing["os_version_full"])
	}
	if env.Fields["os_name"] != "linux" {
		t.Fatalf("os_name = %v，want linux", env.Fields["os_name"])
	}
}

func TestAnonymousUserIDIsLocalGeneratedAndResettable(t *testing.T) {
	home := t.TempDir()
	service, err := Open(home)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer service.Close()

	id := service.AnonymousUserID()
	if !isUUIDv4(id) {
		t.Fatalf("匿名 id = %q，不是 UUIDv4", id)
	}
	path := filepath.Join(home, anonymousUserIDFileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("匿名身份文件没生成（%s）：%v", path, err)
	}
	if strings.TrimSpace(string(raw)) != id {
		t.Fatalf("文件内容 %q 与内存里的 id %q 不一致", strings.TrimSpace(string(raw)), id)
	}

	// 重开同一 home：身份必须稳定（否则跨会话去重失效）。
	again, err := Open(home)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer again.Close()
	if again.AnonymousUserID() != id {
		t.Fatalf("重开后匿名 id 变了：%q → %q", id, again.AnonymousUserID())
	}

	// 官方语义："删文件即重置身份"。
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	reset, err := Open(home)
	if err != nil {
		t.Fatalf("reopen after delete: %v", err)
	}
	defer reset.Close()
	if reset.AnonymousUserID() == id || !isUUIDv4(reset.AnonymousUserID()) {
		t.Fatalf("删掉身份文件后应生成新 id，得到 %q（旧 %q）", reset.AnonymousUserID(), id)
	}

	// 文件被写坏：按"删文件即重置"等价重写，而不是让整条管道打不开。
	broken := t.TempDir()
	if err := os.WriteFile(filepath.Join(broken, anonymousUserIDFileName), []byte("not-a-uuid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixed, err := Open(broken)
	if err != nil {
		t.Fatalf("坏身份文件不该让 Open 失败：%v", err)
	}
	defer fixed.Close()
	if !isUUIDv4(fixed.AnonymousUserID()) {
		t.Fatalf("坏文件没有被重置成新 id：%q", fixed.AnonymousUserID())
	}
}

func TestReportCarriesEnvironmentAndAnonymousIDWithoutNewOutbound(t *testing.T) {
	withEnvRunner(t, func(context.Context, string, []string, int) (string, error) {
		return `{"major":10,"minor":0,"build":"19045","revision":1}`, nil
	})
	var hits atomic.Int64
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()

	home := t.TempDir()
	service, err := Open(home)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer service.Close()
	// 即使配了 collector，默认 mode=local 也绝不出网（W0 裁定）。
	policy := service.Policy()
	policy.CollectorURL = collector.URL
	policy.Mode = ModeLocal
	if _, err := service.UpdatePolicy(policy); err != nil {
		t.Fatalf("UpdatePolicy: %v", err)
	}

	// 调用方自己塞 os_name 必须被白名单丢弃：环境维度只能来自采集器。
	if _, err := service.Report(context.Background(), testEvent("desktop_app_launch", map[string]any{
		"os_name": "forged", "app_arch": "forged", "model_name": "deepseek-chat",
	})); err != nil {
		t.Fatalf("Report: %v", err)
	}
	records, err := service.Dump(time.Time{})
	if err != nil || len(records) != 1 {
		t.Fatalf("Dump: %v %d", err, len(records))
	}
	record := records[0]
	if record.AnonymousUserID != service.AnonymousUserID() || record.AnonymousUserID == "" {
		t.Fatalf("记录没有带匿名身份：%q（服务 %q）", record.AnonymousUserID, service.AnonymousUserID())
	}
	if record.Environment["os_name"] == "forged" {
		t.Fatalf("调用方伪造的 os_name 混进了环境维度：%v", record.Environment)
	}
	if record.Environment["os_name"] != "windows" && record.Environment["os_name"] != "linux" && record.Environment["os_name"] != "macos" {
		t.Fatalf("环境维度里没有真实 os_name：%v", record.Environment)
	}
	if record.Attributes["os_name"] != nil {
		t.Fatalf("attributes 里不该出现环境键：%v", record.Attributes)
	}

	if hits.Load() != 0 || service.OutboundRequests() != 0 {
		t.Fatalf("默认 local 模式出网了：collector hits=%d outbound=%d", hits.Load(), service.OutboundRequests())
	}

	// 环境只采一次：多次 Report 不再重复起子进程（官方也缓存到 analytics 实例生命周期）。
	var calls atomic.Int32
	withEnvRunner(t, func(context.Context, string, []string, int) (string, error) {
		calls.Add(1)
		return "", nil
	})
	for index := 0; index < 3; index++ {
		if _, err := service.Report(context.Background(), testEvent("send_button_click", nil)); err != nil {
			t.Fatalf("Report #%d: %v", index, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("环境采集没有缓存：又跑了 %d 次", calls.Load())
	}
	if count, err := service.Verify(); err != nil || count != 4 {
		t.Fatalf("hash 链在加入环境/身份字段后必须仍然可复算：count=%d err=%v", count, err)
	}
}

func TestEnvironmentKeysSurviveExportSanitization(t *testing.T) {
	env := map[string]any{"os_name": "windows", "cpu_logical_count": float64(16), "evil": "drop-me"}
	clean := sanitizeEnvironment(env)
	if clean["os_name"] != "windows" {
		t.Fatalf("导出复检丢了合法环境字段：%v", clean)
	}
	if _, has := clean["evil"]; has {
		t.Fatalf("导出复检没有丢掉白名单外的键：%v", clean)
	}
	raw, err := json.Marshal(clean)
	if err != nil || !strings.Contains(string(raw), "os_name") {
		t.Fatalf("环境字段不可序列化：%v %v", string(raw), err)
	}
}
