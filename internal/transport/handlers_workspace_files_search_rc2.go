package transport

// 本文件是官方桌面端 0.2.2-rc.2 **新增**方法 `workspaceFiles.search` 的 L1
// 实现（rc.1 的 193 个方法里没有它；rc.2 的 223 个方法里有）。
//
// # 契约来源（官方 0.2.2-rc.2 解包树，只读取证）
//
//   - `node_modules/@deepseek-ai/dsh-api-workspace-files/lib/typert.host.js:443`
//     —— `'workspaceFiles/search'` 描述符：
//       - 参数 [0] `workspaceFileScope`（wire 名 `workspaceFileScopeId`，
//         `source:'lookup'`，**不进 payload args**，由 Session header 派生）；
//       - 参数 [1] `request`：jsonSchema
//         `{query, excludedPaths, includeHidden, respectGitignore}` 四项 required，
//         非 strict 侧 `additionalProperties:true`、strict 侧 `false`；
//       - 结果 `{matches:[{path,type:"file"|"directory"|"other"}], truncated, incomplete}`，
//         `additionalProperties:true`；
//       - 描述语："Within the bounded set of listed files, matches sort by depth,
//         then workspace-relative path."、"incomplete marks excess rg files or exit 2"。
//   - `node_modules/@deepseek-ai/dsh-api-workspace-files/lib/index.js:652-695`
//     —— `async search(workspaceFileScope, request, signal)`：先
//       `request.query.trim()`、空则抛 `gateway/bad-request`；再 resolve/stat
//       `workspaceRoot`（缺失 `workspace-file/not-found`、不是目录
//       `workspace-file/not-directory`）；最后把 `this.config.search`
//       （maxResults/maxEntries/graceMs/timeoutMs/stderrMaxBytes）交给 searchWorkspace。
//   - `node_modules/@deepseek-ai/dsh-api-workspace-files/lib/index.js:189-232`
//     —— `searchWorkspace()`：rg 枚举与匹配语义，Go 等价实现在 `internal/fssearch`。
//
// # 与官方 wire 的两处显式差异
//
//  1. 官方是 typert 的**位置参数**（scope + request 两个位置）；本仓 L1 是一条
//     `POST /api/workspaceFiles.search` + 裸 JSON 参数对象：scope 用 wire 名
//     `workspaceFileScopeId`（兼容 `sessionId`/`agentId`，与同族其它方法一致），
//     `request` 既可嵌套成 `{"request":{…}}` 也可把四个字段平铺在顶层。
//  2. `matches[].type` 只会出现 `file` / `directory`：官方枚举的 `other` 在这条
//     能力上永不产出（`rg --files` 只列普通文件），见 `internal/fssearch` 的 doc.go。
//
// # 错误映射（四字段出口是唯一的 writeOfficialError）
//
// 官方码写在 message 前缀里，HTTP 状态与四字段 code 走本包既有的
// `officialWorkspaceFaultStatus` 映射表（handlers_workspace_official.go:420）：
//
//	gateway/bad-request      （空 query）        → 400 bad_request
//	workspace-file/not-found（工作区根不存在）   → 404 not_found
//	workspace-file/not-directory（根不是目录）    → 400 bad_request
//	workspace-file/outside-workspace（根越界）    → 400 bad_request
//	workspace-file/search-failed（超时/启动失败）  → 400 bad_request
//	取消（context.Canceled / DeadlineExceeded）  → 499 canceled
//
// 宿主没挂实现时回 501 not_implemented 并点名方法——不假成功、不返回空结果。
//
// # 接线（Lead 两步）
//
// 第一步，`internal/transport/server.go` 的 /api 路由里，在
// `if IsOfficialWorkspaceMethod(name)`（当前 :515）**之前**加三行：
//
//	if s.serveWorkspaceFilesSearchIfMatch(w, r, name) {
//		return
//	}
//
// 第二步，`cmd/dshd` 侧在 `attachOfficialWorkspace`（serve_workspace_api.go:1568
// 的 `srv.AttachOfficialWorkspaceAPI` 之后）挂真实实现：
//
//	srv.AttachWorkspaceFilesSearchAPI(transport.WorkspaceFilesSearchAPI{
//		Search: func(ctx context.Context, sessionID string, req fssearch.Request) (fssearch.Result, error) {
//			root, err := host.workspaceRootForSession(sessionID)
//			if err != nil {
//				return fssearch.Result{}, err
//			}
//			return fssearch.Search(ctx, root, req)
//		},
//	})
//
// （`fssearch` 是 `dshgo/internal/fssearch`；`workspaceRootForSession` 见
// cmd/dshd/serve_workspace_api.go:1062，复用会话日志头里的 cwd，与
// workspaceFiles.list/read/stat 的工作区根是同一条判据。）

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"

	"dshgo/internal/fssearch"
)

