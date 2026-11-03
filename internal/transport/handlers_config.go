package transport

import (
	"encoding/json"
	"net/http"
)

// 界面配置在两个 handler 里只被搬动，不被解释。
//
// 协议层认的形状只有一种：**扁平的 key → string 表**。不做嵌套、不做类型推断、
// 不做合并策略——落盘位置与格式由宿主决定（ConfigGet/ConfigSet 背后是 JSON、
// TOML 还是注册表，这一层不知道也不该知道）。一旦这里开始"顺手"重新解读某个
// key，配置就有了两份语义，两边迟早对不上。

// handleConfigGet 是 /api/config.get：把宿主的整份配置原样交出去。
//
// 请求体不用读（界面调它时不带内容），所以这里不解析、也不因为"没 body"报错。
//
// 三种结果分开说：
//   - 没接线 → 501（要宿主补实现，重试没有意义）
//   - 宿主报错 → 500，带上宿主原文（"读不动"和"是空的"必须能分辨）
//   - 成功 → 200；没配置过给空对象 {}，不给 null
func (s *Server) handleConfigGet(w http.ResponseWriter, r *http.Request) {
	if s.opt.ConfigGet == nil {
		writeJSON(w, http.StatusNotImplemented, errorBody("config.get 未接线"))
		return
	}

	values, err := s.opt.ConfigGet()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("读配置失败："+err.Error()))
		return
	}

	// nil map 会被 encoding/json 编成 null，界面那边就得同时处理 null 和 {}。
	// 空配置在这里归一成空对象，让界面只认一种形状。
	if values == nil {
		values = map[string]string{}
	}

	writeJSON(w, http.StatusOK, map[string]any{"values": values})
}

// handleConfigSet 是 /api/config.set：只覆盖请求里给出的 key。
//
// 请求体是 {"values":{"theme":"dark","lang":"zh"}}。没给出的 key 必须保持原样——
// 这是"改若干项"而不是"整份替换"，所以这里不做"清空后重写"。
//
// 四种结果分开说：
//   - 没接线 → 501
//   - 请求体不是 JSON 对象 → 400
//   - values 为 nil 或空 → 400（一次都不改的空调用是调用方的 bug，不该默默成功）
//   - 宿主报错 → 500，带上宿主原文
//
// 成功回 updated=len(values)，让界面能对上"我提交了 3 项，改掉的是不是 3 项"。
func (s *Server) handleConfigSet(w http.ResponseWriter, r *http.Request) {
	if s.opt.ConfigSet == nil {
		writeJSON(w, http.StatusNotImplemented, errorBody("config.set 未接线"))
		return
	}

	var req struct {
		Values map[string]string `json:"values"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("请求体不是 JSON 对象"))
		return
	}
	// nil 与空区分不够：两者都是"这一趟什么都没说要改"，一起拦下。
	if len(req.Values) == 0 {
		writeJSON(w, http.StatusBadRequest, errorBody("缺 values：要给至少一项"))
		return
	}

	if err := s.opt.ConfigSet(req.Values); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("写配置失败："+err.Error()))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"updated": len(req.Values)})
}
