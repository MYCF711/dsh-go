// version_test.go —— `version.go` 的行为断言（白盒：要直接看解析后的字段）。
//
// 为什么白盒（`package updater` 而不是 `channel_test.go` 的 `updater_test`）：
// 这里的判据落在**未导出**的解析细节上（数字段切片、预发布段切片），
// 只从外部黑盒比 -1/0/1 看不出"为什么"。
//
// **夹具只用假版本**（`9.8.x` / `9.9.x`）：仓规有一条守卫
// （`cmd/dshd/version_guard_test.go`）会扫 `internal/` 下 .go 文件的**字符串字面量**，
// 一旦出现当前发布前缀就 FAIL。所以本文件里不出现任何真实版本号，
// 也就不会因为发版换号而变红。
package updater

import (
	"math/rand"
	"sort"
	"testing"
)

// compareOK 取比较结果，顺带把"解析失败"和"返回值越界"判成测试失败。
//
// 单列一个 helper 是因为规则 7 的坑在这：解析失败时 `cmp` 也是 0，
// 若调用方只看 `cmp`，"根本解析不了"会被读成"版本相同"。
func compareOK(t *testing.T, a, b string) int {
	t.Helper()
	cmp, ok := CompareVersions(a, b)
	if !ok {
		t.Fatalf("CompareVersions(%q, %q) 解析失败（ok=false）", a, b)
	}
	if cmp < -1 || cmp > 1 {
		t.Fatalf("CompareVersions(%q, %q) = %d，只允许返回 -1/0/1", a, b, cmp)
	}
	return cmp
}

// TestCompareVersionsNumericSuffix 钉住本任务的核心：
// `.N` 必须**按数字**比，字符串比较会把 `go.9` 判成大于 `go.10`。
func TestCompareVersionsNumericSuffix(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"9.8.2-go.10", "9.8.2-go.9", 1},
		{"9.8.2-go.9", "9.8.2-go.10", -1},
		{"9.8.2-go.100", "9.8.2-go.99", 1},
		{"9.8.2-go.2", "9.8.2-go.1", 1},
		{"9.8.2-go.1", "9.8.2-go.2", -1},
		{"9.8.2-gobeta.10", "9.8.2-gobeta.9", 1},
	}
	for _, c := range cases {
		if got := compareOK(t, c.a, c.b); got != c.want {
			t.Fatalf("CompareVersions(%q, %q) = %d，want %d（数字段必须按数值比）", c.a, c.b, got, c.want)
		}
	}
}

// TestCompareVersionsAcrossCoreVersions 钉住"主体（major/minor/patch）先决"：
// 主体不等时，预发布段根本不参与比较。
func TestCompareVersionsAcrossCoreVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"9.9.2-go.1", "9.8.1-go.31", 1},
		{"9.8.1-go.31", "9.9.2-go.1", -1},
		{"9.8.2-go.9", "9.8.3-go.1", -1},
		{"9.8.3-go.1", "9.8.2-go.9", 1},
		// 缺的段按 0 补齐：9.8 == 9.8.0，所以 9.9 > 9.8。
		{"9.9", "9.8.1-go.31", 1},
		// 段数多了照比：第四段与"补 0"比。
		{"9.8.2.1", "9.8.2", 1},
		{"9.8.2.0", "9.8.2", 0},
	}
	for _, c := range cases {
		if got := compareOK(t, c.a, c.b); got != c.want {
			t.Fatalf("CompareVersions(%q, %q) = %d，want %d", c.a, c.b, got, c.want)
		}
	}
}

