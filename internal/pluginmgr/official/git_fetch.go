package official

// 插件安装的 git 获取器：把官方 `pluginManager.installBundle` / `updateBundle`
// 在 **git 形态**下真正要做的那一步（按 spec 的 `#<commit>` 取仓库内容）落到本仓。
//
// 为什么需要它：本仓此前三个 git 形态全是 501（adapter.go 的 inspect/installBundle
// 走 formUnsupported、updateGitBundle 走 ErrOfficialNotImplemented）。缺口登记
// 把它记成"本仓没有 git 获取器 checkout 它（AGENTS R6 不嵌 pnpm/git）"——那句话
// 的 R6 含义是**不 fork/vendor dsc、不用 mvdan 跑命令**，与"调用本机 git 取一个
// 仓库"不是一回事：本仓既有 `repository_cache.go` 已经在用 `os/exec` 调 git 做
// market 详情的只读读取，命令行工具（bash/pwsh/repohere/forge）也都在用。所以
// 这条缺口是真缺口，不是 R6 禁止的形态。
//
// 官方对照（随包 0.2.2-rc.2 `dsh-plugin-manager`）：
//   - `src/install-spec.ts:16-38` `ParsedInstallSpec`：git spec = 主机简写
//     /`git+…` URL/scp 形态/托管仓库 URL，`#` 后是 pnpm 的片段参数；
//   - `src/install-spec.ts:113-133` `pinnedGitOrigin`：`#<40 位 commit>` +
//     至多一个 `path:<子目录>`；`updateBundle` 只认这种 spec 当更新目标；
//   - `src/install-spec.ts:90-110` `gitHost`：简写→主机表、scp→`user@host`、
//     其余 URL 取 host（含端口）；
//   - `src/git-transport.ts:20-38` `sshGitSpec`：可回退的 HTTPS 地址才有 SSH 形态；
//   - `src/index.ts:1015-1030`：`chooseGitTransport` 之后由 pnpm 按 spec 取包。
//
// 本文件的边界：**只取内容，不改任何管理器状态**。装/换版仍走 `pluginmgr` 的
// 既有事务（journal + staging rename + 回滚）；这里产出的只是一个本地目录，
// 与 registry/tarball 形态交给 Manager 的东西**同一种东西**（见 registry_adapter.go
// 的 materializeRegistrySpec）。
//
// 取内容的方式是"真 git"，但**不克隆整棵仓库**：
//   `git init` → 配 origin → `git fetch --depth 1 <origin> <commit>` → 等价的
//   `git --work-tree=<dst> --git-dir=… checkout <commit> -- .`。
// 选这条路而不是 `git clone` 的理由：
//   - 官方 spec 钉的是**完整 commit**，浅取那一个 commit 就够，不必拉全历史；
//   - clone 之后再 checkout 会留下 `.git` 与远端配置在包目录里，而本仓的包目录
//     要过 `pluginmgr` 的 hash 校验（多余文件不该出现在包里）；
//   - 失败时只需要删掉一个目录，没有"半个 clone"要分辨。
//
// 安全边界（与 repository_cache.go 同一套 git 环境）：关掉交互提示、askpass、
// LFS 拉取；每条命令独立超时；诊断里不带凭据（fetch 诊断由 git 自己打码前
// 我们也不会把 spec 里的 userinfo 拼进去）。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// GitFetchTimeout 是单条 git 命令的上限。官方给的 `gitTransportTimeoutMs`
// 语义（一次访问探测的上限），取同一量级：一次浅取足够，超时就是网络坏了。
const GitFetchTimeout = 120 * time.Second

// gitFragment 是官方 `pinnedGitOrigin` 判据用的那条正则的另一半：
// `#` 之后的片段参数按 `&` 切开，`path:` 是子目录，其余那条必须是完整 commit。
var gitCommitPattern = regexp.MustCompile(`^(?i:[0-9a-f]{40})$`)

// GitFetchFailure 是一次取内容失败的分类。Reason 与官方 `PackageResult.kind`
// 同口径：network / denied / invalid / git-unavailable。
//
// 分类的意义在调用方：network/denied 是"这台机器现在取不到"，invalid 是
// "这个 spec 说的地方没有这个内容"——前者用户过会儿重试可能就好，后者不会。
type GitFetchFailure struct {
	Reason  string
	Message string
	cause   error
}

