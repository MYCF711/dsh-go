package forge

// PARITY-W2-STATE 先红：forgeSession 绑定存储与 provider 拉取**尚不存在**。
// 本文件先写测试，实现体随后补；红 = 编译失败（undefined Store/Binding/...）。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	home := t.TempDir()
	s, err := Open(home)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s, home
}

// 1) track 真持久化：重开 store 仍在，字段逐项对得上。
func TestTrackPersistsAcrossReopen(t *testing.T) {
	s, home := openTestStore(t)
	b, err := s.Track("sess-1", "https://github.com/o/r/pull/7", "github")
	if err != nil {
		t.Fatalf("Track: %v", err)
	}
	if !b.Tracked || b.SessionID != "sess-1" || b.URL != "https://github.com/o/r/pull/7" {
		t.Fatalf("Track 返回不对：%+v", b)
	}

	reopened, err := Open(home)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := reopened.Get("sess-1", "https://github.com/o/r/pull/7")
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if !got.Tracked || got.Provider != "github" {
		t.Fatalf("重开后绑定丢失：%+v", got)
	}
}

// 2) track 幂等：同 session+url 再来一次不产生第二条，也不清掉 TrackedAt。
func TestTrackIsIdempotent(t *testing.T) {
	s, _ := openTestStore(t)
	first, err := s.Track("sess-1", "https://github.com/o/r/pull/7", "github")
	if err != nil {
		t.Fatalf("Track: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	second, err := s.Track("sess-1", "https://github.com/o/r/pull/7", "github")
	if err != nil {
		t.Fatalf("Track 2: %v", err)
	}
	if !second.TrackedAt.Equal(first.TrackedAt) {
		t.Fatalf("重复 track 不应改 TrackedAt：%v vs %v", first.TrackedAt, second.TrackedAt)
	}
	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("应只有 1 条绑定，得到 %d", len(list))
	}
}

// 3) dismiss 真持久化：Tracked=false 且保留记录（官方是"隐藏一条绑定"，不是删）。
func TestDismissKeepsRecordAndPersists(t *testing.T) {
	s, home := openTestStore(t)
	if _, err := s.Track("sess-1", "https://github.com/o/r/pull/7", "github"); err != nil {
		t.Fatalf("Track: %v", err)
	}
	b, err := s.Dismiss("sess-1", "https://github.com/o/r/pull/7")
	if err != nil {
		t.Fatalf("Dismiss: %v", err)
	}
	if b.Tracked || b.DismissedAt.IsZero() {
		t.Fatalf("dismiss 后应 Tracked=false 且有 DismissedAt：%+v", b)
	}

	reopened, err := Open(home)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := reopened.Get("sess-1", "https://github.com/o/r/pull/7")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Tracked || got.DismissedAt.IsZero() {
		t.Fatalf("dismiss 没落盘：%+v", got)
	}
}

// 4) dismiss 不存在的绑定 → 结构化 not-found（不是静默成功）。
func TestDismissUnknownIsNotFound(t *testing.T) {
	s, _ := openTestStore(t)
	_, err := s.Dismiss("sess-x", "https://github.com/o/r/pull/999")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("应为 ErrNotFound，得到 %v", err)
	}
}

// 5) 非法 URL 在本地就拒（不发出去）：只有 https/http 的 code host URL 才算。
func TestTrackRejectsInvalidURL(t *testing.T) {
	s, _ := openTestStore(t)
	for _, bad := range []string{"", "not-a-url", "ftp://x/y", "javascript:alert(1)"} {
		if _, err := s.Track("sess-1", bad, "github"); !errors.Is(err, ErrInvalidURL) {
			t.Fatalf("%q 应被拒为 ErrInvalidURL，得到 %v", bad, err)
		}
	}
}

// 6) refresh 真拉取：provider 收到真实 HTTP 请求，结果落进绑定状态。
func TestRefreshFetchesFromProvider(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if got := r.Header.Get("Accept"); got == "" {
			t.Errorf("refresh 必须带 Accept 头")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"state": "open", "headSha": "abc123", "title": "Fix the thing",
		})
	}))
	defer srv.Close()

	s, _ := openTestStore(t)
	s.SetProvider(&HTTPProvider{})
	if _, err := s.Track("sess-1", srv.URL+"/o/r/pull/7", "github"); err != nil {
		t.Fatalf("Track: %v", err)
	}
	got, err := s.Refresh(context.Background(), "sess-1", srv.URL+"/o/r/pull/7")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if hits != 1 {
		t.Fatalf("provider 应被请求 1 次，得到 %d", hits)
	}
	if got.State != "open" || got.HeadSHA != "abc123" || got.Title != "Fix the thing" {
		t.Fatalf("refresh 未采用真实结果：%+v", got)
	}
	if got.RefreshedAt.IsZero() {
		t.Fatal("refresh 必须记 RefreshedAt")
	}
}

// 7) refresh 没有 provider → 结构化 ErrNoProvider（不静默回旧值）。
func TestRefreshWithoutProviderIsStructured(t *testing.T) {
	s, _ := openTestStore(t)
	if _, err := s.Track("sess-1", "https://github.com/o/r/pull/7", "github"); err != nil {
		t.Fatalf("Track: %v", err)
	}
	if _, err := s.Refresh(context.Background(), "sess-1", "https://github.com/o/r/pull/7"); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("应为 ErrNoProvider，得到 %v", err)
	}
}

// 8) 并发 track 不同绑定不丢（一把锁管读-改-写-落盘）。
func TestConcurrentTrack(t *testing.T) {
	s, _ := openTestStore(t)
	const n = 12
	done := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			_, err := s.Track("sess-1", "https://example.test/o/r/pull/"+string(rune('a'+i)), "github")
			done <- err
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-done; err != nil {
			t.Fatalf("并发 track 失败：%v", err)
		}
	}
	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != n {
		t.Fatalf("应 %d 条绑定，得到 %d（并发丢写）", n, len(list))
	}
}

// 9) 存储路径固定在 <home>/state/forge-sessions.json（不另造账本）。
func TestStorePathIsUnderHomeState(t *testing.T) {
	home := t.TempDir()
	s, err := Open(home)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	want := filepath.Join(home, "state", "forge-sessions.json")
	if s.Path() != want {
		t.Fatalf("Path()=%q，应为 %q", s.Path(), want)
	}
}
