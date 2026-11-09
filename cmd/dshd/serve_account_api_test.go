package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"dshgo/internal/account"
	"dshgo/internal/transport"
)

func accountHome(t *testing.T) string { return t.TempDir() }

func testClient() transport.OfficialAccountClientMetadata {
	return transport.OfficialAccountClientMetadata{Version: "0.2.2-rc.1", Locale: "zh-CN", TimezoneOffsetSeconds: 28800}
}

// TestAccountAPIWiresAllEightRealCapabilities：8 个此前 501 的方法现在都必须
// 是真实回调；Local 默认结果的 source 必须是 "local"。
func TestAccountAPIWiresAllEightRealCapabilities(t *testing.T) {
	home := accountHome(t)
	api := newAccountOfficialAPI(accountOfficialDeps{home: home})
	for name, bound := range map[string]bool{
		"getProfile":      api.AccountGetProfile != nil,
		"getBalance":      api.AccountGetBalance != nil,
		"startSignIn":     api.AccountStartSignIn != nil,
		"cancelSignIn":    api.AccountCancelSignIn != nil,
		"signOut":         api.AccountSignOut != nil,
		"getState":        api.AccountGetState != nil,
		"hasRunningTasks": api.AccountHasRunningTasks != nil,
		"watch":           api.AccountWatch != nil,
		"watchExpiry":     api.AccountWatchExpiry != nil,
	} {
		if !bound {
			t.Fatalf("account.%s 没有绑定真实 provider 回调", name)
		}
	}

	ctx := context.Background()
	view, err := api.AccountGetState(ctx)
	if err != nil || view.Status != account.StatusSignedOut || view.Source != string(account.SourceLocal) {
		t.Fatalf("初始 getState：%+v err=%v", view, err)
	}
	view, err = api.AccountStartSignIn(ctx, testClient(), "http://127.0.0.1:43210", "desktop")
	if err != nil || view.Status != account.StatusCredentialStored || view.Source != string(account.SourceLocal) {
		t.Fatalf("local startSignIn：%+v err=%v", view, err)
	}
	profile, err := api.AccountGetProfile(ctx, testClient())
	if err != nil || profile == nil || profile.Status != "ready" || profile.Source != "local" {
		t.Fatalf("local profile：%+v err=%v", profile, err)
	}
	balance, err := api.AccountGetBalance(ctx, testClient())
	if err != nil || balance == nil || balance.Status != "ready" || balance.Source != "local" || len(balance.Value) != 1 {
		t.Fatalf("local balance：%+v err=%v", balance, err)
	}
	running, err := api.AccountHasRunningTasks(ctx)
	if err != nil || running {
		t.Fatalf("local hasRunningAccountTasks：%v %v", running, err)
	}
	watch, err := api.AccountWatch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first := <-watch; first.Source != "local" || first.Status != account.StatusCredentialStored {
		t.Fatalf("watch 首帧：%+v", first)
	}
	if _, err := api.AccountSignOut(ctx, testClient()); err != nil {
		t.Fatal(err)
	}
	state, err := api.AccountGetState(ctx)
	if err != nil || state.Status != account.StatusSignedOut {
		t.Fatalf("signOut 后：%+v err=%v", state, err)
	}
}

func TestAccountAPIInjectedProviderHonorsSource(t *testing.T) {
	provider, err := account.New(account.Options{Home: accountHome(t), Mode: account.SourceLocal})
	if err != nil {
		t.Fatal(err)
	}
	api := newAccountOfficialAPI(accountOfficialDeps{home: t.TempDir(), provider: provider})
	view, err := api.AccountStartSignIn(context.Background(), testClient(), "http://127.0.0.1:43210", "desktop")
	if err != nil || view.Source != "local" {
		t.Fatalf("注入 provider 后 source：%+v err=%v", view, err)
	}
}

func TestAccountAPIUnknownCancelIsStructuredError(t *testing.T) {
	api := newAccountOfficialAPI(accountOfficialDeps{home: accountHome(t)})
	if _, err := api.AccountCancelSignIn(context.Background(), "missing"); err == nil {
		t.Fatal("未知 attempt 必须返回错误，不能假装取消成功")
	}
}

func TestAccountSignOutNeedsHome(t *testing.T) {
	api := newAccountOfficialAPI(accountOfficialDeps{home: ""})
	if _, err := api.AccountGetState(context.Background()); err == nil {
		t.Fatal("没有 home 时 provider 构造错误必须抛出，不能永久 501/假成功")
	}
}

func TestAccountExpiryStreamIsReal(t *testing.T) {
	provider, err := account.New(account.Options{Home: accountHome(t), Mode: account.SourceLocal, LocalLease: 60 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.StartSignIn(context.Background(), account.StartOptions{}); err != nil {
		t.Fatal(err)
	}
	api := newAccountOfficialAPI(accountOfficialDeps{provider: provider})
	stream, err := api.AccountWatchExpiry(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// 首帧是基线（当前到期时刻/空串），第二帧才是真实到期事件。
	if baseline := <-stream; baseline == "session-expired" {
		t.Fatalf("首帧必须是基线，实际=%q", baseline)
	}
	select {
	case event := <-stream:
		if event != "session-expired" {
			t.Fatalf("到期帧=%q", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("没有真实到期帧")
	}
}

func TestAccountUnknownProviderIsErrorNot501(t *testing.T) {
	t.Setenv("DSHD_ACCOUNT_PROVIDER", "bogus")
	api := newAccountOfficialAPI(accountOfficialDeps{home: accountHome(t)})
	_, err := api.AccountGetState(context.Background())
	if err == nil {
		t.Fatal("未知 provider 必须有结构化错误")
	}
	if errors.Is(err, transport.ErrOfficialNotImplemented) {
		t.Fatal("未知 provider 不能塌成永久 501")
	}
}
