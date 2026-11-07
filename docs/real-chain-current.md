# CURRENT-REAL-CHAIN（W76）：同一新 `dshd` + 真实 UI `apiclient` 当前整链

> 2026-11-05 实测：**核心 `OK=77 FAIL=0 SKIP=0`，当前能力 `OK=76 FAIL=0 SKIP=0`，合计 `OK=153 FAIL=0 SKIP=0`**。真实链脚本退出码 0；没有使用 mockd、ACP、手写宿主响应或可见 GUI 替代这条链。
>
> 最终证据：[`tmp/real-chain-current-ocid3_vp/`](../tmp/real-chain-current-ocid3_vp/)；完整驱动输出：[`tmp/real-chain-current-driver-4.log`](../tmp/real-chain-current-driver-4.log)；机器汇总：[`machine-summary.json`](../tmp/real-chain-current-ocid3_vp/machine-summary.json)。

## 1. 基线、二进制与边界

| 项 | 本次真值 |
|---|---|
| 宿主副本 | `C:/Users/Administrator/AppData/Local/Temp/DSH-GO-current-chain` |
| UI 副本 | `C:/Users/Administrator/AppData/Local/Temp/dsh-ui-current-chain` |
| `dshd_rev` | `af4a49a2817592d7affb99bb3a16c7e03214ba1c`（base + 当前共享工作树） |
| `ui_rev` | `3925e84f1524dd101e020d74b1d5bb422d25cf4d`（base + 当前共享工作树） |
| 新二进制 | [`tmp/real-chain-current-bin/dshd.exe`](../tmp/real-chain-current-bin/dshd.exe) |
| 二进制 SHA-256 | `ac4ac079cbb952ab2bd5002d0e145937ab22664d5d79e0af27c1b7a7de815bf6` |
| legacy 模型出口 | 既有 magpie `http://127.0.0.1:3425/v1`，`/models` 实测 HTTP 200 |
| legacy 模型 | `group/deepseek-v4.1-flash1017` |
| Go / race C 工具链 | `go1.27.3 windows/amd64`；MinGW-w64 GCC 16.2.0；`CGO_ENABLED=1` |

第 0 步实际执行 `python scripts/bin-check.py tmp/real-chain-current-bin/dshd.exe`，原样输出：

```text
  OK   1 个二进制都不比源码旧
合计 OK=1 FAIL=0
```

预构建前同一路径也真实红过：`OK=0 FAIL=1`，原文保存在 [`bin-check-prebuild.log`](../tmp/real-chain-current-baseline/bin-check-prebuild.log)。没有使用旧 `tmp/DSH GO.exe`，没有 commit/push，没有启动 GUI，没有改 UI 产品实现或 canonical 契约。

两个本地 OpenAI 兼容 listener 只充当被测 provider 的模型端点；`dshd serve`、每轮 `dshd run`、HTTP/WS Transport、UI `Client/Mux/History/FillGap` 与工具/进程执行全部是真实产品路径。没有用 `httptest` 或 mockd 替代 `dshd`。

## 2. 实际启动、ready token/cookie 与命令

复跑命令：

```bash
python scripts/real-chain-headless.py \
  --binary tmp/real-chain-current-bin/dshd.exe \
  --ui /c/Users/Administrator/AppData/Local/Temp/dsh-ui-current-chain
```

本次真实宿主 argv 原样存于 [`ready-redacted.json`](../tmp/real-chain-current-ocid3_vp/ready-redacted.json) 与机器汇总：

```text
dshd.exe serve --home C:\Users\Administrator\AppData\Local\Temp\DSH-GO-current-chain\tmp\real-chain-current-ocid3_vp\home \
  --addr 127.0.0.1:0 --policy ask --reasoning max --max-steps 4 \
  --model group/deepseek-v4.1-flash1017 --parent-pid 21740
```

- 临时 home：`C:/Users/Administrator/AppData/Local/Temp/DSH-GO-current-chain/tmp/real-chain-current-ocid3_vp/home`。
- `dshd` PID：`10036`；ready URL：`http://127.0.0.1:49952`。
- ready token 只经 stdout 进入 core 的 UI `Client.Connect`；第二次使用同一 token 得到真实 401。cookie 只存在同一 Go 测试进程内存，由 current 的新 UI `Client` 通过 `SetCookies` 复用；二者证据中均为 `<redacted>`。
- UI Go 测试 PID：`23964`。实际命令为 `go test -overlay=<本次 overlay-combined.json> ./internal/apiclient -run ^TestRealChainCombined$ -count=1 -v -timeout 18m`，退出码 0。
- `TestRealChainCombined` 显式先运行 `W36-core`，再运行 `current`，所以不存在第二个进程重复消费一次性 token 的绕过行为。
- 所有命令、argv、PID 与退出码见 [`machine-summary.json`](../tmp/real-chain-current-ocid3_vp/machine-summary.json)。

