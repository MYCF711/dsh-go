package pluginmgr

// 官方 0.2.2-rc.2 `BundleInfo.update` 与 `pinnedGitOrigin` 的判据测试。
//
// 官方权威源（随包 0.2.2-rc.2）：
//   - `docs/upgrade-guide/v0.2.2-rc.1/plugin-update-method/guide.md`：
//     `updatable` 被 `update: 'registry' | 'git'` 取代，缺省 = updateBundle 换不了；
//   - `@deepseek-ai/dsh-plugin-manager/src/types.ts:105-112`（三态语义）；
//   - `src/index.ts:185-205`（`updateRefusal`：registry / git / 拒绝的三条门槛）；
//   - `src/install-spec.ts:108-128`（`pinnedGitOrigin`：完整 commit + 至多一个
//     `path:`，origin = 小写 host + 仓库目录 + `#path`）。
//
// 这里只钉**判据**：来源字符串怎么读、三态怎么算。真实磁盘状态与 wire 形状分别
// 由 `internal/pluginmgr/official` 与 `internal/transport` 的测试覆盖。

import (
	"os"
	"path/filepath"
	"testing"
)

// sha40 是一个合法的完整 commit（官方 FULL_COMMIT = 40 位十六进制）。
const sha40 = "0123456789abcdef0123456789abcdef01234567"

func TestPinnedGitOriginFollowsOfficialAlgorithm(t *testing.T) {
	cases := []struct {
		name string
		spec string
		want string
		ok   bool
	}{
		{
			name: "github 简写 + 完整 commit",
			spec: "github:acme/widget#" + sha40,
			want: "github.com/acme/widget#",
			ok:   true,
		},
		{
			name: "git+https + .git 后缀",
			spec: "git+https://example.com/acme/widget.git#" + sha40,
			want: "example.com/acme/widget#",
			ok:   true,
		},
		{
			name: "scp 形态 git@host:org/repo.git 与 HTTPS 同一个 origin",
			spec: "git@github.com:acme/widget.git#" + sha40,
			want: "github.com/acme/widget#",
			ok:   true,
		},
		{
			name: "hosted 仓库 URL（无 git+ 前缀）",
			spec: "https://github.com/acme/widget#" + sha40,
			want: "github.com/acme/widget#",
			ok:   true,
		},
		{
			name: "path: 子目录进 origin",
			spec: "https://example.com/acme/widget#" + sha40 + "&path:plugins/x",
			want: "example.com/acme/widget#plugins/x",
			ok:   true,
		},
		{
			name: "path: 两端斜杠被剥掉",
			spec: "https://example.com/acme/widget#" + sha40 + "&path:/a/b/",
			want: "example.com/acme/widget#a/b",
			ok:   true,
		},
		{
			name: "带凭据的 HTTPS：user info 不进 origin",
			spec: "https://user:token@example.com/acme/widget#" + sha40,
			want: "example.com/acme/widget#",
			ok:   true,
		},
		{
			name: "host 大小写归一，仓库路径大小写保留（与官方一致）",
			spec: "git+https://Example.COM/Acme/Widget.git#" + sha40,
			want: "example.com/Acme/Widget#",
			ok:   true,
		},
		{
			name: "尾部斜杠剥掉",
			spec: "git+https://example.com/acme/widget/#" + sha40,
			want: "example.com/acme/widget#",
			ok:   true,
		},
		{
			name: "短 commit 不是完整 commit",
			spec: "github:acme/widget#abc1234",
		},
		{
			name: "没有 fragment",
			spec: "github:acme/widget",
		},
		{
			name: "重复的 path: 参数不指向唯一 origin",
			spec: "https://example.com/acme/widget#" + sha40 + "&path:a&path:b",
		},
		{
			name: "重复的 commit 参数不指向唯一 origin",
			spec: "https://example.com/acme/widget#" + sha40 + "&" + sha40,
		},
		{
			name: "fragment 里除 commit 还有别的参数",
			spec: "https://example.com/acme/widget#" + sha40 + "&semver:^1.0.0",
		},
		{
			name: "registry 名不是 git",
			spec: "acme.audit@" + sha40,
		},
		{
			name: "本地路径不是 git",
			spec: "./private/v1",
		},
		{
			name: "HTTPS 但只一层路径：不是 hosted 仓库 URL",
			spec: "https://example.com/widget#" + sha40,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := PinnedGitOrigin(tc.spec)
			if ok != tc.ok {
				t.Fatalf("PinnedGitOrigin(%q) ok=%v，期望 %v（origin=%q）", tc.spec, ok, tc.ok, got)
			}
			if ok && got != tc.want {
				t.Fatalf("PinnedGitOrigin(%q) = %q，期望 %q", tc.spec, got, tc.want)
			}
		})
	}
}

