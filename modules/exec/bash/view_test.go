package bash

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// 模型看到的正文必须是**原始输出**，不是 JSON 包裹。
//
// 之前走的是 Summary + JSON(Data)：换行被转义、引号被转义，外面还裹着
// exit_code / cwd 这些模型用不上的字段。bash 占工具回显的 64.5%，
// 这层浪费在它身上被放大得最厉害。
func TestShellViewIsRawTextNotJSON(t *testing.T) {
	requireBash(t)
	svc := NewService(t.TempDir())
	defer svc.Close()
	tool := &shellTool{svc: svc, budget: 10 * time.Second}

	res, err := tool.Exec(context.Background(),
		json.RawMessage(`{"command":"seq 3","description":"三行"}`))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(res.Text, "[exit 0]") {
		t.Fatalf("正文应以 [exit N] 开头，得到 %q", res.Text)
	}

	// 真换行：三行数字之间是 "\n" 一个字节，不是两个字符的反斜杠 n。
	want := "1" + string(rune(10)) + "2" + string(rune(10)) + "3"
	if !strings.Contains(res.Text, want) {
		t.Fatalf("正文里必须是真换行（不是转义）: %q", res.Text)
	}

	// 不能有 JSON 外壳
	if strings.Contains(res.Text, `"exit_code"`) || strings.Contains(res.Text, `"output"`) {
		t.Fatalf("正文里不该有 JSON 外壳: %q", res.Text)
	}
}

// 外置之后的正文也要是原始文本（不是 JSON 里的 preview）。
func TestSpilledShellViewStaysRaw(t *testing.T) {
	requireBash(t)
	svc := NewService(t.TempDir())
	defer svc.Close()
	tool := &shellTool{svc: svc, budget: 20 * time.Second}

	res, err := tool.Exec(context.Background(),
		json.RawMessage(`{"command":"seq 1 50","description":"50 行"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Text, `"output"`) {
		t.Fatalf("正文里不该有 JSON 外壳: %q", res.Text)
	}
}
