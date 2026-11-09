package main

// 官方 job.* / terminal.* 的**组装层 + 真流**验证。
//
// 分工：
//   - `terminal_pty_test.go` 验 HS-08 的 PTY 后端原语（create/attach 快照/退出码/501）；
//   - 本文件验 **HS-16D 的交付面**：
//   - `newOfficialJobTerminalService` 现在接的是**真 PTY** 后端（不是管道式）；
//   - `job.list` / `job.follow` / `terminal.attach` / `terminal.retain` 是**真流**
//     （帧机 + 真数据源），不是一次性帧；
//   - A/B 会话围栏、取消、自然退出、进程树回收都在真实链上。
//
// 纪律：
//   - 只用 t.TempDir() 里的临时 home 与临时工作目录；
//   - 只回收本测试自己启动的进程（socket 会话由 bash.Service 关）；
//   - 不杀任何既有 DSH/dshd 进程，不写安装目录。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"dshgo/internal/officialwire"
	"dshgo/internal/transport"
	"dshgo/modules/exec/bash"
)

// newTestOfficialService 建一套**组装好的**官方服务面（真 PTY + 真 broker 可选）。
func newTestOfficialService(t *testing.T, cwd string, broker *jobActionBroker, roster func(string) []transport.JobInfo) (*officialJobTerminalService, string) {
	t.Helper()
	home := t.TempDir()
	svc := bash.NewService(cwd)
	t.Cleanup(svc.Close)
	jt, err := newOfficialJobTerminalService(home, svc, broker, roster,
		func(string) (string, error) { return cwd, nil })
	if err != nil {
		t.Fatalf("组装官方服务面失败：%v", err)
	}
	t.Cleanup(jt.Close)
	return jt, home
}

// drainStream 把一条帧机拉干（带超时，防挂死）。
func drainStream(t *testing.T, stream transport.OfficialFrameStream, timeout time.Duration) []map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var frames []map[string]any
	for {
		raw, done, fault := stream.Next(ctx)
		if fault != nil {
			t.Fatalf("流故障：%s（code=%s status=%d）", fault.Message, fault.Code, fault.Status)
		}
		if done {
			return frames
		}
		var frame map[string]any
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatalf("帧不是 JSON：%v（raw=%s）", err, raw)
		}
		frames = append(frames, frame)
	}
}

// nextFrame 取一帧（带超时）。
func nextFrame(t *testing.T, stream transport.OfficialFrameStream, timeout time.Duration) (map[string]any, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	raw, done, fault := stream.Next(ctx)
	if fault != nil {
		t.Fatalf("流故障：%s（code=%s）", fault.Message, fault.Code)
	}
	if done {
		return nil, true
	}
	var frame map[string]any
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("帧不是 JSON：%v（raw=%s）", err, raw)
	}
	return frame, false
}

// ---------------------------------------------------------------------------
// 接线：官方服务面必须落在真 PTY 后端 + 真流能力上
// ---------------------------------------------------------------------------

func TestOfficialJobTerminal_ServiceUsesPTYBackendAndStreams(t *testing.T) {
	cwd := t.TempDir()
	jt, home := newTestOfficialService(t, cwd, nil, nil)

	// 1) terminal 后端是 PTY（布局目录是 terminals-pty，不是旧的 terminals）。
	if _, err := os.Stat(filepath.Join(home, "state", "terminals-pty")); err != nil {
		t.Fatalf("官方 terminal.* 必须接真 PTY 后端（缺 %s）：%v",
			filepath.Join(home, "state", "terminals-pty"), err)
	}
	if _, err := os.Stat(filepath.Join(home, "state", "terminals")); err == nil {
		t.Fatal("管道式终端布局目录仍然存在：说明旧的管道实现还在被使用")
	}

	// 2) 四个流式方法必须有真流能力（否则 wire 只能 501）。
	var svc transport.OfficialJobTerminalService = jt
	streams, ok := svc.(transport.OfficialJobTerminalStreams)
	if !ok || streams == nil {
		t.Fatal("组装面必须实现 transport.OfficialJobTerminalStreams（job.list/follow、terminal.attach/retain 真流）")
	}

	// 3) create 起的必须是真 ConPTY 会话（PID 真实、可写、有真实游标）。
	info, err := jt.TerminalCreate(context.Background(), "sess-a", transport.OfficialTerminalCreateRequest{
		ID: "t1", Cols: 100, Rows: 30,
	})
	if err != nil {
		t.Fatalf("create 失败：%v", err)
	}
	if info.State != "running" || info.Cols != 100 || info.Rows != 30 || info.Cwd != cwd {
		t.Fatalf("PTY 终端字段不对：%+v", info)
	}
	entry, err := jt.lookup("sess-a", "t1", "create")
	if err != nil {
		t.Fatal(err)
	}
	if entry.term == nil || entry.term.PTY() == nil {
		t.Fatal("官方 terminal.* 必须持有真 PTY 会话（*terminal.Session）")
	}
	if entry.term.PTY().PID() <= 0 {
		t.Fatalf("PTY 会话必须有真实 PID，拿到 %d", entry.term.PTY().PID())
	}
}

// ---------------------------------------------------------------------------
// terminal.create / input / attach 真流 / close 真回收
// ---------------------------------------------------------------------------

