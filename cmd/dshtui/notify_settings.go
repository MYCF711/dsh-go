package main

// TUI 自己的设置文档：`taskNotifications`（三档）与 `notificationMethod`（四档）。
//
// 为什么需要一个文档而不是两个环境变量：这两项是**用户设置**，默认值必须与
// 上游一致（unfocused / auto），要能持久化、能在运行中改并立即生效（上游
// "Applies immediately"）。上游把 `/settings` 的用户层写在
// `~/.dsh-tui/settings.json`（`{version, values, imported}` 形状）；本仓是
// 独立产品（DSH TUI GO），**不写**上游那个路径——同一个文件被两个程序读写，
// 迟早互相覆盖。所以用同构的文档、独立的目录：`~/.dsh-go-tui/settings.json`。
//
// 形状照抄上游（tuiSettingsFile.js）：`{"version":1,"values":{...}}`；
// 坏文件读成空层并**明确告警**（不静默把用户的设置扔掉），写入走
// "临时文件 + rename"的原子替换，权限 0600。

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 设置键名。与上游 `/settings` 的键同名同值域。
const (
	settingTaskNotifications  = "taskNotifications"
	settingNotificationMethod = "notificationMethod"
)

// settingsFileEnv 允许把设置文件指到别处（测试、真机取证、多实例各用一份）。
const settingsFileEnv = "DSH_TUI_SETTINGS_FILE"

// tuiSettings 是这两项设置的值。
type tuiSettings struct {
	condition notificationCondition
	method    notificationMethod
}

// defaultTUISettings 是上游的默认档：仅失焦时通知、投递方式自动。
func defaultTUISettings() tuiSettings {
	return tuiSettings{condition: notifWhenUnfocused, method: notifMethodAuto}
}

// settingsPath 返回设置文档路径：环境变量优先，否则 `~/.dsh-go-tui/settings.json`。
func settingsPath(getenv func(string) string) string {
	if getenv != nil {
		if configured := strings.TrimSpace(getenv(settingsFileEnv)); configured != "" {
			return configured
		}
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".dsh-go-tui", "settings.json")
}

// settingsDocument 是磁盘上的文档形状（与上游同构）。
type settingsDocument struct {
	Version int               `json:"version"`
	Values  map[string]string `json:"values"`
}

// loadTUISettings 读设置文档。返回的设置永远可用（坏值回落到默认），
// 同时返回**告警列表**——调用方必须把告警显示出来，不许静默。
func loadTUISettings(path string) (tuiSettings, []string) {
	settings := defaultTUISettings()
	if path == "" {
		return settings, []string{"找不到用户目录：通知设置用默认值（" + string(settingTaskNotifications) + "=unfocused，" + settingNotificationMethod + "=auto）"}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return settings, nil // 首次运行没有文档是正常的
		}
		return settings, []string{"读设置失败（" + path + "）：" + err.Error() + "；用默认值"}
	}
	var doc settingsDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return settings, []string{"设置文件不是合法 JSON（" + path + "）：" + err.Error() + "；用默认值"}
	}
	var warnings []string
	if v, ok := doc.Values[settingTaskNotifications]; ok {
		parsed, err := parseNotificationCondition(v)
		if err != nil {
			warnings = append(warnings, err.Error()+"；用默认 unfocused")
		} else {
			settings.condition = parsed
		}
	}
	if v, ok := doc.Values[settingNotificationMethod]; ok {
		parsed, err := parseNotificationMethod(v)
		if err != nil {
			warnings = append(warnings, err.Error()+"；用默认 auto")
		} else {
			settings.method = parsed
		}
	}
	return settings, warnings
}

// saveTUISettings 原子写回文档（临时文件 + rename，0600）。
func saveTUISettings(path string, settings tuiSettings) error {
	if path == "" {
		return errors.New("没有可写的设置路径")
	}
	doc := settingsDocument{Version: 1, Values: map[string]string{
		settingTaskNotifications:  string(settings.condition),
		settingNotificationMethod: string(settings.method),
	}}
	payload, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("建设置目录失败（%s）: %w", dir, err)
	}
	temporary, err := os.CreateTemp(dir, ".settings-*.tmp")
	if err != nil {
		return fmt.Errorf("建设置临时文件失败: %w", err)
	}
	temporaryName := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryName)
	}
	if _, err := temporary.Write(payload); err != nil {
		cleanup()
		return fmt.Errorf("写设置失败: %w", err)
	}
	if err := temporary.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("改设置权限失败: %w", err)
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryName)
		return fmt.Errorf("关设置临时文件失败: %w", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		_ = os.Remove(temporaryName)
		return fmt.Errorf("替换设置文件失败（%s）: %w", path, err)
	}
	return nil
}

