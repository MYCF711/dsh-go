package main

import (
	"fmt"
	"strings"

	"dshgo/internal/modes"
	"dshgo/internal/spi"
)

// systemPromptForMode 只给声明了 persona 的 preset 加首段。
// 未声明时原字符串逐字节返回；ACP 的不同会话各取自己的返回值，不写共享 r.system。
func systemPromptForMode(base string, mode modes.Mode, model string) string {
	prefix := mode.PersonaPrefix(model)
	if prefix == "" {
		return base
	}
	if base == "" {
		return prefix
	}
	return prefix + "\n\n" + base
}

// cmdRunModeRegistry 是 run 的增量投影：本任务只改变 cordis 的工具可见性。
// 其他 preset 继续走原来的 reg 指针，保证它们原有请求字节不被这次补齐顺带改写。
func cmdRunModeRegistry(reg spi.ToolRegistry, mode modes.Mode) spi.ToolRegistry {
	if mode.ID == modes.CordisID {
		return modes.Filter(reg, mode)
	}
	return reg
}

// cmdRunExecutionView 给 guardedRunner 的强制准入视图。非 cordis 返回 nil，
// 保留它原先的 reg 回退行为；cordis 则与模型声明共用同一张过滤表。
func cmdRunExecutionView(reg spi.ToolRegistry, mode modes.Mode) spi.ToolRegistry {
	if mode.ID == modes.CordisID {
		return modes.Filter(reg, mode)
	}
	return nil
}

// resolveCmdRunMode 找本轮会话的 preset：显式覆盖优先，其次 v4 header；
// 新会话使用当时选定的默认值。只有 cordis 需要取编译期组合；其他 id 原样
// 返回，避免这次增量让既有 standard / 用户自建 preset 多一次解析或改变行为。
func resolveCmdRunMode(home, resumeSession string) (modes.Mode, error) {
	preset := selectedAgentPresetDefault(home)
	if strings.TrimSpace(resumeSession) != "" {
		got, err := sandboxPresetIDForSession(home, resumeSession)
		if err != nil {
			return modes.Mode{}, err
		}
		preset = got
	}
	if preset != modes.CordisID {
		return modes.Mode{ID: preset}, nil
	}
	mode, err := modes.Resolve(preset)
	if err != nil {
		return modes.Mode{}, fmt.Errorf("cmdRun 会话 preset %q 无法解析: %w", preset, err)
	}
	return mode, nil
}
