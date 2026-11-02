# DSH 重写评估 · 代码审计（只读）

审计对象：`D:\DeepSeek Harness\Desktop\resources\app.asar\dsh`（asar 内，必须用 Electron Node 模式读）
运行环境（`audit-00-env.mjs`）：Electron 44.7.0 / Node 24.21.0 / V8 15.2 / NAPI 10 / win32-x64 / ABI 149

## 0. 规模口径

| 指标 | 数值 | 证据 |
|---|---:|---|
| `dsh/node_modules` 总量 | 401.8 MB / 17,186 文件 | `audit-06-totals.mjs` |
| `@deepseek-ai/*` 包数 | 307 | `audit-00-list.mjs` |
| `@deepseek-ai/*` 总量 | 277.3 MB / 6,946 文件 | `audit-06-totals.mjs` |
| 一手代码文件（js/cjs/mjs/ts，不含嵌套 node_modules） | 1,161 文件 / 1,289,036 行 / 67.9 MB | `audit-02-nodeapi.mjs` |
| 其中压缩/构建产物（`dist/`、`lib/client*.js` >150 KB） | 76 文件 / 25.9 MB | 同上，已单列 |
| 非产物一手代码 | 1,085 文件 / 1,016,813 行 | 同上 |
| 客户端面（package.json 有 `dsh.client`） | 79 包 / 436 js / 49.3 MB | `audit-06-totals.mjs` |
| 完全不 import Node 内置模块的包 | 189 / 307（其中 66 个是客户端面） | `audit-05-purity.mjs` |

> 行数含压缩产物，参考性弱；计数以「文件数 / 字节 / 命中数」为准。8 个包同时发 `src/*.ts` 与编译后 `lib/*.js`，表 2 两者都计入（重复约 0.5%）。

## 1. 表 1：原生 / 二进制依赖

`.node`/`.dll`/`.exe` 共 **22 个**（`audit-01-native.mjs`）。按 package.json 的 `binary`/`gypfile`/`prebuild`/`os` 只能抓到 6 个包，**node-pty 没有任何声明字段**，靠 `.node` 文件扫描才可见。

| 包 | 版本 | 体积 | 二进制文件（大小） | 用途线索 | 谁依赖它（一手） |
|---|---|---:|---|---|---|
| `@deepseek-ai/libreoffice-kit-win32-x64` | 0.1.5 | **190.9 MB**（735 文件） | `bin/libreoffice-kit.exe` 178.6 MB | `Prebuilt LibreOfficeKit engine`：Office→PDF / 重算 / 渲染 | `dsh-office-to-pdf`、`dsh-skill-office`、`dsh-desktop-host` |
| `sherpa-onnx-win-x64`（+`sherpa-onnx-node` 0.1 MB） | 1.13.8 | **22.4 MB** | `onnxruntime.dll` 17.8 MB、`sherpa-onnx.node` 695 KB、`sherpa-onnx-c-api.dll` 4.6 MB、`onnxruntime_providers_shared.dll` 118 KB | `Speech-to-text, text-to-speech…Next-gen Kaldi`：本地语音识别 | `dsh-experimental-speech-to-text-sensevoice` |
| `@img/sharp-win32-x64`（+`sharp` 683 KB JS） | 0.35.5 | **19.9 MB** | `sharp-win32-x64-0.35.5.node` 457 KB、`libvips-42.dll` 19.0 MB、`libvips-cpp-8.18.7.dll` 347 KB | `High performance Node.js image processing`：附件图片解码/缩放 | `dsh-attachment-local` |
| `@vscode/ripgrep-win32-x64`（+`@vscode/ripgrep` 4 KB） | 1.18.0 | **5.4 MB** | `bin/rg.exe` 5.44 MB | ripgrep 二进制，文件内容搜索 | `dsh-tool-fs-search` |
| `node-pty` | 1.2.0-beta.15 | **5.3 MB** | `prebuilds/win32-x64/conpty.node` 305 KB、`conpty_console_list.node` 148 KB、`conpty.dll` ×3、`OpenConsole.exe` ×3（各 1.06 MB） | `Fork pseudoterminals in Node.JS`：交互式终端 | `dsh-subprocess-local` |
| `@koromix/koffi-win32-x64`（+`koffi` 1.77 MB JS/头文件） | 3.1.1 | **1.06 MB** | `win32_x64/koffi.node` 1.06 MB | `dynamic C FFI`：直接调 Win32 API | 8 个包：`dsh-win32-process`、`dsh-sandbox-windows-acl`、`dsh-subprocess-local`、`dsh-fs-local`、`dsh-host-directory-picker-native`、`dsh-session-persistence-jsonl`、`dsh-desktop-host`、`libreoffice-kit` |
| `node-addon-require-builtin-win32-x64-msvc`（+`node-addon-require-builtin` 4 KB、`node-addon-native-custom-loader` 传递依赖） | 0.1.9 | 359 KB | `prebuilt/win32-x64-msvc-napi-v9.node` 357 KB | `runtime probing for internal access`：拿 Node 内部 ESM loader（`internal/modules/esm/loader`） | `cordis-plugin-loader`、`dsh`、`dsh-app-boot`、`dsh-hmr`（`audit-08-verify.mjs` 反查） |
| `@deepseek-ai/node-addon-system` | 0.1.2 | 30 KB | （按平台，Linux Landlock / POSIX flock 预编译） | `system primitives: a Linux Landlock launcher and asynchronous POSIX flock` | `dsh-sandbox-local`、`dsh-session-persistence-jsonl` |

