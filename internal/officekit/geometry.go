package officekit

// 光栅几何：把引擎回报的页面/工作表布局换算成「第几块、多少像素、取哪一块 twips」。
//
// 逐字移植官方 libreoffice-kit/lib/cli.js:74-345：
//   - :74-95    parseCellRange / rangeName / column
//   - :96-100   dataArea
//   - :101-170  spans / dimension（列宽行高的行程编码）
//   - :176-183  sheetRangeRectangle
//   - :173-183  unmirrorRtlTile
//   - :184-237  splitRasterRectangle
//   - :238-262  tileDimensions
//   - :264-283  rgbaPixels（预乘 alpha → 直通 alpha + 通道序）
//   - :284-330  writerPages
//   - :334-345  selectedPages
//
// 这些数字不是我们自己发明的：引擎按 twips（1/1440 英寸）回报几何，输出像素必须
// 用同一套换算，否则渲染出来的图会与原页面错位。

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

const (
	maxColumn = 16383
	maxRow    = 1048575
	// maxTilePixels 是单块像素上限（官方 tileDimensions 里 16 兆的同一个数）。
	maxTilePixels = 16777216
)

// cellRange 是 A1 记法的矩形区域（行列为 0 基）。
type cellRange struct {
	FirstColumn int
	FirstRow    int
	LastColumn  int
	LastRow     int
}

func columnName(index int) string {
	name := ""
	for value := index + 1; value > 0; value = (value - 1) / 26 {
		name = string(rune('A'+(value-1)%26)) + name
	}
	return name
}

// RangeName 把区域渲染成 A1 记法（官方 rangeName）。
func RangeName(r cellRange) string {
	return fmt.Sprintf("%s%d:%s%d", columnName(r.FirstColumn), r.FirstRow+1, columnName(r.LastColumn), r.LastRow+1)
}

// ParseCellRange 解析 "A1" / "A1:D20"（官方 parseCellRange）。
func ParseCellRange(value string) (cellRange, error) {
	text := strings.ToUpper(strings.TrimSpace(value))
	text = strings.ReplaceAll(text, "$", "")
	parts := strings.Split(text, ":")
	if len(parts) > 2 || len(parts) == 0 {
		return cellRange{}, errf(CodeInvalidRequest, "range 必须是 A1 单元格或矩形：%q", value)
	}
	first, err := parseCell(parts[0])
	if err != nil {
		return cellRange{}, err
	}
	last := first
	if len(parts) == 2 {
		last, err = parseCell(parts[1])
		if err != nil {
			return cellRange{}, err
		}
	}
	if last.Column < first.Column || last.Row < first.Row || last.Column > maxColumn || last.Row > maxRow {
		return cellRange{}, errf(CodeInvalidRequest, "range 超出了 Calc 工作表边界或首尾颠倒：%q", value)
	}
	return cellRange{FirstColumn: first.Column, FirstRow: first.Row, LastColumn: last.Column, LastRow: last.Row}, nil
}

type cell struct {
	Column int
	Row    int
}

func parseCell(token string) (cell, error) {
	if token == "" {
		return cell{}, errf(CodeInvalidRequest, "range 里有空的单元格")
	}
	split := 0
	for split < len(token) && token[split] >= 'A' && token[split] <= 'Z' {
		split++
	}
	if split == 0 || split > 3 || split == len(token) {
		return cell{}, errf(CodeInvalidRequest, "range 里的单元格不合法：%q", token)
	}
	column := 0
	for _, ch := range token[:split] {
		column = column*26 + int(ch-'A'+1)
	}
	column--
	rowText := token[split:]
	if strings.HasPrefix(rowText, "0") || len(rowText) > 7 {
		return cell{}, errf(CodeInvalidRequest, "range 里的行号不合法：%q", token)
	}
	row, err := strconv.Atoi(rowText)
	if err != nil || row < 1 {
		return cell{}, errf(CodeInvalidRequest, "range 里的行号不合法：%q", token)
	}
	return cell{Column: column, Row: row - 1}, nil
}

