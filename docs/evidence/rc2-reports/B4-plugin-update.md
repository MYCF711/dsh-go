# B4 · 插件管理面对 rc.2 的同步（`pluginManager.*`）

> 官方权威源：`tmp/GO8-RC2/official-rc2-docs/dsh/node_modules/@deepseek-ai/dsh/`（随包 `docs/`）与
> `tmp/GO8-RC2/official-rc2/dsh/node_modules/@deepseek-ai/dsh-plugin-manager/`（随包 `src/` + `lib/typert.host.js`）。
> 行号均以这两份**实际文件**为准（`grep -n` 逐条核对过，不是凭印象）。
> 宿主路径相对仓库根；门禁在 `git archive HEAD` 副本上跑（AGENTS §5），共享树只跑定向测试。
> 版本号未动（`0.2.2-rc.2-gobeta.1`）；未 commit / 未 push。

## 0. 结论摘要

1. **`BundleInfo.update` 三态已实现**：`listBundles` 每行按真实状态给出 `update: "registry" | "git"` 或**缺省**
   （官方语义：缺省 = `updateBundle` 换不了）。判据分两层：行级门槛在协议层
   （`transport.OfficialBundleUpdateKind`），spec 级判据在 `pluginmgr.UpdateKind`（官方 `install-spec.ts` 的职责）。
2. **旧 `updatable` 保留**为兼容附加字段，并按 guide 的映射收敛（`updatable:true` ⇔ `update:'registry'`）。
   Go 字段上已标 `Deprecated:`。**能力下限成立**：rc.1 读到的键一个没少。
3. **`installBundle.options.expected` 已接入**：装出来的包名/版本不符 → 真实回滚（`Manager.Uninstall`）后回
   `ManagementError.code = "unexpected-package"`；不再把"装成别的东西"报成成功。
4. **`updateBundle.options.spec` 已接入**：git 副本按官方 `planGitUpdate` 逐条校验（完整 commit、同仓库目录、
   版本必须更新），拒绝理由与官方一致（`invalid-spec`）；校验通过后需要 git 获取器 checkout —— 本仓没有
   （AGENTS R6），如实回四字段 501 并点名缺 git，**不改任何状态**。
5. **四个方法（cancelInstall / inspect / installBundle / updateBundle）逐字段核对完**：rc.2 的真实结构性增量只有
   `expected`、`spec`、`update`、`unexpected-package` 四处；其余差异是 zod 枚举**顺序**变化（无语义）与
   `sourceLocation` 行号位移。
6. 未改 `cmd/dshd/serve.go`、未改 `serve_plugins_official.go`（生产装配天然保住了新钩子，见 §6）、未改 AGENTS/CHANGELOG/docs。

## 1. 官方权威源（file:line）

| 事实 | 官方 file:line | 原文要点 |
|---|---|---|
| `updatable` 被 `update` 取代 | `dsh/docs/upgrade-guide/v0.2.2-rc.1/plugin-update-method/guide.md:11-17` | "`'registry'` for a bundle whose newer versions `listVersions` reads, `'git'` for a bundle installed from a Git spec pinned to a full commit, and absent for a bundle `updateBundle` cannot replace."；"A bundle that `updatable: true` marked before is now `update: 'registry'`." |
| `unexpected-package` 新增 | 同上 `:19-21` | "an installation run with `InstallBundleOptions.expected`, which `updateBundle` always passes, fails with it when the spec installs another package or version." |
| `BundleInfo.update` 类型 | `dsh-plugin-manager/src/types.ts:105-112`（`update?: 'registry' \| 'git'`）、`lib/typert.host.js:116` | typ 值只有两个，第三态是字段缺省 |
| `ManagementError.code` 联合 | `src/types.ts:39`（含 `unexpected-package`）、`lib/typert.host.js:57`（zod union 首处） | 结果是 `ManagementError`，不是 HTTP 错误码 |
| `update` 的判据 | `src/index.ts:500-505`（`listBundles` 用 `updateRefusal` 映射）、`src/index.ts:185-205`（`updateRefusal`） | 门槛：第三方 profile 依赖 / 无插件管理 / manifest 是 SemVer / 完整 commit 的 git spec 或"自己名字下的 registry spec" |
| `InstallBundleOptions.expected` | `src/types.ts:218-222`、`lib/typert.host.js:43-46`（zod，`additionalProperties` 见 `:757` 的 jsonSchema `true`） | `{name, version}` 两项必填 |
| `unexpected-package` 的触发与回滚 | `src/operations.ts:515`、`:520`、`:543`；描述见 `lib/typert.host.js:46` | "undone with its profile files and modules before its bundle loads, and fails with `unexpected-package`" |
| `UpdateBundleOptions.spec` | `src/types.ts:328-338`（`readonly spec?: string`，注释 `:331-335`）、`lib/typert.host.js:441`、`:634` | "required for a bundle listed with `update: 'git'`, where it must pin a full commit of the installed copy's repository directory, and refused for any other." |
| `updateBundle` 的顺序与拒绝 | `src/index.ts:738-800`（版本必须更新 → 分流）、`:809-823`（`planGitUpdate`）、`:852-...`（`checkRegistryUpdate`） | git：`invalid-spec`（不是完整 commit / 不同 origin）；registry：给了 spec 也 `invalid-spec` |
| `pinnedGitOrigin` 算法 | `src/install-spec.ts:105`（`FULL_COMMIT`）、`:108-128`（归一化） | origin = 小写 host + 仓库目录 + `#path`，忽略 transport/用户信息/`.git` |
| rc.2 描述符行号 | `lib/typert.host.js:643`（cancelInstall）、`:675`（inspect）、`:726`（installBundle）、`:776`（listBundles）、`:1168`（updateBundle） | 参数名/wire 名以这些描述符为准 |

