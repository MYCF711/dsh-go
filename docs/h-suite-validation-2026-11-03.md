# W11 · 宿主隔离验证（4f7c885 两态）

- 日期：2026-11-03（执行跨到 2026-11-04 00:0x）
- 原 owner `9dff3286` 在收尾前失败；由 Lead 在它留下的两个隔离 worktree 中接管执行。本文件只记录实际命令与输出，不继承原 owner 的口头结论。
- 基线：宿主 **`4f7c885`**（committed）。
- 两态：
  - **A = `4f7c885` committed**，`wssession.go` 无 diff（sha256 `e3b0c442…`，空 diff）。
  - **B = `4f7c885` + `internal/transport/wssession.go` 工作树补丁**，diff sha256 `44fb76db…`。
- 隔离路径：`C:/Users/Administrator/AppData/Local/Temp/w11-2026-11-03/wt-{A,B}`；未在共享树移动 modules、未运行 `scripts/regress.sh`/`scripts/check.sh`。
- **当前宿主 HEAD 已前进到 `83eaa92`（M296 skill 多根）**，W11 的两态结论不覆盖它；该版本另做了当前全仓 test/build/vet，见 §3。

## 1. 两态结果

| 步骤 | A: committed | B: +wssession patch | 证据 |
|---|---:|---:|---|
| `python scripts/bin-check.py` | **exit 0**，2/2 | **exit 0**，2/2 | `logs/lead-A.log` / `lead-B.log` |
| `go build ./...` | **exit 0** | **exit 0** | 同上 |
| `go vet ./...` | **exit 0** | **exit 0** | 同上 |
| `go test ./...` | **exit 0** | **exit 0** | 同上 |
| `python scripts/h-suite.py --fast` | **exit 0**，总体全部通过 | **exit 0**，总体全部通过 | 同上 |
| `bash scripts/race.sh` | **exit 0**，合计 OK=1 FAIL=0 | **exit 0**，合计 OK=1 FAIL=0 | 同上 |

### h-suite fast 明细（两态相同）

| 探针 | 结果 |
|---|---|
| bin-check | OK 2/2 |
| help-guard | OK 15/15 |
| contract-evidence | OK 2/2 |
| flag-wired | OK 2/2 |
| method-wired | OK 4/4 |
| guard-selftest | OK 5/5 |
| h11-stdout | OK 4/4 |
| h10-chain | OK 6/6 |
| h14-isolation / h13 四条 / ui-e2e | **按 --fast 设计跳过（需真实模型）** |
| race.sh 探针 | 默认关闭，另行直接执行 |

非模型探针合计 **40/40，总体：全部通过**。跳过的 6 条不得记作通过。

## 2. 这些结果能证明什么

- committed 与 `+wssession` 两态都能构建、vet、全仓单测通过，且 `-race` 全仓零失败。
- 非模型守卫（二进制体检、help/契约/flag/method、守卫反证、H11、H10 链）在两态都通过。
- **不能证明**：H14 真实隔离、H13 四条真审批、ui-e2e（本次按 fast 跳过）；也**不能证明** wssession “replay→Subscribe 非原子 / pendingReplay 顺序”的语义正确——race 只查数据竞争，不查丢帧/重帧。W1 的定向测试和真机断言仍必须补。

## 3. 当前 HEAD 附加门禁

宿主 **`83eaa92`**（在 W11 基线之后只含 skill 多根改动）：

```bash
CGO_ENABLED=0 go test ./...   # exit 0
CGO_ENABLED=0 go build ./...  # exit 0
CGO_ENABLED=0 go vet ./...    # exit 0
```

原始日志：`/tmp/current-host-test.log`、`/tmp/current-host-build.log`、`/tmp/current-host-vet.log`。当前 HEAD 未重跑 race 与 h-suite，不能把 4f7c885 的两态结果外推到 83eaa92。

## 4. 未验证

- 当前 `83eaa92` 的 race 与 h-suite fast。
- h14 真实双会话、h13 四条真实审批、ui-e2e。
- wssession 重放不丢/不重、replay→Subscribe 原子性、pendingReplay 与 journal replay 的顺序。
- 原 owner 未完成原因未查明；本文件不猜测。
