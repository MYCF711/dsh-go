package transport

// typert-loader（官方 base 插件 #10 `@deepseek-ai/dsh-typert-loader`）的**加载者**。
//
// 补这一层之前：`Registry.RegisterPackage` 存在、语义也对（整批回滚、来源标注、
// schema/name 校验），但**没有任何生产调用者**——挂载包描述的注入层等于没接。
//
// 官方原件（`@deepseek-ai/dsh-typert-loader/README.md`，逐条）：
//
//	"With dsh-typert-loader mounted, every package that mounts in a Loader
//	 composition automatically contributes its generated Typert reflection and
//	 schema factories to the runtime registry — and withdraws them when the
//	 package or the plugin unmounts. Packages without the generated export are
//	 skipped, so adding the plugin to any composition is safe."
//
//	"An explicit `packages` list covers plugins nested behind another Loader entry…"
//
//	"An explicit `packages` entry that cannot be resolved from the config tree,
//	 or that lacks the export, fails loudly and names the package."
//
// 本仓的等价形状：
//
//   - PostgreSQL 那边没有 Cordis Loader，本仓的"挂载包"是 `internal/pluginmgr`
//     里的私有包（`plugin.json` + 包目录）；
//   - 包描述的落点是 `<包目录>/typert.json`（schema `dsh.typert-package@1`，
//     与 `RegisterPackage` 认的同一份格式），也可以由宿主显式给绝对路径；
//   - **自动发现**：包目录里没有 `typert.json` 就静默跳过（官方："Packages
//     without the generated export are skipped"）；
//   - **显式清单**（`DSHD_TYPERT_PACKAGES=pkgA=/abs/typert.json,pkgB=...` ===
//     官方 config `packages`）：给错了包名/路径/文件 → **fail-loud**，并点名包；
//   - **卸载即撤**：`Registry` 之前只有"只加不改"的 `Register`，没有撤销。
//     这里补上按包撤销（`Registry.WithdrawPackage`），并在插件包卸载/禁用时调用，
//     与官方"registration follows the entry lifecycle"同义。
//
// 为什么不做"监听 Loader 条目的 cordis `internal/plugin` 事件"：那是 Cordis 的
// 事件面，本仓没有它，也不该为了这一条去造一个假的 Loader 事件源。这里的发现面
// 就是**真实的已安装包集合**（`pluginmgr.Manager.Bundles()`），有变化时重扫。

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"dshgo/internal/pluginmgr"
)

// TypertPackageFileName 是包内声明 typert 描述的文件名。
//
// 与官方 package.json 的 `exports["./typert"]` 对应：官方是 Node 模块解析面，
// 本仓是包目录+清单文件面，所以用一个固定文件而不是 package.json 字段——
// 本仓的 `plugin.json` 已经承载 `dsh.local-plugin@1`（文件哈希/入口/tools），
// 往里塞另一个包的 schema 会让两个校验器互相踩。
const TypertPackageFileName = "typert.json"

// TypertPackageSource 是一条"包名 → 描述文件"的绑定。
type TypertPackageSource struct {
	// Name 是包名（pluginmgr 的包 id）。
	Name string
	// Path 是 typert.json 的绝对路径。
	Path string
	// Explicit 表示这条来自宿主显式清单（官方 config `packages`）：
	// 显式条目**必须**成功，否则 fail-loud；自动发现来的允许没有。
	Explicit bool
}

// TypertLoadResult 是一次加载的完整结果。
type TypertLoadResult struct {
	// Loaded 是这次真的注入成功的包（名字 → 注入的方法数）。
	Loaded map[string]int
	// Skipped 是自动发现里没有 `typert.json` 的包（官方：静默跳过）。
	Skipped []string
	// Failed 是**必须报出来**的失败（显式条目解析不到 / 描述不合法）。
	Failed []string
	// Withdrawn 是这一轮被撤销的包（名字 → 撤掉的方法数）。
	Withdrawn map[string]int
}

// Err 把 Failed 折成一个错误；没有失败时返回 nil。
func (r TypertLoadResult) Err() error {
	if len(r.Failed) == 0 {
		return nil
	}
	return fmt.Errorf("typert-loader：%d 个包没有注入：%s", len(r.Failed), strings.Join(r.Failed, "；"))
}

