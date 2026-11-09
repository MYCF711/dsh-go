// workdir.go 实现官方 `workingDirectory.defaultDirectory`：新会话的默认工作目录。
//
// 官方原件（app.asar:dsh/node_modules/@deepseek-ai/dsh-working-directory/lib/index.js）：
//
//	:18 static Config = z.object({ defaultDirectory: z.string() })
//	:27 const directory = config.defaultDirectory ?? process.cwd();
//	:28 if (!isAbsolute(directory)) throw new Error("working-directory: defaultDirectory must be absolute");
//	:47   init: (header) => header.cwd ?? null,   // 会话 header 的 cwd 是权威值
//
// 宿主的 `--home` 是**会话根**（sessions/、state/、skills/ 都挂在它下面），
// **不是工作目录**。工作目录此前恒等于进程 cwd，没有 per-deployment 的默认值；
// 想换目录只能切启动目录（`cd` 一下再起）或进会话后 `/cd`。
//
// 这里补的就是那个配置面：`DSHD_DEFAULT_WORKDIR`（官方是配置项 defaultDirectory，
// 宿主统一用环境变量映射，与 personaPrefix 同一种形状）。
//
// **默认行为必须与现在逐字节一致**：未配置（含全空白）时返回空串，各入口
// 走原来的"进程 cwd"路径，一个字节都不变。只有显式配置了才替换。
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"dshgo/codecs/v4"
)

// defaultWorkDirEnv 是官方 `workingDirectory.defaultDirectory` 的环境变量名。
const defaultWorkDirEnv = "DSHD_DEFAULT_WORKDIR"

// defaultWorkDirFromEnv 返回配置的新会话默认工作目录；未配置返回空串。
//
// 空串的语义留给调用方：run / serve 用 `os.Getwd()`（= 官方默认 process.cwd()），
// ACP 的 ensureSession 则保持"客户端没带 cwd 就不改工作区"的原行为。
//
// 配置了就必须是**绝对路径**（官方 :28 明确抛错）：相对路径按"启动目录"解析，
// 而启动目录本身正是这个配置想摆脱的东西——静默解析等于让配置看起来生效了、
// 实际指向别处，是最难查的一类。
func defaultWorkDirFromEnv() (string, error) {
	raw := strings.TrimSpace(os.Getenv(defaultWorkDirEnv))
	if raw == "" {
		return "", nil
	}
	if !filepath.IsAbs(raw) {
		return "", fmt.Errorf("%s=%q 必须是绝对路径（官方 defaultDirectory 要求绝对目录；相对路径会随启动目录漂移）",
			defaultWorkDirEnv, raw)
	}
	// 与 --home / serveCreate 同一条归一化：去掉 \\?\ 前缀、统一分隔符，
	// 否则会写出真实 DSH 永远不会产生的 projectKey。
	return v4.NormalizeCwd(filepath.Clean(raw)), nil
}

// sessionWorkDir 返回新会话要用的工作目录：配置了默认值就用它，否则进程 cwd。
func sessionWorkDir() (string, error) {
	dir, err := defaultWorkDirFromEnv()
	if err != nil {
		return "", err
	}
	if dir != "" {
		return dir, nil
	}
	return os.Getwd()
}
