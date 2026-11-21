package edit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readBack 读回工具写完之后磁盘上的**原始字节**（不做任何行尾规范化）。
func readBack(t *testing.T, root string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// edit 只许改动被替换的那几个字节；文件其余部分必须**逐字节原样**。
//
// 缺陷来源：把规范化（`\r\n`→`\n`）后的整串写回，等于把整份文件的行尾改掉。
// 官方也没做对，所以不照抄它：`dsh-fs-local/src/fsio.ts:721-726` 只采样前 4096
// 字符数多数票，再按票数把**全文**重新编码（`src/fsio.ts:736-738`
// `restoreLineEndings`），混合行尾的文件照样被整体改写。
//
// 这条用例对"写回规范化整串"必然红：CRLF 文件的期望字节里带 `\r`。
func TestEditKeepsCRLFFileByteExact(t *testing.T) {
	tool, fs, root := newTool(t)
	const before = "alpha\r\nbeta\r\ngamma\r\n"
	fixture(t, root, before)
	if _, err := fs.Read("f.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, tool, "f.txt", "beta", "BETA", false); err != nil {
		t.Fatal(err)
	}
	const want = "alpha\r\nBETA\r\ngamma\r\n"
	if got := readBack(t, root); got != want {
		t.Fatalf("CRLF 文件被整体改写了:\n got=%q\nwant=%q", got, want)
	}
}

// 混合行尾的文件：只改命中处，后面那些 lone-LF 行一个字节都不许动。
// 这正是官方 `detectLineEndings` 会踩的坑（多数票把少数派整体转走）。
func TestEditKeepsMixedLineEndings(t *testing.T) {
	tool, fs, root := newTool(t)
	const before = "one\r\ntwo\r\nthree\nfour\n"
	fixture(t, root, before)
	if _, err := fs.Read("f.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, tool, "f.txt", "one", "ONE", false); err != nil {
		t.Fatal(err)
	}
	const want = "ONE\r\ntwo\r\nthree\nfour\n"
	if got := readBack(t, root); got != want {
		t.Fatalf("混合行尾被改写:\n got=%q\nwant=%q", got, want)
	}
}

// 多行 new_string 插进 CRLF 文件时，新行的换行必须跟随文件风格，
// 否则又会自己造出一个混合行尾文件。
func TestEditNewLinesFollowFileStyle(t *testing.T) {
	tool, fs, root := newTool(t)
	fixture(t, root, "keep\r\nreplace-me\r\ntail\r\n")
	if _, err := fs.Read("f.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, tool, "f.txt", "replace-me", "a\nb", false); err != nil {
		t.Fatal(err)
	}
	const want = "keep\r\na\r\nb\r\ntail\r\n"
	if got := readBack(t, root); got != want {
		t.Fatalf("新行没跟随 CRLF:\n got=%q\nwant=%q", got, want)
	}
	// 反过来：LF 文件里插入多行，不许凭空出现 `\r`。
	tool2, fs2, root2 := newTool(t)
	fixture(t, root2, "keep\nreplace-me\ntail\n")
	if _, err := fs2.Read("f.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, tool2, "f.txt", "replace-me", "a\nb", false); err != nil {
		t.Fatal(err)
	}
	if got := readBack(t, root2); got != "keep\na\nb\ntail\n" {
		t.Fatalf("LF 文件被插入 CR:\n got=%q", got)
	}
}

// 纯 LF 文件的行为必须与改前**完全一致**（坐标映射在无 `\r\n` 时是恒等）。
func TestEditLFOnlyIsUnchanged(t *testing.T) {
	tool, fs, root := newTool(t)
	fixture(t, root, "a\nb\nc\n")
	if _, err := fs.Read("f.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, tool, "f.txt", "b", "B", false); err != nil {
		t.Fatal(err)
	}
	if got := readBack(t, root); got != "a\nB\nc\n" {
		t.Fatalf("LF 文件被改动: %q", got)
	}
}

// replace_all 在 CRLF 文件上逐个命中，行尾同样逐个保留。
func TestEditReplaceAllKeepsCRLF(t *testing.T) {
	tool, fs, root := newTool(t)
	fixture(t, root, "x\r\nmid\r\nx\r\ntail\r\n")
	if _, err := fs.Read("f.txt"); err != nil {
		t.Fatal(err)
	}
	res, err := call(t, tool, "f.txt", "x", "Y", true)
	if err != nil {
		t.Fatal(err)
	}
	if got := readBack(t, root); got != "Y\r\nmid\r\nY\r\ntail\r\n" {
		t.Fatalf("replace_all 破坏了 CRLF: %q", got)
	}
	// bytes_after 报的必须是**真实落盘字节数**（CRLF 文件比 LF 长），
	// 否则 payload 里的数字与磁盘对不上。
	if after := res.Data.(payload).BytesAfter; after != len("Y\r\nmid\r\nY\r\ntail\r\n") {
		t.Fatalf("bytes_after=%d，与落盘字节数不一致", after)
	}
}

// 模型看到的 old_string 是**规范化后**的（read 工具按 \n 呈现），
// 所以带 \n 的 old_string 必须能在 CRLF 文件里命中——这条保证上面的修复
// 不会把"匹配"这一层弄坏。
func TestEditMatchesNormalizedOldStringInCRLFFile(t *testing.T) {
	tool, fs, root := newTool(t)
	fixture(t, root, "line1\r\nline2\r\nline3\r\n")
	if _, err := fs.Read("f.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, tool, "f.txt", "line1\nline2", "X", false); err != nil {
		t.Fatalf("规范化 old_string 应当命中 CRLF 文件: %v", err)
	}
	if got := readBack(t, root); got != "X\r\nline3\r\n" {
		t.Fatalf("跨行替换结果错: %q", got)
	}
}

// 坐标映射的单元级守卫：不重叠命中、无 \r\n 时恒等、尾部命中不越界。
func TestMatchStartsAndSpanMapping(t *testing.T) {
	if got := matchStarts("aaaa", "aa"); len(got) != 2 || got[0] != 0 || got[1] != 2 {
		t.Fatalf("非重叠口径错: %v", got)
	}
	if got := matchStarts("abc", ""); got != nil {
		t.Fatalf("空 needle 不该有命中: %v", got)
	}

	// 无 CRLF：规范化坐标 == 原文坐标。
	raw := "abcdef"
	norm := raw
	spans := normSpansToRaw(raw, matchStarts(norm, "cd"), 2)
	if len(spans) != 1 || spans[0] != [2]int{2, 4} {
		t.Fatalf("恒等映射错: %v", spans)
	}

	// 有 CRLF：命中落在第二个 `\r\n` 之后，起点要跳过两个被删的 `\r`。
	raw = "a\r\nb\r\nc"
	norm = strings.ReplaceAll(raw, "\r\n", "\n")
	spans = normSpansToRaw(raw, matchStarts(norm, "c"), 1)
	if len(spans) != 1 || spans[0] != [2]int{6, 7} {
		t.Fatalf("CRLF 偏移映射错: %v（want [6 7]）", spans)
	}
}
