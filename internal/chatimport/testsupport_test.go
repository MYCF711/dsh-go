package chatimport

// 测试夹具：合成 ZIP 与合成对话，全部在内存里构造（官方
// tests/importer.host.spec.ts 的 "synthesized ZIPs" 同一手法）。
//
// 这里**不**读任何 tmp 语料：夹具自己造字节，测试就能在干净 checkout 上跑。

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"sort"
	"testing"

	"dshgo/codecs/v4"
)

// zipEntry 是合成归档里的一个条目。
type zipEntry struct {
	name string
	data []byte
	// store 为真时用 ZIP_STORED（0），否则 deflate（8）。
	store bool
}

// buildZip 按给定顺序把这些条目写成一个 ZIP。
func buildZip(t *testing.T, entries []zipEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, entry := range entries {
		method := zip.Deflate
		if entry.store {
			method = zip.Store
		}
		header := &zip.FileHeader{Name: entry.name, Method: method}
		handle, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatalf("造归档条目 %s 失败: %v", entry.name, err)
		}
		if _, err := handle.Write(entry.data); err != nil {
			t.Fatalf("写归档条目 %s 失败: %v", entry.name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭合成归档失败: %v", err)
	}
	return buffer.Bytes()
}

// fixtureJSON 把测试里的 map 编成 JSON 字节。
func fixtureJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("夹具 JSON 序列化失败: %v", err)
	}
	return raw
}

// manifestEntry 造一份 `manifest.json`（版本默认 3）。
func manifestEntry(t *testing.T, version int) zipEntry {
	t.Helper()
	return zipEntry{name: ManifestEntry, data: fixtureJSON(t, map[string]any{"export_format_version": version})}
}

// indexEntry 造一份 `chat_session_index.json`。
func indexEntry(t *testing.T, rows ...[2]string) zipEntry {
	t.Helper()
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, map[string]any{"id": row[0], "inserted_at": row[1], "updated_at": row[1]})
	}
	return zipEntry{name: IndexEntry, data: fixtureJSON(t, items)}
}

// conversationEntryZip 造一份 `chat_sessions/{id}.json`。
func conversationEntryZip(t *testing.T, id string, title any, messages []map[string]any) zipEntry {
	t.Helper()
	payload := map[string]any{
		"id":          id,
		"title":       title,
		"inserted_at": "2026-11-12T10:00:00.000Z",
		"messages":    messages,
	}
	return zipEntry{name: conversationEntry(id), data: fixtureJSON(t, payload)}
}

// userMessageJSON 造一条 USER 消息。
func userMessageJSON(id int64, parent any, text, insertedAt string) map[string]any {
	return map[string]any{
		"id":          id,
		"parent_id":   parent,
		"role":        "USER",
		"status":      "FINISHED",
		"inserted_at": insertedAt,
		"fragments":   []map[string]any{{"type": "REQUEST", "content": text}},
	}
}

// replyMessageJSON 造一条 ASSISTANT 消息。
func replyMessageJSON(id int64, parent any, status, insertedAt string, fragments []map[string]any) map[string]any {
	return map[string]any{
		"id":          id,
		"parent_id":   parent,
		"role":        "ASSISTANT",
		"status":      status,
		"inserted_at": insertedAt,
		"fragments":   fragments,
	}
}

// parseConversationFixture 把一份对话 JSON 过一遍校验，测试里直接拿结构体。
func parseConversationFixture(t *testing.T, payload map[string]any) ConversationFile {
	t.Helper()
	file, err := ParseConversation(fixtureJSON(t, payload))
	if err != nil {
		t.Fatalf("测试对话校验失败: %v", err)
	}
	return file
}

// fixedIDs 造一个确定性 id 生成器（seq 顺序可断言）。
func fixedIDs() func() string {
	index := 0
	return func() string {
		index++
		return "id-" + itoa(index)
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 8)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

// eventTypes 取事件类型序列，便于逐条断言。
func eventTypes(events []v4.Event) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		out = append(out, event.Type)
	}
	return out
}

// eventData 把一条事件的 data 解成 map（断言字段用）。
func eventData(t *testing.T, event v4.Event) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(event.Data, &decoded); err != nil {
		t.Fatalf("事件 %s 的 data 不是对象: %v", event.Type, err)
	}
	return decoded
}

// sortedKeys 取 map 的键（错误信息用）。
func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
