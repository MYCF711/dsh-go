package official

// 插件市场"私有 Git 插件详情"的只读仓库缓存：官方
// `dsh-plugin-market/src/repository-cache.ts` 的宿主移植。
//
// 为什么需要它（rc.2 release note「只有 SSH 凭据也能看私有 Git 插件」的落点）：
// 索引只声明每版从哪个仓库/commit 装；仓库是私有时，https 匿名读会被服务器
// **拒绝**（authentication failed），而用户可能只有 SSH 凭据。官方因此把
// remote 换成 SSH 形式再取一次（repository-cache.ts:287-303），成功后就固定
// 用 SSH 形式，后续读取不再撞 https 的拒绝。
//
// 这里只做**读**：bare 克隆（无工作树、`--filter=blob:none` 按需取 blob），
// 不执行 checkout / hook / LFS，也不碰安装事务（安装仍走 pluginmgr 的既有
// 路径；本包顶部"registry/pnpm/git 形态一律 501"说的是安装，不是详情读取）。
//
// 权威源（随 rc.2 asar 发布）：
//
//	dsh-plugin-market/src/repository-cache.ts:21   OPERATION_COMMANDS = 5
//	  —— 一次操作最多 一次 fetch + 它的 SSH 重试 + 一次会碰远端的文件读取
//	:76   sshUrlOf：ssh://git@<host><path>；非 http(s)、带凭据或 query 的 URL 返回 undefined
//	:100-120 gitEnv：去 GIT_* 覆盖、关交互提示/askpass/LFS，默认 ssh BatchMode
//	:250-280 仓储布局：`<dir>/<hash>.git` 的 bare 仓库，init 在 staging 完成后 rename
//	:287-303 fetch：https 被拒（denied）→ 改 remote.origin.url 为 SSH 形式重试一次，
//	          两次都失败时合并两条诊断，按 network/denied 归类

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// RepositoryOperationCommands 是官方 OPERATION_COMMANDS（4→5）：一次操作里
// 最多会碰远端的命令数（fetch、失败后的 SSH fetch、以及一次文件读取）。
// 保留这个常量是为了让超时预算与官方同一口径。
const RepositoryOperationCommands = 5

// 分类"这是网络/超时而不是服务器拒绝"的诊断片段（repository-cache.ts:39-41）。
var repositoryNetworkFailure = regexp.MustCompile(
	`(?i)(Could not resolve host|Failed to connect|Connection (?:refused|reset)|timed out|Network is unreachable|SSL|TLS)`)

// 仓库里没有这个路径（不是凭据/网络问题）。
var repositoryMissingPath = regexp.MustCompile(`(?i)(does not exist in|exists on disk, but not in|Path '[^']*' does not exist|not a valid object name)`)

// RepositoryFailure 是一次仓库读取的分类失败。Reason 与官方
// `DetailFailureReason` 对齐：network / denied / too-large / git-unavailable / invalid。
type RepositoryFailure struct {
	Reason  string
	Message string
	cause   error
}

func (e *RepositoryFailure) Error() string {
	return fmt.Sprintf("仓库读取失败（%s）: %s", e.Reason, e.Message)
}

func (e *RepositoryFailure) Unwrap() error { return e.cause }

// RepositoryCache 是 market 详情读取的 bare 仓库缓存。
type RepositoryCache struct {
	// Dir 是缓存根目录（每个仓库一个 `<sha256(url)>.git`）。
	Dir string
	// Timeout 是单条 git 命令的上限（官方 detailGitTimeoutMs 默认 60s）。
	Timeout time.Duration
	// MaxBytes 是一个仓库允许增长的字节上限；超过就删掉缓存并报 too-large。
	MaxBytes int64
	// MaxFileBytes 是单个详情文件的上限（官方 detailMaxBytes 默认 1 MiB）。
	MaxFileBytes int64
	// GitBinary 是 git 可执行文件（测试用假 git 注入同一条真实调用路径）。
	GitBinary string

	// locks 按仓库路径串行化"建缓存/取 commit"（官方 RepositoryCache.locks 的最小版）：
	// 同一仓库的并发详情读取不能同时 init/rename。
	mutex sync.Mutex
	locks map[string]*sync.Mutex
}

// lockFor 返回一个仓库自己的互斥量（进程内；跨进程靠 git 自己的锁）。
func (c *RepositoryCache) lockFor(repository string) *sync.Mutex {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.locks == nil {
		c.locks = map[string]*sync.Mutex{}
	}
	mutex := c.locks[repository]
	if mutex == nil {
		mutex = &sync.Mutex{}
		c.locks[repository] = mutex
	}
	return mutex
}

