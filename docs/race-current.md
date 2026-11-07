# RACE-CURRENT：0.2.2 兼容修复后两仓全仓竞态实测

> 验证时间：2026-11-05 00:26–00:37（UTC+08:00）。只做验证，不改产品代码。
> 本报告来自当前工作树的全仓 `go test -race ./...`，不是 W20 六组定向 race 的外推。

## 结论

| 项目 | 状态 | DATA RACE 报告数 | 全仓包数 | 退出码 | 完整日志 |
|---|---|---:|---:|---:|---|
| **HOST_RACE** | **OK** | **0** | **59 = 49 ok + 10 no test** | **0** | [host-race.log](../tmp/race-current/host-race.log) |
| **UI_RACE** | **FAIL** | **6** | **40 = 33 ok + 6 no test + 1 fail** | **1** | [ui-race.log](../tmp/race-current/ui-race.log) |

这里的 **6** 是 UI 全仓输出中的 `WARNING: DATA RACE` 报告数，不声称是 6 个互不相同的根因。工具链实际可用，因此两项都不是 `SKIP`。

## 被测版本与工具链

测试的是任务指定的**当前工作树（含未提交的 0.2.2 兼容修复）**，HEAD 仅用于定位，不能把结果描述成纯 HEAD 测试：

- 宿主：`C:/Users/Administrator/AppData/Local/Temp/DSH-GO-race-current`，HEAD `af4a49a2817592d7affb99bb3a16c7e03214ba1c`。
- UI：`C:/Users/Administrator/AppData/Local/Temp/dsh-ui-race-current-isolated`，HEAD `3925e84f1524dd101e020d74b1d5bb422d25cf4d`。
- Go：`go version go1.27.3 windows/amd64`。
- C 工具链：`C:/toolchains/mingw64/bin/gcc.exe`，`gcc.exe (MinGW-W64 x86_64-ucrt-posix-seh, built by Brecht Sanders, r3) 16.2.0`。
- target：`x86_64-w64-mingw32`；测试环境 `CGO_ENABLED=1`，`CC=gcc`。
- 工具链与包枚举证据：[toolchain.log](../tmp/race-current/toolchain.log)、[host-packages.txt](../tmp/race-current/host-packages.txt)、[ui-packages.txt](../tmp/race-current/ui-packages.txt)。

共同环境前缀：

```bash
PATH="/c/Program Files/Go/bin:/c/toolchains/mingw64/bin:$PATH" CGO_ENABLED=1
```

## 全仓命令、时间与包数

### HOST_RACE

工作目录：`C:/Users/Administrator/AppData/Local/Temp/DSH-GO-race-current`。

```bash
go list ./...                 # exit 0，59 包
PATH="/c/Program Files/Go/bin:/c/toolchains/mingw64/bin:$PATH" CGO_ENABLED=1 go test -race ./...
```

- 开始：`2026-11-05T00:27:42+08:00`。
- 结束：`2026-11-05T00:32:38+08:00`，耗时 **297 秒**。
- **exit 0**；49 个测试包 `ok`，10 个包 `[no test files]`，合计 59，0 个失败包。
- `WARNING: DATA RACE` = **0**。
- 完整逐包输出与汇总：[host-race.log](../tmp/race-current/host-race.log)。

### UI_RACE

工作目录：`C:/Users/Administrator/AppData/Local/Temp/dsh-ui-race-current-isolated`。

```bash
go list ./...                 # exit 0，40 包
PATH="/c/Program Files/Go/bin:/c/toolchains/mingw64/bin:$PATH" CGO_ENABLED=1 go test -race ./...
```

- 开始：`2026-11-05T00:32:56+08:00`。
- 结束：`2026-11-05T00:33:47+08:00`，耗时 **51 秒**。
- **exit 1**；33 个测试包 `ok`，6 个包 `[no test files]`，`github.com/MYCF711/dsh-ui/cmd/app` 失败，合计 40。
- `WARNING: DATA RACE` = **6**。
- 完整输出与 6 段 race 栈：[ui-race.log](../tmp/race-current/ui-race.log)。

## UI race 最小定位与复现

### 全仓失败归属

| 包 / 测试 | 报告数 | 最小访问位置摘要 |
|---|---:|---|
| `cmd/app` / `TestF2NewSessionWhileApprovalPending` | 5 | 后台 `newSession/openSession/saveScroll` 与绘制路径并发访问 Viva scroll/model 状态；另有两段测试清理 `saveShellGlobals` 与后台 `refreshSession` 并发访问全局状态 |
| `cmd/app` / `TestWPC1HeadlessRealActionsAndTwoSizeEvidence`（失败子测试 `1182x832`） | 1 | `plugins/dsconversation/wpc1.go:117` 的 `drawOutgoing` 读取，与 `cmd/app/wpc1_send.go:115` 的 `dispatchSend.func1` 写入并发 |