// TestCompareVersionsPrereleaseRules 钉住规则 3 的每一条。
func TestCompareVersionsPrereleaseRules(t *testing.T) {
	cases := []struct {
		a, b string
		want int
		why  string
	}{
		{"9.8.2", "9.8.2-go.10", 1, "没有预发布 > 有预发布"},
		{"9.8.2-go.10", "9.8.2", -1, "没有预发布 > 有预发布"},
		{"9.8.2", "9.8.2-rc.2", 1, "没有预发布 > 有预发布"},
		{"9.8.2-go", "9.8.2-go.1", -1, "段数少的小：go < go.1"},
		{"9.8.2-go.1", "9.8.2-go", 1, "段数少的小：go < go.1"},
		{"9.8.2-go.1", "9.8.2-go.x", -1, "数字段 < 非数字段"},
		{"9.8.2-go.x", "9.8.2-go.1", 1, "数字段 < 非数字段"},
		{"9.8.2-go.x", "9.8.2-go.y", -1, "非数字段按 ASCII 字节序（x < y）"},
		{"9.8.2-go.y", "9.8.2-go.x", 1, "非数字段按 ASCII 字节序"},
		{"9.8.2-go.1", "9.8.2-go.1", 0, "逐段全等"},
		{"9.8.2-go.1.0", "9.8.2-go.1", 1, "前缀全等时段数多的更大"},
		{"9.8.2-go.01", "9.8.2-go.1", 0, "前导零按数值解析"},
		{"9.8.2+meta.7", "9.8.2", 0, "+build 元数据忽略"},
		// 规则 3 的确定结论：`-rc.2` 与 `-go.3` 都是预发布段，逐段比时
		// 首段 `rc` 与 `go` 都不是数字 -> 按 ASCII 字节序，'g'(103) < 'r'(114)，
		// 所以 `9.8.2-go.3` 更小、`9.8.2-rc.2` 更大。
		{"9.8.2-rc.2", "9.8.2-go.3", 1, "ASCII 字节序：go < rc"},
		{"9.8.2-go.3", "9.8.2-rc.2", -1, "ASCII 字节序：go < rc"},
	}
	for _, c := range cases {
		if got := compareOK(t, c.a, c.b); got != c.want {
			t.Fatalf("CompareVersions(%q, %q) = %d，want %d（%s）", c.a, c.b, got, c.want, c.why)
		}
	}
}

// TestParseVersionEquality 钉住"相等"的几种写法。
func TestParseVersionEquality(t *testing.T) {
	cases := [][2]string{
		{"9.8.2-go.3", "v9.8.2-go.3"},
		{"9.8.2-go.3", "V9.8.2-go.3"},
		{"9.8.0", "9.8"},
		{"9.8", "9.8.0.0"},
		{"1.0.1", "1.0.01"},
		{"9.8.2-go.03", "9.8.2-go.3"},
		{"9.8.2-gobeta.4", "v9.8.2-gobeta.4"},
	}
	for _, c := range cases {
		if got := compareOK(t, c[0], c[1]); got != 0 {
			t.Fatalf("CompareVersions(%q, %q) = %d，want 0", c[0], c[1], got)
		}
	}
}

// TestParseVersionFailures 钉住规则 4/6/7：这些都必须 ok=false，
// 且 `CompareVersions` 也必须是 ok=false（不能返回"看起来像 0"的 cmp）。
func TestParseVersionFailures(t *testing.T) {
	bad := []string{
		"",
		"nightly",
		"1.x.0",
		"1..2",
		"-1.0",
		"v",
		"V",
		"1.-2",
		"9.8.2-",
		"9.8.2-go.",
		"9.8.2-.go",
		"9.8.2-go..1",
		"v.v9.8.2",
		"9,8,2",
		" 9.8.2",
		"9.8.2 ",
		// 溢出（规则 6）：`strconv.Atoi` 报 ErrRange。
		"99999999999999999999.0.0",
		"9.8.2-go.99999999999999999999",
	}
	for _, raw := range bad {
		if version, ok := ParseVersion(raw); ok {
			t.Fatalf("ParseVersion(%q) = %+v，want ok=false", raw, version)
		}
		cmp, ok := CompareVersions(raw, "9.8.2-go.1")
		if ok {
			t.Fatalf("CompareVersions(%q, 9.8.2-go.1) = %d, ok=true，want ok=false（规则 7）", raw, cmp)
		}
		cmp, ok = CompareVersions("9.8.2-go.1", raw)
		if ok {
			t.Fatalf("CompareVersions(9.8.2-go.1, %q) = %d, ok=true，want ok=false（规则 7）", raw, cmp)
		}
	}
}

