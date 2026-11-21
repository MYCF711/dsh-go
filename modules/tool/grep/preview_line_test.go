package grep

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// 这一组钉「匹配行预览」的两条语义，两条都是官方 `dsh-tool-fs-search/src/search-core.ts:297-302`
// `previewLine` 写明并做到、而本仓原来是错的：
//
//  1. 裁点落在 UTF-8 字符边界上（官方 `GREP_MAX_LINE_BYTES` 注释：the cut preserves UTF-8 boundaries）
//  2. 裁过就标出来（官方 `return kept.truncated ? `${kept.text} (line truncated)` : kept.text`）
//
// 改前的实现是 `line[:400]`：直接切字节。中文/emoji 行被切成半个字符，字符串里带着
// 非法 UTF-8；grep 的结果没有 Text，投影层退回 `Summary + JSON(Data)`
//（`internal/session/project.go:31`），`encoding/json` 把非法字节换成 U+FFFD ——
// 模型看到「这一行有乱码」，而真相是「这一行被截断了、后面还有」。

// chineseLongLine 返回一条 600 字节的中文行（200 个三字节字符）。
func chineseLongLine() string { return strings.Repeat("中", 200) }

// TestPreviewLineKeepsUTF8Boundary 钉裁剪点：必须是合法 UTF-8、必须以原行起始的连续前缀为内容。
func TestPreviewLineKeepsUTF8Boundary(t *testing.T) {
	line := chineseLongLine()
	if len(line) != 600 {
		t.Fatalf("夹具前提：期望 600 字节，得到 %d", len(line))
	}
	// 夹具自证：**这个偏移必须真的切在字符中间**，否则这条用例构造不出缺陷现场
	//（改前实现就是 `line[:maxLineBytes]`）。
	if utf8.RuneStart(line[maxLineBytes]) {
		t.Fatalf("夹具前提不成立：第 %d 字节恰好是字符起点，改前的实现不会切出半个字符", maxLineBytes)
	}

	got := previewLine(line, maxLineBytes)
	if !utf8.ValidString(got) {
		t.Fatalf("预览不是合法 UTF-8：前 24 字节 % x", []byte(got)[:24])
	}
	if strings.ContainsRune(got, utf8.RuneError) {
		t.Fatalf("预览里出现替换字符 U+FFFD：%q", got)
	}
	if !strings.HasSuffix(got, lineTruncMarker) {
		t.Fatalf("裁剪后必须带截断标记 %q，得到 %q 的尾部", lineTruncMarker, tail(got, 24))
	}
	body := strings.TrimSuffix(got, lineTruncMarker)
	if !strings.HasPrefix(line, body) {
		t.Fatal("预览正文不是原行的前缀")
	}
	// 多丢的字节数只可能是一个字符的续字节（≤3），不会把整段吞掉。
	if len(body) > maxLineBytes || len(body) < maxLineBytes-3 {
		t.Fatalf("正文长度 %d 字节，期望落在 [%d,%d]", len(body), maxLineBytes-3, maxLineBytes)
	}
	if len(body)%3 != 0 {
		t.Fatalf("正文长度 %d 不是三字节字符的整数倍——边界对齐没生效", len(body))
	}
}

// TestPreviewLineMarksTruncation 钉「裁过必须标出来」：不标的话模型会把半截行当完整行引用。
func TestPreviewLineMarksTruncation(t *testing.T) {
	long := strings.Repeat("x", maxLineBytes+1)
	if got := previewLine(long, maxLineBytes); !strings.HasSuffix(got, lineTruncMarker) {
		t.Fatalf("被裁过的行必须带标记，得到 %q 的尾部", tail(got, 24))
	}
	// 没被裁的行一个字都不许加（标记只属于真的裁过）。
	short := strings.Repeat("x", maxLineBytes)
	if got := previewLine(short, maxLineBytes); got != short {
		t.Fatalf("未超预算的行被改写了：%d → %d 字节", len(short), len(got))
	}
}

// TestPreviewLineASCIIBudgetUnchanged 钉「纯 ASCII 不多丢」：改前对 ASCII 恰好留 400 字节。
func TestPreviewLineASCIIBudgetUnchanged(t *testing.T) {
	line := strings.Repeat("a", 1000)
	body := strings.TrimSuffix(previewLine(line, maxLineBytes), lineTruncMarker)
	if len(body) != maxLineBytes {
		t.Fatalf("ASCII 行的保留字节数 = %d，改前也是 %d（对齐不该动这一档）", len(body), maxLineBytes)
	}
}

// TestPreviewLineBoundaryEdges 钉边界值：0 / 负数 / 恰好等于上限。
func TestPreviewLineBoundaryEdges(t *testing.T) {
	if got := previewLine("中中中", 0); got != "中中中" {
		t.Fatalf("maxBytes<=0 应原样返回（不裁），得到 %q", got)
	}
	exact := strings.Repeat("中", 3) // 9 字节
	if got := previewLine(exact, 9); got != exact {
		t.Fatalf("恰好等于上限时不该标截断，得到 %q", got)
	}
	// 4 字节字符（emoji）：裁点同样要退到字符起点。
	emoji := strings.Repeat("🧪", 20) // 80 字节
	body := strings.TrimSuffix(previewLine(emoji, 50), lineTruncMarker)
	if !utf8.ValidString(body) || strings.ContainsRune(body, utf8.RuneError) {
		t.Fatalf("4 字节字符的裁剪点没对齐：% x", []byte(body)[len(body)-4:])
	}
	if len(body) > 50 || len(body)%4 != 0 {
		t.Fatalf("4 字节字符正文长度 %d 不合预算/对齐", len(body))
	}
}

// TestGrepLongLineIsValidUTF8AndMarked 是端到端那条：模型看到的 JSON 里不能再有 U+FFFD。
func TestGrepLongLineIsValidUTF8AndMarked(t *testing.T) {
	tool, root := newTool(t)
	// "NEEDLE" 之后接 300 个三字节字符：第 400 字节落在 (400-6)=394，394%3=1 → 续字节。
	line := "NEEDLE" + strings.Repeat("中", 300)
	touch(t, root, "long.go", line+"\n")

	res, err := run(t, tool, map[string]any{"pattern": "NEEDLE"})
	if err != nil {
		t.Fatal(err)
	}
	p := res.Data.(payload)
	if len(p.Matches) != 1 {
		t.Fatalf("期望 1 条命中，得到 %d", len(p.Matches))
	}
	text := p.Matches[0].Text
	if !strings.HasSuffix(text, lineTruncMarker) {
		t.Fatalf("截断行没标出来：%q", tail(text, 32))
	}
	if !utf8.ValidString(text) || strings.ContainsRune(text, utf8.RuneError) {
		t.Fatalf("预览正文不是合法 UTF-8：前 24 字节 % x", []byte(text)[:24])
	}

	// 真正交给模型的是 Data 的 JSON（grep 没有 Text，见 internal/session/project.go:31）。
	// 非法 UTF-8 在这里会被 encoding/json 换成 \ufffd——那正是改前模型看到的乱码。
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `\ufffd`) {
		t.Fatal("JSON 里出现替换字符：模型会把「被截断」看成「命令/文件吐了乱码」")
	}
	if !strings.Contains(string(raw), lineTruncMarker) {
		t.Fatal("JSON 里没有截断标记：模型会把半截行当完整行")
	}
}

// tail 取末端 n 字节，失败信息里够看就行。
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
