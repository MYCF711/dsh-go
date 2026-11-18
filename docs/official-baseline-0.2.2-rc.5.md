# 官方基线 `0.2.2-rc.5`（2026-11-18 取证）

> 上一基线：[`official-baseline-0.2.2-rc.2.md`](official-baseline-0.2.2-rc.2.md)。
> 本文只写**机器可核的差异**；数字全部由本次解包实测得出，不引用官方 release notes。

## 0. 取证路径

| 项 | 值 |
|---|---|
| 更新通道 | `https://download.deepseek.com/dsh-desk/feeds/win-x64/nightly.yml` |
| 通道发布版本 | `0.2.2-rc.5` |
| 安装包 | `deepseek-harness-0.2.2-rc.5-win-x64.exe` |
| 安装包大小 | `299,593,552` 字节（与 `nightly.yml` 声明的 `size` 一致） |
| 安装包 sha256 | `013e28bc8c7147493313…`（前 20 位） |
| 解包路径 | `tmp/GO8-RC5/official-rc5/dsh/node_modules/@deepseek-ai/` |
| 解包结果 | **10,155 个文件 / 321,176,934 字节 / 338 个 `@deepseek-ai` 包** |

解包复用既有工具 `tmp/GO8-RC2/extract_asar.mjs`（`7z` 拆 NSIS → `$PLUGINSDIR/app-64.7z` → `resources/app.asar`）。

## 1. 硬数字对比

| 指标 | rc.2 | rc.5 | 差 |
|---|---|---|---|
| `@deepseek-ai` 包数 | 337 | **338** | **+1** |
| 删除的包 | — | — | **0** |
| UI 插槽（`contract/slots.ts` 里的 `conversation.*`/`sidebar.*`/`settings.*` 等） | 91 | **92** | **+1** |
| `dsh-base/cordis.patch.yml` 功能行差异 | — | — | **1 处** |

> 包内容哈希对比无意义：328/337 个包内容都变了，但那是每次 rc 重建 bundle 的版本串与构建产物变化，不是语义变更。**判断变更要看契约面，不看哈希。**

## 2. 唯一新增的包：`dsh-deepseek-chat-import`

- 版本 `0.2.2-rc.5`，`kind: package-bundle`
- 用途（`README.md` 原文摘要）：*Import DeepSeek website chat exports: upload a `deepseek_data` ZIP from the browser and store its recent conversations as Sessions*
- 即：把 chat.deepseek.com 导出的聊天记录**导入成工作区里的 Session**，用户可以接着聊
- 服务面（`lib/typert.host.js`）：`limits`（上报浏览器上传前要检查的上限，如单包最大字节数）+ 一条 owner-only 的导出上传路由

**对本仓含义**：这是一个**全新的产品能力**（导入官方网站的聊天记录），不是既有能力的修正。按「官方的都要有」的要求需要实现；实现面包含一个新的上传路由 + 把解析结果写成 v4 Session。

## 3. 唯一新增的 UI 插槽：`sidebar.session.action`

- rc.5 新增，rc.2 无；**没有插槽被删除**
- 位置语义：侧栏**每一行会话**上的动作位（与 `sidebar.session.*` 一族同处）

**对本仓含义**：GUI 侧需要在侧栏会话行上补这个座位。因为本仓账本（`../dsh-ui/docs/official-parity.md`）的分母来自官方 UI 插件清单，这一条要进新基线后的账本核算。

## 4. base 组合的唯一行为变更：`searchProvider` 不再固定

`dsh-base/cordis.patch.yml` 相对 rc.2 只有 11 行差异，功能上只有一条：

```
-        searchProvider: deepseek-search-api
```

配套注释（rc.5 原文）：

> composes no fixed provider, so `dsh-web` selects per model provider route: legacy Messages search (`deepseek-official`) for a conversation on a DeepSeek model route, because DeepSeek makes no per-search charge for it, and the DeepSeek Search API (`deepseek-search-api`) for every other route.

即 rc.5 起：

| 会话所在模型路由 | 搜索 provider |
|---|---|
| DeepSeek 模型路由 | `deepseek-official`（legacy Messages 搜索，DeepSeek 不收单次搜索费） |
| 其它任何路由 | `deepseek-search-api` |

**对本仓含义**：本仓 `modules/tool/websearch/` 与宿主 `web.listProviders` 的默认选择逻辑要改成**按会话模型路由动态选**，不能继续固定 `deepseek-search-api`。这是 rc.2 基线里已实现的那份默认值，需要更新。

## 5. 同步工作项（按依赖排序）

| # | 事项 | 面 | 依赖 |
|---|---|---|---|
| S1 | `web_search` 默认 provider 改为按模型路由选 | 宿主 `modules/tool/websearch/`、`web.listProviders` | 无 |
| S2 | `sidebar.session.action` 座位落地 | GUI 侧栏 | 无 |
| S3 | `dsh-deepseek-chat-import`：导出上传路由 + ZIP 解析 + 写成 v4 Session + `limits` 服务面 | 宿主 + GUI | S1 无依赖，可并行 |
| S4 | 基线文档与证据文件切到 rc.5（`docs/evidence/*-0.2.2-rc.5.txt`） | 文档 | S1–S3 完成情况 |

## 6. 未取证项（如实标注）

- **桌面 API 方法总数未重算**：rc.2 基线是 223 条（`docs/evidence/official-desktop-api-methods-0.2.2-rc.2.txt`）。rc.5 的方法声明散在 `lib/index.js` 的装饰器里，本次没有可靠地逐条重建 —— **不拿这次没算出来的数字当结论**，S3 落地时用新包的服务面顺带把它补上。
- **官方 release notes 未读**：本文全部结论来自安装包解包，没有引用官方发布说明（避免把发布说明当证据）。
- 安装包 `sha256` 只记了前 20 位；完整值需要时可复算（文件仍在 `tmp/GO8-RC5/dsh-rc5.exe`）。
