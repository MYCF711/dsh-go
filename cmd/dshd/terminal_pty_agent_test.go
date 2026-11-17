package main

// 官方 agent 终端分配（`openForAgent` / `agentLaunch`）在**真 PTY** 后端上的验证。
//
// 这里验的是"数据真的从官方那几行来的"，不是形状对不对：
//
//	origin/control/sandboxMode   官方 lib/index.js:1462-1467 的三个字段
//	env DSH_SESSION_ID           真的进了子 shell（用 `echo $DSH_SESSION_ID` 回读）
//	confine argv                 包装器返回的命令行**就是**起进程的那条（真跑一遍）
//	容量闸门                     官方 maxTerminals / maxAgentTerminals（:1219-1222 / :1391-1397）
//	控制权移交                   typed=true → control=user，之后 agent 关不掉（:612 / src/agent.ts:127-131）

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"dshgo/internal/transport"
	"dshgo/modules/exec/bash"
)

// skipWithoutConPTY 在非 Windows 上跳过（PTY 只在 Windows 有实现，
// `internal/terminal/pty_other.go` 如实回 ErrUnsupported）。
func skipWithoutConPTY(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("本宿主只有 Windows 上实现 ConPTY（internal/terminal/pty_other.go）")
	}
}

// agentReq 造一条合法的官方 AgentTerminalRequest（字段照 src/agent.ts:23-39）。
func agentReq(mode string) transport.OfficialAgentTerminalRequest {
	return transport.OfficialAgentTerminalRequest{
		Title:           "  Agent bash  ",
		Cols:            90,
		Rows:            25,
		Policy:          transport.OfficialSandboxExecutionPolicy{Mode: mode},
		PreferredShells: nil,
	}
}

