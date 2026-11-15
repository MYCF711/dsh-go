package officekit

// 页面光栅渲染：`office_render` 的引擎侧。
//
// 官方走的是**流式工作进程协议**而不是一次性转换（libreoffice-kit/lib/cli.js:1080-1250）：
//
//	起进程：--operation render-images --program-directory … --input-bytes N --input-format ext
//	        --scratch-directory … --profile-directory … --max-output-bytes … --max-image-resolution …
//	读一行：{"ok":true,"kind":"ready","documentType":"text|presentation|pdf|spreadsheet","tileMode":0|1,
//	         "pages":[{width,height}] | "writerRectangles":"x, y, w, h;…" | "sheets":[{part,info,geometry}]}
//	发一块：P <序号> <part> <像素宽> <像素高> <x> <y> <twips宽> <twips高>
//	读一行：{"ok":true,"kind":"paint","index":N,"width":W,"height":H,"path":"…/tile-N.rgba"}
//	收尾：  DONE  → {"ok":true,"kind":"done"} → 进程退出
//
// 像素落成 RGBA 裸文件（预乘 alpha，tileMode 1 为 BGRA），PNG 编码是宿主这侧的活
// （官方用 cli.js:945-1000 的手写编码器；我们用 image/png）。

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// defaultRenderDPI 是官方 renderImages 的默认分辨率（cli.js:1141 dpi ?? 144）。
	defaultRenderDPI = 144
	// defaultMaxImages 是官方 renderImages 的默认页数上限（cli.js:1142）。
	defaultMaxImages = 100
	// defaultMaxImagePixels / defaultMaxImageDimension 同 cli.js:1143-1144。
	defaultMaxImagePixels    = 16777216
	defaultMaxImageDimension = 8192
	// maxRenderLine 是单行工作进程响应的上限（防御畸形输出）。
	maxRenderLine = 1 << 20
)

// ImageRequest 是一次光栅渲染请求（官方 OfficeImagesRequest 的收敛版，
// dsh-office-kit/src/types.ts:84-104）。
type ImageRequest struct {
	// Pages 是 1 基的页/幻灯片序号；空表示全部。工作表不接受它。
	Pages []int
	// Sheet 是精确工作表名；空表示所有可见工作表。
	Sheet string
	// Range 是 sheet 内的 A1 矩形；空表示已用区域。
	Range string
	// DPI 是 24..600 的分辨率；0 用默认 144。
	DPI int
	// MaxImages 是返回图片数上限（含工作表分块）。
	MaxImages int
	// MaxPixels 是单图像素上限。
	MaxPixels int
	// MaxDimension 是单图边长上限。
	MaxDimension int
	// MaxBytes 是全部 PNG 的总字节上限；0 用 provider 的 MaxOutputBytes。
	MaxBytes int64
}

// Image 是一张渲染出来的 PNG 及其位置。
type Image struct {
	PNG    []byte
	Width  int
	Height int
	Page   int // 0 表示工作表区域（没有页号）
	Sheet  string
	Range  string
}

// ImagesResult 是一次渲染的结果（官方 OfficeImagesResult）。
type ImagesResult struct {
	Images       []Image
	PageCount    int
	DPI          int
	MissingFonts []string
}

// RenderImages 把文档的选定页/工作表区域渲染成 PNG。
func (p *Provider) RenderImages(ctx context.Context, absolutePath string, req ImageRequest) (ImagesResult, error) {
	src, err := p.inspect(absolutePath, false)
	if err != nil {
		return ImagesResult{}, err
	}
	family, _ := FamilyOf(src.path)
	plan, err := normalizeImageRequest(family, req)
	if err != nil {
		return ImagesResult{}, err
	}
	if plan.MaxBytes <= 0 {
		plan.MaxBytes = p.opts.MaxOutputBytes
	}
	release, err := p.acquire(ctx, "foreground")
	if err != nil {
		return ImagesResult{}, err
	}
	defer release()
	return p.renderImages(ctx, src, family, plan)
}

