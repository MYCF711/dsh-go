package analytics

// 官方 OTLP 端点环境变量的接线证据（GO.5 / T1）。
//
// 语义（三条，缺一即返工）：
//  1. **默认仍是本地零出网**：不设 env 时 CollectorURL 为空、mode=local；
//     即使设了 env，也只改端点，不把 mode 抬成 local+export。
//  2. **显式配置 > env > 默认**：策略文件里已有 collectorUrl 时 env 不覆盖。
//  3. **非法 URL fail-loud**：Open 直接报错并点名是哪个变量，绝不静默回退。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// clearCollectorEnv 把四个端点变量都清成空串（等价于"没设"）。
//
// 为什么用 t.Setenv 而不是 os.Unsetenv：t.Setenv 会在用例结束时自动还原，
// 不会把宿主的真实环境改坏；空串在我们的解析里等价于"未设置"。
func clearCollectorEnv(t *testing.T) {
	t.Helper()
	for _, name := range collectorURLEnvNames {
		t.Setenv(name, "")
	}
}

// ① 不设 env：CollectorURL 不变（空），mode 仍是 local。
func TestCollectorURLUnchangedWithoutEnv(t *testing.T) {
	clearCollectorEnv(t)
	service, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer service.Close()

	policy := service.Policy()
	if policy.CollectorURL != "" {
		t.Fatalf("没设 env 时 CollectorURL 不该有值，得到 %q", policy.CollectorURL)
	}
	if policy.Mode != ModeLocal {
		t.Fatalf("默认 mode 必须是 %q，得到 %q", ModeLocal, policy.Mode)
	}
}

// ② 设了官方 env：解析出对应 URL，但**不因此外发**（零出站）。
func TestOfficialOTLPEnvFeedsCollectorURLWithoutExport(t *testing.T) {
	for _, name := range []string{EnvTelemetryOTLPURL, EnvProductAnalyticsOTLPURL} {
		t.Run(name, func(t *testing.T) {
			clearCollectorEnv(t)
			var hits atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			t.Setenv(name, server.URL+"/v1/logs")
			service, err := Open(t.TempDir())
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer service.Close()

			policy := service.Policy()
			if policy.CollectorURL != server.URL+"/v1/logs" {
				t.Fatalf("%s 没有喂进 CollectorURL：%q", name, policy.CollectorURL)
			}
			// 只改端点，不改模式：默认仍是本地零出网。
			if policy.Mode != ModeLocal {
				t.Fatalf("读了 %s 之后 mode 被改成 %q（必须仍是 %q）", name, policy.Mode, ModeLocal)
			}
			if _, err := service.Report(context.Background(), testEvent("desktop_app_launch", nil)); err != nil {
				t.Fatalf("Report: %v", err)
			}
			if got := hits.Load(); got != 0 {
				t.Fatalf("本地模式仍然外发了 %d 次请求", got)
			}
			if got := service.OutboundRequests(); got != 0 {
				t.Fatalf("OutboundRequests = %d，本地模式必须为 0", got)
			}
		})
	}
}

// ③ 更具体的官方名（product analytics）压过通用的 telemetry 名。
func TestProductAnalyticsEnvWinsOverTelemetryEnv(t *testing.T) {
	clearCollectorEnv(t)
	t.Setenv(EnvTelemetryOTLPURL, "http://telemetry.example/v1/logs")
	t.Setenv(EnvProductAnalyticsOTLPURL, "http://analytics.example/v1/logs")

	service, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer service.Close()

	if got := service.Policy().CollectorURL; got != "http://analytics.example/v1/logs" {
		t.Fatalf("优先级不对：CollectorURL = %q", got)
	}
}

// ④ 宿主前缀（DSHD_*）也认，且与官方名同义。
func TestHostPrefixedOTLPEnvAlsoWorks(t *testing.T) {
	clearCollectorEnv(t)
	t.Setenv(envHostTelemetryOTLPURL, "http://host-collector.example/v1/logs")

	service, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer service.Close()

	if got := service.Policy().CollectorURL; got != "http://host-collector.example/v1/logs" {
		t.Fatalf("DSHD_ 前缀没有生效：CollectorURL = %q", got)
	}
}

// ⑤ 非法 URL：Open 必须报错，且报错里点名是哪个变量。
func TestInvalidCollectorURLEnvFailsLoud(t *testing.T) {
	for _, bad := range []string{"ftp://collector.example/v1/logs", "collector.example/v1/logs", "http://"} {
		t.Run(bad, func(t *testing.T) {
			clearCollectorEnv(t)
			t.Setenv(EnvTelemetryOTLPURL, bad)

			service, err := Open(t.TempDir())
			if err == nil {
				service.Close()
				t.Fatalf("非法端点 %q 被静默接受了（CollectorURL=%q）", bad, service.Policy().CollectorURL)
			}
			if !strings.Contains(err.Error(), EnvTelemetryOTLPURL) {
				t.Fatalf("报错没有点名 %s：%v", EnvTelemetryOTLPURL, err)
			}
		})
	}
}

// ⑥ 显式配置 > env：策略文件里的 collectorUrl 不被 env 覆盖。
func TestExplicitPolicyCollectorURLBeatsEnv(t *testing.T) {
	clearCollectorEnv(t)
	t.Setenv(EnvTelemetryOTLPURL, "http://from-env.example/v1/logs")

	home := t.TempDir()
	dir := analyticsDir(home)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	explicit := `{
  "enabled": true,
  "mode": "local+export",
  "collectorUrl": "http://from-policy.example/v1/logs",
  "retentionDays": 30,
  "samplingRate": 1,
  "policyVersion": 3
}
`
	if err := os.WriteFile(filepath.Join(dir, policyFileName), []byte(explicit), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	service, err := Open(home)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer service.Close()

	policy := service.Policy()
	if policy.CollectorURL != "http://from-policy.example/v1/logs" {
		t.Fatalf("显式配置被 env 覆盖了：%q", policy.CollectorURL)
	}
	if policy.Mode != ModeLocalExport {
		t.Fatalf("mode = %q", policy.Mode)
	}
}

// ⑦ 环境变量提供的端点也能满足 local+export 的"必须有端点"约束，
// 但不会把 mode 自己抬上去。
func TestEnvCollectorURLSatisfiesExportModeValidation(t *testing.T) {
	clearCollectorEnv(t)
	t.Setenv(EnvTelemetryOTLPURL, "http://from-env.example/v1/logs")

	service, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer service.Close()

	policy := service.Policy()
	policy.Mode = ModeLocalExport // 只改模式，端点由 env 补
	updated, err := service.UpdatePolicy(policy)
	if err != nil {
		t.Fatalf("UpdatePolicy(local+export + env 端点): %v", err)
	}
	if updated.CollectorURL != "http://from-env.example/v1/logs" {
		t.Fatalf("UpdatePolicy 之后的端点 = %q", updated.CollectorURL)
	}
}
