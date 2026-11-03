package transport

import (
	"io"
	"sync"
	"testing"
	"time"
)

// pipeConn 用两个 channel 模拟一条连接。
type pipeConn struct {
	in       chan ClientFrame
	out      chan ServerFrame
	outClose chan struct{}
	once     sync.Once
}

func newPipe() *pipeConn {
	return &pipeConn{in: make(chan ClientFrame, 8), out: make(chan ServerFrame, 8), outClose: make(chan struct{})}
}

func (p *pipeConn) ReadClientFrame() (ClientFrame, error) {
	f, ok := <-p.in
	if !ok {
		return ClientFrame{}, io.EOF
	}
	return f, nil
}

func (p *pipeConn) WriteServerFrame(f ServerFrame) error {
	select {
	case p.out <- f:
		return nil
	case <-p.outClose:
		return io.ErrClosedPipe
	}
}

func (p *pipeConn) closeIn() { close(p.in) }

func (p *pipeConn) next(t *testing.T) ServerFrame {
	t.Helper()
	select {
	case f := <-p.out:
		return f
	case <-time.After(2 * time.Second):
		t.Fatal("等不到服务端帧")
		return ServerFrame{}
	}
}

// sub 之后，该 topic 的事件要真的推过来。
func TestServeConnDeliversAfterSub(t *testing.T) {
	hub := NewHub()
	p := newPipe()
	done := make(chan error, 1)
	go func() { done <- ServeConn(p, hub) }()

	p.in <- ClientFrame{T: "sub", Topic: "session:a", SinceSeq: 0}
	// 等订阅生效：反复发，直到收到一帧
	deadline := time.Now().Add(2 * time.Second)
	sent := 1
	for {
		hub.Publish("session:a", ServerFrame{Seq: sent, Type: "x"})
		select {
		case f := <-p.out:
			if f.Seq != sent {
				t.Fatalf("收到 seq=%d，期望 %d", f.Seq, sent)
			}
			p.closeIn()
			if err := <-done; err != nil {
				t.Fatalf("正常关闭应当返回 nil，拿到 %v", err)
			}
			return
		case <-time.After(20 * time.Millisecond):
			if time.Now().After(deadline) {
				t.Fatal("订阅之后一直收不到帧")
			}
			sent++
		}
	}
}

// 断连之后 Hub 里不许还留着这个订阅者——否则 Publish 会一直往死连接里写。
func TestServeConnUnsubscribesOnClose(t *testing.T) {
	hub := NewHub()
	p := newPipe()
	done := make(chan error, 1)
	go func() { done <- ServeConn(p, hub) }()

	p.in <- ClientFrame{T: "sub", Topic: "session:a"}
	time.Sleep(50 * time.Millisecond)
	p.closeIn()
	<-done

	// 摘干净了的话，这里不该 panic 也不会写进 p.out
	hub.Publish("session:a", ServerFrame{Seq: 1, Type: "x"})
	select {
	case f := <-p.out:
		t.Fatalf("连接都关了还收到 %+v", f)
	case <-time.After(50 * time.Millisecond):
	}
}

// 坏帧不许当 ping 放过：第一帧坏了，连接立刻结束。
func TestServeConnClosesOnBadFrame(t *testing.T) {
	hub := NewHub()
	p := newPipe()
	done := make(chan error, 1)
	go func() { done <- ServeConn(p, hub) }()
	p.in <- ClientFrame{T: "subscribe"} // 未知类型
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("坏帧应当返回错误")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("坏帧之后连接没结束")
	}
}