func normalizeImageRequest(family string, req ImageRequest) (ImageRequest, error) {
	if req.DPI == 0 {
		req.DPI = defaultRenderDPI
	}
	if req.DPI < 24 || req.DPI > 600 {
		return req, errf(CodeInvalidRequest, "dpi 必须是 24 到 600 之间的整数，拿到 %d", req.DPI)
	}
	if req.MaxImages <= 0 {
		req.MaxImages = defaultMaxImages
	}
	if req.MaxPixels <= 0 {
		req.MaxPixels = defaultMaxImagePixels
	}
	if req.MaxDimension <= 0 {
		req.MaxDimension = defaultMaxImageDimension
	}
	if req.MaxPixels > defaultMaxImagePixels {
		return req, errf(CodeInvalidRequest, "maxPixels 不能超过 %d", defaultMaxImagePixels)
	}
	if req.MaxDimension > defaultMaxImagePixels {
		return req, errf(CodeInvalidRequest, "maxDimension 不能超过 %d", defaultMaxImagePixels)
	}
	calc := family == FamilySpreadsheet
	if calc && len(req.Pages) > 0 {
		return req, errf(CodeInvalidRequest, "工作表用 sheet + range 选区域，不接受 pages")
	}
	if !calc && (strings.TrimSpace(req.Sheet) != "" || strings.TrimSpace(req.Range) != "") {
		return req, errf(CodeInvalidRequest, "sheet / range 只对工作表有效")
	}
	req.Sheet = strings.TrimSpace(req.Sheet)
	req.Range = strings.TrimSpace(req.Range)
	if req.Range != "" && req.Sheet == "" {
		return req, errf(CodeInvalidRequest, "range 必须配合精确的 sheet 名")
	}
	if req.Range != "" {
		if _, err := ParseCellRange(req.Range); err != nil {
			return req, err
		}
	}
	seen := map[int]bool{}
	for _, page := range req.Pages {
		if page < 1 || seen[page] {
			return req, errf(CodeInvalidRequest, "pages 必须是互不相同、从 1 开始的页号")
		}
		seen[page] = true
	}
	if len(req.Pages) > req.MaxImages {
		return req, errf(CodeOutputTooLarge, "选中的页数超过 maxImages（%d）", req.MaxImages)
	}
	return req, nil
}

// region 是一个待渲染分块。
type region struct {
	Page   pageGeom
	Number int // 0 = 工作表区域
	Sheet  string
	Range  string
	RTL    bool
	Rect   rectF
}

