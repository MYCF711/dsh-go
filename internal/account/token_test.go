package account

// 官方 base 插件 #99 `llm-deepseek-account` 需要的两个账号侧入口：
//
//   - `resolveToken(url)`  → 本仓 `AccessToken()` / `InferenceOrigin()`
//     （`dsh-deepseek-account/src/index.ts:454-473`）；
//   - `rejectToken(token)` → 本仓 `RejectToken()`
//     （同文件 `:377-382`，判据是"传入的 token 仍等于当前存储的那一份"）。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveInferenceOriginFollowsOfficialValidation(t *testing.T) {
	// 空串 = 官方默认（`src/index.ts:78`）。
	if got, err := resolveInferenceOrigin(""); err != nil || got != DefaultInferenceOrigin {
		t.Fatalf("默认 inferenceOrigin = %q err=%v，想要 %q", got, err, DefaultInferenceOrigin)
	}
	// 尾斜杠是合法的（官方判据是 pathname === '/'，`:143`）。
	if got, err := resolveInferenceOrigin("https://api.deepseek.com/"); err != nil || got != "https://api.deepseek.com" {
		t.Fatalf("尾斜杠 = %q err=%v", got, err)
	}
	if got, err := resolveInferenceOrigin("http://127.0.0.1:8080"); err != nil || got != "http://127.0.0.1:8080" {
		t.Fatalf("loopback = %q err=%v", got, err)
	}
	for _, bad := range []string{
		"ftp://api.deepseek.com",           // 非 http(s)
		"https://api.deepseek.com/v1",      // 带路径
		"https://api.deepseek.com?x=1",     // 带查询
		"https://api.deepseek.com#f",       // 带片段
		"https://user:pw@api.deepseek.com", // 带凭据
		"api.deepseek.com",                 // 没有 scheme
	} {
		if got, err := resolveInferenceOrigin(bad); err == nil {
			t.Fatalf("%q 应该被拒，实际 = %q", bad, got)
		}
	}
}

func TestNewRejectsBadInferenceOrigin(t *testing.T) {
	if _, err := New(Options{Home: t.TempDir(), Mode: SourceOfficial, InferenceOrigin: "https://api.deepseek.com/v1"}); err == nil {
		t.Fatal("非法 inferenceOrigin 必须在构造期 fail-loud，不能静默退回默认值")
	}
}

func TestAccessTokenFollowsStoredGrant(t *testing.T) {
	home := t.TempDir()
	s, err := New(Options{Home: home, Mode: SourceOfficial, PlatformOrigin: "https://platform.deepseek.com"})
	if err != nil {
		t.Fatal(err)
	}
	if token, ok := s.AccessToken(); ok || token != "" {
		t.Fatalf("未登录时 = %q/%v，想要空/false", token, ok)
	}
	if got := s.InferenceOrigin(); got != DefaultInferenceOrigin {
		t.Fatalf("InferenceOrigin = %q", got)
	}
}

func TestAccessTokenReturnsStoredGrantAndRejectTokenIsExactMatch(t *testing.T) {
	home := t.TempDir()
	writeOfficialGrant(t, home, "grant-token-1")
	s, err := New(Options{Home: home, Mode: SourceOfficial, PlatformOrigin: "https://platform.deepseek.com"})
	if err != nil {
		t.Fatal(err)
	}
	token, ok := s.AccessToken()
	if !ok || token != "grant-token-1" {
		t.Fatalf("已登录时 = %q/%v，想要 grant-token-1/true", token, ok)
	}

	// 不匹配的令牌：**纯 no-op**（官方 `:379`：stored?.token !== token 就返回）。
	if err := s.RejectToken("someone-elses-token"); err != nil {
		t.Fatalf("不匹配的 RejectToken 不该报错: %v", err)
	}
	if token, ok := s.AccessToken(); !ok || token != "grant-token-1" {
		t.Fatalf("不匹配的 RejectToken 不该清掉当前令牌，实际 = %q/%v", token, ok)
	}

	// 匹配的令牌：清掉，并且状态回 signed-out。
	if err := s.RejectToken("grant-token-1"); err != nil {
		t.Fatalf("RejectToken: %v", err)
	}
	if token, ok := s.AccessToken(); ok || token != "" {
		t.Fatalf("作废后仍能取到令牌 = %q/%v", token, ok)
	}
	st, err := s.State(context.Background())
	if err != nil || st.Status != StatusSignedOut {
		t.Fatalf("作废后状态 = %+v err=%v", st, err)
	}
	// 落盘也要生效：重启后仍然是未登录（不是只改了内存）。
	again, err := New(Options{Home: home, Mode: SourceOfficial, PlatformOrigin: "https://platform.deepseek.com"})
	if err != nil {
		t.Fatal(err)
	}
	if token, ok := again.AccessToken(); ok || token != "" {
		t.Fatalf("重启后仍能取到令牌 = %q/%v", token, ok)
	}
}

func TestAccessTokenDropsExpiredLease(t *testing.T) {
	home := t.TempDir()
	// 直接写一份**已过期**的本地凭据：AccessToken 必须走与 getState 相同的过期路径。
	raw := fmt.Sprintf(`{"version":2,"source":"local","status":%q,"token":"expired-token","expiresAt":1}`,
		StatusCredentialStored)
	dir := filepath.Join(home, "state")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "account-local.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{Home: home, Mode: SourceLocal, LocalLease: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if token, ok := s.AccessToken(); ok || strings.TrimSpace(token) != "" {
		t.Fatalf("过期凭据不该给出令牌 = %q/%v", token, ok)
	}
}
