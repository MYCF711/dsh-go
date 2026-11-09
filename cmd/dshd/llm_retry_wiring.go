package main

// 官方 `retryPolicy`（每 provider 一份）的**进程间接线**。
//
// 宿主的形状：serve 进程持有模型目录，每一轮起一个 `dshd run` 子进程跑模型循环。
// provider 的字段（baseURL / credential / api）已经用环境变量交给子进程了
// （serve_loop.go 的 providerLoopEnvironment），重试策略是同一个 provider 的
// 另一个字段，走同一条路——**不新开第二条通道**，否则"界面看到的 provider"与
// "实际跑的策略"会是两份真值。
//
// 子进程侧：`newTurnChat` 从环境里把策略读出来装到那一条 Client 上。没设 = nil =
// 生产默认（retry.go 的 defaultRetryConfig），一个字节都不变。

import (
	"dshgo/internal/agent"
	"dshgo/internal/llm"
)

// newTurnChat 造这一轮用的聊天客户端，并把 per-provider 重试策略接上。
//
// env 显式传入（而不是直接 os.Getenv）：调用点与用例必须看到同一套解析，
// 也让"这一轮到底读了哪些环境变量"在代码里是可见的。
func newTurnChat(base, key, api string, env func(string) string) (agent.Chat, error) {
	policy, err := llm.RetryPolicyFromEnv(env)
	if err != nil {
		return nil, err
	}
	client := llm.NewWithAPI(base, key, api)
	if policy != nil {
		client.ApplyRetryPolicy(policy)
	}
	return client, nil
}

// 编译期确认：*llm.Client 就是 agent 循环要的那个 Chat。
var _ agent.Chat = (*llm.Client)(nil)
