// Package officekit 用「随包预编译的原生 LibreOfficeKit 引擎」（dsoffice.exe）
// 做 Office 文档的真实 PDF 转换、格式转换、公式重算与页面光栅渲染。
//
// 为什么是外部原生进程而不是库：R6 禁止宿主嵌 JS 运行时，官方 rc.2 的引擎恰好是
// 一个独立可执行文件（官方 libreoffice-kit/lib/cli.js:866-905 spawnNative 就是起它），
// 所以宿主可以用同一套协议调用，同时满足「可杀 / 超时 / 取消 / 清理」。
//
// 官方对照（tmp/GO8-RC2/official-rc2/dsh/node_modules/@deepseek-ai/）：
//   - libreoffice-kit/lib/cli.js:754-775        nativeEnvironment（环境变量白名单）
//   - libreoffice-kit/lib/cli.js:776-806        prepareNativeEngine（Windows 短路径）
//   - libreoffice-kit/lib/cli.js:866-905        spawnNative + 工作进程参数
//   - libreoffice-kit/lib/cli.js:1656-1700      readEngine（prebuilds.json 校验）
//   - libreoffice-kit/lib/cli.js:1086-1100      --operation render-images 参数
//   - dsh-office-kit/src/index.ts:369-409       runOperation（临时目录/错误分类）
//   - dsh-office-kit/src/types.ts:157-165       OfficeKitErrorCode
package officekit

import "fmt"

// Code 是官方 OfficeKitErrorCode 的逐字移植（dsh-office-kit/src/types.ts:157-165）。
// 消费者（Transport / 工具）只按这些码分类，不解析引擎诊断文本。
type Code string

const (
	CodeInputTooLarge     Code = "input-too-large"
	CodeOutputTooLarge    Code = "output-too-large"
	CodeInvalidDocument   Code = "invalid-document"
	CodeUnsupportedFormat Code = "unsupported-format"
	CodeInvalidOutput     Code = "invalid-output"
	CodeTimeout           Code = "timeout"
	CodeUnavailable       Code = "unavailable"
	CodeFailed            Code = "failed"
	CodeBusy              Code = "busy"
	CodeSourceChanged     Code = "source-changed"
	CodeInvalidRequest    Code = "invalid-request"
)

// publishedCodes 是「能穿过进程边界」的码集合：引擎回报的未知码一律降级成 failed
// （官方 cli.js:47-58 failureCode 的同一判据）。
var publishedCodes = map[Code]bool{
	CodeInputTooLarge: true, CodeOutputTooLarge: true, CodeInvalidDocument: true,
	CodeUnsupportedFormat: true, CodeInvalidOutput: true, CodeTimeout: true,
	CodeUnavailable: true, CodeFailed: true, CodeBusy: true, CodeSourceChanged: true,
	CodeInvalidRequest: true,
}

// Error 是一次 Office 操作的结构化失败。
//
// 与官方 OfficeKitError 对齐：Code 是可供 UI 展示的分类，Message 是给人看的一句话；
// 引擎诊断放进 Detail（不当作契约）。
type Error struct {
	Code    Code
	Message string
	Detail  string
	// CapableWhen 只在 Code==unavailable 时给出：告诉调用方「装/配什么就能用」。
	// 这是我们比官方多给的一层（官方只有一句固定安装提示），因为本仓的引擎是
	// 外部目录，用户需要知道往哪儿指。
	CapableWhen string
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Detail != "" {
		return e.Message + "：" + e.Detail
	}
	return e.Message
}

// CodeOf 取结构化错误码；不是 *Error 时返回空串。
func CodeOf(err error) Code {
	if e, ok := err.(*Error); ok {
		return e.Code
	}
	return ""
}

// NormalizeCode 把任意字符串码收敛到已发布的集合；不认识的给 failed
// （官方 cli.js:53-56 failureCode）。
func NormalizeCode(code string) Code {
	c := Code(code)
	if publishedCodes[c] {
		return c
	}
	return CodeFailed
}

func errf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// unsatisfiedMessage 是「没有引擎」时的统一措辞：说清去哪儿配，而不是假装能渲染。
const unsatisfiedMessage = "本机没有可用的 LibreOfficeKit 原生引擎（dsoffice.exe）"
