//go:build windows

package main

// `workspaceChangesCanonical` 的 junction 盲区（显示/路径归一，不是放行判定）。
//
// 这个函数的存在理由就是"同一个目录只有一种拼法"：它喂给 `filepath.Rel` 算
// deliverables 卡片的显示路径与工作区相对路径。只靠 filepath.EvalSymlinks 时，
// Go ≥1.23 的 `winsymlink=1` 让 junction 拼法与真实路径被当成两棵树（EvalSymlinks
// 对 junction 下面的路径直接 errno=3），于是和 8.3 短名那个老问题一样，每条 path
// 都退化成 `..\..\…` 的垃圾。
//
// 注意这里的性质：它**不决定能不能写到哪**，只决定显示成什么、git 用哪个目录跑。
// 所以断言钉的是"同目录一种拼法"和"普通路径一字不差"，不是拦截。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWorkspaceChangesCanonicalCollapsesJunction 同一个目录只许一种拼法。
func TestWorkspaceChangesCanonicalCollapsesJunction(t *testing.T) {
	realDir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(realDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "repo-link")
	makeTestJunction(t, link, realDir)

	viaReal := workspaceChangesCanonical(realDir)
	viaLink := workspaceChangesCanonical(link)
	if !strings.EqualFold(filepath.Clean(viaReal), filepath.Clean(viaLink)) {
		t.Fatalf("同一个目录的两种拼法没被折叠：real=%q link=%q"+
			"（显示路径会退化成 ..\\..\\…）", viaReal, viaLink)
	}
	// junction **下面**的路径同样要落到真实目录里（EvalSymlinks 在这个形态上
	// 直接报错，会退回原始拼法）。
	viaLinkSub := workspaceChangesCanonical(filepath.Join(link, "sub"))
	viaRealSub := workspaceChangesCanonical(filepath.Join(realDir, "sub"))
	if !strings.EqualFold(filepath.Clean(viaLinkSub), filepath.Clean(viaRealSub)) {
		t.Fatalf("junction 下的子路径没折到真实路径：link=%q real=%q", viaLinkSub, viaRealSub)
	}
}

// TestWorkspaceChangesCanonicalPlainPathUnchanged 反向控制：普通路径一字不差。
func TestWorkspaceChangesCanonicalPlainPathUnchanged(t *testing.T) {
	dir := t.TempDir()
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if got := workspaceChangesCanonical(dir); got != filepath.Clean(want) {
		t.Fatalf("普通路径的规范形态变了：got=%q want=%q（修前修后必须一致）",
			got, filepath.Clean(want))
	}
	// 不存在的路径走同一条回退（Abs + Clean），也必须不变。
	missing := filepath.Join(dir, "not-yet", "file.txt")
	if got, wantMissing := workspaceChangesCanonical(missing), filepath.Clean(missing); got != wantMissing {
		t.Fatalf("不存在的路径必须退回 Abs+Clean：got=%q want=%q", got, wantMissing)
	}
}
