package transport

// workspaceFiles.search（官方 0.2.2-rc.2 新增方法）的 HTTP 层测试（先红后绿）。
//
// 契约来源（只读官方解包树）：
//
//	node_modules/@deepseek-ai/dsh-api-workspace-files/lib/typert.host.js:443
//	  'workspaceFiles/search' 描述符：参数 [0] workspaceFileScope（wire
//	  workspaceFileScopeId，source=lookup，**不进 payload args**）、参数 [1] request
//	  {query, excludedPaths, includeHidden, respectGitignore} 四项 required；
//	  result {matches:[{path,type}], truncated, incomplete}。
//	node_modules/@deepseek-ai/dsh-api-workspace-files/lib/index.js:652-695
//	  search()：trim 后空 query → gateway/bad-request；root 不存在 →
//	  workspace-file/not-found；root 不是目录 → workspace-file/not-directory。
//
// 本仓 L1 形状：`POST /api/workspaceFiles.search`，body 是**裸参数对象**，
// 成功走 `{"result": …}`（writeJSON 统一加信封），失败四字段 error。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/fssearch"
)

// searchTestAPI 造一个把 scope 映射到临时目录、真正走 fssearch 的实现。
func searchTestAPI(roots map[string]string) WorkspaceFilesSearchAPI {
	return WorkspaceFilesSearchAPI{
		Search: func(ctx context.Context, sessionID string, req fssearch.Request) (fssearch.Result, error) {
			root, ok := roots[sessionID]
			if !ok {
				return fssearch.Result{}, OfficialWorkspaceError("session/not-found", "没有会话 %q", sessionID)
			}
			return fssearch.Search(ctx, root, req)
		},
	}
}

// callSearch 打一次方法（直接调纯函数入口，认证/日志不参与本层）。
func callSearch(t *testing.T, api WorkspaceFilesSearchAPI, method, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/"+method, strings.NewReader(body))
	rec := httptest.NewRecorder()
	ServeWorkspaceFilesSearch(rec, req, method, api)
	var out map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s 的响应不是 JSON 对象: %q", method, rec.Body.String())
		}
	}
	return rec.Code, out
}

// searchResult 取 {"result": …} 载荷。
func searchResult(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	raw, ok := out["result"]
	if !ok {
		t.Fatalf("成功响应没有 result：%v", out)
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("result 不是对象：%v", raw)
	}
	return obj
}

// searchError 断言四字段错误体并返回 (code, message)。
func searchError(t *testing.T, out map[string]any) (string, string) {
	t.Helper()
	raw, ok := out["error"]
	if !ok {
		t.Fatalf("错误响应没有 error：%v", out)
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("error 不是对象：%v", raw)
	}
	for _, key := range []string{"code", "message", "hint", "retryable"} {
		if _, ok := obj[key]; !ok {
			t.Fatalf("四字段错误缺 %q：%v", key, obj)
		}
	}
	code, _ := obj["code"].(string)
	msg, _ := obj["message"].(string)
	return code, msg
}

func writeSearchTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("建目录: %v", err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatalf("写文件: %v", err)
		}
	}
}

const searchValidBody = `{"workspaceFileScopeId":"sess-1","request":` +
	`{"query":"alpha","excludedPaths":[],"includeHidden":false,"respectGitignore":false}}`

// TestWorkspaceFilesSearchMethodName 钉住方法名与判定函数（官方键 `workspaceFiles/search`）。
func TestWorkspaceFilesSearchMethodName(t *testing.T) {
	if got := string(MethodWorkspaceFilesSearch); got != "workspaceFiles.search" {
		t.Fatalf("方法名 = %q, want %q", got, "workspaceFiles.search")
	}
	if !IsWorkspaceFilesSearchMethod("workspaceFiles.search") {
		t.Fatalf("IsWorkspaceFilesSearchMethod(workspaceFiles.search) = false")
	}
	for _, other := range []string{
		"workspaceFiles.list", "workspaceFiles.searchX", "workspaceFiles.search/x",
		"workspaceFiles.changes", "workspaceFiles.stat", "",
	} {
		if IsWorkspaceFilesSearchMethod(other) {
			t.Fatalf("IsWorkspaceFilesSearchMethod(%q) 应当为 false", other)
		}
	}
}