// waitAgentScreen 轮询 attach 快照，直到出现 marker（真 PTY 的输出是异步的）。
func waitAgentScreen(t *testing.T, api *officialPTYTerminalAPI, sessionID, id, marker string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	last := ""
	for time.Now().Before(deadline) {
		snap, err := api.TerminalAttach(context.Background(), sessionID, id)
		if err != nil {
			t.Fatalf("attach 失败：%v", err)
		}
		last = snap.Screen
		if strings.Contains(last, marker) {
			return last
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("等不到输出 %q（最后 screen=%q）", marker, last)
	return last
}

// TestAgentTerminalLaunchOfficialShape 钉住官方 agentLaunch 产出的 info 三字段。
//
// 这是 GUI #54「Agent / 沙箱徽标」的数据源：origin/control/sandboxMode 一个都不能是
// 默认值填出来的，必须是这条分配路径写进去的真值。
func TestAgentTerminalLaunchOfficialShape(t *testing.T) {
	skipWithoutConPTY(t)
	cwd := t.TempDir()
	api, _ := newTestPTYTerminalAPI(t, cwd)

	info, err := api.TerminalOpenForAgent(context.Background(), "sess-agent", agentReq(transport.OfficialSandboxModeDangerFullAccess))
	if err != nil {
		t.Fatalf("agent 终端分配失败：%v", err)
	}
	t.Cleanup(func() { _ = api.TerminalClose(context.Background(), "sess-agent", info.ID) })

	// 官方三字段（lib/index.js:1464-1476）
	if info.Origin != transport.OfficialTerminalOriginAgent {
		t.Fatalf("origin 必须是 agent，拿到 %q", info.Origin)
	}
	if info.Control != transport.OfficialTerminalControlAgent {
		t.Fatalf("control 必须是 agent，拿到 %q", info.Control)
	}
	if info.SandboxMode != transport.OfficialSandboxModeDangerFullAccess {
		t.Fatalf("sandboxMode 必须等于 policy.mode（danger-full-access），拿到 %q", info.SandboxMode)
	}
	// 官方 agentLaunch 的其余字段（:1462-1467）
	if info.Title != "Agent bash" {
		t.Fatalf("title 要去首尾空白（官方 terminalTitle），拿到 %q", info.Title)
	}
	if info.Cwd != cwd {
		t.Fatalf("cwd 缺省要用会话工作目录 %q，拿到 %q", cwd, info.Cwd)
	}
	if info.Cols != 90 || info.Rows != 25 {
		t.Fatalf("尺寸要如实回填 90x25，拿到 %dx%d", info.Cols, info.Rows)
	}
	if info.State != "running" || info.ExitCode != nil {
		t.Fatalf("刚起的 agent 终端要 running 且 exitCode=null，拿到 %q / %v", info.State, info.ExitCode)
	}
	if !strings.HasPrefix(info.ID, officialAgentTerminalIDPrefix) {
		t.Fatalf("宿主生成的 id 要带可辨前缀 %q，拿到 %q", officialAgentTerminalIDPrefix, info.ID)
	}

	// 同一份 roster：侧栏看到的就是这条（terminal.list / watch 的数据源）。
	list, err := api.TerminalList("sess-agent")
	if err != nil {
		t.Fatalf("list 失败：%v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list 要有 1 条，拿到 %d", len(list))
	}
	if list[0].ID != info.ID || list[0].Origin != transport.OfficialTerminalOriginAgent ||
		list[0].SandboxMode != transport.OfficialSandboxModeDangerFullAccess {
		t.Fatalf("roster 里的行要与分配结果一致，拿到 %+v", list[0])
	}
}

// TestAgentTerminalEnvCarriesSessionID 是 env 的**真值**证据：
// 子 shell 里 `echo $DSH_SESSION_ID` 必须打出会话 id。
//
// 官方 `env = {...request.env, DSH_SESSION_ID: agent.id, …}`（lib/index.js:1456-1461）。
// 这条能真跑起来，靠的是 internal/terminal 的 ConPTY 环境块（此前一律继承父环境，
// 表达不了这份算好的环境）。
func TestAgentTerminalEnvCarriesSessionID(t *testing.T) {
	skipWithoutConPTY(t)
	cwd := t.TempDir()
	api, _ := newTestPTYTerminalAPI(t, cwd)

	req := agentReq(transport.OfficialSandboxModeDangerFullAccess)
	req.Env = map[string]string{"DSHD_AGENT_PROBE": "probe-value"}
	info, err := api.TerminalOpenForAgent(context.Background(), "sess-env", req)
	if err != nil {
		t.Fatalf("分配失败：%v", err)
	}
	t.Cleanup(func() { _ = api.TerminalClose(context.Background(), "sess-env", info.ID) })

	if err := api.Input("sess-env", info.ID, "echo AGENTID=$DSH_SESSION_ID PROBE=$DSHD_AGENT_PROBE\r"); err != nil {
		t.Fatalf("写输入失败：%v", err)
	}
	screen := waitAgentScreen(t, api, "sess-env", info.ID, "AGENTID=sess-env")
	if !strings.Contains(screen, "PROBE=probe-value") {
		t.Fatalf("request.env 也要进子进程环境（官方 {...request.env}），screen=%q", screen)
	}
	// 反过来：父进程环境的变量仍然在（base = os.Environ()，不是"只有算好的那几个"）。
	if os.Getenv("PATH") == "" {
		t.Skip("宿主 PATH 为空，跳过继承断言")
	}
	if err := api.Input("sess-env", info.ID, "echo HAS_PATH=${PATH:+yes}\r"); err != nil {
		t.Fatalf("写输入失败：%v", err)
	}
	waitAgentScreen(t, api, "sess-env", info.ID, "HAS_PATH=yes")
}

// TestAgentTerminalConfinedModeWithoutProviderFailsLoudly 钉住 confine 的**响亮失败**。
//
// 官方 `confine`（lib/index.js:1522-1530）：非全权档又拿不到 sandbox provider 就
// `throw`。本宿主没有 argv 级 OS 隔离，所以 read-only / workspace-write 的 agent
// 终端必须在这里失败——绝不能"标着 confined、其实全权"地跑起来。
func TestAgentTerminalConfinedModeWithoutProviderFailsLoudly(t *testing.T) {
	skipWithoutConPTY(t)
	cwd := t.TempDir()
	api, _ := newTestPTYTerminalAPI(t, cwd)

	for _, mode := range []string{transport.OfficialSandboxModeReadOnly, transport.OfficialSandboxModeWorkspaceWrite} {
		t.Run(mode, func(t *testing.T) {
			_, err := api.TerminalOpenForAgent(context.Background(), "sess-confined", agentReq(mode))
			if err == nil {
				t.Fatal("没有 argv 级 provider 时不许成功")
			}
			if !errors.Is(err, transport.ErrJTOfficialUnsupported) {
				t.Fatalf("要 ErrJTOfficialUnsupported（→501），拿到 %v", err)
			}
			for _, want := range []string{"sandbox provider", mode} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("错误要提到 %q（官方原文），拿到 %q", want, err.Error())
				}
			}
			// 没有留下半条终端：没有 entry，也就没有 conhost。
			list, lerr := api.TerminalList("sess-confined")
			if lerr != nil {
				t.Fatalf("list 失败：%v", lerr)
			}
			if len(list) != 0 {
				t.Fatalf("失败路径不许登记终端，拿到 %d 条：%+v", len(list), list)
			}
		})
	}
}

