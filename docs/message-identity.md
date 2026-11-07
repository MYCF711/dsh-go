# MSGID-IDENTITY：真实 v4 内容身份与 load↔WS 精确去重

> 2026-11-05 · owner MSGID-IDENTITY。本文是实现与本次实跑证据，不用历史 ✅ 代替当前验证。

## 1. 基线、共享状态与边界

- 宿主 base：`af4a49a2817592d7affb99bb3a16c7e03214ba1c`；UI base：`3925e84f1524dd101e020d74b1d5bb422d25cf4d`。
- 开工时是上述 base + 当前共享工作树（含 W34/W38/W43/W49/W50/W51/W52/W54/W55）。没有 reset/checkout 覆盖共享修改，没有 commit/push，没有启动 GUI。
- 修改范围只有宿主契约、assistant/user 投影、共享 golden/测试和本文；UI 只改 `internal/apiclient`、`cmd/mockd`、`testdata`。未改 `ws.go`、`serve_approval.go`、Transport journal 持久格式、v4 格式、uikernel/dssettings/其他插件。
- UI 既有 `docs/message-identity.md` 是 W33 历史记录，本轮权限不允许修改；当前契约以宿主 `docs/transport-contract.md` 与本文为准。

## 2. 真值来源：已有持久 Event.Seq，不增加伪 ID

`codecs/v4/read.go` 的 `Event` 已有 `{type,seq,time,data}` 信封；`internal/session/log.go` 的 `Append` 持久分配 seq，第一条是合法的 `seq=0`；`internal/session/history.go` 的 `OpenSessionAt` 以持久事件数恢复下一序号。真实最终探针也证明旧 v4 前缀在另一进程续写后逐字不变。

因此不需要新增身份来源，直接投影既有 `Event.Seq`：

| 线上字段 | 作用域与来源 | 缺失语义 |
|---|---|---|
| `data.v4Seq` / `messages[].v4Seq` | 会话内 v4-backed `user/message`、`assistant/message`；值是持久 `Event.Seq`，0 合法 | 旧 Transport journal、非 v4 或畸形来源可以没有；不许拿 Transport 信封 seq 补 |
| `data.messageId` / `messages[].messageId` | 只透传持久 data 已有 `messageId/id/message.messageId/message.id`；当前真实 user 使用顶层 `id` | 当前 assistant 构造器没有持久 message id，所以省略；不生成时间/hash/进程 ID |
| Transport 信封 `seq` | 每会话 Transport journal 水位、断号、history 补洞 | 永远不参与内容 upsert |

W49 的持久 journal 仍是 `{seq,type,data,time}`。新帧只在 data 增加字段；恢复旧 journal 时不回写、不补造身份。旧 v4 文件即使 data 原本没有这些键，load 也能从其**信封**取得真实 `v4Seq`，旧文件本身不重写。

### scoped fallback 的明确边界

- 有 `v4Seq`：UI 按 `(role,v4Seq)` 精确 upsert；有真实 `messageId` 时可辅助对账。
- 同文两条若 `v4Seq` 不同，就是两条不同消息，全部保留。
- 同一 Stream、另一 Stream、load 后乱序/迟到 WS 重放，都不得复制同一身份。
- 缺 `v4Seq/messageId` 的旧帧只使用 Stream 实例 + Reset 代次 + role 的局部 fallback。它只防误覆盖，**不承诺 load↔WS 精确去重**。
- `turn/done/error.turnId`、`tool/call.callId`、`tool/result.message.toolCallId`、数值 `turn/step`、reasoning/tool/approval/turn-done 的既有身份不变。

## 3. 实现位置

### 宿主

- `cmd/dshd/message_identity.go`：读取真实持久 ID、复制 `Event.Seq`、只给 user/assistant 派生帧加字段。
- `cmd/dshd/serve_tools.go`：follow 与 drain 两个真实 v4 → Transport 入口调用同一投影。
- `cmd/dshd/transport_source.go`：`session.load` 的 user/assistant 投影携带同一身份；tool 不变。
- `internal/transport/api.go`：`Message` 尾部新增可选 `v4Seq/messageId`，兼容 `seq` 不变。
- `cmd/dshd/message_identity_test.go`：续写、seq=0、同文双消息、load/WS 同源、Transport/v4 序号域分离。

