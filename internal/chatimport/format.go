package chatimport

// 官方 `src/format.ts` 的逐条对应物：只校验导入真正读的字段；导入不映射的
// fragment 类型原样接受、不校验，这样导出方以后新增记录类型不会让整份对话
// 变成不可导入（官方 format.ts:1-8 的原话）。

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// SupportedExportFormatVersion 是本包唯一读得懂的 manifest 版本
// （官方 `SUPPORTED_EXPORT_FORMAT_VERSION`，format.ts:12）。
const SupportedExportFormatVersion = 3

// Manifest 是 `manifest.json`（官方 `exportManifest`，format.ts:15）。
type Manifest struct {
	ExportFormatVersion float64
}

// IndexRow 是一条 `chat_session_index.json` 记录，时间已换算成 epoch 毫秒
// （官方 `exportSessionIndex` 的元素，format.ts:32-36）。
type IndexRow struct {
	ID         string
	InsertedAt int64
	UpdatedAt  int64
}

// Message 是一条导出消息（官方 `exportMessage`，format.ts:43-50）。
type Message struct {
	// ID 是消息 id；同一对话内必须唯一。
	ID int64
	// ParentID 是指向父消息的 id；根消息是 nil。
	ParentID *int64
	// Role 取 `USER` 或 `ASSISTANT`。
	Role string
	// Status 是导出方给的回复状态原文（`FINISHED`/`CONTENT_FILTER`/…）。
	Status string
	// InsertedAt 是消息时间；**不在** schema 里做时间校验（官方是普通字符串），
	// 转换时才解析，坏时间只让这一条对话失败。
	InsertedAt string
	// Fragments 是原始信封，按官方 schema 只要求 `type` 是字符串。
	Fragments []json.RawMessage
}

// ConversationFile 是一个对话文件（官方 `exportSessionFile`，format.ts:57-63）。
type ConversationFile struct {
	ID string
	// Title 是导出标题；JSON 里必须是 string 或 null（键必须存在）。
	Title *string
	// CurrentMessageID 是 `current_message_id`；absnet/null/指向不存在的消息
	// 都退回"id 最大的那条"。
	CurrentMessageID *int64
	InsertedAt       string
	Messages         []Message
}

// FileEntry 是一个 `FILE` 片段里的文件条目（官方 `fileEntry`，format.ts:87-94）。
type FileEntry struct {
	ID string
	// FileName 是导出里的原始文件名。
	FileName string
	// FileSize 是声明的字节数；缺失时是 0（官方 `.optional().default(0)`）。
	FileSize int64
	// IsImage 表示它是图片。
	IsImage bool
	// ExportPath 是归档里对应的条目名；非图片/没导出时是 nil。
	ExportPath *string
}

// SearchResult 是一条搜索结果（官方 `searchResult`，format.ts:75-80）。
type SearchResult struct {
	URL         string
	Title       *string
	Snippet     *string
	PublishedAt *string
}

// LinkResult 是一条 `READ_LINK` 结果（官方 `readLinkResult`，format.ts:82-85）。
type LinkResult struct {
	URL   *string
	Title *string
}

// Fragment 是一个已映射的 fragment（官方 `KnownFragment`，format.ts:100-118）。
//
// 用一个结构体装所有类型的字段而不是每型一个：转换器只按 Type 取它需要的
// 那几个字段，与官方 discriminatedUnion 的**语义**一致，且省掉一层类型断言。
type Fragment struct {
	// Type 是 fragment 类型（`REQUEST`/`THINK`/`RESPONSE`/…）。
	Type string
	// Content 是文本类 fragment 的正文。
	Content string
	// ElapsedSecs 是 `THINK` 的过程耗时（秒）；缺省 0。
	ElapsedSecs float64
	// Queries 是 `TOOL_SEARCH` 的查询词。
	Queries []string
	// SearchResults 是 `TOOL_SEARCH` 的结果。
	SearchResults []SearchResult
	// Status 是工具类 fragment 的状态原文。
	Status string
	// StageID 是工具类 fragment 的阶段号；nil 表示没有这个字段。
	StageID *int64
	// URL 是 `TOOL_OPEN` 的地址；nil 表示 null 或缺省（官方 nullable）。
	URL *string
	// Title 是 `TOOL_OPEN` 的页面标题。
	Title *string
	// Pattern 是 `TOOL_FIND` 的查找式（本仓不映射，只做 schema 兼容）。
	Pattern *string
	// LinkResults 是 `READ_LINK` 的结果。
	LinkResults []LinkResult
	// Ratio 是 `PARTIAL_FILE_READ` 的比例（本仓不映射）。
	Ratio float64
	// Files 是 `FILE` 片段里的文件条目。
	Files []FileEntry
}

