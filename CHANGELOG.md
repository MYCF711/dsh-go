# CHANGELOG

格式：`## <版本> — <日期>`，每条注明**上游映射**。语义化：修 bug → `go.N+1`；跟上游新版本 → `<新版本>-go.1`。

## 上游映射表

| 本仓版本 | DSH 上游 | 移植了什么 | 跳过什么 |
|---|---|---|---|
| `0.2.1-go.5` | `0.2.1-rc.5` | 会话 v4 契约、61 工具 schema、ACP 方法表、cordis 装配语义 | Electron/React 前端、provider 生态、`!!js` |

---

## 0.2.1-go.5 · M1-d — 2026-11-02

**shell + 后台任务：真实 bash 进程（R6 的第一步落地）。**

- `modules/exec/bash`
  - 常驻 bash 进程（`--noprofile --norc -s`），**cwd 跨命令保留**
  - 结束哨兵 `DSHD:<seq>:<rc><cwd>`；输出与退出码从同一条流里解析（stdout/stderr 合流，M1 取舍）
  - **自动转后台**：闲置超过 `idle_seconds`（默认 600s）就把整个会话交给后台任务，下次调用自动换新会话
  - **会话自愈**：命令把 shell 打死（如裸 `exit`）时报错并重建，不伪装成功
- `shell` / `job_output` / `job_kill` / `job_list` 四个工具（`exec` 模块提供 `exec` 与 `jobs` 两个服务）
- 大输出不需要工具自己截断：流水线的截断层会把 >4000 字符的结果外置成 artifact

**实测（CLI）**
```
dshd exec --tool shell --args '{"command":"echo hello && pwd"}'
→ 退出码 0，输出 hello / D:/DeepSeek Harness/default-workspace/DSH-GO
```

**已知取舍**：`export` 不跨命令保留；`job_kill` 只杀 bash 进程，其子进程可能存活。

---
## 0.2.1-go.5 · M1-c — 2026-11-02

**glob / grep：独立工具，不是 shell 的包装。**

- `internal/fsutil`：共享遍历规则（跳过 `.git` `node_modules` `refs` `dist` `bin` `tmp`）+ 二进制判定
- `tool-glob`：`**` 跨目录、`*`/`?`/字符类（单段 `path.Match`）；结果排序稳定、上限 200（可到 1000）、截断给 Hint
  - **刻意不支持花括号展开**：少一种隐式语法，少一类"为什么没匹配上"
- `tool-grep`：Go 正则、`include` 文件过滤、`ignore_case`、`limit` 100（可到 500）
  - 只回命中行（带行号），不回上下文噪声；单文件 >2 MiB 与二进制跳过并计数
  - 返回 `ViewSpec=table`（文件/行/内容），客户端直接渲染成表

**实测（CLI）**
```
glob internal/**/*.go            → 10 个文件（命中上限，truncated=true）
grep "func Assemble" include=**/*.go → internal/kernel/assemble.go:22（扫描 41 文件）
```

---
## 0.2.1-go.5 · M1-b — 2026-11-02

**文件组（read / write / edit）与读后写观测策略。**

- `modules/fs/local`：`spi.FS` 默认实现
  - 路径归一：统一正斜杠、相对工作区根；`..` 与工作区外绝对路径一律拒绝
  - **读后写**：文件存在时必须先 `read`；读过之后文件被改过要重读（stat 先于读，宁可多读一次）
  - 新建文件不需要先读；自己写的文件视为已观测
- `spi/keys.go`：服务键集中定义（模块间只经 spi 相遇，不互相 import）
- `tool-read` 迁到 `spi.FS`（读即登记观测）
- `tool-write`：新建/覆盖；覆盖前过读后写策略
- `tool-edit`：逐字替换；**默认要求唯一匹配**，多处必须显式 `replace_all`；空 old_string、无变化编辑一律拒绝
- `dsh.toml` 增加 `fs` / `tool-write` / `tool-edit`

**实测（CLI 跨进程）**
```
dshd exec write 新文件      → 新建 tmp/rbw.txt（4 字节）
dshd exec write 覆盖未读     → 还没读过 tmp/rbw.txt；先 read 它再改（读后写策略：不做盲改）
```

---
## 0.2.1-go.5 · M1-a — 2026-11-02

**工具流水线（三条硬约束）与结果投影。**

