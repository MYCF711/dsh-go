package transport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"dshgo/internal/mcp"
)

// 官方 0.2.2 的 MCP 服务管理面（package `dsh-mcp-manager`）。
//
// 三个方法都是 host 级（不带 session），方法名在官方 typert 里是斜杠形式
// `mcpManager/probe`；本仓 L1 的既有写法是点号（`mcp.list`），所以两种拼写都认，
// 出口一律点号常量。
//
// 契约（逐字段）：
//   - docs/evidence/official-method-contract-delta-0.2.2-rc.2.md
//     `### mcpManager/probe` / `### mcpManager/restart` / `### mcpManager/watch`
//   - 状态形状：internal/mcp.Status（官方 McpManagedServerStatus 的九分支）
//
// 为什么在这里挂一层 mount 而不是往 server.go 的 `switch Method(name)` 里加 case：
// 任务写作用域不含 server.go。`Options.HandlerWrap` 是既有的 additive 槽，
// `frontendMount`/`officialWireMount` 已经证明它是给这种"加一组方法"用的；
// 接线行写在 tmp/GO8-RC2/reports/C3-mcp-manager.md。
const (
	// MethodMCPManagerProbe 探一个 server 的管理面能力（不碰活实例）。
	MethodMCPManagerProbe Method = "mcpManager.probe"
	// MethodMCPManagerRestart 重建一个 server（清理未确认则拒绝替换）。
	MethodMCPManagerRestart Method = "mcpManager.restart"
	// MethodMCPManagerWatch 流式订阅 server 视图（首帧当前列表，之后每次变化一份完整列表）。
	MethodMCPManagerWatch Method = "mcpManager.watch"
)

// MCPManagerDeps 是宿主提供的动作面。
//
// transport 不 import 业务包：三个函数由 cmd/dshd 注入（它们背后是
// modules/tools.MCPManager 的同一份 registry）。nil 的项按"未接线"回 501，
// 绝不返回空结果冒充成功。
type MCPManagerDeps struct {
	// List 给出一次性视图快照。
	List func() ([]mcp.View, error)
	// Probe 探一个 server；ok=false 也是**结果**，不是错误。
	Probe func(ctx context.Context, name string) (mcp.ProbeResult, error)
	// Restart 重建一个 server。
	Restart func(ctx context.Context, name string) error
	// Subscribe 开一条真实状态流。
	Subscribe func(ctx context.Context) (<-chan []mcp.View, error)
}

// MCPManagerMethods 返回本组方法常量。
func MCPManagerMethods() []Method {
	return []Method{MethodMCPManagerProbe, MethodMCPManagerRestart, MethodMCPManagerWatch}
}

// IsMCPManagerMethod 判断一个 method 名（点号或斜杠）是否属于本组。
func IsMCPManagerMethod(name string) bool {
	switch mcpManagerMethodName(strings.ReplaceAll(name, "/", ".")) {
	case MethodMCPManagerProbe, MethodMCPManagerRestart, MethodMCPManagerWatch:
		return true
	}
	return false
}

// mcpManagerMethodName 把 `/api/` 去掉、把斜杠拼写归一成点号。
func mcpManagerMethodName(raw string) Method {
	name := raw
	if i := strings.Index(name, "/api/"); i >= 0 {
		name = name[i+len("/api/"):]
	}
	name = strings.Trim(name, "/")
	return Method(strings.ReplaceAll(name, "/", "."))
}

// MCPManagerMount 把 mcpManager.* 三个方法挂到既有 handler 的外面。
//
// 返回 nil 表示"不挂"：deps 一项都没给时，一个字节都不改既有行为
// （与 frontendMount/officialWireMount 的"打开才生效"同一取舍）。
// 只拦 `/api/mcpManager.*`；其它路径原样交给 inner，所以 /connect、/ws、
// 既有 200+ 方法一个都没动。
func MCPManagerMount(deps MCPManagerDeps) func(*Server) http.Handler {
	return MCPManagerMountAround(deps, nil)
}

