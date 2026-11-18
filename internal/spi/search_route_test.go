package spi

// 官方 rc.5 `dsh-web` 按模型路由选搜索 provider 的接缝测试。
//
// 权威（rc.5 解包 `tmp/GO8-RC5/official-rc5/dsh/node_modules/@deepseek-ai/`）：
//   - `dsh-web/src/index.ts:58`      DEEPSEEK_MODEL_ROUTES
//   - `dsh-web/src/index.ts:64`      DEEPSEEK_ROUTE_SEARCH_PROVIDER = 'deepseek-official'
//   - `dsh-web/src/index.ts:67`      OTHER_ROUTE_SEARCH_PROVIDER = 'deepseek-search-api'
//   - `dsh-web/src/index.ts:229-241` selectSearchProvider
//   - `dsh-web/src/index.ts:305-328` resolveProvider（configured → preferred → single → 报错）
//   - `dsh-tool-web/src/search.ts:409` llmProvider = requestContext().provider

import (
	"context"
	"strings"
	"testing"
)

// routeFakeProvider 是内存 SearchProvider：只记被问过几次，可用性可预置。
type routeFakeProvider struct {
	id        string
	available bool
	searches  int
}

func (p *routeFakeProvider) ID() string { return p.id }
func (p *routeFakeProvider) Available() bool {
	return p.available
}

func (p *routeFakeProvider) Search(context.Context, string, int) (SearchResult, error) {
	p.searches++
	return SearchResult{}, nil
}

func routeSeam(t *testing.T, providers ...*routeFakeProvider) *WebProviders {
	t.Helper()
	seam := NewWebProviders("", "")
	for _, provider := range providers {
		if err := seam.RegisterSearch(provider); err != nil {
			t.Fatalf("注册 %s 失败: %v", provider.id, err)
		}
	}
	return seam
}

// 判据的字面量必须与官方逐字一致（`:58,64,67`）。
func TestOfficialRouteConstants(t *testing.T) {
	if got := strings.Join(DeepSeekModelRouteIDs[:], ","); got != "deepseek-official,deepseek-account" {
		t.Fatalf("DEEPSEEK_MODEL_ROUTES 必须照官方（:58）；实际 %q", got)
	}
	if DeepSeekRouteSearchProviderID != "deepseek-official" {
		t.Fatalf("DeepSeek 路由优先项必须照官方（:64）；实际 %q", DeepSeekRouteSearchProviderID)
	}
	if OtherRouteSearchProviderID != "deepseek-search-api" {
		t.Fatalf("其它路由优先项必须照官方（:67）；实际 %q", OtherRouteSearchProviderID)
	}
}

// 官方 `selectSearchProvider` 的 preferredId 计算（`:229-241`）：
// 路由是集合成员就 DeepSeek 优先项，其余（含空 = 没有模型请求）一律 other 优先项。
func TestPreferredSearchProviderForRoute(t *testing.T) {
	cases := map[string]string{
		"deepseek-official": "deepseek-official",
		"deepseek-account":  "deepseek-official",
		"":                  "deepseek-search-api",
		"gateway":           "deepseek-search-api",
		"openai":            "deepseek-search-api",
		// 官方是 Set 精确匹配，不做大小写/前缀归一。
		"DeepSeek-Official": "deepseek-search-api",
	}
	for route, want := range cases {
		if got := PreferredSearchProviderForRoute(route); got != want {
			t.Fatalf("路由 %q 的优先项该是 %q；实际 %q", route, want, got)
		}
	}
}

