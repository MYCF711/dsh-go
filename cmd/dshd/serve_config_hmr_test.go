package main

// 官方 #4 `hmr` 的 profile 配置热重载半边：**验收测试**。
//
// 判据不是"有个 ticker 在跑"，而是：
//   - 磁盘上的配置文档被外部改动后，`Store.Tree()` 真的变成了新内容（不需要重启）；
//   - 没变的时候不重复广播（revision 比较）；
//   - 坏文件不改内存、错误可见（不是静默保留旧树也不崩）；
//   - 宿主的热重载开关（`profiles/*/cordis.patch.yml`）在**构造期**就被拒绝；
//   - 默认开、可用 `DSHD_PROFILE_HMR=0` 关（官方默认开，本仓可关）。

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"dshgo/internal/config"
)

// writeConfigFile 用 config.Store 自己的 Apply 写一份合法文档（含 revision）。
func writeConfigFile(t *testing.T, store *config.Store, value string) {
	t.Helper()
	_, err := store.Apply(config.Patch{
		ExpectedRevision: store.Revision(),
		Author:           "test",
		Ops:              []config.PatchOp{{Op: config.OpSet, Path: "ui.theme", Value: value}},
	})
	if err != nil {
		t.Fatalf("写配置树失败：%v", err)
	}
}

// 外部改动 → Reload 之后 Store.Tree() 真的变了；消费者被通知一次。
func TestConfigHMRReloadsFromDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config-tree.json")
	store, err := config.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	writeConfigFile(t, store, "dark")

	hmr, err := NewConfigHMR(store, ConfigHMRConfig{Enabled: true, Path: path, Interval: time.Hour})
	if err != nil {
		t.Fatalf("装配失败：%v", err)
	}
	if !hmr.Enabled() {
		t.Fatal("Enabled=true 构造出来的引擎必须 Enabled()")
	}
	var calls int32
	var lastTo int64
	hmr.OnChange(func(c ConfigHMRChange) {
		atomic.AddInt32(&calls, 1)
		atomic.StoreInt64(&lastTo, c.To)
	})

	// 第一次 tick：磁盘没变过（就是自己刚写的），不该有变更。
	if _, changed, err := hmr.Tick(context.Background()); err != nil || changed {
		t.Fatalf("没有外部改动时不该报变更：changed=%v err=%v", changed, err)
	}
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Fatalf("没有变更却通知了 %d 次", n)
	}

	// 模拟"另一个进程改了配置树"：用**另一个** Store 实例写同一份文件。
	other, err := config.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	writeConfigFile(t, other, "light")

	change, changed, err := hmr.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick 失败：%v", err)
	}
	if !changed {
		t.Fatal("磁盘上的 revision 变了，Tick 必须报变更")
	}
	if change.From != 1 || change.To != 2 {
		t.Fatalf("revision 迁移应当是 1→2，实际 %+v", change)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("消费者应当被通知 1 次，实际 %d", n)
	}
	if got := atomic.LoadInt64(&lastTo); got != 2 {
		t.Fatalf("消费者拿到的 To 应当是 2，实际 %d", got)
	}

	// 关键判据：进程内看到的树**真的是新内容**（不是只发了个通知）。
	tree := store.Tree()
	if got := tree.Flatten()["ui.theme"]; got != "light" {
		t.Fatalf("Reload 之后 ui.theme 应当是 light，实际 %q（说明热重载没接上）", got)
	}
	if store.Revision() != 2 {
		t.Fatalf("Store 的 revision 应当变成 2，实际 %d", store.Revision())
	}

	// 再 tick 一次：没新变化 → 不重复通知。
	if _, changed, _ := hmr.Tick(context.Background()); changed {
		t.Fatal("没有新改动时不该再报变更（会导致消费者重复重建）")
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("重复 Tick 不该重复通知，实际 %d 次", n)
	}
}

