package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dshgo/codecs/v4"
	"dshgo/internal/llm"
	"dshgo/internal/spi"
)

// Log 是会话写入端：canonical 事件 → v4 帧。
//
// 它只做两件事：分配 seq、把 payload 落成一行 JSON。帧怎么切、什么时候 fsync 由 codec 负责。
type Log struct {
	w        *v4.Writer
	Path     string
	seq      int64
	now      func() time.Time
	lockPath string // 非空时 Close 要把它删掉
}

// CreateSession 在 <home>/sessions/<projectKey>/<sessionId>/ 下新建会话文件。
func CreateSession(home, cwd, id, agentPreset string) (*Log, error) {
	return createSession(home, cwd, id, agentPreset, time.Now().UnixMilli())
}

// createSession 是 CreateSession 的实际实现，创建时间由调用方给。
//
// 单独拆出来的原因只有一条：**导入历史**（`chatimport`）的 header.createdAt
// 必须是导出里第一条事件的时间，而不是"现在"。官方导入路径同样显式传
// `createdAt`（`dsh-deepseek-chat-import/src/importer.ts:562` 的
// `{version, id, createdAt: conversion.createdAt, cwd, isSeeded: false}`），
// 于是导入的会话在侧栏里按它原本发生的时间排序，而不是全挤在导入那一刻。
func createSession(home, cwd, id, agentPreset string, createdAtMS int64) (*Log, error) {
	if id == "" {
		return nil, fmt.Errorf("session id 不能为空")
	}
	seg, err := v4.EncodeSegment(id)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, "sessions", v4.ProjectKey(cwd), seg)
	path := filepath.Join(dir, "session.v4.jsonl.zstd")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		// 最常见的原因：--home 指到了一个**文件**上（或者父目录被文件占了）。
		// 原始错误是 `mkdir tmpfile: The system cannot find the path specified.`
		// ——看不出是"作业目录本身有问题"还是"权限不够"。
		return nil, fmt.Errorf("建会话目录失败（%s）: %w；确认 --home 指向一个目录、且可写", dir, err)
	}
	h := v4.Header{
		Type:            "session",
		Version:         4,
		ID:              id,
		CreatedAt:       createdAtMS,
		Cwd:             cwd,
		IsSeeded:        false,
		DelegationDepth: 0,
		AgentPreset:     agentPreset,
	}
	w, err := v4.Create(path, h)
	if err != nil {
		return nil, err
	}
	return &Log{w: w, Path: path, now: time.Now}, nil
}

// Append 追加一条事件并立刻提交一帧（M2 阶段一事件一帧；批窗口 200ms 是优化项）。
//
// 写盘之前先拦两种"写进去也读不出来"的事件：
//   - 没有 type：surface 只认 5 类事件，其余一律跳过，等于写了一条永远没人看的数据；
//   - 负载是 null：文件里多一行、seq 也动了，但投影拿不到任何字段。
//
// 两者都不会报错，只会让日志"看起来成功、实际什么也没发生"——所以宁可在
// 写之前拒绝，也不留一条读不回来的记录。
func (l *Log) Append(eventType string, data any) error {
	if strings.TrimSpace(eventType) == "" {
		return fmt.Errorf("事件类型不能为空：写进去也没有任何投影能认出它（seq 未推进、文件未改动）")
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("事件 %s 序列化失败: %w", eventType, err)
	}
	if data == nil || string(raw) == "null" {
		return fmt.Errorf("事件 %s 的负载是 null：落盘后投影读不到任何字段"+
			"（seq 未推进、文件未改动）；给一个真实的数据结构再写", eventType)
	}
	ev := v4.Event{
		Type: eventType,
		Seq:  l.seq,
		Time: l.now().UnixMilli(),
		Data: raw,
	}
	if err := l.w.Append(ev); err != nil {
		return err
	}
	l.seq++
	return nil
}

// Close 关闭文件。
func (l *Log) Close() error {
	err := l.w.Close()
	if l.lockPath != "" {
		removeErr := os.Remove(l.lockPath)
		if removeErr != nil && !os.IsNotExist(removeErr) {
			err = errors.Join(err, fmt.Errorf("释放会话写锁 %s: %w", l.lockPath, removeErr))
		} else {
			l.lockPath = ""
		}
	}
	return err
}

// Seq 返回已写事件数。
func (l *Log) Seq() int64 { return l.seq }

// ---------- 事件构造器：形状抄自真实语料（research/21 与 $DSH_HOME/sessions 实测） ----------

// NewID 生成一个 uuidv4 形状的 id（会话内事件 id 用）。
func NewID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	s := hex.EncodeToString(b[:])
	return s[0:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:32]
}