// MethodWorkspaceFilesSearch 是官方键 `workspaceFiles/search` 的 L1 方法名
// （本仓与既有 200+ 个官方方法同形：`/` 换成 `.`）。
const MethodWorkspaceFilesSearch Method = "workspaceFiles.search"

// IsWorkspaceFilesSearchMethod 判断 /api 路径名是不是这个新增方法。
func IsWorkspaceFilesSearchMethod(name string) bool {
	return Method(name) == MethodWorkspaceFilesSearch
}

// OfficialWorkspaceFileSearchMatch 是官方 `WorkspaceFileSearchResult.matches[]` 的条目。
type OfficialWorkspaceFileSearchMatch struct {
	// Path 是 `/` 连接的 workspace 相对路径，永不为空（官方 :470 描述）。
	Path string `json:"path"`
	// Type 取 file / directory / other（这条能力只产出前两种）。
	Type string `json:"type"`
}

// OfficialWorkspaceFileSearchResult 是官方 `WorkspaceFileSearchResult`。
type OfficialWorkspaceFileSearchResult struct {
	// Matches 已按「深度、再路径」排序，并被 search.maxResults 截断。
	Matches []OfficialWorkspaceFileSearchMatch `json:"matches"`
	// Truncated 表示命中数超过结果上限。
	Truncated bool `json:"truncated"`
	// Incomplete 表示文件数超过 search.maxEntries 被提前终止，或枚举部分失败（rg exit 2）。
	Incomplete bool `json:"incomplete"`
}

// WorkspaceFilesSearchAPI 是宿主侧的真实实现。
//
// `Search` 为 nil 表示当前宿主没有工作区文件搜索能力：调用回 501
// not_implemented 并点名方法，绝不返回空结果冒充成功。
//
// scope 参数是 workspaceFiles 的 **lookup** 身份（wire 名 `workspaceFileScopeId`，
// 官方由 Session header 派生）；宿主按它与 workspaceFiles.list/read/stat 用同一条
// 判据（会话日志头里的 cwd）解析出工作区根。
type WorkspaceFilesSearchAPI struct {
	Search func(ctx context.Context, workspaceFileScopeID string, req fssearch.Request) (fssearch.Result, error)
}

// workspaceFilesSearchMount 按 *Server 索引实现，与 officialWorkspaceMount 同形：
// 两个 Server（A/B）各挂各的，不出现进程级单例被共享。
var workspaceFilesSearchMount = struct {
	mu  sync.RWMutex
	api map[*Server]WorkspaceFilesSearchAPI
}{api: map[*Server]WorkspaceFilesSearchAPI{}}

// AttachWorkspaceFilesSearchAPI 给这个 Server 挂上真实实现。
// 必须发生在服务开始接受 /api 调用之前（serve 的装配阶段）。
func (s *Server) AttachWorkspaceFilesSearchAPI(api WorkspaceFilesSearchAPI) {
	if s == nil {
		return
	}
	workspaceFilesSearchMount.mu.Lock()
	defer workspaceFilesSearchMount.mu.Unlock()
	workspaceFilesSearchMount.api[s] = api
}

