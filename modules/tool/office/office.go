package office

// 三个 Office 工具的模型面实现。
//
// 一次调用的完整链路（与官方 packages/document/tool-office/src/index.ts:142-427 对应）：
//
//	参数形状校验 → 输入族/格式矩阵校验 → 解析并授权读源文件（spi.FS）
//	→ officekit 真转换（外部原生引擎，可杀可超时）→ 官方形状的结构化结果
//
// 边界：工具**不写工作区**。convert/recalculate 把产物放进 provider 的私有产物仓
// （官方 outputStore，保留期后自动清），模型要用 shell 自己复制到目的地；
// render 把 PNG 作为图片结果返回（宿主把它挪进一条用户消息，才可能被模型看到）。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"dshgo/internal/kernel"
	"dshgo/internal/officekit"
	"dshgo/internal/spi"
)

// 各工具的模型面 schema：逐字对齐官方 tool-catalog.md。
var (
	convertSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "input": {"type": "string", "description": "Path to doc, docx, odt, xls, xlsx, ods, ppt, pptx, or odp; not modified."},
    "format": {"type": "string", "description": "Word: pdf, docx, odt, txt. Excel: pdf, xlsx, ods, csv. PowerPoint: pdf, pptx, odp.",
      "enum": ["pdf", "docx", "odt", "txt", "xlsx", "ods", "csv", "pptx", "odp"]},
    "sheet": {"type": "string", "description": "Exact worksheet name for CSV only; required when the workbook has several sheets."}
  },
  "required": ["input", "format"],
  "additionalProperties": false
}`)

	recalculateSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "input": {"type": "string", "description": "Path to an xls, xlsx, or ods workbook; not modified."},
    "format": {"type": "string", "description": "Output workbook format.", "enum": ["xlsx", "ods"]}
  },
  "required": ["input", "format"],
  "additionalProperties": false
}`)

	renderSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "input": {"type": "string", "description": "Path to doc, docx, odt, xls, xlsx, ods, ppt, pptx, odp, or pdf; not modified."},
    "pages": {"type": "array", "description": "Distinct one-based pages or slides; omit for all. Spreadsheets use sheet and range instead.", "items": {"type": "integer"}},
    "sheet": {"type": "string", "description": "Exact worksheet name; omit to render every visible sheet."},
    "range": {"type": "string", "description": "A1 range, such as A1:H40; requires sheet. Omit for the used area."},
    "dpi": {"type": "integer", "description": "Resolution from 24 to 600; default 144."}
  },
  "required": ["input"],
  "additionalProperties": false
}`)
)

// 每个工具声明的参数键（官方 index.ts:114-118 PARAMETER_NAMES）。
var parameterNames = map[string][]string{
	ToolRender:      {"input", "pages", "sheet", "range", "dpi"},
	ToolConvert:     {"input", "format", "sheet"},
	ToolRecalculate: {"input", "format"},
}

// Tool 是三个 Office 工具的共同实现。
type Tool struct {
	operation string
	fs        spi.FS
	kit       *officekit.Provider
}

func (t *Tool) Name() string { return t.operation }

func (t *Tool) Description() string {
	switch t.operation {
	case ToolConvert:
		return "Convert a document to another format. Return a temporary file to copy to its destination with the shell."
	case ToolRecalculate:
		return "Recalculate spreadsheet formulas. Return a temporary workbook to copy to its destination with the shell."
	default:
		return "Render document pages, slides, or worksheet areas as images you can view."
	}
}

func (t *Tool) Schema() json.RawMessage {
	switch t.operation {
	case ToolConvert:
		return convertSchema
	case ToolRecalculate:
		return recalculateSchema
	default:
		return renderSchema
	}
}

// Caps：三者都会起外部原生引擎（Executes）；convert/recalculate 还会往宿主磁盘写产物
// （Writes，但那不是工作区写入，也不可恢复——保留期后自动删，所以不是 Destructive）。
func (t *Tool) Caps() spi.Caps {
	switch t.operation {
	case ToolConvert, ToolRecalculate:
		return spi.Caps{Writes: true, Executes: true}
	default:
		return spi.Caps{Executes: true}
	}
}

// Exec 执行一次调用。
func (t *Tool) Exec(ctx context.Context, raw json.RawMessage) (spi.Result, error) {
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return spi.Result{Hint: fmt.Sprintf(`参数不是合法 JSON；%s 的期望形状见 schema`, t.operation)},
			fmt.Errorf("参数解析失败: %w", err)
	}
	if err := checkParameterNames(t.operation, args, parameterNames); err != nil {
		return spi.Result{Hint: "按 schema 只传该工具声明的参数（pages 只属于 office_render，sheet 只属于 office_convert / office_render）"}, err
	}
	input, ok := args["input"].(string)
	if !ok || strings.TrimSpace(input) == "" {
		return spi.Result{Hint: "input 必填：给一个 doc/docx/odt/xls/xlsx/ods/ppt/pptx/odp 路径"}, fmt.Errorf("input 必须是字符串")
	}
	resolved, err := t.resolveInput(input)
	if err != nil {
		return spi.Result{Hint: "先用 glob/read 确认文件在工作区里存在，再重试"}, err
	}
	switch t.operation {
	case ToolConvert:
		return t.convert(ctx, args, input, resolved)
	case ToolRecalculate:
		return t.recalculate(ctx, args, input, resolved)
	default:
		return t.render(ctx, args, input, resolved)
	}
}

func (t *Tool) resolveInput(input string) (string, error) {
	if t.fs == nil {
		return "", fmt.Errorf("没有挂载文件系统服务，无法授权读取 %q", input)
	}
	abs, err := t.fs.Resolve(input)
	if err != nil {
		return "", fmt.Errorf("cannot read %q: %w", input, err)
	}
	if size, exists, statErr := t.fs.Stat(abs); statErr != nil || !exists {
		return "", fmt.Errorf("cannot read %q: not found", input)
	} else if size < 0 {
		return "", fmt.Errorf("cannot read %q: not a regular file", input)
	}
	return abs, nil
}

// 结果结构（与官方 content.ts 的 canonical value 同形）。
type fileValue struct {
	Operation    string   `json:"operation"`
	Path         string   `json:"path"`
	File         string   `json:"file"`
	Format       string   `json:"format"`
	Bytes        int64    `json:"bytes"`
	PageCount    *int     `json:"pageCount,omitempty"`
	MissingFonts []string `json:"missingFonts"`
}

type renderedImage struct {
	Page         int    `json:"page,omitempty"`
	Sheet        string `json:"sheet,omitempty"`
	Range        string `json:"range,omitempty"`
	AttachmentID string `json:"attachmentId"`
	MediaType    string `json:"mediaType"`
	Bytes        int    `json:"bytes"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
}

