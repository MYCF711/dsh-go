# RESTART-RECOVERY：真实跨进程宿主重启验证

> 2026-11-05 实跑结论：**OK=79 FAIL=5 SKIP=0，脚本 exit=1，未全绿**。
> 5 条 FAIL = 重启后旧 history 缺原始 text/reasoning/tool/approval 的 4 条行为断言，加上 phase B `go test` 因此非零的 1 条驱动断言；不是 5 个独立缺陷。
>
> 证据目录：[`tmp/restart-recovery-20261105-010633/`](../tmp/restart-recovery-20261105-010633/)；完整输出：[`driver.log`](../tmp/restart-recovery-20261105-010633/driver.log)；机器汇总：[`summary.txt`](../tmp/restart-recovery-20261105-010633/summary.txt)；探针：[`scripts/restart-recovery.py`](../scripts/restart-recovery.py)。

## 1. 结论先行

同一临时 home、同一新构建 `dshd.exe`，从宿主进程 A 切到宿主进程 B 后：

| 必验项 | 实测 | 证据 |
|---|---|---|
| A 完成 create/prompt/text/tool/approval | PASS；A 阶段 40/0/0，正文暗号、真实写文件、1 次 `allow_once`、480 bytes reasoning | [`phase-A.log`](../tmp/restart-recovery-20261105-010633/phase-A.log) |
| 旧 cookie/token 按契约处理 | PASS；两者在 B 上都明确 HTTP 401 `unauthorized`，随后 B 新 ready token 以 303 重新 `Connect`；没有猜测或静默复用 | [`phase-B.log`](../tmp/restart-recovery-20261105-010633/phase-B.log) |
| B 重新 list/load/subscribe | PASS；list 找回 alpha/beta，旧水位订阅收到无 seq `session/reset`，随后重新 load，再以新 epoch 订阅 | [`phase-b-old-watermark-events.json`](../tmp/restart-recovery-20261105-010633/phase-b-old-watermark-events.json) |
| 旧 history 水位 | PASS；HTTP 409、typed `APIError{Code:"stale_epoch", Retryable:true}` | [`phase-B.log`](../tmp/restart-recovery-20261105-010633/phase-B.log) |
| 新 journalEpoch | PASS；alpha `1762790511988865908 → 4719603236447376494`，beta `1762790511988865909 → 4719603236447376495` | [`phase-b-final-summary-state.json`](../tmp/restart-recovery-20261105-010633/phase-b-final-summary-state.json) |
| v4 首屏内容恢复 | PASS；alpha 重启前后均为 6 messages（2 user / 3 assistant / 1 tool），beta 仍有自己的 user/assistant | [`phase-b-alpha-reload.json`](../tmp/restart-recovery-20261105-010633/phase-b-alpha-reload.json) |
| **history 保留重启前原始 text/reasoning/tool/approval** | **FAIL**；A 的 16 帧四类齐全，B 在新 epoch、新 prompt 前查询为 `frames=[] / lastSeq=0`，四类全部缺失 | [`minimal-reproduction.md`](../tmp/restart-recovery-20261105-010633/minimal-reproduction.md) |
| 新轮 prompt | PASS；B 上 alpha 新正文经 UI chunks 到达，`turn/done`/`turn/end` 完成，新 history 为 6 帧 | [`phase-b-alpha-history-after-new-turn.json`](../tmp/restart-recovery-20261105-010633/phase-b-alpha-history-after-new-turn.json) |
| A/B 不串 | PASS；重启前后双向暗号检查均过，beta 不含 alpha 的正文/工具/重启后暗号 | [`phase-B.log`](../tmp/restart-recovery-20261105-010633/phase-B.log) |
| v4 append-only 不重写 | PASS；两个文件的旧字节前缀逐字节不变；alpha 追加，beta 整文件不变 | [`v4-prefix-check.json`](../tmp/restart-recovery-20261105-010633/v4-prefix-check.json) |

