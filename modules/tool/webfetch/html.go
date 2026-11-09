package webfetch

import (
	"strconv"
	"strings"
)

// HTML → 模型可读文本。
//
// 官方 dsh-tool-web 用 turndown（DOM + GFM 插件）把抓到的 HTML 转成 markdown；
// 宿主不引第三方 DOM 依赖，这里实现一个**保守的**子集：
//
//   - 丢掉非正文元素：script / style / noscript / template / iframe / object /
//     embed / head，以及 hidden、aria-hidden=true、style 里 display:none /
//     visibility:hidden 的子树（否则模型会读到"页面里写着但人看不见"的内容）；
//   - 保留结构：h1–h6 → #、p/div 分段、li → "- "、a → [text](href)、
//     pre → 代码块、code → 行内反引号、blockquote → "> "、hr → "---"；
//   - 解码实体（命名 + 数字），折叠空白与空行。
//
// 安全边界与官方一致：嵌套深度超过 512 层时**整份省略**（返回占位说明），
// 不让病态输入把转换拖成超线性；宁可少给内容，不可把原始标记塞给模型。

const maxHTMLDepth = 512

// omittedHTMLText 是无法安全转换时的占位（官方同义文案）。
const omittedHTMLText = "[HTML content omitted: unable to convert safely.]"

var voidElements = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true,
	"img": true, "input": true, "link": true, "meta": true, "param": true,
	"source": true, "track": true, "wbr": true,
}

// skipElements 是整棵子树都不进正文的元素。
var skipElements = map[string]bool{
	"script": true, "style": true, "noscript": true, "template": true,
	"iframe": true, "object": true, "embed": true, "head": true,
	"svg": true, "canvas": true, "audio": true, "video": true,
}

// blockElements 是前后需要断开行的元素。
var blockElements = map[string]bool{
	"p": true, "div": true, "section": true, "article": true, "header": true,
	"footer": true, "main": true, "aside": true, "nav": true, "figure": true,
	"figcaption": true, "form": true, "fieldset": true, "address": true,
	"table": true, "thead": true, "tbody": true, "tfoot": true, "tr": true,
	"ul": true, "ol": true, "dl": true, "dt": true, "dd": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"blockquote": true, "pre": true, "hr": true, "li": true,
}

// htmlToMarkdown 把一份 HTML 文档转成模型可读的 markdown 子集。
func htmlToMarkdown(document string) string {
	if strings.TrimSpace(document) == "" {
		return ""
	}
	var out strings.Builder
	depth := 0
	linkHref := ""
	preDepth := 0
	inSkip := 0
	// skipTag 是当前被跳过元素的标签名；inSkip 是同类嵌套计数。
	skipTag := ""

	i := 0
	for i < len(document) {
		if document[i] != '<' {
			next := strings.IndexByte(document[i:], '<')
			if next < 0 {
				next = len(document) - i
			}
			if inSkip == 0 {
				writeHTMLText(&out, document[i:i+next], preDepth > 0)
			}
			i += next
			continue
		}
		if strings.HasPrefix(document[i:], "<!--") {
			end := strings.Index(document[i+4:], "-->")
			if end < 0 {
				break
			}
			i += 4 + end + 3
			continue
		}
		end := findTagEnd(document, i)
		if end < 0 {
			break
		}
		name, closing, attrs, selfClosing := parseHTMLTag(document[i+1 : end])
		i = end + 1
		if name == "" {
			continue
		}
		if inSkip > 0 {
			if closing && name == skipTag {
				inSkip--
				if inSkip == 0 {
					skipTag = ""
				}
			} else if !closing && name == skipTag && !selfClosing && !voidElements[name] {
				inSkip++
			}
			continue
		}
		if !closing {
			if skipElements[name] || htmlElementHidden(name, attrs) {
				if !selfClosing && !voidElements[name] {
					inSkip = 1
					skipTag = name
				}
				continue
			}
			if !voidElements[name] && !selfClosing {
				depth++
				if depth > maxHTMLDepth {
					return omittedHTMLText
				}
			}
		} else if !voidElements[name] {
			depth--
			if depth < 0 {
				depth = 0
			}
		}

		switch {
		case closing:
			switch {
			case isHeading(name) || blockElements[name]:
				// li 的结束不补换行：下一条 "<li>" 自己会断行，否则相邻条目之间
				// 会多出一个空行（松列表）。
				if name != "li" {
					out.WriteString("\n")
				}
			case name == "a" && linkHref != "":
				out.WriteString("](")
				out.WriteString(linkHref)
				out.WriteString(")")
				linkHref = ""
			case name == "code" && preDepth == 0:
				out.WriteString("`")
			case name == "strong" || name == "b":
				out.WriteString("**")
			case name == "em" || name == "i":
				out.WriteString("*")
			case name == "pre":
				out.WriteString("\n```\n")
				if preDepth > 0 {
					preDepth--
				}
			}
		case name == "a":
			linkHref = htmlAttr(attrs, "href")
			if linkHref != "" && !strings.HasPrefix(linkHref, "#") {
				out.WriteString("[")
			} else {
				linkHref = ""
			}
		case name == "br":
			out.WriteString("\n")
		case name == "hr":
			out.WriteString("\n\n---\n\n")
		case isHeading(name):
			level := int(name[1] - '0')
			out.WriteString("\n\n")
			out.WriteString(strings.Repeat("#", level))
			out.WriteString(" ")
		case name == "li":
			out.WriteString("\n- ")
		case name == "td" || name == "th":
			out.WriteString(" ")
		case name == "pre":
			if preDepth == 0 {
				out.WriteString("\n```\n")
			}
			preDepth++
		case name == "code" && preDepth == 0:
			out.WriteString("`")
		case name == "strong" || name == "b":
			out.WriteString("**")
		case name == "em" || name == "i":
			out.WriteString("*")
		case name == "blockquote":
			out.WriteString("\n> ")
		case blockElements[name]:
			out.WriteString("\n")
		}
	}
	return normalizeMarkdown(out.String())
}

