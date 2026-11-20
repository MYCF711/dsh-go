package sandbox

// 受控根（controlled root）与 OS 级 runner 的跨平台契约。
//
// 本文件只放**机制无关**的部分：受控根路径、生命周期、引用计数、如实上报。
// 真正的 OS 限制在 `runner_windows.go`（Windows 受限令牌 + Low 完整性 + 目录 ACL）
// 与 `runner_other.go`（明确不支持）。
//
// 为什么偏要另起一个「受控根」而不是把 ACE 打在用户工作区上：
// 官方 `dsh-sandbox-windows-acl` 的做法是往工作区目录树写**驻留** ACE 与 Low 标签，
// 它自己的 README 写明回收不了（`icacls` 报 1332 `ERROR_NONE_MAPPED`）且标签寿命长于
// 宿主进程。在共享/真实工作区上做这件事，等于把用户目录放宽给同用户下任意低完整性
// 进程。本仓因此把 ACL 与标签只落在**宿主自己拥有的** `<home>/sandbox-roots/<sid>/`
// 上：删掉这个目录就一点不剩（探针已验证：删除后目录消失、父目录 ACL 前后逐字相同）。
//
// 代价必须说清楚，不许当成完整沙箱：
//   - 只限制**写与删**，而且只保证「受控根之外写不进」；
//   - 读、网络、进程可见性、注册表、以及受控根**内部**的写入**都不受限**；
//   - 因此它比官方的 partial 还窄一层：官方把工作区也授给受限 SID，本仓不授。

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

const (
	// EnvACLRoot 是受控根**父目录**：设了才启用 ACL runner，每条命令在
	// `<父目录>/<session-id>/` 里跑。
	//
	// 为什么默认关闭而不是默认开：受控根把命令的可写面收窄到这个宿主自有的目录，
	// workspace-write 档位下命令就写不进用户工作区了。那是「更窄」，但会改变产品
	// 行为，所以做成显式开关；没设时 enforcement 如实报 `policy-only-no-os-isolation`。
	EnvACLRoot = "DSHD_SANDBOX_ACL_ROOT"

	// sandboxRootsDirName 是 `<home>` 下受控根父目录的默认名。
	sandboxRootsDirName = "sandbox-roots"

	// 受控根内给子进程用的临时目录名（TMP/TEMP 指到这里，否则很多工具会往
	// 系统 temp 写而当场被拒）。
	sandboxRootTempDirName = "tmp"
)

var (
	// ErrRunnerUnsupported 表示本平台没有 runner 实现（非 Windows）。
	ErrRunnerUnsupported = errors.New("本平台没有 sandbox runner 实现")
	// ErrRunnerUnavailable 表示 runner 存在但本进程用不了（权限/档位/已关闭）。
	ErrRunnerUnavailable = errors.New("sandbox runner 不可用")
	// ErrRunnerRootInvalid 表示受控根路径不合法（相对路径、经过 reparse point、
	// 或越出父目录）。
	ErrRunnerRootInvalid = errors.New("受控根不合法")
)

// SandboxRootsDir 返回 `<home>/sandbox-roots`：宿主自己拥有的受控根父目录。
func SandboxRootsDir(home string) string {
	return filepath.Join(home, sandboxRootsDirName)
}

// SessionRoot 返回 `<parent>/<session-id>`。
//
// session id 会被当目录名拼进文件系统路径，所以这里 fail-loud 校验：任何分隔符、
// 盘符、`.` / `..`、结尾点或空格都拒绝——不能靠调用方「应该不会传坏值」。
func SessionRoot(parent, sessionID string) (string, error) {
	if strings.TrimSpace(parent) == "" {
		return "", fmt.Errorf("%w: 受控根父目录为空", ErrRunnerRootInvalid)
	}
	abs, err := filepath.Abs(parent)
	if err != nil {
		return "", fmt.Errorf("%w: 父目录 %q 无法取绝对路径: %v", ErrRunnerRootInvalid, parent, err)
	}
	if err := validateSessionDirName(sessionID); err != nil {
		return "", err
	}
	return filepath.Join(filepath.Clean(abs), sessionID), nil
}

// validateSessionDirName 只接受能安全当单层目录名的会话 id。
func validateSessionDirName(sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("%w: 会话 id 为空", ErrRunnerRootInvalid)
	}
	if sessionID == "." || sessionID == ".." {
		return fmt.Errorf("%w: 会话 id 不能是 %q", ErrRunnerRootInvalid, sessionID)
	}
	if len(sessionID) > 128 {
		return fmt.Errorf("%w: 会话 id 过长（%d 字节）", ErrRunnerRootInvalid, len(sessionID))
	}
	if strings.ContainsAny(sessionID, `/\:`) {
		return fmt.Errorf("%w: 会话 id %q 含路径分隔符或盘符", ErrRunnerRootInvalid, sessionID)
	}
	for _, r := range sessionID {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: 会话 id %q 含控制字符", ErrRunnerRootInvalid, sessionID)
		}
	}
	if trimmed := strings.TrimRight(sessionID, ". "); trimmed != sessionID {
		return fmt.Errorf("%w: 会话 id %q 以点或空格结尾", ErrRunnerRootInvalid, sessionID)
	}
	return nil
}