// Span 是行程编码的一段：从上一段结束+1 到 End，值为 Value。
type Span struct {
	End   int
	Value int
}

// spans 解码引擎的行程编码（官方 cli.js:101-130 spans）。
//
// 两种形态：尺寸行（"值:结束 值:结束 …"，值 ≤65535）与标记行（首段带值，
// 之后每段只有结束位置、值按 1/0 交替）。
func spans(encoded string, flags bool, maximum int) ([]Span, error) {
	if len(encoded) > 33554432 {
		return nil, errf(CodeInvalidDocument, "引擎回报的工作表几何编码过大")
	}
	tokens := strings.Fields(strings.TrimSpace(encoded))
	previous := -1
	flag := 0
	result := make([]Span, 0, len(tokens))
	for i, token := range tokens {
		fields := strings.Split(token, ":")
		var value, end int
		var err error
		if flags && i > 0 {
			flag = 1 - flag
			value = flag
			if len(fields) != 1 {
				return nil, errf(CodeInvalidDocument, "引擎回报的工作表标记行程不合法")
			}
			end, err = strconv.Atoi(fields[0])
		} else {
			if len(fields) != 2 {
				return nil, errf(CodeInvalidDocument, "引擎回报的工作表尺寸行程不合法")
			}
			value, err = strconv.Atoi(fields[0])
			if err == nil {
				end, err = strconv.Atoi(fields[1])
			}
			flag = value
		}
		if err != nil || value < 0 || (flags && value > 1) || (!flags && value > 65535) || end <= previous || end > maximum {
			return nil, errf(CodeInvalidDocument, "引擎回报的工作表几何越界")
		}
		previous = end
		result = append(result, Span{End: end, Value: value})
	}
	if previous != maximum {
		return nil, errf(CodeInvalidDocument, "引擎回报的工作表几何没有覆盖完整维度")
	}
	return result, nil
}

// dimensionSpans 是一维（列或行）的行程编码集合。
type dimensionSpans struct {
	Sizes    string `json:"sizes"`
	Hidden   string `json:"hidden"`
	Filtered string `json:"filtered"`
}

// sheetGeometry 是引擎回报的工作表几何（官方 JSON.parse(geometry)）。
type sheetGeometry struct {
	Columns dimensionSpans `json:"columns"`
	Rows    dimensionSpans `json:"rows"`
}

// dimension 计算 [first,last] 在给定缩放下的起点与长度（官方 cli.js:131-160）。
func dimension(data dimensionSpans, first, last, maximum int, scale float64) (start, size float64, err error) {
	sizes, err := spans(data.Sizes, false, maximum)
	if err != nil {
		return 0, 0, err
	}
	hidden, err := spans(data.Hidden, true, maximum)
	if err != nil {
		return 0, 0, err
	}
	filtered, err := spans(data.Filtered, true, maximum)
	if err != nil {
		return 0, 0, err
	}
	position := 0.0
	startAt := 0.0
	index := 0
	si, hi, fi := 0, 0, 0
	for index <= last {
		if si >= len(sizes) || hi >= len(hidden) || fi >= len(filtered) {
			return 0, 0, errf(CodeInvalidDocument, "引擎回报的工作表几何行程提前结束")
		}
		limit := sizes[si].End
		if hidden[hi].End < limit {
			limit = hidden[hi].End
		}
		if filtered[fi].End < limit {
			limit = filtered[fi].End
		}
		if last < limit {
			limit = last
		}
		if index < first && first-1 < limit {
			limit = first - 1
		}
		pixels := 0.0
		if hidden[hi].Value == 0 && filtered[fi].Value == 0 && sizes[si].Value != 0 {
			pixels = math.Max(1, math.Floor(float64(sizes[si].Value)*scale/15))
		}
		if index == first {
			startAt = position
		}
		position += float64(limit-index+1) * pixels
		index = limit + 1
		if index > sizes[si].End {
			si++
		}
		if index > hidden[hi].End {
			hi++
		}
		if index > filtered[fi].End {
			fi++
		}
	}
	return startAt / scale, (position - startAt) / scale, nil
}

