package main

// `dshd run` 的调试输出把工具参数/结果摘要打到 stderr：按字节切会把汉字切成
// 半个，终端上就是乱码（与 e9b0c14 修的工具输出同一类缺陷）。

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateKeepsUTF8Boundary(t *testing.T) {
	s := strings.Repeat("宽", 60) // 180 字节
	got := truncate(s, 120)      // 120 = 40 个整字，正好在边界上
	if !utf8.ValidString(got) {
		t.Fatalf("截断切碎了 UTF-8 码元：%q", got)
	}
	if body := strings.TrimSuffix(got, "…"); len(body) != 120 {
		t.Fatalf("整字边界上应保留 120 字节，实际 %d", len(body))
	}

	got = truncate(s, 121) // 121 落在第 41 个字的码元中间
	if !utf8.ValidString(got) {
		t.Fatalf("截断切碎了 UTF-8 码元：%q", got)
	}
	if body := strings.TrimSuffix(got, "…"); len(body) != 120 {
		t.Fatalf("正文应回退到码元边界 120 字节，实际 %d", len(body))
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("截断标记丢了：%q", got)
	}
	if short := truncate("短", 120); short != "短" {
		t.Fatalf("未超预算时不该改动：%q", short)
	}
}
