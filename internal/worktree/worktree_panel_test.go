package worktree

// 面板状态要的两件事实：**活的**创建/恢复进度（Progress）与原始折叠结果
// （Snapshot）。这两个出口是官方 ForgePanelState 里 worktree.creation 与
// worktree.editable 的唯一真值来源，所以必须证明它们**跟着状态机变**，
// 不是常量。

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Progress 必须在创建开始/结束这两个时刻上跟着变，Held 跟着 inbox hold 变。
func TestProgressFollowsLiveCreation(t *testing.T) {
	repo := newRepo(t)
	store := newMemStore()
	git := &blockingGit{
		real:    ExecGit{Timeout: 30 * time.Second},
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	svc := NewService(store, git)
	store.header["sp"] = repo

	if _, ok := svc.Progress("sp"); ok {
		t.Fatal("还没请求时不该有进度")
	}
	if err := svc.RequestWorktree("sp", nil); err != nil {
		t.Fatalf("RequestWorktree: %v", err)
	}

	go func() { _ = svc.PreStep(context.Background(), "sp", 1) }()
	select {
	case <-git.started:
	case <-time.After(5 * time.Second):
		t.Fatal("创建没开始")
	}

	progress, ok := svc.Progress("sp")
	if !ok {
		t.Fatal("创建进行中必须有进度（否则面板 creation 永远是 null）")
	}
	if progress.Kind != ProgressKindCreation {
		t.Fatalf("Kind 必须是 creation，得到 %q", progress.Kind)
	}
	if progress.Phase != ProgressPhasePrepare {
		t.Fatalf("建分支阶段的 Phase 必须是 prepare，得到 %q", progress.Phase)
	}
	if progress.Branch == "" {
		t.Fatal("进度必须带真实分支名")
	}
	if progress.From != nil {
		t.Fatalf("没请求 base 分支时 From 必须是 nil，得到 %v", *progress.From)
	}
	if progress.Held {
		t.Fatal("没有消息在 hold 里时 Held 必须是 false")
	}

	// 打开一个 inbox hold：Held 必须跟着变 true。
	holdCtx, cancelHold := context.WithCancel(context.Background())
	defer cancelHold()
	holdDone := make(chan error, 1)
	go func() { holdDone <- svc.Wait(holdCtx, "sp") }()

	heldDeadline := time.Now().Add(2 * time.Second)
	held := false
	for time.Now().Before(heldDeadline) {
		if p, ok := svc.Progress("sp"); ok && p.Held {
			held = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !held {
		t.Fatal("有消息在 hold 里等时 Held 必须是 true")
	}

	// 停掉创建：进度必须消失（面板 creation 回到 null）。
	if err := svc.StopCreation("sp"); err != nil {
		t.Fatalf("StopCreation: %v", err)
	}
	if _, ok := svc.Progress("sp"); ok {
		t.Fatal("创建结束后必须没有进度")
	}
	cancelHold()
	select {
	case <-holdDone:
	case <-time.After(5 * time.Second):
		t.Fatal("hold 没退出")
	}
}

// Snapshot 必须给**原始**折叠结果（不是压平的 Status 视图）：面板要靠
// TurnStarted / Created / Failure.Step 分别决定 editable 与 failure。
func TestSnapshotExposesFoldInputs(t *testing.T) {
	store := newMemStore()
	svc := NewService(store, ExecGit{})
	store.header["ss"] = filepath.Join(t.TempDir(), "src")

	snapshot, err := svc.Snapshot("ss")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapshot.TurnStarted || snapshot.Created != nil || snapshot.Requested {
		t.Fatalf("空会话不该有这些值：%+v", snapshot)
	}

	store.append("ss", "turn/start", map[string]any{})
	snapshot, err = svc.Snapshot("ss")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !snapshot.TurnStarted {
		t.Fatal("turn/start 之后 TurnStarted 必须是 true（editable 就是靠它翻 false）")
	}

	store.append("ss", EventType, Record{Type: RecordFailed, Branch: "b", Base: "main", Step: "checking-out-files", Reason: "boom"})
	snapshot, err = svc.Snapshot("ss")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapshot.Failure == nil || snapshot.Failure.Step != "checking-out-files" || snapshot.Failure.Reason != "boom" {
		t.Fatalf("Failure 必须带原始 step/reason：%+v", snapshot.Failure)
	}
}

// OfficialBase 是"内部表示 → 官方 from"的唯一转换点，直接钉住两条规则：
// 空串与字面量 "HEAD" 都是官方 null，真分支名原样带出。
//
// "HEAD" 判据**大小写敏感**：git 的全大写 HEAD 是唯一的内建 ref，小写 "head"
// 是可以存在的普通分支名，不能一起吞掉。
func TestOfficialBaseMapsHEADToNil(t *testing.T) {
	for _, blank := range []string{"", "  ", "HEAD"} {
		if got := OfficialBase(blank); got != nil {
			t.Fatalf("%q 必须映射成官方 null，得到 %q", blank, *got)
		}
	}
	for _, branch := range []string{"main", "release/1.0", " feature "} {
		want := branch
		got := OfficialBase(branch)
		if got == nil || *got != strings.TrimSpace(want) {
			t.Fatalf("%q 必须原样带出，得到 %v", branch, got)
		}
	}
}