**rc.2 相对 rc.1 的结构差异（对这四个方法，逐条核过 `tmp/GO8-RC2/contracts-rc1.json` vs `contracts-rc2.json`）**

| 方法 | rc.2 真实增量 | 其余差异 |
|---|---|---|
| `cancelInstall` | 无 | `status.enum` 顺序变（`not-running` 提到首位；`too-late` 两版都有）、`sourceLocation` 行号变 |
| `inspect` | 无 | `kind.enum`、`problem.enum` 顺序变；`sourceLocation` 行号变 |
| `installBundle` | **`options.expected {name, version}` 新增**（`additionalProperties:true`） | `sourceLocation` 行号变 |
| `updateBundle` | **`options.spec: string` 新增** | 描述文案变；`sourceLocation` 行号变 |
| `listBundles` | **结果 `BundleInfo.update`（TS 类型，结果 schema 本身是 `jsonSchemaError`，所以 JSON Schema 差异表里看不到它）** | — |

## 2. 字段映射表（官方 → 本仓）

### 2.1 `pluginManager/listBundles`（结果行）

| 官方字段 | 本仓 wire | 本仓代码 | 说明 |
|---|---|---|---|
| `update?: 'registry'\|'git'` | `update`（缺省即第三态） | `transport.OfficialBundle.Update`（`handlers_pluginmanager_official.go:680`）；填充 `applyOfficialBundleUpdateKinds`（`handlers_plugin_update_rc2.go:104`） | 生产者不填，协议层按真实行状态 + 宿主判据算 |
| `updatable: boolean`（rc.1，已废弃） | `updatable` | 同结构体（`Deprecated:` 注释） | 兼容附加字段：`update` 有值时收敛为 `update=='registry'`；`update` 缺省时原样保留生产者声明 |
| 行级门槛 | — | `OfficialBundleUpdateKind`（`handlers_plugin_update_rc2.go:70`） | `installed` / `official` / `availability=='profile'` / `source≠""` / `readOnlyReason==""` / `version` 是 SemVer，任一不满足 → 缺省 |
| spec 级判据 | — | `pluginmgr.UpdateKind`（`internal/pluginmgr/updatekind.go:110`） | 完整 commit 的 git spec → `git`；下载缓存目录 → `registry`（本仓多出的判据）；同名 registry spec → `registry`；其余 → 缺省 |

### 2.2 `pluginManager/installBundle`（参数 `[1]` = `options`）

| 官方 wire 键 | 本仓 Go | 解析 | 语义 |
|---|---|---|---|
| `spec` | `OfficialInstallBundleParams.Spec` | 既有 | — |
| `enabled` / `saveExact` / `requestId` / `approvedBuilds` / `registry` | 既有字段 | 既有 | — |
| **`expected: {name, version}`**（rc.2 新增） | `OfficialInstallBundleParams.Expected *OfficialExpectedPackage` | `pmParams.optionalExpected`（`handlers_plugin_update_rc2.go`，容忍官方 `additionalProperties:true` 的多余键；缺 name/version、空串、非对象 → 400） | `adapter.expectedMismatch`（`adapter.go:448`）在 `applied` 之后核对；不符 → `Manager.Uninstall` 回滚 → `error.code=unexpected-package`（`adapter.go:335-346`） |

### 2.3 `pluginManager/updateBundle`（参数 `[2]` = `options`）

