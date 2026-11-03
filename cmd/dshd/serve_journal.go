package main

import (
	"encoding/json"
	"time"

	"dshgo/internal/transport"
)

// 这一层是「发号 + 推送」的唯一入口（H14）。
//
// 以前 seq 有三个来源：事件帧用 v4 `ev.Seq`、结局帧用全局 `serveTurnSeq`、
// 客户端按收到的顺序自己数。三者混用之后，客户端"发现断号"的判断是假的。
//
// 现在统一成：**每会话一条 Transport journal**，任何要推给界面的帧都先过
// `emit`，由 journal 发号并记账；WS 推送与 `session.history` 读的是同一份。

// emitFrame 是三个 runner 共用的发号+推送入口。
//
// journals 为 nil 时退化成"只推不记账"（老测试不关心序号也不用改）。
func emitFrame(journals *transport.Journals, pub func(topic string, f transport.ServerFrame),
	sessionID, typ string, data json.RawMessage) transport.ServerFrame {
	t := time.Now().UnixMilli()
	f := transport.ServerFrame{Type: typ, Data: data, Time: t}
	if journals != nil {
		// 发号与入账在 journal 内部同一把锁里完成，所以这里拿到的 seq
		// 就是 history 将来会还给我的那个 seq——两边不会错位。
		f = journals.Get(sessionID).Append(typ, data, t)
	}
	if pub != nil {
		pub("session:"+sessionID, f)
	}
	return f
}

// emitTurnFrame 推一轮的结局（turn/done 或 turn/error）。
//
// 它现在也进 journal：否则客户端补洞时拿到的事件流里没有"这一轮结束了"，
// 会一直停在"正在思考"——这正是在 H2 那轮踩过的坑。
func emitTurnFrame(journals *transport.Journals, pub func(topic string, f transport.ServerFrame),
	sessionID, turnID string, err error) {
	typ := TurnDone
	var payload any = turnDoneData{TurnID: turnID}
	if err != nil {
		typ = TurnError
		payload = turnErrorData{TurnID: turnID, Error: err.Error()}
	}
	data, mErr := json.Marshal(payload)
	if mErr != nil {
		// 负载全是字符串，实际不可能失败；真失败了帧本身也比什么都不发强。
		data = nil
	}
	emitFrame(journals, pub, sessionID, typ, data)
}

// emit 是 loopRunner 上的便捷写法。
func (r *loopRunner) emit(sessionID, typ string, data json.RawMessage) transport.ServerFrame {
	return emitFrame(r.journals, r.pub, sessionID, typ, data)
}

// emitTurn 是 loopRunner 上的便捷写法。
func (r *loopRunner) emitTurn(sessionID, turnID string, err error) {
	emitTurnFrame(r.journals, r.pub, sessionID, turnID, err)
}
