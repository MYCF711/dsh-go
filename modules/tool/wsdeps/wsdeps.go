// Package wsdeps 提供 `load_workspace_dependencies` 工具：报告这台机器上跑
// Python/Office 活儿要用的运行时与库在哪、什么版本。
//
// 为什么需要它：模型要做 .xlsx/.docx 那类活儿时，先得知道有没有 openpyxl、
// python-docx，以及 python 在哪个路径。以前只能靠 bash 里试 `python -c "import ..."`，
// 一次一个库——既慢又容易在 PATH 上猜错。
//
// 这个工具**会起子进程**（两次 python 调用：一次取解释器版本，一次取库版本），
// 所以 Caps 里写了 Executes: true。按 AGENTS §2，审批与沙箱不按工具名猜——
// 起了进程就说起了进程。代价是 deny 策略下它会被拒，那是正确行为。
//
// 为什么是两趟而不是一趟（M290 回归的根因）：这台机器上 `python -c pass` 冷启动
// 就要 0.7~2.3 秒，再把 numpy/pandas/openpyxl/python-docx/python-pptx/Pillow/lxml
// 全导入一遍，实测 1.5~24 秒，并发重时更久。原来的实现把「python 能不能跑」绑在
// 这趟重库导入上，10 秒预算一到子进程被掐断——而 Windows 上 exec 只回
// "exit status 1"（不是 context.DeadlineExceeded），代码又把这个错误吞了。
// 于是 python 明明在，结果却是 {"path": "...python.exe", "ok": false}，还查不出原因。
//
// 最后一次修正（PR #105 run 44855382735，CI 21.39s 红）——**这是本工具的核心形状**：
// 一个布尔 `ok` 同时代表「确认没装」「跑不起来」「这次没查完」三件**下一步相反**的事
// （装一个 / 修它 / 原样重试），模型只能二选一地猜。现在每一项都带
// `status`（ready/missing/broken/unknown）和 `next`（install/repair/retry）：
//
//	status 说「是什么」，next 说「怎么办」。
//
// 为什么没照官方的形状抄：官方的 load_workspace_dependencies 只解析**随应用打包**
// 的 payload，从不探测宿主（official-rc5:
// `dsh-tool-workspace-dependencies/src/index.ts:285-321`；返回的版本来自
// runtime.json，见 `WorkspaceDependencies` `:50-58`），所以官方根本不存在
// 「探测超时 vs 没装」这对状态，没有可复刻的形状。官方能借的只有两条**原则**：
// 缺项就省略字段（`:303-304` "Absent when the payload ships no pnpm"）、失败用
// 结构化错误身份而不是一个否定的数据值（`dsh-tool-call-timeout-policy/src/index.ts:25,41-48`
// 的 {ToolTimeoutError, TOOL_TIMEOUT}；字段形状见 `dsh-tools/src/contracts.ts:271`）。
// 我们在它之上多给一个 `next`——这次事故的根因就是模型只读一个布尔值就下了结论。
package wsdeps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
)

var schema = json.RawMessage(`{
  "type": "object",
  "properties": {},
  "additionalProperties": false
}`)

// 每趟子进程的预算。分开给的唯一理由：**慢的一趟不许把快的两趟一起拖垮**。
// 原来三件事共用一个 10 秒 ctx，python 一慢，node/pnpm 的版本也跟着空。
//
// 为什么是这些数、以及能不能对官方：
//   - 官方**没有等价物可以对齐**：官方不探测宿主（见包注释），所以「探测预算」
//     这条没有官方数字。
//   - 官方唯一能对的是「一次工具调用给多久」：bash 命令默认 120s
//     （`dsh-bash-local/src/index.ts:102`）。那是给**用户命令**的，借过来等于把一个
//     `python -c` 的预算翻六倍——越界了，跟本轮要修的东西无关。
//   - 所以 **version 保留 20s**：正常 ~1 秒，20 倍余量给冷启动、杀软扫描、机器并发。
//     CI 那次刚好越过（21.39s）不是因为 20s 太小，而是因为第一次启动是冷的——
//     那由 runProbe 的**重试一次**兜住（见 probeAttempts），而不是把数字改大
//     让下一次照样越线。
//   - 而且现在就算真越线，结果也是 unknown（可重试），不再是「没装」。
const (
	defaultVersionBudget = 20 * time.Second
	defaultLibraryBudget = 30 * time.Second
	defaultNodeBudget    = 10 * time.Second
	defaultPnpmBudget    = 20 * time.Second
)

