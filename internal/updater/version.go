// version.go —— 版本号的解析与比较（宿主「更新检测」的比较内核）。
//
// 为什么需要它：宿主会从本仓的 GitHub release 读回一串 tag（`v9.8.2-go.3`、
// `9.8.2-gobeta.4`、`9.8.2-rc.2`、`nightly`…），再和**运行中的版本**比出"有没有新的"。
// 这件事只有一个地方最容易写错：**`.N` 必须按数字比，不能按字符串比**——
// 字符串比较会把 `go.9` 判成大于 `go.10`，于是刚发出去的版本被当成"该回退"。
// 所以解析、补段、逐段比较三件事全部收敛在本文件，调用方只拿 -1/0/1。
//
// 版本命名（仓规 §6，用户裁定）：
//
//	<上游主版本>-go.<N>       稳定版
//	<上游主版本>-gobeta.<N>   任务途中的测试版
//
// `.N` 持续递增、不因上游换 rc 而重置。本文件**不硬编码任何版本号**：
// 运行中的版本由调用方传入（宿主侧只来自唯一出处）。
//
// 规则（逐条实现，测试逐条钉住）：
//
//  1. 允许前导 `v` / `V`。
//  2. 主体是点分十进制数字段（`9.8.2` / `1.0` / `1`）；缺的段按 0 补齐再比
//     （`1.0` == `1.0.0`）。段数**不设上限**：按同一"缺的补 0"规则逐段比。
//  3. 主体全部相等时比预发布段（第一个 `-` 之后的部分，`+build` 元数据忽略）：
//     没有预发布 > 有预发布（`9.8.2` > `9.8.2-go.3`）；
//     两边都有则按 `.` 切段逐段比——纯数字段按数值比、数字段 < 非数字段、
//     两个非数字段按 ASCII 字节序比；所有段都相等时段数少的更小（`go` < `go.1`）。
//  4. 主体里任何一段不是纯数字 → 解析失败（空段 `1..2` 同样失败）。
//  5. 前导零按数值解析（`1.0.01` == `1.0.1`），**不算**失败。
//  6. 数字段一律走 `strconv.Atoi` 并检查溢出（`err != nil` 即解析失败）；
//     不用会溢出成负数的手写循环。
//  7. `CompareVersions` 两边都先 `ParseVersion`，任一失败即 `ok=false`——
//     **不返回"看起来像 0"的 cmp**（那会被调用方读成"版本相同"）。
package updater

import (
	"strconv"
	"strings"
)

// Version 是一个解析后的版本号。
//
// 字段是"照实记录"的两个部分，不做 major/minor/patch 的截断假设：
//
//	Numeric 是第一个 `-` 之前的主体数字段，按出现顺序（`9.8.2` -> [9 8 2]）；
//	Pre     是 `-` 之后按 `.` 切开的预发布段（`go.10` -> ["go" "10"]）；
//	        **nil = 没有预发布**，此时它大于任何带预发布的同主体版本（规则 3）。
//
// 只有 `ParseVersion` 产出的值才满足全部规则：手搓的 `Version` 里若塞进负数段
// 或溢出的数字段，`String` / 比较的结果不在契约之内。
type Version struct {
	// Numeric 是主体数字段，至少一段。
	Numeric []int
	// Pre 是预发布段；nil 表示没有预发布。
	Pre []string
}

