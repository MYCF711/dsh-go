package main

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
)

var update = flag.Bool("update", false, "重写 golden 快照")

func TestMain(m *testing.M) {
	flag.Parse()
	os.Exit(m.Run())
}

func assembleRegistry(t *testing.T) spi.ToolRegistry {
	t.Helper()
	k := kernel.New("snapshot")
	t.Cleanup(k.Root().Dispose)
	if _, err := kernel.Assemble(k.Root(), builtinModules()); err != nil {
		t.Fatalf("装配失败: %v", err)
	}
	reg, err := kernel.Inject(k.Root(), spi.ToolsKey)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// TestToolTableSnapshot 是 R4 的落点：模型看到的工具表（名字 + 描述 + 参数 schema）
// 一旦变化，必须先更新快照，且 diff 必须出现在提交里。
func TestToolTableSnapshot(t *testing.T) {
	reg := assembleRegistry(t)
	table, err := buildToolTable(reg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.MarshalIndent(table, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join("testdata", "tools.snapshot.json")
	if *update {
		if err := os.WriteFile(path, append(got, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("已更新 %s（%d 个工具）", path, len(table))
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到快照 %s：首次生成请跑 go test ./cmd/dshd -run TestToolTableSnapshot -update", path)
	}
	if strings.ReplaceAll(string(want), "\r\n", "\n") != string(got)+"\n" {
		t.Fatalf("工具表变了（R4）。确认是有意的改动后跑：\n  go test ./cmd/dshd -run TestToolTableSnapshot -update\nthen diff testdata/tools.snapshot.json")
	}
}

// TestToolTableIsStableAcrossAssemblies 保证同一装配跑两次产生同样字节。
func TestToolTableIsStableAcrossAssemblies(t *testing.T) {
	a, err := json.Marshal(buildToolTableMust(t))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(buildToolTableMust(t))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatal("两次装配的工具表不一致：顺序或指针地址泄漏进 schema")
	}
}

func buildToolTableMust(t *testing.T) []ToolEntry {
	t.Helper()
	table, err := buildToolTable(assembleRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	return table
}

// TestEveryToolHasDescription 是模型体验的底线：没有描述的工具模型只能猜什么时候用。
func TestEveryToolHasDescription(t *testing.T) {
	for _, e := range buildToolTableMust(t) {
		if strings.TrimSpace(e.Description) == "" {
			t.Errorf("工具 %q 没有 Description", e.Name)
		}
		if len(e.Description) > 120 {
			t.Errorf("工具 %q 的 Description 太长（%d 字），会挤占工具表预算", e.Name, len(e.Description))
		}
		var v any
		if err := json.Unmarshal(e.Parameters, &v); err != nil {
			t.Errorf("工具 %q 的 parameters 不是合法 JSON: %v", e.Name, err)
		}
	}
}
