package main

import (
	"context"
	"strings"

	"dshgo/internal/spi"
)

// inheritFromCtx 取父会话最近若干条，拼成一段给子代理看的文本。
//
// 为什么要限条数：父会话可能很长，全塞进去等于把子代理的上下文预算一次耗光，
// 而它真正需要的是"刚才在聊什么"。
//
// 取不到就返回空串（子代理退化成普通 subagent）——不报错：
// 继承失败不该让任务失败，但**必须在结果里看得出来没继承**。
func inheritFromCtx(home string) func(ctx context.Context) string {
	return func(ctx context.Context) string {
		sid := spi.SessionFrom(ctx)
		if sid == "" {
			return ""
		}
		src := newSessionSource(home, "")
		msgs, err := src.Messages(sid)
		if err != nil || len(msgs) == 0 {
			return ""
		}
		const keep = 20
		if len(msgs) > keep {
			msgs = msgs[len(msgs)-keep:]
		}
		var b strings.Builder
		for _, m := range msgs {
			if m.Text == "" {
				continue
			}
			b.WriteString(m.Role + ": " + m.Text + "\n")
		}
		return b.String()
	}
}
