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
| 当前版本 | `0.2.1-go.8` |
| 分发 | GitHub Release 二进制；**不发 npm**（用户明确要求） |

## 二、当前状态（2026-11-02）

```
分支        main
里程碑      **M0–M5 完成并发布**（0.2.1-go.6）；M6 压缩全自动已接进 ACP 循环
                    https://github.com/MYCF711/dsh-go/releases/tag/v0.2.1-go.6
版本        0.2.1-go.5（未打 tag）
已有资产    方案 / 手册 / 调研 6 份 / 审计 3 份 / Go 骨架 13 个文件
```

| 资产 | 数量 |
|---|---|
| 调研文档 | 6 份（1,424 行，全部带 `文件:行号`） |
| 工具 schema | 61 个，54,936 字符 / 12,209 token（`research/20-dsh-tool-schemas.json`） |
| 参考源码 | `refs/dsc`（**不进 git**，`scripts/fetch-refs.sh` 拉取） |
| 依赖验证 | `probe/`（go-plugin、wazero 已端到端跑通） |

## 三、下一步

1. **`shell`**：**真实 bash 进程 + 持久会话 + 自动转后台；不用 mvdan 执行（R6）。
2. ：真实 bash 进程 + 持久会话 + 自动转后台；**不用 mvdan 执行**（R6）。
3. **M1 收口**：工具一致性套件 + `testdata/tools.snapshot.json` 快照 + 工具表 token 实测。

### M0 的验收证据（已跑）

| 判据 | 结果 |
|---|---|
| `dshd modules graph` 打印激活顺序 | `tools` → `tool-read`（由能力依赖决定，与 manifest 行顺序无关） |
| 删 `modules/` 后 `go build ./internal/...` | 通过 |
| 禁用提供者模块 | `模块 "tool-read" 需要能力 "tools"，但没有模块提供它` |
| manifest 写不存在的 id | `模块 "ghost" 没有编译进二进制；可用模块: [tool-read tools]` |
| `dshd exec --tool read` | 返回结构化 `Data` + `Hint` + `View` |
| `scripts/check.sh` | ALL GREEN（9 项 PASS） |
| `-race` | **未验证**：本机 `CGO_ENABLED=0` 且无 gcc |

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