// sheetInfo 是引擎回报的单个工作表元数据（官方 cli.js:102-110 sheetPartInfo）。
type sheetInfo struct {
	Name       string `json:"name"`
	Visible    int    `json:"visible"`
	RTLLayout  int    `json:"rtllayout"`
	LastColumn int    `json:"lastcolumn"`
	LastRow    int    `json:"lastrow"`
}

func (s sheetInfo) valid() bool {
	if strings.TrimSpace(s.Name) == "" {
		return false
	}
	if s.Visible != 0 && s.Visible != 1 {
		return false
	}
	if s.RTLLayout != 0 && s.RTLLayout != 1 {
		return false
	}
	return s.LastColumn >= 0 && s.LastColumn <= maxColumn && s.LastRow >= 0 && s.LastRow <= maxRow
}

// rectF 是浮点矩形（单位为点，1/72 英寸）。
type rectF struct {
	X      float64
	Y      float64
	Width  float64
	Height float64
}

// SheetRangeRectangle 计算工作表区域在给定缩放下的矩形（官方 cli.js:176-183）。
func SheetRangeRectangle(geometry sheetGeometry, r cellRange, scale float64) (rectF, error) {
	if !(scale > 0) || scale > 16 {
		return rectF{}, errf(CodeInvalidRequest, "工作表光栅缩放不合法")
	}
	x, width, err := dimension(geometry.Columns, r.FirstColumn, r.LastColumn, maxColumn, scale)
	if err != nil {
		return rectF{}, err
	}
	y, height, err := dimension(geometry.Rows, r.FirstRow, r.LastRow, maxRow, scale)
	if err != nil {
		return rectF{}, err
	}
	if width <= 0 || height <= 0 {
		return rectF{}, errf(CodeInvalidRequest, "选中的工作表区域整块被隐藏或过滤掉了")
	}
	for _, value := range []float64{x, y, width, height, x + width, y + height} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value*15 > 2147483647 {
			return rectF{}, errf(CodeInvalidRequest, "选中的工作表区域超出可渲染坐标范围")
		}
	}
	return rectF{X: x, Y: y, Width: width, Height: height}, nil
}

// pageGeom 是引擎回报的一页（X/Y 为 twips 偏移，Width/Height 为点）。
type pageGeom struct {
	Part   int
	X      float64
	Y      float64
	Width  float64
	Height float64
}

// WriterPages 解析 Writer 的页面矩形串（官方 cli.js:284-310 writerPages）。
//
// 引擎用零面积的占位矩形表示「没有可见页面」，必须跳过；尺寸单位是 twips
// 而输出用点，所以除 15。
func WriterPages(rectangles string) ([]pageGeom, error) {
	var pages []pageGeom
	for _, chunk := range strings.Split(rectangles, ";") {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		fields := strings.Split(chunk, ",")
		if len(fields) != 4 {
			return nil, errf(CodeInvalidDocument, "引擎回报的 Writer 页面矩形不合法")
		}
		values := make([]int64, 4)
		for i, field := range fields {
			value, err := strconv.ParseInt(strings.TrimSpace(field), 10, 64)
			if err != nil || value < 0 || value > 2147483647 {
				return nil, errf(CodeInvalidDocument, "引擎回报的 Writer 页面矩形越界")
			}
			values[i] = value
		}
		x, y, width, height := values[0], values[1], values[2], values[3]
		if (width == 0) != (height == 0) || x+width > 2147483647 || y+height > 2147483647 {
			return nil, errf(CodeInvalidDocument, "引擎回报的 Writer 页面矩形不合法")
		}
		if width == 0 {
			continue
		}
		pages = append(pages, pageGeom{
			Part: -1, X: float64(x), Y: float64(y),
			Width: float64(width) / 15, Height: float64(height) / 15,
		})
	}
	if len(pages) == 0 {
		return nil, errf(CodeInvalidDocument, "引擎回报文档没有可渲染页面")
	}
	return pages, nil
}

