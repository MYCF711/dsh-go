package main

// 本文件钉住官方 0.2.2 #78 `session-checkpoint-policy` 的 **`agent/pre-step`**
// 在 serve **内建 runner**（`modelRunner`，即 `DSHD_SERVE_MODEL_RUNNER=1` 那条路）
// 上的落点。
//
// 官方原件（asar 只读解出，dsh-session-checkpoint-policy/lib/index.js）：
//
//	:67-70  ctx.on("agent/pre-step", async ({ agent }, next) => {
//	           await ctx.sessions.flush(agent.session);
//	           return next();
//	        });
//	:55-57  "Checkpoint failures are fail-closed at the model and tool
//	         side-effect boundaries: the downstream adapter or tool body is not invoked."
//
// 为什么单独有这个文件：serve 的 prompt 侧车（`serve_checkpoint.go` 的
// `withDurableCheckpointOptions`）只看得见**第一步**；`serve_turn.go` 的
// `runModelTurnWithEffort` 自建模型步进循环，第二步起（steer 接管、图片降级
// 重试）在此之前是**裸奔**的——那一步的模型请求在请求前缀耐久之前就发出去了。
//
// 判据与 run / acp 的 `agent.Loop.PreStep` 完全同源：
//   - 每一步模型请求**发出之前**先让请求前缀耐久；
//   - 落盘失败 → 该步模型请求根本不派发（fail-closed）；
//   - 同一锚点重复调用是幂等命中（同一轮只落一份）。

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dshgo/internal/session"
	"dshgo/internal/spi"
)

// TestServePreStepFailsClosedBeforeSecondModelRequest 钉「每一步」的 fail-closed：
// 第一步的 pre-step 快照落好之后，把侧车根目录换成普通文件（既读不到侧车、
// 也建不出临时文件），第二步的模型请求**一次都不许发出去**。
//
// 红：没有 pre-step 接线时，第二次模型请求照发（requests=2），Run 正常收工。
func TestServeCheckpointPreStepFailsClosedBeforeSecondModelRequest(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	const sid = "session-serve-prestep-failclosed"
	const turnID = "turn-prestep-failclosed"
	seedTurnSession(t, home, cwd, sid, func(lg *session.Log) error {
		return lg.Append("turn/start", session.TurnStartData(1))
	})

	var (
		mu       sync.Mutex
		requests int
		runner   *modelRunner
	)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		n := requests
		mu.Unlock()
		if n == 1 {
			// 第一步的 pre-step 快照已经落盘；此刻把侧车根目录换成普通文件。
			// 第二步的 pre-step 既读不到侧车（ENOTDIR 不是 os.ErrNotExist），
			// 也建不出临时文件 —— 两条路都必须让它响亮失败。
			if err := os.RemoveAll(filepath.Join(home, "durability-checkpoints")); err != nil {
				t.Errorf("清侧车根目录: %v", err)
			}
			if err := os.WriteFile(filepath.Join(home, "durability-checkpoints"), []byte("not a dir"), 0o600); err != nil {
				t.Errorf("用普通文件占住侧车根目录: %v", err)
			}
			// 排一条 steer 进当前轮：第二步模型请求**必须**被调度，
			// 否则这条用例证明不了「第二步之前也要落盘」。
			if err := runner.Steer(context.Background(), sid, turnID, "steer-into-second-step"); err != nil {
				t.Errorf("提交 steer: %v", err)
			}
		}
		writeSteerTextSSE(w, fmt.Sprintf("assistant-%d", n))
	}))
	defer gateway.Close()
	runner = &modelRunner{home: home, base: gateway.URL + "/v1", model: "m"}

	if err := runner.Run(context.Background(), sid, turnID, "original"); err == nil {
		t.Fatalf("第二步 pre-step 落盘失败却成功收工（不是 fail-closed）")
	} else if !strings.Contains(err.Error(), "durability checkpoint") {
		t.Fatalf("错误没有指出是 durability checkpoint 失败：%v", err)
	}

	mu.Lock()
	got := requests
	mu.Unlock()
	if got != 1 {
		t.Fatalf("pre-step 落盘失败仍派发了 %d 次模型请求（期望只有第 1 步那一次）", got)
	}
}

