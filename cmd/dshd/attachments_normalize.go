package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
)

// 官方 `dsh-attachment-local` 的三条图片归一化参数：
//
//   - `attachment.normalizedImageMaxPixels`（默认 4194304）：归一化时把图片缩到
//     ≤ 该像素数；
//   - `attachment.normalizedImageMaxBytes`（默认 4194304）：归一化后的字节目标；
//   - `attachment.imageCompressionConcurrency`（默认 2）：一条消息里多图压缩的并发路数。
//
// 宿主原来只**校验**不归一化（读 PNG/JPEG/WEBP 头判宽高），所以 8MiB 的相机原图会
// 原样 base64 进模型请求：既浪费上下文窗口，也会把超限的图直接送去网关。
//
// 为什么用标准库自己写缩放：本仓不引 golang.org/x/image（像素级缩放不在
// `image/png`、`image/jpeg` 里），而"把一张图缩到预算内"不需要滤镜质量——双线性
// 就够，而且行为可测。WebP **没有标准库解码器**，所以 WebP 走显式透传并在
// `Note` 里写明"未归一化"，绝不假装处理过。
const (
	// DefaultNormalizedImageMaxPixels 是官方 `attachment.normalizedImageMaxPixels` 的默认值。
	DefaultNormalizedImageMaxPixels = 4194304
	// DefaultNormalizedImageMaxBytes 是官方 `attachment.normalizedImageMaxBytes` 的默认值。
	DefaultNormalizedImageMaxBytes = 4194304
	// DefaultImageCompressionConcurrency 是官方 `attachment.imageCompressionConcurrency` 的默认值。
	DefaultImageCompressionConcurrency = 2
	// DefaultMaxImagesPerMessage 是官方 `attachment.maxImagesPerMessage` 的默认值。
	DefaultMaxImagesPerMessage = 20

	// EnvNormalizedImageMaxPixels 覆盖图片归一化像素预算。
	EnvNormalizedImageMaxPixels = "DSHD_NORMALIZED_IMAGE_MAX_PIXELS"
	// EnvNormalizedImageMaxBytes 覆盖图片归一化字节预算。
	EnvNormalizedImageMaxBytes = "DSHD_NORMALIZED_IMAGE_MAX_BYTES"
	// EnvImageCompressionConcurrency 覆盖多图压缩并发路数。
	EnvImageCompressionConcurrency = "DSHD_IMAGE_COMPRESSION_CONCURRENCY"
	// EnvMaxImagesPerMessage 覆盖单条消息的图片张数上限（0 = 不限）。
	EnvMaxImagesPerMessage = "DSHD_MAX_IMAGES_PER_MESSAGE"
)

// imageNormalizeOptions 是一次消息级归一化用的预算。
type imageNormalizeOptions struct {
	MaxPixels   int64
	MaxBytes    int
	Concurrency int
}

// imageNormalizeOptionsFromEnv 读三条官方参数的当前值（默认值逐值等于官方）。
func imageNormalizeOptionsFromEnv() imageNormalizeOptions {
	return imageNormalizeOptions{
		MaxPixels:   envPositiveInt64(EnvNormalizedImageMaxPixels, DefaultNormalizedImageMaxPixels),
		MaxBytes:    int(envPositiveInt64(EnvNormalizedImageMaxBytes, DefaultNormalizedImageMaxBytes)),
		Concurrency: int(envPositiveInt64(EnvImageCompressionConcurrency, DefaultImageCompressionConcurrency)),
	}
}

// normalizedImage 是一次归一化的结果。Reencoded=false 时 Data 就是输入字节
// （已在预算内，或格式无解码器），Note 写明原因。
type normalizedImage struct {
	Data      []byte
	MIME      string
	Width     int
	Height    int
	Reencoded bool
	Note      string
}

// pendingImage 是待归一化的一张图（消息里的一个图片附件）。
type pendingImage struct {
	Data []byte
	MIME string
}

// normalizeImageFn 是测试注入点：并发断言需要能放慢单张图的处理。
var normalizeImageFn = normalizeImageBytes

