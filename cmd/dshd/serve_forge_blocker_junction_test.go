//go:build windows

package main

// `worktree.blocker` 的 junction 盲区（写边界）。
//
// 这个 blocker 直接决定界面敢不敢把写操作放给当前 checkout：`outside-writable-root`
// 一旦被漏判成 null，写就落到可写根之外。而判定用的是 `forgeCanonicalDir` +
// `officialPathWithin`，前者在 Go ≥1.23 的 `winsymlink=1` 下如果只靠
// `filepath.EvalSymlinks`，就**跟不上目录 junction**（实测见下方用例：
// junction 本身被原样返回，junction 下面的路径直接 errno=3），于是：
//
//	<ws>\link            （link → C:\…\outside）  → 判成在 <ws> 内 → blocker null
//	<ws>\link\repo       （同上，根在 junction 下）→ 同样判成在内
//
// 两条都必须判成 `outside-writable-root`。反向控制同样重要：junction 指向
// **可写根里面**的目录时必须还是 null——修复不能靠"更保守"换来一片红。

import (
	"os"
	"path/filepath"
	"testing"
)

// TestForgePanelBlockerRejectsCheckoutBehindJunction 钉住 junction 盲区本身。
//
// 修复前两条都返回 ""（= blocker null = 写被放行）；修复后必须
// `outside-writable-root`。
func TestForgePanelBlockerRejectsCheckoutBehindJunction(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "ws")
	outside := filepath.Join(base, "outside")
	for _, dir := range []string{ws, filepath.Join(outside, "repo")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(ws, "link")
	makeTestJunction(t, link, outside)

	// 事实的两种形态：checkout 根**就是** junction，以及根在 junction **下面**。
	// git 自己两处都会按"调用方给的拼法"回话，所以两种都必须被判否。
	for i, checkoutRoot := range []string{link, filepath.Join(link, "repo")} {
		t.Run([]string{"junction-is-root", "under-junction"}[i], func(t *testing.T) {
			h := newBlockerHost(t, &blockerFakeGit{root: checkoutRoot, commonDir: checkoutRoot},
				checkoutRoot, nil, fixedRoot(ws))
			if got := blockerFor(t, h); got != forgeBlockerOutsideWritableRoot {
				t.Fatalf("checkout %q 经 junction 落在可写根 %q 之外，blocker=%q，want %q"+
					"（junction 没被解析就是写边界漏了）",
					checkoutRoot, ws, got, forgeBlockerOutsideWritableRoot)
			}
		})
	}

	// 公共 .git 在根外的形态（linked worktree 的典型）：根在内、commonDir 经 junction
	// 落到外面，同样必须判否——`officialPathWithin` 那两条是**与**。
	h := newBlockerHost(t, &blockerFakeGit{root: filepath.Join(ws, "plain"), commonDir: link},
		filepath.Join(ws, "plain"), nil, fixedRoot(ws))
	if got := blockerFor(t, h); got != forgeBlockerOutsideWritableRoot {
		t.Fatalf("公共 .git 经 junction 落在可写根外，blocker=%q，want %q",
			got, forgeBlockerOutsideWritableRoot)
	}
}

// TestForgePanelBlockerAllowsJunctionInsideWritableRoot 反向控制。
//
// junction 的目标仍在可写根里 → 真实位置合法 → 必须仍是 null（不许多拒）。
// 普通目录那条同时钉住"正常路径一个字节都不变"。
func TestForgePanelBlockerAllowsJunctionInsideWritableRoot(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "ws")
	inner := filepath.Join(ws, "inner")
	plain := filepath.Join(ws, "plain")
	for _, dir := range []string{inner, plain} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(ws, "link-in")
	makeTestJunction(t, link, inner)

	for _, checkoutRoot := range []string{plain, link, filepath.Join(link, "…")} {
		h := newBlockerHost(t, &blockerFakeGit{root: checkoutRoot, commonDir: checkoutRoot},
			checkoutRoot, nil, fixedRoot(ws))
		if got := blockerFor(t, h); got != "" {
			t.Fatalf("checkout %q 的真实位置在可写根 %q 内，blocker=%q，want null",
				checkoutRoot, ws, got)
		}
	}
}
