package main

// GO.7 / t11 #26：官方 `workingDirectory.defaultDirectory`（新会话默认工作目录）。
//
// 官方原件（app.asar:dsh/node_modules/@deepseek-ai/dsh-working-directory/lib/index.js）：
//
//	:18  static Config = z.object({ defaultDirectory: z.string() })
//	:27  const directory = config.defaultDirectory ?? process.cwd();
//	:28  if (!isAbsolute(directory)) throw new Error("working-directory: defaultDirectory must be absolute");
//	:47    init: (header) => header.cwd ?? null,     // 会话 header 的 cwd 是权威值
//
// 宿主的 `--home` 是**会话根**（日志、state、skills 都挂它下面），不是工作目录；
// 新会话的工作目录此前恒为进程 cwd。判据（缺一即红）：
//  1. 未配置 = 与现在逐字节一致（进程 cwd，不是某个默认目录）；
//  2. 配置了就必须真的落到新会话的 header.cwd（不是只被读了一下）；
//  3. 相对路径必须 fail-loud（官方明确要求绝对路径）。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"dshgo/codecs/v4"
	"dshgo/internal/kernel"
	"dshgo/internal/session"
	"dshgo/internal/spi"
)

func TestDefaultWorkDirUnsetKeepsProcessCwd(t *testing.T) {
	t.Setenv(defaultWorkDirEnv, "")
	got, err := defaultWorkDirFromEnv()
	if err != nil {
		t.Fatalf("未配置不该报错: %v", err)
	}
	if got != "" {
		t.Fatalf("未配置必须返回空串（各入口保持原行为 = 进程 cwd），实际 %q", got)
	}
	// 空白也算未配置。
	t.Setenv(defaultWorkDirEnv, "  \t ")
	if got, err := defaultWorkDirFromEnv(); err != nil || got != "" {
		t.Fatalf("空白必须等于未配置，得到 (%q, %v)", got, err)
	}
}

func TestDefaultWorkDirRejectsRelative(t *testing.T) {
	t.Setenv(defaultWorkDirEnv, filepath.Join("some", "relative", "dir"))
	if _, err := defaultWorkDirFromEnv(); err == nil {
		t.Fatal("相对路径必须 fail-loud（官方 defaultDirectory must be absolute）")
	}
}

func TestDefaultWorkDirAbsoluteIsNormalized(t *testing.T) {
	dir := t.TempDir()
	// Windows 的 \\?\ 扩展前缀会写出真实 DSH 永远不会产生的 projectKey。
	t.Setenv(defaultWorkDirEnv, `\\?\`+dir)
	got, err := defaultWorkDirFromEnv()
	if err != nil {
		t.Fatalf("绝对路径不该报错: %v", err)
	}
	if want := v4.NormalizeCwd(dir); got != want {
		t.Fatalf("归一化后 = %q，期望 %q", got, want)
	}
}

// 配置了就必须落到**新会话的 header.cwd**：serve 建会话走的就是这条路径。
func TestServeCreateUsesDefaultWorkDir(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv(defaultWorkDirEnv, work)

	workdir, err := defaultWorkDirFromEnv()
	if err != nil {
		t.Fatalf("解析默认工作目录: %v", err)
	}
	create, err := serveCreate(home, workdir)
	if err != nil {
		t.Fatalf("构造 serve 建会话函数: %v", err)
	}
	id, err := create("")
	if err != nil {
		t.Fatalf("建会话: %v", err)
	}
	path, err := session.FindByID(home, id)
	if err != nil {
		t.Fatalf("回读会话 %s: %v", id, err)
	}
	hdr, err := v4.ReadHeader(path)
	if err != nil {
		t.Fatalf("读会话 header: %v", err)
	}
	if want := v4.NormalizeCwd(work); hdr.Cwd != want {
		t.Fatalf("新会话 header.cwd = %q，期望默认工作目录 %q", hdr.Cwd, want)
	}
}

// readMarkerViaAssembly 用给定工作目录走一遍**真实装配路径**（loadModsIn =
// run 入口用的那份），返回 read 工具能不能读到相对路径 marker.txt。
func readMarkerViaAssembly(t *testing.T, workdir string) error {
	t.Helper()
	mods, err := loadModsIn(filepath.Join(t.TempDir(), "no-such-manifest.toml"), t.TempDir(), 0, nil, workdir)
	if err != nil {
		t.Fatalf("装配模块: %v", err)
	}
	k := kernel.New("t11-workdir")
	defer k.Root().Dispose()
	if _, err := kernel.Assemble(k.Root(), mods); err != nil {
		t.Fatalf("装配: %v", err)
	}
	reg, err := kernel.Inject(k.Root(), spi.ToolsKey)
	if err != nil {
		t.Fatal(err)
	}
	read, ok := reg.Get("read")
	if !ok {
		t.Fatal("工具表里没有 read")
	}
	_, err = read.Exec(context.Background(), json.RawMessage(`{"file_path":"marker.txt"}`))
	return err
}

// 工具的根必须跟着默认工作目录走：否则系统提示说 Workspace: A，read/edit 在 B 里干活
// （ACP 那条注释里实测踩过的坑）。先红：没有 loadModsIn / 没有这个配置面。
func TestRunToolRootFollowsDefaultWorkDir(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "marker.txt"), []byte("在这里"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(defaultWorkDirEnv, work)
	workDir, err := sessionWorkDir()
	if err != nil {
		t.Fatalf("解析默认工作目录: %v", err)
	}
	if err := readMarkerViaAssembly(t, workDir); err != nil {
		t.Fatalf("配了默认工作目录后，read 的相对路径必须按它解析: %v", err)
	}
	// 反例：换一个不含 marker.txt 的目录装配 → 读不到。证明这个根**真的**来自参数。
	if err := readMarkerViaAssembly(t, t.TempDir()); err == nil {
		t.Fatal("换目录后仍然读得到，说明工具的根并没有跟着参数走")
	}
}
