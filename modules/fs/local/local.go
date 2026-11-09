// Package local 是 FS 的默认实现：路径归一 + 读后写观测策略。
//
// 它刻意不做沙箱判定（那是 sandbox 模块的事），只守住两条：
// 不许写到工作区外面，不许盲改一个没读过或已过期的文件。
package local

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"dshgo/internal/kernel"
	"dshgo/internal/sandbox"
	"dshgo/internal/spi"
)

type stamp struct {
	mod  time.Time
	size int64
}

// FS 实现 spi.FS。
type FS struct {
	root     string
	homeDir  func() (string, error)
	extra    []string // 工作区之外仍然允许读的根（外置结果 artifact）
	extraCan []string // extra 的 canonical 形式（不存在时保留 Abs 结果）
	sandbox  *sandbox.Boundary
	// gate 是 per-call 策略闸门（可选）：设置之后，CheckWritableCall/WriteCall
	// 能把"这一次调用"的会话、权限与一次性审批绑进判定。未设置时旧路径不变，
	// 但 per-call API fail-loud，不静默降级成"没有策略"。
	gate *sandbox.Gate
	mu   sync.Mutex
	seen map[string]stamp
}

// Option 是 New 的可选项。
type Option func(*FS)

// WithHome 设置显式 ~ 路径使用的宿主 home。
//
// 缺省使用 os.UserHomeDir，并且只在解析 ~ 或 ~/... 时调用；注入临时目录
// 可以让测试完全不碰真实用户 home。home 不要求预先存在，但 tilde 路径
// 归一化后必须仍在这个目录内。
func WithHome(dir string) Option {
	return func(f *FS) {
		f.homeDir = func() (string, error) { return dir, nil }
	}
}

// WithSandboxBoundary 注入 FS 的路径策略。
//
// 生产由进程环境解析（DSHD_SANDBOX_MODE；缺省 workspace-write，与既有边界一致）；
// 这个选项给测试和宿主将来的 per-session 接线用。nil 被忽略，不会把边界关掉。
func WithSandboxBoundary(b *sandbox.Boundary) Option {
	return func(f *FS) {
		if b != nil {
			f.sandbox = b
		}
	}
}

// WithSandboxGate 注入 per-call 策略闸门（与 bash/pwsh 用的是同一个 Gate）。
//
// 进程环境的 DSHD_SANDBOX_MODE 存在时 New 会自动装一个（与命令侧同源），
// 这个选项给宿主把已经构造好的 Gate 传进来（测试与 ACP 用）。
func WithSandboxGate(g *sandbox.Gate) Option {
	return func(f *FS) {
		if g != nil {
			f.gate = g
		}
	}
}

// WithExtraReadRoot 允许**读**工作区之外的某个根。
//
// 存在的理由：工具输出超阈值会被外置成 artifact（放在 home 下），
// 然后 Hint 告诉模型「用 read 读全文」。如果 FS 不放行这个目录，
// 那句 Hint 就是假的 —— 模型照着做会得到"越出工作区"。
// 只放读，不放写：artifact 由宿主自己写。
func WithExtraReadRoot(dir string) Option {
	return func(f *FS) {
		if strings.TrimSpace(dir) == "" {
			return
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			return
		}
		f.extra = append(f.extra, filepath.ToSlash(filepath.Clean(abs)))
	}
}

// New 以 root 为工作区根。root 会被归一成绝对路径 + 正斜杠。
func New(root string, opts ...Option) (*FS, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("解析工作区根失败: %w", err)
	}
	f := &FS{
		root:    filepath.ToSlash(filepath.Clean(abs)),
		homeDir: os.UserHomeDir,
		seen:    make(map[string]stamp),
	}
	for _, o := range opts {
		o(f)
	}
	for _, e := range f.extra {
		canonical, err := sandbox.CanonicalPath(e)
		if err != nil {
			canonical = e // artifact 目录可能还没建；保留 Abs 形式，等它出现再按同一规则比较
		}
		f.extraCan = append(f.extraCan, filepath.ToSlash(canonical))
	}
	if f.sandbox == nil {
		boundary, err := sandbox.BoundaryFromEnv(f.root)
		if err != nil {
			return nil, err
		}
		f.sandbox = boundary
	}
	if f.gate == nil {
		// 与 bash/pwsh 同源：环境里没接线就是 nil（旧行为），设了未知值则装配期炸。
		gate, err := sandbox.GateFromEnv(f.root)
		if err != nil {
			return nil, err
		}
		f.gate = gate
	}
	return f, nil
}

