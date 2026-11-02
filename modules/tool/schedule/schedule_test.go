package schedule

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"dshgo/internal/spi"
)

func mk(dir, kind string) *Tool { return &Tool{st: &store{dir: dir}, kind: kind} }

func ctxWith(id string) context.Context { return spi.WithSession(context.Background(), id) }

// 只有一条定时任务时，update/delete 可以省 id。
//
// 依据：一个会话通常只挂一两个任务，让模型抄一遍刚建出来的 id 只是多一次被拒的机会。
func TestUpdateAndDeleteDefaultToSingleSchedule(t *testing.T) {
	dir := t.TempDir()
	ctx := ctxWith("acp-a")
	if _, err := mk(dir, kindCreate).Exec(ctx, json.RawMessage(`{"title":"体检","prompt":"跑 doctor","kind":"every","every_seconds":300}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := mk(dir, kindUpdate).Exec(ctx, json.RawMessage(`{"title":"新名字"}`)); err != nil {
		t.Fatalf("只有一条时 update 应当能省 id: %v", err)
	}
	if _, err := mk(dir, kindDelete).Exec(ctx, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("只有一条时 delete 应当能省 id: %v", err)
	}
	if items, _ := (&store{dir: dir}).load("acp-a"); len(items) != 0 {
		t.Fatalf("删完应当为空，剩 %d 条", len(items))
	}
}

// 有多条时必须自己选：猜错就改/删了别人的任务。
func TestUpdateRequiresIDWhenMultiple(t *testing.T) {
	dir := t.TempDir()
	ctx := ctxWith("acp-b")
	for _, title := range []string{"一号", "二号"} {
		if _, err := mk(dir, kindCreate).Exec(ctx, json.RawMessage(`{"title":"`+title+`","prompt":"x","kind":"every","every_seconds":300}`)); err != nil {
			t.Fatal(err)
		}
	}
	res, err := mk(dir, kindUpdate).Exec(ctx, json.RawMessage(`{"title":"改"} `))
	if err == nil {
		t.Fatal("多条时必须要求 id")
	}
	if !strings.Contains(res.Hint, "必须填一个") {
		t.Fatalf("Hint 要说清要选一个: %q", res.Hint)
	}
}

// 一条都没有时，提示去建，而不是报"找不到 id"。
func TestDeleteWithNoSchedules(t *testing.T) {
	res, err := mk(t.TempDir(), kindDelete).Exec(ctxWith("acp-c"), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("没有任务时应当报错")
	}
	if !strings.Contains(res.Hint, "schedule_create") {
		t.Fatalf("Hint 应当指向 create: %q", res.Hint)
	}
}
