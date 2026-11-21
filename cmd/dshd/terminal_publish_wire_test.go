package main

// 官方 `terminal.watch` 的 roster 事件源必须是**推送**（`@deepseek-ai/dsh-api-terminal-controller`
// `src/index.ts:405 changed` 遍历该会话的 roster 监听器）。本宿主的两种形态：
//
//	有推送接线   → 身份/状态变化点调 `publishRosterChange` → 正在看的人**立刻**拿到整份新集合；
//	没接线（nil）→ 每会话一个差分管家器按 `DefaultSetWatchInterval`（250ms）重取比对。
//
// 下面两条用例分别钉住「生产接线真的在」和「哪些变更点会报」。两条都**不靠时序运气**：
// 第一条把 `WatchInterval` 设成 1h，差分检测在这条用例的寿命里不可能兜底，所以收到新集合
// 只可能来自推送；第二条直接数推送调用，不经过任何网络或轮询。

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"dshgo/internal/transport"
)

// productionWatchHandler 组装生产 shape 的 handler：`Handler()` 是唯一的接线入口。
//
// WatchInterval 必须在**第一次订阅/推送之前**赋值（事件源是懒建的，见
// `OfficialJobTerminal.terminalWatcher`），所以这里在 Handler() 之后、OpenFrameStream 之前设。
func productionWatchHandler(t *testing.T, api *officialPTYTerminalAPI, interval time.Duration) *transport.OfficialJobTerminal {
	t.Helper()
	svc := &officialPTYJobTerminalService{officialPTYTerminalAPI: api}
	h := svc.Handler()
	h.WatchInterval = interval
	return h
}

