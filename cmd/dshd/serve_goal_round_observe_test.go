package main

// T08：自动续轮失败必须**可观察**（T06 F4 的第二个半场）。
//
// 旧行为：`continueRounds` 里 `r.next.Run` 失败只写一行 stderr 然后 disarm。
// 对用户来说就是"目标自己不跑了"——journal 没有帧、API 没有错误、界面空白。
// 这里钉住修复后的行为：失败的那一轮补一帧 `turn/error`，且仍要 disarm
// （失败不空转的语义不变）。

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"dshgo/internal/transport"
)

// t08SetGoalCap 给 fixture 的目标钉一个真实上限。
//
// 方法名带 t08 前缀：避免与共享树另一份（未合入 HEAD 的）自动续轮测试夹具
// 里的同名 helper 冲突——两份最终会同时进集成树，重名会直接编译红。
func t08SetGoalCap(t *testing.T, f *goalRoundFixture, sessionID string, cap int) {
	t.Helper()
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	if f.store.loadErr != nil {
		t.Fatalf("读 goal store: %v", f.store.loadErr)
	}
	record := f.store.m[sessionID]
	record.Goal.MaxGoalRounds = cap
	next := cloneGoalMap(f.store.m)
	next[sessionID] = record
	if err := writeGoalDocument(f.store.path, goalDocument{Version: goalStoreVersion, Goals: next}); err != nil {
		t.Fatalf("写 goal store: %v", err)
	}
	f.store.m = next
}

// TestGoalRoundAutoRoundFailureEmitsTurnErrorFrame：
// 注入自动轮失败后，除了停+撤资格，必须有一帧可观察的 turn/error。
func TestGoalRoundAutoRoundFailureEmitsTurnErrorFrame(t *testing.T) {
	home := t.TempDir()
	const sid = "session-goal-frame"
	createGoalRoundSession(t, home, sid)
	f := newGoalRoundFixture(t, home)
	if err := f.store.Set(sid, "失败必须可观察"); err != nil {
		t.Fatalf("设 goal: %v", err)
	}
	t08SetGoalCap(t, f, sid, 100)

	const injected = "注入：自动轮 query 失败"
	calls := 0
	firstTurnID := ""
	runner := turnRunnerFunc(func(_ context.Context, sessionID, turnID, text string) error {
		calls++
		if calls == 1 {
			firstTurnID = turnID
			return writeGoalRoundTurn(home, sessionID, text, "completed")
		}
		return errors.New(injected)
	})
	p := newPromptService(f.driver.wrap(runner))
	if _, err := p.Submit(context.Background(), sid, "开始"); err != nil {
		t.Fatalf("提交: %v", err)
	}
	waitGoalRoundIdle(t, p, sid)

	if calls != 2 {
		t.Fatalf("runner 调用=%d，want 2（失败那一轮后必须停）", calls)
	}

	var failures []transport.ServerFrame
	for _, frame := range f.pushed {
		if frame.Type == TurnError {
			failures = append(failures, frame)
		}
	}
	if len(failures) != 1 {
		t.Fatalf("自动轮失败必须产生唯一一帧 turn/error（不得只写 stderr），实际 %d 帧：%+v",
			len(failures), f.pushed)
	}
	var payload struct {
		TurnID string `json:"turnId"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(failures[0].Data, &payload); err != nil {
		t.Fatalf("解 turn/error: %v（data=%s）", err, failures[0].Data)
	}
	wantTurn := goalRoundTurnID(firstTurnID, 2)
	if payload.TurnID != wantTurn {
		t.Fatalf("turn/error.turnId=%q，want %q（失败的是自动第 2 轮）", payload.TurnID, wantTurn)
	}
	if !strings.Contains(payload.Error, injected) {
		t.Fatalf("turn/error.error 必须带真实失败原因：%q", payload.Error)
	}

	// 失败不空转的语义必须保持：只跑了两轮，第二轮失败不算进 roundsStarted。
	if got := f.count(t, sid); got != 1 {
		t.Fatalf("roundsStarted=%d，want 1（失败那一轮不算）", got)
	}
}
