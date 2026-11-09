package main

// boot facade 的生产接线。
//
// 这是一个**开关文件**，不是第二条实现：`newBootFacadeFrontend` 与
// `newFrontend` 读同一份 `DSHD_FRONTEND_DIR`、用同一条"未设置/目录不存在
// 就关闭"的判据，差别只有一处——它把 `DSHD_BOOT_FACADE` 翻成
// `webfrontend.BootOptions`。
//
// 为什么默认必须是"开"：官方 SPA 的入口第一句就读 `__DSH_BOOT_READY__`
// 与 `__ModuleLoader__`，缺了它整页停在 "Failed to load plugins:
// web boot: window.__ModuleLoader__ bootstrap facade is missing"。也就是说
// **关着它等于页面永远起不来**，那不该是默认值。
//
// 关掉它（`DSHD_BOOT_FACADE=0`）只为一件事：A/B。关掉之后 `GET /` 的字节
// 与"没有 boot 三件套"的历史形状**逐字节相同**（依据见
// `internal/webfrontend` 的 TestBootFacadeDisabledKeepsLegacyBytes）。

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"dshgo/internal/transport"
	"dshgo/internal/webfrontend"
)

// bootFacadeEnv 是 boot 三件套的开关（`0` / `off` / `false` = 关）。
//
// 为什么是环境变量：它描述的是"这份部署要不要官方启动链"，不是一次运行的
// 参数；和 DSHD_FRONTEND_DIR 同一层级。
const bootFacadeEnv = "DSHD_BOOT_FACADE"

// bootFacadeOptions 把环境变量翻成 BootOptions。
//
// 拼错的值**不静默退回默认**：`DSHD_BOOT_FACADE=of` 会让人以为关掉了，
// 实际还开着——那种"开关看起来存在但没生效"是最难查的一类。
func bootFacadeOptions() (webfrontend.BootOptions, error) {
	raw := strings.TrimSpace(os.Getenv(bootFacadeEnv))
	switch strings.ToLower(raw) {
	case "", "1", "on", "true", "yes":
		return webfrontend.BootOptions{}, nil
	case "0", "off", "false", "no":
		return webfrontend.BootOptions{Disabled: true}, nil
	default:
		return webfrontend.BootOptions{}, fmt.Errorf("%s=%q 不是开关值（用 1/0）", bootFacadeEnv, raw)
	}
}

// newBootFacadeFrontend 按 DSHD_FRONTEND_DIR 建一份带 boot 三件套的前端。
//
// 与 `newFrontend` 的两条关闭判据完全一致（未设置、目录不存在都不启用，
// 后者打一行 stderr），因为"前端托管开不开"和"启动链开不开"是两件事，
// 不该互相牵连。
func newBootFacadeFrontend() (*webfrontend.Frontend, error) {
	dir := os.Getenv(frontendDirEnv)
	if dir == "" {
		return nil, nil
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		fmt.Fprintf(os.Stderr, "frontend-static: %s=%q 不是目录，前端托管关闭（既有路由不变）\n", frontendDirEnv, dir)
		return nil, nil
	}
	options, err := bootFacadeOptions()
	if err != nil {
		return nil, err
	}
	front, err := webfrontend.New(webfrontend.Config{Dir: dir, Boot: &options})
	if err != nil {
		return nil, err
	}
	// 把"这次注入的启动链是什么"说出来：页面停在加载页时，这一行是
	// "图读到了没有 / 几条 entry"的唯一现场证据。
	if description, ok := front.BootDescription(); ok {
		fmt.Fprintf(os.Stderr, "frontend boot: %s\n", description)
	} else {
		fmt.Fprintf(os.Stderr, "frontend boot: %s 关闭（只有两条全局注入）\n", bootFacadeEnv)
	}
	return front, nil
}

// bootFacadeMount 是 boot 版的生产接线：与 `frontendMount` 语义相同，
// 单独一个名字是为了让 serve.go 的改动止于"把 newFrontend 换成
// newBootFacadeFrontend"这一行——两条接线不共用函数名，避免有人只改一半。
func bootFacadeMount(front *webfrontend.Frontend, inner func(*transport.Server) http.Handler) func(*transport.Server) http.Handler {
	return frontendMount(front, inner)
}
