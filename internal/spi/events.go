package spi

// EventSink 是"把一条**耐久**会话事件写进当前会话日志"的通道。
//
// 为什么这是 SPI 而不是让模块自己开文件：
//   - 一份 v4 会话日志同一时刻只能有一个写入者（session.Log 持写锁）。工具自己
//     再开一个写入端会跟主循环抢锁，写出来是两段互不相认的 seq；
//   - "这次调用属于哪个会话"只有宿主知道（循环在每轮入口把会话身份绑到 ctx 上），
//     模块拿不到，也不该去猜。
//
// 形状与官方 `tool-workflow` 往 session 里 append 的那一层一致（
// `dsh-tool-workflow/src/index.ts:93-151` 的 `createWorkflowRecorder`）：
// **类型 + 数据，只增不改**。失败必须返回给调用方——静默丢掉一条事件，
// 界面上就是少画一个节点，而"少画"比"画错"更难发现。
type EventSink interface {
	// AppendEvent 追加一条事件并持久化；返回错误表示这一条**没有**落盘。
	AppendEvent(eventType string, data any) error
}

// eventSinkFunc 让普通函数满足 EventSink（测试与薄适配层用）。
type eventSinkFunc func(string, any) error

func (f eventSinkFunc) AppendEvent(t string, d any) error { return f(t, d) }

// EventSinkFunc 把一个函数包成 EventSink。
func EventSinkFunc(f func(string, any) error) EventSink { return eventSinkFunc(f) }
