package session

import (
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
)

// 运行时上下文提醒记录（官方 rc.2 `runtime-context-reminders/guide.md:12-14`）。
//
// 官方把运行时事实落成 **durable `user/message`**，`source.kind = 'runtime-context'`，
// 正文包 `<system-reminder>`；本包负责这一条记录的形状与刷新判定。
//
// 逐字依据 `dsh-agent-loop/lib/index.js`：
//   - `:203-215`：`SOURCE = "runtime-context"`、`CLEARED` 文案、`sectionsOf()`（只有
//     `source.form === "snapshot"` 才有结构化 sections）、`textOf()`；
//   - `:337-357`：`project()`（intro 两版文案、body 拼接、`</system-reminder` 转义、
//     `form:"snapshot"` + `sections`，以及"没有变化就不产出"的两条早退）。
//
// 三条语义（guide 的 Migration 第 2 条）：
//  1. 首快照只介绍当前会话，**不**声称替换早前上下文；后续快照明确替换（含摘要里保留的）；
//  2. clear 标记 = `form: 'snapshot'` + `sections: []` + 固定 CLEARED 文案；
//  3. 没有 `source.sections` 的旧记录在**下一次被接收的请求**上刷新**一次**：
//     新增一条消息（append），**不改写既有请求前缀**；已记录 sections 未变的旧记录不为措辞刷新。
const (
	// RuntimeContextSourceKind 是官方 `source.kind`。
	RuntimeContextSourceKind = "runtime-context"
	// RuntimeContextFormSnapshot 是官方 `source.form`（结构化快照的唯一取值）。
	RuntimeContextFormSnapshot = "snapshot"

	// RuntimeContextInitialIntro 是首快照的介绍句（官方 `:343`：retained === undefined 分支）。
	RuntimeContextInitialIntro = "The following information describes your current session:"
	// RuntimeContextUpdateIntro 是后续快照的介绍句：明确替换，含摘要里保留的上下文。
	RuntimeContextUpdateIntro = "The following information describes your current session. It replaces earlier versions of this context, including any retained in conversation summaries."
	// RuntimeContextClearedText 是 clear 标记的固定正文（官方 `:213` 的 CLEARED）。
	RuntimeContextClearedText = "<system-reminder>\nCurrent runtime context: none. Earlier runtime-context snapshots no longer apply.\n</system-reminder>"
)

// contextClosingTagPattern 匹配正文里出现的 `</system-reminder>`（含空白变体，忽略大小写）。
// 官方 `:344`：`/<\/system-reminder\s*>/gi` → `<\\${tag.slice(1)}`。
var contextClosingTagPattern = regexp.MustCompile(`(?i)</system-reminder\s*>`)

// ContextSnapshotSection 是官方 `ContextSnapshotSection`（`name` + `text`）。
type ContextSnapshotSection struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// RuntimeContextRetained 是"日志里已经留下的那条运行时上下文记录"的状态。
//
// `HasSections=false` 表示它是旧版本写下的记录（没有 `form:'snapshot'` 或没有
// `sections`）——按 guide 的要求，它需要在下一个请求上被刷新一次。
type RuntimeContextRetained struct {
	Seq         int64
	HasSections bool
	Sections    []ContextSnapshotSection
}

// RuntimeContextReminder 决定"这一次请求要不要新增一条运行时上下文提醒"。
//
// 返回 `(payload, true)` 表示要写一条 durable `user/message`（调用方按官方形状落盘：
// `content` 一个 text 块 + `source{kind,form,sections}` + `surfaceOp:"append"`）；
// 返回 `(nil, false)` 表示无需变化。
//
// 两条官方的"不需要就不写"早退（`:339-341`）：
//   - 从没有过快照 + 本次 sections 也为空 → 不写（否则每条会话都会多一条空提醒）；
//   - 已记录 sections 与本次深比较相等 → 不写（不为措辞刷新）。
func RuntimeContextReminder(retained *RuntimeContextRetained, sections []ContextSnapshotSection) (map[string]any, bool) {
	if retained == nil && len(sections) == 0 {
		return nil, false
	}
	if retained != nil && retained.HasSections && reflect.DeepEqual(retained.Sections, sections) {
		return nil, false
	}
	text := RuntimeContextReminderText(retained, sections)
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
		"source": map[string]any{
			"kind":     RuntimeContextSourceKind,
			"form":     RuntimeContextFormSnapshot,
			"sections": sectionsPayload(sections),
		},
		"role":      "user",
		"surfaceOp": "append",
	}, true
}

