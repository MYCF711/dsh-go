package main

// 官方 `dsh-api-terminal-controller` 的四条生命周期预算在宿主 PTY 层的落地验证：
//
//	terminal.disposeGraceMs      (1000)      销毁前给 shell 的宽限（先礼后兵）
//	terminal.cleanupRetryMs      (60000)     清理失败的重试节奏
//	terminal.activityPollIntervalMs (30000)  活跃度采样 / 闲置判定
//	terminal.unattendedTimeoutMs (7200000)   无人值守终端的寿命上限
//
// 先红：officialPTYTerminalAPI 没有这四个预算，也没有 sweep()。

import (
	"context"
	"errors"
	"testing"
	"time"

	"dshgo/internal/transport"
)

// disposeGrace>0 时 Close 必须先"礼"（给 shell 发 exit 机会），不能一上来就强杀；
// grace==0 才是历史上的一刀切强制销毁。默认值 1000ms 由构造函数落。
func TestTerminalDisposeGracePoliteBeforeForce(t *testing.T) {
	cwd := t.TempDir()

	polite, _ := newTestPTYTerminalAPI(t, cwd)
	polite.disposeGrace = 500 * time.Millisecond
	if _, err := polite.TerminalCreate(context.Background(), "sess-a", transport.OfficialTerminalCreateRequest{
		ID: "g1", Cols: 80, Rows: 24,
	}); err != nil {
		t.Fatalf("create 失败：%v", err)
	}
	if err := polite.TerminalClose(context.Background(), "sess-a", "g1"); err != nil {
		t.Fatalf("close 失败：%v", err)
	}
	how, waited, gotPolite := polite.disposeOutcome("sess-a", "g1")
	if !gotPolite {
		t.Fatalf("disposeGrace>0 时必须先走「礼」的路径，实际直达强杀（how=%q）", how)
	}
	if how != "grace-exit" && how != "grace-timeout" {
		t.Fatalf("宽限路径的结局只能是 grace-exit / grace-timeout，拿到 %q", how)
	}
	if waited > 550*time.Millisecond { // 轮询粒度 5ms + 调度抖动，给 50ms 容差
		t.Fatalf("宽限等待不能明显超过 disposeGrace（500ms + 50ms 容差），实测 %v", waited)
	}

	forced, _ := newTestPTYTerminalAPI(t, cwd)
	forced.disposeGrace = 0
	if _, err := forced.TerminalCreate(context.Background(), "sess-a", transport.OfficialTerminalCreateRequest{
		ID: "g2", Cols: 80, Rows: 24,
	}); err != nil {
		t.Fatalf("create 失败：%v", err)
	}
	if err := forced.TerminalClose(context.Background(), "sess-a", "g2"); err != nil {
		t.Fatalf("close 失败：%v", err)
	}
	how, waited, gotPolite2 := forced.disposeOutcome("sess-a", "g2")
	if gotPolite2 || how != "forced" {
		t.Fatalf("disposeGrace=0 必须是历史行为（forced、不发礼），拿到 how=%q polite=%v", how, gotPolite2)
	}
	if waited != 0 {
		t.Fatalf("不强杀前不该等：waited=%v", waited)
	}
}

