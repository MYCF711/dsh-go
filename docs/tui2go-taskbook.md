# `dsh-tui2go` 精确任务书（v1）

> **配套**：[architect-official-sync-blueprint.md](architect-official-sync-blueprint.md)、[architect-official-sync-taskbooks.md](architect-official-sync-taskbooks.md)。
> **性质**：只读架构师产出。本文件只定义**可派单**的任务边界与验收命令，不含产品代码。
> **运行模型**：`group/deepseek-v4.1-flash-1017`（已核对，与派单要求一致）。
> **编写时间**：2026-11-08 02:16（Asia/Shanghai）。
> **本任务不解的账**：宿主 stable 门（`HS-*`）与本包无关；本包不改变 `TUI-019-GO-ADAPTER BLOCKED(upstream)` 在宿主任务书里的记法，只在 TUI 侧给出可执行的替代路线。

---

## 0. 一句话定义

`dsh-tui2go` 是 **TUI 的第三方 AgentBackend bundle**：一个 npm 包，包根带 `dsh-plugin.json`，在 `contributes['x-dsh-tui']` 里声明一个 `kind: 'Backend'` 的 `dsh-go` 后端，包入口导出名为 `backend` 的 handler；装进 TUI 0.19 的 profile 并重启后，`/kernel` picker 里出现 `DSH-GO` 一行，选中后由本包拉起并驱动 Go 宿主 `dshd`（`DSH-GO 0.2.2-go.1`）。

**它不是**：Go 宿主插件；不是 TUI 的 fork 或重写；不是把 0.18 的 `src/backends/dsh-go/` 目录塞回 0.19 源码树。

| 事实 | 值 | 证据 |
|---|---|---|
| 最终 npm 包名 | `dsh-tui2go` | 本任务书 §7 |
| backend id（`spec.id`） | `dsh-go` | `src/dsh-go/backend.ts:49`（`DSH_GO_BACKEND_ID`） |
| 显示名（`spec.label.text` / `shortLabel`） | `DSH-GO` | `src/dsh-go/backend.ts:50` |
| `product` | `dshd` | `src/dsh-go/manifest.ts:8` |
| 宿主包名 | `dsh-go-host`（**未发布，npm E404**） | §4.3 |
| 宿主版本 | `0.2.2-go.1` | `./dshd.exe --version` → `DSH GO 0.2.2-go.1` |
| 目标 TUI | `@deepseek-harness-tui/dsh-tui@0.19.0` | `.../tui-019-run/package/package.json` |
| TUI 源码基线（可复用件来源） | `0.18.0` | `dsh-TUI-upstream-src/package.json` |

---

## 1. 包归属与目录

### 1.1 为什么必须放在 TUI 侧（三条独立的硬理由）

1. **协议归属**：`Backend` 是 `tui.dsh/v1alpha1` 贡献族的第三个成员，装配链路**全部**在 TUI 包内，没有一个字节在 Go 侧：
   - 装配入口 `assembleBackendsFromProfile(ctx)` —— `lib/types/dsh-adapter/backend-assembly.js:62`
   - 读 profile 层 `dsh.profile.bundles` —— 同文件 `:209`（`installedBundlesOf`）
   - 读包根 `dsh-plugin.json` 的 `contributes['x-dsh-tui']` —— `lib/types/dsh-adapter/plugin-declaration.js:73`（`declaredBackendsOf`）、`:142`（`declaredBackendsIn`）
   - 注册与 lazy load —— `lib/types/dsh-adapter/backend-registry.js:92`（`registerBackend`）、`:249`（`loadBackend`）
2. **Go 宿主没有这个 ABI，也不该有**：`dshd` 对外只有 Transport（`POST /api/<method>` 裸参数 + 每会话 `/ws` journal）。给它加一个 AgentBackend 插件 ABI 会直接撞上 `AGENTS.md` R6「**不嵌 JS 运行时**」。宿主承担的是「被消费」，不是「装配 UI 插件」。
3. **不能改 TUI 核心**：0.19 的 `lib/types/**` 是**只读依赖**。往 0.19 源码树里加 `src/backends/dsh-go/` 需要重跑 `scripts/gen-backend-index.mjs`（`gen-backend-index.mjs:1-40` 说明它只扫仓内 `src/backends/*/manifest.ts`）并重新构建，等于维护一个 TUI fork。第三方 bundle 是**官方为此保留的、不需要 fork 的那条路**（`docs/dsh-tui-compat.md:10`、`docs/agent-backend-design.md:364-390`）。

### 1.2 最小目录树

仓库根 = 包根（单包仓库，不做 monorepo；`optionalDependencies` 的平台包是别人的包，不是本仓的工作区）。

```text
dsh-tui2go/
├── package.json                 # name=dsh-tui2go, dsh.bundle.patch, exports["."]
├── dsh-plugin.json              # 包根声明（community draft 0.15），contributes['x-dsh-tui']
├── cordis.patch.yml             # 内容就是 `[]`；只为让包成为 profile 的一层
├── tsconfig.json                # 只编译 src → lib，target es2024 / module esnext / bundler
├── README.md
├── LICENSE                      # MIT
├── .gitignore                   # lib/ 与 node_modules/ 不入库（lib 是构建产物）
├── .github/
│   └── workflows/ci.yml         # npm test + tsc --noEmit + pack --dry-run
├── src/
│   ├── index.ts                 # 包入口：export { backend } + unloadExport + manifest + 类型
│   ├── manifest.ts              # BackendManifest 字面量（inTree:false）
│   ├── backend.ts               # createDshGoBackend / DshGoSession / supervisor / catalog / launch
│   ├── pool.ts                  # 模块级池钩子（unloadExport 指向它）
│   ├── transport.ts             # HTTP /api + WS /ws + 一次性 token + sinceSeq 重连
│   ├── events.ts                # 服务端帧 → AgentEvent 翻译器（live 与 replay 同一套）
│   ├── step-stream.ts
│   ├── view-spec.ts
│   ├── task-events.ts
│   ├── trajectory-history.ts
│   ├── attachments.ts
│   ├── auth.ts
│   ├── color.ts
│   ├── compact.ts
│   ├── context.ts
│   ├── effort.ts
│   ├── goals.ts
│   ├── images.ts
│   ├── init.ts
│   ├── mcp.ts
│   ├── modes.ts
│   ├── placement.ts
│   ├── rename.ts
│   ├── session-operations.ts
│   ├── side-query.ts
│   ├── subagents.ts
│   ├── tasks.ts
│   ├── image-size.ts            # 从 0.18 src/backends/shared/image-size.ts 原样 vendor（136 行，零依赖）
│   └── types/
│       └── agent/               # 从 0.18 lib/types/agent/*.d.ts vendor 的**结构副本**
│           ├── backend.d.ts
│           ├── session.d.ts
│           ├── capabilities.d.ts
│           ├── events.d.ts
│           ├── refs.d.ts
│           ├── presentation.d.ts
│           ├── backend-manifest.d.ts
│           └── channel-view.d.ts
└── test/
    ├── unit/                    # 从 0.18 src/dsh-go/*.test.ts 移植
    ├── assembly/               # 0.19 真实装配链（装进一次性 profile）
    ├── conformance/            # W110 移植：真实 TUI + 真实 dshd（见 §6.5）
    └── fixtures/
```

**`lib/` 是构建产物**（`tsc -p tsconfig.json`），`package.json` 的 `files` 只列 `lib`、`dsh-plugin.json`、`cordis.patch.yml`、`README.md`、`LICENSE`。

### 1.3 vendor 还是重写：**vendor 0.18 的 `src/dsh-go/**`，只改 import 机制**

**结论：vendor，不重写。** 理由是可验证的，不是口味：

| 检查 | 命令 | 结果 |
|---|---|---|
| `AgentBackend` 契约是否变过 | `diff -u <0.18>/lib/types/agent/backend.d.ts <0.19>/lib/types/agent/backend.d.ts` | **空**（逐字节相同） |
| `AgentSession` | 同上 `session.d.ts` | **空** |
| `SessionCapabilities` | 同上 `capabilities.d.ts` | **空** |
| `AgentEvent` | 同上 `events.d.ts` | **空** |
| `AgentSessionRef` | 同上 `refs.d.ts` | **空** |
| domain barrel | 同上 `index.d.ts` | **空** |
| `BackendManifest` | 同上 `backend-manifest.d.ts` | **空** |

7 个文件全部逐字节相同 ⇒ 0.18 的 `src/dsh-go/**`（25 个非测试 `.ts`，10,603 行含测试）**不需要**重写方法体。

**但 import 机制必须换。** 0.18 的每个文件都写 `import ... from '../agent/backend.js'`；**包外无法**把它改成 `from '@deepseek-harness-tui/dsh-tui/agent/backend.js'`：

- 0.19 的 `package.json.exports` 只有 `.`、`./working-activity`、`./oauth`、`./workspaces`、`./command-trees`、`./preset-patch`、`./settings-sections`、`./scenes`、`./panels`、`./extensions`、`./plugin-host`、`./api`、`./jsx-runtime`、`./invariant`、`./cordis.patch.yml`、`./settings.json`、`./package.json`、`./icon`、`./locale/*`。
- **没有 `./agent/*`、没有 `./dsh-adapter/*`、没有 `./adapter/*`、没有 `"./*"` 通配**，也没有 `typesVersions`。
- 0.18 的 `exports` 与 0.19 **逐项相同**（同一份 map），所以「回退到 0.18 就好」不成立。

⇒ **类型必须 vendor 到包内**（`src/types/agent/*.d.ts`，纯 `.d.ts`，零运行时代码），业务代码写 `from './types/agent/backend.js'`。

**唯一可以从 `@deepseek-harness-tui/dsh-tui` 根导入的声明面**（`lib/types/dsh-adapter/index.d.ts:293-295`）：

```ts
export { BACKEND, BACKEND_CAPABILITY_NAMES, BACKEND_RESERVED_IDS, validateBackendSpec,
         type BackendInstallRecipe, type BackendSpec } from '../adapter/spec/tui-contributions.js'
export { backendAdmission, type BackendAdmission, type BackendAdmissionHost } from './backend-admission.js'
export { backendContributionOf } from './backend-contribution.js'
```

⇒ 只有 `BackendSpec` 这一家可以用包名导入；`AgentBackend` / `AgentSession` / `BackendHost` / `BackendDetection` / `AgentEvent` / `SessionCapabilities` / `AgentSessionRef` **都不能**。

### 1.4 明确不可改的 Go 宿主

本任务**全程只读**以下路径（任何写入即为越界）：

- `D:/DeepSeek Harness/default-workspace/DSH-GO/**`（除本任务书自身已存在外，本轮不新增任何文件）
- 具体：`cmd/dshd/**`、`internal/**`、`modules/**`、`codecs/**`、`docs/transport-contract.md`、`AGENTS.md`、`PLAN.md`、`UIPLAN.md`、`LEAD.md`、`dsh.toml`
- `dshd.exe`、`dshtui.exe`、`dshd-bridge.exe`、`dist/**`（**不重新构建**）
- `D:/DeepSeek Harness/default-workspace/dsh-TUI-upstream-src/**`（0.18 只读基线）
- `D:/DeepSeek Harness/default-workspace/DSH-GO/tmp/dsh-TUI-pr2/**`（0.19 源码 checkout，只读）
- `C:/Users/Administrator/AppData/Local/Temp/tui-019-run/package/**`（0.19 官方 npm 包，只读）