// RuntimeContextReminderText 渲染提醒正文（官方 `:342-347`）。
//
// clear（sections 为空）用固定 CLEARED 文案；否则
// `<system-reminder>\n{intro}\n\n{body}\n</system-reminder>`，
// body 是各 section 的 text 以空行连接，闭合标签先转义。
func RuntimeContextReminderText(retained *RuntimeContextRetained, sections []ContextSnapshotSection) string {
	if len(sections) == 0 {
		return RuntimeContextClearedText
	}
	intro := RuntimeContextInitialIntro
	if retained != nil {
		// 旧记录（没有 sections）也算"已有快照"：它是被刷新，不是首次介绍。
		intro = RuntimeContextUpdateIntro
	}
	bodies := make([]string, 0, len(sections))
	for _, section := range sections {
		bodies = append(bodies, escapeContextClosingTags(section.Text))
	}
	return "<system-reminder>\n" + intro + "\n\n" + strings.Join(bodies, "\n\n") + "\n</system-reminder>"
}

// escapeContextClosingTags 把正文里的 `</system-reminder>` 转义成 `<\/system-reminder>`，
// 否则一条事实就能提前关掉提醒块（官方 `:344` 同一处理）。
func escapeContextClosingTags(text string) string {
	return contextClosingTagPattern.ReplaceAllStringFunc(text, func(tag string) string {
		return `<\/` + tag[2:]
	})
}

// sectionsPayload 把 sections 转成可落盘的 JSON 值；空 sections 必须是 `[]` 而不是 null
// （官方 clear 标记要求 `sections: []`）。
func sectionsPayload(sections []ContextSnapshotSection) []any {
	out := make([]any, 0, len(sections))
	for _, section := range sections {
		out = append(out, map[string]any{"name": section.Name, "text": section.Text})
	}
	return out
}

// RuntimeContextSections 从一条 durable `user/message` 载荷里读回结构化 sections。
//
// 只有 `source.kind === 'runtime-context'` 且 `source.form === 'snapshot'` 才算
// （官方 `sectionsOf()` 的判据）；旧记录没有 form/sections → ok=false，
// 调用方据此触发"刷新一次"。
func RuntimeContextSections(data json.RawMessage) ([]ContextSnapshotSection, bool) {
	var payload struct {
		Source struct {
			Kind     string                   `json:"kind"`
			Form     string                   `json:"form"`
			Sections []ContextSnapshotSection `json:"sections"`
		} `json:"source"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, false
	}
	if payload.Source.Kind != RuntimeContextSourceKind || payload.Source.Form != RuntimeContextFormSnapshot {
		return nil, false
	}
	if payload.Source.Sections == nil {
		// form 对但 sections 缺失：旧记录，当作"没有结构化 sections"。
		return nil, false
	}
	return payload.Source.Sections, true
}

// RuntimeContextRetainedFrom 从一条 `user/message` 载荷解析出 retained 状态。
//
// 这不是 accomodation：它是"刷新一次"判定的唯一入口——调用方只需拿日志里最新一条
// runtime-context 记录喂进来，`HasSections` 自动区分新旧记录。
func RuntimeContextRetainedFrom(seq int64, data json.RawMessage) *RuntimeContextRetained {
	sections, ok := RuntimeContextSections(data)
	if !ok {
		return &RuntimeContextRetained{Seq: seq}
	}
	return &RuntimeContextRetained{Seq: seq, HasSections: true, Sections: sections}
}