// scriptedConfiner 是 argv 级沙箱 provider 的替身：记录收到的 argv，
// 返回一条**包装后**的命令行（这里用 shell -c 打一个 marker，证明包装结果真的被执行了）。
type scriptedConfiner struct {
	seen   [][]string
	policy []transport.OfficialSandboxExecutionPolicy
	wrap   func(argv []string) []string
	calls  int
	err    error
}

func (c *scriptedConfiner) ConfineArgv(_ context.Context, argv []string, policy transport.OfficialSandboxExecutionPolicy) ([]string, error) {
	c.calls++
	c.seen = append(c.seen, append([]string{}, argv...))
	c.policy = append(c.policy, policy)
	if c.err != nil {
		return nil, c.err
	}
	return c.wrap(argv), nil
}

// TestAgentTerminalConfineResultIsWhatGetsLaunched 钉住"confine 的产物就是命令行"。
//
// 官方 `argv = await this.confine(agent, [shell.path, ...shell.args], request.policy, signal)`
// （lib/index.js:1452），spawn 用的就是这条 argv。这里让 provider 返回
// `<shell> -c "echo CONFINED-OK"`，证明宿主**没有**偷偷用未包装的 shell 起进程。
func TestAgentTerminalConfineResultIsWhatGetsLaunched(t *testing.T) {
	skipWithoutConPTY(t)
	cwd := t.TempDir()
	api, _ := newTestPTYTerminalAPI(t, cwd)
	confiner := &scriptedConfiner{wrap: func(argv []string) []string {
		return append(append([]string{}, argv[0]), "--noprofile", "--norc", "-c", "echo CONFINED-OK")
	}}
	api.confiner = confiner

	info, err := api.TerminalOpenForAgent(context.Background(), "sess-confine", agentReq(transport.OfficialSandboxModeWorkspaceWrite))
	if err != nil {
		t.Fatalf("有 provider 时 confined 档要能开：%v", err)
	}
	t.Cleanup(func() { _ = api.TerminalClose(context.Background(), "sess-confine", info.ID) })

	if confiner.calls != 1 {
		t.Fatalf("confine 必须被调用恰好一次，拿到 %d", confiner.calls)
	}
	// 官方 argv 就是 `[shell.path, ...shell.args]`；本仓默认 bash 的 args 是空
	// （不给 -i，理由见 shellArgsFor），所以这里只钉第一项必须是那个 shell。
	if len(confiner.seen[0]) == 0 || !strings.HasSuffix(strings.ToLower(confiner.seen[0][0]), "bash.exe") {
		t.Fatalf("confine 收到的 argv[0] 要是 shell（官方 :1452），拿到 %v", confiner.seen[0])
	}
	if got := confiner.policy[0]; got.Mode != transport.OfficialSandboxModeWorkspaceWrite {
		t.Fatalf("confine 要收到完整 policy（含 mode），拿到 %+v", got)
	}
	if info.SandboxMode != transport.OfficialSandboxModeWorkspaceWrite {
		t.Fatalf("sandboxMode 要是请求的档位，拿到 %q", info.SandboxMode)
	}
	waitAgentScreen(t, api, "sess-confine", info.ID, "CONFINED-OK")
}

