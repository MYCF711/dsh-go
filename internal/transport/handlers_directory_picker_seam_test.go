package transport

// directoryPicker 的**互斥组合 seam** 契约测试。
//
// 官方把目录选择做成两套互斥后端（native 的 `pick` / browse 的
// `list`+`createDirectory`），一个 Context 只能组合一套；另一套的动词以
// `directory-picker/unavailable` 拒绝并带 `{capability}`。原件：
//
//	dsh-host-directory-picker/src/index.ts:19-28 / :35-59 / :96-113
//	dsh-api-workspace-controller/src/directory-picker.ts:54-62 / :105-119
//
// 这里钉四件事：
//  1. 声明 native 时两个 browse 动词**拒绝**（且一个字节都不落盘）；
//  2. 声明 browse 时 pick **拒绝**（且原生服务一次都不被调用）；
//  3. 拒绝的形状是官方的：code 前缀 `directory-picker/unavailable` + 顶层
//     `details.capability` = 本次声明的 kind；
//  4. `Attach` 拒绝非法声明（两套同时挂 / 缺原语 / 非法 kind）——不是静默接受。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seamRefusal 断言一次拒绝是官方形状，返回 details.capability。
func seamRefusal(t *testing.T, method string, code int, out map[string]any, wantMethod, wantServed string) string {
	t.Helper()
	if code != http.StatusNotImplemented {
		t.Fatalf("%s 应 501（宿主没有这个交互），实际 %d（%v）", method, code, out)
	}
	assertFourFieldError(t, code, out, http.StatusNotImplemented, "not_implemented")
	errObj, ok := out["error"].(map[string]any)
	if !ok {
		t.Fatalf("%s 的错误信封缺 error 对象：%v", method, out)
	}
	msg, _ := errObj["message"].(string)
	if !strings.HasPrefix(msg, "directory-picker/unavailable: ") {
		t.Fatalf("%s 的 message 必须以官方码开头（客户端按它分支）：%q", method, msg)
	}
	// 官方文案逐字：`directoryPicker.<method> needs the <kind> capability; …`
	if !strings.Contains(msg, "directoryPicker."+wantMethod+" needs the ") {
		t.Fatalf("%s 的 message 没点名动词与缺的 kind：%q", method, msg)
	}
	details, ok := out["details"].(map[string]any)
	if !ok {
		t.Fatalf("%s 的拒绝缺顶层 details（官方 {capability}）：%v", method, out)
	}
	served, _ := details["capability"].(string)
	if served != wantServed {
		t.Fatalf("%s 的 details.capability = %q，期望 %q（客户端靠它二选一）", method, served, wantServed)
	}
	return served
}

// TestDirectoryPickerSeamNativeRefusesBrowseVerbs：声明 native 的宿主上，
// list / createDirectory 是"不属于本次组合"的动词——必须拒绝，且**不落盘**。
func TestDirectoryPickerSeamNativeRefusesBrowseVerbs(t *testing.T) {
	// 装配期：按 native 声明挂上——browse 那两个原语**根本没给**，所以"拒绝"
	// 不能是靠"原语是 nil"实现的假拒绝，而必须是按 kind 门控出来的。
	srv := officialWorkspaceTestServer(t, OfficialWorkspaceAPI{}, OfficialDirectoryPickerSeam{
		Kind: DirectoryPickerKindNative,
	})

	code, out := callOfficialWorkspace(t, srv, "directoryPicker.list", `{}`)
	seamRefusal(t, "directoryPicker.list", code, out, "list", "native")

	// createDirectory 的父目录用真目录：能过参数校验，才走到 seam 的拒绝。
	parent := t.TempDir()
	code, out = callOfficialWorkspace(t, srv, "directoryPicker.createDirectory", map[string]any{
		"path": parent, "name": "seam-must-not-create",
	})
	seamRefusal(t, "directoryPicker.createDirectory", code, out, "createDirectory", "native")
	if _, err := os.Stat(filepath.Join(parent, "seam-must-not-create")); !os.IsNotExist(err) {
		t.Fatalf("被拒的 createDirectory 竟然落盘了：%v", err)
	}
}