func (p *Provider) renderImages(ctx context.Context, src source, family string, req ImageRequest) (ImagesResult, error) {
	engine, _, err := p.ensureEngine(ctx)
	if err != nil {
		return ImagesResult{}, err
	}
	scratch, err := os.MkdirTemp("", "dsh-office-kit-images-")
	if err != nil {
		return ImagesResult{}, errf(CodeFailed, "无法创建临时目录：%v", err)
	}
	defer os.RemoveAll(scratch)
	profile := filepath.Join(scratch, "profile")
	if err := os.MkdirAll(profile, 0o700); err != nil {
		return ImagesResult{}, errf(CodeFailed, "无法创建私有 profile：%v", err)
	}
	prepared, release, err := prepareNativeEngine(engine, scratch)
	if err != nil {
		return ImagesResult{}, err
	}
	defer release()
	baseArgs := []string{
		"--operation", "render-images",
		"--program-directory", prepared.ProgramDirectory,
		"--scratch-directory", scratch,
		"--profile-directory", profile,
		"--max-output-bytes", strconv.FormatInt(p.opts.MaxOutputBytes, 10),
		"--max-image-resolution", strconv.Itoa(p.opts.MaxImageResolution),
	}
	session, ready, err := p.openRenderSession(ctx, prepared, profile, baseArgs, src)
	if err != nil {
		return ImagesResult{}, err
	}
	defer session.close()
	if ready.Kind != "ready" {
		return ImagesResult{}, errf(CodeInvalidDocument, "原生渲染器没有完成初始化")
	}
	scale := float64(req.DPI) / 96
	regions, pageCount, err := planRegions(ready, req, scale)
	if err != nil {
		return ImagesResult{}, err
	}
	result := ImagesResult{PageCount: pageCount, DPI: req.DPI, MissingFonts: []string{}}
	var totalBytes int64
	for _, item := range regions {
		width, height, twips, err := TileDimensions(item.Page, item.Rect, scale)
		if err != nil {
			return ImagesResult{}, err
		}
		if width > req.MaxDimension || height > req.MaxDimension || width*height > req.MaxPixels {
			return ImagesResult{}, errf(CodeOutputTooLarge, "选中区域超过 maxPixels 或 maxDimension；降低 dpi 或选更小的范围")
		}
		index := len(result.Images) + 1
		if err := session.writeLine(fmt.Sprintf("P %d %d %d %d %d %d %d %d\n",
			index, item.Page.Part, width, height, twips[0], twips[1], twips[2], twips[3])); err != nil {
			return ImagesResult{}, err
		}
		painted, err := session.readJSON()
		if err != nil {
			return ImagesResult{}, err
		}
		if !painted.OK {
			return ImagesResult{}, workerError(painted, session.stderrString())
		}
		if painted.Kind != "paint" || painted.Index != index || painted.Width != width || painted.Height != height {
			return ImagesResult{}, errf(CodeInvalidOutput, "原生渲染器回报的分块元数据不一致")
		}
		raw, err := readTile(scratch, painted.Path, index, width, height)
		if err != nil {
			return ImagesResult{}, err
		}
		pixels := raw
		if ready.DocumentType != "pdf" {
			pixels, err = RGBAPixels(raw, ready.TileMode)
			if err != nil {
				return ImagesResult{}, err
			}
		}
		if item.RTL {
			UnmirrorRTLTile(width, height, pixels)
		}
		encoded, err := encodePNG(width, height, pixels)
		if err != nil {
			return ImagesResult{}, err
		}
		totalBytes += int64(len(encoded))
		if totalBytes > req.MaxBytes {
			return ImagesResult{}, errf(CodeOutputTooLarge, "渲染出的图片总字节超过 %d 上限", req.MaxBytes)
		}
		result.Images = append(result.Images, Image{
			PNG: encoded, Width: width, Height: height,
			Page: item.Number, Sheet: item.Sheet, Range: item.Range,
		})
	}
	if err := session.writeLine("DONE\n"); err != nil {
		return ImagesResult{}, err
	}
	done, err := session.readJSON()
	if err != nil {
		return ImagesResult{}, err
	}
	if !done.OK || done.Kind != "done" {
		return ImagesResult{}, errf(CodeFailed, "原生渲染器没有正常收尾")
	}
	if err := session.finish(); err != nil {
		return ImagesResult{}, err
	}
	return result, nil
}

