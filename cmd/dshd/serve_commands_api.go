package main

// 宿主侧**真实**命令注册表的装配点（`commands.list` / `commands.execute`）。
//
// # 为什么需要这一层
//
// 本宿主原来没有命令注册表：TUI 的 `/help` `/new` `/compact` 都在
// `cmd/dshtui` 里就地处理，宿主这一侧只有 transport 方法名。所以
// `commands.list` 只能回空数组、`commands.execute` 只能明确 501。
//
// 这里落一份**真注册表**（`transport.OfficialCommandRegistry`：可插拔、
// 可列出、生命周期记账、附件准入），并把能在本宿主落到**真实状态**的官方
// 内置命令注册进去。官方 builtin 是
// `goal / plan / feedback / compact / permission / export`（见
// `@deepseek-ai/dsh-commands/lib/types/builtin.js`）；本宿主里
// `plan`（计划模式）、`permission`（权限预设）、`export`（导出）各自缺一条
// 真实依赖（见 RESULT 的"未接线"），**不注册它们**——注册一个只会回
// "未实现"的命令，等于让客户端以为能用。
//
// `compact` 原先也在这个名单里；现在它接上了宿主**真实**的压缩路径
// （`compactauto.go` 的 `autoCompact`，与 CLI `dshd session compact --auto`、
// Transport `session.compact` 同一段代码），所以在这里注册。
//
// 因此这里注册的是能**真的做事**的那几个：
//
//	/help                列出当前注册表（真实发现）
//	/goal                显示本会话的 goal（真实 goal 存储）
//	/feedback <正文>     记录一条会话反馈（真实 durable 写入）
//	/compact             压一次会话历史（官方 base 插件 #65，真实压缩路径）
//
// 四条都不是"复制 TUI 命令表"：它们的 handler 走的是宿主真实状态。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"dshgo/codecs/v4"
	"dshgo/internal/session"
	"dshgo/internal/transport"
)

// 宿主内置命令名（不带斜杠，官方字符集是小写）。
const (
	hostCommandHelp     = "help"
	hostCommandGoal     = "goal"
	hostCommandFeedback = "feedback"
	hostCommandCompact  = "compact"
)

// 官方 base 插件 #65 `command-compact` 的身份与文案。
//
// 这些字符串**逐字**来自官方原件
// `@deepseek-ai/dsh-command-compact/src/index.ts`（108 行）：definitionId 决定
// 客户端的 scoped shadowing 认不认得出"这就是官方那条 /compact"，error text
// 是给用户看的**官方原文**——改写它们等于换了一条命令。
//
// 关于最后三条：官方 `ManualCompactionError` 是**封闭联合**（busy / cancelled /
// changed / summary / commit / persistence），六条文案在这里逐字留全，
// 但本宿主这条压缩路径只给得出 busy / cancelled / summary 三条的可判定信号，
// 另外三条**不可达**（为什么不可达、硬映射会错在哪，见 `compactFailureText`
// 的注释）。
const (
	officialCompactDefinitionID = "@deepseek-ai/dsh-command-compact"
	officialCompactDescription  = "Compact older conversation history"
	officialCompactUsage        = "Usage: /compact (no arguments)"

	officialCompactErrorBusy      = "Compaction is unavailable because this process has an active compaction, or the agent is not idle."
	officialCompactErrorCancelled = "Compaction cancelled."
	officialCompactErrorSummary   = "Compaction could not produce a useful summary. The attempt is recorded in the session log."

	// 以下三条在本宿主不可达（见 compactFailureText）：留下原文是为了让
	// "官方 6 条 code"在这份源码里是完整的一份，而不是漏掉三条没人知道的。
	officialCompactErrorChanged     = "The history selected for compaction changed before it could be replaced. The attempt is recorded in the session log."
	officialCompactErrorCommit      = "Compaction did not finish cleanly; some session history may have changed. Inspect the current session state before retrying."
	officialCompactErrorPersistence = "Compaction finished, but the session could not be saved."
)

