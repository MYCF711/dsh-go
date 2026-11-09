package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

const (
	MethodContextUsage Method = "context.usage"
	MethodMCPList      Method = "mcp.list"
	MethodMCPReconnect Method = "mcp.reconnect"
	MethodMCPToggle    Method = "mcp.toggle"
	MethodSideQuery    Method = "side.query"
	MethodSideCancel   Method = "side.cancel"
	MethodSideClose    Method = "side.close"
)

var (
	ErrNoSuchMCPServer = errors.New("mcp server not found")
	ErrNoSuchSideQuery = errors.New("side query not found")
	ErrSideQueryExists = errors.New("side query requestId already active")
)

// ContextUsageView 与 W141 中立视图同形；指针切片区分“没有真实计量”与真实空数组。
//
// GO.6 起补上官方 `context.measurement.*` 三组口径（**只加不改**：既有键一个都
// 没动，新键全部 omitempty，旧客户端/旧快照照旧解析）。三个新字段的口径见
// internal/session/usage_projection.go 的注释，源头是官方
// `@deepseek-ai/dsh-token-meter/lib/types/usage-projection.js`。
type ContextUsageView struct {
	Used int `json:"used"`
	Max  int `json:"max,omitempty"`
	// PressureTokens 是官方 `context.measurement.pressureTokens`：
	// 未命中输入 + 缓存读 + 缓存写（prompt 侧三桶，**不含输出**）。
	// 0 = 没有实测锚点（不是"压力是 0"）。
	PressureTokens int `json:"pressureTokens,omitempty"`
	// ProjectedTokens 是官方 `context.measurement.projectedTokens`：
	// max(0, pressure + surface − sampledSurface)，预测**下一次**请求的占用。
	// 0 = 没有实测锚点。
	ProjectedTokens int `json:"projectedTokens,omitempty"`
	// UsageBuckets 是官方 `context.measurement.usageBuckets` 四桶投影；
	// nil = 没有实测锚点（不许用四个 0 冒充"实测全 0"）。
	UsageBuckets *ContextUsageBuckets `json:"usageBuckets,omitempty"`

	Categories []ContextCategory  `json:"categories"`
	Sections   *[]ContextItemView `json:"sections,omitempty"`
	Files      *[]ContextFileView `json:"files,omitempty"`
	Skills     *[]ContextItemView `json:"skills,omitempty"`
	Tools      *[]ContextToolView `json:"tools,omitempty"`
}

// ContextUsageBuckets 是官方 `usage.fields.projectionShape` 的四个桶。
type ContextUsageBuckets struct {
	UncachedInputTokens int `json:"uncachedInputTokens"`
	OutputTokens        int `json:"outputTokens"`
	CacheReadTokens     int `json:"cacheReadTokens"`
	CacheWriteTokens    int `json:"cacheWriteTokens"`
}

type ContextCategory struct {
	Name   string `json:"name"`
	Tokens int    `json:"tokens"`
	Kind   string `json:"kind"`
}

type ContextItemView struct {
	Name   string `json:"name"`
	Tokens int    `json:"tokens"`
}

type ContextFileView struct {
	Path   string `json:"path"`
	Tokens int    `json:"tokens"`
}

type ContextToolView struct {
	Name   string `json:"name"`
	Server string `json:"server,omitempty"`
	Tokens int    `json:"tokens"`
}

type MCPServerView struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	ToolCount *int   `json:"toolCount,omitempty"`
}