func TestOfficialJobTerminal_TerminalCreateRunsRealShell(t *testing.T) {
	cwd := t.TempDir()
	jt, _ := newTestOfficialService(t, cwd, nil, nil)
	ctx := context.Background()

	info, err := jt.TerminalCreate(ctx, "sess-a", transport.OfficialTerminalCreateRequest{
		ID: "t1", Cols: 120, Rows: 40,
	})
	if err != nil {
		t.Fatalf("create 失败：%v", err)
	}
	if info.State != "running" {
		t.Fatalf("刚起的终端 state 要 running，拿到 %q", info.State)
	}
	if info.ExitCode != nil {
		t.Fatalf("运行中的终端 exitCode 必须是 null，拿到 %v", *info.ExitCode)
	}
	if info.Cwd != cwd {
		t.Fatalf("cwd 要如实回填（%q），拿到 %q", cwd, info.Cwd)
	}
	if _, err := os.Stat(info.Shell.Path); err != nil {
		t.Fatalf("shell path 必须是真实存在的可执行文件 %q：%v", info.Shell.Path, err)
	}

	// attach 是真流：首帧 snapshot，之后 output 帧带真实字节。
	stream, err := jt.OpenTerminalAttach(ctx, "sess-a", "t1")
	if err != nil {
		t.Fatalf("attach 打开失败：%v", err)
	}
	defer stream.Close()
	snapshot, done := nextFrame(t, stream, 15*time.Second)
	if done || snapshot["type"] != "snapshot" {
		t.Fatalf("首帧必须是 snapshot，拿到 %v", snapshot)
	}
	if writable, _ := snapshot["writable"].(bool); !writable {
		t.Fatal("活着的终端 snapshot 必须 writable=true")
	}
	// 刚起的终端还没有输出：sequence 允许是 0（"已写字节数"确实为 0）。
	if seq, ok := snapshot["sequence"].(float64); !ok || seq < 0 {
		t.Fatalf("snapshot.sequence 必须是真实已写字节数（>=0），拿到 %v", snapshot["sequence"])
	}
	if _, has := snapshot["oneShot"]; has {
		t.Fatalf("snapshot 帧不许带 oneShot（旧残口）：%v", snapshot)
	}

	marker := fmt.Sprintf("OFFICIAL-REAL-%d", time.Now().UnixNano())
	if err := jt.Input("sess-a", "t1", "echo "+marker+"\n"); err != nil {
		t.Fatalf("写终端失败：%v", err)
	}
	var sawMarker bool
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && !sawMarker {
		frame, done := nextFrame(t, stream, 30*time.Second)
		if done {
			t.Fatal("attach 流在收到真实输出之前就结束了")
		}
		if frame["type"] == "output" {
			data, _ := frame["data"].(string)
			if strings.Contains(data, marker) {
				sawMarker = true
				if seq, _ := frame["sequence"].(float64); seq <= 0 {
					t.Fatalf("output.sequence 必须是真实绝对偏移（>0），拿到 %v", frame["sequence"])
				}
			}
		}
	}
	if !sawMarker {
		t.Fatal("attach 流没有把真实输出推下来")
	}
}