## 3. 本次原样输出

以下来自 [`tmp/real-chain-current-driver-4.log`](../tmp/real-chain-current-driver-4.log)，不是历史记录：

```text
RCH_SUMMARY OK=77 FAIL=0 SKIP=0
RCC_SUMMARY OK=76 FAIL=0 SKIP=0
--- PASS: TestRealChainCombined (22.62s)
    --- PASS: TestRealChainCombined/W36-core (19.94s)
    --- PASS: TestRealChainCombined/current (2.67s)
PASS
ok  	github.com/MYCF711/dsh-ui/internal/apiclient	22.696s
CORE_RESULT OK=77 FAIL=0 SKIP=0 combined_go_test_exit=0
CURRENT_RESULT OK=76 FAIL=0 SKIP=0 combined_go_test_exit=0
SECRET_SCAN_OK literals=3 leaked_files=0
CORE_OK=77 CORE_FAIL=0 CURRENT_OK=76 CURRENT_FAIL=0
OK=153 FAIL=0 SKIP=0
CLEANUP {"dshd_exit": 1, "owner_verified": true, "taskkill_exit": 0, "home_entries": 32}
```

`dshd_exit=1` 只发生在测试完成后对归属根 PID 树的强制清理；UI Go 测试和 Python 驱动均为 0。`QueryFullProcessImageNameW` 与本次新二进制完全一致，`owner_verified=true`，`taskkill_exit=0`。没有按镜像名批杀，没有触碰 DSH/magpie 或其他 PID。

## 4. W36 核心 77 项：原 72 项保留，新增 5 项 MSGID

核心日志逐行见 [`probe.log`](../tmp/real-chain-current-ocid3_vp/probe.log)。

| 链路 | 本次实际断言 |
|---|---|
| 认证 / list / load | ready token 换 cookie、二次 token 401、create/list/load、真实 `journalEpoch` 与 `permission=ask` |
| prompt / text / reasoning | 真模型正文经 UI `Event.Chunks`；allow 工具轮有 841 bytes reasoning，原始 history 同时保留 reasoning |
| tool / allow / deny | allow 真写文件；deny 文件不存在且 `tool/result.isError=true`；审批经 UI `ParseApprovalRequest/Approve` |
| pending cancel / 第二轮 | 同 requestId 的 `approval/cancel`、工具未执行、`turn/error` 收束；第二轮真实正文与 `turn/done`，`.lock=0` |
| WS 断线 / FillGap | 强制 `CloseNow`，断线期间完成一轮；`FillGap` 精确补 7..12 共 6 帧并恢复正文与终局 |
| stale / reset / 重连 | `stale_epoch` 由 UI 解成可重试 `APIError`；错 epoch 收无 seq `session/reset`；正确水位重连无旧帧重放 |
| A/B | 两会话并发，独立 seq 从 1 开始，流与 load 双向暗号隔离 |
| MSGID 新增 5 项 | load 的 user/assistant 均有真实 `v4Seq`；user `messageId` 透传且 assistant 不编造；history 的 `v4Seq` 与 Transport seq 分域；load↔history 身份序列完全一致；真实 load 后对原始 WS 帧倒序/迟到重放仍精确去重 |

原始 JSON：[`history-main-initial.json`](../tmp/real-chain-current-ocid3_vp/history-main-initial.json)、[`history-main-disconnected.json`](../tmp/real-chain-current-ocid3_vp/history-main-disconnected.json)、[`history-allow.json`](../tmp/real-chain-current-ocid3_vp/history-allow.json)、[`history-deny.json`](../tmp/real-chain-current-ocid3_vp/history-deny.json)。

## 5. 当前能力 76 项闭环

### 5.1 effort：真实 v4 → `session.load` → UI client

fixture 使用宿主真实 `session.Log` 写入 v4，不改 codec。经真实 UI `Client.Call("session.load")` 解码：

- A 的 `model/selection.reasoningEffort=high` → `effort="high"`，model 仍为 `model-A`；
- B 的 `request/header.header.config.reasoningEffort=xhigh` → `effort="xhigh"`，model 仍为 `model-B`；
- 旧 v4 只有 `request/context`，`effort` 键省略；
- 旧值 `max` 后接最新未知枚举 `medium`，仍省略，不回退旧值；
- A/B 经 UI `Load` 读取仍各自保留自己的 model，不串值。

### 5.2 settings.describe / mutate

- describe 返回可写 permission/model namespaces 与 sections；
- `permission.defaultPreset` 以 revision 0 原子写为 `deny`，新会话 `session.load.permission=deny`；
- 再用旧 revision 写入得到真实 409 `revision_conflict`；
- 以新 revision `unset` 后恢复启动值 `ask`。