// probeWaitDelay 是「子进程已经被杀」之后最多再等管道关闭多久。
//
// 没有它：一个 .bat/.cmd 垫片（或者任何包装器）被 CommandContext 杀掉时，
// 只死了 cmd.exe，里面真正的子进程还攥着 stdout 的写端，cmd.Wait 会一直卡在
// 复制 goroutine 上——「超时」于是变成「挂死」，比原来的 bug 更难查。
// 探测只要结论，不要清理仪式：等不到就把管道丢掉，把超时**如实报成超时**。
const probeWaitDelay = 5 * time.Second

// budgets 是四趟子进程各自的预算。生产用 defaultBudgets；测试注入毫秒级预算，
// 好把「超时」这条路径在不缩短产品预算的前提下跑成真子进程 + 真掐断。
type budgets struct {
	version time.Duration
	library time.Duration
	node    time.Duration
	pnpm    time.Duration
}

// defaultBudgets 是生产预算（数值与理由见上面 const 块）。
var defaultBudgets = budgets{
	version: defaultVersionBudget,
	library: defaultLibraryBudget,
	node:    defaultNodeBudget,
	pnpm:    defaultPnpmBudget,
}

// versionScript 只取解释器版本——**不导入任何第三方库**。
// 「python 可用不可用」必须由它决定：重库导入慢是环境问题，不是 python 不存在。
const versionScript = `import json, sys
print(json.dumps({"version": "%d.%d.%d" % sys.version_info[:3]}))`

// librariesScript 一次取完所有库的版本。
//
// 关键点：**一个进程取全部**。七个库各起一个 python，在冷启动慢的机器上要几十秒，
// 而这里只需要一次导入。
const librariesScript = `import json
libs = {}
for mod, name in [("numpy","numpy"),("pandas","pandas"),("openpyxl","openpyxl"),
                  ("docx","python-docx"),("pptx","python-pptx"),
                  ("PIL","Pillow"),("lxml","lxml")]:
    try:
        m = __import__(mod)
        libs[name] = getattr(m, "__version__", None) or "unknown"
    except Exception:
        pass
print(json.dumps(libs))`

// libraryNames 是**安装名**（不是 import 名）——模型照着它 pip install 才装得对。
var libraryNames = []string{"numpy", "pandas", "openpyxl", "python-docx", "python-pptx", "Pillow", "lxml"}

// errProbeBudget 是「预算到点、子进程被掐断」的结构化原因。
//
// 为什么必须是一个 sentinel 而不是字符串：调用方要按**原因**决定 status、next、
// Hint 和错误身份（超时 ⇒ 可重试 ⇒ TOOL_TIMEOUT；跑挂 ⇒ 修/换）。字符串匹配在这种
// 地方迟早会错，而且改一个字文案就静默失效。
var errProbeBudget = errors.New("探针预算用尽")

// 错误身份：探针预算用尽。
//
// 值照抄官方（`dsh-tool-call-timeout-policy/src/index.ts:25` 的 `TOOL_TIMEOUT`；
// 结果形状见 `:41-48` 的 toolTimeoutResult：isError + error.info{name,code}）。
// 这是本工具里**唯一**需要机器可读身份的原因：看到它就知道「超时了，重试可能就好」。
const (
	errorNameTimeout = "ToolTimeoutError"
	codeTimeout      = "TOOL_TIMEOUT"
)

