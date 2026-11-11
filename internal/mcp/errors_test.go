package mcp

import (
	"errors"
	"strings"
	"testing"
)

// TestErrorMessagesFollowOfficialWording 钉住错误原文：管理面要按官方文案
// 显示"这个 server 到底缺什么"，我们的四字段信封里的 message 就是这句。
// 官方出处：dsh-mcp-manager/src/index.ts:351/382/388/389/484/292。
func TestErrorMessagesFollowOfficialWording(t *testing.T) {
	cases := []struct {
		err    error
		needle string
	}{
		{UnknownServer("files"), `mcp-manager: unknown server "files"`},
		{CleanupIncomplete("files"), `mcp-manager: cleanup for "files" is incomplete; restart the Host`},
		{ConfigChanging("files"), `mcp-manager: configuration for "files" is changing`},
		{ErrDisposed, "mcp-manager: the manager is disposed"},
		{ErrUnsupportedTransport, "unsupported transport"},
	}
	for _, tc := range cases {
		if !strings.Contains(tc.err.Error(), tc.needle) {
			t.Fatalf("错误原文不对\nwant 含: %s\n  got: %s", tc.needle, tc.err.Error())
		}
	}
}

// TestKindOf 钉住错误分类：transport 层要按这个决定 HTTP 码与 code，
// 不许让 handler 靠字符串匹配猜。
func TestKindOf(t *testing.T) {
	cases := map[Kind]error{
		KindDisposed:             ErrDisposed,
		KindUnknownServer:        UnknownServer("x"),
		KindCleanupIncomplete:    CleanupIncomplete("x"),
		KindConfigChanging:       ConfigChanging("x"),
		KindUnsupportedTransport: ErrUnsupportedTransport,
	}
	for want, err := range cases {
		kind, ok := KindOf(err)
		if !ok || kind != want {
			t.Fatalf("KindOf(%v) = %q,%v，想要 %q", err, kind, ok, want)
		}
		// 包装之后仍然认得出来（handler 会再包一层上下文）。
		wrapped := errors.Join(errors.New("上下文"), err)
		if kind, ok := KindOf(wrapped); !ok || kind != want {
			t.Fatalf("包装后 KindOf 失败: %v", wrapped)
		}
	}
	if _, ok := KindOf(errors.New("无关错误")); ok {
		t.Fatal("无关错误不该被分类")
	}
}