// TestParseVersionFields 是白盒断言：解析后的字段与规范化写法。
func TestParseVersionFields(t *testing.T) {
	cases := []struct {
		raw       string
		numeric   []int
		pre       []string
		canonical string
	}{
		{"v9.8.2-go.3", []int{9, 8, 2}, []string{"go", "3"}, "9.8.2-go.3"},
		{"9.8.2-gobeta.04", []int{9, 8, 2}, []string{"gobeta", "04"}, "9.8.2-gobeta.04"},
		{"9.8", []int{9, 8}, nil, "9.8.0"},
		{"1", []int{1}, nil, "1.0.0"},
		{"9.8.2+meta.7", []int{9, 8, 2}, nil, "9.8.2"},
		{"9.8.2-go.3+meta.7", []int{9, 8, 2}, []string{"go", "3"}, "9.8.2-go.3"},
		{"9.8.2.1-go.1", []int{9, 8, 2, 1}, []string{"go", "1"}, "9.8.2.1-go.1"},
	}
	for _, c := range cases {
		version, ok := ParseVersion(c.raw)
		if !ok {
			t.Fatalf("ParseVersion(%q) 解析失败", c.raw)
		}
		if len(version.Numeric) != len(c.numeric) || len(version.Pre) != len(c.pre) {
			t.Fatalf("ParseVersion(%q) = {Numeric:%v Pre:%v}，want {Numeric:%v Pre:%v}",
				c.raw, version.Numeric, version.Pre, c.numeric, c.pre)
		}
		for i, want := range c.numeric {
			if version.Numeric[i] != want {
				t.Fatalf("ParseVersion(%q).Numeric[%d] = %d，want %d", c.raw, i, version.Numeric[i], want)
			}
		}
		for i, want := range c.pre {
			if version.Pre[i] != want {
				t.Fatalf("ParseVersion(%q).Pre[%d] = %q，want %q", c.raw, i, version.Pre[i], want)
			}
		}
		if got := version.String(); got != c.canonical {
			t.Fatalf("ParseVersion(%q).String() = %q，want %q（规范化写法不带前导 v）", c.raw, got, c.canonical)
		}
		// 规范化写法必须还能解析，且与自身相等（String 与 Parse 互为逆）。
		again, ok := ParseVersion(version.String())
		if !ok {
			t.Fatalf("ParseVersion(%q) 的结果 %q 解析不回去", c.raw, version.String())
		}
		if again.String() != c.canonical {
			t.Fatalf("往返后写法变了：%q -> %q -> %q", c.raw, c.canonical, again.String())
		}
		if got := compareOK(t, c.raw, version.String()); got != 0 {
			t.Fatalf("CompareVersions(%q, %q) = %d，want 0", c.raw, version.String(), got)
		}
	}
}