**不重启 DSH、不重启 magpie、不 `taskkill`。**

---

## 2. 适配器边界

### 2.1 `AgentBackend` 需要实现的方法（0.19 真实签名，`lib/types/agent/backend.d.ts:216-260`）

```ts
interface AgentBackend {
  readonly id: string                                     // 必须是 'dsh-go'
  readonly descriptor: { readonly label: string }         // 'DSH-GO'
  detect(host: BackendHost): Promise<BackendDetection>
  open(target: OpenTarget, host: BackendHost): Promise<AgentSession>
  readonly catalog?: SessionCatalog
  readonly launch?: {
    sessionPrefs(host: BackendHost): BackendSessionPrefs
    resumeCommand(sessionId: string): string
  }
}
```

- `BackendDetection`（`:8-38`）：`installed`、`auth?: 'ok'|'missing'|'unknown'`、`version?`、`stale?: true`、`drift?`、`hint?`、`loginInSession?: true`。**`detect` 永不抛**——失败落在返回值里。
- `OpenTarget`（`:97-`）：`{ kind: 'create'; cwd: string } | { kind: 'resume'; sessionId: string; cwd?: string }`。
- `BackendHost`（`:144-`）：`cwd`、`dataDir?`、`locale()`、`appVersion?`、`tokenStore?`、`debug()`、`warn()`、`stderr?`、`oauthCredential?`。**全部按 feature-detect 用，缺了必须降级，不许自己拼路径**（`dataDir` 缺席 ⇒ 退化成内存 prefs）。
- `SessionCatalog`（`:189-`）：`list(scope?, onPartial?)`、`snapshotKey?()`、`info?()`、`preview?()`、`rename?()`、`delete?()`、`deleteAction?: 'archive'`。
- `BackendInstallRecipe`（`:81-90`）：`{ executor: string; specifier: string; version: string }`。

### 2.2 `AgentSession` 需要实现的方法（`lib/types/agent/session.d.ts:58-`）

```ts
interface AgentSession {
  readonly ref: AgentSessionRef                    // { backendId: 'dsh-go', sessionId }
  readonly cwd: string
  readonly status: AgentSessionStatus               // starting|idle|running|requires-action|disposed
  readonly capabilities: SessionCapabilities
  history(): Promise<readonly AgentEvent[]>
  subscribe(listener: (batch: readonly AgentEvent[], meta: AgentEventMeta) => void): () => void
  submit(input: AgentInput, placement: SubmitPlacement):
    Promise<{ readonly accepted: boolean; readonly reason?: string; readonly dropped?: boolean }>
  cancel(cause: CancelCause): Promise<CancelReceipt>
  dispose(): Promise<void>
}
```

- `AgentInput`（`:10-`）：`text`、`blocks?`、`images?`、`clientMessageId`、`native?`。
- `SubmitPlacement`（`:31`）：`'turn' | 'steer' | 'followup' | 'now'`。**本后端只接受 `turn`**，另三个必须在发 HTTP 之前显式拒绝且 `dropped=true`（`docs/dsh-go-backend.md`「submit」行）。
- `CancelReceipt`（`:53`）：`{ stillQueued: readonly string[]; outcome: 'confirmed'|'unknown'|'failed' }`。**非 `confirmed` 不得当成空队列**。
- `SessionCapabilities`（`capabilities.d.ts:187-`）每个成员都 optional；**没有的就不声明**，UI 会显示不可用。

### 2.3 复用清单：0.18 的 `src/dsh-go/**` 哪些直接复用、哪些要改

源目录：`D:/DeepSeek Harness/default-workspace/dsh-TUI-upstream-src/src/dsh-go/`（**用这一份，不用 `src/backends/dsh-go/`**，理由见 §2.4）。

| # | 0.18 文件 | 行数 | 处理 | 必须的改动 |
|---|---|---:|---|---|
| 1 | `backend.ts` | 1041 | **复用** | 8 处 `from '../agent/...'` → `from './types/agent/...'`；`from '../backends/shared/image-size.js'` 由 `images.ts` 承担 |
| 2 | `transport.ts` | 735 | **复用** | `import WebSocket from 'ws'` 保留（本包自己声明 `ws` 依赖） |
| 3 | `events.ts` | 647 | **复用** | 无（只 import `../agent/*` 与 `./transport.js`） |
| 4 | `conformance.ts` | 559 | **复用（改路径）** | `../agent/*`、`../dsh-adapter/backend-registry.js` 两处改到 `test/` 侧；见 §6.5 |
| 5 | `images.ts` | 367 | **复用** | `../backends/shared/image-size.js` → `./image-size.js` |
| 6 | `placement.ts` | 364 | **复用** | 无 |
| 7 | `tasks.ts` | 297 | **复用** | 无 |
| 8 | `session-operations.ts` | 263 | **复用** | 无 |
| 9 | `rename.ts` | 222 | **复用** | 无 |
| 10 | `trajectory-history.ts` | 185 | **复用** | 无 |
| 11 | `auth.ts` | 183 | **复用** | 无 |
| 12 | `task-events.ts` | 180 | **复用** | 无 |
| 13 | `view-spec.ts` | 174 | **复用** | 无 |
| 14 | `color.ts` | 174 | **复用** | 无 |
| 15 | `attachments.ts` | 159 | **复用** | 无 |
| 16 | `goals.ts` | 157 | **复用** | 无 |
| 17 | `step-stream.ts` | 152 | **复用** | 无 |
| 18 | `side-query.ts` | 139 | **复用** | 无 |
| 19 | `effort.ts` | 134 | **复用** | 无 |
| 20 | `context.ts` | 116 | **复用** | 无 |
| 21 | `init.ts` | 109 | **复用** | 无 |
| 22 | `mcp.ts` | — | **复用** | 无 |
| 23 | `modes.ts` | — | **复用** | 无 |
| 24 | `subagents.ts` | — | **复用** | 无 |
| 25 | `compact.ts` | — | **复用** | 无 |
| 26 | `manifest.ts` | 16 | **重写** | 改成第三方 bundle 的公开声明形状：去掉 `inTree`（`backendManifestOf` 固定 `false`），见 §3.4 |
| 27 | `index.ts` | 17 | **重写** | 加 `unloadExport` 的池钩子；入口默认导出改为 Cordis 空插件，见 §3.5 |
| 28 | `src/backends/shared/image-size.ts` | 136 | **vendor** | 原样复制到 `src/image-size.ts`，零改动（纯叶子，无任何宿主 import） |

**测试文件**：0.18 的 `*.test.ts` 全部可移植（`attachments/auth/backend/backend-integration/backend-selection/color/compact/context/effort/events/goals/images/init/mcp/modes/placement/rename/session-operations/side-query/step-stream/subagents/task-events/tasks/trajectory/trajectory-history/transport/view-spec`）。唯一需要改的是 `backend-selection.test.ts` 里对 `../dsh-adapter/backend-registry.js` 的 import——本包不拥有注册表，改为在 §6.2 的装配夹具里断言选择。

### 2.4 为什么不用 `src/backends/dsh-go/`（集成岛目录）

- 它是 **0.18 仓内**的集成目录，`backend.ts` 1080 行（比 `src/dsh-go/backend.ts` 多 39 行），多出来的是 `setDshGoRemoteSupervisor` / `DshGoRemoteSupervisorSource` 这类**只对仓内 standalone 客户端有意义**的接缝；第三方 bundle 用不上。
- 它的 import 深度是 `../../agent/*`，和 `src/dsh-go/*` 的 `../agent/*` 是同一批文件的两种拼写。**对第三方包来说两者都要改**，所以选行数更少、接缝更干净的 `src/dsh-go/`。
- 仓内目录还带 `transport.test.ts`（265 行，与 `src/dsh-go/transport.test.ts` 的 670 行不同版本）——那是集成期分裂出来的副本，不作为基线。

### 2.5 精确文件清单 + 禁止改的文件

**允许写**（本包仓库内，全部是新文件）：

```text
package.json  dsh-plugin.json  cordis.patch.yml  tsconfig.json  README.md  LICENSE  .gitignore
.github/workflows/ci.yml
src/**            （§1.2 列出的全部）
test/**           （unit / assembly / conformance / fixtures）
```

**禁止改**（任何一条命中即任务失败）：

```text
D:/DeepSeek Harness/default-workspace/DSH-GO/**            # 除本文件外只读
D:/DeepSeek Harness/default-workspace/dsh-TUI-upstream-src/**
D:/DeepSeek Harness/default-workspace/DSH-GO/tmp/dsh-TUI-pr2/**
C:/Users/Administrator/AppData/Local/Temp/tui-019-run/package/**
~/.dsh-tui/**                                              # 真实用户数据目录
~/.dsh/profiles/**                                         # 真实 profile（装配门用一次性临时 profile）
```

**禁止的行为**：`git add -A`；`git push --force`；改 `dshd` 源码；重建 `dshd.exe`；`taskkill`；重启 DSH / magpie；在共享树上跑 `scripts/check.sh`（会 `mv modules`）或 `scripts/regress.sh`（有全局 taskkill）。

---

## 3. 0.19 装配（全部字段以 0.19 真实类型核对，不猜）

### 3.1 `package.json`

```json
{
  "name": "dsh-tui2go",
  "version": "0.1.0",
  "description": "DSH-GO (dshd) AgentBackend bundle for dsh-TUI 0.19",
  "type": "module",
  "license": "MIT",
  "engines": { "node": "^22.19 || >=24" },
  "main": "./lib/index.js",
  "types": "./lib/index.d.ts",
  "exports": {
    ".": { "types": "./lib/index.d.ts", "import": "./lib/index.js" },
    "./package.json": "./package.json"
  },
  "files": ["lib", "dsh-plugin.json", "cordis.patch.yml", "README.md", "LICENSE"],
  "dsh": { "bundle": { "patch": "./cordis.patch.yml" } },
  "keywords": ["dsh", "dsh-tui", "dsh-plugin", "dsh-tui-plugin", "dsh-go", "agent-backend"],
  "repository": { "type": "git", "url": "https://github.com/MYCF711/dsh-tui2go.git" },
  "publishConfig": { "access": "public" },
  "dependencies": { "ws": "^8.21.3" },
  "devDependencies": { "typescript": "^5.9.0", "@types/ws": "^8.18.0", "@types/node": "^24.0.0" }
}
```

**逐项依据**：