// MCPManagerMountAround 把管理面挂在既有 wrap 组合的**外层**。
//
// `Options.HandlerWrap` 只有一个槽，而 `frontendMount`/`officialWireMount` 已经
// 占了它；这个函数就是那层的组合子：命中的 mcpManager 方法由本文件处理，
// 其余原样交给 inner（inner 为 nil 时直接交给 Server 自己）。
// 于是生产接线只需要改 serve.go 里的一行。
func MCPManagerMountAround(deps MCPManagerDeps, inner func(*Server) http.Handler) func(*Server) http.Handler {
	if deps.List == nil && deps.Probe == nil && deps.Restart == nil && deps.Subscribe == nil {
		return inner
	}
	return func(s *Server) http.Handler {
		var base http.Handler = s
		if inner != nil {
			if wrapped := inner(s); wrapped != nil {
				base = wrapped
			}
		}
		return &mcpManagerHandler{server: s, deps: deps, inner: base}
	}
}

type mcpManagerHandler struct {
	server *Server
	deps   MCPManagerDeps
	inner  http.Handler
}

func (h *mcpManagerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		h.inner.ServeHTTP(w, r)
		return
	}
	method := mcpManagerMethodName(r.URL.Path)
	if !IsMCPManagerMethod(string(method)) {
		h.inner.ServeHTTP(w, r)
		return
	}
	h.server.serveMCPManagerAPI(w, r, h.deps, method)
}

// serveMCPManagerAPI 是三个方法的统一入口：先做与既有 /api 完全一致的
// 方法/认证检查，再分派。**认证必须先于参数解析**：管理面方法能起子进程，
// 不能给未认证的调用方任何"参数对/错"的可观测差异。
func (s *Server) serveMCPManagerAPI(w http.ResponseWriter, r *http.Request, deps MCPManagerDeps, method Method) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	if !s.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, errorBody("unauthorized"))
		return
	}
	switch method {
	case MethodMCPManagerProbe:
		s.handleMCPManagerProbe(w, r, deps)
	case MethodMCPManagerRestart:
		s.handleMCPManagerRestart(w, r, deps)
	case MethodMCPManagerWatch:
		s.handleMCPManagerWatch(w, r, deps)
	default:
		writeJSON(w, http.StatusNotImplemented, errorBodyFor("not_implemented", "mcpManager 未知方法 "+string(method)))
	}
}

// mcpManagerParams 是三个方法共用的裸参数：`name` 是 Official 索引 0 的那个
// 位置参数；`id` 只为本仓既有习惯保留（host 级方法不需要会话，给了也忽略）。
type mcpManagerParams struct {
	Name string `json:"name"`
	ID   string `json:"id"`
}

func decodeMCPManagerParams(r *http.Request, allowEmpty bool) (mcpManagerParams, error) {
	var in mcpManagerParams
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return in, errors.New("读请求体失败: " + err.Error())
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		if allowEmpty {
			return in, nil
		}
		return in, errors.New("参数必须是合法 JSON 裸对象（至少要有 name）")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		return in, errors.New("参数必须是合法 JSON 裸对象: " + err.Error())
	}
	if decoder.More() {
		return in, errors.New("参数只能包含一个 JSON 对象")
	}
	return in, nil
}