// ParseVersion 解析一个版本号；ok=false 表示解析不了（规则 4/6）。
//
// 接受的形态：`[v|V]` + 点分十进制主体 + 可选 `-预发布` + 可选 `+元数据`（忽略）。
// 不接受的形态：空串、非数字主体段、空段（`1..2`、`1.-2`、`9.8.2-`）、
// 溢出数字段（`strconv.Atoi` 报错）。前导零合法（规则 5）。
func ParseVersion(raw string) (Version, bool) {
	if raw == "" {
		return Version{}, false
	}
	body := raw
	if body[0] == 'v' || body[0] == 'V' {
		body = body[1:]
	}
	// `+build` 元数据不参与比较（规则 3）：先切掉再找 `-`，因为元数据里也可能有 `-`。
	if index := strings.IndexByte(body, '+'); index >= 0 {
		body = body[:index]
	}

	core := body
	var pre []string
	if index := strings.IndexByte(body, '-'); index >= 0 {
		core = body[:index]
		pre = strings.Split(body[index+1:], ".")
	}

	numeric, ok := parseNumericSegments(core)
	if !ok {
		return Version{}, false
	}
	// 逐段校验预发布段。**不写 `if pre != nil`**：`pre` 为 nil 时 range 本来就不走一圈，
	// 外面再套一层 nil 判空是死条件（staticcheck S1031）。
	for _, segment := range pre {
		// 空段不是合法段（与 `1..2` 同一条判据）。
		if segment == "" {
			return Version{}, false
		}
		// 纯数字的预发布段在这里就查溢出（规则 6），
		// 这样 `ParseVersion` 的 ok 与 `CompareVersions` 的 ok 永远一致。
		if isDigits(segment) {
			if _, err := strconv.Atoi(segment); err != nil {
				return Version{}, false
			}
		}
	}
	return Version{Numeric: numeric, Pre: pre}, true
}

// String 返回规范化写法（不带前导 `v`）。
//
// 规范化 = 主体补足三段（`1` -> `1.0.0`、`1.2` -> `1.2.0`，超过三段照原样）
// + 有预发布时接 `-` 与各段。元数据不保留（它不参与比较）。
func (v Version) String() string {
	numeric := v.Numeric
	if len(numeric) < 3 {
		padded := make([]int, 3)
		copy(padded, numeric)
		numeric = padded
	}
	parts := make([]string, 0, len(numeric)+len(v.Pre))
	for _, value := range numeric {
		parts = append(parts, strconv.Itoa(value))
	}
	out := strings.Join(parts, ".")
	if len(v.Pre) > 0 {
		out += "-" + strings.Join(v.Pre, ".")
	}
	return out
}

// CompareVersions 比较两个版本号：-1 / 0 / 1（a<b / a==b / a>b）。
//
// 任一无法解析时 ok=false、cmp=0——调用方**必须**先看 ok（规则 7）。
func CompareVersions(a, b string) (cmp int, ok bool) {
	left, ok := ParseVersion(a)
	if !ok {
		return 0, false
	}
	right, ok := ParseVersion(b)
	if !ok {
		return 0, false
	}
	return compareVersions(left, right), true
}

// IsOurReleaseTag 报告 tag 是不是**本仓自己的发布命名**。
//
// 只有 `<n>.<n>.<n>-go.<n>` 与 `<n>.<n>.<n>-gobeta.<n>` 两种形态算 ours
// （允许前导 `v`/`V`，三段主体，最后一段必须是纯数字）。
// 别人的 `-rc.2`、`v1.0`、`nightly`、`9.8.2-go.x`、`9.8.2-go.3-extra` 都不算。
func IsOurReleaseTag(tag string) bool {
	_, channel, ok := ourReleaseShape(tag)
	return ok && (channel == channelGo || channel == channelGobeta)
}

// IsBetaTag 报告 tag 是不是本仓的测试版命名（`<n>.<n>.<n>-gobeta.<n>`）。
func IsBetaTag(tag string) bool {
	_, channel, ok := ourReleaseShape(tag)
	return ok && channel == channelGobeta
}

// 两个发布通道名，逐字照仓规 §6。
const (
	channelGo     = "go"
	channelGobeta = "gobeta"
)

// ourReleaseShape 是本仓发布 tag 的**严格**形态判据：
// `[v]<n>.<n>.<n>-<go|gobeta>.<n>`，逐字匹配，多一段少一段都不算。
//
// 为什么不用宽松的 `ParseVersion` 结果直接判：`9.8.2-go.3-extra` 与
// `9.8.2-go.1.2` 都能被 `ParseVersion` 解析（它们是合法版本号），
// 但它们**不是我们的发布 tag**——更新检测要拿它筛 release，放过去就会拿到别人的 tag。
func ourReleaseShape(tag string) (version Version, channel string, ok bool) {
	if tag == "" {
		return Version{}, "", false
	}
	// 我们的发布 tag 从不带 `+build` 元数据；带了就不是那两种形态之一。
	if strings.IndexByte(tag, '+') >= 0 {
		return Version{}, "", false
	}
	version, ok = ParseVersion(tag)
	if !ok {
		return Version{}, "", false
	}
	if len(version.Numeric) != 3 || len(version.Pre) != 2 {
		return Version{}, "", false
	}
	if version.Pre[0] != channelGo && version.Pre[0] != channelGobeta {
		return Version{}, "", false
	}
	if _, numeric := parseNumericSegment(version.Pre[1]); !numeric {
		return Version{}, "", false
	}
	return version, version.Pre[0], true
}

