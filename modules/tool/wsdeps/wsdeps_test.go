package wsdeps

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"dshgo/internal/spi"
)

// ───────────────────────── 假解释器夹具 ─────────────────────────
//
// 为什么要把**测试二进制自己**拷成 PATH 上的 `python`：本轮要证的是「探针没跑完」
// 和「python 没装／跑不起来」**必须给出三种不同结果**。前者只有在探针真的超时
// （真子进程、真 ctx 掐断）时才有意义，而这个包又不该为了测试把产品代码插件化。
// 把测试二进制当假解释器，就能在不改产品逻辑的前提下同时做到：
//
//   - 真子进程、真 kill：产品的 runOnce 走的是同一条 exec 路径；
//   - 预算由测试注入毫秒级（Tool.budgets），**产品预算一个数字都没动**；
//   - 单进程，掐断一定干净。
//
// 为什么不用 .bat/.cmd 垫片：Windows 上 CommandContext 只杀 cmd.exe，垫片里
// `sleep` 的子进程会继续攥着 stdout，测试就变成在测管道，而不是在测产品。
const (
	envFakePython = "WSDEP_FAKE_PYTHON" // 假解释器模式（只对子进程有意义）
	envFakeMarker = "WSDEP_FAKE_MARKER" // 「第一次跑」的标记文件（slow-once 用）

	fakeSleep    = "sleep"     // 永远跑不完：每次都被预算掐断
	fakeSlowOnce = "slow-once" // 第一次跑不完（冷启动），之后正常
	fakeCrash    = "crash"     // 立刻非零退出（装了但跑不起来）
)

// fakeVersionJSON 是假解释器正常时吐的东西；用 9.9.9 这种显然假的版本，
// 避免与真机 python 撞上、也避免有人把夹具值当成真版本。
const fakeVersionJSON = `{"version":"9.9.9"}`

func TestMain(m *testing.M) {
	// 被当成 `python -c ...` / `node --version` 调用时走假解释器分支，否则正常跑测试。
	// 必须赶在 m.Run() 之前：m.Run 会解析命令行，而产品的探针参数在测试二进制眼里
	// 是非法 flag。
	if mode := os.Getenv(envFakePython); mode != "" {
		os.Exit(fakePython(mode))
	}
	os.Exit(m.Run())
}

// fakePython 扮演一个被探针调用的解释器/运行时。
func fakePython(mode string) int {
	switch mode {
	case fakeSleep:
		// 比任何测试预算都长；正常路径上会被产品的 ctx 掐断，走不到下一行。
		time.Sleep(30 * time.Second)
		return 0

	case fakeSlowOnce:
		marker := os.Getenv(envFakeMarker)
		f, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_ = f.Close()
			// 第一次：扮演「冷启动越过预算」，被产品掐断。
			time.Sleep(30 * time.Second)
			return 0
		}
		// 第二次开始：热了，正常应答。
		fmt.Println(fakeVersionJSON)
		return 0

	case fakeCrash:
		fmt.Fprintln(os.Stderr, "Fatal Python error: init_fs_encoding: failed to get the Python codec of the filesystem encoding")
		return 1
	}
	return 2
}

// copyFakeBinary 把测试二进制拷成 dir 下的 name（Windows 补 .exe）并返回路径。
func copyFakeBinary(t *testing.T, dir, name string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("拿不到测试二进制路径：%v", err)
	}
	raw, err := os.ReadFile(self)
	if err != nil {
		t.Fatalf("读测试二进制失败：%v", err)
	}
	if runtime.GOOS == "windows" {
		// Windows 靠 PATHEXT 找可执行文件；只认 .exe（.bat 是另一个故事，见上）。
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(dir, name), raw, 0o755); err != nil {
		t.Fatalf("装假可执行文件 %s 失败：%v", name, err)
	}
}

// installFakePython 把测试二进制装成 PATH 上**唯一**的 `python`（node/pnpm 因此也不在），
// 并返回它所在目录。
//
// 只换 PATH 就够：产品用 exec.LookPath 找 python，走的是同一份 PATH；不改产品、
// 不动本机真的 python、也不写任何全局配置。
func installFakePython(t *testing.T, mode string) string {
	t.Helper()
	dir := t.TempDir()
	copyFakeBinary(t, dir, "python")
	t.Setenv(envFakePython, mode)
	t.Setenv("PATH", dir)
	return dir
}

