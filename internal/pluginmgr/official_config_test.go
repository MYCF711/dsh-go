package pluginmgr

// GO.7 / T9：plugin 线三条官方参数的真实现（先红后绿）。
//
// 三条对应的官方原件（app.asar 只读解出）：
//   - `pluginManager.versionLookupConcurrency` @ dsh-plugin-manager/lib/index.js:1953
//     默认 4；官方 `mapConcurrent(listed, this.versionLookupConcurrency, …)`（:2407）
//     用它把"每个版本的字段查询"限制成最多 N 路并发。
//   - `pluginManager.fallbackRegistries` @ dsh-plugin-manager/lib/index.js:1951
//     默认 `["https://registry.npmmirror.com/"]`；官方 `registryPlan()`（:1076-1096）
//     决定"一次操作按什么顺序问哪些源"，其中最关键的一条是**私有源绝不外溢到公共源**。
//   - `pluginManager.toolsMounted` @ dsh-base/cordis.patch.yml:15-18
//     官方 `tool-plugin-manager` 行 `disabled: true`（默认不把管理工具挂进模型工具表）。
//
// 这三条在 GO.7 之前是 UNREAD-GAP：宿主没有并发池、`Registries()` 里
// `FallbackRegistries: []string{}` 写死为空、插件工具一旦启用就恒挂载。

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// #14 pluginManager.versionLookupConcurrency（默认 4）
// ---------------------------------------------------------------------------

func TestVersionLookupConcurrencyOfficialDefaultAndEnv(t *testing.T) {
	if got := VersionLookupConcurrency(func(string) string { return "" }); got != OfficialVersionLookupConcurrency {
		t.Fatalf("未设时必须是官方默认 %d，得到 %d", OfficialVersionLookupConcurrency, got)
	}
	if OfficialVersionLookupConcurrency != 4 {
		t.Fatalf("官方默认值必须是 4（dsh-plugin-manager/lib/index.js:1953），得到 %d", OfficialVersionLookupConcurrency)
	}
	cases := []struct {
		raw  string
		want int
	}{
		{"", 4},    // 未设 → 官方默认
		{"  ", 4},  // 只有空白 = 未设
		{"1", 1},   // 1 = 串行
		{"7", 7},   // 显式放大
		{"0", 1},   // 官方 min(1)：0 不是"关掉并发"，是非法值 → 夹到 1
		{"-3", 1},  // 负数同理
		{"abc", 4}, // 解析不了 → 回默认（不静默变成 0 路）
		{"2.9", 4}, // 非整数 → 回默认
		{" 12 ", 12},
	}
	for _, tc := range cases {
		getenv := func(key string) string {
			if key != EnvVersionLookupConcurrency {
				t.Fatalf("读了不该读的变量 %q", key)
			}
			return tc.raw
		}
		if got := VersionLookupConcurrency(getenv); got != tc.want {
			t.Fatalf("%s=%q → %d，want %d", EnvVersionLookupConcurrency, tc.raw, got, tc.want)
		}
	}
}

