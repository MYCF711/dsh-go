// Package office 是官方 rc.2 的三个 Office 模型工具：office_convert / office_recalculate /
// office_render。
//
// 模型面契约逐字取自官方 tool-catalog.md（office_convert 段、office_recalculate 段、
// office_render 段）与 packages/document/tool-office/src/index.ts:84-118：
//
//	office_convert      {input, format(enum 9), sheet?}        required: input, format
//	office_recalculate  {input, format(enum 2)}                required: input, format
//	office_render       {input, pages?[], sheet?, range?, dpi?} required: input
//
// 参数之外还有一层 schema 表达不了的规则（官方 formats.ts）：输入族决定可转格式、
// 工作表不接受 pages、range 必须带 sheet、dpi 必须 24..600、未知参数键要明确报错
// 并指出它属于哪个工具。这些规则全部在这里实现，不留给引擎去报错。
package office

import (
	"fmt"
	"strings"

	"dshgo/internal/officekit"
)

// 文档族（官方 formats.ts:12 OfficeFamily）。
const (
	FamilyText         = "text"
	FamilySpreadsheet  = "spreadsheet"
	FamilyPresentation = "presentation"
	FamilyPDF          = "pdf"
)

var families = map[string]string{
	"doc": FamilyText, "docx": FamilyText, "odt": FamilyText,
	"xls": FamilySpreadsheet, "xlsx": FamilySpreadsheet, "ods": FamilySpreadsheet,
	"ppt": FamilyPresentation, "pptx": FamilyPresentation, "odp": FamilyPresentation,
	"pdf": FamilyPDF,
}

// conversionOutputs 是「输入族 → 可转格式」（官方 formats.ts:22-26）。
var conversionOutputs = map[string][]string{
	FamilyText:         {"pdf", "docx", "odt", "txt"},
	FamilySpreadsheet:  {"pdf", "xlsx", "ods", "csv"},
	FamilyPresentation: {"pdf", "pptx", "odp"},
}

// OutputFormats 是 office_convert 的 format 枚举（官方 formats.ts:29，顺序一致）。
var OutputFormats = []string{"pdf", "docx", "odt", "txt", "xlsx", "ods", "csv", "pptx", "odp"}

// RecalculationFormats 是 office_recalculate 的 format 枚举（官方 formats.ts:32）。
var RecalculationFormats = []string{"xlsx", "ods"}

// 工具名（官方 index.ts:84）。
const (
	ToolRender      = "office_render"
	ToolConvert     = "office_convert"
	ToolRecalculate = "office_recalculate"
)

