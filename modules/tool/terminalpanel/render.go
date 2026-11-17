package terminalpanel

// 官方 `tool-terminal/src/render.ts` 的逐条移植：五个 agent 终端工具的**模型可见正文**。
//
// 为什么必须逐字移植：这些正文进 Session 日志与模型请求，UI 侧按同一形状解析
// （`dsh-client-ui-tool/src/client/tool/models/control-details-model.ts:232` 认的就是
// `^started terminal session …` 那一族），翻译或"顺手改顺眼"都会让官方形状立刻失真。
//
// 逐条对应：
//
//	TRUNCATED            render.ts:50
//	retain / byteLength  :52-60（TextRetainer 的 head/tail）
//	fitWithSuffix        :62-66
//	boundBodyWithSuffix  :75-85
//	boundTerminalText    :93-98
//	renderSend           :120-131
//	renderRead           :150-158
//	renderList           :166-177
//	signal / close 的正文 index.ts:356、:380-385
//
// 唯一的本仓差异：`retain(..., 'head')` 在官方按 grapheme 边界截（TextRetainer），
// 本仓没有 grapheme 分段器，按 UTF-8 rune 边界截（不自造依赖）；差别只在组合字符/
// ZWJ 序列上可能出现半个簇。

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"dshgo/internal/terminal"
)

// retainHead 取文本前 maxBytes 字节且不切断 UTF-8 字符（官方 `TextRetainer` 的 head 模式）。
func retainHead(text string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(text) <= maxBytes {
		return text
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

// retainTail 取文本后 maxBytes 字节且不切断 UTF-8 字符（官方 tail 模式）。
func retainTail(text string, maxBytes int) string {
	tail, _ := terminal.NewestBytes(text, maxBytes)
	return tail
}

// fitWithSuffix 是官方 `fitWithSuffix`（`render.ts:62-66`）：保正文尾部 + 必带后缀。
func fitWithSuffix(content, suffix string, maxBytes int) string {
	if len(suffix) >= maxBytes {
		return retainTail(suffix, maxBytes)
	}
	return retainTail(content, maxBytes-len(suffix)) + suffix
}

// fitWithPrefix 是官方 `fitWithPrefix`（`render.ts:68-73`）：保前缀 + 保正文尾部 + 截断标记。
func fitWithPrefix(prefix, content string, maxBytes int) string {
	fixed := prefix + truncatedMarker
	if len(fixed) >= maxBytes {
		return retainHead(fixed, maxBytes)
	}
	return prefix + retainTail(content, maxBytes-len(fixed)) + truncatedMarker
}

// boundBodyWithSuffix 是官方 `boundBodyWithSuffix`（`render.ts:75-85`）。
func boundBodyWithSuffix(content, metadata string, upstreamTruncated bool, maxBytes int) string {
	suffix := metadata
	if upstreamTruncated {
		suffix += truncatedMarker
	}
	complete := content + suffix
	if len(complete) <= maxBytes {
		return complete
	}
	return fitWithSuffix(content, metadata+truncatedMarker, maxBytes)
}

// renderSendText 与官方 `renderSend` 逐字对齐（`render.ts:120-131`）：
//
//	<viewport 或 "(no new output)">
//	[wait: <waitReason>]
//	[session: running | exited code=<c|null> signal=<s|null>]
func renderSendText(viewport, waitReason string, status terminal.AgentTerminalStatus, truncated bool, maxBytes int) string {
	output := viewport
	if output == "" {
		output = "(no new output)"
	}
	session := "running"
	if status.Kind != "running" {
		session = "exited code=" + nullableInt(status.ExitCode) + " signal=" + nullableString(status.Signal)
	}
	return boundBodyWithSuffix(output, "\n[wait: "+waitReason+"]\n[session: "+session+"]", truncated, maxBytes)
}

// renderReadText 与官方 `renderRead` 逐字对齐（`render.ts:150-158`）：
//
//	<text 或 "(no retained output)">
//	[lines: <lineBegin>-<lineEnd> of <totalLines>]
func renderReadText(page terminal.ScreenPage, maxBytes int) string {
	output := page.Text
	if output == "" {
		output = "(no retained output)"
	}
	suffix := "\n[lines: " + strconv.Itoa(page.LineBegin) + "-" + strconv.Itoa(page.LineEnd) + " of " + strconv.Itoa(page.TotalLines) + "]"
	return boundBodyWithSuffix(output, suffix, page.Truncated, maxBytes)
}

// renderSignalText 与官方 signal 的渲染逐字对齐（`index.ts:356`）：
// `delivered <signal> to foreground process group <targetPgid>`。
func renderSignalText(signal string, targetPgid int) string {
	return "delivered " + signal + " to foreground process group " + strconv.Itoa(targetPgid)
}

// renderCloseText 与官方 close 的渲染逐字对齐（`index.ts:380-385`）。
func renderCloseText(sessionID string, closed bool) string {
	if closed {
		return "closed terminal session " + sessionID
	}
	return "terminal session " + sessionID + " was already closing"
}

// renderListText 与官方 `renderList` 逐字对齐（`render.ts:166-177`）：
//
//	(no terminal sessions)
//	<sessionId>[ (<name>)][ [<type>]] <running|exited code=<c|null> signal=<s|null>>[ pid=<pid>]
//
// 注意官方顺序是 `${sessionId}${name} [${type}] ${status}${pid}`：type 恒在方括号里，
// pid 恒在状态之后、且只有拿得到时才出现。
func renderListText(sessions []terminal.AgentTerminalSnapshot, maxBytes int) string {
	if len(sessions) == 0 {
		return "(no terminal sessions)"
	}
	lines := make([]string, 0, len(sessions))
	for _, session := range sessions {
		var sb strings.Builder
		sb.WriteString(session.SessionID)
		if session.Name != "" {
			sb.WriteString(" (")
			sb.WriteString(session.Name)
			sb.WriteString(")")
		}
		sb.WriteString(" [")
		sb.WriteString(session.Type)
		sb.WriteString("] ")
		if session.Status.Kind == "running" {
			sb.WriteString("running")
		} else {
			sb.WriteString("exited code=")
			sb.WriteString(nullableInt(session.Status.ExitCode))
			sb.WriteString(" signal=")
			sb.WriteString(nullableString(session.Status.Signal))
		}
		if session.PID > 0 {
			sb.WriteString(" pid=")
			sb.WriteString(strconv.Itoa(session.PID))
		}
		lines = append(lines, sb.String())
	}
	return boundBodyWithSuffix(strings.Join(lines, "\n"), "", false, maxBytes)
}

// nullableInt 把可空的整数渲染成官方那两种形态（数字 / `null`）。
func nullableInt(v *int) string {
	if v == nil {
		return "null"
	}
	return strconv.Itoa(*v)
}

// nullableString 把可空的信号名渲染成官方那两种形态（字符串 / `null`）。
func nullableString(v *string) string {
	if v == nil {
		return "null"
	}
	return *v
}