func isHeading(name string) bool {
	return len(name) == 2 && name[0] == 'h' && name[1] >= '1' && name[1] <= '6'
}

// findTagEnd 找标签的 '>'（跳过引号内的 '>'）。
func findTagEnd(document string, start int) int {
	quote := byte(0)
	for i := start + 1; i < len(document); i++ {
		char := document[i]
		if quote != 0 {
			if char == quote {
				quote = 0
			}
			continue
		}
		switch char {
		case '"', '\'':
			quote = char
		case '>':
			return i
		}
	}
	return -1
}

// parseHTMLTag 解析 `tag ...`（不含尖括号）为名字、是否闭合、属性串、是否自闭合。
func parseHTMLTag(raw string) (name string, closing bool, attrs string, selfClosing bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false, "", false
	}
	if trimmed[0] == '/' {
		closing = true
		trimmed = strings.TrimSpace(trimmed[1:])
	}
	if strings.HasSuffix(trimmed, "/") {
		selfClosing = true
		trimmed = strings.TrimSpace(trimmed[:len(trimmed)-1])
	}
	end := 0
	for end < len(trimmed) {
		char := trimmed[end]
		if char == ' ' || char == '\t' || char == '\n' || char == '\r' || char == '/' {
			break
		}
		end++
	}
	name = strings.ToLower(trimmed[:end])
	attrs = strings.TrimSpace(trimmed[end:])
	return name, closing, attrs, selfClosing
}

// htmlAttr 取一个属性值（支持双引号/单引号/裸值）。
func htmlAttr(attrs, want string) string {
	lower := strings.ToLower(attrs)
	index := 0
	for {
		position := strings.Index(lower[index:], want)
		if position < 0 {
			return ""
		}
		position += index
		// 必须是独立属性名：前一个字符是空白，后一个字符是 '='。
		before := position == 0 || isHTMLSpace(lower[position-1])
		after := position+len(want) < len(lower) && lower[position+len(want)] == '='
		if before && after {
			value := strings.TrimSpace(attrs[position+len(want)+1:])
			if value == "" {
				return ""
			}
			switch value[0] {
			case '"':
				if end := strings.IndexByte(value[1:], '"'); end >= 0 {
					return value[1 : 1+end]
				}
				return ""
			case '\'':
				if end := strings.IndexByte(value[1:], '\''); end >= 0 {
					return value[1 : 1+end]
				}
				return ""
			default:
				end := 0
				for end < len(value) && !isHTMLSpace(value[end]) {
					end++
				}
				return value[:end]
			}
		}
		index = position + len(want)
	}
}

