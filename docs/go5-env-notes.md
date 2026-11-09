# GO.5 / T1：官方 env 语义收口（OTLP 端点 / max-tokens 结算 / DSH_HOME 命名冲突）

> 状态：**已实现或已成文**（2026-11-09，GO.5 T1）。证据 `tmp/GO5-DELIVERABLE/env/RESULT.md`，
> 总账行见 `docs/parameter-register.md` §7 env 域与 `docs/parameter-register.json`。
>
> 背景：GO.3 总账查出 **6 条官方 env 名在宿主不生效或只部分生效**。本轮把能真实现的做成真实现，
> 把不能实现的写成显式语义（不再让用户"照官方文档设了却没反应"）。

## 1. 一张表看完（6 条 env 的收口结果）

| 官方 env | GO.3 时状态 | 本轮结果 | 宿主读取点 |
|---|---|---|---|
| `DSH_TELEMETRY_OTLP_URL` | 0 处读取 | **真接**：显式设置时覆盖 `CollectorURL` | `internal/analytics/env.go` `CollectorURLFromEnv` |
| `DSH_PRODUCT_ANALYTICS_OTLP_URL` | 0 处读取 | **真接**：同上（比通用名更优先） | `internal/analytics/env.go` |
| `DSH_MAX_TOKENS_AS_SUCCESS` | 0 处读取 | **真读**：`false/0/no` 时 finish=length 结算成失败 | `internal/agent/finish_policy.go` `MaxTokensAsSuccess` |
| `DSH_HOME` | 只部分生效（删除保护） | **不实现 → `N-A（命名冲突，见说明）`** | 见 §4 |
| `DSH_WEB_SEARCH_PROVIDER` | 双前缀已生效 | **核对后保持** `COVERED-EQUIV` | `internal/spi/search.go:375` |
| `DSH_WEB_FETCH_PROVIDER` | 双前缀已生效 | **核对后保持** `COVERED-EQUIV` | `internal/spi/search.go:388` |

## 2. OTLP 端点：显式配置 > env > 默认（本地零出网）

### 2.1 优先级（单点定义在 `internal/analytics/env.go`）

```
显式配置（policy.json 里非空 collectorUrl）
  > 环境变量（越具体的名字越优先）
      1. DSHD_PRODUCT_ANALYTICS_OTLP_URL
      2. DSH_PRODUCT_ANALYTICS_OTLP_URL      ← 官方名
      3. DSHD_TELEMETRY_OTLP_URL
      4. DSH_TELEMETRY_OTLP_URL              ← 官方名
  > 默认（空 = 本地零出网，一个字节都不发）
```

- 宿主自己的前缀（`DSHD_*`）与官方名同义，都喂同一个字段（与 `internal/spi/search.go` 的双前缀约定一致）。
- **环境变量只回答"端点在哪"，不回答"发不发"**：读到 env 不会把 `mode` 从 `local` 抬成
  `local+export`。默认仍是本地零出网（红线 1），要外发必须显式改策略。
- 非法 URL（非 http/https、缺 host、解析失败）**fail-loud**：`analytics.Open` 直接返回错误，
  错误原文点名是哪个变量。绝不静默回退成默认端点。

### 2.2 自己验一遍（不依赖任何测试框架）

```bash
# 1) 不设 env：CollectorURL 为空、mode=local
dshd analytics policy --home /tmp/analytics-demo
#    → {"enabled":true,"mode":"local","retentionDays":30,"samplingRate":1,"policyVersion":1}

# 2) 设官方名：端点进来了，但 mode 仍是 local（不外发）
DSH_TELEMETRY_OTLP_URL=http://127.0.0.1:9/v1/logs dshd analytics policy --home /tmp/analytics-demo
#    → {"enabled":true,"mode":"local","collectorUrl":"http://127.0.0.1:9/v1/logs",...}

# 3) 非法端点：必须报错（不是静默忽略）
DSH_PRODUCT_ANALYTICS_OTLP_URL=ftp://nope/v1/logs dshd analytics policy --home /tmp/analytics-demo
#    → analytics: 环境变量 DSH_PRODUCT_ANALYTICS_OTLP_URL 必须是 http/https URL，收到 "ftp://nope/v1/logs"
```

单测：`go test ./internal/analytics/ -run 'CollectorURL|OTLP'`（7 条，含"不设 env 不变"、
"设了之后零出站"、"非法 URL fail-loud"、"显式配置压过 env"）。

## 3. `DSH_MAX_TOKENS_AS_SUCCESS`：把 max-tokens 结束当成功，除非显式 false

### 3.1 官方语义与宿主实现

官方原件 `@dsa/dsh-sdk-app/cordis.patch.yml:21`，默认 `true`。