func (s *Server) handleMCPManagerProbe(w http.ResponseWriter, r *http.Request, deps MCPManagerDeps) {
	if deps.Probe == nil {
		writeJSON(w, http.StatusNotImplemented, errorBodyFor("not_implemented", "mcpManager.probe 未接线"))
		return
	}
	in, err := decodeMCPManagerParams(r, false)
	if err != nil || in.Name == "" {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "mcpManager.probe 需要裸 name（非空字符串）"))
		return
	}
	result, err := deps.Probe(r.Context(), in.Name)
	if err != nil {
		writeMCPManagerError(w, err)
		return
	}
	// ok=false 是**结果**（阶段分类的失败），HTTP 仍是 200：
	// 官方 probe 就是这么设计的——管理面渲染"卡在哪一步"，而不是拿到异常。
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleMCPManagerRestart(w http.ResponseWriter, r *http.Request, deps MCPManagerDeps) {
	if deps.Restart == nil {
		writeJSON(w, http.StatusNotImplemented, errorBodyFor("not_implemented", "mcpManager.restart 未接线"))
		return
	}
	in, err := decodeMCPManagerParams(r, false)
	if err != nil || in.Name == "" {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "mcpManager.restart 需要裸 name（非空字符串）"))
		return
	}
	if err := deps.Restart(r.Context(), in.Name); err != nil {
		writeMCPManagerError(w, err)
		return
	}
	// 官方结果类型是 void（typert `unsupported void value`）。本仓出口不接受
	// `{"result":null}`（会变成 500），所以用 {"ok":true} 表达"这次调用没有载荷"，
	// 与既有 `mcp.reconnect`/`mcp.toggle` 同形。
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleMCPManagerWatch 是**真实**流：每行一份官方 `McpManagedServerView[]`。
//
// 它没有定时器、没有心跳、没有"先发一帧假的再补真的"：帧只由 registry 的
// 真实状态变化唤起（internal/mcp.Registry 的唤醒集合），首帧是当前完整列表，
// 之后每次状态/条目变化再给一份完整列表；消费慢时合并成最新一份。
// 订阅者断开或管理器释放时流结束。
func (s *Server) handleMCPManagerWatch(w http.ResponseWriter, r *http.Request, deps MCPManagerDeps) {
	if deps.Subscribe == nil {
		writeJSON(w, http.StatusNotImplemented, errorBodyFor("not_implemented", "mcpManager.watch 未接线"))
		return
	}
	// watch 官方没有参数：允许空 body / {} / {"id":...}（id 忽略）。
	if _, err := decodeMCPManagerParams(r, true); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBodyFor("bad_request", "mcpManager.watch 参数只能是空对象或 {id}"))
		return
	}
	stream, err := deps.Subscribe(r.Context())
	if err != nil {
		writeMCPManagerError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	for {
		select {
		case <-r.Context().Done():
			return
		case views, ok := <-stream:
			if !ok {
				// 管理器释放或订阅被取消：流正常结束（官方也是"直到订阅者
				// 或管理器停下"），不是错误。
				return
			}
			raw, err := json.Marshal(views)
			if err != nil {
				return
			}
			if _, err := w.Write(append(raw, '\n')); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

// writeMCPManagerError 把 internal/mcp 的错误分类翻成四字段信封。
//
// 码表（server.go 的 errorHint/retryableFor）是冻结的，本任务不改它，所以：
//   - 未知 server → 404 not_found
//   - 传输未实现 → 501 not_implemented（hint 就是"宿主没接线"）
//   - 配置正在变 / 清理未确认 → 409 busy（"等上一轮结束/重启宿主后再来"，
//     都是"现在不行、之后可能行"的形状；补专属码需要 server.go 两行，见报告）
//   - 已释放 → 503 internal（宿主正在停机）
func writeMCPManagerError(w http.ResponseWriter, err error) {
	if err == nil {
		return
	}
	kind, classified := mcp.KindOf(err)
	if !classified {
		writeJSON(w, http.StatusInternalServerError, errorBodyFor("internal", err.Error()))
		return
	}
	switch kind {
	case mcp.KindUnknownServer:
		writeJSON(w, http.StatusNotFound, errorBodyFor("not_found", err.Error()))
	case mcp.KindUnsupportedTransport:
		writeJSON(w, http.StatusNotImplemented, errorBodyFor("not_implemented", err.Error()))
	case mcp.KindConfigChanging, mcp.KindCleanupIncomplete:
		writeJSON(w, http.StatusConflict, errorBodyFor("busy", err.Error()))
	case mcp.KindDisposed:
		writeJSON(w, http.StatusServiceUnavailable, errorBodyFor("internal", err.Error()))
	default:
		writeJSON(w, http.StatusInternalServerError, errorBodyFor("internal", err.Error()))
	}
}