// TestWorkspaceFilesSearchSuccessShape 钉住成功形状：{matches:[{path,type}],truncated,incomplete}。
func TestWorkspaceFilesSearchSuccessShape(t *testing.T) {
	root := t.TempDir()
	writeSearchTree(t, root, map[string]string{
		"Alpha.txt":      "a",
		"sub/alpha.md":   "b",
		"sub/beta.txt":   "c",
		"other/gamma.md": "d",
	})
	api := searchTestAPI(map[string]string{"sess-1": root})
	status, out := callSearch(t, api, "workspaceFiles.search", searchValidBody)
	if status != http.StatusOK {
		t.Fatalf("状态 = %d, want 200；body=%v", status, out)
	}
	res := searchResult(t, out)
	matches, ok := res["matches"].([]any)
	if !ok {
		t.Fatalf("matches 不是数组：%v", res["matches"])
	}
	if len(matches) != 2 {
		t.Fatalf("matches 数量 = %d, want 2：%v", len(matches), matches)
	}
	first, _ := matches[0].(map[string]any)
	if first["path"] != "Alpha.txt" || first["type"] != "file" {
		t.Fatalf("matches[0] = %v, want {path:Alpha.txt type:file}", first)
	}
	second, _ := matches[1].(map[string]any)
	if second["path"] != "sub/alpha.md" || second["type"] != "file" {
		t.Fatalf("matches[1] = %v, want {path:sub/alpha.md type:file}", second)
	}
	if _, ok := res["truncated"].(bool); !ok {
		t.Fatalf("缺 truncated 布尔：%v", res)
	}
	if _, ok := res["incomplete"].(bool); !ok {
		t.Fatalf("缺 incomplete 布尔：%v", res)
	}
	// 目录条目 shape：目录必须能作为 match 出现，且 type=directory。
	status, out = callSearch(t, api, "workspaceFiles.search",
		`{"workspaceFileScopeId":"sess-1","request":{"query":"sub","excludedPaths":[],"includeHidden":false,"respectGitignore":false}}`)
	if status != http.StatusOK {
		t.Fatalf("状态 = %d, want 200；body=%v", status, out)
	}
	matches, _ = searchResult(t, out)["matches"].([]any)
	if len(matches) != 1 {
		t.Fatalf("query=sub 应只命中目录 sub：%v", matches)
	}
	if dir, _ := matches[0].(map[string]any); dir["path"] != "sub" || dir["type"] != "directory" {
		t.Fatalf("matches[0] = %v, want {path:sub type:directory}", dir)
	}
}

// TestWorkspaceFilesSearchEmptyMatchesIsArray 钉住空结果也是数组（不是 null）。
func TestWorkspaceFilesSearchEmptyMatchesIsArray(t *testing.T) {
	root := t.TempDir()
	writeSearchTree(t, root, map[string]string{"a.txt": "x"})
	api := searchTestAPI(map[string]string{"sess-1": root})
	status, out := callSearch(t, api, "workspaceFiles.search",
		`{"workspaceFileScopeId":"sess-1","request":{"query":"nothing-here","excludedPaths":[],"includeHidden":false,"respectGitignore":false}}`)
	if status != http.StatusOK {
		t.Fatalf("状态 = %d, want 200；body=%v", status, out)
	}
	matches, ok := searchResult(t, out)["matches"].([]any)
	if !ok {
		t.Fatalf("空命中的 matches 也必须是数组：%v", searchResult(t, out)["matches"])
	}
	if len(matches) != 0 {
		t.Fatalf("应无命中：%v", matches)
	}
}

