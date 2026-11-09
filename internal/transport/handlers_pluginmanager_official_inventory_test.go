package transport

// 官方 `@deepseek-ai/dsh-plugin-manager` 0.2.2-rc.1 的 typert host 实际有
// **15** 个 `pluginManager/*` 方法。本文件先钉住其中此前漏登记的三个：
//
//   - pluginManager.listBundles  已安装/可安装 bundle 清单（真实 profile 状态）
//   - pluginManager.listPlugins  当前 Loader 运行时条目与 fiberPhase
//   - pluginManager.registries   pnpm 自身配置命名的 registry（本仓无 pnpm）
//
// 红证：在基线（handler 只实现 12 个方法）上，这三项都不在
// IsOfficialPluginManagerMethod 里，路由会把它们漏进通用 501/404。任务要求
// “关闭恒 501/假 installed”，所以先让测试失败，再补实现。

import (
	"net/http"
	"strings"
	"testing"
)

// TestOfficialPluginManagerInventoryMethodsAreRegistered 先红：官方方法集合是
// 15 个，不是旧注释里的 12 个。漏一个就意味着真实宿主再完整，客户端也拿不到。
func TestOfficialPluginManagerInventoryMethodsAreRegistered(t *testing.T) {
	for _, method := range []string{
		"pluginManager.listBundles",
		"pluginManager.listPlugins",
		"pluginManager.registries",
	} {
		t.Run(method, func(t *testing.T) {
			if !IsOfficialPluginManagerMethod(method) {
				t.Fatalf("%s 未登记：官方 typert.host.js 的 pluginManager 命名空间共 15 个方法，"+
					"当前 handler 只认 12 个，路由会把它漏成通用 501/404", method)
			}
		})
	}
}

// TestOfficialPluginManagerListBundlesUnwiredIsLoud 先红：即使宿主没有挂接
// bundle 清单，也必须是**官方形状**的四字段 501 并点名方法，不能是通用
// 404/“未知方法”。这条同时钉住“未实现 ≠ 已实现但不可用”。
func TestOfficialPluginManagerListBundlesUnwiredIsLoud(t *testing.T) {
	srv := officialPluginTestServer(t, OfficialPluginManagerAPI{})
	code, out := callPluginManager(t, srv, "pluginManager.listBundles", map[string]any{})
	assertFourFieldError(t, code, out, http.StatusNotImplemented, "not_implemented")
	obj := out["error"].(map[string]any)
	msg, _ := obj["message"].(string)
	if !strings.Contains(msg, "pluginManager.listBundles") {
		t.Fatalf("501 没点名方法：%q", msg)
	}
}
