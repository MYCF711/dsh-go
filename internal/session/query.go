package session

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"dshgo/codecs/v4"
)

// 跨会话检索（官方 `session-query` 的 scan 档）。
//
// **引擎是诚实的**：`Engine` 字段恒为 `scan`——对会话日志做字面、大小写
// 不敏感、空白可折叠的子串扫描，**不是** FTS5 排名检索。宿主没有 sqlite
// 依赖（引入它需要 dependency review），也没有排名/词干/相关度，所以绝不许
// 用 `scan` 冒充 `fts5`。
//
// 分页是**游标**（`{sessionId, seq}` 的 URL 安全 base64）：会话按
// (createdAt, sessionId) 升序，事件按 seq 升序，游标指向"最后一个已返回的
// 命中"，下一页从它之后继续——即使会话文件在两次翻页之间被追加，也不重不漏
// （游标是位置，不是偏移量）。
const (
	// QueryEngineScan 是唯一实现的检索引擎名。
	QueryEngineScan = "scan"
	// QueryDefaultLimit 是未指定 limit 时的页大小。
	QueryDefaultLimit = 20
	// QueryMaxLimit 是单页上限。
	QueryMaxLimit = 100
	// QueryMaxChars 是查询串的字符上限（官方 SESSION_SEARCH_QUERY_MAX_CHARS）。
	QueryMaxChars = 500
)

// 检索请求的稳定错误（调用方按 errors.Is 分类）。
var (
	ErrQueryEmpty   = fmt.Errorf("检索 query 不能为空")
	ErrQueryTooLong = fmt.Errorf("检索 query 超过 %d 个字符", QueryMaxChars)
	ErrQueryNUL     = fmt.Errorf("检索 query 不能包含 NUL")
	ErrQueryCursor  = fmt.Errorf("检索游标无效")
)

// QueryCursor 是游标内容：最后一页最后一个命中的 (会话, seq)。
type QueryCursor struct {
	SessionID string `json:"sessionId"`
	Seq       int64  `json:"seq"`
}

