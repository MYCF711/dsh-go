package main

// PARITY-W2-STATE 宿主侧探针：绑定必须真落盘（重开宿主仍在）、refresh 真走
// provider（httptest 真 HTTP）、follow 的版本号必须随写操作推进。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"dshgo/internal/forge"
)

func newForgeHostForTest(t *testing.T, home string) *forgePanelHost {
	t.Helper()
	h, err := newForgePanelHost(home)
	if err != nil {
		t.Fatalf("newForgePanelHost: %v", err)
	}
	if h == nil {
		t.Fatal("home 非空时不应返回 nil")
	}
	return h
}

func TestForgePanelHostPersistsAcrossReopen(t *testing.T) {
	home := t.TempDir()
	h := newForgeHostForTest(t, home)

	b, err := h.track("sess-1", "https://github.com/o/r/pull/7")
	if err != nil {
		t.Fatalf("track: %v", err)
	}
	if !b.Tracked || b.SessionID != "sess-1" {
		t.Fatalf("track 返回不对：%+v", b)
	}
	if b.TrackedAt == "" {
		t.Fatal("track 必须给 TrackedAt")
	}

	// 模拟重启：同一 home 重新构造宿主。
	h2 := newForgeHostForTest(t, home)
	snap, err := h2.snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snap.Bindings) != 1 {
		t.Fatalf("重启后应有 1 条绑定，得到 %d", len(snap.Bindings))
	}
	if snap.Bindings[0].URL != "https://github.com/o/r/pull/7" || !snap.Bindings[0].Tracked {
		t.Fatalf("重启后绑定不对：%+v", snap.Bindings[0])
	}

	// dismiss 也必须落盘。
	if _, err := h2.dismiss("sess-1", "https://github.com/o/r/pull/7"); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	h3 := newForgeHostForTest(t, home)
	snap3, err := h3.snapshot()
	if err != nil {
		t.Fatalf("snapshot 3: %v", err)
	}
	if snap3.Bindings[0].Tracked || snap3.Bindings[0].DismissedAt == "" {
		t.Fatalf("dismiss 没落盘：%+v", snap3.Bindings[0])
	}
}

func TestForgePanelHostRefreshUsesRealProvider(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"state":"open","headSha":"deadbeef","title":"T"}`))
	}))
	defer srv.Close()

	home := t.TempDir()
	h := newForgeHostForTest(t, home)
	// 生产路径用环境变量注入 provider；这里直接验证 store 注入面（同一 store）。
	if _, err := h.track("sess-1", srv.URL+"/o/r/pull/7"); err != nil {
		t.Fatalf("track: %v", err)
	}
	// 注入真实 HTTP provider（打 httptest 服务，不是 stub 返回值）。
	h.store.SetProvider(&forge.HTTPProvider{Client: &http.Client{Timeout: 10 * time.Second}})

	bindings, err := h.refresh(context.Background(), "sess-1", srv.URL+"/o/r/pull/7")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if hits != 1 {
		t.Fatalf("provider 应被请求 1 次，得到 %d", hits)
	}
	if len(bindings) != 1 || bindings[0].State != "open" || bindings[0].HeadSHA != "deadbeef" {
		t.Fatalf("refresh 未采用真实结果：%+v", bindings)
	}
	if bindings[0].RefreshedAt == "" {
		t.Fatal("refresh 必须记 RefreshedAt")
	}
}

func TestForgePanelHostRefreshWithoutProviderIsStructured(t *testing.T) {
	home := t.TempDir()
	h := newForgeHostForTest(t, home)
	if _, err := h.track("sess-1", "https://github.com/o/r/pull/7"); err != nil {
		t.Fatalf("track: %v", err)
	}
	_, err := h.refresh(context.Background(), "sess-1", "https://github.com/o/r/pull/7")
	if err == nil {
		t.Fatal("没有 provider 时 refresh 必须失败（不能静默回旧值）")
	}
}

func TestForgePanelHostVersionAdvancesOnWrite(t *testing.T) {
	home := t.TempDir()
	h := newForgeHostForTest(t, home)
	before, err := h.snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if _, err := h.track("sess-1", "https://github.com/o/r/pull/7"); err != nil {
		t.Fatalf("track: %v", err)
	}
	after, err := h.snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if after.Version == before.Version {
		t.Fatalf("写操作后版本号必须变化：%d", after.Version)
	}
}

func TestForgePanelHostWaitWakesOnBump(t *testing.T) {
	home := t.TempDir()
	h := newForgeHostForTest(t, home)
	snap, err := h.snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- h.wait(context.Background(), snap.Version) }()

	time.Sleep(50 * time.Millisecond)
	if _, err := h.track("sess-1", "https://github.com/o/r/pull/7"); err != nil {
		t.Fatalf("track: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("wait 返回错误：%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("写操作没有唤醒 wait（follow 会卡住）")
	}
}