// openRenderSession 起一个渲染进程并读到 ready 行。
//
// 传输顺序与官方一致：0.2.x 引擎吃 stdin 字节（--input-bytes/--input-format），
// 0.1.x 只认 --input-path。先走官方现行的那条，报「未知参数」时退回旧传输。
func (p *Provider) openRenderSession(ctx context.Context, engine *Engine, profile string, baseArgs []string, src source) (*renderSession, workerResponse, error) {
	data, err := readBounded(src.path, p.opts.MaxInputBytes)
	if err != nil {
		return nil, workerResponse{}, err
	}
	attempts := [][]string{
		append([]string{
			"--input-bytes", strconv.FormatInt(int64(len(data)), 10),
			"--input-format", ExtensionOf(src.path),
		}, baseArgs...),
		append([]string{"--input-path", src.path}, baseArgs...),
	}
	var lastErr error
	for i, args := range attempts {
		var stdin []byte
		if i == 0 {
			stdin = data
		}
		session, err := p.startRenderSession(ctx, engine, profile, args, stdin)
		if err != nil {
			return nil, workerResponse{}, err
		}
		response, readErr := session.readJSON()
		if readErr == nil && response.OK {
			return session, response, nil
		}
		stderr := session.stderrString()
		writeErr := session.writeErr
		session.close()
		if i == 0 && (isUnknownInputPath(stderr, response) || isUnknownArgument(stderr, response)) {
			lastErr = readErr
			continue
		}
		if response.Error != "" {
			return nil, response, workerError(response, stderr)
		}
		if readErr != nil {
			if writeErr != nil {
				return nil, response, errf(CodeFailed, "无法把源文件字节交给引擎：%v", writeErr)
			}
			return nil, response, readErr
		}
		return nil, response, workerError(response, stderr)
	}
	if lastErr == nil {
		lastErr = errf(CodeFailed, "原生渲染器无法接受两种输入传输")
	}
	return nil, workerResponse{}, lastErr
}

func isUnknownArgument(stderr string, response workerResponse) bool {
	text := response.Error + " " + stderr
	return strings.Contains(text, "Unknown worker argument")
}

// planRegions 把 ready 消息换算成待渲染分块（官方 cli.js:1005-1080 plan）。
func planRegions(ready workerResponse, req ImageRequest, scale float64) ([]region, int, error) {
	simplePages := func(pages []pageGeom) ([]region, int, error) {
		selected, err := SelectedPages(req.Pages, len(pages), req.MaxImages)
		if err != nil {
			return nil, 0, err
		}
		regions := make([]region, 0, len(selected))
		for _, number := range selected {
			page := pages[number-1]
			regions = append(regions, region{
				Page: page, Number: number,
				Rect: rectF{X: 0, Y: 0, Width: page.Width, Height: page.Height},
			})
		}
		return regions, len(pages), nil
	}
	switch ready.DocumentType {
	case "pdf", "presentation":
		if len(ready.Pages) == 0 {
			return nil, 0, errf(CodeInvalidDocument, "引擎没有回报页面尺寸")
		}
		pages := make([]pageGeom, 0, len(ready.Pages))
		for part, raw := range ready.Pages {
			if raw.Width < 1 || raw.Height < 1 {
				return nil, 0, errf(CodeInvalidDocument, "引擎回报的页面尺寸不合法")
			}
			pages = append(pages, pageGeom{
				Part: part, Width: float64(raw.Width) / 15, Height: float64(raw.Height) / 15,
			})
		}
		return simplePages(pages)
	case "text":
		if strings.TrimSpace(ready.WriterRectangles) == "" {
			return nil, 0, errf(CodeInvalidDocument, "引擎没有回报 Writer 页面矩形")
		}
		pages, err := WriterPages(ready.WriterRectangles)
		if err != nil {
			return nil, 0, err
		}
		return simplePages(pages)
	default:
		type sheetEntry struct {
			part     int
			info     sheetInfo
			geometry sheetGeometry
		}
		sheets := make([]sheetEntry, 0, len(ready.Sheets))
		visible := 0
		for _, raw := range ready.Sheets {
			var info sheetInfo
			if err := json.Unmarshal([]byte(raw.Info), &info); err != nil || !info.valid() {
				return nil, 0, errf(CodeInvalidDocument, "引擎回报的工作表元数据不合法")
			}
			var geometry sheetGeometry
			if err := json.Unmarshal([]byte(raw.Geometry), &geometry); err != nil {
				return nil, 0, errf(CodeInvalidDocument, "引擎回报的工作表几何不是合法 JSON")
			}
			if info.Visible == 1 {
				visible++
			}
			sheets = append(sheets, sheetEntry{part: raw.Part, info: info, geometry: geometry})
		}
		if visible == 0 {
			return nil, 0, errf(CodeInvalidDocument, "工作簿没有可见工作表")
		}
		var chosen []sheetEntry
		for _, entry := range sheets {
			if req.Sheet == "" {
				if entry.info.Visible == 1 {
					chosen = append(chosen, entry)
				}
			} else if entry.info.Name == req.Sheet {
				chosen = append(chosen, entry)
			}
		}
		if len(chosen) == 0 {
			return nil, 0, errf(CodeInvalidDocument, "指定的工作表名不存在或工作簿没有可见工作表")
		}
		if len(chosen) > req.MaxImages {
			return nil, 0, errf(CodeOutputTooLarge, "选中的工作表数量超过 maxImages；请指定单个 sheet")
		}
		var regions []region
		for _, entry := range chosen {
			target := cellRange{FirstColumn: 0, FirstRow: 0, LastColumn: entry.info.LastColumn, LastRow: entry.info.LastRow}
			if req.Range != "" {
				parsed, err := ParseCellRange(req.Range)
				if err != nil {
					return nil, 0, err
				}
				target = parsed
			}
			rectangle, err := SheetRangeRectangle(entry.geometry, target, scale)
			if err != nil {
				return nil, 0, err
			}
			page := pageGeom{
				Part: entry.part, X: 0, Y: 0,
				Width: rectangle.X + rectangle.Width, Height: rectangle.Y + rectangle.Height,
			}
			tiles, err := SplitRasterRectangle(rectangle, scale, RasterLimits{
				MaxPixels: req.MaxPixels, MaxDimension: req.MaxDimension,
				MaxTiles: req.MaxImages - len(regions),
			})
			if err != nil {
				return nil, 0, err
			}
			for _, tile := range tiles {
				regions = append(regions, region{
					Page: page, Sheet: entry.info.Name, Range: RangeName(target),
					RTL: entry.info.RTLLayout == 1, Rect: tile,
				})
			}
		}
		return regions, visible, nil
	}
}