// compactCommandKeepRecent 是 `/compact` 保留的最近消息条数。
//
// 官方那条命令**不接受任何参数**（多一个字符就是 usage 错误），所以这个数只能
// 由宿主定。取 6 是因为宿主已有的缺省就是 6：Transport `session.compact` 在
// `keep <= 0` 时填 6（internal/transport/handlers_extra.go），CLI
// `dshd session compact --auto` 的 `--keep` 缺省也是 6。换成别的数等于同一个
// 动作在三条入口上有三种"保留多少"，用户按 /compact 和按界面压缩会看到不同结果。
const compactCommandKeepRecent = 6

// commandLifecycleMu 串行化 v4 生命周期追加。
//
// 为什么需要：`session.OpenSessionAt` 抢的是**文件**写锁
// （`<session>.lock`），两个并发 append 会互相撞成"会话正被另一个进程写入"。
// 锁的作用域是进程内，因此这一层的并发由这把互斥锁兜住；跨进程仍由文件锁
// 负责（撞上就重试，重试完还失败就**响亮报错**，绝不静默丢帧）。
var commandLifecycleMu sync.Mutex

// commandLifecycleRetries 是抢写锁的重试次数（写锁持有时间很短）。
const commandLifecycleRetries = 20

// newHostCommandRegistry 把真实命令装配成注册表。
func newHostCommandRegistry(api *agentOfficialAPI) (*transport.OfficialCommandRegistry, error) {
	if api == nil {
		return nil, fmt.Errorf("命令注册表没有宿主对象")
	}
	registry := transport.NewOfficialCommandRegistry(transport.OfficialCommandRegistryOptions{
		Lifecycle: func(sessionID, eventType string, data any) (int64, error) {
			return appendCommandLifecycle(api.deps.src, sessionID, eventType, data)
		},
	})

	if _, err := registry.Register(transport.OfficialCommandDefinition{
		DefinitionID: hostCommandGoal,
		Name:         hostCommandGoal,
		Description:  "显示本会话的目标（阶段 / 轮次 / 剩余轮数）",
		Input:        &transport.OfficialCommandInput{Hint: "（goal 不需要参数）"},
		Handler:      api.commandGoal,
	}); err != nil {
		return nil, err
	}

	if _, err := registry.Register(transport.OfficialCommandDefinition{
		DefinitionID: hostCommandFeedback,
		Name:         hostCommandFeedback,
		Description:  "记录一条会话反馈（落 durable 存储）",
		Input:        &transport.OfficialCommandInput{Hint: "想反馈的内容"},
		Handler:      api.commandFeedback,
	}); err != nil {
		return nil, err
	}

	// /compact：官方 base 插件 #65。definitionId / description 用官方原件里的
	// 原值（不是自己起的名），客户端才认得出这条就是官方的 /compact；
	// 也**不声明 input**——官方那条没有 input，声明了反而会接受官方不接受的附件。
	if _, err := registry.Register(transport.OfficialCommandDefinition{
		DefinitionID: officialCompactDefinitionID,
		Name:         hostCommandCompact,
		Description:  officialCompactDescription,
		Handler:      api.commandCompact,
	}); err != nil {
		return nil, err
	}

	// help 最后注册：它要列出**包含前面那些**的当前注册表。
	helpers := &hostCommandHelpHandler{registry: registry}
	if _, err := registry.Register(transport.OfficialCommandDefinition{
		DefinitionID: hostCommandHelp,
		Name:         hostCommandHelp,
		Description:  "列出所有可用命令",
		Handler:      helpers.run,
	}); err != nil {
		return nil, err
	}
	return registry, nil
}

// hostCommandHelpHandler 需要一个"注册表已存在"的引用，所以单独立一个类型。
type hostCommandHelpHandler struct {
	registry *transport.OfficialCommandRegistry
}

// run 列出当前注册表。
//
// 刻意**不再列一份硬编码清单**：清单的真值就是注册表本身，复制一份必然漂移。
func (h *hostCommandHelpHandler) run(_ context.Context, _ transport.OfficialCommandInvocation) (transport.OfficialCommandOutcome, error) {
	var b strings.Builder
	b.WriteString("可用命令：\n")
	for _, cmd := range h.registry.List() {
		b.WriteString("  /")
		b.WriteString(cmd.Name)
		b.WriteString("  ")
		b.WriteString(cmd.Description)
		b.WriteString("\n")
	}
	return transport.OfficialCommandOutcome{Kind: "success", Text: strings.TrimRight(b.String(), "\n")}, nil
}

