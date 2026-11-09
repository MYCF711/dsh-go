package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"dshgo/internal/transport"
)

// writePNG 生成一张测试 PNG。noisy=true 时像素伪随机（PNG 压不动，用来造字节预算压力）。
func writePNG(t *testing.T, dir, name string, w, h int, noisy bool) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewSource(42))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if noisy {
				img.SetNRGBA(x, y, color.NRGBA{R: uint8(rng.Intn(256)), G: uint8(rng.Intn(256)), B: uint8(rng.Intn(256)), A: 255})
				continue
			}
			img.SetNRGBA(x, y, color.NRGBA{R: 120, G: 160, B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("生成测试 PNG 失败: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("写测试 PNG 失败: %v", err)
	}
	return path
}

// TestAttachmentNormalizeEnvDefaults 钉住三条官方默认值（4194304 / 4194304 / 2）
// 与单条消息图片上限的默认值（20），以及 env 覆盖语义。
func TestAttachmentNormalizeEnvDefaults(t *testing.T) {
	if DefaultNormalizedImageMaxPixels != 4194304 || DefaultNormalizedImageMaxBytes != 4194304 ||
		DefaultImageCompressionConcurrency != 2 || DefaultMaxImagesPerMessage != 20 {
		t.Fatalf("官方默认值必须逐值一致：4194304/4194304/2/20，实际 %d/%d/%d/%d",
			DefaultNormalizedImageMaxPixels, DefaultNormalizedImageMaxBytes,
			DefaultImageCompressionConcurrency, DefaultMaxImagesPerMessage)
	}
	t.Setenv(EnvNormalizedImageMaxPixels, "")
	t.Setenv(EnvNormalizedImageMaxBytes, "")
	t.Setenv(EnvImageCompressionConcurrency, "")
	t.Setenv(EnvMaxImagesPerMessage, "")
	opts := imageNormalizeOptionsFromEnv()
	if opts.MaxPixels != 4194304 || opts.MaxBytes != 4194304 || opts.Concurrency != 2 {
		t.Fatalf("默认归一化预算 = %+v", opts)
	}
	if got := maxImagesPerMessage(); got != 20 {
		t.Fatalf("每消息图片上限 = %d，期望 20", got)
	}

	t.Setenv(EnvNormalizedImageMaxPixels, "1000000")
	t.Setenv(EnvNormalizedImageMaxBytes, "65536")
	t.Setenv(EnvImageCompressionConcurrency, "4")
	t.Setenv(EnvMaxImagesPerMessage, "0")
	opts = imageNormalizeOptionsFromEnv()
	if opts.MaxPixels != 1000000 || opts.MaxBytes != 65536 || opts.Concurrency != 4 {
		t.Fatalf("env 覆盖后归一化预算 = %+v", opts)
	}
	if got := maxImagesPerMessage(); got != 0 {
		t.Fatalf("env=0 应当表示不限，实际 %d", got)
	}
	t.Setenv(EnvNormalizedImageMaxPixels, "bogus")
	if got := imageNormalizeOptionsFromEnv().MaxPixels; got != 4194304 {
		t.Fatalf("非法像素预算应退回默认，实际 %d", got)
	}
}

// TestNormalizeImagePixelBudget 钉住 `attachment.normalizedImageMaxPixels`：
// 3000×2000（6M 像素，超 4194304）必须被缩到预算内，且结果可解码、长宽比不变。
func TestNormalizeImagePixelBudget(t *testing.T) {
	path := writePNG(t, t.TempDir(), "big.png", 3000, 2000, false)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读测试 PNG: %v", err)
	}
	res, err := normalizeImageBytes(data, "image/png", imageNormalizeOptions{
		MaxPixels: 4194304, MaxBytes: 4194304, Concurrency: 2,
	})
	if err != nil {
		t.Fatalf("归一化失败: %v", err)
	}
	if !res.Reencoded {
		t.Fatalf("超像素预算的图必须被重编码，结果 %+v", res)
	}
	if int64(res.Width)*int64(res.Height) > 4194304 {
		t.Fatalf("归一化后仍有 %d×%d = %d 像素，超过 4194304", res.Width, res.Height, int64(res.Width)*int64(res.Height))
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(res.Data))
	if err != nil {
		t.Fatalf("归一化结果解不开: %v", err)
	}
	if cfg.Width != res.Width || cfg.Height != res.Height {
		t.Fatalf("报告的尺寸 %dx%d 与实际 %dx%d 不符", res.Width, res.Height, cfg.Width, cfg.Height)
	}
	// 长宽比保持（3:2）：允许 1 像素取整误差。
	if ratio := float64(res.Width) / float64(res.Height); ratio < 1.45 || ratio > 1.55 {
		t.Fatalf("长宽比被破坏：%dx%d", res.Width, res.Height)
	}
}

