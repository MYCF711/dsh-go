# JOURNAL-RESTART：Transport 原始帧跨进程恢复

> 2026-11-05 最终实测：**`OK=84 FAIL=0 SKIP=0`，驱动 exit=0**。
> 修前同一探针为 `OK=79 FAIL=5 SKIP=0`；重启后旧 history 的 text/reasoning/tool/approval 四条 FAIL 本次全部转绿。
> 最终证据目录：[`tmp/restart-recovery-journal-final/`](../tmp/restart-recovery-journal-final/)；完整原样输出：[`driver-console.log`](../tmp/restart-recovery-journal-final/driver-console.log)。

## 1. 根因与修复边界

旧实现只在内存中创建每会话 Transport journal。`session.load` 能从 v4 恢复首屏投影，但新进程中的 `History(sinceSeq=0)` 没有原始 Transport 帧可读，因此返回 `frames=[] / lastSeq=0`。

v4 不能完整替代 Transport journal：Transport `seq`、envelope `time`、`turn/done` 和 broker approval 不全是 v4 事实。本修复不从 v4 猜测或合成这些信息，而是新增**显式、每会话、已提交前缀 append-only** 的 `transport-journal.v1.jsonl`，与对应 `session.v4.jsonl.zstd` 放在同一会话目录。

本 patch 只涉及：

- `internal/transport/journal.go`：持久化接口、原子 append、只读 Restore；
- `internal/transport/journal_persistence_test.go`：原始帧、新 epoch、A/B、失败不占 seq；
- `cmd/dshd/serve.go`：仅接入启动恢复；
- `cmd/dshd/serve_journal.go`：生产发帧统一走持久化 append，失败不发布；
- `cmd/dshd/serve_journal_store.go`：JSONL 存储、启动扫描、torn-tail 写路径修复；
- `cmd/dshd/serve_journal_store_test.go`：跨进程、reset、只读、损坏与 torn-tail 测试；
- 本文档。

没有在本 patch 中修改 `ws.go`、`serve_approval.go`、UI、canonical snapshot/golden 或探针断言；没有 commit/push，没有启动 GUI。

## 2. 写入、恢复与读路径

### 写入

`transport.Journals.Append` 在同一会话的 journal 锁内依次完成：

1. 以下一个 seq 组装原始 `ServerFrame{seq,type,data,time}`；
2. 同步追加并 `Sync` JSONL 记录；
3. 成功后才推进内存 seq/window；
4. `emitFrameResult` 随后才把同一帧发布到 WS。

因此持久化失败不会消耗 seq、不会留下内存半帧，也不会向 WS 发布重启后必然消失的假成功帧。错误由 `emitFrame` 打到宿主 stderr；`emitFrameResult` 也可把错误显式返回调用方。

JSONL 的 `data` 使用 `[]byte` 的标准 base64 编码，而不是让 `json.RawMessage` 重新序列化内部 JSON，从而逐字保留原始 data 字节。记录含 `version/kind/sessionId/seq/type/data/time`；reset 使用独立 append 记录，旧帧不会在下个进程复活。

### 启动恢复

`cmdServe` 在 HTTP 服务开始前调用 `restoreServeJournals`：

- 只扫描已有 `transport-journal.v1.jsonl`；没有 journal 的旧 v4 会话保持为空 Transport 历史，不伪造旧帧；
- 通过同目录 v4 header 取得真实 session id，逐会话恢复，拒绝重复 id、完整坏行、断号和伪造 time/type/data；
- `Journal.Restore` 恢复原 seq/type/data/time；epoch 仍由本进程 `newEpochFunc` 新分配；
- 全部恢复完成后才接上 persistence。

所以旧 epoch 的 history 仍返回 `stale_epoch`，旧 epoch 的 sub 仍收到无 seq `session/reset`；重新 load 后，新 epoch 与原 seq 水位同时成立。

### 读路径

`session.load`、`session.history` 和 subscribe 只读内存 journal，不调用恢复或修复写盘。启动 Restore 本身也不写文件。测试对恢复与 `Window` 前后的 journal 文件逐字节比较，保持一致。

### torn tail 与完整损坏

- 最后一个未换行终结的片段视为未成功 append 的 torn tail；启动只读跳过，不在启动时截断。
- 下一次 append 先核对文件长度仍等于恢复时的观察值，避免误截外部新增内容；随后用独立 Windows 读写句柄只截去未提交尾巴并同步，再以 `O_APPEND` 写入新记录。
- Write/Sync/Close 失败会记录未提交区间，下一次写入先回到已提交前缀，避免把下一条 JSON 粘到半行后。
- 已提交前缀不重写；换行终结的非法 JSON、未知 kind、错误 session/version、断号均 fail loud。

