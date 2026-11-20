package official_test

// 官方 `pluginManager.{inspect,installBundle,updateBundle}` 的 **git 形态**测试。
//
// 纪律：**不 clone 任何外部仓库**。这里的"远端"是本机临时目录里的真 git
// 仓库，spec 用 `file:///…` 指向它——git 走的是自己的传输层（file 协议），
// 与联网克隆同一条代码路径，只是两端都在本机。
//
// 官方对照（随包 0.2.2-rc.2 `dsh-plugin-manager/src/`）：
//   - `index.ts:620-668`（git 分支在 `:637`）inspect：只回形态
//     `{accepted, kind:'git', bundle:null, registry, host}`，name/version 不出现；
//     `types.ts:243-262` 是它的类型原文（`bundle: boolean|null`）；
//   - `index.ts:969-1055` installBundle：`git-transport.ts:65-72 chooseGitTransport`
//     之后由 pnpm 按 spec 取包，装完的 `ChangeResult` 与 registry/tarball 同一份；
//   - `index.ts:738-800` updateBundle 流程 + `:809-823` planGitUpdate：完整 commit
//     且同 origin 才接受 `options.spec`；`:827-843` gitHostUnreachable 只短路
//     network/timeout；
//   - `install-spec.ts:108-133` pinnedGitOrigin：`#<40hex>`（+ 至多一个 `path:`）、
//     `:53-59` gitHost。
//
// 本文件同时钉住"三个形态返回契约一致"——那是本任务最容易回归的一条：
// git 形态的 ChangeResult 字段若与 registry/tarball 长得不一样，客户端要为
// 一个来源写第二套渲染。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"dshgo/internal/pluginmgr/official"
)

// gitRepo 是"一个能装插件的本地 git 仓库"。
type gitRepo struct {
	dir string
	url string
}

// newGitRepo 建一个空仓库；包由 commitPackage 写进去。
func newGitRepo(t *testing.T) *gitRepo {
	t.Helper()
	requireGit(t)
	dir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "init", "--quiet")
	gitRun(t, dir, "config", "user.email", "pmgit@example.com")
	gitRun(t, dir, "config", "user.name", "PMGIT")
	gitRun(t, dir, "config", "commit.gpgsign", "false")
	resolved, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	// `git+file://` 形态（Windows 上保留盘符）。它是官方 git 形态的一种真实
	// 写法（`GIT_URL` 明认 `git+<proto>://`），也是本机仓库唯一能稳定落进
	// git 形态的写法：裸的 `file://…` 按官方 parseInstallSpec 是**本地路径**
	// （install-spec.ts:75 先剥 `file:`/`link:` 再判 git），本仓同一判据。
	return &gitRepo{dir: dir, url: "git+file://" + filepath.ToSlash(resolved)}
}