// SandboxMode 返回 FS 当前生效的真实档位（给工具结果和测试用）。
func (f *FS) SandboxMode() sandbox.Mode {
	_, boundary, _ := f.snapshot()
	if boundary == nil {
		return ""
	}
	return boundary.Mode()
}

// snapshot 在锁内取一次一致快照：工作区根、写边界、只读额外根。
// SetRoot/SetSandboxMode/AddExtraReadRoot 会并发替换这些字段，读路径必须走它。
func (f *FS) snapshot() (string, *sandbox.Boundary, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.root, f.sandbox, append([]string(nil), f.extraCan...)
}

// SetSandboxMode 把进程级 FS 边界切到另一个档位。
//
// 为什么允许切换：ACP 是一个进程多个会话，而 FS 的 Read/Write 没有 ctx，
// 边界只能是进程级的。调用方（cmd/dshd）必须先证明本进程只有一个档位
// （见 acpRunner 的进程边界），再在这里落值；档位非法 fail-loud。
func (f *FS) SetSandboxMode(mode sandbox.Mode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	boundary, err := sandbox.NewBoundary(mode, f.root)
	if err != nil {
		return err
	}
	f.sandbox = boundary
	return nil
}

// AddExtraReadRoot 追加一个**只读**根（工作区之外仍允许读）。
//
// 供 ACP additionalDirectories 使用：一个进程只服务一个工作区，所以这里是
// 进程级追加；写权限不受影响（策略层仍把 extra 判成只读）。
func (f *FS) AddExtraReadRoot(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return fmt.Errorf("额外只读根为空")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("解析额外只读根失败: %w", err)
	}
	st, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("额外只读根不可用: %w", err)
	}
	if !st.IsDir() {
		return fmt.Errorf("额外只读根不是目录: %s", abs)
	}
	canonical, err := sandbox.CanonicalPath(abs)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.extra = append(f.extra, filepath.ToSlash(filepath.Clean(abs)))
	f.extraCan = append(f.extraCan, filepath.ToSlash(canonical))
	return nil
}

func (f *FS) Root() string {
	root, _, _ := f.snapshot()
	return root
}

// SetRoot 换工作区根。
//
// 为什么需要它：ACP 客户端在 session/new 里带 cwd，而工具的根是**装配期**定下的。
// 不支持换根的话，客户端说"我在 A 目录"，工具却在宿主启动目录里干活——
// 实测踩过：Marтty 以 --workspace 指向某目录，模型报"文件不存在"，
// 然后在 203 个文件里翻，全是宿主仓库的文件。
func (f *FS) SetRoot(root string) error {
	if strings.TrimSpace(root) == "" {
		return fmt.Errorf("工作区根不能为空")
	}
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		return fmt.Errorf("工作区根不是目录: %s", root)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	boundary, err := sandbox.NewBoundary(f.SandboxMode(), abs)
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.root = filepath.ToSlash(filepath.Clean(abs))
	f.sandbox = boundary
	f.seen = make(map[string]stamp) // 换根之后"读过哪些文件"的账要清空
	f.mu.Unlock()
	return nil
}

// within 判断 canonical 路径是否在允许的根之内（工作区 + 只读额外根）。
// 传进来的必须是 canonical 路径；字符串前缀比较会把 workspace-evil 放进来，
// 所以这里用 withinRoot 的 filepath.Rel 规则。
func withinBoundary(root string, extra []string, abs string) bool {
	slashed := filepath.ToSlash(abs)
	if withinRoot(filepath.ToSlash(root), slashed) {
		return true
	}
	for _, e := range extra {
		if withinRoot(e, slashed) {
			return true
		}
	}
	return false
}

func withinRoot(root, path string) bool {
	rel, err := filepath.Rel(filepath.FromSlash(root), filepath.FromSlash(path))
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

func (f *FS) resolveHome() (string, error) {
	home, err := f.homeDir()
	if err != nil {
		return "", fmt.Errorf("解析宿主 home 失败: %w", err)
	}
	if strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("解析宿主 home 失败: home 为空")
	}
	abs, err := filepath.Abs(home)
	if err != nil {
		return "", fmt.Errorf("解析宿主 home 失败: %w", err)
	}
	return filepath.ToSlash(filepath.Clean(abs)), nil
}