关键栈位置（完整栈以全仓及复现日志为准）：

- `cmd/app/main.go:676` / `main.go:553`：后台 `saveScroll`，对侧为 `flex/scroll.go:61`、`flex/frame.go:242` 等绘制访问。
- `cmd/app/main.go:409`：后台 `openSession` 写入，对侧 `cmd/app/main.go:1617` 绘制读取。
- `cmd/app/approval_f2_panic_test.go:341-342`：测试恢复全局变量，对侧 `cmd/app/main.go:451`、`main.go:509` 后台 `refreshSession` 读取。
- `plugins/dsconversation/wpc1.go:117`：绘制读取，对侧 `cmd/app/wpc1_send.go:115` 异步发送写入；写入 goroutine 由 `cmd/app/wpc1_send.go:81` 创建。

### 最小复现命令与结果

以下命令均在 UI 仓根目录运行，并使用上述 MinGW-w64 GCC 16.2 + `CGO_ENABLED=1` 环境。

```bash
# F2：单次即复现
go test -race -count=1 -run '^TestF2NewSessionWhileApprovalPending$' ./cmd/app
# exit 1；3 段 DATA RACE；2026-11-05T00:34:51+08:00 至 00:34:57+08:00
# 日志：tmp/race-current/ui-repro-f2.log（宿主仓）

# WPC1：单次未触发，保留该阴性结果，不隐瞒波动
go test -race -count=1 -run '^TestWPC1HeadlessRealActionsAndTwoSizeEvidence$' ./cmd/app
# exit 0；0 段 DATA RACE；2026-11-05T00:35:11+08:00 至 00:35:19+08:00
# 日志：tmp/race-current/ui-repro-wpc1.log（宿主仓）

# WPC1：同测试重复 5 次复现
go test -race -count=5 -run '^TestWPC1HeadlessRealActionsAndTwoSizeEvidence$' ./cmd/app
# exit 1；1 段 DATA RACE；2026-11-05T00:35:53+08:00 至 00:36:08+08:00
# 日志：tmp/race-current/ui-repro-wpc1-count5.log（宿主仓）
```

复现日志：

- [ui-repro-f2.log](../tmp/race-current/ui-repro-f2.log)
- [ui-repro-wpc1.log](../tmp/race-current/ui-repro-wpc1.log)
- [ui-repro-wpc1-count5.log](../tmp/race-current/ui-repro-wpc1-count5.log)

复现中的报告数单独记录，**不加到全仓的 6 段上**。未修改任何实现或测试来消除 race。

## kernel 边界与 CGO_ENABLED=0 构建

### kernel 依赖

宿主仓根目录实际运行：

```bash
PATH="/c/Program Files/Go/bin:$PATH" go list -deps ./internal/kernel
```

- 时间：`2026-11-05T00:36:54+08:00` 至 `00:36:55+08:00`；**exit 0**。
- 共 93 行依赖；仓内 `dshgo/...` 依赖只有 `dshgo/internal/kernel` 自身 1 个。
- `dshgo/modules/...` = **0**；无其他仓内业务包。
- 完整 93 行与机器汇总：[host-kernel-deps.log](../tmp/race-current/host-kernel-deps.log)。

### 无 CGO 构建

| 仓库 | 完整命令 | 时间 | 包数 | 退出码 | 日志 |
|---|---|---|---:|---:|---|
| 宿主 | `PATH="/c/Program Files/Go/bin:$PATH" CGO_ENABLED=0 go build ./...` | 00:36:55–00:37:01，6 秒 | 59 | **0** | [host-build-cgo0.log](../tmp/race-current/host-build-cgo0.log) |
| UI | `cd /c/Users/Administrator/AppData/Local/Temp/dsh-ui-race-current-isolated && PATH="/c/Program Files/Go/bin:$PATH" CGO_ENABLED=0 go build ./...` | 00:37:02–00:37:06，4 秒 | 40 | **0** | [ui-build-cgo0.log](../tmp/race-current/ui-build-cgo0.log) |

## 验证边界

- 未安装工具链，未重启/关闭 DSH 或 magpie，未 commit/push。
- 未启动 GUI 或可见窗口；UI 相关测试使用其 headless 测试路径。
- 未改两仓产品代码或测试；只新增本报告与 `tmp/race-current/` 验证日志。
- 本结论只覆盖上述当前工作树的全仓 race、kernel 依赖和 CGO=0 构建，不外推真实 Transport 整链、GUI 产品验收或历史 W20 六组定向 race。
