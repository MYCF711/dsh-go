// Package officepdf 把 Office 文档真实转换成 PDF。
//
// 渲染器按顺序选择：显式 DSHD_OFFICE_RENDERER（LibreOffice soffice）→ PATH 里的
// soffice → Windows 上已安装的 Microsoft Word（COM）。没有渲染器时返回结构化
// 错误，不生成空 PDF；转换进程有超时/取消，临时目录每次回收。
package officepdf

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

var ErrNoRenderer = &Error{Code: "renderer_unavailable", Message: "没有可用的 Office 渲染器：安装 LibreOffice（soffice）或 Microsoft Word，或设置 DSHD_OFFICE_RENDERER"}

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

type request struct {
	ctx      context.Context
	path     string
	priority string
	result   chan result
}

type result struct {
	doc Rendered
	err error
}

type Provider struct {
	Home       string
	Renderer   string
	Generation string
	queue      chan *request
	mu         sync.Mutex
	closed     bool
	wg         sync.WaitGroup
}

func Open(home string) *Provider {
	p := &Provider{Home: home, queue: make(chan *request, 32)}
	if exe := strings.TrimSpace(os.Getenv("DSHD_OFFICE_RENDERER")); exe != "" {
		p.Renderer = exe
	}
	if p.Renderer == "" {
		if path, err := exec.LookPath("soffice"); err == nil {
			p.Renderer = path
		} else if runtime.GOOS == "windows" {
			const word = `C:\Program Files\Microsoft Office\root\Office16\WINWORD.EXE`
			if _, err := os.Stat(word); err == nil {
				p.Renderer = "word-com"
			}
		}
	}
	p.Generation = generationFor(p.Renderer)
	p.wg.Add(1)
	go p.worker()
	return p
}

func generationFor(renderer string) string {
	sum := sha256.Sum256([]byte("officepdf|" + renderer + "|v1"))
	return "officepdf-" + hex.EncodeToString(sum[:8])
}

func (p *Provider) GenerationID() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.Generation
}

func (p *Provider) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	close(p.queue)
	p.mu.Unlock()
	p.wg.Wait()
}

func (p *Provider) worker() {
	defer p.wg.Done()
	for req := range p.queue {
		doc, err := p.renderNow(req.ctx, req.path, req.priority)
		req.result <- result{doc: doc, err: err}
	}
}

func (p *Provider) Render(ctx context.Context, scope, rawPath, priority string) (Rendered, error) {
	if p.Renderer == "" {
		return Rendered{}, ErrNoRenderer
	}
	if priority != "foreground" && priority != "background" {
		return Rendered{}, &Error{Code: "bad_request", Message: "priority 只能是 foreground / background"}
	}
	path, err := p.resolve(scope, rawPath)
	if err != nil {
		return Rendered{}, err
	}
	req := &request{ctx: ctx, path: path, priority: priority, result: make(chan result, 1)}
	select {
	case p.queue <- req:
	case <-ctx.Done():
		return Rendered{}, ctx.Err()
	}
	select {
	case out := <-req.result:
		return out.doc, out.err
	case <-ctx.Done():
		return Rendered{}, ctx.Err()
	}
}

func (p *Provider) resolve(scope, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", &Error{Code: "bad_request", Message: "path 是空串"}
	}
	if !filepath.IsAbs(raw) {
		base := p.Home
		if scope != "" {
			base = filepath.Join(p.Home, "workspaces", scope)
		}
		raw = filepath.Join(base, raw)
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", &Error{Code: "not_found", Message: "Office 文件不存在：" + abs}
	}
	if info.IsDir() {
		return "", &Error{Code: "bad_request", Message: "path 是目录，不是 Office 文件"}
	}
	ext := strings.ToLower(filepath.Ext(abs))
	switch ext {
	case ".doc", ".docx", ".odt", ".rtf", ".xls", ".xlsx", ".ods", ".ppt", ".pptx", ".odp":
	default:
		return "", &Error{Code: "unsupported", Message: "不支持的 Office 文件类型：" + ext}
	}
	return abs, nil
}

func (p *Provider) renderNow(ctx context.Context, src, priority string) (Rendered, error) {
	dir, err := os.MkdirTemp("", "dsh-officepdf-*")
	if err != nil {
		return Rendered{}, err
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "rendered.pdf")
	var renderErr error
	if strings.EqualFold(p.Renderer, "word-com") {
		renderErr = renderWord(ctx, src, out)
	} else {
		renderErr = renderLibreOffice(ctx, p.Renderer, src, out)
	}
	if renderErr != nil {
		return Rendered{}, renderErr
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		return Rendered{}, &Error{Code: "render_failed", Message: "读 PDF 失败：" + err.Error()}
	}
	if len(raw) < 5 || string(raw[:5]) != "%PDF-" {
		return Rendered{}, &Error{Code: "render_failed", Message: "渲染器没有产出合法 PDF"}
	}
	sum := sha256.Sum256(raw)
	info, _ := os.Stat(src)
	version := hex.EncodeToString(sum[:8])
	if info != nil {
		version = fmt.Sprintf("%s-%d", version, info.ModTime().UnixNano())
	}
	n := len(raw)
	return Rendered{MissingFonts: []string{}, Generation: p.Generation, Offset: 0, Data: raw, Eof: true,
		AbsolutePath: src, Version: version, Bytes: &n}, nil
}

func renderLibreOffice(ctx context.Context, exe, src, out string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "--headless", "--norestore", "--convert-to", "pdf", "--outdir", filepath.Dir(out), src)
	cmd.Env = append(os.Environ(), "HOME="+filepath.Dir(out), "SAL_USE_VCLPLUGIN=svp")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return &Error{Code: "render_timeout", Message: "LibreOffice 渲染超时/取消"}
		}
		return &Error{Code: "render_failed", Message: fmt.Sprintf("LibreOffice 失败：%v %s", err, strings.TrimSpace(stderr.String()))}
	}
	base := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src)) + ".pdf"
	produced := filepath.Join(filepath.Dir(out), base)
	if produced != out {
		return os.Rename(produced, out)
	}
	return nil
}

func renderWord(ctx context.Context, src, out string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	ps := `$ErrorActionPreference='Stop'; $w=New-Object -ComObject Word.Application; $w.Visible=$false; $d=$null; try { $d=$w.Documents.Open($env:DSH_OFFICE_SRC,$false,$true); $d.ExportAsFixedFormat($env:DSH_OFFICE_OUT,17) } finally { if ($d -ne $null) { $d.Close($false) }; $w.Quit() }`
	const powershell = `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`
	cmd := exec.CommandContext(ctx, powershell, "-NoProfile", "-NonInteractive", "-Command", ps)
	cmd.Env = append(os.Environ(), "DSH_OFFICE_SRC="+src, "DSH_OFFICE_OUT="+out)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return &Error{Code: "render_timeout", Message: "Word 渲染超时/取消"}
		}
		return &Error{Code: "render_failed", Message: fmt.Sprintf("Word COM 失败：%v %s", err, strings.TrimSpace(stderr.String()))}
	}
	return nil
}
