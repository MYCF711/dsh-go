# 定时方向与派单复核 · 2026-11-05 17:08

> 实际复核 17:08–17:58（UTC+08:00），覆盖 16:08/17:08 两次提醒。未操作 goal，未启动可见窗口，未重启/关闭 DSH 或 magpie，未推远端。

## 0. 结论

**两仓与上游当前门禁绿，GUI 已明确从“先功能后结构”纠偏为“先冻官方结构树，再一比一实现”。** 设置中心 W103/W104/W106 是第一个按新方法完成的部件；主壳、侧栏、会话、composer、右栏仍需按 W100 结构树继续实现，不能把设置页完成外推为整 GUI 完成。

## 1. 门禁

| 范围 | 命令 | 结果 |
|---|---|---:|
| 宿主 | `go test ./... -count=1` / `go vet ./...` / `go build ./...` | 全部 exit 0 |
| UI | 同上 | 全部 exit 0 |
| 上游 dsh-TUI | `pnpm typecheck` / `pnpm smoke` | 全部 exit 0 |

W105 源码包仍有既有 build 8 FAIL，忠实保留，不宣称上游构建全绿。

## 2. 官方结构树路线

- W99 设置结构树、W100 主壳结构树、W101 56份官方素材、W102 64节点差距矩阵均已集成。
- W103 完成官方模态几何/mask；W104 完成官方导航模型；W106 已把模型接入真实渲染。
- 当前设置顶级仅有真值 general/models，外观/快捷键/版本归 general，defaultPreset 使用真实 HostForm 字段；自造顶级导航已删除。
- 设置页是两尺寸真实 Viva、键鼠、A/B、全仓 test/vet/build 绿；完整官方 Modal 焦点圈/inert、主壳及其他部件仍未完成。

## 3. 上游 TUI 桥接

- W107 transport 5/5、W108 events 12/12、W109 backend assembly、typecheck、smoke 均通过。
- W110 正在做真实 dshd + 上游 TUI 联调，尚无最终结果，不能称“真实兼容完成”。
- W111 正在修 verify-backend-registry 的正式 manifest parity；不能用手工 seed 或放宽门禁代替正式接入。

## 4. 下一批必须补齐的方向

- 主壳 caption/应用/编辑菜单、侧栏/工作区/会话列表、Hero/Conversation/Composer、右栏 DockKit 与主区 Trajectory/ToolCallTree，全部以 W100 结构树与 W101 素材为基线，不再凭截图自造层级。
- W81 durable feedback、完整工具树/向前历史、W7 可见 GUI 完整工作日/A/B/OS 输入仍未完成。
- 每次 DONE 必须写 rev/命令/真实断言/证据；文档和结构树不等于产品完成。

## 5. 安全核对

未发现 Electron/WebView 主壳回退、共享仓覆盖、原地移动 modules、按镜像名杀 DSH/magpie 进程、mockd/ACP/截图/单测冒充整链。Lead 维持拆包、派单、集成、验收；未操作 goal。