// TestDirectoryPickerSeamBrowseRefusesPick：声明 browse 的宿主上，pick 必须拒绝，
// 而且原生服务**一次都不许被调用**（"只能选一个"不是"两个都做、后做的报错"）。
func TestDirectoryPickerSeamBrowseRefusesPick(t *testing.T) {
	srv, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	var picks int
	srv.AttachOfficialNativeAPI(OfficialNativeAPI{
		PickDirectory: func(context.Context) (string, bool, error) {
			picks++
			return `C:\should-not-open`, true, nil
		},
	})
	t.Cleanup(srv.DetachOfficialNativeAPI)
	if err := srv.AttachOfficialDirectoryPickerSeam(OfficialDirectoryPickerSeam{
		Kind:            DirectoryPickerKindBrowse,
		List:            func(string) (OfficialDirectoryListing, error) { return OfficialDirectoryListing{}, nil },
		CreateDirectory: func(path, name string) (string, error) { return filepath.Join(path, name), nil },
	}); err != nil {
		t.Fatalf("挂 browse 组合: %v", err)
	}
	t.Cleanup(srv.DetachOfficialDirectoryPickerSeam)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/directoryPicker.pick", strings.NewReader(`{}`))
	srv.ServeOfficialNativeAPI(rec, req, string(MethodDirectoryPickerPick))
	var out map[string]any
	if rec.Body.Len() > 0 {
		if uerr := json.Unmarshal(rec.Body.Bytes(), &out); uerr != nil {
			t.Fatalf("pick 的响应不是 JSON 对象: %q", rec.Body.String())
		}
	}
	seamRefusal(t, "directoryPicker.pick", rec.Code, out, "pick", "browse")
	if picks != 0 {
		t.Fatalf("被拒的 pick 仍然调用了原生服务 %d 次", picks)
	}
}

// TestDirectoryPickerSeamBrowseServesAndNativeServesPick：两套各自的成功面仍在
// （拒绝只针对"不属于本次组合"的动词，不是把能力削掉）。
func TestDirectoryPickerSeamBrowseServesAndNativeServesPick(t *testing.T) {
	browseSrv := officialWorkspaceTestServer(t, OfficialWorkspaceAPI{}, OfficialDirectoryPickerSeam{
		Kind: DirectoryPickerKindBrowse,
		List: func(string) (OfficialDirectoryListing, error) {
			return OfficialDirectoryListing{Path: "C:\\home", Home: "C:\\home"}, nil
		},
		CreateDirectory: func(path, name string) (string, error) { return filepath.Join(path, name), nil },
	})
	code, out := callOfficialWorkspace(t, browseSrv, "directoryPicker.list", `{}`)
	if code != http.StatusOK {
		t.Fatalf("browse 组合的 list 应 200，实际 %d（%v）", code, out)
	}

	pickSrv, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	pickSrv.AttachOfficialNativeAPI(OfficialNativeAPI{
		PickDirectory: func(context.Context) (string, bool, error) { return `C:\picked`, true, nil },
	})
	t.Cleanup(pickSrv.DetachOfficialNativeAPI)
	if aerr := pickSrv.AttachOfficialDirectoryPickerSeam(OfficialDirectoryPickerSeam{
		Kind: DirectoryPickerKindNative,
	}); aerr != nil {
		t.Fatalf("挂 native 组合: %v", aerr)
	}
	t.Cleanup(pickSrv.DetachOfficialDirectoryPickerSeam)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/directoryPicker.pick", strings.NewReader(`{}`))
	pickSrv.ServeOfficialNativeAPI(rec, req, string(MethodDirectoryPickerPick))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `C:\\picked`) {
		t.Fatalf("native 组合的 pick 应 200 且回真实路径，实际 %d %q", rec.Code, rec.Body.String())
	}
}