// EncodeQueryCursor 把游标编成 URL 安全的 base64。
func EncodeQueryCursor(cursor QueryCursor) (string, error) {
	raw, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("序列化检索游标失败: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// DecodeQueryCursor 解出游标。空 sessionId 一律视为坏游标。
func DecodeQueryCursor(raw string) (QueryCursor, error) {
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return QueryCursor{}, fmt.Errorf("%w: %v", ErrQueryCursor, err)
	}
	var cursor QueryCursor
	if err := json.Unmarshal(data, &cursor); err != nil {
		return QueryCursor{}, fmt.Errorf("%w: %v", ErrQueryCursor, err)
	}
	if cursor.SessionID == "" {
		return QueryCursor{}, fmt.Errorf("%w: 空 sessionId", ErrQueryCursor)
	}
	return cursor, nil
}

// QueryRequest 是一次跨会话检索。
type QueryRequest struct {
	// Home 是 DSH home（会话根是 <Home>/sessions）。必填。
	Home string
	// ProjectKey 非空时只扫这个项目目录（v4.ProjectKey 算出来的段名）。
	ProjectKey string
	// SessionID 非空时只扫这一个会话（不存在给空页，不报错）。
	SessionID string
	// Text 是字面检索串。
	Text string
	// Types 非空时只保留这些事件类型。
	Types []string
	// Limit 是页大小；<=0 用 QueryDefaultLimit，超过 QueryMaxLimit 夹紧。
	Limit int
	// Cursor 是上一页返回的 NextCursor。
	Cursor string
}

// QueryHit 是一条命中。
type QueryHit struct {
	SessionID string `json:"sessionId"`
	Seq       int64  `json:"seq"`
	Type      string `json:"type"`
	Role      string `json:"role"`
	Time      int64  `json:"time"`
	Excerpt   string `json:"excerpt"`
	// Hidden 表示该 seq 已被压缩遮蔽：还能搜到，但它已经不在模型看到的表面上。
	Hidden bool `json:"hidden"`
}

// QueryPage 是一页结果。
type QueryPage struct {
	Hits       []QueryHit `json:"hits"`
	NextCursor string     `json:"nextCursor,omitempty"`
	HasMore    bool       `json:"hasMore"`
	// Engine 恒为 "scan"：这是子串扫描，不是 FTS5。
	Engine string `json:"engine"`
	// Limit 是本页实际生效的页大小（夹紧之后）。
	Limit int `json:"limit"`
	// ScannedSessions 是本次实际读过的会话文件数（成本要可观测，不能假装免费）。
	ScannedSessions int `json:"scannedSessions"`
}

type querySessionFile struct {
	id        string
	path      string
	createdAt int64
}

// SearchSessions 在 <Home>/sessions 下做跨会话检索。
//
// 读不动的会话文件跳过（面板能看见的损坏会话不该让整次检索失败），但计数里
// 仍然算"扫过"。
func SearchSessions(request QueryRequest) (QueryPage, error) {
	text := strings.TrimSpace(request.Text)
	if text == "" {
		return QueryPage{}, ErrQueryEmpty
	}
	if strings.ContainsRune(text, 0) {
		return QueryPage{}, ErrQueryNUL
	}
	if len([]rune(text)) > QueryMaxChars {
		return QueryPage{}, ErrQueryTooLong
	}
	limit := request.Limit
	if limit <= 0 {
		limit = QueryDefaultLimit
	}
	if limit > QueryMaxLimit {
		limit = QueryMaxLimit
	}
	var cursor QueryCursor
	if request.Cursor != "" {
		decoded, err := DecodeQueryCursor(request.Cursor)
		if err != nil {
			return QueryPage{}, err
		}
		cursor = decoded
	}
	if request.Home == "" {
		return QueryPage{}, fmt.Errorf("检索需要 Home")
	}

	sessions, err := listQuerySessions(request.Home, request.ProjectKey, request.SessionID)
	if err != nil {
		return QueryPage{}, err
	}

	needle, _ := normalizeQueryText(text)
	types := make(map[string]bool, len(request.Types))
	for _, eventType := range request.Types {
		types[eventType] = true
	}

	page := QueryPage{Hits: make([]QueryHit, 0, limit), Engine: QueryEngineScan, Limit: limit}
	started := cursor.SessionID == ""
	for _, candidate := range sessions {
		if !started {
			if candidate.id != cursor.SessionID {
				continue
			}
			started = true
		}
		log, readErr := v4.ReadFile(candidate.path)
		if readErr != nil {
			continue
		}
		page.ScannedSessions++
		hidden := hiddenSeqs(log)
		fromSeq := int64(0)
		if candidate.id == cursor.SessionID {
			fromSeq = cursor.Seq + 1
		}
		for _, event := range log.Events {
			if event.Seq < fromSeq {
				continue
			}
			if len(types) > 0 && !types[event.Type] {
				continue
			}
			body := EventText(event)
			if body == "" {
				continue
			}
			normalized, offsets := normalizeQueryText(body)
			if !strings.Contains(normalized, needle) {
				continue
			}
			if len(page.Hits) == limit {
				// 已经够一页：这一条证明还有更多，游标指回上一条。
				page.HasMore = true
				last := page.Hits[len(page.Hits)-1]
				encoded, encodeErr := EncodeQueryCursor(QueryCursor{SessionID: last.SessionID, Seq: last.Seq})
				if encodeErr != nil {
					return QueryPage{}, encodeErr
				}
				page.NextCursor = encoded
				return page, nil
			}
			page.Hits = append(page.Hits, QueryHit{
				SessionID: candidate.id,
				Seq:       event.Seq,
				Type:      event.Type,
				Role:      roleOf(event.Type),
				Time:      event.Time,
				Excerpt:   excerptOf(body, normalized, needle, offsets),
				Hidden:    hidden[event.Seq],
			})
		}
	}
	return page, nil
}

// listQuerySessions 列出候选会话文件，按 (createdAt, id) 升序——不可变且
// 与会话文件的更新时间无关，游标才稳定。
func listQuerySessions(home, projectKey, sessionID string) ([]querySessionFile, error) {
	sessionsDir := filepath.Join(home, "sessions")
	var projectDirs []string
	if projectKey != "" {
		projectDirs = []string{filepath.Join(sessionsDir, projectKey)}
	} else {
		entries, err := os.ReadDir(sessionsDir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, nil
			}
			return nil, fmt.Errorf("读会话根 %s 失败: %w", sessionsDir, err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				projectDirs = append(projectDirs, filepath.Join(sessionsDir, entry.Name()))
			}
		}
	}
	var out []querySessionFile
	for _, dir := range projectDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			path := filepath.Join(dir, entry.Name(), "session.v4.jsonl.zstd")
			header, err := v4.ReadHeader(path)
			if err != nil || header.ID == "" {
				continue
			}
			if sessionID != "" && header.ID != sessionID {
				continue
			}
			out = append(out, querySessionFile{id: header.ID, path: path, createdAt: header.CreatedAt})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].createdAt != out[j].createdAt {
			return out[i].createdAt < out[j].createdAt
		}
		return out[i].id < out[j].id
	})
	return out, nil
}

