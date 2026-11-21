package transport

import (
	"bufio"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// 这一组用例钉的是「服务端心跳真的跑在 /ws 连接上」这条链：
//
//	WSConn.Ping()        —— 真的发 WS ping 控制帧
//	WSConn.LastClientActivity() —— 对端任何一帧（含 pong）都算读侧活动
//	heartbeat()          —— 按连接自己报的活动时间判空闲，而不是只从启动那刻起算
//
// 修之前：WSConn 没有 Ping（heartbeat 对真连接是空操作）⇒ 半开连接永远不判死；
// 而且 heartbeat 的 `last` 从不刷新 ⇒ 就算接上也会把正在收帧的连接 90 秒后杀掉。

// TestWSConnPingSendsControlFrame 钉 ping 的线上形状：FIN=1、opcode=0x9、
// 服务端帧不掩码、空正文。
func TestWSConnPingSendsControlFrame(t *testing.T) {
	srv, cli := net.Pipe()
	defer srv.Close()
	defer cli.Close()

	c := &WSConn{conn: srv, br: bufio.NewReader(srv)}
	writeErr := make(chan error, 1)
	go func() { writeErr <- c.Ping() }()

	hdr := make([]byte, 2)
	if _, err := io.ReadFull(cli, hdr); err != nil {
		t.Fatalf("读 ping 头部失败: %v", err)
	}
	if hdr[0] != 0x89 {
		t.Fatalf("ping 头字节要 0x89（FIN=1 + opcode 0x9），拿到 0x%02x", hdr[0])
	}
	if hdr[1] != 0x00 {
		t.Fatalf("ping 要空正文且服务端不掩码（第二字节 0x00），拿到 0x%02x", hdr[1])
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("Ping 返回错误: %v", err)
	}
}

// TestWSConnActivityCountsPongFrames 钉「控制帧也算活动」。
//
// 为什么必须钉：对端对服务端 ping 的回应就是 pong，而 pong 在
// ReadClientFrame 里被就地消费、不会返回给 ServeConn。若活动时间只在
// 文本帧上有记录，一个只会回 pong 的合法客户端会被判成空闲。
func TestWSConnActivityCountsPongFrames(t *testing.T) {
	srv, cli := net.Pipe()
	defer srv.Close()
	defer cli.Close()

	c := &WSConn{conn: srv, br: bufio.NewReader(srv)}
	// 装成一个"一小时没读过任何帧"的连接，这样只要读到的 pong 刷新了时间戳，
	// 两个时间之间就有可观测的差值。
	c.lastRead.Store(time.Now().Add(-time.Hour).UnixNano())
	before := c.LastClientActivity()

	// 客户端帧必须掩码：0x8A = FIN + pong，0x80 = 掩码 + 长度 0。
	go func() { _, _ = cli.Write([]byte{0x8A, 0x80, 0x01, 0x02, 0x03, 0x04}) }()

	if _, err := c.readFrame(); err != nil {
		t.Fatalf("读 pong 帧失败: %v", err)
	}
	after := c.LastClientActivity()
	if !after.After(before) {
		t.Fatalf("收到 pong 后活动时间必须前进：before=%v after=%v", before, after)
	}
	if time.Since(after) > 5*time.Second {
		t.Fatalf("活动时间被写成了过去/未来：%v", after)
	}
}

// trackedConn 是会自己报"最近活动时间"的测试连接（真 WS 连接就是这样）。
type trackedConn struct {
	idleConn
	last atomic.Int64
}

func (c *trackedConn) touch() { c.last.Store(time.Now().UnixNano()) }

func (c *trackedConn) LastClientActivity() time.Time {
	n := c.last.Load()
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}

// TestHeartbeatKeepsActiveConnectionAndReapsSilentOne 钉 heartbeat 的判据：
// 只要有读侧活动就不判死；活动停下之后必须很快判死（否则就是连接泄漏）。
func TestHeartbeatKeepsActiveConnectionAndReapsSilentOne(t *testing.T) {
	oldPing, oldIdle := pingEvery, idleLimit
	pingEvery, idleLimit = 10*time.Millisecond, 40*time.Millisecond
	defer func() { pingEvery, idleLimit = oldPing, oldIdle }()

	c := &trackedConn{idleConn: idleConn{closed: make(chan struct{})}}
	c.touch()
	idleFired := make(chan struct{}, 1)
	stop := heartbeat(c, 10*time.Millisecond, 40*time.Millisecond, func() {
		select {
		case idleFired <- struct{}{}:
		default:
		}
	})
	defer stop()

	// 持续给活动：150ms ≈ 3.75 个 idleLimit，旧口径（last 不刷新）早就判死了。
	deadline := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(deadline) {
		c.touch()
		select {
		case <-idleFired:
			t.Fatal("连接一直在活动，却被判成空闲")
		case <-time.After(5 * time.Millisecond):
		}
	}

	// 停止活动：必须在一个 idleLimit 多一点之内判死。
	select {
	case <-idleFired:
	case <-time.After(2 * time.Second):
		t.Fatal("对端停止活动后没有判死——半开连接会一直被当成活的")
	}
}

// TestServeConnHeartbeatsRealWSConn 是端到端证据：真 WSConn 上
// 服务端会主动发 ping；对端回 pong 时连接保持；对端不再回应时必须被回收。
func TestServeConnHeartbeatsRealWSConn(t *testing.T) {
	oldPing, oldIdle := pingEvery, idleLimit
	pingEvery, idleLimit = 10*time.Millisecond, 60*time.Millisecond
	defer func() { pingEvery, idleLimit = oldPing, oldIdle }()

	srv, cli := net.Pipe()
	c := &WSConn{conn: srv, br: bufio.NewReader(srv)}
	c.lastRead.Store(time.Now().UnixNano())

	sawPing := make(chan struct{}, 1)
	answer := atomic.Bool{}
	answer.Store(true)
	peerDone := make(chan struct{})
	go func() {
		defer close(peerDone)
		defer cli.Close()
		br := bufio.NewReader(cli)
		for {
			hdr := make([]byte, 2)
			if _, err := io.ReadFull(br, hdr); err != nil {
				return
			}
			n := int(hdr[1] & 0x7f)
			if n > 0 {
				if _, err := io.ReadFull(br, make([]byte, n)); err != nil {
					return
				}
			}
			switch hdr[0] & 0x0f {
			case wsOpPing:
				select {
				case sawPing <- struct{}{}:
				default:
				}
				if answer.Load() {
					// 掩码 + 空正文的 pong。
					if _, err := cli.Write([]byte{0x8A, 0x80, 0x01, 0x02, 0x03, 0x04}); err != nil {
						return
					}
				}
			case wsOpClose:
				return
			}
		}
	}()

	done := make(chan error, 1)
	go func() { done <- ServeConn(c, NewHub()) }()

	// ① 服务端必须主动发 ping：旧实现（WSConn 没有 Ping）这里一条都收不到。
	select {
	case <-sawPing:
	case <-time.After(2 * time.Second):
		t.Fatal("服务端没有发过 WS ping —— heartbeat 对真连接仍然是空操作")
	}

	// ② 对端一直回 pong 时不许判空闲：120ms ≈ 2 个 idleLimit。
	select {
	case err := <-done:
		t.Fatalf("对端一直回 pong，连接不该被判空闲：%v", err)
	case <-time.After(120 * time.Millisecond):
	}

	// ③ 对端不再回 pong（socket 仍然开着）：必须被回收，而不是永久挂着。
	answer.Store(false)
	select {
	case err := <-done:
		if !errors.Is(err, ErrConnIdle) {
			t.Fatalf("要 ErrConnIdle，拿到 %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("对端不再回应 ping 后连接没有回收——这就是半开连接的泄漏")
	}
	<-peerDone
}
