//go:build windows

package main

// 「宿主刚启动的 PID 映像归属校验」在 junction 拼法下是 **fail-closed** 的（不改）。
//
// `canonicalProcessImage` 用的是 filepath.EvalSymlinks，所以 Go ≥1.23 的
// `winsymlink=1` 下它不会把 junction 拼法解析成真实路径。这里不去改它，理由不是
// "改了麻烦"，而是**失败方向是安全的那一侧**：
//
//	期望值（registerTurnRun 传进来的 canonical）与实测值（QueryFullProcessImageName
//	返回的真实路径）都走同一个 canonicalProcessImage；junction 拼法只会让两者
//	**不相等** → verifyProcessImage 报错 → 拒绝登记。永远不会把"另一个映像"
//	判成"宿主自己启动的那个"。
//
// 这条用例把两半都钉住：真实拼法必须通过（校验本身可用），junction 拼法必须被拒
// （不是放行）。修成"解析 junction"只会让合法拼法通过，安全上零收益——所以不动。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProcessImageOwnershipIsFailClosedOnJunctionSpelling(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	realExe, err := filepath.EvalSymlinks(exe)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", exe, err)
	}
	pid := os.Getpid()

	// 1) 正控：真实拼法必须通过。证明这条校验对真实身份是能用的（不是"永远报错"）。
	canonicalReal, err := canonicalProcessImage(exe)
	if err != nil {
		t.Fatalf("canonicalProcessImage(自己): %v", err)
	}
	if err := verifyProcessImage(pid, canonicalReal); err != nil {
		t.Fatalf("真实拼法的影像归属校验必须通过：%v", err)
	}

	// 2) junction 拼法：canonicalProcessImage 停在 junction 拼法上（这就是"没解析"的事实），
	//    所以它与查出来的真实映像不相等。
	link := filepath.Join(t.TempDir(), "exe-link")
	makeTestJunction(t, link, filepath.Dir(realExe))
	viaLink, err := canonicalProcessImage(filepath.Join(link, filepath.Base(realExe)))
	if err != nil {
		t.Fatalf("canonicalProcessImage(junction 拼法): %v", err)
	}
	if strings.EqualFold(viaLink, canonicalReal) {
		t.Fatalf("前提不成立：junction 拼法被解析成了真实路径（%q），这条用例就没在测 junction",
			viaLink)
	}

	// 3) 关键：于是 verifyProcessImage 必须**报错**。放行才是不安全的那个方向。
	if err := verifyProcessImage(pid, viaLink); err == nil {
		t.Fatalf("junction 拼法被当成了同一个映像放行（expected=%q）：那是 fail-open", viaLink)
	} else if !strings.Contains(err.Error(), "期望") {
		t.Fatalf("拒绝理由应该是「映像不匹配」，实际：%v", err)
	}
}