// installFakeBinaryOnPath 把假可执行文件放到**现有 PATH 最前面**：
// 真的 python/node/pnpm 仍然找得到，只劫持点名的那一个。
func installFakeBinaryOnPath(t *testing.T, name, mode string) {
	t.Helper()
	dir := t.TempDir()
	copyFakeBinary(t, dir, name)
	t.Setenv(envFakePython, mode)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// 假解释器测试用的预算。只给测试用，产品预算（20s/30s/10s/20s）在
// defaultBudgets 里，一个数字都没改。
//
// 两个数必须分开，因为假解释器一次调用的两种结局代价差 20 倍（本机实测）：
//   - 被掐断：进程起来 48ms 后就睡，预算一到就死；
//   - 正常跑完 **且干净退出**：-race 下光是这个 10MB 二进制的正常退场就要
//     ~1.06s（非 race ~50ms）。预算比它小，第二次探针也会被杀，"重试成功"
//     永远测不出来——第一版就是这么在 -race 下红的。
const (
	fakeColdBudget = 400 * time.Millisecond // 只用来「掐断一个正在睡的探针」
	fakeHotBudget  = 3 * time.Second        // 要盖住 -race 下一次干净退出的成本
)

// fakeBudgets 是假解释器测试的预算：version 取给的数，其余压到毫秒级。
func fakeBudgets(version time.Duration) *budgets {
	return &budgets{
		version: version,
		library: 2 * time.Second,
		node:    300 * time.Millisecond,
		pnpm:    300 * time.Millisecond,
	}
}

// decodeDeps 解析模型看到的正文。**失败结果也要能解析**：没查完这种失败恰恰
// 要把「谁查到了、谁没查到」交给模型，正文不许空。
func decodeDeps(t *testing.T, res spi.Result) deps {
	t.Helper()
	if strings.TrimSpace(res.Text) == "" {
		t.Fatalf("Text 不能空——模型看的是它（Summary=%q Hint=%q）", res.Summary, res.Hint)
	}
	var d deps
	if err := json.Unmarshal([]byte(res.Text), &d); err != nil {
		t.Fatalf("Text 不是合法 JSON：%v\n%s", err, res.Text)
	}
	return d
}

func contains(list []string, want string) bool {
	for _, got := range list {
		if got == want {
			return true
		}
	}
	return false
}

// assertComponent 钉住「status 与 next 必须成套出现」：状态说是什么，动作说怎么办。
// 这两个字段说两套话，比原来那个 ok:false 更难查——所以每条断言都一起看。
func assertComponent(t *testing.T, label string, c component, wantStatus probeStatus, wantNext nextAction) {
	t.Helper()
	if c.Status != wantStatus {
		t.Fatalf("%s 的 status 应当是 %q，实际 %q", label, wantStatus, c.Status)
	}
	if c.Next != wantNext {
		t.Fatalf("%s（status=%q）的 next 应当是 %q，实际 %q——状态和下一步必须成套",
			label, wantStatus, wantNext, c.Next)
	}
}

// ───────────────────────── 真实环境 ─────────────────────────

// 正常路径：这台机器上有 python（本仓开发机就有），返回里必须给出路径与版本，
// 而且**库的键名是包名**（python-docx 而不是 docx），模型照着装包才对得上。
//
// 四种 status 都要处理对：unknown **不是**「没装」，不许 skip；broken 也不是
// （python 在但跑不起来，是真实环境问题，要红）；只有确认 missing 才允许跳过。
func TestExecReportsPython(t *testing.T) {
	res, err := (&Tool{}).Exec(context.Background(), json.RawMessage(`{}`))
	d := decodeDeps(t, res)

	// node/pnpm/库的形状是契约的一部分：每一项都必须说清自己是什么状态。
	for name, status := range map[string]probeStatus{
		"node": d.Node.Status, "pnpm": d.Pnpm.Status, "libraries": d.LibrariesStatus,
	} {
		switch status {
		case StatusReady, StatusMissing, StatusBroken, StatusUnknown, StatusSkipped:
		default:
			t.Fatalf("%s 的 status 必须是 ready/missing/broken/unknown/skipped 之一，实际 %q", name, status)
		}
	}

	switch d.Python.Status {
	case StatusReady:
		if d.Python.Path == "" || d.Python.Version == "" {
			t.Fatalf("ready 就必须给路径与版本：%s", res.Text)
		}
		if d.Python.Next != "" {
			t.Fatalf("ready 没有下一步可做，next 必须省略，实际 %q", d.Python.Next)
		}
		if err != nil {
			t.Fatalf("python ready 时这次调用不该是失败：%v\n%s", err, res.Text)
		}

		// 解出来的库名必须是**安装名**，不是 import 名。
		for name := range d.Libraries {
			switch name {
			case "docx", "PIL", "pptx":
				t.Fatalf("库名 %q 是 import 名；模型照着它去 pip 会装错包", name)
			}
		}
		// 摘要里要有版本号，否则 TUI 那一行等于没说。
		if !strings.Contains(res.Summary, d.Python.Version) {
			t.Fatalf("摘要没带上 python 版本：%q", res.Summary)
		}

	case StatusMissing:
		// 这台机器确实没 python：原来的跳过语义保留，但**只在确认没装时**。
		t.Skipf("这台机器确实没有 python（status=missing）")

	case StatusBroken:
		t.Fatalf("python 在 %s 但跑不起来（%s）——这是环境坏了，不是「没装」，也不该跳过。"+
			"\nHint=%q\n%s", d.Python.Path, d.Python.Error, res.Hint, res.Text)

	case StatusUnknown:
		t.Fatalf("python 在 %s，但探针**没跑完**（%s）——这是「没查到」，不是「没装」。\n"+
			"探针在预算内跑不通就是产品缺陷（PR #105 run 44855382735 的 CI 红就是这条），"+
			"不许把它当成「这台机器没有 python」跳过。\nHint=%q\n%s",
			d.Python.Path, d.Python.Error, res.Hint, res.Text)

	default:
		t.Fatalf("python 的 status 是 %q，不是 ready/missing/broken/unknown 之一：%s", d.Python.Status, res.Text)
	}
}

// ───────────────────── 状态一：探针没跑完（可重试） ─────────────────────

// TestProbeTimeoutIsUnknownNotMissing 钉住 PR #105 那个 CI 红的产品侧根因：
// python 在 PATH 上，但探针越过预算被杀。旧实现报 `"ok": false`，和「python
// 根本没装」一模一样，模型无法知道该重试还是该去装。
//
// 现在它必须是：
//   - status=unknown + next=retry（**不是** missing/install），python 不进 missing；
//   - 原因说清是预算用尽，错误身份是官方的 ToolTimeoutError / TOOL_TIMEOUT；
//   - 整次调用是**失败**（结果不完整，可重试）；
//   - 库没查就明说没查（libraries_status=skipped），不许给空 libraries 装成「没装库」。
func TestProbeTimeoutIsUnknownNotMissing(t *testing.T) {
	installFakePython(t, fakeSleep)
	res, err := (&Tool{budgets: fakeBudgets(fakeColdBudget)}).Exec(context.Background(), json.RawMessage(`{}`))
	d := decodeDeps(t, res)

	if err == nil {
		t.Fatalf("探针没跑完必须是一次失败（可重试），不是一个看起来正常的成功结果：%s", res.Text)
	}
	assertComponent(t, "python", d.Python, StatusUnknown, nextRetry)
	if contains(d.Missing, "python") {
		t.Fatalf("超时不许把 python 放进 missing/next=install（那是在说「没装」）：%v", d.Missing)
	}
	if d.Python.Path == "" {
		t.Fatalf("python 明明在 PATH 上，路径必须给出来：%s", res.Text)
	}
	if !strings.Contains(d.Python.Error, "预算") {
		t.Fatalf("原因必须说清是预算用尽，实际 %q", d.Python.Error)
	}
	if res.Info == nil || res.Info.Name != "ToolTimeoutError" || res.Info.Code != "TOOL_TIMEOUT" {
		t.Fatalf("超时的错误身份要照官方 {ToolTimeoutError, TOOL_TIMEOUT}（official-rc5 dsh-tool-call-timeout-policy/src/index.ts:25,41-48），实际 %+v", res.Info)
	}
	if d.LibrariesStatus != StatusSkipped || d.LibrariesError == "" || len(d.Libraries) != 0 {
		t.Fatalf("库没查就必须说「没查」（libraries_status=skipped + libraries_error），"+
			"不能给空 libraries 装成「没装库」：%s", res.Text)
	}
	if !strings.Contains(res.Hint, "重试") {
		t.Fatalf("Hint 必须告诉模型可以重试（而不是去装），实际 %q", res.Hint)
	}
	if !strings.Contains(res.Summary, "没查完") {
		t.Fatalf("摘要要说「没查完」：%q", res.Summary)
	}
}

// TestProbeRetriesColdStartOnce 钉住 CI 上那条真实路径：第一次启动是冷的
// （页缓存空、杀软逐文件扫），越过预算；第二次是热的。产品的决定性探针重试一次
// 就能拿到结论——结果是 ready，既不报 unknown 也不报 missing。
//
// 这条同时是「为什么不需要把 20s 改成 60s」的证据：超时是**可重试的**，
// 不是要一个更大的数字。
func TestProbeRetriesColdStartOnce(t *testing.T) {
	t.Setenv(envFakeMarker, filepath.Join(t.TempDir(), "first-run"))
	installFakePython(t, fakeSlowOnce)
	res, err := (&Tool{budgets: fakeBudgets(fakeHotBudget)}).Exec(context.Background(), json.RawMessage(`{}`))
	d := decodeDeps(t, res)

	if err != nil {
		t.Fatalf("第一次超时、第二次成功时这次调用应当成功（重试兜住冷启动）：%v\n%s", err, res.Text)
	}
	assertComponent(t, "python", d.Python, StatusReady, "")
	if d.Python.Version != "9.9.9" {
		t.Fatalf("版本要来自第二次（热）那次探针，实际 %q：%s", d.Python.Version, res.Text)
	}
}

// ───────────────────── 状态二：确认没装（去装） ─────────────────────

// TestProbeMissingIsConfirmedMissing：PATH 上什么都没有（真把 python 藏起来）。
// 结论必须是 missing + next=install——确定性、重试没用、下一步是「装」。
// 绝不许报成 unknown（那会让模型以为「再试一次就好了」而空转）。
func TestProbeMissingIsConfirmedMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // 空目录：python/node/pnpm 全都不在
	res, err := (&Tool{budgets: fakeBudgets(fakeColdBudget)}).Exec(context.Background(), json.RawMessage(`{}`))
	d := decodeDeps(t, res)

	if err == nil {
		t.Fatalf("三样都确认不在，必须是一次失败（并告诉模型去装）：%s", res.Text)
	}
	assertComponent(t, "python", d.Python, StatusMissing, nextInstall)
	if !contains(d.Missing, "python") || !contains(d.Missing, "node") || !contains(d.Missing, "pnpm") {
		t.Fatalf("missing 列表要把三样都写上，实际 %v", d.Missing)
	}
	if d.Python.Path != "" || d.Python.Version != "" {
		t.Fatalf("没装却给了路径/版本（编数据比没数据更糟）：%s", res.Text)
	}
	if d.Python.Error != "" {
		t.Fatalf("确认不在不是「探针出错」，不该有 error：%q", d.Python.Error)
	}
	if res.Info != nil {
		t.Fatalf("确认不在不是超时，不该带 TOOL_TIMEOUT 身份：%+v", res.Info)
	}
	if d.LibrariesStatus != StatusSkipped {
		t.Fatalf("python 都没装，库没查过，libraries_status 必须是 skipped，实际 %q", d.LibrariesStatus)
	}
	if !strings.Contains(res.Hint, "装") {
		t.Fatalf("Hint 要指向「装一个」，实际 %q", res.Hint)
	}
}