// 坏文件：内存里的树保持原样，错误可见，不 panic。
func TestConfigHMRBadFileKeepsOldTree(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config-tree.json")
	store, err := config.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	writeConfigFile(t, store, "dark")
	before := store.Revision()

	hmr, err := NewConfigHMR(store, ConfigHMRConfig{Enabled: true, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{ 这不是 JSON"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := hmr.Tick(context.Background()); err == nil {
		t.Fatal("坏文件必须报错（不允许静默保留旧树还不说话）")
	}
	if store.Revision() != before {
		t.Fatalf("坏文件不该改内存里的 revision：%d → %d", before, store.Revision())
	}
	if got := store.Tree().Flatten()["ui.theme"]; got != "dark" {
		t.Fatalf("坏文件不该改内存里的树：ui.theme=%q", got)
	}
	if status := hmr.Status(); status.LastError == "" {
		t.Fatal("Status().LastError 应当记录这次失败")
	}
}

// 默认开：官方口径是 "Profile configuration reloads by default"。
func TestConfigHMRDefaultEnabled(t *testing.T) {
	t.Setenv("DSHD_PROFILE_HMR", "")
	cfg := configHMRConfigFromEnv(filepath.Join(t.TempDir(), "config-tree.json"))
	if !cfg.Enabled {
		t.Fatal("profile 配置热重载默认必须开（官方 dsh-base 的默认）")
	}
	// 没有配置文件（这个进程没有配置树）→ 关。
	if got := configHMRConfigFromEnv(""); got.Enabled {
		t.Fatal("没有配置树文件时必须关（没有可重载的对象）")
	}
	for _, off := range []string{"0", "false", "off", "NO"} {
		t.Setenv("DSHD_PROFILE_HMR", off)
		if got := configHMRConfigFromEnv(filepath.Join(t.TempDir(), "c.json")); got.Enabled {
			t.Fatalf("DSHD_PROFILE_HMR=%q 应当关掉", off)
		}
	}
	t.Setenv("DSHD_PROFILE_HMR", "1")
	if got := configHMRConfigFromEnv(filepath.Join(t.TempDir(), "c.json")); !got.Enabled {
		t.Fatal("DSHD_PROFILE_HMR=1 应当开启")
	}
}

// 铁律 3：`profiles/*/cordis.patch.yml` 是宿主的热重载开关，构造期就拒绝。
func TestConfigHMRRefusesHostPatchFile(t *testing.T) {
	dir := t.TempDir()
	for _, path := range []string{
		filepath.Join(dir, "profiles", "standard", "cordis.patch.yml"),
		filepath.Join(dir, "cordis.patch.yml"),
	} {
		store, err := config.OpenStore(filepath.Join(dir, "config-tree.json"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := NewConfigHMR(store, ConfigHMRConfig{Enabled: true, Path: path}); err == nil {
			t.Fatalf("%s 是宿主的热重载开关，必须拒绝", path)
		}
	}
}

// 关闭态是纯 no-op：不扫描、不改 store、不算错误。
func TestConfigHMRDisabledIsNoop(t *testing.T) {
	hmr, err := NewConfigHMR(nil, ConfigHMRConfig{})
	if err != nil {
		t.Fatalf("关闭态不需要 store：%v", err)
	}
	if hmr.Enabled() {
		t.Fatal("零值必须是关闭")
	}
	if _, changed, err := hmr.Tick(context.Background()); changed || err != nil {
		t.Fatalf("关闭态 Tick 必须 no-op：changed=%v err=%v", changed, err)
	}
	if err := hmr.Run(context.Background()); err != ErrConfigHMROff {
		t.Fatalf("关闭态 Run 应当回 ErrConfigHMROff，实际 %v", err)
	}
}

// 开启态缺依赖要 fail-loud（"开着但什么都不会发生"是假象）。
func TestConfigHMRRequiresStoreAndPath(t *testing.T) {
	if _, err := NewConfigHMR(nil, ConfigHMRConfig{Enabled: true, Path: "x.json"}); err == nil {
		t.Fatal("Enabled=true 但没有 store 必须报错")
	}
	store, err := config.OpenStore(filepath.Join(t.TempDir(), "config-tree.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewConfigHMR(store, ConfigHMRConfig{Enabled: true}); err == nil {
		t.Fatal("Enabled=true 但没有 Path 必须报错")
	}
}

// 一个消费者 panic 不该让其它消费者收不到变更，也不该让宿主崩。
func TestConfigHMRPanickingConsumerIsolated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config-tree.json")
	store, err := config.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	writeConfigFile(t, store, "a")
	hmr, err := NewConfigHMR(store, ConfigHMRConfig{Enabled: true, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	var ok int32
	hmr.OnChange(func(ConfigHMRChange) { panic("boom") })
	hmr.OnChange(func(ConfigHMRChange) { atomic.AddInt32(&ok, 1) })

	other, err := config.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	writeConfigFile(t, other, "b")
	change, changed, err := hmr.Tick(context.Background())
	if err != nil || !changed {
		t.Fatalf("Tick 失败：changed=%v err=%v", changed, err)
	}
	if atomic.LoadInt32(&ok) != 1 {
		t.Fatal("后面那个消费者没收到变更（panic 的消费者把它挡住了）")
	}
	if len(change.Errs) == 0 {
		t.Fatal("消费者的 panic 必须可见（不能静默吞掉）")
	}
}