func (e *GitFetchFailure) Error() string {
	return fmt.Sprintf("git 取包失败（%s）: %s", e.Reason, e.Message)
}

func (e *GitFetchFailure) Unwrap() error { return e.cause }

// GitLocation 是一个 git spec 拆出来的三样东西，正好是取内容需要的全部。
type GitLocation struct {
	// Origin 是交给 `git fetch` 的地址（`file:///…`、`https://…`、`git@…:…`）。
	Origin string
	// Host 是官方 `inspect` 回给客户端的 `host` 字段（简写→主机表、scp→host、
	// URL→host（含端口））。
	Host string
	// Commit 是 `#` 后的完整 commit；空串表示 spec 没钉 commit。
	Commit string
	// Path 是 `#` 后的 `path:` 子目录（已去掉两端斜杠）；空串表示仓库根。
	Path string
}

// SplitGitSpec 把 git spec 拆成 Origin/Host/Commit/Path。
//
// 官方判据照抄（install-spec.ts:16-38 的形态、:90-110 的 host、:113-133 的
// 片段解析），差别只有一处：官方要求 `#` 后**必须**是一个完整 commit 才叫
// "pinned"，本函数允许没有 `#`（没有 commit 就是取默认分支 HEAD）——因为
// `installBundle` 的 spec 可以是 `github:o/r`，官方那时交给 pnpm 取默认分支。
//
// 返回 false 表示这不是 git spec（调用方本来也只会在 kind=git 时调它）。
func SplitGitSpec(spec string) (GitLocation, bool) {
	trimmed := strings.TrimSpace(spec)
	if trimmed == "" || tarballSpecSuffix.MatchString(trimmed) {
		// 官方 `TARBALL_SPEC` 先于 git 判据：地址里带 `.tgz` 的永远不是仓库。
		return GitLocation{}, false
	}
	location := GitLocation{}
	body := trimmed
	if hash := strings.Index(body, "#"); hash >= 0 {
		body = body[:hash]
		for _, parameter := range strings.Split(trimmed[hash+1:], "&") {
			switch {
			case strings.HasPrefix(parameter, "path:"):
				// pnpm 读重复参数里最后一个；这里遇到重复就以最后一个为准。
				location.Path = strings.Trim(parameter[len("path:"):], "/")
			case gitCommitPattern.MatchString(parameter):
				location.Commit = strings.ToLower(parameter)
			}
		}
	}
	origin, host, ok := gitOriginOf(body)
	if !ok {
		return GitLocation{}, false
	}
	location.Origin = origin
	location.Host = host
	return location, true
}

// tarballSpecSuffix 是官方 `TARBALL_SPEC`（install-spec.ts:28）：
// `.tgz` / `.tar.gz`，允许结尾带 `#` 片段。
var tarballSpecSuffix = regexp.MustCompile(`(?i)\.(?:tgz|tar\.gz)(?:#.*)?$`)

// gitShorthandHosts 是官方 `GIT_SHORTHAND_HOSTS`（install-spec.ts:31-34）。
var gitShorthandHosts = map[string]string{
	"github": "github.com", "gitlab": "gitlab.com",
	"bitbucket": "bitbucket.org", "gist": "gist.github.com",
}

