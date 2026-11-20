package prune_test

// 注意包名是 prune_test（外部测试包）：第 6 条要验证「投影函数接上了裁剪」，
// 需要 import internal/session，而 session 本身 import prune。外部测试包不构成循环。

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"dshgo/internal/prune"
	"dshgo/internal/session"
	"dshgo/internal/spi"
)

// lines 造 n 行、每行形如 "line-0001" 的正文（带行号便于断言"保留了哪几行"）。
func lines(n int) string {
	parts := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		parts = append(parts, "line-"+pad4(i))
	}
	return strings.Join(parts, "\n")
}

func pad4(i int) string {
	b := []byte{byte('0' + i/1000%10), byte('0' + i/100%10), byte('0' + i/10%10), byte('0' + i%10)}
	return string(b)
}

// runeLen 是"字符数"，单位与官方 codePointLength（`Array.from(s).length`）一致。
func runeLen(s string) int { return utf8.RuneCountInString(s) }

// cut 按**字符**切片，与官方 `Array.from(text).slice()` 同义。
func cut(s string, from, to int) string {
	r := []rune(s)
	if from < 0 {
		from = 0
	}
	if to > len(r) {
		to = len(r)
	}
	if from >= to {
		return ""
	}
	return string(r[from:to])
}

// 1. 最重要的一条：**不超预算就一个字都不动**。
// 裁剪是有代价的（模型看不见被砍的内容，可能要多调一次工具），
// 正常大小的结果不该被顺手整理。
func TestApplyPassesThroughSmallText(t *testing.T) {
	cases := []string{
		"",
		"短输出",
		"a\r\nb\r\n", // CRLF 也不许"顺手"改成 LF
		// 恰好等于字符预算——边界上不许裁（官方是 `totalChars <= thresholdChars` 就返回 null）。
		strings.Repeat("x", prune.DefaultThresholdChars),
		// 同样字符数的中文：**24576 字节**，远超以前那套 16KB 字节预算，但字符数是 8192。
		// 官方按码点量，所以它必须原样透传——这一条就是"按字节量中文会被多压一刀"的判据。
		strings.Repeat("中", prune.DefaultThresholdChars),
		// 600 行 / 5999 字符：行数很多但字数在预算内。旧实现有自造的 200 行上限，
		// 会把这种正常输出压掉一半；官方没有行维度，必须原样给模型看。
		lines(600),
	}
	for i, in := range cases {
		out, truncated, ev := prune.Apply(in, prune.Budget{})
		if truncated {
			t.Fatalf("case %d（%d 字符 / %d 字节）：没超预算却报 truncated=true", i, runeLen(in), len(in))
		}
		if ev != nil {
			t.Fatalf("case %d：没裁剪却给了 Truncation %+v", i, ev)
		}
		if out != in {
			t.Fatalf("case %d：正文被改动了（%d 字符 → %d 字符）", i, runeLen(in), runeLen(out))
		}
	}

	// 显式预算同样适用：预算内的结果原样返回。
	in := "只有 3 行\n第二行\n第三行"
	if out, truncated, _ := prune.Apply(in, prune.Budget{ThresholdChars: 100, HeadChars: 40, TailChars: 10}); truncated || out != in {
		t.Fatalf("显式预算内不该裁剪：truncated=%v out=%q", truncated, out)
	}
}

