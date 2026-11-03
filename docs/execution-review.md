# 执行复核台账

> 只记**可复跑的**证据：命令 + 当时的输出。历史报告、✅、注释都不算通过证据。
> 每次复核在下面追加一节，**不改写旧节**。

## 2026-11-03 15:30（宿主线）

| 项 | 命令 | 当时结果 |
|---|---|---|
| 构建 | `go build ./...` | 通过 |
| 测试 | `go test ./... -count=1` | 无 FAIL |
| 门禁 | `bash scripts/check.sh` | ALL GREEN（14 项） |
| 会话 e2e（临时 home） | `python scripts/full-e2e.py` | **15/15** |
| 真语料只读 | `python scripts/real-home-e2e.py` | **10/10** |
| 界面调用序列 | `python scripts/ui-e2e.py` | **14/14** |
| 工具表 | `./dshd.exe tools` | **47 个 / 25,119 字节** |

### §12 契约（宿主侧）

- ✅ H1 usage、H2 time、H3 view、H4 model、H5 permission(=policy)、H7 content 两种落法、
  H8 信封、H9 入参别名、H11 stdout 只有 ready、H12 contentWidth（默认空）
- 🟡 H6：`assistant/message` 已能进 WS 流（收尾补读已修），**`reasoning` 块仍未在活体上验到**
- 🟡 H10：宿主侧复现不出 404；已加 `DSHD_DEBUG_RESOLVE=1` 排查开关，等界面线回贴日志

### 已知未验

- `-race` 在本机跑不了（无 C 编译器），并发点是 `scripts/race-guard.py` 结构性登记（13 处），
  **那是代理，不是证明**。