// normalizeMessageImages 把一条消息里的图片按官方并发度（默认 2）归一化，
// 返回与输入**同序**的结果（模型看到的图片顺序不能因并发而变）。
func normalizeMessageImages(items []pendingImage, opts imageNormalizeOptions) ([]normalizedImage, error) {
	out := make([]normalizedImage, len(items))
	if len(items) == 0 {
		return out, nil
	}
	conc := opts.Concurrency
	if conc < 1 {
		conc = 1
	}
	if conc > len(items) {
		conc = len(items)
	}
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	queue := make(chan int)
	for i := 0; i < conc; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range queue {
				res, err := normalizeImageFn(items[idx].Data, items[idx].MIME, opts)
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					continue
				}
				out[idx] = res
			}
		}()
	}
	for i := range items {
		queue <- i
	}
	close(queue)
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

// normalizeImageBytes 把一张图缩到官方预算内。
//
// 顺序：解码 → （像素超预算）等比缩小 → 重编码 →（字节仍超预算）继续缩，最多 24 轮。
// 已经在预算内的图**不重编码**：重编码只会丢质量、改字节，而官方归一化是"超了才动"。
// 解不开的格式（WebP 等）原样透传并在 Note 里说明——这里宁可让调用方看到
// "未归一化"，也不要交出一份被偷偷改坏的图片。
func normalizeImageBytes(data []byte, mime string, opts imageNormalizeOptions) (normalizedImage, error) {
	out := normalizedImage{Data: data, MIME: mime}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		out.Note = "无标准库解码器（PNG/JPEG 以外），按原样透传，未做归一化"
		return out, nil
	}
	out.Width, out.Height = cfg.Width, cfg.Height
	pixels := int64(cfg.Width) * int64(cfg.Height)
	if pixels <= opts.MaxPixels && len(data) <= opts.MaxBytes {
		return out, nil
	}

	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		out.Note = "解码失败，按原样透传: " + err.Error()
		return out, nil
	}
	w, h := scaledDimensions(cfg.Width, cfg.Height, opts.MaxPixels)
	img := resizeBilinear(src, w, h)
	for attempt := 0; attempt < 24; attempt++ {
		encoded, mimeOut, encErr := encodeWithinBudget(img, format, opts.MaxBytes)
		if encErr != nil {
			return out, encErr
		}
		if len(encoded) <= opts.MaxBytes {
			out.Data = encoded
			out.MIME = mimeOut
			out.Width, out.Height = w, h
			out.Reencoded = true
			return out, nil
		}
		nw, nh := int(float64(w)*0.85), int(float64(h)*0.85)
		if nw < 1 {
			nw = 1
		}
		if nh < 1 {
			nh = 1
		}
		if nw == w && nh == h { // 1×1 已经压不动了
			break
		}
		img = resizeBilinear(img, nw, nh)
		w, h = nw, nh
	}
	return out, fmt.Errorf("图片归一化后仍超过 %d bytes 预算（%dx%d 已到下限）", opts.MaxBytes, w, h)
}

// scaledDimensions 按像素预算等比缩放：面积超了就乘 sqrt(预算/面积)。
func scaledDimensions(w, h int, maxPixels int64) (int, int) {
	if w <= 0 || h <= 0 || maxPixels <= 0 {
		return w, h
	}
	pixels := int64(w) * int64(h)
	if pixels <= maxPixels {
		return w, h
	}
	scale := math.Sqrt(float64(maxPixels) / float64(pixels))
	nw := int(float64(w) * scale)
	nh := int(float64(h) * scale)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	return nw, nh
}

// encodeWithinBudget 按原格式重编码；JPEG 先试几档质量，PNG 只做单次编码
// （PNG 是无损格式，没有"降质量"这个旋钮，只能靠缩像素降字节）。
func encodeWithinBudget(img image.Image, format string, maxBytes int) ([]byte, string, error) {
	switch format {
	case "jpeg", "jpg":
		var last []byte
		for _, quality := range []int{85, 75, 65, 55, 40} {
			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
				return nil, "", fmt.Errorf("JPEG 重编码失败: %w", err)
			}
			last = buf.Bytes()
			if len(last) <= maxBytes {
				return last, "image/jpeg", nil
			}
		}
		return last, "image/jpeg", nil
	case "png":
		var buf bytes.Buffer
		enc := png.Encoder{CompressionLevel: png.DefaultCompression}
		if err := enc.Encode(&buf, img); err != nil {
			return nil, "", fmt.Errorf("PNG 重编码失败: %w", err)
		}
		return buf.Bytes(), "image/png", nil
	default:
		return nil, "", fmt.Errorf("不支持的图片格式 %q", format)
	}
}

