package main

// 防复发守卫：**版本号只有一个出处**（`cmd/dshd/main.go` 的 `version`）。
//
// 2026-11-09 的真实事故（GO.7 / T2 随后发现、T14 修）：宿主 `--version` 报的是新版本，
// 但插件兼容性判定按 `internal/pluginmgr/compat.go` 里一个没人更新的字面量判——
// **宿主对插件生态说谎**：声明"要求新版本"的插件会被这个宿主判成不兼容。
// 同族还有 `internal/acp/server.go` 的兜底默认值。
//
// 这条守卫把四件事钉死（前两条是"扫全仓"，后两条是"注入真的接上了"）：
//
//	A. 字面量唯一：`cmd/ internal/ modules/ codecs/ scripts/` 里的源码/脚本出现**当前发布前缀**
//	   （从 main.go 现读；形态是 `<major>.<minor>.<patch>-go.`（稳定版）或
//	   `<major>.<minor>.<patch>-gobeta.`（任务途中的测试版），由 `versiontag.ReleasePrefix`
//	   取"最后一个点之前"得到）时，只允许两个文件——
//	   `cmd/dshd/main.go`（唯一出处）与 `cmd/dshtui/brand.go`（`build.sh` 用同一个 `VER`
//	   同时 `-ldflags -X main.version` 注入的两个 main 包之一）。其余任何文件都算"第二个版本源"。
//	B. 两处同值：`cmd/dshtui/brand.go` 的 version 必须逐字节等于 `cmd/dshd/main.go` 的 version。
//	C. 注入一致：`wireHostVersion()` 之后 `pluginmgr.HostRuntimeVersion` 与 `acp.FallbackVersion()`
//	   都必须等于 main.version（而不是某个历史值）。
//	D. 判定真的用它：拿 main.version 造一个 `engines.dsh = ">=<version>"` 的插件声明必须判**兼容**，
//	   `"< <version>"` 的必须判**不兼容**——即"插件兼容性用到的 runtime 版本"就是当前版本。
//
// 反例（把守卫自己弄坏会红）见文件末尾的 `TestVersionGuardCatchesARegression`。

import (
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dshgo/internal/pluginmgr"
	"dshgo/internal/versiontag"
)

// allowedLiteralFiles 是允许出现发布版本字面量的两个文件（相对仓库根，斜杠分隔）。
//
// 为什么 brand.go 在名单里：它与 main.go 一样是 `main` 包，`scripts/build.sh` 用
// **同一个 `VER`** 对两边都做 `-X main.version` 注入（脚本里 `LDFLAGS` 一段即是），
// 所以它的字面量只是"直接 `go build` 不走脚本"时的默认值，产物里永远是注入值。
// 名单之外出现字面量 = 产物里也不会被覆盖 = 又一个会漂移的版本源——那正是本守卫要拦的。
var allowedLiteralFiles = []string{"cmd/dshd/main.go", "cmd/dshtui/brand.go"}

// scanRoots / scanSuffixes 定义"全仓"的扫描面：代码与脚本。
//
// 为什么不扫 `.md` / `.json`：CHANGELOG、`docs/parameter-register.md` 这类**文档**本来就要
// 写明版本号（那是它们的内容），把它们算进来只会让守卫变成噪音。要拦的是**代码里**的第二份来源。
var (
	scanRoots    = []string{"cmd", "internal", "modules", "codecs", "scripts"}
	scanSuffixes = []string{".go", ".sh", ".py", ".ps1", ".cmd"}
)

