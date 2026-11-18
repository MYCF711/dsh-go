package main

import (
	"fmt"

	"dshgo/internal/transport"
)

// serveTerminalResizeHook 是 `terminal.resize`（HTTP 回落投影）的生产接线。
//
// 为什么要单独抽成函数而不是在 serve.go 里内联：serve.go 那份 Options 装配在超大
// 函数里，测试拿不到。测试自己写一份闭包、生产再写一份，就是"测试绿、生产没接"
// 的经典来源（本仓已经踩过）。这里 serve 与测试共用同一个函数——测的是生产接线本身。
//
// 官方 uplink 的 resize 不在 HTTP 面（原文见 handlers_job_terminal_official.go:1300，
// 官方客户端走 L2 mux 的 attach 帧机 `{type:'resize',sequence,cols,rows}`）。所以这条
// 投影是 additive 的：它只补 HTTP 这一面，**不构成 mux 上行已完成的证据**。
func serveTerminalResizeHook(jt *officialPTYJobTerminalService) func(sessionID, id string, cols, rows int) error {
	return func(sessionID, id string, cols, rows int) error {
		if jt == nil || jt.officialPTYTerminalAPI == nil {
			return fmt.Errorf("%w: 官方用户终端控制面未接线（PTY 后端为空）", transport.ErrJTOfficialUnsupported)
		}
		if err := checkOfficialTerminalDimensions(cols, rows); err != nil {
			return err
		}
		return jt.officialPTYTerminalAPI.Resize(sessionID, id, cols, rows)
	}
}

// checkOfficialTerminalDimensions 按**官方那一条** dimensions schema 校验尺寸：
// cols∈[2,maxCols]、rows∈[1,maxRows]，越界**抛**而不是夹
// （resize 一处 `dsh-api-terminal-controller/lib/index.js:622-633`，
// 同一 schema 的 agent/create 一处 `:1541`）。
//
// 常量不另抄一份字面量：2/1 是 terminal_pty_agent.go 的 officialAgentMinCols/
// officialAgentMinRows，500/200 是 serve_terminal_api.go 的 officialTerminalMaxCols/
// officialTerminalMaxRows，它们本来就从官方 config（`lib/index.js:1114-1115`）来。
// 名字里的 Agent 是历史来源（agent 终端先用了这条 schema）——值就是同一条官方
// schema，不为 resize 再造第二份档位（自造档位的后果是"官方接受我们不接受"）。
func checkOfficialTerminalDimensions(cols, rows int) error {
	if cols < officialAgentMinCols || cols > officialTerminalMaxCols ||
		rows < officialAgentMinRows || rows > officialTerminalMaxRows {
		return fmt.Errorf(
			"%w: 尺寸越界：cols∈[%d,%d]、rows∈[%d,%d]（收到 %d×%d）——官方这里报错不是截断（lib/index.js:622-633）",
			transport.ErrJTOfficialBadParam,
			officialAgentMinCols, officialTerminalMaxCols,
			officialAgentMinRows, officialTerminalMaxRows, cols, rows)
	}
	return nil
}