**汇总**：上述原生载体合计 ≈ **249 MB / 401.8 MB**（62%）。全部只有 **win32-x64** 一份（6 个包 `os:["win32"]`），app.asar 里没有任何 arm64/darwin/linux 变体。
另有非原生但外部的进程依赖：`pnpm`（插件安装，`dsh-plugin-manager`）、`electron-updater`（壳层自动更新）。

## 2. 表 2：Node 专有 API 使用密度

统计范围：`@deepseek-ai/*` 全部一手代码（排除嵌套 node_modules 与 76 个构建产物）。脚本 `audit-02-nodeapi.mjs`，原始数据 `audit-02-nodeapi.json`，表格由 `audit-07-fragments.mjs` 生成。

### 2a. 按 API

| API | 文件数 | 出现次数 | 最集中的文件（命中） |
|---|---:|---:|---|
| `child_process` | 20 | 20 | `dsh-client-product-analytics/lib/index.js` (1)；`dsh-host-directory-picker-native/lib/index.js` (1) |
| `spawn(` | 30 | 45 | `dsh-tool-cordis/lib/types/api-catalog.js` (4)；`dsh-tool-fs-search/lib/index.js` (3) |
| `execFile` | 6 | 6 | `dsh-client-product-analytics/lib/index.js` (1)；`dsh-native-command/lib/index.js` (1) |
| `worker_threads` | 14 | 22 | `dsh-session-persistence-jsonl/lib/worker.cjs` (5)；`dsh-plugin-market/lib/worker.cjs` (4) |
| `vm` / `new Function` | 17 | 24 | `dsh-experimental-inspector/…/lighthouse_worker.js` (6)；`dsh-cordis-host-runner/lib/index.js` (2) |
| 动态 `import()` | 51 | 153 | `dsh-experimental-inspector/…/devtools_app.js` (43)；`dsh-experimental-inspector/lib/devtools/chunks/codemirror-CENmPHF2.js` (21) |
| `process.binding` | 1 | 1 | `dsh-experimental-inspector/…/lighthouse_worker.js` (1) |
| `require.extensions` | **0** | 0 | — |
| `AsyncLocalStorage` | 5 | 13 | `dsh-agent/lib/index.js` (3)；`dsh-agent/lib/types/index.js` (3) |
| `node:stream` | 11 | 14 | `dsh-experimental-speech-to-text-sensevoice/lib/index.js` (2)；`libreoffice-kit/lib/cli.js` (2) |
| `Buffer` | 113 | 440 | `…/lighthouse_worker.js` (43)；`dsh-session-persistence-jsonl/lib/index.js` (25) |
| `net`/`http(s)`/`http2` | 10 | 12 | `dsh-host-webserver/lib/index.js` (3)；`dsh-acp/lib/index.js` (1) |
| Electron IPC（`ipcMain`/`ipcRenderer`/`contextBridge`） | **0** | 0 | 一手运行时代码里完全没有；全在壳层（见下） |
| `koffi`（补充项，非 Node 内置） | 12 | 133 | `dsh-subprocess-local/lib/runner-launch-B2zsQ1Dz.js` (35)；`dsh-host-directory-picker-native/lib/worker.cjs` (27) |