// TestWorkspaceFilesSearchRequestValidation 钉住 request 四项 required 与类型校验。
func TestWorkspaceFilesSearchRequestValidation(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string // message 必须包含的片段
	}{
		{"缺 scope", `{}`, "workspaceFileScopeId"},
		{"scope 空串", `{"workspaceFileScopeId":"","request":{"query":"a","excludedPaths":[],"includeHidden":false,"respectGitignore":false}}`, "空串"},
		{"缺 request 整块", `{"workspaceFileScopeId":"s"}`, "query"},
		{"缺 query", `{"workspaceFileScopeId":"s","request":{"excludedPaths":[],"includeHidden":false,"respectGitignore":false}}`, "query"},
		{"缺 excludedPaths", `{"workspaceFileScopeId":"s","request":{"query":"a","includeHidden":false,"respectGitignore":false}}`, "excludedPaths"},
		{"缺 includeHidden", `{"workspaceFileScopeId":"s","request":{"query":"a","excludedPaths":[],"respectGitignore":false}}`, "includeHidden"},
		{"缺 respectGitignore", `{"workspaceFileScopeId":"s","request":{"query":"a","excludedPaths":[],"includeHidden":false}}`, "respectGitignore"},
		{"query 不是字符串", `{"workspaceFileScopeId":"s","request":{"query":3,"excludedPaths":[],"includeHidden":false,"respectGitignore":false}}`, "query"},
		{"query 是 null", `{"workspaceFileScopeId":"s","request":{"query":null,"excludedPaths":[],"includeHidden":false,"respectGitignore":false}}`, "query"},
		{"excludedPaths 不是数组", `{"workspaceFileScopeId":"s","request":{"query":"a","excludedPaths":"x","includeHidden":false,"respectGitignore":false}}`, "excludedPaths"},
		{"excludedPaths 是 null", `{"workspaceFileScopeId":"s","request":{"query":"a","excludedPaths":null,"includeHidden":false,"respectGitignore":false}}`, "excludedPaths"},
		{"excludedPaths 元素不是字符串", `{"workspaceFileScopeId":"s","request":{"query":"a","excludedPaths":["ok",7],"includeHidden":false,"respectGitignore":false}}`, "excludedPaths"},
		{"includeHidden 不是布尔", `{"workspaceFileScopeId":"s","request":{"query":"a","excludedPaths":[],"includeHidden":"yes","respectGitignore":false}}`, "includeHidden"},
		{"respectGitignore 不是布尔", `{"workspaceFileScopeId":"s","request":{"query":"a","excludedPaths":[],"includeHidden":false,"respectGitignore":1}}`, "respectGitignore"},
		{"request 里多字段", `{"workspaceFileScopeId":"s","request":{"query":"a","excludedPaths":[],"includeHidden":false,"respectGitignore":false,"extra":1}}`, `不认识的字段`},
		{"顶层多字段", `{"workspaceFileScopeId":"s","request":{"query":"a","excludedPaths":[],"includeHidden":false,"respectGitignore":false},"extra":1}`, `不认识的字段`},
		{"请求体不是对象", `[]`, "JSON 对象"},
		{"请求体是 null", `null`, "JSON 对象"},
		{"空请求体", ``, "请求体"},
	}
	api := searchTestAPI(nil)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, out := callSearch(t, api, "workspaceFiles.search", c.body)
			if status != http.StatusBadRequest {
				t.Fatalf("状态 = %d, want 400；body=%v", status, out)
			}
			code, msg := searchError(t, out)
			if code != "bad_request" {
				t.Fatalf("四字段 code = %q, want bad_request；msg=%q", code, msg)
			}
			if !strings.Contains(msg, c.want) {
				t.Fatalf("message = %q, 应包含 %q", msg, c.want)
			}
		})
	}
}

// TestWorkspaceFilesSearchAcceptsFlatForm 钉住"嵌套 request 或平铺四字段"两种写法都认，
// 两者同时给算冲突（与官方族既有约定一致）。
func TestWorkspaceFilesSearchAcceptsFlatForm(t *testing.T) {
	root := t.TempDir()
	writeSearchTree(t, root, map[string]string{"alpha.txt": "x"})
	api := searchTestAPI(map[string]string{"sess-1": root})

	flat := `{"workspaceFileScopeId":"sess-1","query":"alpha",` +
		`"excludedPaths":[],"includeHidden":false,"respectGitignore":false}`
	status, out := callSearch(t, api, "workspaceFiles.search", flat)
	if status != http.StatusOK {
		t.Fatalf("平铺写法状态 = %d, want 200；body=%v", status, out)
	}
	if matches, _ := searchResult(t, out)["matches"].([]any); len(matches) != 1 {
		t.Fatalf("平铺写法命中数 = %d, want 1：%v", len(matches), matches)
	}

	both := `{"workspaceFileScopeId":"sess-1","query":"alpha","request":` +
		`{"query":"alpha","excludedPaths":[],"includeHidden":false,"respectGitignore":false}}`
	status, out = callSearch(t, api, "workspaceFiles.search", both)
	if status != http.StatusBadRequest {
		t.Fatalf("嵌套+平铺同时给状态 = %d, want 400；body=%v", status, out)
	}
	if _, msg := searchError(t, out); !strings.Contains(msg, "不能同时给") {
		t.Fatalf("message = %q, 应说明两种写法冲突", msg)
	}
}

