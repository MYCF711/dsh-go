package transport

// HS-16B 先红（workspace 域，transport 判定层）：官方 `resolvePage` 的默认值。
//
// 契约原件（只读镜像，未改安装目录）：
//
//	%TEMP%/official-api-src/dsh/node_modules/@deepseek-ai/dsh-api-workspace-files/lib/index.js
//	  :236  resolvePage(range):
//	          const offset = range.offset === void 0 ? 1 : integerAtLeast(range.offset, 1, "offset");
//	          const limit  = range.limit  === void 0 ? this.config.maxLines : integerAtLeast(range.limit, 1, "limit");
//	        —— offset **缺省是 1**（首页），limit 缺省是部署上限。
//	  :234  cutPage 以 `lineNumber` 从 1 起数行，所以"第 1 行"才是首页。
//	  :265  'workspaceFiles/read' 的参数 `range` 是 `z.object({offset?,limit?})`：
//	        字段都可选；整个 `range` 省略等价于 `{}`（官方实现里三次解构都做了
//	        `range.X === void 0` 判定），不是"参数缺失"错误。
//
// 红点（当前实现与官方不一致）：
//
//	1. 省略 `range` → 本仓 400「缺 range」。官方按 `{}` 处理（offset=1、
//	   limit=maxLines）——客户端少给一个可选对象就被拒，是**假严格**。
//	2. `OfficialWorkspaceFileRange.Offset` 在缺省时是 int64 零值 0。宿主目前
//	   自己补 1，但这个结构是公开契约：任何直接读 `Offset` 的实现都会算出
//	   `start = offset-1 = -1`，页从第 0 行开始错位。官方解析后 offset 就是 1。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// parityWorkspaceServer 造一个挂了最小 memory 实现的 Server（只为打 transport 层）。
func parityWorkspaceServer(t *testing.T, api OfficialWorkspaceAPI) *Server {
	t.Helper()
	srv, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.AttachOfficialWorkspaceAPI(api)
	t.Cleanup(srv.DetachOfficialWorkspaceAPI)
	return srv
}

// parityCredentialStore 是一个"只为让 transport 层走通"的凭据后端：
// 它不写任何地方，set/unset 按夹具配置失败。真实凭据存储的行为在
// cmd/dshd 与 internal/llm 的测试里。
type parityCredentialStore struct {
	setErr error
}

func (s *parityCredentialStore) Describe(refs []string) (map[string]OfficialCredentialInfo, error) {
	out := make(map[string]OfficialCredentialInfo, len(refs))
	for _, ref := range refs {
		out[ref] = OfficialCredentialInfo{Configured: false, Writable: true}
	}
	return out, nil
}

func (s *parityCredentialStore) Set(ref, value string) error { return s.setErr }
func (s *parityCredentialStore) Unset(ref string) error      { return nil }

// parityCall 打一次 /api/<method>，返回 HTTP 状态与解码后的信封。
func parityCall(t *testing.T, srv *Server, method, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/"+method, strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.serveOfficialWorkspaceAPI(rec, req, method)
	var out map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s 的响应不是 JSON 对象: %q", method, rec.Body.String())
		}
	}
	return rec.Code, out
}

// mustFourField 断言四字段错误体，并返回它的 message 与四字段 code。
func mustFourField(t *testing.T, method string, code int, out map[string]any) (string, string) {
	t.Helper()
	obj, ok := out["error"].(map[string]any)
	if !ok {
		t.Fatalf("%s 的错误响应没有 error 对象：%#v", method, out)
	}
	for _, key := range []string{"code", "message", "hint", "retryable"} {
		if _, has := obj[key]; !has {
			t.Fatalf("%s 的错误体缺 %q（四字段契约）：%#v", method, key, obj)
		}
	}
	msg, _ := obj["message"].(string)
	short, _ := obj["code"].(string)
	_ = code
	return msg, short
}

// TestOfficialWorkspaceFilesReadRangeDefaultsToOne 官方 resolvePage 的第一行：
//
//	`const offset = range.offset === void 0 ? 1 : integerAtLeast(range.offset, 1, "offset")`
//
// offset **缺省就是 1**。本仓把 offset 当 int64 零值：宿主只看到
// `HasOffset=false` 时自己补 1，但 `OfficialWorkspaceFileRange.Offset` 仍是 0；
// 任何直接读 `r.Offset` 的实现（含残留的 readBytes 路径与宿主自算 start=offset-1）
// 都会得到 -1，页从"第 0 行"开始错位。
func TestOfficialWorkspaceFilesReadRangeDefaultsToOne(t *testing.T) {
	var got OfficialWorkspaceFileRange
	api := OfficialWorkspaceAPI{
		FilesRead: func(scope, path string, r OfficialWorkspaceFileRange) (OfficialWorkspaceFileText, error) {
			got = r
			return OfficialWorkspaceFileText{Offset: 1, Text: "", Lines: 0, EOF: false,
				AbsolutePath: "/w/" + path, Version: "v"}, nil
		},
	}
	srv := parityWorkspaceServer(t, api)

	// 1) 完全省略 range：官方按 {} 处理，offset=1（首页）。
	code, out := parityCall(t, srv, "workspaceFiles.read", `{"workspaceFileScopeId":"s1","path":"a.txt"}`)
	if code != http.StatusOK {
		msg, _ := mustFourField(t, "workspaceFiles.read", code, out)
		t.Fatalf("省略 range 必须按官方默认值处理（offset=1），实际 %d：%s", code, msg)
	}
	if got.HasOffset {
		t.Fatalf("省略 offset 时 HasOffset 必须是 false（由宿主套默认值）：%+v", got)
	}
	if got.Offset != 1 {
		t.Fatalf("省略 offset 时解析结果的 Offset 必须已是官方默认值 1（当前 %d，宿主拿到零值 0 会算成 start=-1）：%+v",
			got.Offset, got)
	}
}