| 官方 wire 键 | 本仓 Go | 语义 |
|---|---|---|
| `name` / `version` / `registry` / `requestId` | 既有字段 | — |
| **`spec?: string`**（rc.2 新增） | `OfficialUpdateBundleParams.Spec *string`；解析只做"非空字符串"（空串/只有空白 → 400） | 形态判据在适配器：`adapter.updateGitBundle`（`adapter.go:467`）按官方 `planGitUpdate` 逐条校验 |

### 2.4 错误码

| 官方 `ManagementError.code` | 本仓 | 位置 |
|---|---|---|
| `unexpected-package`（rc.2 新增） | `transport.OfficialCodeUnexpectedPackage = "unexpected-package"` | `handlers_plugin_update_rc2.go`；由 `adapter.installBundle` 在回滚之后写入 `OfficialChangeResult.Error.Code`（`adapter.go:341`） |
| 其余 13 个 | 既有 `OfficialChangeError.Code`（自由字符串）+ `mapChangeCode` | 未改动 |

## 3. `update` 三态的实现口径

```
行级（协议层，官方 updateRefusal 的前半）
  !installed || official || availability != "profile" || source == ""   → 缺省
  readOnlyReason != ""                                                  → 缺省（带插件管理，删都删不了）
  version 不是 SemVer                                                    → 缺省
  resolve == nil（宿主说不出判据）                                        → 缺省，绝不猜
spec 级（pluginmgr.UpdateKind，官方 install-spec.ts + updateRefusal 后半）
  PinnedGitOrigin(source) 成功                                           → "git"
  IsDownloadCachePath(source)                                            → "registry"   ← 本仓多出
  splitRegistrySpec(source) 且 name 与包名相同、range 不含 ':'           → "registry"
  其余                                                                    → 缺省
```

- `PinnedGitOrigin`（`internal/pluginmgr/updatekind.go:71`）是 `install-spec.ts:108-128` 的逐行移植：
  完整 commit = `^[0-9a-f]{40}$`；`#` 后至多一个 `path:`，其余参数必须恰好是一个完整 commit；
  origin 归一化（去掉 `git+`/协议/用户信息/`.git`、scp `host:path`→`host/path`、host 小写、剥尾斜杠）。
  **`host:8080` 的端口不能被误当 scp 冒号**——官方用 `(?!\d)`，RE2 无负向断言，改成显式判"下一字符不是数字"。
- **本仓多出的判据**：从 registry/tarball 装的包，`state.json` 记的是**下载缓存目录**（真实来源已下载），
  它仍然是 `updateBundle` 能换新的包。`IsDownloadCachePath` 用 `filepath.Rel` 判子目录，
  所以 `/tmp/cache-other` 不会被当成 `/tmp/cache` 的子目录（有专门测试钉住）。
- 判据唯一的**生产注入点**：`official.New()` 里 `BundleUpdateKind: pluginmgr.UpdateKind`（`adapter.go:79`）。

## 4. `unexpected-package` 的实现位置与回滚语义

- 契约校验点：`internal/pluginmgr/official/adapter.go:335-346`（`installBundle` 的 `applied` 分支）。
  `expected` 不符时先 `a.manager.Uninstall(outcome.Info.ID)` 把这次安装整体撤掉，再回
  `OfficialFailedChange("install", spec, "unexpected-package", diagnostic+"；已撤销这次安装")`。
- 回滚失败不吞：回 `operation-error`，diagnostic 同时写"期望 vs 实际"和回滚失败原因（两个事实都保留）。
- diagnostic 点名**期望与实际**（`期望装成 acme.audit@9.9.9，实际装成了 acme.audit@1.0.0`），
  因为官方原文是 "The output names what the run installed"。
- 真实证据：`TestInstallBundleExpectedMismatchRollsBack` 断言 `Manager.Bundle()` 事后 `installed=false`
  （真回滚，不是只改返回值），并覆盖"版本不符 / 包名不符 / 相符则 applied"三种。

## 5. 先红证据（先红后绿）

红来自**新写的测试打旧实现**（证据原件：`tmp/GO8-RC2/reports/B4-red.log`，在实现之前采集）：

```
# dshgo/internal/pluginmgr
internal\pluginmgr\updatekind_test.go:121:15: undefined: PinnedGitOrigin
internal\pluginmgr\updatekind_test.go:163:13: undefined: UpdateRegistry
internal\pluginmgr\updatekind_test.go:196:13: undefined: UpdateGit
...
--- FAIL: TestInstallBundleExpectedMismatchRollsBack
    pluginManager.installBundle 状态 400，期望 200：... 不认识的字段 "expected"（官方 strict 契约不接受 additionalProperties）
--- FAIL: TestUpdateBundleRegistryRowRefusesSpec
    pluginManager.updateBundle 状态 400：... options 里"spec"（官方 strict 契约不接受 additionalProperties）
--- FAIL: TestUpdateBundleGitRowRequiresPinnedSameOrigin
    错误码应为 "invalid-spec"，实际 not-updatable
```

