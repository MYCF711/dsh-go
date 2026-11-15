// Package providerapi 是 `web.listProviders`（官方 0.2.2-rc.2 `dsh-web`）的
// **宿主接线层**：把 `websearch.Catalog`（纯领域数据，不依赖协议层）映射成
// `internal/transport` 的官方 wire 结果。
//
// 为什么单独一层：
//   - `websearch` 的清单推导是纯领域逻辑（接缝 + 环境 → 数据），不该为把它说出去
//     而依赖 HTTP 协议层；这样它的测试也不会被协议层的改动牵连。
//   - wire 形状与官方 zod 契约一一对应（`dsh-web/src/types.ts:196-217`），
//     改协议只需要动这一层。
//
// 接线一行（见报告）：
//
//	srv.AttachOfficialWebProviderAPI(providerapi.ProviderAPI(os.Getenv))
package providerapi

import (
	"context"
	"time"

	"dshgo/internal/transport"
	"dshgo/modules/tool/websearch"
)

// ProviderAPI 返回宿主的 `web.listProviders` 真实现。
//
// getenv 同时喂给清单推导与上限解析（生产传 os.Getenv，与子进程 `dshd run` 同一份环境）。
func ProviderAPI(getenv func(string) string) transport.OfficialWebProviderAPI {
	return providerAPI{getenv: getenv}
}

type providerAPI struct {
	getenv func(string) string
}

// ListOfficialWebProviders 每次调用重新投影：清单、可用性与选择都是**当下**的真值，
// 不是进程启动时的快照（官方同样是执行期解析，见 `dsh-web/src/index.ts:73-81`）。
func (p providerAPI) ListOfficialWebProviders(_ context.Context) (transport.OfficialWebProviderList, error) {
	catalog, err := websearch.DescribeCatalog(p.getenv)
	if err != nil {
		return transport.OfficialWebProviderList{}, err
	}
	return Official(catalog), nil
}

// Official 把宿主投影映射成官方 wire 结果。
//
// `search` / `fetch` 用官方形状；`resolution` / `baseline` / `limits` / `credentials`
// 是宿主附加（官方 `WebProviderList` 只有两个数组），JSON 里 omitempty。
func Official(catalog websearch.Catalog) transport.OfficialWebProviderList {
	return transport.OfficialWebProviderList{
		Search: entries(catalog.Search),
		Fetch:  entries(catalog.Fetch),
		Resolution: &transport.OfficialWebProviderResolution{
			Search: selection(catalog.SearchSelection),
			Fetch:  selection(catalog.FetchSelection),
		},
		Baseline: &transport.OfficialWebProviderBaseline{
			Search:           catalog.Baseline.Search,
			SearchRegistered: catalog.Baseline.SearchRegistered,
			Fetch:            catalog.Baseline.Fetch,
			FetchRegistered:  catalog.Baseline.FetchRegistered,
		},
		Limits: &transport.OfficialWebSearchLimits{
			MaxQueries:      catalog.Limits.MaxQueries,
			MaxResults:      catalog.Limits.MaxResults,
			SummaryMaxChars: catalog.Limits.SummaryMaxChars,
			SearchTimeoutMS: int(catalog.Limits.SearchTimeout / time.Millisecond),
		},
		Credentials: credentials(catalog.Credentials),
	}
}

func entries(rows []websearch.CatalogEntry) []transport.OfficialWebProviderInfo {
	out := make([]transport.OfficialWebProviderInfo, 0, len(rows))
	for _, row := range rows {
		info := transport.OfficialWebProviderInfo{
			ID:        row.ID,
			Title:     row.Title,
			Default:   row.Default,
			Selected:  row.Selected,
			Available: boolPtr(row.Available),
		}
		// 官方语义：provider 没声明 description 就**缺席**（不是空串）。
		if row.Description != "" {
			info.Description = row.Description
		}
		out = append(out, info)
	}
	return out
}

func selection(state websearch.CapabilityState) transport.OfficialWebProviderSelection {
	return transport.OfficialWebProviderSelection{
		Configured: state.Configured,
		Source:     state.Source,
		Selected:   state.Selected,
		ErrorCode:  state.ErrorCode,
		Error:      state.Error,
	}
}

func credentials(rows []websearch.CredentialState) []transport.OfficialWebCredential {
	out := make([]transport.OfficialWebCredential, 0, len(rows))
	for _, row := range rows {
		out = append(out, transport.OfficialWebCredential{
			Name:       row.Name,
			Configured: row.Configured,
			Role:       row.Role,
			Capability: row.Capability,
		})
	}
	return out
}

func boolPtr(value bool) *bool { return &value }
