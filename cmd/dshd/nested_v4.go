package main

// 子调用（workflow 里发的那些）的 v4 写入点。
//
// # 它存在的原因
//
// 子调用不属于 agent 循环的事件流，循环不会为它们写 v4：编排里跑了 5 次 grep，
// 回去看 session.history 却什么都没有。所以这里补一条**只给 Nested** 的写入点，
// 顶层调用仍由循环自己写（不加这个判断就会把同一次调用记两遍）。
//
// # 为什么要一把锁
//
// v4 是**单写者**模型（R5）：`session.Log.Append` 分配 seq、`v4.Writer.Append`
// 直接写文件，两者都没有锁（internal/session/log.go、codecs/v4/write.go）。
// 串行执行时代这不成问题——同一条管道上只有一个写者。
//
// 同一次模型响应里的多个 tool_call 并发执行之后（GO.6 / T4），两个同时跑的
// 调用可能**各自带着自己的子调用**在同一时刻走到这里：seq 会重号、帧会交错。
// 而 append-only 的会话日志坏掉**没有回头路**——那不是"慢一点"，
// 是一个再也读不回来的会话。
//
// 只锁这一处就是全部：顶层调用的事件由循环在**调用方 goroutine** 上按序产生
// （并行执行期间它正等着工具结果，不会同时写日志），子调用则全部汇聚到这条回调。

import (
	"fmt"
	"sync"
	"sync/atomic"

	"dshgo/internal/session"
	"dshgo/internal/spi"
)

// nestedV4 把"子调用的工具结果"写进会话日志，一次一条，**串行**。
type nestedV4 struct {
	log *session.Log
	// mu 保证 seq 分配与帧写入成对完成（v4 单写者）。
	mu sync.Mutex
	// n 是子调用的计号：同一个进程内不重复即可（它只是日志里的一个引用名，
	// 不参与跨进程语义）。
	n atomic.Int64
}

// newNestedV4 造一个写入点；log 为 nil 时 record 什么都不做
// （没有会话日志的场景：单元测试、`dshd run` 之外的嵌入方）。
func newNestedV4(log *session.Log) *nestedV4 {
	return &nestedV4{log: log}
}

// record 是 spi.EventToolResult 上的回调：只理 Nested 的调用。
func (w *nestedV4) record(c *spi.Call) error {
	if w == nil || w.log == nil || c == nil || !c.Nested {
		return nil
	}
	// 锁的粒度是"一次调用的两条事件"：tool/call 与它的 tool/result 必须相邻，
	// 否则读回来会看到一个"有结果没调用"的会话。
	w.mu.Lock()
	defer w.mu.Unlock()

	id := fmt.Sprintf("wf-%d", w.n.Add(1))
	if err := w.log.Append("tool/call", session.ToolCallEvent(1, 0, id, c.Name, string(c.Args))); err != nil {
		return nil // 记账失败不该打断真正在干活的调用
	}
	var blocks []spi.Block
	switch {
	case c.Result.Text != "":
		blocks = []spi.Block{{Type: "text", Text: c.Result.Text}}
	case c.Result.Summary != "":
		blocks = []spi.Block{{Type: "text", Text: c.Result.Summary}}
	}
	return w.log.Append("tool/result",
		session.ToolResultEvent(1, 0, id, blocks, c.Err != nil, "", nil))
}
