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

## 2026-11-03 15:50（执行引导校正）

### 文档归并

- 现行路线统一为 Go 宿主 + 独立 Viva 原生界面；旧 TypeScript/WebView 主界面、12 method、8 包上限、Q 表停工等不再作为工作单。
- `MAINTENANCE.md` 只保留当前动作；`UIPLAN.md` 与 `../dsh-ui/PLAN.md` 保留用户原话、原生决策、分层和可验完成线。
- `../dsh-ui/AGENTS.md` 改为真宿主→连续性→插件授权/替换/卸载→逐部件→完整工作日，不再等 H1–H7 或拿 mock/静态样例冒充整链。
- 快照路径统一为 `cmd/dshd/testdata/tools.snapshot.json` 与 `internal/transport/testdata/api.snapshot.json`；界面快照在 `../dsh-ui/testdata/api.snapshot.json`。

### 初次审读发现 → 随后并行会话的进展

- 初次审读时 H1 缺顶层合计、H8 只包 map 且错误缺 hint、H9 不读 tailMessages。
- 随后宿主线已提交 M243：H8 任意类型信封与错误 hint/retryable、H9 tailMessages，并有契约测试；H1 也已增加 UsageTotal/ContextTokens 和 tail 切页测试。**H8 的 encode_failure 字面量出口仍需补 hint 与失败测试。**
- 随后 M244 增加同版本 H10 链式验证 5/5；M245 补 H1 合计与切页；M246 补 H11 进程级；M247 补 H2 流帧 time。记录保留在 AGENTS §12。
- 新发现的真实审批缺口登记为 H13：M248 提交同形契约设计与验收（`docs/h13-approval.md`）；并行会话随后已加入 `session.approve`、handler 与 3 条契约单测（原样转发、未知 requestId=404、未知 option=400），但 `serve` 的审批推送/子进程应答、允许后执行/拒绝不执行及界面回传仍未验。本地气泡消失不得作为审批通过。

### 界面当前复核重点

先 Connect 再 list/load/subscribe；HTTP/WS 共用 cookie；每会话独立订阅/水位/流正文；gap 只 history 补缺；真实审批回传 id；model/permission/contentWidth/time/usage 真值消费。插件侧补父槽授权、禁用/替换/卸载和公开槽实画。编译期 Go 与 JSON 只读通道分别验收，不冒充完整动态/WASM 生态。

### 安全边界

`scripts/regress.sh` 仍按镜像名 taskkill，`scripts/check.sh` 仍原地移动 modules，本轮未运行两者。失败分支只在旧版本或隔离副本，不改坏共享源码，不用 checkout/reset 覆盖他人。进程只回收测试自有 PID。

### 本轮验证（2026-11-03）

| 检查 | 命令 | 结果 |
|---|---|---|
| 手册引用存在且 Python 语法可解析 | `python -B scripts/doc-links.py` | 11 个引用，缺失 0，语法坏 0，退出 0 |
| 跨仓文档/快照/代码目录引用 | 自定义只读存在性检查 | 26 项全部存在，退出 0 |
| Shell 语法 | `bash -n scripts/check.sh scripts/regress.sh` | 退出 0；只解析，不执行正文 |
| 行尾 | 9 个本轮文档按字节检查 | CRLF=0，LF 正常；已修正三个旧 CRLF 文档 |
| 空白错误 | `git diff --check`（本轮文档） | 退出 0 |

未运行 Go 测试、`-race`、`scripts/check.sh`、`scripts/regress.sh` 或任何真实产品链路；上述结果只证明文档引用/语法自洽，不证明产品完成。审核为只读静态复核；独立审阅意见若与源码冲突，以重读源码和可复跑测试为准。

## 2026-11-03 16:40（H13 父进程侧缺口定位）

只读核对，**未改代码**：

| 检查 | 结果 |
|---|---|
| `grep -c DSHD_APPROVAL_STDIO cmd/dshd/serve.go` | **0**（子进程的标记通道永远不会被启用） |
| `StdinPipe` | **无**（没有把决定写回子进程的通道） |
| `serve_tools.go:49` | `var stderr bytes.Buffer` —— **整段缓冲，不是逐行流** |

第三条是结构性的：现在是「等子进程跑完再看输出」，而审批要求「**跑到一半停下来问**」。
所以父进程侧不是加几行胶水，而是要把 stderr 从缓冲改成 pipe + 逐行扫描，
并确认不破坏现有的 `turn/error` 播报（`scripts/ui-e2e.py` 14/14 依赖它）。

结论：H13 保持 🟡，三段（开关 / broker / 接线）都还没有；施工图见 `tmp/task-approval-parent.md`。