// ───────────────── 状态三：在，但跑不起来（修/换，不是重试） ─────────────────

// TestProbeBrokenIsNotMissing：python 在 PATH 上、也能启动，但探针非零退出。
// 这是确定性结论——status=broken + next=repair，**不是** unknown（免得模型
// 白等重试），更不是 missing（免得它去装一个已经装了的 python）。
func TestProbeBrokenIsNotMissing(t *testing.T) {
	installFakePython(t, fakeCrash)
	res, err := (&Tool{budgets: fakeBudgets(fakeHotBudget)}).Exec(context.Background(), json.RawMessage(`{}`))
	d := decodeDeps(t, res)

	if err == nil {
		t.Fatalf("三样都不可用必须是一次失败：%s", res.Text)
	}
	assertComponent(t, "python", d.Python, StatusBroken, nextRepair)
	if contains(d.Missing, "python") {
		t.Fatalf("跑不起来不许说成「没装」：%v", d.Missing)
	}
	if res.Info != nil {
		t.Fatalf("这不是超时，不该带 TOOL_TIMEOUT 身份：%+v", res.Info)
	}
	if !strings.Contains(d.Python.Error, "stderr") {
		t.Fatalf("原因里要带 stderr 摘要（否则分不清缺 DLL 还是语法错），实际 %q", d.Python.Error)
	}
	if !strings.Contains(res.Hint, "修") {
		t.Fatalf("Hint 要指向「修它或换一个」，实际 %q", res.Hint)
	}
}

