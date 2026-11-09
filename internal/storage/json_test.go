package storage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func openJSONUnit(t *testing.T, root string, desc Descriptor) Unit {
	t.Helper()
	unit, err := NewJSONBackend(root).Open(desc)
	if err != nil {
		t.Fatalf("打开 JSON 单元失败: %v", err)
	}
	return unit
}

func TestJSONSingleLayoutRoundTripAndReopen(t *testing.T) {
	root := t.TempDir()
	desc := Descriptor{Name: "sessions", Version: 1, Tables: []string{"items"}, HasGlobal: true}

	unit := openJSONUnit(t, root, desc)
	if err := unit.PutRecord("items", "a", map[string]any{"n": 1}); err != nil {
		t.Fatalf("写记录失败: %v", err)
	}
	if err := unit.PutRecord("items", "b", "text"); err != nil {
		t.Fatalf("写记录失败: %v", err)
	}
	if err := unit.SetGlobal(map[string]any{"theme": "dark"}); err != nil {
		t.Fatalf("写 global 失败: %v", err)
	}
	if err := unit.Close(); err != nil {
		t.Fatalf("close 失败: %v", err)
	}

	// 重开是持久化边界：内存状态必须全部来自磁盘。
	reopened := openJSONUnit(t, root, desc)
	defer reopened.Close()
	state, err := reopened.LoadAll()
	if err != nil {
		t.Fatalf("重开读失败: %v", err)
	}
	if state.Version != 1 {
		t.Fatalf("version = %d，期望 1", state.Version)
	}
	if got := state.Tables["items"]["b"]; got != "text" {
		t.Fatalf("b = %v，期望 text", got)
	}
	obj, ok := state.Tables["items"]["a"].(map[string]any)
	if !ok || obj["n"] != json.Number("1") {
		t.Fatalf("a = %#v，期望 JSON 数字保真", state.Tables["items"]["a"])
	}
	global, ok := state.Global.(map[string]any)
	if !ok || global["theme"] != "dark" {
		t.Fatalf("global = %#v", state.Global)
	}
}

func TestJSONSingleLayoutIsHumanReadable(t *testing.T) {
	root := t.TempDir()
	unit := openJSONUnit(t, root, Descriptor{Name: "unit1", Version: 3, Tables: []string{"t"}})
	if err := unit.PutRecord("t", "k", "v"); err != nil {
		t.Fatal(err)
	}
	if err := unit.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "unit1.json"))
	if err != nil {
		t.Fatalf("读单元文件失败: %v", err)
	}
	text := string(raw)
	if !strings.HasSuffix(text, "}\n") {
		t.Fatalf("单元文件必须以换行结尾，得到 %q", text[len(text)-3:])
	}
	if !strings.Contains(text, "\n  \"unit\"") || !strings.Contains(text, "\"version\": 3") {
		t.Fatalf("单元文件必须是带 unit 头的 pretty JSON，得到:\n%s", text)
	}
}

