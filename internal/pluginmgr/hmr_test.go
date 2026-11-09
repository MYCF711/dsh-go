package pluginmgr

// 先红（HS-14）：基线里 grep `fsnotify|hot reload|hmr` = 0，`modules/tool/plugin`
// 自述"无热重载"——插件改完必须重启宿主。本文件补的是**安全**的那一小块：
//
//  1. 默认关闭：零值 `HMRConfig` 就是关的。热重载一旦默认开着，改一个文件就能
//     让运行中的插件集合变化，而这台机器上"整树热重载"曾经把 idle 子代理的
//     inbox 投影冲掉、直接把宿主 exit(1)（见 $DSH_HOME/AGENTS.md 铁律 3）。
//  2. 绝不写 `home/profiles/*/cordis.patch.yml`：那是**宿主的**热重载开关，
//     不是插件的。HMR 只读插件目录、只调用 Loader.Reload，没有写文件的路径；
//     任何指向那个文件的通知一律拒绝（ErrHMRForbiddenTarget）。
//  3. 只在临时 fixture 里演示：测试全部跑在 t.TempDir() 下，不碰真实用户目录。
//
// 检测走"目录 digest 对比"（manifest 的大小+mtime），不引入 fsnotify：
// 本仓不新增依赖，而宿主自己的插件根是有限的小目录。变更进**有界**队列并合并，
// 一串连续改动只触发一次重装。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 红 C 的绿侧：零值就是关闭。
func TestHMRDisabledByDefault(t *testing.T) {
	manager, loader, _ := newRuntime(t)
	pkg := writeWasmPackage(t, filepath.Join(t.TempDir(), "echo-pkg"), "acme.echo", "1.0.0", echoToolDecls())
	if _, err := manager.Install(pkg); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Enable("acme.echo"); err != nil {
		t.Fatal(err)
	}
	before := loader.Loaded()[0].Gen

	h, err := NewHMR(loader, HMRConfig{})
	if err != nil {
		t.Fatalf("零值配置不该报错（它就是\"关闭\"）：%v", err)
	}
	if h.Enabled() {
		t.Fatal("零值 HMRConfig 必须是关闭的")
	}
	if got := h.Status(); got.Enabled {
		t.Fatalf("Status().Enabled = %v，want false", got.Enabled)
	}

	result, err := h.Tick(context.Background())
	if err != nil {
		t.Fatalf("关闭状态 Tick 不该报错: %v", err)
	}
	if !result.Skipped || result.Reloads != 0 {
		t.Fatalf("关闭状态 Tick = %+v，want skipped 且 0 次重装", result)
	}
	if after := loader.Loaded()[0].Gen; after != before {
		t.Fatalf("关闭的 HMR 重装了插件：gen %d → %d", before, after)
	}

	// 开启必须显式，而且不能没有装载器（否则"开着但什么都不会发生"是假象）。
	if _, err := NewHMR(nil, HMRConfig{Enabled: true}); err == nil {
		t.Fatal("Enabled=true 但没有 loader 必须报错")
	}
	if _, err := NewHMR(loader, HMRConfig{Enabled: true, MaxPending: -1}); err == nil {
		t.Fatal("负数 MaxPending 必须报错")
	}
}