// 2. 超字符预算：输出**恰好**是「头 4096 + 官方 marker + 尾 1024」。
//
// 官方 `pruneContent`（lib/index.js:91-124，阈值判定在 :93、头尾切片在 :94-95/:107-111）就是这个形状，所以这里直接钉等号，
// 而不是只钉"头在、尾在"——形状一变（比例改成 60/40、尾段长度漂了）立刻红。
func TestApplyTruncatesToOfficialHeadMarkerTail(t *testing.T) {
	const total = 1000
	in := lines(total)
	inChars := runeLen(in)
	if inChars <= prune.DefaultThresholdChars {
		t.Fatalf("用例没超预算：%d 字符", inChars)
	}

	out, truncated, ev := prune.Apply(in, prune.Budget{})
	if !truncated || ev == nil {
		t.Fatal("超字符预算必须被裁剪，且要留下证据")
	}
	// Kept/Total 的单位是**字符**（官方口径），不是字节也不是行。
	if ev.Total != inChars {
		t.Fatalf("Total = %d，期望原始字符数 %d", ev.Total, inChars)
	}
	if runes := runeLen(out); runes > prune.DefaultThresholdChars {
		t.Fatalf("裁剪后 %d 字符，超过预算 %d", runes, prune.DefaultThresholdChars)
	}
	if ev.Kept != runeLen(out) {
		t.Fatalf("Kept = %d，与输出的实际字符数 %d 不符", ev.Kept, runeLen(out))
	}

	// 逐字符等号：头段 = 前 4096 字符，尾段 = 后 1024 字符。
	//
	// 期望值用**官方字面量**，不引用 prune 的常量：期望值如果也引用同一个常量，
	// 把常量改成 2048/512 这条仍会绿（实测：变异 prune-head-back-to-share 曾 MISSED）。
	// 官方 lib/index.js:12-13 = headChars 4096 / tailChars 1024。
	const (
		officialHead = 4096
		officialTail = 1024
		officialMark = "\n\n[... tool result middle pruned ...]\n\n"
	)
	if prune.DefaultHeadChars != officialHead || prune.DefaultTailChars != officialTail {
		t.Fatalf("头尾预算 = %d/%d，官方是 %d/%d（lib/index.js:12-13）",
			prune.DefaultHeadChars, prune.DefaultTailChars, officialHead, officialTail)
	}
	want := cut(in, 0, officialHead) + officialMark + cut(in, inChars-officialTail, inChars)
	if out != want {
		t.Fatalf("裁剪形状不是「头 %d + 官方 marker + 尾 %d」。\n得到（头 60 字符）%q\n期望（头 60 字符）%q",
			officialHead, officialTail, cut(out, 0, 60), cut(want, 0, 60))
	}
	// marker 恰好出现一次：重复 marker 会让模型以为被砍了两处。
	if n := strings.Count(out, prune.Marker); n != 1 {
		t.Fatalf("marker 出现了 %d 次，应当是 1 次", n)
	}
	// 头在、尾在、中间没了。
	if !strings.Contains(out, "line-0001") {
		t.Fatal("头（第 1 行）必须保留——那是命令/文件的开头")
	}
	if !strings.Contains(out, "line-1000") {
		t.Fatal("尾（最后一行）必须保留——错误和结论通常在最后")
	}
	if strings.Contains(out, "line-0500") {
		t.Fatal("中间的第 500 行不该还在")
	}
	t.Logf("%d 字符(%d 行) → %d 字符；Kept=%d Total=%d Pointer=%q",
		inChars, total, runeLen(out), ev.Kept, ev.Total, ev.Pointer)
}

