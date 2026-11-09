package extern

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// resolveNode 找出可用的 node 可执行文件。
//
// 三级：显式 NodePath → PATH 里的 node → node.exe（Windows 上 LookPath 通常
// 已经处理，但用户可能把 node 放在非标准位置并只给了目录）。
func resolveNode(opts Options) (string, *UnavailableError) {
	if p := strings.TrimSpace(opts.NodePath); p != "" {
		st, err := os.Stat(p)
		if err != nil || st.IsDir() {
			return "", &UnavailableError{
				Code:   CodeNodeMissing,
				Reason: "指定的 node 路径不存在或不是文件：" + p,
				Hint: "把 NodePath 指到真实的 node 可执行文件，或留空让宿主从 PATH 里找；" +
					"没有 node 时 Cordis JS 插件不可用，请改用 Go 原生插件（O1 路线）。",
			}
		}
		return p, nil
	}
	if p, err := exec.LookPath("node"); err == nil && p != "" {
		return p, nil
	}
	if runtime.GOOS == "windows" {
		if p, err := exec.LookPath("node.exe"); err == nil && p != "" {
			return p, nil
		}
	}
	return "", &UnavailableError{
		Code:   CodeNodeMissing,
		Reason: "PATH 里找不到 node（Cordis JS 插件需要一个外部 Node 进程）",
		Hint: "安装 Node.js 并确保 `node --version` 可用；" +
			"或者改用 Go 原生插件（O1 路线）——宿主**不会**用内置 JS 引擎假装插件已经跑起来。",
	}
}

// probeNode 真的跑一次 `node --version`，确认它不是坏的可执行文件。
func probeNode(ctx context.Context, nodePath string) *UnavailableError {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, nodePath, "--version")
	cmd.Env = append(os.Environ(), "NODE_NO_WARNINGS=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return &UnavailableError{
			Code:   CodeNodeUnusable,
			Reason: "node 跑不起来：" + truncate(strings.TrimSpace(string(out)), 240) + "（" + err.Error() + "）",
			Hint: "确认这个 node 可执行（`node --version` 能出 v 开头的版本号），或改 NodePath 指向别的 node；" +
				"继续用 Go 原生插件不受影响。",
		}
	}
	if !strings.HasPrefix(strings.TrimSpace(string(out)), "v") {
		return &UnavailableError{
			Code:   CodeNodeUnusable,
			Reason: "node --version 的输出不像 Node.js：" + truncate(strings.TrimSpace(string(out)), 120),
			Hint:   "这个可执行文件不是 Node.js；请指向真正的 node。",
		}
	}
	return nil
}

// writeBootstrap 把引导脚本写到临时目录，返回 dir 与脚本路径。
//
// 注意：这里写的是**文本文件**，不是把 JS 塞进宿主进程。宿主对它的角色和
// 对任何一个数据文件一样——不解析、不执行。
func writeBootstrap() (dir string, script string, err error) {
	dir, err = os.MkdirTemp("", "cordis-extern-*")
	if err != nil {
		return "", "", err
	}
	script = filepath.Join(dir, "bootstrap.cjs")
	if err := os.WriteFile(script, []byte(bootstrapJS), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", "", err
	}
	return dir, script, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
