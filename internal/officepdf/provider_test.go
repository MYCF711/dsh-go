package officepdf

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRenderRealWordFixture(t *testing.T) {
	src, err := filepath.Abs("../../tmp/GO2-TEAM/PARITY-W4-OFFICE/fixtures/probe.docx")
	if err != nil || !fileExists(src) {
		t.Skip("fixture missing")
	}
	p := Open(t.TempDir())
	defer p.Close()
	if p.Renderer == "" {
		t.Fatal("未探测到 Word/LibreOffice 渲染器")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	doc, err := p.Render(ctx, "", src, "foreground")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(doc.Data) < 5 || string(doc.Data[:5]) != "%PDF-" {
		t.Fatalf("不是 PDF：%q", doc.Data[:min(8, len(doc.Data))])
	}
	if doc.Generation == "" || doc.Version == "" || doc.AbsolutePath != src || !doc.Eof {
		t.Fatalf("结果不完整：%+v", doc)
	}
	if doc.Bytes == nil || *doc.Bytes != len(doc.Data) {
		t.Fatalf("bytes 不对：%+v", doc.Bytes)
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