// 3. 多字节字符：切点永远落在**完整字符**上，输出仍是合法 UTF-8。
//
// 官方按码点切（`Array.from(text).slice()`），所以永远不会劈开汉字；
// 宿主以前按字节切，靠 ToValidUTF8 事后丢掉残缺字节。这条钉住"现在不需要事后修"。
func TestApplyTruncatesCJKOnRuneBoundaries(t *testing.T) {
	// 2000 份 × 6 汉字 = 12000 字符 = 36000 字节。
	in := strings.Repeat("中文内容测试", 2000)
	inChars := runeLen(in)
	if inChars <= prune.DefaultThresholdChars {
		t.Fatalf("用例没超预算：%d 字符", inChars)
	}

	out, truncated, ev := prune.Apply(in, prune.Budget{})
	if !truncated || ev == nil {
		t.Fatal("超字符预算必须被裁剪")
	}
	if !utf8.ValidString(out) {
		t.Fatal("裁剪后不是合法 UTF-8")
	}
	if strings.ContainsRune(out, utf8.RuneError) {
		t.Fatal("裁剪后出现了 U+FFFD —— 切点落在字符中间，模型会看到一堆 �")
	}
	want := cut(in, 0, prune.DefaultHeadChars) + prune.Marker + cut(in, inChars-prune.DefaultTailChars, inChars)
	if out != want {
		t.Fatalf("中文正文的裁剪形状不对：头段 %q … 尾段 %q", cut(out, 0, 6), cut(out, runeLen(out)-6, runeLen(out)))
	}
	if ev.Total != inChars {
		t.Fatalf("Total 应是字符数 %d，实际 %d", inChars, ev.Total)
	}
	t.Logf("%d 字符(%d 字节) → %d 字符；Kept=%d Total=%d", inChars, len(in), runeLen(out), ev.Kept, ev.Total)

	// 显式预算（含退化到比 marker 还小的阈值）：硬约束是"输出字符数 ≤ 阈值"，
	// 且永远是合法 UTF-8。
	for _, budget := range []prune.Budget{
		{ThresholdChars: 100, HeadChars: 80, TailChars: 10},
		{ThresholdChars: 100, HeadChars: 1, TailChars: 1}, // 头尾挤到极限
		{ThresholdChars: 20, HeadChars: 5, TailChars: 5},  // 阈值 < marker 长度 → 退化路径
		{ThresholdChars: 1, HeadChars: 1, TailChars: 1},   // 只留 1 个字符
	} {
		out, truncated, _ := prune.Apply(in, budget)
		if !truncated {
			t.Fatalf("%+v：应当裁剪", budget)
		}
		if n := runeLen(out); n > budget.ThresholdChars {
			t.Fatalf("%+v：裁剪后 %d 字符，超过阈值", budget, n)
		}
		if !utf8.ValidString(out) {
			t.Fatalf("%+v：输出不是合法 UTF-8", budget)
		}
		if strings.ContainsRune(out, utf8.RuneError) {
			t.Fatalf("%+v：输出里出现 U+FFFD", budget)
		}
	}
}

// 4. Pointer 不许为空：要么给 artifact 路径，要么明说"没有外置"。
//
// 官方 pruner 没有 pointer 概念（它只留头尾 + marker），这一条是宿主为 spill
// 补的更严约束：模型永远必须拿到一个非空的"去哪取"。
func TestApplyPointerNeverEmpty(t *testing.T) {
	in := lines(1000)

	_, _, ev := prune.Apply(in, prune.Budget{})
	if ev == nil || ev.Pointer == "" {
		t.Fatalf("Pointer 为空：模型只知道少了东西、不知道去哪取（ev=%+v）", ev)
	}
	if ev.Pointer != prune.NoPointer {
		t.Fatalf("没给 artifact 时 Pointer 应明说没有外置，实际 %q", ev.Pointer)
	}
	if !strings.Contains(ev.Pointer, "未外置") {
		t.Fatalf("Pointer 文案没说清「没有外置」：%q", ev.Pointer)
	}

	// 调用方给了 artifact 路径就原样填进去。
	const artifact = "/tmp/dshd-spill/tool-42.txt"
	_, _, ev = prune.ApplyWithPointer(in, prune.Budget{}, artifact)
	if ev == nil || ev.Pointer != artifact {
		t.Fatalf("给了 artifact 路径却没填进 Pointer：%+v", ev)
	}

	// 只给空白也算没给——不许落一个空指针出去。
	_, _, ev = prune.ApplyWithPointer(in, prune.Budget{}, "   ")
	if ev == nil || ev.Pointer != prune.NoPointer {
		t.Fatalf("空白 pointer 应退回默认文案，实际 %+v", ev)
	}
}

