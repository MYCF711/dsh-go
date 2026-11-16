package main

import (
	"fmt"
	"math"
	"os"

	"dshgo/internal/sessionlog"
	"dshgo/internal/settings"
	"dshgo/internal/settingsform"
)

// sessionLogConfigLoader 让 enabled/maxBytes 在下一次请求即生效，与官方
// volatile 配置一致。命名空间还没写过时返回官方默认；损坏或类型错误只省略
// 本次贡献，由 llm.OnExtensionError 报告，模型请求本身继续。
func sessionLogConfigLoader(home string) sessionlog.ConfigLoader {
	return func() (sessionlog.Config, error) {
		config := sessionlog.DefaultConfig()
		values, _, err := settings.Open(home).Read(settingsform.NamespaceSessionLogDeepSeek)
		if err != nil {
			return sessionlog.Config{}, err
		}
		if raw, exists := values[settingsform.FieldSessionLogEnabled]; exists {
			enabled, ok := raw.(bool)
			if !ok {
				return sessionlog.Config{}, fmt.Errorf("session-log-deepseek.enabled 必须是 bool，实际 %T", raw)
			}
			config.Enabled = enabled
		}
		if raw, exists := values["maxBytes"]; exists {
			number, ok := raw.(float64)
			if !ok || number < 1 || number > float64(math.MaxInt) || number != math.Trunc(number) {
				return sessionlog.Config{}, fmt.Errorf("session-log-deepseek.maxBytes 必须是正整数，实际 %v", raw)
			}
			config.MaxBytes = int(number)
		}
		return config, nil
	}
}

// reportLLMExtensionError 让"只省略贡献、模型请求继续"仍然可诊断。
func reportLLMExtensionError(err error) { fmt.Fprintln(os.Stderr, err) }

// registerSessionLogExtension 是 base 插件 #8 的显式装配点（R2，不用 init）。
// 注册失败只让该字段缺席；调用方必须打印诊断，不能静默成功。
func registerSessionLogExtension(home string) error {
	service := sessionlog.New(home, sessionLogConfigLoader(home))
	service.OnWarning = func(message string) { fmt.Fprintln(os.Stderr, message) }
	return service.Register()
}