- `"type": "module"` —— 0.19 是 ESM 包；`loadThroughLoader` 走 `import()`，CJS 会失败。
- `"main"` / `"exports"."."` —— `resolveEntryUrl()`（`admission-loader.js:234`）通过 Loader 的 ESM resolver 解析**包名**，拿到的是 `exports["."]`。**只写 `main` 不写 `exports` 也能解析，但写上更明确**；注意 `exports` **不要**加 `"./*"` 通配（那会让包内 `.d.ts` 被外部当公开面）。
- `"dsh": { "bundle": { "patch": "./cordis.patch.yml" } }` —— 这是**唯一**让包成为 profile 一层的开关。`/plugin install` 与 CLI 只把声明了 `dsh.bundle` 的包写进 `dsh.profile.bundles`（`docs/agent-backend-design.md:371-372`；`backend-assembly.js:209` 读的就是这个列表）。缺了它：包只是普通依赖，**永远不会被装配**。
- `"engines"` —— 抄 0.19 的 `^22.19 || >=24`（0.19 `package.json.engines.node`）。**TUI 侧没有 `engines.dsh` 这个字段**（那是 Official DSH / Cordis 插件线的形状）；0.19 官方包自身也只有 `engines.node`。不要在这里发明。
- `"dependencies": { "ws" }` —— `src/transport.ts:1` 的 `import WebSocket from 'ws'`。**不能**借用 TUI 的 `ws`：包的 `node_modules` 解析边界在 profile 里，不保证能提升到 TUI 的依赖。版本与 0.19 一致（`^8.21.3`）。
- **`peerDependencies` 不写 `@deepseek-harness-tui/dsh-tui`**：本包在运行时**零 import** 它（类型是 vendor 的，见 §1.3）。写 `peer` 只会制造一个装不上的约束。
- `files` 里**必须有** `dsh-plugin.json` 与 `cordis.patch.yml` —— `findManifest()`（`admission-loader.js:285`）从入口模块向上找**最近的 `package.json`**，再要它的**同级** `dsh-plugin.json`。没进 tarball 就等于没声明。

### 3.2 `cordis.patch.yml`

```yaml
# 本包只声明 Backend，不挂任何插件行。空数组是合法且完整的 patch。
[]
```

依据：0.18 官方夹具 `scripts/fixtures/third-party-backend/cordis.patch.yml` 全文就是 `[]` 加一段注释；`dsh.bundle.patch` 的**存在**才是判据，行内容是其次。

### 3.3 `dsh-plugin.json`（canonical community draft 0.15）

```json
{
  "$schema": "urn:dsh-std:community-draft:dsh-plugin:0.15",
  "manifestVersion": "0.15",
  "id": "dsh-tui2go",
  "name": "DSH-GO TUI Backend",
  "version": "0.1.0",
  "facets": {
    "host": { "entry": "lib/index.js", "apiVersion": "v1alpha1" }
  },
  "requires": { "contracts": [] },
  "permissions": [],
  "contributes": {
    "commands": [],
    "x-dsh-tui": [
      {
        "apiVersion": "tui.dsh/v1alpha1",
        "kind": "Backend",
        "id": "dsh-tui2go",
        "name": "dsh_go_backend",
        "spec": {
          "id": "dsh-go",
          "label": { "text": "DSH-GO" },
          "shortLabel": "DSH-GO",
          "product": "dshd",
          "capabilities": [
            "native", "permissions", "questions", "pendingRetraction", "models",
            "effort", "modes", "goals", "compact", "fork", "rewind", "tasks",
            "subagents", "images", "transcript", "context", "mcp", "sideQuery",
            "auth", "rename", "color", "init"
          ],
          "grants": [],
          "unloadExport": "closeDshGoPool"
        }
      }
    ]
  },
  "subscriptions": [],
  "license": "MIT",
  "source": { "repository": "https://github.com/MYCF711/dsh-tui2go" }
}
```

**逐字段校验（全部对着 0.19 的 schema / validator 核过）**：

| 字段 | 约束 | 本值 | 依据 |
|---|---|---|---|
| 顶层必填 | `$schema` `manifestVersion` `id` `name` `version` `facets` `requires` `permissions` `contributes` `subscriptions` | 全给 | `node_modules/@dsh-std/manifest/schema/dsh-plugin-0.15.schema.json` 的 `required` |
| `manifestVersion` | `const: "0.15"` | `"0.15"` | 同上 |
| `id` | `^[a-z][a-z0-9]*(?:[.-][a-z0-9][a-z0-9-]*)+$` | `dsh-tui2go` ✅（`dsh` + `-tui2go`） | `$defs.namespacedId` |
| `facets.host.entry` | 相对路径、不得盘符、不得 `..` | `lib/index.js` ✅ | `facets.host.entry.pattern` |
| `facets.host.apiVersion` | `^v[1-9][0-9]*(?:(?:alpha\|beta)[1-9][0-9]*)?$` | `v1alpha1` ✅ | 同上 |
| `contributes.commands` | **必填数组**（可空） | `[]` | `contributes.required = ["commands"]` |
| `contributes['x-dsh-tui']` | `patternProperties."^x-"` 要求 `array` | 单元素数组 | `plugin-declaration.js:110-118` |
| 条目 `kind` | 必须 `'Backend'`（其余静默跳过） | ✅ | `plugin-declaration.js:98` |
| 条目 `apiVersion` | 必须 `'tui.dsh/v1alpha1'`（`BACKEND.apiVersion`） | ✅ | `plugin-declaration.js:100-103` |
| 条目 `id` | 必须是 `string`（**可与 `spec.id` 不同**） | `dsh-tui2go` | `plugin-declaration.js:107` |
| 条目 `name` | `^[a-z][a-z0-9_-]*$` | `dsh_go_backend` ✅ | `plugin-declaration.js:112` |
| `spec` 允许键 | 只有 `id,label,shortLabel,product,capabilities,grants,install,unloadExport,confirmation`（+ `x-` 前缀） | 只用了允许的 | `validateBackendSpec` 的 `exactRecord([...])` |
| `spec.id` | `^[a-z0-9][a-z0-9-]{0,31}$` | `dsh-go` ✅ | 同上 + `bin/dsh-tui.js:87` 同一规则 |
| `spec.label.text` | 非空字符串（**只收字面量**） | `DSH-GO` ✅ | `nonEmpty(label.text, ...)` |
| `spec.shortLabel` | 非空字符串 | `DSH-GO` ✅ | `nonEmpty(spec.shortLabel, ...)` |
| `spec.capabilities` | `string[]`；每个名字必须在 `BACKEND_CAPABILITY_NAMES`（27 个）里 | 22 个，**0.18 manifest 原样** | `adapter/spec/backend-capabilities.js:21-49` |
| `spec.grants` | `string[]`；**任何已知权限名都会让它 `waiting_authorization`** | `[]` ✅ | `backend-admission.js` |
| `spec.unloadExport` | 非空字符串；模块里**必须真的导出这个函数** | `closeDshGoPool` | `loadBackend` 的检查 |
| `confirmation` | 可选；`policy ∈ {'confirm','trusted-source'}` | **不写**（默认 `confirm`） | `BACKEND_CONFIRMATION_POLICIES` |

**两个必须写死的"不许"**：

1. **`grants` 必须是 `[]`。** `backendAdmission()` 里只要有**一个**已知权限名且 `granted` 集合为空（今天永远是空），就返回 `waiting_authorization` → `listOfferedBackends()` 会把它滤掉（`backend-registry.js:181`），`loadBackend()` 直接抛 `cannot load`（`:253-255`）。**声明任何 grant 等于让 DSH-GO 从 picker 里消失。**
2. **`capabilities` 不许写 0.19 之外的名字。** 未知名字不拒绝，但会记成 `compatible_degraded` 的 `missingOptional`（`capabilityItem(name)`）。虽然仍可选中，但那是"声明了一个宿主不认识的能力"，属于假声明。

**`capabilities` 为什么是这 22 个**：0.18 `src/dsh-go/manifest.ts:10-14` 原样。0.19 的 27 个里我们**不声明**的 5 个是 `channels`、`commands`、`account`、`workingActivity`、`diagnostics` —— 0.18 的 `DshGoSession` 没有实现它们，声明了就是空壳。

### 3.4 默认 `backend` export

`loadBackend()`（`backend-registry.js:249-283`）的行为是**逐条写死的**：

```js
const module = await entry.load()                       // = Loader 的模块命名空间
const { backendExport, unloadExport } = entry.manifest
if (unloadExport !== undefined) {
  const unload = module[unloadExport]
  if (typeof unload !== 'function') throw new Error(`... declares unloadExport "${unloadExport}", but its module does not export it`)
  loadedUnloads.set(id, unload)
}
const exportName = backendExport ?? 'backend'          // 第三方 bundle 只能用 'backend'
const backend = module[exportName]
if (backend === undefined) throw new Error(`... does not export "${exportName}"`)
assertBackendHandler(backend)                          // 形状断言
if (backend.id !== id) throw new Error(`... exports a handler for ${JSON.stringify(backend.id)}`)
```

⇒ 三条硬约束：

1. **导出名固定 `backend`**。`backendExport` 是 in-tree 私有的（`backendManifestOf` 明确不写它，`backend-contribution.d.ts` 注释"a contribution hands its handler object over at registration"）。声明里命名不了别的导出。
2. **`backend.id === spec.id`**，即 `'dsh-go'`。
3. **`assertBackendHandler` 是结构断言**（`tui-contributions.js:195-204`）：对象、`id` 非空字符串、`detect` 与 `open` 必须是函数、`descriptor`/`catalog`/`launch` 给了就必须是对象。**不做 nominal 类型检查** —— 这正是 vendor 类型能跑通的原因。

`src/index.ts`：

```ts
/** 第三方 DSH-GO AgentBackend bundle 入口。 */
export {
  backend,
  createDshGoBackend,
  dshGoResumeCommand,
  DSH_GO_BACKEND_ID,
  DSH_GO_BACKEND_LABEL,
  type DshGoBackendOptions,
  type DshGoSupervisor,
} from './backend.js'
export { closeDshGoPool } from './pool.js'
export { manifest } from './manifest.js'

/** bundle 是 Cordis 插件包；本包不贡献任何行，默认导出是空实现。 */
export default function dshTui2GoBundle(): void {}
```

`src/pool.ts`（`unloadExport` 的落点）：

```ts
let closes = 0
/** 只关模块级/进程级资源池；会话级资源归 session.dispose()。 */
export async function closeDshGoPool(): Promise<void> { closes += 1 }
export function dshGoPoolCloseCount(): number { return closes }
```

### 3.5 `manifest.ts`（重写，不是 0.18 原样）

0.18 的 `manifest.ts` 是 `BackendManifest`（in-tree 超集），带 `inTree: true|false`。第三方 bundle 的 `spec` 走 `backendManifestOf()` 投影，**`inTree` 由投影固定为 `false`**，声明里写不了也不需要写。

```ts
import type { BackendManifest } from './types/agent/backend-manifest.js'

export const manifest: BackendManifest = {
  id: 'dsh-go',
  label: { kind: 'literal', text: 'DSH-GO' },
  shortLabel: 'DSH-GO',
  product: 'dshd',
  inTree: false,
  capabilities: [ /* 与 dsh-plugin.json 的 spec.capabilities 逐字一致 */ ],
  grants: [],
  unloadExport: 'closeDshGoPool',
}
```

**`dsh-plugin.json` 的 `spec` 与 `manifest.ts` 必须逐字一致**（除 `inTree`）：两处是同一份事实的两种写法，CI 里加一条断言钉住（§6.3 的 `D-1`）。

### 3.6 装配时序（必须知道，否则会写出"装了不生效"）

`assembleBackendsFromProfile(ctx)` 是**同步**的、在 boot **解析 backend choice 之前**跑（`backend-assembly.js:1-40` 的长注释）。它做四件事：

