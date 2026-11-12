# RC2-C1 · `cuaAccess` 9 方法（电脑操作面）实现报告

> 任务：`task-11`（RC2-C1）。官方基线 **0.2.2-rc.2**。
> 写作用域：`internal/cua/**`（新建）+ `internal/transport/handlers_cua_rc2.go(_test.go)`。
> 未改 `cmd/dshd/serve.go`、未改 `internal/transport/server.go`、未改 docs/AGENTS/CHANGELOG、未 commit。
> 证据：`tmp/GO8-RC2/reports/C1-red.log`（先红）、`C1-green.log`（后绿，43 PASS / 0 FAIL）。

---

## 0. 一句话结论

| 口径 | 结果 |
|---|---|
| 官方 9 方法 | **Windows 构建：9/9 真实现**（7 项完全真做，2 项「有条件真做 + 缺条件时 501+capableWhen」），**0 项无条件 501 占位** |
| 非 Windows 构建 | 3 项结构化未实现（`systemStatus` / `openSettings` / `requestPermission`）+ 1 项 additive 枚举未实现，全部带 `capableWhen`；`applications`/`applicationIcons`/`ptcConfiguration`/`revoke`/`retryApproval` 仍为真实现 |
| 额外真能力 | additive 方法 `cuaAccess.runningApplications`：Win32 `EnumWindows` 真实枚举（本机实测 **14 条**，含真实可执行路径） |
| 假数据 | **0**（没有 provider 就报 `providerAvailable:false`；没有条目就报 `writable:false`；取不到图标就不出现） |

**必须先说清的一条官方事实**：官方 rc.2 的 `cuaAccess` 服务由 provider 调 `installCua()` 才创建
（`dsh-cua/src/index.ts:153-177`），而 `dsh-cua` 插件的 `apply()` 是空函数（`index.ts:415`）；
rc.2 的 bundle 只挂 macOS provider（`dsh-cua-bundle/cordis.patch.yml:5-8`
`disabled: !!js process.platform !== 'darwin'`），且 `@deepseek-ai/dsh-experimental-cua-macos`
这个包在 Windows 安装里**根本不存在**（`ls node_modules/@deepseek-ai | grep cua` → 只有
`dsh-cua` / `dsh-cua-bundle` / `dsh-computer-use`）。
**所以官方 Windows 宿主上这 9 个方法一个都不存在**——本仓是「比官方多」，不是「对齐一个已存在的东西」。

---

## 1. 官方证据（file:line）

| 用途 | 位置 |
|---|---|
| 9 方法契约（逐字段 jsonSchema + 描述） | `docs/evidence/official-method-contract-delta-0.2.2-rc.2.md:311-814` |
| 9 方法的唯一实现类 `CuaAccess` | `dsh-cua/src/access.ts:39-142`（`ptcConfiguration:54-61`、`enablePersistentPtc:66-73`、`applications:79-80`、`systemStatus:86-89`、`applicationIcons:95-101`、`requestPermission:109-113`、`openSettings:121-125`、`revoke:132-133`、`retryApproval:140-141`） |
| win32 / darwin 结果分支（discriminatedUnion） | `dsh-cua/src/access.ts:22-29`；类型定义 `dsh-cua/src/access-types.ts:16-37` |
| 应用决策状态机（投影/撤销/重试） | `dsh-cua/src/application-access.ts:65-81`（status）、`:88-108`（revoke）、`:114-123`（retryApproval） |
| 决策持久化（domain storage + 文件锁） | `dsh-cua/src/permission-store.ts:47-129`（`withFileLock` 在 `:115`、`persistent` 判据 `:62`、状态 schema `:23-32`） |
| 应用身份/授权形状 | `dsh-cua/src/application-types.ts:9-16`、`application-access.ts:14-17`（64 位小写十六进制正则） |
| 图标 data URL 形状与上限 | `dsh-cua/src/protocol.ts:178`（`data:image/png;base64,…`，`max(65536)`） |
| 枚举运行中应用属于 provider 协议（不在方法面） | `dsh-cua/src/protocol.ts:52-53`（`listApps`/`findApps`） |
| provider 装配点 / 空 `apply()` | `dsh-cua/src/index.ts:153-177`、`:415` |
| Windows 无 provider | `dsh-cua-bundle/cordis.patch.yml:5-8`；node_modules 缺包（本节开头） |
| 迁移指南（`capabilities` 取代 `description`、平台能力由 provider 提供） | `…/docs/upgrade-guide/v0.2.2-rc.1/cua-native-platform/guide.md` |

