# 定时方向与派单复核 · 2026-11-04 17:08

> 基线：宿主 `c4584f6`、UI `fab1fab`。只做只读复核、Lead 验收、任务表/报告与 AGENTS 维护；未杀进程、未启动共享树服务、未提交、未推送。

## 当前门禁

- 宿主 `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0。
- UI `go test ./...`、`go vet ./...`、`go build ./...` 均 exit 0。
- UI 首次全仓出现 `[setup failed] open tmp\theme-accept`：另一会话临时目录的瞬时断链；`go list ./...` 立即恢复，重跑全仓 exit 0，不是代码失败。
- `AGENTS.md` 6994 字节、`../dsh-ui/AGENTS.md` 6956 字节；文档链接与 diff-check 通过。

## 已收口

### REAL-CHAIN · 39 PASS

- patch SHA256 `67fe3e84…`，38 个新增文件；Lead 独立跑 `bash scripts/real-chain.sh`，run `20261104-164728`：**39 PASS / 0 FAIL / 0 SKIP，exit 0**。
- 覆盖真 dshd + 真 UI 连接、F2 设置、重启后 load、真实 Enter 审批工具、reasoning、真实 Esc 取消；17 张真窗口截图与日志入 `testdata/real-chain/evidence/`。

### MUX-SYSTEM · 真机复验

- patch SHA256 `1c0e72f6…`：`system/message`、`developer/message` 在 `Chunks()` 返回 nil，`Event.Data` 原样保留；未知 kind 且 role 为 system/developer 也兜底不产块。
- 5 条定向、真实 14 帧回放、全仓 test/vet/build 全绿；修后 real-chain `20261104-171207` 再次 **39 PASS / 0 FAIL / 0 SKIP**，新审批截图里 4894 字系统提示已消失。
- 未验：`session.load` 若把 system 放进 messages 的另一路径、mockd/ACP、developer 真机语料。

### 两仓质量线

- 宿主 `e52796d` 父进程看门狗：serve/acp/run 在父进程退出后收尾（此前现场 34 个遗留 dshd），`562bc39` 7 个测试脚本补进程树回收。
- UI `fab1fab`/`064e108`/`93b211c` 守卫可反证与 CI 检查；`20a2169` 四条验收脚本补进程回收。
- 这些是质量基础设施，不当作产品功能完成。

## 下一 READY

- **REAL-CHAIN-2 · `19e24ef7`** 已在 `fab1fab` 隔离副本实派：只扩展 `scripts/real-chain*`/docs/testdata，补审批 Deny、allow_always 第二次免审批、可配置短超时或 SKIP、两尺寸关键容器几何。
- 目标是覆盖上一轮明确未验的 4 项，不接受只重复 allow_once；不改生产代码。

## 方向检查

- 仍是 Go 宿主 + Viva 原生界面；未退回 Electron/WebView/TypeScript UI。
- Transport 仍按裸参数、`/ws`、`{seq,type,data,time}`、每会话 journal/epoch、原始 history frames；本轮未绕过。
- 共享树只跑定向 test/build/vet；无 checkout/reset、原地移动 modules、按镜像名杀进程。
- 不把 39 PASS 外推为完整工作日/官方 11 页/附件/持久反馈完成。

## 未验证

REAL-CHAIN-2 成品；审批 Deny/allow_always/120s 超时；关键容器几何；真实 WPS 200；真实 dshd 失败轮次/重连；共享 golden；真机 epoch 换代；宿主重启 cookie；FrameSub 原子性/重放；pending cancel；stale_epoch；U2 生命周期；OS 原生输入/高 DPI；完整工作日/A/B；settings 11 页；附件/引用/durable feedback/完整工具树。