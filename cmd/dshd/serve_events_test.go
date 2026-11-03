package main

import (
	"errors"
	"strings"
	"testing"

	"dshgo/internal/transport"
)

// 失败必须推出去：prompt 已经回 200 了，这是界面唯一的消息来源。
func TestPublishTurnError(t *testing.T) {
	var got []transport.ServerFrame
	pub := func(topic string, f transport.ServerFrame) { got = append(got, f) }
	publishTurn(pub, "session:a", "turn-1", errors.New("模型连不上"))
	if len(got) != 1 {
		t.Fatalf("该推一帧，推了 %d", len(got))
	}
	if got[0].Type != TurnError {
		t.Fatalf("type 要 %q，拿到 %q", TurnError, got[0].Type)
	}
	// 错误原文照抄，不许换成人话
	if s := string(got[0].Data); !strings.Contains(s, "模型连不上") {
		t.Fatalf("错误原文被改了: %s", s)
	}
	if got[0].Seq <= 0 {
		t.Fatalf("seq 必须 > 0，拿到 %d", got[0].Seq)
	}
}

func TestPublishTurnDone(t *testing.T) {
	var got []transport.ServerFrame
	publishTurn(func(_ string, f transport.ServerFrame) { got = append(got, f) }, "session:a", "turn-2", nil)
	if len(got) != 1 || got[0].Type != TurnDone {
		t.Fatalf("拿到 %+v", got)
	}
}

// nil pub / 空 topic 静默返回，不许 panic——serve 关掉时 pub 可能已经被置空。
func TestPublishTurnQuiet(t *testing.T) {
	publishTurn(nil, "session:a", "t", errors.New("x"))
	publishTurn(func(string, transport.ServerFrame) { t.Fatal("空 topic 不该推") }, "", "t", errors.New("x"))
}
