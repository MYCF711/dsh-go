package session

import "dshgo/codecs/v4"

// NextTurn 算出**会话级**下一个 turn 编号：这份 v4 日志里已经开过几轮，下一轮就是它 +1。
//
// # 为什么判据是"数 turn/start"而不是进程内计数器
//
// serve 的每一轮都是一个新进程（`dshd run --session <id>`，见
// `cmd/dshd/serve_tools.go` 的 childArgs）。进程内计数每轮都从 1 开始，于是同一个
// 会话里出现第二个 `turn:1`——真实帧：
//
//	turn/start {"turn":1}   turn/end {"turn":1}   turn/done {"turnId":"turn-1"}
//	turn/start {"turn":1}   turn/end {"turn":1}   turn/done {"turnId":"turn-2"}
//
// 而 `docs/transport-contract.md`「消息身份」把数值 `turn` 与 `turn/done.turnId`、
// `tool/call.callId` 并列为**既有身份**，文档示例里也有 `"turn":2`。恒为 1 意味着
// 第二轮起所有 turn 混在一起：界面 Turn 大纲与消息分组按它分组，官方
// subagentTiming 也按它把 `turn/start` 与 `turn/end` 配对（`started[turn]`），
// 重复编号会让后一轮的结局配到前一轮的开头。
//
// # 为什么真值必须落在 v4 日志上
//
// 轮次编号要跨进程、跨宿主重启、跨断线重连都对。append-only 的 v4 日志是唯一
// 三个都满足的落点：`load` / `session.history` 本来就是从它投影出来的，
// 重启后重新读一遍文件就能恢复。所以 serve 的进程内 runner
// （`cmd/dshd/serve_turn.go`）、子进程 `cmdRun`、ACP 的 resume 三条路
// 都调这一个函数，不再各数一份。
//
// # 为什么是"已开轮数 + 1"而不是"最大编号 + 1"
//
// 修好之前的历史日志每轮都写 `turn:1`。取"最大编号 + 1"会让下一轮拿到 2——
// 那没和别的编号撞，但也把它自己限定成"只能比历史最大值多一"；取"已开轮数 + 1"
// 则让 5 个 `turn:1` 之后的下一个是 6：编号仍然单调、仍然不重复，
// 而且一眼能看出这份日志里到底跑过几轮。
//
// 读不出日志（nil）退到 1：编号错总比"写不进去"好——调用方在写入处拿到的 error
// 才是这一轮真正失败的原因，不要用编号问题把它盖住。
func NextTurn(back *v4.Log) int {
	if back == nil {
		return 1
	}
	n := 0
	for _, ev := range back.Events {
		if ev.Type == "turn/start" {
			n++
		}
	}
	return n + 1
}

// NextTurnAt 读路径上的 v4 日志再算下一个会话级 turn。
//
// 给手里只有路径、还没解析日志的调用方用（ACP 把旧会话拿出来续写时就是这样）。
// 已经有 `*v4.Log` 的调用方请直接用 NextTurn：长会话的日志可能几十 MB，
// 再读一遍纯属浪费。
func NextTurnAt(path string) int {
	back, err := v4.ReadFile(path)
	if err != nil {
		return 1
	}
	return NextTurn(back)
}
