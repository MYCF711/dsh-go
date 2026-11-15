// Package officepdf 是官方 `officeToPdf`（rc.1 名字）方法的宿主适配层。
//
// 从官方 0.2.2-rc.2 起，真正的渲染器是 `@deepseek-ai/dsh-office-kit` + 随包预编译的
// 原生 LibreOfficeKit 引擎（`libreoffice-kit-win32-x64/bin/dsoffice.exe`）。本包因此
// 不再自己找 soffice / Word COM，而是把调用转给 internal/officekit：
//
//   - 保留既有对外形状（Open / Render / GenerationID / Close / Rendered / Error），
//     所以 `cmd/dshd/serve_office_pdf.go` 无需改动即可拿到真渲染；
//   - 错误码继续用旧调用方认识的字符串（`serve_office_pdf.go` 的 HTTP 映射依赖它），
//     但 **unavailable 时会附带 capableWhen**，说清装/配什么就能用。
//
// 官方对照：docs/upgrade-guide/v0.2.2-alpha.3/office-kit-package/guide.md
// （`OfficeToPdf*` → `OfficeKit*`；`officeToPdf/render` → `officeKit/render`）。
package officepdf

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"dshgo/internal/officekit"
)

// Rendered 与官方 RenderedDocumentBytes 同形；类型别名让旧调用点零改动。
type Rendered = officekit.Rendered

// Error 是旧调用方认识的结构化错误（Code 沿用 officeToPdf 时代的字符串）。
type Error struct {
	Code    string
	Message string
	// CapableWhen 只在 Code==renderer_unavailable 时出现：说明怎么才能用。
	CapableWhen string
}

func (e *Error) Error() string { return e.Message }

// ErrNoRenderer 在没有引擎时返回（保留旧名字）。
var ErrNoRenderer = &Error{
	Code:        "renderer_unavailable",
	Message:     "没有可用的 Office 渲染器：" + officekit.CapableWhen(),
	CapableWhen: officekit.CapableWhen(),
}

// CodeOf 取结构化错误码。
func CodeOf(err error) string {
	var target *Error
	if errors.As(err, &target) {
		return target.Code
	}
	var kitErr *officekit.Error
	if errors.As(err, &kitErr) {
		return string(kitErr.Code)
	}
	return ""
}

// CapableWhen 透出「装/配什么就能用」。
func CapableWhen() string { return officekit.CapableWhen() }

// Provider 是 officeToPdf 方法面的 provider。
type Provider struct {
	Home string
	// Renderer 保留旧字段：显式引擎路径（等价于 officekit 的 Options.EnginePath）。
	Renderer string

	mu         sync.Mutex
	kit        *officekit.Provider
	searchRoot []string
}

// Open 构造 provider；home 用于产物目录与相对路径解析基准。
func Open(home string) *Provider {
	p := &Provider{Home: home}
	p.attach(officekit.Options{Home: home})
	return p
}

// Wrap 用已存在的 officeKit provider 复用同一份引擎与队列（推荐给宿主接线用）。
func Wrap(home string, kit *officekit.Provider) *Provider {
	p := &Provider{Home: home, kit: kit}
	return p
}

func (p *Provider) attach(opts officekit.Options) {
	opts.Home = p.Home
	if opts.EnginePath == "" {
		opts.EnginePath = strings.TrimSpace(os.Getenv(officekit.EngineEnvVar))
	}
	// 旧变量 DSHD_OFFICE_RENDERER 若指向引擎包根，也当作显式配置认。
	if opts.EnginePath == "" {
		if legacy := strings.TrimSpace(os.Getenv("DSHD_OFFICE_RENDERER")); legacy != "" {
			if info, err := os.Stat(legacy); err == nil && (info.IsDir() || strings.EqualFold(filepath.Base(legacy), "dsoffice.exe")) {
				opts.EnginePath = legacy
			}
		}
	}
	p.mu.Lock()
	p.kit = officekit.Open(opts)
	p.mu.Unlock()
}

func (p *Provider) kitProvider() *officekit.Provider {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.kit
}