// probeStatus 是**一项能力被探到了什么**。
//
// 四值缺任何一个，都会回到这个工具最初那个 bug——把「下一步相反」的几件事压成
// 一个 `ok:false`。这里刻意把「跑不起来」（broken，确定性结论）和「没查完」
// （unknown，可重试）**分开**：它们的下一步一个是「修/换」，一个是「原样重试」。
//
// 形式参考官方的 list_agents（每行一个必填 status 枚举，「读不到」是独立取值：
// official-rc5 `dsh-tool-subagent-control/src/list-agents.ts:12,14,52-53,67`），
// 但取值按本工具的领域命名，并额外给出 next——官方的 status 只有状态没有动作，
// 而这次事故证明：模型会只读一个字段就下结论。
type probeStatus string

const (
	// StatusReady 探针跑通，版本是真的。
	StatusReady probeStatus = "ready"
	// StatusMissing **确认不在**（PATH 上没有这个可执行文件）。确定性结论：
	// 重试没用，要么装，要么换路子。
	StatusMissing probeStatus = "missing"
	// StatusBroken 在 PATH 上、也能启动，但探针跑挂了（非零退出 / 输出不可解析 /
	// 退出 0 却什么都没说）。确定性结论：重试没用，要修它或换一个解释器。
	StatusBroken probeStatus = "broken"
	// StatusUnknown 探针**没跑完**：预算用尽被杀、这轮被取消。东西可能就在那儿，
	// 下一次重试可能就好了——所以它不是「没装」也不是「坏了」。
	StatusUnknown probeStatus = "unknown"
	// StatusSkipped 只给 libraries 用：python 本身没就绪，库压根没开始查。
	// 「没查」不许写成「没装库」（那是让模型去重装一堆本来就有的包）。
	StatusSkipped probeStatus = "skipped"
)

// nextAction 是「模型下一步该干什么」。状态说是什么，它说怎么办。
type nextAction string

const (
	// nextInstall 装一个（确认不在）。
	nextInstall nextAction = "install"
	// nextRepair 修它或换一个（在，但跑不起来）。
	nextRepair nextAction = "repair"
	// nextRetry 原样重试（这次只是没查到，不是没有）。
	nextRetry nextAction = "retry"
)

// nextFor 是**唯一**的状态→动作映射点。
//
// 只在这里写一遍的理由：status 与 next 说两套话是比原来那个 bug 更难查的 bug，
// 状态一改就自动跟着改，测试也只需钉住这一张表。
func nextFor(status probeStatus) nextAction {
	switch status {
	case StatusMissing:
		return nextInstall
	case StatusBroken:
		return nextRepair
	case StatusUnknown:
		return nextRetry
	default:
		// ready 无事可做；skipped 的动作在它依赖的那一项上（看 python 的 next）。
		return ""
	}
}

// statusLabel 把状态翻成摘要里的一个词。
//
// **不许**在非 ready 时留空：空串读起来像"没查"，模型又会把几种「不能确定」
// 和「确实没有」混成同一件事——这就是要修的 bug。
func statusLabel(status probeStatus, version string) string {
	switch status {
	case StatusReady:
		if version == "" {
			return "?"
		}
		return version
	case StatusMissing:
		return "没装"
	case StatusBroken:
		return "跑不起来"
	case StatusUnknown:
		return "没查完"
	default:
		return "没查"
	}
}

// component 是一项可探测能力（python / node / pnpm）模型可见的形状。
//
// 约定照官方的「没有就省略字段」：path/version/error 只在有内容时出现，
// 不给 false/空串占位；状态由 status 说，下一步由 next 说。
type component struct {
	Path    string      `json:"path,omitempty"`
	Version string      `json:"version,omitempty"`
	Status  probeStatus `json:"status"`
	// Next 只在非 ready 时出现：install / repair / retry。ready 时无事可做。
	Next nextAction `json:"next,omitempty"`
	// Error 是 status 非 ready 时的原因（预算用尽 / 非零退出 / stderr 摘要 /
	// 输出无法解析）。它给人和模型看细节，**不承担分支语义**——那是 Next 的活。
	Error string `json:"error,omitempty"`
	// cause 是那次探针的原始错误，**不序列化**（unexported，encoding/json 忽略）。
	// 要按结构判断「超时 / 取消 / 跑挂」来决定错误身份与 Hint，只有它能做到
	// （Next 说「重试」，但「为什么值得重试」还得看原因）。
	cause error
}