// RunnerSpec 是一个会话启用 runner 所需的全部输入。
type RunnerSpec struct {
	SessionID string
	// ParentDir 是受控根父目录；空时用 `SandboxRootsDir(HomeDir)`。
	ParentDir string
	// HomeDir 只在 ParentDir 为空时用。
	HomeDir string
	// Mode 是会话档位：`danger-full-access` 明确不启用 runner（那一档的语义就是
	// 「不虚构路径沙盒」，套上受限令牌就是自相矛盾）。
	Mode Mode
}

// rootPath 解析 spec 的受控根路径（纯计算，不落盘）。
func (s RunnerSpec) rootPath() (string, error) {
	parent := strings.TrimSpace(s.ParentDir)
	if parent == "" {
		if strings.TrimSpace(s.HomeDir) == "" {
			return "", fmt.Errorf("%w: ParentDir 与 HomeDir 都为空", ErrRunnerRootInvalid)
		}
		parent = SandboxRootsDir(s.HomeDir)
	}
	return SessionRoot(parent, s.SessionID)
}

// platformSupportsRunner 与 newRunnerImpl 由各平台的 runner_*.go 提供。
type runnerImpl interface {
	// prepare 幂等地建受控根，并且**只**给这个目录打 ACE 与低完整性标签。
	prepare(root string) error
	// teardown 撤销并删掉受控根（幂等；已经不存在也算成功）。
	teardown(root string) error
	// confine 把受限身份与受控根绑到一条待启动的命令上。
	confine(cmd *exec.Cmd, root string) error
	// enforcement 是这个实现**真实**到达的隔离层级。
	enforcement() Enforcement
	// describe 是给证据/诊断用的一句话（机制、受限 SID）。
	describe() string
	// close 释放令牌等句柄（不碰文件系统）。
	close() error
}

// Runner 是一个会话受控根的持有者。
//
// 生命周期用引用计数：`Prepare` 第一次真正建根与令牌，最后一个 `release` 删根。
// 因此并发命令不会互相把根删掉，而进程跑完也不留残留目录。
type Runner struct {
	impl runnerImpl
	root string
	enf  Enforcement

	mu     sync.Mutex
	active int
	closed bool
}

// NewRunner 校验配置并造出 runner。**不动文件系统**：受控根在第一次
// `Prepare` 时才创建，所以「配了但一次没跑」不会留下任何东西。
func NewRunner(spec RunnerSpec) (*Runner, error) {
	if err := ValidateMode(spec.Mode); err != nil {
		return nil, err
	}
	if spec.Mode == ModeDangerFullAccess {
		return nil, fmt.Errorf("%w: %s 档的语义就是「不虚构路径沙盒」，不启用 ACL 受限运行",
			ErrRunnerUnavailable, spec.Mode)
	}
	root, err := spec.rootPath()
	if err != nil {
		return nil, err
	}
	impl, err := newRunnerImpl()
	if err != nil {
		return nil, err
	}
	return &Runner{impl: impl, root: root, enf: impl.enforcement()}, nil
}

// Root 返回这个会话的受控根路径。
func (r *Runner) Root() string {
	if r == nil {
		return ""
	}
	return r.root
}

// Enforcement 返回这个 runner **真实**到达的隔离层级。
func (r *Runner) Enforcement() Enforcement {
	if r == nil {
		return EnforcementPolicyOnlyNoOSIsolation
	}
	return r.enf
}

// Describe 返回机制说明（证据/诊断用）。
func (r *Runner) Describe() string {
	if r == nil {
		return ""
	}
	return r.impl.describe()
}

// Prepare 把一条待启动的命令放进受控根：
//   - 幂等建根 + 只给它打 ACE 与低完整性标签；
//   - 给命令打受限令牌；
//   - cwd 固定到受控根，TMP/TEMP 指到 `<root>/tmp`。
//
// 返回的 release 必须调用（一般 `defer`）：最后一个 release 会删掉受控根。
func (r *Runner) Prepare(cmd *exec.Cmd) (func() error, error) {
	if r == nil {
		return nil, fmt.Errorf("%w: nil runner", ErrRunnerUnavailable)
	}
	if cmd == nil {
		return nil, fmt.Errorf("%w: nil cmd", ErrMissingContext)
	}
	if err := r.acquire(); err != nil {
		return nil, err
	}
	if err := r.impl.confine(cmd, r.root); err != nil {
		_ = r.release()
		return nil, err
	}
	var once sync.Once
	var releaseErr error
	return func() error {
		once.Do(func() { releaseErr = r.release() })
		return releaseErr
	}, nil
}

func (r *Runner) acquire() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return fmt.Errorf("%w: runner 已关闭", ErrRunnerUnavailable)
	}
	if r.active == 0 {
		if err := r.impl.prepare(r.root); err != nil {
			return fmt.Errorf("%w: 建受控根 %s: %v", ErrRunnerUnavailable, r.root, err)
		}
	}
	r.active++
	return nil
}