| 变量值 | finish=length 的结算 | v4 日志里的 `turn/end.reason.kind` |
|---|---|---|
| 未设置 / `true` / `1` / `yes` / 其它 | 这一轮跑完了（只是被截断） | `max-tokens` |
| `false` / `0` / `no`（大小写不敏感） | **失败**，错误原文点名变量 | `error`（`error.message` 含 `DSH_MAX_TOKENS_AS_SUCCESS`） |

- 只认三种 false 写法；其余一律按官方默认 true（"除非显式 false"这句话本来就是这个意思）。
- 闸门在 `internal/agent/loop.go` 里、**任何副作用之前**：被截断的响应不写历史、不跑工具。
- 成功路径的 `max-tokens` 不是"完成"：客户端据此知道回答被截断了（既有行为不变）。

### 3.2 验一遍

```bash
# 单测：解析器 + 循环结算
go test ./internal/agent/ -run 'MaxTokens' -v
# 单测：宿主结算层（真读 v4 日志里的 turn/end kind）
go test ./cmd/dshd/ -run 'TestMaxTokensAsSuccessDrivesTurnSettlement' -v
```

## 4. `DSH_HOME`：**命名冲突，不做别名**（总账 verdict 改 `N-A`）

### 4.1 两个 DSH_HOME 指的不是同一件事

| 来源 | `DSH_HOME` 的含义 | 真实值 |
|---|---|---|
| **官方 DSH**（`@dsa/dsh-home-paths/lib/index.js:15`） | 整个 DSH 的数据根：`显式路径 > $DSH_HOME > ~/.dsh` | 会话、插件、设置全在它下面 |
| **宿主 dsh-go** | home 根走 **`DSHD_HOME`**（`--home` > `DSHD_HOME` > `~/.dshd`）；`DSH_HOME` 只在 3 处**删除保护/历史守卫**里读（`session.UnderDSHHome`、`session.HistoryGuard`、relayd 持久目录回退） | 设 `DSH_HOME` **不会**改会话根目录 |
| **TUI2GO**（`public/dsh-tui2go/launcher/run-019.cmd:6`） | **DSH 桌面 profile home**（`PLAY\home`，装被 overlay 打过补丁的 0.19 TUI 包） | 与上面两个都不是同一个根 |

### 4.2 所以为什么不能"顺手加个别名"

1. **语义撞车**：如果宿主把 `DSH_HOME` 当 home 根，TUI2GO 的 launcher 会在同一台机器上把宿主
   的会话库指到 **桌面 profile home**，两个产物共用一份数据目录——会话/插件/策略互相污染。
2. **覆盖顺序不同**：官方是 `显式路径 > $DSH_HOME > ~/.dsh`；宿主是 `--home > $DSHD_HOME > ~/.dshd`。
   强行合并会让"显式路径"这一层在不同入口上优先级不同。
3. **TUI2GO 那条线不能改**（另一条任务线拥有 launcher），改名会把人家刚做完的可搬移重新打破。

### 4.3 结论与写法

- 总账里 `DSH_HOME` 的 verdict 已改为 **`N-A（命名冲突）`**，`naClass = naming-conflict`。
- **宿主 home 根用 `DSHD_HOME`**；`DSH_HOME` 在宿主里的既有用途只有删除保护/历史守卫，维持不变。
- **TUI2GO 里的 `DSH_HOME` 是桌面 profile home**，与本宿主的 home 根无关。
- 想要"官方那样的一个根"，照宿主的规则设 `DSHD_HOME`（或用 `--home`）。

## 5. `DSH_WEB_SEARCH_PROVIDER` / `DSH_WEB_FETCH_PROVIDER`：核对后保持

`internal/spi/search.go:375 / :388` 的 `WebSearchPinFromEnv` / `WebFetchPinFromEnv` 同时认
`DSHD_*` 与官方 `DSH_*` 两个前缀（都设时 `DSHD_*` 先命中），语义与官方"等价于
`searchProvider` / `fetchProvider` 配置值"一致。本轮**只核对、不改**，总账 verdict 保持
`COVERED-EQUIV`。

## 6. 边界（本任务没做、也不该在这里做的事）

- 不改 TUI2GO launcher（`run-019.cmd` / `TUI-GO.vbs` / `launcher/*` 属另一条线）；`DSH_HOME` 的
  launcher 侧语义原样保留。
- 不改 OTLP **导出器**（`internal/analytics` 的 exporter 仍是本仓自研的 JSONL POST，不是 OTLP
  protobuf/gzip 批量通道）；本条只把"官方 env 名 → 端点字段"接上。
- 不把 `DSH_MAX_TOKENS_AS_SUCCESS` 的默认改成 false：官方默认就是 true。
