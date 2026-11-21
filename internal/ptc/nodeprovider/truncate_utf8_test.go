package nodeprovider

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTruncateNeverSplitsRunes 钉「截断不许切在多字节字符中间」。
//
// 这条 message 会走 workerExit → ptc.Fail 变成工具结果，json.Marshal 把半个
// 字符编码成 U+FFFD——用户/模型看到的是末尾一团乱码，而且完全没法归因。
func TestTruncateNeverSplitsRunes(t *testing.T) {
	// 每个汉字 3 字节：limit=4 时旧实现取 "中" + "文" 的第一个字节。
	got := truncate("中文输出", 4)
	if !utf8.ValidString(got) {
		t.Fatalf("截断结果不是合法 UTF-8：%q（截点落在多字节字符中间）", got)
	}
	if !strings.HasPrefix("中文输出", strings.TrimSuffix(got, "…（截断）")) {
		t.Fatalf("截断之后的内容必须是原文前缀：%q", got)
	}

	// 逐字节预算扫一遍：任何 limit 下都不许切出非法 UTF-8。
	const text = "abcdef中文输出混合 ASCII 与汉字"
	for limit := 0; limit <= len(text); limit++ {
		out := truncate(text, limit)
		if !utf8.ValidString(out) {
			t.Fatalf("limit=%d 切出非法 UTF-8：%q", limit, out)
		}
	}
}

// TestTruncateKeepsASCIIContract 纯 ASCII 时口径不变：截到 limit 字节 + 提示。
func TestTruncateKeepsASCIIContract(t *testing.T) {
	if got := truncate("abcdef", 3); got != "abc…（截断）" {
		t.Fatalf("ASCII 截断口径变了：%q", got)
	}
	if got := truncate("short", 100); got != "short" {
		t.Fatalf("没超限时不许改动原文：%q", got)
	}
}
