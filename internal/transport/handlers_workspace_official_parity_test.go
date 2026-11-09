package transport

// HS-16B 先红（workspace 域，宿主无关的判定层）：官方与实现在**参数默认值**上
// 不一致，且错误码词表把同一件事说成了两件事。
//
// 契约原件（只读镜像）：
//
//	dsh-api-workspace-files/lib/typert.host.js:265  'workspaceFiles/read' 的 `range`
//	  是**位置参数**（`source:'json'`，`z.object({offset?,limit?})`），不是"可选
//	  参数省略"——省略它等于没给这个位置参数，官方按 `acceptsUndefined` 判定：
//	  `range` 允许缺省（zod `z.undefined()` 分支），缺省时以 {} 参与默认值。
//	dsh-api-workspace-files/lib/index.js:236  resolvePage(range)：
//	  `range.offset === void 0 ? 1 : …`，所以 offset 缺省 = 首页。
//	dsh-api-workspace-controller/lib/index.js:1xx  workspace/create 的
//	  `request.path` 是 `z.string()`（非空即可，存在性由 registry 判）；
//	  workspace/rename 的 `request.title` 官方在 controller 内 trim 后判空。
//	dsh-api-workspace-files/lib/index.js:591  locateFile：
//	  非普通文件（目录 / symlink / 其它）→ `workspace-file/not-regular-file`；
//	  官方**没有** `workspaceFiles/not-a-file` 这个码，本仓私有的
//	  `workspaceFiles/symlink-refused` 也不是官方码。
//
// 红点（当前实现与官方不一致）：
//
//	1. read 缺 `range` → 本仓 400「缺 range」；官方按 {} 处理（offset=1、
//	   limit=maxLines）。客户端少给一个可选对象就被拒，是**假严格**。
//	2. 本仓把官方码改写成 `workspaceFiles/not-a-file` /
//	   `workspaceFiles/symlink-refused` / `workspaceFiles/not-a-directory` /
//	   `workspaceFiles/outside-workspace` / `workspace-file/too-large` 等，
//	   与本包其他官方族一律**原样回官方码**（workspace/not-found、
//	   workspace/session-active、directory-picker/exists…）的口径不一致：
//	   客户端按官方码分支时，这一族全部落空。

import (
	"net/http"
	"strings"
	"testing"
)

// TestOfficialWorkspaceFilesReadOmittingRangeUsesDefaults 官方缺省 range 等价于 `{}`。
//
// 红：当前实现 400「缺 range（官方的 range 参数必填；没有窗口就给 {}）」。
func TestOfficialWorkspaceFilesReadOmittingRangeUsesDefaults(t *testing.T) {
	var got OfficialWorkspaceFileRange
	var called bool
	api := OfficialWorkspaceAPI{
		FilesRead: func(scope, path string, r OfficialWorkspaceFileRange) (OfficialWorkspaceFileText, error) {
			called, got = true, r
			return OfficialWorkspaceFileText{
				Offset: 1, Text: "l1", Lines: 1, EOF: true,
				AbsolutePath: "/w/" + path, Version: "v1",
			}, nil
		},
	}
	srv := parityWorkspaceServer(t, api)

	code, out := parityCall(t, srv, "workspaceFiles.read",
		`{"workspaceFileScopeId":"s1","path":"notes.txt"}`)
	if code != http.StatusOK {
		msg, _ := mustFourField(t, "workspaceFiles.read", code, out)
		t.Fatalf("省略 range 必须按 {} 处理（官方 resolvePage 的缺省 offset=1/limit=maxLines），实际 %d：%s",
			code, msg)
	}
	if !called {
		t.Fatalf("省略 range 的调用也必须进宿主（默认值由宿主按配置上限套）")
	}
	if got.HasOffset || got.HasLimit {
		t.Fatalf("省略 range 时不得伪造成显式给了值：%+v", got)
	}
}

// TestOfficialWorkspaceFilesErrorsKeepOfficialFamily 错误 message 必须原样带
// **官方族名**（`workspaceFiles/…` 或官方 `workspace-file/…`）。
//
// 红：本仓把官方并没有的 `workspaceFiles/not-a-file`、`workspaceFiles/not-a-directory`、
// `workspaceFiles/symlink-refused` 当官方码用；而官方 workspace-files 的码族是
// `workspace-file/not-regular-file|not-directory|not-found|outside-workspace|too-large|not-text`
// （`lib/index.js` 的 11 个 RemoteError 全用这个前缀）。客户端按官方族名分支时
// 本仓的 5 个私有码全部落空。
func TestOfficialWorkspaceFilesErrorsKeepOfficialFamily(t *testing.T) {
	// 传递层不许把宿主给的官方码改写成别的族：workspaceFiles/ 与 workspace-file/
	// 都是"这一族"，两者都必须原样出现在 message 里。
	for _, fault := range []string{
		"workspaceFiles/too-large",
		"workspace-file/too-large",
		"workspace-file/not-regular-file",
	} {
		api := OfficialWorkspaceAPI{
			FilesStat: func(scope, path string) (OfficialWorkspaceFileStat, error) {
				return OfficialWorkspaceFileStat{}, OfficialWorkspaceError(fault, "宿主拒绝：%s", fault)
			},
		}
		srv := parityWorkspaceServer(t, api)
		code, out := parityCall(t, srv, "workspaceFiles.stat",
			`{"workspaceFileScopeId":"s1","path":"a.txt"}`)
		if code < 400 {
			t.Fatalf("%s 必须失败，实际 %d", fault, code)
		}
		msg, _ := mustFourField(t, "workspaceFiles.stat", code, out)
		if !strings.Contains(msg, fault) {
			t.Fatalf("错误 message 必须原样带官方码 %q，实际 %q", fault, msg)
		}
		if strings.Contains(msg, "workspaceFiles/"+strings.TrimPrefix(fault, "workspace-file/")) &&
			strings.HasPrefix(fault, "workspace-file/") {
			t.Fatalf("官方 workspace-file/ 码被改写成 workspaceFiles/ 私码：%q", msg)
		}
	}
}