// TextBlock 是内容块。
func TextBlock(text string) map[string]any {
	return map[string]any{"type": "text", "text": text}
}

// ReasoningBlock 是 assistant 消息里的思考块（H6）。
//
// 为什么放在 content 里而不是单独一种事件：v4 的 type 词汇表里没有
// `assistant/reasoning`，而 content 块本来就是"一次回答由哪些块组成"的答案。
// 单独造事件还要改词汇表与两侧折叠逻辑，收益抵不上风险。
func ReasoningBlock(text string) map[string]any {
	return map[string]any{"type": "reasoning", "text": text}
}

// ToolCallBlock 是 assistant 消息里的工具调用块。
func ToolCallBlock(id, name, arguments string) map[string]any {
	return map[string]any{"type": "tool-call", "id": id, "name": name, "arguments": arguments}
}

// UserMessage 构造 user/message 的 data。
func UserMessage(blocks []spi.Block) map[string]any {
	return map[string]any{
		"content":   blocks,
		"source":    map[string]any{"kind": "user"},
		"role":      "user",
		"id":        NewID(),
		"surfaceOp": "append",
	}
}

// UserContentMessage 保留完整有序内容块及附件/引用身份。旧 UserMessage 不改，
// 因而纯文本 CLI/ACP 的线上字节与调用语义完全不变。
func UserContentMessage(blocks []ContentBlock) map[string]any {
	return map[string]any{
		"content":   blocks,
		"source":    map[string]any{"kind": "user"},
		"role":      "user",
		"id":        NewID(),
		"surfaceOp": "append",
	}
}

// ToolCallEvent 构造 tool/call 的 data（arguments 是 JSON 字符串）。
func ToolCallEvent(turn, step int, callID, name, arguments string) map[string]any {
	return map[string]any{
		"turn":      turn,
		"step":      step,
		"callId":    callID,
		"name":      name,
		"arguments": arguments,
	}
}

// ToolResultEvent 构造 tool/result 的 data。
// ToolResultEventWithView 在普通 tool/result 上加一份客户端渲染声明（H3）。
//
// 为什么单独一个函数而不改签名：ToolResultEvent 有 4 个调用点（含 3 个测试），
// 改签名会让所有调用点跟着改；而"多带一个可选的 view"本来就是增量的语义。
func ToolResultEventWithView(turn, step int, callID string, blocks []spi.Block, isError bool, cwd string, sourceSeqs []int64, view json.RawMessage) map[string]any {
	ev := ToolResultEvent(turn, step, callID, blocks, isError, cwd, sourceSeqs)
	if len(view) > 0 {
		ev["view"] = view
	}
	return ev
}

func ToolResultEvent(turn, step int, callID string, blocks []spi.Block, isError bool, cwd string, sourceSeqs []int64) map[string]any {
	if sourceSeqs == nil {
		sourceSeqs = []int64{}
	}
	return map[string]any{
		"turn": turn,
		"step": step,
		"message": map[string]any{
			"role":       "tool",
			"source":     map[string]any{"kind": "tool", "callId": callID},
			"toolCallId": callID,
			"content":    blocks,
			"isError":    isError,
			"id":         NewID(),
		},
		"meta":            map[string]any{"cwd": cwd},
		"sourceEventSeqs": sourceSeqs,
		"surfaceOp":       "append",
	}
}

// AssistantMessageEvent 构造 assistant/message 的 data。
//
// 已知差异：`stream` 里的 replayState 等回放细节暂写空，等流式记录层落地再补。
//
// 消息身份（ID-PERSIST）：`message.id` 在**这里**分配一次并随事件落盘。
//
// 为什么必须写在这里而不是读的时候算：v4 是 append-only，读路径不许写盘，
// 于是"读时生成"只能每次现算一个值——同一条消息换一次读就换一个身份，
// 界面挂在它上面的点赞/评论立刻对不上。官方也是建消息时分配一次的
// （`@deepseek-ai/dsh-llm` 的 `src/message.ts:139-141` `MessageBase.id`，
// `src/message.ts:226` `createMessage` 用 `randomUUID()` 赋值），
// 所以这里给一个等价的持久 uuid，写进去就永不变。
func AssistantMessageEvent(turn, step int, blocks []map[string]any, finish string, usage map[string]any) map[string]any {
	reason := map[string]any{"kind": finishReasonKind(finish)}
	data := map[string]any{
		"turn":    turn,
		"step":    step,
		"message": map[string]any{"role": "assistant", "content": blocks, "id": NewID()},
		"stream": []map[string]any{{
			"type":   "finish",
			"reason": reason,
		}},
		"surfaceOp": "append",
	}
	if usage != nil {
		data["usage"] = usage
	}
	return data
}

