package main

// typert-loader 的**生产装配点**（官方 base 插件 #10）。
//
// 这一层只做接线，不做判定：
//
//  1. 从**真实已安装包集合**（`pluginmgr.Manager.Bundles()`）算每个包的目录
//     （`<manager>/packages/<id>/<version>/`）；
//  2. 发现 `<包目录>/typert.json`（官方 `./typert` 导出在本仓的对应物）；
//  3. 支持显式清单 `DSHD_TYPERT_PACKAGES=name=/abs/typert.json[;name2=...]`
//     （官方 config `packages`），显式条目给错就 fail-loud；
//  4. 把描述注入**调用方给的**注册表（`Registry.RegisterPackage` 的整批回滚）；
//  5. 包被卸载/禁用时撤销它的描述（`Registry.WithdrawPackage`）。
//
// 为什么是"调用方给的注册表"而不是"自己去建一个"：描述注册表必须是被
// ValidateParams/Decode 用的那一份。传 nil 进来时不注入任何东西并如实回报
// 「没有目标注册表」——静默建一份孤立的注册表等于没接。

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"dshgo/internal/pluginmgr"
	"dshgo/internal/transport"
)

// typertPackageOverrides 解析 `DSHD_TYPERT_PACKAGES`。
//
// 格式：`name=/abs/typert.json` 多条用 `;` 或 `,` 分隔（Windows 路径里可能有
// 盘符冒号，所以**不用**冒号分隔）。空串 = 没有显式清单（只用自动发现）。
func typertPackageOverrides(raw string) ([]transport.TypertPackageSource, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool { return r == ';' || r == ',' })
	out := make([]transport.TypertPackageSource, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		eq := strings.Index(part, "=")
		if eq < 0 {
			return nil, fmt.Errorf("typert-loader：显式条目 %q 不是 name=path 形状", part)
		}
		name := strings.TrimSpace(part[:eq])
		path := strings.TrimSpace(part[eq+1:])
		if name == "" || path == "" {
			return nil, fmt.Errorf("typert-loader：显式条目 %q 的包名或路径为空", part)
		}
		out = append(out, transport.TypertPackageSource{Name: name, Path: path, Explicit: true})
	}
	return out, nil
}

// loadTypertPackagesInto 把一个 Manager 的已安装包描述注入 registry。
//
// 返回的 result 非空即"有包没注入"；调用方必须把它报出来（不吞）。
// 这是本函数的全部职责：发现 + 注入，不解释语义。
func loadTypertPackagesInto(registry *transport.Registry, managerRoot string, manager *pluginmgr.Manager, explicitRaw string) (transport.TypertLoadResult, error) {
	explicit, err := typertPackageOverrides(explicitRaw)
	if err != nil {
		return transport.TypertLoadResult{}, err
	}
	if registry == nil {
		return transport.TypertLoadResult{
			Loaded: map[string]int{},
			Failed: []string{"宿主没有描述符注册表（传 nil）：包描述无处可注"},
		}, nil
	}
	if manager == nil {
		// 一个包都没装也是合法状态：显式清单仍然要处理（它可能是宿主自己带的）。
		return registry.LoadTypertPackages(explicit, typertOpen), nil
	}
	bundles, err := manager.Bundles()
	if err != nil {
		return transport.TypertLoadResult{}, fmt.Errorf("typert-loader：读已安装包清单失败：%w", err)
	}
	dirs := make(map[string]string, len(bundles))
	for _, bundle := range bundles {
		dirs[bundle.Name] = transport.TypertPackageDirOf(managerRoot, bundle.Name, bundle.Version)
	}
	sources, discovered := transport.DiscoverTypertPackages(bundles, dirs, explicit, typertReadFile)
	// 先把"磁盘上已经没有描述来源"的包撤掉：官方 "registration follows the entry
	// lifecycle: it is withdrawn when the entry or the plugin unmounts"。装载器这一
	// 侧唯一知道的"卸载"就是**这一轮不再出现**，所以撤销判据就是它。
	//
	// 放在注入之前：同一个方法名"先撤后注"是正常升级路径（换版本），反过来会让
	// "方法已经有描述符了"把升级变成一句假错误。
	live := make(map[string]struct{}, len(sources))
	for _, source := range sources {
		live[source.Name] = struct{}{}
	}
	withdrawn := registry.WithdrawPackagesExcept(live)
	result := registry.LoadTypertPackages(sources, typertOpen)
	result.Withdrawn = withdrawn
	result.Skipped = append(discovered.Skipped, result.Skipped...)
	result.Failed = append(discovered.Failed, result.Failed...)
	sort.Strings(result.Skipped)
	sort.Strings(result.Failed)
	return result, nil
}

func typertReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func typertOpen(path string) (io.ReadCloser, error) { return os.Open(path) }
