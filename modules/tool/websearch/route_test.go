package websearch

// `web_search` 在 rc.5 下的 provider 选择：工具本身不做选择（官方 `dsh-tool-web` 只把
// `request.llmProvider` 交给 `ctx.web.search()`），选择由接缝按模型路由完成。
// 这里钉住"工具确实走接缝的路由判据"，而不是自己再写一份。
//
// 官方依据（rc.5 解包）：
//   - `dsh-tool-web/src/search.ts:409`  llmProvider = exec.agent?.session.requestContext()?.provider
//   - `dsh-web/src/index.ts:229-241`     selectSearchProvider(llmProvider)
//   - `dsh-web/src/index.ts:64,67`       DeepSeek 路由 → deepseek-official；其它 → deepseek-search-api

import (
	"testing"
)

func TestToolSelectsProviderBySeamRoute(t *testing.T) {
	official := &fakeProvider{id: "deepseek-official", available: true}
	other := &fakeProvider{id: "deepseek-search-api", available: true}
	seam := newSeam(t, official, other)

	route := "deepseek-account"
	seam.SetSearchRoute(func() string { return route })
	tool := &Tool{Web: seam}

	if _, err := execSearch(t, tool, `{"queries":["路由一"]}`); err != nil {
		t.Fatal(err)
	}
	if got := len(official.seen()); got != 1 {
		t.Fatalf("DeepSeek 路由上该由 deepseek-official 服务；实际被问 %d 次", got)
	}
	if got := len(other.seen()); got != 0 {
		t.Fatalf("DeepSeek 路由上不该碰 deepseek-search-api；实际被问 %d 次", got)
	}

	route = "gateway"
	if _, err := execSearch(t, tool, `{"queries":["路由二"]}`); err != nil {
		t.Fatal(err)
	}
	if got := len(official.seen()); got != 1 {
		t.Fatalf("其它路由上不该再碰 deepseek-official；实际被问 %d 次", got)
	}
	if got := len(other.seen()); got != 1 {
		t.Fatalf("其它路由上该由 deepseek-search-api 服务；实际被问 %d 次", got)
	}
}
