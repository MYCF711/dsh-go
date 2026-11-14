package tools

// 本文件钉住官方 0.2.2-rc.2 base #79 `@deepseek-ai/dsh-spill-policy` 的宿主侧行为
// （实现在 spill_policy.go）。六条形状各一条，加上估算函数本身的 golden：
//
//  1. 大文本结果超 MaxInlineTokens → 正文换头/尾 + notice，notice 里有真实省略字节数，
//     落盘文件里是完整原文；
//  2. 文本 + 图片共享预算：预算只够一张图时，尾侧那张**整张**保留、另一张被略，
//     notice 报 `Omitted 1 images.`，恢复文件里每张图位置上都是它的可读路径；
//  3. 预算内 → 原样通过（不写盘、不改 Text）；
//  4. 未配 MaxInlineTokens → 行为退回按字符的既有路径；
//  5. 写盘失败 / 图片拿不到可读路径 → 原内容保留 + warning（不 panic、不静默）；
//  6. 图片整留整去：不会出现半张图或空占位。

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"dshgo/internal/kernel"
	"dshgo/internal/session"
	"dshgo/internal/spi"
)

// DSHD_MAX_INLINE_TOKENS 会被 resolveOptions 读走。开发机上带着它跑这个包的测试，
// 会让"未配置"的用例走另一条臂，红得莫名其妙——这里统一摘掉。
func TestMain(m *testing.M) {
	os.Unsetenv(maxInlineTokensEnv)
	os.Exit(m.Run())
}

// spillTestPNG 造一张真 PNG 并返回（裸 base64，文件字节）。
func spillTestPNG(t *testing.T, width, height int) (string, []byte) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.RGBA{R: 7, G: 11, B: 13, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("编 PNG 失败: %v", err)
	}
	raw := buf.Bytes()
	return base64.StdEncoding.EncodeToString(raw), raw
}

// spillTestImages 造 n 张互不相同的真图片，落盘，并给出一个"能解析出可读路径"的
// 解析器（模拟装配期注入的附件库）。
//
// 为什么真落盘：本仓的解析结果必须真的 stat 得到（官方 "never substitutes an
// unreadable path for an image"），假路径在策略里会被拒掉。
func spillTestImages(t *testing.T, dir string, n int) ([]spi.Image, func(spi.Image) (string, bool)) {
	t.Helper()
	images := make([]spi.Image, 0, n)
	paths := make(map[string]string, n)
	for i := 0; i < n; i++ {
		// 1x1、2x2、3x3…：字节互不相同（尺寸进文件头），但都落在"最小像素放大"档，
		// 所以每张的视觉 token 都是同一个 184——预算算术因此是确定的。
		encoded, raw := spillTestPNG(t, i+1, i+1)
		images = append(images, spi.Image{MIME: "image/png", DataBase64: encoded})
		path := filepath.Join(dir, "shot-"+strconv.Itoa(i)+".png")
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatalf("写图片文件失败: %v", err)
		}
		paths[encoded] = path
	}
	resolver := func(img spi.Image) (string, bool) {
		path, ok := paths[img.DataBase64]
		return path, ok
	}
	return images, resolver
}

// spillTestCaptureLog 把 slog 输出接到一个缓冲里，返回读到它的闭包。
func spillTestCaptureLog(t *testing.T) func() string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf.String
}

var omittedBytesPattern = regexp.MustCompile(`Omitted (\d+) bytes\.`)
var omittedImagesPattern = regexp.MustCompile(`Omitted (\d+) images\.`)

func spillTestOmittedBytes(t *testing.T, text string) int {
	t.Helper()
	m := omittedBytesPattern.FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("notice 里没有 'Omitted N bytes.': %q", text)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("省略字节数不是整数: %q", m[1])
	}
	return n
}

func spillTestOmittedImages(t *testing.T, text string) int {
	t.Helper()
	m := omittedImagesPattern.FindStringSubmatch(text)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("省略图片数不是整数: %q", m[1])
	}
	return n
}

