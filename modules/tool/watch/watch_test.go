package watch

import (
	"context"
	"encoding/json"
	"testing"
)

func mk(dir, kind string) *Tool { return &Tool{st: &store{dir: dir}, kind: kind} }

// 只盯了一个仓库时，ack 不填 repo 也要能用——让模型抄一遍刚登记过的名字，
// 只是多一次被拒的机会。
func TestAckDefaultsToSingleWatcher(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	if _, err := mk(dir, kindStart).Exec(ctx, json.RawMessage(`{"repo":"o/n"}`)); err != nil {
		t.Fatal(err)
	}
	res, err := mk(dir, kindAck).Exec(ctx, json.RawMessage(`{"numbers":[3,7]}`))
	if err != nil {
		t.Fatalf("只有一个仓库时应当能省略 repo: %v / %q", err, res.Hint)
	}
}

// 盯了多个就必须指定：猜错会把水位记到别人头上。
func TestAckRequiresRepoWhenMultiple(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	for _, r := range []string{"a/one", "b/two"} {
		if _, err := mk(dir, kindStart).Exec(ctx, json.RawMessage(`{"repo":"`+r+`"}`)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := mk(dir, kindAck).Exec(ctx, json.RawMessage(`{"numbers":[1]}`)); err == nil {
		t.Fatal("多个仓库时必须要求指定 repo")
	}
}

// 一个都没登记时，要说清"先登记"，而不是干巴巴的报错。
func TestAckWithoutAnyWatcher(t *testing.T) {
	res, err := mk(t.TempDir(), kindAck).Exec(context.Background(), json.RawMessage(`{"numbers":[1]}`))
	if err == nil {
		t.Fatal("没有仓库时应当报错")
	}
	if res.Hint == "" {
		t.Fatal("Hint 不能为空")
	}
}