---

## 2. 9 方法契约表与逐条判定

`X` = 本次实现的真实程度；「路径」列是线上 `POST /api/<method>` 的落地代码。

| # | 方法（wire） | 官方参数 | 官方 result | 判定 | 路径 / 关键点 |
|---|---|---|---|---|---|
| 1 | `cuaAccess.systemStatus` | 无 | `darwin{accessibility,screenRecording,helper,parent,launchApplication?}` \| `win32{sessionLocked,captureAvailable,elevated,helper}` | **真实现（Windows）**；非 Windows **结构化未实现** | `internal/cua/platform_windows.go:26-58`（`NewSystemProber:23`）：`OpenInputDesktop` 判锁屏、1×1 `BitBlt` 实测抓屏、`TokenElevation` 判提权、Toolhelp 取父进程；`providerAvailable:false` + `capableWhen` |
| 2 | `cuaAccess.applications` | 无 | `{allowedApps[],blockedApps[],persistentAvailable}` | **真实现** | `internal/cua/store.go:68-109`（投影规则照 `application-access.ts:69-81`：有 `always` → allowed；无 `always` 且任一会话 deny → blocked） |
| 3 | `cuaAccess.applicationIcons` | 无 | `{可执行路径: data:image/png;base64,…}` | **真实现（Windows）**；非 Windows 返回契约允许的空对象 | `internal/cua/icons_windows.go:36-158`（`iconDataURL:52`、`drawIconRGBA:88`）：`SHGetFileInfoW(SHGFI_ICON\|SHGFI_LARGEICON)` → 64×64 32bpp DIBSection → `DrawIconEx` → 反预乘 → PNG → data URL（实测 notepad.exe = **4006 字符**） |
| 4 | `cuaAccess.openSettings` | `permission: accessibility\|screenRecording`, `locale: string` | void | **真实现（Windows）**；非 Windows **结构化未实现** | `internal/cua/settings_windows.go:32-45`（映射）、`:62-85`（先验 `HKCR\ms-settings\shell\open\command` 注册，再 `ShellExecuteW`，返回值 ≤32 判失败） |
| 5 | `cuaAccess.ptcConfiguration` | 无 | `{persistent,writable}` | **真实现（真话）** | `internal/cua/ptc.go:66-82`：按官方 `ptc-runtime` 条目（`access.ts:59,70,72`）读真实 profile；本宿主未装配该模块 → `false/false` + `entryFound:false` + `capableWhen` |
| 6 | `cuaAccess.enablePersistentPtc` | 无 | void | **有条件真实现** | `internal/cua/ptc.go:85-102`：已持久化 → no-op（官方 `access.ts:68` 同义）；有条目且可写 → 真写配置；都没有 → **501 + capableWhen**（不谎称已持久化） |
| 7 | `cuaAccess.requestPermission` | `permission`, `locale` | darwin\|win32 权限观测 | **有条件真实现** | `internal/cua/service.go:231-249`：返回**真实再观测**的 win32 分支，并显式 `requestedPermission` / `consentEstablished:false` / `providerAvailable:false`。官方语义本身就是「requesting does not establish consent」；本平台没有 provider、没有同意 UI，**不谎称弹过授权框** |
| 8 | `cuaAccess.retryApproval` | `id`（64hex） | void | **真实现** | `internal/cua/store.go:145-174`：deny → ask + revision+1，**不授予、不回放**；没有 deny 可清时纯 no-op（照 `application-access.ts:116-122`，不推 revision） |
| 9 | `cuaAccess.revoke` | `id`（64hex） | void | **真实现** | `internal/cua/store.go:111-143`：`revoked+1`、全会话改 deny + revision+1、原子落盘；「有持久许可但不能持久化」→ 409（官方同规则） |

