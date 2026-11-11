package officekit

// 原生工作进程的调用层：参数、环境、超时/取消、输出校验。
//
// 官方工作进程协议（libreoffice-kit/lib/cli.js）：
//
//	--program-directory <LO program 目录>     // :869
//	--input-path <file> 或 --input-bytes N --input-format <ext>   // :845-862
//	--output-path <file>                      // :870
//	--profile-directory <私有 profile>        // :872
//	--max-output-bytes N --max-image-resolution N                 // :877-882
//	--format <pdf|docx|odt|txt|xlsx|ods|csv|pptx|odp>  --recalculate true|false  [--sheet <名>]
//	stdout 一行 JSON：{"ok":true,"missingFonts":[...],"pageCount"?:N} / {"ok":false,"code":"...","error":"..."}
//	（cli.js:929-950：stdout 上限 64 KiB，`ok !== true` 或退出码非 0 都是失败）
//
// 环境变量由 nativeEnvironment 白名单拼出（cli.js:754-775）：只透传 PATH/SystemRoot/…
// 与 TZ，其余全部指向私有 profile，并关掉打印机发现——这两条直接影响「会不会卡在
// 网络/驱动等待上」。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// nativeEnvironmentKeys 逐字移植官方 cli.js:714-726。
var nativeEnvironmentKeys = []string{
	"PATH", "SystemRoot", "SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT",
	"LANG", "LC_ALL", "TZ", "OFFICE_GRAPHITE_DIAGNOSTICS", "OFFICE_LOAD_DIAGNOSTICS",
}

// maxWorkerStdout 是官方对工作进程响应体的上限（cli.js:929）。
const maxWorkerStdout = 64 * 1024

// 文档族与可转换格式（官方 cli.js:1700-1740 CONVERSION_FORMATS 与
// dsh-tool-office/src/formats.ts:22-26 CONVERSION_OUTPUTS 同一张表）。
const (
	FamilyText         = "text"
	FamilySpreadsheet  = "spreadsheet"
	FamilyPresentation = "presentation"
	FamilyPDF          = "pdf"
)

var familyOfExtension = map[string]string{
	"doc": FamilyText, "docx": FamilyText, "odt": FamilyText,
	"xls": FamilySpreadsheet, "xlsx": FamilySpreadsheet, "ods": FamilySpreadsheet,
	"ppt": FamilyPresentation, "pptx": FamilyPresentation, "odp": FamilyPresentation,
	"pdf": FamilyPDF,
}

var conversionFormats = map[string][]string{
	FamilyText:         {"pdf", "docx", "odt", "txt"},
	FamilySpreadsheet:  {"pdf", "xlsx", "ods", "csv"},
	FamilyPresentation: {"pdf", "pptx", "odp"},
}

// RecalculationFormats 是重算的输出格式（官方 types.ts:71）。
var RecalculationFormats = []string{"xlsx", "ods"}

// ExtensionOf 取小写扩展名（不带点）。
func ExtensionOf(path string) string {
	base := filepath.Base(path)
	dot := strings.LastIndex(base, ".")
	if dot <= 0 || dot == len(base)-1 {
		return ""
	}
	return strings.ToLower(base[dot+1:])
}

// FamilyOf 返回文档族；不认识的扩展名给 false。
func FamilyOf(path string) (string, bool) {
	family, ok := familyOfExtension[ExtensionOf(path)]
	return family, ok
}

// ConversionFormatsFor 返回该文档族可转换到的格式（顺序同官方）。
func ConversionFormatsFor(family string) []string { return conversionFormats[family] }

// Rendered 是 `officeKit.render` 的结果，形状与官方 RenderedDocumentBytes 一致
// （dsh-office-kit/src/types.ts:167-172 + WorkspaceFileBytes）。
type Rendered struct {
	MissingFonts []string `json:"missingFonts"`
	Generation   string   `json:"generation"`
	Offset       int      `json:"offset"`
	Data         []byte   `json:"data"`
	Eof          bool     `json:"eof"`
	AbsolutePath string   `json:"absolutePath"`
	Version      string   `json:"version"`
	Bytes        *int     `json:"bytes,omitempty"`
}

// FileResult 是 convert / recalculate 的产物描述（官方 OfficeFileResult，
// types.ts:129-137）。
type FileResult struct {
	// Path 是产物绝对路径（provider 私有产物仓里，保留期内可读）。
	Path string `json:"path"`
	// Bytes 是完整字节数。
	Bytes int64 `json:"bytes"`
	// PageCount 是引擎报告的页数（可能缺）。
	PageCount *int `json:"pageCount,omitempty"`
	// MissingFonts 是被替换的字体族。
	MissingFonts []string `json:"missingFonts"`
}

