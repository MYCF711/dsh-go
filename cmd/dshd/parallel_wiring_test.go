package main

// 并行工具调用（GO.6 / T4）在**宿主装配层**的接线证据。
//
// 两件事只有在这里才证明得了：
//
//  1. 环境变量真的能到达真正跑循环的那个进程。`dshd serve` 每轮起一个
//     `dshd run` 子进程，那一路的 env 是**重建**的
//     （serve_loop.go 的 providerLoopEnvironment 从 os.Environ 过滤后拼一份）；
//     过滤时顺手把 DSHD_MAX_PARALLEL_TOOL_CALLS 也删掉，那么"设了没反应"，
//     而且在日志里一行都不会留下。
//  2. 子调用（workflow 里发的那些）写 v4 时是**串行**的。并行执行之后，
//     两个调用可能各自带着子调用同时走到那条回调；v4 的 seq 分配与写帧都没有锁
//     （R5 单写者），重号/交错的日志是读不回来的。

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"dshgo/codecs/v4"
	"dshgo/internal/llm"
	"dshgo/internal/modes"
	"dshgo/internal/session"
	"dshgo/internal/spi"
)

// 模型端点的 env 每轮会被 providerLoopEnvironment 重建成"去掉 LLM 那三个键"
// 的一份；其余变量必须原样保留，否则 serve 起的子进程读不到任何调优参数。
func TestProviderLoopEnvironmentKeepsParallelToolCalls(t *testing.T) {
	t.Setenv("DSHD_MAX_PARALLEL_TOOL_CALLS", "3")
	env := providerLoopEnvironment(llm.ModelProviderConfig{BaseURL: "http://127.0.0.1:9/v1"})

	got, found := lookupEnv(env, "DSHD_MAX_PARALLEL_TOOL_CALLS")
	if !found {
		t.Fatalf("serve 子进程的 env 里没有 DSHD_MAX_PARALLEL_TOOL_CALLS：设了也不会生效。"+
			"providerLoopEnvironment 会重建整份 env，遗漏的键就是死开关（共 %d 项）", len(env))
	}
	if got != "3" {
		t.Fatalf("DSHD_MAX_PARALLEL_TOOL_CALLS = %q，期望 %q", got, "3")
	}
}

// lookupEnv 按 Windows 的规则（环境名不区分大小写）找一项。
func lookupEnv(env []string, name string) (string, bool) {
	for _, entry := range env {
		k, v, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(k, name) {
			return v, true
		}
	}
	return "", false
}

// 并发写 v4：nestedV4 必须让"一次调用的 tool/call + tool/result"成对且 seq 连续。
func TestNestedV4SerializesConcurrentAppends(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	logw, err := session.CreateSession(home, cwd, "sess-nested-v4", modes.DefaultID)
	if err != nil {
		t.Fatalf("建会话失败：%v", err)
	}
	w := newNestedV4(logw)

	const writers, perWriter = 8, 20
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perWriter; j++ {
				// 非 Nested 的调用不写盘：那条路由顶层循环负责（单写者，别记两遍）。
				if err := w.record(&spi.Call{Nested: false, Name: "grep"}); err != nil {
					t.Errorf("非嵌套调用不该写盘也不该报错：%v", err)
					return
				}
				if err := w.record(&spi.Call{
					Nested: true, Name: "grep",
					Args:   json.RawMessage(`{"pattern":"x"}`),
					Result: spi.Result{Summary: "命中 1 行"},
				}); err != nil {
					t.Errorf("写子调用失败：%v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	path := logw.Path
	if err := logw.Close(); err != nil {
		t.Fatalf("关会话失败：%v", err)
	}

	back, err := v4.ReadFile(path)
	if err != nil {
		t.Fatalf("会话日志读不回来（并发写把它写坏了）：%v", err)
	}
	// 文件头 + 每对事件。
	if got, want := len(back.Events), writers*perWriter*2; got != want {
		t.Fatalf("事件数 = %d，期望 %d（丢了帧或被覆盖）", got, want)
	}
	for i, ev := range back.Events {
		if ev.Seq != int64(i) {
			t.Fatalf("第 %d 条事件 seq = %d，期望 %d：seq 重号说明有两个写者同时分配",
				i, ev.Seq, i)
		}
	}
	// tool/call 与 tool/result 必须成对相邻，且 callId 对得上。
	for i := 0; i < len(back.Events); i += 2 {
		call, result := back.Events[i], back.Events[i+1]
		if call.Type != "tool/call" || result.Type != "tool/result" {
			t.Fatalf("第 %d-%d 条事件类型 = %s,%s，期望 tool/call,tool/result 成对相邻",
				i, i+1, call.Type, result.Type)
		}
		// 两个事件的 callId 落在不同位置：tool/call 在顶层，tool/result 在
		// message.source.callId / message.toolCallId（v4 的既有形状，别改）。
		var callData struct {
			CallID string `json:"callId"`
		}
		var resultData struct {
			Message struct {
				CallID     string `json:"toolCallId"`
				SourceCall string `json:"-"`
				Source     struct {
					CallID string `json:"callId"`
				} `json:"source"`
			} `json:"message"`
		}
		if err := json.Unmarshal(call.Data, &callData); err != nil {
			t.Fatalf("解析 tool/call 失败：%v", err)
		}
		if err := json.Unmarshal(result.Data, &resultData); err != nil {
			t.Fatalf("解析 tool/result 失败：%v", err)
		}
		resultCallID := resultData.Message.CallID
		if resultData.Message.Source.CallID != resultCallID {
			t.Fatalf("tool/result 里的两个 callId 不一致：%q / %q",
				resultCallID, resultData.Message.Source.CallID)
		}
		if callData.CallID != resultCallID {
			t.Fatalf("第 %d 对事件的 callId 不一致：call=%q result=%q（串了别的调用）",
				i/2, callData.CallID, resultCallID)
		}
	}
}