// TestAgentTerminalCapacityGates 钉住官方两道容量闸门与它们的**官方错误码**。
func TestAgentTerminalCapacityGates(t *testing.T) {
	skipWithoutConPTY(t)
	cwd := t.TempDir()
	api, _ := newTestPTYTerminalAPI(t, cwd)
	api.maxAgentTerminals = 1

	first, err := api.TerminalOpenForAgent(context.Background(), "sess-cap", agentReq(transport.OfficialSandboxModeDangerFullAccess))
	if err != nil {
		t.Fatalf("第一条要成功：%v", err)
	}
	t.Cleanup(func() { _ = api.TerminalClose(context.Background(), "sess-cap", first.ID) })

	_, err = api.TerminalOpenForAgent(context.Background(), "sess-cap", agentReq(transport.OfficialSandboxModeDangerFullAccess))
	var agentErr *transport.OfficialAgentTerminalError
	if !errors.As(err, &agentErr) {
		t.Fatalf("超 agent 容量要回 *OfficialAgentTerminalError，拿到 %v", err)
	}
	if agentErr.Code != transport.OfficialAgentTerminalLimitReached {
		t.Fatalf("code 要 terminal/limit-reached（官方 lib/index.js:1222），拿到 %q", agentErr.Code)
	}
	if agentErr.Limit != 1 {
		t.Fatalf("limit 要如实报 1，拿到 %d", agentErr.Limit)
	}

	// 会话总容量是独立的闸门（官方 requireCapacity，:1391-1397）。
	api2, _ := newTestPTYTerminalAPI(t, t.TempDir())
	api2.maxTerminals = 1
	api2.maxAgentTerminals = 4
	userInfo, err := api2.TerminalCreate(context.Background(), "sess-total", transport.OfficialTerminalCreateRequest{ID: "user-1", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("先开一条用户终端：%v", err)
	}
	t.Cleanup(func() { _ = api2.TerminalClose(context.Background(), "sess-total", userInfo.ID) })
	_, err = api2.TerminalOpenForAgent(context.Background(), "sess-total", agentReq(transport.OfficialSandboxModeDangerFullAccess))
	if !errors.As(err, &agentErr) || agentErr.Code != transport.OfficialAgentTerminalLimitReached || agentErr.Limit != 1 {
		t.Fatalf("会话终端到顶要回 terminal/limit-reached（limit=1），拿到 %v", err)
	}
}

// TestAgentTerminalRequestValidation 钉住 title/尺寸的官方判据（含"越界是报错不是截断"）。
func TestAgentTerminalRequestValidation(t *testing.T) {
	skipWithoutConPTY(t)
	api, _ := newTestPTYTerminalAPI(t, t.TempDir())
	cases := []struct {
		name string
		mut  func(*transport.OfficialAgentTerminalRequest)
		want string
	}{
		{"空标题", func(r *transport.OfficialAgentTerminalRequest) { r.Title = "  " }, "title"},
		{"标题 121 字", func(r *transport.OfficialAgentTerminalRequest) { r.Title = strings.Repeat("题", 121) }, "title"},
		{"cols=1", func(r *transport.OfficialAgentTerminalRequest) { r.Cols = 1 }, "尺寸越界"},
		{"cols=501", func(r *transport.OfficialAgentTerminalRequest) { r.Cols = 501 }, "尺寸越界"},
		{"rows=0", func(r *transport.OfficialAgentTerminalRequest) { r.Rows = 0 }, "尺寸越界"},
		{"rows=201", func(r *transport.OfficialAgentTerminalRequest) { r.Rows = 201 }, "尺寸越界"},
		{"相对 cwd", func(r *transport.OfficialAgentTerminalRequest) { p := "sub"; r.Cwd = &p }, "绝对路径"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := agentReq(transport.OfficialSandboxModeDangerFullAccess)
			tc.mut(&req)
			_, err := api.TerminalOpenForAgent(context.Background(), "sess-bad", req)
			if !errors.Is(err, transport.ErrJTOfficialBadParam) {
				t.Fatalf("要 ErrJTOfficialBadParam（→400），拿到 %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误要提到 %q，拿到 %q", tc.want, err.Error())
			}
		})
	}
	// 边界值本身必须合法（官方上下限是闭区间）。
	req := agentReq(transport.OfficialSandboxModeDangerFullAccess)
	req.Cols, req.Rows = officialAgentMinCols, officialAgentMinRows
	info, err := api.TerminalOpenForAgent(context.Background(), "sess-edge", req)
	if err != nil {
		t.Fatalf("官方下限 2x1 必须合法：%v", err)
	}
	t.Cleanup(func() { _ = api.TerminalClose(context.Background(), "sess-edge", info.ID) })
}

