# 40 · 官方 DSH 桌面壳（Electron）解剖

> 目的：为「GUI 单独立项」提供**可施工的边界**——官方壳到底把哪些东西留给了 Electron，哪些留给了宿主，中间那条缝是怎么缝的。
>
> 版本：`@deepseek-ai/dsh-desktop` **0.2.1-rc.5**，`main: lib/main.js`，构建提交 `d4db512a969fccf0c5cc7ec8fe38e9c1d27af916`（`app.asar/package.json:1-30`）。
> 读法：`ELECTRON_RUN_AS_NODE=1 "D:/DeepSeek Harness/Desktop/DeepSeek Harness.exe" script.mjs` + `node:fs` 读 asar；全程只读。

## 0. 口径与证据约定

| 写法 | 指什么 |
|---|---|
| `lib/main.js:11935` | `D:/DeepSeek Harness/Desktop/resources/app.asar/lib/main.js` 第 11935 行 |
| `@…/lib/index.js:202` | `app.asar/dsh/node_modules/@deepseek-ai/…/lib/index.js` 第 202 行（宿主侧包） |
| `@…/lib/client.js:623` | 同一包的浏览器半边（`exports["./client"]`） |
| `dist/index-BXV-oxvX.js@556044` | `@deepseek-ai/dsh-web-frontend/dist/assets/index-BXV-oxvX.js` 的**字节偏移**（该文件被压缩成 192 行，行号没有分辨率） |

实测基线：`app.asar` 本体 **182,766,714 B**（174.3 MiB）；`lib/main.js` **516,716 B / 13,198 行**；`lib/` 下 5 个 preload + 1 个 `welcome/` 子目录；`renderer/` 只有 13 个文件（welcome / update-dialog / mandatory-update 三套静态页）；`dsh/node_modules/@deepseek-ai/` 下 **307** 个包目录。

**先说三条结论**，后面全是证据：

1. **主界面根本不在壳里，也不在 `renderer/` 里。** 壳把 `dsh-web-frontend/dist` 当静态站发到自定义协议 `dsh-app://app/` 上，其余路径**反向代理**给一个子进程里的 dsh 宿主。壳与 UI 之间只有 4 个接缝：静态 dist、`__DSH_BOOT__` 注入行、`/api` 反代、`dshDesktop` 预加载 API。
2. **宿主是独立子进程**（同一个 Electron 可执行文件以 Node 模式跑），`--host 127.0.0.1 --port 0` 由内核分配端口，用 `process.send({type:'ready',url,injections})` 交回壳。
3. **UI 与内核的协议是「HTTP RPC + 一条 WS 复用通道」**，事件就是会话 v4 的 62 种事件类型本身，不是 ACP 那套 `session/update`。

---

## 1. 窗口与进程

### 1.1 到底开了几个 BrowserWindow

| # | 窗口 | 构造点 | 尺寸 / 形态 | 加载什么 | 何时存在 |
|---|---|---|---|---|---|
| 1 | **主窗口** | `lib/main.js:11936`（`createWindow`），调用点 `lib/main.js:12889` | 1280×820，min 520×600；Win 用 `titleBarStyle:'hidden'` + `titleBarOverlay{height:40}`，mac 用 `hiddenInset`+`vibrancy:'sidebar'`（`11937-11959`） | `dsh-app://app/`（`12134` 定义，`12312` / `12288` 调用 `navigateMain`） | 全程 |
| 2 | **welcome 窗口** | `lib/main.js:8631`（`openWelcomeWindow`） | `welcomeWindowOptions(platform, locale)` | `renderer/welcome.html`（`8697`，`loadFile`） | 首次启动无凭据时 |
| 3 | **update-dialog** | `lib/main.js:10150` | 720×760，父窗口 `parent`（`10150-10153`） | `dsh-app://shell/update-dialog.html`（`10328`） | 有更新且需确认时 |
| 4 | **mandatory-update** | `lib/main.js:11541`（`create(parent, preload, title, modal)`） | 透明子窗口，跟随父窗 bounds | `dsh-app://shell/mandatory-update.html`（`9764`） | 强制更新策略命中；**Windows 上不建窗口，改在主文档里嵌 iframe**（`lib/preload-app.cjs:860`、`lib/main.js:9766` 注释） |

另有两类**不是 BrowserWindow 的渲染面**：

| # | 形态 | 构造点 | 用途 |
|---|---|---|---|
| 5 | `WebContentsView` | `lib/main.js:6841` | 内嵌 Platform 账号页（登录/充值），独立 `session` 分区 + `preload-platform-account.cjs` |
| 6 | `<webview>` guest | `lib/main.js:11966` 仅在主窗开 `webviewTag`；租约管理在 `browser-guests`（`10481-10500`） | 侧栏浏览器（右侧栏的那个「浏览器」标签页） |

### 1.2 主窗口的 URL 与端口

`loadURL` 的目标**不是** `http://127.0.0.1:<port>`，而是壳自己的协议：

```js
const applicationUrl = `${SCHEME}://app/`;          // lib/main.js:12134
await navigateMain(applicationUrl);                 // lib/main.js:12312（先开窗）
await backend.start(async () => {...});             // lib/main.js:12313（再拉宿主）
```

`SCHEME = "dsh-app"`（`lib/main.js:7009`），启动时用 `protocol.registerSchemesAsPrivileged([{scheme, privileges:{standard:true,…}}])`（`11880-11886`）注册为特权 scheme。真正的路由分派只有三行（`lib/main.js:12406-12415`）：

| 请求 | 谁响应 | 证据 |
|---|---|---|
| `dsh-app://shell/*` | 壳自己的 `renderer/` 目录（welcome、update-dialog…） | `lib/main.js:12408` |
| `dsh-app://app/`、`/index.html`、`/assets/*`、`/favicon.svg`、`/manifest.webmanifest` | **本机静态**：`@deepseek-ai/dsh-web-frontend/dist` | `lib/main.js:12410` |
| `dsh-app://app/<其它一切>` | **反代**到宿主：`forwardWebRequest(request, hostUrl, hostCookie)` | `lib/main.js:12411-12412` |
| 其它 hostname | 404 | `lib/main.js:12414` |

端口来源：**宿主自己选，壳不知道也不传**。宿主启动参数写死 `--port 0`（`@…/dsh-desktop-host/lib/index.js:234-240`），绑定后把 `ctx.webServer.port` 拼进回传 URL：

```js
const url = ctx.connection.authenticatedUrl(`${ctx.webServer.protocol}//127.0.0.1:${String(ctx.webServer.port)}`);
process.send({ type: "ready", url, injections: ctx.webServer.collectIndexInjections() });
// @…/dsh-desktop-host/lib/index.js:342-349
```

壳拿到 URL 后立刻用它换一个 cookie（`lib/main.js:8229-8235`，期望 303 + `set-cookie`），之后所有 `/api` 反代都带这个 cookie，**cookie 永不进渲染进程**（`WITHHELD_RESPONSE_HEADERS` 里显式丢掉 `set-cookie`，`lib/main.js:8243-8255`）。

> 端口号在运行期是随机的（`--port 0`），壳把它当不透明字符串用。**这一点对 Go 移植很关键：GUI 不应该假设固定端口。**
> 运行期实际端口**未验证**（索引要求 launch token，裸 `GET /` 返回 401：`dsh web authentication required; reopen the URL printed by dsh web`）。

### 1.3 `webPreferences` 的实际取值

主窗口（`lib/main.js:11960-11968`）：

```js
webPreferences: { preload, nodeIntegration: false, contextIsolation: true,
                  sandbox: true, webSecurity: true, webviewTag: primary, devTools: true }
