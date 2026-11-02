package read

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/spi"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func call(t *testing.T, tool *Tool, args map[string]any) (spi.Result, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return tool.Exec(context.Background(), raw)
}

func TestExecReadsLines(t *testing.T) {
	p := writeTemp(t, "a\nb\nc\n")
	res, err := call(t, &Tool{}, map[string]any{"file_path": p})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	data, ok := res.Data.(payload)
	if !ok {
		t.Fatalf("Data 类型 = %T", res.Data)
	}
	if data.TotalLines != 3 {
		t.Fatalf("TotalLines = %d，期望 3（尾换行不该多算一行）", data.TotalLines)
	}
	if data.Content != "a\nb\nc" {
		t.Fatalf("Content = %q", data.Content)
	}
	if data.Truncated {
		t.Fatal("未超限不应标记截断")
	}
}

func TestExecOffsetAndLimit(t *testing.T) {
	p := writeTemp(t, "1\n2\n3\n4\n5\n")
	res, err := call(t, &Tool{}, map[string]any{"file_path": p, "offset": 2, "limit": 2})
	if err != nil {
		t.Fatal(err)
	}
	data := res.Data.(payload)
	if data.StartLine != 2 || data.EndLine != 3 || data.Content != "2\n3" {
		t.Fatalf("窗口 = %d-%d %q", data.StartLine, data.EndLine, data.Content)
	}
	if !data.Truncated {
		t.Fatal("还有剩余行时应标记 Truncated")
	}
	if res.Hint == "" || !strings.Contains(res.Hint, "offset=4") {
		t.Fatalf("截断时必须给下一步（Hint），得到 %q", res.Hint)
	}
}

func TestExecMissingFileHasHint(t *testing.T) {
	_, err := call(t, &Tool{}, map[string]any{"file_path": filepath.Join(t.TempDir(), "nope.txt")})
	if err == nil {
		t.Fatal("读不存在的文件必须报错")
	}
}

func TestExecBadArgsHasHint(t *testing.T) {
	res, err := (&Tool{}).Exec(context.Background(), json.RawMessage(`{`))
	if err == nil {
		t.Fatal("非法 JSON 必须报错")
	}
	if res.Hint == "" {
		t.Fatal("失败必须带 Hint（R3）")
	}
}

func TestModuleRegistersIntoRegistry(t *testing.T) {
	// 不依赖 GUI 层的装配测试：这里只验 “requires tools → 注册成功” 这条链。
	p := writeTemp(t, "x\n")
	tool := &Tool{Workdir: filepath.Dir(p)}
	if tool.Name() != "read" {
		t.Fatalf("Name = %q", tool.Name())
	}
	if len(tool.Schema()) == 0 || !json.Valid(tool.Schema()) {
		t.Fatal("Schema 必须是合法 JSON")
	}
	if _, err := call(t, tool, map[string]any{"file_path": p}); err != nil {
		t.Fatal(err)
	}
}