Node 内置模块的**文件数**分布（`audit-05-purity.mjs`）：`path` 124、`crypto` 76、`fs` 63、`os` 41、`url` 37、`module` 26、`util` 25、`child_process` 20、`buffer` 17、`worker_threads` 14、`stream` 11、`events` 9、`net` 6、`http` 5、`async_hooks` 5、`perf_hooks` 4、`vm` 3、`zlib` 3、`sqlite` 1。

### 2b. Top 30 文件（硬语义命中：child_process/spawn/worker_threads/vm/动态 import/process.binding/require.extensions/AsyncLocalStorage/net-http/Electron IPC）

| # | 文件（`@deepseek-ai/…`） | 硬命中 | 总命中 | 命中明细 |
|---|---|---:|---:|---|
| 1 | `dsh-experimental-inspector/lib/devtools/entrypoints/devtools_app/devtools_app.js` | 43 | 43 | `dynamic import()`=43 |
| 2 | `dsh-experimental-inspector/lib/devtools/chunks/codemirror-CENmPHF2.js` | 21 | 21 | `dynamic import()`=21 |
| 3 | `dsh-app-boot/lib/index.js` | 12 | 12 | `worker_threads`=1 `dynamic import()`=11 |
| 4 | `dsh-ptc-runtime-node/lib/index.js` | 9 | 17 | `spawn(`=2 `dynamic import()`=4 `AsyncLocalStorage`=3 `Buffer`=8 |
| 5 | `dsh-experimental-inspector/lib/devtools/entrypoints/lighthouse_worker/lighthouse_worker.js` | 8 | 51 | `vm`=6 `dynamic import()`=1 `process.binding`=1 `Buffer`=43 |
| 6 | `dsh-session-persistence-jsonl/lib/worker.cjs` | 7 | 33 | `worker_threads`=5 `vm`=1 `dynamic import()`=1 `stream`=1 `Buffer`=24 `koffi`=1 |
| 7 | `dsh-experimental-inspector/lib/index.js` | 6 | 7 | `worker_threads`=2 `dynamic import()`=3 `Buffer`=1 `net/http`=1 |
| 8 | `cordis-plugin-loader/lib/index.js` | 6 | 6 | `vm`=1 `dynamic import()`=5 |
| 9 | `dsh-host-directory-picker-native/lib/index.js` | 5 | 16 | `child_process`=1 `spawn(`=2 `dynamic import()`=2 `Buffer`=2 `koffi`=9 |
| 10 | `dsh-tool-cordis/lib/types/api-catalog.js` | 5 | 6 | `spawn(`=4 `dynamic import()`=1 `Buffer`=1 |
| 11 | `dsh-hmr/lib/index.js` | 5 | 5 | `dynamic import()`=3 `AsyncLocalStorage`=2 |
| 12 | `libreoffice-kit/lib/cli.js` | 4 | 16 | `child_process`=1 `spawn(`=2 `worker_threads`=1 `stream`=2 `Buffer`=4 `koffi`=6 |
| 13 | `libreoffice-kit/lib/index.js` | 4 | 16 | 同上 |
| 14 | `dsh-tool-fs-search/lib/index.js` | 4 | 6 | `spawn(`=3 `dynamic import()`=1 `Buffer`=2 |
| 15 | `cordis-plugin-loader/src/config/tree.ts` | 4 | 4 | `dynamic import()`=4 |
| 16 | `dsh/lib/bin.js` | 4 | 4 | `dynamic import()`=4 |
| 17 | `dsh-client-modules/lib/client.js` | 4 | 4 | `dynamic import()`=4 |
| 18 | `dsh-plugin-market/lib/worker.cjs` | 4 | 4 | `worker_threads`=4 |
| 19 | `dsh-session-persistence-jsonl/lib/index.js` | 3 | 30 | `worker_threads`=1 `dynamic import()`=2 `stream`=1 `Buffer`=25 `koffi`=1 |
| 20 | `dsh-ptc-runtime-node/lib/persistent-process.js` | 3 | 12 | `AsyncLocalStorage`=2 `stream`=1 `Buffer`=8 `net/http`=1 |
| 21 | `dsh-subprocess-local/lib/index.js` | 3 | 7 | `child_process`=1 `spawn(`=2 `execFile`=1 `stream`=1 `Buffer`=2 |
| 22 | `dsh-agent/lib/index.js` | 3 | 3 | `AsyncLocalStorage`=3 |
| 23 | `dsh-agent/lib/types/index.js` | 3 | 3 | `AsyncLocalStorage`=3 |
| 24 | `dsh-host-webserver/lib/index.js` | 3 | 3 | `net/http`=3 |
| 25 | `dsh-http-proxy/lib/index.js` | 3 | 3 | `dynamic import()`=3 |
| 26 | `dsh-web-app/lib/index.js` | 3 | 3 | `child_process`=1 `spawn(`=1 `dynamic import()`=1 |
| 27 | `dsh-web-fetch-http/lib/index.js` | 3 | 3 | `dynamic import()`=2 `net/http`=1 |
| 28 | `dsh-experimental-inspector/lib/worker.js` | 2 | 13 | `worker_threads`=1 `Buffer`=11 `net/http`=1 |
| 29 | `dsh-cordis-host-runner/lib/types/sandbox.js` | 2 | 7 | `vm`=2 `Buffer`=5 |
| 30 | `dsh-experimental-inspector/lib/devtools/chunks/puppeteer-xGpmPP-i.js` | 2 | 5 | `vm`=1 `dynamic import()`=1 `Buffer`=3 |