// TestServePreStepWritesSidecarWithoutPromptWrapper 钉「runner 自己也要落盘」：
// 直接调 `Run`（**不经过** `withDurableCheckpointOptions` 的 prompt 侧车），
// 两模型步的一轮结束后磁盘上必须有请求前快照，且能按产品自己的恢复 API 复核。
func TestServeCheckpointPreStepWritesSidecarWithoutPromptWrapper(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	const sid = "session-serve-prestep-sidecar"
	const turnID = "turn-prestep-sidecar"
	// 种子 2 帧（turn/start + user/message）：请求前前缀的锚点就是 2。
	seedTurnSession(t, home, cwd, sid, func(lg *session.Log) error {
		if err := lg.Append("turn/start", session.TurnStartData(1)); err != nil {
			return err
		}
		return lg.Append("user/message", session.UserMessage([]spi.Block{{Type: "text", Text: "上一轮的问题"}}))
	})

	var (
		mu       sync.Mutex
		requests int
		runner   *modelRunner
	)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		n := requests
		mu.Unlock()
		if n == 1 {
			if err := runner.Steer(context.Background(), sid, turnID, "steer-into-second-step"); err != nil {
				t.Errorf("提交 steer: %v", err)
			}
		}
		writeSteerTextSSE(w, fmt.Sprintf("assistant-%d", n))
	}))
	defer gateway.Close()
	runner = &modelRunner{home: home, base: gateway.URL + "/v1", model: "m"}

	if err := runner.Run(context.Background(), sid, turnID, "original"); err != nil {
		t.Fatalf("两模型步的一轮应当正常收工: %v", err)
	}
	mu.Lock()
	got := requests
	mu.Unlock()
	if got != 2 {
		t.Fatalf("这一轮应当走满 2 个模型步，实际 %d 次模型请求", got)
	}

	cps, err := session.ListDurableCheckpoints(home, sid)
	if err != nil {
		t.Fatalf("列快照: %v", err)
	}
	if len(cps) == 0 {
		t.Fatalf("两模型步的一轮没有留下任何请求前快照（pre-step 没有接线）")
	}
	if len(cps) != 1 {
		t.Fatalf("同一锚点（v4 在这一轮内不增长）应只有一份快照，实际 %d 份: %+v", len(cps), cps)
	}
	cp := cps[0]
	if cp.SessionID != sid {
		t.Fatalf("快照归属错了：id=%q 期望 %q", cp.SessionID, sid)
	}
	if cp.CoveredEvents != 2 {
		t.Fatalf("快照覆盖事件数 = %d，期望 2（种子 turn/start + user/message）", cp.CoveredEvents)
	}
	// 官方 pre-step 就是一次 flush，没有请求载荷：请求身份由同一锚点先到的
	// prompt 侧车保留。直接调 Run 时没有任何 prompt 侧车，所以这里必须是 nil。
	if cp.Request != nil {
		t.Fatalf("pre-step 是 flush，不该记请求载荷：%+v", cp.Request)
	}
	rec, err := session.RecoverDurableCheckpoint(home, sid)
	if err != nil {
		t.Fatalf("重启恢复（只读复核前缀）失败: %v", err)
	}
	if rec.Checkpoint.CoveredEvents != 2 || rec.PrefixBytes <= 0 {
		t.Fatalf("恢复出的前缀不自洽：%+v", rec.Checkpoint)
	}
}