// 官方 `resolveProvider`（`:305-328`）的顺序与失败码。
func TestSelectSearchForRouteFollowsOfficialOrder(t *testing.T) {
	t.Run("configured wins over route preference", func(t *testing.T) {
		official := &routeFakeProvider{id: DeepSeekRouteSearchProviderID, available: true}
		other := &routeFakeProvider{id: OtherRouteSearchProviderID, available: true}
		seam := NewWebProviders(other.id, "")
		for _, provider := range []*routeFakeProvider{official, other} {
			if err := seam.RegisterSearch(provider); err != nil {
				t.Fatal(err)
			}
		}
		selected, err := seam.SelectSearchForRoute("deepseek-account")
		if err != nil {
			t.Fatal(err)
		}
		// 钉死 other 时官方仍选它（`:309-315` 在 preferred 之前返回）。
		if selected.ID() != OtherRouteSearchProviderID {
			t.Fatalf("配置钉死优先于路由规则；实际 %q", selected.ID())
		}
	})

	t.Run("preferred usable wins among many", func(t *testing.T) {
		official := &routeFakeProvider{id: DeepSeekRouteSearchProviderID, available: true}
		other := &routeFakeProvider{id: OtherRouteSearchProviderID, available: true}
		seam := routeSeam(t, official, other)
		selected, err := seam.SelectSearchForRoute("deepseek-official")
		if err != nil {
			t.Fatal(err)
		}
		if selected.ID() != DeepSeekRouteSearchProviderID {
			t.Fatalf("DeepSeek 路由上该选 deepseek-official；实际 %q", selected.ID())
		}
	})

	t.Run("preferred unusable falls back to single usable", func(t *testing.T) {
		official := &routeFakeProvider{id: DeepSeekRouteSearchProviderID, available: false}
		other := &routeFakeProvider{id: OtherRouteSearchProviderID, available: true}
		seam := routeSeam(t, official, other)
		selected, err := seam.SelectSearchForRoute("deepseek-official")
		if err != nil {
			t.Fatal(err)
		}
		if selected.ID() != OtherRouteSearchProviderID {
			t.Fatalf("优先项不可用该退回唯一可用；实际 %q", selected.ID())
		}
	})

	t.Run("preferred unusable with two usable is ambiguous", func(t *testing.T) {
		official := &routeFakeProvider{id: DeepSeekRouteSearchProviderID, available: false}
		a := &routeFakeProvider{id: "a", available: true}
		b := &routeFakeProvider{id: "b", available: true}
		seam := routeSeam(t, official, a, b)
		_, err := seam.SelectSearchForRoute("deepseek-official")
		if WebErrorCode(err) != WebProviderAmbiguous {
			t.Fatalf("优先项不可用且多个可用该报 %s；实际 %q", WebProviderAmbiguous, WebErrorCode(err))
		}
	})

	t.Run("no usable is unavailable", func(t *testing.T) {
		seam := routeSeam(t, &routeFakeProvider{id: "x", available: false})
		_, err := seam.SelectSearchForRoute("")
		if WebErrorCode(err) != WebProviderUnavailable {
			t.Fatalf("没有可用 provider 该报 %s；实际 %q", WebProviderUnavailable, WebErrorCode(err))
		}
	})
}

// SelectSearch 用的是接缝上装的路由读取器（官方 request.llmProvider 的宿主载体）。
func TestSelectSearchUsesSeamRoute(t *testing.T) {
	official := &routeFakeProvider{id: DeepSeekRouteSearchProviderID, available: true}
	other := &routeFakeProvider{id: OtherRouteSearchProviderID, available: true}
	seam := routeSeam(t, official, other)

	route := "deepseek-account"
	seam.SetSearchRoute(func() string { return route })
	selected, err := seam.SelectSearch()
	if err != nil || selected.ID() != DeepSeekRouteSearchProviderID {
		t.Fatalf("路由 deepseek-account 上该选 deepseek-official: %v %v", err, selected)
	}
	if got := seam.SearchRoute(); got != "deepseek-account" {
		t.Fatalf("SearchRoute 该报当前路由；实际 %q", got)
	}

	route = "gateway"
	selected, err = seam.SelectSearch()
	if err != nil || selected.ID() != OtherRouteSearchProviderID {
		t.Fatalf("路由 gateway 上该选 deepseek-search-api: %v %v", err, selected)
	}

	// 没有装读取器 = 没有模型请求：官方仍走 other 优先项。
	bare := routeSeam(t, &routeFakeProvider{id: OtherRouteSearchProviderID, available: true},
		&routeFakeProvider{id: DeepSeekRouteSearchProviderID, available: true})
	if bare.SearchRoute() != "" {
		t.Fatalf("没装读取器时路由该为空；实际 %q", bare.SearchRoute())
	}
	if selected, err := bare.SelectSearch(); err != nil || selected.ID() != OtherRouteSearchProviderID {
		t.Fatalf("没有模型请求该选 deepseek-search-api: %v %v", err, selected)
	}

	// Search 走同一条选择（不是另一份判据）。
	before := official.searches + other.searches
	if _, err := seam.Search(context.Background(), "q", 1); err != nil {
		t.Fatal(err)
	}
	if official.searches+other.searches != before+1 {
		t.Fatal("Search 必须真的落到选中的 provider 上")
	}
	if other.searches != 1 || official.searches != 0 {
		t.Fatalf("路由 gateway 时只该调 deepseek-search-api；实际 official=%d other=%d",
			official.searches, other.searches)
	}
}