// commandGoal 显示本会话真实 goal 记录。
//
// 数据源是 goals.* 用的**同一个** versioned snapshot 存储——没有第二份 goal
// 存储，也没有"为了有输出编一个"。
func (a *agentOfficialAPI) commandGoal(_ context.Context, inv transport.OfficialCommandInvocation) (transport.OfficialCommandOutcome, error) {
	record, exists, err := a.agentCurrentGoal(inv.SessionID)
	if err != nil {
		return transport.OfficialCommandOutcome{}, err
	}
	if !exists {
		return transport.OfficialCommandOutcome{Kind: "success", Text: "本会话没有 goal。"}, nil
	}
	view, err := a.agentGoalView(inv.SessionID, record)
	if err != nil {
		return transport.OfficialCommandOutcome{}, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "goal %s：%s\n", view.ID, view.Phase)
	fmt.Fprintf(&b, "目标：%s\n", view.Objective)
	fmt.Fprintf(&b, "轮次：%d", view.RoundsStarted)
	if view.MaxGoalRounds > 0 {
		fmt.Fprintf(&b, " / 上限 %d", view.MaxGoalRounds)
	}
	return transport.OfficialCommandOutcome{Kind: "success", Text: b.String()}, nil
}

// commandFeedback 记录一条真实会话反馈。
//
// 走的是 sessionFeedback.record 的**同一个** durable 写入；空正文明确报错
// （官方 error text 必须非空，空反馈等于什么都没记）。
func (a *agentOfficialAPI) commandFeedback(_ context.Context, inv transport.OfficialCommandInvocation) (transport.OfficialCommandOutcome, error) {
	text := strings.TrimSpace(inv.RawInput)
	if text == "" {
		return transport.OfficialCommandOutcome{
			Kind: "error",
			Text: "/feedback 需要正文：/feedback <想反馈的内容>",
		}, nil
	}
	result, err := a.sessionFeedbackRecord(transport.OfficialSessionFeedbackRequest{
		SessionID: inv.SessionID, Text: text,
	})
	if err != nil {
		return transport.OfficialCommandOutcome{}, err
	}
	if !result.OK {
		code := "记录失败"
		if result.Error != nil {
			code = result.Error.Code
		}
		return transport.OfficialCommandOutcome{Kind: "error", Text: "记录反馈失败：" + code}, nil
	}
	return transport.OfficialCommandOutcome{Kind: "success", Text: "已记录这条反馈。"}, nil
}

// commandCompact 跑一次**真实**的手动压缩（官方 base 插件 #65 `command-compact`）。
//
// 它不另写一套压缩：走的是 `compactauto.go` 的 `autoCompact`——和 CLI
// `dshd session compact --auto`、Transport `session.compact` 完全同一段代码，
// 所以三条入口不可能压出三种结果。
//
// 官方 handler 的三条语义在这里逐条落地：
//  1. 带参数（`rawInput.trim()` 非空）→ usage 错误，**不压**；
//  2. 没有可压的历史（官方 `compactNow` 回 null）→ success 的
//     `No compactable history yet.`——那是正常结果，不是失败；
//  3. 压成功 → `Compacted <n> history items (~<tokens> tokens).`，
//     并把摘要帧的 seq 作为 `sourceEventSeq` 回报。
func (a *agentOfficialAPI) commandCompact(ctx context.Context, inv transport.OfficialCommandInvocation) (transport.OfficialCommandOutcome, error) {
	// 官方是 `rawInput.trim().length > 0`：只空白也算没参数。
	if strings.TrimSpace(inv.RawInput) != "" {
		return transport.OfficialCommandOutcome{Kind: "error", Text: officialCompactUsage}, nil
	}
	path, err := session.ResolvePath(a.deps.home, "", inv.SessionID)
	if err != nil {
		return transport.OfficialCommandOutcome{}, err
	}
	// 压缩**前**先读一次日志。理由只有一个：压完之后被遮蔽的那段就不在
	// surface 上了（`session.Surface` 会把 shadowedSeqs 滤掉），而下面要报的
	// token 数说的正是"被压掉的那段历史有多大"——那时候再读就拿不到了。
	before, err := v4.ReadFile(path)
	if err != nil {
		return transport.OfficialCommandOutcome{}, fmt.Errorf("/compact 读会话日志失败: %w", err)
	}

	res, err := autoCompact(ctx, path, compactCommandKeepRecent, 0,
		envOr("DSHD_LLM_MODEL", "group/deepseek-v4.1-flash-1017"),
		envOr("DSHD_LLM_BASE", defaultLLMBase))
	if err != nil {
		return transport.OfficialCommandOutcome{Kind: "error", Text: compactFailureText(ctx, err)}, nil
	}
	if !res.Compacted {
		return transport.OfficialCommandOutcome{Kind: "success", Text: "No compactable history yet."}, nil
	}

	outcome := transport.OfficialCommandOutcome{
		Kind: "success",
		Text: fmt.Sprintf("Compacted %d history items (~%d tokens).",
			res.Shadowed, compactShadowedTokens(before, res.From, res.To)),
	}
	// 官方 `sourceEventSeq` 是 `result.summarySeq`（摘要帧自己的 seq）。宿主的
	// `autoCompact` 把 `session.Compact` 返回的 id/seq 丢掉了，这条 seq 的唯一
	// 真值来源就是刚落盘的日志——所以读回来，而不是按"应该等于之前那条 +2"去推
	// （推导会随 `session.Compact` 的帧数变化而静默错掉）。
	if seq, ok := compactSummarySeq(path); ok {
		outcome.SourceEventSeq = &seq
	}
	return outcome, nil
}

