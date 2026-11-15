package officepdf

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dshgo/internal/officekit"
)

// fixture 是 internal/officekit/testdata 里随测试一起进版本库的真 docx（985 字节）。
func fixture(t *testing.T) string {
	t.Helper()
	src, err := filepath.Abs(filepath.Join("..", "officekit", "testdata", "probe.docx"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Skipf("fixture 缺失：%v", err)
	}
	return src
}

// requireEngine 是"真实原生引擎"用例的前置检查。
//
// 引擎是**本机安装件**（官方 `libreoffice-kit-<platform>/bin/dsoffice.exe`），
// 不是仓内资产：开发机装了，GitHub windows-latest runner 没有。缺件时 skip 并
// 把"试过哪些路径 + 怎么补救"打出来——这不是放宽断言：一旦发现引擎，下面的
// 转换结果与字段断言一条都不少；缺件是环境事实，不是产品缺陷
// （2026-11-12 run 43191452874：本机绿、CI 红，根因就是这里假定了本机安装）。
func requireEngine(t *testing.T, p *Provider) {
	t.Helper()
	if p.Engine() != nil {
		return
	}
	t.Skipf("本机没有 LibreOfficeKit 原生引擎（本机已验；CI runner 常缺 libreoffice-kit-%s/bin/dsoffice.exe）：tried=%v capableWhen=%s",
		officekit.TargetPlatform(), p.Tried(), CapableWhen())
}

// TestEngineDiscovered 要求本机找到官方原生引擎（rc.2 起不再是 soffice/Word COM）。
func TestEngineDiscovered(t *testing.T) {
	p := Open(t.TempDir())
	defer p.Close()
	requireEngine(t, p)
	if p.GenerationID() == "" || strings.Contains(p.GenerationID(), "unavailable") {
		t.Fatalf("generation 不合法：%q", p.GenerationID())
	}
}

// TestRenderRealDocument 要求一次**真**转换：docx → PDF 字节。
func TestRenderRealDocument(t *testing.T) {
	src := fixture(t)
	p := Open(t.TempDir())
	defer p.Close()
	requireEngine(t, p)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	doc, err := p.Render(ctx, "", src, "foreground")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(doc.Data) < 8 || string(doc.Data[:5]) != "%PDF-" {
		t.Fatalf("不是 PDF：%q", head(doc.Data, 8))
	}
	if doc.Generation == "" || doc.Version == "" || doc.AbsolutePath != src || !doc.Eof || doc.Offset != 0 {
		t.Fatalf("结果字段不完整：%+v", doc)
	}
	if doc.Bytes == nil || *doc.Bytes != len(doc.Data) {
		t.Fatalf("bytes 与 data 不一致：%v vs %d", doc.Bytes, len(doc.Data))
	}
	if doc.MissingFonts == nil {
		t.Fatal("missingFonts 必须是数组（可以是空），不能是 null")
	}
}

// TestNoEngineIsStructured501 要求没有引擎时是结构化 unavailable + capableWhen，
// 绝不返回空 PDF 或假成功。
func TestNoEngineIsStructured501(t *testing.T) {
	src := fixture(t)
	p := Open(t.TempDir())
	defer p.Close()
	// 把发现根指向一个空目录，并显式给一个不存在的引擎路径。
	p.SetEngineUnavailableForTest(t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := p.Render(ctx, "", src, "foreground")
	if err == nil {
		t.Fatal("没有引擎却渲染成功了——这是假渲染")
	}
	// 旧调用方（cmd/dshd/serve_office_pdf.go）按 renderer_unavailable 映射 503，
	// 所以 officeToPdf 面保留这个字符串；官方 officeKit 面的不可用码是 unavailable。
	code := CodeOf(err)
	if code != "renderer_unavailable" {
		t.Fatalf("错误码应为 renderer_unavailable，得到 %q（%v）", code, err)
	}
	if !strings.Contains(err.Error(), CapableWhen()) {
		t.Fatalf("错误里必须带 capableWhen：%v", err)
	}
}

func head(data []byte, n int) string {
	if len(data) < n {
		n = len(data)
	}
	return string(data[:n])
}