// readTile 读一块裸 RGBA，并校验它确实落在自己的 scratch 目录里
// （官方 cli.js:1203 的「图像不能跑出输出目录」同一条）。
func readTile(scratch, reported string, index, width, height int) ([]byte, error) {
	expected := fmt.Sprintf("tile-%d.rgba", index)
	if !strings.EqualFold(filepath.Base(filepath.FromSlash(reported)), expected) {
		return nil, errf(CodeInvalidOutput, "原生渲染器回报的分块路径不在私有 scratch 目录里")
	}
	path := filepath.Join(scratch, expected)
	info, err := os.Stat(path)
	if err != nil {
		return nil, errf(CodeInvalidOutput, "原生渲染器没有写出分块像素：%v", err)
	}
	want := int64(width) * int64(height) * 4
	if info.Size() != want {
		return nil, errf(CodeInvalidOutput, "原生渲染器回报的像素数量不完整（%d != %d）", info.Size(), want)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errf(CodeInvalidOutput, "读取分块像素失败：%v", err)
	}
	if int64(len(raw)) != want {
		return nil, errf(CodeInvalidOutput, "读取到的像素数量不完整")
	}
	return raw, nil
}

func encodePNG(width, height int, rgba []byte) ([]byte, error) {
	if width <= 0 || height <= 0 || len(rgba) != width*height*4 {
		return nil, errf(CodeInvalidOutput, "PNG 编码入参不合法")
	}
	img := &image.NRGBA{Pix: rgba, Stride: width * 4, Rect: image.Rect(0, 0, width, height)}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, errf(CodeInvalidOutput, "PNG 编码失败：%v", err)
	}
	return buf.Bytes(), nil
}

// renderSession 是一个活着的 render-images 工作进程。
type renderSession struct {
	stop   func()
	wait   func() error
	stdin  io.WriteCloser
	buffer *bufio.Writer
	stdout *bufio.Reader
	stderr *limitedBuffer
	done   chan struct{}
	closed bool
	// writeErr 记录把源字节交给引擎时的写入失败。引擎可能先报错再关 stdin，
	// 所以这里**不立刻失败**：先读它给出的 JSON，才能报出真正的原因。
	writeErr error
}