// EventText 抽出一条事件里"可检索的正文"。
//
// 不只 `type=="text"`：工具调用与思考块同样是"这一轮说过的话"。只拼 text
// 块的后果是**只有 tool_calls 的 assistant 在检索里完全查不到**，而"上次
// 那个 ls 是哪一轮"正是这个能力存在的理由。
//
// 形态与 `cmd/dshd` 的 `toolCallText` / `reasoningText` 一致：模型看见
// `[调用 bash：{"command":"ls"}]` 就知道该按什么搜；片段里也不会是整段事件
// JSON（那是机器形状，翻出来全是转义符）。
func EventText(event v4.Event) string {
	blocks, ok := ContentBlocks(event.Data, PreferNested)
	if !ok || len(blocks) == 0 {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case "text":
			// 空白正文和"没有"没区别。
			if strings.TrimSpace(block.Text) != "" {
				parts = append(parts, block.Text)
			}
		case "reasoning":
			if text, ok := ReasoningText(block.Type, block.Text); ok {
				parts = append(parts, "[思考："+flattenQueryText(text)+"]")
			}
		case "tool-call":
			parts = append(parts, "[调用 "+block.Name+"："+flattenQueryText(block.Arguments)+"]")
		}
	}
	return strings.Join(parts, "\n")
}

func flattenQueryText(text string) string { return strings.Join(strings.Fields(text), " ") }

// normalizeQueryText 做大小写折叠 + 空白折叠，并返回"归一化字节 → 原文
// 字节偏移"的映射（摘录要能切回原文）。
func normalizeQueryText(text string) (string, []int) {
	var builder strings.Builder
	offsets := make([]int, 0, len(text))
	pendingSpace := false
	for index, current := range text {
		if unicode.IsSpace(current) {
			pendingSpace = true
			continue
		}
		if pendingSpace && builder.Len() > 0 {
			offsets = append(offsets, index)
			builder.WriteByte(' ')
		}
		pendingSpace = false
		lowered := unicode.ToLower(current)
		start := builder.Len()
		builder.WriteRune(lowered)
		for position := start; position < builder.Len(); position++ {
			offsets = append(offsets, index)
		}
	}
	return builder.String(), offsets
}

// excerptOf 取命中位置前后各一小段。返回值里的换行一律摊平（一行摘录）。
func excerptOf(text, normalized, needle string, offsets []int) string {
	index := strings.Index(normalized, needle)
	if index < 0 {
		return ""
	}
	const before, after = 80, 120
	start := index - before
	if start < 0 {
		start = 0
	}
	end := index + len(needle) + after
	if end > len(normalized) {
		end = len(normalized)
	}
	originalStart := 0
	if start < len(offsets) {
		originalStart = offsets[start]
	}
	originalEnd := len(text)
	if end < len(offsets) {
		originalEnd = offsets[end]
	}
	if originalStart > originalEnd {
		originalStart = originalEnd
	}
	snippet := strings.ReplaceAll(text[originalStart:originalEnd], "\n", " ")
	if originalStart > 0 {
		snippet = "…" + snippet
	}
	if originalEnd < len(text) {
		snippet += "…"
	}
	return snippet
}

// hiddenSeqs 收集所有被压缩遮蔽的 seq。
func hiddenSeqs(log *v4.Log) map[int64]bool {
	out := map[int64]bool{}
	for _, event := range log.Events {
		if event.Type != "compaction/summary" && event.Type != "compaction/prune" {
			continue
		}
		var data struct {
			ShadowedSeqs []int64 `json:"shadowedSeqs"`
		}
		if json.Unmarshal(event.Data, &data) != nil {
			continue
		}
		for _, seq := range data.ShadowedSeqs {
			out[seq] = true
		}
	}
	return out
}

// roleOf 把事件类型映射成对话角色（与历史检索同一套词表）。
func roleOf(eventType string) string {
	switch eventType {
	case "user/message":
		return "user"
	case "assistant/message":
		return "assistant"
	case "tool/result":
		return "tool"
	case "system/message":
		return "system"
	}
	return ""
}
