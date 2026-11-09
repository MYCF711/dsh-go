package main

// PARITY-ACCOUNT：把 internal/account 的三提供方状态机适配成 Transport 的
// 官方 account 回调。Transport 只认官方字段；来源标签（local/official/
// self-hosted）由这里透传，Local 结果永远带 source:"local"。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"dshgo/internal/account"
	"dshgo/internal/transport"
)

func accountProviderFromDeps(deps accountOfficialDeps) (account.Provider, error) {
	if deps.provider != nil {
		return deps.provider, nil
	}
	mode := account.SourceLocal
	if raw := strings.ToLower(strings.TrimSpace(os.Getenv("DSHD_ACCOUNT_PROVIDER"))); raw != "" {
		mode = account.Source(raw)
	}
	lease := 30 * 24 * time.Hour
	if raw := strings.TrimSpace(os.Getenv("DSHD_ACCOUNT_LOCAL_LEASE_SECONDS")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			lease = time.Duration(n) * time.Second
		}
	}
	oidc := account.OIDCConfig{
		Issuer:        os.Getenv("DSHD_ACCOUNT_OIDC_ISSUER"),
		ClientID:      os.Getenv("DSHD_ACCOUNT_OIDC_CLIENT_ID"),
		ClientSecret:  os.Getenv("DSHD_ACCOUNT_OIDC_CLIENT_SECRET"),
		Scopes:        os.Getenv("DSHD_ACCOUNT_OIDC_SCOPES"),
		DeviceAuthURL: os.Getenv("DSHD_ACCOUNT_OIDC_DEVICE_URL"),
		TokenURL:      os.Getenv("DSHD_ACCOUNT_OIDC_TOKEN_URL"),
		UserInfoURL:   os.Getenv("DSHD_ACCOUNT_OIDC_USERINFO_URL"),
	}
	return account.New(account.Options{
		Home:           deps.home,
		Mode:           mode,
		PlatformOrigin: os.Getenv("DSHD_ACCOUNT_PLATFORM_ORIGIN"),
		OIDC:           oidc,
		LocalLease:     lease,
	})
}

func bindAccountProvider(p account.Provider) transport.OfficialAccountAPI {
	return transport.OfficialAccountAPI{
		AccountGetProfile:           func(ctx context.Context, c transport.OfficialAccountClientMetadata) (*transport.OfficialAccountProfile, error) { return accountProfile(ctx, p, c) },
		AccountGetBalance:           func(ctx context.Context, c transport.OfficialAccountClientMetadata) (*transport.OfficialAccountBalance, error) { return accountBalance(ctx, p, c) },
		AccountGetUnnotifiedBonuses: func(ctx context.Context, c transport.OfficialAccountClientMetadata) (*transport.OfficialAccountBonusList, error) { return accountBonuses(ctx, p, c) },
		AccountAckBonusNotified: func(ctx context.Context, accountID, orderID string, c transport.OfficialAccountClientMetadata) (bool, error) {
			return p.AckBonus(ctx, accountID, orderID, accountClient(c))
		},
		AccountStartSignIn: func(ctx context.Context, c transport.OfficialAccountClientMetadata, callbackOrigin, loginSource string) (transport.OfficialAccountView, error) {
			st, err := p.StartSignIn(ctx, account.StartOptions{Client: accountClient(c), CallbackOrigin: callbackOrigin, LoginSource: loginSource})
			return accountView(st), err
		},
		AccountCancelSignIn: func(ctx context.Context, attemptID string) (transport.OfficialAccountView, error) {
			st, err := p.CancelSignIn(ctx, attemptID)
			if errors.Is(err, account.ErrNoAttempt) {
				return transport.OfficialAccountView{}, fmt.Errorf("%w: 没有这个登录尝试", transport.ErrOfficialBadInput)
			}
			return accountView(st), err
		},
		AccountSignOut: func(ctx context.Context, c transport.OfficialAccountClientMetadata) (transport.OfficialAccountView, error) {
			st, err := p.SignOut(ctx)
			return accountView(st), err
		},
		AccountGetState: func(ctx context.Context) (transport.OfficialAccountView, error) {
			st, err := p.State(ctx)
			return accountView(st), err
		},
		AccountHasRunningTasks: func(ctx context.Context) (bool, error) {
			tasks, err := p.RunningTasks(ctx)
			if err != nil {
				return false, err
			}
			return len(tasks) > 0, nil
		},
		AccountWatch: func(ctx context.Context) (<-chan transport.OfficialAccountView, error) {
			in, err := p.Watch(ctx)
			if err != nil {
				return nil, err
			}
			out := make(chan transport.OfficialAccountView, 8)
			go func() {
				defer close(out)
				for {
					select {
					case <-ctx.Done():
						return
					case st, ok := <-in:
						if !ok {
							return
						}
						select {
						case out <- accountView(st):
						case <-ctx.Done():
							return
						}
					}
				}
			}()
			return out, nil
		},
		AccountWatchExpiry: func(ctx context.Context) (<-chan string, error) {
			return p.WatchExpiry(ctx)
		},
	}
}