### 5.3 catalog + provider：两个真实本地 OpenAI 端点

UI `ModelCatalogUpsert/Test/SetActive` 全链路实际执行。两个 endpoint 分别验证 `/v1/chat/completions`、model、stream/max_tokens 与 `Authorization: Bearer <本次 secret>`；证据只保存 `authorizationOK=true`，不保存 header literal。

实际 runner argv 见 [`dshd-stderr.log`](../tmp/real-chain-current-ocid3_vp/dshd-stderr.log)：

```text
spawn pid=10452 argv=[run ... --model model-a --reasoning max --base http://127.0.0.1:51135/v1]
spawn pid=24992 argv=[run ... --model model-b --reasoning max --base http://127.0.0.1:51136/v1]
spawn pid=27388 argv=[run ... --policy allow ... --model model-job --reasoning max --base http://127.0.0.1:51135/v1]
```

A/B 的真实 runner 请求分别落到各自 endpoint，path/model/Authorization/stream 全部匹配；普通 legacy 会话仍经 magpie 完成 `RCC_LEGACY_OK`，canonical `models.list` 也仍含原模型。错误 credential 得到端点真实 401，宿主/UI 错误为 `upstream_error`，正文只显示 `Bearer [redacted]`。

每次 UI HTTP 调用的状态、裸请求和原始响应保存在 `wire-0001.json` 至 `wire-0081.json`。credential 在请求证据中也替换为 `[redacted]`；三个随机 literal 对 wire、dshd stderr 和驱动日志的扫描均为 0 回显。唯一合法含 literal 的文件是临时 home 的 write-only `state/model-catalog.json`，在 [`secret-scan.json`](../tmp/real-chain-current-ocid3_vp/secret-scan.json) 中单独声明，不作为 Transport 回显。

### 5.4 plugin 本地包全生命周期

真实本地 v1/v2 包经 UI `PluginInstall/Enable/Disable/Update/Restore/Reconcile/Uninstall`：

- install 默认 disabled；enable 后更新 1.0.0→1.1.0，previousVersion 正确；
- 用旧版 update 得真实 409 `version_conflict`；
- disable、restore 回 1.0.0、reconcile `recovered=0`、uninstall 后 list 消失；
- 卸载后 enable 得真实 404 `not_found`；
- entry 是若执行就会写 `PLUGIN_ENTRY_EXECUTED` 的脚本，最终该文件不存在，证明只复制/校验而未执行 entry。

### 5.5 schedule delete / clear-ended

临时 home 真实 schedule 文件包含 delete/ended/running/recurring 四项：

- 按 list 返回的 SHA-256 revision 单删，remaining=3；重复删除真实 404，stale revision 真实 409；
- clear-ended 只删除 ended=1，权威 readback 保留 running 与 recurring 两项；
- 第二次 clear `deleted=0`，两项仍在。

### 5.6 jobs output / stop / 进程树 / 结束后 readback

真实 `dshd→run→bash` 启动 helper parent→child→grandchild，模型第二份响应在 stop/readback 后才放行，避免把已注销的 `cmdRun` 当成可用任务。

- `jobs.output` 第一页 `A甲🙂乙B`，rune total=5；cursor=1/limit=2 得 `甲🙂`，不切坏 UTF-8；
- 跨会话 stop 真实 403 `cross_session`，未知 id 真实 404；
- [`job-owned-pids.json`](../tmp/real-chain-current-ocid3_vp/job-owned-pids.json)：parent `32812`、child `34884`、grandchild `41388`；三者均通过精确 PID 的完整映像与 `os.SameFile` 核对；
- 首次 stop `stopped=true/done=true`，第二次真实 409 `job_finished`；三级 PID 随后全部消失；
- stop 后 cursor=3/limit=2 读回 `乙B/done=true`；放行模型并等 `turn/done`、`cmdRun` 退出后，再次读取逐字段一致。

## 6. 保留的真实红与判据修正

没有为变绿删除核心断言、降低 jobs/MSGID 判据或改产品实现。以下失败均真实执行并保留：

