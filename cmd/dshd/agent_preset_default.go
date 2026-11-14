package main

// agent-preset registry 的 `selectedDefault`：读路径的唯一解析点 + 写路径的存在性校验。
//
// 官方语义（`dsh-agent-preset-registry/src/index.ts:58-82`）：
//
//	get defaultId() { return this.config.selectedDefault.get() ?? this.config.default }
//
// 设置文档里的 `selectedDefault` 决定"后续新建会话用哪个 preset"；本仓的 preset
// 就是 `internal/modes` 里的 mode，所以这个解析结果同时喂给：
//   - `agentPresets.list.isDefault`（官方 UI 用它显示"默认"角标）；
//   - 会话创建时写进 v4 header 的 agentPreset；
//   - `agentPresets.*` 里"没点名 preset 的会话"的回落值。
//
// 为什么读失败/值失效就回落 `modes.DefaultID` 而不是报错：设置文档可能被手改、
// 或被删掉的那个 preset 仍在文档里（官方 `selectedDefault` 是 volatile，读的时候
// 也拿不到"曾经写过的 id"）。**但写路径不回落**——写一个不存在的 id 会当场被拒
// （见 validateAgentPresetSelection），否则就是"报告成功、实际没生效"的静默成功。

import (
	"fmt"
	"path/filepath"
	"strings"

	"dshgo/internal/modes"
	"dshgo/internal/settings"
	"dshgo/internal/settingsform"
)

// selectedAgentPresetDefault 解析"后续会话的默认 preset"。
func selectedAgentPresetDefault(home string) string {
	if strings.TrimSpace(home) == "" {
		return modes.DefaultID
	}
	store := settings.Open(home)
	value, _, err := store.Read(settingsform.NamespaceAgentPreset)
	if err != nil {
		// 文档损坏是 fail-loud 的（Store 自己保留 loadErr），但这里只是"读默认值"：
		// 没有可用的默认值时用宿主内置默认，不让一次设置面故障拦住建会话。
		return modes.DefaultID
	}
	id, _ := value[settingsform.FieldSelectedDefault].(string)
	id = strings.TrimSpace(id)
	if id == "" || !agentPresetKnown(home, id) {
		return modes.DefaultID
	}
	return id
}

// agentPresetKnown 判断一个 id 是不是真实存在的 preset（内置 ∪ `<home>/modes/*.toml`）。
func agentPresetKnown(home, id string) bool {
	if _, err := modes.Resolve(id); err == nil {
		return true
	}
	user, err := modes.LoadDir(filepath.Join(home, "modes"))
	if err != nil {
		return false
	}
	for _, mode := range user {
		if mode.ID == id {
			return true
		}
	}
	return false
}

// validateAgentPresetSelection 是 `agent-preset-registry` 写路径的语义校验。
//
// 它只判 schema 表达不了的那一条：声明的 id 必须真实存在。类型/长度由
// settingsform 的字段校验先跑（所以这里只处理"已经过类型校验"的值，但仍然
// 自己再判一次类型，避免调用顺序变化时静默放行）。
func validateAgentPresetSelection(home string, user map[string]any) error {
	raw, ok := user[settingsform.FieldSelectedDefault]
	if !ok {
		return nil
	}
	id, isString := raw.(string)
	if !isString {
		return &settingsform.Error{
			Code: "invalid_type", Message: "selectedDefault 必须是字符串", Status: 400,
			Hint: "写一个真实存在的 preset id（见 agentPresets.list）",
		}
	}
	id = strings.TrimSpace(id)
	if id == "" {
		// 清空 = 回落宿主默认 preset；这是官方 `selectedDefault` 未设置时的形状。
		return nil
	}
	if !agentPresetKnown(home, id) {
		return &settingsform.Error{
			Code: "invalid_value",
			Message: fmt.Sprintf("preset %q 不存在（内置：%s；用户自建：<home>/modes/*.toml）",
				id, modes.IDs()),
			Hint:   "先用 agentPresets.list 确认 id，再写 selectedDefault",
			Status: 400,
		}
	}
	return nil
}
