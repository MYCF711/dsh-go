// 本文件是 durable 事件层：把一帧上下文落成 v4 `user/message`，以及从已落盘事件
// **重放**出 FrameState（跨轮状态 + resume reconcile 的唯一真值来源）。
//
// 官方在同一件事上用的是 `createUserMessage` + `session.append`
// （`src/state.ts:85-102`、`src/index.ts:194-203`）：
//
//	createUserMessage({
//	  content: [{ type: 'text', text }],
//	  source: { kind: 'agent-instructions', form: 'instructions',
//	            baseline: true, baselineIdentity, changes },
//	})
//
// 落盘形状逐字照抄：`user/message` 的 data 是
// `{ content:[{type:'text',text}], source:{kind,form,baseline?,baselineIdentity?,changes},
//
//	role:'user', id, surfaceOp:'append' }`。
//
// # 为什么重放而不是缓存
//
// 官方 `visibleInstructionChanges`（`src/state.ts:140-161`）每次 reconcile 都**重放整份
// surface**。这不是性能取舍，是**正确性**要求：进程重启/断线重连之后，插件没有任何
// 内存状态可用，唯一能回答"现在生效的是哪些指令"的地方就是日志本身。本包照抄这条：
// `FrameStateFrom` 就是重放函数，`Runner.ComposeAndCommit` 每次现读。
//
// # 与官方的一处形状差异（写在这里，不埋在报告里）
//
// 官方基线帧的渲染文本里**含**总说明（`AGENT_INSTRUCTIONS_INTRO`，`src/render.ts:18-20`）
// 与 `<system-reminder>` 帧。宿主的基线实体由 `prompt.LoadInstructionChain` 产出，
// 它已经带了帧与总说明（同一条链，见 instructions.go 的 `buildInstructionFrame`），
// 所以这里**不再补一层帧**——两层帧会让模型看到嵌套的 `<system-reminder>`，而
// 官方转义规则（`escapeInstructionFrameBody`）本来就是为了禁止这种嵌套。
package ainstr

import (
	"encoding/json"
	"errors"

	"dshgo/internal/session"
)

// FrameEvent 是一帧上下文在 v4 里的落点：事件类型 + 载荷。
//
// 只给"类型 + data"，不碰 writer：v4 只有一个写入者（`session.Log`），本包不另造一个。
type FrameEvent struct {
	Type string
	Data map[string]any
}

// EventTypeUserMessage 是唯一的落点类型（官方 `session.append('user/message', …)`）。
const EventTypeUserMessage = "user/message"

// FrameEventOf 把一帧落成 v4 `user/message` 载荷（官方形状，逐字）。
//
// `surfaceOp` 写 `append`：这条消息**追加**到模型可见面上（官方 inbox 语义是
// "插在本步第一条之后"，落到日志里等价于"按时间顺序追加"，因为它总是紧跟
// 认领它的那条用户消息之后写入）。
func FrameEventOf(frame Frame) FrameEvent {
	// source.Changes 是权威（官方 createUserMessage 的 source 就是唯一载荷）；
	// frame.Changes 只是同一份数据的便捷字段，没给时回落。
	changesIn := frame.Source.Changes
	if changesIn == nil {
		changesIn = frame.Changes
	}
	changes := make([]any, 0, len(changesIn))
	for _, change := range changesIn {
		item := map[string]any{
			"action": change.Action,
			"scope":  change.Scope,
			"path":   change.Path,
		}
		if change.Digest != "" {
			item["digest"] = change.Digest
		}
		changes = append(changes, item)
	}
	source := map[string]any{
		"kind":    SourceKind,
		"form":    SourceForm,
		"changes": changes,
	}
	if frame.Source.Baseline {
		source["baseline"] = true
		source["baselineIdentity"] = frame.Source.BaselineIdentity
	}
	return FrameEvent{
		Type: EventTypeUserMessage,
		Data: map[string]any{
			"content":   []any{map[string]any{"type": "text", "text": frame.Text}},
			"source":    source,
			"role":      "user",
			"id":        session.NewID(),
			"surfaceOp": "append",
		},
	}
}