// Engine 返回已发现的引擎（没有则 nil）——给测试与状态面板用。
func (p *Provider) Engine() *officekit.Engine { return p.kitProvider().Engine() }

// Tried 返回引擎发现试过的路径（排障用）。
func (p *Provider) Tried() []string { return p.kitProvider().Capability().Tried }

// Capability 报告能力与补救方法。
func (p *Provider) Capability() officekit.Capability { return p.kitProvider().Capability() }

// GenerationID 是 provider 生命周期标识（引擎一变就变）。
func (p *Provider) GenerationID() string { return p.kitProvider().GenerationID() }

// Close 回收 provider。
func (p *Provider) Close() { p.kitProvider().Close() }

// SetEngineUnavailableForTest 把发现范围收窄到一个没有引擎的目录，
// 用来验证「没有引擎时必须是结构化 501，不许假渲染」。
func (p *Provider) SetEngineUnavailableForTest(dir string) {
	p.mu.Lock()
	p.searchRoot = []string{dir}
	p.mu.Unlock()
	p.attach(officekit.Options{
		Home:        p.Home,
		EnginePath:  filepath.Join(dir, "no-such-engine"),
		SearchRoots: []string{dir},
	})
}

// Render 把文档真实转成 PDF。scope 为会话/工作区标识，path 可为相对路径。
func (p *Provider) Render(ctx context.Context, scope, rawPath, priority string) (Rendered, error) {
	abs, err := p.resolve(scope, rawPath)
	if err != nil {
		return Rendered{}, err
	}
	doc, err := p.kitProvider().Render(ctx, abs, priority)
	if err != nil {
		return Rendered{}, translate(err)
	}
	return doc, nil
}

// translate 把 officekit 的官方错误码映射回旧调用方认识的字符串，
// 并把 capableWhen 拼进 Message（Transport 只透出 code + message）。
func translate(err error) error {
	var kitErr *officekit.Error
	if !errors.As(err, &kitErr) {
		return err
	}
	message := kitErr.Error()
	code := "render_failed"
	switch kitErr.Code {
	case officekit.CodeUnavailable:
		code = "renderer_unavailable"
		if kitErr.CapableWhen != "" && !strings.Contains(message, kitErr.CapableWhen) {
			message += "；补救方式：" + kitErr.CapableWhen
		}
	case officekit.CodeTimeout:
		code = "render_timeout"
	case officekit.CodeInvalidRequest, officekit.CodeInvalidDocument, officekit.CodeSourceChanged:
		code = "bad_request"
	case officekit.CodeUnsupportedFormat:
		code = "unsupported"
	case officekit.CodeInputTooLarge, officekit.CodeOutputTooLarge, officekit.CodeInvalidOutput:
		code = "bad_request"
	case officekit.CodeBusy:
		code = "render_failed"
	}
	converted := &Error{Code: code, Message: message, CapableWhen: kitErr.CapableWhen}
	if kitErr.Code == officekit.CodeUnavailable {
		return &Error{Code: code, Message: converted.Message, CapableWhen: kitErr.CapableWhen}
	}
	return converted
}

// resolve 把 scope+相对路径归一成绝对路径，并核验存在性与类型白名单。
//
// 白名单与官方一致（doc/docx/xls/xlsx/ppt/pptx；rc.1 还认 odt/ods/odp/rtf，
// rc.2 的 officeKit 只认 OOXML，所以这里与官方对齐）。
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
		return "", &Error{Code: "bad_request", Message: "路径无法解析：" + err.Error()}
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", &Error{Code: "not_found", Message: "Office 文件不存在：" + abs}
		}
		return "", &Error{Code: "render_failed", Message: "无法读取文件状态：" + err.Error()}
	}
	if info.IsDir() {
		return "", &Error{Code: "bad_request", Message: "path 是目录，不是 Office 文件"}
	}
	ext := strings.ToLower(filepath.Ext(abs))
	switch ext {
	case ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx":
	default:
		return "", &Error{Code: "unsupported", Message: fmt.Sprintf("不支持的 Office 文件类型：%s（官方 rc.2 只认 doc/docx/xls/xlsx/ppt/pptx）", ext)}
	}
	return abs, nil
}