- `modules/tools/pipeline.go`：唯一执行入口 `Runner`
  - 四槽：`tools/pre-execute`（waterfall，可 veto）→ `execute` → `tools/post-execute`（可改写）→ `tools/result`（广播）
  - **序列化层**：`Data` 必须是合法 JSON，否则转成带 Hint 的错误
  - **截断层**：超 4000 字符外置成 artifact + 指针；替换体 ≤ 阈值 1/4（防「取回又被外置」死循环）；落盘失败保留内联，绝不把成功变失败
  - **提示层**：失败没给 Hint 就兜底并记账（`Violations()`），日志告警
- `internal/kernel.OnTyped[T]`：载荷类型化订阅，类型不符返回明确错误而不是静默跳过
- `internal/session/project.go`：`Result → content[]` 投影（摘要→数据→截断→提示，顺序固定、同输入同字节）+ `Flatten`
- 模块 `tools` 同时提供 `tools` 与 `toolrunner` 两个服务：只给目录不给 Runner 等于允许绕过全部中间件
- CLI `dshd exec` 改走 Runner，端到端覆盖中间件

**实测**：43 个测试全绿；门禁 15 项 ALL GREEN；`dshd exec --tool read` 经流水线返回结构化结果。

---
## 0.2.1-go.5 · M0 — 2026-11-02

**内核四件事 + 工具 SPI + 装配器，门禁全绿。**

- `internal/kernel`
  - `Scope`：`Effect` / 级联撤销 / `-race` 安全的锁序 / disposer panic 隔离
  - `Provide` / `Inject`：泛型键、**部署级命名空间**（一个能力一个提供者）、缺能力 `ErrMissingService` fail-loud
  - `Bus`：emit / serial / bail / waterfall / parallel，五模式齐备，监听器 panic 一律转错误
  - `Assemble`：能力解析 → 拓扑排序 → 逐模块实例化 → **失败回滚**
- `internal/spi/tool.go`：`Tool` / `Result`（Summary·Data·Artifacts·Truncated·Hint·View）/ `ViewSpec`（card·table·plain）/ `ToolRegistry`
- `modules/tools`：工具目录（重名即错、顺序稳定）
- `modules/tool/read`：第一个真实工具，结构化 `Data` + 截断 `Hint` + plain `View`
- `dsh.toml`：按 id 命中、`enabled` 过滤、config **整块替换**、未知 id 报错
- CLI：`dshd modules graph|list`、`dshd exec --tool --args`、`version`
- 门禁 `scripts/check.sh`：build / vet / test / R1 / A3 / 文档 / 凭据，9 项 ALL GREEN

**实测**
- 禁用提供者模块 → `模块 "tool-read" 需要能力 "tools"，但没有模块提供它`
- 未知模块 id → `模块 "ghost" 没有编译进二进制；可用模块: [tool-read tools]`
- `dshd exec --tool read` → 结构化 Data + Hint + View

**未验证**：`-race`（本机 `CGO_ENABLED=0`、无 gcc；CI 上待验）

---

## 0.2.1-go.5 · 初始化 — 2026-11-02

- 方案：`PLAN.md` v1.0（十项方向已拍板、架构、M0–M5、CI 七条）
- 手册：`AGENTS.md`（六条红线、代码地图、会话 v4 字节级清单）
- 调研（6 份，1,424 行，全部带 `文件:行号`）：
  - `research/10-dsc-reference.md`、`research/11-dsc-reuse.md`
  - `research/20-dsh-tool-schemas.{json,index.md}`（61 工具 / 12,209 token 实测）
  - `research/21-dsh-session-v4.md`（62 事件全表 + zstd 多帧物理格式）
  - `research/22-dsh-acp.md`（28 个 method + 15 个 update 变体）
  - `research/23-dsh-plugin-surface.md`（96 条 bundle entry + 104 个 service key）
  - `research/30-go-env-probe.md`（11 个依赖实测；go-plugin/wazero 端到端跑通）
- 审计：`evidence/`（Node 绑定面、dsh↔dsc 覆盖、59 个真实工具名）
- 维护：`MAINTENANCE.md` / `CHECKLIST.md` / `UPDATING.md` / `scripts/check.sh` / CI
- 结论：M0 未开始；下一步 `go mod init dshgo` + `internal/kernel` + `internal/spi/tool.go`