> 这份红日志里还有两条**不是我这条线的**噪声，别误读：
> `FAIL dshgo/internal/transport [setup failed]` 来自并行 agent 当时未提交半成品的
> `internal/transport/handlers_workspace_files_search_rc2_test.go:29`（`dsh-go/internal/fssearch` 模块路径写错），
> 与 pluginManager 无关；
> `TestListBundlesUpdateFollowsRealSource` 的第一次红是**我测试夹具写错了**（craft 的 state 指向了
> `packages/<id>/<version>` 之外的目录，`pluginmgr.Open` 校验直接失败），修夹具后才进入真正的判据红。
> 真实的产品级红是下面这几条。

红点清单（每条对应一个 rc.2 事实）：

| 红点 | 修复后靠什么证明绿 |
|---|---|
| `updatekind_test.go` 编译不过 | `UpdateKind` / `PinnedGitOrigin` 实现（31 PASS / 0 FAIL） |
| `installBundle` 收到 `expected` 直接 400 | 参数解析 + `expectedMismatch` + 回滚（`TestInstallBundleExpectedParam` 19 PASS 组内） |
| `updateBundle` 收到 `spec` 直接 400 | `OfficialUpdateBundleParams.Spec` + `updateGitBundle` |
| git 行给 spec 被当成 `not-updatable` | 形态分流到 `updateGitBundle`，按官方判据回 `invalid-spec` |
| 本地目录包 `updatable` 恒 false、无 `update` | 三态判据 + 下载缓存判据（`TestListBundlesUpdateFollowsRealSource`） |

## 6. 接线行（生产路径，全部零新增接线）

| 位置 | 作用 |
|---|---|
| `internal/pluginmgr/official/adapter.go:79` | `BundleUpdateKind: pluginmgr.UpdateKind` —— update 判据的**唯一**生产注入点 |
| `internal/transport/handlers_pluginmanager_official.go:1303` | `normalizeOfficialBundles(bundles, api.BundleUpdateKind)` —— 每行按钩子算 `update`/`updatable` |
| `cmd/dshd/serve_plugins_official.go:36-37` | 既有 `base := officialpluginmgr.New(manager); api := base` —— **结构体整体赋值**，新钩子自动保留；随后只覆盖 `ListBundles`/`ListPlugins`/… |
| `cmd/dshd/serve.go:813` | 既有 `srv.AttachOfficialPluginManagerAPI(newOfficialPluginManagerAPI(pluginRT))` |
| `cmd/dshd/serve_plugin_loader.go` | **未改动**：钩子在 `official.New` 里，不需要 loader 层接线（这是任务书里留的"如需"分支，实际不需要） |

> 为什么不改 `serve_plugins_official.go`：它是行投影（`officialPluginBundles`），而 `update` 是**判据**不是投影。
> 判据放投影里 = 每个投影点都要复制一份门槛逻辑；放协议层 + 宿主钩子 = 只有一个判据点。

## 7. 测试与门禁

**新增测试**

| 文件 | 覆盖 | 结果 |
|---|---|---|
| `internal/pluginmgr/updatekind_test.go` | `PinnedGitOrigin` 17 例（简写/SSH/HTTPS/`path:`/重复参数/短 commit/端口…）、`UpdateKind` 三态 10 例、缓存根兄弟目录 1 例 | **31 PASS / 0 FAIL** |
| `internal/transport/handlers_plugin_update_rc2_test.go` | `update` 三态与门槛、`updatable` 兼容、`expected`（含多余键容忍与 5 类 400）、`spec`（含 4 类 400）、`unexpected-package` 透传、四方法结构对齐 | **19 PASS / 0 FAIL** |
| `internal/pluginmgr/official/update_rc2_test.go` | 真实磁盘：三种来源的 `update`、`expected` 回滚（版本/包名/相符）、registry 行拒 spec、git 行四条判据 + 契约通过后 501 且状态不变 | **4 PASS / 0 FAIL**（含多层断言） |

**回归（`git archive HEAD` + 我的 7 个文件，AGENTS §5 指定口径；原件 `tmp/GO8-RC2/reports/B4-gate.log`）**

