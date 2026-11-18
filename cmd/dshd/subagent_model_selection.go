package main

// 子代理「模型选择」的用户设置 → 会话 durable 授权。
//
// # 官方消费方（rc.2，随包 TypeScript）
//
// `dsh-tool-subagent/src/model-selection-settings.ts:54-76` 的 `[Service.init]`：
//
//   - 注入 `sessionProjections` / `sessions`，先注册
//     `subagentModelSelectionProjectionDefinition`（host-only 投影）；
//   - `agent/created` 时：
//       · 已经有策略（投影非 null）→ 什么都不做；
//       · 子会话（`origin === 'subagent'`）→ 从父会话的投影**继承** allowedModels；
//       · 全新顶层会话（`firstLiveSeq === 0` 且 seq0 不是 `session/end-seed`）→
//         读用户设置：`enabled ? allowedModels : undefined`；
//       · 拿到 allowedModels 才 `target.append('subagent/model-selection-policy',
//         { allowedModels })`——**在第一次模型请求之前**。
//   - `current()`（:78-90）：读设置 → `assertAllowedModelRoutes` → 若
//     `enabled && allowedModels.length === 0` 抛
//     `enabled subagent model selection requires at least one allowed model`。
//
// # 本仓落点
//
// 本仓没有 cordis `agent/created` 事件；会话在四个地方被真正创建：serve 的
// `session.create`（serve_create.go）、run 的新会话（main.go 的 cmdRun）、
// ACP 的 openSession（acp.go）、以及子代理 durable 子会话
// （internal/agent/child_session.go）。前三处 = 官方的新顶层会话，第四处 = 子会话
// 继承。四个点都调本文件的两个函数，判据与写入内容只有这一份。
//
// # 设置文档的读取纪律
//
// 这里**不**用 `settingsSection`（那会把读盘/校验错误吞成 ok=false）：用户写了
// `enabled=true` 却因为文档损坏静默不生效，等于界面保存成功而行为不变。
// 读失败、形状不对、enabled 但清单为空，全部响亮失败——它们在真正建会话之前发生，
// 所以失败是"这一轮没开始"，不是"跑了一半才发现"。

import (
	"fmt"
	"strings"

	"dshgo/internal/session"
	"dshgo/internal/settings"
	"dshgo/internal/settingsform"
)

// subagentModelSelectionSettings 是官方 `SubagentModelSelectionSettings`
// （model-selection-settings.ts:30-36）的 Go 形状。
type subagentModelSelectionSettings struct {
	// Enabled 是"新顶层会话是否获得模型选择授权"。
	Enabled bool
	// AllowedModels 是精确的 provider/model 路由清单。
	AllowedModels []session.AllowedModelRoute
}

// subagentModelSelectionFromSettings 读 `subagent-model-selection-settings` 命名空间。
//
// 官方 Config 是 `enabled: z.boolean().default(false).volatile()` 与
// `allowedModels: z.array(AllowedModelRouteSchema).default([]).volatile()`
// （model-selection-settings.ts:52-55）——所以"没有这份文档"= 官方 shipped 默认
// = 关闭 + 空清单；只有**用户显式写过**的字段才参与（与 settings_effective.go
// 的三条判据同源，base 默认值不冒充用户选择）。
func subagentModelSelectionFromSettings(home string) (subagentModelSelectionSettings, error) {
	if strings.TrimSpace(home) == "" {
		return subagentModelSelectionSettings{}, fmt.Errorf(
			"读 %s 需要会话根目录（home 为空，设置文档没有落点）",
			settingsform.NamespaceSubagentModelSelection)
	}
	// 直接读命名空间的 user 段（`settings.Open(home).Read` 对"没有这个 ns"返回
	// 空 map + 无错误 = 官方 shipped 默认）。**不**用 settingsform 的 Section：
	// `allowedModels` 在声明里是 schemaOnly（对象数组装不进 L1 标量表），
	// Section.Fields 里没有它；而这里要的正是它的原始值。
	user, _, err := settings.Open(home).Read(settingsform.NamespaceSubagentModelSelection)
	if err != nil {
		return subagentModelSelectionSettings{}, fmt.Errorf("读设置 %s 失败: %w",
			settingsform.NamespaceSubagentModelSelection, err)
	}
	var current subagentModelSelectionSettings
	if raw, ok := user[settingsform.FieldSubagentModelSelectionEnabled]; ok && raw != nil {
		enabled, isBool := raw.(bool)
		if !isBool {
			return subagentModelSelectionSettings{}, fmt.Errorf(
				"设置 %s.%s 必须是布尔值，收到 %T",
				settingsform.NamespaceSubagentModelSelection,
				settingsform.FieldSubagentModelSelectionEnabled, raw)
		}
		current.Enabled = enabled
	}
	if raw, ok := user[settingsform.FieldAllowedModels]; ok && raw != nil {
		routes, err := session.ParseAllowedModelRoutes(raw)
		if err != nil {
			return subagentModelSelectionSettings{}, fmt.Errorf("设置 %s.%s 不合法: %w",
				settingsform.NamespaceSubagentModelSelection, settingsform.FieldAllowedModels, err)
		}
		current.AllowedModels = routes
	}
	if current.Enabled && len(current.AllowedModels) == 0 {
		// 官方 current() 的原话（model-selection-settings.ts:84-86）。
		return subagentModelSelectionSettings{}, fmt.Errorf(
			"enabled subagent model selection requires at least one allowed model")
	}
	return current, nil
}

// appendSubagentModelSelectionPolicyOnCreate 在**新顶层会话**创建时写 durable 授权。
//
// 设置关闭 = 不写任何东西（官方 `allowedModels = current.enabled ? ... : undefined`）：
// 没有策略的会话，显式选择只能点名父路由——那是官方语义，不是本仓的降级。
//
// **调用顺序有讲究**：先 `subagentModelSelectionFromSettings` 校验、再建会话。
// 反过来会在设置非法时留下一份没有授权记录的空会话（既不能续问也没人清）。
func appendSubagentModelSelectionPolicyOnCreate(log *session.Log, current subagentModelSelectionSettings) error {
	if !current.Enabled {
		return nil
	}
	if err := session.AppendSubagentModelSelectionPolicy(log, current.AllowedModels); err != nil {
		return fmt.Errorf("把子代理模型选择授权写进会话日志失败: %w", err)
	}
	return nil
}

// inheritSubagentModelSelectionPolicy 已并入 internal/session（子会话由
// internal/agent/child_session.go 创建，那一层不能 import cmd/dshd）；
// 同一个实现见 session.InheritSubagentModelSelectionPolicy。
