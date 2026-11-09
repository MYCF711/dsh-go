# dsh-go

用 Go 重写 [DeepSeek Harness](https://github.com/deepseek-ai)（DSH）的**宿主**。

**一句话**：内核只做插件运行时，任何业务能力——agent loop、工具、会话、传输——都由 manifest 装配、都能被换掉；工具层为模型设计，不为 shell 设计。

> 界面不在本仓。原生界面（Go + Viva）在 `../dsh-ui`；本仓只提供 Transport 与宿主能力。

---

## 现在能做什么

| 能力 | 状态 |
|---|---|
| 宿主 serve（HTTP `/api` + 每会话 WS） | ✅ 可用 |
| **dsh-TUI 直连**（真实终端界面） | ✅ 可用，见下节 |
| 会话 / 消息 / 工具 / 审批 / 取消 / 恢复 | ✅ 可用 |
| 后台任务、终端、日程、目标、压缩、模型切换 | ✅ 可用 |
| 官方 0.2.2 方法对齐 | 🟡 82/153 已接线，其余逐条收口（见 [兼容性](#兼容性)） |
| 沙盒 | 🟡 策略层（三档 + 一次性升级）；**无 OS 级隔离** |

版本：`0.2.2-go.1`（上游基线 `0.2.2-rc.1`；双渠道已统一，不再有 beta 线）。

---

## 快速开始

```bash
# 1) 构建
export PATH="/c/Program Files/Go/bin:$PATH"     # Windows / Git Bash
go build ./...

# 2) 起宿主（--port 0 = 由系统分配端口，启动后会打印 url 与 token）
./dshd.exe serve --home /tmp/dsh-home --addr 127.0.0.1:0 --policy ask

# 3) 用 curl 冒烟
curl -s -c /tmp/c.txt "http://127.0.0.1:<port>/connect?token=<token>"
curl -s -b /tmp/c.txt -X POST "http://127.0.0.1:<port>/api/session.list" -d '{}'
```

真实模型需要网关：

```bash
export DSHD_LLM_BASE="http://127.0.0.1:3425/v1"
export DSHD_LLM_KEY="<your-key>"
```

环境变量速查（宿主自己的都是 `DSHD_` 前缀；官方 `DSH_*` 名只有下面两行真接了，其余见
[docs/parameter-register.md](docs/parameter-register.md) §7 的 env 域）：

| 变量 | 作用 |
|---|---|
| `DSHD_HOME` | 会话根目录（`--home` > `DSHD_HOME` > `~/.dshd`）。**官方的 `DSH_HOME` 不是它**：`DSH_HOME` 在本宿主只用于删除保护/历史守卫，而 TUI2GO 的 `run-019.cmd` 里 `DSH_HOME` 是 **DSH 桌面 profile home**——两个语义撞车，所以不设别名（见 [docs/go5-env-notes.md](docs/go5-env-notes.md) §4）。 |
| `DSH_TELEMETRY_OTLP_URL` / `DSH_PRODUCT_ANALYTICS_OTLP_URL` | 覆盖 analytics 的 collector 端点（优先级：显式配置 > env > 默认）。**默认仍是本地零出网**：只改端点、不改 mode，要外发得显式把策略改成 `local+export`；非法 URL 直接报错。 |
| `DSH_MAX_TOKENS_AS_SUCCESS` | 默认 `true`：`finish=length` 当作这一轮跑完了（日志记 `max-tokens`）。设 `false` / `0` / `no` 则把它结算成失败（日志记 `error`）。 |

---

## 用 dsh-TUI 玩

**dsh-TUI 是官方终端界面，本宿主是它的后端之一（`--backend dsh-go`）。** 已经可以直接用。

```bash
# 1) 起宿主（同上），记下打印的 url 与 token

# 2) 起 TUI，指向本宿主
node ../dsh-TUI/bin/dsh-tui.js --backend dsh-go \
  --base http://127.0.0.1:<port> --token <token>

# 无头自检（不需要终端，适合 CI）
node ../dsh-TUI/bin/dsh-tui.js --backend dsh-go --dump-screen --size 120x36
```

TUI 里常用的东西：

| 你要做的 | 怎么做 |
|---|---|
| 发消息 | 直接打字 + `Enter` |
| 中断当前回答 | `Esc` |
| 切换模型 | `/model` |
| 切权限模式 | `/permission`（read-only / workspace-write / danger-full-access） |
| 看后台任务 | `/jobs` |
| 压缩上下文 | `/compact` |
| 导出会话 | `/export`（Markdown） |
| 恢复历史会话 | 启动时 `--session <id>` |
| 断线重连 | 自动（WS journal 续传，不重放历史） |

> 已知限制：TUI 侧 `/permission` 每项会画两行、极窄终端下工具卡参数偶见多余括号——这两个是 TUI 渲染问题，已登记在 `LEAD.md` 的 KNOWN-ISSUE，**不在宿主**。

---

## 架构

```
cmd/dshd           宿主入口（serve / run / acp / schedule / 子命令）
internal/kernel    插件运行时：只认 Scope，不认业务
internal/spi       模块契约（Tool / FS / …）
internal/transport HTTP /api + /ws，每会话一个 journal
internal/session   v4 append-only 会话日志（唯一真值）
internal/officialwire  官方 0.2.2 wire（/connect cookie、envelope、remote.mux、$events）
modules/           业务能力：exec/bash、fs、tool/*、scheduler、goal……
codecs/            v4 编解码
```

三条硬规矩（详见 [AGENTS.md](AGENTS.md)）：内核不识业务、模块只拿 `Scope`、只有 Transport 能跨线。

---

## 兼容性

官方桌面端 0.2.2-rc.1 有 **153 个 typert 方法**（方法名全集见 [`docs/evidence/official-desktop-api-methods-0.2.2-rc.1.txt`](docs/evidence/official-desktop-api-methods-0.2.2-rc.1.txt)）。

- **逐方法状态**：[docs/official-desktop-api-parity.md](docs/official-desktop-api-parity.md) —— 已接线 82，其余按 account / 外部服务 / 媒体市场三组收口，每条都给「真实现」或「官方形状未实现 + 缺什么」。
- **Wire 兼容**：`/connect` cookie、`POST /api/<ns>/<method>` envelope、200-on-error、`/api/remote.mux` 七帧、`$events` 全生命周期、multipart 附件——已用**未修改的官方客户端包**联调通过。
- **宿主插件面**：97 项官方 base 的 DONE/PARTIAL/MISSING 见 [docs/official-plugins-022-map.md](docs/official-plugins-022-map.md)。

---

## 仓库结构

```
AGENTS.md        给 AI 的操作手册（红线、代码地图、验证规则）
LEAD.md          派单账本：谁在做什么、做到哪、证据在哪
PLAN.md          方案：架构决策与里程碑
docs/            契约、证据、审计、官方结构树
scripts/         门禁与探针（bin-check / doc-links / regress …）
refs/            上游参考源码（不入库，scripts/fetch-refs.sh 拉取）
```

发版产物**不入库**：`*.exe` 已在 `.gitignore`，走 GitHub Release 附件。

---

## 分支保护

**禁止强推。** `.githooks/pre-push` 会拒绝任何非快进更新（`git config core.hooksPath .githooks` 已设）。

协作方式：

```bash
git worktree add ../<task>-<id> -b task/<id>   # 每个任务一棵树
# … 干活 …
git merge --no-ff task/<id>                     # 全绿后显式合并
```

- 禁止 `git add -A`（会带进别人半成品）。
- 测试红的分支不进主线。
- 稳定版过 CI 才推 GitHub；日常分支只留本地。

---

## 许可

MIT，见 [LICENSE](LICENSE)。

上游项目与商标归 DeepSeek 所有；本仓是独立重写实现。