func (r *Runner) release() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active > 0 {
		r.active--
	}
	if r.active > 0 || r.closed {
		return nil
	}
	return r.impl.teardown(r.root)
}

// Close 释放令牌并删掉受控根。幂等。
func (r *Runner) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.active = 0
	r.mu.Unlock()

	teardownErr := r.impl.teardown(r.root)
	closeErr := r.impl.close()
	if teardownErr != nil {
		return teardownErr
	}
	return closeErr
}

// ProbeExecutable 在受控身份下**真跑一次** exe，判断「这个程序在受限身份里能不能起来」。
//
// 为什么接线前必须有这一步：本机实测 PowerShell 7 在受限令牌下**加载期就**
// 0xc0000142（STATUS_DLL_INIT_FAILED）退出，而 `cmd.exe` 正常；把两个因子拆开
// （只造受限令牌、不降完整性）后仍然 0xc0000142，所以拦住它的是**受限令牌本身**，
// 不是完整性级别。不先探一次就直接接线，pwsh-sandbox 会在第一条真实命令上炸，
// 而不是在启用时如实退回策略层。
//
// 探针只跑一条无副作用命令（`exit 0` 一类），失败返回的错误里带子进程输出。
func (r *Runner) ProbeExecutable(exe string, args ...string) error {
	if r == nil {
		return fmt.Errorf("%w: nil runner", ErrRunnerUnavailable)
	}
	if strings.TrimSpace(exe) == "" {
		return fmt.Errorf("%w: 探针可执行文件为空", ErrMissingContext)
	}
	cmd := exec.Command(exe, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	release, err := r.Prepare(cmd)
	if err != nil {
		return err
	}
	runErr := cmd.Run()
	releaseErr := release()
	if runErr != nil {
		out := strings.TrimSpace(buf.String())
		if out == "" {
			out = "（无输出）"
		}
		return fmt.Errorf("%w: %s 在受限身份下起不来（%v）：%s",
			ErrRunnerUnavailable, filepath.Base(exe), runErr, out)
	}
	if releaseErr != nil {
		return fmt.Errorf("%w: 探针跑完后收工失败: %v", ErrRunnerUnavailable, releaseErr)
	}
	return nil
}

// EnforcementFor 报告在 spec 这份配置下**真实**会生效的隔离层级。//
// 纯计算：不建目录、不打 ACL、不造令牌，所以可以放心地在设置接口里调。
// 第二个返回值是给人/界面看的原因；未到达 runner 时必须说明为什么。
func EnforcementFor(spec RunnerSpec) (Enforcement, string) {
	switch {
	case spec.Mode == ModeDangerFullAccess:
		return EnforcementPolicyOnlyNoOSIsolation, "danger-full-access 档不启用 ACL 受限运行"
	case !platformSupportsRunner():
		return EnforcementPolicyOnlyNoOSIsolation, "本平台没有 ACL runner 实现（受控根只在 Windows 上生效）"
	case strings.TrimSpace(spec.ParentDir) == "":
		return EnforcementPolicyOnlyNoOSIsolation, "未配置 " + EnvACLRoot + "：命令仍以宿主用户权限运行"
	}
	root, err := spec.rootPath()
	if err != nil {
		return EnforcementPolicyOnlyNoOSIsolation, "受控根不可用：" + err.Error()
	}
	return EnforcementWindowsACLWriteOnly,
		"受控根 " + root + "：只限制根之外的写/删，读/网络/进程不受限"
}

// RunnerEnv 是给子进程/工具用的环境行：把父目录传下去。
// 空 parent 返回 nil（不启用），不写空值——空值读回来是「未启用」，与「设成空」不可区分。
func RunnerEnv(parent string) []string {
	if strings.TrimSpace(parent) == "" {
		return nil
	}
	return []string{EnvACLRoot + "=" + parent}
}

// tempEnv 把 TMP/TEMP/TMPDIR 指到受控根里的临时目录，其余环境原样保留。
//
// base 为 nil 时从 os.Environ 起：os/exec 只有在 Cmd.Env 为 nil 时才继承父环境，
// 这里一旦要覆盖 TMP 就必须自己把父环境铺开。
func tempEnv(base []string, dir string) []string {
	if base == nil {
		base = os.Environ()
	}
	override := map[string]string{
		"TMP":    dir,
		"TEMP":   dir,
		"TMPDIR": dir,
	}
	out := make([]string, 0, len(base)+len(override))
	for _, kv := range base {
		name := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			name = kv[:i]
		}
		if _, ok := override[strings.ToUpper(name)]; ok {
			continue
		}
		out = append(out, kv)
	}
	for _, name := range []string{"TMP", "TEMP", "TMPDIR"} {
		out = append(out, name+"="+override[name])
	}
	return out
}

// WithinRoot 用 filepath.Rel 判断 path 是否在 root 之内（与包内边界判定同一套规则）。
//
// 导出它是因为工具侧要判「workdir 是否落在受控根里」：静默替调用方换 cwd 会让命令在
// 错误目录里产生副作用，所以受控根模式下越界必须 fail-loud，而判据不能各写一份。
func WithinRoot(root, path string) bool {
	return withinRoot(root, path)
}