1. `profileManifestPathOf(ctx)`：从 `ctx.baseUrl`（`file:` URL）取 `<profile>/package.json`。**没有 `baseUrl` ⇒ 直接跳过**（`profileManifestPathOf` 返回 `undefined`）。
2. `installedBundlesOf(profile)`：读 `parsed?.dsh?.profile?.bundles`，过滤非字符串/空串。
3. `scanned = bundles.filter(name => !isCompositionOwned(name))`（`plugin-declaration.js:203`，排除 `@deepseek-harness-tui/dsh-tui` 自身）。
4. 每个 bundle：通过 **Loader 自己的 ESM resolver** 解析入口（`resolveEntryUrl`）→ 从入口向上找最近的 `package.json` 再要同级 `dsh-plugin.json`（`findManifest`）→ `declaredBackendsIn` → **一个 bundle 只能有一个 `Backend`**（`:149` `backends.length > 1` ⇒ 拒绝，理由 `'only one Backend per bundle can bind the fixed backend export'`）→ `backendManifestOf` → `registerBackend({manifest, load})`。

**失败是"一行 warning + 其它 bundle 继续"**，不 fatal。所以「装上了但 picker 里没有」的唯一正确排查路径是**看 debug 日志里那行 `dsh-tui: bundle "dsh-tui2go" declares a backend that was not registered: <reason>`**，不是猜。

---

## 4. Go 宿主检测 / 安装

### 4.1 环境变量契约（0.18 `src/dsh-go/backend.ts` 逐行核对）

| 变量 | 必需 | 默认 | 语义 | 代码位置 |
|---|---|---|---|---|
| `DSH_GO_HOME` | **是** | 无 | 独立于官方 DSH home 的会话根；**缺席时 `launchDshd` 直接抛** `'dsh-go: DSH_GO_HOME is required'` | `backend.ts:247-248` |
| `DSH_GO_DSHD` | 否 | `'dshd'`（走 PATH） | `dshd` 可执行文件路径 | `backend.ts:249`、`:320` |
| `DSH_GO_POLICY` | 否 | `'ask'` | `allow` / `ask` / `deny`；其它值**抛** `'dsh-go: unsupported DSH_GO_POLICY ...'` | `backend.ts:250-251` |
| `DSH_GO_MODEL` | 否 | 空 | 模型覆盖；空 = 用宿主默认 | `backend.ts:253`、`:938` |

**启动 argv**：`dshd serve --home <dir> --addr 127.0.0.1:0 --policy <policy>`。stdout **只**输出一行 `ready {"url":"http://127.0.0.1:<port>","token":"<一次性 token>"}`；stderr 原样交给 host（`BackendHost.stderr?`）。

**实测核对**（`./dshd.exe serve --help`，2026-11-08）：

```text
  -addr string        监听地址（默认 127.0.0.1:0，端口由 OS 分配）
  -home string        会话根目录（必填）
  -policy string      子进程用哪个审批策略：allow（默认，完全权限）/ask（转给界面）/deny (default "allow")
  -model string       模型 id（透传给子进程） (default "group/deepseek-v4.1-flash-1017")
  -reasoning string   推理强度：空=不要（默认）| xhigh | max
  -tools-mode string  工具表形态：compact（默认）| full | merged | lazy
```

⚠️ **默认值不一致，必须写进 README 并留断言**：`dshd` 的 `-policy` 默认是 `allow`，而本 bundle 在 `DSH_GO_POLICY` 缺席时传 `ask`。这是**故意的**（`docs/dsh-go-backend.md`：「不默认扩大权限」），但用户会看到"我什么都没设，怎么在弹审批"。README 必须写清。

### 4.2 检测（`detect`）

```ts
execFile(executable, ['--version'], { env, timeout: 3_000, maxBuffer: 64 * 1024, windowsHide: true })
```

- 成功：`installed: true`、`auth: 'unknown'`、`version` 从 `/\b\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?/` 提取。**实测** `./dshd.exe --version` → `DSH GO 0.2.2-go.1`（多行，含 `commit: dev` / `built: unknown`），正则命中 `0.2.2-go.1` ✅。
- 失败：**不抛**，返回 `{ installed: false, hint: <按 host.locale() 双语> }`（`backend.ts:336`）。
- `DSH_GO_HOME` 缺席但二进制在：`installed: true` + `hint: '设置 DSH_GO_HOME 后启动 DSH-GO。'`（`backend.ts:332`）。**这是"装了但没配"的合法中间态，不许报 `installed:false`。**

### 4.3 `dsh-go-host` + 平台包 与 `BackendInstallRecipe` 的关系（**这是全任务最容易造假的一节**）

**实测事实（2026-11-08 02:16）**：

```console
$ npm view dsh-go-host version
npm error code E404 ... 404 Not Found - GET https://registry.npmjs.org/dsh-go-host

$ npm view dsh-tui2go version
npm error code E404 ... 404 Not Found - GET https://registry.npmjs.org/dsh-tui2go
```

⇒ **`dsh-go-host` 与三个平台包（`win32-x64` / `linux-x64` / `darwin-arm64`）都不存在。** `dshd` 当前只以裸二进制分发：`DSH-GO/dshd.exe`（24,130,560 B）与 `dist/{windows-amd64,linux-amd64,darwin-arm64}/dsh-0.2.2-go.1[.exe]` + `dist/SHA256SUMS`。

**`BackendInstallRecipe` 到底能装什么（0.19 真实实现）**：

- 执行器表里**只有一个值**：`PNPM_PROFILE_ADD = 'pnpm-profile-add'`（`install/executors.js:4-8`）。
- 它的 `resolveTarget()`（`pnpm-profile-add.js:35-40`）：
  ```js
  if (isStandaloneRuntime()) return { kind: 'standalone' }
  const profile = resolveDshProfileName(argv) ?? (env.DSH_TUI_PROFILE?.trim() || undefined)
  return profile === undefined ? { kind: 'no-profile' } : { kind: 'profile', dir: dirname(profileWorkspaceYamlPath(profile)) }
  ```
- 它的 `start(specifier, dir)` **就是一句** `pnpm add <specifier>`，cwd = 官方 DSH profile 根；Windows 上走 `shell: true`（pnpm 是 `.cmd` 垫片，Node ≥22 不能直 spawn），`windowsHide: true`，stdio 全捕获（`pnpm-profile-add.js:44-98`）。
- 它**只认识 npm 包**：`specifier` 形如 `@scope/name@<version>`。

⇒ **三条结论，缺一不可**：

1. **`pnpm-profile-add` 装不了 `dshd` 的裸二进制。** 它只会往官方 DSH profile 的 `node_modules` 里 `pnpm add` 一个包。要让 picker 的「安装」按钮真的有用，`dshd` 必须**先**被发布成 `dsh-go-host`（meta 包）+ `dsh-go-host-win32-x64` / `-linux-x64` / `-darwin-arm64`（`optionalDependencies` + `os`/`cpu` 门），并且 meta 包的 `bin` 或 postinstall 把 `dshd` 落到可执行位。
2. **本包今天不能声明 `install`。** 声明了会让 `installableOf()` 返回 `true`（`backend-registry.js:52`，因为 `'pnpm-profile-add'` 在执行器表里），picker 行变成可点，Enter 打开向导，`pnpm add dsh-go-host@0.2.2-go.1` → **404**。这就是任务书明令禁止的**假按钮**。
3. **`resolveTarget()` 在独立启动路径上返回 `no-profile`。** `bin/dsh-tui-standalone.js --backend dsh-go --base … --token …` 场景下没有 `--profile`、也没有 `DSH_TUI_PROFILE`，向导只能显示"手动安装说明"。**这不是 bug，是设计**：独立宿主客户端不托管在 profile 里。

**所以缺宿主时的"可点击安装路径"是这一条（codex 式一等公民降级）**：

- **不声明 `spec.install`**（保持 `installable: false`）。
- `detect()` 返回 `installed: false` + **可操作**的 `hint`，picker 行置灰并把 hint 显示出来（`kernelCatalog.d.ts:14-29` 的 `KernelUnavailableReason` 与 `hint`）。
- `hint` 文案必须给**三条**：① 去哪拿（`https://github.com/MYCF711/dsh-go/releases` 的 `dist/<platform>/` + `SHA256SUMS`）；② 怎么验（`dshd --version` 应打印 `DSH GO 0.2.2-go.1`）；③ 怎么指（`export DSH_GO_DSHD=<abs path>` + `export DSH_GO_HOME=<dir>`）。
- 依据：`docs/agent-backend-design.md:357-358`「驱动系统 CLI 的后端……**不声明** `install` —— 声明了等于长出一个装不出任何东西的按钮——改为在 `detect()` 里报告 `installed` / `version` 与"缺什么、怎么补"的 hint」。

**当 `dsh-go-host` 真的发布之后**（**不在本任务范围**），才把 `spec.install` 改成：

```json
"install": { "executor": "pnpm-profile-add", "specifier": "dsh-go-host@0.2.2-go.1", "version": "0.2.2-go.1" }
```

并在 CI 里加一条门：`npm view dsh-go-host@<version> version` 必须 exit 0，否则**拒绝合并**这条 recipe。

### 4.4 "不能伪造安装成功"（写进验收的硬断言）

| 面 | 允许 | 禁止 |
|---|---|---|
| `detect()` | 只在 `dshd --version` 真 exit 0 **且**解析出版本时给 `installed: true` | 因为 `DSH_GO_HOME` 设了就给 `installed:true`；因为文件存在就给 `installed:true` |
| `open()` | 真起 `dshd serve`、真读 `ready` 行、真 `GET /connect?token=` | 起不来时回落到 `dsh` 内核；resume 失败时新建会话（`backend.ts:955-963` 明确禁止） |
| `install` | `SdkInstallResult` 只由**真实 `pnpm add` 的 exit code** 决定 | 在 `pnpm` 缺席（`kind:'pnpm-missing'`）时返回 `ok`；把 `no-profile` 当成"已安装" |
| `hint` | 如实说"没找到 dshd" | 说"已自动安装" |

---

## 5. 并行任务图

### 5.1 调度规则（抄本工作区）

- 角色：`lead`（只拆包/集成/验收）、`architect`（`group/kimi-k3.1-pro`，**只读**）、`backend` / `tui` / `qa` / `gui`（**`group/deepseek-v4.1-flash-1017`**）。
- **执行层一律 `group/deepseek-v4.1-flash-1017`，禁止继承 Kimi。**
- 同文件同一时刻只允许一个 active writer；默认**深度 ≤3、单父并发 ≤4**。
- 每任务开独立 worktree/分支 `task/<task_id>`，交付 binary patch + 证据；**不写共享树、不 commit、不 push、不 `git add -A`**。
- 产物根：`S=C:/Users/Administrator/AppData/Local/Temp/TUI2GO-20261108/<task_id>`。
- 长驻命令一律套 `timeout <秒>`（`timeout 300 …`），禁止裸跑不限时命令。

### 5.2 依赖图（**只有开头 1 处真并行，其余是硬串行链**）

```text
Wave 1 ──┬── T1 SCAFFOLD ─────────────────────────────┐
         └── T2 TYPES ──────┬───────────────────────┐│
                            ▼                       ││
Wave 2 ── T3 TRANSPORT ─────┼───────────────────┐   ││
         T6 ASSEMBLY (只依赖 T1) ─────────────┐  │   ││
                            ▼                  │  │   ││
Wave 3 ── T5 CAPABILITIES ─┐                  │  │   ││
                           ▼                  │  │   ││
Wave 4 ── T4 BACKEND ──────┴──────────────────┼──┼───┘│
                                              ▼  │    │
Wave 5 ── T7 CONFORMANCE ─────────────────────┼──┘    │
                                              ▼       │
Wave 6 ── T8 RELEASE ─────────────────────────┴───────┘
```