// compactShadowedTokens 估算本次被遮蔽的那段历史占多少 token。
//
// # 为什么不用现成字段
//
// 官方 `ManualCompactionResult.shadowedTokenCount` 是"被压掉的那段历史有多少
// token"；本宿主的 `CompactResult` 里**没有**这个数——`Summary` 是**摘要字数**
// （compactauto.go:28 的字段文档），而 `CompactionMeta.TokenCount` 在同一条
// 自动压缩路径上写进去的也是摘要字数（compactauto.go:70，落盘成
// `shadowedTokenCount`）。把那个数当 token 报出去，是拿一个已知不是 token 的
// 数字去顶 token 的位置——不是"没有更好的办法"，是撒谎。
//
// # 所以怎么算
//
// 用宿主**自己**的估算器（`session.Meter`，ACP 上下文压力判阈值用的同一个）
// 对 `[from,to]` 区间里的表面消息估一次。两个输入都是真实值：区间来自压缩
// 结果的真实字段，消息来自压缩**前**的真实日志；区间筛选口径与
// `autoCompact` 自己选 items 的口径一致（compactauto.go:52 的同一个判断）。
//
// 极端并发下（读完快照之后日志又被追加，导致快照里找不到这段区间）会估成 0：
// 宁可报一个明确的 0，也不去别的区间借一个数字来充数。
func compactShadowedTokens(before *v4.Log, from, to int64) int {
	if before == nil {
		return 0
	}
	items := make([]session.SurfaceMsg, 0)
	for _, m := range session.Surface(before) {
		if m.Seq >= from && m.Seq <= to {
			items = append(items, m)
		}
	}
	if len(items) == 0 {
		return 0
	}
	return (&session.Meter{}).Estimate(items)
}

// compactSummarySeq 读回刚落盘的那条 `compaction/summary` 帧的 seq。
//
// 读的是**日志**而不是上一次读的快照：摘要帧是这次压缩刚追加的，快照里没有。
// autoCompact 一次只追加一组 compaction/start → summary → end，所以"最后一条
// compaction/summary"就是本次这条。读不到（文件打不开等）时返回 false，由调用方
// 决定退回"不带 sourceEventSeq"——压缩**已经成功了**，不能因为读不到一个可选
// 字段就把整条命令报成失败，那样用户会重压一次已经压过的历史。
func compactSummarySeq(path string) (int64, bool) {
	after, err := v4.ReadFile(path)
	if err != nil {
		return 0, false
	}
	var seq int64
	found := false
	for _, ev := range after.Events {
		if ev.Type == "compaction/summary" {
			seq, found = ev.Seq, true
		}
	}
	return seq, found
}

