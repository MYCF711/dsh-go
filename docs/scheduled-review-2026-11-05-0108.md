# 定时方向与派单复核 · 2026-11-05 01:08

> 实际复核 01:50–02:00（UTC+08:00）。基线：宿主 `af4a49a`、UI `3925e84`，均含当前共享工作树。未操作 goal，未启动可见窗口，未重启/关闭 DSH 或 magpie，未按镜像名杀进程。

## 方向与实测

- 已读取宿主 AGENTS、PLAN §14.1、UIPLAN §3–§4.1、Transport 契约、UI AGENTS/PLAN。
- 路线保持 Go 宿主 + Go/Viva 原生界面；未退 Electron/WebView，未恢复 TypeScript 主壳，未按包数或截图冒充完成。
- canonical 保持 HTTP 裸参数、`/ws`、`{seq,type,data,time}`、每会话 journal/epoch、原始 history、stale_epoch/reset；W45 跨进程旧 history 缺失明确归 W49，未用 v4 load 或 golden 冒称连续。
- 初检宿主：`go test ./...` / `go vet ./...` exit 1，原因是 `tmp/restart-recovery-*/restart_recovery_test.go` 被本仓 `./...` 误收，出现 `Chunk/Event/Client undefined`。
- 已将证据探针改为 `.go.txt` 并同步修改 `scripts/restart-recovery.py`；此前 real-chain 探针同样处理且 overlay 编译 exit 0。最终宿主与 UI `go test ./...`、`go vet ./...`、`go build ./...` 全部 exit 0。

## 本轮集成与验证

- W34 `51c69802…`：replay闸→Subscribe→二次Window→journal重放；乱序补洞、pending requestId去重、reset/pending time>0；Lead 定向包与 race 均 exit 0。
- W43 `6d55b209…`：Paint所有权队列、不可变快照、后台WaitGroup、Outgoing逐帧复制；Lead **UI全仓 `go test -race ./...` exit 0，0 DATA RACE**，普通 test/vet/build 全绿。
- W48 `bd250f6f…`：受限本地插件包管理 scoped 已集成；严格manifest/hash/事务回滚与8方法适配器通过。**主dshd分派未接，不能算产品完成，归W50。**
- W45 按 `OK=79 FAIL=5 SKIP=0` 保留失败；证据与最小复现完整，修复归W49。

## 当前六路并行

| ID | owner | 内容 |
|---|---|---|
| W38 | `1d8d3476` | UI/TUI WS可选压缩 |
| W49 | `d03972f2` | 跨进程Transport journal原始帧恢复 |
| W50 | `b9cb1528` | 插件管理真实主分派/CLI接线 |
| W52 | `32d2cd34` | 设置页快捷键参考 |
| W54 | `74c2c5a2` | JSON单件启停/恢复与失败隔离 |
| W55 | `9ab64333` | Provides/Requires服务授权 |

## 依赖纠偏

- W51 模型目录三态、W53 版本行除已完成的 W43 外，还与 W52 共用 `dssettings`，故继续 BLOCKED(W52)，不能提前派。
- W37 性能实测等待当前高负载任务结束，避免并发污染。
- W50 只接 W48 已验证入口；不把独立 handler 测试冒充主程序已暴露。
- W54 不改 uikernel/uiplugins，与 W55 文件不重叠，已同轮外派。
- WP-C/WP-D/WP-E/A-FEEDBACK 继续契约阻塞，不伪造附件、durable feedback 或完整历史。

## 02:08 追加：清理旧 umbrella 状态

- W0–W3/W7 已按 W33/W34/W45 重写：不再声称 UI 快照是旧 v1、serve follow/debug 未修或重启完全未验。
- W34 进程内 serve 交接 scoped DONE；W45 重启认证 scoped DONE，但旧 history 失败仍由 W49 负责。
- 新增 W56 MSGID：assistant/user 真实 `v4Seq/messageId` 与 load↔WS 内容去重，等 W49/W50 宿主写锁释放后派；没有字段不得伪造。
- W49 初步源码证明不能从 v4 完整重建 Transport history：seq/envelope time/turn-done/broker approval 并非完整 v4 记录，选择显式每会话 append-only journal，启动只读恢复，读路径不写盘。

## 02:25 追加：W38/W52/W55集成

- W38 host `cbb8c71d…` / UI `7a614260…` 已集成；可选permessage-deflate、1MiB上限、旧路径回退与重连通过，两仓全仓及相关race均0。
- W52快捷键 `839d4fe9…` 与W55服务授权 `88a799ba…` 已集成；合并时真实捕获dsshortcuts可选读取未声明，已补内建OptionalRequires并复验，普通全仓与合并后race均0。
- W51模型三态、W53版本行已同轮外派；当前五路为W49/W50/W51/W53/W54。
- 契约H6与§3.1已同步W34，新增§3.2 WS压缩；不再保留serve/time未修的旧描述。

## 未验证

W49/W50/W51/W53/W54 成品；W56 等 W49/W50；W37 性能；真实可见 GUI、完整工作日/A/B、附件/引用/durable feedback、真实 WPS 200。