func TestJSONPerRecordLayoutIsOneDocumentPerRecord(t *testing.T) {
	root := t.TempDir()
	desc := Descriptor{Name: "projcache", Version: 7, Tables: []string{"sessions"}, HasGlobal: true, Layout: LayoutPerRecord}
	unit := openJSONUnit(t, root, desc)
	if err := unit.PutRecord("sessions", "s-1", map[string]any{"rows": 1}); err != nil {
		t.Fatal(err)
	}
	if err := unit.PutRecord("sessions", "s-2", map[string]any{"rows": 2}); err != nil {
		t.Fatal(err)
	}
	if err := unit.SetGlobal("global-value"); err != nil {
		t.Fatal(err)
	}
	if err := unit.Close(); err != nil {
		t.Fatal(err)
	}

	// 整单元文件在这一布局下不存在（否则重写一条记录就要重写整棵树）。
	if _, err := os.Stat(filepath.Join(root, "projcache.json")); err == nil {
		t.Fatal("per-record 布局不该产生整单元文件")
	}
	for _, name := range []string{"s-1.json", "s-2.json"} {
		path := filepath.Join(root, "projcache", "sessions", name)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("记录文档 %s 不存在: %v", path, err)
		}
		var doc struct {
			Version int `json:"version"`
			Record  any `json:"record"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s 不是合法 JSON: %v", path, err)
		}
		if doc.Version != 7 || doc.Record == nil {
			t.Fatalf("%s 缺少 version/record 戳: %s", path, raw)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "projcache", "global.json")); err != nil {
		t.Fatalf("global.json 不存在: %v", err)
	}

	reopened := openJSONUnit(t, root, desc)
	defer reopened.Close()
	state, err := reopened.LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Tables["sessions"]) != 2 || state.Global != "global-value" {
		t.Fatalf("重开状态 = %#v", state)
	}
}

func TestJSONPerRecordRejectsUnsafeKey(t *testing.T) {
	root := t.TempDir()
	unit := openJSONUnit(t, root, Descriptor{Name: "u", Version: 1, Tables: []string{"t"}, Layout: LayoutPerRecord})
	defer unit.Close()
	for _, key := range []string{"../escape", "a/b", "a\\b", "", ".."} {
		err := unit.PutRecord("t", key, "v")
		if err == nil {
			t.Fatalf("key %q 必须被拒绝", key)
		}
		wantCode(t, err, CodeInvalidKey)
	}
	// 拒绝之后介质上不能留下任何东西。
	entries, err := os.ReadDir(filepath.Join(root, "u", "t"))
	if err == nil && len(entries) != 0 {
		t.Fatalf("非法 key 不该落盘，得到 %v", entries)
	}
}

func TestJSONWholeUnitVersionStamps(t *testing.T) {
	root := t.TempDir()
	unit := openJSONUnit(t, root, Descriptor{Name: "u", Version: 1, Tables: []string{"t"}})
	if err := unit.PutRecord("t", "k", "v"); err != nil {
		t.Fatal(err)
	}
	if err := unit.Close(); err != nil {
		t.Fatal(err)
	}

	// 未声明兼容版本 → 拒绝（整单元只有一份文档，只能失败）。
	if _, err := NewJSONBackend(root).Open(Descriptor{Name: "u", Version: 2, Tables: []string{"t"}}); err == nil {
		t.Fatal("未声明的版本必须拒绝")
	} else {
		wantCode(t, err, CodeVersionMismatch)
	}

	// 声明兼容版本 → 接受，并按当前版本重写（state.version = descriptor.version）。
	accepted := openJSONUnit(t, root, Descriptor{Name: "u", Version: 2, CompatibleVersions: []int{1}, Tables: []string{"t"}})
	state, err := accepted.LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	if state.Version != 2 {
		t.Fatalf("接受兼容版本后 state.version = %d，期望 2", state.Version)
	}
	if state.Tables["t"]["k"] != "v" {
		t.Fatalf("兼容版本读取丢了记录: %#v", state.Tables)
	}
	_ = accepted.Close()
}

func TestJSONPerRecordForeignDocumentReadsAsAbsent(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "u", "t")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 版本戳不在接受集合里的记录文档 = 外来文档 = 当作不存在（一条坏记录不许烧掉整个单元）。
	if err := os.WriteFile(filepath.Join(dir, "stale.json"), []byte(`{"version":99,"record":"old"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "good.json"), []byte(`{"version":1,"record":"ok"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte(`{`), 0o644); err != nil {
		t.Fatal(err)
	}
	unit := openJSONUnit(t, root, Descriptor{Name: "u", Version: 1, Tables: []string{"t"}, Layout: LayoutPerRecord})
	defer unit.Close()
	state, err := unit.LoadAll()
	if err != nil {
		t.Fatalf("外来文档不该让 LoadAll 失败: %v", err)
	}
	records := state.Tables["t"]
	if len(records) != 1 || records["good"] != "ok" {
		t.Fatalf("records = %#v，期望只有 good=ok", records)
	}
}

func TestJSONMalformedMedium(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "u.json"), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewJSONBackend(root).Open(Descriptor{Name: "u", Version: 1, Tables: []string{"t"}}); err == nil {
		t.Fatal("坏 JSON 必须失败")
	} else {
		wantCode(t, err, CodeMalformedMedium)
	}

	// 外来 unit 头（名字不匹配）同样是 malformed-medium。
	if err := os.WriteFile(filepath.Join(root, "u.json"), []byte(`{"unit":{"name":"other","version":1},"tables":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewJSONBackend(root).Open(Descriptor{Name: "u", Version: 1, Tables: []string{"t"}}); err == nil {
		t.Fatal("外来 unit 头必须失败")
	} else {
		wantCode(t, err, CodeMalformedMedium)
	}
}

func TestJSONPublishFailureKeepsCommittedState(t *testing.T) {
	root := t.TempDir()
	unit := openJSONUnit(t, root, Descriptor{Name: "u", Version: 1, Tables: []string{"t"}})
	if err := unit.PutRecord("t", "k", "old"); err != nil {
		t.Fatal(err)
	}
	// 把根目录换成一个文件：下一次发布必然失败（目录不存在）。
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := unit.PutRecord("t", "k", "new"); err == nil {
		t.Fatal("发布失败必须返回错误")
	}
	state, err := unit.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll 失败: %v", err)
	}
	if state.Tables["t"]["k"] != "old" {
		t.Fatalf("发布失败后内存必须回滚到旧值，得到 %#v", state.Tables["t"]["k"])
	}
	_ = unit.Close()
}

func TestJSONDeleteRecordAndDeleteMany(t *testing.T) {
	for _, layout := range []Layout{LayoutSingle, LayoutPerRecord} {
		t.Run(string(layout), func(t *testing.T) {
			root := t.TempDir()
			desc := Descriptor{Name: "u", Version: 1, Tables: []string{"t"}, Layout: layout}
			unit := openJSONUnit(t, root, desc)
			for _, key := range []string{"a", "b", "c"} {
				if err := unit.PutRecord("t", key, key); err != nil {
					t.Fatal(err)
				}
			}
			ok, err := unit.DeleteRecord("t", "a")
			if err != nil || !ok {
				t.Fatalf("DeleteRecord = %v, %v", ok, err)
			}
			if ok, err := unit.DeleteRecord("t", "a"); err != nil || ok {
				t.Fatalf("重复删除必须幂等返回 false，得到 %v, %v", ok, err)
			}
			removed, err := unit.DeleteRecords("t", []string{"b", "b", "missing"})
			if err != nil || removed != 1 {
				t.Fatalf("DeleteRecords = %d, %v，期望 1", removed, err)
			}
			if err := unit.Close(); err != nil {
				t.Fatal(err)
			}

			reopened := openJSONUnit(t, root, desc)
			defer reopened.Close()
			state, err := reopened.LoadAll()
			if err != nil {
				t.Fatal(err)
			}
			if len(state.Tables["t"]) != 1 || state.Tables["t"]["c"] != "c" {
				t.Fatalf("删除后状态 = %#v", state.Tables["t"])
			}
		})
	}
}

func TestJSONPerRecordLegacyBootstrap(t *testing.T) {
	root := t.TempDir()
	// 旧整单元文件先于 per-record 布局存在：读取必须从它自举，且不删改原文件。
	legacy := `{
  "unit": {"name": "u", "version": 3},
  "global": null,
  "tables": {"t": {"old": "seeded"}}
}
`
	legacyPath := filepath.Join(root, "u.json")
	if err := os.WriteFile(legacyPath, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	desc := Descriptor{Name: "u", Version: 7, CompatibleVersions: []int{3}, Tables: []string{"t"}, Layout: LayoutPerRecord}
	unit := openJSONUnit(t, root, desc)
	state, err := unit.LoadAll()
	if err != nil {
		t.Fatalf("自举失败: %v", err)
	}
	if state.Tables["t"]["old"] != "seeded" {
		t.Fatalf("自举丢了记录: %#v", state.Tables)
	}
	if err := unit.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatalf("旧文件被删了: %v", err)
	}
	if string(raw) != legacy {
		t.Fatal("自举不许改动旧整单元文件")
	}
	// 自举出来的记录文档按当前版本戳落盘。
	doc, err := os.ReadFile(filepath.Join(root, "u", "t", "old.json"))
	if err != nil {
		t.Fatalf("自举文档不存在: %v", err)
	}
	if !strings.Contains(string(doc), `"version": 7`) {
		t.Fatalf("自举文档必须按当前版本戳写: %s", doc)
	}
}

func TestJSONClosedUnitRejects(t *testing.T) {
	root := t.TempDir()
	unit := openJSONUnit(t, root, Descriptor{Name: "u", Version: 1, Tables: []string{"t"}})
	if err := unit.Close(); err != nil {
		t.Fatal(err)
	}
	if err := unit.PutRecord("t", "k", "v"); err == nil {
		t.Fatal("关闭后写必须失败")
	} else {
		wantCode(t, err, CodeClosed)
	}
	if err := unit.Close(); err != nil {
		t.Fatalf("close 必须幂等: %v", err)
	}
}

func TestJSONBackupRecordMovesAside(t *testing.T) {
	root := t.TempDir()
	unit := openJSONUnit(t, root, Descriptor{Name: "u", Version: 1, Tables: []string{"t"}, Layout: LayoutPerRecord})
	if err := unit.PutRecord("t", "bad", "v"); err != nil {
		t.Fatal(err)
	}
	backup, err := unit.(RecordBackup).BackupRecord("t", "bad")
	if err != nil {
		t.Fatalf("备份失败: %v", err)
	}
	if !strings.Contains(backup, "bad.json.bak.") {
		t.Fatalf("备份路径 = %q", backup)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("备份文件不存在: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "u", "t", "bad.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("原文档必须已被移走: %v", err)
	}
	_ = unit.Close()
}