// 清理失败不能就这样泄漏：按 cleanupRetryMs 进重试队列，sweep 到期再试一次。
func TestTerminalCleanupRetriedAfterFailure(t *testing.T) {
	api, _ := newTestPTYTerminalAPI(t, t.TempDir())
	api.disposeGrace = 0
	api.cleanupRetry = 10 * time.Millisecond
	base := time.Now()
	api.now = func() time.Time { return base }
	fails := 0
	api.closeFn = func(string) error {
		if fails == 0 {
			fails++
			return errors.New("terminal busy")
		}
		return nil
	}
	if _, err := api.TerminalCreate(context.Background(), "sess-a", transport.OfficialTerminalCreateRequest{
		ID: "r1", Cols: 80, Rows: 24,
	}); err != nil {
		t.Fatalf("create 失败：%v", err)
	}
	if err := api.TerminalClose(context.Background(), "sess-a", "r1"); err != nil {
		t.Fatalf("close 第一次失败也要先记账再重试，不该直接上抛：%v", err)
	}
	if n, _ := api.cleanupStats("sess-a", "r1"); n != 1 {
		t.Fatalf("第一次失败应记 1 次尝试，拿到 %d", n)
	}
	// 未到重试时刻：不动。
	api.sweep(base.Add(5 * time.Millisecond))
	if n, _ := api.cleanupStats("sess-a", "r1"); n != 1 {
		t.Fatalf("还没到 cleanupRetry 就不该重试，拿到 %d 次", n)
	}
	// 到期：重试并成功。
	api.sweep(base.Add(20 * time.Millisecond))
	n, done := api.cleanupStats("sess-a", "r1")
	if n != 2 || !done {
		t.Fatalf("到期应重试并成功（attempts=2 done=true），拿到 attempts=%d done=%v", n, done)
	}
}

// activityPollIntervalMs：采样活跃度，把"很久没人碰"的终端标成闲置。
func TestTerminalActivityPollMarksIdle(t *testing.T) {
	api, _ := newTestPTYTerminalAPI(t, t.TempDir())
	api.disposeGrace = 0
	base := time.Now()
	api.now = func() time.Time { return base }
	api.activityPoll = 30 * time.Millisecond
	if _, err := api.TerminalCreate(context.Background(), "sess-a", transport.OfficialTerminalCreateRequest{
		ID: "a1", Cols: 80, Rows: 24,
	}); err != nil {
		t.Fatalf("create 失败：%v", err)
	}
	api.sweep(base.Add(10 * time.Millisecond))
	if api.isIdle("sess-a", "a1") {
		t.Fatal("最后一次活动在采样窗口内，不该判闲置")
	}
	api.sweep(base.Add(60 * time.Millisecond))
	if !api.isIdle("sess-a", "a1") {
		t.Fatal("超过 activityPoll 没有活动，必须判闲置")
	}
	// 有一次真实输入就恢复活跃。
	api.now = func() time.Time { return base.Add(65 * time.Millisecond) }
	if err := api.Input("sess-a", "a1", "\n"); err != nil {
		t.Fatalf("写终端失败：%v", err)
	}
	api.sweep(base.Add(65 * time.Millisecond))
	if api.isIdle("sess-a", "a1") {
		t.Fatal("刚有输入，必须重新算活跃")
	}
}

// unattendedTimeoutMs：闲置超过寿命上限的无人值守终端要被回收（否则进程级泄漏）。
func TestTerminalUnattendedReaped(t *testing.T) {
	api, _ := newTestPTYTerminalAPI(t, t.TempDir())
	api.disposeGrace = 0
	base := time.Now()
	api.now = func() time.Time { return base }
	api.activityPoll = 10 * time.Millisecond
	api.unattendedTTL = 50 * time.Millisecond
	if _, err := api.TerminalCreate(context.Background(), "sess-a", transport.OfficialTerminalCreateRequest{
		ID: "u1", Cols: 80, Rows: 24,
	}); err != nil {
		t.Fatalf("create 失败：%v", err)
	}
	api.sweep(base.Add(20 * time.Millisecond)) // 判闲置，开始计时
	if api.isReaped("sess-a", "u1") {
		t.Fatal("刚判闲置还不该回收")
	}
	api.sweep(base.Add(200 * time.Millisecond)) // 超过 50ms 寿命
	if !api.isReaped("sess-a", "u1") {
		t.Fatal("闲置超过 unattendedTimeoutMs 必须回收")
	}
	if _, err := api.TerminalAttach(context.Background(), "sess-a", "u1"); err == nil {
		t.Fatal("被回收的终端不该还能 attach 成功")
	}
}