**计数**：Windows 构建 **9 真实现 / 0 结构化未实现**（其中 #6、#7 是「有条件真做」，条件不满足时给 501/如实标注，而不是假成功）；
非 Windows 构建 **3 结构化未实现**（#1、#4、#7）+ additive 枚举。

### 2.1 与任务书措辞的一处偏差（必须显式说明）

任务书写的是「`applications`（枚举运行中应用）」。**官方语义不是这样**：
`applications` 读的是**已保存的应用决策**（`access.ts:75-80` → `application-access.ts:65-81`），
描述里明确写着「Read fresh stored application decisions **without issuing a native request**」；
枚举运行中应用属于 provider 的 native 协议（`protocol.ts:52-53` 的 `listApps`/`findApps`），
**不在 9 方法面内**。

所以本仓的处理是：**契约照官方实现**（`applications` = 决策投影），
**把 Win32 枚举做成 additive 方法** `cuaAccess.runningApplications`（真实现，本机实测 14 条，
含 `explorer.exe` / `magpie.exe` / `Notepad.exe` 等真实可执行路径）。
这样既不伪造官方语义，也不丢掉「EnumWindows 真做」这项要求。

---

## 3. Win32 能力边界（能做 / 不能做，逐条给证据）

### 真做（都在本机跑过）

| 能力 | 实现 | 实测证据 |
|---|---|---|
| 会话锁屏判定 | `OpenInputDesktop(DESKTOP_READOBJECTS)`：打开成功=未锁；`ERROR_ACCESS_DENIED`=锁屏；其它=nil（**不知道**，不写 false） | `{"sessionLocked": false}` |
| 抓屏可用性 | 屏幕 DC + 1×1 兼容位图 + `BitBlt(SRCCOPY)` 真拷一个像素 | `{"captureAvailable": true}` |
| 提权判定 | `OpenProcessToken` + `GetTokenInformation(TokenElevation)` | `{"elevated": true}`（测试进程为提权会话） |
| 进程元数据 | `QueryFullProcessImageName` + `CreateToolhelp32Snapshot` 取父进程 | `helper{pid:17304,…cua.test.exe}`、`parent{pid:3188,…go.exe}` |
| 图标提取 | `SHGetFileInfoW` + DIB + `DrawIconEx` + `image/png` | notepad.exe → `data:image/png;base64,…` **4006 字符**（≤65536） |
| 应用枚举 | `EnumWindows` + `IsWindowVisible` + `GetWindowTextW` + 进程路径 + `sha256(小写规范路径)` 身份 | 本机 **14** 条（详见 §5 样本） |
| 设置页打开 | 注册表校验 `ms-settings` 协议 → `ShellExecuteW("open", uri)` | URI 映射与协议注册由 `TestWin32SettingsURIsAreRegistered` 钉住（**测试不弹窗**，避免打扰用户） |
| 跨进程决策锁 | `LockFileEx` 1 字节排它锁 + 原子 rename | `TestStoreConcurrentRevokesSerialise`（8 并发撤销 → `revoked=8`，不丢更新） |

### 不做（且**不是**「偷懒」，是平台/官方事实）

| 能力 | 为什么不做 | 如何对外表达 |
|---|---|---|
| AX / 无障碍树读取 | 依赖 `dsh-cua` 原生 provider（官方 Windows 无） | `systemStatus.providerAvailable=false` + `capableWhen`；AX 读树本身不在 9 方法面内 |
| 截图 / 鼠标 / 键盘注入 | 同上（`native-build.js` 的 helper 是平台原生二进制，Windows 不提供） | 同上；本包不注册任何这类方法 |
| `accessibility` / `screenRecording` 布尔 | win32 分支**根本没有**这两个字段（无 TCC） | `platformNote` 说清「win32 没有 TCC，权限面就是 sessionLocked/captureAvailable/elevated」 |
| 原生同意对话框 | 没有 provider 就没有同意 UI | `requestPermission` 显式 `consentEstablished:false`，**不谎称问过用户** |