func accountClient(c transport.OfficialAccountClientMetadata) account.Client {
	return account.Client{Version: c.Version, Locale: c.Locale, TimezoneOffsetSeconds: c.TimezoneOffsetSeconds}
}

func accountView(st account.State) transport.OfficialAccountView {
	out := transport.OfficialAccountView{Status: st.Status, Source: string(st.Source),
		Links: transport.OfficialAccountLinks{UsageURL: st.Links.UsageURL, TopUpURL: st.Links.TopUpURL}}
	if st.Attempt != nil {
		out.Attempt = &transport.OfficialAccountAttempt{
			ID: st.Attempt.ID, Phase: st.Attempt.Phase, AuthorizeURL: st.Attempt.AuthorizeURL,
			ExpiresAt: st.Attempt.ExpiresAt, ErrorCode: st.Attempt.ErrorCode,
		}
	}
	return out
}

func accountProfile(ctx context.Context, p account.Provider, c transport.OfficialAccountClientMetadata) (*transport.OfficialAccountProfile, error) {
	profile, err := p.Profile(ctx, accountClient(c))
	if errors.Is(err, account.ErrSignedOut) {
		return &transport.OfficialAccountProfile{Status: "failed", Source: string(p.Source())}, nil
	}
	if err != nil {
		return nil, err
	}
	if profile == nil {
		return nil, nil
	}
	return &transport.OfficialAccountProfile{
		Status: "ready", Source: string(p.Source()),
		Value: &transport.OfficialAccountProfileValue{
			ID: profile.ID, Name: profile.Name, Contact: profile.Contact, AvatarURL: profile.AvatarURL,
		},
	}, nil
}

func accountBalance(ctx context.Context, p account.Provider, c transport.OfficialAccountClientMetadata) (*transport.OfficialAccountBalance, error) {
	bal, err := p.Balance(ctx, accountClient(c))
	if err != nil {
		return nil, err
	}
	if bal == nil {
		return nil, nil
	}
	return &transport.OfficialAccountBalance{
		Status: bal.Status, Source: string(p.Source()),
		Value:        accountWallets(bal.Value),
		BonusWallets: accountWallets(bal.BonusWallets),
	}, nil
}

func accountWallets(in []account.Wallet) []transport.OfficialAccountWallet {
	out := make([]transport.OfficialAccountWallet, 0, len(in))
	for _, w := range in {
		out = append(out, transport.OfficialAccountWallet{Currency: w.Currency, Balance: w.Balance})
	}
	return out
}

func accountBonuses(ctx context.Context, p account.Provider, c transport.OfficialAccountClientMetadata) (*transport.OfficialAccountBonusList, error) {
	list, err := p.Bonuses(ctx, accountClient(c))
	if err != nil {
		return nil, err
	}
	if list == nil {
		return nil, nil
	}
	out := &transport.OfficialAccountBonusList{AccountID: list.AccountID, Source: string(p.Source()),
		Bonuses: make([]transport.OfficialAccountBonus, 0, len(list.Bonuses))}
	for _, b := range list.Bonuses {
		out.Bonuses = append(out.Bonuses, transport.OfficialAccountBonus{
			Title: b.Title, OrderID: b.OrderID, Campaign: b.Campaign, Amount: b.Amount,
			Currency: b.Currency, GrantedAt: b.GrantedAt, ExpiresAt: b.ExpiresAt, Message: b.Message,
		})
	}
	return out, nil
}