`DSHD_SERVE_MODEL_RUNNER=1` 调试 runner 在 persist 前直接发简化 text，不是 v4 follow/drain 真值路径；本任务没有给它编造 `v4Seq`，其缺字段分支仍走 scoped fallback。

### UI / 共享夹具

- `internal/apiclient/client.go`：load JSON 真正解码 `v4Seq`，保留 0 与缺失的区别。
- 既有 W33 `Stream/Merge` 强身份逻辑复用，不改 reasoning/tool/approval 归约。
- `cmd/mockd/contractgolden.go`：从共享帧生成 load 投影，供真实 `apiclient.Load/History` 网络重放对账；不再只返回空 messages 基线。
- B golden 有两条同文 user（v4Seq 71/73）与两条同文 assistant（72/74），并以 5→2→4→3→2→5 的顺序重放。

## 4. 先红：修改产品实现前的原样输出

以下都是旧实现可编译后的**行为断言失败**，不是缺函数造成的编译红。

### 宿主 load + follow/drain

```text
=== RUN   TestLoadCarriesPersistentV4MessageIdentityAcrossReopen
    message_identity_test.go:98: load 第 0 条缺 v4Seq（seq=0 也必须出现）：null err=unexpected end of JSON input
--- FAIL: TestLoadCarriesPersistentV4MessageIdentityAcrossReopen (0.10s)
=== RUN   TestFollowProjectsSameV4IdentityAsLoadWithoutInventingAssistantID
spawn pid=33964 argv=[run --home C:\Users\ADMINI~1\AppData\Local\Temp\TestFollowProjectsSameV4IdentityAsLoadWithoutInventingAssistantI1102948307\001 --session message-identity-follow --prompt 第二轮 --policy deny --max-steps 2 --model m]
    message_identity_test.go:150: WS user/message 缺 v4Seq：{"content":[{"type":"text","text":"这一轮子进程写的用户消息"}],"id":"7fe8dded-3a7d-4c72-80f8-3bc0aa09786d","role":"user","source":{"kind":"user"},"surfaceOp":"append"} err=<nil>
--- FAIL: TestFollowProjectsSameV4IdentityAsLoadWithoutInventingAssistantID (0.14s)
FAIL
FAIL	dshgo/cmd/dshd	0.308s
FAIL
[exit code: 1]
```

### 宿主快照

```text
=== RUN   TestSessionAPISnapshot
    api_snapshot_test.go:151: 接口契约变了（快照 testdata/api.snapshot.json 与代码不一致）。
--- FAIL: TestSessionAPISnapshot (0.00s)
=== RUN   TestGoldenMessageIdentityTruth
--- PASS: TestGoldenMessageIdentityTruth (0.00s)
FAIL
FAIL	dshgo/internal/transport	0.085s
FAIL
[exit code: 1]
```

### UI load 解码与真实 apiclient 重放

```text
=== RUN   TestLoadJSONDecodesPersistentMessageIdentity
    message_identity_test.go:259: load 第 0 条没有保留 v4Seq=0（0 与缺失必须区分）：{Seq:0 Role:user Text:零值身份 V4Seq:<nil> MessageID:user-zero TurnID: Turn:<nil> Step:<nil> CallID: identity: toolIdentity: toolScope: Tool: Args: Summary: Reasoning: View:<nil> Usage:<nil>}
--- FAIL: TestLoadJSONDecodesPersistentMessageIdentity (0.00s)
=== RUN   TestSameTextMessagesWithDifferentV4SeqBothSurviveLateReplay
--- PASS: TestSameTextMessagesWithDifferentV4SeqBothSurviveLateReplay (0.00s)
=== RUN   TestLegacyReplayWithoutIdentityUsesScopedFallbackNotExactDedup
--- PASS: TestLegacyReplayWithoutIdentityUsesScopedFallbackNotExactDedup (0.00s)
FAIL
FAIL	github.com/MYCF711/dsh-ui/internal/apiclient	0.048s
FAIL
[exit code: 1]
```