// TestWorkspaceFilesSearchBlankQueryRefused 钉住空 query 走官方码 gateway/bad-request。
func TestWorkspaceFilesSearchBlankQueryRefused(t *testing.T) {
	root := t.TempDir()
	writeSearchTree(t, root, map[string]string{"a.txt": "x"})
	api := searchTestAPI(map[string]string{"sess-1": root})
	for _, q := range []string{"", "   ", "\t", "\n", "\u3000", "\uFEFF"} {
		// 用 json.Marshal 生成 body：直接拼字符串会把 TAB 这类字符写成裸字节，
		// 于是测的就成了"非法 JSON"而不是"空 query"。
		encoded, merr := json.Marshal(map[string]any{
			"workspaceFileScopeId": "sess-1",
			"request": map[string]any{
				"query": q, "excludedPaths": []string{},
				"includeHidden": false, "respectGitignore": false,
			},
		})
		if merr != nil {
			t.Fatalf("序列化请求体: %v", merr)
		}
		status, out := callSearch(t, api, "workspaceFiles.search", string(encoded))
		if status != http.StatusBadRequest {
			t.Fatalf("query=%q 状态 = %d, want 400；body=%v", q, status, out)
		}
		code, msg := searchError(t, out)
		if code != "bad_request" {
			t.Fatalf("query=%q 四字段 code = %q, want bad_request", q, code)
		}
		if !strings.Contains(msg, "gateway/bad-request") {
			t.Fatalf("query=%q message = %q, 应带官方码 gateway/bad-request", q, msg)
		}
	}
}

// TestWorkspaceFilesSearchRootFaults 钉住工作区根错误：越界 → outside-workspace（400），
// 不存在 → workspace-file/not-found（404），不是目录 → not-directory（400）。
func TestWorkspaceFilesSearchRootFaults(t *testing.T) {
	base := t.TempDir()
	writeSearchTree(t, base, map[string]string{"afile.txt": "x"})
	// 注意不能用 filepath.Join：它会把 `..` 清理掉。手工拼一个未清理的路径。
	escape := base + string(os.PathSeparator) + "ws" + string(os.PathSeparator) + ".." +
		string(os.PathSeparator) + "ws"
	api := searchTestAPI(map[string]string{
		"sess-escape":  escape,
		"sess-missing": filepath.Join(base, "nope"),
		"sess-file":    filepath.Join(base, "afile.txt"),
	})
	const body = `{"workspaceFileScopeId":"%s","request":{"query":"x","excludedPaths":[],"includeHidden":false,"respectGitignore":false}}`

	status, out := callSearch(t, api, "workspaceFiles.search", strings.Replace(body, "%s", "sess-escape", 1))
	if status != http.StatusBadRequest {
		t.Fatalf("越界根状态 = %d, want 400；body=%v", status, out)
	}
	if _, msg := searchError(t, out); !strings.Contains(msg, "outside-workspace") {
		t.Fatalf("越界根 message = %q, 应带官方码 workspace-file/outside-workspace", msg)
	}

	status, out = callSearch(t, api, "workspaceFiles.search", strings.Replace(body, "%s", "sess-missing", 1))
	if status != http.StatusNotFound {
		t.Fatalf("不存在的根状态 = %d, want 404；body=%v", status, out)
	}
	code, msg := searchError(t, out)
	if code != "not_found" || !strings.Contains(msg, "workspace-file/not-found") {
		t.Fatalf("不存在的根 = (%q,%q), want not_found + workspace-file/not-found", code, msg)
	}

	status, out = callSearch(t, api, "workspaceFiles.search", strings.Replace(body, "%s", "sess-file", 1))
	if status != http.StatusBadRequest {
		t.Fatalf("文件当根状态 = %d, want 400；body=%v", status, out)
	}
	if _, msg := searchError(t, out); !strings.Contains(msg, "not-directory") {
		t.Fatalf("文件当根 message = %q, 应带官方码 workspace-file/not-directory", msg)
	}

	// 宿主自己的官方码（search-failed）照旧按官方码透出，不被吞成成功。
	status, out = callSearch(t, WorkspaceFilesSearchAPI{
		Search: func(context.Context, string, fssearch.Request) (fssearch.Result, error) {
			return fssearch.Result{}, OfficialWorkspaceError("workspace-file/search-failed", "rg 起不来")
		},
	}, "workspaceFiles.search", strings.Replace(body, "%s", "sess-1", 1))
	if status != http.StatusBadRequest {
		t.Fatalf("search-failed 状态 = %d, want 400；body=%v", status, out)
	}
	if _, msg := searchError(t, out); !strings.Contains(msg, "search-failed") {
		t.Fatalf("search-failed message = %q", msg)
	}
}