// DetachWorkspaceFilesSearchAPI 摘掉实现（测试收尾用；生产不需要调）。
func (s *Server) DetachWorkspaceFilesSearchAPI() {
	if s == nil {
		return
	}
	workspaceFilesSearchMount.mu.Lock()
	defer workspaceFilesSearchMount.mu.Unlock()
	delete(workspaceFilesSearchMount.api, s)
}

func (s *Server) workspaceFilesSearchAPI() (WorkspaceFilesSearchAPI, bool) {
	if s == nil {
		return WorkspaceFilesSearchAPI{}, false
	}
	workspaceFilesSearchMount.mu.RLock()
	defer workspaceFilesSearchMount.mu.RUnlock()
	api, ok := workspaceFilesSearchMount.api[s]
	return api, ok
}

// serveWorkspaceFilesSearchIfMatch 是 /api 路由的唯一接线入口：命中就处理并
// 返回 true，没命中返回 false 交给既有 switch（一行式接线，见文件头注释）。
func (s *Server) serveWorkspaceFilesSearchIfMatch(w http.ResponseWriter, r *http.Request, name string) bool {
	if !IsWorkspaceFilesSearchMethod(name) {
		return false
	}
	api, _ := s.workspaceFilesSearchAPI()
	ServeWorkspaceFilesSearch(w, r, name, api)
	return true
}

// ServeWorkspaceFilesSearch 是本方法的纯函数入口（认证/日志已在 handleAPI 做过）。
func ServeWorkspaceFilesSearch(w http.ResponseWriter, r *http.Request, name string, api WorkspaceFilesSearchAPI) {
	if !IsWorkspaceFilesSearchMethod(name) {
		writeJSON(w, http.StatusNotImplemented, errorBodyFor("not_implemented",
			fmt.Sprintf("不是 workspaceFiles.search 方法：%q", name)))
		return
	}
	params, err := decodeOfficialParams(r)
	if err != nil {
		writeOfficialError(w, err)
		return
	}
	scope, req, err := workspaceFilesSearchParams(params)
	if err != nil {
		writeOfficialError(w, err)
		return
	}
	if api.Search == nil {
		writeOfficialError(w, officialNotImplemented(
			"workspaceFiles.search 未接线：宿主没有真实的工作区文件搜索实现"))
		return
	}
	ctx := r.Context()
	result, apiErr := api.Search(ctx, scope, req)
	if apiErr == nil {
		// 官方在返回前还有一次 `combined.throwIfAborted()`：取消后的"半截结果"
		// 不能被当成成功交出去。
		if cerr := ctx.Err(); cerr != nil {
			apiErr = cerr
		}
	}
	if apiErr != nil {
		writeOfficialError(w, officialWorkspaceFault(workspaceFilesSearchFault(apiErr)))
		return
	}
	wire, werr := workspaceFilesSearchWire(result)
	if werr != nil {
		writeOfficialError(w, werr)
		return
	}
	writeJSON(w, http.StatusOK, wire)
}

// workspaceFilesSearchParams 解析 (scope, request)：scope 是 lookup 参数，
// request 的四项都是 required（官方 strict 侧 additionalProperties:false）。
func workspaceFilesSearchParams(p *officialParams) (string, fssearch.Request, error) {
	var req fssearch.Request
	scope, err := officialFileScope(p)
	if err != nil {
		return "", req, err
	}
	o, err := officialPickObject(p, "request",
		[]string{"query", "excludedPaths", "includeHidden", "respectGitignore"})
	if err != nil {
		return "", req, err
	}
	query, has, err := o.requiredString("query")
	if err != nil {
		return "", req, err
	}
	if !has {
		return "", req, officialBadRequest(
			"缺 %s.query（大小写不敏感子串；含 `/` 时匹配工作区相对路径）", o.owner)
	}
	excluded, has, err := o.stringArray("excludedPaths")
	if err != nil {
		return "", req, err
	}
	if !has {
		return "", req, officialBadRequest(
			"缺 %s.excludedPaths（字面量排除列表；没有就传 []）", o.owner)
	}
	includeHidden, has, err := o.requiredBool("includeHidden")
	if err != nil {
		return "", req, err
	}
	if !has {
		return "", req, officialBadRequest("缺 %s.includeHidden（布尔）", o.owner)
	}
	respectGitignore, has, err := o.requiredBool("respectGitignore")
	if err != nil {
		return "", req, err
	}
	if !has {
		return "", req, officialBadRequest("缺 %s.respectGitignore（布尔）", o.owner)
	}
	if err := o.finish(); err != nil {
		return "", req, err
	}
	if err := p.finish(); err != nil {
		return "", req, err
	}
	return scope, fssearch.Request{
		Query:            query,
		ExcludedPaths:    excluded,
		IncludeHidden:    includeHidden,
		RespectGitignore: respectGitignore,
	}, nil
}

