package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"dshgo/codecs/v4"
)

// 耐久工作目录（官方 `working-directory`）：**每个会话**有一个有效目录，
// 变更以 `working-directory/change` 事件落进 v4 日志，冷读靠重放恢复。
//
// 为什么不是 `os.Chdir`：进程 cwd 是全局的，多会话会互相串味，而且重放
// 看不到——重开之后"我在哪"只能靠猜。官方语义也明确"已有的进程保留它自己的
// 目录"：切目录**不改**任何正在跑的进程，只改这个会话的投影值。

// WorkingDirectoryChange 是 `working-directory/change` 事件的 data（官方形状）。
type WorkingDirectoryChange struct {
	// Cwd 是提交后的绝对目录。
	Cwd string `json:"cwd"`
	// 以下字段只在返回值里出现，不落盘（落盘形状必须与官方一字不差）。
	Previous string `json:"-"`
	Seq      int64  `json:"-"`
	At       int64  `json:"-"`
	// Recorded 表示这次调用真的追加了事件（目录没变时不追加）。
	Recorded bool `json:"-"`
}

// WorkingDirectoryState 是一次重放的折叠结果。
type WorkingDirectoryState struct {
	// Cwd 是有效目录；没有变更事件时回落到 header.cwd。
	Cwd string
	// Seq 是最后一条 cwd 事件的 seq；没有事件时是 -1。
	Seq int64
	// At 是最后一条 cwd 事件的时间戳。
	At int64
	// Changed 表示有效目录来自事件（而不是 header 兜底）。
	Changed bool
	// Events 是重放了几条有效 cwd 事件。
	Events int
}

