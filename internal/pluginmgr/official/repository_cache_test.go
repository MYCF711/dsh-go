package official_test

// 官方 `dsh-plugin-market/src/repository-cache.ts` 的宿主移植测试。
//
// 这里**不造假 git**：第一个 https 请求打到一个真实的 HTTP 认证服务器（401 +
// WWW-Authenticate），git 自己判定 "Authentication failed" = denied；SSH 重试
// 走真实的 ssh 传输，只把 ssh 命令换成一个 `exec git upload-pack <本地仓库>` 的
// 最小替身——这仍然是 git 自己发起的两次 fetch，只是网络两端都在本机。
//
// 为什么不能只打桩：任务要求"真实 git 调用"，而 SSH 回退的关键行为（把
// remote.origin.url 改成 SSH 形式、成功后就固定、失败合并两条诊断）只有在
// 真实调用 git 时才会发生。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"dshgo/internal/pluginmgr/official"
)

// requireGit 在测试环境没有 git 时直接跳过（本仓 CI 环境有 git）。
func requireGit(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("测试环境没有 git：%v", err)
	}
	return path
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

// createSourceRepository 建一个有 README.md 的真实本地仓库，返回仓库目录与 HEAD commit。
func createSourceRepository(t *testing.T) (string, string) {
	t.Helper()
	requireGit(t)
	dir := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "init", "--quiet")
	gitRun(t, dir, "config", "user.email", "d4@example.com")
	gitRun(t, dir, "config", "user.name", "D4")
	gitRun(t, dir, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Private plugin\n\n只有 SSH 凭据也能读。\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "README.md")
	gitRun(t, dir, "commit", "--quiet", "-m", "init")
	return dir, gitRun(t, dir, "rev-parse", "HEAD")
}

// deniedHTTPServer 是一个真实 HTTP 认证拒绝：任何请求都 401 + WWW-Authenticate。
// git 对它的诊断是 "Authentication failed"，在官方分类里正是 `denied`。
func deniedHTTPServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	return server
}