// TestOfficialTerminalWatchPushesRosterChanges 证明「新建/关闭终端后，watch 立刻发新集合」。
func TestOfficialTerminalWatchPushesRosterChanges(t *testing.T) {
	api, _ := newTestPTYTerminalAPI(t, t.TempDir())
	// 1h：差分器在这条用例里永不触发，"来帧"只可能是推送。用默认 250ms 也能过，
	// 但那样红了就分不清是"没推送"还是"推送太慢"，判据会变弱。
	h := productionWatchHandler(t, api, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, fault := h.OpenFrameStream(ctx, "terminal.watch", json.RawMessage(`{"sessionId":"s1"}`))
	if fault != nil {
		t.Fatalf("打开 terminal.watch 失败：%s（%s）", fault.Code, fault.Message)
	}
	defer stream.Close()

	next := func(want string) string {
		t.Helper()
		nextCtx, nextCancel := context.WithTimeout(ctx, 5*time.Second)
		defer nextCancel()
		frame, done, f := stream.Next(nextCtx)
		if f != nil {
			t.Fatalf("%s：流报错 %s（%s）", want, f.Code, f.Message)
		}
		if done {
			t.Fatalf("%s：流提前结束（没有新集合；推送没接线或没落在变化点上）", want)
		}
		return string(frame)
	}

	// 首帧 = 当前全集（此刻一条都没有）。
	if first := next("首帧"); strings.TrimSpace(first) != "[]" {
		t.Fatalf("首帧必须是当前全集（空数组），拿到 %s", first)
	}

	// 建一条真 PTY 终端 → 必须立刻来一整份新集合。
	if _, err := api.TerminalCreate(context.Background(), "s1", transport.OfficialTerminalCreateRequest{
		ID: "term-1", Cols: 80, Rows: 24,
	}); err != nil {
		t.Fatalf("建 PTY 终端失败：%v", err)
	}
	created := next("新建终端后的集合")
	if !strings.Contains(created, `"id":"term-1"`) {
		t.Fatalf("新建终端后必须立刻收到含 term-1 的整份集合，拿到 %s", created)
	}
	infos, raw := decodeWatchRoster(t, created)
	if len(infos) != 1 || infos[0].ID != "term-1" {
		t.Fatalf("新建后集合必须正好一条 term-1，拿到 %s", created)
	}
	// 整份集合的字段形状不能被这次接线改掉（rc.2 的 WebTerminalInfo 必填字段）。
	for _, k := range []string{"id", "title", "shell", "cwd", "cols", "rows", "state", "exitCode", "origin", "control"} {
		if _, ok := raw[0][k]; !ok {
			t.Fatalf("rc.2 必填字段 %q 丢了：%s", k, created)
		}
	}

	// 改名 → title 在 info 里，属于集合内容变了，也要立刻来。
	if err := api.TerminalRename(context.Background(), "s1", "term-1", "构建日志"); err != nil {
		t.Fatalf("改名失败：%v", err)
	}
	if renamed := next("改名后的集合"); !strings.Contains(renamed, `"构建日志"`) {
		t.Fatalf("改名后必须立刻收到新 title，拿到 %s", renamed)
	}

	// 关闭 → 集合少一条，也要立刻来（官方 close 成功后调 changed，src/index.ts:369）。
	if err := api.TerminalClose(context.Background(), "s1", "term-1"); err != nil {
		t.Fatalf("关闭终端失败：%v", err)
	}
	if closed := next("关闭终端后的集合"); strings.TrimSpace(closed) != "[]" {
		t.Fatalf("关闭后集合必须回到空，拿到 %s", closed)
	}
}

// TestOfficialTerminalRosterPublishPoints 钉住**哪些**变更点报 roster 变化。
//
// 为什么这条要单独立着：报得太多会放大帧数（每次 Publish 都可能换来一次重取，
// 抖动型变更比如拖动改尺寸会让帧数从"每 250ms 一帧"变成"每个事件一帧"），
// 报得太少就退回 250ms。所以这份清单是**故意**的，不是遗漏：
//
//	报：create / agent create / close / agent close（委托 close）/ rename / typed 接管
//	不报：resize、retain、进程退出（惰性发现，见下面注释）
func TestOfficialTerminalRosterPublishPoints(t *testing.T) {
	api, _ := newTestPTYTerminalAPI(t, t.TempDir())

	var mu sync.Mutex
	var calls []string
	// 直接换掉出口：不涉及 watch 流，也不涉及时序。
	api.setRosterPublisher(func(sessionID string) {
		mu.Lock()
		calls = append(calls, sessionID)
		mu.Unlock()
	})
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(calls)
	}
	expect := func(step string, want int) {
		t.Helper()
		if got := count(); got != want {
			mu.Lock()
			got2 := append([]string(nil), calls...)
			mu.Unlock()
			t.Fatalf("%s 之后推送次数应为 %d，实际 %d（%v）", step, want, got, got2)
		}
	}

	// create：官方 allocate 提交后 changed（src/index.ts:427）。
	if _, err := api.TerminalCreate(context.Background(), "s1", transport.OfficialTerminalCreateRequest{
		ID: "t1", Cols: 80, Rows: 24,
	}); err != nil {
		t.Fatalf("建终端失败：%v", err)
	}
	expect("terminal.create", 1)

	// resize：**故意不报**。cols/rows 虽然在 info 里，但 resize 是抖动型变更
	// （拖分栏时每次 onResize 都发一次 terminal.resize），接推送会把整份集合的帧数
	// 从 4/s 放大到事件数/s；而发起方本来就能从自己的响应/本地状态知道新尺寸
	// （dsh-ui `cmd/app/terminal_wiring.go` 的 terminalResizeOfficial 已本地回填），
	// 别的观察者 250ms 内照样拿到——**收益≈0，代价是帧数放大**，所以留在差分里。
	if err := api.Resize("s1", "t1", 100, 30); err != nil {
		t.Fatalf("改尺寸失败：%v", err)
	}
	expect("terminal.resize", 1)

	// retain：只动保留位与落盘，不是 info 的字段，集合字节不变 → 报了也只会被去重成不发帧。
	if err := api.TerminalRetain(context.Background(), "s1", "t1"); err != nil {
		t.Fatalf("retain 失败：%v", err)
	}
	expect("terminal.retain", 1)

	// rename：title 在 info 里 → 官方 broadcastState→stateListener→changed（src/terminal.ts:321-324）。
	if err := api.TerminalRename(context.Background(), "s1", "t1", "dev"); err != nil {
		t.Fatalf("改名失败：%v", err)
	}
	expect("terminal.rename", 2)

	// typed 接管：control 在 info 里（官方 src/terminal.ts:138 改完就 broadcastState）。
	// 先把真终端的 control 改成 agent（模拟 agent 开的终端），再让"人"敲一次键。
	api.mu.Lock()
	if entry := api.entries["s1\x00t1"]; entry != nil {
		entry.control = transport.OfficialTerminalControlAgent
	}
	api.mu.Unlock()
	if _, err := api.InputTyped("s1", "t1", "x", true); err != nil {
		t.Fatalf("typed 输入失败：%v", err)
	}
	expect("terminal.input(typed)", 3)

	// close：官方 close 成功后 changed（src/index.ts:369）。
	if err := api.TerminalClose(context.Background(), "s1", "t1"); err != nil {
		t.Fatalf("关闭失败：%v", err)
	}
	expect("terminal.close", 4)

	// 进程退出**不报**：本宿主的退出是**惰性发现**的（`screenOf` 里读 `pty.Exited()`
	// 才把 state 改成 exited，`terminal_pty.go` 的 screenOf），而 screenOf 只有在
	// list/attach 读路径上才会被调。要在退出点推送就得有一个真正的进程监听器，
	// 那是另一件事；现在退出仍走 250ms 差分（也是唯一能发现它的路径）。
	// 这条注释就是它不在上面清单里的理由。
}

// decodeWatchRoster 解一帧整份集合，同时留一份原始字段表给形状断言用。
func decodeWatchRoster(t *testing.T, frame string) ([]transport.OfficialWebTerminalInfo, []map[string]any) {
	t.Helper()
	var infos []transport.OfficialWebTerminalInfo
	if err := json.Unmarshal([]byte(frame), &infos); err != nil {
		t.Fatalf("watch 帧不是终端数组：%v（%s）", err, frame)
	}
	var raw []map[string]any
	if err := json.Unmarshal([]byte(frame), &raw); err != nil {
		t.Fatalf("watch 帧字段表解析失败：%v（%s）", err, frame)
	}
	return infos, raw
}