| 波次 | 可同轮并行 | 必须串行 |
|---|---|---|
| Wave 1 | `T1` ∥ `T2` | — |
| Wave 2 | `T3` ∥ `T6` | 两者都等 Wave 1 |
| Wave 3 | — | `T5` 等 `T3`（`session-operations.ts`/`subagents.ts` 要 `events.ts`/`task-events.ts` 的**值**导出） |
| Wave 4 | — | `T4` 等 `T3` + `T5` |
| Wave 5 | — | `T7` 等 `T1` + `T4` |
| Wave 6 | — | `T8` 等 `T7` |

**不要伪造并行。** 这个移植是**一条 import 链**：`types → transport/events → 能力模块 → backend 工厂 → 装配 → 真机`。除 Wave 1/2 外没有可拆的独立面。

### 5.3 任务卡

---

#### T1 · PKG-SCAFFOLD

| | |
|---|---|
| **owner** | `backend` |
| **model** | `group/deepseek-v4.1-flash-1017` |
| **写作用域** | `package.json`、`dsh-plugin.json`、`cordis.patch.yml`、`tsconfig.json`、`README.md`、`LICENSE`、`.gitignore`、`src/index.ts`、`src/manifest.ts`、`src/pool.ts`、`src/backend.stub.ts`（临时桩）、`src/types/agent/**`（只放空 `export {}`，让 T2 填）、`test/assembly/**`、`$S/**` |
| **依赖** | 无 |
| **先红** | `node -e "import('./lib/index.js')"` 在**未构建**时 exit 1（`ERR_MODULE_NOT_FOUND`）；`npx tsc -p tsconfig.json` 在桩未填时 exit 1。两者都必须在隔离副本里真 exit 1，环境错不算红。 |
| **验收命令** | ① `npm run build > $S/build.log 2>&1; echo "EXIT=$?"` → `EXIT=0`，产物含 `lib/index.js` 与 `lib/index.d.ts`<br>② `node -e "const m=await import('./lib/index.js'); if(!m.backend) throw new Error('no backend'); if(m.backend.id!=='dsh-go') throw new Error('bad id '+m.backend.id); console.log('OK', m.backend.id)"` → `OK dsh-go`<br>③ `node -e "const {validateBackendSpec}=await import('@deepseek-harness-tui/dsh-tui'); const s=JSON.parse(require('fs').readFileSync('dsh-plugin.json','utf8')); const spec=s.contributes['x-dsh-tui'][0].spec; console.log(JSON.stringify(validateBackendSpec(spec)))"` → 不抛<br>④ `npx tsc -p tsconfig.json --noEmit > $S/typecheck.log 2>&1; echo "EXIT=$?"` → `EXIT=0`<br>⑤ `npm pack --dry-run > $S/pack.log 2>&1; echo "EXIT=$?"` → `EXIT=0`，且 tarball 含 `dsh-plugin.json` 与 `cordis.patch.yml`<br>⑥ `git diff --check; echo "EXIT=$?"` → `EXIT=0` |
| **产出** | `$S/RESULT.md`、binary patch、`build.log`/`typecheck.log`/`pack.log`、`pack-contents.txt`、worktree 证据（`git rev-parse --show-toplevel` / `--short` / `HEAD`）、`WIRING.md`（无则 `NO_WIRING.md` + 理由） |

**必做要点**：`docs` 里的 `package.json` / `dsh-plugin.json` 必须**逐字段**照 §3.1 / §3.3 抄；`spec.capabilities` 与 `manifest.ts` 的 `capabilities` 必须逐字一致（用一条测试钉住）。`tsconfig.json` 抄 0.18 的（`target: es2024`、`module: esnext`、`moduleResolution: bundler`、`rootDir: src`、`outDir: lib`、`declaration: true`）。

---

#### T2 · TYPES-VENDOR

| | |
|---|---|
| **owner** | `backend` |
| **model** | `group/deepseek-v4.1-flash-1017` |
| **写作用域** | `src/types/agent/*.d.ts`（8 个）、`src/image-size.ts`、`test/unit/types-quality.test.ts`、`$S/**` |
| **依赖** | 无（与 T1 同轮） |
| **先红** | 在 `src/types/agent/` 为空时，故意写一个 `import type { AgentBackend } from '@deepseek-harness-tui/dsh-tui/agent/backend.js'` 的探针文件，`npx tsc --noEmit` 必须报 **`TS2307: Cannot find module ... or its corresponding type declarations`**（证明 `exports` 真的挡住了）。这个红必须**留下日志**，不许删。 |
| **验收命令** | ① `diff -q <0.18>/lib/types/agent/backend.d.ts src/types/agent/backend.d.ts` 等 7 个文件 → **全部相同**（`echo "EXIT=$?"` = 0）<br>② `diff -q <0.18>/src/backends/shared/image-size.ts src/image-size.ts` → 相同<br>③ `grep -rn "from '@deepseek-harness-tui/dsh-tui" src/ \| grep -v "adapter/spec" \| grep -v "dsh-adapter/backend-admission" \| grep -v "dsh-adapter/backend-contribution"` → **无输出**（`echo "EXIT=$?"` = 1）<br>④ `grep -rn "^import.*[^t]ype " src/types/agent/` → 无输出（vendor 的必须是纯 `.d.ts`，零运行时）<br>⑤ `npx tsc -p tsconfig.json --noEmit; echo "EXIT=$?"` → `EXIT=0` |
| **产出** | `$S/RESULT.md`、patch、7 个 `diff -q` 的原始输出、`exports-blocked.log`（那条 TS2307）、`import-audit.txt` |

**必做要点**：只 vendor 这 8 个 `.d.ts`：`backend` / `session` / `capabilities` / `events` / `refs` / `presentation` / `backend-manifest` / `channel-view`（后两个是 `backend.d.ts` 与 `index.d.ts` 的传递依赖，必须一起）。**不要**把 `agent/index.d.ts` 的 barrel 也 vendor 进来——它会把 `adapter/ports/*` 拖进来，而那是宿主内部面。`formatSessionRef` / `parseSessionRef` / `sameSessionRef` 是**运行时**函数（`refs.js` 有实现），必须在 `src/refs.ts` 里手写 10 行等价物，不许 import。

---

#### T3 · TRANSPORT

| | |
|---|---|
| **owner** | `backend` |
| **model** | `group/deepseek-v4.1-flash-1017` |
| **写作用域** | `src/transport.ts`、`src/events.ts`、`src/step-stream.ts`、`src/view-spec.ts`、`src/task-events.ts`、`src/trajectory-history.ts`、`src/refs.ts`、`test/unit/{transport,events,step-stream,view-spec,task-events,trajectory-history}.test.ts`、`$S/**` |
| **依赖** | `T2` |
| **先红** | 从 0.18 的 `src/dsh-go/transport.test.ts`（670 行）里挑**至少 3 条**在移植前必然失败的：① `journalEpoch` 必须是**文本**（`'9007199254740993'` 往返不许丢位，`Number` 会变 `9007199254740992`）；② 未 `GET /connect?token=` 换到 `dshd_session` cookie 时 `call()` 必须失败（不许带空凭据继续）；③ 失败响应四字段 `{error:{code,message,hint,retryable}}` → `DshGoTransportError` 必须带 `status` 与可选 `journalEpoch`/`lastSeq`。隔离副本里真 exit 1。 |
| **验收命令** | ① `timeout 300 node --import tsx/esm --test test/unit/transport.test.ts > $S/transport.log 2>&1; echo "EXIT=$?"` → `EXIT=0`，且 `# fail 0`<br>② 同法跑 `events` / `step-stream` / `view-spec` / `task-events` / `trajectory-history` → 各自 `EXIT=0`<br>③ **真实 socket 用例**：起一个本地 `http.createServer` + `WebSocketServer`（0.18 `backend.test.ts` 里的夹具，1:1 搬），跑 `sinceSeq` 重连与 `session/reset` 重基 → 断言"重连后不重放、不丢帧"<br>④ `grep -c "from 'ws'" src/transport.ts` → `1` |
| **产出** | `$S/RESULT.md`、patch、6 份 `*.log`、`wire-traces/*.json`（每次 HTTP/WS 的请求响应原始帧，token 打码） |

**必做要点**：`ws` 用 **8.x**（`"ws": "^8.21.3"`，与 0.19 依赖表一致）。`journalEpoch` 的 `JSON.parse` reviver **必须保留原始文本**——这是 0.18 里最贵的一条教训（63 位十进制超出 `Number.MAX_SAFE_INTEGER`）。

---

#### T4 · BACKEND-CORE

> **T2 实测补注（Lead 裁定，2026-11-08 02:34）**
> - 接受第 9 个类型文件 `channel-session.d.ts`：`backend.d.ts:5` 的 `../adapter/ports/channel-session.js` 若不留同目录副本，类型图必断；禁止删成死引用。
> - 0.18↔0.19 的 9 个类型文件是 **9/9 逐字节相同**，不是任务书早先写的“7 个相同 + channel-view 差异”；以实测为准。
> - 纯 `.d.ts` 不产 `.js`，以下 **8 个 `export declare` 值成员必须在 T4/T5 手写运行时实现**，不得从 `.d.ts` 直接 import 值：
>   - `refs.d.ts:24/30/37/39`：`DEFAULT_BACKEND_ID`、`formatSessionRef`、`parseSessionRef`、`sameSessionRef`
>   - `presentation.d.ts:28`：`isSuppressedPresentation`
>   - `events.d.ts:754`：`NO_EVENTS`
>   - `backend-manifest.d.ts:134/150`：`BACKEND_ID_PATTERN`、`isBackendIdSyntax`
> - T2 未建 `test/unit/types-quality.test.ts`；由 T4/T5 在各自单测里覆盖运行时值，或 T7 补一条消费者探针，不单独重开 T2。

| | |
|---|---|
| **owner** | `backend` |
| **model** | `group/deepseek-v4.1-flash-1017` |
| **写作用域** | `src/backend.ts`、`src/index.ts`（填实）、`test/unit/{backend,backend-integration,backend-selection}.test.ts`、`$S/**` |
| **依赖** | `T3`、`T5` |
| **先红** | ① `createDshGoBackend()` 的 `id` 必须是 `'dsh-go'`（`loadBackend` 会 `backend.id !== id` 抛）；② resume 失败**绝不新建**（`session.load` 抛 → `open` 抛，不回落 `session.create`）；③ `submit` 只接受 `'turn'`，另三个 **在发 HTTP 之前**拒绝且 `dropped=true`；④ `cancel` 成功 → `outcome:'unknown'`、失败 → `'failed'`，**不许**伪造 `'confirmed'`。四条在移植前真 exit 1。 |
| **验收命令** | ① `timeout 300 node --import tsx/esm --test test/unit/backend.test.ts > $S/backend.log 2>&1; echo "EXIT=$?"` → `EXIT=0`<br>② 同法 `backend-integration.test.ts` → `EXIT=0`（这条覆盖 A/B 隔离：两个 session 的 translator / pending / in-flight / 订阅 / watermark 互不串）<br>③ `timeout 300 node --import tsx/esm --test test/unit/backend-selection.test.ts` → `EXIT=0`<br>④ **cleanShutdown**：`node -e "…createDshGoBackend(); open(); dispose(); 检查 process._getActiveHandles().length 与 interval 数回到基线…"` → 断言无残留 `WebSocket`/`ChildProcess`<br>⑤ `npx tsc --noEmit; echo "EXIT=$?"` → `EXIT=0` |
| **产出** | `$S/RESULT.md`、patch、`backend.log`/`backend-integration.log`/`backend-selection.log`、`shutdown-handles.json`（dispose 前后 handle 快照） |

