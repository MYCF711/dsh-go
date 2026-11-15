package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"dshgo/internal/kernel"
	"dshgo/internal/llm"
	"dshgo/internal/spi"
	"dshgo/modules/search"
)

// 这一组用例钉的是**用户能感知的**那条链：
//
//	credentials.set（界面存 key）→ 凭据库文件 → 模块按 ref 取值 → 请求头
//
// 断言点全部落在真实 HTTP 请求的 x-api-key 上，不是"某个函数被调了"：
// 假端点收回来的那个头就是 provider 真的拿去认证的东西。
//
// 为什么必须端到端：这个缺口原来的形态就是"单测全绿、界面上存了 key、
// 搜索仍然报没凭据"——只测 provider 内部解析会漏掉接线（模块拿不到服务、
// 装配顺序、白名单丢模块）那一半。

const deepSeekSearchResultBody = `{"content":[{"type":"web_search_tool_result","content":[
	{"type":"web_search_result","url":"https://a.example","title":"来源一"}]}]}`

// deepSeekKeyCapture 起一个假的 Messages 端点，记下最后一次请求的 x-api-key。
func deepSeekKeyCapture(t *testing.T) (*httptest.Server, func() string) {
	t.Helper()
	var mu sync.Mutex
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = r.Header.Get("x-api-key")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, deepSeekSearchResultBody)
	}))
	t.Cleanup(srv.Close)
	return srv, func() string {
		mu.Lock()
		defer mu.Unlock()
		return seen
	}
}

// assembleSearch 走**生产同一份**装配：凭据模块 + search 模块。
//
// 顺序故意把 search 排在凭据模块**前面**：模块是在解析那一刻才注入凭据服务的，
// 所以装配顺序不许影响结果。谁把注入改回装配期快照，这条顺序就会立刻红。
func assembleSearch(t *testing.T, home string) *spi.WebProviders {
	t.Helper()
	k := kernel.New("test-credentials-search")
	root := k.Root()
	t.Cleanup(root.Dispose)
	mods := []kernel.Module{search.Module(), credentialsModule(home)}
	if _, err := kernel.Assemble(root, mods); err != nil {
		t.Fatalf("装配 search + credentials 模块失败: %v", err)
	}
	seam, err := kernel.Inject(root, spi.WebKey)
	if err != nil {
		t.Fatalf("注入 web 接缝失败: %v", err)
	}
	return seam
}

// storeCredential 用**真实**后端写一条凭据（官方 credentials.set 的同一条路径）。
func storeCredential(t *testing.T, home, ref, value string) {
	t.Helper()
	store := llm.OpenModelCatalog(home)
	if _, err := store.SetCredential(ref, value, nil); err != nil {
		t.Fatalf("写入凭据失败（ref=%s）: %v", ref, err)
	}
}

// clearSearchCredentials 把环境清干净：机器上真的设过这两个变量时，
// "都没有"那一档会被环境兜住，用例就变成假绿。值本身不进断言文本。
func clearSearchCredentials(t *testing.T) {
	t.Helper()
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("DSHD_SEARCH_API_KEY", "")
}

// ① 只有凭据库有 key（环境一个都没设）→ 请求必须带上**库里那个**。
//
// 这是"界面上存了 API Key"的最直接形态：进程环境里什么都没有。
func TestSearchUsesStoredCredentialWithoutEnvironment(t *testing.T) {
	clearSearchCredentials(t)
	const storedKey = "stored-only-key-not-env"
	home := t.TempDir()
	storeCredential(t, home, "DEEPSEEK_API_KEY", storedKey)

	srv, seenKey := deepSeekKeyCapture(t)
	t.Setenv("DEEPSEEK_SEARCH_BASE_URL", srv.URL)

	seam := assembleSearch(t, home)
	if !seam.SearchReady() {
		t.Fatal("凭据库里有 key，搜索就该就绪")
	}
	assertProviderKey(t, seam, seenKey, storedKey)
}

// ② 环境有值、凭据库也有值 → **凭据库赢**（官方：可写文件是主来源）。
func TestSearchStoredCredentialBeatsEnvironment(t *testing.T) {
	clearSearchCredentials(t)
	const storedKey = "stored-key-wins-over-env"
	const envKey = "env-key-must-lose"
	home := t.TempDir()
	storeCredential(t, home, "DEEPSEEK_API_KEY", storedKey)

	srv, seenKey := deepSeekKeyCapture(t)
	t.Setenv("DEEPSEEK_SEARCH_BASE_URL", srv.URL)
	t.Setenv("DEEPSEEK_API_KEY", envKey)

	seam := assembleSearch(t, home)
	assertProviderKey(t, seam, seenKey, storedKey)
}