---

## 4. 先红后绿（证据在 `C1-red.log` / `C1-green.log`）

**红 1 —— 实现缺失**（此时只有占位 stub）：

```
internal\cua\platform_windows_test.go:159:23: undefined: settingsURI
internal\cua\platform_windows_test.go:173:12: undefined: msSettingsProtocolRegistered
FAIL	dshgo/internal/cua [build failed]
--- FAIL: TestCUARc2DefaultServiceIsReal
    handlers_cua_rc2_test.go:306: platform 必须是 win32，得到
--- FAIL: TestCUARc2RunningApplicationsIsAdditive
    handlers_cua_rc2_test.go:362: EnumWindows 应至少枚举到一个可见窗口：{"result":[]}
FAIL	dshgo/internal/transport	0.444s
```

**红 2 —— 图标常量写错**（真实 bug，被测试抓住）：

```
SHGetFileInfo ok=1 hIcon=0
platform_windows_test.go:91: notepad.exe 应该有图标（有图标资源的系统 exe），得到 map[]
```

根因：`SHGFI_ICON` 写成 `0x00000080`，真值是 **`0x00000100`**（shellapi.h）。
这类常量写错**不会报错**，只会静默拿不到图标——正是「先红」要抓的东西。

**红 3 —— 空路径也能造出身份**：

```
store_test.go:36: 空路径不该造出身份，得到 {ID:e3b0c442…b855 …}
```

`sha256("")` 是一个「看起来合法」的 64 位摘要，等于给所有没路径的东西编了同一个身份（撤一个撤掉全部）。

**绿**：`tmp/GO8-RC2/reports/C1-green.log`（两包**全量**测试）→ `--- PASS` **676** 条，`--- FAIL` **0** 条；
其中本任务新写的 **39** 条（`internal/cua` **29** + `internal/transport` **10**）全部 PASS。

**`-race` 未验证**：本机无 gcc（`which gcc` 空，`-race requires cgo`），与 AGENTS.md「`-race` 未成功=未验证」同口径。
并发正确性由 `TestStoreConcurrentRevokesSerialise`（8 并发撤销 → `revoked=8`）在无 race 检测下先行钉住。

---

## 5. 真实样本（本机采集，`-run TestEvidenceDump -v`）

```json
{
  "platform": "win32",
  "sessionLocked": false,
  "captureAvailable": true,
  "elevated": true,
  "helper": { "pid": 17304, "path": "…\\go-build…\\b001\\cua.test.exe", "name": "cua.test.exe" },
  "parent": { "pid": 3188, "path": "C:\\Program Files\\Go\\bin\\go.exe", "name": "go.exe" },
  "providerAvailable": false,
  "helperRole": "host",
  "consentEstablished": false,
  "platformNote": "win32 分支没有 accessibility/screenRecording：Windows 没有 macOS 的 TCC 授权模型…",
  "capableWhen": "当平台提供 dsh-cua 原生 provider（官方 macOS 包 @deepseek-ai/dsh-experimental-cua-macos，rc.2 的 dsh-cua-bundle 只在 darwin 挂载：cordis.patch.yml:5-8）…",
  "observedAt": "2026-11-11T22:07:33Z"
}
```

```
icon[C:\Windows\System32\notepad.exe] len=4006 prefix=data:image/png;base64,
runningApplications=14
  *What we accept - Notepad | pid=27688 | …\WindowsApps\Microsoft.WindowsNotepad_…\Notepad.exe | id=c718cfd624a4…
  DeepSeek Harness - 文件资源管理器 | pid=19380 | C:\Windows\explorer.exe | id=4cee57f2a02b…
  WP Data Access … | pid=46132 | C:\Program Files\Citro Labs\ego lite\Application\ego.exe | id=dbcdc4a5c23a…
  Windows 输入体验 | pid=40540 | …\TextInputHost.exe | id=1f6d1891eab1…
  magpie | pid=50964 | C:\Users\Administrator\AppData\Local\Programs\magpie\magpie.exe | id=238366835f09…
```