// TestWorkspaceFilesSearchTruncatedFlags 钉住 truncated/incomplete 原样透出（不被压成 false）。
func TestWorkspaceFilesSearchTruncatedFlags(t *testing.T) {
	api := WorkspaceFilesSearchAPI{
		Search: func(context.Context, string, fssearch.Request) (fssearch.Result, error) {
			return fssearch.Result{
				Matches: []fssearch.Match{
					{Path: "a.txt", Type: fssearch.TypeFile},
					{Path: "dir", Type: fssearch.TypeDirectory},
				},
				Truncated:  true,
				Incomplete: true,
			}, nil
		},
	}
	status, out := callSearch(t, api, "workspaceFiles.search", searchValidBody)
	if status != http.StatusOK {
		t.Fatalf("状态 = %d, want 200；body=%v", status, out)
	}
	res := searchResult(t, out)
	if res["truncated"] != true || res["incomplete"] != true {
		t.Fatalf("truncated/incomplete 应原样透出：%v", res)
	}
	matches, _ := res["matches"].([]any)
	if len(matches) != 2 {
		t.Fatalf("matches 数量 = %d, want 2", len(matches))
	}
	if dir, _ := matches[1].(map[string]any); dir["type"] != "directory" {
		t.Fatalf("目录条目 type 应为 directory：%v", dir)
	}
}

// TestWorkspaceFilesSearchPassThroughRequest 钉住 scope 只作 lookup、四个 request 字段原样进实现。
func TestWorkspaceFilesSearchPassThroughRequest(t *testing.T) {
	var gotScope string
	var gotReq fssearch.Request
	api := WorkspaceFilesSearchAPI{
		Search: func(_ context.Context, sessionID string, req fssearch.Request) (fssearch.Result, error) {
			gotScope, gotReq = sessionID, req
			return fssearch.Result{Matches: []fssearch.Match{}}, nil
		},
	}
	body := `{"workspaceFileScopeId":"sess-42","request":{"query":"Keep/Case",` +
		`"excludedPaths":["build","gen/out.txt"],"includeHidden":true,"respectGitignore":true}}`
	status, out := callSearch(t, api, "workspaceFiles.search", body)
	if status != http.StatusOK {
		t.Fatalf("状态 = %d, want 200；body=%v", status, out)
	}
	if gotScope != "sess-42" {
		t.Fatalf("实现收到的 scope = %q, want sess-42", gotScope)
	}
	if gotReq.Query != "Keep/Case" {
		t.Fatalf("query = %q（大小写必须原样传入，匹配侧才转小写）", gotReq.Query)
	}
	if len(gotReq.ExcludedPaths) != 2 || gotReq.ExcludedPaths[0] != "build" || gotReq.ExcludedPaths[1] != "gen/out.txt" {
		t.Fatalf("excludedPaths = %v, want [build gen/out.txt]", gotReq.ExcludedPaths)
	}
	if !gotReq.IncludeHidden || !gotReq.RespectGitignore {
		t.Fatalf("开关没原样传入：%+v", gotReq)
	}
}

// TestWorkspaceFilesSearchRejectsHostContractViolation 钉住出口守卫：
// 宿主塞进绝对路径/空 path 时按宿主内部错误报，不把内部路径透给客户端。
func TestWorkspaceFilesSearchRejectsHostContractViolation(t *testing.T) {
	for _, bad := range []string{"C:/abs/x.txt", "/abs/x.txt", "", "a/../b", `a\b`} {
		api := WorkspaceFilesSearchAPI{
			Search: func(context.Context, string, fssearch.Request) (fssearch.Result, error) {
				return fssearch.Result{Matches: []fssearch.Match{{Path: bad, Type: fssearch.TypeFile}}}, nil
			},
		}
		status, out := callSearch(t, api, "workspaceFiles.search", searchValidBody)
		if status != http.StatusInternalServerError {
			t.Fatalf("path=%q 状态 = %d, want 500；body=%v", bad, status, out)
		}
		if code, _ := searchError(t, out); code != "internal" {
			t.Fatalf("path=%q 四字段 code = %q, want internal", bad, code)
		}
	}
}

