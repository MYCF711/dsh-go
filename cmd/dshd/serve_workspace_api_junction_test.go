//go:build windows

package main

// `workspaceFiles.*` 读边界对目录 junction 的盲区。
//
// `officialResolveWithin` 先用词法比较挡 `..`，再按**真实解析后**的路径复核一次
// 符号链接/junction 逃逸。第二道复核如果只靠 `filepath.EvalSymlinks`，在 Go ≥1.23
// 的 `winsymlink=1` 下对目录 junction 完全不起作用（junction 本身原样返回、
// junction 下面的路径直接 ERROR_PATH_NOT_FOUND / 实测 errno=3），于是
//
//	<root>\link-out\secret.txt     （link-out → 工作区外的目录）
//
// 会被当成工作区内路径**读出去**。三个读方法（read / list / stat）共用这条路径，
// 所以三个都要钉。反向控制同样要钉：junction 指向工作区**里面**时官方的
// symlink 语义是跟随（不是拒绝），修复不许把它一起判死。

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// workspaceScopeID 是本组用例的会话 id（每个用例独立 home + 独立 id）。
func junctionScopeSession(t *testing.T, h *officialWorkspaceHarness, id string) (root, outside string) {
	t.Helper()
	base := t.TempDir()
	root = newOfficialWorkspaceDir(t, base, "root")
	outside = newOfficialWorkspaceDir(t, base, "outside")
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("outside-secret\n"), 0o644); err != nil {
		t.Fatalf("写工作区外文件: %v", err)
	}
	newRealSession(t, h.home, root, id)
	return root, outside
}

// TestOfficialWorkspaceFilesRefuseReadThroughJunction 读族三方法经 junction 越界必须拒。
func TestOfficialWorkspaceFilesRefuseReadThroughJunction(t *testing.T) {
	h := newOfficialWorkspaceHarness(t)
	const id = "ffffffff-0000-0000-0000-00000000j001"
	root, outside := junctionScopeSession(t, h, id)
	link := filepath.Join(root, "link-out")
	makeTestJunction(t, link, outside)

	cases := []struct {
		method string
		body   map[string]any
	}{
		{"workspaceFiles.read", map[string]any{
			"workspaceFileScopeId": id, "path": "link-out/secret.txt", "range": map[string]any{},
		}},
		{"workspaceFiles.list", map[string]any{
			"workspaceFileScopeId": id, "path": "link-out",
		}},
		{"workspaceFiles.stat", map[string]any{
			"workspaceFileScopeId": id, "path": "link-out/secret.txt",
		}},
	}
	for _, c := range cases {
		msg := h.fail(t, c.method, c.body, http.StatusBadRequest)
		if !strings.Contains(msg, "workspaceFiles/outside-workspace") {
			t.Fatalf("%s 经 junction 读到工作区外，错误=%q，want workspaceFiles/outside-workspace"+
				"（junction 没被解析 = 读边界漏了）", c.method, msg)
		}
	}

	// 内容本身没被发出去：read 的结果里不许出现工作区外的正文。
	code, out := h.call(t, "workspaceFiles.read", map[string]any{
		"workspaceFileScopeId": id, "path": "link-out/secret.txt", "range": map[string]any{},
	})
	if code == http.StatusOK {
		t.Fatalf("工作区外文件被读出来了：%v", out)
	}
}

// TestOfficialWorkspaceFilesFollowJunctionInsideWorkspace 反向控制。
//
// junction 的目标仍在工作区里 → 官方语义是"跟随目录"（与 symlink 同一条），
// list 要能列、read 要能读，不许因为修复变成一律拒绝。
func TestOfficialWorkspaceFilesFollowJunctionInsideWorkspace(t *testing.T) {
	h := newOfficialWorkspaceHarness(t)
	const id = "ffffffff-0000-0000-0000-00000000j002"
	root := newOfficialWorkspaceDir(t, t.TempDir(), "root")
	inner := filepath.Join(root, "inner")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inner, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	newRealSession(t, h.home, root, id)
	link := filepath.Join(root, "link-in")
	makeTestJunction(t, link, inner)

	res := h.ok(t, "workspaceFiles.list", map[string]any{
		"workspaceFileScopeId": id, "path": "link-in",
	})
	entries, _ := res["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("工作区内的目录 junction 必须能列（官方 list 跟随目录）：%v", res)
	}
	if entry, _ := entries[0].(map[string]any); entry["name"] != "a.txt" {
		t.Fatalf("子项不对：%v", res)
	}

	// 普通路径一个字节都不变：走同一条 officialResolveWithin。
	res = h.ok(t, "workspaceFiles.read", map[string]any{
		"workspaceFileScopeId": id, "path": "inner/a.txt", "range": map[string]any{},
	})
	if res["text"] != "a" {
		t.Fatalf("工作区内普通路径的读语义必须照旧：%v", res)
	}
}
