package transport

// PARITY-W4-OFFICE：officeToPdf.generation/render 的 L1 契约。
// 真实转换在 internal/officepdf（Word COM / LibreOffice）；本文件只做官方
// 参数形状、结果字段与结构化错误，不生成假 PDF。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

const MethodOfficeToPdfGeneration Method = "officeToPdf.generation"

type OfficialOfficePDFResult struct {
	MissingFonts []string `json:"missingFonts"`
	Generation   string   `json:"generation"`
	Offset       int      `json:"offset"`
	Data         []byte   `json:"data"`
	Eof          bool     `json:"eof"`
	AbsolutePath string   `json:"absolutePath"`
	Version      string   `json:"version"`
	Bytes        *int     `json:"bytes,omitempty"`
}

type OfficialOfficePDFAPI interface {
	Generation(context.Context) (string, error)
	Render(context.Context, string, string, string) (OfficialOfficePDFResult, error)
}

type OfficePDFError struct {
	Status  int
	Code    string
	Message string
	// CapableWhen 只在渲染器不可用时出现（RC2-C4）：说明装/配什么就能用。
	CapableWhen string
}

func (e *OfficePDFError) Error() string { return e.Message }

func IsOfficialOfficePDFMethod(name string) bool {
	return Method(name) == MethodOfficeToPdfGeneration || Method(name) == MethodOfficeToPdfRender
}

var officePDFMount = struct {
	mu sync.RWMutex
	m  map[*Server]OfficialOfficePDFAPI
}{m: map[*Server]OfficialOfficePDFAPI{}}

func (s *Server) AttachOfficialOfficePDFAPI(api OfficialOfficePDFAPI) {
	officePDFMount.mu.Lock()
	defer officePDFMount.mu.Unlock()
	officePDFMount.m[s] = api
}
func (s *Server) DetachOfficialOfficePDFAPI() {
	officePDFMount.mu.Lock()
	defer officePDFMount.mu.Unlock()
	delete(officePDFMount.m, s)
}

func (s *Server) ServeOfficialOfficePDFAPI(w http.ResponseWriter, r *http.Request, name string) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", err.Error()))
		return
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		raw = []byte("{}")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "请求体必须是 JSON 对象"))
		return
	}
	ctx := r.Context()
	if Method(name) == MethodOfficeToPdfGeneration {
		if len(obj) != 0 {
			writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "officeToPdf.generation 不接受参数"))
			return
		}
		api := officePDFMount.m[s]
		if api == nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBodyFor("renderer_unavailable",
				fmt.Sprintf("%s 未接线：宿主没有 AttachOfficialOfficePDFAPI（Office 渲染器不可用）", name)))
			return
		}
		gen, err := api.Generation(ctx)
		if err != nil {
			writeOfficePDFError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"result": gen})
		return
	}
	str := func(key string, required bool) (string, bool) {
		raw, ok := obj[key]
		if !ok {
			return "", !required
		}
		var s string
		if json.Unmarshal(raw, &s) != nil || strings.TrimSpace(s) == "" {
			return "", false
		}
		return s, true
	}
	scope, ok := str("workspaceFileScopeId", true)
	if !ok {
		scope, ok = str("agentId", true)
	}
	if !ok {
		scope, ok = str("sessionId", true)
	}
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "缺 workspaceFileScopeId"))
		return
	}
	path, ok := str("path", true)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "缺 path"))
		return
	}
	priority, ok := str("priority", true)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "缺 priority"))
		return
	}
	if priority != "foreground" && priority != "background" {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "priority 只能是 foreground / background"))
		return
	}
	for field := range obj {
		switch field {
		case "workspaceFileScopeId", "agentId", "sessionId", "path", "priority":
		default:
			writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "不认识的字段: "+field))
			return
		}
	}
	api := officePDFMount.m[s]
	if api == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBodyFor("renderer_unavailable",
			fmt.Sprintf("%s 未接线：宿主没有 AttachOfficialOfficePDFAPI（Office 渲染器不可用）", name)))
		return
	}
	result, err := api.Render(ctx, scope, path, priority)
	if err != nil {
		writeOfficePDFError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": result})
}

func writeOfficePDFError(w http.ResponseWriter, err error) {
	var e *OfficePDFError
	if ok := errorsAsOfficePDF(err, &e); ok {
		// RC2-C4：渲染器不可用时附带 capableWhen（说清装/配什么），
		// 形状与 officeKit 面的 501 一致，客户端不必为两个方法写两套解析。
		if e.CapableWhen != "" {
			writeJSON(w, e.Status, errorBodyFor(e.Code, e.Message, map[string]any{"capableWhen": e.CapableWhen}))
			return
		}
		writeJSON(w, e.Status, errorBodyFor(e.Code, e.Message))
		return
	}
	writeJSON(w, http.StatusBadGateway, errorBodyFor("render_failed", err.Error()))
}

func errorsAsOfficePDF(err error, target **OfficePDFError) bool {
	e, ok := err.(*OfficePDFError)
	if ok {
		*target = e
	}
	return ok
}
