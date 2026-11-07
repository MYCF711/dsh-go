# 官方 UI 插件对齐清单（0.2.2-rc.1）

> 来源：`D:/DeepSeek Harness/Desktop-0.2.2-rc.1/resources/app.asar`（sha256 `5fa8a6b2…`）。包名全集固化在 [`docs/evidence/official-ui-plugins-0.2.2-rc.1.txt`](evidence/official-ui-plugins-0.2.2-rc.1.txt)。
> 官方客户端 UI 插件：**60 个** `dsh-client-ui-*` + **5 个** `dsh-experimental-client-ui-*`。
> 本仓 `../dsh-ui/plugins/`：**24 个** `ds*` 插件。
> 本文只登记对齐目标与映射，**不代表任何插件已达标**；状态以 `LEAD.md` §2.4 账本与各 RESULT 为准。

## 1. 映射与状态

状态只取：`COVERED`（结构与行为已对齐并有证据）/ `PARTIAL`（存在但缺结构/行为/官方证据）/ `UNMAPPED`（无对应实现）/ `DO-NOT-SHIP`（官方无此包且无证据，须 derived 标注或 HIDE）。

### 1.1 有明确对应

| 官方包 | 本仓插件 | 状态 | 差距要点 |
|---|---|---|---|
| `layout` | `dsshell` | PARTIAL | 壳/框架几何；官方另有独立收放与区域契约，需按官方结构树核对 |
| `chat` | `dsconversation` | PARTIAL | 官方 chat 是整屏容器；本仓消息区与输入区跨 `dsconversation`/`dscomposer` |
| `conversation` | `dsconversation` | PARTIAL | 消息渲染、折叠、流式 |
| `tool` | `dstool` | PARTIAL | 工具卡结构（含 Bash 参数多括号、结果折叠） |
| `jobs` | `dsjobs` | PARTIAL | 面板/输出分页/停止 |
| `schedule` | `dsschedule` | PARTIAL | 列表/视图/历史 |
| `goal` | `dsgoal` | PARTIAL | 目标面板与暂停/恢复 |
| `deliverables` | `dsdeliverables` | PARTIAL | 交付列表 |
| `user-questions` | `dsquestions` | PARTIAL | 提问/回答通道 |
| `commands` | `dscommands` | PARTIAL | 命令注册表与执行 |
| `shortcuts` | `dsshortcuts` | PARTIAL | 快捷键面板 |
| `sidebar` | `dssidebar` | PARTIAL | 侧栏容器 |
| `sidebar-files` | `dsfiles` | PARTIAL | 文件区 |
| `sidebar-right` | `dsrightbar` | PARTIAL | 右栏 |
| `sidebar-terminal` | `dsterminal` | PARTIAL | 终端区 |
| `brand-official` | `dsbrand` | PARTIAL | 品牌资产 |
| `open-in-app` | `dsopen` | PARTIAL | 打开本地 |
| `settings` | `dssettings` | PARTIAL | 设置模态壳 |
| `settings-*`（11 个） | `dssettings` | PARTIAL | 官方 11 个 ui-settings 包需逐个落页，见 `docs/official-settings-tree.md` |
| `plugin-manager` | `dssettings`（plugins tab） | PARTIAL | 缺 install/update/version/detail 真动作 |
| `theme` | `dssettings`（外观） | PARTIAL | 官方 theme 包独立 |

### 1.2 无对应实现（UNMAPPED）

`approval`、`attachment`、`model-selection`、`permission-presets`、`plan`、`reference`、`renderer`、`secret-mask`、`session`、`skill`、`subagent`、`trajectory`、`workflow-run`、`workspace`、`agent-preset`、`action-cards`、`creation-recommendations`、`entry-customization`、`context-menu`、`file-actions`、`file-drag`、`input-trigger`、`message-feedback`、`directory-picker-browse`、`directory-picker-native`、`plugin-market`、`plugin-recommendations`、`sidebar-browser`、`sidebar-documentpreview`、`cordis`；实验：`agent-team`、`cot-translation`、`forge`、`stats`、`voice-input`。

### 1.3 本仓独有，官方无同名包（必须证明或收敛）

`dstopbar`、`dsstatusbar`、`dscore`、`dsdecl`、`dsassets`、`dsdemo`。

- 官方客户端插件清单里**没有 topbar / statusbar 包**；`dstopbar`、`dsstatusbar` 若是自造布局，必须给出对应官方结构证据（可能属于 `layout`/`sidebar-right`），否则按 `DO-NOT-SHIP` 处理：**derived 标注或 HIDE**，不得继续当官方部件。
- `dscore`/`dsdecl`/`dsassets`/`dsdemo` 是基础设施与素材，不是官方 UI 部件，可不要求同名，但不得冒充官方包。

## 2. 优先级

| 级别 | 范围 | 理由 |
|---|---|---|
| **P0 首屏与主交互** | `layout`、`chat`、`conversation`、`input-trigger`、`attachment`、`model-selection`、`permission-presets`、`approval`、`tool`、`sidebar*`、`session` | 没有这些就没有可用的聊天主界面 |
| **P1 功能面板** | `jobs`、`schedule`、`goal`、`deliverables`、`user-questions`、`commands`、`shortcuts`、`settings*`(11)、`plugin-manager`、`skill`、`subagent`、`workspace`、`plan`、`trajectory` |
| **P2 生态/实验** | `plugin-market`、`plugin-recommendations`、`message-feedback`、`action-cards`、`file-*`、`directory-picker-*`、`cordis`、`agent-team`、`cot-translation`、`forge`、`stats`、`voice-input` |

## 3. 硬规则（与 AGENTS.md 一致）

1. **结构树与素材优先**：开工先读 `$DSH_HOME/DSH-UI-official.md` 与 `$DSH_HOME/dsh-ui-official/`；已有结构树/素材直接复用，不重复拆 asar。
2. **无官方证据就 HIDE**：不得用自定义布局、文案、图标、交互补位；derived 必须记录证据、理由、验收。
3. **不在 UI 层伪造宿主能力**：宿主没真值（如无 PluginList、无真实 provider 分组）时该部件 HIDE，而不是画假数据。
4. **对照官方包名建插件**：新部件按官方包名落目录（例如 `ui-settings-account`），不用 `ds*` 自造名覆盖官方语义。
5. **验收分层**：单测 ≠ headless 渲染 ≠ 可见 GUI；P0 部件必须有真实两尺寸 headless 证据，可见 GUI/OS 键鼠另列未验项。

## 4. 验收

- 每个插件：官方包 → 本仓目录 → 结构树条目 → 素材 sha256 → 两尺寸证据 → 未验项，一行不缺。
- 全仓 `go test ./...`、`go vet ./...`、`go build ./...` exit 0。
- 可见 GUI 验收（真实窗口、键鼠、IME、高 DPI）单列，不因 headless 绿而写成已验。
