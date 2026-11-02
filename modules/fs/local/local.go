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
	root string
	mu   sync.Mutex
	seen map[string]stamp
}

// New 以 root 为工作区根。root 会被归一成绝对路径 + 正斜杠。
func New(root string) (*FS, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("解析工作区根失败: %w", err)
	}
	return &FS{root: filepath.ToSlash(filepath.Clean(abs)), seen: make(map[string]stamp)}, nil
}

func (f *FS) Root() string { return f.root }

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
	if slash != f.root && !strings.HasPrefix(slash, f.root+"/") {
		return "", fmt.Errorf("路径 %q 越出工作区 %s；只允许操作工作区内的文件", p, f.root)
	}
	return slash, nil
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
func (f *FS) CheckWritable(p string) error {
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
		return fmt.Errorf("还没读过 %s；先 read 它再改（读后写策略：不做盲改）", p)
	}
	if !s.mod.Equal(st.ModTime()) || s.size != st.Size() {
		return fmt.Errorf("%s 在读过之后被改过；重新 read 再改，避免覆盖别人的修改", p)
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
func Module(root string) kernel.Module {
	return kernel.Module{
		ID:       "fs",
		Backend:  kernel.BackendStatic,
		Provides: []string{"fs"},
		Register: func(s *kernel.Scope) error {
			fs, err := New(root)
			if err != nil {
				return err
			}
			return kernel.Provide(s, spi.FSKey, spi.FS(fs))
		},
	}
}
