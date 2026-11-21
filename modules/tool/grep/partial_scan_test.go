package grep

import (
	"strings"
	"testing"
)

// 这一组钉「扫到一半就停下」不许静默。
//
// 现场：`bufio.Scanner` 的缓冲是 `scannerBufMax`（1 MiB），遇到更长的行会直接
// `Scan()==false`——该文件**后面的行一条都没扫**，而循环体一次都没进到那一行。
// 改前没有 `Err()` 检查，于是 1.2 MiB 单行 + 下一行 NEEDLE 的结果是
// count=0 / skipped=0 / 提示「没有命中：确认 base 路径对不对」：
// 把「没扫到」报成了「不存在」，模型据此断定代码里没有这个符号。
//
// 判据只有一条：**结果可能不全时，必须说出来**。加的是信息，不是上限，
// 跳过/命中/上限语义一个字都没改。

// longLineFile 造一个「第一行 1.2 MiB、第二行才有 NEEDLE」的文件内容。
func longLineFile() string {
	return strings.Repeat("a", 1200*1024) + "\nNEEDLE here\n"
}

// TestGrepReportsPartialScan 钉：只扫了一半的文件要进 PartialFiles，并且 Hint 必须顶在前面说。
func TestGrepReportsPartialScan(t *testing.T) {
	tool, root := newTool(t)
	touch(t, root, "bundle.js", longLineFile())

	res, err := run(t, tool, map[string]any{"pattern": "NEEDLE"})
	if err != nil {
		t.Fatal(err)
	}
	p := res.Data.(payload)
	if p.Count != 0 {
		t.Fatalf("夹具前提：长行之后的 NEEDLE 本来就扫不到，期望 count=0，得到 %d", p.Count)
	}
	if p.PartialFiles != 1 {
		t.Fatalf("只扫了一半的文件数 = %d，期望 1（改前这里是 0，等于没人知道文件没扫完）", p.PartialFiles)
	}
	// 关键：不能只说"没有命中"。路径是对的，是文件没扫完。
	if !strings.Contains(res.Hint, "没扫到") {
		t.Fatalf("Hint 必须说清「后面的行没扫到」，得到 %q", res.Hint)
	}
	if !strings.HasPrefix(res.Hint, "1 个文件") {
		t.Fatalf("这句要顶在 Hint 最前面（它推翻了后面那句「确认 base 路径」），得到 %q", res.Hint)
	}
	if !strings.Contains(res.Hint, "确认 base 路径") {
		t.Fatalf("原有的下一步建议不该被吃掉，得到 %q", res.Hint)
	}
}

// TestGrepPartialScanLeavesNormalScanAlone 钉：没有长行时这个数必须是 0，Hint 一个字不加。
func TestGrepPartialScanLeavesNormalScanAlone(t *testing.T) {
	tool, root := newTool(t)
	touch(t, root, "ok.go", "NEEDLE here\n")
	res, err := run(t, tool, map[string]any{"pattern": "NEEDLE"})
	if err != nil {
		t.Fatal(err)
	}
	p := res.Data.(payload)
	if p.Count != 1 || p.PartialFiles != 0 {
		t.Fatalf("正常文件不该被记成只扫了一半：count=%d partial=%d", p.Count, p.PartialFiles)
	}
	if res.Hint != "" {
		t.Fatalf("命中且没截断时不该有 Hint，得到 %q", res.Hint)
	}
}

// TestGrepPartialScanCoexistsWithHits 钉：有命中也不许吞掉这句（结果不全和有没有命中无关）。
func TestGrepPartialScanCoexistsWithHits(t *testing.T) {
	tool, root := newTool(t)
	touch(t, root, "bundle.js", longLineFile())
	touch(t, root, "ok.go", "NEEDLE here\n")
	res, err := run(t, tool, map[string]any{"pattern": "NEEDLE"})
	if err != nil {
		t.Fatal(err)
	}
	p := res.Data.(payload)
	if p.Count != 1 || p.PartialFiles != 1 {
		t.Fatalf("count=%d partial=%d，期望 1/1", p.Count, p.PartialFiles)
	}
	if !strings.Contains(res.Hint, "没扫到") {
		t.Fatalf("有命中时同样要说清结果可能不全，得到 %q", res.Hint)
	}
}
