package pluginmgr

// 红 C：**JS / Cordis 插件被静默当成功**。
//
// 现状（隔离基线上真实跑出来的行为）：`kind = "wasm"` + `entry = "index.js"` 的包
// 会被 `Install` 原样接受——它只复制字节、校验 SHA-256，从不看入口是不是能被宿主
// 执行的东西。于是"装上了"被当成成功，而本仓不嵌 JS 运行时（AGENTS R6），
// 那个入口永远不可能跑起来。这是**静默成功**，比报错难查得多。
//
// 本文件的断言是"显式不支持"：错误必须是四字段 Error、错误码必须是
// `unsupported_plugin`、文案必须点名 JS / Cordis 与"不嵌 JS 运行时"，
// 且被拒绝的包不能留在已安装状态。
//
// 为什么错误码不复用 `invalid_manifest`：坏 manifest 是"你写错了"，
// 而这是一个**合法的 Cordis/JS 包**，只是本仓不支持——两件事的下一步完全不同
// （前者改文件，后者换插件或等宿主支持）。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRawPackage 写一个"任意形状"的包：payload 按 name 原样落盘，
// files 里自动补上它的 SHA-256；manifest 的其余字段由调用方给。
func writeRawPackage(t *testing.T, dir, name string, payload []byte, manifest map[string]any) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	if raw, ok := manifest["files"]; ok && raw != nil {
		existing, ok := raw.(map[string]string)
		if !ok {
			t.Fatalf("files 必须是 map[string]string，得到 %T", raw)
		}
		files = existing
	}
	sum := sha256.Sum256(payload)
	files[name] = hex.EncodeToString(sum[:])
	manifest["files"] = files
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func assertUnsupportedPlugin(t *testing.T, err error, wantInMessage ...string) *Error {
	t.Helper()
	if err == nil {
		t.Fatal("期望显式拒绝，实际返回 nil（静默当成功）")
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("拒绝必须是四字段 Error，得到 %T: %v", err, err)
	}
	if apiErr.Code != "unsupported_plugin" {
		t.Fatalf("错误码 = %q，want unsupported_plugin（明确不支持，不是坏 manifest）: %v", apiErr.Code, apiErr)
	}
	if apiErr.Status != 422 {
		t.Fatalf("状态码 = %d，want 422（语义不可处理）: %v", apiErr.Status, apiErr)
	}
	if strings.TrimSpace(apiErr.Hint) == "" {
		t.Fatalf("四字段错误必须有 hint（模型/用户要知道下一步）: %v", apiErr)
	}
	if !strings.Contains(apiErr.Hint, "JS") {
		t.Fatalf("hint 必须点名 JS 运行时（这是本仓的红线 R6）: %q", apiErr.Hint)
	}
	for _, want := range wantInMessage {
		if !strings.Contains(apiErr.Message, want) {
			t.Fatalf("message 缺少 %q（要能一眼看出是哪种包）: %q", want, apiErr.Message)
		}
	}
	return apiErr
}

func TestJSEntryPackageIsExplicitlyUnsupported(t *testing.T) {
	manager, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pkg := writeRawPackage(t, filepath.Join(t.TempDir(), "js-plugin"), "index.js",
		[]byte("export default { name: 'cordis-js-plugin', apply() {} }\n"),
		map[string]any{
			"schema": "dsh.local-plugin@1", "id": "acme.cordis", "version": "1.0.0",
			"kind": "wasm", "entry": "index.js",
		})
	info, err := manager.Install(pkg)
	if err == nil {
		t.Fatalf("JS 入口插件被静默当成功：install 返回 %+v；本仓不嵌 JS 运行时，必须显式拒绝", info)
	}
	assertUnsupportedPlugin(t, err, "index.js")
	plugins, listErr := manager.List()
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(plugins) != 0 {
		t.Fatalf("被拒绝的 JS 包不能留在已安装状态：%+v", plugins)
	}
}

// Cordis 包（package.json + index.js，没有 dsh 的 plugin.json）走的是另一条
// 拒绝路径：今天它只报"读取 plugin.json 失败"，读起来像"文件放错了"，
// 而不是"本仓不支持这种插件"。必须点名 Cordis。
func TestCordisPackageIsExplicitlyUnsupported(t *testing.T) {
	manager, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "cordis-plugin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"),
		[]byte(`{"name":"dsh-cordis-demo","version":"1.0.0","main":"index.js"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.js"),
		[]byte("module.exports = { name: 'demo' }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := manager.Install(dir)
	if err == nil {
		t.Fatalf("Cordis/npm 插件被静默当成功：install 返回 %+v；本仓不嵌 JS 运行时，必须显式拒绝", info)
	}
	assertUnsupportedPlugin(t, err, "Cordis")
}

// 反例守卫：普通 wasm 包（没有 tools 声明）必须继续装得上。
// 拒绝 JS 不等于把"没声明工具"也一起拒掉——那是另一件事，且会让既有包全红。
func TestPlainWasmPackageStillInstalls(t *testing.T) {
	manager, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pkg := writeTestPackage(t, filepath.Join(t.TempDir(), "plain"), "acme.plain", "1.0.0")
	info, err := manager.Install(pkg)
	if err != nil {
		t.Fatalf("普通 wasm 包不该被拒绝：%v", err)
	}
	if info.ID != "acme.plain" || info.Enabled {
		t.Fatalf("install 投影不对：%+v", info)
	}
}