// mark 是**唯一**写 status/next 的地方：映射只经 nextFor，避免两套话。
func (c *component) mark(status probeStatus, errText string, cause error) {
	c.Status = status
	c.Next = nextFor(status)
	c.Error = errText
	c.cause = cause
}

// deps 是返回给模型的结构化形状。
//
// 失败时这份结构照样带上（见 Exec 的注释）——「哪些查到了、哪些没查到」
// 是模型判断下一步的唯一依据。
type deps struct {
	Python component `json:"python"`
	// Libraries 是探针**真的取到**的库版本（安装名→版本）。
	Libraries map[string]string `json:"libraries"`
	// LibrariesStatus 说这份 libraries 能不能信：ready 才可信；
	// unknown = 没查完（可重试）；broken = 查挂了；skipped = python 没就绪，没查。
	LibrariesStatus probeStatus `json:"libraries_status"`
	// LibrariesError 是 libraries 非 ready 时的原因（人话细节）。
	LibrariesError string    `json:"libraries_error,omitempty"`
	Node           component `json:"node"`
	Pnpm           component `json:"pnpm"`
	// Missing 只放**确认不在**的名字（python/node/pnpm 或已知没装的库），
	// 「没查完」「跑不起来」都不进这里。
	Missing []string `json:"missing,omitempty"`
}

// Tool 是 load_workspace_dependencies 的实现。
//
// budgets 为 nil = 生产预算。这个字段只为一件测试需求存在：把「超时」这条路径
// 在毫秒级跑完，而**既不改产品逻辑、也不动产品预算**（改包级 var 会被 -race 抓、
// 也会让测试之间互相污染）。
type Tool struct {
	budgets *budgets
}

func (t *Tool) Name() string { return "load_workspace_dependencies" }
func (t *Tool) Caps() spi.Caps {
	// 起 python 子进程 = 执行。不因为"只是读版本"就把它藏起来。
	return spi.Caps{Executes: true}
}
func (t *Tool) Schema() json.RawMessage { return schema }
func (t *Tool) Description() string {
	// 枚举语义写进描述：模型不该靠猜 status 是不是「没装」——那正是本工具修掉的歧义。
	return "查 Python/Node/pnpm 路径与库版本（会起子进程）。status=ready 可用、missing 没装、broken 跑不起来、unknown 没查完；next=install/repair/retry 是下一步"
}

// budget 返回这次调用用的预算（生产默认值，或测试注入值）。
func (t *Tool) budget() budgets {
	if t.budgets == nil {
		return defaultBudgets
	}
	return *t.budgets
}

func (t *Tool) Exec(ctx context.Context, _ json.RawMessage) (spi.Result, error) {
	bud := t.budget()

	var d deps
	d.Libraries = map[string]string{}

	probePython(ctx, bud, &d)
	probeBin(ctx, bud.node, "node", &d.Node, &d.Missing)
	probeBin(ctx, bud.pnpm, "pnpm", &d.Pnpm, &d.Missing)

	data, _ := json.MarshalIndent(d, "", "  ")
	// 摘要里的库那一栏：只有**没查完/没查**时才加状态括注。ready 时加状态既啰嗦
	// 又会落到 statusLabel 的兜底分支（ready 没有版本可显示），变成「库 6 个（?）」。
	libs := fmt.Sprintf("库 %d 个", len(d.Libraries))
	if d.LibrariesStatus != StatusReady {
		libs += "（" + statusLabel(d.LibrariesStatus, "") + "）"
	}
	res := spi.Result{
		Summary: fmt.Sprintf("python %s｜%s｜node %s｜pnpm %s",
			statusLabel(d.Python.Status, d.Python.Version),
			libs,
			statusLabel(d.Node.Status, d.Node.Version),
			statusLabel(d.Pnpm.Status, d.Pnpm.Version)),
		Text: string(data),
		Data: d,
	}

	// 结局分三种，**下一步完全不同**，所以形状也必须分开（见包注释：
	// 把三种压成一个 ok:false 正是上一版的全部问题）：
	//
	//	① 有「没查完」⇒ 这次的结果不完整，可重试 ⇒ 失败 + TOOL_TIMEOUT（超时时）
	//	② 一样都不可用 ⇒ 确定性结论，按组件说清「装」还是「修」⇒ 失败
	//	③ 至少一样可用 ⇒ 成功；其余项的状态/next 自己会说话
	switch {
	case d.Python.Status == StatusUnknown:
		// 主问题没查完：python 在不在、能不能用，这次没有结论。
		return failIncomplete(res, "python", &d.Python)
	case d.Node.Status == StatusUnknown:
		return failIncomplete(res, "node", &d.Node)
	case d.Pnpm.Status == StatusUnknown:
		return failIncomplete(res, "pnpm", &d.Pnpm)
	case !anyReady(d):
		res.Hint = unavailableHint(d)
		return res, fmt.Errorf("wsdeps: python/node/pnpm 都不可用")
	}

	// 剩下的都是成功：至少一样可用，其余项在 Text 里带着自己的 status/next
	// （例如 python broken + node ready：模型读 python.next=repair 就知道修它，
	// 不必因为一次「部分坏」把整次查询判死）。
	return res, nil
}

