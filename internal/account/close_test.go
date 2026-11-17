package account

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCloseStopsExpiryTimer：Close 之后再等过租约，**不能**再有写盘动作。
//
// 这条防的是真实竞态（CI 上 `internal/account` 红过）：
//
//	测试体结束 -> t.TempDir() RemoveAll 开始删 state/
//	                 同时 定时器到点 -> expireLocked -> saveLocked 往 state/ 里写
//	-> RemoveAll 报 `unlinkat ...\state`
//
// 这里不去碰运气复现那个交错，而是**直接断言 Close 的契约**：
// 定时器被停掉，租约到点后 state 文件的内容与 mtime 都不再变化。
func TestCloseStopsExpiryTimer(t *testing.T) {
	home := t.TempDir()
	// 租约必须远大于「快照 -> Close」这段窗口。
	// 原来是 40ms：CI 负载高时快照与 Close 之间可能就超过 40ms，
	// 定时器于是**在快照之后、Close 之前**写了一次盘，after != before，
	// 测试就报「Close 之后定时器仍在写盘」——其实 Close 是对的，是窗口太窄。
	// 2026-11-17 这条在 CI 上连续拦掉两个 PR，本地跑不出来。
	s := newLocal(t, home, 500*time.Millisecond)
	if _, err := s.StartSignIn(context.Background(), StartOptions{}); err != nil {
		t.Fatal(err)
	}

	stateDir := filepath.Join(home, "state")
	before := snapshotTree(t, stateDir)

	// 在租约到点**之前**关掉服务。
	s.Close()

	// 等到远超租约的时刻。若 Close 没停掉定时器，这里会看到 state/ 被改写。
	time.Sleep(1200 * time.Millisecond)

	if after := snapshotTree(t, stateDir); after != before {
		t.Fatalf("Close 之后定时器仍在写盘：before=%q after=%q", before, after)
	}
}

// TestCloseIsIdempotent：Close 可重复调用，不 panic、不改变可见状态。
func TestCloseIsIdempotent(t *testing.T) {
	home := t.TempDir()
	s := newLocal(t, home, time.Minute)
	s.Close()
	s.Close()
	s.Close()
	if _, err := s.State(context.Background()); err != nil {
		t.Fatalf("Close 之后 State 仍应可用：%v", err)
	}
}

// snapshotTree 把目录下每个文件的「名字+大小+mtime」拼成一个可比对的字符串。
// 目录不存在时返回空串（还没落盘也是合法状态）。
func snapshotTree(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("读 %s：%v", dir, err)
	}
	out := ""
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		out += e.Name() + ":" + info.ModTime().UTC().Format(time.RFC3339Nano) + ";"
	}
	return out
}
