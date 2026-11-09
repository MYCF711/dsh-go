package llm

// 模型列表 TTL 缓存（官方 `llm.deepseek.modelsMaxAgeMs`，默认 600000ms）。
//
// 宿主旧实现每次 `DiscoverModels` 都发真实 `GET /models`：界面每刷一次、每开一个
// 选择器就打一次上游，放大延迟与限流风险（`docs/parameter-register.md` #16）。
//
// 这里用真实 httptest 端点 + 请求计数钉住"第二次访问不发网络"，同时守住三件
// 不能顺手做坏的事：TTL=0 表示不缓存、不同凭据不能共享同一份列表、失败不缓存。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// countingModelsServer 是一个按请求计数、固定回一份模型列表的假端点。
type countingModelsServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests int
	fail     bool
}

func newCountingModelsServer(t *testing.T) *countingModelsServer {
	t.Helper()
	fake := &countingModelsServer{}
	fake.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		fake.requests++
		fail := fake.fail
		fake.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error":{"message":"upstream down"}}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "model-a", "context_length": 1_000_000},
				{"id": "model-b"},
			},
		})
	}))
	t.Cleanup(fake.Close)
	return fake
}

func (s *countingModelsServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

// TestDiscoverModelsCachesWithinTTL 先红：两次调用 = 一次网络请求。
func TestDiscoverModelsCachesWithinTTL(t *testing.T) {
	t.Setenv("DSHD_LLM_MODELS_MAX_AGE_MS", "60000")
	fake := newCountingModelsServer(t)
	client := New(fake.URL+"/v1", "k")

	first, err := client.DiscoverModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.DiscoverModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := fake.count(); got != 1 {
		t.Fatalf("两次 discoverModels 发了 %d 次真实请求：旧实现完全没有缓存"+
			"（官方 modelsMaxAgeMs 默认 600000ms）", got)
	}
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("两次结果 = %d/%d 条，期望都是 2 条", len(first), len(second))
	}
	if second[0].ID != "model-a" || second[0].ContextWindow != 1_000_000 {
		t.Fatalf("缓存结果漂移: %+v", second[0])
	}
}

// TestDiscoverModelsRefetchesAfterTTL：TTL 到期必须重新问端点（不能变成永久缓存）。
func TestDiscoverModelsRefetchesAfterTTL(t *testing.T) {
	t.Setenv("DSHD_LLM_MODELS_MAX_AGE_MS", "1")
	fake := newCountingModelsServer(t)
	client := New(fake.URL+"/v1", "k")

	if _, err := client.DiscoverModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(15 * time.Millisecond)
	if _, err := client.DiscoverModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := fake.count(); got != 2 {
		t.Fatalf("TTL 1ms 到期后仍然只发了 %d 次请求：缓存没有过期", got)
	}
}

// TestDiscoverModelsTTLZeroDisablesCache：官方 modelsMaxAgeMs=0 是"只在访问时刷新"，
// 即不缓存。这个档位必须能被表达出来。
func TestDiscoverModelsTTLZeroDisablesCache(t *testing.T) {
	t.Setenv("DSHD_LLM_MODELS_MAX_AGE_MS", "0")
	fake := newCountingModelsServer(t)
	client := New(fake.URL+"/v1", "k")

	for i := 0; i < 3; i++ {
		if _, err := client.DiscoverModels(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got := fake.count(); got != 3 {
		t.Fatalf("modelsMaxAgeMs=0 时发了 %d 次请求，期望 3 次（不缓存）", got)
	}
}

// TestDiscoverModelsCacheIsPerCredential：凭据不同的两条 client 不能共享一份列表
// （官方：每个账号 token 有自己的 listing）。
func TestDiscoverModelsCacheIsPerCredential(t *testing.T) {
	t.Setenv("DSHD_LLM_MODELS_MAX_AGE_MS", "60000")
	fake := newCountingModelsServer(t)

	if _, err := New(fake.URL+"/v1", "key-a").DiscoverModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := New(fake.URL+"/v1", "key-b").DiscoverModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := fake.count(); got != 2 {
		t.Fatalf("不同凭据共享了缓存：共 %d 次请求，期望 2 次", got)
	}
	// 同一凭据再来一次仍然命中缓存。
	if _, err := New(fake.URL+"/v1", "key-a").DiscoverModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := fake.count(); got != 2 {
		t.Fatalf("同一凭据第三次访问又打了一次上游：共 %d 次请求，期望 2 次", got)
	}
}

// TestDiscoverModelsFailureIsNotCached：一次失败不能被缓存成 10 分钟的"失败结论"。
func TestDiscoverModelsFailureIsNotCached(t *testing.T) {
	t.Setenv("DSHD_LLM_MODELS_MAX_AGE_MS", "60000")
	fake := newCountingModelsServer(t)
	client := New(fake.URL+"/v1", "k")

	fake.mu.Lock()
	fake.fail = true
	fake.mu.Unlock()
	if _, err := client.DiscoverModels(context.Background()); err == nil {
		t.Fatal("上游 500 被吞掉了")
	}

	fake.mu.Lock()
	fake.fail = false
	fake.mu.Unlock()
	if _, err := client.DiscoverModels(context.Background()); err != nil {
		t.Fatalf("失败被缓存：端点恢复后第一次访问仍然报错: %v", err)
	}
	if got := fake.count(); got != 2 {
		t.Fatalf("共 %d 次请求，期望 2 次（失败不缓存）", got)
	}
}

// TestModelsMaxAgeDefaultIsOfficial600s：默认 TTL 必须是官方 600000ms。
func TestModelsMaxAgeDefaultIsOfficial600s(t *testing.T) {
	age, err := modelsMaxAgeFromEnv(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if age != 10*time.Minute {
		t.Fatalf("默认 modelsMaxAgeMs = %v，期望 600000ms", age)
	}
	for _, bad := range []string{"-1", "abc", "1.5"} {
		if _, err := modelsMaxAgeFromEnv(func(string) string { return bad }); err == nil {
			t.Fatalf("非法 %s=%q 被静默忽略：必须 fail-loud", modelsMaxAgeEnv, bad)
		}
	}
	if zero, err := modelsMaxAgeFromEnv(func(string) string { return "0" }); err != nil || zero != 0 {
		t.Fatalf("modelsMaxAgeMs=0 必须解析成「不缓存」，得到 %v/%v", zero, err)
	}
}

// TestDiscoverModelsEmptyListingKeepsPrevious：官方 "A failed or empty listing
// retains the previous listing"。上游抖一下回 `{"data":[]}` 时，选择器不能瞬间变空。
func TestDiscoverModelsEmptyListingKeepsPrevious(t *testing.T) {
	t.Setenv(modelsMaxAgeEnv, "60000")
	resetDiscoverModelsCache()

	var mu sync.Mutex
	empty := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		serveEmpty := empty
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if serveEmpty {
			fmt.Fprint(w, `{"data":[]}`)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"model-a"}]}`)
	}))
	defer srv.Close()

	client := New(srv.URL+"/v1", "k")
	if _, err := client.DiscoverModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	empty = true
	mu.Unlock()
	// 让缓存过期，逼它真的再问一次上游（这一次拿到的是空列表）。
	time.Sleep(2 * time.Millisecond)
	t.Setenv(modelsMaxAgeEnv, "1")
	time.Sleep(2 * time.Millisecond)
	models, err := client.DiscoverModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "model-a" {
		t.Fatalf("空列表把上一份列表冲掉了：%+v（官方语义是保留上一份）", models)
	}
}