// anyReady 报告三样里有没有一样是**确认可用**的。
func anyReady(d deps) bool {
	for _, s := range []probeStatus{d.Python.Status, d.Node.Status, d.Pnpm.Status} {
		if s == StatusReady {
			return true
		}
	}
	return false
}

// failIncomplete 是「某项没查完」时的失败结果。
//
// 为什么整次调用是失败而不是带 status 的成功：结果**不完整**时把调用报成成功，
// 模型会当成「查完了，就是这样」，于是去装一个可能已经装好的东西。官方对超时
// 也走结构化错误（`dsh-tool-call-timeout-policy/src/index.ts:41-48`），不是数据值。
// 但 Text/Data 照样带上：失败结果里的真实产出是模型判断下一步的依据
// （internal/spi/tool.go:190-198 的 WithErrorInfo 注释、R3）。
func failIncomplete(res spi.Result, name string, comp *component) (spi.Result, error) {
	switch {
	case errors.Is(comp.cause, errProbeBudget):
		res.Hint = fmt.Sprintf("%s 在 %s，但探针**没跑完**（%v）——这是「没查到」，不是「没装」："+
			"先原样重试一次这个工具（next=retry）；要立刻确认就在 bash 里手工跑一遍同样的命令",
			name, comp.Path, comp.cause)
		res = res.WithErrorInfo(errorNameTimeout, codeTimeout)
	case errors.Is(comp.cause, context.Canceled):
		res.Hint = fmt.Sprintf("%s 的探针**被取消了**（这轮被停），它的状态没有确认——"+
			"重新发起一次就能查完，别当成没装", name)
	default:
		res.Hint = fmt.Sprintf("%s 在 %s，但探针**没跑完**（%v）——这是「没查到」，不是「没装」："+
			"先看现场（进程/权限/杀软）再决定要不要重试（next=retry）", name, comp.Path, comp.cause)
	}
	return res, fmt.Errorf("wsdeps: %s 的探针没跑完（这是「没查到」，不是「没装」）：%w", name, comp.cause)
}

// unavailableHint 把「一样都不可用」翻成按组件分开的下一步：
// 没装的说装、跑不起来的说修——两句话术不同，因为动作不同。
func unavailableHint(d deps) string {
	var parts []string
	for _, it := range []struct {
		name string
		c    *component
	}{
		{"python", &d.Python},
		{"node", &d.Node},
		{"pnpm", &d.Pnpm},
	} {
		switch it.c.Status {
		case StatusMissing:
			parts = append(parts, it.name+" 没装（装一个）")
		case StatusBroken:
			parts = append(parts, fmt.Sprintf("%s 在 %s 但跑不起来（修它或换一个）", it.name, it.c.Path))
		}
	}
	return "三样都不可用：" + strings.Join(parts, "；") +
		"。装好/修好一个再调这个工具；也可以先用 bash 手工确认它们的位置"
}

