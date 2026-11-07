# 定时方向与派单复核 · 2026-11-04 20:08

> 基线：宿主 `55b9f1e`、UI `3925e84`。只做只读复核、Lead 验收、任务表/报告与 AGENTS 维护；未启动 GUI、未杀产品进程、未重启 DSH/magpie。

## 当前门禁

- 宿主 `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0。
- UI `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0。
- 无 `dshgogui/dsh-gui/real-chain` 残留窗口进程。
- `AGENTS.md` 6995 字节、`../dsh-ui/AGENTS.md` 6949 字节；文档链接与 diff-check 通过。

## 本轮纠偏：任务表重写

旧 75 行历史表已归档到 `docs/archive-dispatch-2026-11-04.md`，`UIPLAN.md` §4.1 改为五块：

- 当前基线/复核
- IN_PROGRESS
- BLOCKED
- 近期 DONE
- 当前摘要

目的是让执行者一眼看出“现在谁能并行”，不再从历史行里猜状态。

## 四个 IN_PROGRESS（路径不重叠）

| 任务 | owner | owned paths | 状态 |
|---|---|---|---|
| COMPOSER-SELECT | `6b16fc19` | `plugins/dscomposer/**`、`dsassets/**` | 无补丁，施工中 |
| SIDEBAR-FOOTER | `903c3ae6` | `plugins/dssidebar/**` | 无补丁，施工中 |
| SETTINGS-CENTER | `f87e782d` | `plugins/dssettings/**` | 无补丁，施工中 |
| VISUAL-AUDIT | `7f781159` | `scripts/**`、`docs/visual-audit.md`、`testdata/visual-audit/**` | 无补丁，施工中 |

四个副本均从 `3925e84` 建出，基线定向测试全绿；Lead 不 wait/poll，完成方主动通知后逐个验收。

## 阻塞项

- U1-H13：pending cancel 归宿主 W4；
- WP-C/D/E：完整工具树/历史/附件/reference/durable feedback 契约未冻结；
- A-FEEDBACK：durable feedback 契约未冻结，本地 👍/👎 不算完成。

## 方向检查

- 仍是 Go 宿主 + Viva 原生界面；未退回 Electron/WebView/TypeScript UI。
- Transport 未绕过：裸参数、`/ws`、`{seq,type,data,time}`、每会话 journal/epoch、原始 history frames。
- 共享树只跑定向 test/build/vet；无 checkout/reset、原地移动 modules、按镜像名杀产品进程。
- 四个任务都禁止可见窗口，避免再次弹窗。

## 20:18 追加：两个并行任务已收口

- SIDEBAR-FOOTER patch SHA256 `b7698b8d…` 已集成：无服务/坏类型整行不画；点击真实 `dssettings.Open()` 一次；1920 y=980、1182 y=780，42px；全仓 test/vet/build 绿。账号/图标/rail 未做。
- VISUAL-AUDIT patch SHA256 `d0538a0d…` 已集成：20 判定项 PASS/0 FAIL/20 SKIP，四图偏差全 0；`--self-test` 6/6；`docs/scripts.md` 已补 visual-audit 与 real-chain 三脚本；`scripts-index.py --check` PASS。
- 当前仍两路并行：COMPOSER-SELECT、SETTINGS-CENTER。

## 未验证

COMPOSER-SELECT/SETTINGS-CENTER 成品；真实 WPS 200；共享 golden；真机 epoch 换代；宿主重启 cookie；FrameSub/重放；pending cancel；stale_epoch；U2 生命周期；OS 输入/高DPI；完整工作日/A/B；settings 11 页；附件/引用/durable feedback/完整工具树。