### 2c. Electron 耦合的真实位置（不在 `@deepseek-ai` 里）

| 文件 | 行数 | ipcMain | webContents | contextBridge | ipcRenderer | BrowserWindow |
|---|---:|---:|---:|---:|---:|---:|
| `app.asar/lib/main.js`（壳层主进程） | 13,198 | 35 | 99 | 0 | 0 | 6 |
| `app.asar/lib/preload-app.cjs` | 884 | 0 | 0 | 7 | 35 | 0 |
| `app.asar/lib/preload-welcome.cjs` | 393 | 0 | 0 | 1 | 10 | 0 |
| `app.asar/lib/preload-platform-account.cjs` | 57 | 0 | 0 | 1 | 2 | 0 |
| `app.asar/lib/preload-update-dialog.cjs` | 28 | 0 | 0 | 1 | 4 | 0 |
| `app.asar/lib/preload-mandatory.cjs` | 27 | 0 | 0 | 1 | 4 | 0 |

壳层把运行时当**子进程**拉起（`lib/main.js:3794 spawn(this.node, …)`），再 `loadURL` 本地 HTTP 地址（`lib/main.js:9951`、`10229`），并用 `onBeforeSendHeaders` 给 `ws://127.0.0.1/*` 注入头（`lib/main.js:12447`）。即 Electron 只做窗口/更新/权限代理，业务全部在 Node 运行时进程内。