// DiscoverTypertPackages 从**真实已安装包集合**里发现包描述。
//
// 顺序：先自动发现（包目录里的 `typert.json`），再接显式清单。同一个包名
// 出现两次（自动 + 显式）时用显式那条并如实报告冲突——两条描述指向不同文件
// 时"谁生效"不能靠 map 迭代顺序决定。
func DiscoverTypertPackages(bundles []pluginmgr.BundleState, packageDirs map[string]string, explicit []TypertPackageSource, readFile func(string) ([]byte, error)) ([]TypertPackageSource, TypertLoadResult) {
	var out TypertLoadResult
	if out.Loaded == nil {
		out.Loaded = map[string]int{}
	}
	seen := map[string]TypertPackageSource{}
	names := make([]string, 0, len(bundles))
	for _, bundle := range bundles {
		names = append(names, bundle.Name)
	}
	sort.Strings(names) // 稳定顺序：同一次装配两次跑出来的结果必须一样
	for _, name := range names {
		dir, ok := packageDirs[name]
		if !ok || dir == "" {
			out.Skipped = append(out.Skipped, name+"（没有包目录）")
			continue
		}
		path := filepath.Join(dir, TypertPackageFileName)
		if _, err := readFile(path); err != nil {
			if os.IsNotExist(err) {
				out.Skipped = append(out.Skipped, name) // 官方：没有导出就静默跳过
				continue
			}
			out.Failed = append(out.Failed, fmt.Sprintf("%s：读 %s 失败：%v", name, path, err))
			continue
		}
		seen[name] = TypertPackageSource{Name: name, Path: path}
	}
	for _, item := range explicit {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			out.Failed = append(out.Failed, fmt.Sprintf("显式条目缺包名（path=%s）", item.Path))
			continue
		}
		if strings.TrimSpace(item.Path) == "" {
			out.Failed = append(out.Failed, name+"：显式条目缺描述文件路径")
			continue
		}
		if prev, dup := seen[name]; dup && prev.Path != item.Path {
			out.Failed = append(out.Failed,
				fmt.Sprintf("%s：自动发现（%s）与显式清单（%s）指向不同文件，拒绝二选一", name, prev.Path, item.Path))
			continue
		}
		if _, err := readFile(item.Path); err != nil {
			// 官方："An explicit `packages` entry that cannot be resolved … fails loudly and names the package."
			out.Failed = append(out.Failed, fmt.Sprintf("%s：显式描述文件 %s 读不到：%v", name, item.Path, err))
			continue
		}
		seen[name] = TypertPackageSource{Name: name, Path: item.Path, Explicit: true}
	}
	keys := make([]string, 0, len(seen))
	for name := range seen {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	list := make([]TypertPackageSource, 0, len(keys))
	for _, name := range keys {
		list = append(list, seen[name])
	}
	return list, out
}

// LoadTypertPackages 把发现到的包描述**注入**注册表。
//
// 每个包一条 `RegisterPackage`（整批回滚语义在 Registry 内部）；某一条失败时
// 记录并继续下一个包——官方："a later failure is logged per package without
// preventing unrelated packages from registering"。
func (r *Registry) LoadTypertPackages(sources []TypertPackageSource, open func(string) (io.ReadCloser, error)) TypertLoadResult {
	out := TypertLoadResult{Loaded: map[string]int{}}
	for _, source := range sources {
		reader, err := open(source.Path)
		if err != nil {
			out.Failed = append(out.Failed, fmt.Sprintf("%s：打开 %s 失败：%v", source.Name, source.Path, err))
			continue
		}
		count, registerErr := r.RegisterPackage(source.Name, reader)
		_ = reader.Close()
		if registerErr != nil {
			out.Failed = append(out.Failed, fmt.Sprintf("%s：%v", source.Name, registerErr))
			continue
		}
		out.Loaded[source.Name] = count
	}
	return out
}

// WithdrawPackage 撤销一个包注入的全部描述符。
//
// 官方："registration follows the entry lifecycle: it is withdrawn when the entry
// or the plugin unmounts"。没有这个，插件卸载之后它的方法描述还在注册表里，
// 调用会被校验到，但没有任何实现——那就是"注册了但值是空"的另一种形态。
//
// 只撤 `package:<name>` 来源的描述；canonical / additive / runtime 的不动
// （它们不属于任何包）。
func (r *Registry) WithdrawPackage(name string) int {
	if strings.TrimSpace(name) == "" {
		return 0
	}
	source := "package:" + name
	r.mu.Lock()
	defer r.mu.Unlock()
	removed := 0
	for method, descriptor := range r.byMethod {
		if descriptor.Source == source {
			delete(r.byMethod, method)
			removed++
		}
	}
	return removed
}

// WithdrawPackagesExcept 撤销所有**不在 live 里**的包的描述。
//
// 返回被撤销的包与条数。这是"装载器这一轮没再看到这个包"的定义——
// 宿主调用它时 live 就是这一轮的完整发现结果。
func (r *Registry) WithdrawPackagesExcept(live map[string]struct{}) map[string]int {
	r.mu.RLock()
	present := map[string]struct{}{}
	for _, descriptor := range r.byMethod {
		name, ok := strings.CutPrefix(descriptor.Source, "package:")
		if !ok {
			continue
		}
		if _, keep := live[name]; keep {
			continue
		}
		present[name] = struct{}{}
	}
	r.mu.RUnlock()
	if len(present) == 0 {
		return nil
	}
	out := make(map[string]int, len(present))
	for name := range present {
		if removed := r.WithdrawPackage(name); removed > 0 {
			out[name] = removed
		}
	}
	return out
}

// TypertPackageDirOf 回答"这个包的描述文件在哪个目录"。
//
// 真实来源是 `pluginmgr` 的包目录约定（`<manager>/packages/<id>/<version>/`），
// 由调用方（装配点）用 Manager 的真实投影算出目录再传进来——本文件不猜路径。
func TypertPackageDirOf(managerRoot, id, version string) string {
	return filepath.Join(managerRoot, "packages", id, version)
}