// knownFragmentTypes 是导入会**映射**的类型集合（官方 KNOWN_FRAGMENT_TYPES，format.ts:127）。
var knownFragmentTypes = map[string]struct{}{
	"REQUEST":           {},
	"RESPONSE":          {},
	"THINK":             {},
	"TEMPLATE_RESPONSE": {},
	"TOOL_SEARCH":       {},
	"TOOL_OPEN":         {},
	"TOOL_FIND":         {},
	"READ_LINK":         {},
	"TOOL_IMAGE_SEARCH": {},
	"PARTIAL_FILE_READ": {},
	"TIP":               {},
	"FILE":              {},
}

// ParseManifest 校验 `manifest.json`（官方 `exportManifest.safeParse`）。
func ParseManifest(data []byte) (Manifest, error) {
	object, err := decodeObject(data)
	if err != nil {
		return Manifest{}, err
	}
	raw, err := requireKey(object, "export_format_version")
	if err != nil {
		return Manifest{}, err
	}
	value, err := asNumber(raw)
	if err != nil {
		return Manifest{}, fmt.Errorf("export_format_version must be a number")
	}
	return Manifest{ExportFormatVersion: value}, nil
}

// ParseIndex 校验 `chat_session_index.json`（官方 `exportSessionIndex.safeParse`）。
// 重复 id 在这里被去重（官方 `parseIndex` 的 Map，importer.ts:425-429）。
func ParseIndex(data []byte) ([]IndexRow, error) {
	var rows []json.RawMessage
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("chat_session_index.json must be an array")
	}
	out := make([]IndexRow, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for index, raw := range rows {
		object, err := decodeObject(raw)
		if err != nil {
			return nil, fmt.Errorf("chat_session_index.json[%d] must be an object", index)
		}
		idRaw, err := requireKey(object, "id")
		if err != nil {
			return nil, fmt.Errorf("chat_session_index.json[%d]: %w", index, err)
		}
		id, err := asString(idRaw)
		if err != nil || id == "" {
			return nil, fmt.Errorf("chat_session_index.json[%d].id must be a non-empty string", index)
		}
		inserted, err := requiredTime(object, "inserted_at", index)
		if err != nil {
			return nil, err
		}
		updated, err := requiredTime(object, "updated_at", index)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, IndexRow{ID: id, InsertedAt: inserted, UpdatedAt: updated})
	}
	return out, nil
}

