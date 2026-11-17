//go:build windows

package terminal

// ConPTY 环境块的形状与去重（`CreateProcess(CREATE_UNICODE_ENVIRONMENT)` 的约定）。
//
// 真值证据在 `cmd/dshd/terminal_pty_agent_test.go`（起真 PTY、`echo $DSH_SESSION_ID`
// 回读）；这里只钉住"块是怎么拼的"，因为拼错的表现是"子进程环境缺变量"，而
// 那种失败在真机上是静默的。

import (
	"strings"
	"testing"
	"unicode/utf16"
)

// blockToStrings 把 UTF-16 环境块拆回 KEY=VALUE 列表（顺带校验双 NUL 结尾）。
func blockToStrings(t *testing.T, block []uint16) []string {
	t.Helper()
	if len(block) == 0 || block[len(block)-1] != 0 {
		t.Fatalf("环境块必须以 NUL 结尾，拿到 %v", block)
	}
	var out []string
	var cur []uint16
	for _, unit := range block {
		if unit == 0 {
			if len(cur) == 0 {
				break // 双 NUL = 结束
			}
			out = append(out, string(utf16.Decode(cur)))
			cur = cur[:0]
			continue
		}
		cur = append(cur, unit)
	}
	return out
}

// TestBuildEnvironmentBlockShape 钉住"每项 NUL 结尾 + 整块双 NUL + 按名字排序"。
func TestBuildEnvironmentBlockShape(t *testing.T) {
	block, err := buildEnvironmentBlock([]string{"B=2", "A=1", "C="})
	if err != nil {
		t.Fatalf("拼环境块失败：%v", err)
	}
	got := blockToStrings(t, block)
	want := []string{"A=1", "B=2", "C="}
	if len(got) != len(want) {
		t.Fatalf("要有 %d 项，拿到 %d：%v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 项要 %q（排序 + 空值保留），拿到 %q（整块 %v）", i, want[i], got[i], got)
		}
	}
}

// TestBuildEnvironmentBlockCaseInsensitiveLastWins 钉住 Windows 语义：
// 变量名不区分大小写，后出现的覆盖先出现的（与 Go `os/exec` 的 last-wins 一致）。
func TestBuildEnvironmentBlockCaseInsensitiveLastWins(t *testing.T) {
	block, err := buildEnvironmentBlock([]string{"path=first", "OTHER=1", "PATH=last"})
	if err != nil {
		t.Fatalf("拼环境块失败：%v", err)
	}
	got := blockToStrings(t, block)
	if len(got) != 2 {
		t.Fatalf("同名（忽略大小写）要合成一项，拿到 %v", got)
	}
	for _, entry := range got {
		if strings.HasPrefix(strings.ToUpper(entry), "PATH=") && entry != "PATH=last" {
			t.Fatalf("后出现的 PATH=last 要赢，拿到 %q", entry)
		}
	}
}

// TestBuildEnvironmentBlockEmpty 钉住"空环境也是合法环境"（不是 nil、不是继承）。
func TestBuildEnvironmentBlockEmpty(t *testing.T) {
	block, err := buildEnvironmentBlock([]string{})
	if err != nil {
		t.Fatalf("空环境要合法：%v", err)
	}
	if len(block) != 1 || block[0] != 0 {
		t.Fatalf("空环境块要只有结尾 NUL，拿到 %v", block)
	}
}

// TestBuildEnvironmentBlockRejectsMalformed 钉住"非法项响亮失败，不静默丢变量"。
func TestBuildEnvironmentBlockRejectsMalformed(t *testing.T) {
	for _, entry := range []string{"NOEQUALS", "=novalue", "A=1\x00B=2"} {
		if _, err := buildEnvironmentBlock([]string{entry}); err == nil {
			t.Fatalf("%q 必须被拒绝（静默丢变量是最难查的失败）", entry)
		}
	}
}
