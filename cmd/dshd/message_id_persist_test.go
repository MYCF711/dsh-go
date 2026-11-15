package main

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"testing"

	"dshgo/codecs/v4"
	"dshgo/internal/session"
	"dshgo/internal/spi"
)

// ID-PERSIST：assistant 的持久身份必须是**写时落盘**的 v4 `message.id`，不是位置。
//
// 缺陷原文（docs/handoff-2026-11-15.md §5 第 7 条）：GUI 的反馈只能拿 `v4:<seq>` 适配，
// 因为宿主没给 assistant 落持久 `messageId`。`v4:<seq>` 是信封序号（位置），官方给的是
// 消息自己的 `MessageId`（`@deepseek-ai/dsh-llm` 的 `MessageBase.id`，UUID，建消息时分配一次）。
//
// 这一组的判据只有一条：**身份从事件的 message.id 来，不从 seq / 正文 / 位置来**。

// messageIDUUIDv4 是官方 MessageId 的线上形状：`dsh-llm/src/message.ts:226`
// 用 `randomUUID()` 生成，即 RFC 4122 v4。
var messageIDUUIDv4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// wiredMessageID 是同一个形状在**落盘字节**里的样子（带 `"id":` 键名、不带锚点）。
var wiredMessageID = regexp.MustCompile(`"id":"[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}"`)

// assistantPersistedID 取 assistant/message 落盘 data 里的 `message.id`。
// 空串就是缺陷本身（旧实现根本没有这个字段），不是"恰好缺"。
func assistantPersistedID(t *testing.T, data map[string]any) string {
	t.Helper()
	msg, ok := data["message"].(map[string]any)
	if !ok {
		t.Fatalf("assistant/message 没有 message 对象：%v", data)
	}
	id, _ := msg["id"].(string)
	return id
}

// loadedByV4Seq 按持久 v4 信封序号找 load 投影出来的那条消息。
func loadedByV4Seq(t *testing.T, msgs []loadedMessage, seq int64) loadedMessage {
	t.Helper()
	for _, m := range msgs {
		if m.v4Seq != nil && *m.v4Seq == seq {
			return m
		}
	}
	t.Fatalf("load 里找不到 v4Seq=%d 的消息：%+v", seq, msgs)
	return loadedMessage{}
}

type loadedMessage struct {
	v4Seq     *int64
	messageID string
	transport int
}

func loadMessages(t *testing.T, home, cwd, sid string) []loadedMessage {
	t.Helper()
	msgs, err := newSessionSource(home, cwd).Messages(sid)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]loadedMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, loadedMessage{v4Seq: m.V4Seq, messageID: m.MessageID, transport: m.Seq})
	}
	return out
}

