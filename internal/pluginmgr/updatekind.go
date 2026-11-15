package pluginmgr

// 官方 0.2.2-rc.2 `BundleInfo.update` 的判据与 `pinnedGitOrigin` 的移植。
//
// 为什么这两件事在 pluginmgr 而不是 transport：它们读的是**安装 spec**
// （registry 名 / git 地址 / 本地路径）的形态，也就是官方 `install-spec.ts`
// 的职责；本仓同一职责已经在 bundles.go 的 `SourceKind/SourceIsRegistrySpec`
// 里。transport 只做协议翻译，不复制一份容易漂移的解析器。
//
// 权威源（随包 0.2.2-rc.2）：
//   - guide `docs/upgrade-guide/v0.2.2-rc.1/plugin-update-method/guide.md`：
//     `updatable` → `update: 'registry' | 'git'`，缺省 = `updateBundle` 换不了；
//   - `src/index.ts:185-205`：`updateRefusal` 的门槛（第三方 profile 依赖、
//     没有插件管理、manifest 版本是 SemVer、来源是完整 commit 钉住的 git spec
//     或"自己名字下的 registry spec"）；
//   - `src/install-spec.ts:108-128`：`pinnedGitOrigin` 的完整算法
//     （完整 commit、至多一个 `path:`、origin 归一化）；
//   - `src/install-spec.ts:16-38`：`ParsedInstallSpec` 的形态判据。

import (
	"path/filepath"
	"regexp"
	"strings"
)

// `BundleInfo.update` 的两个取值（官方 types.ts:112：`'registry' | 'git'`）。
const (
	// UpdateRegistry 表示这个包从 registry 装成、且以自己名字保存：
	// `listVersions` 能列版本，`updateBundle` 能换到 `name@version`。
	UpdateRegistry = "registry"
	// UpdateGit 表示这个包从**完整 commit 钉住**的 git spec 装成：
	// `updateBundle` 能换到同一仓库目录的另一个 commit（`options.spec`）。
	UpdateGit = "git"
)

var (
	// 官方 install-spec.ts:20-22：pnpm 走 git 的三种形态。
	gitShorthandPattern = regexp.MustCompile(`^(?i:github|gitlab|bitbucket|gist):`)
	gitURLPattern       = regexp.MustCompile(`^(?i:git(?:\+[a-z]+)?://|git@[^:]+:)`)
	hostedRepoPattern   = regexp.MustCompile(`^(?i:https?://[^/]+/[^/]+/[^/#]+(?:\.git)?(?:#.*)?$)`)
	// 官方 install-spec.ts:29：tarball 不算 git（哪怕地址里带 .git 片段）。
	tarballSpecPattern = regexp.MustCompile(`(?i)\.(?:tgz|tar\.gz)(?:#.*)?$`)
	// 官方 install-spec.ts:31-32：registry 能接受的包名（PACKAGE_NAME）。
	// 与 exemption.go 的同名判据差别：官方这里多接受 `~`，所以单独一份。
	registrySpecNamePattern = regexp.MustCompile(`^(?:@[a-z0-9][a-z0-9._~-]*/)?[a-z0-9][a-z0-9._~-]*$`)
	// 官方 install-spec.ts:105：`#` 后作为 commit 参数的必须是 40 位十六进制。
	fullCommitPattern = regexp.MustCompile(`^(?i:[0-9a-f]{40})$`)
	// 官方 install-spec.ts:121-126 的 origin 归一化各步。
	gitPlusPrefixPattern  = regexp.MustCompile(`^(?i:git\+)`)
	protocolPrefixPattern = regexp.MustCompile(`^[a-z][a-z0-9+.-]*://`)
	userInfoPrefixPattern = regexp.MustCompile(`^[^@/]+@`)
	gitSuffixPattern      = regexp.MustCompile(`(?i)\.git$`)
	repositoryPathPattern = regexp.MustCompile(`^([^/]+)(.*)$`)
)

