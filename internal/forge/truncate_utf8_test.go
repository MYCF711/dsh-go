package forge

// GitHub 响应正文会进错误消息给人看（评审面板、git 面板）：按字节切会把
// 中文 commit message 切成半个。

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateForgeKeepsUTF8Boundary(t *testing.T) {
	s := strings.Repeat("宽", 200) // 600 字节
	got := truncateForge(s, 500)  // 500 = 166 个整字 + 2 字节，落在码元中间
	if !utf8.ValidString(got) {
		t.Fatalf("截断切碎了 UTF-8 码元：%q", got)
	}
	if body := strings.TrimSuffix(got, "…"); len(body) != 498 {
		t.Fatalf("正文应回退到码元边界 498 字节，实际 %d", len(body))
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("截断标记丢了：%q", got)
	}
	if short := truncateForge("短", 500); short != "短" {
		t.Fatalf("未超预算时不该改动：%q", short)
	}
}