// codeText 返回一个文件里**除注释以外**的文本，用来判断"代码里有没有版本字面量"。
//
//   - `.go`：用 go/scanner 逐 token 取字面量，跳过 COMMENT（字符串/raw string 仍然算，
//     因为它们是真的会进产物的值）；解析失败就退回原文，宁可多报也不漏报。
//   - `.sh` / `.py` / `.ps1` / `.cmd`：按行剔除 `#` / `//` / `REM` 开头的注释行。
//     （脚本里真正的赋值仍然会被扫到，例如 `VER=0.2.2-rc.2-go.1`。）
func codeText(path string, raw []byte) string {
	// 用 rune 拼换行，避免在测试源码里再出现被工具层转义的转义序列。
	newline := string(rune(10))
	if !strings.HasSuffix(path, ".go") {
		var b strings.Builder
		for _, line := range strings.Split(string(raw), newline) {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//") ||
				strings.HasPrefix(strings.ToUpper(trimmed), "REM ") {
				continue
			}
			b.WriteString(line)
			b.WriteString(newline)
		}
		return b.String()
	}
	fset := token.NewFileSet()
	file := fset.AddFile(path, fset.Base(), len(raw))
	var sc scanner.Scanner
	sc.Init(file, raw, nil, 0)
	var b strings.Builder
	for {
		_, tok, lit := sc.Scan()
		if tok == token.EOF {
			break
		}
		if tok == token.COMMENT {
			continue
		}
		b.WriteString(lit)
		b.WriteString(newline)
	}
	if sc.ErrorCount > 0 {
		return string(raw)
	}
	return b.String()
}

func repoRootFromCmdDshd(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("解析仓库根：%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("仓库根不像仓库根（没有 go.mod）：%s", root)
	}
	return root
}

// TestHostVersionHasExactlyOneSourceOutsideBuildInjectedMains 见文件头 A 条。
func TestHostVersionHasExactlyOneSourceOutsideBuildInjectedMains(t *testing.T) {
	root := repoRootFromCmdDshd(t)
	version, err := versiontag.FromMainSource(filepath.Join(root, "cmd", "dshd", "main.go"))
	if err != nil {
		t.Fatalf("读唯一出处失败：%v", err)
	}
	prefix := versiontag.ReleasePrefix(version)
	if prefix == "" || prefix == version {
		t.Fatalf("版本号 %q 解析不出发布前缀（%q）：守卫的针没拿稳", version, prefix)
	}
	allowed := map[string]bool{}
	for _, name := range allowedLiteralFiles {
		allowed[name] = true
	}

	var offenders []string
	scanned := 0
	for _, dir := range scanRoots {
		base := filepath.Join(root, filepath.FromSlash(dir))
		err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				switch info.Name() {
				case ".git", "node_modules", "testdata":
					return filepath.SkipDir
				}
				return nil
			}
			matched := false
			for _, suffix := range scanSuffixes {
				if strings.HasSuffix(info.Name(), suffix) {
					matched = true
					break
				}
			}
			if !matched {
				return nil
			}
			scanned++
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			// 只认**代码**里的字面量：注释里写一行版本号示例（例如
			// `// 0.2.2-rc.2-go.1  上游带 rc 的稳定版`）不会让产物说谎，
			// 把它算成"第二个版本源"是误报。误报的守卫最后一定被加豁免，
			// 然后没人再看它——所以在这里就把注释剔掉，判据反而更准。
			if !strings.Contains(codeText(path, raw), prefix) {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if !allowed[rel] {
				offenders = append(offenders, rel)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("扫描 %s：%v", dir, err)
		}
	}
	if scanned < 100 {
		// 扫到的文件太少 = 走错目录了，**不能报绿**（这条守卫自己也要防"假通过"）。
		t.Fatalf("只扫到 %d 个文件，扫描面不对（仓库根 %s）", scanned, root)
	}
	if len(offenders) > 0 {
		t.Fatalf("发布版本 %q 的字面量（前缀 %q）出现在 %v；\n"+
			"只允许 %v —— 其余地方必须从唯一出处取（宿主启动注入 / versiontag.FromMainSource），"+
			"否则产物会像 2026-11-09 那样对插件生态说谎。", version, prefix, offenders, allowedLiteralFiles)
	}
	t.Logf("扫描 %d 个文件，发布前缀 %q 只出现在 %v", scanned, prefix, allowedLiteralFiles)
}