// TestVersionSortOrder 是"真的排一遍"的断言：打乱一组版本，
// 用 `CompareVersions` 当 `sort.Slice` 的 less，断言逐项顺序正确。
func TestVersionSortOrder(t *testing.T) {
	shuffled := []string{
		"9.8.2-go.10", "9.9.1-go.1", "9.8.2", "9.8.2-go.2",
		"9.8.3-go.1", "9.8.2-rc.2", "9.8.2-go.100", "9.8.1-go.31",
		"9.9.2-go.1", "9.8.2-go.1", "9.8.2-go.99", "9.8.2-go.9",
	}
	want := []string{
		"9.8.1-go.31",
		"9.8.2-go.1",
		"9.8.2-go.2",
		"9.8.2-go.9",
		"9.8.2-go.10",
		"9.8.2-go.99",
		"9.8.2-go.100",
		"9.8.2-rc.2",
		"9.8.2",
		"9.8.3-go.1",
		"9.9.1-go.1",
		"9.9.2-go.1",
	}
	if len(shuffled) != len(want) {
		t.Fatalf("夹具长度不一致：%d vs %d", len(shuffled), len(want))
	}
	sorted := append([]string(nil), shuffled...)
	sort.Slice(sorted, func(i, j int) bool {
		return compareOK(t, sorted[i], sorted[j]) < 0
	})
	for i := range want {
		if sorted[i] != want[i] {
			t.Fatalf("排序后第 %d 项 = %q，want %q（完整结果 %v）", i, sorted[i], want[i], sorted)
		}
	}
	// 相邻项必须严格递增（本夹具里没有相等的两项）。
	for i := 0; i+1 < len(sorted); i++ {
		if got := compareOK(t, sorted[i], sorted[i+1]); got != -1 {
			t.Fatalf("排序后 %q 与 %q 的比较 = %d，want -1", sorted[i], sorted[i+1], got)
		}
	}
	// 字符串排序会把 `go.10` 排到 `go.9` 前面——显式钉住"不是字符串比较"。
	// 两者必须真的不同，否则这条断言没有区分度（夹具退化了也看不出来）。
	stringSorted := append([]string(nil), shuffled...)
	sort.Strings(stringSorted)
	same := true
	for i := range want {
		if stringSorted[i] != want[i] {
			same = false
			break
		}
	}
	if same {
		t.Fatalf("字符串排序与数字排序结果相同（%v），夹具已失去区分度", stringSorted)
	}
}

// TestIsOurReleaseTag 钉住本仓发布命名的**严格**形态。
func TestIsOurReleaseTag(t *testing.T) {
	ours := []string{
		"v9.8.2-go.3",
		"9.8.2-go.3",
		"V9.8.2-go.10",
		"9.8.2-gobeta.4",
		"v9.8.2-gobeta.1",
		"9.8.2-go.01",
	}
	for _, tag := range ours {
		if !IsOurReleaseTag(tag) {
			t.Fatalf("IsOurReleaseTag(%q) = false，want true", tag)
		}
	}
	notOurs := []string{
		"v9.8.2-rc.2",
		"v9.8",
		"nightly",
		"",
		"9.8.2-go.x",
		"9.8.2-go.3-extra",
		"9.8.2-go",
		"9.8.2-go.3.4",
		"9.8.2.1-go.1",
		"1.0.0-rc.1-go.1",
		"9.8.2-GO.1",
		"v1.0",
		"9.8.2-go.3+build.9",
		"9.8.2-go.-1",
		"v9.8.2-gobeta.x",
	}
	for _, tag := range notOurs {
		if IsOurReleaseTag(tag) {
			t.Fatalf("IsOurReleaseTag(%q) = true，want false", tag)
		}
	}
}

// TestIsBetaTag 钉住测试版命名（只有 `-gobeta.<n>`）。
func TestIsBetaTag(t *testing.T) {
	if !IsBetaTag("9.9.1-gobeta.4") {
		t.Fatalf("IsBetaTag(9.9.1-gobeta.4) = false，want true")
	}
	if !IsBetaTag("v9.9.1-gobeta.4") {
		t.Fatalf("IsBetaTag(v9.9.1-gobeta.4) = false，want true")
	}
	for _, tag := range []string{"9.9.1-go.4", "9.9.1-rc.1", "9.9.1", "", "9.9.1-gobeta.x", "9.9.1-gobeta"} {
		if IsBetaTag(tag) {
			t.Fatalf("IsBetaTag(%q) = true，want false", tag)
		}
	}
	// `-gobeta.` 一定是 ours（beta 是 ours 的子集）。
	if !IsOurReleaseTag("9.9.1-gobeta.4") {
		t.Fatalf("beta tag 必须同时是 ours")
	}
}