// statusOfErr 把一次探针的失败翻成状态。
//
// 等够预算没跑完 = unknown（可重试）；这轮被取消 = unknown（不是它的错）；
// 其余（非零退出、缺 DLL、超时之外的各种跑挂）= broken（确定性，重试没用）。
func statusOfErr(err error) probeStatus {
	if errors.Is(err, errProbeBudget) || errors.Is(err, context.Canceled) {
		return StatusUnknown
	}
	return StatusBroken
}

// probePython 填 python 这一项（两趟：版本 → 库）。
func probePython(ctx context.Context, bud budgets, d *deps) {
	pyPath, err := exec.LookPath("python")
	if err != nil {
		// 确认不在：确定性结论，重试没用。
		d.Python.mark(StatusMissing, "", nil)
		d.Missing = append(d.Missing, "python")
		// 库没开始查，是因为 python 都没就绪——说清「没查」，不许留空 libraries 装成「没装库」。
		d.LibrariesStatus = StatusSkipped
		d.LibrariesError = "python 没装，库版本没查（这不是「库没装」）"
		return
	}
	d.Python.Path = pyPath

	// 第一趟：便宜、决定性。只取解释器版本，不导入任何第三方库。
	out, err := runProbe(ctx, bud.version, pyPath, "-c", versionScript)
	if err != nil {
		// 探针没跑完/跑挂 ≠ 没装。PR #105 run 44855382735 的全部内容就是这一行：
		// python 在 hostedtoolcache 里，探针越过 20s 被杀，结果却报 ok:false，
		// 和「根本没装」一模一样。
		d.Python.mark(statusOfErr(err), err.Error(), err)
		d.LibrariesStatus = StatusSkipped
		d.LibrariesError = "python 的版本探针没跑完，库版本没查（这不是「库没装」）"
		return
	}

	var got struct {
		Version string `json:"version"`
	}
	if json.Unmarshal([]byte(out), &got) != nil || got.Version == "" {
		// 进程跑完了、退出码 0，但给的东西不可用：这是**它坏了**，不是「没查到」。
		msg := fmt.Sprintf("版本探针输出无法解析：%q", oneLine(out, 200))
		d.Python.mark(StatusBroken, msg, errors.New(msg))
		d.LibrariesStatus = StatusSkipped
		d.LibrariesError = "python 的版本探针没给出可用结果，库版本没查（这不是「库没装」）"
		return
	}
	d.Python.Version = got.Version
	d.Python.mark(StatusReady, "", nil)

	// 第二趟：贵，best-effort。它失败不影响 python 可用，但要如实说「没查完/查挂了」，
	// 不许让一个空 libraries 自己去暗示「一个库都没装」。
	libOut, libErr := runOnce(ctx, bud.library, pyPath, "-c", librariesScript)
	if libErr != nil {
		d.LibrariesStatus = statusOfErr(libErr)
		d.LibrariesError = libErr.Error()
		return
	}
	var libs map[string]string
	if json.Unmarshal([]byte(libOut), &libs) != nil {
		d.LibrariesStatus = StatusBroken
		d.LibrariesError = fmt.Sprintf("库探针输出无法解析：%q", oneLine(libOut, 200))
		return
	}
	d.LibrariesStatus = StatusReady
	for k, v := range libs {
		d.Libraries[k] = v
	}
	for _, name := range libraryNames {
		if _, ok := d.Libraries[name]; !ok {
			d.Missing = append(d.Missing, name)
		}
	}
}

// probeBin 填 node/pnpm 这一项：路径 + 版本 + 状态 + 下一步。
// 探针没跑完就是 unknown，**不编版本号**，也不说成「没装」。
func probeBin(ctx context.Context, budget time.Duration, name string, dst *component, missing *[]string) {
	p, err := exec.LookPath(name)
	if err != nil {
		dst.mark(StatusMissing, "", nil)
		*missing = append(*missing, name)
		return
	}
	dst.Path = p

	out, err := runProbe(ctx, budget, p, "--version")
	if err != nil {
		dst.mark(statusOfErr(err), err.Error(), err)
		return
	}
	v := trimNL(out)
	if v == "" {
		// 退出码 0 却什么都没说：这也是「它坏了」，不是「没装」。
		msg := fmt.Sprintf("%s --version 没有任何输出", name)
		dst.mark(StatusBroken, msg, errors.New(msg))
		return
	}
	dst.Version = v
	dst.mark(StatusReady, "", nil)
}