func TestOfficialJobTerminal_TerminalAttachStateFrameOnExit(t *testing.T) {
	cwd := t.TempDir()
	jt, _ := newTestOfficialService(t, cwd, nil, nil)
	ctx := context.Background()
	if _, err := jt.TerminalCreate(ctx, "sess-a", transport.OfficialTerminalCreateRequest{ID: "t1", Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	stream, err := jt.OpenTerminalAttach(ctx, "sess-a", "t1")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if frame, done := nextFrame(t, stream, 15*time.Second); done || frame["type"] != "snapshot" {
		t.Fatalf("首帧必须是 snapshot，拿到 %v", frame)
	}
	if err := jt.Input("sess-a", "t1", "exit 3\n"); err != nil {
		t.Fatal(err)
	}
	// 自然退出：流必须以 state 帧结束，且 exitCode 是真实值（不是编的 0）。
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		frame, done := nextFrame(t, stream, 30*time.Second)
		if done {
			t.Fatal("attach 流在 state 帧之前结束了")
		}
		if frame["type"] != "state" {
			continue
		}
		info, _ := frame["info"].(map[string]any)
		if info == nil {
			t.Fatalf("state 帧必须带 info：%v", frame)
		}
		if state, _ := info["state"].(string); state != "exited" {
			t.Fatalf("state 帧的 info.state 必须是 exited，拿到 %q", state)
		}
		if code, ok := info["exitCode"].(float64); !ok || int(code) != 3 {
			t.Fatalf("state 帧必须带真实 exitCode=3，拿到 %v", info["exitCode"])
		}
		if writable, _ := frame["writable"].(bool); writable {
			t.Fatal("已退出的终端 state 帧必须 writable=false")
		}
		return
	}
	t.Fatal("30s 内没有等到 state 终态帧")
}

func TestOfficialJobTerminal_TerminalListCrossSession(t *testing.T) {
	cwd := t.TempDir()
	jt, _ := newTestOfficialService(t, cwd, nil, nil)
	ctx := context.Background()
	if _, err := jt.TerminalCreate(ctx, "sess-a", transport.OfficialTerminalCreateRequest{ID: "t1", Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	listA, err := jt.TerminalList("sess-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(listA) != 1 || listA[0].ID != "t1" {
		t.Fatalf("A 会话要看到自己的 t1：%+v", listA)
	}
	listB, err := jt.TerminalList("sess-b")
	if err != nil {
		t.Fatal(err)
	}
	if len(listB) != 0 {
		t.Fatalf("B 会话不许看到 A 的终端：%+v", listB)
	}

	// 跨会话的 attach 流：**打开时**不出错，首帧就是 403 故障（流语义）。
	stream, err := jt.OpenTerminalAttach(ctx, "sess-b", "t1")
	if err != nil {
		t.Fatalf("打开流本身不该报错（故障在首帧）：%v", err)
	}
	defer stream.Close()
	if _, _, fault := stream.Next(context.Background()); fault == nil || fault.Status != 403 {
		t.Fatalf("跨会话 attach 必须 403 故障，拿到 %+v", fault)
	}
	if err := jt.TerminalClose(ctx, "sess-b", "t1"); !errors.Is(err, transport.ErrCrossSession) {
		t.Fatalf("跨会话 close 必须是 cross_session，拿到 %v", err)
	}
	if err := jt.TerminalRename(ctx, "sess-b", "t1", "偷改"); !errors.Is(err, transport.ErrCrossSession) {
		t.Fatalf("跨会话 rename 必须是 cross_session，拿到 %v", err)
	}
	listA, _ = jt.TerminalList("sess-a")
	if listA[0].Title == "偷改" {
		t.Fatal("跨会话 rename 居然生效了")
	}
}

func TestOfficialJobTerminal_TerminalCloseKillsProcessTree(t *testing.T) {
	cwd := t.TempDir()
	jt, _ := newTestOfficialService(t, cwd, nil, nil)
	ctx := context.Background()
	if _, err := jt.TerminalCreate(ctx, "sess-a", transport.OfficialTerminalCreateRequest{ID: "t1", Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}

	beat := filepath.Join(cwd, "hb")
	readBeat := func() string {
		raw, err := os.ReadFile(beat)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(raw))
	}

	// 先被动等提示符（只读，不写），再发心跳命令：ConPTY 在 shell 初始化时
	// 会冲掉早到的输入（HS-08 层行为；官方客户端也是 attach 拿到 snapshot 之后
	// 才写）。本测试验的是**进程树回收**，不把启动时序判成产品失败。
	promptDeadline := time.Now().Add(60 * time.Second) // 显式上限：8 agent 负载下 shell 启动可能 >10s
	for {
		out, err := jt.Output("sess-a", "t1", 0)
		if err != nil {
			t.Fatalf("读终端失败：%v", err)
		}
		if strings.Contains(out.Output, "$ ") {
			break
		}
		if time.Now().After(promptDeadline) {
			t.Fatalf("等不到 shell 提示符（60s，单调时钟），测试无效（screen=%q）", out.Output)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// 后台循环，每 100ms 覆盖一次时间戳；它属于这条 shell 的进程树。
	// 命令保持短（< 50 字节）：ConPTY 输入层对长行有已知丢字问题（见 RESULT 的
	// 交接发现）；本测试只验进程树回收，不把它变成"长命令能不能粘贴"的门。
	cmd := "while :;do date +%s%N>hb;sleep .1;done&\n"
	if err := jt.Input("sess-a", "t1", cmd); err != nil {
		t.Fatalf("起心跳失败：%v", err)
	}

	// 条件驱动：等到心跳文件出现（**不是**等固定时长；60s 只是明确的失败上限）。
	waitFirst := func() (string, bool) {
		deadline := time.Now().Add(60 * time.Second) // 单调时钟
		for {
			if v := readBeat(); v != "" {
				return v, true
			}
			if !time.Now().Before(deadline) {
				return "", false
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	first, ok := waitFirst()
	if !ok {
		out, _ := jt.Output("sess-a", "t1", 0)
		t.Fatalf("心跳文件没出现（60s）：后台循环没跑起来，测试无效（screen=%q）", out.Output)
	}
	// 强断言：必须在动（100ms 心跳，5s 内至少变一次）。
	changeDeadline := time.Now().Add(5 * time.Second)
	changed := false
	for time.Now().Before(changeDeadline) {
		if readBeat() != first {
			changed = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !changed {
		t.Fatal("心跳没在动：测试无效")
	}

	if err := jt.TerminalClose(ctx, "sess-a", "t1"); err != nil {
		t.Fatalf("close 失败：%v", err)
	}
	// 官方语义：重复 close 成功。
	if err := jt.TerminalClose(ctx, "sess-a", "t1"); err != nil {
		t.Fatalf("重复 close 必须成功（官方语义），拿到 %v", err)
	}

	// 进程树全部终止的**强断言**：心跳文件必须停止变化——连续 600ms 内值不变
	// 才算停；15s 只是失败上限（不会把"还在写"当成通过，也不加长 sleep 充数）。
	last := readBeat()
	stableSince := time.Now()
	stopDeadline := time.Now().Add(15 * time.Second)
	for {
		time.Sleep(50 * time.Millisecond)
		now := readBeat()
		if now != last {
			last = now
			stableSince = time.Now()
		} else if time.Since(stableSince) >= 600*time.Millisecond {
			break
		}
		if time.Now().After(stopDeadline) {
			t.Fatalf("close 之后进程树还在写心跳（15s 内未稳定）：%q → %q", last, now)
		}
	}
	if err := jt.Input("sess-a", "t1", "echo x\n"); !errors.Is(err, transport.ErrJTOfficialTerminalGone) {
		t.Fatalf("关掉的终端不许还能写，拿到 %v", err)
	}
	if list, err := jt.TerminalList("sess-a"); err != nil || len(list) != 0 {
		t.Fatalf("close 之后 list 不许再有它：%v %+v", err, list)
	}
}

// ---------------------------------------------------------------------------
// terminal.retain：retained 帧 + 保持打开 + 真落盘
// ---------------------------------------------------------------------------

func TestOfficialJobTerminal_TerminalRetainStreamHoldsAndPersists(t *testing.T) {
	cwd := t.TempDir()
	jt, home := newTestOfficialService(t, cwd, nil, nil)
	ctx := context.Background()
	if _, err := jt.TerminalCreate(ctx, "sess-a", transport.OfficialTerminalCreateRequest{ID: "t1", Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	marker := fmt.Sprintf("RETAIN-%d", time.Now().UnixNano())
	if err := jt.Input("sess-a", "t1", "echo "+marker+"\n"); err != nil {
		t.Fatal(err)
	}
	// 等真实输出进滚动缓冲（retain 的 screen 尾部要含它）。
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if out, err := jt.Output("sess-a", "t1", 0); err == nil && strings.Contains(out.Output, marker) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := jt.TerminalRename(ctx, "sess-a", "t1", "构建日志"); err != nil {
		t.Fatalf("rename 失败：%v", err)
	}

	stream, err := jt.OpenTerminalRetain(ctx, "sess-a", "t1")
	if err != nil {
		t.Fatalf("retain 打开失败：%v", err)
	}
	defer stream.Close()
	frame, done := nextFrame(t, stream, 15*time.Second)
	if done || frame["type"] != "retained" {
		t.Fatalf("首帧必须是 retained，拿到 %v", frame)
	}

	// 落盘是真实文件，不是内存里的旗标。
	layout := filepath.Join(home, "state", "terminals-pty", "sess-a.json")
	raw, err := os.ReadFile(layout)
	if err != nil {
		t.Fatalf("retain 之后必须有落盘布局 %s：%v", layout, err)
	}
	var persisted persistedTerminalLayout
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatalf("布局不是合法 JSON：%v", err)
	}
	if len(persisted.Terminals) != 1 || persisted.Terminals[0].Title != "构建日志" || !persisted.Terminals[0].Retained {
		t.Fatalf("落盘内容不对：%+v", persisted.Terminals)
	}
	if !strings.Contains(persisted.Terminals[0].Screen, marker) {
		t.Fatalf("retain 要留下屏幕尾部（含 %q），实际 %q", marker, persisted.Terminals[0].Screen)
	}

	// 关键：retained 之后流必须**保持打开**，直到取消。
	holdCtx, cancel := context.WithCancel(context.Background())
	waiter := make(chan bool, 1)
	go func() {
		_, done, _ := stream.Next(holdCtx)
		waiter <- done
	}()
	select {
	case <-waiter:
		t.Fatal("retained 帧之后流立刻结束了：retain 必须保持打开（open lifetime stream）")
	case <-time.After(300 * time.Millisecond):
	}
	cancel()
	select {
	case done := <-waiter:
		if !done {
			t.Fatal("取消之后 retain 流必须正常结束")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("取消之后 retain 流没有结束（泄漏连接）")
	}

	// 宿主重启：同一 home 上的新实例应当恢复被保留的身份。
	svc2 := bash.NewService(cwd)
	defer svc2.Close()
	api2, err := newOfficialPTYTerminalAPI(home, svc2,
		func(string) (string, error) { return cwd, nil }, 1000)
	if err != nil {
		t.Fatal(err)
	}
	defer api2.Close()
	list, err := api2.TerminalList("sess-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "t1" || list[0].Title != "构建日志" {
		t.Fatalf("重启后要恢复 1 条被保留的终端：%+v", list)
	}
	if list[0].State != "exited" {
		t.Fatalf("重启后进程已不存在，state 必须是 exited（不许假装 running），拿到 %q", list[0].State)
	}
}

// ---------------------------------------------------------------------------
// environment / shells / 尺寸
// ---------------------------------------------------------------------------

func TestOfficialJobTerminal_TerminalEnvironmentReal(t *testing.T) {
	cwd := t.TempDir()
	jt, _ := newTestOfficialService(t, cwd, nil, nil)
	env, err := jt.TerminalEnvironment(context.Background(), "sess-a")
	if err != nil {
		t.Fatal(err)
	}
	if env.Cwd != cwd {
		t.Fatalf("cwd 要来自会话真值（%q），拿到 %q", cwd, env.Cwd)
	}
	if env.MaxInputBytes != officialTerminalMaxInputBytes {
		t.Fatalf("maxInputBytes 要等于真实强制值 %d，拿到 %d", officialTerminalMaxInputBytes, env.MaxInputBytes)
	}
	if env.MaxBufferedBytes != officialTerminalMaxScreenBytes {
		t.Fatalf("maxBufferedBytes 要等于真实快照上限 %d，拿到 %d", officialTerminalMaxScreenBytes, env.MaxBufferedBytes)
	}
	if env.MaxCols != officialTerminalMaxCols || env.MaxRows != officialTerminalMaxRows {
		t.Fatalf("maxCols/maxRows 不对：%d×%d", env.MaxCols, env.MaxRows)
	}
	// 真 PTY 才有行滚动缓冲：官方默认 1000 行。报 0 就是"管道式"（旧残口）。
	if env.Scrollback <= 0 {
		t.Fatalf("真 PTY 必须报真实 scrollback（>0），拿到 %d", env.Scrollback)
	}
}

func TestOfficialJobTerminal_TerminalShellsAreProbed(t *testing.T) {
	cwd := t.TempDir()
	jt, _ := newTestOfficialService(t, cwd, nil, nil)
	shells, err := jt.TerminalShells(context.Background(), "sess-a")
	if err != nil {
		t.Fatalf("shells 失败：%v", err)
	}
	if len(shells) == 0 {
		t.Fatal("至少要报出默认 shell")
	}
	def := shells[0]
	if def.Supported == nil || !*def.Supported {
		t.Fatalf("第一条必须是可用的默认 shell：%+v", def)
	}
	for i, shell := range shells {
		if shell.Supported == nil {
			t.Fatalf("第 %d 条缺 supported：%+v", i, shell)
		}
		want := i == 0 || runtime.GOOS == "windows"
		if *shell.Supported != want {
			t.Fatalf("%s 的 supported=%v，按 create 判据应当是 %v", shell.Path, *shell.Supported, want)
		}
	}
	for _, shell := range shells {
		if _, err := os.Stat(shell.Path); err != nil {
			t.Fatalf("报出来的 shell 必须真实存在：%s（%v）", shell.Path, err)
		}
		if shell.Args == nil {
			t.Fatalf("args 必须是数组：%+v", shell)
		}
	}
	// 默认 shell 必须能真跑（否则"可用"就是假的）。
	if _, err := jt.TerminalCreate(context.Background(), "sess-a",
		transport.OfficialTerminalCreateRequest{ID: "probe", Cols: 80, Rows: 24}); err != nil {
		t.Fatalf("默认 shell 起不来：%v", err)
	}
}

func TestOfficialJobTerminal_TerminalCreateOtherShell(t *testing.T) {
	cmdPath, err := exec.LookPath("cmd.exe")
	if err != nil {
		t.Skip("这台机器上没有 cmd.exe")
	}
	cwd := t.TempDir()
	jt, _ := newTestOfficialService(t, cwd, nil, nil)
	ctx := context.Background()
	info, err := jt.TerminalCreate(ctx, "sess-a", transport.OfficialTerminalCreateRequest{
		ID: "c1", Cols: 80, Rows: 24, ShellPath: cmdPath,
	})
	if err != nil {
		t.Fatalf("用 cmd.exe 建终端失败：%v", err)
	}
	if !strings.EqualFold(filepath.Base(info.Shell.Path), "cmd.exe") {
		t.Fatalf("回填的 shell 应当是被选中的 cmd.exe，拿到 %q", info.Shell.Path)
	}
	// `ver` 只有 cmd 认；bash 认不出这个命令。
	if err := jt.Input("sess-a", "c1", "ver\r\n"); err != nil {
		t.Fatalf("写 cmd 失败：%v", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		out, err := jt.Output("sess-a", "c1", 0)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.ToLower(out.Output), "microsoft") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("cmd 没真的跑起来（没有 Microsoft 版本行）")
}

func TestOfficialJobTerminal_TerminalInputLimit(t *testing.T) {
	cwd := t.TempDir()
	jt, _ := newTestOfficialService(t, cwd, nil, nil)
	ctx := context.Background()
	if _, err := jt.TerminalCreate(ctx, "sess-a", transport.OfficialTerminalCreateRequest{ID: "t1", Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("x", officialTerminalMaxInputBytes+1)
	if err := jt.Input("sess-a", "t1", big); !errors.Is(err, transport.ErrJTOfficialBadParam) {
		t.Fatalf("超过 maxInputBytes 必须拒绝，拿到 %v", err)
	}
}

func TestOfficialJobTerminal_TerminalCreateIdempotent(t *testing.T) {
	cwd := t.TempDir()
	jt, _ := newTestOfficialService(t, cwd, nil, nil)
	ctx := context.Background()
	req := transport.OfficialTerminalCreateRequest{ID: "t1", Cols: 80, Rows: 24}
	first, err := jt.TerminalCreate(ctx, "sess-a", req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := jt.TerminalCreate(ctx, "sess-a", req)
	if err != nil {
		t.Fatalf("同参数重复 create 必须成功（幂等）：%v", err)
	}
	if first.Shell.Path != second.Shell.Path || first.ID != second.ID {
		t.Fatalf("幂等返回的身份不一致：%+v vs %+v", first, second)
	}
	if list, _ := jt.TerminalList("sess-a"); len(list) != 1 {
		t.Fatalf("幂等 create 不许起第二条会话，列表 %d 条", len(list))
	}
	if _, err := jt.TerminalCreate(ctx, "sess-a", transport.OfficialTerminalCreateRequest{ID: "t1", Cols: 100, Rows: 40}); !errors.Is(err, transport.ErrJTOfficialBadParam) {
		t.Fatalf("同 id 换参数必须 fail-loud，拿到 %v", err)
	}
	if err := jt.TerminalClose(ctx, "sess-a", "t1"); err != nil {
		t.Fatal(err)
	}
	if _, err := jt.TerminalCreate(ctx, "sess-a", req); !errors.Is(err, transport.ErrJTOfficialTerminalGone) {
		t.Fatalf("已关闭的身份不许复活，拿到 %v", err)
	}
	if _, err := jt.TerminalCreate(ctx, "sess-b", req); err != nil {
		t.Fatalf("另一个会话用同一 id 必须能建：%v", err)
	}
}

// ---------------------------------------------------------------------------
// job.*：包在**真实** jobActionBroker 之上
// ---------------------------------------------------------------------------

// writeRetainedJob 写既有 jobs 服务认的保留输出文件（`readRetained` 的路径）。
func writeRetainedJob(t *testing.T, home, sessionID, id, text string, done bool, exitCode int) {
	t.Helper()
	dir := filepath.Join(home, "state", "jobs", sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	out := transport.JobOutput{ID: id, SessionID: sessionID, Output: text, Done: done, ExitCode: exitCode, Total: len(text)}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOfficialJobTerminal_JobListFencesSessions(t *testing.T) {
	roster := func(sessionID string) []transport.JobInfo {
		return []transport.JobInfo{
			{ID: "job-a1", SessionID: "sess-a", Command: "npm test\n--watch", Done: false, Bytes: 42},
			{ID: "job-b1", SessionID: "sess-b", Command: "sleep 1", Done: true, ExitCode: 0, Bytes: 7},
			{ID: "job-free", SessionID: "", Command: "unowned", Done: true, ExitCode: 3, Bytes: 0},
		}
	}
	svc := &officialJobService{roster: roster}
	frame, err := svc.JobList("sess-a")
	if err != nil {
		t.Fatal(err)
	}
	if frame.Type != "rows" {
		t.Fatalf("type 必须是 rows，拿到 %q", frame.Type)
	}
	ids := []string{}
	for _, job := range frame.Jobs {
		ids = append(ids, job.ID)
	}
	if len(ids) != 2 || ids[0] != "job-a1" || ids[1] != "job-free" {
		t.Fatalf("A 会话只该看到自己的 + 未归属的：%v", ids)
	}
	running := frame.Jobs[0]
	if running.Status != "running" || running.Owner != "sess-a" || running.Kind != "shell" {
		t.Fatalf("运行中任务的换算不对：%+v", running)
	}
	if running.Label != "npm test" {
		t.Fatalf("label 要压成一行（命令首行），拿到 %q", running.Label)
	}
	if running.Output.Total != 42 {
		t.Fatalf("output.total 要等于写出字节数 42，拿到 %d", running.Output.Total)
	}
	if running.StartedAt != 0 {
		t.Fatalf("既有服务没有登记时间，startedAt 必须如实报 0，拿到 %d", running.StartedAt)
	}
	failed := frame.Jobs[1]
	if failed.Owner != "" {
		t.Fatalf("未归属任务不许乱填 owner：%+v", failed)
	}
	if failed.Status != "failed" || failed.Detail != "exit code: 3" {
		t.Fatalf("非零退出要如实成 failed + detail：%+v", failed)
	}
}

// TestOfficialJobTerminal_JobFollowStreamsRealBroker 用**真实** jobActionBroker
// 验 follow 帧机的 opened → output → status 与绝对字节偏移。
func TestOfficialJobTerminal_JobFollowStreamsRealBroker(t *testing.T) {
	home := t.TempDir()
	broker := newJobActionBroker(home)
	roster := func(string) []transport.JobInfo {
		return []transport.JobInfo{{ID: "job-1", SessionID: "sess-a", Command: "echo hi", Done: true, ExitCode: 0, Bytes: len("hello\nworld\n")}}
	}
	jt, _ := newTestOfficialJobTerminalWithHome(t, home, broker, roster)
	writeRetainedJob(t, home, "sess-a", "job-1", "hello\nworld\n", true, 0)
	ctx := context.Background()

	stream, err := jt.OpenJobFollow(ctx, "sess-a", "job-1", -1)
	if err != nil {
		t.Fatalf("follow 打开失败：%v", err)
	}
	defer stream.Close()
	frames := drainStream(t, stream, 15*time.Second)
	if len(frames) != 3 {
		t.Fatalf("opened + output + status 恰好三帧，拿到 %d：%v", len(frames), frames)
	}
	if frames[0]["type"] != "opened" {
		t.Fatalf("第一帧必须是 opened，拿到 %v", frames[0])
	}
	if from, _ := frames[0]["from"].(float64); from != 0 {
		t.Fatalf("省略 from 时 opened.from 必须是最旧保留字节 0，拿到 %v", frames[0]["from"])
	}
	if frames[1]["type"] != "output" {
		t.Fatalf("第二帧必须是 output，拿到 %v", frames[1])
	}
	chunks, _ := frames[1]["chunks"].([]any)
	if len(chunks) != 1 {
		t.Fatalf("output 帧要 1 个 chunk，拿到 %v", frames[1]["chunks"])
	}
	chunk, _ := chunks[0].(map[string]any)
	if text, _ := chunk["text"].(string); text != "hello\nworld\n" {
		t.Fatalf("chunk.text 要是完整保留输出，拿到 %q", text)
	}
	if at, _ := chunk["at"].(float64); at != 0 {
		t.Fatalf("chunk.at 必须从 0 起，拿到 %v", chunk["at"])
	}
	if next, _ := frames[1]["next"].(float64); next != float64(len("hello\nworld\n")) {
		t.Fatalf("next 要等于读到的字节数，拿到 %v", frames[1]["next"])
	}
	if frames[2]["type"] != "status" {
		t.Fatalf("终态帧必须是 status，拿到 %v", frames[2])
	}
	job, _ := frames[2]["job"].(map[string]any)
	if job == nil || job["status"] != "completed" {
		t.Fatalf("status 帧必须带已完成 job：%v", frames[2])
	}

	// 从末尾再读：opened.from 就是末尾，然后立刻 status。
	tail, err := jt.OpenJobFollow(ctx, "sess-a", "job-1", int64(len("hello\nworld\n")))
	if err != nil {
		t.Fatal(err)
	}
	defer tail.Close()
	tailFrames := drainStream(t, tail, 15*time.Second)
	if len(tailFrames) != 2 || tailFrames[0]["type"] != "opened" || tailFrames[1]["type"] != "status" {
		t.Fatalf("从末尾读要 opened + status，拿到 %v", tailFrames)
	}
	if from, _ := tailFrames[0]["from"].(float64); from != float64(len("hello\nworld\n")) {
		t.Fatalf("opened.from 要等于请求的末尾，拿到 %v", tailFrames[0]["from"])
	}

	// 请求超过末尾 → 停在末尾（不编造数据）。
	over, err := jt.OpenJobFollow(ctx, "sess-a", "job-1", 999)
	if err != nil {
		t.Fatal(err)
	}
	defer over.Close()
	overFrames := drainStream(t, over, 15*time.Second)
	if len(overFrames) != 2 || overFrames[1]["type"] != "status" {
		t.Fatalf("越界 from 应当回 opened + status，拿到 %v", overFrames)
	}
}

// newTestOfficialJobTerminalWithHome 用**指定 home** 组装（job.* 要读保留输出文件）。
func newTestOfficialJobTerminalWithHome(t *testing.T, home string, broker *jobActionBroker, roster func(string) []transport.JobInfo) (*officialJobTerminalService, error) {
	t.Helper()
	cwd := t.TempDir()
	svc := bash.NewService(cwd)
	t.Cleanup(svc.Close)
	jt, err := newOfficialJobTerminalService(home, svc, broker, roster,
		func(string) (string, error) { return cwd, nil })
	if err != nil {
		t.Fatalf("组装官方服务面失败：%v", err)
	}
	t.Cleanup(jt.Close)
	return jt, nil
}

// TestOfficialJobTerminal_JobKillReusesRealBroker 验 job.kill 的 409/404 围栏。
func TestOfficialJobTerminal_JobKillReusesRealBroker(t *testing.T) {
	home := t.TempDir()
	broker := newJobActionBroker(home)
	jt, _ := newTestOfficialJobTerminalWithHome(t, home, broker, func(string) []transport.JobInfo { return nil })
	writeRetainedJob(t, home, "sess-a", "job-1", "hello\n", true, 0)
	ctx := context.Background()

	// 已结束 → 既有服务回 ErrJobFinished（传输层据此 409）。
	if _, err := jt.JobKill(ctx, "sess-a", "job-1"); !errors.Is(err, transport.ErrJobFinished) {
		t.Fatalf("已结束的任务要 ErrJobFinished，拿到 %v", err)
	}
	// 不存在 → ErrNoSuchJob。
	if _, err := jt.JobKill(ctx, "sess-a", "job-missing"); !errors.Is(err, transport.ErrNoSuchJob) {
		t.Fatalf("不存在的任务要 ErrNoSuchJob，拿到 %v", err)
	}
	// follow 不存在 → 首帧故障 404（不是空流）。
	stream, err := jt.OpenJobFollow(ctx, "sess-a", "job-missing", -1)
	if err != nil {
		t.Fatalf("打开流本身不该报错（故障在首帧）：%v", err)
	}
	defer stream.Close()
	if _, _, fault := stream.Next(ctx); fault == nil || fault.Status != 404 {
		t.Fatalf("不存在的 job 必须 404 故障，拿到 %+v", fault)
	}
}

// TestOfficialJobTerminal_JobKillCrossSession 用真实 broker 的"别的会话有活进程"分支证 403。
func TestOfficialJobTerminal_JobKillCrossSession(t *testing.T) {
	if _, err := exec.LookPath("cmd.exe"); err != nil {
		t.Skip("这台机器上没有 cmd.exe")
	}
	home := t.TempDir()
	broker := newJobActionBroker(home)
	jt, _ := newTestOfficialJobTerminalWithHome(t, home, broker, func(string) []transport.JobInfo { return nil })

	// 起一个**本测试自己的**子进程，并把它登记成 sess-b 的宿主进程。
	cmdPath, _ := exec.LookPath("cmd.exe")
	child := exec.Command(cmdPath, "/c", "timeout", "/t", "30")
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatalf("起测试子进程失败：%v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	run, err := broker.registerRun("sess-b", child.Process.Pid, cmdPath, stdin)
	if err != nil {
		t.Fatalf("登记测试进程失败：%v", err)
	}
	defer broker.unregisterRun(run)

	// A 会话没有任何活进程，却想杀 job-x：既有服务判成 cross_session。
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := jt.JobKill(ctx, "sess-a", "job-x"); !errors.Is(err, transport.ErrCrossSession) {
		t.Fatalf("跨会话杀任务必须 ErrCrossSession，拿到 %v", err)
	}
}

// ---------------------------------------------------------------------------
// attach 的官方 uplink（write / resize）—— mux 面用
// ---------------------------------------------------------------------------

// TestOfficialJobTerminal_TerminalAttachUplink 证明 attach 流的**上行**是真的：
// write 真的写进 PTY、resize 真的改子进程窗口、每帧都回官方 ack；
// 已关闭的终端回 refusal=not-running（不是静默丢输入）。
func TestOfficialJobTerminal_TerminalAttachUplink(t *testing.T) {
	cwd := t.TempDir()
	jt, _ := newTestOfficialService(t, cwd, nil, nil)
	ctx := context.Background()
	if _, err := jt.TerminalCreate(ctx, "sess-a", transport.OfficialTerminalCreateRequest{ID: "t1", Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	stream, err := jt.OpenTerminalAttach(ctx, "sess-a", "t1")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	up, ok := stream.(transport.OfficialTerminalUplink)
	if !ok {
		t.Fatal("attach 流必须实现 transport.OfficialTerminalUplink（官方 uplink 的 write/resize）")
	}
	if frame, done := nextFrame(t, stream, 15*time.Second); done || frame["type"] != "snapshot" {
		t.Fatalf("首帧必须是 snapshot，拿到 %v", frame)
	}

	// write：真的写进 PTY。
	marker := fmt.Sprintf("UPLINK-%d", time.Now().UnixNano())
	writeFrame, err := json.Marshal(map[string]any{"type": "write", "sequence": 1, "data": "echo " + marker + "\n"})
	if err != nil {
		t.Fatal(err)
	}
	if err := up.Push(writeFrame); err != nil {
		t.Fatalf("write 帧被拒绝：%v", err)
	}
	ack, done := nextFrame(t, stream, 15*time.Second)
	if done || ack["type"] != "ack" {
		t.Fatalf("write 之后必须先回 ack，拿到 %v", ack)
	}
	if seq, ok := ack["sequence"].(float64); !ok || seq != 1 {
		t.Fatalf("ack.sequence 必须回显上行帧序号 1（官方逐帧推进），拿到 %v", ack["sequence"])
	}
	if _, has := ack["refusal"]; has {
		t.Fatalf("接受的写入不许带 refusal：%v", ack)
	}
	// 输出真的产生了（ack 之后是 output 帧）。
	deadline := time.Now().Add(30 * time.Second)
	var saw bool
	for time.Now().Before(deadline) && !saw {
		frame, done := nextFrame(t, stream, 30*time.Second)
		if done {
			t.Fatal("uplink 写入之后流提前结束了")
		}
		if data, _ := frame["data"].(string); strings.Contains(data, marker) {
			saw = true
		}
	}
	if !saw {
		t.Fatal("uplink 写入没有真的进 PTY（没看到 echo 回显）")
	}

	// resize：真的改子进程窗口（stty size 回读）。
	resizeFrame, err := json.Marshal(map[string]any{"type": "resize", "sequence": 2, "cols": 140, "rows": 45})
	if err != nil {
		t.Fatal(err)
	}
	if err := up.Push(resizeFrame); err != nil {
		t.Fatalf("resize 帧被拒绝：%v", err)
	}
	if ack, done := nextFrame(t, stream, 15*time.Second); done || ack["type"] != "ack" {
		t.Fatalf("resize 之后必须先回 ack，拿到 %v", ack)
	}
	if err := up.Push(json.RawMessage(`{"type":"write","sequence":1,"data":"stty size\n"}`)); err != nil {
		t.Fatalf("write 帧被拒绝：%v", err)
	}
	deadline = time.Now().Add(30 * time.Second)
	var sized bool
	for time.Now().Before(deadline) && !sized {
		frame, done := nextFrame(t, stream, 30*time.Second)
		if done {
			t.Fatal("resize 验证时流提前结束")
		}
		if data, _ := frame["data"].(string); strings.Contains(data, "45 140") {
			sized = true
		}
	}
	if !sized {
		t.Fatal("resize 没有真的作用在子进程上（stty size 不是 45 140）")
	}

	// 非法帧必须拒绝（不静默丢输入）。
	if err := up.Push(json.RawMessage(`{"type":"bogus"}`)); !errors.Is(err, transport.ErrJTOfficialBadParam) {
		t.Fatalf("非法 uplink 帧必须 400 语义，拿到 %v", err)
	}

	// 已关闭的终端：write 被拒绝，且回一帧 refusal=not-running 的 ack。
	if err := jt.TerminalClose(ctx, "sess-a", "t1"); err != nil {
		t.Fatal(err)
	}
	closedFrame, err := json.Marshal(map[string]any{"type": "write", "sequence": 4, "data": "echo x" + "\n"})
	if err != nil {
		t.Fatal(err)
	}
	if err := up.Push(closedFrame); !errors.Is(err, transport.ErrJTOfficialTerminalGone) {
		t.Fatalf("已关闭终端的 write 必须报 gone，拿到 %v", err)
	}
	ack, done = nextFrame(t, stream, 15*time.Second)
	if done || ack["type"] != "ack" {
		t.Fatalf("拒绝也要回 ack，拿到 %v", ack)
	}
	if refusal, _ := ack["refusal"].(string); refusal != "not-running" {
		t.Fatalf("已关闭终端的 ack.refusal 必须是 not-running，拿到 %q", refusal)
	}
}

// ---------------------------------------------------------------------------
// job.list：whole-set rows 真流（打开一帧 + 只在变化时发；没有自然结束）
// ---------------------------------------------------------------------------

func jobIDsOf(t *testing.T, frame map[string]any) []string {
	t.Helper()
	rows, _ := frame["jobs"].([]any)
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		job, _ := row.(map[string]any)
		id, _ := job["id"].(string)
		ids = append(ids, id)
	}
	return ids
}

func TestOfficialJobTerminal_JobListStreamsRealRoster(t *testing.T) {
	cwd := t.TempDir()
	var mu sync.Mutex
	jobs := []transport.JobInfo{
		{ID: "job-a1", SessionID: "sess-a", Command: "npm test\n--watch", Bytes: 42},
		{ID: "job-b1", SessionID: "sess-b", Command: "sleep 1", Done: true, Bytes: 7},
		{ID: "job-free", SessionID: "", Command: "unowned", Done: true, ExitCode: 3},
	}
	roster := func(sessionID string) []transport.JobInfo {
		mu.Lock()
		defer mu.Unlock()
		out := make([]transport.JobInfo, 0, len(jobs))
		for _, info := range jobs {
			if info.SessionID == "" || info.SessionID == sessionID {
				out = append(out, info)
			}
		}
		return out
	}
	jt, _ := newTestOfficialService(t, cwd, nil, roster)
	stream, err := jt.OpenJobList(context.Background(), "sess-a")
	if err != nil {
		t.Fatalf("OpenJobList 失败：%v", err)
	}
	defer stream.Close()

	first, done := nextFrame(t, stream, 5*time.Second)
	if done || first["type"] != "rows" {
		t.Fatalf("job.list 首帧必须是 whole-set rows，拿到 %v（done=%v）", first, done)
	}
	if ids := jobIDsOf(t, first); len(ids) != 2 || ids[0] != "job-a1" || ids[1] != "job-free" {
		t.Fatalf("A 会话只该看到自己的 + 未归属的（整份），拿到 %v", ids)
	}
	if rows, _ := first["jobs"].([]any); len(rows) == 2 {
		job, _ := rows[0].(map[string]any)
		if job["status"] != "running" || job["label"] != "npm test" || job["startedAt"] != float64(0) {
			t.Fatalf("running 行的换算不对：%v", job)
		}
	}

	// 没有变化：**不许**发帧。定时重复同一份就是假流。
	if frame, done := nextFrame(t, stream, 250*time.Millisecond); !done {
		t.Fatalf("可见集合没变时不许发帧（假流）：%v", frame)
	}

	// 生命周期提交：job-a1 结算，job-a2 出现。轮询窗口里应合并成**一整份**新帧。
	mu.Lock()
	jobs[0].Done = true
	jobs[0].ExitCode = 0
	jobs = append(jobs, transport.JobInfo{ID: "job-a2", SessionID: "sess-a", Command: "go test", Bytes: 0})
	mu.Unlock()

	second, done := nextFrame(t, stream, 5*time.Second)
	if done || second["type"] != "rows" {
		t.Fatalf("生命周期提交后必须再发一整份 rows，拿到 %v（done=%v）", second, done)
	}
	if ids := jobIDsOf(t, second); len(ids) != 3 || ids[0] != "job-a1" || ids[1] != "job-free" || ids[2] != "job-a2" {
		t.Fatalf("变更帧必须是整份替换（不是增量），拿到 %v", ids)
	}
	if rows, _ := second["jobs"].([]any); len(rows) == 3 {
		job, _ := rows[0].(map[string]any)
		if job["status"] != "completed" || job["detail"] != "exit code: 0" {
			t.Fatalf("结算后的状态没进帧：%v", job)
		}
	}

	// 关流：必须正常结束（官方：没有自然结束，载体关闭才结束）。
	stream.Close()
	if _, done := nextFrame(t, stream, 2*time.Second); !done {
		t.Fatal("Close 之后必须结束，而不是继续发帧")
	}
}

// serve.go 生产装配走 newOfficialPTYJobTerminalService。它挂到 Options 上的
// Handler().Svc 必须真的实现四条流能力——否则四条流会在 wire 上静默 501。
func TestOfficialJobTerminal_ProductionPTYServiceHasStreams(t *testing.T) {
	cwd := t.TempDir()
	home := t.TempDir()
	svc := bash.NewService(cwd)
	t.Cleanup(svc.Close)
	jt, err := newOfficialPTYJobTerminalService(home, svc, nil,
		func(string) []transport.JobInfo { return nil },
		func(string) (string, error) { return cwd, nil }, 100)
	if err != nil {
		t.Fatalf("生产装配失败：%v", err)
	}
	t.Cleanup(jt.Close)

	service := jt.Handler().Svc
	streams, ok := service.(transport.OfficialJobTerminalStreams)
	if !ok || streams == nil {
		t.Fatal("生产接线（newOfficialPTYJobTerminalService）必须实现 transport.OfficialJobTerminalStreams；" +
			"serve.go 当前就挂这个类型，缺了它 job.list/follow、terminal.attach/retain 会全部 501")
	}
	stream, err := streams.OpenJobList(context.Background(), "sess-a")
	if err != nil {
		t.Fatalf("生产接线的 OpenJobList 失败：%v", err)
	}
	defer stream.Close()
	if frame, done := nextFrame(t, stream, 5*time.Second); done || frame["type"] != "rows" {
		t.Fatalf("生产接线的 job.list 首帧必须是 rows，拿到 %v（done=%v）", frame, done)
	}
}

// ---------------------------------------------------------------------------
// 既有 terminal.input/output 的官方 PTY 回落（HTTP 侧上行）
// ---------------------------------------------------------------------------

func TestOfficialJobTerminal_InputOutputByIDFallback(t *testing.T) {
	cwd := t.TempDir()
	jt, _ := newTestOfficialService(t, cwd, nil, nil)
	ctx := context.Background()
	if _, err := jt.TerminalCreate(ctx, "sess-a", transport.OfficialTerminalCreateRequest{ID: "t1", Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}

	// 既有 terminal.input 只给 id：必须能写进官方 PTY 终端。
	if err := jt.InputByID("t1", "echo hi > out.txt\n"); err != nil {
		t.Fatalf("InputByID 必须路由到官方 PTY：%v", err)
	}
	target := filepath.Join(cwd, "out.txt")
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(target); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("官方 PTY 没有真的执行 InputByID 的写入（缺 %s）", target)
		}
		time.Sleep(50 * time.Millisecond)
	}

	out, err := jt.OutputByID("t1", 0)
	if err != nil {
		t.Fatalf("OutputByID 必须能读官方 PTY：%v", err)
	}
	if out.Next <= 0 {
		t.Fatalf("OutputByID 的游标必须是真实字节数，拿到 %+v", out)
	}

	// 没有这个 id：明确 gone（调用方据此回落既有管道终端，而不是当 500）。
	if err := jt.InputByID("no-such-terminal", "x"); !errors.Is(err, transport.ErrJTOfficialTerminalGone) {
		t.Fatalf("未知 id 必须是 ErrJTOfficialTerminalGone，拿到 %v", err)
	}

	// 跨会话撞名：不猜（猜错就是往别人会话的终端打字），按 400 语义拒绝。
	if _, err := jt.TerminalCreate(ctx, "sess-b", transport.OfficialTerminalCreateRequest{ID: "t1", Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	if err := jt.InputByID("t1", "x"); !errors.Is(err, transport.ErrJTOfficialBadParam) {
		t.Fatalf("跨会话撞名必须拒绝（ErrJTOfficialBadParam），拿到 %v", err)
	}
}

// ---------------------------------------------------------------------------
// mux 上行适配：attach 帧机 → officialwire.UplinkStream
// ---------------------------------------------------------------------------

type stubUplinkFrameStream struct {
	pushes []json.RawMessage
	err    error
}

func (s *stubUplinkFrameStream) Next(ctx context.Context) (json.RawMessage, bool, *transport.OfficialStreamFault) {
	<-ctx.Done()
	return nil, true, nil
}

func (s *stubUplinkFrameStream) Close() {}

func (s *stubUplinkFrameStream) Push(raw json.RawMessage) error {
	if s.err != nil {
		return s.err
	}
	s.pushes = append(s.pushes, raw)
	return nil
}

type stubPlainFrameStream struct{}

func (stubPlainFrameStream) Next(ctx context.Context) (json.RawMessage, bool, *transport.OfficialStreamFault) {
	<-ctx.Done()
	return nil, true, nil
}

func (stubPlainFrameStream) Close() {}

func TestOfficialJobTerminal_MuxUplinkAdapter(t *testing.T) {
	up := &stubUplinkFrameStream{}
	wrapped := wrapOfficialMuxUplinkStream(&officialMuxStream{stream: up})
	uplink, ok := wrapped.(officialwire.UplinkStream)
	if !ok {
		t.Fatal("实现 transport.OfficialTerminalUplink 的 attach 流必须被包成 officialwire.UplinkStream（mux uplink 只有这一条路）")
	}
	if err := uplink.Push(json.RawMessage(`{"type":"write","sequence":1,"data":"x"}`)); err != nil {
		t.Fatalf("合法上行不许被拒绝：%v", err)
	}
	if len(up.pushes) != 1 {
		t.Fatalf("上行必须真的转给帧机，拿到 %d 次", len(up.pushes))
	}

	// 拒绝必须变成 gateway/bad-request 错误帧，不能吞掉。
	bad := &stubUplinkFrameStream{err: fmt.Errorf("%w: 非法 uplink 帧", transport.ErrJTOfficialBadParam)}
	badWrapped := wrapOfficialMuxUplinkStream(&officialMuxStream{stream: bad})
	badUplink, ok := badWrapped.(officialwire.UplinkStream)
	if !ok {
		t.Fatal("拒绝型流也必须被包成 UplinkStream（否则输入被静默丢弃）")
	}
	if err := badUplink.Push(json.RawMessage(`{"type":"bogus"}`)); err == nil {
		t.Fatal("被帧机拒绝的上行必须返回错误帧")
	} else if err.Code != "gateway/bad-request" {
		t.Fatalf("拒绝的错误码要是 gateway/bad-request，拿到 %q", err.Code)
	}

	// 没有上行能力的流：原样返回，不冒充 UplinkStream（mux 对它们仍回"不接受上行"）。
	plain := wrapOfficialMuxUplinkStream(&officialMuxStream{stream: stubPlainFrameStream{}})
	if _, ok := plain.(officialwire.UplinkStream); ok {
		t.Fatal("没有上行能力的流不许被包成 UplinkStream（会改变 mux 的既有错误语义）")
	}
	if plain == nil {
		t.Fatal("wrap 不许把非 uplink 流变成 nil")
	}
}
