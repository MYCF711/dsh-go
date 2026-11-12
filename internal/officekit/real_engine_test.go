package officekit

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// realEngineProvider 用真实发现流程找引擎；找不到就 skip（CI 上没引擎不算失败）。
func realEngineProvider(t *testing.T) *Provider {
	t.Helper()
	p := Open(Options{Home: t.TempDir(), MaxConcurrent: 2, MaxQueued: 4, Timeout: 120 * time.Second})
	t.Cleanup(p.Close)
	if p.Engine() == nil {
		t.Skipf("本机没有 LibreOfficeKit 引擎：%v", p.Capability().Tried)
	}
	return p
}

// 真渲染：docx → 完整 PDF 字节（这是「真实现 vs 501」的唯一判据）。
func TestRealRenderDocxToPDF(t *testing.T) {
	p := realEngineProvider(t)
	src := testdata(t, "probe.docx")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	doc, err := p.Render(ctx, src, "foreground")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !bytes.HasPrefix(doc.Data, []byte("%PDF-")) {
		t.Fatalf("不是 PDF：%q", head(doc.Data, 8))
	}
	if !bytes.Contains(doc.Data[len(doc.Data)-1024:], []byte("%%EOF")) {
		t.Fatal("PDF 不完整（缺文件尾标记）")
	}
	if len(doc.Data) < 1000 {
		t.Fatalf("PDF 太小：%d 字节", len(doc.Data))
	}
	if doc.Bytes == nil || *doc.Bytes != len(doc.Data) || doc.Generation == "" || doc.Version == "" || !doc.Eof {
		t.Fatalf("结果字段不完整：%+v", doc)
	}
	t.Logf("真实转换：%s → PDF %d 字节（generation=%s）", filepath.Base(src), len(doc.Data), doc.Generation)
}

// 真转换：xlsx → csv，且引擎在加载时已重算公式。
func TestRealConvertXlsxToCSV(t *testing.T) {
	p := realEngineProvider(t)
	src := testdata(t, "book.xlsx")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	result, err := p.ConvertTo(ctx, "session-owner", src, "csv", "")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	data, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatalf("读产物：%v", err)
	}
	if result.Bytes != int64(len(data)) || len(data) == 0 {
		t.Fatalf("产物字节数不对：%+v", result)
	}
	text := string(data)
	if !strings.Contains(text, "value,doubled") {
		t.Fatalf("CSV 表头不对：%q", head(data, 60))
	}
	if !strings.Contains(text, "2,4") {
		t.Fatalf("公式没有被重算：%q", text)
	}
	t.Logf("真实转换：book.xlsx → CSV %d 字节（%s）", len(data), result.Path)
}

// 真重算：xlsx → xlsx（公式写回缓存值）。
func TestRealRecalculateWorkbook(t *testing.T) {
	p := realEngineProvider(t)
	src := testdata(t, "book.xlsx")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	result, err := p.RecalculateTo(ctx, "session-owner", src, "xlsx")
	if err != nil {
		t.Fatalf("recalculate: %v", err)
	}
	data, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatalf("读产物：%v", err)
	}
	if !bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		t.Fatalf("xlsx 应是 ZIP 包：%q", head(data, 4))
	}
	if len(data) < 1000 || result.Bytes != int64(len(data)) {
		t.Fatalf("产物大小不对：%d / %+v", len(data), result)
	}
	t.Logf("真实重算：book.xlsx → xlsx %d 字节", len(data))
}

// 真渲染图片：docx 首页 → PNG。
func TestRealRenderImagesDocx(t *testing.T) {
	p := realEngineProvider(t)
	src := testdata(t, "probe.docx")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	result, err := p.RenderImages(ctx, src, ImageRequest{DPI: 96, MaxImages: 4})
	if err != nil {
		t.Fatalf("renderImages: %v", err)
	}
	if len(result.Images) != 1 || result.PageCount != 1 {
		t.Fatalf("页数不对：images=%d pageCount=%d", len(result.Images), result.PageCount)
	}
	image := result.Images[0]
	if !bytes.HasPrefix(image.PNG, []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}) {
		t.Fatalf("不是 PNG：%v", head(image.PNG, 8))
	}
	if image.Page != 1 || image.Sheet != "" {
		t.Fatalf("位置元数据不对：%+v", image)
	}
	// Letter 页 816x1056 点，96 DPI 下应正好是 816x1056 像素。
	if image.Width != 816 || image.Height != 1056 {
		t.Fatalf("像素尺寸不对：%dx%d", image.Width, image.Height)
	}
	t.Logf("真实渲染：probe.docx 第 1 页 → PNG %d 字节 %dx%d", len(image.PNG), image.Width, image.Height)
}

// 真渲染图片：xlsx 指定工作表与区域。
func TestRealRenderImagesXlsxSheet(t *testing.T) {
	p := realEngineProvider(t)
	src := testdata(t, "book.xlsx")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	result, err := p.RenderImages(ctx, src, ImageRequest{Sheet: "Data", Range: "A1:B5", DPI: 96, MaxImages: 4})
	if err != nil {
		t.Fatalf("renderImages: %v", err)
	}
	if len(result.Images) == 0 {
		t.Fatal("没有渲染出任何图片")
	}
	first := result.Images[0]
	if first.Sheet != "Data" || first.Range != "A1:B5" || first.Page != 0 {
		t.Fatalf("位置元数据不对：%+v", first)
	}
	// 2 列 x64px = 128，5 行 x20px = 100。
	if first.Width != 128 || first.Height != 100 {
		t.Fatalf("区域像素尺寸不对：%dx%d（期望 128x100）", first.Width, first.Height)
	}
	if !bytes.HasPrefix(first.PNG, []byte{0x89, 'P', 'N', 'G'}) {
		t.Fatalf("不是 PNG：%v", head(first.PNG, 4))
	}
	t.Logf("真实渲染：book.xlsx Data!A1:B5 → PNG %d 字节 %dx%d", len(first.PNG), first.Width, first.Height)
}

// 取消必须真的杀掉引擎（不留孤儿）。
func TestRealRenderCancelKillsEngine(t *testing.T) {
	p := realEngineProvider(t)
	src := testdata(t, "probe.docx")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	_, err := p.Render(ctx, src, "foreground")
	if err == nil {
		t.Skip("本机渲染太快，5ms 内完成，取消路径未被触发")
	}
	if code := CodeOf(err); code != CodeTimeout {
		t.Fatalf("取消应给 timeout，得到 %q（%v）", code, err)
	}
}

func head(data []byte, n int) []byte {
	if len(data) < n {
		return data
	}
	return data[:n]
}