## 3. 表 3：插件 / 扩展面

### 3.1 发现（discovery）

| 环节 | 事实 | 证据 |
|---|---|---|
| 插件单元 | 一个 profile 目录 = `$DSH_HOME/profiles/<name>/`，含 `package.json`（`dsh.profile.bundles` 有序列表）、`cordis.patch.yml`、`pnpm-workspace.yaml`、`node_modules/` | `dsh-app-boot/lib/index.js:488`、`:507-509`、`:548`、`:598`、`:623-625` |
| 组合顺序 | 每个 bundle 的 `dsh.bundle.patch`（1 个或数组，按序）→ profile 自身 patches → 启动器 `--patch` 覆盖层 | `dsh-app-boot/lib/index.js:485-497`、`:1012-1028` |
| patch 语义 | 对 entry 列表按 `id` 做覆盖/禁用/插入；未匹配只 warn 不报错 | `dsh-app-boot/lib/index.js:61`（`applyEntryPatches`） |
| 配置方言 | YAML + 自定义 `!!js` 标签（可执行表达式），`js-yaml` 扩展 `tag:yaml.org,2002:js` | `dsh-app-boot/lib/index.js:20`、`:33`；`cordis-plugin-loader/lib/index.js:233-244` |
| 热重载 | profile 的 `cordis.patch.yml` 被标记为"long-lived surfaces 上热重载"；`cordis-plugin-include` 在运行时读 YAML 并 `write()` 回写 | `dsh-app-boot/lib/index.js:508`；`cordis-plugin-loader/lib/index.js:588`、`:646` |
| 安装 | 不自己解包 npm：`execa` 调 **pnpm**（`pnpm add` / `pnpm install` / `pnpm view`），写 profile 的 `package.json`+`pnpm-lock.yaml`，失败回滚 | `dsh-plugin-manager/lib/index.js:12`、`:409`、`:610`、`:742-757` |

### 3.2 加载（loading）——是不是运行时 `import()` 任意 npm 包：**是**

| 事实 | 证据 |
|---|---|
| 插件说明符 → `import()`：`cordis:` 前缀走内置表，其余 `await import(name)`；相对路径按 `ctx.baseUrl` 解析 | `cordis-plugin-loader/lib/index.js:214-228` |
| 更好的一条路：**Node 内部 ESM loader**。用 native addon `require("node-addon-require-builtin").requireBuiltin("internal/modules/esm/loader")` 拿到 `getOrInitializeCascadedLoader()`，再 `internal.import(name, baseUrl, {})`；无 addon 时退回 `--expose-internals` | `cordis-plugin-loader/lib/index.js:9-17`、`:29-38`、`:218` |
| 配置表达式执行：`new Function("ctx","expr", "with (ctx) { return eval(expr) }")`，对 YAML 里每个 `!!js` 节点求值 | `cordis-plugin-loader/lib/index.js:233-237`、`:239-244` |
| 插件实例化：`this.ctx.registry.plugin(plugin, config, stack)` | `cordis-plugin-loader/lib/index.js:463` |
| 沙箱执行（动态 host 半边）：`node:vm` 的 `createContext`/`runInContext`，`(async()=>{…})()` 带 `timeout`；`new Function` 只用于语法预检 | `dsh-cordis-host-runner/lib/types/sandbox.js:13`、`:164-175`、`:216` |
| 客户端半边：包 `package.json` 的 `dsh.client{platform,inject,external,immediately}` 声明 → 宿主拼 `/plugins/??<id>/client.js,<id2>/client.js&rev=…` combo 路由下发 | `dsh-client-modules/lib/index.js:64`、`:201-210`、`:415-435` |

### 3.3 插件作者能碰到的宿主能力

