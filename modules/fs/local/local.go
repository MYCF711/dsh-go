// Package local 是 FS 的默认实现：路径归一 + 读后写观测策略。
//
// 它刻意不做沙箱判定（那是 sandbox 模块的事），只守住两条：
// 不许写到工作区外面，不许盲改一个没读过或已过期的文件。
package local

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
)

type stamp struct {
	mod  time.Time
	size int64
}

// FS 实现 spi.FS。
type FS struct {
	root  string
	extra []string // 工作区之外仍然允许读的根（外置结果 artifact）
	mu    sync.Mutex
	seen  map[string]stamp
}

// Option 是 New 的可选项。
type Option func(*FS)

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
	f := &FS{root: filepath.ToSlash(filepath.Clean(abs)), seen: make(map[string]stamp)}
	for _, o := range opts {
		o(f)
	}
	return f, nil
}

func (f *FS) Root() string { return f.root }

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
	f.mu.Lock()
	f.root = filepath.ToSlash(filepath.Clean(abs))
	f.seen = make(map[string]stamp) // 换根之后"读过哪些文件"的账要清空
	f.mu.Unlock()
	return nil
}

// within 判断 abs 是否在允许的根之内（工作区 + 只读额外根）。
func (f *FS) within(abs string) bool {
	if abs == f.root || strings.HasPrefix(abs, f.root+"/") {
		return true
	}
	for _, e := range f.extra {
		if abs == e || strings.HasPrefix(abs, e+"/") {
			return true
		}
	}
	return false
}

// Resolve 归一化并做越界检查。
func (f *FS) Resolve(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("路径为空；给一个相对工作区根(%s)的路径", f.root)
	}
	slashed := strings.ReplaceAll(p, "\\", "/")
	var abs string
	if filepath.IsAbs(filepath.FromSlash(slashed)) {
		abs = filepath.Clean(filepath.FromSlash(slashed))
	} else {
		abs = filepath.Join(filepath.FromSlash(f.root), filepath.FromSlash(slashed))
	}
	slash := filepath.ToSlash(abs)
	if f.within(slash) {
		return slash, nil
	}
	return "", fmt.Errorf("路径 %q 越出工作区 %s；只允许操作工作区内的文件", p, f.root)
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
	return data, nil
}

// CheckWritable 实施读后写策略。
//
// 措辞对齐 DSH（真实语料实测）：一句话说清「做了什么、为什么不行、下一步怎么做」，
// 模型不用再去看别的字段就能改。这三类占 edit 失败的 84%。
func (f *FS) CheckWritable(p, op string) error {
	abs, err := f.Resolve(p)
	if err != nil {
		return err
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
		return err
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