// TestPartialBrokenDoesNotFailTheCall：坏的是**次要项**（node），python 与 pnpm
// 都正常——这次查询的结论是确定的，不许因为一个次要项坏掉就整次报失败。
// 模型读 node.next=repair 就知道该修 node，同时可以照常用 python。
func TestPartialBrokenDoesNotFailTheCall(t *testing.T) {
	// 只劫持 node：真的 python/pnpm 还在 PATH 上。用生产预算（真的探针要真的时间）。
	installFakeBinaryOnPath(t, "node", fakeCrash)
	res, err := (&Tool{}).Exec(context.Background(), json.RawMessage(`{}`))
	d := decodeDeps(t, res)

	if err != nil {
		t.Fatalf("python/pnpm 可用时不该整次失败：%v\n%s", err, res.Text)
	}
	if d.Python.Status != StatusReady {
		t.Skipf("这台机器 python 不是 ready（%q），本用例需要真 python 才能验「部分坏不影响成功」",
			d.Python.Status)
	}
	assertComponent(t, "node", d.Node, StatusBroken, nextRepair)
}

// ───────────────────────── 形状契约 ─────────────────────────

// TestStatusNextContract 钉住 status→next 的唯一映射与摘要用词。
// 只改一处映射表就能让模型拿到自相矛盾的两个字段，所以这张表必须被钉死。
func TestStatusNextContract(t *testing.T) {
	cases := []struct {
		status probeStatus
		next   nextAction
		label  string
	}{
		{StatusReady, "", "3.13.15"},
		{StatusMissing, nextInstall, "没装"},
		{StatusBroken, nextRepair, "跑不起来"},
		{StatusUnknown, nextRetry, "没查完"},
		{StatusSkipped, "", "没查"},
	}
	for _, c := range cases {
		if got := nextFor(c.status); got != c.next {
			t.Fatalf("%q 的 next 应当是 %q，实际 %q", c.status, c.next, got)
		}
		version := ""
		if c.status == StatusReady {
			version = "3.13.15"
		}
		if got := statusLabel(c.status, version); got != c.label {
			t.Fatalf("%q 在摘要里应当是 %q，实际 %q", c.status, c.label, got)
		}
	}
}

