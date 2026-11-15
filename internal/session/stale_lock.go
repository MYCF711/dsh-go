package session

// 死进程留下的会话写锁的**显式**回收。
//
// ## 修的是什么
//
// 宿主被强杀（任务管理器结束进程、kill -9、断电）时，`claimWriter` 写下的
// `<session>.lock` 不会有人 `releaseWriter`——它在磁盘上留了下来。重启后第一次
// prompt 打开这份会话，`claimWriter` 只会等满 `sessionLockWait`（2s）然后拒绝：
//
//	会话正被另一个进程写入（锁文件 …）；确认没有别的 dshd 在用这份会话后删掉它再 resume
//
// 用户看到的是"这个会话再也发不出去"，而且**错误文案指向的是一个根本不存在
// 的并发写者**——重启之后那个进程已经不在了。
//
// ## 为什么回收不放进 claimWriter
//
// `claimWriter` 的既有契约是「只等、不删、不按 pid 强清」（lock_wait_test.go 的
// T12 钉住：持续存在的未知锁必须让打开失败、且锁文件字节不变）。那条契约保护
// 的是**读不懂的锁**：锁内容格式一旦不是本包写的，就没有任何依据判断它该不该删。
// 把"探死 pid + 删锁"塞进抢占路径，等于让每一次普通抢占都带着删别人锁的能力。
//
// 所以分成两步，谁需要谁调：
//
//  1. `claimWriter` 保持原样——抢占时**只等不删**；
//  2. 宿主在"人被强杀过、现在要接着写"这条路径上显式调 `ReclaimStaleWriterLock`，
//     再由 `OpenSessionAtRecoveringStale` 重试一次。
//
// ## 回收的准入条件（四条同时满足，缺一条都不删）
//
//  1. 锁文件能读到，而且内容是 `claimWriter` 自己写的形状
//     （`pid=<正整数>` + `at=<RFC3339>`）——别人的锁/半截文件一律不碰；
//  2. 锁里的 pid 不是本进程（本进程的锁是重入，不是失效）；
//  3. 那个 pid **确定已经不在**（`pidAlive` 为假）；
//  4. 删除本身成功（失败原样上报，不静默）。
//
// pid 被新的 live 进程复用时 `pidAlive` 为真 ⇒ 不回收。也就是说这个判断的
// 失败方向永远是"少回收一次"，不会变成"删掉一个真在写的进程的锁"。

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// writerLockOwner 是锁文件里记的持有者。
type writerLockOwner struct {
	pid int
	at  time.Time
}

// parseWriterLock 只认 `claimWriter` 写的形状：`pid=<正整数>\nat=<RFC3339>\n`。
//
// 严格是有意的：这是"能不能删这把锁"的唯一依据，宽松解析等于把认错的锁删掉。
func parseWriterLock(content string) (writerLockOwner, bool) {
	var (
		owner   writerLockOwner
		havePID bool
		haveAt  bool
	)
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if value, ok := strings.CutPrefix(line, "pid="); ok {
			pid, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || pid <= 0 {
				return writerLockOwner{}, false
			}
			owner.pid = pid
			havePID = true
			continue
		}
		if value, ok := strings.CutPrefix(line, "at="); ok {
			at, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
			if err != nil {
				return writerLockOwner{}, false
			}
			owner.at = at
			haveAt = true
			continue
		}
		// 多出来的任何一行都说明这不是本包写的锁。
		return writerLockOwner{}, false
	}
	if !havePID || !haveAt {
		return writerLockOwner{}, false
	}
	return owner, true
}

// ReclaimStaleWriterLock 回收**确定已经退出的进程**留下的会话写锁。
//
// 返回 reclaimed=true 表示"锁已经不在了，调用方可以重试抢占"（自己删的，或
// 另一个进程先一步删掉）；返回 (false, nil) 表示"没有可回收的锁"——没锁、
// 锁读不到、锁不是本包的形状、或者那个 pid 还活着。这些都不是错误：调用方
// 应当把**原先的错误**原样报出去。
//
// 删之前用 `os.Rename` 把这把锁**原子地抢走**再删：读内容与删除之间有两个
// 独立竞态（另一个进程也在回收；死锁刚被收走、新 writer 立刻抢占），只有
// rename 的赢家会真的走到删除，且删前还要复核抢到的确实是刚读到的那份内容。
func ReclaimStaleWriterLock(path string) (bool, error) {
	// 进程内先串行化：并发回收时只有一个能走到 rename。
	unlock := lockSessionPath(path)
	defer unlock()

	lock := path + ".lock"
	content, err := readLockFileShared(lock)
	if err != nil {
		return false, nil // 没锁（或读不到）＝没什么可回收
	}
	owner, ok := parseWriterLock(string(content))
	if !ok {
		return false, nil
	}
	if owner.pid == os.Getpid() {
		return false, nil
	}
	if pidAlive(owner.pid) {
		return false, nil
	}

	stolen := fmt.Sprintf("%s.reclaim-%d-%d", lock, os.Getpid(), time.Now().UnixNano())
	if renameErr := os.Rename(lock, stolen); renameErr != nil {
		if os.IsNotExist(renameErr) {
			// 读完之后被别人收走了：锁已经不在了，调用方重试即可。
			return true, nil
		}
		return false, fmt.Errorf("回收死进程(%d)留下的会话写锁 %s 失败: %w", owner.pid, lock, renameErr)
	}
	// 复核：万一 rename 抢到的是"死锁刚被别人收走、新 writer 立刻抢占"出来的
	// 那把**新锁**，必须原样放回去——回收能力不许删掉真在写的进程的锁。
	if after, readErr := os.ReadFile(stolen); readErr != nil || string(after) != string(content) {
		if backErr := os.Rename(stolen, lock); backErr != nil {
			return false, fmt.Errorf("写锁回收复核不一致，且放回失败（读=%v 放回=%v）", readErr, backErr)
		}
		return false, nil
	}
	if removeErr := os.Remove(stolen); removeErr != nil && !os.IsNotExist(removeErr) {
		return true, fmt.Errorf("回收死进程(%d)留下的会话写锁 %s 失败: %w", owner.pid, lock, removeErr)
	}
	return true, nil
}

// OpenSessionAtRecoveringStale 打开会话续写；撞上**死进程**留下的写锁时先回收再重试一次。
//
// 只对"写锁被占"这一种失败做恢复：其它错误（文件损坏、id 不符、打不开写入）
// 原样返回——那些不是过期锁的问题，重试只会把真实原因盖住。
func OpenSessionAtRecoveringStale(path string) (*Log, error) {
	lg, err := OpenSessionAt(path)
	if err == nil || !errors.Is(err, ErrWriterBusy) {
		return lg, err
	}
	reclaimed, reclaimErr := ReclaimStaleWriterLock(path)
	if reclaimErr != nil {
		return nil, errors.Join(err, reclaimErr)
	}
	if !reclaimed {
		// 锁属于一个还活着的进程，或者不是本包写的：照原样拒绝，不硬写。
		return nil, err
	}
	return OpenSessionAt(path)
}
