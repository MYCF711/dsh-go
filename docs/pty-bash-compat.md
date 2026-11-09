# PTY 与 Git Bash 5.3 的已知兼容性（环境依赖）

> 本文记录**不是本仓缺陷**、但会改变 PTY 行为的上游/环境问题，写清版本、症状、
> 最小复现、验证命令和「什么版本下会恢复」。复验前先读 §1 的结论。
>
> 结论一句话：**Git Bash 5.3.15（MSYS 3.6 / Cygwin）在 ConPTY 下的
> console 输入读侧吞吐塌到 ~25 B/s，且 readline 8.3 在 win32-input-mode
> 下对「成块输入」整段挂住；本仓 `internal/terminal` 的传输层没有缺陷**
> （同一写路径、同一 ConPTY，原生读侧 65536 字节 0.23s 逐字节一致）。

## 1. 版本与症状

| 项 | 值 |
|---|---|
| 出问题的 shell | `GNU bash, version 5.3.15(2)-release (x86_64-pc-cygwin)` |
| 出问题的 Git | `git version 2.55.0.windows.5`（Git for Windows，MSYS 3.6 线） |
| 平台 | Windows 10/11 x64 + ConPTY（Win10 1809+ 的 `CreatePseudoConsole`） |
| 首次发现 | 2026-11-09，`go test ./internal/terminal` 稳定红（三次独立运行） |
| 干净基线 | `git archive 99a5473`（GO.6 之前的 main）同样红 —— **不是 GO.6 引入** |

症状（三条互相独立，可分别复现）：

1. **长行不执行**：往交互式 bash 写一整行 ≥ 约 96 字节（`echo xxxx… MARKER\n`），
   **既没有回显也没有执行**；再写一条短命令时，前面那条长行才「一起」执行。
2. **粘贴被拖死**：MSYS 读侧（`head -c 65536`、`cat`）在 `stty -icanon -echo`
   原始模式下只有 **~25 B/s**：实测 2s 收到 512 字节、22s 收到 1024 字节，
   64KB 需要约 40 分钟。
3. **`--noediting` 立刻恢复**：`bash --noprofile --norc --noediting` 下同样的
   139 字节长行**正常回显并执行**；`TERM=dumb` 同样正常。

## 2. 排除本仓传输层（关键证据）

| 实验 | 结果 | 含义 |
|---|---|---|
| 原生 Go 读侧（测试二进制 re-exec）经同一 ConPTY 收 65536 字节 | **0.23s，`bytes.Equal` 全等** | 写路径 + ConPTY 管道逐字节完整送达 |
| 原始模式 `stty -icanon -echo` + `cat`，单次 139 字节写 | **全部到达**（140 字节，含 `^D`） | 单次大写在 ConPTY 层没问题 |
| 交互式 bash（readline）同一条 139 字节写 | 无回显、不执行 | 卡在 **readline 8.3** 的输入消费 |
| `bash --noediting` 同一条 139 字节写 | 正常执行 | 关掉 readline 即恢复 |
| 管道输入缓冲 `CreatePipe(..., 1<<20)` 改成 4KB 再跑 | 只是变慢（n=256 从 ~4s 变成 15.8s），**仍然失败** | 与宿主管道缓冲无关，已回退 |

所以「输入到不了子进程」这一层已被排除：**字节到了，是 MSYS/readline 读不动。**

## 3. 最小复现

`tmp/GO6-PARAMS/pty-bash53/probe/main.go`（Go，直接用本仓 `internal/terminal`）：

```bash
go run ./tmp/GO6-PARAMS/pty-bash53/probe          # 默认：交互式 readline
go run ./tmp/GO6-PARAMS/pty-bash53/probe -noedit  # 对照：--noediting
```

期望输出（当前环境）：

```
[readline ] 139B-line executed=false    ← 上游缺陷
[noediting] 139B-line executed=true     ← 关掉 readline 就正常
```

不依赖本仓的最小复现（只要求 Windows + Git Bash 5.3 + 任意 ConPTY 宿主）：

```bash
# 在 Windows Terminal（同样是 ConPTY）里粘贴一行 >96 字节的 echo，同样卡住
python -c "print('echo ' + 'x'*129 + ' MARK')"   # 粘贴这一行到 Git Bash 5.3
```

## 4. 本仓怎么应对（不是放宽断言）

`internal/terminal` 的长输入用例（T10/T13/T14）测的是**宿主 Write 路径把
长输入逐字节完整送进伪终端**，不是 MSYS 的读速或 readline 的行编辑行为。
所以用例按「读侧不能是瓶颈」选型，断言一条没动：

| 用例 | 之前的读侧 | 现在 | 为什么 |
|---|---|---|---|
| `TestPTYLongInputPacedWrite`（50/60/256/512 字节长行） | 交互式 bash（readline） | `bash --noediting` | readline 8.3 对成块输入整段挂住；关掉它仍走同一条 ConPTY + Write 分片 |
| `TestPTYLongPasteExactBytes/n=4096` | `head -c 4096`（MSYS） | 不变（~5s 可接受） | MSYS 读侧在 4KB 量级还能承受 |
| `TestPTYLongPasteExactBytes/n=65536` | `head -c 65536`（MSYS，40 分钟） | 原生 Go 字节汇（与 T14 同一机制） | 测的是 Write 路径，不能拿 MSYS 读速当产品指标 |
| `TestT14ColdNativeSink`（65536 原生汇） | 原生 | 不变 | 一直是原生读侧，本来就是对的 |

断言强度不变：字节数、`bytes.Equal` 逐字节、后续短命令仍可执行、超时窗口都没放宽。

## 5. 什么版本下会恢复

按第 2 节的判据，两条上游任一修好就恢复（届时把 §4 的读侧改回 MSYS/readline 即可）：

1. **MSYS/Cygwin console 读侧吞吐**：`cat`/`head` 在 ConPTY 原始模式下
   恢复到 ≥ 1 MB/s（Git 2.55 之前是正常的；具体哪版回归未逐版 bisect）。
2. **readline 在 win32-input-mode 下消费成块输入**：交互式 bash 能正常
   回显/执行 ≥ 128 字节的成块写入（bash 5.2 / readline 8.2 线正常）。

验证命令（恢复后应全绿，且 §3 复现的 `[readline]` 一行变成 `executed=true`）：

```bash
go test ./internal/terminal -count=1 -run 'TestPTYLongInputPacedWrite|TestPTYLongPasteExactBytes|TestT14ColdNativeSink' -v
go run ./tmp/GO6-PARAMS/pty-bash53/probe
```

## 6. 对产品的影响面（如实记录）

- **`terminal.open` 的交互式 PTY 会话**：用户把 ≥96 字节一次性粘贴进
  Git Bash 5.3 的 PTY 时，**粘贴内容会延迟到下一次输入才被处理**（不是丢失）。
  手工逐键输入（每次 1 字节）不受影响。
- **`modules/exec/bash` 的管道式 `bash` 工具**：不走 PTY（`--noprofile --norc -s`
  + 三条管道），**不受影响**；`go test ./modules/exec/bash` 全绿。
- **原生读侧（非 MSYS）**：不受影响（T14：65536 字节 0.23s）。
- 这是上游行为，本仓无法在传输层修复（已实测：改管道缓冲、改分片、bracketed
  paste、`\r` 换行、`TERM`/`INPUTRC` 都不解决；唯一有效的是关掉 readline）。