```text
=== RUN   TestContractGoldenThroughRealAPIClient
=== RUN   TestContractGoldenThroughRealAPIClient/load与乱序WS重放按v4Seq精确去重
    contractgolden_test.go:302: B 的 load 应有同文 user/assistant 各两条，实际 0：[]
--- FAIL: TestContractGoldenThroughRealAPIClient (0.02s)
    --- FAIL: TestContractGoldenThroughRealAPIClient/load与乱序WS重放按v4Seq精确去重 (0.00s)
FAIL
FAIL	github.com/MYCF711/dsh-ui/cmd/mockd	0.098s
FAIL
[exit code: 1]
```

## 5. 修后定向输出（原样）

### 宿主

```text
=== RUN   TestLoadCarriesPersistentV4MessageIdentityAcrossReopen
--- PASS: TestLoadCarriesPersistentV4MessageIdentityAcrossReopen (0.09s)
=== RUN   TestFollowProjectsSameV4IdentityAsLoadWithoutInventingAssistantID
spawn pid=11960 argv=[run --home C:\Users\ADMINI~1\AppData\Local\Temp\TestFollowProjectsSameV4IdentityAsLoadWithoutInventingAssistantI2176410575\001 --session message-identity-follow --prompt 第二轮 --policy deny --max-steps 2 --model m]
--- PASS: TestFollowProjectsSameV4IdentityAsLoadWithoutInventingAssistantID (0.15s)
PASS
ok  	dshgo/cmd/dshd	0.313s
```

```text
=== RUN   TestSessionAPISnapshot
--- PASS: TestSessionAPISnapshot (0.00s)
=== RUN   TestGoldenFramesMatchContract
--- PASS: TestGoldenFramesMatchContract (0.00s)
=== RUN   TestGoldenFramesReplayInOrder
--- PASS: TestGoldenFramesReplayInOrder (0.01s)
=== RUN   TestGoldenGapUsesHistoryNotReload
--- PASS: TestGoldenGapUsesHistoryNotReload (0.00s)
=== RUN   TestGoldenHTTPBareParamsAndErrors
transport: /api/session.history
transport: /api/session.history
--- PASS: TestGoldenHTTPBareParamsAndErrors (0.00s)
=== RUN   TestGoldenMessageIdentityTruth
--- PASS: TestGoldenMessageIdentityTruth (0.00s)
PASS
ok  	dshgo/internal/transport	0.095s
```

### UI 身份与真实 apiclient 重放

```text
=== RUN   TestLoadJSONDecodesPersistentMessageIdentity
--- PASS: TestLoadJSONDecodesPersistentMessageIdentity (0.00s)
=== RUN   TestSameTextMessagesWithDifferentV4SeqBothSurviveLateReplay
--- PASS: TestSameTextMessagesWithDifferentV4SeqBothSurviveLateReplay (0.00s)
=== RUN   TestLegacyReplayWithoutIdentityUsesScopedFallbackNotExactDedup
--- PASS: TestLegacyReplayWithoutIdentityUsesScopedFallbackNotExactDedup (0.00s)
PASS
ok  	github.com/MYCF711/dsh-ui/internal/apiclient	0.077s
```

```text
=== RUN   TestContractGoldenThroughRealAPIClient
=== RUN   TestContractGoldenThroughRealAPIClient/HTTP裸参数与原始history
=== RUN   TestContractGoldenThroughRealAPIClient/stale_epoch四字段经APIError冒泡
=== RUN   TestContractGoldenThroughRealAPIClient/两会话隔离与断号检测
=== RUN   TestContractGoldenThroughRealAPIClient/FillGap补原始帧与内容重放
=== RUN   TestContractGoldenThroughRealAPIClient/load与乱序WS重放按v4Seq精确去重
=== RUN   TestContractGoldenThroughRealAPIClient/旧epoch订阅只收reset
--- PASS: TestContractGoldenThroughRealAPIClient (0.12s)
    --- PASS: TestContractGoldenThroughRealAPIClient/HTTP裸参数与原始history (0.00s)
    --- PASS: TestContractGoldenThroughRealAPIClient/stale_epoch四字段经APIError冒泡 (0.00s)
    --- PASS: TestContractGoldenThroughRealAPIClient/两会话隔离与断号检测 (0.00s)
    --- PASS: TestContractGoldenThroughRealAPIClient/FillGap补原始帧与内容重放 (0.00s)
    --- PASS: TestContractGoldenThroughRealAPIClient/load与乱序WS重放按v4Seq精确去重 (0.00s)
    --- PASS: TestContractGoldenThroughRealAPIClient/旧epoch订阅只收reset (0.10s)
=== RUN   TestContractSnapshotRemainsCanonical
--- PASS: TestContractSnapshotRemainsCanonical (0.00s)
PASS
ok  	github.com/MYCF711/dsh-ui/cmd/mockd	0.170s
```

