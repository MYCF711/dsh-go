package main

import (
	"fmt"
	"sync"

	"dshgo/internal/session"
	"dshgo/internal/spi"
)

// sessionEventSink 是 `spi.EventSink` 的生产实现：把工具写的**耐久**会话事件
// 落到当前会话的 v4 日志（append-only），由 serve 的 follow 路径原样镜像成
// Transport journal 帧，GUI 再从 journal 投影出节点。
//
// 为什么必须锁：v4 的 seq 分配与帧写入是"读-改-写"（`session.Log.Append` 先取
// `l.seq` 再自增），并发追加会写出两段互不相认的 seq。而 workflow 的扇出恰恰
// 是并发的——N 个子代理同时收尾，每个都要写自己的 agent-end。这把锁就是
// "同一会话只有一个写入者"那条约束在进程内的落点。
type sessionEventSink struct {
	mu  sync.Mutex
	log *session.Log
}

// newSessionEventSink 给一个会话写入端造 sink。log 为 nil 时每次写入都**报错**
// 而不是静默成功——"记录能力没接上"必须是可见的。
func newSessionEventSink(log *session.Log) *sessionEventSink {
	return &sessionEventSink{log: log}
}

// AppendEvent 追加一条事件并立即提交一帧。
func (s *sessionEventSink) AppendEvent(eventType string, data any) error {
	if s == nil || s.log == nil {
		return fmt.Errorf("这个入口没有会话日志写入端：%s 事件没有落盘", eventType)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.log.Append(eventType, data); err != nil {
		return fmt.Errorf("写 %s 事件失败（session=%s）: %w", eventType, s.log.Path, err)
	}
	return nil
}

// 编译期确认接口（两处入口都用这一个类型，别让签名漂掉）。
var _ spi.EventSink = (*sessionEventSink)(nil)