// compactFailureText 把宿主压缩失败映射成官方 `ManualCompactionError` 的文案。
//
// # 能真的区分的三条
//
//   - cancelled：这次调用被取消。官方先看 `signal.aborted` 再看错误类型；宿主的
//     ctx 一路传进摘要调用（autoCompact → compact.SummarizeWithRetry →
//     chat.Stream），所以 `ctx.Err()` 非空就是"这次压缩确实被取消了"。
//   - busy：会话写锁被占（internal/session/history.go 的 sessionWriterBusy）。
//     在宿主这条路径上，"现在压不了"的**唯一**信号就是这个：另一个 writer 正持
//     有这份会话——可能是另一个进程，也可能是本进程里正跑着的一轮/一次压缩。
//     官方 busy 文案覆盖的正是这两种成因（active compaction / agent not idle）。
//     同包已有先例：serve_worktree_api.go:154 也是按这段错误文本判"会话忙"。
//   - summary：摘要没生成出来。宿主把这一类包成"生成摘要失败"
//     （internal/compact/compact.go 与 compactauto.go:61），或者干脆是内容为空
//     被拒（internal/session/compact.go:88 的"摘要为空"）。
//
// # 不映射的三条（不是漏做，是映射会撒谎）
//
//   - changed（"选中的历史在替换前变了"）：宿主的 autoCompact 压缩前读一次日志、
//     压缩时直接用那份快照（compactauto.go:38/68），**没有**在拿到写锁后重读比对
//     "还是不是同一份"。这个成因在宿主里既不会发生，也无从判定——映射成它等于
//     编一个没发生过的原因。
//   - commit（"没干净收尾，历史可能已经变了"）与 persistence（"压完了但存不下"）：
//     两者只在 `session.Compact` 追加中途失败、或 `Log.Close()` 失败时出现，而
//     autoCompact 把这两步的错误**原样上抛、不加任何标记**（compactauto.go:72/76）。
//     从错误本身分不出是哪一步，硬猜会把"摘要失败"报成"没存下"——那比直说
//     "压失败了、原因是 X"更坏。
//
// 所以其余一律回一条说清**真实原因**的错误文案（官方封闭联合里没有对应 code）。
func compactFailureText(ctx context.Context, err error) string {
	if ctx != nil && ctx.Err() != nil {
		return officialCompactErrorCancelled
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return officialCompactErrorCancelled
	}
	text := ""
	if err != nil {
		text = err.Error()
	}
	switch {
	case strings.Contains(text, "正被另一个进程写入"):
		return officialCompactErrorBusy
	case strings.Contains(text, "生成摘要失败"),
		strings.Contains(text, "摘要为空"),
		strings.Contains(text, "没有可摘要的消息"):
		return officialCompactErrorSummary
	default:
		return fmt.Sprintf("压缩失败：%s", text)
	}
}

// appendCommandLifecycle 把一帧命令生命周期写进会话 v4 日志，返回帧上的 seq。
//
// 官方 `appendLifecycle` 是"log-only 直接追加、没有 turn 包裹"。这里同样：
// 只追加这一帧，不新开 turn、不强制 flush（持久化在普通检查点收）。
func appendCommandLifecycle(src *sessionSource, sessionID, eventType string, data any) (int64, error) {
	if src == nil {
		return 0, fmt.Errorf("宿主没有接会话来源（sessionSource 为 nil）")
	}
	path, err := src.resolve(sessionID)
	if err != nil {
		return 0, err
	}
	var lastErr error
	for attempt := 0; attempt < commandLifecycleRetries; attempt++ {
		if attempt > 0 {
			// 写锁持有时间在微秒级；退避只是为了给并发的 turn 收尾让路。
			time.Sleep(time.Duration(attempt) * time.Millisecond)
		}
		commandLifecycleMu.Lock()
		logw, err := session.OpenSessionAt(path)
		if err != nil {
			commandLifecycleMu.Unlock()
			lastErr = err
			continue
		}
		appendErr := logw.Append(eventType, data)
		seq := logw.Seq()
		closeErr := logw.Close()
		commandLifecycleMu.Unlock()
		if appendErr != nil {
			return 0, fmt.Errorf("追加 %s 失败: %w", eventType, appendErr)
		}
		if closeErr != nil {
			return 0, fmt.Errorf("关闭 %s 写入失败: %w", eventType, closeErr)
		}
		return seq, nil
	}
	return 0, fmt.Errorf("追加 %s 失败（写锁一直抢不到）: %w", eventType, lastErr)
}