相关包完整回归：

```text
ok  	dshgo/cmd/dshd	55.535s
ok  	dshgo/internal/transport	0.878s
```

```text
ok  	github.com/MYCF711/dsh-ui/internal/apiclient	7.188s
ok  	github.com/MYCF711/dsh-ui/cmd/mockd	9.962s
ok  	github.com/MYCF711/dsh-ui/plugins/dsconversation	3.090s
```

## 6. 双仓全量 test / vet / build

实际命令（宿主）：

```bash
go test ./... -count=1 && go vet ./... && CGO_ENABLED=0 go build ./...
```

实际命令（UI）：

```bash
CGO_ENABLED=0 go test ./... && CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go build ./...
```

两条均 exit 0；vet/build 没有诊断输出。完整未删节 test 输出保存在：

- `C:/Users/Administrator/AppData/Local/Temp/message-identity-artifacts/host-full.log`
- `C:/Users/Administrator/AppData/Local/Temp/message-identity-artifacts/ui-full.log`

宿主原样包汇总：

```text
ok  	dshgo/cmd/dshd	57.285s
ok  	dshgo/cmd/dshtui	2.698s
ok  	dshgo/codecs/v4	33.696s
ok  	dshgo/internal/acp	5.520s
ok  	dshgo/internal/agent	0.205s
ok  	dshgo/internal/approval	1.363s
ok  	dshgo/internal/compact	0.196s
ok  	dshgo/internal/fsutil	3.702s
ok  	dshgo/internal/kernel	3.655s
ok  	dshgo/internal/llm	0.279s
ok  	dshgo/internal/modes	4.911s
ok  	dshgo/internal/pluginmgr	19.612s
ok  	dshgo/internal/prompt	8.138s
ok  	dshgo/internal/prune	0.244s
ok  	dshgo/internal/schedule	5.261s
ok  	dshgo/internal/schema	0.232s
ok  	dshgo/internal/session	31.528s
ok  	dshgo/internal/spi	0.250s
ok  	dshgo/internal/transport	1.201s
ok  	dshgo/internal/wasmhost	22.161s
ok  	dshgo/modules/exec/bash	18.505s
ok  	dshgo/modules/fs/local	7.796s
ok  	dshgo/modules/tool/ask	0.208s
ok  	dshgo/modules/tool/edit	4.893s
ok  	dshgo/modules/tool/github	9.289s
ok  	dshgo/modules/tool/glob	2.771s
ok  	dshgo/modules/tool/goal	1.220s
ok  	dshgo/modules/tool/grep	3.352s
ok  	dshgo/modules/tool/hindsight	1.552s
ok  	dshgo/modules/tool/plan	6.835s
ok  	dshgo/modules/tool/plugin	4.687s
ok  	dshgo/modules/tool/present	1.408s
?   	dshgo/modules/tool/pwsh	[no test files]
ok  	dshgo/modules/tool/read	4.992s
ok  	dshgo/modules/tool/readimage	2.075s
ok  	dshgo/modules/tool/schedule	9.478s
ok  	dshgo/modules/tool/skill	5.760s
ok  	dshgo/modules/tool/subagent	0.170s
ok  	dshgo/modules/tool/team	8.351s
ok  	dshgo/modules/tool/teamtask	5.855s
ok  	dshgo/modules/tool/todo	0.133s
ok  	dshgo/modules/tool/unlag	3.740s
ok  	dshgo/modules/tool/watch	5.549s
ok  	dshgo/modules/tool/wd	2.014s
ok  	dshgo/modules/tool/webfetch	0.227s
ok  	dshgo/modules/tool/websearch	0.374s
ok  	dshgo/modules/tool/workflow	0.407s
ok  	dshgo/modules/tool/write	1.821s
ok  	dshgo/modules/tool/wsdeps	9.402s
ok  	dshgo/modules/tools	0.451s
```