**必做要点**：`DshGoSession` 的 `capabilities` 返回的键集**必须是 `manifest.capabilities` 的子集**（`backend-manifest.d.ts` 的 `capabilities` 注释：声明必须**覆盖**运行时实际服务的键集）。这条在 §6.3 的 D-2 里再钉一次。

---

#### T5 · CAPABILITIES

| | |
|---|---|
| **owner** | `backend` |
| **model** | `group/deepseek-v4.1-flash-1017` |
| **写作用域** | `src/{attachments,auth,color,compact,context,effort,goals,images,init,mcp,modes,placement,rename,session-operations,side-query,subagents,tasks}.ts`（17 个）、`test/unit/{同名}.test.ts`、`$S/**` |
| **依赖** | `T3` |
| **先红** | 至少 3 条：① `images.ts` 的图片嗅探必须拒未知格式（`probeImageSize` 返回 `null` ⇒ **强制解码**，不许当"够小"）；② `placement.ts` 的 `clientMessageId` 竞态（`turn/end` 早于 ACK 时不许在 ACK 后重新混入取消快照）；③ `rename.ts` / `color.ts` 的标题/颜色变更必须只经 `AgentEvent` 表达，不许旁路。隔离副本真 exit 1。 |
| **验收命令** | ① 逐个 `timeout 300 node --import tsx/esm --test test/unit/<name>.test.ts > $S/<name>.log 2>&1; echo "EXIT=$?"` → **17 个全 `EXIT=0`**<br>② 汇总：`for f in $S/*.log; do echo "$f $(grep -c '^# fail 0' $f)"; done` → 每行末位 `1`<br>③ `npx tsc --noEmit; echo "EXIT=$?"` → `EXIT=0` |
| **产出** | `$S/RESULT.md`、patch、17 份 `*.log`、`capability-matrix.json`（能力名 → 实现文件 → 测试文件 → 通过/未做） |

**必做要点**：`src/image-size.ts` 用 T2 的 vendor 副本，**不许** `import '../backends/shared/image-size.js'`（宿主内部面）。**17 个能力模块全部只依赖 `./types/agent/*` 与同目录兄弟**，一条 `grep -rn "\.\./" src/*.ts` 都不该出现。

---

#### T6 · ASSEMBLY（0.19 真实装配链，**只依赖 T1**）

| | |
|---|---|
| **owner** | `qa` |
| **model** | `group/deepseek-v4.1-flash-1017` |
| **写作用域** | `test/assembly/**`、`scripts/verify-assembly.mjs`、`$S/**` |
| **依赖** | `T1`（可以用 T1 的桩 handler；T4 落地后原样复跑） |
| **先红** | ① 用 T1 的 tarball 装进一个**一次性 profile**（`mkdtemp`），不写 `dsh.profile.bundles` → `assembleBackendsFromProfile(ctx)` 返回 `registered: []`（证明"装了不重启/不写列表 = 不生效"）；② 写进列表但 `package.json` 缺 `dsh.bundle` → 依然 `registered: []`；③ `contributes['x-dsh-tui']` 放**两个** `Backend` → 必须被拒且 reason 含 `only one Backend per bundle can bind the fixed backend export`（`backend-assembly.js:149`）。三条真 exit 1。 |

> **T6 实测更正（Lead 裁定，2026-11-08 03:00）**
> 真实 0.19 装配**不读 `package.json.dsh.bundle`**；`dsh.bundle` 只出现在 `backend-assembly.js:205` 注释里。真正的层门是宿主安装链写进 profile 的 `dsh.profile.bundles`（`installedBundlesOf()`，`backend-assembly.js:209-222`）。
> - 三体对照（本包去掉 `dsh.bundle` / fixture 无 / fixture 有，三者都在 `dsh.profile.bundles`）→ **三个全部注册**；`dsh.bundle` 是宿主 `/plugin install` 的 `reconcile()` 事实，不是 TUI 侧装配事实。
> - E-2 改为：**不在 `dsh.profile.bundles` 里 ⇒ 不成为层**；"缺 `dsh.bundle`"移到宿主安装链层验证。
> - T6 按原派单保留 ② 为回归探针（当前真实 exit 1），哪天 0.19 真按它关门会翻绿。

| **验收命令** | ① `timeout 300 node scripts/verify-assembly.mjs > $S/assembly.log 2>&1; echo "EXIT=$?"` → `EXIT=0`<br>② 日志里必须出现 `PASS` 行（逐条）：`bundle-installed-as-layer`、`entry-resolved-through-loader-resolver`、`manifest-found-beside-package-root`、`registered-as-dsh-go`、`inTree-false`、`label-literal`、`no-alwaysAvailable`、`no-nativeKey`、`no-backendExport`、`listed-in-listOfferedBackends`、`loadBackend-returns-handler`、`handler-id-equals-spec-id`、`lazy-import-never-happened-before-select`、`unloadExport-called-exactly-once`、`second-pass-is-noop`、`two-backends-refused`<br>③ `grep -c "FAIL " $S/assembly.log` → `0`<br>④ 真实 0.19 装配（**不是 mock**）：`node --import tsx/esm <0.19>/bin/dsh-tui.js --help` exit 0，且 `grep -c "dsh-go" <输出>` 由 registry 断言（不是文本 grep） |
| **产出** | `$S/RESULT.md`、`assembly.log`、`assembly.json`（逐条 check + 证据路径）、`profile-tree.txt`（临时 profile 的 `package.json` + `node_modules/dsh-tui2go/` 快照）、`red.log`（三条先红原始输出） |

**必做要点**：这个脚本就是 0.18 `scripts/verify-backend-assembly.ts`（120 行头 + 大量 case）的第三方版。**它跑的必须是 0.19 的真实 `assembleBackendsFromProfile` / `registerBackend` / `loadBackend`**，不是本仓自己的复制品——所以它 `import` 的是 `C:/Users/Administrator/AppData/Local/Temp/tui-019-run/package/lib/types/dsh-adapter/*.js`（只读）。这条是 T6 与 T7 的分界：**T6 证明"装配进 picker"，T7 证明"真能跑 turn"**。

---

#### T7 · CONFORMANCE（真实 TUI + 真实 dshd）

| | |
|---|---|
| **owner** | `qa` |
| **model** | `group/deepseek-v4.1-flash-1017` |
| **写作用域** | `test/conformance/**`、`scripts/conformance.mjs`、`scripts/conformance-tui.mjs`、`scripts/conformance-report.mjs`、`$S/**` |
| **依赖** | `T1`、`T4`（+`T5` 全绿） |
| **先红** | 在**没有** `DSH_GO_HOME` 的环境里启动真实 TUI，`/kernel` 里 `DSH-GO` 行必须置灰且带 hint（不是"可选但一选就崩"）；在**有** `DSH_GO_DSHD` 但二进制是假的环境里，`open()` 必须抛 `dsh-go: session.load returned ...` 或启动失败，**绝不**回落 `dsh` 内核。两条真 exit 1。 |
| **验收命令** | 见 §6 全表。核心一条：`timeout 600 node scripts/conformance-tui.mjs > $S/tui.log 2>&1; echo "EXIT=$?"` → `EXIT=0` 且 `W110_TUI_FAIL` 计数为 `0` |
| **产出** | `$S/RESULT.md`、`tui.log`（含屏幕转义原文）、`tui.json`（逐 check 结构化）、`wire.json`（全量 HTTP/WS 原始帧，token 打码）、`report.json`（P0 矩阵，见 §6.6）、`processes.json`（本任务起的 PID，收尾必须全灭） |

**必做要点**：**直接移植 0.18 的 W110 三件套**——`src/dsh-go/conformance.ts`（559 行，真 `dshd` + 真 HTTP/WS + 真模型）、`src/dsh-go/conformance-tui.mjs`（`@xterm/headless` + `node-pty` + ConPTY，真 TUI 渲染）、`src/dsh-go/conformance-report.mjs`（P0 矩阵 + 命令退出码汇总）。**只改两处**：① `../agent/*` → `./types/agent/*`；② `../dsh-adapter/backend-registry.js` 的 `loadBackend`/`parseBackendChoice` 改成从 0.19 包导入（只读）。`W110_ARTIFACT_ROOT` 指到 `$S`。

---

#### T8 · RELEASE

| | |
|---|---|
| **owner** | `lead`（**独占**，不外包） |
| **model** | `group/deepseek-v4.1-flash-1017` |
| **写作用域** | 仓库 `.github/**`、`README.md` 发布段、`CHANGELOG.md`、GitHub repo settings（topics）、Release `v0.1.0`、`$S/**` |
| **依赖** | `T7` 全绿 |
| **先红** | `npm pack --dry-run --json` 的 `files` 列表里**必须**含 `dsh-plugin.json`、`cordis.patch.yml`、`lib/index.js`；缺任一 → exit 1。`node -e "const p=require('./package.json'); if(p.name!=='dsh-tui2go') process.exit(1); if(!p.dsh?.bundle?.patch) process.exit(1)"` 必须 exit 0。 |
| **验收命令** | ① `npm pack --dry-run --json > $S/pack.json 2>&1; echo "EXIT=$?"` → `EXIT=0`，`files` 含上述三项<br>② `node -e "const p=require('./package.json');console.log(p.name,p.version,p.dsh.bundle.patch)"` → `dsh-tui2go 0.1.0 ./cordis.patch.yml`<br>③ `node -e "const s=require('./dsh-plugin.json');console.log(s.contributes['x-dsh-tui'][0].spec.id)"` → `dsh-go`<br>④ `gh repo view MYCF711/dsh-tui2go --json repositoryTopics` → 含 `dsh-tui-plugin` / `dsh-plugin` / `dsh-tui` / `dsh-go` 四个<br>⑤ `gh release view v0.1.0 --repo MYCF711/dsh-tui2go` → exit 0<br>⑥ `npm view dsh-tui2go version` → `0.1.0`（**发布后**才有；发布前允许 E404，但不许假报） |
| **产出** | `$S/RESULT.md`、`pack.json`、`release-url.txt`、`topics.json`、`publish.log`（真实 `npm publish` 输出，**失败就保留失败**） |

**发布前必须由用户当次明确授权**（`AGENTS.md` §4：两仓 push 都需用户当次确认；发布 npm 同级别）。

---

## 6. 验收门（全表）

> **总则**：mock / 单测**不能替代**真实 TUI + 真实 `dshd`。§6.1–6.4 只证明"自己没写错"，§6.5 才证明"真的能用"。任何一栏 `UNVERIFIED` 都不许写 `DONE`。

### 6.1 构建

```bash
npm ci                                                      # 或 pnpm install --frozen-lockfile
npx tsc -p tsconfig.json > $S/build.log 2>&1; echo "EXIT=$?"   # 必须 0
ls lib/index.js lib/index.d.ts lib/backend.js               # 三个都在
npm pack --dry-run --json > $S/pack.json 2>&1; echo "EXIT=$?" # 必须 0
```