// FrameEventOfText 是"只要文本 + source"的便捷版（测试与纯投影用）。
//
// 它与 `FrameEventOf` **同一份形状**：两处各写一份 map 就是"同一条规则两份实现"，
// 所以这里只是把 Frame 组出来后调同一个构造函数。
func FrameEventOfText(text string, source Source) FrameEvent {
	frame := Frame{
		Text:     text,
		Source:   source,
		Baseline: source.Baseline,
		Identity: source.BaselineIdentity,
		Changes:  source.Changes,
	}
	return FrameEventOf(frame)
}

// FrameFromData 从一条已落盘的 `user/message` 载荷里读回"是不是我们的帧"，以及它的
// text / source。不是指令帧时 ok=false（**不报错**：日志里绝大多数 user/message
// 是用户自己说的话，把它们当错误会让每轮都红）。
func FrameFromData(data json.RawMessage) (text string, source Source, ok bool) {
	var payload struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Source *struct {
			Kind             string          `json:"kind"`
			Form             string          `json:"form"`
			Baseline         bool            `json:"baseline"`
			BaselineIdentity string          `json:"baselineIdentity"`
			Changes          json.RawMessage `json:"changes"`
		} `json:"source"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", Source{}, false
	}
	if payload.Source == nil || payload.Source.Kind != SourceKind {
		return "", Source{}, false
	}
	changes, err := decodeChanges(payload.Source.Changes)
	if err != nil {
		// 一条坏 change 不让整帧作废（官方 workspaceInstructionChanges 逐条跳过，
		// 见 state.go 的 validChange）；但 source.kind 已经确认是我们，所以仍算"我们的帧"。
		changes = nil
	}
	return joinTextBlocks(payload.Content), Source{
		Kind:             payload.Source.Kind,
		Form:             payload.Source.Form,
		Baseline:         payload.Source.Baseline,
		BaselineIdentity: payload.Source.BaselineIdentity,
		Changes:          changes,
	}, true
}

// decodeChanges 是官方 `workspaceInstructionChanges`（`src/state.ts:116-131`）的 Go 版：
// 逐条校验 action/scope/path，坏条目**跳过**而不是整帧作废。
func decodeChanges(raw json.RawMessage) ([]Change, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, errors.New("agent-instructions: changes 不是数组")
	}
	changes := make([]Change, 0, len(items))
	for _, item := range items {
		var change Change
		if err := json.Unmarshal(item, &change); err != nil {
			continue
		}
		if validChange(change) {
			changes = append(changes, change)
		}
	}
	return changes, nil
}

func joinTextBlocks(blocks []struct {
	Type string `json:"type"`
	Text string `json:"text"`
}) string {
	out := ""
	for _, block := range blocks {
		if block.Type != "text" {
			continue
		}
		if out != "" {
			out += "\n"
		}
		out += block.Text
	}
	return out
}

// FrameStateFrom 从按落盘顺序给出的事件**重放**出跨轮状态。
//
// 官方 `visibleInstructionChanges`（`src/state.ts:140-161`）+ `visibleBaselineSource`
// （`src/index.ts:47-64`）的合成物：
//
//   - `Visible` 是所有 change 按时间顺序折叠（同 scope 后者覆盖前者）；
//   - `Baseline` 是**最后一条**基线帧；
//   - `LastText` / `LastSource` 是**最后一条**指令帧的载荷（alreadySupplied 用）。
//
// `ExcludedBaselineScopes` **不在这里推**：它需要"基线 scope 全集"，而那要有 cwd
// 与配置（见 runner.go 的 `ExcludedScopesFrom` + `Compose`）。重放只负责日志里
// 写着的事实，不猜环境。
//
// 宿主侧请走 `FrameStateFromLog`（session.go）；本函数是纯数据入口（测试用），
// 两者共用同一份重放实现。
func FrameStateFrom(events []eventPair) FrameState {
	return frameStateFrom(events)
}

// collapseChanges 把时间顺序的 change 序列折叠成"同 scope 最后一条生效"。
//
// 官方是 Map 覆盖（`visible.set(change.scope, change)`），这里保持同一个语义，
// 但**保留首次出现的顺序**：reconcile 的 scope 遍历要有稳定顺序
// （见 reconcile.go 里那段排序注释）。
func collapseChanges(changes []Change) []Change {
	index := make(map[string]int, len(changes))
	out := make([]Change, 0, len(changes))
	for _, change := range changes {
		if !validChange(change) {
			continue
		}
		if at, ok := index[change.Scope]; ok {
			out[at] = change
			continue
		}
		index[change.Scope] = len(out)
		out = append(out, change)
	}
	return out
}