UI 原样包汇总：

```text
ok  	github.com/MYCF711/dsh-ui/cmd/app	24.936s
ok  	github.com/MYCF711/dsh-ui/cmd/mockd	10.066s
ok  	github.com/MYCF711/dsh-ui/cmd/plugingen	30.757s
ok  	github.com/MYCF711/dsh-ui/internal/ansi	0.141s
ok  	github.com/MYCF711/dsh-ui/internal/apiclient	16.558s
ok  	github.com/MYCF711/dsh-ui/internal/mdlite	2.072s
ok  	github.com/MYCF711/dsh-ui/internal/secretmask	0.100s
ok  	github.com/MYCF711/dsh-ui/internal/uidecl	2.400s
ok  	github.com/MYCF711/dsh-ui/internal/uikernel	0.573s
ok  	github.com/MYCF711/dsh-ui/internal/uiplugins	0.592s
ok  	github.com/MYCF711/dsh-ui/pluginapi	0.145s
ok  	github.com/MYCF711/dsh-ui/plugins/dsassets	0.293s
ok  	github.com/MYCF711/dsh-ui/plugins/dsbrand	0.113s
ok  	github.com/MYCF711/dsh-ui/plugins/dscommands	1.503s
ok  	github.com/MYCF711/dsh-ui/plugins/dscomposer	13.557s
ok  	github.com/MYCF711/dsh-ui/plugins/dsconversation	23.846s
ok  	github.com/MYCF711/dsh-ui/plugins/dscore	0.125s
ok  	github.com/MYCF711/dsh-ui/plugins/dsdecl	6.185s
ok  	github.com/MYCF711/dsh-ui/plugins/dsdeliverables	0.124s
ok  	github.com/MYCF711/dsh-ui/plugins/dsdemo	0.117s
ok  	github.com/MYCF711/dsh-ui/plugins/dsfiles	1.404s
ok  	github.com/MYCF711/dsh-ui/plugins/dsgoal	1.630s
ok  	github.com/MYCF711/dsh-ui/plugins/dsjobs	0.119s
ok  	github.com/MYCF711/dsh-ui/plugins/dsopen	1.862s
ok  	github.com/MYCF711/dsh-ui/plugins/dsquestions	2.218s
ok  	github.com/MYCF711/dsh-ui/plugins/dsrightbar	8.895s
ok  	github.com/MYCF711/dsh-ui/plugins/dsschedule	0.175s
ok  	github.com/MYCF711/dsh-ui/plugins/dssettings	12.020s
ok  	github.com/MYCF711/dsh-ui/plugins/dsshell	6.524s
ok  	github.com/MYCF711/dsh-ui/plugins/dsshortcuts	5.189s
ok  	github.com/MYCF711/dsh-ui/plugins/dssidebar	8.707s
ok  	github.com/MYCF711/dsh-ui/plugins/dsstatusbar	9.802s
ok  	github.com/MYCF711/dsh-ui/plugins/dsterminal	0.824s
ok  	github.com/MYCF711/dsh-ui/plugins/dstool	3.532s
ok  	github.com/MYCF711/dsh-ui/plugins/dstopbar	5.043s
```

## 7. 定向 race（原样输出）

第一次直接跑时没有把已有 MinGW 加入 PATH，原样失败为 `cgo: C compiler "gcc" not found`。随后显式使用 `C:/toolchains/mingw64/bin`（GCC 16.2.0）重跑，不修改全局环境：

```bash
CGO_ENABLED=1 go test -race ./cmd/dshd ./internal/transport -count=1
```

```text
ok  	dshgo/cmd/dshd	220.730s
ok  	dshgo/internal/transport	2.197s
```