// NewRepositoryCache 用缓存根目录建一个仓库缓存；限额取官方默认值。
func NewRepositoryCache(dir string) *RepositoryCache {
	return &RepositoryCache{
		Dir:          dir,
		Timeout:      60 * time.Second,
		MaxBytes:     50 * 1024 * 1024,
		MaxFileBytes: 1024 * 1024,
		GitBinary:    "git",
	}
}

// SshURLOf 是官方 `sshUrlOf`：http(s) 仓库的 SSH 形式 `ssh://git@<host><path>`。
//
// 返回 false 表示这个 URL 不适合回退：其它 scheme，或者带凭据 / query——
// 那些不是"同一仓库的另一种传输"，换了就是另一个地址，不能拿来重试。
func SshURLOf(raw string) (string, bool) {
	lower := strings.ToLower(raw)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return "", false
	}
	rest := raw[strings.Index(raw, "://")+3:]
	// 手工解析（不引入 net/url 的重编码）：authority 到第一个 '/' 结束。
	slash := strings.Index(rest, "/")
	if slash <= 0 {
		return "", false
	}
	authority, path := rest[:slash], rest[slash:]
	if strings.ContainsAny(authority, "@") || strings.ContainsAny(authority, "?#") || strings.ContainsAny(path, "?#") {
		return "", false
	}
	host := authority
	// 端口不属于 SSH 形式的默认 22（官方同样只保留 hostname）。
	if colon := strings.LastIndex(host, ":"); colon > 0 && !strings.Contains(host[colon:], "]") {
		host = host[:colon]
	}
	if host == "" {
		return "", false
	}
	return "ssh://git@" + host + path, true
}

// ReadFile 读取仓库某个 commit 下的一个文件（先确保 commit 已在缓存里）。
func (c *RepositoryCache) ReadFile(ctx context.Context, url, commit, name string) ([]byte, error) {
	resolved, err := c.EnsureCommit(ctx, url, commit)
	if err != nil {
		return nil, err
	}
	repository := c.repositoryPath(url)
	if strings.HasPrefix(name, "/") || strings.Contains(name, "..") || name == "" {
		return nil, &RepositoryFailure{Reason: "invalid", Message: "仓库文件路径非法: " + name}
	}
	stdout, stderr, code, runErr := c.run(ctx, repository, "show", resolved+":"+name)
	if runErr != nil || code != 0 {
		diagnostic := strings.TrimSpace(stderr)
		if repositoryMissingPath.MatchString(diagnostic) {
			return nil, &RepositoryFailure{Reason: "invalid", Message: diagnostic, cause: runErr}
		}
		failure := repositoryFailureOf(diagnostic, url+": git show", runErr)
		// 读取文件也会碰远端（promisor 懒取 blob）：同样走"被拒 → SSH 重试一次"。
		if _, retryErr := c.retryOverSsh(ctx, repository, url, failure); retryErr != nil {
			return nil, retryErr
		}
		stdout, stderr, code, runErr = c.run(ctx, repository, "show", resolved+":"+name)
		if runErr != nil || code != 0 {
			second := repositoryFailureOf(strings.TrimSpace(stderr), url+": git show", runErr)
			reason := "denied"
			if second.Reason == "network" || failure.Reason == "network" && second.Reason != "denied" {
				reason = "network"
			}
			return nil, &RepositoryFailure{Reason: reason, Message: failure.Message + "\n" + second.Message, cause: second.cause}
		}
	}
	if int64(len(stdout)) > c.MaxFileBytes {
		return nil, &RepositoryFailure{Reason: "too-large", Message: fmt.Sprintf("%s 超过 %d 字节", url+"/"+name, c.MaxFileBytes)}
	}
	return []byte(stdout), nil
}

