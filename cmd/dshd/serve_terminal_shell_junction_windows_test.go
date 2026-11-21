//go:build windows

package main

// `resolveShellPath` 的 junction 拼法：**不是**围栏，不改（附实测依据）。
//
// 这个函数只做一件事：把候选 shell 解析成绝对路径并确认它是可执行文件，结果进
// `transport.OfficialTerminalShell.Path`，用途只有两个——界面列出可用 shell、PTY
// 启动时 exec 这个路径。全程没有"这条路径是否允许"的判定，也没有拿它跟任何
// 根目录做包含比较（对比 `officialResolveWithin` / `forgeCanonicalDir`）。
//
// junction 拼法下的差别只有**字符串写法**：EvalSymlinks 停在 junction 拼法上，
// 但 (a) 后面那句 `os.Stat` 走的是操作系统语义，照样能 stat 到目标文件，
// (b) exec 时操作系统同样会跟着 junction 走。所以"启动/显示"两个消费者拿到的都是
// 同一个可执行文件 —— 实测用 os.SameFile 证明，不是推断。
//
// 收益对比：改成解析 junction 只能让界面少显示一段 junction 拼法，代价是给一个
// 纯启动路径引入新的依赖与新的失败分支；不抵，所以如实记"不改"。

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveShellPathJunctionSpellingDenotesSameFile(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	realExe, err := filepath.EvalSymlinks(exe)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", exe, err)
	}
	link := filepath.Join(t.TempDir(), "bin-link")
	makeTestJunction(t, link, filepath.Dir(realExe))

	// junction 拼法必须被**接受**：这里的判据是"是个文件且扩展名像 exe"，
	// junction 不会让 os.Stat 失败，所以探测不会因为 junction 部署而找不到 shell。
	got, err := resolveShellPath(filepath.Join(link, filepath.Base(realExe)))
	if err != nil {
		t.Fatalf("junction 拼法的 shell 必须被接受（否则 default shell 探测在 junction 部署上会失败）：%v", err)
	}
	gotInfo, err := os.Stat(got)
	if err != nil {
		t.Fatalf("stat 解析结果 %q: %v", got, err)
	}
	realInfo, err := os.Stat(realExe)
	if err != nil {
		t.Fatalf("stat 真实路径 %q: %v", realExe, err)
	}
	if !os.SameFile(gotInfo, realInfo) {
		t.Fatalf("junction 拼法的结果 %q 与真实路径 %q 不是同一个文件", got, realExe)
	}

	// 反向控制：目录与非可执行扩展名照旧拒（这条守卫没被顺手放宽）。
	if _, err := resolveShellPath(filepath.Dir(realExe)); err == nil {
		t.Fatalf("目录必须被拒")
	}
}