```
HEAD = 9896f19
go vet ./internal/transport/ ./internal/pluginmgr/...            → VET_EXIT=0
go test ./internal/transport/ ./internal/pluginmgr/ ./internal/pluginmgr/official/
  ok  dshgo/internal/transport            4.897s
  ok  dshgo/internal/pluginmgr           34.831s
  ok  dshgo/internal/pluginmgr/official  29.245s   → TEST_EXIT=0
go test -run 'Plugin|Update|Expected|ListBundles' ./cmd/dshd/    → ok (28.353s)
go build ./cmd/dshd/                                             → BUILD_EXIT=0
（gofmt 复检后 transport 再跑一次：ok 5.040s）
```

**共享工作树（并行写者同树）现状——必须点名**

在同一棵工作树里跑 `go test ./internal/transport/` 目前 **FAIL 16 条**，全部是**别人在飞**的域，
与本次改动无关：

- `TestMCPManager*`（11 条，MCP manager 探针）
- `TestOfficialWebListProvidersNotWired`（1 条）等

证明方式：同一份 `internal/transport` 源码，**只叠加我改的 7 个文件**到 `git archive HEAD` 副本后
`ok`（上面那行），说明这 16 条红来自工作树里其它 agent 未提交的改动。
本次域内（pluginManager）定向跑：`64 PASS / 0 FAIL`（原件 `tmp/GO8-RC2/reports/B4-inplace-plugin.log`）。

## 8. 未实现项 / 明确边界（都点了为什么、缺什么）

1. **git 更新目标只能做到契约校验**：`options.spec` 的合法性（完整 commit、同仓库目录、版本必须更新）已按
   官方逐条实现并回 `invalid-spec`；但"checkout 那个 commit 并安装"需要 git 获取器，本仓不嵌 pnpm/git
   （AGENTS R6）。校验通过后回四字段 501，`error.code=not_implemented`，message 点名缺 git 与目标 spec，
   **不改任何状态**（测试断言版本仍是 1.0.0）。这是"能力缺失"而不是"假成功"。
2. **本地目录装的包，`update` 缺省**：官方三态只有 registry/git，没有"本地目录"。本仓仍可用本地 store 里
   保留的版本切版（`updateBundle` 既有本地路径，host 扩展），但**不在 wire 上宣称**能换新——客户端若只看
   `update`，不会看到这个 host 扩展入口。能力不减少（rc.1 的 `updatable` 本来就是 false），也不虚报。
3. **`expected` 的 `not-bundle` 分支不产生**：本仓每个可安装单元本身就是 bundle（没有官方第二层 patch），
   所以官方那条"expected 的包不声明 bundle → `not-bundle`"在本仓恒不成立，只会有 `unexpected-package`。
4. **`expected` 无法检测"新增/变更其它直接依赖"**：官方靠 pnpm 改 `package.json` 后再比对（`operations.ts`）；
   本仓安装事务只装一个包、不写 profile 的依赖表，所以没有这个可比较的集合。已在 §2.2 注明。
5. **`updateBundle` 的 HMR `restore` 分支不产生**：官方"先移除再装、失败则重装回原版本"依赖 HMR 下卸载
   语义；本仓 `Manager.Update` 是原子事务（保留 `PreviousVersion`，不需要先删），所以 `PluginUpdateResult.restore`
   恒缺省。这是既有边界（本仓不报 `restart-required`，见文件头注释）。
6. **`packageResult` / `pendingBuilds` / `approvedBuilds` / `registries` / `failedAt`** 等 pnpm 专属字段依旧不产生
   —— 本仓没有 pnpm 运行，"没有发生过一次 pnpm 运行"本来就是真话（既有约定，未改动）。

## 9. 改动文件清单（只这些）

```
 M internal/pluginmgr/official/adapter.go                        (+87)
 M internal/transport/handlers_pluginmanager_official.go          (+84/-11)
?? internal/pluginmgr/updatekind.go                              238 行（判据 + install-spec 移植）
?? internal/pluginmgr/updatekind_test.go                         247 行
?? internal/pluginmgr/official/update_rc2_test.go                372 行（真实磁盘）
?? internal/transport/handlers_plugin_update_rc2.go              156 行（rc.2 契约增量）
?? internal/transport/handlers_plugin_update_rc2_test.go         422 行
```

未触碰：`cmd/dshd/serve.go`、`cmd/dshd/serve_plugins_official.go`、`cmd/dshd/serve_plugin_loader.go`、
`internal/pluginmgr/compat.go`（工作树里那条 `M` 是别人改的版本号注释）、AGENTS.md / CHANGELOG / docs、其它域。
版本号未动（`0.2.2-rc.2-gobeta.1`）。未 commit / 未 push。