---

## 6. 接线行（Lead 执行；本任务按边界**没有**改这两个文件）

### 6.1 路由（`internal/transport/server.go`，`handleAPI` 内官方方法块）

**注意：真正的 /api 分派在 `internal/transport/server.go:423` 的 `handleAPI`，不是 `cmd/dshd/serve.go`。**
插在 `IsOfficialAgentTeamRC2Method` 分支之后（当前 `server.go:516-520`）：

```go
	// 官方 0.2.2-rc.2 电脑操作面：9 个 `cuaAccess.*`（+1 additive runningApplications）。
	// 不依赖原生 provider 的真做（观测/决策/图标/设置页/PTC），缺能力时 501 + capableWhen。
	if IsOfficialCUARc2Method(name) {
		s.ServeOfficialCUARc2API(w, r, name)
		return
	}
```

### 6.2 宿主装配（`cmd/dshd/serve.go`，紧跟 `AttachOfficialAgentTeamRC2API`，当前 `:801`）

```go
	// CUA：进程内存储 + 平台真实观测；给 home 才有持久化决策（persistentAvailable=true）。
	srv.AttachOfficialCUARc2API(transport.NewOfficialCUARc2APIFromService(
		cua.NewService(cua.Options{HomeDir: *home})))
```

需要新增 import：`dshgo/internal/cua`。**不接这一行也能用**（默认实现是真实 Service，只是
`persistentAvailable:false`——那是真话：没给状态目录就不能跨重启保留决策）。

---

## 7. 测试清单（本任务新增 39 条，全绿；两包全量 676 PASS / 0 FAIL）

`internal/cua`（29）：

| 文件 | 覆盖 |
|---|---|
| `store_test.go`（10） | 身份摘要稳定性/大小写归一、allowed/blocked 投影、always 压过 session deny、revoke（代数/revision/持久化）、retryApproval（deny→ask、no-op 不推 revision）、非法 id、内存态拒持久许可、坏文件不静默重置、8 并发撤销不丢更新、落盘文件与官方 domain 同形（2 键 + 锁文件） |
| `service_test.go`（14） | applications 不顺手取图标、图标只取有路径的应用、无路径返回空对象且不调平台、超长图标丢弃、systemStatus 透传且 `providerAvailable=false`、501 透传、openSettings 枚举校验与转发、requestPermission 不建立同意且不写决策、ptcConfiguration 真话、enablePersistentPtc 已持久化 no-op、profile probe 真写、`retainsState` 双键识别、runningApplications 透传、revoke/retry 改真状态 |
| `platform_windows_test.go`（5，`//go:build windows`） | systemStatus 真实观测（helper=本进程、路径存在、providerAvailable=false、ObservedAt 可解析）、图标真 PNG 可解码 ≤65536、缺失文件不编图标、枚举条目身份/名称/pid/路径全部真实且数量≥1、设置页 URI 映射 + `ms-settings` 协议注册 |

`internal/transport`（10）：方法表（9+1、顺序、不混表）、无参方法拒未知字段（6 个）、参数校验（11 例 400）、void 用 `{}`、501 带 `capableWhen` 且四字段齐、applications 线上形状（`[]` 不是 null）、revoke↔retry 线上往返、**未接线时默认实现是真读**（Windows 200 / 非 Windows 501）、超长图标响亮失败、additive 枚举（Windows 真枚举 ≥1 / 非 Windows 501）。

---

## 8. 已知边界（登记，不隐藏）