func validateContextUsageView(view ContextUsageView) error {
	const maxSafe = int64(1<<53 - 1)
	validTokens := func(value int, field string, positive bool) error {
		if value < 0 || (positive && value == 0) || int64(value) > maxSafe {
			return fmt.Errorf("%s 必须是非负安全整数", field)
		}
		return nil
	}
	if err := validTokens(view.Used, "context.usage.used", false); err != nil {
		return err
	}
	if view.Max != 0 {
		if err := validTokens(view.Max, "context.usage.max", true); err != nil {
			return err
		}
	}
	// GO.6：三组官方口径的取值域与 used 同一条判据（非负安全整数）。
	// 0 是"没有实测锚点"的合法占位（omitempty 时压根不出现），所以不要求正数。
	if err := validTokens(view.PressureTokens, "context.usage.pressureTokens", false); err != nil {
		return err
	}
	if err := validTokens(view.ProjectedTokens, "context.usage.projectedTokens", false); err != nil {
		return err
	}
	if b := view.UsageBuckets; b != nil {
		for _, bucket := range []struct {
			name  string
			value int
		}{
			{"uncachedInputTokens", b.UncachedInputTokens},
			{"outputTokens", b.OutputTokens},
			{"cacheReadTokens", b.CacheReadTokens},
			{"cacheWriteTokens", b.CacheWriteTokens},
		} {
			if err := validTokens(bucket.value, "context.usage.usageBuckets."+bucket.name, false); err != nil {
				return err
			}
		}
	}
	if view.Categories == nil {
		return errors.New("context.usage.categories 必须是数组")
	}
	validName := func(name string) error {
		if name == "" {
			return errors.New("context.usage 含空名称/path")
		}
		return nil
	}
	for _, row := range view.Categories {
		if err := validName(row.Name); err != nil || row.Kind == "" || validTokens(row.Tokens, "context.usage.categories.tokens", false) != nil {
			return errors.New("context.usage.categories 含非法 name/kind/tokens")
		}
	}
	validateItems := func(rows []ContextItemView) error {
		for _, row := range rows {
			if err := validName(row.Name); err != nil {
				return err
			}
			if err := validTokens(row.Tokens, "context.usage.details.tokens", false); err != nil {
				return err
			}
		}
		return nil
	}
	if view.Sections != nil {
		if err := validateItems(*view.Sections); err != nil {
			return err
		}
	}
	if view.Skills != nil {
		if err := validateItems(*view.Skills); err != nil {
			return err
		}
	}
	if view.Files != nil {
		for _, row := range *view.Files {
			if err := validName(row.Path); err != nil {
				return err
			}
			if err := validTokens(row.Tokens, "context.usage.files.tokens", false); err != nil {
				return err
			}
		}
	}
	if view.Tools != nil {
		for _, row := range *view.Tools {
			if err := validName(row.Name); err != nil {
				return err
			}
			if err := validTokens(row.Tokens, "context.usage.tools.tokens", false); err != nil {
				return err
			}
		}
	}
	return nil
}

func ContextMCPMethods() []Method {
	return []Method{MethodContextUsage, MethodMCPList, MethodMCPReconnect, MethodMCPToggle}
}

func SideQueryMethods() []Method { return []Method{MethodSideQuery, MethodSideCancel, MethodSideClose} }

func decodeW149Params(r *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("参数必须是合法 JSON 裸对象: %w", err)
	}
	if decoder.More() {
		return errors.New("参数只能包含一个 JSON 对象")
	}
	return nil
}

func writeW149Error(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNoSuchSession):
		writeJSON(w, http.StatusNotFound, errorBodyFor("no_such_session", err.Error()))
	case errors.Is(err, ErrNoSuchMCPServer), errors.Is(err, ErrNoSuchSideQuery):
		writeJSON(w, http.StatusNotFound, errorBodyFor("not_found", err.Error()))
	case errors.Is(err, ErrSideQueryExists):
		writeJSON(w, http.StatusConflict, errorBodyFor("busy", err.Error()))
	default:
		writeJSON(w, http.StatusInternalServerError, errorBodyFor("internal", err.Error()))
	}
}

func (s *Server) handleContextUsage(w http.ResponseWriter, r *http.Request) {
	if s.opt.ContextUsage == nil {
		writeJSON(w, http.StatusNotImplemented, errorBodyFor("not_implemented", "context.usage 未接线"))
		return
	}
	var in struct {
		ID     string `json:"id"`
		Detail string `json:"detail"`
	}
	if err := decodeW149Params(r, &in); err != nil || in.ID == "" || in.Detail != "full" {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "context.usage 需要裸 id 与 detail=full"))
		return
	}
	usage, err := s.opt.ContextUsage(in.ID, in.Detail)
	if err != nil {
		writeW149Error(w, err)
		return
	}
	if err := validateContextUsageView(usage); err != nil {
		writeW149Error(w, err)
		return
	}
	writeJSON(w, http.StatusOK, usage)
}

func (s *Server) handleMCPList(w http.ResponseWriter, r *http.Request) {
	if s.opt.MCPList == nil {
		writeJSON(w, http.StatusNotImplemented, errorBodyFor("not_implemented", "mcp.list 未接线"))
		return
	}
	var in struct {
		ID string `json:"id"`
	}
	if err := decodeW149Params(r, &in); err != nil || in.ID == "" {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "mcp.list 需要裸 id"))
		return
	}
	servers, err := s.opt.MCPList(in.ID)
	if err != nil {
		writeW149Error(w, err)
		return
	}
	if servers == nil {
		servers = []MCPServerView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"servers": servers})
}