```bash
CGO_ENABLED=1 go test -race ./internal/apiclient ./cmd/mockd ./plugins/dsconversation -count=1
```

```text
ok  	github.com/MYCF711/dsh-ui/internal/apiclient	8.647s
ok  	github.com/MYCF711/dsh-ui/cmd/mockd	11.008s
ok  	github.com/MYCF711/dsh-ui/plugins/dsconversation	19.094s
```

完整输出：`message-identity-artifacts/host-race.log`、`ui-race.log`；0 DATA RACE。

## 8. 真实 dshd + 真实 UI apiclient 同 home 重启

### 配置与命令

- 新二进制：`message-identity-artifacts/dshd-msgid.exe`，SHA-256 `9255672ddd652ae73e189e1c676449d5d6231ee098f49a789c69bb2ac0ba1736`；`bin-check` 为 `合计 OK=1 FAIL=0`。
- 临时 home：最终证据目录下的 `home/`；回环 `--addr 127.0.0.1:0`、`--policy ask`、`--reasoning max`、`--max-steps 4`。
- 模型：`group/deepseek-v4.1-flash1017` 经本机既有 magpie；未改 magpie/DSH 配置。
- A/B 两会话、真实 text/reasoning/tool/审批/新 prompt、旧 cookie/token 401、stale_epoch/reset、v4 append-only；只终止脚本启动且核对映像路径的 dshd PID，没有启动 GUI。

```bash
python scripts/restart-recovery.py \
  --binary C:/Users/Administrator/AppData/Local/Temp/message-identity-artifacts/dshd-msgid.exe \
  --ui C:/Users/Administrator/AppData/Local/Temp/dsh-ui-message-identity-isolated \
  --evidence-dir C:/Users/Administrator/AppData/Local/Temp/DSH-GO-message-identity/tmp/message-identity-restart-final \
  --timeout 600
```

原样关键汇总（完整输出在 `message-identity-artifacts/restart-final.log`）：

```text
RR_SUMMARY phase=A OK=40 FAIL=0 SKIP=0
--- PASS: TestRestartRecovery (3.79s)
PASS
ok  	github.com/MYCF711/dsh-ui/internal/apiclient	3.853s
RR_SUMMARY phase=B OK=33 FAIL=0 SKIP=0
--- PASS: TestRestartRecovery (2.17s)
PASS
ok  	github.com/MYCF711/dsh-ui/internal/apiclient	2.232s
DRIVER_OK v4-append-only-old-prefix-unchanged [{'path': 'sessions\\--C-Users-Administrator-AppData-Local-Temp-DSH-GO-message-identity--\\54b53a52-2856-721d-88b9-8213a52fcfa2\\session.v4.jsonl.zstd', 'beforeBytes': 6631, 'afterBytes': 6631, 'oldPrefixUnchanged': True}, {'path': 'sessions\\--C-Users-Administrator-AppData-Local-Temp-DSH-GO-message-identity--\\7b330e41-1587-91cf-395a-6c88a1bba98a\\session.v4.jsonl.zstd', 'beforeBytes': 14402, 'afterBytes': 20790, 'oldPrefixUnchanged': True}]
DRIVER_OK v4-only-alpha-appended-beta-not-rewritten {'alpha': 'sessions\\--C-Users-Administrator-AppData-Local-Temp-DSH-GO-message-identity--\\7b330e41-1587-91cf-395a-6c88a1bba98a\\session.v4.jsonl.zstd', 'alphaAppended': True, 'beta': 'sessions\\--C-Users-Administrator-AppData-Local-Temp-DSH-GO-message-identity--\\54b53a52-2856-721d-88b9-8213a52fcfa2\\session.v4.jsonl.zstd', 'betaUnchanged': True}
OK=84 FAIL=0 SKIP=0
```

随后用 `message-identity-artifacts/verify_restart_identity.py` 自动比对本次真实 load/history，而非手写 JSON：

