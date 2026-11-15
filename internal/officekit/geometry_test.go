package officekit

import (
	"strings"
	"testing"
)

func TestParseCellRange(t *testing.T) {
	valid := map[string]cellRange{
		"A1":        {0, 0, 0, 0},
		"A1:B5":     {0, 0, 1, 4},
		"$A$1:$B$5": {0, 0, 1, 4},
		"aa10:aa12": {26, 9, 26, 11},
	}
	for input, want := range valid {
		got, err := ParseCellRange(input)
		if err != nil {
			t.Fatalf("%s: %v", input, err)
		}
		if got != want {
			t.Fatalf("%s = %+v，期望 %+v", input, got, want)
		}
	}
	for _, input := range []string{"", "1A", "A0", "A1:B0", "B5:A1", "A1:ZZZZ1", "A1:B2:C3", "Sheet1.A1"} {
		if _, err := ParseCellRange(input); err == nil {
			t.Fatalf("%q 应该被拒", input)
		}
	}
	if got := RangeName(cellRange{0, 0, 3, 19}); got != "A1:D20" {
		t.Fatalf("RangeName = %q", got)
	}
}

// 引擎真实回报的 xlsx 几何：列宽 963 twips、行高 300 twips（book.xlsx）。
func TestSheetRangeRectangleRealGeometry(t *testing.T) {
	geometry := sheetGeometry{
		Columns: dimensionSpans{Sizes: "963:16383 ", Hidden: "0:16383 ", Filtered: "0:16383 "},
		Rows:    dimensionSpans{Sizes: "300:1048575 ", Hidden: "0:1048575 ", Filtered: "0:1048575 "},
	}
	// 96 DPI → scale 1：列宽 floor(963/15)=64px，行高 floor(300/15)=20px。
	rect, err := SheetRangeRectangle(geometry, cellRange{0, 0, 1, 4}, 1)
	if err != nil {
		t.Fatalf("sheetRangeRectangle: %v", err)
	}
	if rect.X != 0 || rect.Y != 0 || rect.Width != 128 || rect.Height != 100 {
		t.Fatalf("矩形不对：%+v（期望 0,0,128,100）", rect)
	}
	// 144 DPI → scale 1.5：列宽先取整成 floor(963*1.5/15)=96 像素，再除回 1.5
	// 得到 128 点；行高 floor(300*1.5/15)=30 → 5 行 = 150 像素 → 100 点。
	// 也就是说引擎的像素取整会吃掉半像素，不能简单按比例放大。
	rect, err = SheetRangeRectangle(geometry, cellRange{0, 0, 1, 4}, 1.5)
	if err != nil {
		t.Fatalf("scale 1.5: %v", err)
	}
	if rect.Width != 128 || rect.Height != 100 {
		t.Fatalf("scale 1.5 的取整结果不对：%+v（期望 128x100）", rect)
	}
}

func TestSpansRejectsIncompleteCoverage(t *testing.T) {
	if _, err := spans("963:10", false, maxColumn); err == nil {
		t.Fatal("覆盖不完整必须被拒")
	}
	if _, err := spans("1:0", false, maxColumn); err == nil {
		t.Fatal("结束位置不大于上一段必须被拒")
	}
	if _, err := spans("2:10", true, 10); err == nil {
		t.Fatal("标记行的值只能是 0/1")
	}
	got, err := spans("0:5 10", true, 10)
	if err != nil {
		t.Fatalf("合法标记行程被拒：%v", err)
	}
	if len(got) != 2 || got[0].Value != 0 || got[1].Value != 1 {
		t.Fatalf("标记行程解码不对：%+v", got)
	}
}

func TestWriterPages(t *testing.T) {
	pages, err := WriterPages("284, 284, 12240, 15840")
	if err != nil {
		t.Fatalf("writerPages: %v", err)
	}
	if len(pages) != 1 || pages[0].Width != 816 || pages[0].Height != 1056 || pages[0].X != 284 || pages[0].Part != -1 {
		t.Fatalf("页面几何不对：%+v", pages)
	}
	// 零面积占位（没有可见页面）必须跳过。
	pages, err = WriterPages("0, 0, 0, 0; 284, 284, 12240, 15840")
	if err != nil || len(pages) != 1 {
		t.Fatalf("零面积占位应被跳过：%+v %v", pages, err)
	}
	if _, err := WriterPages("0, 0, 0, 0"); err == nil || !strings.Contains(err.Error(), "没有可渲染页面") {
		t.Fatalf("全零面积应报没有页面：%v", err)
	}
	if _, err := WriterPages("1,2,3"); err == nil {
		t.Fatal("字段数不对必须被拒")
	}
}

