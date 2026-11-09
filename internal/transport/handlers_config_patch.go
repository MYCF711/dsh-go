package transport

// `config.patch.*` 是配置树的 additive 面（官方 `config.get|set` 只搬扁平 kv，
// 没有嵌套、没有 revision）。两个方法：
//
//   - `config.patch.get`：读当前树 + revision + 扁平投影（旧客户端仍能解析）；
//   - `config.patch.apply`：带 expectedRevision 原子写一批 op。
//
// 为什么新增而不是改 `config.get|set`：canonical 25 的形状被快照钉死，改了就是
// 破坏契约；而"两个人同时改配置后写覆盖先写"这个坑必须由 revision 解决。
//
// 接线方式与既有 additive 适配器一致（`Options.PluginHandler` / `SettingsFormHandler`
// 那一路）：本类型自己是一个 http.Handler，宿主在 cookie 认证之后按
// `config.patch.` 前缀分派进来即可——不需要改 `handleAPI` 的 switch。
// 具体三行接线写在 `$S/WIRING.md`。

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"dshgo/internal/config"
)

const (
	// MethodConfigPatchGet 读配置树。
	MethodConfigPatchGet Method = "config.patch.get"
	// MethodConfigPatchApply 原子提交一批补丁。
	MethodConfigPatchApply Method = "config.patch.apply"
)

// maxConfigPatchBody 是一次补丁请求的上限。配置是人改的：256 条 op 的文本
// 也远小于 1 MiB，超过它一定是调用方出了问题。
const maxConfigPatchBody = 1 << 20

// ConfigPatchMethods 是独立 additive 扩展面，绝不加入 canonical 25。
func ConfigPatchMethods() []Method {
	return []Method{MethodConfigPatchGet, MethodConfigPatchApply}
}

// ConfigPatchDescriptors 是这两个方法的描述符（typert 层）。
//
// required 在这里是**真的**必填：`expectedRevision` 不给就没法判断你读到的是
// 哪一版，`ops` 不给就是空调用——两者都不是"合理默认"，直接拒。
func ConfigPatchDescriptors() []Descriptor {
	return []Descriptor{
		{
			Method:  MethodConfigPatchGet,
			Summary: "读配置树、revision 与扁平投影（无参数）",
			Source:  "additive:config.patch",
			Params:  []ParamSpec{},
		},
		{
			Method:  MethodConfigPatchApply,
			Summary: "带 expectedRevision 原子提交一批配置补丁",
			Source:  "additive:config.patch",
			Result:  "revision / applied / values / flat",
			Params: []ParamSpec{
				{Name: "expectedRevision", Kind: ParamInteger, Required: true},
				{Name: "ops", Kind: ParamArray, Required: true, Items: ParamObject, MinLength: 1},
				{Name: "author", Kind: ParamString},
			},
		},
	}
}

// ConfigPatchHandler 是 config.patch.* 的宿主适配器。
type ConfigPatchHandler struct {
	store       *config.Store
	descriptors *Registry
	// descriptorErr 只在"内置描述符自己都不合法"时非 nil。
	// 那种情况回 500：它是宿主内部不变量被破坏，不是调用方的错。
	descriptorErr error
}

// NewConfigPatchHandler 造一个适配器。
//
// store 为 nil = 没有接线：两个方法都回 501（不是 500，也不是静默成功）。
// descriptors 为 nil 时用 ConfigPatchDescriptors() 自建一份。
func NewConfigPatchHandler(store *config.Store, descriptors *Registry) *ConfigPatchHandler {
	handler := &ConfigPatchHandler{store: store, descriptors: descriptors}
	if descriptors == nil {
		registry := NewRegistry()
		if err := registry.RegisterAll(ConfigPatchDescriptors()); err != nil {
			handler.descriptorErr = err
			return handler
		}
		handler.descriptors = registry
	}
	return handler
}

