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

// 默认只读：任务到点时不能动写类工具。
// 提示词是模型写的、执行时人不在场、授权在登记时给——不留这道门等于放行一切。
func TestAllowWritesDefaultsOff(t *testing.T) {
	dir := t.TempDir()
	ctx := ctxWith("acp-r")
	res, err := mk(dir, kindCreate).Exec(ctx, json.RawMessage(`{"title":"巡检","prompt":"看看磁盘","kind":"every","every_seconds":300}`))
	if err != nil {
		t.Fatal(err)
	}
	m, _ := res.Data.(map[string]any)
	sch, ok := m["schedule"].(Schedule)
	if !ok {
		t.Fatalf("拿不到任务: %#v", res.Data)
	}
	if sch.AllowWrites {
		t.Fatal("没显式 opt-in 时必须默认只读")
	}
}

// 显式打开才允许写，且要落盘（重启后仍然生效）。
func TestAllowWritesPersists(t *testing.T) {
	dir := t.TempDir()
	ctx := ctxWith("acp-w")
	if _, err := mk(dir, kindCreate).Exec(ctx, json.RawMessage(`{"title":"清理","prompt":"删临时文件","kind":"every","every_seconds":300,"allow_writes":true}`)); err != nil {
		t.Fatal(err)
	}
	items, err := (&store{dir: dir}).load("acp-w")
	if err != nil || len(items) != 1 {
		t.Fatalf("读回来: %v %v", items, err)
	}
	if !items[0].AllowWrites {
		t.Fatal("allow_writes=true 应当落盘")
	}
}

// 只有 schedule_list 能并发：它只读本会话的 JSON 文件。
// create/update/delete 会写**同一份**文件（读-改-写），并发就会互相覆盖。
//
// 官方 rc.2（`dsh-tool-schedule/src/index.ts:406,426`）里 list/view 是
// `isConcurrencySafe: () => true`，三个写工具**没有**声明。
func TestScheduleListIsConcurrencySafeWritesAreNot(t *testing.T) {
	dir := t.TempDir()
	list := mk(dir, kindList)
	if !spi.ConcurrencySafe(list, json.RawMessage(`{}`)) {
		t.Fatal("schedule_list 必须声明并发安全（官方 isConcurrencySafe: () => true）")
	}
	if list.Caps().NeedsApproval() {
		t.Fatal("schedule_list 只读，不该要审批——要审批就走不了并发车道")
	}
	for _, kind := range []string{kindCreate, kindUpdate, kindDelete} {
		if spi.ConcurrencySafe(mk(dir, kind), json.RawMessage(`{}`)) {
			t.Fatalf("%s 会读-改-写同一份文件，必须独占", kind)
		}
	}
}

// 没有会话上下文 ⇒ 官方 `ToolContextUnavailableError` / `TOOL_CONTEXT_UNAVAILABLE`。
//
// 这不是"参数错了"：模型改参数也没用，必须让接线补上会话。身份里说清这一点，
// 模型才不会一遍遍重试（§4 官方 `lib/types/contracts.js:42-56`）。
func TestScheduleWithoutSessionCarriesToolContextUnavailable(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct{ kind, args string }{
		{kindCreate, `{"title":"t","prompt":"p","kind":"every","every_seconds":300}`},
		{kindList, `{}`},
		{kindUpdate, `{"title":"改"}`},
		{kindDelete, `{"id":"sch-0001-abcd"}`},
	} {
		res, err := mk(dir, tc.kind).Exec(context.Background(), json.RawMessage(tc.args))
		if err == nil {
			t.Fatalf("%s 没有会话上下文时必须报错，不能假装成功", tc.kind)
		}
		if res.Hint == "" {
			t.Fatalf("%s 失败时必须给 Hint（R3）", tc.kind)
		}
		if res.Info == nil {
			t.Fatalf("%s 的失败结果没有错误身份（官方 error.info）: %+v", tc.kind, res)
		}
		if res.Info.Name != spi.ErrorNameToolContextUnavailable ||
			res.Info.Code != spi.CodeToolContextUnavailable {
			t.Fatalf("%s 的身份 = %+v，期望 ToolContextUnavailableError/TOOL_CONTEXT_UNAVAILABLE",
				tc.kind, *res.Info)
		}
	}
}