// commandSettings 处理 `/settings`：
//
//	/settings                       → 显示两项设置与此刻的解析结果
//	/settings <key>=<value>         → 校验、生效、持久化；非法值明确报错
//
// 立即生效是上游写明的要求（"Applies immediately"）；`jumpBack` 明确不在本版
// （它写 HKCU 注册表、要原生 toast 路径，单独立项），所以给了就明确拒绝，
// 不装作设上了。
func (a *app) commandSettings(args []string) {
	if len(args) == 0 {
		a.printNotificationSettings()
		return
	}
	assignment := strings.Join(args, " ")
	key, value, ok := strings.Cut(assignment, "=")
	if !ok {
		a.err = "用法：/settings taskNotifications=unfocused|always|off · /settings notificationMethod=auto|osc9|bel|windows"
		return
	}
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	switch key {
	case settingTaskNotifications:
		parsed, err := parseNotificationCondition(value)
		if err != nil {
			a.err = err.Error()
			return
		}
		a.notify.condition = parsed
		a.notify.notifier.configure(a.notify.condition, a.notify.method)
	case settingNotificationMethod:
		parsed, err := parseNotificationMethod(value)
		if err != nil {
			a.err = err.Error()
			return
		}
		a.notify.method = parsed
		a.notify.notifier.configure(a.notify.condition, a.notify.method)
	case "jumpBack":
		a.err = "jumpBack 本版未实现（要原生 toast 路径与 HKCU 注册表，单独立项）：本次没有改动任何设置"
		return
	default:
		a.err = "还不支持设置项 " + key + "（现在只有 " + settingTaskNotifications + " 与 " + settingNotificationMethod + "）"
		return
	}
	a.settings.condition = a.notify.condition
	a.settings.method = a.notify.method
	if err := saveTUISettings(a.settingsPath, a.settings); err != nil {
		a.err = "设置已生效，但写盘失败：" + err.Error() + "（重启后会回到旧值）"
		return
	}
	a.push("已设置 " + key + "=" + value + "（立即生效，已写入 " + a.settingsPath + "）")
	a.printNotificationSettings()
}

// printNotificationSettings 把当前设置、解析出来的投递路径与相关前提说清楚。
//
// 为什么把"解析结果"也打出来：设置值是"auto"，但真正走哪条路取决于终端
// 与平台（Windows 本地是 tint/toast、SSH 走终端协议）。只说 auto，人没法判断
// "为什么我的通知没出现"。
func (a *app) printNotificationSettings() {
	condition := a.notify.condition
	if condition == "" {
		condition = notifWhenUnfocused
	}
	method := a.notify.method
	if method == "" {
		method = notifMethodAuto
	}
	a.push("设置：" + settingTaskNotifications + "=" + string(condition) +
		"（unfocused 仅失焦时 / always 总是 / off 关闭）")
	a.push("      " + settingNotificationMethod + "=" + string(method) +
		"（auto / osc9 / bel / windows）")
	if a.notify.notifier != nil {
		transport := a.notify.notifier.resolvedTransport()
		a.push("当前解析：" + string(transport) + "；stdout 是终端=" + yesNo(a.notify.notifier.facts.tty) +
			"；终端焦点=" + focusDescription(a.notify))
		if transport == transportWindows {
			a.push("！" + windowsToastNote)
		}
		if os.Getenv("TMUX") != "" {
			a.push("tmux：OSC 通知需 set -g allow-passthrough on；焦点需 set -g focus-events on")
		}
	}
	if a.settingsPath != "" {
		a.push("设置文件：" + a.settingsPath)
	}
}

// resolvedTransport 是按当前设置与终端事实解析出来的投递路径（给 /settings 显示用）。
func (n *terminalNotifier) resolvedTransport() notificationTransport {
	termProgram := n.facts.getenv("TERM_PROGRAM")
	term := n.facts.getenv("TERM")
	protocol := notificationProtocol(termProgram, term, n.facts.xtversion)
	return resolveNotificationTransport(n.method, n.facts.platform, isSSHSession(n.facts.getenv), protocol)
}

func focusDescription(state notifyState) string {
	if !state.focusKnown {
		return "未知（按聚焦处理；这类终端要通知请选 always）"
	}
	return yesNo(state.focused)
}

func yesNo(v bool) string {
	if v {
		return "是"
	}
	return "否"
}