// ───────────────────────── 原有断言（加强版） ─────────────────────────

// 找不到东西时：**不许编**。整项省略或进 missing，且 Summary 要说实话。
//
// 这里用空 PATH 把「三样都没有」变成确定性输入（原来的写法在本机会直接 skip，
// 等于这条断言从来没跑过）。
func TestNoFakeVersionsWhenMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	res, _ := (&Tool{budgets: fakeBudgets(fakeColdBudget)}).Exec(context.Background(), json.RawMessage(`{}`))
	d := decodeDeps(t, res)

	// node 不存在时 Node.Path 必须为空、Version 也必须为空。
	if d.Node.Path == "" && d.Node.Version != "" {
		t.Fatalf("没有 node 却给了版本号 %q —— 编数据比没数据更糟", d.Node.Version)
	}
	if d.Pnpm.Path == "" && d.Pnpm.Version != "" {
		t.Fatalf("没有 pnpm 却给了版本号 %q", d.Pnpm.Version)
	}
	// 反过来也钉住：状态说 missing 的，不许同时给出路径或版本。
	for name, bin := range map[string]component{"node": d.Node, "pnpm": d.Pnpm} {
		if bin.Status == StatusMissing && (bin.Path != "" || bin.Version != "") {
			t.Fatalf("%s 是 missing 却带着 path=%q version=%q", name, bin.Path, bin.Version)
		}
	}
	for _, name := range d.Missing {
		if v, ok := d.Libraries[name]; ok {
			t.Fatalf("%s 在 missing 里却又有版本 %q", name, v)
		}
	}
	// 库没查（python 都不在）时，libraries 必须是空的、且不许编出 missing 的库名。
	for _, name := range libraryNames {
		if contains(d.Missing, name) {
			t.Fatalf("python 都没装却把库 %q 写进 missing —— 那是「没查」，不是「没装」", name)
		}
	}
}

// Caps 必须承认它会起子进程：审批与沙箱不按工具名猜（AGENTS §2）。
func TestCapsDeclareExecutes(t *testing.T) {
	if !(&Tool{}).Caps().Executes {
		t.Fatal("这个工具会起 python/node 子进程，Executes 必须是 true")
	}
}

// 描述长度上限（门禁同一口径：按 rune 数 ≤120），且枚举语义必须写进描述：
// 模型的唯一说明来源。缺一个词，它就得多猜一次——那正是这个工具要修的歧义。
func TestDescriptionLength(t *testing.T) {
	desc := (&Tool{}).Description()
	if n := len([]rune(desc)); n > 120 {
		t.Fatalf("Description %d 字，超过 120", n)
	}
	for _, word := range []string{"ready", "missing", "broken", "unknown", "install", "repair", "retry"} {
		if !strings.Contains(desc, word) {
			t.Fatalf("Description 里要说清 %q（模型的唯一说明来源）", word)
		}
	}
}