// TestAgentTerminalTypedTakeoverBlocksAgentClose 钉住控制权移交的完整链路：
// typed=true → control=user（官方 :612）→ agent 关不掉（官方 src/agent.ts:127-131）。
func TestAgentTerminalTypedTakeoverBlocksAgentClose(t *testing.T) {
	skipWithoutConPTY(t)
	api, _ := newTestPTYTerminalAPI(t, t.TempDir())
	ctx := context.Background()

	info, err := api.TerminalOpenForAgent(ctx, "sess-take", agentReq(transport.OfficialSandboxModeDangerFullAccess))
	if err != nil {
		t.Fatalf("分配失败：%v", err)
	}

	// 人真的敲了键盘：typed=true。
	if _, err := api.InputTyped("sess-take", info.ID, "echo hi\r", true); err != nil {
		t.Fatalf("typed 写入失败：%v", err)
	}
	list, err := api.TerminalList("sess-take")
	if err != nil || len(list) != 1 {
		t.Fatalf("list 失败或条数不对：%v / %d", err, len(list))
	}
	if list[0].Control != transport.OfficialTerminalControlUser {
		t.Fatalf("typed 之后 control 要变 user（官方 lib/index.js:612），拿到 %q", list[0].Control)
	}
	// origin 永远不变（官方：origin 是"谁建的"，不是"谁在控制"）。
	if list[0].Origin != transport.OfficialTerminalOriginAgent {
		t.Fatalf("origin 要保持 agent，拿到 %q", list[0].Origin)
	}

	err = api.TerminalAgentClose(ctx, "sess-take", info.ID)
	var agentErr *transport.OfficialAgentTerminalError
	if !errors.As(err, &agentErr) || agentErr.Code != transport.OfficialAgentTerminalUserControl {
		t.Fatalf("人接管后 agent 关闭要被拒绝（user-control），拿到 %v", err)
	}
	// 终端还在（留给人）：list 仍有一条，且人关得掉。
	if again, lerr := api.TerminalList("sess-take"); lerr != nil || len(again) != 1 {
		t.Fatalf("拒绝之后终端必须还在：%v / %d", lerr, len(again))
	}
	if err := api.TerminalClose(ctx, "sess-take", info.ID); err != nil {
		t.Fatalf("人（宿主侧）关闭必须成功：%v", err)
	}
}

// TestAgentTerminalCloseWhileAgentControls 钉住"agent 还控制着时能关"。
func TestAgentTerminalCloseWhileAgentControls(t *testing.T) {
	skipWithoutConPTY(t)
	api, _ := newTestPTYTerminalAPI(t, t.TempDir())
	ctx := context.Background()
	info, err := api.TerminalOpenForAgent(ctx, "sess-close", agentReq(transport.OfficialSandboxModeDangerFullAccess))
	if err != nil {
		t.Fatalf("分配失败：%v", err)
	}
	if err := api.TerminalAgentClose(ctx, "sess-close", info.ID); err != nil {
		t.Fatalf("agent 控制时关闭要成功：%v", err)
	}
	list, err := api.TerminalList("sess-close")
	if err != nil {
		t.Fatalf("list 失败：%v", err)
	}
	if len(list) != 0 {
		t.Fatalf("关掉之后 list 要空，拿到 %+v", list)
	}
	// 重复关：官方 repeated closes succeed（终端已不在 → 404 而不是假成功）。
	if err := api.TerminalAgentClose(ctx, "sess-close", info.ID); err == nil {
		t.Fatal("已关闭的终端再关必须失败（不是静默成功）")
	}
}