// TestWorkspaceFilesSearchNotMounted 钉住没接线时是 501 + 四字段（不假成功、不空结果）。
func TestWorkspaceFilesSearchNotMounted(t *testing.T) {
	status, out := callSearch(t, WorkspaceFilesSearchAPI{}, "workspaceFiles.search", searchValidBody)
	if status != http.StatusNotImplemented {
		t.Fatalf("状态 = %d, want 501；body=%v", status, out)
	}
	code, msg := searchError(t, out)
	if code != "not_implemented" {
		t.Fatalf("四字段 code = %q, want not_implemented", code)
	}
	if !strings.Contains(msg, "workspaceFiles.search") {
		t.Fatalf("message = %q 应点名方法", msg)
	}
}

// TestWorkspaceFilesSearchRejectsOtherMethod 钉住别的路径名不会被这个入口接走。
func TestWorkspaceFilesSearchRejectsOtherMethod(t *testing.T) {
	api := searchTestAPI(nil)
	status, out := callSearch(t, api, "workspaceFiles.list", searchValidBody)
	if status != http.StatusNotImplemented {
		t.Fatalf("状态 = %d, want 501；body=%v", status, out)
	}
	if code, _ := searchError(t, out); code != "not_implemented" {
		t.Fatalf("四字段 code = %q, want not_implemented", code)
	}
}

// TestWorkspaceFilesSearchCancellation 钉住取消：499 + canceled，不返回半截结果。
func TestWorkspaceFilesSearchCancellation(t *testing.T) {
	root := t.TempDir()
	writeSearchTree(t, root, map[string]string{"a.txt": "x"})
	api := searchTestAPI(map[string]string{"sess-1": root})
	req := httptest.NewRequest(http.MethodPost, "/api/workspaceFiles.search", strings.NewReader(searchValidBody))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	ServeWorkspaceFilesSearch(rec, req, "workspaceFiles.search", api)
	if rec.Code != 499 {
		t.Fatalf("取消状态 = %d, want 499；body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是 JSON: %q", rec.Body.String())
	}
	code, _ := searchError(t, out)
	if code != "canceled" {
		t.Fatalf("四字段 code = %q, want canceled", code)
	}
}

// TestWorkspaceFilesSearchMountedDispatch 钉住 Server 挂载与 /api 分派入口：
// 命中 workspaceFiles.search 返回 true 并写响应；其它方法返回 false 交给既有 switch。
func TestWorkspaceFilesSearchMountedDispatch(t *testing.T) {
	srv, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	root := t.TempDir()
	writeSearchTree(t, root, map[string]string{"alpha.txt": "x"})

	// 未挂载 → 501。
	req := httptest.NewRequest(http.MethodPost, "/api/workspaceFiles.search", strings.NewReader(searchValidBody))
	rec := httptest.NewRecorder()
	if !srv.serveWorkspaceFilesSearchIfMatch(rec, req, "workspaceFiles.search") {
		t.Fatalf("命中的方法应返回 true（已接管）")
	}
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("未挂载状态 = %d, want 501", rec.Code)
	}

	srv.AttachWorkspaceFilesSearchAPI(searchTestAPI(map[string]string{"sess-1": root}))
	t.Cleanup(srv.DetachWorkspaceFilesSearchAPI)

	req = httptest.NewRequest(http.MethodPost, "/api/workspaceFiles.search", strings.NewReader(searchValidBody))
	rec = httptest.NewRecorder()
	if !srv.serveWorkspaceFilesSearchIfMatch(rec, req, "workspaceFiles.search") {
		t.Fatalf("命中的方法应返回 true（已接管）")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("挂载后状态 = %d, want 200；body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是 JSON: %q", rec.Body.String())
	}
	if matches, _ := searchResult(t, out)["matches"].([]any); len(matches) != 1 {
		t.Fatalf("挂载后命中数 = %d, want 1", len(matches))
	}

	// 非本方法 → 不接管（false），交给既有 /api 路由。
	req = httptest.NewRequest(http.MethodPost, "/api/workspaceFiles.list", strings.NewReader("{}"))
	rec = httptest.NewRecorder()
	if srv.serveWorkspaceFilesSearchIfMatch(rec, req, "workspaceFiles.list") {
		t.Fatalf("workspaceFiles.list 不该被本入口接管")
	}

	srv.DetachWorkspaceFilesSearchAPI()
	req = httptest.NewRequest(http.MethodPost, "/api/workspaceFiles.search", strings.NewReader(searchValidBody))
	rec = httptest.NewRecorder()
	srv.serveWorkspaceFilesSearchIfMatch(rec, req, "workspaceFiles.search")
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("摘掉实现后状态 = %d, want 501", rec.Code)
	}
}