// 官方 install-spec.ts:23-26：pnpm 简写对应的主机。
var gitShorthandHosts = map[string]string{
	"github": "github.com", "gitlab": "gitlab.com",
	"bitbucket": "bitbucket.org", "gist": "gist.github.com",
}

// PinnedGitOrigin 返回"完整 commit 钉住的 git spec"装的是哪儿：
// 小写 host + 仓库目录（去掉协议/用户/`.git`）+ `#` + `path:` 子目录。
//
// 官方语义（install-spec.ts:97-107）：origin 忽略 pnpm 保存的形态，
// 所以同一个仓库的 HTTPS 形态、SSH 形态与主机简写形态 origin 相同——
// `updateBundle` 靠这个判"是不是同一个仓库目录的新 commit"。
//
// 第二个返回值是 false 表示：不是 git spec，或 `#` 后不是一个完整 commit
// 加至多一个 `path:`（这种 spec 不能用来定义"同一仓库"）。
func PinnedGitOrigin(raw string) (string, bool) {
	spec, ok := gitSpecOf(raw)
	if !ok {
		return "", false
	}
	hash := strings.Index(spec, "#")
	if hash < 0 {
		return "", false
	}
	parameters := strings.Split(spec[hash+1:], "&")
	var paths, others []string
	for _, parameter := range parameters {
		if strings.HasPrefix(parameter, "path:") {
			paths = append(paths, parameter)
		} else {
			others = append(others, parameter)
		}
	}
	// pnpm 读重复参数里**最后一个**，所以重复即"没有一个唯一 origin"。
	if len(paths) > 1 || len(others) != 1 || !fullCommitPattern.MatchString(others[0]) {
		return "", false
	}
	path := ""
	if len(paths) == 1 {
		path = strings.Trim(paths[0][len("path:"):], "/")
	}
	return gitOriginLocation(spec[:hash]) + "#" + path, true
}

// UpdateKind 返回官方 `BundleInfo.update` 的三态之一（`""` = 换不了）。
//
// 入参是 Manager 落盘的真实三元组：来源 spec、包名、manifest 版本。
// 官方 `updateRefusal` 里能只靠这三样判的部分都在这里；其余门槛
// （installed / official / availability / readOnlyReason）在协议层由
// `transport.OfficialBundleUpdateKind` 判——那些是行的状态，不是 spec 的形态。
//
// 本仓多出一条官方没有的判据：从 registry/tarball 装的包，Manager 落盘的是
// **下载缓存目录**（真实来源已下载），它仍然是 registry 可更新的包，
// 所以 `UpdateKind` 认缓存根（`CacheDir()`）并回 `registry`。
func UpdateKind(source, name, version string) string {
	if _, err := parseSemver(version); err != nil {
		return ""
	}
	source = strings.TrimSpace(source)
	if source == "" {
		return ""
	}
	if _, ok := PinnedGitOrigin(source); ok {
		return UpdateGit
	}
	if IsDownloadCachePath(source) {
		return UpdateRegistry
	}
	specName, specRange, ok := splitRegistrySpec(source)
	if !ok || specName != name {
		return ""
	}
	// 官方同判：范围里带协议（如 `npm:` 别名）说明装的不是同名包。
	if strings.Contains(specRange, ":") {
		return ""
	}
	return UpdateRegistry
}

