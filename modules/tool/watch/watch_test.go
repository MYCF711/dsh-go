package watch

import (
	"context"
	"encoding/json"
	"strings"
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

// 水位跳过去 = 那些编号再也不会被当成新活。
// 一次 ack [15] 会静默吞掉 4..14，所以必须把跳过的算出来写在 Summary 里。
func TestAckReportsSkippedNumbers(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	if _, err := mk(dir, kindStart).Exec(ctx, json.RawMessage(`{"repo":"o/n"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := mk(dir, kindAck).Exec(ctx, json.RawMessage(`{"numbers":[3]}`)); err != nil {
		t.Fatal(err)
	}
	res, err := mk(dir, kindAck).Exec(ctx, json.RawMessage(`{"numbers":[15]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Summary, "跳过 11 个编号") {
		t.Fatalf("Summary 要说清跳过了多少: %q", res.Summary)
	}
	if !strings.Contains(res.Summary, "4-14") {
		t.Fatalf("连续编号要压成区间: %q", res.Summary)
	}
	if res.Hint == "" {
		t.Fatal("跳过时必须给一句怎么做（现在改还来得及）")
	}
	m, _ := res.Data.(map[string]any)
	if m["skipped_count"] != 11 {
		t.Fatalf("skipped_count = %v，期望 11", m["skipped_count"])
	}
}

// 连续 ack 不跳号时不该有噪音。
func TestAckWithoutGapHasNoSkipNote(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	if _, err := mk(dir, kindStart).Exec(ctx, json.RawMessage(`{"repo":"o/n"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := mk(dir, kindAck).Exec(ctx, json.RawMessage(`{"numbers":[1,2]}`)); err != nil {
		t.Fatal(err)
	}
	res, err := mk(dir, kindAck).Exec(ctx, json.RawMessage(`{"numbers":[3,4]}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Summary, "跳过") {
		t.Fatalf("没跳号却报了跳过: %q", res.Summary)
	}
}

func TestSpanText(t *testing.T) {
	cases := []struct {
		in   []int
		want string
	}{
		{[]int{4, 5, 6, 9}, "4-6、9"},
		{[]int{7}, "7"},
		{[]int{1, 2, 3}, "1-3"},
		{nil, ""},
	}
	for _, c := range cases {
		if got := spanText(c.in); got != c.want {
			t.Fatalf("spanText(%v) = %q，期望 %q", c.in, got, c.want)
		}
	}
}
