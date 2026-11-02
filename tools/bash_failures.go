//go:build ignore

// 把 bash 的失败样本全部打出来，看失败到底分几类。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"dshgo/codecs/v4"
)

type res struct {
	Message struct {
		ToolCallID string `json:"toolCallId"`
		IsError    bool   `json:"isError"`
		Content    []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
}

func main() {
	names := map[string]string{}
	var fails []string
	filepath.Walk(os.Args[1], func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".zstd") {
			return nil
		}
		log, err := v4.ReadFile(p)
		if err != nil {
			return nil
		}
		for _, ev := range log.Events {
			switch ev.Type {
			case "tool/call":
				var d struct {
					CallID string `json:"callId"`
					Name   string `json:"name"`
				}
				json.Unmarshal(ev.Data, &d)
				names[d.CallID] = d.Name
			case "tool/result":
				var r res
				if json.Unmarshal(ev.Data, &r) != nil || !r.Message.IsError {
					continue
				}
				if names[r.Message.ToolCallID] != "bash" {
					continue
				}
				var b strings.Builder
				for _, c := range r.Message.Content {
					b.WriteString(c.Text)
				}
				fails = append(fails, strings.TrimSpace(b.String()))
			}
		}
		return nil
	})
	sort.Strings(fails)
	cats := map[string]int{}
	for _, f := range fails {
		key := f
		if i := strings.Index(key, "\n"); i > 0 {
			key = key[:i]
		}
		if len([]rune(key)) > 70 {
			key = string([]rune(key)[:70])
		}
		cats[key]++
	}
	fmt.Printf("bash 失败共 %d 条，按首行归类：\n", len(fails))
	type kv struct {
		k string
		n int
	}
	var list []kv
	for k, n := range cats {
		list = append(list, kv{k, n})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].n > list[j].n })
	for _, e := range list {
		fmt.Printf("%4d  %s\n", e.n, e.k)
	}
}
