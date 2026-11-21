package pluginmgr

// registry 的错误消息会带上 HTTP 响应正文（可能含中文包描述）：按字节切会把
// 汉字切成半个，那半个码元直接进 plugin.list / install 的错误结果。

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateTextKeepsUTF8Boundary(t *testing.T) {
	s := strings.Repeat("宽", 120) // 360 字节
	got := truncateText(s, 300)   // 300 = 100 个整字，正好在边界上
	if !utf8.ValidString(got) {
		t.Fatalf("截断切碎了 UTF-8 码元：%q", got)
	}
	if body := strings.TrimSuffix(got, "…"); len(body) != 300 {
		t.Fatalf("整字边界上应保留 300 字节，实际 %d", len(body))
	}

	got = truncateText(s, 301) // 301 落在第 101 个字的码元中间
	if !utf8.ValidString(got) {
		t.Fatalf("截断切碎了 UTF-8 码元：%q", got)
	}
	if body := strings.TrimSuffix(got, "…"); len(body) != 300 {
		t.Fatalf("正文应回退到码元边界 300 字节，实际 %d", len(body))
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("截断标记丢了：%q", got)
	}
	if short := truncateText("短", 300); short != "短" {
		t.Fatalf("未超预算时不该改动：%q", short)
	}
}