// TestNormalizeTag 钉住"只削前导 v，其余一个字节都不动"。
func TestNormalizeTag(t *testing.T) {
	cases := []struct{ in, want string }{
		{"v9.8.2-go.3", "9.8.2-go.3"},
		{"V9.8.2-go.3", "9.8.2-go.3"},
		{"9.8.2-go.3", "9.8.2-go.3"},
		{"9.8.2", "9.8.2"},
		{"v9.8", "9.8"},
		{"", ""},
		{"v", ""},
		{"vv9.8.2-go.3", "v9.8.2-go.3"},
		{"nightly", "nightly"},
	}
	for _, c := range cases {
		if got := NormalizeTag(c.in); got != c.want {
			t.Fatalf("NormalizeTag(%q) = %q，want %q", c.in, got, c.want)
		}
	}
	// 规范化后的 tag 必须仍是 ours（前导 v 不影响判定）。
	if !IsOurReleaseTag(NormalizeTag("v9.8.2-go.3")) {
		t.Fatalf("NormalizeTag 之后 IsOurReleaseTag 变了")
	}
}

// TestVersionCompareProperties 是属性测试：反对称、自反、传递。
//
// 用**全对全 + 全三元组**（确定性穷举，比抽样强）再叠一层**固定种子**的抽样，
// 种子写死所以每次跑的结果完全一样——属性测试红一次就必须能原样重放。
func TestVersionCompareProperties(t *testing.T) {
	fixtures := []string{
		"9.8.2-go.9",
		"9.8.2-go.10",
		"9.8.2-go.9+build.7", // 与上一项必须相等（元数据忽略）
		"v9.8.2-go.9",        // 与上一项必须相等（前导 v）
		"9.8.2-go",
		"9.8.2-go.9.1",
		"9.8.2-rc.2",
		"9.8.2",
		"9.8.2-gobeta.2",
		"9.8.1-go.31",
		"1.0",
		"1.0.0",
		"01.0.1",
		"9.9.2-go.1",
	}
	for _, raw := range fixtures {
		if _, ok := ParseVersion(raw); !ok {
			t.Fatalf("夹具 %q 必须可解析", raw)
		}
		// 自反。
		if got := compareOK(t, raw, raw); got != 0 {
			t.Fatalf("CompareVersions(%q, %q) = %d，want 0（自反）", raw, raw, got)
		}
	}
	// 反对称 + 传递。
	for _, a := range fixtures {
		for _, b := range fixtures {
			ab := compareOK(t, a, b)
			ba := compareOK(t, b, a)
			if ab != -ba {
				t.Fatalf("反对称被破坏：Compare(%q, %q)=%d 而 Compare(%q, %q)=%d", a, b, ab, b, a, ba)
			}
			for _, c := range fixtures {
				bc := compareOK(t, b, c)
				ac := compareOK(t, a, c)
				if ab <= 0 && bc <= 0 && !(ac <= 0) {
					t.Fatalf("传递性被破坏：%q<=%q<=%q 但 Compare(%q, %q)=%d", a, b, c, a, c, ac)
				}
				if ab >= 0 && bc >= 0 && !(ac >= 0) {
					t.Fatalf("传递性被破坏：%q>=%q>=%q 但 Compare(%q, %q)=%d", a, b, c, a, c, ac)
				}
			}
		}
	}
	// 固定种子抽样（种子写死 = 可复现，不是真随机）。
	rng := rand.New(rand.NewSource(20261113))
	for i := 0; i < 500; i++ {
		a := fixtures[rng.Intn(len(fixtures))]
		b := fixtures[rng.Intn(len(fixtures))]
		c := fixtures[rng.Intn(len(fixtures))]
		ab := compareOK(t, a, b)
		if got := compareOK(t, b, a); got != -ab {
			t.Fatalf("抽样[%d] 反对称被破坏：Compare(%q, %q)=%d 而反向=%d", i, a, b, ab, got)
		}
		bc := compareOK(t, b, c)
		ac := compareOK(t, a, c)
		if ab <= 0 && bc <= 0 && !(ac <= 0) {
			t.Fatalf("抽样[%d] 传递性被破坏：%q<=%q<=%q 但 Compare(%q, %q)=%d", i, a, b, c, a, c, ac)
		}
		// 排序用到的严格弱序前提：`cmp == 0` 必须真的等价（互相为 0）。
		if ab == 0 && compareOK(t, b, a) != 0 {
			t.Fatalf("抽样[%d] 相等不对合：%q 与 %q", i, a, b)
		}
	}
}
