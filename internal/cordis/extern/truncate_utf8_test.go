package extern

// node 探测失败的原因串里带着 node 的原始输出：那段要进诊断结果给人看，
// 按字节切会把汉字切成半个。

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateKeepsUTF8Boundary(t *testing.T) {
	s := strings.Repeat("宽", 40) // 120 字节
	got := truncate(s, 100)      // 100 = 33 个整字 + 1 字节，落在第 34 个字的码元中间
	if !utf8.ValidString(got) {
		t.Fatalf("截断切碎了 UTF-8 码元：%q", got)
	}
	if body := strings.TrimSuffix(got, "…"); len(body) != 99 {
		t.Fatalf("正文应回退到码元边界 99 字节，实际 %d：%q", len(body), got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("截断标记丢了：%q", got)
	}
	if short := truncate("短", 100); short != "短" {
		t.Fatalf("未超预算时不该改动：%q", short)
	}
}