// 5. 幂等：对已裁剪的正文再 Apply 一次，不该再缩水。
func TestApplyIsIdempotent(t *testing.T) {
	cases := map[string]string{
		"超字符(多行)": lines(1000),
		"超字符(中文)": strings.Repeat("中文内容测试", 2000),
	}
	for name, in := range cases {
		once, truncated, _ := prune.Apply(in, prune.Budget{})
		if !truncated {
			t.Fatalf("%s：第一次就没裁", name)
		}
		twice, truncatedAgain, ev := prune.Apply(once, prune.Budget{})
		if truncatedAgain || ev != nil {
			t.Fatalf("%s：第二次又裁了一遍（说明第一次没把结果压进预算）", name)
		}
		if twice != once {
			t.Fatalf("%s：第二次改动了正文（%d → %d 字符）", name, runeLen(once), runeLen(twice))
		}
	}
}

// 6. 接在投影函数上：只有超预算的正文被裁，正常大小原样透传，Data/View 不动。
func TestProjectResultPrunesOnlyOverBudget(t *testing.T) {
	// 6.1 正常大小：逐字节透传，不多出一个块。
	view := &spi.ViewSpec{Kind: spi.ViewPlain, Title: "t"}
	small := "文件读完了\n一共 3 行"
	r := spi.Result{
		Summary: "读到了 3 行",
		Text:    small,
		Data:    map[string]any{"total": 3},
		View:    view,
	}
	blocks := session.ProjectResult(r)
	if len(blocks) != 1 || blocks[0].Text != small {
		t.Fatalf("正常大小的正文必须原样透传，得到 %+v", blocks)
	}
	// 不许动 Data / View：裁剪只作用于"送进模型的那份正文"。
	if r.View != view || r.Data.(map[string]any)["total"] != 3 {
		t.Fatal("投影改动了 Result.Data / Result.View")
	}

	// 6.2 超预算：正文被裁，且补一条"保留多少、去哪取"的说明。
	big := lines(1000)
	bigChars := runeLen(big)
	r = spi.Result{Text: big, Data: map[string]any{"total": 1000}, View: view}
	blocks = session.ProjectResult(r)
	if len(blocks) != 2 {
		t.Fatalf("裁剪后应有「正文 + 裁剪说明」两块，得到 %d 块", len(blocks))
	}
	if !strings.Contains(blocks[0].Text, prune.Marker) {
		t.Fatal("块 0 应该是裁剪后的正文（含官方 marker）")
	}
	if n := runeLen(blocks[0].Text); n > prune.DefaultThresholdChars {
		t.Fatalf("投影出去的正文 %d 字符，超预算", n)
	}
	if !strings.Contains(blocks[1].Text, "已按预算裁剪") || !strings.Contains(blocks[1].Text, "未外置") {
		t.Fatalf("缺少裁剪说明块：%q", blocks[1].Text)
	}
	// 裁剪说明要报**原始字符数**，且单位说清是字符（按字节报会夸大中文的损失）。
	if !strings.Contains(blocks[1].Text, strconv.Itoa(bigChars)) {
		t.Fatalf("裁剪说明要报原始字符数 %d：%q", bigChars, blocks[1].Text)
	}
	if !strings.Contains(blocks[1].Text, "字符") {
		t.Fatalf("裁剪说明要带单位「字符」：%q", blocks[1].Text)
	}
	// 投影不改 Result 本体。
	if r.View != view || r.Text != big {
		t.Fatal("投影改动了 Result 本体")
	}

	// 6.3 已经有截断证据（且带指针）时，止损于两块：不再重复第三条"去哪取"。
	r = spi.Result{
		Text:      big,
		Artifacts: []spi.Artifact{{Name: "big", Path: "/tmp/spill/big.txt", Bytes: 9000}},
		Truncated: &spi.Truncation{Kept: 3000, Total: 9000, Pointer: "/tmp/spill/big.txt"},
	}
	blocks = session.ProjectResult(r)
	if len(blocks) != 2 {
		t.Fatalf("已有截断说明时不该再多一块，得到 %d 块：%+v", len(blocks), blocks)
	}
	if !strings.Contains(blocks[1].Text, "/tmp/spill/big.txt") {
		t.Fatalf("指针丢了：%q", blocks[1].Text)
	}
	// 指针只出现一次（spill 的路径被 sourcePointer 带进了正文的省略说明里）。
	if n := strings.Count(strings.Join([]string{blocks[0].Text, blocks[1].Text}, "\n"), "/tmp/spill/big.txt"); n > 2 {
		t.Fatalf("同一个指针在一屏里出现了 %d 次，冗余", n)
	}

	// 6.4 截断证据的指针是空的（残缺证据）：prune 必须补上"没有外置"，
	// 绝不能让模型看到一句「全文在 」然后就没了。
	r = spi.Result{Text: big, Truncated: &spi.Truncation{Kept: 1, Total: 2}}
	blocks = session.ProjectResult(r)
	if len(blocks) != 3 {
		t.Fatalf("残缺证据时要补一块裁剪说明，得到 %d 块", len(blocks))
	}
	if !strings.Contains(blocks[1].Text, "未外置") {
		t.Fatalf("prune 的说明必须明说没有外置：%q", blocks[1].Text)
	}
}

