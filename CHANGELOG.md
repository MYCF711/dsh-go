# CHANGELOG

格式：`## <版本> — <日期>`，每条注明**上游映射**。语义化：修 bug → `go.N+1`；跟上游新版本 → `<新版本>-go.1`。

## 上游映射表

| 本仓版本 | DSH 上游 | 移植了什么 | 跳过什么 |
|---|---|---|---|
| `0.2.1-go.5` | `0.2.1-rc.5` | 会话 v4 契约、61 工具 schema、ACP 方法表、cordis 装配语义 | Electron/React 前端、provider 生态、`!!js` |

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