```text
MSGID_OK cross-process-load-identity-stable before=[('user', 2), ('assistant', 3), ('user', 7), ('assistant', 8), ('assistant', 11)] after=[('user', 2), ('assistant', 3), ('user', 7), ('assistant', 8), ('assistant', 11)]
MSGID_OK cross-process-history-frames-byte-equivalent before=16 after=16
MSGID_OK cross-process-new-history-prefix-unchanged before=16 final=22
MSGID_OK load-before-matches-raw-history load=[('user', 2), ('assistant', 3), ('user', 7), ('assistant', 8), ('assistant', 11)] history=[('user', 2), ('assistant', 3), ('user', 7), ('assistant', 8), ('assistant', 11)]
MSGID_OK load-after-matches-raw-history load=[('user', 2), ('assistant', 3), ('user', 7), ('assistant', 8), ('assistant', 11)] history=[('user', 2), ('assistant', 3), ('user', 7), ('assistant', 8), ('assistant', 11)]
MSGID_OK transport-seq-is-not-v4Seq
MSGID_RESTART_SUMMARY OK=76 FAIL=0
```

76 项完整输出：`message-identity-artifacts/restart-identity-verify.log`。原始输入帧、load、revisions、进程归属与清理记录均在 `tmp/message-identity-restart-final/`。

前两次探针把证据目录放在宿主工作区外，`write` 工具按既有沙箱拒绝目标文件，原样汇总均为 `OK=82 FAIL=2 SKIP=0`；没有放宽真实写入断言。最终把证据目录放进宿主 `tmp/` 后全绿，这两次环境失败不冒充产品回归。

## 9. 冻结 golden 哈希

双仓同相对路径的三份共享文件逐字节一致：

| 文件 | SHA-256 |
|---|---|
| `contract-golden.json` | `854048de84c4beafda4dd4509c2d6b5b886486eb6920a02dff2e1e68a504f5ec` |
| `ws-golden.jsonl` | `bf627a8a7c64783d3cfae70df55081626265f6a9766437f71b13403b853fbc02` |
| `ws-golden-b.jsonl` | `2db71d8d3c818f421de03acb0aaa634b50341c6caa30ef054fc4105f2f286666` |

快照独立哈希：宿主 `api.snapshot.json` = `5a9674fdade2012d04f7186889250b22448bc0f387e60d782f81a135e30158f9`；UI `api.snapshot.json` = `d919c43f5a35e882c4a4443b698eb49b29208450ac3f9ce8c55d714c55eeb0a1`。

## 10. 交付物与验收映射

- `C:/Users/Administrator/AppData/Local/Temp/message-identity-host.patch`
- `C:/Users/Administrator/AppData/Local/Temp/message-identity-ui.patch`
- 同目录对应 `.sha256` 文件记录各自 SHA-256。
- patch 只含本任务相对开工快照的增量，不夹带共享工作树其他 owner 的修改。

| 验收项 | 本次证据 |
|---|---|
| load 与 WS 同一 assistant/user 只显示一次 | 真实 apiclient B load + 6 次乱序/迟到重放仍 4 条；真实重启 load/history 5 条身份一致 |
| 同文两条都保留 | B golden 同文 user 71/73、assistant 72/74；单测与网络重放均通过 |
| A/B、迟到重放不串 | 共享 golden 两会话隔离、倒序重放；真实探针 A/B no-cross-talk |
| 跨进程身份稳定 | 最终真实重启 `OK=84` + 身份比对 `OK=76`；16 帧逐字恢复 |
| reasoning/tool/approval/turn-done 不变 | 全量 golden、相关包与真实审批/工具恢复全绿 |
| 旧记录缺字段 scoped fallback | `TestLegacyReplayWithoutIdentityUsesScopedFallbackNotExactDedup` 保留独立局部消息，不宣称精确 |
| 非手写 JSON | 真实 `apiclient.Load/History/Frame.Event/Stream/Merge` + 两个真实 dshd 进程同 home 探针 |
| 全仓与 race | 双仓全量 test/vet/build exit 0；五个相关包 race exit 0 |

未验证范围：可见 GUI 未启动（任务要求）；缺身份的普通旧 journal 仍不承诺跨 load/WS 精确去重；调试 modelRunner 的 persist 前简化帧不冒充 v4-backed 身份。本任务不外推完整工作日或其他插件能力。