```

| 项 | 值 | 含义 |
|---|---|---|
| `nodeIntegration` | **false** | 渲染进程无 `require` |
| `contextIsolation` | **true** | preload 与页面世界隔离，只能走 `contextBridge` |
| `sandbox` | **true** | 渲染进程进 Chromium 沙箱，preload 也只有有限 node API |
| `webSecurity` | true | — |
| `webviewTag` | **仅主窗口 true** | 侧栏浏览器靠它 |

即：**主界面是纯 Web 应用**，与「用浏览器打开 `dsh web`」是同一份代码，只是被塞进了壳的协议与 IPC 里。旁证：`@deepseek-ai/dsh-web-frontend/dist/index.html` 全文只有 **18 行**（`<div id="root">` + 2 个 module preload + 2 个 CSS），没有任何 Electron 痕迹。

---

## 2. IPC 面

### 2.1 总量

channel 名不是字符串散落，而是 5 张常量表（`lib/main.js:6707 / 6981 / 8554 / 9664 / 10323`）：

| 命名空间 | 数量 | 定义点 | 归属 |
|---|---:|---|---|
| `dsh-desktop:*` | 25 | `lib/main.js:6981-7007` | 主壳 ↔ 主界面（及快捷键/主题/菜单） |
| `dsh-platform:*` | 5 | `lib/main.js:6707-6713` | 内嵌 Platform 视图 |
| `dsh-welcome:*` | 9 | `lib/main.js:8554-8564` | welcome 窗口（生命周期内注册） |
| `dsh-desktop:mandatory-*` | 3 | `lib/main.js:9664-9668` | 强制更新模态 |
| `dsh-update-dialog:*` | 3 | `lib/main.js:10323-10327` | 更新确认框 |
| **合计字符串** | **45** | | |

注册情况：`ipcMain.handle` **30** 个、`ipcMain.handleOnce` **0** 个、`ipcMain.on` **5** 个 → **共 35 个已注册处理器**；其余 10 个是主→渲染的单向推送（`webContents.send` 共 18 处）。

### 2.2 按功能分组（35 个已注册）

| 组 | 数量 | 代表 channel（含注册点） | Go 端性质 |
|---|---:|---|---|
| **启动与引导** | 5 | `dsh-desktop:boot`(`12424`)、`boot-failed`(`12433`)、`onboarding-api-key`(`12525`)、`onboarding-active`(`12529`)、`locale-bootstrap`(`12499`) | 🔴 必须（`boot` 是 UI 唯一的启动入口） |
| **窗口/外观** | 4 | `native-theme-set`(`12495`)、`windows-appearance`(`12869`)、`windows-menu`(`12836`)、`locale-changed`(`12507`) | 🟡 便利（主界面自适应可退化） |
| **快捷键** | 4 | `shortcuts-get`(`11260`)、`shortcuts-edit`(`11266`)、`shortcuts-recording`(`11271`)、`shortcuts-close-window`(`11277`) | 🟡 便利 |
| **文件对话框** | 1 | `directory-pick`(`7030`，仅主帧 + 原生 `openDirectory`) | 🔴 必须（Web 无法选目录） |
| **侧栏浏览器租约** | 2（+1 推送） | `browser-acquire`(`12439`)、`browser-release`(`12443`)、推送 `browser-open-requested`(`10594`) | 🟢 可砍 |
| **更新** | 2 | `updates-status`(`12517`)、`updates-open`(`12538`)、推送 `updates-presentation`(`12277`) | 🟢 可砍 |
| **强制更新模态** | 2 | `dsh-desktop:mandatory-status`(`9794`)、`mandatory-action`(`9798`) | 🟢 可砍 |
| **更新确认框** | 2 | `dsh-update-dialog:status`(`10357`)、`respond`(`10361`) | 🟢 可砍 |
| **账号（welcome）** | 8 | `dsh-welcome:save-api-key`(`8668`)、`start`(`8677`)、`cancel`(`8681`)、`copy-link`(`8686`)、`skip`(`8673`)、`analytics`(`8657`)、`analytics-enabled`(`8653`)、`take-notice`(`8664`) | 🟢 可砍（本地 API key 也能从设置里给） |
| **Platform 内嵌视图** | 4 | `dsh-platform:bootstrap`(`12475`，`sendSync`)、`open`(`12482`)、`bounds`(`12487`)、`close`(`12491`) | 🟢 可砍 |
| **设备信息** | 1 | `device-info`(`12521`) | 🟡 可衍生 |

推送型（10 个，无处理器）：`shortcuts-input`(`11234`/`11422`)、`shortcuts-changed`(`11251`)、`window-fullscreen`(`11976`)、`enter-workspace`(`12947`)、`updates-presentation`(`12277`)、`browser-open-requested`(`10594`)、`dsh-platform:locale-changed`(`6916`)、`mandatory-state`(`9908` 等 5 处)、`update-dialog:changed`(`10414`/`10431`)、`welcome:state`(`12205` 等 3 处)。

### 2.3 一条硬性安全约束（Go 端照抄）

每个处理器都先做**发送方归属校验**：`assertDesktopSender(event, ["app"])` 要求 `senderFrame.url` 的协议是 `dsh-app:` 且 hostname 在白名单里，否则抛「rejected IPC from an unowned renderer」（`lib/main.js:7015-7020`）；`assertProductSender` 进一步要求是主窗口的**主帧**（`12157-12160`）。这是 ELECTRON 壳里最值得抄走的一条设计：**IPC 授权按「谁的文档」判，不按「谁调了」判**。

---

## 3. preload 暴露面（壳的 API 面量化）

5 个 preload 文件，**11 个全局名 / 42 个可调用方法**（另含 3 个常量字段）。

### 3.1 `lib/preload-app.cjs`（41,075 B / 884 行）——主界面的全部能力

| 全局名 | 方法数 | 方法 | 证据 |
|---|---:|---|---|
| `dshDesktop` | **13**（+`protocolVersion:1`） | `browser.acquire/release/onOpenRequested`、`deviceInfo`、`keyboard.closeWindow/subscribe`、`shortcuts.get/edit/recording/subscribe`、`updates.status/open/subscribe` | `789-843`，挂载 `876` |
| `dshDesktopBoot` | 2 | `ready()`（→ `{injections, streamBaseUrl}`）、`failed(msg)` | `863-866` |
| `dshPlatform` | 3 | `open/setBounds/close` | `867-871` |
| `__DSH_LOCALE__` | 2 | `read/onChange` | `877-882` |
| `__DSH_DIRECTORY_PICKER__` | 1 | `pick()` | `861` |
| `__DSH_HOST_PATHS__` | 1 | `pathFor(File)`（拖入文件的真实路径） | `862` |
| `dshOnboarding` | 2 | `hasApiKey/setActive` | `845-850` |

注意两个**不是 API、而是 DOM 副作用**的 preload 行为：`markDocumentPlatform()` 往 `<html data-platform>` 写平台名（`50-56`，主界面 CSS 靠它区分 darwin/win32）；`syncNativeTheme()` 用 `MutationObserver` 盯 `html[data-ds-theme-source]` 并回传主进程设 `nativeTheme.themeSource`（`85-99`）。**也就是说：主题的单一真相在渲染进程的 DOM 属性上，原生外观是它的下游。**

### 3.2 其它 preload（都在隔离文档里，只服务壳自己的窗口）

| 文件 | 全局名 | 方法数 | 证据 |
|---|---|---:|---|
| `lib/preload-welcome.cjs`（21,477 B） | `dshWelcome` | 9 | `369-391` |
| `lib/preload-mandatory.cjs`（1,033 B） | `dshMandatoryUpdate` | 3 | `12-25` |
| `lib/preload-update-dialog.cjs`（1,108 B） | `dshUpdateDialog` | 3 | `13-26` |
| `lib/preload-platform-account.cjs`（2,083 B） | `dsh`（注意：和 host 的进程内注入同名，但不是一回事） | 3 + 2 常量 | `37-54` |

**结论**：壳给主界面的 API 面只有 **24 个方法**（`dshDesktop` 13 + boot 2 + platform 3 + locale 2 + picker 1 + hostpaths 1 + onboarding 2）。这 24 个就是「桌面壳相对浏览器多出来的全部东西」。主界面真正**必需**的只有 `dshDesktopBoot.ready()` 一个——见 §6.1 的启动握手。

---

## 4. 宿主服务

### 4.1 起法：子进程，不是同进程 `import()`

```js
const entry  = join(runtimeDir, "node_modules/@deepseek-ai/dsh-desktop-host/lib/index.js");
const child  = spawn(this.node, ["--expose-internals", ...inspect, entry,
                                 runtimeDir, projectDir, primaryRuntime, pnpm, nodeBin],
                     { cwd: projectDir, env: desktopNodeEnvironment(...),
                       stdio: ["ignore", "pipe", "pipe", "ipc"] });