// gitOriginOf 把 spec 的 `#` 之前部分翻成 `git fetch` 认的地址 + 官方 host。
//
// 主机简写要展开（`github:o/r` → `https://github.com/o/r`）：本仓不引入
// pnpm 的简写解析，而 git 不认简写。其余形态原样交给 git——包括
// `git+https://…`（先去 `git+` 前缀）与 scp 形态 `git@host:path`。
func gitOriginOf(body string) (origin, host string, ok bool) {
	lower := strings.ToLower(body)
	if colon := strings.Index(lower, ":"); colon > 0 {
		if mapped, isShorthand := gitShorthandHosts[lower[:colon]]; isShorthand {
			rest := body[colon+1:]
			return "https://" + mapped + "/" + rest, mapped, true
		}
	}
	// `git+<proto>://` → `<proto>://`；`git://` 原样。
	origin = body
	if strings.HasPrefix(lower, "git+") {
		origin = body[len("git+"):]
		lower = strings.ToLower(origin)
	}
	switch {
	case strings.HasPrefix(lower, "git@"):
		// scp 形态：`git@host:path`，host 到冒号为止。
		rest := origin[len("git@"):]
		colon := strings.Index(rest, ":")
		if colon <= 0 {
			return "", "", false
		}
		return origin, strings.ToLower(rest[:colon]), true
	case strings.Contains(lower, "://"):
		rest := origin[strings.Index(origin, "://")+len("://"):]
		// 去掉 userinfo（`user@host` 里的 host 才是主机）。
		if at := strings.LastIndex(rest, "@"); at >= 0 {
			if slash := strings.Index(rest, "/"); slash < 0 || at < slash {
				rest = rest[at+1:]
			}
		}
		end := strings.IndexAny(rest, "/?#")
		if end < 0 {
			end = len(rest)
		}
		host = strings.ToLower(rest[:end])
		if host == "" {
			return "", "", false
		}
		return origin, host, true
	}
	return "", "", false
}

// FetchGitPackage 把 spec 指向的仓库内容取到一个新的本地目录并返回它。
//
// dst 由调用方给（通常在 `pluginmgr.CacheDir()` 之下）：目录不存在会被建出来，
// 已存在则报错（不覆盖别人的目录）。返回的目录**不含** `.git`：git 目录放在
// 同级的临时位置，取完内容就删——包目录里只该有包自己的文件。
func FetchGitPackage(ctx context.Context, spec, dst string) (GitLocation, error) {
	location, ok := SplitGitSpec(spec)
	if !ok {
		return GitLocation{}, &GitFetchFailure{Reason: "invalid", Message: "不是 git spec: " + spec}
	}
	if strings.TrimSpace(dst) == "" {
		return GitLocation{}, &GitFetchFailure{Reason: "invalid", Message: "目标目录为空"}
	}
	if _, err := os.Stat(dst); err == nil {
		return GitLocation{}, &GitFetchFailure{Reason: "invalid", Message: "目标目录已存在: " + dst}
	}
	parent := filepath.Dir(dst)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return GitLocation{}, &GitFetchFailure{Reason: "git-unavailable", Message: "建缓存目录失败: " + err.Error(), cause: err}
	}
	// git 目录放在目标目录**旁边**：checkout 的 --work-tree 指目标目录，这样
	// 目标目录里不会出现 .git（包目录要过 hash 校验）。
	gitDir, err := os.MkdirTemp(parent, ".git-fetch-")
	if err != nil {
		return GitLocation{}, &GitFetchFailure{Reason: "git-unavailable", Message: "建临时 git 目录失败: " + err.Error(), cause: err}
	}
	defer os.RemoveAll(gitDir)
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return GitLocation{}, &GitFetchFailure{Reason: "git-unavailable", Message: "建目标目录失败: " + err.Error(), cause: err}
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(dst)
		}
	}()
	git := func(args ...string) error {
		return runGit(ctx, gitDir, args...)
	}
	if err := git("init", "--quiet", "--template=", "--bare"); err != nil {
		return GitLocation{}, err
	}
	// 浅取一个 commit：官方 spec 钉的就是完整 commit；没有 commit 时取默认分支 HEAD。
	target := location.Commit
	if target == "" {
		target = "HEAD"
	}
	if err := git("fetch", "--quiet", "--depth", "1", "--no-tags", "--", location.Origin, target); err != nil {
		return GitLocation{}, err
	}
	resolved := "FETCH_HEAD"
	if location.Commit != "" {
		resolved = location.Commit
	}
	// `checkout` 到 --work-tree：`-f` 是必须的（bare 仓库没有工作树，
	// read-tree 会把 --work-tree 里的东西当成要删的）。
	if err := git("--work-tree="+dst, "checkout", "--quiet", "-f", resolved, "--", "."); err != nil {
		return GitLocation{}, err
	}
	// 子目录：spec 的 `#<commit>&path:<sub>` 只装那个目录，与 pnpm 对
	// `path:` 的处理一致。
	if location.Path != "" {
		if err := selectSubdirectory(dst, location.Path); err != nil {
			return GitLocation{}, err
		}
	}
	if err := os.RemoveAll(filepath.Join(dst, ".git")); err != nil {
		return GitLocation{}, &GitFetchFailure{Reason: "git-unavailable", Message: "清掉 .git 失败: " + err.Error(), cause: err}
	}
	success = true
	return location, nil
}

