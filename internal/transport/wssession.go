package transport

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// Conn 是一条连接的读写面。WS 的真实实现以后补，测试用内存管道。
//
// 读那一面：ServeConn 是唯一的调用者，所以实现不必为并发读做准备。
// 写那一面：Hub 的推送路径和本文件的 pong 都会写，**串行由 ServeConn 的写锁负责**，
// 实现本身不必并发安全（真去写 socket 的实现最好自己再兜一层）。
type Conn interface {
	// ReadClientFrame 阻塞等下一帧；对端关闭时返回 io.EOF（或包着它的错误）。
	ReadClientFrame() (ClientFrame, error)
	// WriteServerFrame 写一帧；写失败表示连接已坏。
	WriteServerFrame(ServerFrame) error
}

// connSubscriber 把一条连接适配成 Hub 的 Subscriber。
//
// 写锁不是可选项：Hub.Publish 在锁外推送（见 hub.go 的锁纪律），
// 所以多个 Publish 可以并发进同一个 Push；而"写一帧"对一条连接来说不可交错——
// 两帧的字节交叠在客户端那边就是一段坏 JSON，且**看起来像网络故障**，极难查。
type connSubscriber struct {
	conn Conn

	mu sync.Mutex
}

// 钉住契约：连接订阅者就是 Hub 要的那种订阅者。
var _ Subscriber = (*connSubscriber)(nil)

// Push 把一帧写给客户端。返回 error 表示连接已坏，Hub 会把它从所有 topic 上摘掉。
func (s *connSubscriber) Push(frame ServerFrame) error {
	return s.write(frame)
}

// write 是这条连接**唯一**的写入口：Hub 推送与 pong 都走它，所以两者不会交错。
func (s *connSubscriber) write(frame ServerFrame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn.WriteServerFrame(frame)
}

// ServeConn 跑完一条连接，直到对端关闭或帧坏掉。
//
// 规矩（都是踩过的）：
//   - 第一帧必须是合法客户端帧；坏帧 → 立刻返回错误，别当 ping 放过
//   - sub：往 hub 订 topic；同一连接可以对同一 topic 重复 sub（以最后一次 sinceSeq 为准）
//   - unsub：退订；**退订后要真的收不到**
//   - ping：回一条 pong
//   - 连接结束（任何原因）→ 必须 hub.UnsubscribeAll(this)，否则 Hub 会一直往死连接里写
//
// 返回 nil 表示对端正常关闭。
func ServeConn(conn Conn, hub *Hub) error {
	if conn == nil {
		// 没有连接就没什么可读的；返回错误而不是在这里 nil 解引用。
		return errors.New("transport: ServeConn 收到 nil 连接")
	}
	if hub == nil {
		// 没给总线：订阅照收，但一条帧也发不出去（这条连接本身仍然能 ping/pong）。
		// 不 panic、也不假装"订上了会有人推"——Hub 那边的表只有这一条死记录，随连接结束消失。
		hub = NewHub()
	}

	sub := &connSubscriber{conn: conn}

	// 无论怎么退出（对端关闭、坏帧、写失败），都要把自己从 Hub 上摘干净。
	// 早退晚退都走 defer：留一条死连接在表里，之后每次 Publish 都要往它身上白费一次写，
	// 而且连接对象（连带它的缓冲）永远收不掉。
	defer hub.UnsubscribeAll(sub)

	// 心跳：只在连接实现了 Pinger 时启动（内存管道不需要）。
	// 读侧长时间没有帧 → 关掉连接把读解开，并返回 ErrConnIdle 而不是"use of closed connection"。
	var idle int32
	stop := heartbeat(conn, pingEvery, idleLimit, func() {
		atomic.StoreInt32(&idle, 1)
		if c, ok := conn.(io.Closer); ok {
			_ = c.Close()
		}
	})
	defer stop()

	first := true
	for {
		frame, err := conn.ReadClientFrame()
		if err != nil {
			if atomic.LoadInt32(&idle) == 1 {
				return ErrConnIdle
			}
			// 对端正常关闭：这条连接的活干完了，不算错误。
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}

		// 先验再动状态：坏帧必须在**任何**订阅变化之前被拦下，
		// 否则会出现"订阅改了、连接却已经该关了"的半截状态。
		if err := checkClientFrame(frame, first); err != nil {
			return err
		}
		first = false

		switch frame.T {
		case FrameSub:
			// H14：先核世代。客户端带着一个不认识的世代来订阅，说明中间的 journal
			// 已经换代（宿主重启 / 主动重建基线）。这时按 seq 继续推只会推错位的数据，
			// 所以先发一条 session/reset 让它丢弃水位——**它不能自己发现这件事**。
			if hub.journals != nil && frame.JournalEpoch != 0 {
				if j := hub.journals.Get(topicSession(frame.Topic)); j.Epoch() != frame.JournalEpoch {
					reset, _ := json.Marshal(map[string]any{
						"journalEpoch": j.Epoch(),
						"lastSeq":      j.LastSeq(),
					})
					// reset 不带 seq：它不是事件流里的一帧（与 pong 同理），
					// 给它编号反而会污染客户端的连续性判断。
					if err := sub.write(ServerFrame{Type: "session/reset", Data: reset}); err != nil {
						return err
					}
				}
			}
			// 重放还挂着的请求（H13）：断线期间冒出来的审批问题，客户端不重放就永远
			// 看不到——而子进程还在那边等着，用户眼里就是"卡住了"。
			if hub.pendingReplay != nil {
				for _, pf := range hub.pendingReplay(topicSession(frame.Topic)) {
					if err := sub.write(pf); err != nil {
						return err
					}
				}
			}
			// 重复 sub 由 Hub 处理：以最后一次 sinceSeq 为准，不会重复收帧。
			hub.Subscribe(frame.Topic, frame.SinceSeq, sub)

		case FrameUnsub:
			hub.Unsubscribe(frame.Topic, sub)

		case FramePing:
			// pong 直接构造结构体、不走 NewServerFrame：后者的第一道检查是 seq <= 0 → ErrBadSeq，
			// 理由是 seq 是客户端判连续性的唯一依据，不许造 0。
			// 但 pong 根本不是事件流里的一帧（没有 data、不进历史、不参与重放），
			// "没有序号"正是它该有的样子——给它编一个假序号反而会污染客户端的连续性判断。
			if err := sub.write(ServerFrame{Type: "pong"}); err != nil {
				// 写不出去＝连接已坏，没必要再等下一次读。
				return err
			}

		default:
			// checkClientFrame 已经拦过；留这一支只是为了让 switch 穷尽，
			// 免得以后加了帧类型忘了在这里处理，静默地什么也不做。
			return fmt.Errorf("%w: t=%q", ErrUnknownFrameType, frame.T)
		}
	}
}