// probeAttempts 是一次**决定性**探针最多试几次。
//
// 为什么允许在工具内部重试：第一次启动解释器/运行时是冷的（页缓存为空、杀软要
// 逐文件扫），第二次是热的。CI 上实测这一趟刚好越过 20s 预算（PR #105
// run 44855382735，21.39s），本机 -race 只要 5s——那不是"预算太小"，是"第一次
// 贵得不正常"。官方对同一种情况的处理是把失败当**可重试**的、不记成否定结论
// （`dsh-tool-workspace-dependencies/src/index.ts:313-317`：preparation 一出错就
// 清空，下次调用重新准备）。这里把那个重试放进工具内部，省模型一次往返；
// **预算本身一个数字都没放宽**（见 defaultVersionBudget 的注释）。
const probeAttempts = 2

// runProbe 跑一次决定性探针；预算用尽时再试一次（见 probeAttempts）。
//
// 只重试「没跑完」，不重试「跑挂了」：非零退出、缺 DLL 是确定性结论，
// 重试只是白等一倍时间。
func runProbe(ctx context.Context, budget time.Duration, name string, args ...string) (string, error) {
	var out string
	var err error
	for i := 1; i <= probeAttempts; i++ {
		out, err = runOnce(ctx, budget, name, args...)
		if err == nil {
			return out, nil
		}
		// 父 ctx 已经结束（用户取消/这轮被掐）：再试也是立刻失败，别浪费一倍预算。
		if !errors.Is(err, errProbeBudget) || ctx.Err() != nil {
			return out, err
		}
	}
	return out, fmt.Errorf("%w（共尝试 %d 次，每次都是这个结果）", err, probeAttempts)
}

// runOnce 跑一条子进程命令，返回 stdout；出错时把**原因**带回来。
//
// 为什么不能只返回 err：Windows 上被 context 掐断的子进程只会给 "exit status 1"，
// 与「python 真的退出 1」看不出区别（实测；不是 context.DeadlineExceeded）。
// 所以先用 c.Err() 判预算，再把 stderr 摘要附上——否则调用方只能拿到一个
// 无声的 false，真正的原因（超时 / 缺 DLL / 语法错）全丢在管道里。
func runOnce(ctx context.Context, budget time.Duration, name string, args ...string) (string, error) {
	c, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	cmd := exec.CommandContext(c, name, args...)
	// 被掐断之后别陪着孙进程等（理由见 probeWaitDelay）。
	cmd.WaitDelay = probeWaitDelay
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if cErr := c.Err(); cErr != nil {
			// 只有「真的等够了一个预算」才算超时。父 ctx 被取消是另一件事：
			// 有人把这轮停了，下一步是重新发起，不是「预算太小」——所以它**不能**
			// 冒充 TOOL_TIMEOUT。
			if errors.Is(cErr, context.DeadlineExceeded) {
				return stdout.String(), fmt.Errorf("%w（%v，每次预算 %s）", errProbeBudget, cErr, budget)
			}
			return stdout.String(), fmt.Errorf("探针被取消：%w", cErr)
		}
		if s := oneLine(stderr.String(), 400); s != "" {
			return stdout.String(), fmt.Errorf("%w（stderr: %s）", err, s)
		}
		return stdout.String(), err
	}
	return stdout.String(), nil
}

func trimNL(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}

// oneLine 把子进程错误输出压成一行（保留可读性，按 rune 截断不劈开中文）。
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

// Module 返回装配用的模块描述。
func Module() kernel.Module {
	return kernel.Module{
		ID:       "tool-wsdeps",
		Backend:  kernel.BackendStatic,
		Requires: []string{"tools"},
		Register: func(s *kernel.Scope) error {
			reg, err := kernel.Inject(s, spi.ToolsKey)
			if err != nil {
				return err
			}
			return reg.Register(&Tool{})
		},
	}
}