// TestServePreStepIsolatesConcurrentSessions 钉并发矩阵：两个会话**同时**在飞
// （各自第一步的请求都由网关闸住），各自第二步的 pre-step 落自己那一份，
// A/B 的侧车锚点、归属、v4 字节都不许互相污染。
func TestServeCheckpointPreStepIsolatesConcurrentSessions(t *testing.T) {
	home := t.TempDir()
	const (
		sidA  = "session-prestep-ab-a"
		sidB  = "session-prestep-ab-b"
		turnA = "turn-prestep-ab-a"
		turnB = "turn-prestep-ab-b"
	)
	pathA := seedTurnSession(t, home, t.TempDir(), sidA, func(lg *session.Log) error {
		return lg.Append("turn/start", session.TurnStartData(1))
	}) // 1 帧
	pathB := seedTurnSession(t, home, t.TempDir(), sidB, func(lg *session.Log) error {
		if err := lg.Append("turn/start", session.TurnStartData(1)); err != nil {
			return err
		}
		if err := lg.Append("user/message", session.UserMessage([]spi.Block{{Type: "text", Text: "B 的前缀"}})); err != nil {
			return err
		}
		return lg.Append("assistant/message",
			session.AssistantMessageEvent(1, 1, []map[string]any{session.TextBlock("B 的回答")}, "stop", nil))
	}) // 3 帧
	beforeA, err := os.ReadFile(pathA)
	if err != nil {
		t.Fatalf("读 A 的 v4: %v", err)
	}
	beforeB, err := os.ReadFile(pathB)
	if err != nil {
		t.Fatalf("读 B 的 v4: %v", err)
	}

	// 两个第一步请求必须真的同时在飞，才可能暴露串扰。
	var arrived sync.WaitGroup
	arrived.Add(2)
	var releaseOnce sync.Once
	release := make(chan struct{})
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	go func() {
		arrived.Wait()
		releaseAll()
	}()

	var (
		runnerA, runnerB *modelRunner
		mu               sync.Mutex
		requestsA        int
		requestsB        int
	)
	gatewayA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestsA++
		n := requestsA
		mu.Unlock()
		if n == 1 {
			arrived.Done()
			<-release
			if serr := runnerA.Steer(context.Background(), sidA, turnA, "steer-a"); serr != nil {
				t.Errorf("A 提交 steer: %v", serr)
			}
		}
		writeSteerTextSSE(w, "A")
	}))
	defer gatewayA.Close()
	gatewayB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestsB++
		n := requestsB
		mu.Unlock()
		if n == 1 {
			arrived.Done()
			<-release
			if serr := runnerB.Steer(context.Background(), sidB, turnB, "steer-b"); serr != nil {
				t.Errorf("B 提交 steer: %v", serr)
			}
		}
		writeSteerTextSSE(w, "B")
	}))
	defer gatewayB.Close()
	// 最后注册 → 最先执行：先放行两个 handler，再关网关。
	defer releaseAll()
	runnerA = &modelRunner{home: home, base: gatewayA.URL + "/v1", model: "m"}
	runnerB = &modelRunner{home: home, base: gatewayB.URL + "/v1", model: "m"}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if rerr := runnerA.Run(context.Background(), sidA, turnA, "original-A"); rerr != nil {
			t.Errorf("A 这一轮失败: %v", rerr)
		}
	}()
	go func() {
		defer wg.Done()
		if rerr := runnerB.Run(context.Background(), sidB, turnB, "original-B"); rerr != nil {
			t.Errorf("B 这一轮失败: %v", rerr)
		}
	}()
	wg.Wait()

	mu.Lock()
	gotA, gotB := requestsA, requestsB
	mu.Unlock()
	if gotA != 2 || gotB != 2 {
		t.Fatalf("两个会话都应当走满 2 个模型步：A=%d B=%d", gotA, gotB)
	}

	cpsA, err := session.ListDurableCheckpoints(home, sidA)
	if err != nil {
		t.Fatalf("列 A 的快照: %v", err)
	}
	cpsB, err := session.ListDurableCheckpoints(home, sidB)
	if err != nil {
		t.Fatalf("列 B 的快照: %v", err)
	}
	if len(cpsA) != 1 || len(cpsB) != 1 {
		t.Fatalf("两个会话各应只有一份快照：A=%+v B=%+v", cpsA, cpsB)
	}
	if cpsA[0].SessionID != sidA || cpsA[0].CoveredEvents != 1 {
		t.Fatalf("A 的快照串了：%+v", cpsA[0])
	}
	if cpsB[0].SessionID != sidB || cpsB[0].CoveredEvents != 3 {
		t.Fatalf("B 的快照串了：%+v", cpsB[0])
	}
	// 侧车只读派生：任何一份都不许回写 v4（R5 append-only）。
	// 这一轮自己落盘的事件会追加在 before 之后，所以只比前缀。
	afterA, err := os.ReadFile(pathA)
	if err != nil {
		t.Fatalf("读 A 的 v4: %v", err)
	}
	afterB, err := os.ReadFile(pathB)
	if err != nil {
		t.Fatalf("读 B 的 v4: %v", err)
	}
	if !bytes.HasPrefix(afterA, beforeA) {
		t.Fatalf("A 的 v4 前缀被改写了：%d -> %d 字节", len(beforeA), len(afterA))
	}
	if !bytes.HasPrefix(afterB, beforeB) {
		t.Fatalf("B 的 v4 前缀被改写了：%d -> %d 字节", len(beforeB), len(afterB))
	}
}

