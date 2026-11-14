package main

// 数值 `turn` 是**会话级**身份：`docs/transport-contract.md` 的「消息身份」把它与
// `turn/done.turnId`、`tool/call.callId` 并列，文档示例里也出现 `"turn":2`。
//
// 真机验收抓到的 bug：同一个 session 连发两次 `session.prompt`，
// `session.history` 里第二个 `turn/start` / `turn/end` 仍是 `turn:1`——
// 因为 serve 的每一轮都是新进程 `dshd run --session X`，而子进程把轮次写死成 1。
// 后果是第二轮起所有 turn 在界面上混成一轮（Turn 大纲、消息分组）。
//
// 这条用例只认**行为**：读 `session.history` 返回的原始帧，断言帧上的数值 turn。
// 它不检查任何函数被调用过，也不读进程内计数器。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

// startNumericTurnGateway 是一个只回 "ok" 的模型网关：本轮不跑工具，
// 因此每个 prompt 只产生一轮、一步，帧序列最短，断言只看 turn 编号。
func startNumericTurnGateway(t *testing.T) string {
	t.Helper()
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(gateway.Close)
	return gateway.URL + "/v1"
}

// readNumericTurnFrames 读一次 `session.history` 的原始帧（sinceSeq=0 全量）。
func readNumericTurnFrames(t *testing.T, client *http.Client, base, sid string) []map[string]any {
	t.Helper()
	envelope := postPluginGlue(t, client, base, "session.history",
		map[string]any{"id": sid, "sinceSeq": 0, "limit": 2000}, http.StatusOK)
	raw, ok := envelope["result"].(map[string]any)["frames"].([]any)
	if !ok {
		t.Fatalf("session.history 没有 frames: %+v", envelope)
	}
	frames := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		frame, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("history 帧不是对象: %#v", item)
		}
		frames = append(frames, frame)
	}
	return frames
}

func dumpNumericTurnFrames(frames []map[string]any) string {
	out := ""
	for _, frame := range frames {
		raw, _ := json.Marshal(frame["data"])
		out += fmt.Sprintf("\n  %v %s", frame["type"], raw)
	}
	if out == "" {
		return "（空）"
	}
	return out
}

// numericTurns 按 journal 顺序取某类帧上的数值 `turn`。
func numericTurns(t *testing.T, frames []map[string]any, typ string) []int {
	t.Helper()
	var out []int
	for _, frame := range frames {
		if frame["type"] != typ {
			continue
		}
		data, ok := frame["data"].(map[string]any)
		if !ok {
			t.Fatalf("%s 帧缺 data: %+v", typ, frame)
		}
		raw, ok := data["turn"]
		if !ok {
			t.Fatalf("%s 帧缺数值 turn（契约里的既有身份）: %+v", typ, frame)
		}
		value, ok := raw.(float64)
		if !ok {
			t.Fatalf("%s 帧的 turn 不是数字: %#v", typ, raw)
		}
		out = append(out, int(value))
	}
	return out
}

// countTurnOutcomes 数结局帧（turn/done 与 turn/error 都算一次收工）。
func countTurnOutcomes(frames []map[string]any) int {
	n := 0
	for _, frame := range frames {
		switch frame["type"] {
		case "turn/done", "turn/error":
			n++
		}
	}
	return n
}

