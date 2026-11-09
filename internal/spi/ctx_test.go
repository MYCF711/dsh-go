package spi

import (
	"context"
	"encoding/json"
	"testing"
)

// 工具靠 ctx 知道"我是哪个会话"——这条链路断了，信箱、目标这类工具只能拒绝。
func TestSessionRoundTrip(t *testing.T) {
	ctx := WithSession(context.Background(), "acp-123")
	if got := SessionFrom(ctx); got != "acp-123" {
		t.Fatalf("SessionFrom=%q，期望 acp-123", got)
	}
}

func TestSessionAbsentIsEmpty(t *testing.T) {
	if got := SessionFrom(context.Background()); got != "" {
		t.Fatalf("没绑会话时应当返回空串，得到 %q", got)
	}
	// nil ctx 也不许 panic：工具在异常路径下照样会被调用。
	//lint:ignore SA1012 这里就是要在 nil ctx 上验"不 panic"，传 context.TODO 就测不到
	if got := SessionFrom(nil); got != "" {
		t.Fatalf("nil ctx 应当返回空串，得到 %q", got)
	}
}

func TestEmptySessionIgnored(t *testing.T) {
	ctx := WithSession(context.Background(), "")
	if got := SessionFrom(ctx); got != "" {
		t.Fatalf("空 id 不该被绑上，得到 %q", got)
	}
}

// run id 与 session id 各存各的，别互相覆盖。
func TestRunIDSeparate(t *testing.T) {
	ctx := WithRunID(WithSession(context.Background(), "s1"), "r1")
	if SessionFrom(ctx) != "s1" || RunIDFrom(ctx) != "r1" {
		t.Fatalf("session=%q run=%q", SessionFrom(ctx), RunIDFrom(ctx))
	}
}

// fakeRunner 只用来证明 SubDispatch 里的 Runner 没被换成别的东西（按身份比）。
type fakeRunner struct{ id string }

func (f *fakeRunner) Run(context.Context, string, json.RawMessage) (Result, error) {
	return Result{}, nil
}

// 同一 ctx 上 run id 与子调用通道**必须各存各的**。
//
// 两者曾经共用 `ctxKey(1)`：谁后绑谁把对方覆盖掉。生产形状是"宿主每轮先放
// SubDispatch、Loop 再放 RunID"（也有一条反过来），两种顺序都得对：
//   - 覆盖掉 run id ⇒ repeat-tool-reminder 的链跨轮累计（新的一轮不再提醒）；
//   - 覆盖掉 SubDispatch ⇒ 嵌套调用的 Depth 归零（子调用被当成顶层，提醒不该提醒的，
//     workflow 也拿不到 runner）。
func TestRunIDAndSubDispatchCoexist(t *testing.T) {
	runnerA := &fakeRunner{id: "A"}
	runnerB := &fakeRunner{id: "B"}

	t.Run("runid-then-subdispatch", func(t *testing.T) {
		ctx := WithSession(context.Background(), "sess-1")
		ctx = WithRunID(ctx, "run-1")
		ctx = WithSubDispatch(ctx, SubDispatch{Runner: runnerA, Depth: 2})

		if got := RunIDFrom(ctx); got != "run-1" {
			t.Fatalf("绑了子调用通道之后 run id 丢了：RunIDFrom=%q（期望 run-1）", got)
		}
		sub := SubDispatchFrom(ctx)
		if sub.Runner != Runner(runnerA) {
			t.Fatalf("SubDispatch.Runner 被换掉了：%#v", sub.Runner)
		}
		if sub.Depth != 2 {
			t.Fatalf("SubDispatch.Depth 丢了：%d（期望 2）", sub.Depth)
		}
		if got := SessionFrom(ctx); got != "sess-1" {
			t.Fatalf("会话 id 丢了：%q", got)
		}
	})

	t.Run("subdispatch-then-runid", func(t *testing.T) {
		ctx := WithSession(context.Background(), "sess-2")
		ctx = WithSubDispatch(ctx, SubDispatch{Runner: runnerB, Depth: 1})
		ctx = WithRunID(ctx, "run-2")

		sub := SubDispatchFrom(ctx)
		if sub.Runner != Runner(runnerB) || sub.Depth != 1 {
			t.Fatalf("绑了 run id 之后子调用通道丢了：%#v", sub)
		}
		if got := RunIDFrom(ctx); got != "run-2" {
			t.Fatalf("run id 丢了：%q", got)
		}
		if got := SessionFrom(ctx); got != "sess-2" {
			t.Fatalf("会话 id 丢了：%q", got)
		}
	})
}

// 没绑子调用通道时是零值（Runner=nil），不 panic、不凭空造一个 Runner。
func TestSubDispatchAbsentIsZero(t *testing.T) {
	sub := SubDispatchFrom(WithRunID(context.Background(), "r"))
	if sub.Runner != nil || sub.Depth != 0 {
		t.Fatalf("没绑时应当是零值，得到 %#v", sub)
	}
	//lint:ignore SA1012 这里就是要在 nil ctx 上验"不 panic"
	if sub := SubDispatchFrom(nil); sub.Runner != nil {
		t.Fatalf("nil ctx 应当返回零值，得到 %#v", sub)
	}
	// 空 SubDispatch（Runner=nil）不算绑定：原样返回，不往 ctx 里塞空值。
	ctx := context.Background()
	if got := WithSubDispatch(ctx, SubDispatch{Depth: 3}); got != ctx {
		t.Fatal("Runner=nil 的 SubDispatch 不该被绑上（那会让 Depth>0 却没有通道）")
	}
	if got := WithRunID(ctx, ""); got != ctx {
		t.Fatal("空 run id 不该被绑上")
	}
}