// TestServeCheckpointKillRestartRealProcess 是「真实 dshd kill/restart 恢复」的
// 硬证据：起**真的** dshd 二进制（`serve --addr 127.0.0.1:0` + 内建 modelRunner），
// 用一次真实 HTTP `session.prompt` 把模型请求发出去（假网关收到 POST 才写
// in-flight 标记），然后整进程被杀；重启后必须能用产品自己的恢复 API 复核
// 请求前的全部帧，并且会话仍可正常 load。
//
// 这条用例在接线前后都应当是绿的（prompt 侧车已经覆盖同一锚点）：它守的是
// 「加完 pre-step 之后，崩溃恢复语义没有被改坏」。
func TestServeCheckpointKillRestartRealProcess(t *testing.T) {
	exe := buildPluginGlueDSHD(t)
	home := t.TempDir()
	const sid = "session-serve-prestep-kill"
	const prompt = "杀掉服务之前这一句必须已经落成耐久快照"
	path := seedTurnSession(t, home, t.TempDir(), sid, func(lg *session.Log) error {
		if err := lg.Append("turn/start", session.TurnStartData(1)); err != nil {
			return err
		}
		if err := lg.Append("user/message", session.UserMessage([]spi.Block{{Type: "text", Text: "上一轮的问题"}})); err != nil {
			return err
		}
		if err := lg.Append("assistant/message",
			session.AssistantMessageEvent(1, 1, []map[string]any{session.TextBlock("上一轮的回答")}, "stop", nil)); err != nil {
			return err
		}
		return lg.Append("turn/end", session.TurnEndData(1, "completed", ""))
	})
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读请求前的 v4: %v", err)
	}

	marker := filepath.Join(home, "in-flight.marker")
	release := make(chan struct{})
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if werr := os.WriteFile(marker, []byte(r.Method+" "+r.URL.Path), 0o600); werr != nil {
			t.Errorf("写 in-flight 标记: %v", werr)
		}
		// 不产生任何响应：请求一直吊着，服务在这时被杀。
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer gateway.Close()
	defer close(release)

	server := startCheckpointServe(t, exe, home,
		"DSHD_SERVE_MODEL_RUNNER=1",
		"DSHD_LLM_BASE="+gateway.URL+"/v1",
	)
	client := connectPluginGlueServer(t, server)
	postPluginGlue(t, client, server.base, "session.prompt",
		map[string]any{"sessionId": sid, "text": prompt}, http.StatusOK)

	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, serr := os.Stat(marker); serr == nil {
			break
		}
		if time.Now().After(deadline) {
			server.stop()
			t.Fatalf("serve 没有把模型请求发出去（等 in-flight 标记超时）；stderr=%s", server.stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	// 请求**已经在飞**：此刻落盘的一切都是「请求前」的东西。
	if kerr := server.cmd.Process.Kill(); kerr != nil {
		t.Fatalf("杀 serve 进程: %v", kerr)
	}
	<-server.done

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("杀进程后读 v4: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("v4 会话文件被改写了：请求前 %d 字节，杀进程后 %d 字节（append-only 违规）", len(before), len(after))
	}

	rec, err := session.RecoverDurableCheckpoint(home, sid)
	if err != nil {
		t.Fatalf("重启后恢复不出请求前缀: %v（stderr=%s）", err, server.stderr.String())
	}
	if rec.Checkpoint.SessionID != sid {
		t.Fatalf("快照会话 id = %q，期望 %q", rec.Checkpoint.SessionID, sid)
	}
	if rec.Checkpoint.CoveredEvents != 4 {
		t.Fatalf("快照覆盖事件数 = %d，期望 4（种子 turn/start+user+assistant+turn/end）", rec.Checkpoint.CoveredEvents)
	}
	if rec.Checkpoint.Request == nil || rec.Checkpoint.Request.Text != prompt {
		t.Fatalf("侧车没有记录这次请求的身份：%+v", rec.Checkpoint.Request)
	}
	if rec.PrefixBytes != int64(len(before)) {
		t.Fatalf("请求前前缀 %d 字节与会话长度 %d 不一致", rec.PrefixBytes, len(before))
	}

	// 重启：同一份 home 必须还能正常起来，并且会话可以 load。
	restarted := startCheckpointServe(t, exe, home,
		"DSHD_SERVE_MODEL_RUNNER=1",
		"DSHD_LLM_BASE="+gateway.URL+"/v1",
	)
	restartedClient := connectPluginGlueServer(t, restarted)
	postPluginGlue(t, restartedClient, restarted.base, "session.load",
		map[string]any{"sessionId": sid}, http.StatusOK)
}

// startCheckpointServe 起一个**真的** dshd serve 子进程（`--addr 127.0.0.1:0`），
// 额外环境变量只作用于这个子进程（模型基址与内建 runner 开关）。
func startCheckpointServe(t *testing.T, exe, home string, extraEnv ...string) *pluginGlueServer {
	t.Helper()
	cmd := exec.Command(exe, "serve", "--home", home, "--addr", "127.0.0.1:0")
	cmd.Env = append(pluginGlueEnvironment(filepath.Join(t.TempDir(), "decoy")), extraEnv...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("建 serve stdout 管道: %v", err)
	}
	server := &pluginGlueServer{cmd: cmd, done: make(chan struct{})}
	cmd.Stderr = &server.stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("起真实 dshd serve: %v", err)
	}
	go func() {
		server.waitErr = cmd.Wait()
		close(server.done)
	}()
	t.Cleanup(server.stop)

	type readResult struct {
		line string
		err  error
	}
	read := make(chan readResult, 1)
	go func() {
		line, rerr := bufio.NewReader(stdout).ReadString('\n')
		read <- readResult{line: line, err: rerr}
	}()
	select {
	case got := <-read:
		if got.err != nil {
			server.stop()
			t.Fatalf("读 ready 行: %v stderr=%s", got.err, server.stderr.String())
		}
		if !strings.HasPrefix(got.line, "ready ") {
			server.stop()
			t.Fatalf("ready 行格式不对 %q stderr=%s", got.line, server.stderr.String())
		}
		var ready readyLine
		if uerr := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(got.line, "ready "))), &ready); uerr != nil {
			server.stop()
			t.Fatalf("ready JSON 解不出来: %v line=%q", uerr, got.line)
		}
		server.base, server.token = ready.URL, ready.Token
	case <-server.done:
		t.Fatalf("serve 在 ready 之前退出: %v stderr=%s", server.waitErr, server.stderr.String())
	case <-time.After(60 * time.Second):
		server.stop()
		t.Fatalf("等 serve ready 超时 stderr=%s", server.stderr.String())
	}
	if server.base == "" || server.token == "" {
		server.stop()
		t.Fatalf("ready 值不全: base=%q token-empty=%v", server.base, server.token == "")
	}
	go func() { _, _ = io.Copy(io.Discard, stdout) }()
	return server
}
