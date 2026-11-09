package tools

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"dshgo/internal/spi"
)

// repeat-tool-reminder（官方 0.2.2 base #83）的宿主实现。
//
// 官方语义取自 app.asar 里的原件：
//
//		dsh/node_modules/@deepseek-ai/dsh-repeat-tool-reminder/lib/index.js
//
//	  - 链的键是 (工具名, 规范化参数)：深键排序后 stringify，所以只差属性顺序的
//	    两次调用算同一条链（lib/index.js:2156-2169）；
//	  - 同键连续计 1、2、3…；换成别的键立刻回到 1（lib/index.js:2228-2239）；
//	  - 只在**精确等于** thresholds（默认 [3,5,8]）的那一次提醒，越过最高阈值后
//	    不再提醒（lib/index.js:2239）；
//	  - 第一个阈值给 gentle 文案（thresholds[0]，不是字面量 3），其余给详细文案
//	    （工具名 + 连续次数 + 参数预览，预览上限 500 字符，lib/index.js:2138-2183）；
//	  - 被拒绝的调用也计数（官方把 observe 放在 post-execute waterfall 上，
//	    deny 也走那条路，lib/index.js:2220-2227）。
//
// 宿主差异（也写在 HOST-PLUGIN-REPEAT-REMINDER-RESULT.md 里）：官方把提醒塞进
// additionalContexts，由循环追加成一条带 source.kind='repeat-tool-reminder' 的
// 合成 user 消息；本仓没有这个通道，模型可见的落点是 spi.Result.Hint（投影成
// 「提示：…」文本块）。载荷相同，落点不同——不改变工具的成败、不吞原始输出。
const (
	// repeatGentleReminder 是 lib/index.js:2142 的 GENTLE_REMINDER 原文。
	repeatGentleReminder = "You are repeating the exact same tool call with identical arguments. " +
		"Carefully analyze the previous result before calling again: if the task is not complete, " +
		"try a different approach or different arguments instead of repeating the call."

	// repeatDetailedHead 与 repeatDetailedTail 拼出 lib/index.js:2144-2147 的
	// detailedReminder() 原文（中间三段是变量，见 repeatDetailedReminder）。
	repeatDetailedHead = "Repeated tool call detected:"
	repeatDetailedTail = "The repeated calls are not making progress. Do not call this tool with " +
		"these exact arguments again. Inspect the latest result and choose a different action, " +
		"different arguments, or finish the task if enough evidence has been gathered."

	// repeatArgumentsPreviewChars 是官方 argumentsPreviewChars 的默认值（lib/index.js:2130）。
	repeatArgumentsPreviewChars = 500
)

// repeatThresholds 返回官方的默认阈值 [3,5,8]。
//
// 用"函数返一份新的"而不是包级 var：本包的计数状态一律挂在 Pipeline 上，
// 不允许包级可变全局（并发会话/多内核共用同一个进程时会串账）。
func repeatThresholds() []int { return []int{3, 5, 8} }

// repeatScope 是一次"连续重复"的计数作用域：单会话 + 单轮。
//
// 为什么必须带 session：A/B 两个会话同时跟同一个模型说话，链必须是两条；
// 否则 B 的一次调用会把 A 的计数冲掉，A 反而收不到提醒。
//
// turn 取自 spi.RunIDFrom(ctx)：agent 循环在**每一轮入口**放一次（同一轮里所有
// step 稳定、下一轮必然换一个），所以"新的用户消息清零"是构造出来的，不靠 ctx
// 生命周期兜底——turn ctx 常常活到进程退出，靠它等于不重置。
type repeatScope struct {
	session string
	turn    string
}

// repeatChain 是链上"最近一次调用"的状态。
type repeatChain struct {
	ctx   context.Context // 建立这条链的那一次 ctx；它结束就代表这一轮结束了
	key   string          // (工具名, 规范化参数) 的指纹
	count int             // 连续次数
	// seq 是建立序号：链表超过上限时按它淘汰最老的一条（见 evictLocked）。
	seq uint64
}

// maxRepeatChains 是链表的硬上限。
//
// 为什么需要：run id 每轮一个新值，而 turn ctx 常常活到进程退出，只靠
// ctx.Done() 回收等于不回收——一个跑了几万轮的长驻会话会留下同样多的链，
// 其中除了最后一轮以外再也不会被用到。上限只影响"多老的轮次还能被记住"，
// 不影响任何一轮内的计数：一轮的链只要还在上限内就是准确的。
const maxRepeatChains = 4096

// repeatGuard 是计数器本体。它由 Pipeline 持有，不注册任何全局状态。
type repeatGuard struct {
	thresholds []int
	preview    int

	mu     sync.Mutex
	seq    uint64
	chains map[repeatScope]*repeatChain
}

func newRepeatGuard() *repeatGuard {
	return &repeatGuard{
		thresholds: repeatThresholds(),
		preview:    repeatArgumentsPreviewChars,
		chains:     make(map[repeatScope]*repeatChain),
	}
}