// promptAndWaitNumericTurn 发一次 prompt，并等到**第 wantOutcomes 条**结局帧落进 journal。
//
// 为什么要按"第几条"而不是按 turnId 等：宿主重启后 turnId 由新进程的 prompt 序号
// 重新起算，而这里要验的是数值 turn 的**会话级**连续性——拿 turnId 等重启后的第一条
// 会永远等不到。turnId 自己的语义由下面的用例单独钉住（不许被这条用例改掉）。
//
// 等结局帧而不是 sleep：`turn/done` 是在 follow 把 v4 全部 drain 完之后才发的
// （serve_loop.go 的 emitTurn 在 close(done)/fw.Wait() 之后），所以它出现时
// 同一轮的 `turn/start`/`turn/end` 一定已经进了 journal。
func promptAndWaitNumericTurn(t *testing.T, client *http.Client, base, sid, text string, wantOutcomes int) (string, []map[string]any) {
	t.Helper()
	envelope := postPluginGlue(t, client, base, "session.prompt",
		map[string]any{"id": sid, "text": text}, http.StatusOK)
	result, _ := envelope["result"].(map[string]any)
	turnID, _ := result["turnId"].(string)
	if turnID == "" {
		t.Fatalf("session.prompt 没有返回 turnId: %+v", envelope)
	}
	deadline := time.Now().Add(realChainWait)
	for {
		frames := readNumericTurnFrames(t, client, base, sid)
		for _, frame := range frames {
			if frame["type"] != "turn/error" {
				continue
			}
			data, _ := frame["data"].(map[string]any)
			if data["turnId"] == turnID {
				t.Fatalf("turn %s 失败: %s", turnID, dumpNumericTurnFrames(frames))
			}
		}
		if countTurnOutcomes(frames) >= wantOutcomes {
			return turnID, frames
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待第 %d 条结局帧超时（%v）: %s",
				wantOutcomes, realChainWait, dumpNumericTurnFrames(frames))
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// assertNumericTurnSequence 断言 turn/start 与 turn/end 上的数值 turn 都等于 want。
//
// 两类帧都要看：只验 turn/start 会漏掉 turnCloser 补写的那条 turn/end
// （失败/取消/步数用满的出口），而宿主的出口兜底正是它写结局的唯一责任人。
func assertNumericTurnSequence(t *testing.T, label string, frames []map[string]any, want []int) {
	t.Helper()
	for _, typ := range []string{"turn/start", "turn/end"} {
		got := numericTurns(t, frames, typ)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s：%s 的数值 turn = %v，want %v（契约要求会话级递增）。帧:%s",
				label, typ, got, want, dumpNumericTurnFrames(frames))
		}
	}
}