// Render 把 Office 文档转成完整 PDF（官方 officeKit.render 的引擎部分：
// index.ts:278-332 renderFile → convert({kind:'pdf'})）。
func (p *Provider) Render(ctx context.Context, absolutePath, priority string) (Rendered, error) {
	src, err := p.inspect(absolutePath, true)
	if err != nil {
		return Rendered{}, err
	}
	release, err := p.acquire(ctx, priority)
	if err != nil {
		return Rendered{}, err
	}
	defer release()
	pdf, missingFonts, _, err := p.convertBytes(ctx, src, "pdf", false, "")
	if err != nil {
		return Rendered{}, err
	}
	n := len(pdf)
	return Rendered{
		MissingFonts: missingFonts, Generation: p.GenerationID(), Offset: 0, Data: pdf, Eof: true,
		AbsolutePath: src.path, Version: src.version, Bytes: &n,
	}, nil
}

// ConvertTo 把文档转成另一个格式并落到 owner 的产物目录（官方 convertTo，
// index.ts:222-226）。
func (p *Provider) ConvertTo(ctx context.Context, owner, absolutePath, format, sheet string) (FileResult, error) {
	src, err := p.inspect(absolutePath, true)
	if err != nil {
		return FileResult{}, err
	}
	family, ok := FamilyOf(src.path)
	if !ok || family == FamilyPDF {
		return FileResult{}, errf(CodeUnsupportedFormat, "不支持转换的输入类型：%s", ExtensionOf(src.path))
	}
	if err := validateConversion(family, format, sheet); err != nil {
		return FileResult{}, err
	}
	release, err := p.acquire(ctx, "foreground")
	if err != nil {
		return FileResult{}, err
	}
	defer release()
	data, missingFonts, pageCount, err := p.convertBytes(ctx, src, format, false, sheet)
	if err != nil {
		return FileResult{}, err
	}
	path, err := p.outputs.save(owner, outputName(src.path, format), data)
	if err != nil {
		return FileResult{}, err
	}
	return FileResult{Path: path, Bytes: int64(len(data)), PageCount: pageCount, MissingFonts: missingFonts}, nil
}

// RecalculateTo 重算工作簿全部公式并落盘（官方 recalculate，index.ts:237-240）。
func (p *Provider) RecalculateTo(ctx context.Context, owner, absolutePath, format string) (FileResult, error) {
	src, err := p.inspect(absolutePath, true)
	if err != nil {
		return FileResult{}, err
	}
	if family, ok := FamilyOf(src.path); !ok || family != FamilySpreadsheet {
		return FileResult{}, errf(CodeUnsupportedFormat, "重算只接受 xls / xlsx / ods 工作簿")
	}
	valid := false
	for _, candidate := range RecalculationFormats {
		if candidate == format {
			valid = true
		}
	}
	if !valid {
		return FileResult{}, errf(CodeInvalidRequest, "重算输出格式只能是 xlsx / ods，拿到 %q", format)
	}
	release, err := p.acquire(ctx, "foreground")
	if err != nil {
		return FileResult{}, err
	}
	defer release()
	data, missingFonts, pageCount, err := p.convertBytes(ctx, src, format, true, "")
	if err != nil {
		return FileResult{}, err
	}
	path, err := p.outputs.save(owner, outputName(src.path, format), data)
	if err != nil {
		return FileResult{}, err
	}
	return FileResult{Path: path, Bytes: int64(len(data)), PageCount: pageCount, MissingFonts: missingFonts}, nil
}

func validateConversion(family, format, sheet string) error {
	accepted := conversionFormats[family]
	ok := false
	for _, candidate := range accepted {
		if candidate == format {
			ok = true
		}
	}
	if !ok {
		return errf(CodeUnsupportedFormat, "%s 族只能转成 %s，拿到 %q", family, strings.Join(accepted, ", "), format)
	}
	if sheet != "" && format != "csv" {
		return errf(CodeInvalidRequest, "sheet 只在 CSV 转换时有效")
	}
	return nil
}

func outputName(src, format string) string {
	base := filepath.Base(src)
	if dot := strings.LastIndex(base, "."); dot > 0 {
		base = base[:dot]
	}
	if base == "" {
		base = "output"
	}
	return base + "." + format
}

// source 是一次操作读入并核验过的源文件。
type source struct {
	path    string
	data    []byte
	size    int64
	version string
}

