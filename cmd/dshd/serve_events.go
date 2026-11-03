package main

import (
	"encoding/json"
	"sync/atomic"

	"dshgo/internal/transport"
)

// turnEvent 是一轮结束时推给界面的事件类型（WS 的 type 字段）。
//
// 只有两种，因为它们只回答一个问题：「这一轮结束了没有、成没成」。
// 中间过程（assistant/tool/usage 那些）不在这里造，那是会话日志的事。
const (
	TurnDone  = "turn/done"
	TurnError = "turn/error"
)

//
// 为什么不复用会话日志的 seq：日志 seq 是**持久事实**（落进 v4 文件、
// 参与 surface 与遮蔽的排序），而这里的 seq 只是这条连接上的送达顺序，
// 一次 serve 重启就该从 1 重来。两者一旦混用，界面就会拿不连续的日志
// seq 去补历史，补出来的是别的东西。

// turnDoneData 是 turn/done 的负载。字段名是界面契约，别改。
type turnDoneData struct {
	TurnID string `json:"turnId"`
}

// turnErrorData 是 turn/error 的负载。
//
// Error 是**错误原文**：不做归纳、不换成人话。界面要能把它原样显示给用户，
// 我们这一层一旦开始"翻译"，用户看到的就是二手信息，
// 而真正的形状（哪一步、哪个文件、哪个状态码）恰恰是最难复现的部分。
type turnErrorData struct {
	TurnID string `json:"turnId"`
	Error  string `json:"error"`
}

// publishTurn 把一轮的结局推给订阅了这个会话的连接。
//
// 为什么必须推：prompt 是"立刻返回 200"的，界面拿到 200 之后**唯一的消息来源就是这里**。
// 不推 = 界面永远停在"正在思考"。
//
// seq：用宿主自己的自增序列（从 1 开始），不与会话日志的 seq 混用——
// 日志 seq 是持久事实，这里的 seq 只是这条连接上的送达顺序。
//
// pub 为 nil（测试）或 topic 为空（不知道推给谁）时静默返回：
// 这两种情况都不是"这一轮失败了"，不该伪造一条事件。
// serveTurnSeq 是结局帧的序号源。
//
// 必须和事件流同一条线：以前它从 1 开始，跑第二轮时 turn/done 的 seq
// 比客户端水位小，被 sinceSeq 过滤掉——界面永远等不到"这一轮结束了"。
// followProgress 把 follow 走到了哪里交回来。只在 fw.Wait() 之后读，所以不需要锁。
type followProgress struct {
	offset  int64
	lastSeq int64
	path    string
}

var serveTurnSeq atomic.Int64

func publishTurn(pub func(topic string, f transport.ServerFrame), topic, turnID string, err error, seq int) {
	if pub == nil || topic == "" {
		return
	}

	typ := TurnDone
	var payload any = turnDoneData{TurnID: turnID}
	if err != nil {
		typ = TurnError
		payload = turnErrorData{TurnID: turnID, Error: err.Error()}
	}

	// 负载全是字符串字段，Marshal 实际上不可能失败；但真失败了也不能沉默——
	// 帧本身（type + seq）已经足够告诉界面"这一轮结束了"，那比什么都不发好。
	data, mErr := json.Marshal(payload)
	if mErr != nil {
		data = nil
	}

	// 直接构造而不是走 NewServerFrame：它会对 seq<=0 / type 为空 / data 非法报错，
	// 而这三样在这里都不可能发生（seq 由自增保证 ≥1，type 是常量，data 来自 Marshal）。
	pub(topic, transport.ServerFrame{
		Seq:  seq,
		Type: typ,
		Data: data,
	})
}
