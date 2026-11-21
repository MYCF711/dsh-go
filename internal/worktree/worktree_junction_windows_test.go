//go:build windows

package worktree

// `canonicalPath` 的 junction 拼法：**不改**，但要用实测证明失败方向是保守的。
//
// `canonicalPath` 只回答"这两条路径是不是同一个目录"（`samePath`），它出现在
// `inspectRemoval`（归档会话时是否允许 `git worktree remove`）的两处：
//
//	archive.go:442   if !samePath(strings.TrimSpace(top), root) { return false, nil }   // 不删
//	archive.go:535   if samePath(worktree list 的条目, path) { 返回条目 } else → nil    // 不删
//
// filepath.EvalSymlinks 在 Go ≥1.23 的 `winsymlink=1` 下不跟目录 junction，
// 于是同一个目录的 junction 拼法与真实拼法会被判成**不相等**。关键在于这个偏差
// 只有"该相等时说不相等"这一个方向：
//   - 两个调用点都把 false 当成"不满足身份判据" → **保留 checkout，不删**；
//   - 反向（把两个**不同**目录判成相等）不可能由"少解析"造成，所以不存在
//     "把别处的目录当成池内 checkout 删掉"这条路。
//
// 删掉的那一侧还有 git 自己的判据兜着（判据 2-6：`worktree list --porcelain` 里必须
// 有这条 path 的条目、HEAD 必须是这条分支、干净、无 submodule 改动），本函数不是
// 唯一防线。收益是"改错会更保守"，风险是"为了更少false再换来新的判定分支"——
// 不抵，所以如实不改。

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/sandbox"
)

// makeWorktreeJunction 建一个目录 junction；环境不支持时 SKIP。
func makeWorktreeJunction(t *testing.T, link, target string) {
	t.Helper()
	out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Skipf("本机建不了目录 junction，跳过这条方向断言：%v（%s）",
			err, strings.TrimSpace(string(out)))
	}
	resolved, rerr := sandbox.ResolveExistingPath(link)
	if rerr != nil || strings.EqualFold(filepath.Clean(resolved), filepath.Clean(link)) {
		t.Skipf("junction 建了但解析不到目标（resolved=%q err=%v）", resolved, rerr)
	}
}

func TestSamePathJunctionDeviationIsConservative(t *testing.T) {
	base := t.TempDir()
	realDir := filepath.Join(base, "repo")
	otherDir := filepath.Join(base, "other")
	for _, dir := range []string{realDir, otherDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(base, "repo-link")
	makeWorktreeJunction(t, link, realDir)

	viaReal := canonicalPath(realDir)
	viaLink := canonicalPath(link)

	// 前提：canonicalPath 停在 junction 拼法上（这就是"没解析"的事实）。
	if strings.EqualFold(viaReal, viaLink) {
		t.Fatalf("前提不成立：junction 拼法被解析成了真实路径（%q）", viaReal)
	}

	// 同一拼法必须相等（samePath 本身没坏）。
	if !samePath(viaReal, viaReal) {
		t.Fatalf("同一条路径必须与自身相等：canonicalPath=%q", viaReal)
	}

	// 唯一的偏差方向：该相等时说**不相等** → 两个调用点都落到"不删"。
	// 这条断言就是在钉"偏差只往保守那边偏"。
	if samePath(viaLink, viaReal) {
		t.Fatalf("junction 拼法被解析了（%q == %q），本用例的前提不再成立——"+
			"请连同 archive.go 的注释一起复核", viaLink, viaReal)
	}

	// 危险方向（把两个**不同**目录判成相等）必须不成立：那样会把别处的 checkout
	// 当成池内 worktree 交给 `git worktree remove`。
	if samePath(link, otherDir) {
		t.Fatalf("不同目录被判成相等（%q, %q）：那是唯一不安全的方向", link, otherDir)
	}
	if samePath(viaLink, canonicalPath(otherDir)) {
		t.Fatalf("不同目录的 canonical 被判成相等（%q, %q）", viaLink, canonicalPath(otherDir))
	}
}
