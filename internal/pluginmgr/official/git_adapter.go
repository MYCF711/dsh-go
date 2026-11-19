package official

// git 形态的落地：把 spec 指向的提交取成本地目录，再交给 `pluginmgr` 的既有事务。
//
// 与 `registry_adapter.go` 的分工是**故意对称**的：
//   - registry/tarball 用真实的 HTTP 下载 + 解包，落到 `pluginmgr.CacheDir()`；
//   - git 用真实的 `git fetch` + checkout，落到同一个缓存根。
// 两者产出的都是"一个含 plugin.json 的本地目录"，之后走同一条 Manager 事务，
// 所以三个形态的**返回契约**是同一份（见 git_adapter_test.go 的对齐断言）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dshgo/internal/pluginmgr"
	"dshgo/internal/transport"
)

// materializeGitSpec 把 git 规格取成一个本地目录并返回它。
//
// 目录布局与 registry/tarball 同一套缓存根，但**子目录必须分开**：
// `git/<spec 的 sha256 前缀>/`。
//
// 复用判据只有一种：spec **钉住了完整 commit**（内容是确定的）且目标目录里
// 已经有 `plugin.json`。没钉 commit 的 spec（`github:o/r` 这种取默认分支的）
// 每次都要重新取——复用旧 checkout 会把用户"再装一次"变成装回旧提交。
func (a *adapter) materializeGitSpec(spec string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), GitFetchTimeout+time.Minute)
	defer cancel()
	location, ok := SplitGitSpec(spec)
	if !ok {
		return "", &GitFetchFailure{Reason: "invalid", Message: "不是 git spec: " + strings.TrimSpace(spec)}
	}
	cache := strings.TrimSpace(pluginmgr.CacheDir())
	if cache == "" {
		return "", &GitFetchFailure{Reason: "invalid", Message: "插件缓存目录未配置（DSHD_PLUGIN_CACHE）"}
	}
	sum := sha256.Sum256([]byte(spec))
	dst := filepath.Join(cache, "git", hex.EncodeToString(sum[:16]))
	if _, err := os.Stat(dst); err == nil {
		if location.Commit != "" {
			if _, err := os.Stat(filepath.Join(dst, "plugin.json")); err == nil {
				return dst, nil
			}
		}
		// 上一次取的内容不可复用：这个目录是我们自己的缓存分片，删掉重取。
		if err := os.RemoveAll(dst); err != nil {
			return "", &GitFetchFailure{Reason: "git-unavailable", Message: "清旧缓存失败: " + err.Error(), cause: err}
		}
	}
	if _, err := FetchGitPackage(ctx, spec, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// gitRefusalCode 把一次 git 取包失败翻成官方 `ManagementError.code`。
//
// 官方对 git 形态的失败码只有两类用法（`src/operations.ts` 的 install 失败分类）：
//   - 这台机器现在取不到（network/denied/超时/没有 git）→ 不是"你写错了"，
//     用 `operation-error`（官方同样只在 `invalid` 时回 invalid-spec）；
//   - spec 指向的东西不存在（invalid）→ `invalid-spec`。
func gitRefusalCode(err error) string {
	var failure *GitFetchFailure
	if !errors.As(err, &failure) {
		return "operation-error"
	}
	if failure.Reason == "invalid" {
		return "invalid-spec"
	}
	return "operation-error"
}

// gitChangeRefused 造 installBundle 的 git 失败结果（application=failed）。
func gitChangeRefused(spec string, err error) transport.OfficialChangeResult {
	return transport.OfficialFailedChange("install", spec, gitRefusalCode(err), err.Error())
}
