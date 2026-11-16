package main

import (
	"errors"
	"fmt"
	"os"

	"dshgo/codecs/v4"
	"dshgo/internal/analytics"
	"dshgo/internal/session"
	"dshgo/internal/sessiontelemetry"
	"dshgo/internal/settingsform"
	"dshgo/internal/transport"
)

// openSessionTelemetry 是官方 base #38 的生产装配点。设置只提供四个 L1
// 叶子，端点 env 由 ConfigFromSettings 合并；默认 DISABLED 是本仓零出网部署差异。
func openSessionTelemetry(home string, store *serveSettingsStore, applicationVersion string) (*sessiontelemetry.Coordinator, error) {
	if store == nil || store.namespaces == nil {
		return nil, fmt.Errorf("session-telemetry-otel: settings store 未接线")
	}
	values, _, err := store.namespaces.Read(settingsform.NamespaceSessionTelemetryOTel)
	if err != nil {
		return nil, fmt.Errorf("session-telemetry-otel: 读取设置: %w", err)
	}
	cfg, err := sessiontelemetry.ConfigFromSettings(values, os.Getenv)
	if err != nil {
		return nil, err
	}
	cfg.ApplicationVersion = applicationVersion
	if cfg.Mode == sessiontelemetry.ModeDisabled {
		// DISABLED 不连 product analytics 的 policy/目录也不读，避免无关损坏阻断宿主。
		return sessiontelemetry.NewCoordinator(nil, cfg)
	}
	service, err := analytics.Open(home)
	if err != nil {
		return nil, fmt.Errorf("session-telemetry-otel: %w", err)
	}
	return sessiontelemetry.NewCoordinator(service, cfg)
}

// appendCanonicalFeedback 先追加 canonical 事件，再让 coordinator 释放截至该
// 事件的未交接前缀。旧 JSON 文档仅为既有 API 的兼容投影，不是导出授权来源。
func (a *agentOfficialAPI) appendCanonicalFeedback(requestedSessionID, eventType string, data map[string]any) error {
	path, err := a.agentSessionPath(requestedSessionID)
	if err != nil {
		return fmt.Errorf("%w: 定位 canonical 反馈日志: %v", transport.ErrStatePersistence, err)
	}
	back, err := v4.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%w: 读取 canonical 反馈日志: %v", transport.ErrStatePersistence, err)
	}
	if _, ok := data["sessionId"]; ok {
		// API 接受 session- 前缀别名，canonical data.sessionId 必须等于 header.id。
		data["sessionId"] = back.Header.ID
	}
	logw, err := session.OpenSessionAt(path)
	if err != nil {
		return fmt.Errorf("%w: 打开 canonical 反馈日志: %v", transport.ErrStatePersistence, err)
	}
	appendErr := logw.Append(eventType, data)
	throughSeq := logw.Seq() - 1
	closeErr := logw.Close()
	if appendErr != nil || closeErr != nil {
		return fmt.Errorf("%w: 追加 canonical 反馈: %v", transport.ErrStatePersistence,
			errors.Join(appendErr, closeErr))
	}
	if a.deps.telemetry != nil {
		if err := a.deps.telemetry.CaptureFeedback(path, throughSeq); err != nil {
			// 记录反馈不能被导出诊断反向破坏；失败必须响亮，且不伪称已交付。
			fmt.Fprintf(os.Stderr, "session-telemetry-otel: capture failed: %v\n", err)
		}
	}
	return nil
}
