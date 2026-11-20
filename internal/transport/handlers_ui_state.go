package transport

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// 界面状态（uiState.get/set/delete）是宿主 additive 扩展，对应官方 Electron
// `Local Storage`（leveldb）那一层。
//
// # 为什么不是 settings.*
//
// settings.mutate 带 revision 栅栏：界面每点一次"已读"都会推进 revision，用户
// 这时保存设置就会 revision_conflict。界面状态不是设置，也不该占设置的闸门。
//
// # 协议形状
//
// 三个方法都收**裸参数对象**（与外层 /api 契约一致，不套 params），key 逐字是
// 官方 localStorage 的键名（如 `dsh.sidebar-browser.v1.session-<uuid>`）；
// value 是**原样 JSON**，宿主不解释、不改写，读回来逐字节相同。
//
//	get    {key}          → {found:true, value:<原样>&nbsp;} / {found:false}
//	set    {key, value}   → {key, bytes}
//	delete {key}          → {deleted}
//
// 三条都不进 canonical 25：它们不是官方 host 方法，而是"官方把这份状态放在
// 客户端容器里"的宿主替身。

// uiStateParams 是三个方法共同的入参：key 必填，value 只有 set 用。
type uiStateParams struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// decodeUIStateParams 解裸参数对象：只许一个 JSON 对象、不许尾随垃圾、
// 不许 null/数组/标量、不许未知字段。
//
// 比 attachment.put 那种宽松 decode 严一点，理由是这个面是**新**的：现在严格，
// 将来加字段时才有"拒绝静默忽略"的余地；宽松 decode 会让拼错的键静默变成零值。
func decodeUIStateParams(r *http.Request, target *uiStateParams) error {
	dec := json.NewDecoder(r.Body)
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return fmt.Errorf("请求体必须是裸 JSON 参数对象: %w", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("请求体只能有一个 JSON 参数对象")
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return errors.New("请求体必须是裸 JSON 参数对象，不能是 null、数组或标量")
	}
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	if err := strict.Decode(target); err != nil {
		return fmt.Errorf("裸参数含未知字段或类型错误: %w", err)
	}
	return nil
}

// uiStateBadKey 是 key 非法时的统一拒绝（四个字段仍由 errorBodyFor 造）。
func writeUIStateBadKey(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", err.Error()))
}

// requireKey 先挡掉空 key。字符集/长度是**宿主**的事（它决定文件名），
// 这一层只挡"根本没给"，因为那是协议错误、与落点无关。
func (in *uiStateParams) requireKey(w http.ResponseWriter, method string) bool {
	if strings.TrimSpace(in.Key) == "" {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", method+" 需要非空 key"))
		return false
	}
	return true
}

func (s *Server) handleUIStateGet(w http.ResponseWriter, r *http.Request) {
	if s.opt.UIState == nil {
		writeJSON(w, http.StatusNotImplemented, errorBody("uiState.get 未接线"))
		return
	}
	var in uiStateParams
	if err := decodeUIStateParams(r, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", err.Error()))
		return
	}
	if !in.requireKey(w, "uiState.get") {
		return
	}
	entry, found, err := s.opt.UIState.Get(in.Key)
	if err != nil {
		if errors.Is(err, ErrUIStateBadKey) {
			writeUIStateBadKey(w, err)
			return
		}
		writeJSON(w, http.StatusInternalServerError, errorBodyFor("internal", "读界面状态失败："+err.Error()))
		return
	}
	// 没存过是**正常状态**，不是错误：官方 localStorage.getItem 也是回 null。
	// 用 200 + found:false 表达，客户端不必把"第一次读"当异常处理。
	if !found {
		writeJSON(w, http.StatusOK, map[string]any{"found": false})
		return
	}
	// 值按**原样 JSON** 交出去，不 base64（[]byte 会被 encoding/json 编成
	// base64，客户端就得自己再解一层，且丢掉"宿主不解释"这个性质）。
	// 文件不是合法 JSON 只可能是被外部写坏：报出来，不假装读过。
	if !json.Valid(entry.Value) {
		writeJSON(w, http.StatusInternalServerError,
			errorBodyFor("encode_failure", "界面状态 "+in.Key+" 的序列化失败：磁盘上的值不是合法 JSON"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"found": true, "value": json.RawMessage(entry.Value)})
}

func (s *Server) handleUIStateSet(w http.ResponseWriter, r *http.Request) {
	if s.opt.UIState == nil {
		writeJSON(w, http.StatusNotImplemented, errorBody("uiState.set 未接线"))
		return
	}
	var in uiStateParams
	if err := decodeUIStateParams(r, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", err.Error()))
		return
	}
	if !in.requireKey(w, "uiState.set") {
		return
	}
	raw := bytes.TrimSpace(in.Value)
	// 空值不是状态：要清掉一份状态就用 uiState.delete（两条路各自一个语义，
	// 免得"写了个空值"和"没有这份状态"在磁盘上长得不一样）。
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request",
			"uiState.set 需要非空 value（清状态请用 uiState.delete）"))
		return
	}
	if err := s.opt.UIState.Set(in.Key, raw); err != nil {
		if errors.Is(err, ErrUIStateBadKey) {
			writeUIStateBadKey(w, err)
			return
		}
		writeJSON(w, http.StatusInternalServerError, errorBodyFor("internal", "写界面状态失败："+err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": in.Key, "bytes": len(raw)})
}

func (s *Server) handleUIStateDelete(w http.ResponseWriter, r *http.Request) {
	if s.opt.UIState == nil {
		writeJSON(w, http.StatusNotImplemented, errorBody("uiState.delete 未接线"))
		return
	}
	var in uiStateParams
	if err := decodeUIStateParams(r, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", err.Error()))
		return
	}
	if !in.requireKey(w, "uiState.delete") {
		return
	}
	deleted, err := s.opt.UIState.Delete(in.Key)
	if err != nil {
		if errors.Is(err, ErrUIStateBadKey) {
			writeUIStateBadKey(w, err)
			return
		}
		writeJSON(w, http.StatusInternalServerError, errorBodyFor("internal", "删界面状态失败："+err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted})
}