// Resolve 归一化并做越界检查。
func (f *FS) Resolve(p string) (string, error) {
	root, boundary, extra := f.snapshot()
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("路径为空；给一个相对工作区根(%s)的路径", root)
	}
	slashed := strings.ReplaceAll(p, "\\", "/")

	// 只有完整的 ~ 或 ~/... 是 home 通道。~other 和路径中间的 ~
	// 继续走下面的旧规则，不会被 shell 式展开。
	if slashed == "~" || strings.HasPrefix(slashed, "~/") {
		home, err := f.resolveHome()
		if err != nil {
			return "", err
		}
		rest := strings.TrimLeft(strings.TrimPrefix(slashed, "~"), "/")
		abs := filepath.Join(filepath.FromSlash(home), filepath.FromSlash(rest))
		slash := filepath.ToSlash(abs)
		// home 里可能有一个指向外部的目录联接（reparse point）。读路径必须与写路径
		// 用同一条 canonical 规则：先解析再判定，不能拿字符串前缀当边界。
		canonicalTarget, err := sandbox.CanonicalPath(slash)
		if err != nil {
			return "", err
		}
		canonicalHome, err := sandbox.CanonicalPath(home)
		if err != nil {
			return "", err
		}
		if withinRoot(filepath.ToSlash(canonicalHome), filepath.ToSlash(canonicalTarget)) {
			return slash, nil
		}
		return "", fmt.Errorf("%w: 路径 %q 越出宿主 home %s（canonical %s）；显式 ~ 路径只允许操作 home 内的文件",
			sandbox.ErrPathOutsideHome, p, home, filepath.ToSlash(canonicalTarget))
	}

	var abs string
	if filepath.IsAbs(filepath.FromSlash(slashed)) {
		abs = filepath.Clean(filepath.FromSlash(slashed))
	} else {
		abs = filepath.Join(filepath.FromSlash(root), filepath.FromSlash(slashed))
	}
	slash := filepath.ToSlash(abs)
	if boundary != nil && boundary.Mode() == sandbox.ModeDangerFullAccess {
		// danger-full-access 明确不提供路径沙盒：读/写可以越出工作区。
		return slash, nil
	}
	canonical, err := sandbox.CanonicalPath(slash)
	if err != nil {
		return "", err
	}
	canonicalSlash := filepath.ToSlash(canonical)
	// boundary.WorkspaceRoot() 已经是 canonical 形式；用非 canonical 的 root
	// 比较会在 8.3 短名/长名之间误判成"越界"。
	workspaceRoot := boundary.WorkspaceRoot()
	if workspaceRoot == "" {
		workspaceRoot = root
	}
	if withinBoundary(workspaceRoot, extra, canonicalSlash) {
		return slash, nil
	}
	return "", fmt.Errorf("%w: 路径 %q 越出工作区 %s（canonical %s）；只允许操作工作区内的文件",
		sandbox.ErrOutsideReadBoundary, p, root, canonicalSlash)
}

// Read 先 stat 再读，并用 stat 的结果登记——若读的瞬间文件被改，登记的是旧值，
// 后续写入会被要求重读（宁可多一次读，不可静默覆盖）。
func (f *FS) Read(p string) ([]byte, error) {
	abs, err := f.Resolve(p)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s 不存在", p)
		}
		return nil, fmt.Errorf("读 %s 失败: %w", p, err)
	}
	if st.IsDir() {
		return nil, fmt.Errorf("%s 是目录；要看目录内容用 glob", p)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("读 %s 失败: %w", p, err)
	}
	f.mark(abs, st.ModTime(), st.Size())
	// per-call 写路径（CheckWritableCall/WriteCall）拿的是 canonical path；
	// 这里把同一份观测也登记到 canonical 别名上，避免"读过却报没读过"。
	if canonical, cerr := sandbox.CanonicalPath(abs); cerr == nil {
		f.mark(filepath.ToSlash(canonical), st.ModTime(), st.Size())
	}
	return data, nil
}