// spillTestSpillFiles 数 SpillDir 里落了几个文件（0 = 没写盘）。
func spillTestSpillFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("读 SpillDir 失败: %v", err)
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Name())
	}
	return out
}

// ---------------------------------------------------------------------------
// 1. 大文本：头/尾 + notice，省略字节数是真的，落盘是完整原文
// ---------------------------------------------------------------------------

func TestSpillPolicyRetainsOversizedText(t *testing.T) {
	dir := t.TempDir()
	body := strings.Repeat("0123456789abcdef", 500) // 8000 个 ASCII 字符 ≈ 2000 估算 token
	tl := &stubTool{name: "big", res: spi.Result{Text: body}}
	// MaxChars 放到很大：这条用例只测 token 臂，别让字符臂掺进来。
	p, k := newPipe(t, Options{SpillDir: dir, MaxChars: 1 << 20, MaxInlineTokens: 300}, tl)
	defer k.Root().Dispose()

	res, err := p.Run(context.Background(), "big", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Text == body {
		t.Fatal("超预算的正文必须被换掉，不能原样内联")
	}
	if !strings.Contains(res.Text, spillGap) {
		t.Fatalf("头/尾之间要有 %q 分隔: %q", spillGap, res.Text)
	}
	notice := "(Omitted "
	if !strings.Contains(res.Text, notice) {
		t.Fatalf("notice 必须以 'Omitted … bytes.' 开头（官方 notice.ts:21-24）: %q", res.Text)
	}
	if !strings.Contains(res.Text, "Full formatted result stored at: ") {
		t.Fatalf("notice 要给出恢复文件位置: %q", res.Text)
	}
	if !strings.Contains(res.Text, "Use read with offset/limit, or grep this path to search within it.") {
		t.Fatalf("notice 要给出取回指引（官方 spill-local retrievalHint 原话）: %q", res.Text)
	}
	omitted := spillTestOmittedBytes(t, res.Text)
	kept := len(body) - omitted
	if omitted <= 0 || kept <= 0 {
		t.Fatalf("省略字节数/保留字节数都不该是 0：omitted=%d kept=%d", omitted, kept)
	}
	// 头在最前面、尾在最后面（留尾是因为报错/结论常在末尾）。
	if !strings.HasPrefix(res.Text, body[:16]) {
		t.Fatalf("头部不是原文前缀: %q", res.Text[:len(body)/4])
	}
	if !strings.Contains(res.Text, body[len(body)-16:]) {
		t.Fatalf("尾部不是原文后缀: %q", res.Text)
	}
	// 替换体必须真的落在预算内。
	if estimated := session.EstimateRunes(res.Text); estimated > 300 {
		t.Fatalf("保留结果估算 %d token 超过预算 300: %q", estimated, res.Text)
	}
	// token 臂不设 Truncated：投影层看到它会在预算外再补一条截断说明（见 retainInline 注释）。
	if res.Truncated != nil {
		t.Fatalf("token 臂不该设 Truncated（投影会重复说明同一件事）: %+v", res.Truncated)
	}
	if len(res.Artifacts) != 1 {
		t.Fatalf("应登记 1 个恢复文件 artifact，得到 %d", len(res.Artifacts))
	}
	if !strings.HasSuffix(res.Artifacts[0].Path, ".txt") {
		t.Fatalf("恢复文件应是 .txt（模型按纯文本 read）: %q", res.Artifacts[0].Path)
	}
	if !strings.Contains(res.Text, res.Artifacts[0].Path) {
		t.Fatalf("notice 里的路径必须就是落盘的那个文件: %q vs %q", res.Text, res.Artifacts[0].Path)
	}
	recovered, err := os.ReadFile(res.Artifacts[0].Path)
	if err != nil {
		t.Fatalf("恢复文件读不到: %v", err)
	}
	if string(recovered) != body {
		t.Fatalf("恢复文件里必须是完整原文（%d 字节），实际 %d 字节", len(body), len(recovered))
	}
	if res.Artifacts[0].Bytes != int64(len(body)) {
		t.Fatalf("artifact 字节数应等于正文长度 %d，得到 %d", len(body), res.Artifacts[0].Bytes)
	}
	// 真实输出摘要（-v 时可见）：模型这一轮到底看到什么。
	t.Logf("原文 %d 字节 → 内联估算 %d token（预算 300），省 %d 字节，恢复文件 %s",
		len(body), session.EstimateRunes(res.Text), omitted, res.Artifacts[0].Path)
	if noticeStart := strings.Index(res.Text, "(Omitted "); noticeStart >= 0 {
		t.Logf("notice = %s", res.Text[noticeStart:])
	}
}

// ---------------------------------------------------------------------------
// 2. 文本与图片共享预算；图片整留整去；恢复文件里记可读路径
// ---------------------------------------------------------------------------

func TestSpillPolicySharesBudgetAcrossTextAndImages(t *testing.T) {
	imageDir := t.TempDir()
	images, resolver := spillTestImages(t, imageDir, 2)
	// 一张 1x1 PNG 的估算成本 = 视觉 token 184 + 恢复文件里那行占位文本（含可读路径）
	// ≈ 226。预算 580 只够"正文头/尾各一段 + 一张图"，第二张必然放不下。
	dir := t.TempDir()
	body := strings.Repeat("abcdefgh", 500) // 4000 个 ASCII 字符 ≈ 1000 估算 token
	tl := &stubTool{name: "shots", res: spi.Result{Text: body, Images: images}}
	p, k := newPipe(t, Options{
		SpillDir: dir, MaxChars: 1 << 20, MaxInlineTokens: 580, ImageReadablePath: resolver,
	}, tl)
	defer k.Root().Dispose()

	res, err := p.Run(context.Background(), "shots", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := spillTestOmittedImages(t, res.Text); got != 1 {
		t.Fatalf("预算只够一张图时应报 'Omitted 1 images.'，实际 %d：%q", got, res.Text)
	}
	if len(res.Images) != 1 {
		t.Fatalf("整留整去：应只剩 1 张图，得到 %d", len(res.Images))
	}
	// 图片在内容里排在正文之后 → 尾侧优先保留最后一张；留下来的必须是**整张**。
	if res.Images[0].DataBase64 != images[1].DataBase64 {
		t.Fatal("尾侧保留的应是最后那张图（整张），不是第一张、也不是半张")
	}
	if res.Images[0].DataBase64 == "" || res.Images[0].MIME != images[1].MIME {
		t.Fatalf("保留下来的图片不能是空占位: %+v", res.Images[0])
	}
	if omitted := spillTestOmittedBytes(t, res.Text); omitted <= 0 || omitted >= len(body) {
		t.Fatalf("文本也要给出真实省略字节数（0 < omitted < %d），实际 %d", len(body), omitted)
	}
	// 预算：正文 + 留下的那张图都算进同一个预算。
	estimated := session.EstimateRunes(res.Text) + EstimateImageTokens(1, 1)
	if estimated > 580 {
		t.Fatalf("正文+图片估算 %d token 超过共享预算 580", estimated)
	}
	// 恢复文件里每张图片位置都是**它的可读路径**，且不复制附件字节。
	if len(res.Artifacts) != 1 {
		t.Fatalf("应登记 1 个恢复文件，得到 %d", len(res.Artifacts))
	}
	recovered, err := os.ReadFile(res.Artifacts[0].Path)
	if err != nil {
		t.Fatalf("恢复文件读不到: %v", err)
	}
	fileText := string(recovered)
	if !strings.Contains(fileText, body) {
		t.Fatal("恢复文件里必须是完整正文")
	}
	for index := range images {
		path := filepath.Join(imageDir, "shot-"+strconv.Itoa(index)+".png")
		if !strings.Contains(fileText, `[Image: `+spillJSONString(path)+`; image/png; `) {
			t.Fatalf("恢复文件里第 %d 张图的位置要写它的可读路径 %q: %q", index+1, path, fileText)
		}
	}
	if strings.Contains(fileText, images[0].DataBase64) {
		t.Fatal("附件字节不能复制进恢复文件（官方：Attachment bytes are not copied into this file）")
	}
	t.Logf("2 张图 → 整留 %d 张（第 2 张）、省 %d 张；内联估算 %d token（共享预算 580）；恢复文件 %d 字节",
		len(res.Images), spillTestOmittedImages(t, res.Text), session.EstimateRunes(res.Text), len(recovered))
	if noticeStart := strings.Index(res.Text, "(Omitted "); noticeStart >= 0 {
		t.Logf("notice = %s", res.Text[noticeStart:])
	}
	for _, line := range strings.Split(fileText, "\n") {
		if strings.HasPrefix(line, "[Image: ") {
			t.Logf("恢复文件图片行 = %s", line)
		}
	}
}

// ---------------------------------------------------------------------------
// 3. 预算内：原样通过，不写盘、不改 Text
// ---------------------------------------------------------------------------

func TestSpillPolicyLeavesFittingResultUntouched(t *testing.T) {
	imageDir := t.TempDir()
	images, resolver := spillTestImages(t, imageDir, 1)
	dir := t.TempDir()
	tl := &stubTool{name: "small", res: spi.Result{Text: "很短的结果", Images: images}}
	p, k := newPipe(t, Options{
		SpillDir: dir, MaxChars: 1 << 20, MaxInlineTokens: 100000, ImageReadablePath: resolver,
	}, tl)
	defer k.Root().Dispose()

	res, err := p.Run(context.Background(), "small", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "很短的结果" {
		t.Fatalf("预算内的正文必须原样通过: %q", res.Text)
	}
	if len(res.Images) != 1 || res.Images[0].DataBase64 != images[0].DataBase64 {
		t.Fatal("预算内的图片必须原样通过")
	}
	if res.Truncated != nil || len(res.Artifacts) != 0 {
		t.Fatalf("预算内不该有任何截断/外置证据: %+v %+v", res.Truncated, res.Artifacts)
	}
	if files := spillTestSpillFiles(t, dir); len(files) != 0 {
		t.Fatalf("预算内不许写盘，实际落了 %v", files)
	}
}

// ---------------------------------------------------------------------------
// 4. 未配 MaxInlineTokens：行为退回按字符的既有路径
// ---------------------------------------------------------------------------

func TestSpillPolicyDisabledKeepsCharArm(t *testing.T) {
	dir := t.TempDir()
	body := strings.Repeat("line\n", 1600) // 8000 字节，超过 MaxChars=4000
	tl := &stubTool{name: "big", res: spi.Result{Text: body}}
	// MaxInlineTokens 省略（0）= 官方"omitting maxInlineTokens disables retention"。
	p, k := newPipe(t, Options{SpillDir: dir, MaxChars: 4000}, tl)
	defer k.Root().Dispose()

	res, err := p.Run(context.Background(), "big", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "中间省略") {
		t.Fatalf("没配 token 预算时应走原来的字符臂（中文省略说明）: %q", res.Text)
	}
	if strings.Contains(res.Text, "(Omitted ") {
		t.Fatal("没配 token 预算时不该出现官方 spill notice")
	}
	if res.Truncated == nil {
		t.Fatal("字符臂要留下 Truncated 证据（既有行为）")
	}
	if files := spillTestSpillFiles(t, dir); len(files) != 0 {
		t.Fatalf("这条路径不写恢复文件，实际落了 %v", files)
	}
}

// ---------------------------------------------------------------------------
// 5. 写盘失败：原内容保留 + warning（不 panic、不静默）
// ---------------------------------------------------------------------------

func TestSpillPolicyKeepsContentWhenSpillWriteFails(t *testing.T) {
	base := t.TempDir()
	blocker := filepath.Join(base, "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("造阻塞文件失败: %v", err)
	}
	// SpillDir 的父路径是个普通文件 → MkdirAll 必然失败（Windows/Linux 同理）。
	spillDir := filepath.Join(blocker, "artifacts")
	body := strings.Repeat("deadbeef", 1000)
	tl := &stubTool{name: "big", res: spi.Result{Text: body}}

	logs := spillTestCaptureLog(t)
	p, k := newPipe(t, Options{SpillDir: spillDir, MaxChars: 1 << 20, MaxInlineTokens: 300}, tl)
	defer k.Root().Dispose()

	res, err := p.Run(context.Background(), "big", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("写盘失败不该把一次成功的调用变成失败: %v", err)
	}
	if res.Text != body {
		t.Fatalf("写盘失败必须保留原内容: %d 字节 vs %d 字节", len(res.Text), len(body))
	}
	if len(res.Artifacts) != 0 || res.Truncated != nil {
		t.Fatalf("写盘失败时不该留下外置/截断证据: %+v %+v", res.Artifacts, res.Truncated)
	}
	if !strings.Contains(logs(), "结果外置失败，保留内联") {
		t.Fatalf("写盘失败必须记一条可见的 warning，实际日志: %s", logs())
	}
}

// 图片没有可读路径：保留原内容 + warning，绝不把读不到的路径写进恢复文件。
func TestSpillPolicyKeepsContentWhenImagePathUnavailable(t *testing.T) {
	dir := t.TempDir()
	images, _ := spillTestImages(t, dir, 1)
	body := strings.Repeat("abcdefgh", 500)
	missing := filepath.Join(dir, "gone.png")

	cases := []struct {
		name     string
		resolver func(spi.Image) (string, bool)
		wantWarn string
	}{
		{name: "没有解析器（本仓当前装配）", resolver: nil, wantWarn: "没有可读路径"},
		{name: "解析器给了不存在的路径", resolver: func(spi.Image) (string, bool) { return missing, true }, wantWarn: "没有可读路径"},
		{name: "解析器解不出这一张", resolver: func(spi.Image) (string, bool) { return "", false }, wantWarn: "没有可读路径"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spillDir := t.TempDir()
			tl := &stubTool{name: "shots", res: spi.Result{Text: body, Images: images}}
			logs := spillTestCaptureLog(t)
			p, k := newPipe(t, Options{
				SpillDir: spillDir, MaxChars: 1 << 20, MaxInlineTokens: 200, ImageReadablePath: tc.resolver,
			}, tl)
			defer k.Root().Dispose()

			res, err := p.Run(context.Background(), "shots", json.RawMessage(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			if res.Text != body {
				t.Fatalf("拿不到图片可读路径时必须保留原正文: %q", res.Text)
			}
			if len(res.Images) != 1 || res.Images[0].DataBase64 != images[0].DataBase64 {
				t.Fatal("拿不到可读路径时不许动图片（更不能只留一半）")
			}
			if files := spillTestSpillFiles(t, spillDir); len(files) != 0 {
				t.Fatalf("这条路不该落盘（先判图片再写），实际 %v", files)
			}
			if !strings.Contains(logs(), tc.wantWarn) {
				t.Fatalf("要有一条说明原因的 warning（%q），实际: %s", tc.wantWarn, logs())
			}
		})
	}
}

// notice 自己都放不进预算 → 原内容保留（官方 index.ts:112-114）。
func TestSpillPolicyKeepsOriginalWhenNoticeExceedsBudget(t *testing.T) {
	dir := t.TempDir()
	body := strings.Repeat("q", 4000)
	tl := &stubTool{name: "big", res: spi.Result{Text: body}}
	logs := spillTestCaptureLog(t)
	p, k := newPipe(t, Options{SpillDir: dir, MaxChars: 1 << 20, MaxInlineTokens: 5}, tl)
	defer k.Root().Dispose()

	res, err := p.Run(context.Background(), "big", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != body {
		t.Fatalf("notice 都放不进预算时必须保留原内容: %q", res.Text)
	}
	if !strings.Contains(logs(), "省略说明本身超过 maxInlineTokens") {
		t.Fatalf("要说明为什么没保留，实际日志: %s", logs())
	}
}

// ---------------------------------------------------------------------------
// 6. notice-only：预览一个字都放不下时，只给 notice（官方允许）
// ---------------------------------------------------------------------------

func TestSpillPolicyAllowsNoticeOnlyResult(t *testing.T) {
	imageDir := t.TempDir()
	images, resolver := spillTestImages(t, imageDir, 1)
	dir := t.TempDir()
	// 只有一张图、没有正文：预算比图片本身小，比 notice 大。
	tl := &stubTool{name: "shot", res: spi.Result{Images: images}}
	p, k := newPipe(t, Options{
		SpillDir: dir, MaxChars: 1 << 20, MaxInlineTokens: 120, ImageReadablePath: resolver,
	}, tl)
	defer k.Root().Dispose()

	res, err := p.Run(context.Background(), "shot", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.Text, "(Omitted 0 bytes. Omitted 1 images. Full formatted result stored at: ") {
		t.Fatalf("预览放不下时应是 notice-only: %q", res.Text)
	}
	if len(res.Images) != 0 {
		t.Fatalf("notice-only 时图片应全部省掉，得到 %d 张", len(res.Images))
	}
	if estimated := session.EstimateRunes(res.Text); estimated > 120 {
		t.Fatalf("notice-only 也必须落在预算内：%d > 120", estimated)
	}
	if len(res.Artifacts) != 1 {
		t.Fatalf("notice-only 也必须登记恢复文件，得到 %d", len(res.Artifacts))
	}
}

// `read` 的结果不参与保留（官方 index.ts:136）：否则 notice 让模型去 read，
// 读出来的又被外置，形成环。
func TestSpillPolicySkipsReadTool(t *testing.T) {
	dir := t.TempDir()
	body := strings.Repeat("0123456789abcdef", 500)
	tl := &stubTool{name: "read", res: spi.Result{Text: body}}
	p, k := newPipe(t, Options{SpillDir: dir, MaxChars: 1 << 20, MaxInlineTokens: 300}, tl)
	defer k.Root().Dispose()

	res, err := p.Run(context.Background(), "read", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != body {
		t.Fatalf("read 的结果必须原样放行: %q", res.Text)
	}
	if files := spillTestSpillFiles(t, dir); len(files) != 0 {
		t.Fatalf("read 不该写恢复文件，实际 %v", files)
	}
}

// ---------------------------------------------------------------------------
// 估算函数（官方 image-tokens.ts 的移植）：golden 值 + 尺寸解析
// ---------------------------------------------------------------------------

// golden 值来自把官方 dsh-llm-deepseek/src/image-tokens.ts 的 deepSeekImageTokens
// 原样在 node 里跑一遍（同一段代码去掉 TS 类型）。任何一位漂移都说明移植走样了。
func TestEstimateImageTokensMatchesOfficial(t *testing.T) {
	cases := []struct {
		width, height, tokens int
	}{
		{1, 1, 184},
		{14, 14, 184},
		{100, 100, 184},
		{256, 256, 184},
		{544, 544, 184},
		{800, 600, 317},
		{1024, 1024, 652},
		{1920, 1080, 968},
		{4096, 4096, 994},
		{3000, 2000, 977},
		{64, 4096, 314},
		{600, 600, 242},
		{500, 700, 223},
		{1, 4096, 1024},
		{4096, 1, 832},
		{1200, 1600, 1010},
	}
	for _, tc := range cases {
		if got := EstimateImageTokens(tc.width, tc.height); got != tc.tokens {
			t.Fatalf("%dx%d 的视觉 token 估算 = %d，官方是 %d",
				tc.width, tc.height, got, tc.tokens)
		}
	}
	// 非正输入按最坏情况处理（不会因为 0 让预算看起来还很宽）。
	for _, dims := range [][2]int{{0, 10}, {10, 0}, {-1, -1}} {
		if got := EstimateImageTokens(dims[0], dims[1]); got != imageMaxTokens {
			t.Fatalf("非正尺寸 %v 应返回单图上限 %d，得到 %d", dims, imageMaxTokens, got)
		}
	}
}

func TestImageDimensionsReadsHeaders(t *testing.T) {
	t.Run("png", func(t *testing.T) {
		_, raw := spillTestPNG(t, 7, 5)
		width, height, ok := imageDimensions(raw)
		if !ok || width != 7 || height != 5 {
			t.Fatalf("PNG 尺寸 = %dx%d ok=%v，期望 7x5", width, height, ok)
		}
	})
	t.Run("gif", func(t *testing.T) {
		var buf bytes.Buffer
		img := image.NewPaletted(image.Rect(0, 0, 9, 4), color.Palette{color.Black, color.White})
		if err := gif.Encode(&buf, img, nil); err != nil {
			t.Fatalf("编 GIF 失败: %v", err)
		}
		width, height, ok := imageDimensions(buf.Bytes())
		if !ok || width != 9 || height != 4 {
			t.Fatalf("GIF 尺寸 = %dx%d ok=%v，期望 9x4", width, height, ok)
		}
	})
	t.Run("jpeg", func(t *testing.T) {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 11, 3)), nil); err != nil {
			t.Fatalf("编 JPEG 失败: %v", err)
		}
		width, height, ok := imageDimensions(buf.Bytes())
		if !ok || width != 11 || height != 3 {
			t.Fatalf("JPEG 尺寸 = %dx%d ok=%v，期望 11x3", width, height, ok)
		}
	})
	t.Run("webp-vp8x", func(t *testing.T) {
		raw := make([]byte, 30)
		copy(raw[0:4], "RIFF")
		copy(raw[8:12], "WEBP")
		copy(raw[12:16], "VP8X")
		raw[24], raw[25], raw[26] = 0x1f, 0x03, 0x00 // 画布宽-1 = 799
		raw[27], raw[28], raw[29] = 0x57, 0x02, 0x00 // 画布高-1 = 599
		width, height, ok := imageDimensions(raw)
		if !ok || width != 800 || height != 600 {
			t.Fatalf("VP8X 尺寸 = %dx%d ok=%v，期望 800x600", width, height, ok)
		}
	})
	t.Run("webp-vp8-lossy", func(t *testing.T) {
		raw := make([]byte, 30)
		copy(raw[0:4], "RIFF")
		copy(raw[8:12], "WEBP")
		copy(raw[12:16], "VP8 ")
		raw[23], raw[24], raw[25] = 0x9d, 0x01, 0x2a // 起始码
		binary.LittleEndian.PutUint16(raw[26:28], 640)
		binary.LittleEndian.PutUint16(raw[28:30], 480)
		width, height, ok := imageDimensions(raw)
		if !ok || width != 640 || height != 480 {
			t.Fatalf("VP8 尺寸 = %dx%d ok=%v，期望 640x480", width, height, ok)
		}
	})
	t.Run("webp-vp8l-lossless", func(t *testing.T) {
		raw := make([]byte, 25)
		copy(raw[0:4], "RIFF")
		copy(raw[8:12], "WEBP")
		copy(raw[12:16], "VP8L")
		raw[20] = 0x2f
		binary.LittleEndian.PutUint32(raw[21:25], uint32(320-1)|uint32(240-1)<<14)
		width, height, ok := imageDimensions(raw)
		if !ok || width != 320 || height != 240 {
			t.Fatalf("VP8L 尺寸 = %dx%d ok=%v，期望 320x240", width, height, ok)
		}
	})
	t.Run("垃圾字节", func(t *testing.T) {
		if _, _, ok := imageDimensions([]byte("not an image at all")); ok {
			t.Fatal("垃圾字节不该被认成图片")
		}
		if _, _, ok := imageDimensions(nil); ok {
			t.Fatal("空字节不该被认成图片")
		}
	})
}

// ---------------------------------------------------------------------------
// 正文口径：必须与 session.ProjectResult 的投影一致（漂了两边会各自算一套长度）
// ---------------------------------------------------------------------------

func TestModelVisibleTextMatchesProjection(t *testing.T) {
	cases := []spi.Result{
		{Text: "工具自己渲染好的正文"},
		{Summary: "摘要", Data: map[string]any{"a": 1}},
		{Summary: "只有摘要"},
		{Data: []string{"x"}},
	}
	for _, res := range cases {
		projected := session.ProjectResult(res)
		parts := make([]string, 0, len(projected))
		for _, block := range projected {
			parts = append(parts, block.Text)
		}
		want := strings.Join(parts, "\n")
		got := modelVisibleText(&res)
		if got != want {
			t.Fatalf("正文口径与投影不一致：modelVisibleText=%q ProjectResult=%q", got, want)
		}
	}
	// 全空结果：投影会给「（无输出）」，但那是给人看的一句话、不是载荷，
	// 所以 token 臂什么都不做（返回 false 走后面的路径）。
	empty := spi.Result{}
	if got := modelVisibleText(&empty); got != "" {
		t.Fatalf("空结果不该有正文，得到 %q", got)
	}
}

// ---------------------------------------------------------------------------
// 装配期校验：负值/非法必须在装配期炸，不是每次调用才炸
// ---------------------------------------------------------------------------

func TestResolveOptionsValidatesInlineTokens(t *testing.T) {
	cases := []struct {
		name    string
		opts    Options
		env     string
		want    int
		wantErr bool
	}{
		{name: "未配置也未设环境变量", opts: Options{}, want: 0},
		{name: "环境变量给正整数", opts: Options{}, env: "5000", want: 5000},
		{name: "环境变量是空串 = 未配置", opts: Options{}, env: "", want: 0},
		{name: "环境变量不是整数", opts: Options{}, env: "abc", wantErr: true},
		{name: "环境变量是负值", opts: Options{}, env: "-7", wantErr: true},
		{name: "Options 显式负值", opts: Options{MaxInlineTokens: -1}, wantErr: true},
		{name: "Options 显式正值优先于环境变量", opts: Options{MaxInlineTokens: 250}, env: "5000", want: 250},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(maxInlineTokensEnv, tc.env)
			resolved, err := resolveOptions(tc.opts)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("非法配置必须在装配期报错，得到 MaxInlineTokens=%d", resolved.MaxInlineTokens)
				}
				return
			}
			if err != nil {
				t.Fatalf("合法配置不该报错: %v", err)
			}
			if resolved.MaxInlineTokens != tc.want {
				t.Fatalf("MaxInlineTokens = %d，期望 %d", resolved.MaxInlineTokens, tc.want)
			}
		})
	}
}

func TestNewPipelineFailsLoudOnNegativeInlineTokens(t *testing.T) {
	k := kernel.New("t")
	defer k.Root().Dispose()
	defer func() {
		if recover() == nil {
			t.Fatal("负的 MaxInlineTokens 必须在装配期 fail-loud，而不是留到每次调用")
		}
	}()
	NewPipeline(New(), k.Bus(), Options{SpillDir: t.TempDir(), MaxInlineTokens: -1})
}