| 能力 | 接口 | 证据 |
|---|---|---|
| 服务（约 **104 个 ctx key**） | `class X extends Service { constructor(ctx){ super(ctx,"key") } }`；`static inject = [...]` 声明依赖 | `audit-03-services2.mjs`：97 个文件 `extends Service`、98 处 `static inject`、104 个不同 key（`tools` `commands` `fs` `shell` `subprocess` `net` `credentials` `sessions` `llm` `sandbox` `typertGateway` `webServer` …）；`cordis/src/service.ts:42` |
| 工具注册 | `ctx.tools.register(definition)`（effect 作用域，卸载即撤） | `dsh-tools/lib/index.js:2974`、`:2982` |
| 命令注册 | `ctx.commands.register(definition)` / `registerFileReceiptResolver` | `dsh-commands/lib/index.js:266`、`:275` |
| UI 组件 | 双面包：host 半边注册服务，client 半边是 `window.__ModuleLoader__.load({id, factory:(require)=>{…}})`，`require("react")` 由宿主注入 | `dsh-client-ui-chat/lib/client.js:1`；`dsh-client-modules/lib/index.js:460-472` |
| 生命周期钩子 | cordis fiber 状态机（PENDING/LOADING/READY/UNLOADING/DISPOSED）+ `ctx.effect()` 收集 disposer + 事件总线 `ctx.on(...)` | `cordis/src/fiber.ts:147`、`:184-196`、`:230` |
| 沙箱内的收窄面 | 动态宿主半边只能拿到 `ctx.tools.register` / `ctx.on` / `ctx.provide` / `timer` + 显式 inject 的服务；`root/fiber/registry/extend/plugin` 被显式拒绝 | `dsh-cordis-host-runner/lib/index.js:579-590`、`:629-634` |
| Agent 自改插件 | `dsh-tool-cordis` 给模型暴露 `cordis_inspect_list` / `cordis_inspect_query`（只读运行时检视） | `dsh-tool-cordis/lib/index.js:39`、`:55` |

## 4. 表 4：UI 层技术栈与 RPC

| 项 | 事实 | 证据 |
|---|---|---|
| 框架 | **React 18.3.1**（bundle 内 `version="18.3.1"`；无 Vue/Svelte/Solid 运行时，命中的 `vue`/`svelte` 字样来自高亮语法/语言包） | `dsh-web-frontend/dist/assets/index-BXV-oxvX.js`（`audit-04-ui.mjs`） |
| 构建 | Vite 产物：`index.html` 单入口 + `vendor-*.js` 741 KB + `index-*.js` 941 KB + `langs/*` | `dsh-web-frontend/dist/index.html:10-13`；`dsh-web-frontend/package.json:3`（"vite build over the @deepseek-ai/dsh-client-web shell library"） |
| 客户端插件 | **自研 AMD 风格模块系统**：`window.__ModuleLoader__.load({id, factory:(require)=>{…}})`，宿主先注入 queue 模式再 create；React 系全部 external：`require("react")` / `require("react/jsx-runtime")` / `require("react-dom")` / `@tanstack/react-virtual` / `@deepseek-ai/dsh-client-store` 由宿主模块表提供 | `dsh-client-ui-chat/lib/client.js:1`（第 385、585 字符处即 `require("react")`/`require("react-dom")`）；`dsh-client-modules/lib/client.js:463`、`:460-472` |
| 是否"浏览器 React 通过 WS/RPC 连宿主" | 是。浏览器侧 `dsh-api-gateway/client.browser.js`（84 KB）与 `client.node.js`（73 KB）同一套契约 | `dsh-api-gateway/package.json`（exports `./client` 的 browser/node 双实现） |
| 传输 1：单向调用 | 共享 `/api` FetchHandler 上的 HTTP 请求；`typertGateway.invoke()` 校验命名参数、解析资源、调用业务方法 | `dsh-api-gateway/README.md:31`；`dsh-api-gateway/lib/index.js:750-821` |
| 传输 2：流 | 单独一条 WebSocket，专用于多路复用 Remote stream：`/api/remote.mux`，`new WebSocketServer({ noServer: true })` + HTTP upgrade 准入 | `dsh-api-gateway/lib/index.js:133`、`:300`、`:785` |
| 帧格式 | JSON 文本帧，判别字段 `type`：`open` / `item` / `cancel` / `end` / `error`；首帧 `{type:"ready"}` 证明事件源就绪 | `dsh-api-gateway/lib/index.js:137`、`:238-244`、`:481-521` |
| 协议定义 | `dsh-typert-protocol`：`@Remote` / `@RemoteScope` 装饰器 → 生成的 `InvocationDescriptor`（zod 参数 schema + JSON Schema 投影）、`RemoteError{code,details}`、`RemoteStream<Out,In>` 双向流、`AbortSignal` 取消、`$events`/`$events/result` 事件转发 | `dsh-typert-protocol/lib/index.js:11`、`:36`、`:326`、`:363`；`dsh-api-gateway/lib/types/stream-protocol.js:4-12`、`:18-53` |
| 另一条对外通道 | SDK 用 JSON-RPC over **stdio**（`dsh-sdk-jsonrpc-server`），ACP 适配器（`dsh-acp` + `@agentclientprotocol/sdk`） | `dsh-sdk-jsonrpc-server/lib/index.js:348`、`:402`；`dsh-acp/lib/index.js:15-25` |
| 本地库 | `ws` 8.x（WebSocket）、`zod` 4.x（协议校验）、`schemastery`（cordis 配置 schema）、`xterm/headless`、`minisearch` | 各包 `package.json` |

