# 定时方向与派单复核 · 2026-11-05 11:08

> 实际复核 11:08–11:12（UTC+08:00）。基线：宿主 `af4a49a`、UI `3925e84`，均含当前共享工作树。未操作 goal，未启动可见窗口，未重启/关闭 DSH 或 magpie，未按镜像名杀进程。

## 方向与门禁

- 已读取宿主 AGENTS、PLAN §14.1、UIPLAN §3–§4.1、Transport 契约、UI AGENTS/PLAN。
- 保持 Go 宿主 + Go/Viva 原生界面；未退 Electron/WebView，未恢复 TypeScript 主壳。
- canonical 保持 HTTP 裸参数、`/ws`、`{seq,type,data,time}`、每会话 journal/epoch、原始 history、stale_epoch/reset。
- 两仓 `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0。
- 全仓门禁不覆盖 W76 真实整链的 147/6；该红证据仍保留，不能写成已完成。

## 当前任务

- W77 `ea615107`：自然成功/失败退出后的 jobs 持久输出。
  - 已证实 `root.Dispose()` 删除 `spi.JobKey`，之后 Inject 必得 `ErrMissingService` 并静默 return。
  - 修 Dispose 前捕获 Job、Dispose 后读取最终快照，注入/读页/写盘错误汇入 cmdRun error。
  - 三轮真实整链 jobs 均 76/0，三级树/readback 成功；0xC0000142 未复现。
- W76：BLOCKED(W77)，执行方153/0不代替Lead复验；修复后必须重跑。
- W7：继续等无打扰真实UI窗口。

没有依赖已满足却未派出的 READY。

## 11:20 追加：W77自然持久化完成，W78定位MSYS启动ACL

- W77 `4af144a0…` 已集成，Lead自然成功/失败定向exit0；W76 ended readback不再404。
- 但D:完整链再次复现bash `NtCreateDirectoryObject(\BaseNamedObjects\msys-2.0S5-…):0xC0000022`，exit0xC0000142，原始167字节错误已读取，helper未启动；不是普通输出波动。
- W76转BLOCKED(W78)，W78 `1b3798e5` 做D:/C:同版对照与最小复现；环境失败不写成产品PASS。

## 12:12 追加：W78根因收口，W76由Lead在C:签收

- W78 `1d122d81…` 已集成：同二进制C:=High、D:=Medium，双向复制证明位置而非内容；D cwd/home与产品Job Object均非根因，不改ACL。
- Lead在C:新构建+bin-check后实跑：核心77/0、current 76/0、`OK=153 FAIL=0 SKIP=0`，exit0；jobs三级树/readback成功。
- W76 scoped DONE；D: Medium红继续保留，不写成产品PASS。两仓AGENTS已加入二进制位置/完整性规则。
- 当前无功能施工在途；W7等无打扰真实UI窗口。

## 未验证

真实可见 GUI、完整工作日/A/B、附件/引用/durable feedback、真实 WPS 200。