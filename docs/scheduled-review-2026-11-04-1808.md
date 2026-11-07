# 定时方向与派单复核 · 2026-11-04 18:08

> 基线：宿主 `1bb37e2`、UI `00b5439`。只做只读复核、Lead 验收、任务表/报告与 AGENTS 维护；未改产品源码、未杀产品进程、未重启 DSH/magpie。

## 当前门禁

- 宿主 `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0。
- UI `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0。
- 当前无 `dshgogui/dsh-gui` 残留窗口进程。
- `AGENTS.md` 6851 字节、`../dsh-ui/AGENTS.md` 6957 字节；文档链接与 diff-check 通过。

## 弹窗事件与处置

`REAL-CHAIN-2` 的校准循环在隔离副本里多次启动：

```text
dshgogui.exe -title rcx-demo -demo-approval -addr http://127.0.0.1:1
```

窗口 parking 在本机不可靠，反复弹到桌面。Lead 已：

- 终止两条测试进程树；
- 将整个 `dsh-ui-real-chain2-isolated` 副本改名为 `.blocked-by-user`，同一路径无法再启动；
- 确认无残留 `dshgogui` 进程；
- 将 REAL-CHAIN-2 标为 BLOCKED，恢复前必须先实现真正无窗口/不抢焦点的驱动。

这是测试基础设施缺陷，不是产品主动弹窗。

## 产品身份收口

- 宿主 `4ab3737` 修 `.syso` 目录树布局：目录树必须在数据前，否则 build/run 正常但 VersionInfo 静默全空；补两条布局测试。
- 宿主 `7b4c5cf`/`1bb37e2` 统合 TUI 产品名与文档口径。
- UI `2936bcc`/`00b5439` 统合 GUI 产品名、窗口标题与产物名；修掉陈旧生成物并加守卫。
- 已登记 `PRODUCT-IDENTITY` DONE scoped；真实任务管理器/资源管理器属性未人工复验。

## OFFICIAL-ASSETS 在途

- 用户指出仍有手绘近似；已恢复事实：目前只复用官方 logo、主题令牌、CSS/JSX 几何与部分 SVG。
- 新派 `OFFICIAL-ASSETS · 376ff7b9`：从 `tmp/official`/asar 抽取官方 SVG `viewBox`/`d`，建统一素材包，逐项标 exact/手绘/缺失，只在逐命令一致时替换；不嵌 React、不启动 GUI 窗口。
- 当前未交付 patch，不把“开始抽取”当素材复用完成。

## 方向检查

- 仍是 Go 宿主 + Viva 原生界面；未退回 Electron/WebView/TypeScript UI。
- Transport 未绕过：裸参数、`/ws`、`{seq,type,data,time}`、每会话 journal/epoch、history frames 保持。
- 共享树只跑定向 test/build/vet；无 checkout/reset、原地移动 modules、按镜像名杀产品进程。
- 不把门禁全绿当 GUI 官方素材对齐完成。

## 18:35 追加：OFFICIAL-ASSETS 集成、REAL-CHAIN-2 记账

- OFFICIAL-ASSETS patch SHA256 `d3ffcc05…` 已集成：新增 `plugins/dsassets`，发送箭头、权限四档、四条拇指改为官方路径原文；停止/copy/branch 因 viva 无弧命令明确 skip 并量化偏差，朗读官方不存在不造。
- Lead 修共享解包布局回退与解包不完整 SKIP 后，全仓 test/vet/build 全绿；`plugins_assembled_test` 判据改为“有 register.go 才算插件”，保留 ≥20 阈值。
- REAL-CHAIN-2 的 85 PASS 按执行方证据记账；Lead 只做静态补丁/证据/全仓门禁，不重启 live 窗口。期间测试窗口多次弹到桌面，已终止进程并封存旧副本。
- 新发现产品崩溃：审批挂着时 F2→Enter 新建会话，窗口 `index out of range`。已派 `fbbae827` 纯测试复现并修，不启动 GUI。

## 18:50 追加：APPROVAL-F2-PANIC 已修

- patch SHA256 `d0070673…`，4 文件；根因是 dsconversation 把上一帧的 `flex.Box` 留到下一帧 `afterDraw` 读，draw 的 hero/搜索早退发生在清 `turnBoxes` 之前。
- 修复：draw 帧首清 `turnBoxes` 并置 `frameDrawn`；afterDraw 只在本帧真画过时读 Box 并消费标记；`!drawn` 直接返回。
- 必红测试走真实产品 F2→Enter 路径，复现 `[170]/109`；拆修复反证分别红在 hero/搜索；定向 `-count=5` 稳定，全仓 test/vet/build 全绿。
- 未启动 GUI，Windows 事件包装层未重触发；真实窗口复验另列。

## 未验证

真实 VersionInfo 属性；APPROVAL-F2-PANIC 真实窗口包装层；REAL-CHAIN-2 live 未由 Lead 复跑；真实 WPS 200；共享 golden；真机 epoch 换代；宿主重启 cookie；FrameSub 原子性/重放；pending cancel；stale_epoch；U2 生命周期；OS 原生输入/高 DPI；完整工作日/A/B；settings 11 页；附件/引用/durable feedback/完整工具树。