// NormalizeTag 去掉前导 `v` / `V`（其余原样返回，**不做规范化重写**）；空串返回空串。
//
// 与 `ParseVersion(...).String()` 的区别：那个会把 `9.8` 写成 `9.8.0`，
// 而 tag 是要拿去和 GitHub 上的名字对账的，只削前导 `v`、别的一个字节都不动。
func NormalizeTag(tag string) string {
	if tag != "" && (tag[0] == 'v' || tag[0] == 'V') {
		return tag[1:]
	}
	return tag
}

// compareVersions 是解析结果之间的比较（两边都已合法）。
func compareVersions(a, b Version) int {
	if cmp := compareNumeric(a.Numeric, b.Numeric); cmp != 0 {
		return cmp
	}
	return comparePre(a.Pre, b.Pre)
}

// compareNumeric 逐段比主体；缺的段按 0 补（规则 2）。
func compareNumeric(a, b []int) int {
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		var left, right int
		if i < len(a) {
			left = a[i]
		}
		if i < len(b) {
			right = b[i]
		}
		if left != right {
			if left < right {
				return -1
			}
			return 1
		}
	}
	return 0
}

// comparePre 比预发布段（规则 3）。
func comparePre(a, b []string) int {
	switch {
	case len(a) == 0 && len(b) == 0:
		return 0
	case len(a) == 0:
		// 没有预发布 > 有预发布。
		return 1
	case len(b) == 0:
		return -1
	}
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if cmp := comparePreSegment(a[i], b[i]); cmp != 0 {
			return cmp
		}
	}
	switch {
	case len(a) < len(b):
		// 段数少的更小：`go` < `go.1`。
		return -1
	case len(a) > len(b):
		return 1
	}
	return 0
}

// comparePreSegment 比单个预发布段：数字段按数值、数字段 < 非数字段、
// 非数字段按 ASCII 字节序（Go 的字符串比较就是无符号逐字节比较）。
func comparePreSegment(a, b string) int {
	left, leftNumeric := numericPreSegment(a)
	right, rightNumeric := numericPreSegment(b)
	switch {
	case leftNumeric && rightNumeric:
		switch {
		case left < right:
			return -1
		case left > right:
			return 1
		}
		return 0
	case leftNumeric:
		return -1
	case rightNumeric:
		return 1
	}
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// numericPreSegment 报告一个预发布段是不是纯数字段，是则给出数值。
//
// 溢出在这里按"非数字段"处理：`ParseVersion` 已经挡掉了溢出的纯数字段，
// 所以只有手搓的 `Version` 才可能走到这个分支，契约之外。
func numericPreSegment(segment string) (int, bool) {
	if !isDigits(segment) {
		return 0, false
	}
	value, err := strconv.Atoi(segment)
	if err != nil {
		return 0, false
	}
	return value, true
}

// parseNumericSegments 解析点分十进制主体（规则 4/5/6）。
func parseNumericSegments(core string) ([]int, bool) {
	parts := strings.Split(core, ".")
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		value, ok := parseNumericSegment(part)
		if !ok {
			return nil, false
		}
		out = append(out, value)
	}
	return out, true
}

// parseNumericSegment 解析一个纯数字段；空段、非数字、溢出都算失败。
func parseNumericSegment(part string) (int, bool) {
	if !isDigits(part) {
		return 0, false
	}
	value, err := strconv.Atoi(part)
	if err != nil {
		// 溢出（`Atoi` 报 ErrRange）：解析失败，而不是当成某个负数。
		return 0, false
	}
	return value, true
}

// isDigits 报告 s 是否非空且全是 ASCII 数字。
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