// TestPinnedGitOriginEqualsAcrossTransports 钉住官方的那条判据：
// "Equal origins name one repository on a host that serves it at the same path
// over every transport"（install-spec.ts:104-107）。
func TestPinnedGitOriginEqualsAcrossTransports(t *testing.T) {
	https, ok1 := PinnedGitOrigin("git+https://github.com/acme/widget.git#" + sha40)
	ssh, ok2 := PinnedGitOrigin("git@github.com:acme/widget.git#" + sha40)
	short, ok3 := PinnedGitOrigin("github:acme/widget#" + sha40)
	if !ok1 || !ok2 || !ok3 {
		t.Fatalf("三种形态都应是完整 commit 钉住的 git spec：%v %v %v", ok1, ok2, ok3)
	}
	if https != ssh || https != short {
		t.Fatalf("同一仓库不同 transport 的 origin 应相等：%q / %q / %q", https, ssh, short)
	}
}

func TestUpdateKindThreeStates(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("DSHD_PLUGIN_CACHE", cache)

	cases := []struct {
		name    string
		source  string
		bundle  string
		version string
		want    string
	}{
		{
			name:    "registry spec（自己的名字）→ registry",
			source:  "acme.audit@1.0.0",
			bundle:  "acme.audit",
			version: "1.0.0",
			want:    UpdateRegistry,
		},
		{
			name:    "registry spec（scoped 名字）→ registry",
			source:  "@acme/audit@^1.2.0",
			bundle:  "@acme/audit",
			version: "1.2.3",
			want:    UpdateRegistry,
		},
		{
			name:    "registry 名不带范围也算 registry spec",
			source:  "acme.audit",
			bundle:  "acme.audit",
			version: "1.0.0",
			want:    UpdateRegistry,
		},
		{
			name:    "换名的 alias（npm:）与官方 same-name 判据不符 → 换不了",
			source:  "npm:other-package@1.0.0",
			bundle:  "acme.audit",
			version: "1.0.0",
		},
		{
			name:    "registry spec 装到了别的名字下 → 换不了",
			source:  "other-package@1.0.0",
			bundle:  "acme.audit",
			version: "1.0.0",
		},
		{
			name:    "完整 commit 钉住的 git spec → git",
			source:  "github:acme/widget#" + sha40,
			bundle:  "acme.audit",
			version: "1.0.0",
			want:    UpdateGit,
		},
		{
			name:    "下载缓存目录里的包（registry/tarball 装的）→ registry",
			source:  filepath.Join(cache, "acme.audit", "1.0.0"),
			bundle:  "acme.audit",
			version: "1.0.0",
			want:    UpdateRegistry,
		},
		{
			name:    "本地目录装的包 → 官方三态里没有对应取值（缺省=换不了）",
			source:  filepath.Join(t.TempDir(), "private", "v1"),
			bundle:  "acme.audit",
			version: "1.0.0",
		},
		{
			name:    "没有来源 → 换不了",
			source:  "",
			bundle:  "acme.audit",
			version: "1.0.0",
		},
		{
			name:    "manifest 版本不是 SemVer → 换不了（官方 updateRefusal 同判）",
			source:  "acme.audit@1.0.0",
			bundle:  "acme.audit",
			version: "not-a-version",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := UpdateKind(tc.source, tc.bundle, tc.version); got != tc.want {
				t.Fatalf("UpdateKind(%q, %q, %q) = %q，期望 %q",
					tc.source, tc.bundle, tc.version, got, tc.want)
			}
		})
	}
}

// TestUpdateKindDownloadCacheIsNotAnyLocalDirectory 钉住"缓存根之下的才叫下载缓存"：
// 兄弟目录（前缀相同的另一个目录）不能被误判成缓存。
func TestUpdateKindDownloadCacheIsNotAnyLocalDirectory(t *testing.T) {
	base := t.TempDir()
	cache := filepath.Join(base, "cache")
	sibling := filepath.Join(base, "cache-other", "pkg")
	t.Setenv("DSHD_PLUGIN_CACHE", cache)
	if err := os.MkdirAll(sibling, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := UpdateKind(sibling, "acme.audit", "1.0.0"); got != "" {
		t.Fatalf("前缀相同的兄弟目录被误判成下载缓存：%q", got)
	}
}