// ParseConversation 校验一个对话文件，失败时点名缺陷
// （官方 `parseConversation`，importer.ts:485-491）。
func ParseConversation(data []byte) (ConversationFile, error) {
	object, err := decodeObject(data)
	if err != nil {
		return ConversationFile{}, fmt.Errorf("invalid conversation file: not valid JSON")
	}
	var out ConversationFile
	if raw, err := requireKey(object, "id"); err != nil {
		return ConversationFile{}, fmt.Errorf("invalid conversation file: %w", err)
	} else if id, err := asString(raw); err != nil || id == "" {
		return ConversationFile{}, fmt.Errorf("invalid conversation file: id must be a non-empty string")
	} else {
		out.ID = id
	}
	// title 是 `string | null` 且**键必须存在**：缺键不是 null。
	titleRaw, err := requireKey(object, "title")
	if err != nil {
		return ConversationFile{}, fmt.Errorf("invalid conversation file: %w", err)
	}
	if !isJSONNull(titleRaw) {
		title, err := asString(titleRaw)
		if err != nil {
			return ConversationFile{}, fmt.Errorf("invalid conversation file: title must be a string or null")
		}
		out.Title = &title
	}
	// current_message_id 可缺省、可 null、可非负整数。
	if raw, ok := object["current_message_id"]; ok && !isJSONNull(raw) {
		value, err := asInteger(raw)
		if err != nil || value < 0 {
			return ConversationFile{}, fmt.Errorf("invalid conversation file: current_message_id must be a non-negative integer")
		}
		out.CurrentMessageID = &value
	}
	if raw, err := requireKey(object, "inserted_at"); err != nil {
		return ConversationFile{}, fmt.Errorf("invalid conversation file: %w", err)
	} else if _, err := asString(raw); err != nil {
		return ConversationFile{}, fmt.Errorf("invalid conversation file: inserted_at must be a string")
	} else {
		out.InsertedAt, _ = asString(raw)
	}
	messagesRaw, err := requireKey(object, "messages")
	if err != nil {
		return ConversationFile{}, fmt.Errorf("invalid conversation file: %w", err)
	}
	var messages []json.RawMessage
	if err := json.Unmarshal(messagesRaw, &messages); err != nil {
		return ConversationFile{}, fmt.Errorf("invalid conversation file: messages must be an array")
	}
	out.Messages = make([]Message, 0, len(messages))
	for index, raw := range messages {
		message, err := parseMessage(raw)
		if err != nil {
			return ConversationFile{}, fmt.Errorf("invalid conversation file: messages[%d]: %w", index, err)
		}
		out.Messages = append(out.Messages, message)
	}
	return out, nil
}

// parseMessage 校验一条消息（官方 `exportMessage`）。
func parseMessage(raw json.RawMessage) (Message, error) {
	object, err := decodeObject(raw)
	if err != nil {
		return Message{}, fmt.Errorf("must be an object")
	}
	var out Message
	idRaw, err := requireKey(object, "id")
	if err != nil {
		return Message{}, err
	}
	id, err := asInteger(idRaw)
	if err != nil || id < 0 {
		return Message{}, fmt.Errorf("id must be a non-negative integer")
	}
	out.ID = id
	parentRaw, err := requireKey(object, "parent_id")
	if err != nil {
		return Message{}, err
	}
	if !isJSONNull(parentRaw) {
		parent, err := asInteger(parentRaw)
		if err != nil || parent < 0 {
			return Message{}, fmt.Errorf("parent_id must be a non-negative integer or null")
		}
		out.ParentID = &parent
	}
	roleRaw, err := requireKey(object, "role")
	if err != nil {
		return Message{}, err
	}
	role, err := asString(roleRaw)
	if err != nil || (role != "USER" && role != "ASSISTANT") {
		return Message{}, fmt.Errorf("role must be USER or ASSISTANT")
	}
	out.Role = role
	statusRaw, err := requireKey(object, "status")
	if err != nil {
		return Message{}, err
	}
	if out.Status, err = asString(statusRaw); err != nil {
		return Message{}, fmt.Errorf("status must be a string")
	}
	insertedRaw, err := requireKey(object, "inserted_at")
	if err != nil {
		return Message{}, err
	}
	if out.InsertedAt, err = asString(insertedRaw); err != nil {
		return Message{}, fmt.Errorf("inserted_at must be a string")
	}
	fragmentsRaw, err := requireKey(object, "fragments")
	if err != nil {
		return Message{}, err
	}
	if err := json.Unmarshal(fragmentsRaw, &out.Fragments); err != nil {
		return Message{}, fmt.Errorf("fragments must be an array")
	}
	if out.Fragments == nil {
		return Message{}, fmt.Errorf("fragments must be an array")
	}
	return out, nil
}