1. **fixture 编译红**（[`driver-red-seed.log`](../tmp/real-chain-current-driver-red-seed.log)）：`os.Stdout.Write` 两个返回值误赋一个变量，`OK=0 FAIL=1 SKIP=0`；只修探针并先编译确认。
2. **一次性 token 跨进程复用红**（[`driver-2.log`](../tmp/real-chain-current-driver-2.log)）：核心 77/0 后，current 第二次 `/connect` 真实 401，`RCC_SUMMARY OK=0 FAIL=1`，总 `OK=77 FAIL=2`；改为同一 Go 进程 core→current 共享 UI 内存 cookie，二次 token 401 断言仍保留。
3. **jobs 生命周期红**（[`driver-3.log`](../tmp/real-chain-current-driver-3.log) 与 [`real-chain-current-lz8ivnh1/`](../tmp/real-chain-current-lz8ivnh1/)）：第二份模型响应过早放行，`cmdRun` 注销后出现 8 个 `not_found`/PID 红，`RCC_SUMMARY OK=68 FAIL=8`，总 `OK=145 FAIL=9`；修正为 stop/readback 后再放行，并新增 `cmdRun` 结束后的 readback 验证，没有放宽任何断言。
4. 同次扫描把 write-only catalog 存储含 literal 计为泄漏；仅把这一明确的持久秘密存储从“响应/日志回显”扫描中排除，wire 与日志扫描仍全量执行。

## 7. 双仓全量 test / vet / build 与相关 race

命令与完整逐包输出均在 [`tmp/real-chain-current-gates/`](../tmp/real-chain-current-gates/)。

### UI（已完成）

```text
GO_TEST_EXIT=0
GO_VET_EXIT=0
GO_BUILD_EXIT=0
UI_FULL_TEST_VET_BUILD_OK
```

`go test ./... -count=1` 包含 `internal/apiclient 14.861s`、`cmd/mockd 10.125s`、`plugins/dssettings 10.299s`，其余包也全部成功；随后 vet/build 均退出 0。见 [`ui-test-vet-build.log`](../tmp/real-chain-current-gates/ui-test-vet-build.log)。

相关 race 实际命令：

```bash
CGO_ENABLED=1 go test -race ./internal/apiclient ./cmd/mockd ./plugins/dsconversation ./plugins/dssettings -count=1
```

原样输出：

```text
ok  	github.com/MYCF711/dsh-ui/internal/apiclient	12.330s
ok  	github.com/MYCF711/dsh-ui/cmd/mockd	11.113s
ok  	github.com/MYCF711/dsh-ui/plugins/dsconversation	16.742s
ok  	github.com/MYCF711/dsh-ui/plugins/dssettings	39.147s
UI_RELATED_RACE_EXIT=0
UI_RELATED_RACE_OK
```

### 宿主

全仓 `go test ./... -count=1 && go vet ./... && go build ./...` 已实际完成，原样汇总：

```text
GO_TEST_EXIT=0
GO_VET_EXIT=0
GO_BUILD_EXIT=0
HOST_FULL_TEST_VET_BUILD_OK
```

全量测试包含 `cmd/dshd 81.148s`、`codecs/v4 43.868s`、`internal/transport 1.053s`、`internal/llm 1.512s`、`internal/pluginmgr 4.851s`、`modules/exec/bash 14.214s`，所有包成功；完整输出见 [`host-test-vet-build.log`](../tmp/real-chain-current-gates/host-test-vet-build.log)。

相关 race 的实际命令为：

```bash
CGO_ENABLED=1 go test -race \
  ./cmd/dshd ./internal/transport ./internal/llm ./internal/settingsform \
  ./internal/pluginmgr ./internal/schedule ./modules/exec/bash -count=1
```

原样输出：

```text
ok  	dshgo/cmd/dshd	225.109s
ok  	dshgo/internal/transport	2.984s
ok  	dshgo/internal/llm	2.652s
ok  	dshgo/internal/settingsform	1.259s
ok  	dshgo/internal/pluginmgr	5.328s
ok  	dshgo/internal/schedule	2.011s
ok  	dshgo/modules/exec/bash	9.370s
HOST_RELATED_RACE_EXIT=0
HOST_RELATED_RACE_OK
```

完整输出见 [`host-related-race.log`](../tmp/real-chain-current-gates/host-related-race.log)，没有 DATA RACE。最终机器门禁汇总见 [`gates/summary.json`](../tmp/real-chain-current-gates/summary.json)。

## 8. 交付边界与产物

- UI 没有新增工作树文件：当前 Go 探针通过 `-overlay` 从宿主证据目录映射进真实 `internal/apiclient` 包执行。因此不需要 `real-chain-current-ui.patch`，也不存在 UI 产品实现 diff。
- 宿主改动仅限 `scripts/real-chain-headless.py`、本文与 `tmp/real-chain-current-*` 证据；未改宿主产品实现、canonical 快照/golden 或其他任务路径。
- 未启动 GUI、未做可见像素/键盘验收、未做完整工作日或性能 soak；本链不外推这些产品层结论。
- 宿主 patch：`C:/Users/Administrator/AppData/Local/Temp/real-chain-current-host.patch`；对应 SHA-256 在同目录 `real-chain-current-host.patch.sha256`。UI 无改动，故无 UI patch。未 commit/push。