// EnsureCommit 保证仓库的 commit 已在本地 bare 缓存里，返回它（commit 为空时
// 用默认分支 HEAD 解析出来的 commit）。
func (c *RepositoryCache) EnsureCommit(ctx context.Context, url, commit string) (string, error) {
	if strings.TrimSpace(url) == "" {
		return "", &RepositoryFailure{Reason: "invalid", Message: "仓库 URL 为空"}
	}
	if c.Dir == "" {
		return "", &RepositoryFailure{Reason: "invalid", Message: "仓库缓存目录未配置"}
	}
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return "", &RepositoryFailure{Reason: "git-unavailable", Message: "建缓存目录失败: " + err.Error(), cause: err}
	}
	repository := c.repositoryPath(url)
	lock := c.lockFor(repository)
	lock.Lock()
	defer lock.Unlock()
	if _, err := os.Stat(repository); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return "", &RepositoryFailure{Reason: "git-unavailable", Message: "看缓存仓库失败: " + err.Error(), cause: err}
		}
		if err := c.initialize(ctx, repository, url); err != nil {
			return "", err
		}
	}
	target := strings.TrimSpace(commit)
	if target == "" {
		target = "HEAD"
	}
	if _, stderr, code, runErr := c.run(ctx, repository, "cat-file", "-e", target+"^{commit}"); runErr == nil && code == 0 {
		return target, nil
	} else if runErr != nil && errors.Is(runErr, context.DeadlineExceeded) {
		return "", &RepositoryFailure{Reason: "network", Message: "git cat-file 超时", cause: runErr}
	} else {
		_ = stderr
	}
	if err := c.fetch(ctx, repository, target); err != nil {
		return "", err
	}
	if strings.TrimSpace(commit) == "" {
		stdout, stderr, code, runErr := c.run(ctx, repository, "rev-parse", "FETCH_HEAD")
		if runErr != nil || code != 0 {
			return "", repositoryFailureOf(strings.TrimSpace(stderr), url+": git rev-parse", runErr)
		}
		return strings.TrimSpace(stdout), nil
	}
	return strings.TrimSpace(commit), nil
}

// initialize 建 bare 仓库并写好 promisor remote；先在 staging 里完成再 rename，
// 中断不会留下半个仓库（官方同做法，repository-cache.ts:250-280）。
func (c *RepositoryCache) initialize(ctx context.Context, repository, url string) error {
	staging, err := os.MkdirTemp(c.Dir, ".init-")
	if err != nil {
		return &RepositoryFailure{Reason: "git-unavailable", Message: "建 staging 目录失败: " + err.Error(), cause: err}
	}
	defer os.RemoveAll(staging)
	for _, args := range [][]string{
		{"init", "--quiet", "--bare", "--template="},
		{"config", "remote.origin.url", url},
		{"config", "remote.origin.promisor", "true"},
		{"config", "remote.origin.partialclonefilter", "blob:none"},
	} {
		_, stderr, code, runErr := c.run(ctx, staging, args...)
		if runErr != nil || code != 0 {
			return &RepositoryFailure{Reason: "git-unavailable", Message: strings.TrimSpace(stderr), cause: runErr}
		}
	}
	if err := os.Rename(staging, repository); err != nil {
		return &RepositoryFailure{Reason: "git-unavailable", Message: "落盘缓存仓库失败: " + err.Error(), cause: err}
	}
	return nil
}

// fetch 是**SSH 回退的落点**：先按 remote 现有 URL fetch；被拒（denied）且
// remote 是 http(s) 时，把 remote.origin.url 换成 SSH 形式再 fetch 一次。
// 重试成功后 remote 固定为 SSH 形式——后续读取不会再撞 https 的拒绝。
func (c *RepositoryCache) fetch(ctx context.Context, repository, target string) error {
	args := []string{"fetch", "--quiet", "--depth", "1", "--filter=blob:none", "--no-tags", "origin", target}
	remote, stderr, code, runErr := c.run(ctx, repository, "config", "remote.origin.url")
	if runErr != nil || code != 0 {
		return &RepositoryFailure{Reason: "git-unavailable", Message: strings.TrimSpace(stderr), cause: runErr}
	}
	remote = strings.TrimSpace(remote)
	before := repositorySize(repository)
	_, stderr, code, runErr = c.run(ctx, repository, args...)
	if runErr == nil && code == 0 {
		return c.checkSize(repository, remote, before)
	}
	failure := repositoryFailureOf(strings.TrimSpace(stderr), remote+": git fetch", runErr)
	if failure.Reason != "denied" {
		return failure
	}
	ssh, retryErr := c.retryOverSsh(ctx, repository, remote, failure)
	if retryErr != nil {
		return retryErr
	}
	_, stderr, code, runErr = c.run(ctx, repository, args...)
	if runErr == nil && code == 0 {
		// 成功：remote 已固定为 SSH 形式，后续读取不会再撞 https 的拒绝。
		return c.checkSize(repository, ssh, before)
	}
	second := repositoryFailureOf(strings.TrimSpace(stderr), ssh+": git fetch", runErr)
	// 官方合并两条诊断，并按"是否网络问题"归类（network 优先，否则 denied）。
	reason := "denied"
	if second.Reason == "network" || failure.Reason == "network" && second.Reason != "denied" {
		reason = "network"
	}
	return &RepositoryFailure{Reason: reason, Message: failure.Message + "\n" + second.Message, cause: second.cause}
}