// inspect 解析并核验源路径：绝对、存在、是普通文件、类型受支持、体积在限内。
//
// `loadData` 为 true 时一次性读入（上限 MaxInputBytes，超限即 input-too-large，
// 官方 index.ts:308-313 的同一条）；images 走独立实现，不需要这里读。
func (p *Provider) inspect(raw string, loadData bool) (source, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return source{}, errf(CodeInvalidRequest, "path 是空串")
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return source{}, errf(CodeInvalidRequest, "路径无法解析：%v", err)
	}
	info, statErr := os.Stat(abs)
	if statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return source{}, errf(CodeInvalidRequest, "文件不存在：%s", abs)
		}
		return source{}, errf(CodeFailed, "无法读取文件状态：%v", statErr)
	}
	if info.IsDir() {
		return source{}, errf(CodeInvalidRequest, "path 是目录，不是 Office 文件")
	}
	if !info.Mode().IsRegular() {
		return source{}, errf(CodeInvalidRequest, "path 不是普通文件")
	}
	if _, ok := FamilyOf(abs); !ok {
		return source{}, errf(CodeUnsupportedFormat, "不支持的 Office 文件类型：%q", ExtensionOf(abs))
	}
	if info.Size() > p.opts.MaxInputBytes {
		return source{}, errf(CodeInputTooLarge, "源文件 %d 字节，超过 %d 字节上限", info.Size(), p.opts.MaxInputBytes)
	}
	src := source{path: abs, size: info.Size()}
	if !loadData {
		return src, nil
	}
	raw2, err := readBounded(abs, p.opts.MaxInputBytes)
	if err != nil {
		return source{}, err
	}
	src.data = raw2
	sum := sha256.Sum256(raw2)
	src.version = hex.EncodeToString(sum[:8]) + "-" + strconv.FormatInt(int64(len(raw2)), 10)
	return src, nil
}

