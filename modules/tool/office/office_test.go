package office

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dshgo/internal/officekit"
	"dshgo/internal/spi"
	"dshgo/modules/fs/local"
)

// workspace 造一个临时工作区，把 testdata 里的真实 Office 文件放进去。
func workspace(t *testing.T) (string, *local.FS) {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"probe.docx", "book.xlsx"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "..", "internal", "officekit", "testdata", name))
		if err != nil {
			t.Skipf("testdata 缺失：%v", err)
		}
		if err := os.WriteFile(filepath.Join(root, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fs, err := local.New(root)
	if err != nil {
		t.Fatalf("local fs: %v", err)
	}
	return root, fs
}

func provider(t *testing.T) *officekit.Provider {
	t.Helper()
	p := officekit.Open(officekit.Options{Home: t.TempDir(), Timeout: 120 * time.Second})
	t.Cleanup(p.Close)
	return p
}

func toolFor(t *testing.T, fs spi.FS, kit *officekit.Provider, operation string) *Tool {
	t.Helper()
	return &Tool{operation: operation, fs: fs, kit: kit}
}

func exec(t *testing.T, tool *Tool, args string) (spi.Result, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	return tool.Exec(ctx, json.RawMessage(args))
}

// schema 必须逐字对齐官方 tool-catalog.md（三个工具的 required / enum / 描述）。
func TestSchemasMatchOfficialCatalog(t *testing.T) {
	fs := spi.FS(nil)
	kit := provider(t)
	cases := []struct {
		operation string
		required  []string
		enums     map[string][]string
	}{
		{ToolConvert, []string{"input", "format"}, map[string][]string{
			"format": {"pdf", "docx", "odt", "txt", "xlsx", "ods", "csv", "pptx", "odp"}}},
		{ToolRecalculate, []string{"input", "format"}, map[string][]string{
			"format": {"xlsx", "ods"}}},
		{ToolRender, []string{"input"}, nil},
	}
	for _, testCase := range cases {
		t.Run(testCase.operation, func(t *testing.T) {
			tool := toolFor(t, fs, kit, testCase.operation)
			var schema struct {
				Type       string `json:"type"`
				Required   []string
				Properties map[string]struct {
					Type        string `json:"type"`
					Description string `json:"description"`
					Enum        []string
					Items       struct {
						Type string `json:"type"`
					} `json:"items"`
				}
			}
			if err := json.Unmarshal(tool.Schema(), &schema); err != nil {
				t.Fatalf("schema 不是合法 JSON：%v", err)
			}
			if schema.Type != "object" {
				t.Fatalf("schema.type = %q", schema.Type)
			}
			if strings.Join(schema.Required, ",") != strings.Join(testCase.required, ",") {
				t.Fatalf("required 不对：%v，期望 %v", schema.Required, testCase.required)
			}
			for field, wantEnum := range testCase.enums {
				property, ok := schema.Properties[field]
				if !ok {
					t.Fatalf("缺属性 %s", field)
				}
				if strings.Join(property.Enum, ",") != strings.Join(wantEnum, ",") {
					t.Fatalf("%s 的 enum 不对：%v", field, property.Enum)
				}
			}
			for name, property := range schema.Properties {
				if strings.TrimSpace(property.Description) == "" {
					t.Fatalf("%s 缺 description（模型靠它决定怎么传参）", name)
				}
			}
			if testCase.operation == ToolRender {
				if len(schema.Properties["pages"].Items.Type) == 0 {
					t.Fatal("pages 必须是 integer 数组")
				}
			}
			if tool.Name() != testCase.operation {
				t.Fatalf("工具名 = %q", tool.Name())
			}
			if strings.TrimSpace(tool.Description()) == "" {
				t.Fatal("description 不能为空")
			}
		})
	}
}

// Caps 必须显式声明：三者都起外部引擎，convert/recalculate 还会写宿主磁盘。
func TestCapsDeclared(t *testing.T) {
	kit := provider(t)
	if caps := toolFor(t, nil, kit, ToolRender).Caps(); !caps.Executes || caps.Writes {
		t.Fatalf("office_render Caps 不对：%+v", caps)
	}
	for _, operation := range []string{ToolConvert, ToolRecalculate} {
		caps := toolFor(t, nil, kit, operation).Caps()
		if !caps.Executes || !caps.Writes {
			t.Fatalf("%s Caps 不对：%+v", operation, caps)
		}
	}
}

// 参数层：未知键、缺 input、族与格式不匹配、pages/sheet 冲突都要在碰引擎前拒。
func TestArgumentValidation(t *testing.T) {
	root, fs := workspace(t)
	_ = root
	kit := provider(t)
	convert := toolFor(t, fs, kit, ToolConvert)
	cases := []struct {
		name string
		tool *Tool
		args string
		want string
	}{
		{"未知键", convert, `{"input":"probe.docx","format":"pdf","pages":[1]}`, "pages applies only to office_render"},
		{"缺input", convert, `{"format":"pdf"}`, "input 必填"},
		{"docx转xlsx", convert, `{"input":"probe.docx","format":"xlsx"}`, "this file converts to"},
		{"sheet非csv", convert, `{"input":"book.xlsx","format":"pdf","sheet":"Data"}`, "sheet applies only to CSV"},
		{"重算非工作簿", toolFor(t, fs, kit, ToolRecalculate), `{"input":"probe.docx","format":"xlsx"}`, "recalculate accepts"},
		{"重算格式非法", toolFor(t, fs, kit, ToolRecalculate), `{"input":"book.xlsx","format":"pdf"}`, "format 只能是 xlsx 或 ods"},
		{"渲染工作表用pages", toolFor(t, fs, kit, ToolRender), `{"input":"book.xlsx","pages":[1]}`, "pages does not apply to spreadsheets"},
		{"渲染非表格用sheet", toolFor(t, fs, kit, ToolRender), `{"input":"probe.docx","sheet":"Data"}`, "sheet applies only to spreadsheets"},
		{"range缺sheet", toolFor(t, fs, kit, ToolRender), `{"input":"book.xlsx","range":"A1:B2"}`, "range requires sheet"},
		{"dpi越界", toolFor(t, fs, kit, ToolRender), `{"input":"probe.docx","dpi":900}`, "dpi must be an integer from 24 to 600"},
		{"文件不存在", convert, `{"input":"missing.docx","format":"pdf"}`, "cannot read"},
		{"非法JSON", convert, `not json`, "参数不是合法 JSON"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := exec(t, testCase.tool, testCase.args)
			if err == nil {
				t.Fatalf("应当失败：%+v", result)
			}
			if result.Hint == "" {
				t.Fatal("失败必须带 Hint（R3）")
			}
			// 措辞可能落在 error 上，也可能落在 Hint 上（R3：失败必须给出下一步）。
			combined := err.Error() + " | " + result.Hint
			if !strings.Contains(combined, testCase.want) {
				t.Fatalf("错误措辞没说到点上：%v / hint=%q（期望含 %q）", err, result.Hint, testCase.want)
			}
		})
	}
}

