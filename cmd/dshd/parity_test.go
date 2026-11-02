package main

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"dshgo/codecs/v4"
	"os"
)

// dshTool 是 DSH 真实会话里 `request/header.header.tools[]` 的形状。
type dshTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// loadDSHTools 从真实会话里抽出「DSH 发给模型的那张工具表」。
//
// 这是对拍的地基：`request/header.header.tools` 就是模型看到的东西，
// 不是我们从文档猜的。
func loadDSHTools(t *testing.T) map[string]dshTool {
	t.Helper()
	root := os.Getenv("DSH_HOME")
	if root == "" {
		root = `D:\DeepSeek Harness\home`
	}
	sessions := filepath.Join(root, "sessions")
	if _, err := os.Stat(sessions); err != nil {
		t.Skip("本机没有真实会话目录")
	}
	out := map[string]dshTool{}
	err := filepath.Walk(sessions, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".zstd") || len(out) >= 60 {
			return nil
		}
		raw, err := v4.ReadFile(p)
		if err != nil {
			return nil
		}
		for _, ev := range raw.Events {
			if ev.Type != "request/header" {
				continue
			}
			var d struct {
				Header struct {
					Tools []dshTool `json:"tools"`
				} `json:"header"`
			}
			if json.Unmarshal(ev.Data, &d) != nil {
				continue
			}
			for _, tl := range d.Header.Tools {
				if tl.Name != "" {
					if _, ok := out[tl.Name]; !ok {
						out[tl.Name] = tl
					}
				}
			}
			break
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Skip("没有找到带 request/header 的会话")
	}
	return out
}

type schema struct {
	Properties map[string]any `json:"properties"`
	Required   []string       `json:"required"`
}

func parseSchema(raw json.RawMessage) schema {
	var s schema
	_ = json.Unmarshal(raw, &s)
	return s
}

// TestToolParityWithDSH 是「工具语义对拍」的门。
//
// 判定标准刻意窄，只拦**会导致模型调用失败**的差异：
//  1. 我们有的工具，DSH 里必须有同名工具（否则按 DSH 习惯写的调用会打空）；
//  2. DSH 标为 required 的参数，我们的 schema 必须认（否则模型照 DSH 传参会被我们拒）。
//
// 描述措辞、非必需参数的多少只报告，不判失败。
func TestToolParityWithDSH(t *testing.T) {
	dsh := loadDSHTools(t)
	ours := buildToolTableMust(t)

	var problems []string
	report := make([]string, 0, len(ours))

	for _, o := range ours {
		d, ok := dsh[o.Name]
		if !ok {
			problems = append(problems, o.Name+": DSH 里没有同名工具")
			report = append(report, o.Name+"\t—\t缺失")
			continue
		}
		var oursSchema, dshSchema schema
		_ = json.Unmarshal(o.Parameters, &oursSchema)
		dshSchema = parseSchema(d.Parameters)

		missing := []string{}
		for _, req := range dshSchema.Required {
			if _, ok := oursSchema.Properties[req]; !ok {
				missing = append(missing, req)
			}
		}
		if len(missing) > 0 {
			problems = append(problems,
				o.Name+": DSH 要求但我们不认的参数 "+strings.Join(missing, ","))
		}

		extra := []string{}
		for p := range oursSchema.Properties {
			if _, ok := dshSchema.Properties[p]; !ok {
				extra = append(extra, p)
			}
		}
		sort.Strings(extra)
		status := "一致"
		if len(missing) > 0 {
			status = "缺参"
		} else if len(extra) > 0 {
			status = "超集"
		}
		report = append(report, o.Name+"\t"+status+"\t额外: "+strings.Join(extra, ","))
	}

	for _, line := range report {
		t.Logf("%s", line)
	}
	if len(problems) > 0 {
		t.Fatalf("与 DSH 的工具契约不一致：\n  %s", strings.Join(problems, "\n  "))
	}
}
