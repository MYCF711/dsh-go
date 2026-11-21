package transport

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"
)

// 本文件 = wire 契约**时序/语义**审计的两条宿主侧证据（不是字段形状）。
//
// 审计镜头：界面（`dsh-ui`）与宿主各自测试全绿，但两边对"水位/基线"的时序假设不同，
// 对接起来会静默丢内容。两条都是从"界面会怎么用"倒推出来的：
//
//  1. 首屏基线：界面先 `session.load` 拿 `{journalEpoch, lastSeq}`，再用
//     `lastSeq` 当 `sinceSeq` 订阅（`dsh-ui/cmd/app/main.go:769-784`）。
//     宿主如果**在投影之后**才读水位，投影期间追加的帧既不在 messages 里、
//     也不会被重放——客户端永远拿不到，而且两边都不报错。
//
//  2. 水位超前：界面的 `since` 可能比宿主 journal 还大（缓存水位跨进程、
//     journal 被 Reset/Forget、或 epoch 恰好对上）。`session.history` 对同一份
//     输入明确回 409 `stale_epoch`（`TestHistoryRejectsStaleEpoch`），
//     WS `sub` 那条路却**什么都不回**——同样的请求，两条通道两种答复。

// auditLoadWatermarkAppender 在投影期间往同一条 journal 追加一帧，
// 模拟真实宿主里的后台 follow 循环（`cmd/dshd/serve_tools.go:461` 那个
// 200ms 轮询的 emit）：它和 `session.load` 的投影是**并发**的。
type auditLoadWatermarkAppender struct {
	journals *Journals
	session  string
	once     sync.Once
	seq      int
}

func (a *auditLoadWatermarkAppender) load(string, int) (LoadResult, error) {
	// 投影开始之后、返回之前追加：这一帧属于 journal，但**不属于** messages。
	a.once.Do(func() {
		f, err := a.journals.Append(a.session, "assistant/text",
			json.RawMessage(`{"text":"投影之后才到的一句"}`), 1_760_000_000_000)
		if err == nil {
			a.seq = f.Seq
		}
	})
	return LoadResult{Messages: []Message{}}, nil
}

// TestAuditLoadWatermarkMustNotOutrunProjection 断言首屏基线的时序约束：
//
//	load 给出的 lastSeq 之后，客户端必须还能拿到投影没带上的那一帧。
//
// 界面拿到 lastSeq 就把它当 `sinceSeq` 订阅（`cmd/app/main.go:773-779` →
// `mux.go:181`），而 `readLoop` 会把 `seq <= sinceSeq` 的帧当重复丢掉
// （`mux.go:258`）。所以 load 的 lastSeq 一旦越过投影读点，那一帧就在
// **三个地方同时消失**：load 的 messages（投影在前）、WS 重放（水位在后）、
// 以及客户端自己的判重（seq <= 上次水位）。症状是"发出去没反应"。
func TestAuditLoadWatermarkMustNotOutrunProjection(t *testing.T) {
	js := NewJournals(nil)
	appender := &auditLoadWatermarkAppender{journals: js, session: "s1"}
	s := newTestServer(t, Options{Journals: js, Load: appender.load})

	rec := postJSON(t, s, "/api/session.load", `{"id":"s1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("load 要 200，拿到 %d：%s", rec.Code, rec.Body.String())
	}
	var env struct {
		Result LoadResult `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("load 响应不是 JSON：%v", err)
	}
	res := env.Result
	if appender.seq == 0 {
		t.Fatal("测试前提不成立：投影期间没有追加到任何帧")
	}
	if len(res.Messages) != 0 {
		t.Fatalf("测试前提不成立：messages 应当是空的，实际 %d 条", len(res.Messages))
	}

	// ① 水位不许越过投影：投影没带上的帧必须落在 lastSeq 之后。
	if res.LastSeq >= appender.seq {
		t.Fatalf("load 的 lastSeq=%d 越过了投影读点（投影期间追加的是 seq=%d）："+
			"这一帧既不在这份 messages 里，也不会被重放", res.LastSeq, appender.seq)
	}

	// ② 用界面那一套接法验一遍：load → sinceSeq=lastSeq 订阅，必须能收到那一帧。
	hub := NewHub()
	hub.SetJournals(js)
	p := newPipe()
	go ServeConn(p, hub)
	p.in <- ClientFrame{T: "sub", Topic: "session:s1", SinceSeq: res.LastSeq, JournalEpoch: res.JournalEpoch}

	got := p.next(t)
	if got.Seq != appender.seq {
		t.Fatalf("从 lastSeq=%d 订阅后第一帧应是 seq=%d，拿到 seq=%d type=%q",
			res.LastSeq, appender.seq, got.Seq, got.Type)
	}
	p.closeIn()
}