// concreteInts 给泛型助手一个真实切片（测试里不引入 reflect）。
func concreteInts(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

func TestMapConcurrentBoundsInFlightAndKeepsOrder(t *testing.T) {
	const items = 8
	const unit = 20 * time.Millisecond

	run := func(limit int) (out []int, peak int, elapsed time.Duration) {
		var mu sync.Mutex
		var inFlight, max int64
		work := func(i int) int {
			mu.Lock()
			inFlight++
			if inFlight > max {
				max = inFlight
			}
			mu.Unlock()
			time.Sleep(unit)
			mu.Lock()
			inFlight--
			mu.Unlock()
			return i * 2
		}
		start := time.Now()
		out = MapConcurrent(concreteInts(items), limit, work)
		elapsed = time.Since(start)
		return out, int(atomic.LoadInt64(&max)), elapsed
	}

	// 顺序必须与输入一致（并发不等于乱序结算）。
	out, peak, elapsed := run(4)
	for i, v := range out {
		if v != i*2 {
			t.Fatalf("结果顺序被打乱：out[%d]=%d want %d（%v）", i, v, i*2, out)
		}
	}
	if peak > 4 {
		t.Fatalf("limit=4 时同时在飞 %d 路，超过上限", peak)
	}
	if peak < 4 {
		t.Fatalf("limit=4 时峰值只有 %d 路：并发池没有真的并发（8 件 / 4 路应该是 2 波）", peak)
	}
	if elapsed >= items*unit {
		t.Fatalf("limit=4 用了 %v，与串行（%v）无区别", elapsed, items*unit)
	}

	// limit=1 必须是真串行：峰值 1 路，且耗时不少于 N 个单元。
	_, peak1, elapsed1 := run(1)
	if peak1 != 1 {
		t.Fatalf("limit=1 时峰值 %d 路，必须恰好 1（串行）", peak1)
	}
	if elapsed1 < items*unit {
		t.Fatalf("limit=1 只用了 %v，少于串行下界 %v", elapsed1, items*unit)
	}

	// limit 超过件数时不应人为限速，也不应超过件数。
	_, peakN, elapsedN := run(items + 10)
	if peakN != items {
		t.Fatalf("limit>N 时峰值 %d 路，want %d（全部并发）", peakN, items)
	}
	if elapsedN >= items*unit {
		t.Fatalf("limit>N 用了 %v，与串行无区别", elapsedN)
	}

	// 非法 limit（<=0）按 1 处理，绝不变成"无上限"。
	_, peakZero, _ := run(0)
	if peakZero != 1 {
		t.Fatalf("limit=0 时峰值 %d 路，必须夹到 1", peakZero)
	}

	// 空输入不 panic、不阻塞。
	if out := MapConcurrent([]int(nil), 4, func(i int) int { return i }); len(out) != 0 {
		t.Fatalf("空输入返回 %v", out)
	}
}

// ---------------------------------------------------------------------------
// #15 pluginManager.fallbackRegistries（默认 ["https://registry.npmmirror.com/"]）
// ---------------------------------------------------------------------------

func TestFallbackRegistriesOfficialDefaultAndEnv(t *testing.T) {
	if len(OfficialFallbackRegistries) != 1 || OfficialFallbackRegistries[0] != "https://registry.npmmirror.com/" {
		t.Fatalf("官方默认回退源必须是 [https://registry.npmmirror.com/]（index.js:1951/1045），得到 %v", OfficialFallbackRegistries)
	}
	none := func(string) string { return "" }
	got := FallbackRegistries(none)
	if len(got) != 1 || got[0] != "https://registry.npmmirror.com/" {
		t.Fatalf("未设时必须回官方默认列表，得到 %v", got)
	}

	cases := []struct {
		raw  string
		want []string
	}{
		{"", []string{"https://registry.npmmirror.com/"}}, // 未设 → 官方默认
		{"none", nil}, // 显式关闭
		{"https://a.example,https://b.example", []string{"https://a.example/", "https://b.example/"}}, // 逗号分隔 + 规范化
		{" https://a.example/ , https://a.example ", []string{"https://a.example/"}},                  // 去重
		{"https://A.Example", []string{"https://a.example/"}},                                         // 小写化（pnpm 比较形态）
		{"not-a-url", nil}, // 非法值不产生源
	}
	for _, tc := range cases {
		getenv := func(key string) string {
			if key != EnvFallbackRegistries {
				t.Fatalf("读了不该读的变量 %q", key)
			}
			return tc.raw
		}
		got := FallbackRegistries(getenv)
		if len(got) != len(tc.want) {
			t.Fatalf("%s=%q → %v，want %v", EnvFallbackRegistries, tc.raw, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("%s=%q → %v，want %v", EnvFallbackRegistries, tc.raw, got, tc.want)
			}
		}
	}
}

func TestRegistryPlanMatchesOfficialOrdering(t *testing.T) {
	npmmirror := "https://registry.npmmirror.com/"
	npm := "https://registry.npmjs.org/"
	private := "https://registry.internal.example/"

	cases := []struct {
		name       string
		requested  string
		configured string
		resolved   string
		fallbacks  []string
		want       []string
	}{
		{
			name:       "没指定源时先问配置的首源，再问回退源",
			configured: npm, fallbacks: []string{npmmirror},
			want: []string{npm, npmmirror},
		},
		{
			name:      "指定的源就在配置集合里：它排第一，其余按配置顺序",
			requested: npmmirror, configured: npm, fallbacks: []string{npmmirror},
			want: []string{npmmirror, npm},
		},
		{
			name:      "指定的私有源不在配置集合里：只问它自己（私有源绝不外溢到公共源）",
			requested: private, configured: npm, fallbacks: []string{npmmirror},
			want: []string{private},
		},
		{
			name:       "配置首源本身就等于第一个回退源：不能问两次",
			configured: npmmirror, fallbacks: []string{npmmirror},
			want: []string{npmmirror},
		},
		{
			name:       "没有配置首源且 pnpm 自有源未知：只剩回退源也不该被问（没有已知的公共身份）",
			configured: "", fallbacks: []string{npmmirror},
			want: nil,
		},
		{
			name:       "pnpm 自有源已知等于 npm 官方：它是一个合法的公共成员",
			configured: "", resolved: npm, fallbacks: []string{npmmirror},
			want: []string{npm, npmmirror},
		},
		{
			name: "一个源都没有：空计划（调用方必须回结构化 unavailable，不是去问空地址）",
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RegistryPlan(tc.requested, tc.configured, tc.resolved, tc.fallbacks)
			if len(got) != len(tc.want) {
				t.Fatalf("plan = %v，want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("plan[%d] = %q，want %q（整个 plan = %v）", i, got[i], tc.want[i], got)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// #16 pluginManager.toolsMounted（官方默认 false = 装了不挂）
// ---------------------------------------------------------------------------

func TestToolsMountedSwitchGatesPluginTools(t *testing.T) {
	manager, loader, table := newRuntime(t)
	pkg := writeWasmPackage(t, t.TempDir(), "acme.gated", "1.0.0", echoToolDecls())
	if _, err := manager.Install(pkg); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := manager.Enable("acme.gated"); err != nil {
		t.Fatalf("enable: %v", err)
	}

	// 默认（state.json 里没有 toolsMounted）= 现在行为：插件工具在模型工具表里。
	if !manager.ToolsMounted() {
		t.Fatalf("未设时 ToolsMounted() 必须为 true（不设即零影响：现状就是挂载）")
	}
	if _, ok := table.Get("echo"); !ok {
		t.Fatalf("未设时插件工具必须在表里，现有 %v", toolNames(table))
	}

	// 关掉挂载开关：工具从模型工具表消失，但**不是**卸载（重新打开能立刻回来）。
	if err := manager.SetToolsMounted(false); err != nil {
		t.Fatalf("SetToolsMounted(false): %v", err)
	}
	if manager.ToolsMounted() {
		t.Fatalf("SetToolsMounted(false) 之后 ToolsMounted() 仍为 true")
	}
	if _, ok := table.Get("echo"); ok {
		t.Fatalf("关掉挂载后 model 工具表里不该有 echo：%v", toolNames(table))
	}
	if names := toolNames(table); len(names) != 0 {
		t.Fatalf("关掉挂载后插件工具必须从表里整体消失，现有 %v", names)
	}
	if loaded := loader.Loaded(); len(loaded) != 1 || len(loaded[0].Tools) != 2 {
		t.Fatalf("关掉挂载不能卸载插件（装载集合必须不变）：%+v", loaded)
	}

	// 开关持久化：重开管理器后仍然是 false。
	root := manager.root
	if err := loader.Close(context.Background()); err != nil {
		t.Fatalf("close loader: %v", err)
	}
	reopened, err := Open(root)
	if err != nil {
		t.Fatalf("重开管理器: %v", err)
	}
	if reopened.ToolsMounted() {
		t.Fatalf("toolsMounted=false 没有落盘：重开管理器后又变成挂载")
	}

	// 打开回来：工具恢复到表里（不需要重新装载）。
	if err := reopened.SetToolsMounted(true); err != nil {
		t.Fatalf("SetToolsMounted(true): %v", err)
	}
	if !reopened.ToolsMounted() {
		t.Fatalf("SetToolsMounted(true) 之后仍为 false")
	}
	reopenedLoader, err := NewLoader(reopened)
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}
	defer func() { _ = reopenedLoader.Close(context.Background()) }()
	if _, err := reopenedLoader.SyncAll(context.Background()); err != nil {
		t.Fatalf("SyncAll: %v", err)
	}
	reopenedTable := reopenedLoader.Table(nil)
	if _, ok := reopenedTable.Get("echo"); !ok {
		t.Fatalf("打开挂载后插件工具必须回到表里，现有 %v", toolNames(reopenedTable))
	}
}

func TestToolsMountedEnvOverrideWinsOverState(t *testing.T) {
	manager, loader, _ := newRuntime(t)
	pkg := writeWasmPackage(t, t.TempDir(), "acme.env", "1.0.0", echoToolDecls())
	if _, err := manager.Install(pkg); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := manager.Enable("acme.env"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	getenv := func(key string) string {
		if key == EnvToolsMounted {
			return "false"
		}
		return ""
	}
	if manager.ToolsMountedWith(getenv) {
		t.Fatalf("%s=false 必须在 state 未设时生效（部署期的下线开关）", EnvToolsMounted)
	}
	if !manager.ToolsMounted() {
		t.Fatalf("环境变量只是覆盖读取，不能改 state：无 env 时仍应为 true")
	}
	if err := loader.Close(context.Background()); err != nil {
		t.Fatalf("close loader: %v", err)
	}
}