// 红 D 的绿侧：profiles/cordis.patch.yml 是宿主的开关，HMR 一律不许碰。
func TestHMRForbiddenTargetRejectsProfilePatch(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "profiles", "default", "cordis.patch.yml")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte("plugins: []\n")
	if err := os.WriteFile(target, original, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		target,
		filepath.Join(home, "profiles", "other", "cordis.patch.yml"),
		filepath.Join("C:\\Users\\x\\", ".dsh", "profiles", "default", "cordis.patch.yml"),
	} {
		if !ForbiddenHMRTarget(path) {
			t.Fatalf("%q 必须被判为禁止目标", path)
		}
	}
	for _, path := range []string{
		filepath.Join(home, "packages", "acme.echo", "1.0.0", "plugin.json"),
		filepath.Join(home, "profiles", "default", "notes.md"),
	} {
		if ForbiddenHMRTarget(path) {
			t.Fatalf("%q 不该被判为禁止目标", path)
		}
	}

	_, loader, _ := newRuntime(t)
	h, err := NewHMR(loader, HMRConfig{Enabled: true, Root: filepath.Join(home, "packages")})
	if err != nil {
		t.Fatal(err)
	}
	err = h.Observe(target)
	if !errors.Is(err, ErrHMRForbiddenTarget) {
		t.Fatalf("Observe(profiles/.../cordis.patch.yml) 错误 = %v，want ErrHMRForbiddenTarget", err)
	}
	// 拒绝之后那个文件必须一个字节都没变，也没有旁路写出来的临时文件。
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("热重载开关被删了: %v", err)
	}
	if string(got) != string(original) {
		t.Fatalf("cordis.patch.yml 被改了：%q", got)
	}
	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("profiles 目录里多了文件：%v", entries)
	}

	// root 指向 profiles 也一样拒绝：那是把宿主开关当插件目录用。
	if _, err := NewHMR(loader, HMRConfig{Enabled: true, Root: filepath.Join(home, "profiles")}); !errors.Is(err, ErrHMRForbiddenTarget) {
		t.Fatalf("Root 指向 profiles 时错误 = %v，want ErrHMRForbiddenTarget", err)
	}
}