// TestAssistantMessageIDIsPersistedAtAppend 钉住"写时生成、落盘一次、读路径只投影"。
func TestAssistantMessageIDIsPersistedAtAppend(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	const sid = "message-id-persist"
	ids := make([]string, 0, 2)
	path := seedTurnSession(t, home, cwd, sid, func(lg *session.Log) error {
		for step := 1; step <= 2; step++ {
			// 两条**完全同文**的 assistant：身份不能由正文或先后顺序推导。
			data := session.AssistantMessageEvent(1, step,
				[]map[string]any{session.TextBlock("同文回答")}, "stop", nil)
			id := assistantPersistedID(t, data)
			if id == "" {
				t.Fatalf("assistant/message 没有持久 message.id：%v", data)
			}
			if !messageIDUUIDv4.MatchString(id) {
				t.Fatalf("assistant message.id=%q 不是官方 MessageId 的 uuidv4 形状", id)
			}
			ids = append(ids, id)
			if err := lg.Append("assistant/message", data); err != nil {
				return err
			}
		}
		return nil
	})
	if ids[0] == ids[1] {
		t.Fatalf("两条同文 assistant 共用了身份 %q（身份被正文/位置决定）", ids[0])
	}

	// 身份就在 v4 字节里：重新读文件即可取回，不需要任何"按位置重算"。
	back, err := v4.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Events) != 2 {
		t.Fatalf("事件数 = %d，期望 2", len(back.Events))
	}
	for i, ev := range back.Events {
		var data map[string]any
		if err := json.Unmarshal(ev.Data, &data); err != nil {
			t.Fatal(err)
		}
		if got := assistantPersistedID(t, data); got != ids[i] {
			t.Fatalf("v4 第 %d 条 assistant 落盘 id=%q，期望 %q", i, got, ids[i])
		}
	}

	// 续写第三条：前两条身份与旧字节都不许动（append-only）。
	logw, err := session.OpenSessionAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := logw.Append("assistant/message", session.AssistantMessageEvent(2, 1,
		[]map[string]any{session.TextBlock("同文回答")}, "stop", nil)); err != nil {
		t.Fatal(err)
	}
	if err := logw.Close(); err != nil {
		t.Fatal(err)
	}

	// load 投影：messageId 就是落盘那个 id，且与位置字段互不冒充。
	msgs := loadMessages(t, home, cwd, sid)
	if len(msgs) != 3 {
		t.Fatalf("load 消息数 = %d，期望 3：%+v", len(msgs), msgs)
	}
	for i := 0; i < 2; i++ {
		m := loadedByV4Seq(t, msgs, int64(i))
		if m.messageID != ids[i] {
			t.Fatalf("load 第 %d 条 messageId=%q，应为落盘持久 id=%q", i, m.messageID, ids[i])
		}
		if m.messageID == strconv.FormatInt(int64(i), 10) ||
			m.messageID == strconv.Itoa(m.transport) || m.messageID == "v4:"+strconv.Itoa(i) {
			t.Fatalf("messageId=%q 与位置（v4Seq=%d / transport=%d）混用", m.messageID, i, m.transport)
		}
	}
	if msgs[2].messageID == "" || msgs[2].messageID == ids[0] || msgs[2].messageID == ids[1] {
		t.Fatalf("续写来的第三条必须有自己的身份，实际 %q", msgs[2].messageID)
	}
}

// TestAssistantIDSurvivesCompactionAndWindow 钉住"压缩/窗口变化不改身份"。
//
// 压缩只追加 compaction/* 事件并遮蔽既有 seq（派生投影），v4 一个字节不重写。
// 因此身份**必须**保持不变：它来自事件本身。旧实现只能拿 v4:<seq> 的地方，
// 一旦换到另一套位置语义（fork 新文件、其它投影窗口）就对不上。
func TestAssistantIDSurvivesCompactionAndWindow(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	const sid = "message-id-compaction"
	var keptID string
	path := seedTurnSession(t, home, cwd, sid, func(lg *session.Log) error {
		if err := lg.Append("user/message", session.UserMessage([]spi.Block{{Type: "text", Text: "旧问题"}})); err != nil {
			return err
		}
		kept := session.AssistantMessageEvent(1, 1, []map[string]any{session.TextBlock("旧回答")}, "stop", nil)
		if err := lg.Append("assistant/message", kept); err != nil {
			return err
		}
		if err := lg.Append("user/message", session.UserMessage([]spi.Block{{Type: "text", Text: "新问题"}})); err != nil {
			return err
		}
		newer := session.AssistantMessageEvent(2, 1, []map[string]any{session.TextBlock("新回答")}, "stop", nil)
		keptID = assistantPersistedID(t, newer)
		return lg.Append("assistant/message", newer)
	})

	before := loadMessages(t, home, cwd, sid)
	wantSeq := int64(3)
	if got := loadedByV4Seq(t, before, wantSeq).messageID; got != keptID {
		t.Fatalf("压缩前 v4Seq=%d 的 messageId=%q，期望 %q", wantSeq, got, keptID)
	}

	// 真压缩前两条（seq 0..1），走生产实现 session.Compact。
	logged, err := v4.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	logw, err := session.OpenSessionAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := session.Compact(logw, logged.Events, session.Span{Start: 0, End: 1},
		[]spi.Block{{Type: "text", Text: "旧问题被压成摘要"}},
		session.CompactionMeta{Provider: "test", Model: "m"}); err != nil {
		t.Fatalf("压缩: %v", err)
	}
	if err := logw.Close(); err != nil {
		t.Fatal(err)
	}

	after := loadMessages(t, home, cwd, sid)
	got := loadedByV4Seq(t, after, wantSeq)
	if got.messageID != keptID {
		t.Fatalf("压缩后 v4Seq=%d 的 messageId=%q，期望不变 %q（身份被窗口位置决定了）",
			wantSeq, got.messageID, keptID)
	}
	if got.messageID == strconv.FormatInt(*got.v4Seq, 10) {
		t.Fatalf("messageId=%q 退化成 v4 seq（位置冒充身份）", got.messageID)
	}
}