func (p *Provider) startRenderSession(ctx context.Context, engine *Engine, profile string, args []string, stdin []byte) (*renderSession, error) {
	cmd := exec.Command(engine.Executable, args...)
	cmd.Dir = filepath.Dir(engine.Executable)
	cmd.Env = nativeEnvironmentFor(engine, profile)
	cmd.WaitDelay = waitDelay
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, errf(CodeFailed, "无法接入引擎 stdin：%v", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, errf(CodeFailed, "无法接入引擎 stdout：%v", err)
	}
	stderrBuffer := &limitedBuffer{limit: maxWorkerStdout}
	cmd.Stderr = stderrBuffer
	stop, wait, err := startManaged(cmd)
	if err != nil {
		return nil, errf(CodeFailed, "无法启动原生渲染器 %s：%v", engine.Executable, err)
	}
	session := &renderSession{
		stop: stop, wait: wait, stdin: stdinPipe,
		buffer: bufio.NewWriterSize(stdinPipe, 64*1024),
		stdout: bufio.NewReaderSize(stdoutPipe, 64*1024),
		stderr: stderrBuffer,
		done:   make(chan struct{}),
	}
	go func() {
		select {
		case <-ctx.Done():
			session.stop()
		case <-session.done:
		}
	}()
	if stdin != nil {
		if _, err := session.buffer.Write(stdin); err != nil {
			session.writeErr = err
		} else if err := session.buffer.Flush(); err != nil {
			session.writeErr = err
		}
	}
	return session, nil
}

func (s *renderSession) writeLine(line string) error {
	if _, err := s.buffer.WriteString(line); err != nil {
		return errf(CodeFailed, "写入引擎 stdin 失败：%v", err)
	}
	if err := s.buffer.Flush(); err != nil {
		return errf(CodeFailed, "刷新引擎 stdin 失败：%v", err)
	}
	return nil
}

func (s *renderSession) readJSON() (workerResponse, error) {
	line, err := s.readLimitedLine()
	if err != nil {
		return workerResponse{}, err
	}
	var response workerResponse
	if err := json.Unmarshal([]byte(line), &response); err != nil {
		return response, errf(CodeInvalidOutput, "原生渲染器返回了非法 JSON：%s", truncateForError(line))
	}
	return response, nil
}

func (s *renderSession) readLimitedLine() (string, error) {
	buffer := make([]byte, 0, 256)
	for {
		chunk, err := s.stdout.ReadSlice('\n')
		buffer = append(buffer, chunk...)
		if len(buffer) > maxRenderLine {
			return "", errf(CodeInvalidOutput, "原生渲染器的响应超过单行上限")
		}
		if err == nil {
			return strings.TrimRight(string(buffer), "\r\n"), nil
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		return "", errf(CodeFailed, "读取原生渲染器响应失败：%v %s", err, s.stderrString())
	}
}

func (s *renderSession) stderrString() string {
	if s.stderr == nil {
		return ""
	}
	return s.stderr.String()
}

// finish 关 stdin、等进程退出并核对退出码（官方 cli.js:1265-1270）。
func (s *renderSession) finish() error {
	s.closed = true
	close(s.done)
	_ = s.buffer.Flush()
	_ = s.stdin.Close()
	waitErr := s.wait()
	if waitErr != nil {
		return errf(CodeFailed, "原生渲染器退出异常：%v %s", waitErr, s.stderrString())
	}
	return nil
}

func (s *renderSession) close() {
	if s == nil || s.closed {
		return
	}
	s.closed = true
	close(s.done)
	if s.buffer != nil {
		_ = s.buffer.Flush()
	}
	if s.stdin != nil {
		_ = s.stdin.Close()
	}
	s.stop()
	_ = s.wait()
}

func truncateForError(text string) string {
	if len(text) > 400 {
		return text[:400] + "…"
	}
	return text
}
