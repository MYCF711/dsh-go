package transport

import (
	"errors"
	"time"
)

// Pinger 是可选能力：能发 WS 控制帧 ping 的连接实现它。
//
// 为什么是可选接口而不是塞进 Conn：内存管道那类实现不需要心跳，
// 逼它们实现一个空方法，测试会看起来"验过了心跳"而其实什么也没发生。
type Pinger interface {
	Ping() error
}

// 默认节奏：每 30 秒 ping 一次；90 秒没有任何客户端帧就判定连接已死。
//
// 为什么要有上限：TCP 连接在进程被挂起、网络被拔掉时**不会报错**，
// 读会一直阻塞下去。界面那头看到的是"没有新消息"，看起来像模型不干活。
const (
	defaultPingEvery = 30 * time.Second
	defaultIdleLimit = 90 * time.Second
)

// 测试会改这两个值把节奏压到毫秒级。生产走上面的常量。
var (
	pingEvery = defaultPingEvery
	idleLimit = defaultIdleLimit
)

// ErrConnIdle 表示连接长时间没有任何客户端帧，判定已死。
var ErrConnIdle = errors.New("transport: 连接超时（长时间没有任何帧）")

// activityTracker 是可选的"最近一次读到对端帧"的时间源。
//
// 为什么必须有它：心跳启动时的 `last` 只是个**起点**，不是"读侧空闲时长"。
// 不把每条客户端帧（含 WS 控制帧的回 pong）算成活动，90 秒一到就会把一条
// 正在正常收帧的连接判死——这比"不判死"更糟。
type activityTracker interface {
	LastClientActivity() time.Time
}

// heartbeat 只在 conn 实现了 Pinger 时启动；否则返回的 stop 是空操作。
//
// 判定依据是**读侧空闲**，不是写侧：ping 发得出去不代表对端还活着
// （半开连接上写会成功很多次）。真正能证明活着的只有对端发来帧。
//
// 空闲时长取自 conn 的 activityTracker（真 WS 连接实现了它）；连接没提供
// 这个能力时退化成"从心跳启动那一刻起算"，只适合根本不发帧的对端
// （测试夹具），别拿它当真实连接的口径。
func heartbeat(conn Conn, pingEvery, idleLimit time.Duration, onIdle func()) (stop func()) {
	p, ok := conn.(Pinger)
	if !ok {
		return func() {}
	}
	tracker, _ := conn.(activityTracker)
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(pingEvery)
		defer t.Stop()
		last := time.Now()
		if tracker != nil {
			if at := tracker.LastClientActivity(); !at.IsZero() {
				last = at
			}
		}
		for {
			select {
			case <-done:
				return
			case <-t.C:
				_ = p.Ping() // 发不出去不代表死，读侧空闲才是判据
				if tracker != nil {
					// ping 的回包（pong）在 ReadClientFrame 里被消费掉，
					// 所以只能从连接自己的时间戳读，不能在这个循环里数帧。
					if at := tracker.LastClientActivity(); at.After(last) {
						last = at
					}
				}
				if idleLimit > 0 && time.Since(last) > idleLimit {
					if onIdle != nil {
						onIdle()
					}
					return
				}
			}
		}
	}()
	return func() { close(done) }
}