// 7. 默认预算必须**逐字等于官方三件套**，不许再出现自造的数。
//
// 官方 `dsh-compaction-tool-result-pruner/lib/index.js:10-14`：
//
//	const DEFAULTS = deepFreeze({ thresholdChars: 8192, headChars: 4096, tailChars: 1024 })
//
// 同值在 `dsh-base/cordis.patch.yml:438-440` 被出厂配置显式再配一遍。
// 官方还有一条配置校验（`lib/index.js:43-44`）：头 + marker + 尾 必须 ≤ 阈值，
// 这里一并钉住，否则"裁剪完还超预算"会变成每次投影再裁一刀。
func TestDefaultBudgetIsOfficialTriple(t *testing.T) {
	if prune.DefaultThresholdChars != 8192 {
		t.Fatalf("thresholdChars = %d，官方是 8192（lib/index.js:11）", prune.DefaultThresholdChars)
	}
	if prune.DefaultHeadChars != 4096 {
		t.Fatalf("headChars = %d，官方是 4096（lib/index.js:12）", prune.DefaultHeadChars)
	}
	if prune.DefaultTailChars != 1024 {
		t.Fatalf("tailChars = %d，官方是 1024（lib/index.js:13）", prune.DefaultTailChars)
	}
	// marker 是官方常量原文（lib/index.js:8），逐字节比对（含首尾各两个换行）。
	const officialMarker = "\n\n[... tool result middle pruned ...]\n\n"
	if prune.Marker != officialMarker {
		t.Fatalf("marker = %q，官方是 %q", prune.Marker, officialMarker)
	}
	// 零值预算 == 官方默认（语义）：调用点（internal/session/project.go）传的就是
	// Budget{}，所以它与 Default() 必须给出**逐字节相同**的结果与证据。
	probe := strings.Repeat("中a", prune.DefaultThresholdChars) // 16384 字符，必超预算
	zeroOut, zeroTrunc, zeroEv := prune.Apply(probe, prune.Budget{})
	defOut, defTrunc, defEv := prune.Apply(probe, prune.Default())
	if zeroTrunc != defTrunc || zeroOut != defOut {
		t.Fatal("零值预算与官方默认的结果不一致")
	}
	if zeroEv == nil || defEv == nil || *zeroEv != *defEv {
		t.Fatalf("零值预算与官方默认的证据不一致：%+v vs %+v", zeroEv, defEv)
	}
	if !zeroTrunc {
		t.Fatal("零值预算没有生效（16384 字符应当被裁）")
	}
	// 零值预算要真的生效在裁剪点上（不是只有 Default() 那一条路对）。
	justOver := strings.Repeat("x", prune.DefaultThresholdChars+1)
	out, truncated, ev := prune.Apply(justOver, prune.Budget{})
	if !truncated || ev == nil {
		t.Fatalf("零值预算没有生效：truncated=%v ev=%+v", truncated, ev)
	}
	if n := runeLen(out); n > prune.DefaultThresholdChars {
		t.Fatalf("零值预算下裁剪后 %d 字符，超阈值", n)
	}
	if err := prune.Default().Validate(); err != nil {
		t.Fatalf("默认预算必须通过官方那条不自洽校验：%v", err)
	}
	// 官方 lib/index.js:43-44 的等价式：headChars + marker + tailChars ≤ thresholdChars。
	if emitted := prune.DefaultHeadChars + runeLen(prune.Marker) + prune.DefaultTailChars; emitted > prune.DefaultThresholdChars {
		t.Fatalf("headChars + marker + tailChars = %d 超过 thresholdChars %d（官方 lib/index.js:44 会直接 throw）",
			emitted, prune.DefaultThresholdChars)
	}
	// 不自洽的预算必须被判出来，不能静默裁出一个超预算的结果。
	bad := prune.Budget{ThresholdChars: 100, HeadChars: 80, TailChars: 80}
	if err := bad.Validate(); err == nil {
		t.Fatal("80+marker+80 > 100 的预算必须报不自洽")
	}
}