// topicSession 从 "session:<id>" 里取会话 id；不是这个形状时原样返回，
// 反正 Get 只是拿一个 key（拿不到就是新建域，不影响正确性）。
func topicSession(topic string) string {
	const p = "session:"
	if len(topic) > len(p) && topic[:len(p)] == p {
		return topic[len(p):]
	}
	return topic
}

// checkClientFrame 拦下不该进状态机的帧。
//
// 正常路径上 ParseClientFrame 已经在字节层验过一遍了；但 Conn 的实现者未必走它
// （内存管道测试就会直接搓 ClientFrame），所以这里再验一次形状——
// 代价是一次 switch，而漏掉的代价是：一个 t 不认识的帧被当成 ping 放过之后，
// 客户端以为自己订阅成功了，然后一直等不到事件，**且两边都没有任何报错**。
func checkClientFrame(frame ClientFrame, first bool) error {
	// 第一帧单独提一句：它出错时客户端连"这条连接要怎么用"都还没说清，
	// 把这一点写进错误里，比笼统的"帧不合法"好查。
	where := "帧"
	if first {
		where = "第一帧"
	}

	switch frame.T {
	case FrameSub:
		if err := requireTopic(frame.Topic, frame.T); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
		// 与 ParseClientFrame 同一条规矩：只支持 "session:<非空 id>"。
		if err := requireSessionTopic(frame.Topic); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
		if frame.SinceSeq < 0 {
			return fmt.Errorf("%w: %s的 sinceSeq=%d 是负数；要从头开始就整个省略该字段",
				ErrBadSeq, where, frame.SinceSeq)
		}
		return nil

	case FrameUnsub:
		// unsub 只要求带 topic（不查前缀）：取消不掉比多挂一条更糟。
		if err := requireTopic(frame.Topic, frame.T); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
		return nil

	case FramePing:
		// ping 不改变任何状态，topic / sinceSeq 带了也忽略。
		return nil

	case "":
		return fmt.Errorf("%w: %s缺 t 字段；可选 %s / %s / %s",
			ErrUnknownFrameType, where, FrameSub, FrameUnsub, FramePing)

	default:
		return fmt.Errorf("%w: %s的 t=%q；可选 %s / %s / %s",
			ErrUnknownFrameType, where, frame.T, FrameSub, FrameUnsub, FramePing)
	}
}