## 3. 真红回归与修复后输出

新增 `TestServeJournalStoreTornTailRepairedOnlyOnNextAppend` 先证明旧实现有真实失败：启动能读 torn tail 前的完整帧，但随后 append 会把新 JSON 粘在未终结片段后，第三个进程无法恢复。

修前原样输出（exit=1，完整见 [`torn-tail-before.log`](../tmp/restart-recovery-journal-final/validation/torn-tail-before.log)）：

```text
=== RUN   TestServeJournalStoreTornTailRepairedOnlyOnNextAppend
    serve_journal_store_test.go:231: torn tail 后追加的记录必须能被下一进程恢复：C:\Users\ADMINI~1\AppData\Local\Temp\TestServeJournalStoreTornTailRepairedOnlyOnNextAppend1254120040\001\sessions\--C-Users-ADMINI~007E1-AppData-Local-Temp-TestServeJournalStoreTornTailRepairedOnlyOnNextAppend1254120040-002--\session-tail-append\transport-journal.v1.jsonl 第 2 行不是已提交的合法记录：invalid character '{' after object key:value pair
--- FAIL: TestServeJournalStoreTornTailRepairedOnlyOnNextAppend (0.04s)
FAIL
FAIL	dshgo/cmd/dshd	0.095s
FAIL
```

修复后同一组 store 测试原样输出（exit=0，完整见 [`journal-store-after3.log`](../tmp/restart-recovery-journal-final/validation/journal-store-after3.log)）：

```text
=== RUN   TestServeJournalStoreCrossProcessRawRecovery
--- PASS: TestServeJournalStoreCrossProcessRawRecovery (0.18s)
=== RUN   TestServeJournalStoreResetAndReadOnlyRecovery
--- PASS: TestServeJournalStoreResetAndReadOnlyRecovery (0.08s)
=== RUN   TestServeJournalStoreTornTailAndCorruption
--- PASS: TestServeJournalStoreTornTailAndCorruption (0.07s)
=== RUN   TestServeJournalStoreTornTailRepairedOnlyOnNextAppend
--- PASS: TestServeJournalStoreTornTailRepairedOnlyOnNextAppend (0.06s)
PASS
ok  	dshgo/cmd/dshd	0.489s
```

第一次只加 `O_RDWR` 仍在 Windows append 句柄上得到 `Truncate: Access is denied`；没有把它算作通过。最终改为独立修复句柄后才转绿，中间原样输出保留在 `validation/journal-store-after.log` 与 `validation/journal-store-after2.log`。

## 4. 最终真实跨进程验证

实际命令：

```bash
export PATH="/c/Program Files/Go/bin:$PATH"
go build -o tmp/restart-recovery-journal-final-bin/dshd.exe ./cmd/dshd
python scripts/restart-recovery.py \
  --binary tmp/restart-recovery-journal-final-bin/dshd.exe \
  --ui C:/Users/Administrator/AppData/Local/Temp/dsh-ui-restart-recovery-isolated \
  --evidence-dir tmp/restart-recovery-journal-final
```

二进制经 `bin-check`，真实 UI `internal/apiclient` 经 Go overlay 执行探针；未修改 UI 工作树，未使用 mockd/ACP/GUI。版本、二进制 SHA-256 和 UI 路径见 [`revisions.json`](../tmp/restart-recovery-journal-final/revisions.json)，进程归属与退出见 [`processes.json`](../tmp/restart-recovery-journal-final/processes.json)。

关键最终原样输出：