// TestDirectoryPickerSeamAttachRejectsIllegalComposition：装配期就要红，
// 不许"同时挂两套但先声明一个"这种半真状态。
func TestDirectoryPickerSeamAttachRejectsIllegalComposition(t *testing.T) {
	list := func(string) (OfficialDirectoryListing, error) { return OfficialDirectoryListing{}, nil }
	create := func(path, name string) (string, error) { return filepath.Join(path, name), nil }

	cases := []struct {
		name string
		seam OfficialDirectoryPickerSeam
		want string
	}{
		{
			name: "两套同时挂（native 带 browse 原语）",
			seam: OfficialDirectoryPickerSeam{Kind: DirectoryPickerKindNative, List: list, CreateDirectory: create},
			want: "native",
		},
		{
			name: "browse 缺 createDirectory",
			seam: OfficialDirectoryPickerSeam{Kind: DirectoryPickerKindBrowse, List: list},
			want: "browse",
		},
		{
			name: "browse 缺 list",
			seam: OfficialDirectoryPickerSeam{Kind: DirectoryPickerKindBrowse, CreateDirectory: create},
			want: "browse",
		},
		{
			name: "kind 空串（没声明组合）",
			seam: OfficialDirectoryPickerSeam{List: list, CreateDirectory: create},
			want: "kind",
		},
		{
			name: "kind 拼错",
			seam: OfficialDirectoryPickerSeam{Kind: "auto"},
			want: "auto",
		},
	}
	for _, c := range cases {
		srv, err := NewServer(Options{})
		if err != nil {
			t.Fatalf("%s: NewServer: %v", c.name, err)
		}
		err = srv.AttachOfficialDirectoryPickerSeam(c.seam)
		if err == nil {
			t.Fatalf("%s: Attach 应报错，实际接受了（声明=%q）", c.name, string(c.seam.Kind))
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: 错误文案没点名原因 %q：%v", c.name, c.want, err)
		}
		// 被拒的装配一个字段都不许写进去：这个 Server 仍然"没声明组合"。
		if kind, declared := srv.officialDirectoryPickerKind(); declared {
			t.Fatalf("%s: 装配被拒后却留下了 kind=%q", c.name, string(kind))
		}
	}
}

// TestDirectoryPickerSeamDiscoveryIsReadOnly：客户端"问出组合"的方式是打只读
// 动词 list——200 = browse；501 + details.capability = 本次声明的另一套。
// 两条路都不许弹原生框、不许写盘。
func TestDirectoryPickerSeamDiscoveryIsReadOnly(t *testing.T) {
	nativeSrv := officialWorkspaceTestServer(t, OfficialWorkspaceAPI{}, OfficialDirectoryPickerSeam{
		Kind: DirectoryPickerKindNative,
	})
	code, out := callOfficialWorkspace(t, nativeSrv, "directoryPicker.list", `{}`)
	if got := seamRefusal(t, "directoryPicker.list", code, out, "list", "native"); got != "native" {
		t.Fatalf("问出来的 kind = %q", got)
	}

	browseSrv := officialWorkspaceTestServer(t, OfficialWorkspaceAPI{}, OfficialDirectoryPickerSeam{
		Kind:            DirectoryPickerKindBrowse,
		List:            func(string) (OfficialDirectoryListing, error) { return OfficialDirectoryListing{}, nil },
		CreateDirectory: func(path, name string) (string, error) { return filepath.Join(path, name), nil },
	})
	code, out = callOfficialWorkspace(t, browseSrv, "directoryPicker.list", `{}`)
	if code != http.StatusOK {
		t.Fatalf("browse 组合的 list 应 200（客户端据此判定 browse），实际 %d（%v）", code, out)
	}
}