// TestAgentTerminalPreferredShells 钉住官方 agentShell 的挑 shell 顺序。
func TestAgentTerminalPreferredShells(t *testing.T) {
	skipWithoutConPTY(t)
	api, _ := newTestPTYTerminalAPI(t, t.TempDir())
	ctx := context.Background()

	// ① 不存在的名字 → 落到环境默认 shell（官方 agentShell 的 fallback）。
	req := agentReq(transport.OfficialSandboxModeDangerFullAccess)
	req.PreferredShells = []string{"definitely-not-a-shell-xyz"}
	info, err := api.TerminalOpenForAgent(ctx, "sess-shell", req)
	if err != nil {
		t.Fatalf("未知 preferredShells 要回落默认 shell：%v", err)
	}
	defer func() { _ = api.TerminalClose(ctx, "sess-shell", info.ID) }()
	def, err := officialDefaultShell()
	if err != nil {
		t.Fatalf("取默认 shell 失败：%v", err)
	}
	if !strings.EqualFold(info.Shell.Path, def.Path) {
		t.Fatalf("回落要用默认 shell %q，拿到 %q", def.Path, info.Shell.Path)
	}

	// ② 真实存在的名字（大小写 + .exe 都要忽略，官方 findShell 语义）→ 选中它。
	if _, lookErr := exec.LookPath("cmd.exe"); lookErr != nil {
		t.Skip("本机没有 cmd.exe，跳过正向匹配")
	}
	req2 := agentReq(transport.OfficialSandboxModeDangerFullAccess)
	req2.Title = "cmd shell"
	req2.PreferredShells = []string{"CMD"}
	info2, err := api.TerminalOpenForAgent(ctx, "sess-shell2", req2)
	if err != nil {
		t.Fatalf("preferred=CMD 要能选中 cmd.exe：%v", err)
	}
	defer func() { _ = api.TerminalClose(ctx, "sess-shell2", info2.ID) }()
	if got := strings.ToLower(filepath.Base(info2.Shell.Path)); got != "cmd.exe" {
		t.Fatalf("preferred=CMD 要选中 cmd.exe，拿到 %q", info2.Shell.Path)
	}
}

// TestAgentTerminalHistoryRule 钉住官方 HISTFILE 规则表（lib/index.js:1454-1459）。
//
// 本宿主只在 Windows 上起 PTY，所以 posix 那一支没有生产路径；但**规则只有一份**：
// 这里把官方那张表逐格钉死，将来接 posix PTY 时不用重新推导。
func TestAgentTerminalHistoryRule(t *testing.T) {
	// Windows：两支都不成立（官方 `platform === "posix"` 才是前提）。
	for _, mode := range []string{
		transport.OfficialSandboxModeReadOnly,
		transport.OfficialSandboxModeWorkspaceWrite,
		transport.OfficialSandboxModeDangerFullAccess,
	} {
		env, release := officialAgentTerminalHistoryForPlatform("windows", mode, "t1")
		if env != nil || release != nil {
			t.Fatalf("windows + %s 不该有 HISTFILE/release，拿到 env=%v release=%t", mode, env, release != nil)
		}
	}
	// posix + danger-full-access：confined 为假 → 不加 HISTFILE。
	if env, release := officialAgentTerminalHistoryForPlatform("posix", transport.OfficialSandboxModeDangerFullAccess, "t1"); env != nil || release != nil {
		t.Fatalf("posix + danger-full-access 不该有 HISTFILE，拿到 env=%v release=%t", env, release != nil)
	}
	// posix + workspace-write：给出历史文件，并且 release 会真的删掉它。
	env, release := officialAgentTerminalHistoryForPlatform("posix", transport.OfficialSandboxModeWorkspaceWrite, "t1")
	if env["HISTFILE"] != "/tmp/dsh-agent-history-t1" {
		t.Fatalf("posix + workspace-write 的 HISTFILE 要是 /tmp/dsh-agent-history-t1，拿到 %q", env["HISTFILE"])
	}
	if release == nil {
		t.Fatal("posix + workspace-write 必须有 release（官方 :1466 删历史文件）")
	}
	// posix + read-only：confined 但不给历史（官方把 HISTFILE 置空串），且没有 release。
	env, release = officialAgentTerminalHistoryForPlatform("posix", transport.OfficialSandboxModeReadOnly, "t1")
	if value, ok := env["HISTFILE"]; !ok || value != "" {
		t.Fatalf("posix + read-only 的 HISTFILE 要是空串（存在但为空），拿到 %v", env)
	}
	if release != nil {
		t.Fatal("posix + read-only 不该有 release")
	}
}