```text
RR_OK phase=B alpha-new-journal-epoch old=6309589881769124244 new=5558789810290245389
RR_OK phase=B beta-new-journal-epoch old=6309589881769124245 new=5558789810290245390
RR_OK phase=B reset-matches-reloaded-baseline resetEpoch=5558789810290245389 loadEpoch=5558789810290245389 resetLastSeq=16 loadLastSeq=16
RR_OK phase=B post-restart-history-original-assistant-text frames=16
RR_OK phase=B post-restart-history-original-reasoning
RR_OK phase=B post-restart-history-original-tool-call-result
RR_OK phase=B post-restart-history-original-approval
RR_OK phase=B post-reset-no-stale-replay baseline=16
RR_OK phase=B post-restart-A-B-no-cross-talk
RR_SUMMARY phase=B OK=33 FAIL=0 SKIP=0
DRIVER_OK v4-append-only-old-prefix-unchanged [{'path': 'sessions\\--C-Users-Administrator-AppData-Local-Temp-DSH-GO-journal-restart--\\7706ac62-a915-1261-3e69-50ab4329158b\\session.v4.jsonl.zstd', 'beforeBytes': 14401, 'afterBytes': 20808, 'oldPrefixUnchanged': True}, {'path': 'sessions\\--C-Users-Administrator-AppData-Local-Temp-DSH-GO-journal-restart--\\96d3d37d-d168-044f-78e1-c2226c89dab8\\session.v4.jsonl.zstd', 'beforeBytes': 6646, 'afterBytes': 6646, 'oldPrefixUnchanged': True}]
DRIVER_OK v4-only-alpha-appended-beta-not-rewritten {'alpha': 'sessions\\--C-Users-Administrator-AppData-Local-Temp-DSH-GO-journal-restart--\\7706ac62-a915-1261-3e69-50ab4329158b\\session.v4.jsonl.zstd', 'alphaAppended': True, 'beta': 'sessions\\--C-Users-Administrator-AppData-Local-Temp-DSH-GO-journal-restart--\\96d3d37d-d168-044f-78e1-c2226c89dab8\\session.v4.jsonl.zstd', 'betaUnchanged': True}
OK=84 FAIL=0 SKIP=0
```

完整 A/B 输出分别在 [`phase-A.log`](../tmp/restart-recovery-journal-final/phase-A.log) 与 [`phase-B.log`](../tmp/restart-recovery-journal-final/phase-B.log)。重启前、重启后新 prompt 前、最终三份原始 history 均保存在证据目录；reset/stale epoch、load/v4、A/B 隔离、后续新帧续号与 v4 旧前缀不变都由同一轮验证覆盖。

修前对照仍保留在 [`tmp/restart-recovery-journal-before/`](../tmp/restart-recovery-journal-before/)：`OK=79 FAIL=5 SKIP=0`，其中 `frames=0`，四条原始 history 判据失败。

## 5. 测试、race、vet、build

全部在当前隔离副本实际执行，日志与退出码集中在 [`validation/`](../tmp/restart-recovery-journal-final/validation/)。

| 验证 | 结果 | 原样输出 |
|---|---|---|
| `go test ./internal/transport ./cmd/dshd -count=1` | exit=0 | [`targeted-test.log`](../tmp/restart-recovery-journal-final/validation/targeted-test.log) |
| MinGW + `CGO_ENABLED=1 go test -race ./internal/transport ./cmd/dshd -run 'TestJournalPersistence|TestJournalRestore|TestServeJournalStore|TestConcurrentJournalAndHubInvariants' -count=1` | exit=0；两包 ok；0 DATA RACE | [`targeted-race-cgo.log`](../tmp/restart-recovery-journal-final/validation/targeted-race-cgo.log) |
| `go test ./... -count=1` | exit=0；全仓所有包通过 | [`full-test.log`](../tmp/restart-recovery-journal-final/validation/full-test.log) |
| `go vet ./...` | exit=0；`VET_EXIT=0` | [`full-vet.log`](../tmp/restart-recovery-journal-final/validation/full-vet.log) |
| `go build ./...` | exit=0；`BUILD_EXIT=0` | [`full-build.log`](../tmp/restart-recovery-journal-final/validation/full-build.log) |
| 最终 dshd 构建 | `FINAL_BINARY_BUILD_EXIT=0` | [`final-binary-build.log`](../tmp/restart-recovery-journal-final/validation/final-binary-build.log) |

第一次 race 命令在未启用 CGO 的环境得到 `go: -race requires cgo; enable cgo by setting CGO_ENABLED=1`，exit=2；原文保留为 `targeted-race.log`。随后使用本机 `/c/toolchains/mingw64`、GCC 16.2.0 和 `CGO_ENABLED=1` 真跑，未把环境失败算作验证通过。

第一次修后探针误把已含构建产物的目录作为 `--evidence-dir`，驱动因 `home` 已存在而真实报 `OK=2 FAIL=1`；该日志保留在 `tmp/restart-recovery-journal-after/driver-console.log`。随后使用全新证据目录的两轮正式复跑均为 `OK=84 FAIL=0 SKIP=0`，最终轮即上表结果。

## 6. 未外推项

- 本证据证明真实 dshd + 真实 UI apiclient 的同 home 跨进程恢复，不外推可见 GUI 像素/键盘或完整工作日。
- 本次审批在重启前已真实允许并结算；未验证 pending broker 状态在宿主强杀后继续等待应答，恢复的是原始 approval Transport 帧。
- 未验证容量淘汰、超大批次、多实例同时写同一 home 或掉电文件系统语义；外部改动检测会拒绝猜测，不作为多写者协调机制。
