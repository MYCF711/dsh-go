package transport

// 官方 `pluginInventory.list`（@deepseek-ai/dsh-host-plugin-inventory
// 0.2.2-rc.1，app.asar lib/typert.host.js:6-32）：
//
//	pluginInventory.list() → {
//	  managementAvailable?: boolean,
//	  entries: [{
//	    entryId, moduleName,
//	    meta?: { title?, description?, icon?, error? },
//	    enabled, fiberPhase: null|failed|active|pending|loading|unloading,
//	    diagnostic?
//	  }],
//	  agentPresets?: [...]   // 只有部署组合了 agent-preset roster 时才出现
//	}
//
// 零参数、direct 调用。官方读的是 Cordis Loader 的**当前非 group 条目**；
// 本仓对应的真实来源是 `internal/pluginmgr`：
//   - 已安装条目（含 disabled）来自 Manager.Bundles()/state.json；
//   - fiberPhase 只按"当前进程真的装载了/没有"给 active/null，
//     不伪造 pending/loading/failed（本仓没有这几态的真实来源）；
//   - 装载器与 state.json 版本不一致时给 diagnostic（下一次 reload 会收敛）。
//
// `agentPresets` 本仓**不产出**：官方只在宿主组合 agent-preset roster 时给；
// 本仓的 modes 不是 Cordis 插件组合 roster，不能拿它的行冒充官方 composition。
// 官方把它标为 optional，所以省略是合法且诚实的，而不是空数组冒充"没有"。
// `meta` 同样省略：插件包 manifest 没有官方 title/description/icon 元数据。

import (
	"net/http"
	"strings"
)

// MethodPluginInventoryList 是官方 zero-arg 读方法。
const MethodPluginInventoryList Method = "pluginInventory.list"

// OfficialPluginInventoryMeta 是官方 meta 对象（本仓当前不产出）。
type OfficialPluginInventoryMeta struct {
	Title       any    `json:"title,omitempty"`
	Description any    `json:"description,omitempty"`
	Icon        string `json:"icon,omitempty"`
	Error       string `json:"error,omitempty"`
}

// OfficialPluginInventoryEntry 是 entries 的一项。
type OfficialPluginInventoryEntry struct {
	EntryID    string                       `json:"entryId"`
	ModuleName string                       `json:"moduleName"`
	Meta       *OfficialPluginInventoryMeta `json:"meta,omitempty"`
	Enabled    bool                         `json:"enabled"`
	FiberPhase *string                      `json:"fiberPhase"`
	Diagnostic string                       `json:"diagnostic,omitempty"`
}

// OfficialPluginInventory 是 pluginInventory.list 的结果。
type OfficialPluginInventory struct {
	ManagementAvailable *bool                          `json:"managementAvailable,omitempty"`
	Entries             []OfficialPluginInventoryEntry `json:"entries"`
}

// IsOfficialPluginInventoryMethod 只认 list。
func IsOfficialPluginInventoryMethod(name string) bool {
	return Method(name) == MethodPluginInventoryList
}

// ServeOfficialPluginInventoryAPI 是 `/api/pluginInventory.list` 的入口。
//
// 宿主实现挂在 OfficialPluginManagerAPI.PluginInventoryList 上：它与
// pluginManager.listPlugins 共用同一份 Manager/Loader，不是第二份状态。
func (s *Server) ServeOfficialPluginInventoryAPI(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/")
	if !IsOfficialPluginInventoryMethod(name) {
		writeJSON(w, http.StatusNotFound, errorBodyFor("not_found", "不是 pluginInventory.list"))
		return
	}
	params, err := decodeOfficialParams(r)
	if err != nil {
		writeOfficialError(w, err)
		return
	}
	result, err := s.officialPluginInventoryList(params)
	if err != nil {
		writeOfficialError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) officialPluginInventoryList(p *officialParams) (any, error) {
	if err := p.finish(); err != nil {
		return nil, err
	}
	api, ok := s.officialPluginManagerAPI()
	if !ok || api.PluginInventoryList == nil {
		return nil, officialNotImplemented(
			"pluginInventory.list 未接线：宿主没有挂接已安装插件清单（OfficialPluginManagerAPI.PluginInventoryList）")
	}
	return api.PluginInventoryList()
}