// FoldWorkingDirectory 重放 cwd 事件。坏事件（空/相对路径/坏 JSON）一律
// 跳过——一条坏记录不许把"我在哪"弄坏。
func FoldWorkingDirectory(header v4.Header, events []v4.Event) (WorkingDirectoryState, error) {
	state := WorkingDirectoryState{Cwd: header.Cwd, Seq: -1}
	for _, event := range events {
		if event.Type != EventWorkingDirectoryChange {
			continue
		}
		var data struct {
			Cwd string `json:"cwd"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			continue
		}
		cwd := strings.TrimSpace(data.Cwd)
		if cwd == "" || strings.ContainsRune(cwd, 0) || !filepath.IsAbs(cwd) {
			continue
		}
		state = WorkingDirectoryState{Cwd: cwd, Seq: event.Seq, At: event.Time, Changed: true, Events: state.Events + 1}
	}
	return state, nil
}

// ResolveWorkingDirectory 把一次 set 的入参解析成"存在的绝对目录"。
//
// 官方规则：空串拒绝；相对路径按会话当前目录拼；目标必须是已存在的目录；
// 返回的是 canonical 绝对路径（不解析符号链接——那是另一个取舍，这里不做）。
func ResolveWorkingDirectory(current, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("working-directory: cd 不能为空")
	}
	if strings.ContainsRune(path, 0) {
		return "", fmt.Errorf("working-directory: 路径不能包含 NUL")
	}
	base := current
	if base == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("working-directory: 取不到当前目录: %w", err)
		}
		base = cwd
	}
	target := path
	if !filepath.IsAbs(target) {
		target = filepath.Join(base, target)
	}
	absolute, err := filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("working-directory: 解析绝对路径失败: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("working-directory: 目录不存在: %q", path)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("working-directory: 不是目录: %q", path)
	}
	return filepath.Clean(absolute), nil
}

// RecordWorkingDirectory 把一个会话的有效目录改成 cwd，并追加耐久事件。
//
// 目录没变时**不追加**事件（官方 `previous === directory` 直接返回）：
// 幂等的 set 不该在日志里留一串空变更。
func RecordWorkingDirectory(log *Log, cwd string) (WorkingDirectoryChange, error) {
	if log == nil {
		return WorkingDirectoryChange{}, fmt.Errorf("working-directory: 需要打开的会话日志")
	}
	current, err := log.WorkingDirectory()
	if err != nil {
		return WorkingDirectoryChange{}, fmt.Errorf("working-directory: 读当前目录失败: %w", err)
	}
	resolved, err := ResolveWorkingDirectory(current.Cwd, cwd)
	if err != nil {
		return WorkingDirectoryChange{}, err
	}
	change := WorkingDirectoryChange{Cwd: resolved, Previous: current.Cwd}
	if current.Cwd == resolved {
		return change, nil
	}
	previousSeq := log.Seq()
	if err := log.Append(EventWorkingDirectoryChange, WorkingDirectoryChange{Cwd: resolved}); err != nil {
		return WorkingDirectoryChange{}, fmt.Errorf("working-directory: 写变更事件失败: %w", err)
	}
	change.Recorded = true
	change.Seq = previousSeq
	change.At = time.Now().UnixMilli()
	return change, nil
}

// WorkingDirectory 重放这个会话的 cwd 状态（重新读盘，不信任缓存）。
func (l *Log) WorkingDirectory() (WorkingDirectoryState, error) {
	parsed, err := v4.ReadFile(l.Path)
	if err != nil {
		return WorkingDirectoryState{}, err
	}
	return FoldWorkingDirectory(parsed.Header, parsed.Events)
}

// ReopenLog 打开已存在的会话日志继续追加。
//
// 与 `CreateSession` 的区别：**seq 必须接着历史走**。从 0 重来会让新事件
// 与旧事件撞 seq，读路径（游标、checkpoint watermark、journal）全部失效。
func ReopenLog(path string) (*Log, error) {
	parsed, err := v4.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("重开会话失败: %w", err)
	}
	writer, err := v4.OpenAppend(path)
	if err != nil {
		return nil, fmt.Errorf("重开会话失败: %w", err)
	}
	nextSeq := int64(0)
	if len(parsed.Events) > 0 {
		nextSeq = parsed.Events[len(parsed.Events)-1].Seq + 1
	}
	return &Log{w: writer, Path: path, seq: nextSeq, now: time.Now}, nil
}

// CwdFollower 是 `spi.CwdFollower` 的会话级实现：换掉的是**这个会话**的
// 有效目录（耐久事件 + 投影值），不是进程 cwd。
//
// 装配点见 WIRING.md：`modules/tool/wd` 与 serve 的 cwd 注入应改成
// `NewCwdFollower(log)`，从而多会话不再互相串味、重开之后也能重放。
type CwdFollower struct {
	mu  sync.Mutex
	log *Log
	cwd string
}

// NewCwdFollower 为一个会话建 follower。log 为 nil 时所有写都失败（fail-loud）。
func NewCwdFollower(log *Log) *CwdFollower { return &CwdFollower{log: log} }

// SetCwd 实现 spi.CwdFollower。相对路径按该会话当前目录解析。
func (f *CwdFollower) SetCwd(dir string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.log == nil {
		return fmt.Errorf("working-directory: follower 没有会话日志")
	}
	// 当前目录只在这里读一次做 fail-loud：真正的路径解析在 RecordWorkingDirectory 里做
	// （它自己会读同一份状态），所以这里不保留返回值。
	if _, err := f.log.WorkingDirectory(); err != nil {
		return fmt.Errorf("working-directory: 读当前目录失败: %w", err)
	}
	// 相对路径按该会话当前目录解析（ResolveWorkingDirectory 负责），绝对路径原样。
	change, err := RecordWorkingDirectory(f.log, dir)
	if err != nil {
		return err
	}
	f.cwd = change.Cwd
	return nil
}

// Current 返回这个会话当前的有效目录（提交后的值）。
func (f *CwdFollower) Current() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cwd != "" {
		return f.cwd
	}
	if f.log == nil {
		return ""
	}
	state, err := f.log.WorkingDirectory()
	if err != nil {
		return ""
	}
	f.cwd = state.Cwd
	return f.cwd
}