// 真跑：docx → PDF（office_convert）。
func TestConvertRealDocxToPDF(t *testing.T) {
	_, fs := workspace(t)
	kit := provider(t)
	if kit.Engine() == nil {
		t.Skip("本机没有 LibreOfficeKit 引擎")
	}
	result, err := exec(t, toolFor(t, fs, kit, ToolConvert), `{"input":"probe.docx","format":"pdf"}`)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	var value struct {
		Operation    string
		Path         string
		File         string
		Format       string
		Bytes        int64
		MissingFonts []string
	}
	raw, _ := json.Marshal(result.Data)
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("结果结构：%v", err)
	}
	if value.Operation != "convert" || value.Format != "pdf" || value.Bytes < 1000 {
		t.Fatalf("结果不对：%+v", value)
	}
	produced, err := os.ReadFile(value.File)
	if err != nil {
		t.Fatalf("产物读不到：%v", err)
	}
	if !strings.HasPrefix(string(produced), "%PDF-") || int64(len(produced)) != value.Bytes {
		t.Fatalf("产物不是完整 PDF：%d 字节 %q", len(produced), produced[:min(8, len(produced))])
	}
	if value.MissingFonts == nil {
		t.Fatal("missingFonts 必须是数组")
	}
	if !strings.Contains(result.Text, "Copy this temporary file") {
		t.Fatalf("呈现文本缺复制指引：%q", result.Text)
	}
	t.Logf("office_convert 真实产物：%s（%d 字节）", value.File, value.Bytes)
}

// 真跑：xlsx → csv（带工作表族校验）。
func TestConvertRealXlsxToCSV(t *testing.T) {
	_, fs := workspace(t)
	kit := provider(t)
	if kit.Engine() == nil {
		t.Skip("本机没有 LibreOfficeKit 引擎")
	}
	result, err := exec(t, toolFor(t, fs, kit, ToolConvert), `{"input":"book.xlsx","format":"csv"}`)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	var value struct {
		File  string
		Bytes int64
	}
	raw, _ := json.Marshal(result.Data)
	_ = json.Unmarshal(raw, &value)
	data, err := os.ReadFile(value.File)
	if err != nil {
		t.Fatalf("产物读不到：%v", err)
	}
	if !strings.Contains(string(data), "value,doubled") {
		t.Fatalf("CSV 内容不对：%q", string(data))
	}
	t.Logf("office_convert 真实产物：%s（%d 字节）", value.File, value.Bytes)
}

