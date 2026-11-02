# CHANGELOG

格式：`## <版本> — <日期>`，每条注明**上游映射**。语义化：修 bug → `go.N+1`；跟上游新版本 → `<新版本>-go.1`。

## 上游映射表

| 本仓版本 | DSH 上游 | 移植了什么 | 跳过什么 |
|---|---|---|---|
| `0.2.1-go.5` | `0.2.1-rc.5` | 会话 v4 契约、61 工具 schema、ACP 方法表、cordis 装配语义 | Electron/React 前端、provider 生态、`!!js` |

---

## 0.2.1-go.5 — 2026-11-02

**初始化，无产品代码。**

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
