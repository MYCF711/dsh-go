package main

// T11：自动标题后台 goroutine 的 quiescence 与取消边界。
//
// I04 的 TempDir 竞态不是"负载 flake"：主 turn 非阻塞返回后，标题 goroutine
// 仍可能写审计/标题/索引，而测试立刻读事件并让 TempDir 清理。这里钉住产品侧
// 的两条契约：
//   1. stop 之后不再启动新调用；已取消的 Run 连审计都不写；
//   2. 取消 in-flight 调用后 stop 必须等到 goroutine 真正退出（可确定等待，
//      不靠 sleep），且不写 title/index。

import (
	"context"
	"os"
	"testing"
	"time"
)

// waitGatewayCalls 等到假网关至少收到 n 次请求（用可观测事实代替 sleep 猜）。
func waitGatewayCalls(t *testing.T, gateway *autoTitleGateway, n int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		if gateway.count() >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待标题调用到达网关超时：count=%d，want >=%d", gateway.count(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestAutoTitleCanceledRunWritesNoAudit：ctx 在 Run 之前就取消时，连
// `session/title-llm-request` 审计帧都不能落（取消后的写边界从第一笔开始）。
func TestAutoTitleCanceledRunWritesNoAudit(t *testing.T) {
	home := t.TempDir()
	const sessionID = "session-auto-title-canceled"
	path := autoTitleSeed(t, home, sessionID, "please fix the login redirect")
	gateway := newAutoTitleGateway(t, nil)
	service, base := autoTitleDirect(home, gateway.base(), time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Run(ctx, sessionID, "auto-title-model", base, ""); err == nil {
		t.Fatal("已取消的 Run 必须返回错误，不能假装成功")
	}
	if got := autoTitleEventTypes(t, path); autoTitleContains(got, "session/title-llm-request") {
		t.Fatalf("取消后仍写了审计帧：%v", got)
	}
	if got := autoTitleTitleEvents(t, path); len(got) != 0 {
		t.Fatalf("取消后仍写了 session/title：%+v", got)
	}
	if _, statErr := os.Stat(autoTitleIndexPath(path)); !os.IsNotExist(statErr) {
		t.Fatalf("取消后仍写了索引：%v", statErr)
	}
	if got := gateway.count(); got != 0 {
		t.Fatalf("取消的 Run 仍打了网关：%d 次", got)
	}
}

// TestAutoTitleKickAfterStopWritesNothing：stop 是闸门——之后的 Kick 不得
// 再起 goroutine、不得写任何东西（不靠 timeout 自然结束）。
func TestAutoTitleKickAfterStopWritesNothing(t *testing.T) {
	home := t.TempDir()
	const sessionID = "session-auto-title-after-stop"
	path := autoTitleSeed(t, home, sessionID, "please fix the login redirect")
	gateway := newAutoTitleGateway(t, nil)
	service, base := autoTitleDirect(home, gateway.base(), time.Second)

	service.stop()
	service.stop() // 幂等：重复 stop 不得 panic/死等
	service.Kick(sessionID, "auto-title-model", base, "")
	service.wait()

	if got := gateway.count(); got != 0 {
		t.Fatalf("stop 之后 Kick 仍打了网关：%d 次", got)
	}
	if got := autoTitleEventTypes(t, path); autoTitleContains(got, "session/title-llm-request") {
		t.Fatalf("stop 之后 Kick 仍写了审计：%v", got)
	}
	if got := autoTitleTitleEvents(t, path); len(got) != 0 {
		t.Fatalf("stop 之后 Kick 仍写了标题：%+v", got)
	}
	if _, statErr := os.Stat(autoTitleIndexPath(path)); !os.IsNotExist(statErr) {
		t.Fatalf("stop 之后 Kick 仍写了索引：%v", statErr)
	}
}

// TestAutoTitleStopCancelsInFlightAndWaits：取消一个挂在网关上的标题调用，
// stop 必须在有界时间内等到 goroutine 退出，且不写 title/index。
func TestAutoTitleStopCancelsInFlightAndWaits(t *testing.T) {
	home := t.TempDir()
	const sessionID = "session-auto-title-inflight-stop"
	path := autoTitleSeed(t, home, sessionID, "please fix the login redirect")
	gateway := newAutoTitleGateway(t, func(autoTitleGatewayRequest) (string, int, time.Duration) {
		return "", 200, time.Hour // 只由取消来结束
	})
	// 服务超时故意放大：这里证明的是 stop 的取消，不是 timeout 自然到期。
	service, base := autoTitleDirect(home, gateway.base(), 10*time.Second)

	service.Kick(sessionID, "auto-title-model", base, "")
	waitGatewayCalls(t, gateway, 1, 5*time.Second)

	started := time.Now()
	service.stop()
	elapsed := time.Since(started)
	if elapsed > 2*time.Second {
		t.Fatalf("stop 没有等到 in-flight 标题 goroutine 退出：耗时 %s", elapsed)
	}
	if got := autoTitleTitleEvents(t, path); len(got) != 0 {
		t.Fatalf("取消后仍写了 session/title：%+v", got)
	}
	if _, statErr := os.Stat(autoTitleIndexPath(path)); !os.IsNotExist(statErr) {
		t.Fatalf("取消后仍写了索引：%v", statErr)
	}

	// stop 之后不再有第二次调用。
	before := gateway.count()
	service.Kick(sessionID, "auto-title-model", base, "")
	service.wait()
	if got := gateway.count(); got != before {
		t.Fatalf("stop 之后又打了网关：%d → %d", before, got)
	}
}