// TestAuditSubscribeWatermarkAheadMustAskForBaseline 断言两条通道对**同一份输入**
// （客户端水位比宿主 journal 还大）给同一种答复：明确要求重建基线。
//
// 现状：`session.history` 已经这么做（409 `stale_epoch`，见
// `TestHistoryRejectsStaleEpoch`）；WS `sub` 那条路把 `sinceSeq` 直接当订阅下界，
// 于是 `Window(sinceSeq)` 永远返回空集 → 之后每个新帧都被 `seq > sinceSeq` 过掉
// → **一帧不发、不报错、不发 gap**。客户端只看到"连着但什么都不来"。
//
// 客户端必然拿到这种水位：`dsh-ui` 的 `m.seqs[id]` 在宿主重启/换 journal 之后
// 仍然留着旧值，而 epoch 只在 `m.epochs[id]` 里、可能压根没拿到（`cmd/app/main.go:1127`
// 用 `m.lastSeq` 当 since，`mux.go:180` 的 epochLookup 取不到时就是 0）。
func TestAuditSubscribeWatermarkAheadMustAskForBaseline(t *testing.T) {
	js := NewJournals(nil)
	j := js.Get("s1")
	for i := 1; i <= 3; i++ {
		j.Append("assistant/text", json.RawMessage(`{"text":"x"}`), int64(1_760_000_000_000+i))
	}
	epoch := j.Epoch()

	// 对照：同一份水位走 HTTP 面 → 409 stale_epoch（已有测试钉住，这里再取一次事实）。
	s := newTestServer(t, Options{Journals: js})
	rec := postJSON(t, s, "/api/session.history",
		fmt.Sprintf(`{"id":"s1","sinceSeq":42,"journalEpoch":%d}`, epoch))
	if rec.Code != http.StatusConflict {
		t.Fatalf("history 对超前水位应回 409，拿到 %d：%s", rec.Code, rec.Body.String())
	}

	for _, tc := range []struct {
		name  string
		epoch int64
	}{
		{"epoch 对得上", epoch},
		{"epoch 未知（0，客户端没有基线）", 0},
		{"epoch 是上一代", epoch + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub := NewHub()
			hub.SetJournals(js)
			p := newPipe()
			go ServeConn(p, hub)

			p.in <- ClientFrame{T: FrameSub, Topic: "session:s1", SinceSeq: 42, JournalEpoch: tc.epoch}
			select {
			case f := <-p.out:
				if f.Type != "session/reset" {
					t.Fatalf("第一条帧应是 session/reset，拿到 type=%q seq=%d", f.Type, f.Seq)
				}
			case <-time.After(300 * time.Millisecond):
				t.Fatal("sinceSeq=42 > lastSeq=3 时宿主一帧都没发：客户端会永远收不到任何事件，" +
					"而 history 面同一份水位回的是 409 stale_epoch —— 两条通道语义不一致")
			}

			// reset 之后必须能从新基线接着收：新帧要真的推过来。
			// 走生产同一条路（先入 journal 发号，再 Publish 提醒订阅者补洞）。
			nf, err := js.Append("s1", "assistant/text", json.RawMessage(`{"text":"reset 之后"}`), 1_760_000_000_100)
			if err != nil {
				t.Fatalf("追加失败：%v", err)
			}
			hub.Publish("session:s1", nf)
			deadline := time.After(2 * time.Second)
			for {
				select {
				case f := <-p.out:
					if f.Type == "session/reset" {
						continue
					}
					if f.Seq != nf.Seq {
						t.Fatalf("reset 之后应收到 seq=%d，拿到 seq=%d type=%q", nf.Seq, f.Seq, f.Type)
					}
					p.closeIn()
					return
				case <-deadline:
					t.Fatal("reset 之后仍然收不到新帧：水位没有真正重建")
				}
			}
		})
	}
}

