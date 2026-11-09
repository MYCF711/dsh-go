package transport

// 官方 `permissionPresets.catalog`（@deepseek-ai/dsh-permission-presets
// 0.2.2-rc.1，app.asar lib/typert.host.js:6-45）：
//
//	permissionPresets.catalog() → {
//	  options: [{ value, name, description? }],
//	  defaultOptions: [{ value, name, description? }],
//	  defaultPreset: string
//	}
//
// 零参数、direct 调用。options 是当前进程所有可选 preset（贡献顺序），
// defaultOptions 是未来新会话可选的默认 preset，defaultPreset 是未显式配置时
// 的生效默认。
//
// 本仓的真实来源是 `internal/modes`：
//   - 三档 sandbox 预设 read-only / workspace-write / danger-full-access 是
//     `session.setPermission` 与 `agentPresets` 共用的真值（modes.Builtins）；
//   - 名称/描述直接取 modes 的声明，不另写一份文案；
//   - defaultPreset 取默认模式（modes.DefaultID）的 SandboxMode()，即
//     workspace-write；本仓没有"把其它档设为新会话默认"的写入面，所以
//     defaultOptions 只列这个真实生效的默认，不把不可写的档冒充成可选默认。
//
// 不伪造 `auto`/`custom`：官方 auto 只在 Auto review 集成注册后才出现，本仓
// 没有该集成；custom 是"当前值与任何预设都不匹配"的派生显示值，不是目录项。

import (
	"net/http"
	"strings"

	"dshgo/internal/modes"
)

const MethodPermissionPresetsCatalog Method = "permissionPresets.catalog"

// OfficialPermissionPresetOption 是官方 options/defaultOptions 的一项。
type OfficialPermissionPresetOption struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// OfficialPermissionPresetCatalog 是 catalog 结果（字段逐字对齐官方 zod）。
type OfficialPermissionPresetCatalog struct {
	Options        []OfficialPermissionPresetOption `json:"options"`
	DefaultOptions []OfficialPermissionPresetOption `json:"defaultOptions"`
	DefaultPreset  string                           `json:"defaultPreset"`
}

// IsOfficialPermissionPresetsMethod 只认 catalog。
func IsOfficialPermissionPresetsMethod(name string) bool {
	return Method(name) == MethodPermissionPresetsCatalog
}

// ServeOfficialPermissionPresetsAPI 是 `/api/permissionPresets.catalog` 的入口。
func (s *Server) ServeOfficialPermissionPresetsAPI(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/")
	if !IsOfficialPermissionPresetsMethod(name) {
		writeJSON(w, http.StatusNotFound, errorBodyFor("not_found",
			"不是 permissionPresets.catalog"))
		return
	}
	params, err := decodeOfficialParams(r)
	if err != nil {
		writeOfficialError(w, err)
		return
	}
	result, err := officialPermissionPresetsCatalog(params)
	if err != nil {
		writeOfficialError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// permissionPresetOrder 是 sandbox preset 的稳定顺序：
// read-only → workspace-write → danger-full-access（从窄到宽）。
var permissionPresetOrder = []string{
	"read-only",
	"workspace-write",
	"danger-full-access",
}

func officialPermissionPresetsCatalog(p *officialParams) (any, error) {
	if err := p.finish(); err != nil {
		return nil, err
	}
	options := make([]OfficialPermissionPresetOption, 0, len(permissionPresetOrder))
	for _, id := range permissionPresetOrder {
		mode, ok := modes.Lookup(id)
		if !ok {
			// 内置模式缺失是编译期/装配期 bug；不能静默少一项冒充完整目录。
			return nil, officialInternal("permissionPresets.catalog：内置模式 %q 不存在", id)
		}
		options = append(options, OfficialPermissionPresetOption{
			Value:       mode.ID,
			Name:        mode.Name,
			Description: mode.Description,
		})
	}
	defaultMode, ok := modes.Lookup(modes.DefaultID)
	if !ok {
		return nil, officialInternal("permissionPresets.catalog：默认模式 %q 不存在", modes.DefaultID)
	}
	defaultSandbox, err := defaultMode.SandboxMode()
	if err != nil {
		return nil, officialInternal("permissionPresets.catalog：默认模式的 sandbox 无效：%v", err)
	}
	defaultValue := string(defaultSandbox)
	var defaultOption *OfficialPermissionPresetOption
	for i := range options {
		if options[i].Value == defaultValue {
			defaultOption = &options[i]
		}
	}
	if defaultOption == nil {
		return nil, officialInternal("permissionPresets.catalog：默认 sandbox %q 不在 preset 目录里", defaultValue)
	}
	return OfficialPermissionPresetCatalog{
		Options:        options,
		DefaultOptions: []OfficialPermissionPresetOption{*defaultOption},
		DefaultPreset:  defaultValue,
	}, nil
}
