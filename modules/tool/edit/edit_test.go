package edit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/spi"
	"dshgo/modules/fs/local"
)

func newTool(t *testing.T) (*Tool, *local.FS, string) {
	t.Helper()
	root := t.TempDir()
	fs, err := local.New(root)
	if err != nil {
		t.Fatal(err)
	}
	return &Tool{fs: fs}, fs, root
}

func fixture(t *testing.T, root, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func call(t *testing.T, tool *Tool, file, oldS, newS string, all bool) (spi.Result, error) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{
		"file_path": file, "old_string": oldS, "new_string": newS, "replace_all": all,
	})
	return tool.Exec(context.Background(), raw)
}

func TestSingleReplace(t *testing.T) {
	tool, fs, root := newTool(t)
	fixture(t, root, "hello world\n")
	if _, err := fs.Read("f.txt"); err != nil {
		t.Fatal(err)
	}
	res, err := call(t, tool, "f.txt", "world", "gopher", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Data.(payload).Replacements != 1 {
		t.Fatalf("替换数 = %d", res.Data.(payload).Replacements)
	}
	got, _ := os.ReadFile(filepath.Join(root, "f.txt"))
	if string(got) != "hello gopher\n" {
		t.Fatalf("内容 = %q", got)
	}
}

func TestAmbiguousWithoutReplaceAllRejected(t *testing.T) {
	tool, fs, root := newTool(t)
	fixture(t, root, "a\nb\na\n")
	if _, err := fs.Read("f.txt"); err != nil {
		t.Fatal(err)
	}
	res, err := call(t, tool, "f.txt", "a", "x", false)
	if err == nil {
		t.Fatal("多处匹配且未开 replace_all 必须拒绝")
	}
	if !strings.Contains(res.Hint, "2 次") || !strings.Contains(res.Hint, "replace_all") {
		t.Fatalf("Hint 应说清次数与出路，得到 %q", res.Hint)
	}
}

func TestReplaceAll(t *testing.T) {
	tool, fs, root := newTool(t)
	fixture(t, root, "a\nb\na\n")
	if _, err := fs.Read("f.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, tool, "f.txt", "a", "x", true); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "f.txt"))
	if string(got) != "x\nb\nx\n" {
		t.Fatalf("内容 = %q", got)
	}
}

func TestMissingOldStringHasHint(t *testing.T) {
	tool, fs, root := newTool(t)
	fixture(t, root, "abc\n")
	if _, err := fs.Read("f.txt"); err != nil {
		t.Fatal(err)
	}
	res, err := call(t, tool, "f.txt", "zzz", "y", false)
	if err == nil {
		t.Fatal("找不到 old_string 必须报错")
	}
	if !strings.Contains(res.Hint, "read") {
		t.Fatalf("Hint 应引导先 read，得到 %q", res.Hint)
	}
}

func TestEditWithoutReadRejected(t *testing.T) {
	tool, _, root := newTool(t)
	fixture(t, root, "abc\n")
	res, err := call(t, tool, "f.txt", "abc", "x", false)
	if err == nil {
		t.Fatal("没读过就改必须被拒绝")
	}
	if res.Hint == "" {
		t.Fatal("拒绝必须带 Hint")
	}
}

func TestEmptyOldStringRejected(t *testing.T) {
	tool, _, _ := newTool(t)
	if _, err := call(t, tool, "f.txt", "", "x", false); err == nil {
		t.Fatal("空 old_string 必须报错")
	}
}

func TestNoOpEditRejected(t *testing.T) {
	tool, _, _ := newTool(t)
	if _, err := call(t, tool, "f.txt", "a", "a", false); err == nil {
		t.Fatal("无变化的编辑应被拒绝（避免噪声工具调用）")
	}
}

// 没读过就改：错误文本必须和 DSH 一样，一句话说清下一步。
// 真实语料里这是 edit 失败的最大来源（853 次调用 / 75 次失败，其中 29 次是它）。
func TestEditWithoutReadSaysHowToFix(t *testing.T) {
	tl, _, root := newTool(t)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := tl.Exec(context.Background(), json.RawMessage(`{"file_path":"a.txt","old_string":"hello","new_string":"world"}`))
	if err == nil {
		t.Fatal("没读过就改必须被拒绝")
	}
	if !strings.Contains(err.Error(), "file has not been read") ||
		!strings.Contains(err.Error(), "read the file, then retry") {
		t.Fatalf("措辞不一致（模型得靠这句话学会下一步）: %v", err)
	}
}