1. **非 Windows 构建**：`systemStatus` / `openSettings` / `requestPermission` / `runningApplications` 为结构化 501 + `capableWhen`；`lockFile` 退化为进程内锁（不做 flock）。本仓支持范围只到 Windows（AGENTS.md），非 Windows 明确 `OUT_OF_SUPPORT`。
2. **图标画布固定 64×64**：系统图标尺寸 16–256 不等，256×256 的 PNG base64 可能顶到官方 65536 上限；64×64 是权限页实际显示尺寸（实测 4006 字符）。这是**按目标尺寸重绘**，不是裁图。
3. **`openSettings` 在 Windows 打开对等设置页**（`ms-settings:easeofaccess` / `ms-settings:privacy`），不是 macOS 的 TCC 面板——契约枚举本来就只有 macOS 两种权限，Windows 没有对应面板。
4. **`helper` 报宿主进程**（`helperRole:"host"`）：本仓没有独立原生 helper 进程，宿主自己就是原生侧；报真实 pid/path/name，不编一个 helper。
5. **`applications` 不枚举进程**：见 §2.1，枚举是 additive 方法。
6. **应用身份摘要算法是自定的**（`sha256(小写规范路径)`）：官方用 provider 算的平台元数据摘要；没有 provider 时这是同机稳定可复算的等价物，已写在 `types.go:238`（`ApplicationIdentity`）的注释里。

---

## 9. 环境侧记录（不是我的改动）

- 本工作树多路并行：`git status` 里有 150+ 个他人改动（`cmd/dshd/serve.go`、`internal/transport/server.go`、AGENTS/CHANGELOG 等），**都不是本任务改的**；本任务只新增了 §10 列的 3 个路径。
- 采集绿证据时 `go build ./...` 有红，两条都**不在本任务范围**：
  1. `tmp/GO8-RC2/d3-backup/internal/agent/parallel.go`（别人留在 tmp 的备份副本，`undefined: Loop` 等）；
  2. `internal/transport/server.go:1009` 当时正被另一个 agent 编辑（`assignment mismatch: … s.officialHost returns 1 value`，属 rc.2「空 content 续跑」新增代码的中间态）；该文件稳定后本包测试复跑仍为绿。
  3. `modules/tool/schedule/schedule.go:403-410`（`t.view`/`t.edit`/`t.editLegacy` 未定义）—— 同一时刻另一个 agent 的 `tools/schedule` 在写（`git status` 显示该文件与测试都被改过）。**本任务未碰 `modules/`。**

---

## 10. 交付物

| 路径 | 说明 |
|---|---|
| `internal/cua/types.go` | 包契约、官方形状类型、`Error`（含 `CapableWhen`）、身份摘要 |
| `internal/cua/store.go` | 决策存储状态机（投影/revoke/retry/decide/always）+ 原子落盘 + 校验 |
| `internal/cua/service.go` | 9 方法 + additive 枚举的服务层、注入点、`capableWhen` |
| `internal/cua/ptc.go` | PTC 状态探针（官方 `ptc-runtime` 条目）+ `Enable` 语义 |
| `internal/cua/lock_windows.go` / `lock_other.go` | 跨进程 `LockFileEx` / 非 Windows 退化 |
| `internal/cua/win32_windows.go` | DLL/proc 表、进程/桌面/DIB 基础封装 |
| `internal/cua/platform_windows.go` | 真实 systemStatus 观测 |
| `internal/cua/icons_windows.go` | 真实图标提取 → PNG data URL |
| `internal/cua/apps_windows.go` | 真实 `EnumWindows` 应用枚举 |
| `internal/cua/settings_windows.go` | `ms-settings` 设置页打开 |
| `internal/cua/platform_other.go` | 非 Windows 明确不可用的桩（可编译、语义 OUT_OF_SUPPORT） |
| `internal/transport/handlers_cua_rc2.go` | 10 个方法的路由、严格参数校验、错误出口、挂载点、Service 适配器 |
| 测试 | `internal/cua/{store,service,platform_windows}_test.go`、`internal/transport/handlers_cua_rc2_test.go` |