// resizeBilinear 双线性缩放。先把源图物化到 NRGBA（每像素只调一次 At），
// 再在 Pix 上做插值——直接对 image.Image 的 At 做插值在 8M 像素量级上太慢。
func resizeBilinear(src image.Image, dstW, dstH int) *image.NRGBA {
	if dstW < 1 || dstH < 1 {
		dstW, dstH = 1, 1
	}
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	if sw <= 0 || sh <= 0 {
		return image.NewNRGBA(image.Rect(0, 0, dstW, dstH))
	}
	flat := image.NewNRGBA(image.Rect(0, 0, sw, sh))
	for y := 0; y < sh; y++ {
		for x := 0; x < sw; x++ {
			r, g, b, a := src.At(sb.Min.X+x, sb.Min.Y+y).RGBA()
			flat.SetNRGBA(x, y, color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: uint8(a >> 8)})
		}
	}
	if sw == dstW && sh == dstH {
		return flat
	}
	dst := image.NewNRGBA(image.Rect(0, 0, dstW, dstH))
	for dy := 0; dy < dstH; dy++ {
		fy := (float64(dy)+0.5)*float64(sh)/float64(dstH) - 0.5
		y0 := int(math.Floor(fy))
		ty := fy - float64(y0)
		if y0 < 0 {
			y0, ty = 0, 0
		}
		y1 := y0 + 1
		if y1 >= sh {
			y1 = sh - 1
		}
		for dx := 0; dx < dstW; dx++ {
			fx := (float64(dx)+0.5)*float64(sw)/float64(dstW) - 0.5
			x0 := int(math.Floor(fx))
			tx := fx - float64(x0)
			if x0 < 0 {
				x0, tx = 0, 0
			}
			x1 := x0 + 1
			if x1 >= sw {
				x1 = sw - 1
			}
			i00 := flat.PixOffset(x0, y0)
			i10 := flat.PixOffset(x1, y0)
			i01 := flat.PixOffset(x0, y1)
			i11 := flat.PixOffset(x1, y1)
			for c := 0; c < 4; c++ {
				top := float64(flat.Pix[i00+c])*(1-tx) + float64(flat.Pix[i10+c])*tx
				bottom := float64(flat.Pix[i01+c])*(1-tx) + float64(flat.Pix[i11+c])*tx
				dst.Pix[dst.PixOffset(dx, dy)+c] = uint8(top*(1-ty) + bottom*ty + 0.5)
			}
		}
	}
	return dst
}

// maxImagesPerMessage 解析单条消息的图片张数上限：默认 20；0 = 不限。
func maxImagesPerMessage() int {
	raw := strings.TrimSpace(os.Getenv(EnvMaxImagesPerMessage))
	if raw == "" {
		return DefaultMaxImagesPerMessage
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return DefaultMaxImagesPerMessage
	}
	return n
}

// checkImagesPerMessage 在消息准入点强制官方上限（0 = 不限）。
//
// 超限是**参数错误**（400 四字段），不是静默丢图：用户发 21 张图却只送前 20 张，
// 那不是"限制生效"，是"少发了东西却不说"。
func checkImagesPerMessage(count int) error {
	limit := maxImagesPerMessage()
	if limit <= 0 || count <= limit {
		return nil
	}
	return attachmentBadRequest(fmt.Sprintf("一条消息最多 %d 张图片（当前 %d 张），用 %s 可调整上限",
		limit, count, EnvMaxImagesPerMessage), nil)
}

// envPositiveInt64 读一个正整数环境变量；空/非法/非正返回默认值。
func envPositiveInt64(name string, def int64) int64 {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