**失败边界很清楚**：`session.load` 的 v4 投影仍在，失败的是任务书额外要求的“跨进程后 `session.history` 仍保留旧原始 Transport 帧”。现有实现启动时创建新的内存 [`Journal`](../internal/transport/journal.go#L20-L37)，`session.load` 只补当前 journal 的 epoch/lastSeq（[`server.go`](../internal/transport/server.go#L497-L503)），没有从 v4 回放 Transport journal。是否持久化/重建原始 journal 属于产品与契约 owner 的后续判定，本任务未改产品代码或契约。

## 2. 基线、二进制与真实模型

| 项 | 本次真值 |
|---|---|
| `dshd_rev` | `af4a49a2817592d7affb99bb3a16c7e03214ba1c`（base `af4a49a` + 当前共享工作树，含 H14） |
| `ui_rev` | `3925e84f1524dd101e020d74b1d5bb422d25cf4d`（base `3925e84` + 当前共享工作树） |
| UI 副本 | `C:/Users/Administrator/AppData/Local/Temp/dsh-ui-restart-recovery-isolated` |
| dshd 二进制 | [`tmp/restart-recovery-20261105-010633/dshd.exe`](../tmp/restart-recovery-20261105-010633/dshd.exe) |
| SHA-256 | `ce87bd8a7760f37be7e74c4054533f959a23716f278e73583e9a2fd578d5dddc` |
| 真实模型 | 本机 magpie `http://127.0.0.1:3425/v1` 的 `group/deepseek-v4.1-flash1017`，目录预检命中，0 SKIP |
| 宿主参数 | `--policy ask --reasoning max --max-steps 4 --addr 127.0.0.1:0` |

第 0 步初始 [`bin-check`](../tmp/restart-recovery-20261105-010633/initial-bin-check.log) 为 `OK=0 FAIL=1`，原因是本副本尚未构建目标二进制；没有拿旧 exe 继续跑。随后实际执行：

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
go build -o tmp/restart-recovery-20261105-010633/dshd.exe ./cmd/dshd
python scripts/bin-check.py tmp/restart-recovery-20261105-010633/dshd.exe
```

构建 exit=0；门禁 exit=0，`OK=1 FAIL=0`，见 [`build-exit.txt`](../tmp/restart-recovery-20261105-010633/build-exit.txt)、[`bin-check.log`](../tmp/restart-recovery-20261105-010633/bin-check.log)。Go overlay 探针先单独编译，exit=0，见 [`go-compile.log`](../tmp/restart-recovery-20261105-010633/go-compile.log)。版本与路径另存 [`revisions.json`](../tmp/restart-recovery-20261105-010633/revisions.json)。

## 3. 实际命令与跨进程形状

复跑命令（本次另传了固定证据目录）：

```bash
python scripts/restart-recovery.py \
  --binary tmp/restart-recovery-20261105-010633/dshd.exe \
  --ui C:/Users/Administrator/AppData/Local/Temp/dsh-ui-restart-recovery-isolated \
  --evidence-dir tmp/restart-recovery-20261105-010633 --timeout 600
```

A/B 宿主 argv 与两个 Go 阶段 argv 全部原样记录在 [`commands.json`](../tmp/restart-recovery-20261105-010633/commands.json)。Go 调用形如：

```text
go test -overlay=<证据目录>/overlay.json ./internal/apiclient \
  -run ^TestRestartRecovery$ -count=1 -v -timeout 10m
```

overlay 只把证据目录里的 `restart_recovery_test.go` 映射进真实 UI `internal/apiclient`；所有 HTTP/WS 行为都经 UI 自己的 `Client / Mux / Event.Chunks / ParseApprovalRequest / Approve`。UI 开始/结束 `git status --porcelain` **逐字节相同**，见 [`ui-status-at-start.txt`](../tmp/restart-recovery-20261105-010633/ui-status-at-start.txt) 与 [`ui-status-at-end.txt`](../tmp/restart-recovery-20261105-010633/ui-status-at-end.txt)。

| 进程 | PID / URL | 终止与退出真值 |
|---|---|---|
| A | PID 28936，`http://127.0.0.1:50654` | 映像路径与本次 exe 逐字核对后，只对该 PID 树执行 `taskkill /PID 28936 /T /F`；taskkill exit=0；强制终止后的 dshd exit=1 |
| B | PID 22640，`http://127.0.0.1:53720` | 同样核对归属，只清理 PID 22640 树；taskkill exit=0；dshd exit=1 |
| UI Go A | PID 8980 | `RR_SUMMARY phase=A OK=40 FAIL=0 SKIP=0`，go test exit=0 |
| UI Go B | PID 40744 | `RR_SUMMARY phase=B OK=29 FAIL=4 SKIP=0`，go test exit=1 |

进程证据见 [`processes.json`](../tmp/restart-recovery-20261105-010633/processes.json)。没有按镜像名批杀，没有触碰其他 DSH/magpie 进程，没有启动 GUI，没有 mockd/ACP/截图替代。

## 4. 水位、恢复顺序与原始帧

重启前记录在 [`restart-state.json`](../tmp/restart-recovery-20261105-010633/restart-state.json)：alpha `epoch=1762790511988865908,lastSeq=16`，beta `epoch=1762790511988865909,lastSeq=6`。

B 阶段严格按以下顺序执行：

1. 旧 cookie 调真实 `List` → HTTP 401；旧 ready token 调真实 `Connect` → HTTP 401。
2. 新 ready token 调真实 `Connect` → 303 + cookie；`List` 找回两会话。
3. 带旧 `{epoch,lastSeq}` 调 `History` → 409 `stale_epoch`。
4. 带旧 `{epoch,lastSeq}` 经真实 `Mux.Subscribe` → 一个无 seq `session/reset{journalEpoch:4719603236447376494,lastSeq:0}`。
5. **之后**才重新 `Load`；reset 与 load 的新 epoch/lastSeq 完全一致，且新 epoch 与旧值不同。
6. 用新 epoch 查询 history：旧原始帧缺失，四条判据失败。
7. 用新 epoch/水位订阅，发新 prompt；正文与终局到达，新 history lastSeq=6，证明失败不是 B 的 history endpoint 整体失活。

A 的原始 history 有 16 帧：`system/message=2, turn/start=2, user/message=2, assistant/message=3, turn/end=2, turn/done=2, approval/request=1, tool/call=1, tool/result=1`。reasoning 位于 assistant content 块。B 在新 prompt 前的原始 history 是 0 帧；新 prompt 后只有新 turn 的 6 帧。三份原始响应分别是：

- [`phase-a-alpha-history.json`](../tmp/restart-recovery-20261105-010633/phase-a-alpha-history.json)
- [`phase-b-alpha-history-before-new-turn.json`](../tmp/restart-recovery-20261105-010633/phase-b-alpha-history-before-new-turn.json)
- [`phase-b-alpha-history-after-new-turn.json`](../tmp/restart-recovery-20261105-010633/phase-b-alpha-history-after-new-turn.json)

## 5. v4 与临时 home 形状

[`home-shape.json`](../tmp/restart-recovery-20261105-010633/home-shape.json) 记录最终 home 共 3 个文件：

| 相对路径 | 最终大小 |
|---|---:|
| `logs/dshd-2026-11-05.log` | 2,608 bytes |
| `sessions/.../413cdcea-.../session.v4.jsonl.zstd`（beta） | 6,741 bytes |
| `sessions/.../e74af76f-.../session.v4.jsonl.zstd`（alpha） | 20,946 bytes |

v4 前后 manifest：[`v4-before.json`](../tmp/restart-recovery-20261105-010633/v4-before.json)、[`v4-after.json`](../tmp/restart-recovery-20261105-010633/v4-after.json)。

- beta：6,741 → 6,741 bytes，SHA-256 始终 `dabf2bbaf51345e628df74172321f0783808251e9e5ae4c03e466d65b8dd40c0`。
- alpha：14,540 → 20,946 bytes；最终文件的前 14,540 bytes 与重启前文件逐字节相同，随后才追加新 turn。
- 两个旧文件都仍存在，没有第三份 v4，没有重写旧前缀。

## 6. 汇总、保密与最小复现

| 出口 | 结果 |
|---|---|
| phase A | `OK=40 FAIL=0 SKIP=0`，exit=0 |
| phase B | `OK=29 FAIL=4 SKIP=0`，exit=1 |
| 驱动附加检查 | bin-check、进程归属、v4 前缀、密钥清理等计入最终汇总 |
| 最终 | **`OK=79 FAIL=5 SKIP=0`**，exit=1 |

收尾另跑证据一致性复核：机器汇总、三份 history、v4 前缀、密钥清理、UI 状态、全部 Markdown 链接、JSON 解析与宿主写入边界共 `VERIFY_SUMMARY OK=40 FAIL=0`，见 [`final-verification.log`](../tmp/restart-recovery-20261105-010633/final-verification.log)。这只是证据完整性检查，**不抵消上述 4 条产品行为 FAIL**。

ready token 只经子进程环境传递；cookie 只在两个 Go 阶段间短暂写入固定证据目录下的 0600 临时文件，收尾核对父目录后删除，`transient-cookie-artifact-removed` 为 OK。日志中的 token 均写 `<redacted>`，没有 cookie 值证据。

最小失败case及机器可读输入/响应已单独保存：[`minimal-reproduction.md`](../tmp/restart-recovery-20261105-010633/minimal-reproduction.md)、[`minimal-reproduction.json`](../tmp/restart-recovery-20261105-010633/minimal-reproduction.json)。第一次驱动试跑因探针自身把 `subprocess.DEVNULL` 误写为 `DNULL`，在任何宿主启动前退出；原文留在 [`attempt-01-driver.log`](../tmp/restart-recovery-20261105-010633/attempt-01-driver.log)，没有混入产品结论。

## 7. 未验项与不得外推

- 未验证**挂起中的审批跨强杀宿主**；本次审批在 A 内真实允许并结算后，才重启，验的是原始 approval 帧能否跨进程保留。
- 未验证 `allow_always` 跨进程、审批超时跨进程、容量淘汰后的恢复、>500 帧分批、MSGID 精确 load↔WS 去重。
- 未启动可见 GUI，未验 Viva 像素、键盘、窗口重连；本证据层级是“真实 dshd + 真实 UI apiclient”，不外推为桌面产品完成。
- 未做完整工作日、长跑 soak、`-race`、性能或其他模型；只有本次指定真实模型，0 SKIP。
- 旧 history 四类缺失是**已实测失败**，不是未验项；除这一项外，本表其余通过结论仅限本次 A/B 两个短会话与一次跨进程重启。