// IsDownloadCachePath 判断一个已落盘来源是不是本进程的下载缓存目录之下。
//
// 用 `filepath.Rel` 而不是字符串前缀：`/tmp/cache-other` 不是 `/tmp/cache` 的子目录，
// 否则"前缀相同的兄弟目录"会被误判成 registry 装的包。
func IsDownloadCachePath(source string) bool {
	cache := strings.TrimSpace(CacheDir())
	if cache == "" || strings.TrimSpace(source) == "" {
		return false
	}
	relative, err := filepath.Rel(filepath.Clean(cache), filepath.Clean(source))
	if err != nil {
		return false
	}
	if relative == "." || relative == ".." || filepath.IsAbs(relative) {
		return false
	}
	return !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// gitSpecOf 复刻官方 `parseInstallSpec` 的前半段：把 raw 归到四种形态之一，
// 并只在它是 git spec 时返回 trim 之后的 spec。
func gitSpecOf(raw string) (string, bool) {
	spec := strings.TrimSpace(raw)
	if spec == "" {
		return "", false
	}
	// file:/link: 前缀与绝对路径都是本地路径（官方先判路径，再判 git）。
	if strings.HasPrefix(spec, "file:") || strings.HasPrefix(spec, "link:") {
		return "", false
	}
	if isAbsoluteSpecPath(spec) || isRelativeSpecPath(spec) {
		return "", false
	}
	git := gitShorthandPattern.MatchString(spec) || gitURLPattern.MatchString(spec) ||
		hostedRepoPattern.MatchString(spec)
	if !git || tarballSpecPattern.MatchString(spec) {
		return "", false
	}
	return spec, true
}

// isAbsoluteSpecPath 对齐官方 `isAbsolute`（node:path）在两种平台上的判据。
func isAbsoluteSpecPath(spec string) bool {
	if strings.HasPrefix(spec, "/") || strings.HasPrefix(spec, `\`) {
		return true
	}
	return len(spec) >= 3 && spec[1] == ':' && (spec[2] == '/' || spec[2] == '\\')
}

// isRelativeSpecPath 对齐官方 install-spec.ts:78 的 `^\.{1,2}(?:[\\/]|$)`：
// 相对路径在官方是**拒绝**（不是 path 形态），所以也不能当 git spec。
func isRelativeSpecPath(spec string) bool {
	if spec == "." || spec == ".." {
		return true
	}
	return strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, ".\\") ||
		strings.HasPrefix(spec, "../") || strings.HasPrefix(spec, "..\\")
}

// splitRegistrySpec 复刻官方 `parseInstallSpec` 的 registry 分支：
// `name` 或 `name@range`，名字必须是 registry 接受的字面量。
func splitRegistrySpec(spec string) (name, versionRange string, ok bool) {
	at := strings.Index(spec[1:], "@")
	if at < 0 {
		name = spec
	} else {
		at++ // 换算成 spec 的下标；官方 indexOf('@', 1) 同理
		name, versionRange = spec[:at], spec[at+1:]
		if versionRange == "" {
			return "", "", false
		}
	}
	if len(name) > 214 || !registrySpecNamePattern.MatchString(name) {
		return "", "", false
	}
	return name, versionRange, true
}

// gitOriginLocation 复刻官方 `pinnedGitOrigin` 里对 `#` 之前部分的归一化：
// 去掉协议、用户信息与 `.git`，把 scp 形态的 `host:path` 变成 `host/path`，
// 小写化 host，剥掉尾部斜杠。
func gitOriginLocation(location string) string {
	if matches := gitShorthandPattern.FindStringSubmatch(location); matches != nil {
		location = gitShorthandHosts[strings.ToLower(matches[0][:len(matches[0])-1])] + "/" +
			location[len(matches[0]):]
	}
	location = gitPlusPrefixPattern.ReplaceAllString(location, "")
	location = protocolPrefixPattern.ReplaceAllString(location, "")
	location = userInfoPrefixPattern.ReplaceAllString(location, "")
	// `host:path`（scp 形态）里的冒号变斜杠，但 `host:8080` 的端口不能动。
	// 官方用 `^([^/:]+):(?!\d)`；RE2 没有负向断言，这里显式判"下一字符不是数字"。
	if colon := strings.Index(location, ":"); colon > 0 && !strings.ContainsAny(location[:colon], "/") {
		rest := location[colon+1:]
		if rest == "" || rest[0] < '0' || rest[0] > '9' {
			location = location[:colon] + "/" + rest
		}
	}
	location = strings.TrimRight(location, "/")
	location = gitSuffixPattern.ReplaceAllString(location, "")
	if matches := repositoryPathPattern.FindStringSubmatch(location); matches != nil {
		location = strings.ToLower(matches[1]) + matches[2]
	}
	return location
}
