// Package hindsight 是本机自有记忆：把一段文字存下来，按创建时间列出来。
//
// 为什么不让模型自己去写 markdown：记忆要能被**搜到**、能被**按 id 读全文**，
// 这两件事都要求文件名与内容之间有一份稳定映射。让模型自由写 markdown，
// 下一次它就得先 ls 再猜哪个文件是哪个主题，一步变三步。
//
// 边界感：
//   - 只碰 <home>/memory 这一个目录，别处一律不写（Caps.Writes 只给 ingest 声明）
//   - 一个文档一个文件：<home>/memory/<id>.json，整文件写（temp + rename）
//   - 目录不存在是"还没有记忆"，不是错误——第一次 ingest 才创建
package hindsight

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
)

const (
	// maxSlugChars 是 id 里 slug 部分的长度上限（不含 8 位随机十六进制）。
	maxSlugChars = 40
	// randomHexChars 是 id 尾部的随机段长度。
	randomHexChars = 8
	// fileMode / dirMode 与仓库其它落盘工具一致：只给属主写权限。
	fileMode = 0o644
	dirMode  = 0o755
	// suffix 是文档文件扩展名。
	suffix = ".json"
	// idAttempts 是撞 id 时的重试次数（同一秒内同名标题才会用到）。
	idAttempts = 5
)

var ingestSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "title": {"type": "string", "description": "这段记忆叫什么，会参与生成 id"},
    "content": {"type": "string", "description": "要记住的正文"},
    "tags": {"type": "array", "items": {"type": "string"}, "description": "可选标签"}
  },
  "required": ["title", "content"],
  "additionalProperties": false
}`)

var listSchema = json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)

// document 是磁盘上一份文档的形状。键名就是文件里的键名，不加别的。
type document struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Tags    []string `json:"tags"`
	Body    string   `json:"body"`
	Created int64    `json:"created"`
}

// ---------------------------------------------------------------------------
// ingest
// ---------------------------------------------------------------------------

type ingestTool struct{ dir string }

func (t *ingestTool) Name() string { return "hindsight_ingest_document" }

func (t *ingestTool) Description() string {
	return "把一段结论/踩坑/约定存进本机记忆，下次能按 id 捞回来；趁还记得时用。"
}

func (t *ingestTool) Schema() json.RawMessage { return ingestSchema }

// Caps: 会建目录、写文件，必须声明，审批与沙箱都按它判定。
func (t *ingestTool) Caps() spi.Caps { return spi.Caps{Writes: true} }

type ingestArgs struct {
	Title   string   `json:"title"`
	Content string   `json:"content"`
	Tags    []string `json:"tags"`
}

type ingestData struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

func (t *ingestTool) Exec(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	var a ingestArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return spi.Result{Hint: `参数不合法：期望 {"title": "…", "content": "…", "tags"?: ["…"]}`},
			fmt.Errorf("参数解析失败: %w", err)
	}
	title := strings.TrimSpace(a.Title)
	if title == "" {
		return spi.Result{Hint: "缺 title：这段记忆叫什么？"}, fmt.Errorf("title 为空")
	}
	if strings.TrimSpace(a.Content) == "" {
		return spi.Result{Hint: "缺 content：要记的是哪段内容？"}, fmt.Errorf("content 为空")
	}
	if err := ctx.Err(); err != nil {
		return spi.Result{Hint: "这次调用被取消了；原参数重发一次即可"}, err
	}

	tags := a.Tags
	if tags == nil {
		tags = []string{}
	}

	// 目录由 ingest 创建：读路径遇到不存在只会说"还没有记忆"。
	if err := os.MkdirAll(t.dir, dirMode); err != nil {
		return spi.Result{Hint: "写入 " + slash(t.dir) + " 失败：先确认这个目录可写"},
			fmt.Errorf("创建记忆目录失败: %w", err)
	}

	doc := document{
		Title:   title,
		Tags:    tags,
		Body:    a.Content,
		Created: time.Now().Unix(),
	}
	path, err := t.writeNew(&doc)
	if err != nil {
		return spi.Result{Hint: "写入 " + slash(t.dir) + " 失败：先确认这个目录可写"}, err
	}
	_ = path

	return spi.Result{
		Text:    fmt.Sprintf("已存入记忆「%s」（%s）", doc.Title, doc.ID),
		Summary: fmt.Sprintf("已存入记忆「%s」", doc.Title),
		Data:    ingestData{ID: doc.ID, Title: doc.Title},
	}, nil
}

// writeNew 生成不撞车的 id 并原子落盘（同目录 temp + rename）。
func (t *ingestTool) writeNew(doc *document) (string, error) {
	slug := slugify(doc.Title)
	var lastErr error
	for i := 0; i < idAttempts; i++ {
		suffixHex, err := randomHex(randomHexChars)
		if err != nil {
			return "", fmt.Errorf("生成 id 失败: %w", err)
		}
		id := slug + "-" + suffixHex
		doc.ID = id
		p := filepath.Join(t.dir, id+suffix)

		// 文件已存在就换一个随机段，绝不覆盖别人的记忆。
		if _, err := os.Stat(p); err == nil {
			lastErr = fmt.Errorf("id 已存在: %s", id)
			continue
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("检查 id 失败: %w", err)
		}

		if err := writeFileAtomic(p, doc); err != nil {
			return "", err
		}
		return p, nil
	}
	return "", fmt.Errorf("连续 %d 次没拿到可用 id: %w", idAttempts, lastErr)
}

// ---------------------------------------------------------------------------
// list
// ---------------------------------------------------------------------------

type listTool struct{ dir string }

func (t *listTool) Name() string { return "hindsight_list_knowledge_pages" }

func (t *listTool) Description() string {
	return "列出本机存过的记忆（id、标题、字数，新→旧）；不确定有哪些记忆时先用它。"
}

func (t *listTool) Schema() json.RawMessage { return listSchema }

// Caps 全空：只读目录。
func (t *listTool) Caps() spi.Caps { return spi.Caps{} }

type pageData struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	BodyChars int    `json:"body_chars"`
	Created   int64  `json:"created"`
}

type listData struct {
	Count int        `json:"count"`
	Pages []pageData `json:"pages"`
}

func (t *listTool) Exec(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	// 无参工具仍校验：Schema 是 additionalProperties:false，吃了多余参数就得说出来。
	if s := strings.TrimSpace(string(raw)); s != "" && s != "null" {
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(raw, &probe); err != nil {
			return spi.Result{Hint: "hindsight_list_knowledge_pages 不接受参数：直接发 {} 即可"},
				fmt.Errorf("参数不合法: %w", err)
		}
		if len(probe) > 0 {
			keys := make([]string, 0, len(probe))
			for k := range probe {
				keys = append(keys, k)
			}
			return spi.Result{Hint: "hindsight_list_knowledge_pages 不接受参数（收到 " + strings.Join(keys, ",") + "）：直接发 {} 即可"},
				fmt.Errorf("多余参数: %s", strings.Join(keys, ","))
		}
	}
	if err := ctx.Err(); err != nil {
		return spi.Result{Hint: "这次调用被取消了；原参数重发一次即可"}, err
	}

	docs, err := loadDocs(t.dir)
	if err != nil {
		return spi.Result{Hint: "读 " + slash(t.dir) + " 失败：先确认这个目录可读"}, err
	}
	if len(docs) == 0 {
		return spi.Result{
			Text:    "还没有任何记忆",
			Summary: "0 条记忆",
			Data:    listData{Count: 0, Pages: []pageData{}},
			Hint:    "用 hindsight_ingest_document 存第一条",
		}, nil
	}

	// created 倒序；同一秒内按 id 正序，保证两次调用输出一致（可重放）。
	sort.SliceStable(docs, func(i, j int) bool {
		if docs[i].Created != docs[j].Created {
			return docs[i].Created > docs[j].Created
		}
		return docs[i].ID < docs[j].ID
	})

	var b strings.Builder
	pages := make([]pageData, 0, len(docs))
	for i, d := range docs {
		chars := utf8.RuneCountInString(d.Body)
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%s\t%s\t（%d 字）", d.ID, d.Title, chars)
		pages = append(pages, pageData{ID: d.ID, Title: d.Title, BodyChars: chars, Created: d.Created})
	}

	return spi.Result{
		Text:    b.String(),
		Summary: fmt.Sprintf("%d 条记忆", len(docs)),
		Data:    listData{Count: len(docs), Pages: pages},
	}, nil
}

// ---------------------------------------------------------------------------
// 存储
// ---------------------------------------------------------------------------

// loadDocs 读目录里的全部文档。目录（或 home）不存在 → 空列表，不是错误。
// 单独一个坏文件不拖垮整张单子：它不是我们能解释的文档，跳过。
func loadDocs(dir string) ([]document, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	docs := make([]document, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), suffix) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var d document
		if err := json.Unmarshal(b, &d); err != nil {
			continue
		}
		if d.ID == "" || d.Title == "" {
			continue
		}
		docs = append(docs, d)
	}
	return docs, nil
}

// writeFileAtomic 先写同目录临时文件再 rename：中途失败不会留下半个 json。
func writeFileAtomic(path string, doc *document) error {
	b, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("序列化文档失败: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, fileMode); err != nil {
		return fmt.Errorf("写临时文件失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("提交文档失败: %w", err)
	}
	return nil
}

// slugify：小写、非 [a-z0-9] 一律换成 '-'、连续 '-' 折叠、去首尾 '-'、截到 40。
// 全中文标题会退化成空串，此时用 "memory" 兜底——id 不能只剩随机段。
func slugify(title string) string {
	s := strings.ToLower(title)
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > maxSlugChars {
		out = strings.TrimRight(out[:maxSlugChars], "-")
	}
	if out == "" {
		return "memory"
	}
	return out
}

// randomHex 返回 n 位小写十六进制。id 尾部用它，避免同标题覆盖。
func randomHex(n int) (string, error) {
	buf := make([]byte, (n+1)/2)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf)[:n], nil
}

// slash 把路径统一成正斜杠再交给模型（反斜杠会被误读成转义）。
func slash(p string) string { return filepath.ToSlash(p) }

// Module 返回装配用的模块描述。
func Module(home string) kernel.Module {
	return kernel.Module{
		ID:       "tool-hindsight",
		Backend:  kernel.BackendStatic,
		Requires: []string{"tools"},
		Register: func(s *kernel.Scope) error {
			reg, err := kernel.Inject(s, spi.ToolsKey)
			if err != nil {
				return err
			}
			dir := filepath.Join(home, "memory")
			if err := reg.Register(&ingestTool{dir: dir}); err != nil {
				return err
			}
			return reg.Register(&listTool{dir: dir})
		},
	}
}