func TestSelectedPages(t *testing.T) {
	got, err := SelectedPages(nil, 3, 10)
	if err != nil || len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Fatalf("空选择应取全部页：%v %v", got, err)
	}
	if _, err := SelectedPages(nil, 10, 3); err == nil || CodeOf(err) != CodeOutputTooLarge {
		t.Fatalf("超过上限应给 output-too-large：%v", err)
	}
	if _, err := SelectedPages([]int{5}, 3, 10); err == nil || CodeOf(err) != CodeInvalidRequest {
		t.Fatalf("越界页号应给 invalid-request：%v", err)
	}
	if _, err := SelectedPages(nil, 0, 10); err == nil || CodeOf(err) != CodeInvalidDocument {
		t.Fatalf("零页文档应给 invalid-document：%v", err)
	}
}

func TestTileDimensionsAndSplitting(t *testing.T) {
	page := pageGeom{Part: -1, X: 284, Y: 284, Width: 816, Height: 1056}
	width, height, twips, err := TileDimensions(page, rectF{0, 0, 816, 1056}, 1)
	if err != nil {
		t.Fatalf("整页: %v", err)
	}
	if width != 816 || height != 1056 {
		t.Fatalf("整页像素不对：%dx%d", width, height)
	}
	if twips != [4]int64{284, 284, 12240, 15840} {
		t.Fatalf("twips 不对：%v", twips)
	}
	if _, _, _, err := TileDimensions(page, rectF{0, 0, 900, 1056}, 1); err == nil {
		t.Fatal("超出页面的分块必须被拒")
	}
	tiles, err := SplitRasterRectangle(rectF{0, 0, 816, 1056}, 1, RasterLimits{MaxPixels: 16777216, MaxDimension: 500, MaxTiles: 20})
	if err != nil {
		t.Fatalf("切块: %v", err)
	}
	if len(tiles) != 6 {
		t.Fatalf("500px 上限应切成 2x3=6 块，得到 %d", len(tiles))
	}
	for _, tile := range tiles {
		width, height, _, err := TileDimensions(page, tile, 1)
		if err != nil {
			t.Fatalf("切块几何不合法：%+v %v", tile, err)
		}
		if width > 500 || height > 500 {
			t.Fatalf("切块超过 maxDimension：%dx%d", width, height)
		}
	}
	if _, err := SplitRasterRectangle(rectF{0, 0, 816, 1056}, 1, RasterLimits{MaxPixels: 16777216, MaxDimension: 500, MaxTiles: 2}); err == nil {
		t.Fatal("块数超过 maxTiles 必须被拒")
	}
}

// 引擎给的是预乘 alpha + BGRA（tileMode 1），PNG 要直通 alpha + RGBA。
func TestRGBAPixels(t *testing.T) {
	// BGRA: B=0 G=0 R=255 A=255 → RGBA(255,0,0,255)
	got, err := RGBAPixels([]byte{0, 0, 255, 255}, 1)
	if err != nil {
		t.Fatalf("rgbapixels: %v", err)
	}
	if got[0] != 255 || got[1] != 0 || got[2] != 0 || got[3] != 255 {
		t.Fatalf("BGRA→RGBA 不对：%v", got)
	}
	// 预乘：alpha=128、B=128 → 直通 R=255；alpha=0 → 全 0。
	got, err = RGBAPixels([]byte{0, 0, 128, 128}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != 255 || got[3] != 128 {
		t.Fatalf("预乘还原不对：%v", got)
	}
	got, _ = RGBAPixels([]byte{9, 9, 9, 0}, 1)
	if got[0] != 0 || got[1] != 0 || got[2] != 0 || got[3] != 0 {
		t.Fatalf("全透明像素必须清零：%v", got)
	}
	if _, err := RGBAPixels([]byte{1, 2, 3}, 0); err == nil {
		t.Fatal("字节数不是 4 的倍数必须被拒")
	}
	if _, err := RGBAPixels([]byte{1, 2, 3, 4}, 2); err == nil {
		t.Fatal("不支持的 tileMode 必须被拒")
	}
}

func TestUnmirrorRTLTile(t *testing.T) {
	// 2x1：左像素 R=1，右像素 R=2 → 翻转后左 2 右 1。
	rgba := []byte{1, 0, 0, 255, 2, 0, 0, 255}
	UnmirrorRTLTile(2, 1, rgba)
	if rgba[0] != 2 || rgba[4] != 1 {
		t.Fatalf("RTL 镜像没翻回来：%v", rgba)
	}
}