// commitPackage 把给定版本的包写进仓库（subdir 空则写根）并提交，返回 commit。
func (r *gitRepo) commitPackage(t *testing.T, subdir, id, version string) string {
	t.Helper()
	target := r.dir
	if subdir != "" {
		target = filepath.Join(r.dir, filepath.FromSlash(subdir))
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	packageFixture(t, target, id, version, nil)
	gitRun(t, r.dir, "add", "--all")
	gitRun(t, r.dir, "commit", "--quiet", "-m", "publish "+id+"@"+version)
	return gitRun(t, r.dir, "rev-parse", "HEAD")
}

// spec 返回"钉住这个 commit"的 git spec。
func (r *gitRepo) spec(commit, subdir string) string {
	spec := r.url + "#" + commit
	if subdir != "" {
		spec += "&path:" + subdir
	}
	return spec
}

// TestGitInspectAcceptedOnlyCarriesForm 钉住官方 git 分支**只回形态**。
func TestGitInspectAcceptedOnlyCarriesForm(t *testing.T) {
	repo := newGitRepo(t)
	commit := repo.commitPackage(t, "", "acme.gitinspect", "1.2.3")

	f := newFixture(t, "")
	result := f.result(t, "pluginManager.inspect", map[string]any{"spec": repo.spec(commit, "")})

	// 官方 git 分支的字段集合：status/kind/bundle/registry/host。
	if result["status"] != "accepted" {
		t.Fatalf("git inspect 必须 accepted：%v", result)
	}
	if result["kind"] != "git" {
		t.Fatalf("kind 必须是 git：%v", result)
	}
	// bundle 是 **null**（形态说不出有没有 patch），不是 false。
	bundle, present := result["bundle"]
	if !present || bundle != nil {
		t.Fatalf("git inspect 的 bundle 必须是 null（形态说不出），实际 present=%v %#v", present, bundle)
	}
	// name/version **不出现**：取下来才知道（官方只回形态）。
	for _, absent := range []string{"name", "version"} {
		if value, present := result[absent]; present && value != nil && value != "" {
			t.Fatalf("git inspect 不该报 %s（官方只回形态）：%v", absent, result)
		}
	}
	// host 与 registry 两个键都必须在（registry 可以是 null，键不能缺）。
	if _, present := result["host"]; !present {
		t.Fatalf("git inspect 必须带 host 字段：%v", result)
	}
	if _, present := result["registry"]; !present {
		t.Fatalf("git inspect 必须带 registry 键（null 也要显式出现）：%v", result)
	}
	// inspect 是"看形态"，不落盘。
	if got := installedIDs(t, f.manager); len(got) != 0 {
		t.Fatalf("inspect 不该改变安装状态：%+v", got)
	}
}

// TestGitInstallBundleFromLocalRepo 是 git 形态安装的主链。
func TestGitInstallBundleFromLocalRepo(t *testing.T) {
	t.Setenv("DSHD_PLUGIN_CACHE", t.TempDir())
	repo := newGitRepo(t)
	commit := repo.commitPackage(t, "", "acme.gitinstall", "1.0.0")

	f := newFixture(t, "")
	result := f.result(t, "pluginManager.installBundle", map[string]any{"spec": repo.spec(commit, "")})
	if result["changed"] != true || result["application"] != "applied" {
		t.Fatalf("git install 必须真实应用：%v", result)
	}
	if result["bundle"] != "acme.gitinstall" || result["version"] != "1.0.0" {
		t.Fatalf("git install 的结果字段不对：%v", result)
	}
	if result["stage"] != "install" {
		t.Fatalf("git install 的 stage 必须是 install：%v", result)
	}

	// 真实状态：包目录里是那个 commit 的内容。
	installed := installedIDs(t, f.manager)
	info, ok := installed["acme.gitinstall"]
	if !ok {
		t.Fatalf("git 装完没落到真实状态：%+v", installed)
	}
	if info.Version != "1.0.0" || !info.Enabled {
		t.Fatalf("安装状态不对：%+v", info)
	}
	pkgDir := filepath.Join(f.root, "packages", "acme.gitinstall", "1.0.0")
	if _, err := os.Stat(filepath.Join(pkgDir, ".git")); err == nil {
		t.Fatalf("装好的包里不该有 .git：%s", pkgDir)
	}
	if _, err := os.Stat(filepath.Join(pkgDir, "plugin.json")); err != nil {
		t.Fatalf("装好的包里没有 plugin.json：%v", err)
	}
}

// TestGitInstallSubdirectorySpec：`#<commit>&path:<sub>` 只装那个子目录。
func TestGitInstallSubdirectorySpec(t *testing.T) {
	t.Setenv("DSHD_PLUGIN_CACHE", t.TempDir())
	repo := newGitRepo(t)
	// 根目录放一个**别的**包：装错目录会被这条抓住。
	repo.commitPackage(t, "", "acme.root", "9.9.9")
	commit := repo.commitPackage(t, "plugins/widget", "acme.subdir", "2.0.0")

	f := newFixture(t, "")
	result := f.result(t, "pluginManager.installBundle", map[string]any{
		"spec": repo.spec(commit, "plugins/widget"),
	})
	if result["application"] != "applied" || result["bundle"] != "acme.subdir" {
		t.Fatalf("path: 子目录没被装上：%v", result)
	}
	installed := installedIDs(t, f.manager)
	if _, ok := installed["acme.root"]; ok {
		t.Fatalf("装错了目录（根目录的包）：%+v", installed)
	}
	if info := installed["acme.subdir"]; info.Version != "2.0.0" {
		t.Fatalf("子目录版本不对：%+v", info)
	}
}

// TestGitInstallUnppinnedSpecRefetchesHead 钉住缓存复用判据：
// **只有钉住完整 commit 的 spec 才允许复用旧 checkout**。
//
// 没钉 commit 的 spec（`git+file://…/repo` 取默认分支）内容会随时间变，
// 复用旧目录会把"再装一次"变成"装回旧提交"——那是静默给用户旧代码。
func TestGitInstallUnppinnedSpecRefetchesHead(t *testing.T) {
	t.Setenv("DSHD_PLUGIN_CACHE", t.TempDir())
	repo := newGitRepo(t)
	repo.commitPackage(t, "", "acme.githead", "1.0.0")
	// 没有 `#<commit>`：取默认分支 HEAD。
	spec := repo.url

	f := newFixture(t, "")
	if result := f.result(t, "pluginManager.installBundle", map[string]any{"spec": spec}); result["version"] != "1.0.0" {
		t.Fatalf("第一次安装应装到 1.0.0：%v", result)
	}
	if err := f.manager.Uninstall("acme.githead"); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	// 同一个 spec 串，但仓库的 HEAD 已经前进了一版。
	repo.commitPackage(t, "", "acme.githead", "2.0.0")
	result := f.result(t, "pluginManager.installBundle", map[string]any{"spec": spec})
	if result["application"] != "applied" || result["version"] != "2.0.0" {
		t.Fatalf("未钉 commit 的 spec 必须重取（拿到新的 HEAD 2.0.0），实际 %v", result)
	}
}

// TestGitInstallRecordsSpecNotCachePath 钉住 state.json 记的是**安装 spec**。
//
// 为什么这条重要：registry/tarball/git 的包体在本地缓存里，若把缓存路径记成
// 来源，`BundleInfo.source`/`update` 就判不出这个包还能不能换版——列表刷新
// 之后用户会看到"来源是一个临时目录、换不了版本"，那是假信息。
func TestGitInstallRecordsSpecNotCachePath(t *testing.T) {
	t.Setenv("DSHD_PLUGIN_CACHE", t.TempDir())
	repo := newGitRepo(t)
	commit := repo.commitPackage(t, "", "acme.gitsource", "1.0.0")
	spec := repo.spec(commit, "")

	f := newUpdateFixture(t, "")
	if result := f.result(t, "pluginManager.installBundle", map[string]any{"spec": spec}); result["application"] != "applied" {
		t.Fatalf("install: %v", result)
	}
	state, ok, err := f.manager.Bundle("acme.gitsource")
	if err != nil || !ok {
		t.Fatalf("Bundle: ok=%v err=%v", ok, err)
	}
	if state.Source != spec {
		t.Fatalf("state.json 的来源必须是安装 spec %q，实际 %q", spec, state.Source)
	}
	// listBundles 的 update 判据因此必须是 'git'（由同一个真实来源算出来）。
	row := bundleRow(t, f, "acme.gitsource")
	if row["update"] != "git" {
		t.Fatalf("从 git 装的包 update 必须是 git：%v", row)
	}
	if row["source"] != spec {
		t.Fatalf("listBundles 的 source 必须是安装 spec：%v", row["source"])
	}
}

// TestGitUpdateBundleToNewCommit 是 git 形态更新的主链。
func TestGitUpdateBundleToNewCommit(t *testing.T) {
	t.Setenv("DSHD_PLUGIN_CACHE", t.TempDir())
	repo := newGitRepo(t)
	first := repo.commitPackage(t, "", "acme.gitupdate", "1.0.0")
	second := repo.commitPackage(t, "", "acme.gitupdate", "2.0.0")

	f := newUpdateFixture(t, "")
	installed := f.result(t, "pluginManager.installBundle", map[string]any{"spec": repo.spec(first, "")})
	if installed["application"] != "applied" || installed["version"] != "1.0.0" {
		t.Fatalf("前置安装没成功：%v", installed)
	}
	if row := bundleRow(t, f, "acme.gitupdate"); row["update"] != "git" {
		t.Fatalf("前置条件：update 必须是 git：%v", row)
	}

	result := f.result(t, "pluginManager.updateBundle", map[string]any{
		"name": "acme.gitupdate", "version": "2.0.0",
		"options": map[string]any{"spec": repo.spec(second, "")},
	})
	if result["from"] != "1.0.0" || result["to"] != "2.0.0" || result["step"] != "install" {
		t.Fatalf("git update 三段式不对：%v", result)
	}
	inner, ok := result["result"].(map[string]any)
	if !ok || inner["application"] != "applied" {
		t.Fatalf("git update 的 result 必须是 applied：%v", result)
	}
	state, _, err := f.manager.Bundle("acme.gitupdate")
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	if state.Version != "2.0.0" {
		t.Fatalf("更新没落到真实状态：%s", state.Version)
	}
	if want := repo.spec(second, ""); state.Source != want {
		t.Fatalf("更新后来源必须是新的安装 spec %q，实际 %q", want, state.Source)
	}
}

// TestGitUpdateRefusesBadTargets 钉住 git 更新的四条契约拒绝（不取任何内容）。
func TestGitUpdateRefusesBadTargets(t *testing.T) {
	t.Setenv("DSHD_PLUGIN_CACHE", t.TempDir())
	repo := newGitRepo(t)
	first := repo.commitPackage(t, "", "acme.gitrefuse", "1.0.0")
	second := repo.commitPackage(t, "", "acme.gitrefuse", "2.0.0")
	other := newGitRepo(t)
	otherCommit := other.commitPackage(t, "", "acme.gitrefuse", "2.0.0")

	f := newUpdateFixture(t, "")
	if result := f.result(t, "pluginManager.installBundle", map[string]any{"spec": repo.spec(first, "")}); result["application"] != "applied" {
		t.Fatalf("前置安装没成功：%v", result)
	}

	cases := []struct {
		name   string
		body   map[string]any
		fragmt string
	}{
		{
			name:   "没有 options.spec",
			body:   map[string]any{"name": "acme.gitrefuse", "version": "2.0.0"},
			fragmt: "full commit",
		},
		{
			name: "短 commit",
			body: map[string]any{"name": "acme.gitrefuse", "version": "2.0.0",
				"options": map[string]any{"spec": repo.url + "#abc1234"}},
			fragmt: "full commit",
		},
		{
			name: "完整 commit 但别的仓库",
			body: map[string]any{"name": "acme.gitrefuse", "version": "2.0.0",
				"options": map[string]any{"spec": other.spec(otherCommit, "")}},
			// 官方 diagnostic 点名"不在装进来的仓库目录 <origin> 里"。
			// origin 的规范化形态（小写 host + 路径）由 updatekind_test.go 钉住。
			fragmt: "装进来的仓库目录",
		},
		{
			name: "版本不比已装的更新",
			body: map[string]any{"name": "acme.gitrefuse", "version": "1.0.0",
				"options": map[string]any{"spec": repo.spec(second, "")}},
			fragmt: "1.0.0",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := f.result(t, "pluginManager.updateBundle", tc.body)
			if result["step"] != "check" {
				t.Fatalf("拒绝时 step 必须是 check：%v", result)
			}
			inner, _ := result["result"].(map[string]any)
			errObj, _ := inner["error"].(map[string]any)
			if errObj["code"] != "invalid-spec" {
				t.Fatalf("拒绝码必须是 invalid-spec：%v", result)
			}
			diagnostic, _ := errObj["diagnostic"].(string)
			if !strings.Contains(diagnostic, tc.fragmt) {
				t.Fatalf("diagnostic 应含 %q，实际 %q", tc.fragmt, diagnostic)
			}
			state, _, err := f.manager.Bundle("acme.gitrefuse")
			if err != nil {
				t.Fatalf("Bundle: %v", err)
			}
			if state.Version != "1.0.0" {
				t.Fatalf("被拒的更新改了版本：%s", state.Version)
			}
		})
	}
}

