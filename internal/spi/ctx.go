package spi

import "context"

// 会话上下文：让**工具**知道自己正在为哪个会话干活。
//
// 为什么必须走 ctx 而不是工具字段：工具是全局注册一次的，
// 一个进程里可能服务多个会话。要在工具里回答"我是谁"（比如读自己的信箱、
// 记自己的目标），只能靠每次调用带下来的 ctx。
type ctxKey int

const sessionKey ctxKey = 0

// WithSession 把会话 id 绑到 ctx 上；空 id 原样返回。
func WithSession(ctx context.Context, id string) context.Context {
	if ctx == nil || id == "" {
		return ctx
	}
	return context.WithValue(ctx, sessionKey, id)
}

// SessionFrom 取当前会话 id；没有就返回空串（工具据此给出明确的 Hint）。
func SessionFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	s, _ := ctx.Value(sessionKey).(string)
	return s
}

// RunIDKey 是"这一次 run 的标识"，与 SessionID 分开：
// 同一个会话里可能跑多次 run（每轮一次），信箱去重靠它。
const runIDKey ctxKey = 1

// WithRunID 绑定本次 run 的 id。
func WithRunID(ctx context.Context, id string) context.Context {
	if ctx == nil || id == "" {
		return ctx
	}
	return context.WithValue(ctx, runIDKey, id)
}

// RunIDFrom 取本次 run 的 id。
func RunIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	s, _ := ctx.Value(runIDKey).(string)
	return s
}
