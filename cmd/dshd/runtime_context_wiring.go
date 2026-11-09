package main

// 官方 `context.runtimeContext.perStep`（默认 true）在宿主入口上的接线。
//
// 官方原件：@deepseek-ai/dsh-agent-loop/README.md:120
//   "the loop re-projects runtime context facts every attempt without
//    reassembling the prompt"
//
// 宿主以前把"今天几号 / 在哪个目录 / 什么平台"只写进**装配那一刻**的系统提示
// （cmd/dshd/acp.go 的 buildSystemPromptWithHome），长会话跑几小时后这些事实
// 就过期了：模型还在说"今天是周五"，而用户看不出是提示词陈旧。
//
// 判据：
//  1. 事实每步**重新取**（时间不是装配时的那一份）；
//  2. 默认开启（官方默认 true），可用 `--runtime-context=false` 或
//     `DSHD_RUNTIME_CONTEXT=false` 关掉；
//  3. 关掉时请求消息与既有形状逐字节一致（见 internal/agent 的
//     TestRuntimeContextAbsentKeepsRequestBytes）。

import (
	"os"
	"runtime"
	"strings"
	"time"
)

const (
	// runtimeContextEnv 是 per-step 运行时事实的开关（官方没有 env，宿主补一个
	// 运维口子：排查"提示词漂移"时要能一键回退到装配期快照）。
	runtimeContextEnv = "DSHD_RUNTIME_CONTEXT"
)

// runtimeContextEnabled 解析开关：未设置 = 开启（官方默认 true）。
// 只认显式关闭的写法（false/0/no/off），其余一律按开启处理——静默关闭会让人
// 以为"时间明明是新的"。
func runtimeContextEnabled(getenv func(string) string) bool {
	if getenv == nil {
		getenv = os.Getenv
	}
	switch strings.ToLower(strings.TrimSpace(getenv(runtimeContextEnv))) {
	case "false", "0", "no", "off":
		return false
	default:
		return true
	}
}

// runtimeContextFacts 返回"此刻"的运行时事实提供者。
//
// 每次调用都重新取时间与工作目录：这正是 per-step 重投影的意义——值必须**当下**
// 是真的，不能是闭包创建那一刻的快照（那和写进系统提示没有区别）。
func runtimeContextFacts(sessionID string) func() string {
	return func() string {
		var b strings.Builder
		b.WriteString("现在时间: " + time.Now().Format(time.RFC3339) + "\n")
		if wd, err := os.Getwd(); err == nil && wd != "" {
			b.WriteString("工作目录: " + strings.ReplaceAll(wd, "\\", "/") + "\n")
		}
		b.WriteString("平台: " + runtime.GOOS + "/" + runtime.GOARCH)
		if sessionID != "" {
			b.WriteString("\n会话: " + sessionID)
		}
		return b.String()
	}
}

// runtimeContextProvider 按开关返回 Loop 用的提供者；关闭时返回 nil（不注入）。
func runtimeContextProvider(enabled bool, sessionID string) func() string {
	if !enabled {
		return nil
	}
	return runtimeContextFacts(sessionID)
}
