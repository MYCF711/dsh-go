//go:build windows

package terminal

// ConPTY 子进程的**环境块**：`CreateProcess(CREATE_UNICODE_ENVIRONMENT)` 要的是
// 一段 UTF-16、双 NUL 结尾、按变量名不区分大小写排序的 `KEY=VALUE\0…\0\0` 内存块
// （不是 `[]string`，也不是 `cmd.Env`）。
//
// 为什么这条路径要单独存在：官方 agent 终端给子 shell 的是一份**算好的**环境
// （`dsh-api-terminal-controller/lib/index.js:1456-1461`：
// `{...request.env, DSH_SESSION_ID: agent.id, ...HISTFILE}`），其中
// `DSH_SESSION_ID` 是模型侧识别"我在哪个会话里"的唯一凭据。宿主此前一律继承父
// 进程环境（`lpEnvironment=NULL`），表达不了"这份环境是我算的"，也表达不了
// "把某个变量设为空"（confined POSIX 的 `HISTFILE=""` 正是空值而不是未设）。

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf16"
)

// buildEnvironmentBlock 把 `KEY=VALUE` 列表打成 CreateProcess 认的环境块。
//
// 行为约定（与 Windows 自己的规则、以及 Go `os/exec` 的 last-wins 一致）：
//   - 变量名**不区分大小写**；同名（忽略大小写）以后出现的为准；
//   - 输出按变量名（大写归一后）排序：Windows 的 `CreateProcess` 文档要求块内
//     按字母序，乱序的块在部分 conhost/shell 组合下会丢变量；
//   - 每项以 NUL 结尾，整块再加一个 NUL 收尾（`GetEnvironmentStrings` 的形状）；
//   - 非法项（没有 `=`、名字为空、含 NUL）**响亮失败**：静默跳过会让"环境里少了
//     一个变量"变成一条永远查不到的谜案。
func buildEnvironmentBlock(env []string) ([]uint16, error) {
	index := make(map[string]int, len(env))
	entries := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || name == "" {
			return nil, fmt.Errorf("环境项 %q 不是 KEY=VALUE", kv)
		}
		if strings.ContainsRune(kv, 0) {
			return nil, fmt.Errorf("环境项 %q 含 NUL", kv)
		}
		key := strings.ToUpper(name)
		if at, seen := index[key]; seen {
			entries[at] = kv
			continue
		}
		index[key] = len(entries)
		entries = append(entries, kv)
	}
	sort.Slice(entries, func(i, j int) bool {
		return strings.ToUpper(entries[i]) < strings.ToUpper(entries[j])
	})
	block := make([]uint16, 0, len(entries)*16+1)
	for _, kv := range entries {
		block = append(block, utf16.Encode([]rune(kv))...)
		block = append(block, 0)
	}
	block = append(block, 0)
	return block, nil
}