// 真 fixture：改插件 manifest → 扫到变化 → 真的重装（gen 前进、工具还在）。
func TestHMRScanDetectsManifestChangeAndReloads(t *testing.T) {
	manager, loader, table := newRuntime(t)
	pkg := writeWasmPackage(t, filepath.Join(t.TempDir(), "echo-pkg"), "acme.echo", "1.0.0", echoToolDecls())
	if _, err := manager.Install(pkg); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Enable("acme.echo"); err != nil {
		t.Fatal(err)
	}
	genBefore := loader.Loaded()[0].Gen

	h, err := NewHMR(loader, HMRConfig{Enabled: true, Root: manager.root, MaxPending: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Stop()

	// 第一次扫描只建立基线：目录本来就那样，不该触发重装。
	first, err := h.Tick(context.Background())
	if err != nil {
		t.Fatalf("首次扫描失败: %v", err)
	}
	if first.Scanned == 0 {
		t.Fatal("首次扫描一个 manifest 都没看到（扫错目录了）")
	}
	if first.Reloads != 0 {
		t.Fatalf("首次扫描不该重装：%+v", first)
	}

	// 改 manifest 的 mtime（内容不动，避免把包改坏）：相当于"插件文件被重新写了一遍"。
	manifest := filepath.Join(manager.root, "packages", "acme.echo", "1.0.0", "plugin.json")
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(manifest, future, future); err != nil {
		t.Fatal(err)
	}

	second, err := h.Tick(context.Background())
	if err != nil {
		t.Fatalf("第二次扫描失败: %v", err)
	}
	if second.Reloads != 1 {
		t.Fatalf("第二次扫描 reloads = %d，want 1（%+v）", second.Reloads, second)
	}
	if joined := strings.Join(second.Changed, ","); joined != "acme.echo" {
		t.Fatalf("changed = %q，want acme.echo", joined)
	}
	loaded := loader.Loaded()
	if len(loaded) != 1 || loaded[0].Gen != genBefore+1 {
		t.Fatalf("重装没有真发生：%+v（genBefore=%d）", loaded, genBefore)
	}
	if _, ok := table.Get("echo"); !ok {
		t.Fatalf("重装之后工具没了：%v", toolNames(table))
	}

	// 没有变化时不重装。
	third, err := h.Tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if third.Reloads != 0 {
		t.Fatalf("无变化却重装了：%+v", third)
	}
	if got := h.Status().Reloads; got != 1 {
		t.Fatalf("Status().Reloads = %d，want 1", got)
	}
}

// 一串连续通知必须合并成一次重装：否则"保存 10 次"就是 10 次重装。
func TestHMRObserveCoalescesBurst(t *testing.T) {
	manager, loader, _ := newRuntime(t)
	pkg := writeWasmPackage(t, filepath.Join(t.TempDir(), "echo-pkg"), "acme.echo", "1.0.0", echoToolDecls())
	if _, err := manager.Install(pkg); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Enable("acme.echo"); err != nil {
		t.Fatal(err)
	}
	genBefore := loader.Loaded()[0].Gen

	h, err := NewHMR(loader, HMRConfig{Enabled: true, Root: manager.root})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Stop()
	if _, err := h.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}

	manifest := filepath.Join(manager.root, "packages", "acme.echo", "1.0.0", "plugin.json")
	for i := 0; i < 5; i++ {
		if err := h.Observe(manifest); err != nil {
			t.Fatalf("第 %d 次 Observe 失败: %v", i, err)
		}
	}
	if got := h.Status().Pending; got != 1 {
		t.Fatalf("Pending = %d，want 1（同一插件的一串变更要合并）", got)
	}
	result, err := h.Tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Reloads != 1 {
		t.Fatalf("5 条通知触发了 %d 次重装，want 1", result.Reloads)
	}
	if got := loader.Loaded()[0].Gen; got != genBefore+1 {
		t.Fatalf("gen = %d，want %d", got, genBefore+1)
	}
	if got := h.Status().Pending; got != 0 {
		t.Fatalf("重装后 Pending = %d，want 0", got)
	}
}

func TestHMRQueueIsBounded(t *testing.T) {
	manager, loader, _ := newRuntime(t)
	h, err := NewHMR(loader, HMRConfig{Enabled: true, Root: manager.root, MaxPending: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Stop()

	one := filepath.Join(manager.root, "packages", "acme.one", "1.0.0", "plugin.json")
	two := filepath.Join(manager.root, "packages", "acme.two", "1.0.0", "plugin.json")
	if err := h.Observe(one); err != nil {
		t.Fatal(err)
	}
	if err := h.Observe(two); !errors.Is(err, ErrHMRQueueFull) {
		t.Fatalf("超界错误 = %v，want ErrHMRQueueFull（无界队列迟早把内存吃完）", err)
	}
	if got := h.Status().Pending; got != 1 {
		t.Fatalf("Pending = %d，want 1", got)
	}

	// 不在插件根之下的路径根本不归 HMR 管。
	if err := h.Observe(filepath.Join(t.TempDir(), "elsewhere", "plugin.json")); !errors.Is(err, ErrHMRNotManaged) {
		t.Fatalf("越界路径错误 = %v，want ErrHMRNotManaged", err)
	}
}

// Run 是宿主接线用的：显式开启才起，Stop 立刻收敛（不留孤儿 goroutine）。
func TestHMRRunStopsOnStop(t *testing.T) {
	manager, loader, _ := newRuntime(t)
	h, err := NewHMR(loader, HMRConfig{Enabled: true, Root: manager.root, Interval: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- h.Run(context.Background()) }()

	deadline := time.Now().Add(3 * time.Second)
	for h.Status().Scans == 0 {
		if time.Now().After(deadline) {
			t.Fatal("Run 起来了但一次都没扫过")
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.Stop()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop 之后 Run 没有退出")
	}

	// 关闭的 HMR 不许 Run（否则"关着但后台在扫"是假象）。
	closed, err := NewHMR(loader, HMRConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Run(context.Background()); !errors.Is(err, ErrHMROff) {
		t.Fatalf("关闭状态 Run 错误 = %v，want ErrHMROff", err)
	}
}

// digest 扫描不许碰真实用户目录：Root 缺省时什么都不扫。
func TestHMRScanWithoutRootScansNothing(t *testing.T) {
	_, loader, _ := newRuntime(t)
	h, err := NewHMR(loader, HMRConfig{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Stop()
	result, err := h.Tick(context.Background())
	if err != nil {
		t.Fatalf("没有 Root 时 Tick 失败: %v", err)
	}
	if result.Scanned != 0 || result.Reloads != 0 {
		t.Fatalf("没有 Root 时扫到了东西：%+v", result)
	}
	if strings.TrimSpace(h.Status().LastError) == "" {
		t.Fatal("开着但没 Root 是配置缺项，必须在 Status 里说出来")
	}
}
