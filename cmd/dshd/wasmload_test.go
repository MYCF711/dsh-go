package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/kernel"
)

// wasmFixture 是 internal/wasmhost 的夹具：60 字节，导出 add / spin。
// 跨包引用而不是复制一份：两个包看到同一份字节，"摘要对不上"才有意义。
// 测试的 cwd 是 cmd/dshd，所以要往上两级。
const wasmFixture = "../../internal/wasmhost/testdata/fixture.wasm"

// TestWasmModuleAssemblesAndShowsInGraph：合法 wasm 模块能装配，
// 装配结果里 backend 是 wasm、Provides 为空，且 `modules graph` / `list`
// 能看到它并显示 digest 前 12 位。
func TestWasmModuleAssemblesAndShowsInGraph(t *testing.T) {
	resetWasm(t)
	fixture := fixturePath(t)
	digest := sha256hexFile(t, fixture)
	logs := captureLogs(t)

	mp := writeManifest(t, `[profile]
name = "wasm"

[[module]]
id      = "wasm-probe"
backend = "wasm"
path    = "`+filepath.ToSlash(fixture)+`"
digest  = "`+digest+`"
`)
	mods, err := loadMods(mp, t.TempDir(), 0, nil)
	if err != nil {
		t.Fatalf("合法的 wasm 模块必须能装配，实际报错：%v", err)
	}
	defer closeLoadedWasm(context.Background())

	var got *kernel.Module
	for i := range mods {
		if mods[i].ID == "wasm-probe" {
			got = &mods[i]
		}
	}
	if got == nil {
		t.Fatal("装配结果里没有 wasm-probe")
	}
	if got.Backend != kernel.BackendWasm {
		t.Fatalf("backend 应当是 wasm，实际 %q", got.Backend)
	}
	// 这条是"不许假装它提供能力"的落点：装配结果里必须真的是空的，
	// 而不是靠日志里一句话搪塞。
	if len(got.Provides) != 0 {
		t.Fatalf("wasm 模块现在不提供任何能力，Provides 应当为空，实际 %v", got.Provides)
	}
	if got.Register != nil {
		t.Fatal("还没做工具契约映射，wasm 模块不该有 Register")
	}

	list := loadedWasm()
	if len(list) != 1 {
		t.Fatalf("登记的 wasm 模块应当正好 1 个，实际 %d", len(list))
	}
	if list[0].digest != digest {
		t.Fatalf("登记的摘要应当是文件内容的 sha256：%s vs %s", list[0].digest, digest)
	}

	var graph bytes.Buffer
	if err := writeModules(&graph, "graph", mods); err != nil {
		t.Fatalf("modules graph 失败：%v", err)
	}
	g := graph.String()
	// 路径分隔符在 Windows 上是反斜杠，输出比对前统一成正斜杠：
	// 这里要钉的是"打出了哪个文件"，不是分隔符长什么样。
	gSlash := strings.ReplaceAll(g, `\`, "/")
	for _, want := range []string{"wasm-probe", "wasm", digest[:12], filepath.ToSlash(fixture)} {
		if !strings.Contains(gSlash, want) {
			t.Fatalf("modules graph 输出里应当有 %q：\n%s", want, g)
		}
	}

	var listing bytes.Buffer
	if err := writeModules(&listing, "list", mods); err != nil {
		t.Fatalf("modules list 失败：%v", err)
	}
	for _, want := range []string{"wasm-probe", "wasm", digest[:12]} {
		if !strings.Contains(listing.String(), want) {
			t.Fatalf("modules list 输出里应当有 %q：\n%s", want, listing.String())
		}
	}

	// 日志必须明说"它不提供能力"，而不是让人从空 PROVIDES 列里猜。
	if !strings.Contains(logs.String(), "不提供任何能力") {
		t.Fatalf("日志里必须明说 wasm 模块不提供任何能力：\n%s", logs.String())
	}
}

// TestWasmDigestMismatchFails：manifest 里的 digest 与实际文件不符 → 装配失败。
// 这正是 manifest 校验的意义：内容被换掉要在启动时炸。
func TestWasmDigestMismatchFails(t *testing.T) {
	resetWasm(t)
	fixture := fixturePath(t)
	real := sha256hexFile(t, fixture)
	wrong := strings.Repeat("0", 64)
	if wrong == real {
		t.Fatal("夹具摘要不可能是全 0，测试前提被破坏了")
	}

	mp := writeManifest(t, `[profile]
name = "wasm"

[[module]]
id      = "wasm-probe"
backend = "wasm"
path    = "`+filepath.ToSlash(fixture)+`"
digest  = "`+wrong+`"
`)
	if _, err := loadMods(mp, t.TempDir(), 0, nil); err == nil {
		t.Fatal("digest 不匹配必须让装配失败，不能装作没看见")
	} else {
		msg := err.Error()
		if !strings.Contains(msg, "digest") && !strings.Contains(msg, "摘要") {
			t.Fatalf("错误里要指出是摘要校验失败：%v", err)
		}
		if !strings.Contains(msg, "wasm-probe") {
			t.Fatalf("错误里要点名是哪个模块：%v", err)
		}
		if !strings.Contains(msg, real) {
			t.Fatalf("错误里要给出实际摘要，便于核对：%v", err)
		}
	}
	// 失败后不能留下"已登记"的假象。
	if got := loadedWasm(); len(got) != 0 {
		t.Fatalf("装配失败后不该留下登记的 wasm 模块：%v", got)
	}
}

// TestWasmMissingFileFails：文件不存在 → 装配失败，错误里带路径。
func TestWasmMissingFileFails(t *testing.T) {
	resetWasm(t)
	missing := filepath.Join(t.TempDir(), "no-such-module.wasm")

	mp := writeManifest(t, `[profile]
name = "wasm"

[[module]]
id      = "wasm-gone"
backend = "wasm"
path    = "`+filepath.ToSlash(missing)+`"
`)
	_, err := loadMods(mp, t.TempDir(), 0, nil)
	if err == nil {
		t.Fatal("路径不存在必须让装配失败")
	}
	if !strings.Contains(err.Error(), "no-such-module.wasm") {
		t.Fatalf("错误里要带路径：%v", err)
	}
	if !strings.Contains(err.Error(), "wasm-gone") {
		t.Fatalf("错误里要点名是哪个模块：%v", err)
	}
}

// TestWasmNonWasmFileFails：文件在，但不是 wasm → 报错要带 wasmhost 的原始原因（魔数）。
func TestWasmNonWasmFileFails(t *testing.T) {
	resetWasm(t)
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake.wasm")
	if err := os.WriteFile(fake, []byte("这不是 wasm，只是一段文本"), 0o644); err != nil {
		t.Fatal(err)
	}

	mp := writeManifest(t, `[profile]
name = "wasm"

[[module]]
id      = "wasm-fake"
backend = "wasm"
path    = "`+filepath.ToSlash(fake)+`"
`)
	_, err := loadMods(mp, t.TempDir(), 0, nil)
	if err == nil {
		t.Fatal("非 wasm 文件必须让装配失败")
	}
	msg := err.Error()
	if !strings.Contains(msg, "0asm") {
		t.Fatalf("错误里要保留 wasmhost 的原始原因（魔数 \\0asm）：%v", err)
	}
	if !strings.Contains(msg, "fake.wasm") {
		t.Fatalf("错误里要带路径：%v", err)
	}
}

// TestWasmProvidesRejected：manifest 给 wasm 模块写 provides → 直接拒绝。
// 不静默忽略，是因为"写了 provides 却被丢掉"等于配置说谎。
func TestWasmProvidesRejected(t *testing.T) {
	resetWasm(t)
	fixture := fixturePath(t)

	mp := writeManifest(t, `[profile]
name = "wasm"

[[module]]
id       = "wasm-probe"
backend  = "wasm"
path     = "`+filepath.ToSlash(fixture)+`"
provides = ["compaction"]
`)
	_, err := loadMods(mp, t.TempDir(), 0, nil)
	if err == nil {
		t.Fatal("wasm 模块现在不提供能力，写 provides 必须报错")
	}
	if !strings.Contains(err.Error(), "不提供任何能力") {
		t.Fatalf("错误里要说清为什么不行：%v", err)
	}
}

// TestWasmDisabledSkipped：enabled = false 的 wasm 模块连文件都不读——
// 一个关掉的模块不该因为路径坏了把启动拖下水。
func TestWasmDisabledSkipped(t *testing.T) {
	resetWasm(t)
	missing := filepath.Join(t.TempDir(), "never-touched.wasm")

	mp := writeManifest(t, `[profile]
name = "wasm"

[[module]]
id      = "wasm-off"
backend = "wasm"
path    = "`+filepath.ToSlash(missing)+`"
enabled = false
`)
	mods, err := loadMods(mp, t.TempDir(), 0, nil)
	if err != nil {
		t.Fatalf("禁用的 wasm 模块不该被装载：%v", err)
	}
	for _, m := range mods {
		if m.ID == "wasm-off" {
			t.Fatal("禁用的模块不该出现在装配结果里")
		}
	}
	if got := loadedWasm(); len(got) != 0 {
		t.Fatalf("禁用的模块不该被登记：%v", got)
	}
}

// --- 测试辅助 ---

func fixturePath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(wasmFixture)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("夹具不在预期位置（%s）：%v", p, err)
	}
	return p
}

func writeManifest(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "dsh.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func sha256hexFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// captureLogs 把 slog 默认 handler 换成 buffer，返回它以便断言日志内容。
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// resetWasm 让每个用例从"没有已装载 wasm"开始，并在结束时收干净。
// 顺带把 slog 静音：Apply 的白名单告警每个用例 40+ 行，不该淹掉测试输出。
func resetWasm(t *testing.T) {
	t.Helper()
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	closeLoadedWasm(context.Background())
	t.Cleanup(func() { closeLoadedWasm(context.Background()) })
}