// TestTUIAndHostVersionLiteralsAgree 见文件头 B 条。
func TestTUIAndHostVersionLiteralsAgree(t *testing.T) {
	root := repoRootFromCmdDshd(t)
	host, err := versiontag.FromMainSource(filepath.Join(root, "cmd", "dshd", "main.go"))
	if err != nil {
		t.Fatalf("读宿主版本：%v", err)
	}
	tui, err := versiontag.FromMainSource(filepath.Join(root, "cmd", "dshtui", "brand.go"))
	if err != nil {
		t.Fatalf("读 TUI 版本：%v", err)
	}
	if host != tui {
		t.Fatalf("两个被 build.sh 注入的 main 包版本分叉：host=%q tui=%q", host, tui)
	}
}

// TestHostRuntimeVersionIsInjectedFromSingleSource 见文件头 C / D 条。
func TestHostRuntimeVersionIsInjectedFromSingleSource(t *testing.T) {
	root := repoRootFromCmdDshd(t)
	want, err := versiontag.FromMainSource(filepath.Join(root, "cmd", "dshd", "main.go"))
	if err != nil {
		t.Fatalf("读唯一出处：%v", err)
	}

	// C：注入一次（与真实启动同一条路径），两个下游都必须拿到同一个值。
	wireHostVersion()
	if pluginmgr.HostRuntimeVersion != want {
		t.Fatalf("pluginmgr.HostRuntimeVersion = %q，want %q（宿主 runtime 版本必须来自 main.version）",
			pluginmgr.HostRuntimeVersion, want)
	}
	if got := acpFallbackVersion(); got != want {
		t.Fatalf("acp 兜底版本 = %q，want %q", got, want)
	}

	// D：插件兼容性判定真的按这个版本判。
	compatible := pluginmgr.Manifest{
		Schema: pluginmgr.ManifestSchema, ID: "acme.future", Version: "1.0.0",
		Engines: &pluginmgr.Engines{DSH: ">=" + want},
	}
	issue, err := pluginmgr.EvaluateCompatibility(compatible, nil, pluginmgr.HostRuntimeVersion)
	if err != nil {
		t.Fatalf("EvaluateCompatibility(>=%s)：%v", want, err)
	}
	if issue != nil {
		t.Fatalf("声明 engines.dsh=\">=%s\" 的插件必须判为兼容，得到 %+v", want, issue)
	}
	incompatible := pluginmgr.Manifest{
		Schema: pluginmgr.ManifestSchema, ID: "acme.future", Version: "1.0.0",
		Engines: &pluginmgr.Engines{DSH: "<" + want},
	}
	issue, err = pluginmgr.EvaluateCompatibility(incompatible, nil, pluginmgr.HostRuntimeVersion)
	if err != nil {
		t.Fatalf("EvaluateCompatibility(<%s)：%v", want, err)
	}
	if issue == nil {
		t.Fatalf("声明 engines.dsh=\"<%s\" 的插件必须判为不兼容", want)
	}
	if got := issue.Requirements["engines.dsh"]; got != "<"+want {
		t.Fatalf("不兼容要求字段 = %q，want %q", got, "<"+want)
	}
}

// TestVersionGuardCatchesARegression 是守卫的**自证**：把版本字面量塞进一个临时文件，
// 用同一套判据跑一遍，必须能抓到。没有它，上面那条"全绿"可能只是扫描面写错了。
func TestVersionGuardCatchesARegression(t *testing.T) {
	root := repoRootFromCmdDshd(t)
	version, err := versiontag.FromMainSource(filepath.Join(root, "cmd", "dshd", "main.go"))
	if err != nil {
		t.Fatalf("读唯一出处：%v", err)
	}
	prefix := versiontag.ReleasePrefix(version)

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "internal", "pluginmgr"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 复刻事故形态：库代码里写死一份发布版本。
	body := "package pluginmgr\n\nvar HostRuntimeVersion = \"" + version + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "internal", "pluginmgr", "compat.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "internal", "pluginmgr", "compat.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), prefix) {
		t.Fatalf("自证失败：造出来的回归样例里没有 %q", prefix)
	}
	allowed := map[string]bool{"cmd/dshd/main.go": true, "cmd/dshtui/brand.go": true}
	rel := "internal/pluginmgr/compat.go"
	if allowed[rel] {
		t.Fatalf("自证失败：%s 不该在允许名单里", rel)
	}
}