// ServeHTTP 按路径分派。路径形状与 /api 一致：`/api/config.patch.apply`。
func (h *ConfigPatchHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/")
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	if h.store == nil {
		writeJSON(w, http.StatusNotImplemented, errorBody(name+" 未接线"))
		return
	}
	if h.descriptorErr != nil {
		writeJSON(w, http.StatusInternalServerError, errorBodyFor("internal", "config.patch 描述符不合法："+h.descriptorErr.Error()))
		return
	}

	switch Method(name) {
	case MethodConfigPatchGet:
		h.serveGet(w)
	case MethodConfigPatchApply:
		h.serveApply(w, r)
	default:
		writeJSON(w, http.StatusNotImplemented, errorBody(name+" 未接线"))
	}
}

func (h *ConfigPatchHandler) serveGet(w http.ResponseWriter) {
	tree := h.store.Tree()
	writeJSON(w, http.StatusOK, map[string]any{
		"revision": h.store.Revision(),
		"values":   tree.Snapshot(),
		"flat":     tree.Flatten(),
	})
}

func (h *ConfigPatchHandler) serveApply(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxConfigPatchBody+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "读请求体失败："+err.Error()))
		return
	}
	if len(raw) > maxConfigPatchBody {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "请求体超过 1 MiB（配置补丁不该这么大）"))
		return
	}

	// 先按描述符校验形状：类型错、缺必填、未知字段都在这里就拦下，
	// 不要等 config 层再报一个"op 不合法"——两者的下一步完全不同。
	if _, err := h.descriptors.Decode(string(MethodConfigPatchApply), raw); err != nil {
		var paramErr *ParamError
		if errors.As(err, &paramErr) {
			writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", paramErr.Error()))
			return
		}
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", err.Error()))
		return
	}

	var in struct {
		ExpectedRevision *int64           `json:"expectedRevision"`
		Ops              []config.PatchOp `json:"ops"`
		Author           string           `json:"author"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "补丁体不是合法 JSON："+err.Error()))
		return
	}
	if in.ExpectedRevision == nil {
		// 描述符已经拦过一遍；这里是"描述符被换掉"时的第二道闸。
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "缺 expectedRevision：先 config.patch.get 拿到当前 revision"))
		return
	}

	result, err := h.store.Apply(config.Patch{
		ExpectedRevision: *in.ExpectedRevision,
		Ops:              in.Ops,
		Author:           in.Author,
	})
	if err != nil {
		h.writeApplyError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"revision": result.Revision,
		"applied":  result.Applied,
		"values":   result.Tree.Snapshot(),
		"flat":     result.Tree.Flatten(),
	})
}

// writeApplyError 把 config 层的三类失败分开：
//
//	409 revision_conflict —— 你读到的是旧版本（带服务端 revision，客户端立刻重读）
//	400 bad_request       —— 补丁内容不合法（op/path/值）
//	500 internal          —— 落盘/内部失败（内存没前进，磁盘还是旧版本）
//
// 混成一个码的后果是客户端不知道"该重读还是该改参数"，只能两个都试或都不试。
func (h *ConfigPatchHandler) writeApplyError(w http.ResponseWriter, err error) {
	var conflict *config.RevisionConflict
	if errors.As(err, &conflict) {
		writeJSON(w, http.StatusConflict, errorBodyFor("revision_conflict", err.Error(), map[string]any{
			"revision":         conflict.Actual,
			"expectedRevision": conflict.Expected,
		}))
		return
	}
	if isConfigPatchRejection(err) {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", err.Error()))
		return
	}
	writeJSON(w, http.StatusInternalServerError, errorBodyFor("internal", err.Error()))
}

func isConfigPatchRejection(err error) bool {
	for _, sentinel := range []error{
		config.ErrInvalidPath,
		config.ErrNoSuchPath,
		config.ErrNotObject,
		config.ErrUnknownOp,
		config.ErrInvalidValue,
		config.ErrInvalidKey,
	} {
		if errors.Is(err, sentinel) {
			return true
		}
	}
	return false
}
