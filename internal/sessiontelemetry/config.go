package sessiontelemetry

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"dshgo/internal/analytics"
	"dshgo/internal/settingsform"
)

// ConfigFromSettings 把 settings 的四个 L1 叶子与官方 env 覆盖装配成后端配置。
// 显式 exporterURL 优先于 DSHD_TELEMETRY_OTLP_URL / DSH_TELEMETRY_OTLP_URL；
// product analytics 专用 env 不会串到这条独立 Session 通道。
func ConfigFromSettings(values map[string]any, getenv func(string) string) (Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	cfg := Config{
		Mode:            settingsform.DefaultSessionTelemetryMode,
		ShutdownTimeout: DefaultShutdownTimeout,
		MaxRequestBytes: settingsform.DefaultMaxRequestBytes,
	}
	if value, ok := values[settingsform.FieldSessionTelemetryMode]; ok {
		mode, valid := value.(string)
		if !valid {
			return Config{}, fmt.Errorf("session-telemetry-otel: mode must be a string")
		}
		cfg.Mode = mode
	}
	if value, ok := values[settingsform.FieldSessionTelemetryExporterURL]; ok {
		endpoint, valid := value.(string)
		if !valid {
			return Config{}, fmt.Errorf("session-telemetry-otel: exporter.url must be a string")
		}
		cfg.ExporterURL = strings.TrimSpace(endpoint)
	}
	if cfg.ExporterURL == "" {
		cfg.ExporterURL, _, _ = analytics.SessionTelemetryCollectorURLFromEnv(getenv)
	}
	if value, ok := values[settingsform.FieldSessionTelemetryShutdownTimeoutMillis]; ok {
		milliseconds, err := integerSetting(value, "shutdownTimeoutMillis")
		if err != nil {
			return Config{}, err
		}
		if milliseconds < settingsform.ShutdownTimeoutMillisMin || milliseconds > settingsform.ShutdownTimeoutMillisMax {
			return Config{}, fmt.Errorf("session-telemetry-otel: shutdownTimeoutMillis must be in [%d,%d]",
				settingsform.ShutdownTimeoutMillisMin, settingsform.ShutdownTimeoutMillisMax)
		}
		cfg.ShutdownTimeout = time.Duration(milliseconds) * time.Millisecond
	}
	if value, ok := values[settingsform.FieldSessionTelemetryMaxRequestBytes]; ok {
		limit, err := integerSetting(value, "maxRequestBytes")
		if err != nil {
			return Config{}, err
		}
		cfg.MaxRequestBytes = int(limit)
	}
	if cfg.Mode != ModeDisabled && cfg.Mode != ModeFeedbackOnly {
		return Config{}, fmt.Errorf("session-telemetry-otel: unsupported mode %q", cfg.Mode)
	}
	// DISABLED 与官方一致，不读取/校验传输配置；尤其“只配 URL”不会激活它。
	if cfg.Mode == ModeDisabled {
		return cfg, nil
	}
	if cfg.ExporterURL == "" {
		return Config{}, fmt.Errorf("session-telemetry-otel: exporter.url is required (the full OTLP logs endpoint)")
	}
	if err := validateEndpoint(cfg.ExporterURL); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func integerSetting(value any, name string) (int64, error) {
	var number int64
	switch typed := value.(type) {
	case int:
		number = int64(typed)
	case int64:
		number = typed
	case float64:
		number = int64(typed)
		if float64(number) != typed {
			return 0, fmt.Errorf("session-telemetry-otel: %s must be an integer", name)
		}
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, fmt.Errorf("session-telemetry-otel: %s must be an integer: %w", name, err)
		}
		number = parsed
	default:
		return 0, fmt.Errorf("session-telemetry-otel: %s must be an integer", name)
	}
	return number, nil
}

func validateEndpoint(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("session-telemetry-otel: exporter.url must be a valid http(s) URL, got %q", raw)
	}
	return nil
}
