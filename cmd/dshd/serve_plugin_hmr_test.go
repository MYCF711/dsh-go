package main

// 插件热重载在**宿主装配层**的接线证据（HS-14 WIRING §C）。
//
// 这里只钉住三件事，实现语义的细节在 internal/pluginmgr/hmr_test.go：
//  1. 默认路径（不设环境变量）构造出来的运行时，HMR 恒为 disabled；
//  2. 只有显式 `DSHD_PLUGIN_HMR=1` 才构造开启配置，且 Root 指向插件管理器根；
//  3. 关闭状态下 `Tick` 是纯 no-op，不会扫描/重装/写任何文件。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/pluginmgr"
)

// TestPluginHMRConfigDefaultsToDisabled：默认关闭是本任务的安全要求。
func TestPluginHMRConfigDefaultsToDisabled(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plugins")
	for _, value := range []string{"", "0", "false", "off", "no", " "} {
		t.Setenv("DSHD_PLUGIN_HMR", value)
		cfg := pluginHMRConfig(root)
		if cfg.Enabled {
			t.Fatalf("DSHD_PLUGIN_HMR=%q 居然开启了 HMR（默认必须 disabled）", value)
		}
		if cfg.Root != "" {
			t.Fatalf("关闭态不该带 Root：%q", cfg.Root)
		}
	}
}

// TestPluginHMRConfigExplicitOptIn：显式为真才开启，且周期/根被固定。
func TestPluginHMRConfigExplicitOptIn(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plugins")
	for _, value := range []string{"1", "true", "TRUE", "yes", "on"} {
		t.Setenv("DSHD_PLUGIN_HMR", value)
		cfg := pluginHMRConfig(root)
		if !cfg.Enabled {
			t.Fatalf("DSHD_PLUGIN_HMR=%q 没有开启 HMR", value)
		}
		if cfg.Root != root {
			t.Fatalf("Root = %q，want %q", cfg.Root, root)
		}
		if cfg.Interval <= 0 {
			t.Fatalf("Interval = %v，want > 0", cfg.Interval)
		}
	}
}

// TestPluginRuntimeHMRDisabledByDefault：newPluginRuntime 出来的 HMR 必须是关闭的，
// 且 Tick 不扫描（关闭态是纯 no-op，不是"空转但报错"）。
func TestPluginRuntimeHMRDisabledByDefault(t *testing.T) {
	manager, err := pluginmgr.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := newPluginRuntime(manager)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })

	if runtime.HMR() == nil {
		t.Fatal("运行时没有 HMR 句柄：接线点拿不到 Enabled()")
	}
	if runtime.HMR().Enabled() {
		t.Fatal("newPluginRuntime 的 HMR 默认必须是关闭的")
	}
	result, err := runtime.HMR().Tick(context.Background())
	if err != nil {
		t.Fatalf("关闭态 Tick 不该报错: %v", err)
	}
	if !result.Skipped {
		t.Fatalf("关闭态 Tick 应返回 Skipped=true，拿到 %+v", result)
	}
	if result.Scanned != 0 || result.Reloads != 0 {
		t.Fatalf("关闭态 Tick 不许扫描/重装：%+v", result)
	}
}

// TestPluginRuntimeHMREnabledConfig：显式开启后构造成功（Enabled、Root 都在），
// 并且打开的是一个**空插件根**——Tick 只建基线，不写任何文件。
func TestPluginRuntimeHMREnabledConfig(t *testing.T) {
	t.Setenv("DSHD_PLUGIN_HMR", "1")
	managerRoot := t.TempDir()
	manager, err := pluginmgr.Open(managerRoot)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "plugins")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	runtime, err := newPluginRuntimeWithHMR(manager, pluginHMRConfig(root))
	if err != nil {
		t.Fatalf("显式开启的 HMR 构造失败: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()); runtime.HMR().Stop() })

	if !runtime.HMR().Enabled() {
		t.Fatal("显式 DSHD_PLUGIN_HMR=1 之后 HMR 仍未开启")
	}
	result, err := runtime.HMR().Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick 失败: %v", err)
	}
	if result.Scanned != 0 {
		t.Fatalf("空插件根不该扫出 manifest：%+v", result)
	}
	// HMR 没有写文件代码：根目录里必须还是空的（不是"顺手建了个索引文件"）。
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("HMR 在插件根写入了文件：%v", names)
	}
	if status := runtime.HMR().Status(); !status.Enabled {
		t.Fatalf("Status().Enabled = false，want true：%+v", status)
	}
}

// TestPluginHMRNeverTouchesHostPatchFile：HMR 的入口守卫拒绝宿主热重载开关。
// （实现级用例在 internal/pluginmgr/hmr_test.go；这里钉的是宿主装配不会绕过它。）
func TestPluginHMRNeverTouchesHostPatchFile(t *testing.T) {
	manager, err := pluginmgr.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := newPluginRuntimeWithHMR(manager, pluginmgr.HMRConfig{
		Enabled: true,
		Root:    filepath.Join(t.TempDir(), "plugins"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()); runtime.HMR().Stop() })

	home := t.TempDir()
	patchPath := filepath.Join(home, "profiles", "default", "cordis.patch.yml")
	if err := os.MkdirAll(filepath.Dir(patchPath), 0o700); err != nil {
		t.Fatal(err)
	}
	const original = "# host hot-reload switch: HMR must never write this\n"
	if err := os.WriteFile(patchPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := runtime.HMR().Observe(patchPath); err == nil {
		t.Fatal("Observe 接受了 profiles/*/cordis.patch.yml：入口守卫失效")
	} else if !strings.Contains(err.Error(), "cordis.patch.yml") {
		t.Fatalf("拒绝理由没有点名那个文件: %v", err)
	}
	if _, err := runtime.HMR().Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	raw, err := os.ReadFile(patchPath)
	if err != nil {
		t.Fatalf("宿主开关文件不见了: %v", err)
	}
	if string(raw) != original {
		t.Fatalf("宿主开关文件被改了：%q", raw)
	}
}