// TestAgentTerminalEnvPrecedence 钉住官方 `{...request.env, DSH_SESSION_ID, ...history}`
// 的覆盖顺序（src/agent.ts:35-36：控制器的那两个变量优先级更高）。
func TestAgentTerminalEnvPrecedence(t *testing.T) {
	base := []string{"PATH=/usr/bin", "DSH_SESSION_ID=from-base"}
	requested := map[string]string{"DSH_SESSION_ID": "from-request", "HISTFILE": "from-request", "ZED": "z"}
	history := map[string]string{"HISTFILE": "/tmp/dsh-agent-history-x"}
	env := officialAgentTerminalEnv(base, requested, "sess-1", history)

	last := map[string]string{}
	for _, kv := range env {
		name, value, ok := strings.Cut(kv, "=")
		if !ok {
			t.Fatalf("环境项 %q 不是 KEY=VALUE", kv)
		}
		last[name] = value
	}
	// 注：ConPTY 环境块自己会做 last-wins 去重，所以这里只看"最后出现的那次"。
	if last["DSH_SESSION_ID"] != "sess-1" {
		t.Fatalf("DSH_SESSION_ID 要覆盖 request.env（最后出现的必须是会话 id），拿到 %q", last["DSH_SESSION_ID"])
	}
	if last["HISTFILE"] != "/tmp/dsh-agent-history-x" {
		t.Fatalf("HISTFILE 要覆盖 request.env（官方 :1456-1461），拿到 %q", last["HISTFILE"])
	}
	if last["ZED"] != "z" || last["PATH"] != "/usr/bin" {
		t.Fatalf("request.env 与 base 都要在，拿到 %v", last)
	}
	// 顺序断言：base < requested < DSH_SESSION_ID < history（决定 last-wins 的结果）。
	// idx 取**最后一次**出现的位置：ConPTY 环境块是 last-wins，覆盖关系体现在后者。
	idx := func(key string) int {
		at := -1
		for i, kv := range env {
			if strings.HasPrefix(kv, key+"=") {
				at = i
			}
		}
		return at
	}
	if !(idx("PATH") < idx("ZED") && idx("ZED") < idx("HISTFILE")) {
		t.Fatalf("覆盖顺序不对：PATH=%d ZED=%d HISTFILE=%d", idx("PATH"), idx("ZED"), idx("HISTFILE"))
	}
}

// TestOfficialShellKind 钉住官方 `shellKind`（src/shells.ts:44-46）：小写 + 去 .exe。
func TestOfficialShellKind(t *testing.T) {
	cases := map[string]string{
		`C:\Program Files\Git\bin\BASH.EXE`: "bash",
		"/usr/bin/Zsh":                      "zsh",
		"cmd.exe":                           "cmd",
		"pwsh":                              "pwsh",
	}
	for in, want := range cases {
		if got := officialShellKind(in); got != want {
			t.Fatalf("officialShellKind(%q) 要 %q，拿到 %q", in, want, got)
		}
	}
}