// lib/main.js:3793-3811
```

| 事实 | 证据 |
|---|---|
| `this.node` = 打包的 Electron 可执行文件，以 **Node 模式**跑（子进程环境被强制设为 `ELECTRON_RUN_AS_NODE=1`） | `lib/main.js:3776`、`3649-3657`（`desktopNodeEnvironment` 的 `3652` 行） |
| 子进程与壳之间只有 `stdio: ipc` 一条通道，自定义消息协议 | `lib/main.js:3805-3810` |
| 子进程 stderr 保留最后 64 KB 用于崩溃诊断 | `lib/main.js:3813-3816` |
| 消息类型：`ready` / `fatal` / `shutdown-complete` / `platform-session` / 控制请求应答 | `lib/main.js:3818-3837` |
| 壳 → 子进程：`shutdown` / `quit-inspection` / `update-tasks` | `@…/dsh-desktop-host/lib/index.js:268-324` |
| 子进程在 ASAR 里跑，但 Office 引擎要落到 `app.asar.unpacked`，用 `module.registerHooks` 改解析 | `@…/dsh-desktop-host/lib/index.js:40-63` |

宿主启动的是一个**完整的 dsh profile**（`runProfile({profile:"desktop"})`），不是精简 web 服务：

```js
const profile = loadProfileDirectory("dsh", projectDir, installAnchor);   // :224
const application = runProfile({ environment: loadLayeredEnv("dsh"), profile: "desktop",
                                 patchFiles: [],
                                 args: ["--no-open", "--host", "127.0.0.1", "--port", "0"] }); // :226-240