type renderValue struct {
	Operation    string          `json:"operation"`
	Path         string          `json:"path"`
	PageCount    int             `json:"pageCount"`
	DPI          int             `json:"dpi"`
	Images       []renderedImage `json:"images"`
	MissingFonts []string        `json:"missingFonts"`
}

func (t *Tool) convert(ctx context.Context, args map[string]any, input, abs string) (spi.Result, error) {
	format, _ := args["format"].(string)
	format = strings.TrimSpace(format)
	if format == "" {
		return spi.Result{Hint: "format 必填：pdf/docx/odt/txt/xlsx/ods/csv/pptx/odp"}, fmt.Errorf("format 必须是字符串")
	}
	sheet, _ := args["sheet"].(string)
	family, err := inputFamily(ToolConvert, input)
	if err != nil {
		return spi.Result{Hint: "按输入的文档族选格式：Word→pdf/docx/odt/txt，Excel→pdf/xlsx/ods/csv，PowerPoint→pdf/pptx/odp"}, err
	}
	if err := checkConversionArguments(input, format, sheet, family); err != nil {
		return spi.Result{Hint: "sheet 只在 CSV 转换时有效；其它格式按输入族转换"}, err
	}
	result, err := t.kit.ConvertTo(ctx, "office-tool", abs, outputExtension(format), sheet)
	if err != nil {
		return spi.Result{Hint: officeHint(err)}, err
	}
	value := fileValue{Operation: "convert", Path: steep(abs), File: result.Path, Format: format,
		Bytes: result.Bytes, PageCount: result.PageCount, MissingFonts: nonNil(result.MissingFonts)}
	return spi.Result{
		Summary: fmt.Sprintf("converted %s to %s (%d bytes)", input, format, result.Bytes),
		Data:    value,
		Text:    fileText("Converted", value),
	}, nil
}