// takeNonNull 取字段原始值，但把 JSON `null` 当"类型不对"拒掉。
//
// 为什么不能直接用 o.str / o.boolean：`json.Unmarshal("null", &s)` 与
// `json.Unmarshal("null", &b)` 都**不报错**（保持零值），于是 `"query":null`
// 会变成空串、`"includeHidden":null` 会变成 false——把"少传/传错"静默成了
// 默认值。官方 zod schema 不接受 null，这一层必须同样拒。
func (o *officialObject) takeNonNull(name string) (json.RawMessage, bool, error) {
	raw, ok := o.take(name)
	if !ok {
		return nil, false, nil
	}
	if strings.TrimSpace(string(raw)) == "null" {
		return nil, false, officialBadRequest("%s.%s 不能是 null（官方 schema 不接受 null）", o.owner, name)
	}
	return raw, true, nil
}

// requiredString 取 required 字符串，null 视为类型错误。
func (o *officialObject) requiredString(name string) (string, bool, error) {
	raw, ok, err := o.takeNonNull(name)
	if err != nil || !ok {
		return "", ok, err
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false, officialBadRequest("%s.%s 必须是字符串", o.owner, name)
	}
	return s, true, nil
}

// requiredBool 取 required 布尔，null 视为类型错误。
func (o *officialObject) requiredBool(name string) (bool, bool, error) {
	raw, ok, err := o.takeNonNull(name)
	if err != nil || !ok {
		return false, ok, err
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return false, false, officialBadRequest("%s.%s 必须是布尔", o.owner, name)
	}
	return b, true, nil
}

// stringArray 取一个 required 的字符串数组字段。
//
// 与 `officialObject.array` 的区别：这里要求元素都是字符串（官方 schema 是
// `z.array(z.string())`），并且 `null` 不算"空数组"——`z.array` 会拒绝 null，
// 本层不能把 null 静默当 []（那会让"少传字段"看起来像"没有排除项"）。
func (o *officialObject) stringArray(name string) ([]string, bool, error) {
	raw, ok, nerr := o.takeNonNull(name)
	if nerr != nil || !ok {
		return nil, ok, nerr
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, false, officialBadRequest("%s.%s 必须是字符串数组", o.owner, name)
	}
	if arr == nil {
		return nil, false, officialBadRequest("%s.%s 必须是字符串数组（收到 null）", o.owner, name)
	}
	out := make([]string, 0, len(arr))
	for i, el := range arr {
		var s string
		if err := json.Unmarshal(el, &s); err != nil {
			return nil, false, officialBadRequest("%s.%s[%d] 必须是字符串", o.owner, name, i)
		}
		out = append(out, s)
	}
	return out, true, nil
}

