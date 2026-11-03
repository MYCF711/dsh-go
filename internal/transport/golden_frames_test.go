package transport

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// goldenFrames 读 testdata/ws-golden.jsonl：这是**跨仓共享的契约夹具**（H14）。
//
// 界面仓拿同一份文件写解析器，宿主拿它写形状断言——两边看到的必须是同一批帧。
// 改动这个文件等于改契约，必须两边一起改（docs/transport-contract.md §1）。
func goldenFrames(t *testing.T) []ServerFrame {
	t.Helper()
	f, err := os.Open("testdata/ws-golden.jsonl")
	if err != nil {
		t.Fatalf("打不开夹具：%v", err)
	}
	defer f.Close()

	var out []ServerFrame
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for line := 1; sc.Scan(); line++ {
		s := strings.TrimSpace(sc.Text())
		if s == "" || strings.HasPrefix(s, "//") {
			continue
		}
		var fr ServerFrame
		if err := json.Unmarshal([]byte(s), &fr); err != nil {
			t.Fatalf("第 %d 行不是合法帧：%v", line, err)
		}
		out = append(out, fr)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("读夹具失败：%v", err)
	}
	return out
}

// 白名单里的 type 才许出现（含 v4 原词与显式登记的 Transport 事件）。
var allowedTypes = map[string]bool{
	"turn/start": true, "turn/end": true, "turn/done": true, "turn/error": true,
	"user/message": true, "assistant/message": true, "tool/call": true, "tool/result": true,
	"system/message": true, "developer/message": true,
	"approval/request": true, "approval/timeout": true, "approval/cancel": true,
	"session/reset": true, "pong": true,
}

// H14：夹具里的每一帧都要符合契约。
//
// 三个硬约束：
//  1. type 在白名单里（界面靠它选渲染器，多一个词就是没约定的行为）
//  2. 带 seq 的帧 seq 连续递增；**pong / session/reset 必须没有 seq**
//  3. 每一帧都要有墙钟 time（H2：没有 time 就算不出速率）
func TestGoldenFramesMatchContract(t *testing.T) {
	frames := goldenFrames(t)
	if len(frames) < 10 {
		t.Fatalf("夹太薄了：%d 帧，至少要有 text/reasoning/tool/approval/reset", len(frames))
	}

	prev := 0
	var sawReasoning, sawTool, sawApproval, sawReset, sawPong bool
	for i, f := range frames {
		if !allowedTypes[f.Type] {
			t.Fatalf("第 %d 帧 type=%q 不在白名单里", i+1, f.Type)
		}
		if f.Type == "pong" || f.Type == "session/reset" {
			if f.Seq != 0 {
				t.Fatalf("%s 不该带 seq，实际 %d", f.Type, f.Seq)
			}
		} else {
			if f.Seq != prev+1 {
				t.Fatalf("第 %d 帧 seq 应接在 %d 之后，实际 %d", i+1, prev, f.Seq)
			}
			prev = f.Seq
		}
		if f.Time <= 0 {
			t.Fatalf("第 %d 帧（%s）没有 time", i+1, f.Type)
		}
		if len(f.Data) > 0 && !json.Valid(f.Data) {
			t.Fatalf("第 %d 帧的 data 不是合法 JSON", i+1)
		}
		switch {
		case strings.Contains(string(f.Data), `"reasoning"`):
			sawReasoning = true
		case strings.Contains(string(f.Data), `"tool-call"`) || f.Type == "tool/call":
			sawTool = true
		case f.Type == "approval/request":
			sawApproval = true
		case f.Type == "session/reset":
			sawReset = true
		case f.Type == "pong":
			sawPong = true
		}
	}

	// 缺任何一类，"夹具能驱动界面"这句话就不成立。
	for name, ok := range map[string]bool{
		"reasoning": sawReasoning, "tool": sawTool,
		"approval": sawApproval, "reset": sawReset, "pong": sawPong,
	} {
		if !ok {
			t.Fatalf("夹具缺 %s 样本：界面那一侧就验不到它", name)
		}
	}
}

// H14：夹具必须能被**同一套折叠逻辑**吃掉——帧原样进，事件按序出。
//
// 这条是给界面仓的对照：它折叠出来的事件序列应当与这里一致。
func TestGoldenFramesReplayInOrder(t *testing.T) {
	frames := goldenFrames(t)

	j := NewJournal(1, 0)
	for _, f := range frames {
		if f.Type == "pong" || f.Type == "session/reset" {
			continue // 不进 journal：它们不是事件流的一部分
		}
		got := j.Append(f.Type, f.Data, f.Time)
		if got.Seq != f.Seq {
			t.Fatalf("重放时 %s 拿到 seq=%d，夹具写的是 %d", f.Type, got.Seq, f.Seq)
		}
	}
	replayed, last, _ := j.Since(0, 0)
	if len(replayed) != 8 || last != 8 {
		t.Fatalf("应重放 8 帧、水位 8，实际 %d 帧 / 水位 %d", len(replayed), last)
	}
}
