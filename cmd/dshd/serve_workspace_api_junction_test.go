//go:build windows

package main

// workspaceFiles 的边界复核是**安全判据**（决定放不放行）：本文件头承诺
// "读路径一律先做词法边界检查，再对真实解析后的路径复核一次，因此 `..`、
// 绝对路径越界、符号链接逃逸都出不去工作区根"。
//
// 这个承诺在 Go ≥1.23 的 `winsymlink=1` 语义下对目录 junction 是假的：
// `filepath.EvalSymlinks` **不跟 junction**（原样返回），于是
// `root\escape-junction\secret.txt` 的"真实解析结果"看起来还在 root 里，
// 而 os.Open/os.ReadFile 会照着 junction 读到工作区外。
//
// 修复：边界复核改用 internal/sandbox.ResolveExistingPath（#120 的同一份实现：
// 先按 REPARSE_POINT 属性位探测，命中才用 GetFinalPathNameByHandleW；
// 普通路径仍走 EvalSymlinks，行为一字节不变）。

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOfficialWorkspaceFilesRejectsJunctionEscape 经 junction 的越界读/列目录/读字节
// 必须报 workspaceFiles/outside-workspace；工作区**内部**的 junction 不算逃逸。
func TestOfficialWorkspaceFilesRejectsJunctionEscape(t *testing.T) {
	h := newOfficialWorkspaceHarness(t)
	root := newOfficialWorkspaceDir(t, t.TempDir(), "root")
	outside := newOfficialWorkspaceDir(t, t.TempDir(), "outside")
	id := "dddddddd-0000-0000-0000-0000000000a1"
	newRealSession(t, h.home, root, id)

	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("nope\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	makeTestJunction(t, filepath.Join(root, "escape-junction"), outside)

	// 变异点：把 sandbox.ResolveExistingPath 改回裸 filepath.EvalSymlinks，这三条
	// 都会红——修复前 resolvedTarget 仍是 root\escape-junction\...（junction 没被
	// 跟），词法上"还在工作区里"，read 会真的把 outside 的内容返回给客户端。
	for _, method := range []string{"workspaceFiles.read", "workspaceFiles.list", "workspaceFiles.readBytes"} {
		body := map[string]any{"workspaceFileScopeId": id, "path": "escape-junction/secret.txt"}
		switch method {
		case "workspaceFiles.read":
			body["range"] = map[string]any{}
		case "workspaceFiles.list":
			body["path"] = "escape-junction"
		case "workspaceFiles.readBytes":
			body["options"] = map[string]any{}
		}
		msg := h.fail(t, method, body, http.StatusBadRequest)
		if !strings.Contains(msg, "workspaceFiles/outside-workspace") {
			t.Fatalf("%s 经 junction 越界应报 workspaceFiles/outside-workspace：%q", method, msg)
		}
	}

	// 正控 1：根内普通文件照旧可读（别把正常路径一起判死）。
	if err := os.WriteFile(filepath.Join(root, "ok.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := h.ok(t, "workspaceFiles.read", map[string]any{
		"workspaceFileScopeId": id, "path": "ok.txt", "range": map[string]any{},
	})
	if res["text"] != "ok" {
		t.Fatalf("普通文件应照旧可读：%v", res)
	}

	// 正控 2：指向**工作区内**的 junction 不是逃逸（按解析后的真实路径判，不做
	// 一刀切拒绝）；并且 absolutePath 必须是解析后的真实路径——官方这一层是
	// Node `fs.realpath`，它跟 junction，而 Go 的 EvalSymlinks 不跟。
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(root, "sub", "inside.txt")
	if err := os.WriteFile(inside, []byte("inside"), 0o644); err != nil {
		t.Fatal(err)
	}
	makeTestJunction(t, filepath.Join(root, "inside-junction"), filepath.Join(root, "sub"))
	res = h.ok(t, "workspaceFiles.read", map[string]any{
		"workspaceFileScopeId": id, "path": "inside-junction/inside.txt", "range": map[string]any{},
	})
	if res["text"] != "inside" {
		t.Fatalf("工作区内的 junction 不该被拒：%v", res)
	}
	got, _ := res["absolutePath"].(string)
	want := resolvedOrSelf(inside)
	if !strings.EqualFold(filepath.Clean(got), filepath.Clean(want)) {
		t.Fatalf("absolutePath 应报 junction 解析后的真实路径：%q（期望 %q）", got, want)
	}
}