// ParseKnownFragment 校验导入映射的 fragment；不映射的类型返回 (nil, nil)
// （官方 `parseKnownFragment`，format.ts:135-137）。
func ParseKnownFragment(raw json.RawMessage) (*Fragment, error) {
	object, err := decodeObject(raw)
	if err != nil {
		return nil, fmt.Errorf("fragment must be an object")
	}
	typeRaw, err := requireKey(object, "type")
	if err != nil {
		return nil, err
	}
	kind, err := asString(typeRaw)
	if err != nil {
		return nil, fmt.Errorf("fragment type must be a string")
	}
	if _, known := knownFragmentTypes[kind]; !known {
		return nil, nil
	}
	out := &Fragment{Type: kind}
	switch kind {
	case "REQUEST", "RESPONSE", "TEMPLATE_RESPONSE", "TIP":
		if out.Content, err = requiredString(object, "content"); err != nil {
			return nil, err
		}
	case "THINK":
		if out.Content, err = requiredString(object, "content"); err != nil {
			return nil, err
		}
		if raw, ok := object["elapsed_secs"]; ok && !isJSONNull(raw) {
			elapsed, err := asNumber(raw)
			if err != nil || elapsed < 0 {
				return nil, fmt.Errorf("elapsed_secs must be a non-negative number or null")
			}
			out.ElapsedSecs = elapsed
		}
	case "TOOL_SEARCH":
		if out.Queries, err = requiredStringArray(object, "queries"); err != nil {
			return nil, err
		}
		if out.SearchResults, err = requiredSearchResults(object, "results"); err != nil {
			return nil, err
		}
		if err := optionalStatus(object, &out.Status); err != nil {
			return nil, err
		}
		if err := optionalStage(object, &out.StageID); err != nil {
			return nil, err
		}
	case "TOOL_OPEN":
		if out.Status, err = requiredString(object, "status"); err != nil {
			return nil, err
		}
		urlRaw, err := requireKey(object, "url")
		if err != nil {
			return nil, err
		}
		if !isJSONNull(urlRaw) {
			url, err := asString(urlRaw)
			if err != nil {
				return nil, fmt.Errorf("url must be a string or null")
			}
			out.URL = &url
		}
		if err := optionalString(object, "title", &out.Title); err != nil {
			return nil, err
		}
		if err := optionalStage(object, &out.StageID); err != nil {
			return nil, err
		}
	case "TOOL_FIND":
		if out.Status, err = requiredString(object, "status"); err != nil {
			return nil, err
		}
		if err := optionalString(object, "pattern", &out.Pattern); err != nil {
			return nil, err
		}
		if err := optionalStage(object, &out.StageID); err != nil {
			return nil, err
		}
	case "READ_LINK":
		raw, err := requireKey(object, "results")
		if err != nil {
			return nil, err
		}
		var rows []json.RawMessage
		if err := json.Unmarshal(raw, &rows); err != nil {
			return nil, fmt.Errorf("results must be an array")
		}
		out.LinkResults = make([]LinkResult, 0, len(rows))
		for _, row := range rows {
			link, err := parseLinkResult(row)
			if err != nil {
				return nil, err
			}
			out.LinkResults = append(out.LinkResults, link)
		}
	case "TOOL_IMAGE_SEARCH":
		if out.Status, err = requiredString(object, "status"); err != nil {
			return nil, err
		}
		raw, err := requireKey(object, "results")
		if err != nil {
			return nil, err
		}
		var rows []json.RawMessage
		if err := json.Unmarshal(raw, &rows); err != nil {
			return nil, fmt.Errorf("results must be an array")
		}
		if err := optionalStage(object, &out.StageID); err != nil {
			return nil, err
		}
	case "PARTIAL_FILE_READ":
		raw, err := requireKey(object, "ratio")
		if err != nil {
			return nil, err
		}
		if out.Ratio, err = asNumber(raw); err != nil {
			return nil, fmt.Errorf("ratio must be a number")
		}
	case "FILE":
		filesRaw, err := requireKey(object, "files")
		if err != nil {
			return nil, err
		}
		var rows []json.RawMessage
		if err := json.Unmarshal(filesRaw, &rows); err != nil {
			return nil, fmt.Errorf("files must be an array")
		}
		out.Files = make([]FileEntry, 0, len(rows))
		for _, row := range rows {
			file, err := parseFileEntry(row)
			if err != nil {
				return nil, err
			}
			out.Files = append(out.Files, file)
		}
	}
	return out, nil
}