func finishReasonKind(finish string) string {
	switch finish {
	case "stop", "":
		return "completed"
	case "tool_calls":
		return "tool-calls"
	case "length":
		return "max-tokens"
	default:
		return finish
	}
}

// TurnEndKind 把 agent 的 finish reason 映射成 v4 的 `turn/end.reason.kind`。
//
// 词汇表（research/21 §turn/end）是：completed / aborted / blocked / error /
// max-tokens / interrupted / forked。
//
// 为什么需要它：`turn/end` 原来在三个入口都**恒写 "completed"**。于是一个被
// max_tokens 截断的回合（模型只说了半句）在日志里也是「完成」——客户端读
// turn/end 得到的结论是"这一轮做完了"，而 assistant/message 里的
// finish=max-tokens 被它盖过去了。这正是"有产出、但结论是假的、没人报错"。
//
// awaiting_user 仍记 completed：它不是失败，只是把控制权交回人；
// v4 词汇表里没有更贴切的 kind，硬造一个会让客户端解不动。
//
// 注意：这个函数只负责**模型收工**那一路的映射。失败路径（模型报错、步数用满、
// 取消、panic）由调用方的出口统一补 `turn/end{kind:"error"/"aborted"}`——
// 见 cmd/dshd/main.go 的 turnCloser。失败绝不许从这里拿到 completed。
func TurnEndKind(finish string) string {
	switch finish {
	case "length":
		return "max-tokens"
	case "aborted", "blocked", "error", "interrupted", "forked", "max-tokens":
		return finish // 已经是 v4 的 kind，原样写
	default:
		return "completed"
	}
}

// SystemMessageData 构造 system/message 的 data（角色=system，首条受保护）。
//
// `message.id` 同 AssistantMessageEvent：写时分配、随事件落盘。官方 v4 明确要求
// 非空 system message id（`@deepseek-ai/dsh-session-format-v3-to-v4` 的
// `src/system-message.ts:43`：`string(message['id'], 'system message id', true)`），
// 缺了它官方校验器会认为这条 v4 记录不合法。
func SystemMessageData(turn, step int, text string) map[string]any {
	return map[string]any{
		"turn": turn,
		"step": step,
		"message": map[string]any{
			"role":    "system",
			"content": []map[string]any{TextBlock(text)},
			"id":      NewID(),
		},
		"surfaceOp": "append",
	}
}

// TurnStart / TurnEnd 的 data。
func TurnStartData(turn int) map[string]any { return map[string]any{"turn": turn} }

func TurnEndData(turn int, kind, detail string) map[string]any {
	r := map[string]any{"kind": kind}
	if detail != "" {
		switch kind {
		case "error":
			r["error"] = map[string]any{"name": "Error", "message": detail}
		case "aborted":
			r["reason"] = detail
		}
	}
	return map[string]any{"turn": turn, "reason": r}
}

// AssistantBlocks 是「一次回答 → v4 content 块」的**唯一一份**铺块逻辑：
// 思考 → 正文 → 调用。serve、ACP 的写盘 sink、以及 chat-import 的转写都调它。
//
// 参数取三样零件而不是 *llm.Response，是因为 ACP 那条路手里只有
// `agent.AssistantTurn`（同一个模型的同一份产物，只是它在 agent 层的形状）。
// 为它写第二份铺块，就是单点实现登记表要消灭的那种"两处各自维护、迟早漂"。
//
// 为什么思考块放正文之前：折叠出来就是"先想了什么、再回答什么"；顺序反了，
// 读日志的人会以为模型先说了话又倒回去想。
//
// 2026-11-18 从 cmd/dshd/serve_turn.go 搬到这里：它原来在 `package main` 里，
// 库代码（internal/chatimport）**根本 import 不到**，于是只能手写块 map ——
// 而单点实现守卫正确地禁止了手写。单点放在够不着的地方，等于逼人写第二份。
func AssistantBlocks(text, reasoning string, toolCalls []llm.ToolCall) []map[string]any {
	blocks := make([]map[string]any, 0, 2+len(toolCalls))
	if strings.TrimSpace(reasoning) != "" {
		blocks = append(blocks, ReasoningBlock(reasoning))
	}
	if strings.TrimSpace(text) != "" {
		blocks = append(blocks, TextBlock(text))
	}
	for _, tc := range toolCalls {
		blocks = append(blocks, ToolCallBlock(tc.ID, tc.Function.Name, tc.Function.Arguments))
	}
	return blocks
}
