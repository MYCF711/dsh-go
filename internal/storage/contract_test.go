package storage

import (
	"fmt"
	"testing"
)

// TestStorageBackendContract 是两种布局共用的后端一致性套件（对应官方
// `tests/contract.ts`）：同一组断言必须在 single 与 per-record 上都成立，
// 否则"布局可切换"就只是名字不同。
func TestStorageBackendContract(t *testing.T) {
	for _, layout := range []Layout{LayoutSingle, LayoutPerRecord} {
		layout := layout
		t.Run(string(layout), func(t *testing.T) {
			root := t.TempDir()
			desc := Descriptor{Name: "contract", Version: 4, Tables: []string{"alpha", "beta"}, HasGlobal: true, Layout: layout}

			unit, err := NewJSONBackend(root).Open(desc)
			if err != nil {
				t.Fatalf("打开: %v", err)
			}
			// 空单元：每个声明表都在，且是空 map（不是 nil，调用方不必判空）。
			state, err := unit.LoadAll()
			if err != nil {
				t.Fatalf("空单元 LoadAll: %v", err)
			}
			if state.Version != 4 {
				t.Fatalf("空单元 version = %d，期望 4", state.Version)
			}
			for _, table := range []string{"alpha", "beta"} {
				records, ok := state.Tables[table]
				if !ok || records == nil || len(records) != 0 {
					t.Fatalf("空单元表 %q = %#v（必须是非 nil 空 map）", table, records)
				}
			}
			if state.Global != nil {
				t.Fatalf("空 global = %#v，期望 nil", state.Global)
			}

			// 写：值原样回来（含嵌套结构），表之间互不串。
			if err := unit.PutRecord("alpha", "k1", map[string]any{"deep": []any{"a", "b"}}); err != nil {
				t.Fatalf("PutRecord: %v", err)
			}
			if err := unit.PutRecord("beta", "k1", "beta-value"); err != nil {
				t.Fatalf("PutRecord: %v", err)
			}
			state, err = unit.LoadAll()
			if err != nil {
				t.Fatalf("LoadAll: %v", err)
			}
			value, ok := state.Tables["alpha"]["k1"].(map[string]any)
			if !ok || len(value["deep"].([]any)) != 2 {
				t.Fatalf("alpha.k1 = %#v", state.Tables["alpha"]["k1"])
			}
			if state.Tables["beta"]["k1"] != "beta-value" {
				t.Fatalf("beta.k1 = %#v", state.Tables["beta"]["k1"])
			}

			// global 与记录是两条独立通道。
			if err := unit.SetGlobal(map[string]any{"revision": 7}); err != nil {
				t.Fatalf("SetGlobal: %v", err)
			}
			state, _ = unit.LoadAll()
			global, ok := state.Global.(map[string]any)
			if !ok || fmt.Sprint(global["revision"]) != "7" {
				// 整数值不丢（json.Number 原样保留）；关键是值真的是 7 而不是丢失。
				t.Fatalf("global = %#v", state.Global)
			}

			// 删除幂等 + 批量返回真实条数。
			if ok, err := unit.DeleteRecord("alpha", "k1"); err != nil || !ok {
				t.Fatalf("DeleteRecord = %v, %v", ok, err)
			}
			if ok, err := unit.DeleteRecord("alpha", "k1"); err != nil || ok {
				t.Fatalf("重复 DeleteRecord = %v, %v", ok, err)
			}
			if removed, err := unit.DeleteRecords("beta", []string{"k1", "k1"}); err != nil || removed != 1 {
				t.Fatalf("DeleteRecords = %d, %v，期望 1", removed, err)
			}

			// 关闭之后所有操作都拒绝（closed 是稳定码）。
			if err := unit.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if _, err := unit.LoadAll(); err == nil {
				t.Fatal("关闭后 LoadAll 必须失败")
			} else {
				wantCode(t, err, CodeClosed)
			}
			if err := unit.PutRecord("alpha", "k2", "v"); err == nil {
				t.Fatal("关闭后 PutRecord 必须失败")
			} else {
				wantCode(t, err, CodeClosed)
			}

			// 重开：进程重启后状态必须原样回来。
			reopened, err := NewJSONBackend(root).Open(desc)
			if err != nil {
				t.Fatalf("重开: %v", err)
			}
			defer reopened.Close()
			state, err = reopened.LoadAll()
			if err != nil {
				t.Fatalf("重开 LoadAll: %v", err)
			}
			if len(state.Tables["alpha"]) != 0 || len(state.Tables["beta"]) != 0 {
				t.Fatalf("删除没有持久化: %#v", state.Tables)
			}
			if state.Global == nil {
				t.Fatal("global 没有持久化")
			}
		})
	}
}

// TestStorageBackendWritesAreSequential 证明单次调用发布的是完整文档：
// 连续写 N 条之后，磁盘上是最后一次的完整状态（没有半份、没有丢键）。
func TestStorageBackendWritesAreSequential(t *testing.T) {
	for _, layout := range []Layout{LayoutSingle, LayoutPerRecord} {
		t.Run(string(layout), func(t *testing.T) {
			root := t.TempDir()
			desc := Descriptor{Name: "seq", Version: 1, Tables: []string{"t"}, Layout: layout}
			unit, err := NewJSONBackend(root).Open(desc)
			if err != nil {
				t.Fatal(err)
			}
			for index := 0; index < 20; index++ {
				key := string(rune('a' + index%26))
				if err := unit.PutRecord("t", key, index); err != nil {
					t.Fatalf("第 %d 次写失败: %v", index, err)
				}
			}
			if err := unit.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := NewJSONBackend(root).Open(desc)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			state, err := reopened.LoadAll()
			if err != nil {
				t.Fatal(err)
			}
			if len(state.Tables["t"]) != 20 {
				t.Fatalf("记录条数 = %d，期望 20", len(state.Tables["t"]))
			}
		})
	}
}
