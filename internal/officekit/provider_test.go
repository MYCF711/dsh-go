package officekit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testdata(t *testing.T, name string) string {
	t.Helper()
	src, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Skipf("testdata 缺失：%v", err)
	}
	return src
}

// noEngineProvider 造一个“本机没有引擎”的 provider（发现范围收窄到空目录）。
func noEngineProvider(t *testing.T) *Provider {
	t.Helper()
	empty := t.TempDir()
	p := Open(Options{Home: t.TempDir(), EnginePath: filepath.Join(empty, "absent"), SearchRoots: []string{empty}})
	t.Cleanup(p.Close)
	return p
}

// 官方 rc.2 的语义：引擎缺失时 provider 仍然构造成功，具体操作才失败
// （office-kit-package 升级指南：“It now registers without Kit”）。
func TestProviderConstructsWithoutEngine(t *testing.T) {
	p := noEngineProvider(t)
	if p.Engine() != nil {
		t.Fatal("空目录里不该有引擎")
	}
	if p.GenerationID() != "officekit-unavailable" {
		t.Fatalf("无引擎时 generation 应为 officekit-unavailable，得到 %q", p.GenerationID())
	}
	capability := p.Capability()
	if capability.Available {
		t.Fatal("没有引擎却报告 available")
	}
	if capability.CapableWhen == "" || !strings.Contains(capability.CapableWhen, EngineEnvVar) {
		t.Fatalf("capableWhen 必须说明怎么补救：%q", capability.CapableWhen)
	}
	if len(capability.Tried) == 0 {
		t.Fatal("capability 应带上试过的路径")
	}
}

// 无引擎时任何操作都必须是结构化 unavailable + capableWhen，绝不假成功。
func TestOperationsFailStructuredWhenUnavailable(t *testing.T) {
	p := noEngineProvider(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	docx := testdata(t, "probe.docx")
	xlsx := testdata(t, "book.xlsx")
	cases := []struct {
		name string
		run  func() error
	}{
		{"render", func() error { _, err := p.Render(ctx, docx, "foreground"); return err }},
		{"convert", func() error { _, err := p.ConvertTo(ctx, "owner", docx, "pdf", ""); return err }},
		{"recalculate", func() error { _, err := p.RecalculateTo(ctx, "owner", xlsx, "xlsx"); return err }},
		{"images", func() error { _, err := p.RenderImages(ctx, docx, ImageRequest{}); return err }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := testCase.run()
			if err == nil {
				t.Fatal("没有引擎却成功了——这是假结果")
			}
			if code := CodeOf(err); code != CodeUnavailable {
				t.Fatalf("错误码应为 unavailable，得到 %q（%v）", code, err)
			}
			kitErr, ok := err.(*Error)
			if !ok {
				t.Fatalf("应是 *officekit.Error，得到 %T", err)
			}
			if kitErr.CapableWhen == "" {
				t.Fatal("unavailable 必须带 capableWhen")
			}
		})
	}
}

// 输入校验在碰引擎之前就要拒：文件不存在、类型不支持、上限超了。
func TestInspectRejectsBeforeEngine(t *testing.T) {
	p := Open(Options{Home: t.TempDir(), SearchRoots: []string{t.TempDir()}, EnginePath: filepath.Join(t.TempDir(), "absent")})
	t.Cleanup(p.Close)
	if _, err := p.Render(context.Background(), filepath.Join(t.TempDir(), "nope.docx"), "foreground"); CodeOf(err) != CodeInvalidRequest {
		t.Fatalf("不存在的文件应先给 invalid-request，得到 %v", err)
	}
	txt := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(txt, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Render(context.Background(), txt, "foreground"); CodeOf(err) != CodeUnsupportedFormat {
		t.Fatalf("不支持的扩展名应给 unsupported-format，得到 %v", err)
	}
	if _, err := p.ConvertTo(context.Background(), "owner", testdata(t, "probe.docx"), "xlsx", ""); CodeOf(err) != CodeUnsupportedFormat {
		t.Fatalf("docx→xlsx 应给 unsupported-format，得到 %v", err)
	}
	if _, err := p.RecalculateTo(context.Background(), "owner", testdata(t, "probe.docx"), "xlsx"); CodeOf(err) != CodeUnsupportedFormat {
		t.Fatalf("非工作簿重算应给 unsupported-format，得到 %v", err)
	}
	if _, err := p.ConvertTo(context.Background(), "owner", testdata(t, "book.xlsx"), "csv", ""); CodeOf(err) != CodeUnavailable {
		t.Fatalf("合法转换在没有引擎时应给 unavailable，得到 %v", err)
	}
	if _, err := p.Render(context.Background(), testdata(t, "probe.docx"), "now"); err == nil || CodeOf(err) != CodeInvalidRequest {
		t.Fatalf("priority 非法应给 invalid-request，得到 %v", err)
	}
}