## 5. 摘要（≤200 字）

好移植：189/307 包零 Node 内置，纯 JSON/事件/状态机。锁死：22 个二进制（libreoffice 191MB、sherpa 22MB、sharp 20MB 等）须重绑；插件靠运行时 import() 任意 npm 包、内部 ESM loader、new Function/vm 执行配置表达式；前端 79 包 React 自研模块系统；Electron 壳 13k 行。

---

## 附录：可复现脚本（均在 `tmp/`，只读）

| 脚本 | 作用 | 关键产出 |
|---|---|---|
| `audit-00-env.mjs` | 运行时版本、`node:sqlite` 使用者 | Electron 44.7.0 / Node 24.21.0 / NAPI 10 |
| `audit-00-list.mjs` | 顶层包清单 | 549 项，其中 `@deepseek-ai` 307 |
| `audit-00-shell.mjs`、`audit-02b-electron.mjs` | 壳层文件与 IPC 计数 | `lib/main.js` 13,198 行 / 35 `ipcMain` |
| `audit-01-native.mjs` | 扫 `.node`/`.dll`/`.exe` + package.json 原生标记 | 22 个二进制、6 个声明原生包 |
| `audit-01b-native-detail.mjs` | 原生包体积/描述/反查依赖 | 表 1 |
| `audit-02-nodeapi.mjs` | 13 类 API 的逐文件命中 | `audit-02-nodeapi.json`、表 2 |
| `audit-05-purity.mjs` | 每包 Node 内置模块依赖 | 189/307 零内置 |
| `audit-06-totals.mjs` | 体积汇总、client-face 统计 | 401.8 MB / 277.3 MB / 79 包 |
| `audit-07-fragments.mjs` | 从 JSON 生成表 2a/2b 的 markdown | `audit-07-top30.md`、`audit-07-api.md` |
| `audit-03-services2.mjs` | 枚举 cordis service key | 104 个 key |
| `audit-04-ui.mjs`、`audit-04b-clientbundle.mjs` | 框架/版本/模块系统 | React 18.3.1 |
| `g.mjs` / `p.mjs` / `gg.mjs` | asar 内 grep / 打印行 / 递归 grep 小工具 | 所有 `file:line` 佐证 |

运行方式：`ELECTRON_RUN_AS_NODE=1 "D:/DeepSeek Harness/Desktop/DeepSeek Harness.exe" <脚本绝对路径>`（asar 内路径 bash 不可读，必须走 Electron 的 Node 模式）。