// TestGitFetchFailureIsStructured 钉住"取不到"是结构化答案，不是 500，也不改状态。
func TestGitFetchFailureIsStructured(t *testing.T) {
	t.Setenv("DSHD_PLUGIN_CACHE", t.TempDir())
	f := newFixture(t, "")
	missing := "git+file://" + filepath.ToSlash(filepath.Join(t.TempDir(), "absent.git"))
	spec := missing + "#0123456789abcdef0123456789abcdef01234567"

	result := f.result(t, "pluginManager.installBundle", map[string]any{"spec": spec})
	if result["application"] != "failed" || result["changed"] != false {
		t.Fatalf("取不到的 git 安装必须是结构化 failed：%v", result)
	}
	if got := installedIDs(t, f.manager); len(got) != 0 {
		t.Fatalf("失败的 git 安装装了东西：%+v", got)
	}
	// inspect 同一条 spec 必须是 200 结构化回答（不是 500）。
	code, _ := f.call(t, "pluginManager.inspect", map[string]any{"spec": spec})
	if code != http.StatusOK {
		t.Fatalf("git inspect 必须回 200（acccepted/refused 都是结构化），实际 %d", code)
	}
}

// TestGitContractMatchesRegistryAndTarball 是本任务的核心断言：
// **三个形态的返回契约一致**。逐字段比对 ChangeResult 的键集合与取值形状。
func TestGitContractMatchesRegistryAndTarball(t *testing.T) {
	t.Setenv("DSHD_PLUGIN_CACHE", t.TempDir())

	// registry 与 tarball 的远端都是本机 httptest（不联网）。
	registryPkg := packageFixture(t, t.TempDir(), "acme.formregistry", "1.0.0", nil)
	tarballPkg := packageFixture(t, t.TempDir(), "acme.formtarball", "1.0.0", nil)
	registryTar := tarPackage(t, registryPkg)
	tarballTar := tarPackage(t, tarballPkg)

	mux := http.NewServeMux()
	mux.HandleFunc("/acme.formregistry", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name":      "acme.formregistry",
			"dist-tags": map[string]string{"latest": "1.0.0"},
			"versions": map[string]any{"1.0.0": map[string]any{
				"version": "1.0.0",
				"dist":    map[string]any{"tarball": "http://" + r.Host + "/acme.formregistry/-/pkg.tgz"},
			}},
		})
	})
	mux.HandleFunc("/acme.formregistry/-/pkg.tgz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(registryTar) })
	mux.HandleFunc("/tarball.tgz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(tarballTar) })
	server := httptest.NewServer(mux)
	defer server.Close()

	repo := newGitRepo(t)
	gitCommit := repo.commitPackage(t, "", "acme.formgit", "1.0.0")

	shapes := map[string]map[string]any{}
	for name, spec := range map[string]string{
		"registry": "acme.formregistry@1.0.0",
		"tarball":  server.URL + "/tarball.tgz",
		"git":      repo.spec(gitCommit, ""),
	} {
		f := newFixture(t, "")
		body := map[string]any{"spec": spec}
		if name == "registry" {
			body["options"] = map[string]any{"registry": server.URL}
		}
		result := f.result(t, "pluginManager.installBundle", body)
		if result["application"] != "applied" {
			t.Fatalf("%s 形态的安装没成功：%v", name, result)
		}
		shapes[name] = result
	}

	// 1) 键集合完全一致。
	keys := func(m map[string]any) []string {
		out := make([]string, 0, len(m))
		for key := range m {
			out = append(out, key)
		}
		sort.Strings(out)
		return out
	}
	want := strings.Join(keys(shapes["registry"]), ",")
	for _, name := range []string{"tarball", "git"} {
		if got := strings.Join(keys(shapes[name]), ","); got != want {
			t.Fatalf("%s 形态的 ChangeResult 键集合与 registry 不一致：%q vs %q", name, got, want)
		}
	}
	// 2) 三个形态的取值形状相同（避免"键一样、类型不一样"）。
	for name, result := range shapes {
		if result["stage"] != "install" || result["changed"] != true {
			t.Fatalf("%s 形态的 stage/changed 不对：%v", name, result)
		}
		if _, ok := result["bundle"].(string); !ok {
			t.Fatalf("%s 形态的 bundle 不是字符串：%#v", name, result["bundle"])
		}
		version, ok := result["version"].(string)
		if !ok || !strings.HasPrefix(version, "1.0.0") {
			t.Fatalf("%s 形态的 version 不对：%#v", name, result["version"])
		}
		if enabled, ok := result["enabled"].(bool); !ok || !enabled {
			t.Fatalf("%s 形态的 enabled 不对：%#v", name, result["enabled"])
		}
		if warnings, ok := result["warnings"].([]any); !ok || len(warnings) == 0 {
			t.Fatalf("%s 形态的 warnings 必须是数组（applied 结果都带运行时边界说明）：%#v", name, result["warnings"])
		}
	}
}

// TestGitSpecParsing 钉住 spec 拆分（形态判据是契约的一部分，不是实现细节）。
func TestGitSpecParsing(t *testing.T) {
	commit := "0123456789abcdef0123456789abcdef01234567"
	cases := []struct {
		spec   string
		host   string
		commit string
		path   string
	}{
		{"github:acme/widget#" + commit, "github.com", commit, ""},
		{"gitlab:acme/widget#" + commit + "&path:plugins/x", "gitlab.com", commit, "plugins/x"},
		{"git+https://example.com/acme/widget.git#" + commit, "example.com", commit, ""},
		{"https://example.com:8443/acme/widget#" + strings.ToUpper(commit), "example.com:8443", commit, ""},
		{"git@example.com:acme/widget.git#" + commit, "example.com", commit, ""},
		{"https://example.com/acme/widget", "example.com", "", ""},
		{"https://example.com/acme/widget#abc1234", "example.com", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.spec, func(t *testing.T) {
			location, ok := official.SplitGitSpec(tc.spec)
			if !ok {
				t.Fatalf("%q 必须是 git spec", tc.spec)
			}
			if location.Host != tc.host {
				t.Fatalf("host 应为 %q，实际 %q", tc.host, location.Host)
			}
			if location.Commit != tc.commit {
				t.Fatalf("commit 应为 %q，实际 %q", tc.commit, location.Commit)
			}
			if location.Path != tc.path {
				t.Fatalf("path 应为 %q，实际 %q", tc.path, location.Path)
			}
			if strings.TrimSpace(location.Origin) == "" {
				t.Fatalf("%q 的 origin 为空", tc.spec)
			}
		})
	}
	// 非 git 形态一律 false（别把 registry 名/本地路径吞进来）。
	for _, spec := range []string{"", "acme.bundle", "./local", "https://e.com/x-1.0.0.tgz"} {
		if _, ok := official.SplitGitSpec(spec); ok {
			t.Fatalf("%q 不该被当成 git spec", spec)
		}
	}
	// 主机简写要展开成 git 认的 URL（git 不认 `github:`）。
	location, _ := official.SplitGitSpec("github:acme/widget#" + commit)
	if !strings.HasPrefix(location.Origin, "https://github.com/acme/widget") {
		t.Fatalf("主机简写必须展开成 https URL，实际 %q", location.Origin)
	}
}
