package edit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 官方 `fs.diffBasisMaxBytes`（默认 10 MiB）：编辑落盘前先把**原文**留一份基线快照，
// 让"这一次到底改了哪些字节"事后可复核（edit 只回 bytes_before/after 两个数字，
// 数字说明不了改了什么）。超过上限就不做快照——不拿磁盘换证据。
//
// 先红：当前 edit 没有基线快照，payload 里也没有这两个字段。
func TestDiffBasisSnapshotRecorded(t *testing.T) {
	tool, fs, root := newTool(t)
	fixture(t, root, "alpha\nbeta\n")
	if _, err := fs.Read("f.txt"); err != nil { // edit 要求先读后写（既有防陈旧覆盖）
		t.Fatal(err)
	}

	res, err := call(t, tool, "f.txt", "beta", "gamma", false)
	if err != nil {
		t.Fatalf("单处替换不该失败：%v", err)
	}
	p, ok := res.Data.(payload)
	if !ok {
		t.Fatalf("Data 形状变了：%T", res.Data)
	}
	if p.DiffBasis == "" {
		t.Fatal("小文件必须在落盘前留 diff 基线快照（fs.diffBasisMaxBytes）")
	}
	raw, err := os.ReadFile(p.DiffBasis)
	if err != nil {
		t.Fatalf("基线快照读不出来：%v", err)
	}
	if string(raw) != "alpha\nbeta\n" {
		t.Fatalf("基线快照必须是**原文**，拿到 %q", string(raw))
	}
	now, err := os.ReadFile(filepath.Join(root, "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(now) != "alpha\ngamma\n" {
		t.Fatalf("文件本体要照常改掉，拿到 %q", string(now))
	}
	if !strings.Contains(res.Summary, "基线") && !strings.Contains(res.Summary, "diff") {
		t.Fatalf("Summary 要能看出留了基线，拿到 %q", res.Summary)
	}
}

// 超过 `fs.diffBasisMaxBytes` 就不做快照：这个参数的意义是"上限"，不是"永远做"。
func TestDiffBasisSkippedWhenOverLimit(t *testing.T) {
	t.Setenv("DSHD_EDIT_DIFF_BASIS_MAX_BYTES", "4")
	tool, fs, root := newTool(t)
	fixture(t, root, "0123456789\nOLD\n")
	if _, err := fs.Read("f.txt"); err != nil { // edit 要求先读后写（既有防陈旧覆盖）
		t.Fatal(err)
	}

	res, err := call(t, tool, "f.txt", "OLD", "NEW", false)
	if err != nil {
		t.Fatalf("超限只跳过快照，编辑本身照常成功：%v", err)
	}
	p := res.Data.(payload)
	if p.DiffBasis != "" {
		t.Fatalf("超过上限不该留快照，拿到 %q", p.DiffBasis)
	}
	if p.DiffBasisSkipped != "too-large" {
		t.Fatalf("跳过原因要结构化写出来（too-large），拿到 %q", p.DiffBasisSkipped)
	}
}