// selectSubdirectory 把 dst 收敛成 `sub` 那个子目录的内容。
//
// 先在同级的临时目录里做（不允许"只搬一半"）：搬完再 rename 回来，失败时 dst
// 保持取出来的原样，调用方看到的是 `invalid`（这个 spec 指向的目录不存在）。
func selectSubdirectory(dst, sub string) error {
	if strings.HasPrefix(sub, "/") || strings.Contains(sub, "..") {
		return &GitFetchFailure{Reason: "invalid", Message: "spec 的子目录非法: " + sub}
	}
	source := filepath.Join(dst, filepath.FromSlash(sub))
	info, err := os.Stat(source)
	if err != nil || !info.IsDir() {
		return &GitFetchFailure{
			Reason:  "invalid",
			Message: fmt.Sprintf("仓库里没有目录 %q（spec 的 path: 子目录）", sub),
		}
	}
	staging, err := os.MkdirTemp(filepath.Dir(dst), ".subdir-")
	if err != nil {
		return &GitFetchFailure{Reason: "git-unavailable", Message: "建临时目录失败: " + err.Error(), cause: err}
	}
	defer os.RemoveAll(staging)
	moved := filepath.Join(staging, "content")
	if err := os.Rename(source, moved); err != nil {
		return &GitFetchFailure{Reason: "git-unavailable", Message: "搬子目录失败: " + err.Error(), cause: err}
	}
	if err := os.RemoveAll(dst); err != nil {
		return &GitFetchFailure{Reason: "git-unavailable", Message: "清目标目录失败: " + err.Error(), cause: err}
	}
	if err := os.Rename(moved, dst); err != nil {
		return &GitFetchFailure{Reason: "git-unavailable", Message: "落子目录失败: " + err.Error(), cause: err}
	}
	return nil
}

// runGit 跑一条 git 命令：独立超时、去交互、诊断只回文本。
func runGit(ctx context.Context, gitDir string, args ...string) error {
	runCtx, cancel := context.WithTimeout(ctx, GitFetchTimeout)
	defer cancel()
	command := exec.CommandContext(runCtx, gitBinary(), append([]string{"--git-dir=" + gitDir}, args...)...)
	command.Env = repositoryGitEnv()
	var stderr strings.Builder
	command.Stderr = &stderr
	command.Stdout = nil
	runErr := command.Run()
	if runErr == nil {
		return nil
	}
	diagnostic := strings.TrimSpace(stderr.String())
	label := "git " + strings.Join(args, " ")
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return &GitFetchFailure{
			Reason:  "network",
			Message: fmt.Sprintf("%s 超时（%s）", label, GitFetchTimeout),
			cause:   context.DeadlineExceeded,
		}
	}
	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) {
		// 连二进制都没起来：这台机器没有 git。
		return &GitFetchFailure{
			Reason:  "git-unavailable",
			Message: "跑不了 git（本机没有 git 可执行文件？）: " + runErr.Error(),
			cause:   runErr,
		}
	}
	failure := repositoryFailureOf(diagnostic, label, runErr)
	// repositoryFailureOf 把非网络一律归成 denied；带仓库内容的那条要在
	// invalid 里——"这个 commit 在仓库里不存在"不是权限问题。
	if failure.Reason == "denied" && repositoryMissingPath.MatchString(diagnostic) {
		failure.Reason = "invalid"
	}
	return failure
}

// gitBinary 是 git 可执行文件名（被 repository_cache 的 GitBinary 字段覆盖时
// 那条路径优先；这里沿用同一默认）。
func gitBinary() string { return "git" }