// ③ 别名优先级：库里的**别名** ref 赢过环境里的**主名** ref。
//
// 这一档专门钉死"两层各走一轮"的写法：逐个别名地做「库→env」会在这里红
// （库里的 DSHD_SEARCH_API_KEY 会被环境的 DEEPSEEK_API_KEY 抢过去），
// 那正好把官方"文件优先于环境"反过来。
func TestSearchStoredAliasBeatsEnvironmentPrimary(t *testing.T) {
	clearSearchCredentials(t)
	const storedAliasKey = "stored-alias-key-wins"
	const envKey = "env-primary-must-lose"
	home := t.TempDir()
	storeCredential(t, home, "DSHD_SEARCH_API_KEY", storedAliasKey)

	srv, seenKey := deepSeekKeyCapture(t)
	t.Setenv("DEEPSEEK_SEARCH_BASE_URL", srv.URL)
	t.Setenv("DEEPSEEK_API_KEY", envKey)

	seam := assembleSearch(t, home)
	assertProviderKey(t, seam, seenKey, storedAliasKey)
}

// ④ 凭据库没有、环境有 → 环境生效（fallback 与接入前一致）。
func TestSearchFallsBackToEnvironmentWhenStoreEmpty(t *testing.T) {
	clearSearchCredentials(t)
	const envKey = "env-fallback-key"
	home := t.TempDir() // 空 home：凭据库文件根本不存在

	srv, seenKey := deepSeekKeyCapture(t)
	t.Setenv("DEEPSEEK_SEARCH_BASE_URL", srv.URL)
	t.Setenv("DEEPSEEK_API_KEY", envKey)

	seam := assembleSearch(t, home)
	assertProviderKey(t, seam, seenKey, envKey)
}

// ⑤ 都没有 → 与接入凭据库之前**逐字节一致**：provider 不可用、
// 直接调用给稳定的「缺凭据」码，选择器报"没有可用 provider"。
func TestSearchWithoutAnyCredentialIsUnavailable(t *testing.T) {
	clearSearchCredentials(t)
	home := t.TempDir()
	srv, seenKey := deepSeekKeyCapture(t)
	t.Setenv("DEEPSEEK_SEARCH_BASE_URL", srv.URL)

	seam := assembleSearch(t, home)
	if seam.SearchReady() {
		t.Fatal("没有任何凭据时不能算就绪")
	}
	if _, err := seam.SelectSearch(); spi.WebErrorCode(err) != spi.WebProviderUnavailable {
		t.Fatalf("没有可用 provider 要报 WEB_PROVIDER_UNAVAILABLE: %v", err)
	}
	provider := search.NewDeepSeekFromEnv(nil) // 没有环境、也没有凭据库入口
	provider.BaseURL = srv.URL
	if _, err := provider.Search(context.Background(), "x", 8); spi.WebErrorCode(err) != spi.WebProviderCredentialMissing {
		t.Fatalf("缺凭据要报 WEB_PROVIDER_CREDENTIAL_MISSING: %v", err)
	}
	if got := seenKey(); got != "" {
		t.Fatal("没有凭据时绝不能真的发请求（假端点不该收到任何头）")
	}
}

// ⑥ 装配之后才存下的 key，**下一步就生效**：解析是现读，不是装配期快照。
//
// 这是"存了 key 搜索却不生效"的原始症状；缓存任何一层都会让这条红。
func TestSearchSeesCredentialStoredAfterAssembly(t *testing.T) {
	clearSearchCredentials(t)
	const laterKey = "stored-after-assembly-key"
	home := t.TempDir()
	srv, seenKey := deepSeekKeyCapture(t)
	t.Setenv("DEEPSEEK_SEARCH_BASE_URL", srv.URL)

	seam := assembleSearch(t, home)
	if seam.SearchReady() {
		t.Fatal("装配时库里还没有 key，不能算就绪")
	}
	storeCredential(t, home, "DEEPSEEK_API_KEY", laterKey)
	if !seam.SearchReady() {
		t.Fatal("存下 key 之后必须立刻就绪（不能等重启）")
	}
	assertProviderKey(t, seam, seenKey, laterKey)
}

// assertProviderKey 选 provider、真发一次搜索，断言请求头里是 wantKey。
//
// 断言文本里**不出现**任何凭据值：只说要的是哪一档（库里的 / 环境的），
// 不把值打进失败信息。值一旦进断言文本，测试日志就成了一条泄漏路径。
func assertProviderKey(t *testing.T, seam *spi.WebProviders, seenKey func() string, wantKey string) {
	t.Helper()
	provider, err := seam.SelectSearch()
	if err != nil {
		t.Fatalf("选搜索 provider 失败: %v", err)
	}
	if provider.ID() != search.DeepSeekProviderID {
		t.Fatalf("应当选中 DeepSeek 原生搜索，实际选中 %q", provider.ID())
	}
	if _, err := provider.Search(context.Background(), "官方同步", 8); err != nil {
		t.Fatalf("搜索失败: %v", err)
	}
	if got := seenKey(); got != wantKey {
		t.Fatalf("请求头里的 key 不是预期那一档（凭据库/环境的优先级反了或没接上）")
	}
}