// waitTitleSettled 等"自动标题"这一轮的写手收工。
//
// 为什么必须先等它再去 kill 宿主：标题是 serve 进程里的**另一个 v4 写者**
// （`serve_title_auto.go` 的 recordRequest/commit：OpenSessionAt → Append → Close），
// 它在 turn/done 之后才异步跑。紧接着 kill 宿主会让它带着会话写锁一起死，
// 磁盘上留下一个 `.lock`（`session.claimWriter` 只等 2s、不检测死 pid），
// 重启后第一次 prompt 就会以"会话正被另一个进程写入"失败。
//
// 那是另一件事（会话写锁不回收死进程的锁，本次任务不碰），但它会让用例红在一个
// 与被测行为无关的地方。`session/title` 帧是在 Close 之后才发的，所以
// "看到它"就等于"锁已释放"。
func waitTitleSettled(t *testing.T, client *http.Client, base, sid string) {
	t.Helper()
	deadline := time.Now().Add(realChainWait)
	for {
		frames := readNumericTurnFrames(t, client, base, sid)
		for _, frame := range frames {
			if frame["type"] == "session/title" {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待自动标题落盘超时（%v）: %s", realChainWait, dumpNumericTurnFrames(frames))
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestServeNumericTurnIsSessionScopedAcrossPromptsRestartAndSessions(t *testing.T) {
	t.Setenv("DSHD_LLM_BASE", startNumericTurnGateway(t))
	t.Setenv("DSHD_LLM_KEY", "")
	// 生产形状：每轮一个 `dshd run` 子进程。进程内 runner 不是这条用例的对象。
	t.Setenv("DSHD_SERVE_MODEL_RUNNER", "")

	exe := buildPluginGlueDSHD(t)
	home := t.TempDir()
	var servers []*pluginGlueServer
	start := func() *pluginGlueServer {
		server := startPluginGlueServer(t, exe, home, t.TempDir())
		servers = append(servers, server)
		return server
	}
	// 失败时把每个宿主进程的 stderr 都贴出来：轮次编号错了和"宿主根本没起来"
	// 是两种完全不同的红，不能让下一个人从头猜。
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		for i, server := range servers {
			t.Logf("serve#%d stderr:\n%s", i, server.stderr.String())
		}
	})
	server := start()
	client := connectPluginGlueServer(t, server)
	createSession := func() string {
		envelope := postPluginGlue(t, client, server.base, "session.create", map[string]any{}, http.StatusOK)
		sid, _ := envelope["result"].(map[string]any)["sessionId"].(string)
		if sid == "" {
			t.Fatalf("session.create 没有返回 sessionId: %+v", envelope)
		}
		return sid
	}
	sidA, sidB := createSession(), createSession()

	// ① 同一个会话连发 3 次 prompt：第 N 次的数值 turn 必须是 N。
	var ackTurnIDs []string
	for i, text := range []string{"first", "second", "third"} {
		turnID, frames := promptAndWaitNumericTurn(t, client, server.base, sidA, text, i+1)
		ackTurnIDs = append(ackTurnIDs, turnID)
		want := make([]int, 0, i+1)
		for n := 1; n <= i+1; n++ {
			want = append(want, n)
		}
		assertNumericTurnSequence(t, fmt.Sprintf("会话 A 第 %d 次 prompt", i+1), frames, want)
	}
	// ② turnId 语义不变：它按 **prompt 序号**递增（turn-1/turn-2/turn-3），
	//    与数值 turn 是两条独立身份。谁把这条改成"数值 turn 的字符串形式"，
	//    这里就红——既有身份一律不动。
	if want := []string{"turn-1", "turn-2", "turn-3"}; !reflect.DeepEqual(ackTurnIDs, want) {
		t.Fatalf("session.prompt 的 turnId = %v，want %v（turnId 语义不许随数值 turn 改变）",
			ackTurnIDs, want)
	}
	// ③ 每一轮的结局帧带的是**自己那一轮的** turnId，一轮一条，不许合并/重复。
	framesA := readNumericTurnFrames(t, client, server.base, sidA)
	var doneTurnIDs []string
	for _, frame := range framesA {
		if frame["type"] != "turn/done" {
			continue
		}
		data, _ := frame["data"].(map[string]any)
		id, _ := data["turnId"].(string)
		doneTurnIDs = append(doneTurnIDs, id)
	}
	if !reflect.DeepEqual(doneTurnIDs, ackTurnIDs) {
		t.Fatalf("turn/done 的 turnId = %v，与 prompt ACK 的 %v 不一一对应", doneTurnIDs, ackTurnIDs)
	}

	// ④ 不同会话各自从 1 开始，互不影响。
	_, framesB := promptAndWaitNumericTurn(t, client, server.base, sidB, "other session", 1)
	assertNumericTurnSequence(t, "会话 B 第 1 次 prompt", framesB, []int{1})

	// ⑤ 宿主重启后再发一次：继续递增（3 -> 4），不是回到 1。
	//    这是"不能只靠内存计数器"的判据：轮次只能从 append-only 的 v4 日志恢复。
	waitTitleSettled(t, client, server.base, sidB) // 别在标题写手持锁时 kill（见该函数注释）
	server.stop()
	server = start()
	client = connectPluginGlueServer(t, server)
	_, framesARestart := promptAndWaitNumericTurn(t, client, server.base, sidA, "after restart", 4)
	assertNumericTurnSequence(t, "会话 A 重启后第 4 次 prompt", framesARestart, []int{1, 2, 3, 4})

	// ⑥ 重启后另一个会话也按**自己的**日志继续（B: 1 -> 2），不借 A 的轮次。
	_, framesBRestart := promptAndWaitNumericTurn(t, client, server.base, sidB, "other session second", 2)
	assertNumericTurnSequence(t, "会话 B 重启后第 2 次 prompt", framesBRestart, []int{1, 2})
}
