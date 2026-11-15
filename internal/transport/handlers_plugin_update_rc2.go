package transport

// 官方 0.2.2-rc.2 插件管理面（pluginManager.*）的**契约增量**。
//
// 为什么单独一个文件：这四项都是 rc.2 相对 rc.1 的结构性变化，集中在
// 260 行左右，混进 1300 行的 handlers_pluginmanager_official.go 会让
// "这次升级到底改了什么"不可审。
//
// 权威源（随包 0.2.2-rc.2）：
//
//	docs/upgrade-guide/v0.2.2-rc.1/plugin-update-method/guide.md
//	  - `BundleInfo.updatable: boolean` 被 `BundleInfo.update?: 'registry' | 'git'`
//	    取代：registry = 可列版本、可换新；git = 完整 commit 钉住的 git spec，
//	    可换同仓库目录的另一个 commit；**缺省 = updateBundle 换不了**。
//	  - `ManagementError['code']` 新增 `unexpected-package`：
//	    `InstallBundleOptions.expected` 与实际装出来的包/版本不符时用它
//	    （`updateBundle` 总是带 expected）。
//	dsh-plugin-manager/lib/typert.host.js
//	  - `installBundle` 的 `parameters[1].jsonSchema.properties.expected`
//	    `{name, version}` 两项 required、`additionalProperties: true`；
//	  - `updateBundle` 的 `parameters[2].jsonSchema.properties.spec`（string，
//	    "required for a bundle listed with `update: 'git'`"）。
//	dsh-plugin-manager/src/types.ts
//	  - `:39` ManagementError code 联合（含 `unexpected-package`）；
//	  - `:105-112` `BundleInfo.update` 三态；
//	  - `:218-222` `InstallBundleOptions.expected`；
//	  - `:328-338` `UpdateBundleOptions.spec`。
//
// 能力下限：`updatable` **保留**为兼容附加字段（rc.1 的读者不能因为升级就
// 读不到），并与 guide 的映射保持一致（`updatable:true` ⇔ `update:'registry'`）。

import (
	"encoding/json"
	"strings"
)

const (
	// OfficialUpdateRegistry / OfficialUpdateGit 是 `BundleInfo.update` 的两个取值。
	// 三态的第三态是**缺省**（字段不出现）：`updateBundle` 换不了这个包。
	OfficialUpdateRegistry = "registry"
	OfficialUpdateGit      = "git"

	// OfficialCodeUnexpectedPackage 是 `ManagementError.code` 在 rc.2 新增的取值：
	// 带 `expected` 的安装最终装的包名或版本不是它要求的（官方会先回滚再报它）。
	OfficialCodeUnexpectedPackage = "unexpected-package"
)

// OfficialExpectedPackage 是 `InstallBundleOptions.expected`（rc.2 新增）。
//
// 官方语义：带它的安装必须**最终**落在 `name@version` 上；否则安装被撤销
// （profile 文件与模块都还原）并回 `unexpected-package`。`updateBundle`
// 内部总是带它，这样"换新版本却装成了别的包/别的版本"不会被当成成功。
type OfficialExpectedPackage struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// OfficialBundleUpdateKind 判一个已列出的 bundle 的 `update` 三态。
//
// 分工与官方一致（`src/index.ts:185-205` 的 updateRefusal）：
//
//   - **行级门槛**在这里判（installed / official / availability / 插件管理 /
//     manifest 版本是 SemVer）——这些是行的状态，不是 spec 的形态；
//   - **spec 级判据**交给宿主：`resolve` 是宿主给的判据函数
//     （`internal/pluginmgr.UpdateKind`：完整 commit 的 git spec、自己名字下的
//     registry spec、下载缓存里的包）。传 nil 表示宿主说不出——那就**缺省**
//     这个字段，绝不猜。
//
// 返回 `""` 即"缺省"（官方：`updateBundle` 换不了）。
func OfficialBundleUpdateKind(row OfficialBundle, resolve func(source, name, version string) string) string {
	// "A third-party dependency installed in this profile": 官方 updateRefusal 的第一条。
	if !row.Installed || row.Official || row.Availability != "profile" {
		return ""
	}
	if strings.TrimSpace(row.Source) == "" {
		return ""
	}
	// 带插件管理的包连删都删不了，自然也换不了。
	if row.ReadOnlyReason != "" {
		return ""
	}
	// 官方：`declares no semver version` → 换不了。
	if !pmSemverPattern.MatchString(row.Version) {
		return ""
	}
	if resolve == nil {
		return ""
	}
	switch kind := resolve(row.Source, row.Name, row.Version); kind {
	case OfficialUpdateRegistry, OfficialUpdateGit:
		return kind
	}
	return ""
}

// applyOfficialBundleUpdateKinds 给每一行填 `update`，并把 rc.1 的兼容字段
// `updatable` 收敛到 guide 的映射上。
//
// 收敛规则：
//   - `update` 有值 → `updatable = (update == 'registry')`（guide 原文：
//     "A bundle that `updatable: true` marked before is now `update: 'registry'`"）；
//   - `update` 缺省（宿主判不出/换不了）→ **保留** provider 填的 `updatable`：
//     那是 rc.1 读者的能力声明，不能因为升级就把已知能力抹成 false。
func applyOfficialBundleUpdateKinds(bundles []OfficialBundle, resolve func(source, name, version string) string) {
	for i := range bundles {
		kind := OfficialBundleUpdateKind(bundles[i], resolve)
		if kind == "" {
			continue
		}
		bundles[i].Update = kind
		bundles[i].Updatable = kind == OfficialUpdateRegistry
	}
}

// optionalExpected 读可选的 `expected: {name, version}`。
//
// 官方 zod 对它写的是 `additionalProperties: true`（typert.host.js 的
// non-strict 参数 schema），所以多余的键**容忍**——宿主只读 name/version，
// 不因为客户端多带一个字段就把整次安装拒成 400。
func (pm *pmParams) optionalExpected(name string) (*OfficialExpectedPackage, error) {
	raw, has, err := pm.raw(name)
	if err != nil || !has {
		return nil, err
	}
	if string(bytesTrimSpace(raw)) == "null" {
		// 官方是 `.optional()`（undefined），null 不在契约里；本仓按"没给"处理，
		// 因为把 null 当 400 只会让客户端为一次空值重试整次安装。
		return nil, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, officialBadRequest("%s 必须是 {name, version} 对象", name)
	}
	expected := &OfficialExpectedPackage{}
	nameRaw, ok := fields["name"]
	if !ok {
		return nil, officialBadRequest("%s 缺 name", name)
	}
	if err := json.Unmarshal(nameRaw, &expected.Name); err != nil {
		return nil, officialBadRequest("%s.name 必须是字符串", name)
	}
	versionRaw, ok := fields["version"]
	if !ok {
		return nil, officialBadRequest("%s 缺 version", name)
	}
	if err := json.Unmarshal(versionRaw, &expected.Version); err != nil {
		return nil, officialBadRequest("%s.version 必须是字符串", name)
	}
	if strings.TrimSpace(expected.Name) == "" {
		return nil, officialBadRequest("%s.name 是空串：要么不给 expected，要么点名包", name)
	}
	if strings.TrimSpace(expected.Version) == "" {
		return nil, officialBadRequest("%s.version 是空串：要么不给 expected，要么点名版本", name)
	}
	return expected, nil
}