// retryOverSsh 把 remote 改成 SSH 形式（如果这个 URL 有 SSH 形式），成功则返回
// SSH 地址。remote 已经就是 SSH 形式时不重复重试（官方同判）。
func (c *RepositoryCache) retryOverSsh(ctx context.Context, repository, remote string, first *RepositoryFailure) (string, error) {
	ssh, ok := SshURLOf(remote)
	if !ok {
		return "", first
	}
	if _, stderr, code, runErr := c.run(ctx, repository, "config", "remote.origin.url", ssh); runErr != nil || code != 0 {
		return "", &RepositoryFailure{Reason: "git-unavailable", Message: strings.TrimSpace(stderr), cause: runErr}
	}
	return ssh, nil
}

func (c *RepositoryCache) checkSize(repository, url string, before int64) error {
	after := repositorySize(repository)
	if c.MaxBytes > 0 && after-before > c.MaxBytes {
		_ = os.RemoveAll(repository)
		return &RepositoryFailure{
			Reason:  "too-large",
			Message: fmt.Sprintf("%s: 一次 fetch 增加 %d 字节，超过上限 %d", url, after-before, c.MaxBytes),
		}
	}
	return nil
}

func (c *RepositoryCache) repositoryPath(url string) string {
	sum := sha256.Sum256([]byte(url))
	return filepath.Join(c.Dir, hex.EncodeToString(sum[:16])+".git")
}

func (c *RepositoryCache) gitBinary() string {
	if strings.TrimSpace(c.GitBinary) == "" {
		return "git"
	}
	return c.GitBinary
}

func (c *RepositoryCache) timeout() time.Duration {
	if c.Timeout <= 0 {
		return 60 * time.Second
	}
	return c.Timeout
}

// run 跑一条 git 命令：独立超时、去交互、只回 stdout/stderr 文本（不把仓库内容
// 混进诊断日志的其它出口）。
func (c *RepositoryCache) run(ctx context.Context, dir string, args ...string) (string, string, int, error) {
	runCtx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	command := exec.CommandContext(runCtx, c.gitBinary(), args...)
	command.Dir = dir
	command.Env = repositoryGitEnv()
	var stdout, stderr strings.Builder
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			code = -1
		}
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			err = fmt.Errorf("git %s 超时（%s）: %w", strings.Join(args, " "), c.timeout(), context.DeadlineExceeded)
		}
	}
	return stdout.String(), stderr.String(), code, err
}

// repositoryGitEnv 是官方 gitEnv 的移植：去掉调用方留下的 GIT_* 覆盖，关掉
// 交互提示/askpass/LFS，默认 ssh BatchMode（用户自己的 GIT_SSH_COMMAND 优先）。
func repositoryGitEnv() []string {
	base := os.Environ()
	filtered := make([]string, 0, len(base)+8)
	for _, entry := range base {
		name := entry
		if index := strings.IndexByte(entry, '='); index >= 0 {
			name = entry[:index]
		}
		if strings.HasPrefix(name, "GIT_") && name != "GIT_SSH" && name != "GIT_SSH_COMMAND" {
			continue
		}
		filtered = append(filtered, entry)
	}
	hasSsh := false
	for _, entry := range filtered {
		if strings.HasPrefix(entry, "GIT_SSH=") || strings.HasPrefix(entry, "GIT_SSH_COMMAND=") {
			hasSsh = true
			break
		}
	}
	if !hasSsh {
		filtered = append(filtered, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
	}
	return append(filtered,
		"LC_ALL=C",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
		"SSH_ASKPASS_REQUIRE=never",
		"GCM_INTERACTIVE=never",
		"GIT_LFS_SKIP_SMUDGE=1",
		"GIT_LITERAL_PATHSPECS=1",
	)
}

// repositoryFailureOf 把一条 git 诊断归类成官方那几档。
func repositoryFailureOf(diagnostic, label string, cause error) *RepositoryFailure {
	reason := "denied"
	if repositoryNetworkFailure.MatchString(diagnostic) {
		reason = "network"
	}
	if diagnostic == "" {
		diagnostic = "git 命令失败（无诊断输出）"
	}
	return &RepositoryFailure{Reason: reason, Message: label + ": " + diagnostic, cause: cause}
}

func repositorySize(repository string) int64 {
	var total int64
	_ = filepath.WalkDir(repository, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry == nil || entry.IsDir() {
			return nil
		}
		if info, statErr := entry.Info(); statErr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}
