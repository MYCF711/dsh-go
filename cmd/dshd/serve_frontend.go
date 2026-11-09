package main

import (
	"fmt"
	"net/http"
	"os"

	"dshgo/internal/transport"
	"dshgo/internal/webfrontend"
)

// frontendDirEnv 是官方前端 dist 的根目录。
//
// 为什么是环境变量而不是 flag：它是一条**部署事实**（dist 装在哪由打包
// 决定），不是一次运行的参数。官方那边同样是组装期解析出来的
// `distIndex`（`@deepseek-ai/dsh-host-frontend-static` 的 Config）。
const frontendDirEnv = "DSHD_FRONTEND_DIR"

// newFrontend 按 DSHD_FRONTEND_DIR 建前端。
//
// 未设置 → (nil, nil)，目录不存在 → (nil, nil) + 一条 stderr 说明：
// 两种都是"不启用任何响应"，上游 Start 用的就是原来的 handler，**既有
// 行为逐字节不变**。为什么缺目录不报错：这个变量可能由一份通用环境
// 配置带进来（比如打包脚本给所有宿主都设），一个不存在的目录不该让
// 整个宿主起不来；但静默降级又查不出来，所以打一行 stderr 说清楚。
//
// 只有"目录在、但 index.html 缺注入锚点"这类**配置正确却做不到**的
// 情况才返回 error（由调用方决定是否致命）。
func newFrontend() (*webfrontend.Frontend, error) {
	dir := os.Getenv(frontendDirEnv)
	if dir == "" {
		return nil, nil
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		fmt.Fprintf(os.Stderr, "frontend-static: %s=%q 不是目录，前端托管关闭（既有路由不变）\n", frontendDirEnv, dir)
		return nil, nil
	}
	return webfrontend.New(webfrontend.Config{Dir: dir})
}

// frontendHandlerWrap 是 transport.Options.HandlerWrap 的实现。
//
// 前端未启用时返回 nil——`Start` 里那个 `if wrapped := …; wrapped != nil`
// 分支就不会进，`handler` 还是 Server 自己。这是"默认关闭时一个字节都
// 不变"的落点，别再包一层 identity handler 上去。
func frontendHandlerWrap(front *webfrontend.Frontend) func(*transport.Server) http.Handler {
	if front == nil {
		return nil
	}
	return func(s *transport.Server) http.Handler {
		// 静态站只占"没有具名路由命中"的回退席位；/connect、/api/*、
		// /ws、/debug/stats 由 Server 自己接（见 webfrontend.namedRoute）。
		return front.Wrap(s)
	}
}

// frontendMount 把前端装到 inner 的**外面**，供 serve.go 组合别的 HandlerWrap。
//
// 为什么需要组合而不是直接赋值：`Options.HandlerWrap` 只有一个槽，而
// 官方 wire（`officialWireMount`）已经占了它。静态站必须在外层——否则
// `GET /?token=<launch>` 根本到不了这里，浏览器拿到的是一个没有 cookie
// 的 404，而"打不开登录页"会被误判成认证坏了。
//
// inner 为 nil（wire 关闭）时退化成 `front.Wrap(srv)`；front 为 nil
// （前端关闭）时就是 inner 自己的结果——两条开关正交，四种组合都成立。
func frontendMount(front *webfrontend.Frontend, inner func(*transport.Server) http.Handler) func(*transport.Server) http.Handler {
	return func(s *transport.Server) http.Handler {
		var handler http.Handler = s
		if inner != nil {
			if wrapped := inner(s); wrapped != nil {
				handler = wrapped
			}
		}
		return front.Wrap(handler)
	}
}
