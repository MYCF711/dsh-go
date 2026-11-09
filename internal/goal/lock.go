package goal

// 跨进程互斥。
//
// # 为什么需要它
//
// serve 模式下模型的 goal 工具跑在**子进程**（`dshd run`），宿主 store 跑在
// 父进程；两个进程读写同一份 `<home>/state/session-goal.json`。光靠
// `sync.Mutex` 只能串行进程内的调用：父进程给 A 会话推进轮次、子进程给 B 会话
// 建目标，两次读-改-写会互相覆盖对方的会话记录（最后写入者赢，另一个会话的
// 目标凭空消失）。
//
// # 为什么是 OS 文件锁
//
// 曾经用过"锁目录"（`os.Mkdir` 原子创建 + 人工陈旧判定），它有一个**真实
// 事故**：serve 被 `stop()` 强杀时正好持锁，锁目录留了下来，下一次启动等满
// 10s 后报"取 goal 锁超时"，goal 域整体不可用（测试里可复现：A/B 重启用例
// 偶发失败）。OS 文件锁由内核在进程退出时释放，天然没有这个问题。
//
// 平台实现分别在 `lock_windows.go`（`LockFileEx`）与 `lock_unix.go`（`flock`）；
// 本文件只放与平台无关的重试/超时策略。

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// lockWaitTimeout 是一次取锁最多等多久。超过就是真卡住了，明确报错，
	// 不能让调用方无限期挂着（HTTP 请求会 120s 超时，这里先失败）。
	lockWaitTimeout = 10 * time.Second
	// lockPollInterval 是重试间隔：goal 写不是热路径，2ms 的抖动可以忽略。
	lockPollInterval = 2 * time.Millisecond
)

// errLockBusy 表示锁被别的持有者占着（不是真错误，是"重试"信号）。
var errLockBusy = errors.New("goal 锁被占用")

// Lock 取 `<path>.lock` 的**排它文件锁**，返回解锁函数。
//
// 调用方必须 `defer unlock()`；解锁是幂等的（重复调用不会误放别人的锁）。
// 取锁失败返回 `ErrPersistence`（调用方按"状态没提交"处理，不能返回成功）。
//
// 崩溃安全：锁由内核挂在打开的文件句柄上，进程被杀/崩溃时自动释放，
// 不会留下需要人工清理的残留。
func Lock(path string) (func(), error) {
	lockPath := path + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return nil, fmt.Errorf("%w: 建 goal 锁目录 %s: %v", ErrPersistence, filepath.Dir(lockPath), err)
	}
	file, err := openLockFile(lockPath)
	if err != nil {
		return nil, fmt.Errorf("%w: 打开 goal 锁文件 %s: %v", ErrPersistence, lockPath, err)
	}
	deadline := time.Now().Add(lockWaitTimeout)
	for {
		err := tryLockFile(file)
		if err == nil {
			break
		}
		if !errors.Is(err, errLockBusy) {
			_ = file.Close()
			return nil, fmt.Errorf("%w: 取 goal 锁 %s: %v", ErrPersistence, lockPath, err)
		}
		if time.Now().After(deadline) {
			_ = file.Close()
			return nil, fmt.Errorf("%w: 取 goal 锁 %s 超时 %s", ErrPersistence, lockPath, lockWaitTimeout)
		}
		time.Sleep(lockPollInterval)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = unlockFile(file)
			_ = file.Close()
		})
	}, nil
}

// openLockFile 打开（必要时创建）锁文件。
//
// 兼容一次历史遗留：早期实现用**目录**当锁（`os.Mkdir`）。那种残留目录会让
// `OpenFile` 报 EISDIR；它不是有效锁，删掉重来。正常路径永远走不到这里。
func openLockFile(lockPath string) (*os.File, error) {
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err == nil {
		return file, nil
	}
	if info, statErr := os.Stat(lockPath); statErr == nil && info.IsDir() {
		if removeErr := os.Remove(lockPath); removeErr != nil {
			return nil, fmt.Errorf("清理历史锁目录 %s: %v", lockPath, removeErr)
		}
		return os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	}
	if errors.Is(err, fs.ErrExist) {
		return os.OpenFile(lockPath, os.O_RDWR, 0o600)
	}
	return nil, err
}
