# dsh-go

用 Go 重写 [DeepSeek Harness](https://github.com/deepseek-ai)（DSH）的宿主。

**一句话**：内核只做插件运行时，任何业务能力——包括 agent loop、工具、会话、传输、UI——都由 manifest 装配、都能被换掉；工具层为模型设计，不为 shell 设计。

| | 现状（实测） | 目标 |
|---|---:|---:|
| 常驻私有工作集 | 870 MB | < 150 MB |
| 冷启动 | 数秒 | < 100 ms |
| 工具表 token | 12,209 | ≤ 6,000 |

## 文档

| 文件 | 讲什么 |
|---|---|
| [PLAN.md](PLAN.md) | **方案**：十项已拍板决策、架构、里程碑、CI 规则 |
| [AGENTS.md](AGENTS.md) | **操作手册**：六条红线、代码地图、写代码的规矩 |
| [MAINTENANCE.md](MAINTENANCE.md) | **维护台账**：接手先读这页 |
| [CHECKLIST.md](CHECKLIST.md) | **交付门**：代码交付前 / 发版前两张表 |
| [UPDATING.md](UPDATING.md) | **发布流程** |
| [CONTRIBUTING.md](CONTRIBUTING.md) | 贡献约定 |
| [CHANGELOG.md](CHANGELOG.md) | 变更日志与上游版本映射 |
| `research/` | 六份调研（全部带 `文件:行号`） |
| `evidence/` | 三份审计原始产物 |

## 状态

宿主与独立 `../dsh-ui` 原生界面均已有实现，当前执行入口见 `AGENTS.md`；宿主顺序见 `PLAN.md` §14.1，跨线契约与证据见 `docs/transport-contract.md`。不要把最初 M0–M5 或 P0 工具清单当成当前工作单。

## 版本

`<上游版本>-go.<N>`，N 是 Go 自有序号，不是上游 rc 序号；当前值读源码与 `CHANGELOG.md`。上游基线为 DSH `0.2.1-rc.5`。
仓库私密开发，**不发 npm**；分发走 GitHub Release 的二进制。

## 许可

MIT，见 [LICENSE](LICENSE)。
