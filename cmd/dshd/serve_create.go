package main

import (
	"crypto/rand"
	"fmt"
	"os"

	"dshgo/codecs/v4"
	"dshgo/internal/session"
)

// serveID 生成会话 id：uuid 的形状，每一段都来自 crypto/rand。
//
// 为什么不能用时间戳：界面点两下就在同一毫秒里建两个会话，时间戳会撞；
// 而已经建过一个会话也挡不住重复（建会话自己不查重）。
// crypto/rand 失败是**真失败**（拿不到熵），必须报出来，不能退回时间戳。
func serveID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("生成会话 id 失败（crypto/rand）: %w", err)
	}
	// 8-4-4-4-12 = 16 字节。每段直接取原始字节的十六进制，不做版本位改写：
	// 这些 id 只用来当目录名，不参与任何 uuid 语义。
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// serveCreate 是 serve 里 session.create 的实现：真建一份 v4 会话。
//
// 三件事必须做对：
//  1. id 自己生成（crypto/rand 的 uuid 形状），不许用时间戳——同一秒建两个会撞
//  2. header 里写 agentPreset（为空就 "default"），否则界面显示的模式是空的
//  3. **建完要能立刻被读**：返回前把日志句柄关掉（Close 会刷盘），
//     否则紧接着的 session.load 可能读到空文件
//
// 返回的闭包签名正好是 transport.Options.Create 要的 func(mode string) (string, error)。
// cwd 为空时用进程当前目录（同样要 NormalizeCwd：\\?\ 前缀会写出真实 DSH
// 永远不会产生的 projectKey）。
func serveCreate(home, cwd string) (func(mode string) (string, error), error) {
	if home == "" {
		return nil, fmt.Errorf("建会话需要会话根目录，--home 是空的")
	}
	if cwd == "" {
		got, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("取当前目录失败: %w", err)
		}
		cwd = got
	}
	cwd = v4.NormalizeCwd(cwd)

	return func(mode string) (string, error) {
		// mode 就是 header.agentPreset。空值留给"没选"这一刻，落盘必须是具体模式，
		// 否则界面左栏的模式列是空白，resume 时也恢复不到任何东西。
		preset := mode
		if preset == "" {
			// 官方 defaultId = selectedDefault ?? default：设置里写下的默认 preset
			// 必须真的决定新会话的 header，否则那个开关就是只显示不生效。
			preset = selectedAgentPresetDefault(home)
		}
		id, err := serveID()
		if err != nil {
			return "", err
		}
		if err := serveCreateWithID(home, cwd, id, preset); err != nil {
			return "", err
		}
		return id, nil
	}, nil
}

// serveCreateWithID 用**给定 id** 落一份 v4 会话。
//
// 为什么要把"铸 id"与"落盘"拆开：官方 `sessionId` 有两种用法
// （`@deepseek-ai/dsh-api-session-controller/src/commands.ts:135-142` 与
// `.../src/agent.ts:229-233` / `:350`）：
//
//   - 这个名字下没有已存会话 → 用**这个名字本身**建新会话
//     （"the identity of a new Session when none is stored under it"）；
//   - 有已存会话但不能复用 → 另铸新 id，结果与请求的 id 不同
//     （"The created or reused Session, which may differ from the requested `sessionId`"）。
//
// 旧的 serveCreate 把两件事焊在闭包里，只能铸新 id。这里只负责落盘，
// preset 必须是**已解析好的具体值**：空串会让 header 的 preset 列留空，界面与
// resume 都恢复不到东西（与 serveCreate 闭包里的注释同一条理由）。
//
// 已存在同一路径时直接拒绝：v4.Create 是 O_TRUNC，写下去会把那份会话清空。
func serveCreateWithID(home, cwd, id, preset string) error {
	if id == "" {
		return fmt.Errorf("建会话需要 session id")
	}
	path, err := session.SessionPath(home, v4.NormalizeCwd(cwd), id)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		// 调用方保证"这个名字下没有已存会话"才该走到这里；真撞上了就是上层 bug，
		// 宁可响亮失败，也不拿 O_TRUNC 覆盖一份真会话。
		return fmt.Errorf("会话 %q 已存在于 %s，拒绝覆盖", id, path)
	}
	// 官方 `agent/created`（dsh-tool-subagent/src/model-selection-settings.ts:54-76）：
	// GUI 的 session.create 就是"新顶层会话"的唯一创建点，授权必须在第一次模型请求
	// 之前写进这份日志。设置先校验、再建会话：反过来会在设置非法（enabled 但清单为空）
	// 时留下一份没有授权记录的空会话。
	selection, err := subagentModelSelectionFromSettings(home)
	if err != nil {
		return err
	}
	lg, err := session.CreateSession(home, v4.NormalizeCwd(cwd), id, preset)
	if err != nil {
		return err
	}
	if perr := appendSubagentModelSelectionPolicyOnCreate(lg, selection); perr != nil {
		lg.Close()
		return perr
	}
	// 先关再返回：Close 会刷盘并放掉写锁。不关的话紧接着的 session.load
	// 可能读到还没落完的帧（表现为"刚建的会话是空的"）。
	return lg.Close()
}
