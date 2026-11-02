# MAINTENANCE.md —— 维护台账（给接手的 agent 看）

> 接手前先读这一页，读完再按 `AGENTS.md` §5 的流程开工。
> **每次干完活必须回来更新「当前状态」与「下一步」。**

---

## 一、身份

| 项 | 值 |
|---|---|
| 仓库 | `MYCF711/dsh-go`（**私有**） |
| 形态 | Go 单体二进制 + 插件目录（static / wasm / process 三层） |
| 上游基线 | DSH `0.2.1-rc.5` |
| 当前版本 | `0.2.1-go.5` |
| 分发 | GitHub Release 二进制；**不发 npm**（用户明确要求） |

## 二、当前状态（2026-11-02）

```
分支        main
里程碑      M0 未开始（产品代码零行）
已有资产    方案 PLAN.md / 手册 AGENTS.md / 调研 research 6 份 / 审计 evidence 3 份
```

| 资产 | 数量 |
|---|---|
| 调研文档 | 6 份（1,424 行，全部带 `文件:行号`） |
| 工具 schema | 61 个，54,936 字符 / 12,209 token（`research/20-dsh-tool-schemas.json`） |
| 参考源码 | `refs/dsc`（**不进 git**，`scripts/fetch-refs.sh` 拉取） |
| 依赖验证 | `probe/`（go-plugin、wazero 已端到端跑通） |

## 三、下一步

1. **M0**：`go mod init dshgo` → `internal/kernel`（scope/registry/eventbus/assemble）→ `internal/spi/tool.go`。
2. **M0 验收**：`scripts/check.sh` 全绿 + 删 `modules/` 仍能编译 + 禁用任意模块报「缺 X 能力」。
3. 之后按 `PLAN.md` §14 推进 M1→M5，一里程一个 tag。

## 四、硬约束（违反即返工）

1. **内核零业务**（R1）：`internal/kernel` 不认识 agent/tool/session/llm。
2. **SPI 之外无通路**（R2）：模块只拿 `*Scope`，不拿 `*App`、不写包级变量。
3. **工具结构化 + 有界 + 错误带 Hint**（R3）。
4. **会话日志 append-only**（R5）：视图永远是投影。
5. **不引 JS 运行时、不 fork/vendor dsc、不用 mvdan 执行命令**（R6）。
6. **没跑过的写成「未验证」，不写「通过」。**
7. **不动真机**：不重启 DSH、不写 `D:/DeepSeek Harness/home/profiles/`、asar 只读。

## 五、最短接手路径

`README.md`（是什么）→ **本文件**（现状与约束）→ `AGENTS.md`（怎么写）→ `PLAN.md` §1（十项决策，别重新讨论）→ `CHECKLIST.md`（交付门）→ `UPDATING.md`（怎么发版）。