func (t *Tool) recalculate(ctx context.Context, args map[string]any, input, abs string) (spi.Result, error) {
	format, _ := args["format"].(string)
	format = strings.TrimSpace(format)
	valid := false
	for _, candidate := range RecalculationFormats {
		if candidate == format {
			valid = true
		}
	}
	if !valid {
		return spi.Result{Hint: "format 只能是 xlsx 或 ods"}, fmt.Errorf("format 必须是 xlsx 或 ods，拿到 %q", format)
	}
	if _, err := inputFamily(ToolRecalculate, input); err != nil {
		return spi.Result{Hint: "office_recalculate 只接受 xls / xlsx / ods 工作簿"}, err
	}
	result, err := t.kit.RecalculateTo(ctx, "office-tool", abs, format)
	if err != nil {
		return spi.Result{Hint: officeHint(err)}, err
	}
	value := fileValue{Operation: "recalculate", Path: steep(abs), File: result.Path, Format: format,
		Bytes: result.Bytes, PageCount: result.PageCount, MissingFonts: nonNil(result.MissingFonts)}
	return spi.Result{
		Summary: fmt.Sprintf("recalculated %s -> %s (%d bytes)", input, format, result.Bytes),
		Data:    value,
		Text:    fileText("Recalculated", value),
	}, nil
}

func (t *Tool) render(ctx context.Context, args map[string]any, input, abs string) (spi.Result, error) {
	family, err := inputFamily(ToolRender, input)
	if err != nil {
		return spi.Result{Hint: "office_render 接受 doc/docx/odt/xls/xlsx/ods/ppt/pptx/odp/pdf"}, err
	}
	pages, err := pageList(args["pages"])
	if err != nil {
		return spi.Result{Hint: "pages 必须是互不相同的、从 1 开始的页号数组"}, err
	}
	sheet, _ := args["sheet"].(string)
	cellRange, _ := args["range"].(string)
	var dpi *int64
	if raw, ok := args["dpi"]; ok && raw != nil {
		number, ok := raw.(float64)
		if !ok || number != float64(int64(number)) {
			return spi.Result{Hint: "dpi 必须是整数"}, fmt.Errorf("dpi 必须是整数")
		}
		value := int64(number)
		dpi = &value
	}
	if err := checkRenderArguments(pages, sheet, cellRange, dpi, family); err != nil {
		return spi.Result{Hint: "工作表用 sheet(+range)，非工作表用 pages；dpi 在 24..600"}, err
	}
	request := officekit.ImageRequest{Pages: toInts(pages), Sheet: strings.TrimSpace(sheet), Range: strings.TrimSpace(cellRange)}
	if dpi != nil {
		request.DPI = int(*dpi)
	}
	result, err := t.kit.RenderImages(ctx, abs, request)
	if err != nil {
		return spi.Result{Hint: officeHint(err)}, err
	}
	if len(result.Images) == 0 {
		return spi.Result{Hint: "文档没有可渲染的页面；确认 sheet/range/pages 后重试"},
			fmt.Errorf("渲染没有产出任何图片")
	}
	value := renderValue{Operation: "render", Path: steep(abs), PageCount: result.PageCount,
		DPI: result.DPI, MissingFonts: nonNil(result.MissingFonts)}
	var images []spi.Image
	var lines []string
	lines = append(lines, renderSummary(len(result.Images), input, result.PageCount, result.DPI, result.MissingFonts))
	for index, image := range result.Images {
		attachmentID := fmt.Sprintf("office-image-%d-%dx%d", index+1, image.Width, image.Height)
		rendered := renderedImage{Page: image.Page, Sheet: image.Sheet, Range: image.Range,
			AttachmentID: attachmentID, MediaType: "image/png", Bytes: len(image.PNG),
			Width: image.Width, Height: image.Height}
		value.Images = append(value.Images, rendered)
		lines = append(lines, fmt.Sprintf("%s: %dx%d px", imageLocation(rendered), image.Width, image.Height))
		images = append(images, spi.Image{MIME: "image/png", DataBase64: base64.StdEncoding.EncodeToString(image.PNG)})
	}
	return spi.Result{
		Summary: fmt.Sprintf("rendered %d image(s) of %s at %d DPI", len(result.Images), input, result.DPI),
		Data:    value,
		Text:    strings.Join(lines, "\n"),
		Images:  images,
		View: &spi.ViewSpec{Kind: spi.ViewCard, Title: "office_render",
			Badge: fmt.Sprintf("%d 图", len(result.Images))},
	}, nil
}

