package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// 官方 `skill.collectCacheMaxEntries`（默认 128）：收集结果要缓存，别每次列/搜都
// 把文件系统走一遍。这里用 TTL 作为缓存有效期，**上限**是缓存条目数上限。
//
// 先红：Tool 没有缓存层（包注释写的是"不缓存"）。
func TestCollectCacheServesWithinTTL(t *testing.T) {
	t.Setenv("DSHD_SKILLS_CACHE_TTL_MS", "60000")
	dir := t.TempDir()
	writeSkill(t, dir, "alpha", "# alpha\n")
	tool := &Tool{dir: dir}
	list := func() string {
		res, err := tool.Exec(context.Background(), json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("列清单失败：%v", err)
		}
		return res.Text
	}
	if got := list(); !strings.Contains(got, "alpha") {
		t.Fatalf("首轮应看到 alpha：%s", got)
	}
	writeSkill(t, dir, "beta", "# beta\n")
	if got := list(); strings.Contains(got, "beta") {
		t.Fatalf("TTL 内应命中缓存（不该看到刚加的 beta）：%s", got)
	}
}

// 官方 `skillFs.watchUsePolling`：开着轮询监听时，磁盘一变缓存就要失效，
// 不能等 TTL 到期（会话里改完 skill 立刻就该可见）。
func TestPollingWatcherInvalidatesCache(t *testing.T) {
	t.Setenv("DSHD_SKILLS_CACHE_TTL_MS", "600000")
	t.Setenv("DSHD_SKILLS_WATCH_POLL_MS", "5")
	dir := t.TempDir()
	writeSkill(t, dir, "alpha", "# alpha\n")
	tool := &Tool{dir: dir}
	if res, _ := tool.Exec(context.Background(), json.RawMessage(`{}`)); !strings.Contains(res.Text, "alpha") {
		t.Fatalf("首轮应看到 alpha：%s", res.Text)
	}
	writeSkill(t, dir, "beta", "# beta\n")
	deadline := time.Now().Add(3 * time.Second)
	for {
		res, err := tool.Exec(context.Background(), json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(res.Text, "beta") {
			return // 轮询监听把缓存刷掉了
		}
		if time.Now().After(deadline) {
			t.Fatalf("开了轮询监听却一直看不到新 skill（TTL 是 600s，只能是监听没生效）：%s", res.Text)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// 缓存条目数不许无界：超过 `collectCacheMaxEntries` 就不缓存（行为不变，只是没有性能收益）。
func TestCollectCacheRespectsMaxEntries(t *testing.T) {
	t.Setenv("DSHD_SKILLS_CACHE_TTL_MS", "60000")
	t.Setenv("DSHD_SKILLS_COLLECT_CACHE_MAX", "2")
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		writeSkill(t, dir, fmt.Sprintf("s%d", i), "# s\n")
	}
	tool := &Tool{dir: dir}
	if _, err := tool.Exec(context.Background(), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if n, limit := tool.cachedSkillCount(), 2; n > limit {
		t.Fatalf("缓存条目 %d 超过上限 %d", n, limit)
	}
	// 超限时只是不缓存：结果仍然是完整的 5 条。
	writeSkill(t, dir, "late", "# late\n")
	res, err := tool.Exec(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "late") {
		t.Fatalf("超限不缓存时每次都要重新扫盘，理应看到 late：%s", res.Text)
	}
}