// fakeSshUploadPack 写一个替身 ssh：忽略 host/path，直接用本地仓库跑
// `git upload-pack`（协议另一端仍然是真 git 的 fetch）。
func fakeSshUploadPack(t *testing.T, localRepo string) string {
	t.Helper()
	script := strings.Join([]string{
		"#!/bin/sh",
		`exec git -c uploadpack.allowFilter=true upload-pack "` + localRepo + `"`,
		"",
	}, "\n")
	if runtime.GOOS == "windows" {
		script = strings.ReplaceAll(script, "\r\n", "\n")
	}
	path := filepath.Join(t.TempDir(), "fake-ssh.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// sshCommandFor 把脚本变成 GIT_SSH_COMMAND：git 会把它交给 sh 执行，所以
// 路径必须是 POSIX 形式（Windows 的反斜杠会被 shell 吃掉）。
func sshCommandFor(path string) string {
	return "sh '" + filepath.ToSlash(path) + "'"
}

// failingSsh 是一个必然失败的 ssh：输出公共密钥被拒的诊断。
func failingSsh(t *testing.T) string {
	t.Helper()
	script := strings.Join([]string{
		"#!/bin/sh",
		`echo "git@127.0.0.1: Permission denied (publickey)." >&2`,
		"exit 255",
		"",
	}, "\n")
	path := filepath.Join(t.TempDir(), "failing-ssh.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func newCache(t *testing.T) *official.RepositoryCache {
	t.Helper()
	cache := official.NewRepositoryCache(filepath.Join(t.TempDir(), "repositories"))
	cache.Timeout = 60 * time.Second
	return cache
}

// TestSshURLOf 钉住官方 `sshUrlOf` 的形状：只对无凭据、无 query 的 http(s)
// 生效；其它一律不给 SSH 形式（换了就是另一个地址，不能拿来重试）。
func TestSshURLOf(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"https://github.com/o/r.git", "ssh://git@github.com/o/r.git", true},
		{"http://example.com/o/r", "ssh://git@example.com/o/r", true},
		{"HTTPS://Example.COM/o/r.git", "ssh://git@Example.COM/o/r.git", true},
		{"https://example.com:8443/o/r.git", "ssh://git@example.com/o/r.git", true},
		{"https://user:pw@example.com/o/r.git", "", false},
		{"https://user@example.com/o/r.git", "", false},
		{"https://example.com/o/r.git?x=1", "", false},
		{"https://example.com/o/r.git#frag", "", false},
		{"https://example.com", "", false},
		{"git@github.com:o/r.git", "", false},
		{"ssh://git@github.com/o/r.git", "", false},
		{"file:///tmp/r.git", "", false},
	}
	for _, testCase := range cases {
		got, ok := official.SshURLOf(testCase.in)
		if ok != testCase.ok || got != testCase.want {
			t.Fatalf("SshURLOf(%q) = (%q, %v)，期望 (%q, %v)", testCase.in, got, ok, testCase.want, testCase.ok)
		}
	}
	if official.RepositoryOperationCommands != 5 {
		t.Fatalf("OPERATION_COMMANDS 必须是官方的 5，得到 %d", official.RepositoryOperationCommands)
	}
}

// TestRepositoryCacheSshRetryAfterDenied 是 release note「只有 SSH 凭据也能看
// 私有 Git 插件」的核心证据：https 被真实拒绝 → 换 SSH 形式重试成功 →
// remote 固定为 SSH 形式 → 文件读得出来。
func TestRepositoryCacheSshRetryAfterDenied(t *testing.T) {
	localRepo, _ := createSourceRepository(t)
	server := deniedHTTPServer(t)
	ssh := fakeSshUploadPack(t, localRepo)
	t.Setenv("GIT_SSH_COMMAND", sshCommandFor(ssh))

	cache := newCache(t)
	content, err := cache.ReadFile(context.Background(), server.URL+"/o/r.git", "", "README.md")
	if err != nil {
		t.Fatalf("SSH 回退后读取应成功，得到 %v", err)
	}
	entries, _ := os.ReadDir(cache.Dir)
	if len(entries) > 0 {
		repoDir := filepath.Join(cache.Dir, entries[0].Name())
		t.Logf("git 版本: %s", gitRun(t, repoDir, "--version"))
		t.Logf("https 源: %s（真实 401 拒绝）", server.URL+"/o/r.git")
		t.Logf("回退后 remote.origin.url: %s", gitRun(t, repoDir, "config", "remote.origin.url"))
	}
	if !strings.Contains(string(content), "只有 SSH 凭据也能读") {
		t.Fatalf("README 内容不对：%q", content)
	}
}

// TestRepositoryCacheRemoteFixedToSsh：回退成功后 remote.origin.url 必须固定成
// SSH 形式（后续读取不再撞 https 的拒绝）。
func TestRepositoryCacheRemoteFixedToSsh(t *testing.T) {
	localRepo, _ := createSourceRepository(t)
	server := deniedHTTPServer(t)
	ssh := fakeSshUploadPack(t, localRepo)
	t.Setenv("GIT_SSH_COMMAND", sshCommandFor(ssh))

	cache := newCache(t)
	repository := filepath.Join(cache.Dir, "")
	if _, err := cache.EnsureCommit(context.Background(), server.URL+"/o/r.git", ""); err != nil {
		t.Fatalf("EnsureCommit 失败：%v", err)
	}
	entries, err := os.ReadDir(repository)
	if err != nil || len(entries) == 0 {
		t.Fatalf("缓存目录里没有仓库：%v %v", entries, err)
	}
	repoDir := filepath.Join(repository, entries[0].Name())
	remote := gitRun(t, repoDir, "config", "remote.origin.url")
	want, ok := official.SshURLOf(server.URL + "/o/r.git")
	if !ok {
		t.Fatalf("测试 URL 没有 SSH 形式：%s", server.URL)
	}
	if remote != want {
		t.Fatalf("remote.origin.url = %q，期望固定为 SSH 形式 %q", remote, want)
	}
}

// TestRepositoryCacheMergesBothDiagnostics：两次都失败时合并两条诊断，按
// denied 归类（不是网络问题时），不吞第一条。
func TestRepositoryCacheMergesBothDiagnostics(t *testing.T) {
	server := deniedHTTPServer(t)
	t.Setenv("GIT_SSH_COMMAND", sshCommandFor(failingSsh(t)))

	cache := newCache(t)
	_, err := cache.EnsureCommit(context.Background(), server.URL+"/o/r.git", "")
	if err == nil {
		t.Fatal("两次都失败必须报错")
	}
	var failure *official.RepositoryFailure
	if !errors.As(err, &failure) {
		t.Fatalf("错误必须是 *RepositoryFailure：%v", err)
	}
	if failure.Reason != "denied" {
		t.Fatalf("reason = %q，期望 denied（%v）", failure.Reason, err)
	}
	if !strings.Contains(failure.Message, "Authentication failed") && !strings.Contains(failure.Message, "could not read Username") {
		t.Fatalf("合并诊断缺少第一条（https 拒绝）：%v", failure.Message)
	}
	if !strings.Contains(failure.Message, "Permission denied") && !strings.Contains(failure.Message, "Could not read from remote repository") {
		t.Fatalf("合并诊断缺少第二条（SSH 失败）：%v", failure.Message)
	}
}

// TestRepositoryCacheNetworkFailureIsClassified：连不上不是"被拒"，归 network，
// 不许拿 SSH 重试掩盖（官方只有 denied 才回退）。
func TestRepositoryCacheNetworkFailureIsClassified(t *testing.T) {
	requireGit(t)
	cache := newCache(t)
	cache.Timeout = 20 * time.Second
	_, err := cache.EnsureCommit(context.Background(), "http://127.0.0.1:1/o/r.git", "")
	if err == nil {
		t.Fatal("连不上必须报错")
	}
	var failure *official.RepositoryFailure
	if !errors.As(err, &failure) {
		t.Fatalf("错误必须是 *RepositoryFailure：%v", err)
	}
	if failure.Reason != "network" {
		t.Fatalf("reason = %q，期望 network（%v）", failure.Reason, err)
	}
}

// TestRepositoryCacheMissingFileIsInvalid：仓库在、文件不在 → invalid（不是
// denied/network），上层据此给"文档不存在"而不是"凭据问题"。
func TestRepositoryCacheMissingFileIsInvalid(t *testing.T) {
	localRepo, _ := createSourceRepository(t)
	server := deniedHTTPServer(t)
	t.Setenv("GIT_SSH_COMMAND", sshCommandFor(fakeSshUploadPack(t, localRepo)))

	cache := newCache(t)
	_, err := cache.ReadFile(context.Background(), server.URL+"/o/r.git", "", "NOPE.md")
	if err == nil {
		t.Fatal("不存在的文件必须报错")
	}
	var failure *official.RepositoryFailure
	if !errors.As(err, &failure) {
		t.Fatalf("错误必须是 *RepositoryFailure：%v", err)
	}
	if failure.Reason != "invalid" {
		t.Fatalf("reason = %q，期望 invalid（%v）", failure.Reason, err)
	}
}