func parseSearchResult(raw json.RawMessage) (SearchResult, error) {
	object, err := decodeObject(raw)
	if err != nil {
		return SearchResult{}, fmt.Errorf("results entries must be objects")
	}
	var out SearchResult
	if out.URL, err = requiredString(object, "url"); err != nil {
		return SearchResult{}, err
	}
	if err := optionalString(object, "title", &out.Title); err != nil {
		return SearchResult{}, err
	}
	if err := optionalString(object, "snippet", &out.Snippet); err != nil {
		return SearchResult{}, err
	}
	if err := optionalString(object, "published_at", &out.PublishedAt); err != nil {
		return SearchResult{}, err
	}
	return out, nil
}

func parseLinkResult(raw json.RawMessage) (LinkResult, error) {
	object, err := decodeObject(raw)
	if err != nil {
		return LinkResult{}, fmt.Errorf("results entries must be objects")
	}
	var out LinkResult
	if err := optionalString(object, "url", &out.URL); err != nil {
		return LinkResult{}, err
	}
	if err := optionalString(object, "title", &out.Title); err != nil {
		return LinkResult{}, err
	}
	return out, nil
}

func parseFileEntry(raw json.RawMessage) (FileEntry, error) {
	object, err := decodeObject(raw)
	if err != nil {
		return FileEntry{}, fmt.Errorf("files entries must be objects")
	}
	var out FileEntry
	if out.ID, err = requiredString(object, "id"); err != nil {
		return FileEntry{}, err
	}
	if out.FileName, err = requiredString(object, "file_name"); err != nil {
		return FileEntry{}, err
	}
	if raw, ok := object["file_size"]; ok && !isJSONNull(raw) {
		size, err := asInteger(raw)
		if err != nil || size < 0 {
			return FileEntry{}, fmt.Errorf("file_size must be a non-negative integer")
		}
		out.FileSize = size
	}
	isImageRaw, err := requireKey(object, "is_image")
	if err != nil {
		return FileEntry{}, err
	}
	if out.IsImage, err = asBool(isImageRaw); err != nil {
		return FileEntry{}, fmt.Errorf("is_image must be a boolean")
	}
	pathRaw, err := requireKey(object, "export_path")
	if err != nil {
		return FileEntry{}, err
	}
	if !isJSONNull(pathRaw) {
		path, err := asString(pathRaw)
		if err != nil {
			return FileEntry{}, fmt.Errorf("export_path must be a string or null")
		}
		out.ExportPath = &path
	}
	return out, nil
}

// ParseExportTime 解析一条导出时间（官方 `parseExportTime`，format.ts:144-147）：
// RFC 3339 UTC 串，小数位 0/3/6/9 位。返回 epoch 毫秒。
func ParseExportTime(value string) (int64, bool) {
	text := strings.TrimSpace(value)
	if text == "" {
		return 0, false
	}
	layouts := []string{
		time.RFC3339Nano,
		// 官方注释点名的 0/3/6/9 位小数由 RFC3339Nano 覆盖；
		// JS 的 Date.parse 还吃空格分隔，这里保持同样宽容。
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05Z07:00",
	}
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, text)
		if err == nil {
			ms := parsed.UnixMilli()
			if ms >= 0 {
				return ms, true
			}
			return 0, false
		}
	}
	return 0, false
}

