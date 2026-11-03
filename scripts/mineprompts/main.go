package main

// mineprompts：从真实会话里抠出用户的开场 prompt，用于"用真实任务重放"这类实验。
//
// 为什么要有它：上一轮我用正则去抠，v4 里正文嵌在 data.message.content 的块数组里，
// 多一层嵌套就全不命中——脚本跑了 2 分钟输出 0 条。这里改成递归找 text，
// 与 v4.ReadSummary 用的是同一套思路：**不猜层级**。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"dshgo/codecs/v4"
)

// firstText 递归找第一个 "text" 字段。层级不写死：上游以后多包一层也不会失效。
func firstText(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	var walk func(any) string
	walk = func(x any) string {
		switch t := x.(type) {
		case map[string]any:
			if s, ok := t["text"].(string); ok && strings.TrimSpace(s) != "" {
				return s
			}
			for _, v2 := range t {
				if s := walk(v2); s != "" {
					return s
				}
			}
		case []any:
			for _, v2 := range t {
				if s := walk(v2); s != "" {
					return s
				}
			}
		}
		return ""
	}
	return walk(v)
}

func main() {
	root := os.Args[1]
	max := 8
	var out []string
	seen := map[string]bool{}
	files, _ := filepath.Glob(filepath.Join(root, "*", "*", "session.v4.jsonl.zstd"))
	sort.Sort(sort.Reverse(sort.StringSlice(files)))
	skippedNoCwd := 0
	for _, f := range files {
		// 只留"原工作目录现在还活着"的会话。
		//
		// 为什么必须先过这一关：上一轮抠出来的 8 条里有一半指向
		// E:\Downloads\sub2api_poc 这类已经不在的路径。拿它们重放，
		// 失败原因是"路径不存在"而不是被测的那个变量——**测的是环境**。
		h, herr := v4.ReadHeader(f)
		if herr != nil {
			skippedNoCwd++
			continue
		}
		if h.Cwd == "" {
			skippedNoCwd++
			continue
		}
		if st, serr := os.Stat(h.Cwd); serr != nil || !st.IsDir() {
			skippedNoCwd++
			continue
		}
		lg, err := v4.ReadFile(f)
		if err != nil {
			continue
		}
		for _, ev := range lg.Events {
			if ev.Type != "user/message" {
				continue
			}
			t := strings.TrimSpace(firstText(ev.Data))
			if len([]rune(t)) < 20 || len([]rune(t)) > 200 {
				continue
			}
			key := t[:20]
			if seen[key] {
				break
			}
			seen[key] = true
			out = append(out, t)
			break
		}
		if len(out) >= max {
			break
		}
	}
	for i, t := range out {
		fmt.Printf("%d. %s\n", i+1, strings.ReplaceAll(t, "\n", " "))
	}
	fmt.Fprintf(os.Stderr, "可重放: %d 条；cwd 已不在而跳过: %d 个会话\n", len(out), skippedNoCwd)
	if err := os.WriteFile("tmp/realprompts.txt", []byte(strings.Join(out, "\n")), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}