### 6.2 类型检查

```bash
npx tsc -p tsconfig.json --noEmit > $S/typecheck.log 2>&1; echo "EXIT=$?"   # 必须 0
# 反向门：包内不许出现对 TUI 内部面的类型 import（BackendSpec 家族除外）
grep -rn "from '@deepseek-harness-tui/dsh-tui" src/ \
  | grep -v 'adapter/spec' | grep -v 'backend-admission' | grep -v 'backend-contribution'
# 上面必须无输出（exit 1）
```

### 6.3 0.19 真实装配（T6，**不用 mock**）

| # | 检查 | 通过判据 |
|---|---|---|
| D-1 | `dsh-plugin.json.spec` 与 `manifest.ts` 逐字一致 | 一条 `assert.deepEqual` 测试 |
| D-2 | `manifest.capabilities ⊇ 运行时 session.capabilities 键集` | 真开会话拿 `session.capabilities` 的 `Object.keys`，做子集断言 |
| D-3 | `validateBackendSpec(spec)` 不抛 | `EXIT=0` |
| D-4 | `backendManifestOf(spec)` 的 `inTree === false` | 深比较 |
| D-5 | `registerBackend` 接受（不抛 reserved / duplicate / label / alwaysAvailable / nativeKey） | 无异常 |
| D-6 | `listOfferedBackends()` 含 `dsh-go`（**不是 `waiting_authorization`**） | 成员断言 |
| D-7 | `loadBackend('dsh-go')` 返回 handler 且 `handler.id === 'dsh-go'` | 断言 |
| D-8 | `import` 发生在**选中之后**（lazy） | 首次 `listBackends()` 后入口模块的 marker 文件**不存在**；`loadBackend` 后存在 |
| D-9 | `unloadExport` 恰好被调 1 次，且在 `dispose` 之后 | 计数器 |
| D-10 | 二次装配是 no-op（不抛 "already registered"） | 连跑两遍 `assembleBackendsFromProfile` |
| D-11 | 一个 bundle 声明两个 Backend 被拒 | reason 含 `only one Backend` |

### 6.4 装配失败的可归因性

| # | 检查 | 通过判据 |
|---|---|---|
| E-1 | 坏 `dsh-plugin.json`（非法 JSON）⇒ 一行 warning + 其它 bundle 照常 | 日志含 `declares a backend that was not registered: the declaration is not valid JSON` |
| E-2 | 不在层列表 ⇒ 不成为层（T6 更正：`dsh.bundle` 只是宿主安装链事实） | `dsh.profile.bundles` 里没有它 |
| E-3 | `contributes['x-dsh-tui']` 不是数组 ⇒ `problems` 含 `must be an array` | 断言 |
| E-4 | 保留 id（`dsh`/`claude`/`codex`）⇒ 拒 | `BACKEND_ID_RESERVED` |

### 6.5 真实 TUI + 真实 `dshd`（T7，**这一节才叫"能用"**）

环境（照 0.18 W110 原样）：

```bash
export HOME=$(mktemp -d) USERPROFILE=$HOME
export DSH_HOME=$HOME/.dsh
export DSH_TUI_SESSION_ROOT=$HOME/sessions
export DSH_TUI_NO_DELEGATE=1
export DSH_TUI_LANG=en
export DSH_GO_DSHD=D:/DeepSeek Harness/default-workspace/DSH-GO/dshd.exe
export DSH_GO_HOME=$HOME/go-home
export DSH_GO_POLICY=ask
export DSHD_APPROVAL_TIMEOUT_SEC=3
export NODE_ENV=production TERM=xterm-256color
# 把本包 junction 进一次性 profile 的 node_modules，再写 dsh.profile.bundles
```

| # | 门 | 命令 / 判据 |
|---|---|---|
| G-1 | **真实 TUI 加载** | `timeout 120 node <0.19>/bin/dsh-tui.js --backend dsh-go --dump-screen --size 100x30` → `EXIT=0`，屏幕上出现 `DSH-GO` 短名 |
| G-2 | **`/kernel` picker** | 交互式驱动 `/kernel` → 选项含 `dsh-go`；当前项打勾；不是灰行（因为 `installed:true`） |
| G-3 | **`detect`** | picker 行的 version 字段 = `0.2.2-go.1`（来自 `dshd --version`） |
| G-4 | **`open`** | 选中后真的起一个 `dshd serve` 子进程（`processes.json` 里 `ExecutablePath` 等于 `DSH_GO_DSHD`）；`ready` 行被解析出 `url` + `token` |
| G-5 | **`/connect`** | `GET /connect?token=<一次性>` 返回 `set-cookie: dshd_session=…`；**同一 token 第二次必须失败**（一次性） |
| G-6 | **`/ws`** | `sub` 带 `topic:"session:<id>"` + `sinceSeq` + `journalEpoch`；`ping`/`pong`；`seq=0` 是合法帧且**不推进水位** |
| G-7 | **真实 Go 宿主 turn** | 发一条 prompt → 收到 `turn/start` → `assistant/message`（**真实模型文本**，不是合成 delta）→ `turn/end` → `turn/done`；`session.history` 与 live 帧同一 journal |
| G-8 | **审批三态** | `allow-once` / `allow-always` / `reject` 各自落到 `session.approve` 的 `allow_once` / `allow_always` / `reject_once`；reject 的**自由文本不进 `session.approve`**（界面明确提示"拒绝已发、反馈未传"） |
| G-9 | **超时** | `DSHD_APPROVAL_TIMEOUT_SEC=3` ⇒ 3 秒后 `permission-settled` 且**无写入** |
| G-10 | **取消** | `session.cancel` 成功 → `outcome:'unknown'`；失败 → `'failed'`；**不许** `'confirmed'` 除非真有回执 |
| G-11 | **缺宿主** | `DSH_GO_DSHD` 指向不存在路径 ⇒ picker 行置灰 + hint 含 GitHub Releases 与 `DSH_GO_DSHD`；**不崩溃**、**不回落 dsh 内核** |
| G-12 | **缺 `DSH_GO_HOME`** | picker 行**可选**（`installed:true`）但 hint 说"设置 DSH_GO_HOME"；选中后 `open()` 抛 `dsh-go: DSH_GO_HOME is required` |
| G-13 | **错误** | `dshd` 启动即退 ⇒ `BackendDetection` 的失败路径 + `open()` 抛；**不许**静默降级 |
| G-14 | **超时** | `dshd` 起得来但不回 `ready`（`readyTimeoutMs` 内）⇒ `launchDshd` 抛超时，子进程被回收 |
| G-15 | **cleanShutdown** | 退出 TUI 后：`dshd` 子进程**全部**退出（`processes.json` 逐个 PID 查 `Get-Process` 全空）；`handle` 快照回到基线；`unloadExport` 被调 1 次 |

**G-7 是唯一不能省的**：它同时证明"真实 Go 宿主 turn"和"事件只来自 load/WS，不合成"。**任何 mock 夹具都不能记这一条。**

### 6.6 P0 矩阵（照 0.18 `conformance-report.mjs` 的 `p0Definition`）

| P0 组 | 必须全 PASS 的检查 |
|---|---|
| `backend-selectable-and-tui-connect` | `backend-selection.real-registry`、`mount-and-real-session`、`selected-backend-spawned-real-dshd`、`real-create-load-connect`、`quit-exit-zero` |
| `connect-create-list-load` | `connect.token-single-use`、`create.distinct-ids`、`list.contains-created`、`load.ready-baseline` |
| `prompt-text-tool-call-result` | `A-shared-1.completed-frame`、`text.A-real-response`、`allow.tool-call-result`、`allow.real-write` |
| `approval-allow-once-and-always` | `allow.permission-settled`、`allow.tool-success`、`allow-always.second-not-asked`、`allow-always.second-write`、`allow-always.one-http-approval`、`allow-always.two-successful-tools` |
| `approval-reject` | `reject.permission-settled`、`reject.tool-refusal-truth`、`reject.no-write`、`reject.feedback-honest-notice` |
| `approval-timeout` | `timeout.permission-settled`、`timeout.real-host-frame`、`timeout.no-write` |
| `approval-cancel-and-turn-cancel` | `cancel.receipt-honest`、`cancel.permission-settled`、`cancel.real-host-frame`、`cancel.no-write`、`cancel-second-turn.text` |
| `ws-history-gap-and-reconnect` | `gap.real-frame-dropped`、`gap.session-history-called`、`gap.restored-text-once`、`reconnect.no-new-launch-token`、`reconnect.new-turn-text` |
| `host-restart-reset-epoch-rebaseline` | `restart.catalog-persisted`、`restart.session-reset`、`restart.epoch-changed`、`restart.rebaseline-history`、`restart.old-epoch-subscription`、`restart.new-epoch-subscription`、`restart.new-turn-text` |
| `AB-watermark-isolation` | `AB.no-cross-talk`、`AB.independent-watermarks`、`AB.real-independent-seq` |
| `resume-failure-never-creates` | `resume-missing.no-create`、`resume-missing.completed` |
| `same-text-distinct-identity-no-duplicate` | `identity.same-text-twice-not-dropped`、`identity.assistant-same-text-distinct-v4`、`text.A-real-response`、`text.B-real-response` |

汇总规则（原样）：`overall = requiredCommandsOkay && p0.every(PASS) && realReport.summary.fail === 0 ? 'PASS' : 'FAIL'`。**`NOT_RUN` 不得记 PASS。**

### 6.7 明确"不算通过"的形态

- 只有 `npm test` 绿、没有 G-7 ⇒ **不算**。
- 用 mock `dshd`（本地 `http.createServer`）跑 G-5/G-6 ⇒ **只证明协议层**，不抵扣 G-7。
- `--dump-screen` 出图但没发过 turn ⇒ **只证明渲染**。
- `assembleBackendsFromProfile` 用本仓复制品跑通 ⇒ **不算**（必须用 0.19 的只读实现，见 T6 必做要点）。
- 日志里出现 `UNVERIFIED` / `still` / `known issue` / `未做` 而不登记 ⇒ 不算（`AGENTS.md` §5）。

---

## 7. 发布契约

### 7.1 三个键，各管一件事

| 键 | 值 | 谁在读 | 读错了会怎样 |
|---|---|---|---|
| **npm 包名** | `dsh-tui2go` | 用户 `pnpm add` / `/plugin install dsh-tui2go` | 装不上 |
| **`dsh-plugin.json` 的 `id`** | `dsh-tui2go`（namespaced id，`^[a-z][a-z0-9]*(?:[.-][a-z0-9][a-z0-9-]*)+$`） | `projectManifest()` 规范投影 | 清单被拒 |
| **`spec.id`（backend id）** | `dsh-go` | `--backend dsh-go`、`AgentSessionRef.backendId`、`~/.dsh-tui/backends/dsh-go/` | 选不中 |
| **GitHub topics** | `dsh-tui-plugin` / `dsh-plugin` / `dsh-tui` / `dsh-go` | **社区发现**（GitHub 搜索） | 人搜不到 |
| **Release tag** | `v0.1.0` | 人 / CI 的版本锚点 | 对不齐 |

**关键区分**：