```

### 4.2 端口与绑定

| 项 | 值 | 证据 |
|---|---|---|
| 绑定地址 | `127.0.0.1`（只回环） | `@…/dsh-desktop-host/lib/index.js:237` |
| 端口 | `0` → OS 分配 | `:238-239` |
| 协议 | `ctx.webServer.protocol`（`http:` 或 `https:`，TLS 配置存在时） | `:342`；`@…/dsh-host-webserver/README.md:57` |
| 认证 | `ctx.connection.authenticatedUrl(base)` 把进程 launch token 拼进 URL | `:342`；`@…/dsh-client-connection/lib/index.js:440` |
| 索引注入 | `ctx.webServer.collectIndexInjections()` 一并回传 | `:346` |

`@deepseek-ai/dsh-host-webserver` 本身**只是个路由注册表**（`node:http`，可选 `node:https`），不含任何 harness 概念、不提供文件服务（`@…/dsh-host-webserver/README.zh.md:12`）。所有 `path` 都是别的插件注册进来的。

### 4.3 实际路由表（静态可证的）

| 路由 | 类型 | 谁注册 | 干什么 | 证据 |
|---|---|---|---|---|
| `/` + `/assets/*` + `/favicon.svg` + `/manifest.webmanifest` | 静态（**壳发**，不经宿主） | Electron 壳 | 主界面 SPA | `lib/main.js:12410` |
| `/api`（prefix） | HTTP | `dsh-client-connection` | 全部 RPC/unary 调用；先 `connection.admit(req)`（401/403），再 `waterfall("connection/request")` 过中间件 | `@…/dsh-client-connection/lib/index.js:1044-1058`、`:14` |
| `/connect`（exact） | HTTP | 同上 | launch token → 持久签名 cookie 交换 | `@…/dsh-client-connection/lib/index.js:1059-1065`、`:18-22` |
| `/api/remote.mux` | **WebSocket（upgrade）** | `dsh-api-gateway` | 所有 Remote 流复用一条 WS | `@…/dsh-api-gateway/lib/types/stream-protocol.js:4` |
| `/plugins`（prefix） | HTTP | `dsh-client-modules` | 插件 client bundle（combo 与 chunk） | `@…/dsh-client-modules/lib/index.js:202`、`:564-568` |
| HMR 事件流（SSE） | HTTP | `dsh-client-hmr` | 开发期向浏览器推 graph / rebuilt | `@…/dsh-client-hmr/lib/index.js:130-165` |
| `/oauth/callback` | HTTP | `dsh-deepseek-account-platform` | 账号登录回调 | `@…/dsh-deepseek-account-platform/lib/index.js:991-993` |
| `/api/file` | HTTP | `dsh-api-session-controller` | 附件读写 | `@…/dsh-api-session-controller/lib/index.js:2483` |

反代时壳会**改写缓存语义**：`/plugins/*` 原本是 `immutable`（按每次启动变化的 rev），壳改成 `no-store`，理由是「否则 Chromium 磁盘缓存只会攒下一堆下次启动用不上的 bundle」（`lib/main.js:8256-8294`）。

---

## 5. UI 与宿主的协议

### 5.1 载体：两条，不是一条

| 载体 | 用途 | 证据 |
|---|---|---|
| **HTTP `/api`** | 一元 RPC（带 `{path:[...], key}` 形状的调用）、事件结果回传 `$events/result` | `@…/dsh-client-connection/lib/index.js:14`；`@…/dsh-api-gateway/lib/types/stream-protocol.js:8` |
| **WebSocket `/api/remote.mux`** | 所有**流**（会话事件流、助手增量、终端、任务日志）复用一条连接 | `@…/dsh-api-gateway/lib/types/stream-protocol.js:4`；`@…/dsh-client-connection/lib/client.browser.js:1348-1350`（`openConnectionWebSocket(path, factory, baseUrl)`，`baseUrl` 就是 `streamBaseUrl`） |

渲染进程从 `dshDesktopBoot.ready()` 拿 `streamBaseUrl`，塞进 `globalThis.__DSH_TRANSPORT__ = {ownsHost:true, streamBaseUrl}`（`dist/index-BXV-oxvX.js@921836`）。在纯浏览器（`dsh web`）里，`baseUrl` 缺省即文档同源，**同一份前端代码两种宿主都能跑**。

### 5.2 消息类型（分三层，别混）

**第一层：会话事件（durable，落盘的那 62 种）**——`@…/dsh-api-session-controller/lib/client.js:137-200` 是一张 `KNOWN_SESSION_EVENT_TYPES` 白名单，与本仓 `codecs/v4` 已知的类型完全同源。代表 20 个：

`turn/start`、`turn/end`、`step/start`、`step/end`、`user/message`、`assistant/message`、`assistant/attempt`、`system/message`、`developer/message`、`tool/call`、`tool/result`、`request/header`、`request/context`、`approval/asked`、`approval/decided`、`compaction/summary`、`session/title`、`todo/write`、`job/registered`、`deliverables/presented`
（证据：同上 `:137-200`；本仓 `AGENTS.md` §4 的 62 类型是同一条词汇表，已有 `research/21`。）

**第二层：日志流的增量帧（transport 级，不是事件）**——`replace` / `prepend` / `append` / `settle-assistant`，且必须**连续**；断号时用尾页修复：

> 「opens follow before the initial page, publishes only contiguous `replace`, `prepend`, `append`, and `settle-assistant` changes, and repairs reconnect or sequence gaps through the tail page」
> —— `@…/dsh-api-session-controller/README.md:54`

**第三层：助手流式增量（process-local，不落盘）**——三种 chunk：

| chunk | 证据 |
|---|---|
| `text-delta` | `@…/dsh-api-session-controller/lib/client.js:1893` |
| `reasoning-delta` | 同上 `:1898` |
| `tool-call-delta` | 同上 `:1903` |

外层是一个**严格版本号窗口**：`{type:'start', revision:1, attemptId, turn, step}` → `{type:'chunk', revision:n+1, index:0..n}` → `{type:'end'}`；`revision` 不连续就整体丢弃重来，`index` 不连续就丢掉当前 attempt（`@…/dsh-api-session-controller/lib/types/assistant-stream.js:18-58`）。重连时不重放 chunk，而是给一个**基线快照**（同上 `:63-81`）。

### 5.3 和 ACP 的 update 类型是同一套吗？

**不是。** ACP 走 `session/update`（`agent_message_chunk` / `agent_thought_chunk` / `tool_call` / `tool_call_update` / `usage_update`…）是本仓 `internal/acp` 与 `research/22` 处理的那一套；Web GUI 用的是上表三层——**v4 会话事件的原始词汇 + journal 增量 + 助手 chunk**。两者是对同一底层事件流的**两种投影**：

| | Web GUI | ACP |
|---|---|---|
| 载体 | HTTP `/api` + WS `/api/remote.mux` | stdio JSON-RPC |
| 会话事件 | 直接是 `turn/start`…62 种 | 翻译成 `session/update` 的 4~5 种 |
| 流式文本 | `text-delta` / `reasoning-delta` / `tool-call-delta` | `agent_message_chunk` / `agent_thought_chunk` |
| 审批 | `approval/asked` 事件 + `dsh-user-approval` 服务 | `session/request_permission` |

**对 Go 的含义**：GUI 不要复用 ACP 的编码层。要么直接吐 v4 事件（我们已经会编码/解码），要么在 v4 之上再造一层 ACP。前者便宜得多。

---

## 6. client 插件装载链

### 6.1 从「宿主扫描 loader 条目」到「浏览器里 factory 被调用」

| 步 | 发生地 | 做什么 | 证据 |
|---:|---|---|---|
| 1 | 宿主 · cordis loader | profile patch（`dsh-base` 96 行 + `dsh-web-app` 91 行）装插件；每个装上的条目带 `options.name` | `@…/dsh-base/cordis.patch.yml:15-…`；`@…/dsh-web-app/cordis.patch.yml` |
| 2 | 宿主 · `dsh-client-modules` | `ClientModuleRegistry` 构造时 `for (const entry of ctx.loader.entries()) this.dirty.add(entry.options.name)`，然后 `flush()` | `@…/dsh-client-modules/lib/index.js:558-562` |
| 3 | 宿主 · 同上 | 对每个条目解析 `package.json` 的 `exports["./client"]` + `dsh.client`（`inject`/`external`/`immediately`/`platform`）；缺 bundle 或缺声明 → 一次性聚合抛错 | 同上 `:171-182`、`:68-73`；`:150-157`（错误类） |
| 4 | 宿主 · 同上 | `orderByModuleGraph()` 按 `external` 拓扑排序，**有环即抛**（「factory-form CJS 不能交付半成品 exports」） | 同上 `:422-444` |
| 5 | 宿主 · 同上 | 按 3 KB URL 上限切批，生成两类批次 `bootstrap` / `application` | 同上 `:162`、`:237-253`、`:673-674` |
| 6 | 宿主 · 同上 | `bootInjections(graph)` 产出注入行：`<script>` 内联队列 + `script-preload`（application 批）+ `script-src`（bootstrap 批，阻塞）+ `global __DSH_BOOT__ = graph` | 同上 `:460-505` |
| 7 | 宿主 · `dsh-host-webserver` | 把这些行渲染进 index HTML；Electron 下不走 HTML，而是 `collectIndexInjections()` 交回壳 | `@…/dsh-host-webserver/lib/index.js:522-535` |
| 8 | 壳 → 渲染 | `dsh-desktop:boot` 返回 `{injections, streamBaseUrl}` | `lib/main.js:12424-12432` |
| 9 | 渲染 · 前端 | 先有 `globalThis.__DSH_BOOT_READY__`（壳注入，`lib/main.js:8221`）；`dshDesktopBoot.ready()` → 逐行应用注入（`global`/`script`/`script-src`/`style`/`html`） | `dist/index-BXV-oxvX.js@921836`（`UZ()` 注入应用器） |
| 10 | 渲染 · `__ModuleLoader__` | 队列模式下 `load({id,factory})` 只入队；`create()` 从队列里找出 `@deepseek-ai/dsh-client-modules` 那条，**调用它的 factory 引导出模块系统** | `@…/dsh-client-modules/lib/index.js:461-482` |
| 11 | 渲染 · 模块系统 | `require(spec)` 先查 **11 个平台种子**，再查已物化模块，最后查已注册 factory；都没有 → `missed the module table` 硬错 | `@…/dsh-client-modules/lib/client.js:832-841` |
| 12 | 渲染 · 每个插件 | `factory(makeRequire)` 执行 → 插件体里的 `apply(ctx)` 由 `dsh-cordis-client-runner` 当 cordis 插件跑 | `@…/dsh-client-modules/lib/client.js:804-830`；`@…/dsh-cordis-client-runner/lib/client.js` |
| 13 | 渲染 · HMR | `/plugins` 的 rev 变了或 HMR SSE 推 `rebuilt`，`reload(id,rev)` 重取 | `@…/dsh-client-modules/lib/client.js:431`；`@…/dsh-client-hmr/lib/index.js:154-161` |

### 6.2 平台种子模块（壳必须提供的 11 个）

```
react · react/jsx-runtime · react-dom · react-dom/client · @deepseek-ai/cordis · zod
@tanstack/react-virtual · @deepseek-ai/dsh-client-store
@deepseek-ai/dsh-client-ui-slots · @deepseek-ai/dsh-client-ui-primitives · @deepseek-ai/dsh-client-ui-dockkit
```

证据：`dist/index-BXV-oxvX.js@917831`（`OZ()` 返回的对象字面量）。这 11 个名字在 `dsh-web-frontend/dist` 里各出现 1 次——它们**不是运行时包**，是构建期被内联进 shell bundle 的。

> 这条最能说明「壳/UI 的边界」：**插件只被允许 `require` 这 11 个 + 自己的依赖包**。React 是壳给的，不是插件自带的。

### 6.3 量化

| 指标 | 值 | 证据 |
|---|---:|---|
| `@deepseek-ai/*` 包目录 | 307 | 实测列举 |
| 声明 `dsh.client` 的包 | **79** | 各 `package.json` 的 `dsh.client` |
| 有 `exports["./client"]` 的包 | 90（含 `.browser`/`.node` 变体） | 同上 |
| 目录名 `dsh-client-ui-*` | **55**（+ `dsh-experimental-client-ui-*` 4） | 实测 |
| `dsh.client.immediately`（首屏必载） | 11 | `dsh-api-gateway`、`dsh-api-remotes`、`dsh-client-connection`、`dsh-client-file-upload`、`dsh-client-hmr`、`dsh-client-locale`、`dsh-client-modules`、`dsh-client-ui-renderer`、`dsh-client-ui-theme`、`dsh-experimental-inspector`、`dsh-typert-registry` |
| `dsh.client.inject` 引用总数 | 354 | 74 个包声明了 inject |
| **base + web-app patch 实际带来的 client bundle** | **71**（去重后） | 96 + 91 行 patch ∩ 79 个 `dsh.client` |

> 任务书里「约 80 个 `dsh.client`」「67 个 `dsh-client-ui-*`」，实测分别是 **79** 与 **55**（另有 4 个 experimental 前缀）。**以实测为准**。

---

## 7. slots 契约

### 7.1 形状：声明 + 授权树，不是自由挂载点

注册一个 slot 就是注册一个组件，同时**声明它下面有哪些子槽**：

```js
ctx.slots.register({
  name: "root", locale: "common",
  children: {
    "sidebar":      { kind: "single", scope: "root" },
    "main":         { kind: "keyed",  scope: "root" },
    "rightbar":     { kind: "single", scope: "root" },
    "shell.bottom": { kind: "single", scope: "root" },
    "shell.overlay":{ kind: "list",   scope: "root" },
    "shell.leading":{ kind: "single", scope: "root" },
  },
  store,
}, AppFrame);
```
证据：`@…/dsh-client-ui-layout/lib/client.js:623-653`。

授权规则很硬（这是设计上最值得学的一条）：**只能往「父条目 `children` 里声明过的槽」里注册**，否则抛错：

| 违规 | 报错 | 证据 |
|---|---|---|
| 槽不属于该条目的 children | `SlotOwnershipError: slot 'X' is not declared by this entry's children` | `@…/dsh-client-ui-renderer/lib/client.js:348` |
| 对 `chain` 槽用了单值渲染 | `use renderSlotChain` | 同上 `:349` |
| 条目已释放还在渲染 | `StaleAuthorizationError` | 同上 `:346` |

运行时 API（`ctx.slots.*`）：

| 方法 | 作用 | 证据 |
|---|---|---|
| `register(decl, Component)` | 注册（返回 disposer） | `layout:623` |
| `registerFactory(...)` | 延迟物化的注册 | `conversation:19936` |
| `inject(slotId, cb)` | 「等该槽存在再注册，槽消失则自动 dispose」——插件侧最常用 | `attachment:901-917` |
| `entries(slotId)` | 读条目（layout 用它决定 `main` 有哪些面板键） | `layout:615-617` |
| `subscribe(slotId, cb)` | 订阅条目变化 | `layout:689` |
| `provideRoot({hooks})` | 给整棵树注入 selector hooks | `layout:621` |

槽的 `kind`：`single` | `list` | `keyed`（按 `options.key` 取一，`main` 用这个） | `chain`（瀑布，`conversation.composer` 用这个）。
槽的 `scope`：`root` | `session` | `session-maybe`。

### 7.2 真实的 20 个 slot id

从 76 个 `client.js` 里静态提取到 **87 个**不同 slot id。按填充者数量排序，取 20 个：

| slot id | 填充者数 | 谁在用（节选） |
|---|---:|---|
| `sidebar.right.pane.tab` | 10 | deliverables / plan / schedule / sidebar-browser / documentpreview / sidebar-files |
| `settings.general.item` | 8 | client-locale / chat / conversation / permission-presets / settings-general |
| `shell.overlay` | 8 | chat / plugin-manager / schedule / settings-account / settings-session-log |
| `sidebar.right.pane.tab.title` | 7 | plan / schedule / sidebar-browser / documentpreview / sidebar-right |
| `conversation.chat.node` | 5 | chat / goal / tool / user-questions / workflow-run |
| `conversation.user.text.node` | 5 | conversation / goal / reference / secret-mask / skill |
| `settings.section` | 5 | agent-preset / settings-account / settings-general / settings-models / settings-plugins |
| `conversation.session.header.actions` | 4 | agent-preset / jobs / subagent / agent-team |
| `plugins.item` | 4 | settings-agent-loop / settings-shell / settings-subagent / settings-web-search |
| `tool.call.toolview` | 4 | cordis / deliverables / skill / tool |
| `conversation.chat.turnTail` | 3 | deliverables / plan / schedule |
| `conversation.composer` | 3 | approval / subagent / user-questions |
| `conversation.input.overlay` | 3 | commands / input-trigger / message-feedback |
| `conversation.session.header.utilities` | 3 | open-in-app / schedule / session-log-export |
| `main` | 3 | conversation / plugin-manager / schedule（keyed 面板） |
| `conversation.input.dock` | 2 | conversation / goal |
| `sidebar.brand.mark` / `sidebar.brand.name` | 1 / 1 | brand-official |
| `conversation.input.model` | 1 | model-selection |
| `conversation.session.header.corner` | 1 | sidebar-right |
| `sidebar.footer.action` | 1 | cordis |
| `tool.call.images` | 1 | attachment |

（完整 87 个可由 `slots.inject("…")` / `{name:"…"}` 在 76 个 `client.js` 上静态枚举。）
**命名规律**：`区域.子区域.位置`，`.` 分段，`sidebar.` / `conversation.` / `settings.` / `shell.` / `tool.` 五大前缀。

---

## 8. 布局骨架

`@deepseek-ai/dsh-client-ui-layout`（client 32,834 B）**只做骨架**：一个 `AppFrame` + 拖拽 + 宽度状态 + 主题挂载点。区域树取 CSS Grid 三列 + 底部行 + 覆盖层（`@…/dsh-client-ui-layout/lib/client.js:316-373`，类名见 `:83-90`）：

| 区域 | slot | kind | 谁填充 | 证据 |
|---|---|---|---|---|
| 左栏 | `sidebar` | single | `@deepseek-ai/dsh-client-ui-sidebar`（会话列表/工作区）；`sidebar.*` 子槽由 brand-official、cordis、subagent 等填 | `layout:301-308`；`sidebar/lib/client.js` |
| 中栏 | `main` | **keyed** | `dsh-client-ui-conversation`（`main.conversation`，主对话）、`dsh-client-ui-trajectory`、`dsh-client-ui-plugin-manager`、`dsh-client-ui-schedule`；面板 id 由 `usePanelInfo().activePanelId` 决定，缺省 `"conversation"` | `layout:119`、`:309-312`；`conversation:19929-19935` |
| 右栏 | `rightbar` | single | `@deepseek-ai/dsh-client-ui-sidebar-right`（196 KB，右边栏宿主）；页签来自 `sidebar.right.pane.tab`（10 个填充者：files / documentpreview / browser / terminal / plan / schedule / deliverables / …） | `layout:339-343`；`sidebar-right/lib/client.js` |
| 底栏 | `shell.bottom` | single | **出厂无人填**（只有 layout 声明 + cordis-client-runner 透传） | `layout:344-348`；`shell.bottom` 全库仅 2 处命中 |
| 覆盖层 | `shell.overlay` | list | chat（命令面板/审批浮层）、plugin-manager、schedule、settings-account、settings-general、settings-session-log 等 8 个 | `layout:349-353` |
| 左上角（mac） | `shell.leading` | single | `dsh-client-ui-sidebar`（侧栏收起时让出红绿灯位） | `layout:354-358`、`:315`（`darwin && sidebarCollapsed`） |
| 标题栏 | —（非 slot） | — | 壳注入 `titleBarOverlay`；`data-windows-titlebar` 由 CSS 读 | `layout:320`；`lib/main.js:11942-11949` |

拖动柄 `DragHandle` 只对 `sidebar` / `rightbar` 各一个（`layout:359-372`），宽度落 `LayoutController`（`layout:617-620`）并通过 `ctx.reflect.provide("layout", layout)` 变成服务（`layout:622`）。

---

## 9. 重活在哪

### 9.1 逐项对应

| 活 | 在哪个包 | 用了什么 | 证据 |
|---|---|---|---|
| **Markdown 渲染** | 壳 bundle 内的 `dsh-client-ui-primitives`（`MarkdownText` / `MarkdownDelegateProvider`） | **micromark**（CommonMark）+ `mdast` 自建管线；**不是** remark/unified | `dist/index-BXV-oxvX.js`：`micromark`×1、`mdast`×2、`CommonMark`×1；消费者 `@…/dsh-client-ui-chat/lib/client.js:2909`、`:4370` |
| **代码高亮** | 同上（primitives） | **Shiki**（`css-variables` 主题 + 懒编译 grammar） | `index-BXV-oxvX.js@790536`（`wR=mb({name:"css-variables",variablePrefix:"--shiki-"…})`、`a4=1e3`、grammar 懒加载 `xb`） |
| **数学公式** | 同上 | **KaTeX** `renderToString`，`strict:"ignore"`、`throwOnError:false` | `index-BXV-oxvX.js@836727` |
| **diff 视图** | 同上（自研，**无第三方库**） | 手写 unified-diff 解析 → hunks → 行级 `del/add/context` 渲染，带「展开上下文」 | `index-BXV-oxvX.js@556044`（构造 `{oldFileName,newFileName,hunks}`、`\ No newline at end of file`、CSS `_del_`/`_add_`/`_context`/`_expand`）；`twoFilesPatch` / `diffLines` / `Myers` 计数 **0** |
| **终端（浏览器）** | `@deepseek-ai/dsh-client-ui-sidebar-terminal` | **xterm.js**，且**懒加载**：`client.js` 19,965 B，真正的终端是 `client.terminal.js` **686,966 B** | `sidebar-terminal/lib/client.js`（`Load xterm only after a terminal body is mounted`）；`LazyTerminalBody` → `require.async("./client.terminal.js")` |
| **终端（宿主）** | `@deepseek-ai/dsh-api-terminal-controller` | **`@xterm/headless` + `@xterm/addon-serialize`**（服务端保住屏幕状态，重连可还原） | `api-terminal-controller/package.json` deps |
| **PTY** | `@deepseek-ai/dsh-subprocess-local` / `dsh-terminal-bash` | **node-pty 1.2.0-beta.15** + **koffi 3.1.1**（Win32 进程控制） | `subprocess-local/package.json`、`dsh-win32-process` |
| **虚拟滚动** | `dsh-client-ui-chat` / `trajectory` / `conversation` / `experimental-session-inspector` | **`@tanstack/react-virtual`**（作为 11 个种子模块之一由壳提供） | `index-BXV-oxvX.js@917831`；`chat`、`trajectory` 里 `virtualizer` 命中 |
| **PDF 预览** | `@deepseek-ai/dsh-client-ui-sidebar-documentpreview` | 懒 chunk `client.pdf.js` **7,108,812 B** | `sidebar-documentpreview/lib/` 目录实测 |
| **表格预览** | 同上 | 懒 chunk `client.excel.js` **7,060,291 B**（含 SheetJS 一类解析） | 同上；`client.js` 里 `"xlsx"/"xls"/"csv"/"tsv"` 后缀分派 |
| **Office 文档** | 宿主 `@deepseek-ai/dsh-office-to-pdf` + `libreoffice-kit` | 真·LibreOffice（`fflate`/`fontkit`/`koffi`/`saxes`），引擎落在 `app.asar.unpacked` | `office-to-pdf/package.json`；`dsh-desktop-host/lib/index.js:40-63` |
| **附件/图片** | `dsh-client-ui-attachment` + 宿主 `dsh-attachment-local` | 走 5 个 slot（`conversation.input.attachments`、`tool.call.images`…） | `attachment/lib/client.js:901-917` |

### 9.2 重写时最难替代的 5 件事

| # | 难点 | 为什么难 | 现在的实现 |
|---:|---|---|---|
| 1 | **Markdown + 高亮 + 公式三件套的流式友好性** | 流式增量意味着「半截代码块」「半截表格」每帧都要合法渲染，还要不抖 | micromark + Shiki（懒 grammar）+ KaTeX，全在壳里共享 |
| 2 | **diff 视图** | 工具结果是 patch 文本，要按 hunk 折叠、上下文展开、行号对齐；现成库（diff2html）尺寸与样式都不对味，官方干脆手写 | 自研解析 + CSS 类，约 1 个 region |
| 3 | **终端** | 不是「打印 ANSI」：要 headless 终端保屏幕状态（`@xterm/headless` + `addon-serialize`），才有「重连还原」；PTY 在 Windows 上还要 koffi 兜 | xterm.js + headless + node-pty |
| 4 | **会话事件流的增量协议** | 4 种 journal 变更必须**连续**，断号只能用尾页修；助手增量另有 revision/index 双连续 + 重连基线 | `RemoteJournalStream` + `AssistantStreamAccumulator` |
| 5 | **slots 的授权树** | 87 个槽、10 个填充者的共存靠「只能往父条目声明的子槽注册」这条硬规则，否则插件互相踩 | `dsh-client-ui-slots`（种子模块） |

---

## 10. 量化

### 10.1 前端产物字节数（实测，非估算）

| 部分 | 文件数 | 原始字节 | gzip⁹ | 说明 |
|---|---:|---:|---:|---|
| 壳 bundle（`dsh-web-frontend/dist`） | 4 | **1,787,333** | 486,368 | `index-*.js` 941,436 + `vendor-*.js` 741,114 + 两个 CSS 77,471+27,312 |
| 插件 `client.js`（76 个包） | 76 | **11,708,471** | 4,409,067 | 每个包一个 factory bundle |
| 懒加载 chunk（真正被 `require.async` 引用的 4 个） | 4 | **15,063,358** | ≈4,663,122 | pdf 7.1 MB / excel 7.1 MB / xterm 687 KB / frontmatter 207 KB |
| **合计（浏览器可下载）** | 84 | **28,559,162**（27.2 MiB） | **≈9.56 MB**（9.1 MiB） | gzip 为本机 `zlib.gzipSync(level:9)` 重算，非 CDN 实测 |

> 口径提醒：如果把 `exports["./client"]` 的所有变体（`client.browser.js` / `client.node.js` / `client.js`）都算进去，单 `client.*.js` 家族是 **11,882,650 B**；上面用 76 个真正的浏览器 `client.js`（11,708,471 B）。
> `dsh-client-ui-*` 52 个包的 client.js 合计 **9,059,926 B**（gz 3,951,890）。

### 10.2 最大的 5 个包（按 client.js 原始字节）

| # | 包 | client.js | gz | 为什么大 |
|---:|---|---:|---:|---|
| 1 | `dsh-client-ui-settings-account` | **3,417,921** | 2,465,487 | **内联 15 张 base64 图片**（`.webp`×8、`.svg`×2、`data:image`×15）——gz 几乎压不动，占了全部前端字节的 12% |
| 2 | `dsh-api-remotes` | 1,087,482 | 104,041 | 所有 Remote 描述符的生成物（类型反射表，重复度高所以 gz 很小） |
| 3 | `dsh-client-ui-conversation` | 782,997 | 193,638 | 对话容器 + composer + 草稿/附件/工作区切换 |
| 4 | `dsh-client-ui-plugin-manager` | 626,646 | 231,678 | 插件市场与管理 UI |
| 5 | `dsh-client-ui-chat` | 595,683 | 153,637 | 消息渲染 + Markdown 接线 + 推理块 |

（第 6~10：`dsh-cordis-client-runner` 411,786、`dsh-client-ui-trajectory` 398,745、`dsh-client-ui-schedule` 328,631、`dsh-client-ui-sidebar-documentpreview` 286,866、`dsh-client-ui-workspace` 232,860。）

### 10.3 `lib/main.js` 的 516,716 B 在干什么

文件被切成 **63 个 `//#region`**，其中 **253,021 B（49%）是壳自己的代码**（`lib/types/*`），**262,222 B（51%）是从 monorepo 里摇进来的库**（js-yaml、llm、schemastery…）。按功能归类（字节为该 region 原文长度）：

| 类别 | 约计字节 | 主要 region（行号） | 评价 |
|---|---:|---|---|
| **主流程 / 窗口** | ~54,139 + 2,177 + 4,041 + 11,894 | `main.js`(11799)、`runtime-tree`(90)、`project-manager`(3549)、`host-process`(3660) | 壳的骨架：起窗、起宿主、管 profile |
| **更新（全家桶）** | **~51,700** | `update-coordinator`(7748)、`update-schedule`(9119)、`update-journal`(9019)、`update-presentation`(9214)、`update-attention`(9670)、`update-overlay`(11517)、`update-dialog`(10320)、`mandatory-update-policy`(9443)、`mandatory-update-window`(9762)、`update-channel`(7667)、`update-http-executor`(7682)、`update-error`(7731) | **12 个 region、约占壳代码 1/5**，全是为 electron-updater 服务的 |
| **IPC 与安全** | ~2,064 + 1,231 + 1,577 + 10,448 | `ipc.js`(6978)、`directory-picker`(7022)、`microphone-permissions`(7047)、`platform-view`(6715) | 45 个 channel + 归属校验 |
| **键盘/快捷键** | **~32,000** | `keyboard`(11210)、`shortcuts/lib/protocol`(10666)、`keybindings`(11183) | 32 KB 只为快捷键协议 |
| **账号 / 登录** | ~6,002 + 6,088 + 6,064 + 7,028 + 4,948 | `account-backend`(8710)、`welcome-backend`(8871)、`login-shell-environment`(9259)、`policy-test-auth`(10072)、`welcome-window`(8574) | welcome 引导链路 |
| **本地化** | **21,051** | `locale.js`(7271) | 中英两套壳文案（含快捷键名、菜单、更新提示） |
| **Web 文档/反代** | ~4,476 | `web-document.js`(8184) | 静态 + 反代 + cookie 扣留 |
| **崩溃/恢复/单实例** | ~5,355 + 4,073 + 1,821 + 652 | `crash-report`(8405)、`fatal-recovery`(8301)、`background-notice`(11738)、`single-instance`(7650) | — |
| **托盘** | **1,253** | `tray.js`(11692) | 意外地小 |
| **命令安装管理** | **10,424** | `command-management.js`(7958) | 往 shell PATH 里装 `dsh` 命令 |
| **侧栏浏览器租约** | ~6,704 | `browser-guests.js`(10478) | `<webview>` 分区 |
| 摇进来的库 | ~262,222 | `js-yaml@4.3.2`(**91,760**)、`llm/llm`(**54,343**)、`schemastery`(22,836)、`values`(21,080)、`loader`(15,438)、`cosmokit`(12,408)、`typert/protocol`(8,509)、其余 | 主进程里为什么有 `llm` 和 `js-yaml`：读 profile 配置 + welcome 窗口的模型元数据（**未逐一验证**） |

**一句话**：壳自己的 253 KB 里，**约 1/5 是自动更新，另约 1/5 是快捷键与本地化**；真正「让界面活着」的只有 `main.js` 的启动编排 + `web-document.js` 的 4.5 KB。

---

## 11. 给 Go 重写的边界建议

分三类。每条一句话理由。

### ① 必须复刻（不做就不能用）——**10 条**

| # | 必须复刻什么 | 理由 |
|---:|---|---|
| 1 | 宿主 Web 服务：回环绑定 + **OS 分配端口** + `/connect` token→cookie + `/api` 前缀 + 无 cookie 401 | 这是 UI 与内核之间唯一活线；端口写死会在多实例/被占用时直接不可用 |
| 2 | `/api/remote.mux` 一条 WS 复用所有流 | 一条连接管所有流才有「切会话不用重连」，否则每开一个面板就多一条 socket |
| 3 | 会话 journal 流：`replace/prepend/append/settle-assistant` + 连续性 + 尾页补洞 | 断号不修会静默丢消息，用户看到的是「历史缺一段」 |
| 4 | 助手流式增量：`start/chunk/end` + revision/index 双连续 + 重连基线快照 | 少了基线，「切走再切回」正在生成的回答就空了 |
| 5 | 启动注入 `__DSH_BOOT__`（图 + 两阶段批 + 3 KB URL 切分）+ `/plugins` combo 下发 | 只要保留「UI 也是插件」，这就是唯一的装载契约 |
| 6 | 11 个平台种子模块（`react`…`dsh-client-ui-slots`）的**名单与语义** | 插件 bundle 只允许 require 这些；名单一变全部插件报废 |
| 7 | slots 的**声明 + 授权树**（只能往父条目声明的子槽注册，`single/list/keyed/chain`，`root/session/session-maybe`） | 87 个槽共存靠这条硬规则；退化成「全局事件名」会立刻互相踩 |
| 8 | 父子进程握手：`ready{url,injections}` / `fatal{message,diagnostic}` / `shutdown` / `quit-inspection` / `update-tasks` | 崩溃隔离与「退出前问一句」全靠它；同进程做就没有恢复力 |
| 9 | 原生目录选择 + 拖入文件真实路径（`dialog.showOpenDialog(openDirectory)`、`webUtils.getPathForFile`） | 浏览器沙箱**永远**给不了这两样，只能由壳提供 |
| 10 | 发送方归属校验（按文档 origin 判 IPC 授权）+ 导航/外链白名单（`will-navigate` 一律 `openExternal`） | 一条 `file://` 或第三方 origin 混进来就等于把宿主交出去 |

（另有一条**可选但强烈建议**：单实例 + 窗口聚焦——`lib/main.js:13184`。丢了只会「开两个」，不致命，故不列入 10 条。）

### ② 可以砍（桌面便利 / 遥测 / 更新）——**10 条**

| # | 砍什么 | 一句话理由 |
|---:|---|---|
| 1 | electron-updater 全链（12 个 region，约 51.7 KB 主进程代码：check/download/journal/schedule/attention/mandatory/dialog/overlay） | 我们发二进制，更新由用户/包管理器决定，不需要在壳里再长一套状态机 |
| 2 | welcome 窗口 + 账号登录 + `dsh-welcome:*` 9 个 channel（8 个处理器） | API key 在设置里给就行，登录链路是平台生意，不是产品必需 |
| 3 | 埋点（`desktop_app_launch`、`track()`、`dsh-client-product-analytics`、`dsh-session-log-deepseek` 上报） | 与本仓「不上报」的定位冲突 |
| 4 | 崩溃上报（`crash-report.js` 5.3 KB、`pruneCrashReports`、`fatal-recovery` 的远程通道） | 本地日志 + `dshd doctor` 已经覆盖，远程上报属于遥测 |
| 5 | 托盘（`tray.js` 1.2 KB + `tray.ico`）、`Notification`、`background-notice` | 托盘只服务「关窗不退出」这一个习惯，代价是常驻与误点 |
| 6 | Windows/macOS 窗口材质（`titleBarOverlay`、`vibrancy:'sidebar'`、`windows-appearance`、`chromeFallbackFill`） | 纯观感；退了只是标题栏不好看 |
| 7 | 快捷键系统（`keyboard` 11.3 KB + `shortcuts/protocol` 19.9 KB + 持久化 keybindings） | 原生菜单加速键能覆盖 90%，自定义键位是加分项 |
| 8 | 侧栏浏览器（`webviewTag`、`browser-guests`、`browser-acquire/release`、租约与分区） | 这是「壳里再装一个浏览器」，攻击面与复杂度都极高，且与主流程无关 |
| 9 | Platform 内嵌账号视图（`WebContentsView` + `preload-platform-account` + `dsh-platform:*`） | 同上：账号生意，不是编辑器能力 |
| 10 | 命令安装管理（`command-management.js` 10.4 KB：往 shell 里装/修/删 `dsh` 命令） | 我们的分发是单二进制，用户自己 `PATH`，不需要壳去改 shell 启动文件 |

（可选再砍：PDF/Excel 内联预览共 **14.2 MB** 前端字节——改用系统默认程序打开，能砍掉前端一半体积。）

### ③ 可以换实现（同一职责，换技术栈）——**9 条**

| # | 现在是什么 | 换成什么 | 一句话理由 |
|---:|---|---|---|
| 1 | Electron 壳（`createWindow` + 5 个 preload + 45 channel） | Wails v3 / WebView2（PLAN §11 已定「不用 Electron」） | 职责只是「一个窗口 + 一个 WebView + 少量原生能力」，Electron 的 174 MB asar 与 870 MB 内存没有必要 |
| 2 | 自定义 `dsh-app://` 协议 + 手写反代 + cookie 扣留 | 直接让页面跑 `http://127.0.0.1:<port>`，或 Go 用 `http.FileServer` + `httputil.ReverseProxy` 内嵌 | 那 15 行协议分派代码存在的唯一理由是「把 dist 和宿主拼在同一个 origin 上」 |
| 3 | 主界面 dist 由**壳**静态托管 | 由**宿主**自己发（`dsh web` 本来就会发） | 一份资源两处托管必然漂移；宿主发、壳只当浏览器就行 |
| 4 | markdown（micromark）+ Shiki + KaTeX | goldmark/chroma（Go 侧）或前端自选；公式可先不做 | 三件套是纯渲染，契约只有「HTML 片段」一个 |
| 5 | 自研 unified-diff 渲染 | 自研也可以，但换语言/换库都行 | 契约是「patch 文本进、行级渲染出」，没有跨进程协议 |
| 6 | xterm.js + `@xterm/headless` + `addon-serialize` + node-pty | Go 侧 `creack/pty` + 前端 xterm.js（保留前端这一半最省事） | 「headless 保屏幕」这件事 Go 也能做（自己存 scrollback），不必依赖 `addon-serialize` |
| 7 | `@tanstack/react-virtual` | 自研窗口化，或换成 `ViewSpec` 渲染器的内建裁剪 | 本仓已决定「客户端只经 Transport + ViewSpec 接触内核」，虚拟滚动属于渲染器内部事务 |
| 8 | 主题同步（`data-ds-theme-source` → `MutationObserver` → `nativeTheme.themeSource`） | 壳直接读配置里的主题值，或前端 `postMessage` 一次 | DOM 属性当真相源是为了「Web 与桌面共用一套 CSS」，Go 端可以直接把主题当配置项 |
| 9 | 会话日志 JSONL+zstd | **已经换完了**（`codecs/v4`，M2） | 官方与我们的落盘格式字节级对齐，这条不用再讨论 |

### ④ 一页速查：Go 桌面端最小可用边界

```
┌──────────────────────────── 壳（Go）────────────────────────────┐
│ 主窗口(WebView)  原生目录选择  拖入文件路径  外链白名单  单实例    │
│ 主题/语言读取    退出前活动任务检查                                 │
└───────────────┬──────────────────────────────────────────────────┘
                │ ① 启动：拉起宿主，等 ready{url, injections}
                │ ② 注入：把 injections 交给前端（或让宿主自己渲染 index）
                │ ③ 传输：前端连 http://127.0.0.1:<port>
                ▼
┌──────────────────────────── 宿主（Go, dshd）─────────────────────┐
│ /api（HTTP RPC）   /api/remote.mux（WS 流复用）   /plugins（可选） │
│ 会话事件 62 种  +  journal 4 种增量  +  助手 3 种 chunk            │
│ v4 日志读写（M2 已完成）  工具流水线（M1 已完成）  ACP（M3，另一条路）│
└──────────────────────────────────────────────────────────────────┘
```

---

## 附录 A · 未验证清单（不编）

| # | 未验证的是什么 | 为什么没验 |
|---:|---|---|
| 1 | 运行期宿主实际端口 | index 需 launch token，裸 `GET http://127.0.0.1:56319/` 返回 **401** |
| 2 | 本次运行 `__DSH_BOOT__` 图里到底几条 entry、combo 切成几批 | 同上，拿不到带 token 的 index；静态只能算出「最多 71 个 bundle」 |
| 3 | desktop profile 目录（`paths.profile`）里是否还有额外 patch 行 | 任务书禁止碰 `D:/DeepSeek Harness/home/profiles/` |
| 4 | `lib/main.js` 里 `llm/llm`(54 KB)、`js-yaml`(92 KB) 被摇进来的**具体调用点** | 只按 region 名与字节归类，未逐函数追引用 |
| 5 | `dsh-client-ui-settings-account` 那 15 张内联图分别是什么 | 只数了 `data:image`/`.webp` 的出现次数 |
| 6 | gzip 列是否为官方 CDN 口径 | 本机 `zlib.gzipSync(level:9)` 重算，非线上实测 |
| 7 | `shell.bottom` 是否在某些 profile 下有填充者 | 出厂发行包里 0 个填充者，其它 profile 未查 |
| 8 | HMR（`dsh-client-hmr` + SSE）在生产包里是否默认开 | 只在代码里看到路由注册，未见开关默认值 |
