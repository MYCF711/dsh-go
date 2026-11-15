// Package versiontag 读"版本号唯一出处"：`cmd/dshd/main.go` 的 `version` 变量。
//
// 为什么需要它：仓规是「版本号只有一个出处（`cmd/dshd/main.go`）」，而需要这个值的地方
// 不止一处，且它们都不能自己再抄一份：
//
//   - **防复发守卫**（`cmd/dshd/version_guard_test.go`）：它要拿"当前版本"当针，
//     去全仓找第二份硬编码；针必须从唯一出处取，否则守卫自己就成了第二个源。
//   - **库的测试装配**（`internal/pluginmgr`、`internal/pluginmgr/official` 的 `TestMain`）：
//     测试进程不走 `main()`，所以它们要像真实宿主一样把版本注入进去，
//     而注入的值必须是**当前的**版本，不能是测试里写死的一份。
//
// 生产代码**不调用**它：真实二进制由 `cmd/dshd` 在启动时把 `main.version` 注入下游
// （见 `cmd/dshd/host_version.go`）。本包只做"从唯一出处读出来"这一件事。
package versiontag

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// versionLine 匹配 `main.go` 里 `version = "…"` 的**定义行**（行首缩进后可选的
// `version` 标识符 + 等号 + 字符串字面量）。用 `(?m)^` 钉住行首，避免匹配到
// `if version == …` 这类比较。
var versionLine = regexp.MustCompile(`(?m)^[ \t]*version[ \t]*=[ \t]*"([^"]+)"`)

// FromMainSource 从 `cmd/dshd/main.go` 读出当前版本号。
//
// 取**第一个**匹配：main.go 顶部的 `var ( version = … )` 块就在那几个常量里，
// 且没有任何比较表达式能命中这个形态（`case "version"`、`if version ==` 都不匹配）。
func FromMainSource(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("versiontag: 读不到版本唯一出处：%w", err)
	}
	match := versionLine.FindSubmatch(raw)
	if match == nil {
		return "", fmt.Errorf("versiontag: %s 里找不到 `version = \"…\"` 定义行", path)
	}
	if version := strings.TrimSpace(string(match[1])); version != "" {
		return version, nil
	}
	return "", fmt.Errorf("versiontag: %s 的 version 定义是空的", path)
}

// ReleasePrefix 返回"发布版本前缀"——版本号里最后一个点之前的部分加上那个点。
//
// 例：`<major>.<minor>.<patch>-go.<n>` → 去掉最后一段，得到 `<major>.<minor>.<patch>-go.`；
// 测试版 `<major>.<minor>.<patch>-gobeta.<n>` 同理得到 `…-gobeta.`。
// 守卫用它当针：任何**别的**文件里出现这个前缀，就说明有人把发布版本抄了第二份
// （2026-11-09 的实际事故形态）。
func ReleasePrefix(version string) string {
	if index := strings.LastIndex(version, "."); index >= 0 {
		return version[:index+1]
	}
	return version
}