// workspaceFilesSearchFault 把实现侧的错误翻成官方码（message 前缀 + 四字段映射）。
func workspaceFilesSearchFault(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &officialError{
			status: 499, code: "canceled",
			msg: "workspaceFiles.search 在完成前被取消；结果不完整，取消原因见客户端，重发即可重新搜索",
		}
	}
	switch {
	case errors.Is(err, fssearch.ErrBlankQuery):
		return OfficialWorkspaceError("gateway/bad-request",
			"query 去空白后为空：官方拒绝空搜索词（workspaceFiles/search）")
	case errors.Is(err, fssearch.ErrInvalidRoot):
		return OfficialWorkspaceError("workspace-file/outside-workspace",
			"工作区根越界，拒绝搜索：%s", err.Error())
	case errors.Is(err, fssearch.ErrRootNotFound):
		return OfficialWorkspaceError("workspace-file/not-found", "%s", err.Error())
	case errors.Is(err, fssearch.ErrRootNotDirectory):
		return OfficialWorkspaceError("workspace-file/not-directory", "%s", err.Error())
	case errors.Is(err, fssearch.ErrTimeout):
		return OfficialWorkspaceError("workspace-file/search-failed",
			"%s；narrow the workspace or increase search.timeoutMs", err.Error())
	}
	return err
}

// workspaceFilesSearchWire 把实现结果映射成官方 wire 形状，并守住两条契约：
// path 非空且是 workspace 相对路径；type 只在官方枚举里。
//
// 守在出口而不是信任实现：宿主把绝对路径或空 path 塞进结果，等于把内部路径
// 泄漏/让客户端拿到它读不了的东西——按宿主内部错误报，不静默改写成空串。
func workspaceFilesSearchWire(res fssearch.Result) (OfficialWorkspaceFileSearchResult, error) {
	out := OfficialWorkspaceFileSearchResult{
		Matches:    make([]OfficialWorkspaceFileSearchMatch, 0, len(res.Matches)),
		Truncated:  res.Truncated,
		Incomplete: res.Incomplete,
	}
	if out.Truncated && len(res.Matches) == 0 {
		return out, officialInternal("宿主报告 truncated=true 但 matches 为空：结果自相矛盾")
	}
	for i, m := range res.Matches {
		wireType, err := workspaceFilesSearchWireType(m.Type)
		if err != nil {
			return out, officialInternal("宿主返回的第 %d 条 match 类型非法：%v", i, err)
		}
		if err := validateWorkspaceRelativePath(m.Path); err != nil {
			return out, officialInternal("宿主返回的第 %d 条 match 路径非法：%v", i, err)
		}
		out.Matches = append(out.Matches, OfficialWorkspaceFileSearchMatch{Path: m.Path, Type: wireType})
	}
	return out, nil
}

func workspaceFilesSearchWireType(t fssearch.EntryType) (string, error) {
	switch t {
	case fssearch.TypeFile, fssearch.TypeDirectory, fssearch.TypeOther:
		return string(t), nil
	}
	return "", fmt.Errorf("未知条目类型 %q（官方枚举只有 file/directory/other）", string(t))
}

// validateWorkspaceRelativePath 要求结果是 `/` 连接的 workspace 相对路径：
// 非空、不以 `/` 开头、不含 `..` 段、不含反斜杠与 NUL（官方 :470 描述）。
func validateWorkspaceRelativePath(p string) error {
	switch {
	case p == "":
		return errors.New("path 不能为空（官方说 never empty）")
	case strings.HasPrefix(p, "/"):
		return fmt.Errorf("path %q 是绝对路径，必须是 workspace 相对路径", p)
	case filepath.IsAbs(p) || looksLikeWindowsAbsPath(p):
		return fmt.Errorf("path %q 是平台绝对路径，必须是 workspace 相对路径", p)
	case strings.Contains(p, `\`):
		return fmt.Errorf("path %q 含反斜杠，官方要求 `/` 连接", p)
	case strings.ContainsRune(p, 0):
		return fmt.Errorf("path %q 含 NUL", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." || seg == "" || seg == "." {
			return fmt.Errorf("path %q 含非法段 %q", p, seg)
		}
	}
	return nil
}

// looksLikeWindowsAbsPath 在非 Windows 构建上也认得出 `C:/x`、`\\server\share`
// 这类路径：wire 契约与宿主平台无关，出口守卫不能因为编译平台不同就放过它们。
func looksLikeWindowsAbsPath(p string) bool {
	if len(p) >= 2 && p[1] == ':' {
		return true
	}
	return strings.HasPrefix(p, `\\`) || strings.HasPrefix(p, "//")
}