// 真跑：公式重算。
func TestRecalculateRealWorkbook(t *testing.T) {
	_, fs := workspace(t)
	kit := provider(t)
	if kit.Engine() == nil {
		t.Skip("本机没有 LibreOfficeKit 引擎")
	}
	result, err := exec(t, toolFor(t, fs, kit, ToolRecalculate), `{"input":"book.xlsx","format":"xlsx"}`)
	if err != nil {
		t.Fatalf("recalculate: %v", err)
	}
	var value struct {
		Operation, File string
		Bytes           int64
	}
	raw, _ := json.Marshal(result.Data)
	_ = json.Unmarshal(raw, &value)
	if value.Operation != "recalculate" || value.Bytes < 1000 {
		t.Fatalf("结果不对：%+v", value)
	}
	if _, err := os.Stat(value.File); err != nil {
		t.Fatalf("产物不存在：%v", err)
	}
	t.Logf("office_recalculate 真实产物：%s（%d 字节）", value.File, value.Bytes)
}

// 真跑：docx 渲染成图片，图片必须以 spi.Image 交给宿主（否则模型看不到）。
func TestRenderRealDocxProducesImages(t *testing.T) {
	_, fs := workspace(t)
	kit := provider(t)
	if kit.Engine() == nil {
		t.Skip("本机没有 LibreOfficeKit 引擎")
	}
	result, err := exec(t, toolFor(t, fs, kit, ToolRender), `{"input":"probe.docx","dpi":96}`)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(result.Images) != 1 {
		t.Fatalf("应返回 1 张图，得到 %d", len(result.Images))
	}
	if result.Images[0].MIME != "image/png" || len(result.Images[0].DataBase64) < 100 {
		t.Fatalf("图片结果不对：%+v", result.Images[0].MIME)
	}
	var value struct {
		Operation string
		PageCount int
		DPI       int
		Images    []struct {
			Page, Width, Height, Bytes int
			AttachmentID, MediaType    string
		}
		MissingFonts []string
	}
	raw, _ := json.Marshal(result.Data)
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("结果结构：%v", err)
	}
	if value.Operation != "render" || value.PageCount != 1 || value.DPI != 96 || len(value.Images) != 1 {
		t.Fatalf("结果不对：%+v", value)
	}
	image := value.Images[0]
	if image.Page != 1 || image.Width != 816 || image.Height != 1056 || image.MediaType != "image/png" || image.AttachmentID == "" {
		t.Fatalf("图片元数据不对：%+v", image)
	}
	if !strings.Contains(result.Text, "Rendered 1 image of probe.docx (1 in the document) at 96 DPI.") {
		t.Fatalf("呈现文本不对：%q", result.Text)
	}
	t.Logf("office_render 真实图片：%dx%d，%d 字节 PNG", image.Width, image.Height, image.Bytes)
}

// 没有引擎时三个工具都必须是结构化失败 + 可执行 Hint（绝不假成功）。
func TestUnavailableEngineIsActionable(t *testing.T) {
	_, fs := workspace(t)
	empty := t.TempDir()
	kit := officekit.Open(officekit.Options{Home: t.TempDir(), EnginePath: filepath.Join(empty, "absent"), SearchRoots: []string{empty}})
	t.Cleanup(kit.Close)
	for _, testCase := range []struct {
		tool *Tool
		args string
	}{{
		toolFor(t, fs, kit, ToolConvert), `{"input":"probe.docx","format":"pdf"}`,
	}, {
		toolFor(t, fs, kit, ToolRecalculate), `{"input":"book.xlsx","format":"xlsx"}`,
	}, {
		toolFor(t, fs, kit, ToolRender), `{"input":"probe.docx"}`,
	}} {
		result, err := exec(t, testCase.tool, testCase.args)
		if err == nil {
			t.Fatalf("%s 没有引擎却成功了", testCase.tool.Name())
		}
		if result.Hint == "" || !strings.Contains(result.Hint, "不可用") {
			t.Fatalf("%s 的 Hint 不可执行：%q", testCase.tool.Name(), result.Hint)
		}
		if !strings.Contains(err.Error(), "LibreOfficeKit") {
			t.Fatalf("%s 的错误没说清缺什么：%v", testCase.tool.Name(), err)
		}
	}
}

// Module 必须在缺 tools/fs 时明确失败，不许静默不注册。
func TestModuleRequires(t *testing.T) {
	module := Module(provider(t))
	if module.ID != "tool-office" || module.Backend == "" {
		t.Fatalf("module 元信息不对：%+v", module)
	}
	if strings.Join(module.Requires, ",") != "tools,fs" {
		t.Fatalf("Requires 不对：%v", module.Requires)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