// Module 装配三个工具。provider 必须由宿主创建并复用（同一引擎/同一队列）。
func Module(provider *officekit.Provider) kernel.Module {
	return kernel.Module{
		ID:       "tool-office",
		Backend:  kernel.BackendStatic,
		Requires: []string{"tools", "fs"},
		Register: func(s *kernel.Scope) error {
			registry, err := kernel.Inject(s, spi.ToolsKey)
			if err != nil {
				return err
			}
			fsValue, err := kernel.Inject(s, spi.FSKey)
			if err != nil {
				return err
			}
			for _, operation := range []string{ToolRender, ToolConvert, ToolRecalculate} {
				tool := &Tool{operation: operation, kit: provider, fs: fsValue}
				if err := registry.Register(tool); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// ---------------------------------------------------------------------------
// 呈现细节（与官方 content.ts 的措辞一致）
// ---------------------------------------------------------------------------

func fileText(verb string, value fileValue) string {
	pages := ""
	if value.PageCount != nil {
		unit := "pages"
		if *value.PageCount == 1 {
			unit = "page"
		}
		pages = fmt.Sprintf(", %d %s", *value.PageCount, unit)
	}
	fonts := ""
	if len(value.MissingFonts) > 0 {
		fonts = "\nFonts not installed and substituted: " + strings.Join(value.MissingFonts, ", ") + "."
	}
	return fmt.Sprintf("%s %s to %s (%d bytes%s): %s\nCopy this temporary file to its destination with the shell now; "+
		"it may be deleted after a retention period or to free space.%s",
		verb, value.Path, value.Format, value.Bytes, pages, value.File, fonts)
}

func renderSummary(count int, path string, pageCount, dpi int, missingFonts []string) string {
	unit := "images"
	if count == 1 {
		unit = "image"
	}
	fonts := ""
	if len(missingFonts) > 0 {
		fonts = "\nFonts not installed and substituted: " + strings.Join(missingFonts, ", ") + "."
	}
	return fmt.Sprintf("Rendered %d %s of %s (%d in the document) at %d DPI.%s", count, unit, path, pageCount, dpi, fonts)
}

func imageLocation(image renderedImage) string {
	if image.Page > 0 {
		return fmt.Sprintf("Page %d", image.Page)
	}
	if image.Sheet == "" {
		return "Image"
	}
	if image.Range == "" {
		return fmt.Sprintf("Sheet %q", image.Sheet)
	}
	return fmt.Sprintf("Sheet %q %s", image.Sheet, image.Range)
}

// steep 只做展示用的路径归一（不改变真实路径）。
func steep(path string) string { return filepath.ToSlash(path) }

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func pageList(raw any) ([]int64, error) {
	if raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("pages 必须是数组")
	}
	var result []int64
	for _, item := range list {
		number, ok := item.(float64)
		if !ok || number != float64(int64(number)) {
			return nil, fmt.Errorf("pages 必须是整数")
		}
		result = append(result, int64(number))
	}
	return result, nil
}

func toInts(values []int64) []int {
	if values == nil {
		return nil
	}
	result := make([]int, 0, len(values))
	for _, value := range values {
		result = append(result, int(value))
	}
	return result
}