// convertBytes 跑一次工作进程并把产物读回。
func (p *Provider) convertBytes(ctx context.Context, src source, format string, recalculate bool, sheet string) ([]byte, []string, *int, error) {
	engine, _, err := p.ensureEngine(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	scratch, err := os.MkdirTemp("", "dsh-office-kit-")
	if err != nil {
		return nil, nil, nil, errf(CodeFailed, "无法创建临时目录：%v", err)
	}
	defer os.RemoveAll(scratch)
	profile := filepath.Join(scratch, "profile")
	if err := os.MkdirAll(profile, 0o700); err != nil {
		return nil, nil, nil, errf(CodeFailed, "无法创建私有 profile：%v", err)
	}
	prepared, release, err := prepareNativeEngine(engine, scratch)
	if err != nil {
		return nil, nil, nil, err
	}
	defer release()
	outPath := filepath.Join(scratch, "output."+format)
	baseArgs := []string{
		"--program-directory", prepared.ProgramDirectory,
		"--output-path", outPath,
		"--profile-directory", profile,
		"--max-output-bytes", strconv.FormatInt(p.opts.MaxOutputBytes, 10),
		"--max-image-resolution", strconv.Itoa(p.opts.MaxImageResolution),
		"--format", format,
		"--recalculate", strconv.FormatBool(recalculate),
	}
	if sheet != "" {
		baseArgs = append(baseArgs, "--sheet", sheet)
	}
	// 传输顺序与官方 0.2.x 一致：stdin 字节（--input-bytes/--input-format）。
	// 0.1.x 引擎只认 --input-path，报「未知参数」时退回旧传输。
	transportArgs := []string{
		"--input-bytes", strconv.FormatInt(int64(len(src.data)), 10),
		"--input-format", ExtensionOf(src.path),
	}
	response, stderr, runErr := p.runWorker(ctx, prepared, profile, append(transportArgs, baseArgs...), src.data)
	if runErr == nil && !response.OK && (isUnknownInputPath(stderr, response) || isUnknownArgument(stderr, response)) {
		_ = os.Remove(outPath)
		response, stderr, runErr = p.runWorker(ctx, prepared, profile, append([]string{"--input-path", src.path}, baseArgs...), nil)
	}
	if runErr != nil {
		return nil, nil, nil, runErr
	}
	if !response.OK {
		return nil, nil, nil, workerError(response, stderr)
	}
	data, err := readBounded(outPath, p.opts.MaxOutputBytes)
	if err != nil {
		return nil, nil, nil, err
	}
	if format == "pdf" {
		if err := validatePDF(data); err != nil {
			return nil, nil, nil, err
		}
	}
	return data, response.MissingFonts, response.PageCount, nil
}

func isUnknownInputPath(stderr string, response workerResponse) bool {
	text := response.Error + " " + stderr
	return strings.Contains(text, "Unknown worker argument") && strings.Contains(text, "input-path")
}

// workerResponse 是工作进程 stdout 的那一行 JSON。
type workerResponse struct {
	OK           bool     `json:"ok"`
	Code         string   `json:"code"`
	Error        string   `json:"error"`
	MissingFonts []string `json:"missingFonts"`
	PageCount    *int     `json:"pageCount"`
	Kind         string   `json:"kind"`
	Index        int      `json:"index"`
	Width        int      `json:"width"`
	Height       int      `json:"height"`
	Path         string   `json:"path"`
	TileMode     int      `json:"tileMode"`
	DocumentType string   `json:"documentType"`
	Pages        []struct {
		Width  int64 `json:"width"`
		Height int64 `json:"height"`
	} `json:"pages"`
	WriterRectangles string `json:"writerRectangles"`
	Sheets           []struct {
		Part     int    `json:"part"`
		Info     string `json:"info"`
		Geometry string `json:"geometry"`
	} `json:"sheets"`
}

// workerError 把工作进程的失败映射成官方错误码（未知码降级 failed，
// 官方 cli.js:47-58）。
func workerError(response workerResponse, stderr string) error {
	code := NormalizeCode(response.Code)
	message := response.Error
	if strings.TrimSpace(message) == "" {
		message = strings.TrimSpace(stderr)
	}
	if message == "" {
		message = "原生引擎转换失败"
	}
	return &Error{Code: code, Message: message, Detail: strings.TrimSpace(stderr)}
}

// runWorker 起一次工作进程：可取消、可超时、整棵进程树可杀。
//
// 返回值里的 stderr 在失败时用于归因；stdout 只取第一行 JSON（render-images
// 走另一条流式路径，见 images.go）。
func (p *Provider) runWorker(ctx context.Context, engine *Engine, profile string, args []string, stdin []byte) (workerResponse, string, error) {
	runCtx, cancel := context.WithTimeout(ctx, p.opts.Timeout)
	defer cancel()
	cmd := exec.Command(engine.Executable, args...)
	cmd.Dir = filepath.Dir(engine.Executable)
	cmd.Env = nativeEnvironmentFor(engine, profile)
	cmd.WaitDelay = waitDelay
	var stdout limitedBuffer
	stdout.limit = maxWorkerStdout
	var stderr limitedBuffer
	stderr.limit = maxWorkerStdout
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	stop, wait, err := startManaged(cmd)
	if err != nil {
		return workerResponse{}, "", errf(CodeFailed, "无法启动原生引擎 %s：%v", engine.Executable, err)
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-runCtx.Done():
			stop()
		case <-done:
		}
	}()
	waitErr := wait()
	close(done)
	response := parseWorkerResponse(stdout.String())
	if runCtx.Err() != nil {
		return response, stderr.String(), &Error{Code: CodeTimeout,
			Message: "原生引擎超时或已取消（上限 " + p.opts.Timeout.String() + "）",
			Detail:  strings.TrimSpace(stderr.String())}
	}
	if waitErr != nil && response.OK {
		return response, stderr.String(), errf(CodeFailed, "原生引擎退出异常：%v", waitErr)
	}
	if waitErr != nil && response.Code == "" && response.Error == "" {
		return response, stderr.String(), &Error{Code: CodeFailed, Message: "原生引擎退出异常",
			Detail: fmt.Sprintf("%v %s", waitErr, strings.TrimSpace(stderr.String()))}
	}
	return response, stderr.String(), nil
}

func parseWorkerResponse(text string) workerResponse {
	line := strings.TrimSpace(text)
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = strings.TrimSpace(line[:idx])
	}
	var response workerResponse
	if line == "" {
		return response
	}
	if err := json.Unmarshal([]byte(line), &response); err != nil {
		return workerResponse{Code: string(CodeFailed), Error: "原生引擎返回了非法 JSON"}
	}
	return response
}