// observe 推进一次计数，返回"这一次该注入的提醒"（没命中阈值时 ok=false）。
//
// 计数点与官方一致：**每一次尝试**都计数，包括失败的、被 pre-execute 拒绝的、
// 工具名不存在的。一个模型反复撞同一堵墙，恰恰是最该被提醒的循环。
func (g *repeatGuard) observe(ctx context.Context, toolName string, args json.RawMessage) (string, bool) {
	canonical := repeatCanonicalArgs(args)
	key := toolName + "\x00" + canonical
	scope := repeatScope{session: spi.SessionFrom(ctx), turn: spi.RunIDFrom(ctx)}

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.chains == nil {
		g.chains = make(map[repeatScope]*repeatChain)
	}

	chain := g.chains[scope]
	if chain != nil && chain.ctx != nil && chain.ctx.Err() != nil {
		// 上一轮已经结束（turn ctx 被取消/超时）。官方口径是"新的用户消息清零"，
		// 这里用 ctx 的生命周期当同一条界线——查 Err 是同步的，所以不必等
		// expire goroutine 跑完，下一轮第一次调用一定会重新从 1 数。
		chain = nil
		delete(g.chains, scope)
	}

	count := 1
	if chain != nil && chain.key == key {
		count = chain.count + 1
	}
	g.seq++
	next := &repeatChain{ctx: ctx, key: key, count: count, seq: g.seq}
	g.chains[scope] = next
	g.evictLocked()
	if chain == nil && ctx != nil && ctx.Done() != nil {
		// 链按 ctx 生命周期回收：不这么做的话，长驻进程里每个会话每一轮都会留
		// 一条再也用不到的链。身份比对是必要的——回收时作用域可能已经被新一轮
		// 的同名链替换掉了。
		go g.expire(scope, next, ctx.Done())
	}

	if !g.hit(count) {
		return "", false
	}
	if count == g.thresholds[0] {
		return repeatGentleReminder, true
	}
	return repeatDetailedReminder(toolName, count, repeatPreview(canonical, g.preview)), true
}

// expire 在作用域结束（ctx 结束）时丢掉那一条链。
func (g *repeatGuard) expire(scope repeatScope, chain *repeatChain, done <-chan struct{}) {
	<-done
	g.mu.Lock()
	if g.chains[scope] == chain {
		delete(g.chains, scope)
	}
	g.mu.Unlock()
}

// evictLocked 在链表超过上限时淘汰最老的一条。
//
// 只在**新增**链时调用，所以稳态下每次工具调用最多做一次全表扫描（4096 条，
// 微秒级）——比"每轮留一条永久链"的内存增长便宜得多。
func (g *repeatGuard) evictLocked() {
	if len(g.chains) <= maxRepeatChains {
		return
	}
	var (
		oldestScope repeatScope
		oldestSeq   uint64
		found       bool
	)
	for scope, chain := range g.chains {
		if !found || chain.seq < oldestSeq {
			oldestScope, oldestSeq, found = scope, chain.seq, true
		}
	}
	if found {
		delete(g.chains, oldestScope)
	}
}

// hit 判断这次次数是不是配置里的某个阈值（精确命中，不是"达到"）。
func (g *repeatGuard) hit(count int) bool {
	for _, th := range g.thresholds {
		if th == count {
			return true
		}
	}
	return false
}

// repeatDetailedReminder 拼出官方 detailedReminder() 的原文。
func repeatDetailedReminder(toolName string, count int, canonical string) string {
	return repeatDetailedHead + "\n" +
		"- tool: " + toolName + "\n" +
		"- consecutive_calls: " + itoa(count) + "\n" +
		"- arguments: " + canonical + "\n" +
		repeatDetailedTail
}

// repeatCanonicalArgs 是参数的规范化形式：解析成 JSON 值后重新序列化（encoding/json
// 对 map 的键是**排序输出**的，等价于官方的深键排序 + JSON.stringify）。
//
// 解析不了就用原始字符串兜底——官方对畸形参数 JSON 走的就是这条 fallback
// （lib/index.js:2148-2155 的注释），链的键仍然稳定。
func repeatCanonicalArgs(args json.RawMessage) string {
	var v any
	if err := json.Unmarshal(args, &v); err != nil {
		return string(args)
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	// 关掉 HTML 转义：官方是 JSON.stringify，不会把 < > & 写成 \u003c。
	// 不关的话，同一份参数在提醒里长得跟前端/官方不一样，模型看到的是转义串。
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return string(args)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// repeatPreview 按官方口径截断"给模型看的那段参数"：超cap就留头 + 省略量。
//
// 只截展示，不截指纹：链的键始终是全文，否则"前 500 字符相同、后面不同"的两次
// 调用会被误判成同一条链。
func repeatPreview(canonical string, max int) string {
	r := []rune(canonical)
	if max <= 0 || len(r) <= max {
		return canonical
	}
	return string(r[:max]) + "… (+" + itoa(len(r)-max) + " more chars)"
}

// applyRepeatReminder 把提醒追加到结果的 Hint 上。
//
// 三条硬约束：
//   - 不改变成败：不碰 err、不碰 Stop；
//   - 不吞原始输出：只改 Hint，Summary/Text/Data/Artifacts 原样；
//   - 不破 R3：Hint 里已有的那句"下一步"必须留着，提醒追加在后面。
func (p *Pipeline) applyRepeatReminder(ctx context.Context, call *spi.Call, res *spi.Result) {
	if p == nil || p.repeat == nil || call == nil || res == nil {
		return
	}
	if call.Nested {
		// 子调用（workflow 里发的）不是"正在和我对话的那个模型"发的：官方按 agent
		// 分链，子代理的重复不能算到父代理头上，也不能把父代理的链冲掉。
		return
	}
	reminder, ok := p.repeat.observe(ctx, call.Name, call.Args)
	if !ok {
		return
	}
	if strings.TrimSpace(res.Hint) == "" {
		res.Hint = reminder
		return
	}
	res.Hint += "\n\n" + reminder
}
