package session

import (
	"testing"

	"dshgo/codecs/v4"
)

// NextTurn 是数值 `turn` 的唯一判据（会话级递增）。这里把它钉成四条形状：
// 空日志从 1 起、每开一轮 +1、非轮次事件不参与计数、历史遗留的重复编号不会
// 让新编号撞车。
func TestNextTurnCountsTurnStartsOnly(t *testing.T) {
	turnStart := func(seq int64) v4.Event {
		return v4.Event{Type: "turn/start", Seq: seq, Data: []byte(`{"turn":1}`)}
	}
	other := func(typ string, seq int64) v4.Event {
		return v4.Event{Type: typ, Seq: seq}
	}
	cases := []struct {
		name string
		back *v4.Log
		want int
	}{
		{"读不出日志", nil, 1},
		{"空日志", &v4.Log{}, 1},
		{"只有非轮次事件", &v4.Log{Events: []v4.Event{
			other("system/message", 0), other("user/message", 1), other("turn/end", 2),
		}}, 1},
		{"开过一轮", &v4.Log{Events: []v4.Event{turnStart(0), other("turn/end", 1)}}, 2},
		{"开过三轮", &v4.Log{Events: []v4.Event{
			turnStart(0), other("turn/end", 1),
			turnStart(2), other("turn/end", 3),
			turnStart(4), other("turn/end", 5),
		}}, 4},
		// 修好之前的历史日志每轮都写 turn:1：新编号必须仍然唯一且单调，
		// 不能因为"最大编号是 1"就让下一轮又是 2（更不能再是 1）。
		// 五轮都写 turn:1 的老日志，下一个编号是 6。
		{"历史遗留的重复编号", &v4.Log{Events: []v4.Event{
			turnStart(0), other("turn/end", 1),
			turnStart(2), other("turn/end", 3),
			turnStart(4), other("turn/end", 5),
			turnStart(6), other("turn/end", 7),
			turnStart(8), other("turn/end", 9),
		}}, 6},
		// 悬挂的一轮（子进程崩溃，turn/end 由宿主补写之前）也占一个编号：
		// 新的一轮不能复用它的号。
		{"悬挂的轮次", &v4.Log{Events: []v4.Event{turnStart(0)}}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NextTurn(tc.back); got != tc.want {
				t.Fatalf("NextTurn = %d, want %d", got, tc.want)
			}
		})
	}
}

// NextTurnAt 读不出文件时退到 1（编号错好过写不进去），读得出时与 NextTurn 同值。
func TestNextTurnAtFallsBackToOneAndMatchesNextTurn(t *testing.T) {
	if got := NextTurnAt(t.TempDir() + "/missing.jsonl"); got != 1 {
		t.Fatalf("缺文件时 NextTurnAt = %d, want 1", got)
	}
	home := t.TempDir()
	lg, err := CreateSession(home, home, "session-turn-at", "default")
	if err != nil {
		t.Fatal(err)
	}
	path := lg.Path
	if err := lg.Append("turn/start", TurnStartData(1)); err != nil {
		t.Fatal(err)
	}
	if err := lg.Append("turn/end", TurnEndData(1, "completed", "")); err != nil {
		t.Fatal(err)
	}
	if err := lg.Append("turn/start", TurnStartData(2)); err != nil {
		t.Fatal(err)
	}
	if err := lg.Close(); err != nil {
		t.Fatal(err)
	}
	back, err := v4.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := NextTurn(back)
	if want != 3 {
		t.Fatalf("日志里已经开过两轮，NextTurn = %d, want 3", want)
	}
	if got := NextTurnAt(path); got != want {
		t.Fatalf("NextTurnAt = %d, NextTurn = %d，两条入口必须同值", got, want)
	}
}