- **tag 是社区发现键**。它不参与任何运行时解析——`resolveEntryUrl` 只看包名与 `exports`。tag 的作用是让人在 GitHub 上搜到、并让 `npm view` 的 `gitHead` 能对回一个可审计的提交。
- **包名是安装键**。`pnpm add dsh-tui2go` 之后，`dsh.profile.bundles` 里写的是**包名**，`findManifest` 也是**按包名解析出的入口**往上找 `package.json`。所以：**改名 = 断链**。`dsh-tui2go` 一旦发布，`spec.id`（`dsh-go`）可以永远不变，但包名不能动。
- **`spec.id` 是身份键**。`loadBackend` 的 `backend.id !== id` 会抛；`backendDataDir(id)` 会落到 `~/.dsh-tui/backends/dsh-go/`。`dsh-go` 不是保留 id（保留集只有 `dsh`/`claude`/`codex`），合法。

### 7.2 包根必须有的文件（少一个就装不上）

```text
package.json          name=dsh-tui2go, dsh.bundle.patch 指向 cordis.patch.yml, exports["."] → lib/index.js
dsh-plugin.json       包根声明（必须在 package.json 同级 —— findManifest 的判据）
cordis.patch.yml      `[]`
lib/index.js          构建产物，导出 `backend`（+ `closeDshGoPool`）
lib/index.d.ts
README.md
LICENSE
```

### 7.3 GitHub 仓库

- 仓库名：`MYCF711/dsh-tui2go`。
- Topics：`dsh-tui-plugin`、`dsh-plugin`、`dsh-tui`、`dsh-go`（**四个都给**；topic 是被人搜到的唯一入口）。
- Release：tag `v0.1.0`，正文必须写清 ① 要求 TUI `^0.19.0`；② 要求 `dshd`（`dsh-go-host` 未发布，需手工装）；③ 三个环境变量；④ 已知未验项（§8）。

### 7.4 版本语义

- 包版本从 `0.1.0` 起（不是 `0.19.x`——那是 TUI 的版本线，跟错会让人以为要配 0.19 的某个 TUI 补丁）。
- 与宿主版本的对应关系写在 README 的兼容表里：`dsh-tui2go 0.1.x ↔ DSH-GO 0.2.2-go.* ↔ dsh-tui ^0.19.0`。
- **TUI 侧没有 `engines.dsh`**；兼容性靠 peer 表 + README 说明，不靠一个不存在的字段。

---

## 8. 风险与未验

### 8.1 六条已知风险（按"会不会让任务白干"排序）

| # | 风险 | 现实 | 降级 / 回滚 |
|---|---|---|---|
| R1 | **0.19 源码 checkout 无构建** | `DSH-GO/tmp/dsh-TUI-pr2/` 只有 `package.json`/`scripts`/`src`/`standalone`/`tui-profile`/`vendor`，**没有 `bin`/`lib`/`node_modules`**。它**不能**直接跑。 | 所有"0.19 真机"验收**一律用官方 npm 包** `C:/Users/Administrator/AppData/Local/Temp/tui-019-run/package`（有 `bin`/`lib`/`node_modules`）。pr2 只当源码参考。**这是一条硬约束，写进 T7 的环境准备。** |
| R2 | **0.18 依赖 workspace 协议** | 0.18 `package.json` 的 `@dsh-std/*` 与 `@dsh-tui-vendor/*` 全是 `workspace:*`，在 0.18 checkout 里 `pnpm install` 会去解 workspace；本包**不碰**这些（只用 `ws`）。 | 本包 `dependencies` 只有 `ws`。**如果 T3 移植时发现任何 `@dsh-std/*` 或 `@dsh-tui-vendor/*` 的 import，立刻停下报 BLOCKER**——那说明碰到了宿主内部面。 |
| R3 | **0.19 包 peer 范围** | 0.19 的 `peerDependencies` 有 40+ 个 `@deepseek-ai/dsh-*`，全是 `optional: true`。本包**不声明任何 peer**（§3.1），所以这些范围对我们无影响。但**如果**有人后来为了"类型方便"加了 `@deepseek-harness-tui/dsh-tui` 的 peer，就会把 TUI 版本钉死。 | 保持零 peer。需要类型就 vendor（T2 已经做了）。 |
| R4 | **Windows 编码** | `dshd` 的 `--version` 输出是 UTF-8（`DSH GO 0.2.2-go.1` ✅），但 `serve --help` 是中文 GBK/OEM，`execFile` 默认按 buffer 收。**`detect` 只解析 `/\b\d+\.\d+\.\d+…/`，不受编码影响**；但任何"把 stderr 原样打印"的调试路径会花屏。 | `BackendHost.stderr` 收的是**行**，由宿主决定编码。本包不主动解码；调试日志里只写 `Buffer.byteLength`，不写内容。**真机验一条：`DSH_GO_DSHD` 指向中文路径时 `execFile` 不炸**（Windows 上路径编码是独立问题）。 |
| R5 | **并发 profile** | 整份 0.19 只允许**一个**进程驱动一个 session（`reserveMount` 的 mount claim：两个进程驱动一个 session 会 interleave transcript）。本包的 supervisor 按 **cwd** 复用（`cwdKey`），同 cwd 多 session 共享一个 `dshd`。 | 验收里**必须**有一条 A/B 隔离（§6.6 的 `AB-watermark-isolation`）。跨进程并发不在本包职责内——那是 TUI 的 mount claim 的事，**不要在本包重造**。 |
| R6 | **安装权限** | `pnpm-profile-add` 在 Windows 上走 `shell: true`（pnpm 是 `.cmd` 垫片，Node ≥22 拒直 spawn，DEP0190）。若 profile 目录在受保护路径（如 `C:\Program Files`），`pnpm add` 会因权限失败。 | 本包**不声明 `install`**（§4.3），所以这条今天不触发。等 `dsh-go-host` 发布后再验；失败时必须原样把 `SdkInstallResult.failed.exitCode` 与 `tail` 抛给用户，**不许**吞成 `cancelled`。 |

### 8.2 未验项（交付时必须原样抄进 `RESULT.md` 的「未验证」段）

- **真实 `/kernel` 交互**：picker 的键盘/鼠标/置灰/hint 渲染是 TUI 的事，本包只提供数据。本轮**只验数据面**（`KernelEntry.installable`、`hint`），**不验像素**。
- **`dsh-go-host` 安装链**：包不存在（E404），**整条 `pnpm-profile-add` 路径未验**。
- **真实模型**：G-7 需要 magpie 出口与真实模型；如果 `DSHD_LLM_BASE` 不可达，G-7 记 `UNVERIFIED`，**不许**用假响应顶替。
- **多平台**：只在 `win32-x64` 上验；`linux-x64` / `darwin-arm64` 的 `dshd` 二进制存在（`dist/`）但**未跑**。
- **长期运行 / HMR / 多 scope**：未验。
- **旧宿主（TUI < 0.19）**：**明确不支持**。0.18 的装配面虽一致，但本包只对 0.19 声明兼容。
- **`~/.dsh-tui/backends/dsh-go/` 的真实落盘**：`BackendHost.dataDir` 由宿主给；如果宿主不给（`undefined`），本包退化内存。**两条路径都要有测试**，但真实目录内容未验。

### 8.3 回滚

| 层 | 回滚动作 |
|---|---|
| 包（未发布） | 删除仓库 / 关闭 PR。无外部影响。 |
| 包（已发布 `0.1.0`） | `npm deprecate dsh-tui2go@0.1.0 "use 0.1.1"`，**不要** `npm unpublish`（会打断已装用户的 lockfile）。 |
| 已装进 profile | 从 `dsh.profile.bundles` 删掉 `dsh-tui2go` 一行 → 重启 TUI → 该 backend 从 picker 消失。**不需要卸载包**（`plugins` 列表会显示"已安装但未启用"）。 |
| 用户数据 | 本包只写 `~/.dsh-tui/backends/dsh-go/`（prefs）与 `DSH_GO_HOME`（会话）。前者删目录即清；后者**是宿主数据，不许删**。 |
| Go 宿主 | **本任务全程未改**，无需回滚。 |

### 8.4 本任务书自身的未做项

- 未创建任何 goal（按派单要求）。
- 未改 `PLAN.md` / `UIPLAN.md` / `AGENTS.md` / `LEAD.md`。
- 未改 `DSH-GO` 任何源码或二进制。
- 未 `commit` / `push` / `npm publish` / 安装 / 重启。
- 未向第三方上游（`ccch1mneyyy/dsh-TUI` 等）做任何写操作。

---

## 附录 A · 一页速查

```text
包名        dsh-tui2go
backend id  dsh-go
显示名      DSH-GO
宿主        dshd  (DSH-GO 0.2.2-go.1)
目标 TUI    @deepseek-harness-tui/dsh-tui@0.19.0
环境变量    DSH_GO_HOME(必填)  DSH_GO_DSHD(默认 dshd)  DSH_GO_POLICY(默认 ask)  DSH_GO_MODEL(可选)
启动        dshd serve --home <dir> --addr 127.0.0.1:0 --policy <policy>
就绪        一行  ready {"url":…,"token":…}
传输        POST /api/<method>（裸参数）  ·  GET /connect?token=（一次性）  ·  WS /ws（sub/unsub/ping）
契约        tui.dsh/v1alpha1#Backend   ·   7 个 agent/*.d.ts 在 0.18↔0.19 逐字节相同
装配        包根 dsh-plugin.json  +  package.json.dsh.bundle.patch  +  dsh.profile.bundles
入口        模块命名空间导出 `backend`（固定名）  +  spec.unloadExport 指的池钩子
硬禁区      grants 必须 []  ·  capabilities 必须 ⊆ 27 个已知名  ·  inTree 由投影固定 false
安装        install 今天不能声明（dsh-go-host npm E404）
```

## 附录 B · 本任务书引用的只读证据清单

| 证据 | 路径 |
|---|---|
| 0.19 官方包（装配/契约/安装执行器） | `C:/Users/Administrator/AppData/Local/Temp/tui-019-run/package/` |
| 0.18 源码（可复用件来源） | `D:/DeepSeek Harness/default-workspace/dsh-TUI-upstream-src/` |
| 0.19 源码 checkout（**无 bin/lib/node_modules，只当参考**） | `D:/DeepSeek Harness/default-workspace/DSH-GO/tmp/dsh-TUI-pr2/` |
| 第三方 bundle 官方夹具（可照抄） | `dsh-TUI-upstream-src/scripts/fixtures/third-party-backend/` |
| 0.19 真实装配门 | `dsh-TUI-upstream-src/scripts/verify-backend-assembly.ts` |
| W110 真机 conformance 三件套 | `dsh-TUI-upstream-src/src/dsh-go/conformance{,.test}.ts`、`conformance-tui.mjs`、`conformance-report.mjs` |
| 后端接入规范 | `dsh-TUI-upstream-src/docs/agent-backend-design.md`（§接入新后端 / §第三方后端 bundle） |
| DSH-GO 兼容性说明 | `DSH-GO/docs/dsh-tui-compat.md:7-11` |
| 团队编制与派单纪律 | `DSH-GO/AGENTS.md:11-12`、`:20` |
| 宿主 `serve` 真实旗标 | `./dshd.exe serve --help` |
| 宿主版本 | `./dshd.exe --version` → `DSH GO 0.2.2-go.1` |
| npm 包存在性 | `npm view dsh-go-host version` / `npm view dsh-tui2go version` → 均 E404 |