// TestNormalizeImageByteBudget 钉住 `attachment.normalizedImageMaxBytes`：
// 字节仍超预算就继续缩像素，直到进了预算（结果 ≤ 预算且可解码）。
func TestNormalizeImageByteBudget(t *testing.T) {
	path := writePNG(t, t.TempDir(), "noise.png", 600, 600, true)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读测试 PNG: %v", err)
	}
	const budget = 20000
	res, err := normalizeImageBytes(data, "image/png", imageNormalizeOptions{
		MaxPixels: 4194304, MaxBytes: budget, Concurrency: 2,
	})
	if err != nil {
		t.Fatalf("归一化失败: %v", err)
	}
	if len(res.Data) > budget {
		t.Fatalf("归一化后 %d bytes 仍超过预算 %d", len(res.Data), budget)
	}
	if _, _, err := image.DecodeConfig(bytes.NewReader(res.Data)); err != nil {
		t.Fatalf("归一化结果解不开: %v", err)
	}
	if res.Width >= 600 {
		t.Fatalf("字节超预算时应当缩像素，实际仍是 %dx%d", res.Width, res.Height)
	}
}

// TestNormalizeMessageImagesConcurrencyAndOrder 钉住 `attachment.imageCompressionConcurrency`
// （默认 2）：多图压缩最多 2 路并行，且结果顺序与输入一致。
func TestNormalizeMessageImagesConcurrencyAndOrder(t *testing.T) {
	sizes := []struct{ w, h int }{{8, 6}, {9, 7}, {10, 8}, {11, 9}}
	var items []pendingImage
	dir := t.TempDir()
	for i, s := range sizes {
		path := writePNG(t, dir, string(rune('a'+i))+".png", s.w, s.h, false)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读测试 PNG: %v", err)
		}
		items = append(items, pendingImage{Data: data, MIME: "image/png"})
	}

	orig := normalizeImageFn
	var inflight, maxInflight atomic.Int64
	normalizeImageFn = func(data []byte, mime string, opts imageNormalizeOptions) (normalizedImage, error) {
		cur := inflight.Add(1)
		for {
			max := maxInflight.Load()
			if cur <= max || maxInflight.CompareAndSwap(max, cur) {
				break
			}
		}
		defer inflight.Add(-1)
		time.Sleep(20 * time.Millisecond)
		return orig(data, mime, opts)
	}
	defer func() { normalizeImageFn = orig }()

	out, err := normalizeMessageImages(items, imageNormalizeOptions{MaxPixels: 4194304, MaxBytes: 4194304, Concurrency: 2})
	if err != nil {
		t.Fatalf("批量归一化失败: %v", err)
	}
	if n := maxInflight.Load(); n != 2 {
		t.Fatalf("图片压缩并发 = %d，期望 2（官方 imageCompressionConcurrency）", n)
	}
	for i, want := range sizes {
		if out[i].Width != want.w || out[i].Height != want.h {
			t.Fatalf("第 %d 张归一化结果 %dx%d，期望 %dx%d（顺序被打乱）",
				i, out[i].Width, out[i].Height, want.w, want.h)
		}
	}
}

// TestNormalizeImagePassThroughIsHonest 钉住"没有解码器就明说"：
// WebP 没有标准库解码器，必须原样透传并在 Note 里写明未归一化，不能假装处理过。
func TestNormalizeImagePassThroughIsHonest(t *testing.T) {
	fake := append([]byte("RIFF\x24\x00\x00\x00WEBPVP8 "), bytes.Repeat([]byte{0x11}, 32)...)
	res, err := normalizeImageBytes(fake, "image/webp", imageNormalizeOptions{
		MaxPixels: 4194304, MaxBytes: 4194304, Concurrency: 2,
	})
	if err != nil {
		t.Fatalf("不可解码格式不该被当错误: %v", err)
	}
	if res.Reencoded {
		t.Fatal("不可解码的格式不能被标记为重编码")
	}
	if res.Note == "" {
		t.Fatal("不可解码的格式必须留下 Note 说明未归一化")
	}
	if !bytes.Equal(res.Data, fake) {
		t.Fatal("不可解码的格式必须原样透传")
	}
}

// TestMaxImagesPerMessageEnforcedAtResolveBlocks 钉住 `attachment.maxImagesPerMessage`：
// 一条消息 20 张图通过、21 张报参数错误（不静默丢图），env=0 时不限。
func TestMaxImagesPerMessageEnforcedAtResolveBlocks(t *testing.T) {
	t.Setenv(EnvMaxImagesPerMessage, "")
	home := t.TempDir()
	store := newAttachmentStore(home)
	pngPath := writePNG(t, t.TempDir(), "img.png", 8, 8, false)

	blocks := make([]transport.ContentBlock, 0, 21)
	for i := 0; i < 20; i++ {
		a, err := store.Put(pngPath)
		if err != nil {
			t.Fatalf("Put(%d): %v", i, err)
		}
		blocks = append(blocks, transport.ContentBlock{Type: "attachment", AttachmentID: a.ID})
	}
	if _, err := store.ResolveBlocks(blocks); err != nil {
		t.Fatalf("20 张图应当通过（官方上限 20）: %v", err)
	}
	last, err := store.Put(pngPath)
	if err != nil {
		t.Fatalf("Put(21): %v", err)
	}
	over := append(append([]transport.ContentBlock{}, blocks...),
		transport.ContentBlock{Type: "attachment", AttachmentID: last.ID})
	if _, err := store.ResolveBlocks(over); err == nil {
		t.Fatal("21 张图必须报错")
	}

	// 0 = 不限：同一份 21 张块必须通过。
	t.Setenv(EnvMaxImagesPerMessage, "0")
	if _, err := store.ResolveBlocks(over); err != nil {
		t.Fatalf("env=0 表示不限，21 张应当通过: %v", err)
	}
}