// 8. 默认阈值对中文的边界：8192 个汉字（24576 字节）**一个不裁**，多一个字符才裁。
//
// 这一条顶替掉旧的 `TestDefaultBudgetExceedsSpillReach`（它钉的是"16KB 字节预算
// 必须大于 spill 4000 字符的最坏产出 12KB"，理由是"别在 spill 已截断的内容上再截一刀"）。
// 那条前提随着 `maxChars` 的自造默认被删（#101）已经不成立，而且**方向本身是错的**：
// 官方把预算定义在字符维度上，中文结果再多字节也只按字符算，所以"中文 12KB"这个
// 担忧在官方口径下根本不存在——旧实现反而因为按字节量，在 5461 个汉字处就多压了一刀。
//
// 现在钉的是官方边界本身，两个方向都钉：
//   - 8192 字符（含 8192 个汉字 = 24576 字节）→ 原样透传；
//   - 8193 字符 → 裁。
//
// 比旧断言更严：旧断言只说"默认值 > 12000 字节"（对具体裁剪点没有约束），
// 新断言把裁剪点精确钉在官方数字上，且用的是对字节预算最不利的中文正文。
func TestDefaultThresholdIsOfficialCharBoundaryForCJK(t *testing.T) {
	// 8192 个汉字 = 24576 字节：旧 16KB 字节预算会在这里裁，官方不裁。
	atLimit := strings.Repeat("中", prune.DefaultThresholdChars)
	if len(atLimit) <= 16<<10 {
		t.Fatalf("用例没造出「字符数达标但字节数超旧预算」的形状：%d 字节", len(atLimit))
	}
	if out, truncated, ev := prune.Apply(atLimit, prune.Budget{}); truncated || ev != nil || out != atLimit {
		t.Fatalf("%d 字符 / %d 字节的中文正文不该被裁：truncated=%v ev=%v",
			runeLen(atLimit), len(atLimit), truncated, ev)
	}

	overLimit := atLimit + "中"
	out, truncated, ev := prune.Apply(overLimit, prune.Budget{})
	if !truncated || ev == nil {
		t.Fatalf("%d 字符必须被裁", runeLen(overLimit))
	}
	if ev.Total != prune.DefaultThresholdChars+1 {
		t.Fatalf("Total = %d，期望 %d", ev.Total, prune.DefaultThresholdChars+1)
	}
	if n := runeLen(out); n != prune.DefaultHeadChars+runeLen(prune.Marker)+prune.DefaultTailChars {
		t.Fatalf("裁剪后 %d 字符，期望 %d", n,
			prune.DefaultHeadChars+runeLen(prune.Marker)+prune.DefaultTailChars)
	}
}
