package transport

// RC2-C4：官方 0.2.2-rc.2 把 `officeToPdf.*` 换成了 `officeKit.*`（新包 dsh-office-kit）。
//
// 契约（docs/evidence/official-method-contract-delta-0.2.2-rc.2.md:2127-2166）：
//
//	officeKit/generation  () → string
//	officeKit/render      (workspaceFileScope, path, priority:foreground|background)
//	                      → RenderedDocumentBytes{missingFonts, generation, offset, data, eof,
//	                        absolutePath, version, bytes?}
//
// 与 rc.1 的 officeToPdf.render 相比只多一个事实：**引擎可能不存在**。官方 rc.2 的形状是
// 「provider 照常注册，用到时报 OfficeKitError code=unavailable」
// （upgrade-guide/v0.2.2-alpha.3/office-kit-package/guide.md）。本文件据此把
// 「没有引擎」做成 **501 + code=unavailable + capableWhen**（说清装/配什么就能用），
// 绝不返回空 PDF、空 data 或假成功。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

const (
	// MethodOfficeKitGeneration 是官方 `officeKit/generation`（本仓把 / 换成 .）。
	MethodOfficeKitGeneration Method = "officeKit.generation"
	// MethodOfficeKitRender 是官方 `officeKit/render`。
	MethodOfficeKitRender Method = "officeKit.render"
)

// OfficialOfficeKitCapability 是「现在能不能渲染 / 怎么补救」的结构化描述。
//
// 官方只给一句固定安装提示；本仓的引擎是外部目录，调用方需要知道往哪儿指，
// 所以把它结构化放进错误体（error 的兄弟字段，不污染 result）。
type OfficialOfficeKitCapability struct {
	Available        bool     `json:"available"`
	Generation       string   `json:"generation"`
	EngineVersion    string   `json:"engineVersion,omitempty"`
	EngineRoot       string   `json:"engineRoot,omitempty"`
	Executable       string   `json:"executable,omitempty"`
	ProgramDirectory string   `json:"programDirectory,omitempty"`
	Source           string   `json:"source,omitempty"`
	Tried            []string `json:"tried,omitempty"`
	CapableWhen      string   `json:"capableWhen,omitempty"`
}

// OfficialOfficeKitAPI 是宿主注入的 officeKit 实现（真实引擎在 internal/officekit）。
type OfficialOfficeKitAPI interface {
	// Generation 返回 provider 生命周期标识（引擎/字体/配置一变就变）。
	Generation(context.Context) (string, OfficialOfficeKitCapability, error)
	// Render 把 Office 文件真转成 PDF（结果形状同官方 RenderedDocumentBytes）。
	Render(ctx context.Context, workspaceFileScope, path, priority string) (OfficialOfficePDFResult, error)
}

// OfficeKitAPIError 是带 HTTP 状态与可选补救说明的结构化失败。
type OfficeKitAPIError struct {
	Status      int
	Code        string
	Message     string
	CapableWhen string
	Capability  *OfficialOfficeKitCapability
}

func (e *OfficeKitAPIError) Error() string { return e.Message }

// OfficialOfficeKitMethods 按固定顺序返回本文件实现的方法（给快照/文档/门禁用）。
func OfficialOfficeKitMethods() []Method {
	return []Method{MethodOfficeKitGeneration, MethodOfficeKitRender}
}

// IsOfficialOfficeKitMethod 判断 /api 路径名是否属于 officeKit 面。
func IsOfficialOfficeKitMethod(name string) bool {
	switch Method(name) {
	case MethodOfficeKitGeneration, MethodOfficeKitRender:
		return true
	}
	return false
}

var officeKitMount = struct {
	mu sync.RWMutex
	m  map[*Server]OfficialOfficeKitAPI
}{m: map[*Server]OfficialOfficeKitAPI{}}

// AttachOfficialOfficeKitAPI 挂上实现；不挂时两个方法都给结构化 501。
func (s *Server) AttachOfficialOfficeKitAPI(api OfficialOfficeKitAPI) {
	officeKitMount.mu.Lock()
	defer officeKitMount.mu.Unlock()
	officeKitMount.m[s] = api
}

// DetachOfficialOfficeKitAPI 摘掉实现（测试清理用）。
func (s *Server) DetachOfficialOfficeKitAPI() {
	officeKitMount.mu.Lock()
	defer officeKitMount.mu.Unlock()
	delete(officeKitMount.m, s)
}

