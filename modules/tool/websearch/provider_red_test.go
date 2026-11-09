package websearch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
	"dshgo/modules/search"
	"dshgo/modules/tools"
)

// 配了 deepseek-official 但没有任何 API key 时，**不许**静默回落到另一个后端
// 然后返回"命中 0 条来源"——那等于告诉模型"互联网上没有"，而真实原因是
// 这个宿主没配凭据。旧实现忽略 provider 选择、无 key 时照样用 SearxNG 端点
// 返回空结果（exit 0 的空成功），这条用例在旧实现上因此是红的。
func TestConfiguredProviderWithoutKeyFailsLoud(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"results":[]}`)
	}))
	defer srv.Close()

	t.Setenv("DSHD_SEARCH_URL", srv.URL+"/search?q={query}&format=json")
	t.Setenv("DSHD_WEB_SEARCH_PROVIDER", "deepseek-official")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("DSHD_SEARCH_API_KEY", "")

	tool := assembleWebSearch(t)
	res, err := tool.Exec(context.Background(), json.RawMessage(`{"queries":["官方同步"]}`))
	if err == nil {
		t.Fatalf("配了 deepseek-official 却没有凭据，不能成功返回空结果: %+v", res)
	}
	if !strings.Contains(res.Hint, "DEEPSEEK_API_KEY") {
		t.Fatalf("Hint 要点名缺的凭据: %q", res.Hint)
	}
	if hits != 0 {
		t.Fatalf("没有凭据时不该落到别的搜索端点（打了 %d 次）", hits)
	}
}

// assembleWebSearch 用与生产同一套模块装配（tools + search + websearch），
// 不是手搓一个假 SearchProvider——否则测的就不是真实接线。
func assembleWebSearch(t *testing.T) spi.Tool {
	t.Helper()
	k := kernel.New("test-websearch")
	root := k.Root()
	if _, err := kernel.Assemble(root, []kernel.Module{
		tools.Module(tools.Options{}),
		search.Module(),
		Module(),
	}); err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	reg, err := kernel.Inject(root, spi.ToolsKey)
	if err != nil {
		t.Fatal(err)
	}
	tool, ok := reg.Get("web_search")
	if !ok {
		t.Fatal("web_search 没注册")
	}
	return tool
}
