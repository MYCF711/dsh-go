package main

// 官方 base 插件 #23 `authorization` 的**生产接线**：把宿主已经有的真实登录流
// 注册进 `internal/authorization` 的通用 flow 注册表。
//
// 为什么需要这一段（而不是只有注册表本身）：
// 补这一段之前，本仓只有 `internal/account` 的**具体**登录流（StartSignIn /
// CompleteSignIn / OIDC device code），第三方插件想贡献自己的 OAuth / 一次性
// code / 账号选择**无处注册**，UI 也没法用一套接口列出"现在能授权什么"。
// 官方 `@deepseek-ai/dsh-authorization` 的价值就在这个通用面：
//
//	ctx.authorization.registerFlow({ key, label, methods, run })
//	ctx.authorization.list() / describe(key) / begin(...) / cancel(key)
//
// 这里把本仓的自托管 OIDC 登录**真的**包成一条 flow，键是官方约定形状的
// `<scope>/<id>`（`account/oidc`），于是：
//
//   - `authorization.list` 能看到它（label + methods + inFlight）；
//   - `begin` 走**真实的** device-code 流：notice 把 verification_uri 与
//     user_code 交给**发起它的那个 surface**（人不看日志也能知道去哪个页面、
//     输什么码）；
//   - `complete`（人在浏览器里输完码之后）在 flow 里被 replay —— 见下面
//     "两段式尝试"的说明；
//   - 成功之后 flow 调 `Commit()`，于是 `begin` 回 `authorized` **只可能**意味着
//     账号服务真的把凭据写下了（`internal/account` 的 `StatusCredentialStored`）。
//
// ## 两段式尝试（与官方的显式差异）
//
// 官方 `run()` 是"一次调用里把人问完"：device code 那一页由交互回填答案。
// 本仓现有的真实登录是 **RPC 两段**（`account.startSignIn` → 人开浏览器 →
// `account.completeSignIn`），这是既有的产品形状，不重写。
//
// 所以这里的 flow 在 `run()` 里：
//
//  1. 调 `StartSignIn` 拿真实的 device code；
//  2. `Notify` 把 verification_uri + user_code 发给发起方；
//  3. 调 `Prompt` 问"你已经在浏览器里输完码了吗？"（`select`：继续 / 取消）——
//     这一步是**人确认**，不是假的等待：选"取消"就是 `cancelled`；
//  4. 人确认之后调 `CompleteSignIn`（真正换 token + 写凭据）；
//  5. 写成功之后 `Commit()`。
//
// 这样 `authorized` 的语义与官方完全一致（记录真的写了），而 device code 的
// 真实时序没有被打断。**没有**伪造"两段式就是官方一次式"——差异写在这里。

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"dshgo/internal/account"
	"dshgo/internal/authorization"
)

// accountOIDCFlowKey 是这条 flow 写的凭据记录键（官方形状 `<scope>/<id>`）。
const accountOIDCFlowKey = "account/oidc"

// accountAuthorizer 是 flow 需要的**真实**账号服务能力。
//
// 用接口而不是直接吃 `*account.Service`：测试可以注入一个真实实现（跑真 OIDC
// 服务端），而生产用真服务。接口只有两个方法，正好是两段式登录的两个真实步骤。
type accountAuthorizer interface {
	StartSignIn(ctx context.Context, opts account.StartOptions) (account.State, error)
	CompleteSignIn(ctx context.Context, attemptID, code string) (account.State, error)
}

// accountOIDCFlow 把 account 服务的 device-code 登录包成一条官方 flow。
func accountOIDCFlow(svc accountAuthorizer) authorization.Flow {
	return authorization.Flow{
		Key:   accountOIDCFlowKey,
		Label: "深度求索账号（自托管 OIDC 设备码）",
		Methods: []authorization.Method{
			{ID: "device-code", Label: "用浏览器打开验证页并输入设备码"},
		},
		Run: func(ctx context.Context, session *authorization.Surface) error {
			if svc == nil {
				return errors.New("authorization: 没有账号服务（这个进程没装配 account），无法走 OIDC 登录")
			}
			state, err := svc.StartSignIn(ctx, account.StartOptions{})
			if err != nil {
				return err
			}
			attempt := state.Attempt
			if attempt == nil || strings.TrimSpace(attempt.AuthorizeURL) == "" {
				return fmt.Errorf("authorization: account.startSignIn 没有给出 device code（attempt=%+v）", attempt)
			}
			// notice 只到发起这次尝试的 surface（官方："notices and prompts only
			// to the surface that started it"）。它不携带 secret：user_code 是
			// 人在浏览器里输入的一次性码，不是凭据。
			if err := session.Notify(authorization.Notice{
				Message: "在浏览器里打开下面的地址并输入设备码，然后回到这里继续",
				URL:     attempt.AuthorizeURL,
				Code:    attempt.UserCode,
			}); err != nil {
				return err
			}
			// 人确认（select）：取消就是 cancelled（官方："A human's 'no' is an
			// outcome, not a breakage"）。
			answer, err := session.Prompt(authorization.Prompt{
				Kind:    authorization.PromptSelect,
				Message: "已经完成浏览器端的授权吗？",
				Options: []authorization.PromptOption{
					{ID: "done", Label: "已完成，继续"},
					{ID: "cancel", Label: "取消这次登录"},
				},
			})
			if err != nil {
				return err
			}
			switch strings.TrimSpace(answer) {
			case "done":
			case "cancel":
				return authorization.ErrDeclined
			default:
				return fmt.Errorf("authorization: 无法识别的答复 %q（可选：done / cancel）", answer)
			}
			final, err := svc.CompleteSignIn(ctx, attempt.ID, "")
			if err != nil {
				return err
			}
			// 真的写下了才 Commit：authorized 必须意味着凭据在存储里。
			if final.Status != account.StatusCredentialStored {
				return fmt.Errorf("authorization: 登录结束但凭据没写下（status=%s）", final.Status)
			}
			return session.Commit()
		},
	}
}

// newAuthorizationRegistry 装配宿主唯一的 flow 注册表。
//
// svc 为 nil（进程没有账号服务）时仍然注册表存在，只是没有 flow ——
// `authorization.list` 会如实回空，`begin` 会回 NO_FLOW（不静默成功）。
func newAuthorizationRegistry(svc accountAuthorizer) (*authorization.Registry, error) {
	registry := authorization.NewRegistry()
	if svc == nil {
		return registry, nil
	}
	if _, err := registry.RegisterFlow(accountOIDCFlow(svc)); err != nil {
		return nil, fmt.Errorf("注册 account OIDC flow 失败：%w", err)
	}
	return registry, nil
}