func (s *Server) handleMCPReconnect(w http.ResponseWriter, r *http.Request) {
	if s.opt.MCPReconnect == nil {
		writeJSON(w, http.StatusNotImplemented, errorBodyFor("not_implemented", "mcp.reconnect 未接线"))
		return
	}
	var in struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := decodeW149Params(r, &in); err != nil || in.ID == "" || in.Name == "" {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "mcp.reconnect 需要裸 id/name"))
		return
	}
	if err := s.opt.MCPReconnect(r.Context(), in.ID, in.Name); err != nil {
		writeW149Error(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleMCPToggle(w http.ResponseWriter, r *http.Request) {
	if s.opt.MCPToggle == nil {
		writeJSON(w, http.StatusNotImplemented, errorBodyFor("not_implemented", "mcp.toggle 未接线"))
		return
	}
	var in struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Enabled *bool  `json:"enabled"`
	}
	if err := decodeW149Params(r, &in); err != nil || in.ID == "" || in.Name == "" || in.Enabled == nil {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "mcp.toggle 需要裸 id/name/enabled boolean"))
		return
	}
	if err := s.opt.MCPToggle(r.Context(), in.ID, in.Name, *in.Enabled); err != nil {
		writeW149Error(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSideQuery(w http.ResponseWriter, r *http.Request) {
	if s.opt.SideQuery == nil {
		writeJSON(w, http.StatusNotImplemented, errorBodyFor("not_implemented", "side.query 未接线"))
		return
	}
	var in struct {
		ID        string `json:"id"`
		RequestID string `json:"requestId"`
		Prompt    string `json:"prompt"`
		Stream    bool   `json:"stream"`
	}
	if err := decodeW149Params(r, &in); err != nil || in.ID == "" || in.RequestID == "" || in.Prompt == "" {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "side.query 需要裸 id/requestId/prompt，stream 可选"))
		return
	}
	if in.Stream {
		s.handleSideQueryStream(w, r, in.ID, in.RequestID, in.Prompt)
		return
	}
	answer, err := s.opt.SideQuery(r.Context(), in.ID, in.RequestID, in.Prompt, nil)
	if errors.Is(err, context.Canceled) {
		writeJSON(w, http.StatusOK, map[string]any{"requestId": in.RequestID, "answer": nil, "cancelled": true})
		return
	}
	if err != nil {
		writeW149Error(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"requestId": in.RequestID, "answer": answer})
}

func (s *Server) handleSideQueryStream(w http.ResponseWriter, r *http.Request, sessionID, requestID, prompt string) {
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	flusher, _ := w.(http.Flusher)
	writeLine := func(value any) bool {
		raw, err := json.Marshal(value)
		if err != nil {
			return false
		}
		if _, err := w.Write(append(raw, '\n')); err != nil {
			return false
		}
		if flusher != nil {
			flusher.Flush()
		}
		return true
	}
	answer, err := s.opt.SideQuery(r.Context(), sessionID, requestID, prompt, func(delta string) error {
		if !writeLine(map[string]any{"type": "text", "requestId": requestID, "text": delta}) {
			return context.Canceled
		}
		return nil
	})
	switch {
	case errors.Is(err, context.Canceled):
		writeLine(map[string]any{"type": "done", "requestId": requestID, "answer": nil, "cancelled": true})
	case err != nil:
		line := errorBodyFor("internal", err.Error())
		line["type"] = "error"
		line["requestId"] = requestID
		writeLine(line)
	default:
		writeLine(map[string]any{"type": "done", "requestId": requestID, "answer": answer})
	}
}

func (s *Server) handleSideCancel(w http.ResponseWriter, r *http.Request) {
	if s.opt.SideCancel == nil {
		writeJSON(w, http.StatusNotImplemented, errorBodyFor("not_implemented", "side.cancel 未接线"))
		return
	}
	var in struct {
		ID        string `json:"id"`
		RequestID string `json:"requestId"`
	}
	if err := decodeW149Params(r, &in); err != nil || in.ID == "" || in.RequestID == "" {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "side.cancel 需要裸 id/requestId"))
		return
	}
	if err := s.opt.SideCancel(in.ID, in.RequestID); err != nil {
		writeW149Error(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSideClose(w http.ResponseWriter, r *http.Request) {
	if s.opt.SideClose == nil {
		writeJSON(w, http.StatusNotImplemented, errorBodyFor("not_implemented", "side.close 未接线"))
		return
	}
	var in struct {
		ID string `json:"id"`
	}
	if err := decodeW149Params(r, &in); err != nil || in.ID == "" {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "side.close 需要裸 id"))
		return
	}
	if err := s.opt.SideClose(in.ID); err != nil {
		writeW149Error(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
