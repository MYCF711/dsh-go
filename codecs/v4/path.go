package v4

import (
	"fmt"
	"strings"
	"unicode/utf16"
)

// ProjectKey 把 cwd 变成项目目录名。规则逐字对齐（research/21 §1）：
//
//	1. `/` `\` `:` 各变一个 `-`，连续 `-` 折叠；
//	2. ASCII [A-Za-z0-9._-] 且非 `~` 原样，其余每个 **UTF-16 码元** → `~XXXX`（大写 4 位 hex）；
//	3. 去前导 `-`；空 → `root`；截 251；
//	4. 包成 `--<key>--`。
func ProjectKey(cwd string) string {
	var b strings.Builder
	for _, r := range cwd {
		if r == '/' || r == '\\' || r == ':' {
			b.WriteByte('-')
			continue
		}
		b.WriteRune(r)
	}

	var collapsed strings.Builder
	for i, r := range b.String() {
		if r == '-' && i > 0 && b.String()[i-1] == '-' {
			continue
		}
		collapsed.WriteRune(r)
	}

	key := escapeUTF16(collapsed.String())
	key = strings.TrimLeft(key, "-")
	if key == "" {
		key = "root"
	}
	if len(key) > 251 {
		key = key[:251]
	}
	return "--" + key + "--"
}

// EncodeSegment 转义会话 id 之类的单段文件名。
//
// `.` 与 `..` 必须特判，否则会变成目录穿越。
func EncodeSegment(id string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("会话 id 为空")
	}
	switch id {
	case ".":
		return "~002E", nil
	case "..":
		return "~002E~002E", nil
	}
	return escapeUTF16(id), nil
}

// escapeUTF16 逐 UTF-16 码元转义：ASCII 安全集原样，其余（含 `~`）→ `~XXXX`。
func escapeUTF16(s string) string {
	var out strings.Builder
	for _, u := range utf16.Encode([]rune(s)) {
		c := rune(u)
		safe := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-'
		if safe {
			out.WriteRune(c)
			continue
		}
		out.WriteString(fmt.Sprintf("~%04X", u))
	}
	return out.String()
}