// nativeEnvironment 逐字复刻官方 cli.js:754-775。
func nativeEnvironment(profile string) []string {
	env := make([]string, 0, len(nativeEnvironmentKeys)+7)
	for _, key := range nativeEnvironmentKeys {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	env = append(env,
		"HOME="+profile,
		"USERPROFILE="+profile,
		"TMPDIR="+profile,
		"TMP="+profile,
		"TEMP="+profile,
		"SAL_DISABLE_PRINTERLIST=1",
		"SAL_DISABLE_DEFAULTPRINTER=1",
	)
	return env
}

// nativeEnvironmentFor 在 nativeEnvironment 之上补非 Windows 的 loader 路径。
func nativeEnvironmentFor(engine *Engine, profile string) []string {
	env := nativeEnvironment(profile)
	if runtime.GOOS != "windows" && engine != nil {
		env = append(env, "LD_LIBRARY_PATH="+engine.ProgramDirectory)
	}
	return env
}

// prepareNativeEngine 处理 Windows 长路径：官方 cli.js:776-806 在任意路径 ≥260
// 字符时把包根 junction 到私有短目录、并把可执行文件复制过去。
//
// 我们的实现同样只在超长时动手；junction 建不出来就**明确失败**（给出 capableWhen），
// 不带着可能崩的路径继续跑。
func prepareNativeEngine(engine *Engine, scratch string) (*Engine, func(), error) {
	noop := func() {}
	if engine == nil {
		return nil, noop, errf(CodeUnavailable, unsatisfiedMessage)
	}
	if runtime.GOOS != "windows" || (len(engine.Executable) < 260 && len(engine.ProgramDirectory) < 260) {
		return engine, noop, nil
	}
	link := filepath.Join(scratch, "engine")
	if err := os.Symlink(engine.Root, link); err != nil {
		return nil, noop, &Error{Code: CodeFailed,
			Message:     "引擎路径超过 Windows 260 字符上限，且无法把引擎目录链接到短路径",
			Detail:      err.Error(),
			CapableWhen: "把引擎包放到更短的位置（例如 C:\\dsh-engine\\）后用 " + EngineEnvVar + " 指过去"}
	}
	executable := filepath.Join(scratch, "dsoffice.exe")
	if err := copyFile(engine.Executable, executable); err != nil {
		_ = os.Remove(link)
		return nil, noop, errf(CodeFailed, "复制引擎可执行文件失败：%v", err)
	}
	prepared := *engine
	prepared.Root = link
	prepared.Executable = executable
	if rel, err := filepath.Rel(engine.Root, engine.ProgramDirectory); err == nil {
		prepared.ProgramDirectory = filepath.Join(link, rel)
	}
	release := func() {
		_ = os.Remove(executable)
		_ = os.Remove(link)
	}
	return &prepared, release, nil
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(to)
		return err
	}
	return out.Close()
}

// validatePDF 校验完整 PDF：头 `%PDF-<数字>.<数字>`、尾部 `%%EOF`
// （官方 output.ts:47-54 readPdf 的同一判据）。
func validatePDF(data []byte) error {
	if len(data) < 8 {
		return errf(CodeInvalidOutput, "引擎产出的 PDF 过短")
	}
	head := data
	if len(head) > 8 {
		head = head[:8]
	}
	if !bytes.HasPrefix(head, []byte("%PDF-")) || len(head) < 7 || head[5] < '0' || head[5] > '9' {
		return errf(CodeInvalidOutput, "引擎没有产出合法 PDF")
	}
	tail := data
	if len(tail) > 1024 {
		tail = tail[len(tail)-1024:]
	}
	if !bytes.Contains(tail, []byte("%%EOF")) {
		return errf(CodeInvalidOutput, "引擎产出的 PDF 不完整（缺 %%EOF）")
	}
	return nil
}

// readBounded 读一个文件并强制上限；超限给 output-too-large（官方 output.ts:16-37）。
func readBounded(path string, limit int64) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errf(CodeInvalidOutput, "引擎没有产出结果文件")
		}
		return nil, errf(CodeInvalidOutput, "无法读取引擎产物：%v", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errf(CodeInvalidOutput, "引擎产物不是普通文件")
	}
	if info.Size() > limit {
		return nil, errf(CodeOutputTooLarge, "引擎产物 %d 字节，超过 %d 字节上限", info.Size(), limit)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errf(CodeInvalidOutput, "读取引擎产物失败：%v", err)
	}
	if int64(len(data)) > limit {
		return nil, errf(CodeOutputTooLarge, "引擎产物超过 %d 字节上限", limit)
	}
	return data, nil
}

// limitedBuffer 是带上限的写入缓冲；超出部分丢弃（不把内存交给子进程）。
type limitedBuffer struct {
	buf   bytes.Buffer
	limit int
	over  bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.over || b.limit <= 0 {
		b.over = true
		return len(p), nil
	}
	remaining := b.limit - b.buf.Len()
	if remaining <= 0 {
		b.over = true
		return len(p), nil
	}
	if len(p) > remaining {
		b.buf.Write(p[:remaining])
		b.over = true
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *limitedBuffer) String() string { return b.buf.String() }