// TileDimensions 校验并算出这一块的像素尺寸与 twips 矩形（官方 cli.js:238-262）。
func TileDimensions(page pageGeom, rect rectF, scale float64) (width, height int, twips [4]int64, err error) {
	finite := func(values ...float64) bool {
		for _, value := range values {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return false
			}
		}
		return true
	}
	if !finite(rect.X, rect.Y, rect.Width, rect.Height, scale) || rect.X < 0 || rect.Y < 0 ||
		rect.Width <= 0 || rect.Height <= 0 || scale <= 0 ||
		rect.X+rect.Width > page.Width+0.001 || rect.Y+rect.Height > page.Height+0.001 {
		return 0, 0, twips, errf(CodeInvalidRequest, "渲染分块超出了所在页面")
	}
	width = int(math.Ceil(rect.Width*scale - 1e-9))
	height = int(math.Ceil(rect.Height*scale - 1e-9))
	if width <= 0 || height <= 0 || width*height > maxTilePixels {
		return 0, 0, twips, errf(CodeOutputTooLarge, "渲染分块超出 16 兆像素上限")
	}
	twips = [4]int64{
		int64(page.X + math.Round(rect.X*15)),
		int64(page.Y + math.Round(rect.Y*15)),
		int64(math.Round(rect.Width * 15)),
		int64(math.Round(rect.Height * 15)),
	}
	for _, value := range twips {
		if value < 0 || value > 2147483647 {
			return 0, 0, twips, errf(CodeInvalidRequest, "渲染分块坐标超出范围")
		}
	}
	if twips[0]+twips[2] > 2147483647 || twips[1]+twips[3] > 2147483647 {
		return 0, 0, twips, errf(CodeInvalidRequest, "渲染分块坐标超出范围")
	}
	return width, height, twips, nil
}

// RasterLimits 是切块上限（官方 splitRasterRectangle 的 limits 参数）。
type RasterLimits struct {
	MaxPixels    int
	MaxDimension int
	MaxTiles     int
}

// SplitRasterRectangle 把区域切成不超限的小块（官方 cli.js:184-237）。
func SplitRasterRectangle(rectangle rectF, scale float64, limits RasterLimits) ([]rectF, error) {
	finite := func(values ...float64) bool {
		for _, value := range values {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return false
			}
		}
		return true
	}
	if !finite(rectangle.X, rectangle.Y, rectangle.Width, rectangle.Height, scale) ||
		rectangle.X < 0 || rectangle.Y < 0 || rectangle.Width <= 0 || rectangle.Height <= 0 || scale <= 0 ||
		limits.MaxPixels < 1 || limits.MaxPixels > maxTilePixels || limits.MaxDimension < 1 ||
		limits.MaxDimension > maxTilePixels || limits.MaxTiles < 0 {
		return nil, errf(CodeInvalidRequest, "光栅切块参数不合法")
	}
	pixelWidth := int(math.Ceil(rectangle.Width*scale - 1e-9))
	pixelHeight := int(math.Ceil(rectangle.Height*scale - 1e-9))
	if pixelWidth <= 0 || pixelHeight <= 0 {
		return nil, errf(CodeOutputTooLarge, "光栅尺寸超出坐标范围")
	}
	tileWidth := minInt(pixelWidth, limits.MaxDimension)
	tileHeight := minInt(pixelHeight, limits.MaxDimension)
	if tileWidth*tileHeight > limits.MaxPixels {
		side := int(math.Floor(math.Sqrt(float64(limits.MaxPixels))))
		if tileWidth <= side {
			tileHeight = limits.MaxPixels / tileWidth
		} else if tileHeight <= side {
			tileWidth = limits.MaxPixels / tileHeight
		} else {
			tileWidth = side
			tileHeight = minInt(tileHeight, limits.MaxPixels/tileWidth)
		}
	}
	columns := (pixelWidth + tileWidth - 1) / tileWidth
	rows := (pixelHeight + tileHeight - 1) / tileHeight
	if columns*rows > limits.MaxTiles {
		return nil, errf(CodeOutputTooLarge, "工作表切块后超过 maxImages")
	}
	result := make([]rectF, 0, columns*rows)
	for row := 0; row < rows; row++ {
		for column := 0; column < columns; column++ {
			left := float64(column*tileWidth) / scale
			top := float64(row*tileHeight) / scale
			result = append(result, rectF{
				X:      rectangle.X + left,
				Y:      rectangle.Y + top,
				Width:  math.Min(float64(tileWidth)/scale, rectangle.Width-left),
				Height: math.Min(float64(tileHeight)/scale, rectangle.Height-top),
			})
		}
	}
	return result, nil
}