// extensionOf 取小写扩展名（官方 formats.ts:82-86：同时认 / 与 \）。
func extensionOf(path string) string {
	name := path
	if idx := strings.LastIndexAny(name, `/\`); idx >= 0 {
		name = name[idx+1:]
	}
	dot := strings.LastIndex(name, ".")
	if dot <= 0 || dot == len(name)-1 {
		return ""
	}
	return strings.ToLower(name[dot+1:])
}

// inputFamily 选输入族（官方 formats.ts:95-107）。
func inputFamily(operation, path string) (string, error) {
	extension := extensionOf(path)
	family, known := families[extension]
	if operation == ToolRecalculate {
		if !known || family != FamilySpreadsheet {
			return "", fmt.Errorf("cannot recalculate %q: recalculate accepts xls, xlsx, or ods workbooks", path)
		}
		return family, nil
	}
	if !known || (family == FamilyPDF && operation == ToolConvert) {
		accepted := "doc, docx, odt, xls, xlsx, ods, ppt, pptx, or odp"
		if operation == ToolRender {
			accepted = "doc, docx, odt, xls, xlsx, ods, ppt, pptx, odp, or pdf"
		}
		return "", fmt.Errorf("cannot %s %q: %s accepts %s files", verbOf(operation), path, verbOf(operation), accepted)
	}
	return family, nil
}

func verbOf(operation string) string {
	switch operation {
	case ToolRender:
		return "render"
	case ToolConvert:
		return "convert"
	default:
		return "recalculate"
	}
}

// checkParameterNames 拒绝工具没声明的参数键，并指出它属于哪个工具
// （官方 formats.ts:67-75：开放参数对象会静默忽略多余键，必须显式拒）。
func checkParameterNames(operation string, args map[string]any, declared map[string][]string) error {
	for key := range args {
		allowed := false
		for _, name := range declared[operation] {
			if name == key {
				allowed = true
				break
			}
		}
		if allowed {
			continue
		}
		var owners []string
		for other, names := range declared {
			for _, name := range names {
				if name == key {
					owners = append(owners, other)
					break
				}
			}
		}
		if len(owners) == 0 {
			return fmt.Errorf("%s does not accept %s", operation, key)
		}
		return fmt.Errorf("%s applies only to %s", key, strings.Join(owners, " or "))
	}
	return nil
}

// checkRenderArguments 校验 schema 表达不了的渲染选择（官方 formats.ts:115-129）。
func checkRenderArguments(pages []int64, sheet, cellRange string, dpi *int64, family string) error {
	calc := family == FamilySpreadsheet
	if pages != nil {
		if calc {
			return fmt.Errorf("pages does not apply to spreadsheets; select sheet and range instead")
		}
		if len(pages) == 0 {
			return fmt.Errorf("pages must list distinct one-based page numbers")
		}
		seen := map[int64]bool{}
		for _, page := range pages {
			if page < 1 || seen[page] {
				return fmt.Errorf("pages must list distinct one-based page numbers")
			}
			seen[page] = true
		}
	}
	if !calc && sheet != "" {
		return fmt.Errorf("sheet applies only to spreadsheets")
	}
	if cellRange != "" && sheet == "" {
		return fmt.Errorf("range requires sheet")
	}
	if strings.TrimSpace(sheet) == "" && sheet != "" {
		return fmt.Errorf("sheet must be an exact worksheet name")
	}
	if dpi != nil && (*dpi < 24 || *dpi > 600) {
		return fmt.Errorf("dpi must be an integer from 24 to 600")
	}
	return nil
}

// checkConversionArguments 校验格式与区域选择（官方 formats.ts:137-144）。
func checkConversionArguments(path, format, sheet, family string) error {
	accepted := conversionOutputs[family]
	ok := false
	for _, candidate := range accepted {
		if candidate == format {
			ok = true
		}
	}
	if !ok {
		return fmt.Errorf("cannot convert %q to %s: this file converts to %s", path, format, strings.Join(accepted, ", "))
	}
	if sheet != "" && format != "csv" {
		return fmt.Errorf("sheet applies only to CSV conversion")
	}
	if sheet != "" && strings.TrimSpace(sheet) == "" {
		return fmt.Errorf("sheet must be an exact worksheet name")
	}
	return nil
}

// outputExtension 归一化产物扩展名（与 format 同名，官方 index.ts:401）。
func outputExtension(format string) string { return format }

// formatFamilies 把 officekit 的错误码翻成模型可执行的下一句话
// （官方 index.ts:290-297 officeFailure）。
func officeHint(err error) string {
	code := string(officekit.CodeOf(err))
	switch code {
	case string(officekit.CodeUnavailable):
		return "告诉用户这个操作不可用；如果原因可能是暂时的就稍后重试。不要改用系统 soffice、npx 或别的转换器。"
	case string(officekit.CodeBusy):
		return "等其它文档操作结束后重试。"
	case string(officekit.CodeOutputTooLarge):
		return "减少页数、缩小 range 或降低 dpi。"
	case string(officekit.CodeInvalidRequest), string(officekit.CodeInvalidDocument):
		return "按上面的说明修正参数后重试。"
	case string(officekit.CodeTimeout):
		return "文件可能太大或引擎卡住；缩小范围后重试。"
	default:
		return "把错误原文告诉用户；不要伪造转换结果。"
	}
}