func (s *Server) officeKitAPI() OfficialOfficeKitAPI {
	officeKitMount.mu.RLock()
	defer officeKitMount.mu.RUnlock()
	return officeKitMount.m[s]
}

// ServeOfficialOfficeKitAPI 是 officeKit 两方法的 HTTP 入口。
func (s *Server) ServeOfficialOfficeKitAPI(w http.ResponseWriter, r *http.Request, name string) {
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
	api := s.officeKitAPI()
	if Method(name) == MethodOfficeKitGeneration {
		if len(obj) != 0 {
			writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "officeKit.generation 不接受参数"))
			return
		}
		if api == nil {
			writeOfficeKitUnavailable(w, name, nil)
			return
		}
		generation, capability, err := api.Generation(ctx)
		if err != nil {
			writeOfficeKitError(w, err, capability)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"result": generation})
		return
	}

	str := func(key string) (string, bool) {
		value, ok := obj[key]
		if !ok {
			return "", false
		}
		var text string
		if json.Unmarshal(value, &text) != nil || strings.TrimSpace(text) == "" {
			return "", false
		}
		return text, true
	}
	// 官方这个位置的 name 是 workspaceFileScope、wire 名是 workspaceFileScopeId；
	// 本仓会话 id 另有 agentId / sessionId 两种既有写法，三种都认。
	scope, ok := str("workspaceFileScopeId")
	if !ok {
		if scope, ok = str("agentId"); !ok {
			scope, ok = str("sessionId")
		}
	}
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "缺 workspaceFileScopeId"))
		return
	}
	path, ok := str("path")
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "缺 path"))
		return
	}
	priority, ok := str("priority")
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
	if api == nil {
		writeOfficeKitUnavailable(w, name, nil)
		return
	}
	result, err := api.Render(ctx, scope, path, priority)
	if err != nil {
		var capability *OfficialOfficeKitCapability
		if generation, cap, capErr := api.Generation(ctx); capErr == nil {
			_ = generation
			capability = &cap
		}
		writeOfficeKitError(w, err, *capabilityOrEmpty(capability))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": result})
}

func capabilityOrEmpty(capability *OfficialOfficeKitCapability) *OfficialOfficeKitCapability {
	if capability != nil {
		return capability
	}
	return &OfficialOfficeKitCapability{}
}

// writeOfficeKitUnavailable 是「没有引擎 / 没有接线」的统一形状：
// 501 + code=unavailable + capableWhen + capability。
//
// 为什么是 501 而不是 503：这个部署**没有实现**渲染能力（引擎没装），
// 不是"暂时过载"；与 503 的语义区分开，UI 才能给出「去装/去配」的动作。
func writeOfficeKitUnavailable(w http.ResponseWriter, name string, capability *OfficialOfficeKitCapability) {
	cap := capabilityOrEmpty(capability)
	body := errorBodyFor("unavailable",
		fmt.Sprintf("%s 需要随包预编译的 LibreOfficeKit 原生引擎：%s", name, cap.CapableWhen),
		map[string]any{"capableWhen": cap.CapableWhen, "capability": *cap})
	writeJSON(w, http.StatusNotImplemented, body)
}

// writeOfficeKitError 把实现层的错误映射成 HTTP + 四字段错误体。
//
// capableWhen / capability 作为 error 的兄弟字段出现（不改 error 的四字段契约）。
func writeOfficeKitError(w http.ResponseWriter, err error, capability OfficialOfficeKitCapability) {
	apiErr, ok := err.(*OfficeKitAPIError)
	if !ok {
		writeJSON(w, http.StatusBadGateway, errorBodyFor("failed", err.Error()))
		return
	}
	status := apiErr.Status
	if status == 0 {
		status = http.StatusBadGateway
	}
	code := apiErr.Code
	if code == "" {
		code = "failed"
	}
	extras := map[string]any{}
	if apiErr.CapableWhen != "" {
		extras["capableWhen"] = apiErr.CapableWhen
	}
	if status == http.StatusNotImplemented {
		if apiErr.Capability != nil {
			extras["capability"] = *apiErr.Capability
		} else {
			extras["capability"] = capability
		}
	}
	message := apiErr.Message
	if apiErr.CapableWhen != "" && !strings.Contains(message, apiErr.CapableWhen) {
		message += "；补救方式：" + apiErr.CapableWhen
	}
	writeJSON(w, status, errorBodyFor(code, message, extras))
}