func isHTMLSpace(char byte) bool {
	return char == ' ' || char == '\t' || char == '\n' || char == '\r' || char == '/' || char == '>'
}

// htmlElementHidden 判定一个开始标签是否属于"人看不见"的子树。
func htmlElementHidden(name, attrs string) bool {
	lower := strings.ToLower(attrs)
	if strings.Contains(lower, " hidden") || strings.HasPrefix(lower, "hidden") {
		return true
	}
	if aria := strings.ToLower(htmlAttr(attrs, "aria-hidden")); aria == "true" {
		return true
	}
	if name == "input" && strings.ToLower(htmlAttr(attrs, "type")) == "hidden" {
		return true
	}
	style := strings.ToLower(htmlAttr(attrs, "style"))
	for _, declaration := range strings.Split(style, ";") {
		declaration = strings.TrimSpace(declaration)
		if declaration == "" {
			continue
		}
		if strings.HasPrefix(declaration, "display:none") || strings.HasPrefix(declaration, "display: none") {
			return true
		}
		if strings.HasPrefix(declaration, "visibility:hidden") || strings.HasPrefix(declaration, "visibility: hidden") {
			return true
		}
	}
	return false
}

// writeHTMLText 写一段文本节点：折叠空白（pre 内保留换行），解码实体。
func writeHTMLText(out *strings.Builder, text string, preserve bool) {
	decoded := decodeHTMLEntities(text)
	if preserve {
		out.WriteString(decoded)
		return
	}
	var collapsed strings.Builder
	space := false
	for _, char := range decoded {
		switch char {
		case ' ', '\t', '\n', '\r', '\f', '\v', '\u00a0':
			space = true
		default:
			if space {
				collapsed.WriteByte(' ')
				space = false
			}
			collapsed.WriteRune(char)
		}
	}
	out.WriteString(collapsed.String())
}

// decodeHTMLEntities 解码命名与数字实体；未知实体原样保留。
func decodeHTMLEntities(text string) string {
	if !strings.Contains(text, "&") {
		return text
	}
	var out strings.Builder
	for i := 0; i < len(text); i++ {
		if text[i] != '&' {
			out.WriteByte(text[i])
			continue
		}
		end := strings.IndexByte(text[i:], ';')
		if end < 0 || end > 10 {
			out.WriteByte(text[i])
			continue
		}
		entity := text[i+1 : i+end]
		decoded, ok := decodeHTMLEntity(entity)
		if !ok {
			out.WriteByte(text[i])
			continue
		}
		out.WriteString(decoded)
		i += end
	}
	return out.String()
}

func decodeHTMLEntity(entity string) (string, bool) {
	if strings.HasPrefix(entity, "#") {
		number := entity[1:]
		base := 10
		if len(number) > 1 && (number[0] == 'x' || number[0] == 'X') {
			base = 16
			number = number[1:]
		}
		value, err := strconv.ParseInt(number, base, 32)
		if err != nil || value <= 0 || value > 0x10FFFF {
			return "", false
		}
		return string(rune(value)), true
	}
	named := map[string]string{
		"amp": "&", "lt": "<", "gt": ">", "quot": "\"", "apos": "'", "nbsp": " ",
		"copy": "©", "reg": "®", "trade": "™", "hellip": "…", "mdash": "—",
		"ndash": "–", "lsquo": "‘", "rsquo": "’", "ldquo": "“", "rdquo": "”",
		"middot": "·", "deg": "°", "times": "×", "divide": "÷", "laquo": "«",
		"raquo": "»", "sect": "§", "para": "¶", "euro": "€", "pound": "£",
		"yen": "¥", "cent": "¢", "plusmn": "±", "frac12": "½", "bull": "•",
	}
	value, ok := named[strings.ToLower(entity)]
	return value, ok
}

// normalizeMarkdown 折叠空白：去行尾空格、压掉 3+ 连续换行、去首尾空行。
func normalizeMarkdown(text string) string {
	lines := strings.Split(text, "\n")
	var out []string
	blank := 0
	for _, line := range lines {
		line = strings.TrimRight(line, " \t\r")
		if strings.TrimSpace(line) == "" {
			blank++
			if blank > 1 {
				continue
			}
			out = append(out, "")
			continue
		}
		blank = 0
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