// CheckWritable 实施读后写策略。
//
// 措辞对齐 DSH（真实语料实测）：一句话说清「做了什么、为什么不行、下一步怎么做」，
// 模型不用再去看别的字段就能改。这三类占 edit 失败的 84%。
func (f *FS) CheckWritable(p, op string) error {
	abs, err := f.Resolve(p)
	if err != nil {
		return f.resolveWriteError(p, err)
	}
	_, boundary, _ := f.snapshot()
	if _, err := boundary.CheckWrite(abs); err != nil {
		return f.writePolicyError(p, err)
	}
	st, err := os.Stat(abs)
	if os.IsNotExist(err) {
		return nil // 新建文件不需要先读
	}
	if err != nil {
		return fmt.Errorf("检查 %s 失败: %w", p, err)
	}

	f.mu.Lock()
	s, seen := f.seen[abs]
	f.mu.Unlock()

	if !seen {
		return fmt.Errorf(`cannot modify %q: file has not been read — read the file, then retry`, p)
	}
	if !s.mod.Equal(st.ModTime()) || s.size != st.Size() {
		verb := op
		if verb == "" {
			verb = "modify"
		}
		return fmt.Errorf(`cannot %s %q: file changed since it was read — re-read the file, then retry`, verb, p)
	}
	return nil
}

// Stat 查存在性与大小。
func (f *FS) Stat(p string) (int64, bool, error) {
	abs, err := f.Resolve(p)
	if err != nil {
		return 0, false, err
	}
	st, err := os.Stat(abs)
	if os.IsNotExist(err) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("检查 %s 失败: %w", p, err)
	}
	return st.Size(), true, nil
}

// Write 写文件并登记新内容。
func (f *FS) Write(p string, data []byte) error {
	abs, err := f.Resolve(p)
	if err != nil {
		return f.resolveWriteError(p, err)
	}
	_, boundary, _ := f.snapshot()
	if _, err := boundary.CheckWrite(abs); err != nil {
		return f.writePolicyError(p, err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return fmt.Errorf("建目录失败: %w", err)
	}
	if err := os.WriteFile(abs, data, 0o644); err != nil {
		return fmt.Errorf("写 %s 失败: %w", p, err)
	}
	if st, err := os.Stat(abs); err == nil {
		f.mark(abs, st.ModTime(), st.Size())
	}
	return nil
}

// resolveWriteError 把"读边界"的越界错误在写路径上标注成写边界错误，
// 让调用方用 errors.Is(err, sandbox.ErrWriteOutsideWorkspace) 分类。
func (f *FS) resolveWriteError(p string, err error) error {
	if errors.Is(err, sandbox.ErrOutsideReadBoundary) {
		return fmt.Errorf("%w: %v", sandbox.ErrWriteOutsideWorkspace, err)
	}
	return err
}

// writePolicyError 把策略错误翻成模型能照做的下一步。
// 错误链保留 sentinel，宿主/测试可以 errors.Is；文案说清"哪一档、为什么、怎么办"。
func (f *FS) writePolicyError(p string, err error) error {
	switch {
	case errors.Is(err, sandbox.ErrReadOnlyWrite):
		return fmt.Errorf("路径 %q 写被拒绝：%w；要写文件先把会话档位切到 workspace-write 或 danger-full-access", p, err)
	case errors.Is(err, sandbox.ErrWriteOutsideWorkspace):
		root, _, _ := f.snapshot()
		return fmt.Errorf("路径 %q 越出工作区 %s；不允许写工作区外的文件（要越界写请显式选择 danger-full-access 档）: %w", p, root, err)
	default:
		return err
	}
}

func (f *FS) mark(abs string, mod time.Time, size int64) {
	f.mu.Lock()
	f.seen[abs] = stamp{mod: mod, size: size}
	f.mu.Unlock()
}

// Module 返回装配用的模块描述。
func Module(root string, opts ...Option) kernel.Module {
	return kernel.Module{
		ID:       "fs",
		Backend:  kernel.BackendStatic,
		Provides: []string{"fs"},
		Register: func(s *kernel.Scope) error {
			fs, err := New(root, opts...)
			if err != nil {
				return err
			}
			return kernel.Provide(s, spi.FSKey, spi.FS(fs))
		},
	}
}