// TestLegacyAssistantWithoutPersistedIDStillLoads 钉住"旧数据仍能读、且不补造"。
//
// 修复前写下的 v4 里 assistant 没有 message.id。append-only ⇒ 那些字节永远补不上，
// 读路径也**不许**为它们现算一个 id（那会在下一次读出时变一个值）。
// 旧记录只能继续用 v4Seq 做兜底身份，界面据此走 scoped fallback。
func TestLegacyAssistantWithoutPersistedIDStillLoads(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	const sid = "message-id-legacy"
	legacy := json.RawMessage(`{"turn":1,"step":1,` +
		`"message":{"role":"assistant","content":[{"type":"text","text":"改造前的回答"}]},` +
		`"stream":[{"type":"finish","reason":{"kind":"completed"}}],"surfaceOp":"append"}`)
	seedTurnSession(t, home, cwd, sid, func(lg *session.Log) error {
		return lg.Append("assistant/message", legacy)
	})

	msgs := loadMessages(t, home, cwd, sid)
	if len(msgs) != 1 {
		t.Fatalf("旧记录必须照样读出来，实际 %d 条：%+v", len(msgs), msgs)
	}
	if msgs[0].v4Seq == nil || *msgs[0].v4Seq != 0 {
		t.Fatalf("旧记录仍须有持久 v4Seq（seq=0 也合法）：%+v", msgs[0])
	}
	if msgs[0].messageID != "" {
		t.Fatalf("旧记录没有持久 id，读路径不得现算一个：%q", msgs[0].messageID)
	}

	// 再读一次：没有稳定来源时，结论必须稳定为空，不能一次一个值。
	again := loadMessages(t, home, cwd, sid)
	if again[0].messageID != "" || *again[0].v4Seq != *msgs[0].v4Seq {
		t.Fatalf("旧记录两次读取不一致：%+v vs %+v", msgs[0], again[0])
	}
}

// normalizeAssistantMessageID 把 `message.id` 归一化成固定占位符，供"落盘字节必须与
// 改前一致"的基线用例使用（`TestRunNoReasoningPayloadBytesUnchanged` /
// `TestACPPromptNoReasoningPayloadBytesUnchanged`）。
//
// 为什么是归一化而不是改期望值：那两条用例的判据是"常见的一轮回答，落盘字节没被误伤"。
// ID-PERSIST 是**显式审查过的形状变更**（官方 v4 的消息对象本来就带 `MessageBase.id`，
// `dsh-llm/src/message.ts:139-141`），不属于"误伤"。把身份位挖掉之后，其余字节仍要
// 逐字节相等，任何一个别的键动了都会红；同时这里**先断言**身份存在且是 uuidv4，
// 免得"根本没写 id"被归一化悄悄放过。
func normalizeAssistantMessageID(t *testing.T, raw []byte) []byte {
	t.Helper()
	const placeholder = `"id":"<message-id>"`
	found := wiredMessageID.FindAll(raw, -1)
	if len(found) != 1 {
		t.Fatalf("assistant/message 必须恰有一个 uuidv4 形状的 message.id，实际 %d 个：%s", len(found), raw)
	}
	return bytes.Replace(raw, found[0], []byte(placeholder), 1)
}
