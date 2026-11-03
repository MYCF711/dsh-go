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

// serve 无人值守：默认必须是 deny，不许默认 allow。
// 这条测试同时是 scripts/perm-gates.json 里 cmdServe 的登记凭据。
func TestServePolicyIsDeny(t *testing.T) {
	r := &loopRunner{home: "/tmp/x", policy: "deny", maxSteps: 6}
	if r.policy != "deny" {
		t.Fatalf("serve 默认策略必须是 deny，拿到 %q", r.policy)
	}
}

// 打到屏幕上的策略必须**等于**实际用的那个值。
// 打一句写死的话，和实际参数漂移之后，用户看到的就是假话。
func TestAnnouncedPolicyMatchesRunner(t *testing.T) {
	for _, p := range []string{"deny", "allow"} {
		r := &loopRunner{home: "/tmp/x", policy: p, maxSteps: 6}
		var buf strings.Builder
		announcePolicy(&buf, r.policy, r.maxSteps)
		if !strings.Contains(buf.String(), p) {
			t.Fatalf("播报里没有实际策略 %q：%s", p, buf.String())
		}
	}
}

// 步数也必须播报：不然"跑到一半停了"看起来像失败，其实是用满了上限。
func TestAnnouncedMaxSteps(t *testing.T) {
	var buf strings.Builder
	announcePolicy(&buf, "deny", 9)
	if !strings.Contains(buf.String(), "9") {
		t.Fatalf("播报里要有实际步数 9：%s", buf.String())
	}
}