// TestAgentTerminalIDShape 钉住 id 的形状：官方 randomUUID() 的 v4 + 可辨前缀 + 能进既有围栏。
func TestAgentTerminalIDShape(t *testing.T) {
	id, err := officialAgentTerminalID()
	if err != nil {
		t.Fatalf("生成 id 失败：%v", err)
	}
	if !strings.HasPrefix(id, officialAgentTerminalIDPrefix) {
		t.Fatalf("id 要带前缀 %q，拿到 %q", officialAgentTerminalIDPrefix, id)
	}
	body := strings.TrimPrefix(id, officialAgentTerminalIDPrefix)
	if len(body) != 36 {
		t.Fatalf("UUID 体要 36 字符，拿到 %d：%q", len(body), body)
	}
	if body[14] != '4' {
		t.Fatalf("UUID 版本位要是 4，拿到 %q", body)
	}
	if !strings.ContainsAny(string(body[19]), "89ab") {
		t.Fatalf("UUID variant 位要是 8/9/a/b，拿到 %q", string(body[19]))
	}
	if !validJobActionPart(id) {
		t.Fatalf("id 必须能通过既有围栏 validJobActionPart：%q", id)
	}
	// 两次生成必然不同（randomUUID 语义）。
	other, err := officialAgentTerminalID()
	if err != nil || other == id {
		t.Fatalf("两次生成要不同：%q / %q（err=%v）", id, other, err)
	}
}

// TestAgentTerminalSurvivesRestartAsAgentPlaceholder 钉住"重启后的占位行仍是 agent 造的"。
//
// 宿主重启后进程早没了，但 origin（谁建的）与 sandboxMode（按哪一档起的）仍是事实；
// control 则确实回到了人手里（没有活的 agent 了）。不落盘 origin 的话，恢复出来的
// 行会被 `aligned()` 补成 user——界面上就是"agent 终端重启后变用户终端"的假历史。
func TestAgentTerminalSurvivesRestartAsAgentPlaceholder(t *testing.T) {
	skipWithoutConPTY(t)
	home := t.TempDir()
	cwd := t.TempDir()
	svc := bash.NewService(cwd)
	t.Cleanup(svc.Close)
	api, err := newOfficialPTYTerminalAPIWith(home, svc,
		func(string) (string, error) { return cwd, nil }, 1000, false)
	if err != nil {
		t.Fatalf("起 PTY 终端层失败：%v", err)
	}
	ctx := context.Background()
	info, err := api.TerminalOpenForAgent(ctx, "sess-restart", agentReq(transport.OfficialSandboxModeDangerFullAccess))
	if err != nil {
		t.Fatalf("分配失败：%v", err)
	}
	// retain 才落盘（布局的写入口就在这里）。
	if err := api.TerminalRetain(ctx, "sess-restart", info.ID); err != nil {
		t.Fatalf("retain 失败：%v", err)
	}
	api.Close()

	svc2 := bash.NewService(cwd)
	t.Cleanup(svc2.Close)
	api2, err := newOfficialPTYTerminalAPI(home, svc2,
		func(string) (string, error) { return cwd, nil }, 1000)
	if err != nil {
		t.Fatalf("重建 PTY 终端层失败：%v", err)
	}
	t.Cleanup(api2.Close)
	list, err := api2.TerminalList("sess-restart")
	if err != nil {
		t.Fatalf("list 失败：%v", err)
	}
	if len(list) != 1 || list[0].ID != info.ID {
		t.Fatalf("重启后要恢复 1 条被保留的终端 %q，拿到 %+v", info.ID, list)
	}
	got := list[0]
	if got.State != "exited" {
		t.Fatalf("重启后进程已不存在，state 必须 exited（不许假装 running），拿到 %q", got.State)
	}
	if got.Origin != transport.OfficialTerminalOriginAgent {
		t.Fatalf("重启后 origin 要保持 agent（谁建的不会因为进程没了而变），拿到 %q", got.Origin)
	}
	if got.Control != transport.OfficialTerminalControlUser {
		t.Fatalf("重启后 control 要回到 user（没有活的 agent 了），拿到 %q", got.Control)
	}
	if got.SandboxMode != transport.OfficialSandboxModeDangerFullAccess {
		t.Fatalf("重启后 sandboxMode 要保持 danger-full-access，拿到 %q", got.SandboxMode)
	}
}