// SelectedPages 把请求的页列表收敛成实际要渲染的页（官方 cli.js:334-345）。
func SelectedPages(pages []int, count, limit int) ([]int, error) {
	if count < 1 {
		return nil, errf(CodeInvalidDocument, "文档没有可渲染的页面")
	}
	var result []int
	if len(pages) == 0 {
		upper := count
		if limit+1 < upper {
			upper = limit + 1
		}
		for page := 1; page <= upper; page++ {
			result = append(result, page)
		}
	} else {
		result = append(result, pages...)
	}
	if len(result) > limit {
		return nil, errf(CodeOutputTooLarge, "选中的页数超过 maxImages（%d）", limit)
	}
	for _, page := range result {
		if page > count {
			return nil, errf(CodeInvalidRequest, "选中的页 %d 超出 %d 页的文档", page, count)
		}
	}
	return result, nil
}

// RGBAPixels 把引擎给的像素转成 PNG 需要的直通 alpha RGBA
// （官方 cli.js:264-283 rgbaPixels：Cairo/LO 是预乘 alpha，tileMode 1 是 BGRA 序）。
func RGBAPixels(source []byte, tileMode int) ([]byte, error) {
	if (tileMode != 0 && tileMode != 1) || len(source)%4 != 0 {
		return nil, errf(CodeInvalidOutput, "引擎回报的像素格式不支持")
	}
	result := make([]byte, len(source))
	for index := 0; index < len(source); index += 4 {
		alpha := source[index+3]
		multiplier := 0.0
		if alpha != 0 {
			multiplier = 255 / float64(alpha)
		}
		redOffset, blueOffset := 0, 2
		if tileMode == 1 {
			redOffset, blueOffset = 2, 0
		}
		result[index] = scaleChannel(source[index+redOffset], multiplier)
		result[index+1] = scaleChannel(source[index+1], multiplier)
		result[index+2] = scaleChannel(source[index+blueOffset], multiplier)
		result[index+3] = alpha
	}
	return result, nil
}

// scaleChannel 复刻 JS 的 Uint8ClampedArray 语义：先乘再四舍五入，落在 [0,255]。
func scaleChannel(value byte, multiplier float64) byte {
	scaled := math.Round(float64(value) * multiplier)
	if scaled < 0 {
		return 0
	}
	if scaled > 255 {
		return 255
	}
	return byte(scaled)
}

// UnmirrorRTLTile 把 Calc 的 RTL 运输镜像翻回来（官方 cli.js:173-183）。
func UnmirrorRTLTile(width, height int, rgba []byte) {
	half := width / 2
	for y := 0; y < height; y++ {
		row := y * width * 4
		for x := 0; x < half; x++ {
			left := row + x*4
			right := row + (width-1-x)*4
			for channel := 0; channel < 4; channel++ {
				rgba[left+channel], rgba[right+channel] = rgba[right+channel], rgba[left+channel]
			}
		}
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