// ---------- JSON 小工具：zod 语义的 Go 落地 ----------

func decodeObject(data []byte) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, fmt.Errorf("not an object")
	}
	return object, nil
}

func requireKey(object map[string]json.RawMessage, key string) (json.RawMessage, error) {
	raw, ok := object[key]
	if !ok {
		return nil, fmt.Errorf("missing required field %s", key)
	}
	return raw, nil
}

func requiredString(object map[string]json.RawMessage, key string) (string, error) {
	raw, err := requireKey(object, key)
	if err != nil {
		return "", err
	}
	value, err := asString(raw)
	if err != nil {
		return "", fmt.Errorf("%s must be a string", key)
	}
	return value, nil
}

func requiredStringArray(object map[string]json.RawMessage, key string) ([]string, error) {
	raw, err := requireKey(object, key)
	if err != nil {
		return nil, err
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%s must be an array of strings", key)
	}
	if out == nil {
		return nil, fmt.Errorf("%s must be an array of strings", key)
	}
	return out, nil
}

func requiredSearchResults(object map[string]json.RawMessage, key string) ([]SearchResult, error) {
	raw, err := requireKey(object, key)
	if err != nil {
		return nil, err
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("%s must be an array", key)
	}
	out := make([]SearchResult, 0, len(rows))
	for _, row := range rows {
		parsed, err := parseSearchResult(row)
		if err != nil {
			return nil, err
		}
		out = append(out, parsed)
	}
	return out, nil
}

func optionalString(object map[string]json.RawMessage, key string, target **string) error {
	raw, ok := object[key]
	if !ok || isJSONNull(raw) {
		return nil
	}
	value, err := asString(raw)
	if err != nil {
		return fmt.Errorf("%s must be a string or null", key)
	}
	*target = &value
	return nil
}

func optionalStatus(object map[string]json.RawMessage, target *string) error {
	raw, ok := object["status"]
	if !ok || isJSONNull(raw) {
		return nil
	}
	value, err := asString(raw)
	if err != nil {
		return fmt.Errorf("status must be a string")
	}
	*target = value
	return nil
}

func optionalStage(object map[string]json.RawMessage, target **int64) error {
	raw, ok := object["stage_id"]
	if !ok || isJSONNull(raw) {
		return nil
	}
	value, err := asInteger(raw)
	if err != nil {
		return fmt.Errorf("stage_id must be an integer or null")
	}
	*target = &value
	return nil
}

func requiredTime(object map[string]json.RawMessage, key string, index int) (int64, error) {
	raw, err := requireKey(object, key)
	if err != nil {
		return 0, fmt.Errorf("chat_session_index.json[%d]: %w", index, err)
	}
	text, err := asString(raw)
	if err != nil {
		return 0, fmt.Errorf("chat_session_index.json[%d].%s must be a timestamp string", index, key)
	}
	value, ok := ParseExportTime(text)
	if !ok {
		return 0, fmt.Errorf("chat_session_index.json[%d].%s is an invalid timestamp", index, key)
	}
	return value, nil
}

func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

func asString(raw json.RawMessage) (string, error) {
	var out string
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	return out, nil
}

func asBool(raw json.RawMessage) (bool, error) {
	var out bool
	if err := json.Unmarshal(raw, &out); err != nil {
		return false, err
	}
	return out, nil
}

func asNumber(raw json.RawMessage) (float64, error) {
	var out float64
	if err := json.Unmarshal(raw, &out); err != nil {
		return 0, err
	}
	return out, nil
}

// asInteger 落地 zod 的 `z.number().int()`：JSON 数字且没有小数部分。
func asInteger(raw json.RawMessage) (int64, error) {
	value, err := asNumber(raw)
	if err != nil {
		return 0, err
	}
	if value != float64(int64(value)) {
		return 0, fmt.Errorf("not an integer")
	}
	return int64(value), nil
}
