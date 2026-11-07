# 定时方向与派单复核 · 2026-11-05 02:08

> 实际恢复复核 02:32–02:42（UTC+08:00）。基线：宿主 `af4a49a`、UI `3925e84`，均含当前共享工作树。用户已明确重启，本记录按持久化文件恢复，不操作 goal，不启动可见窗口，不重启/关闭 DSH 或 magpie。

## 重启恢复事实

- `list_agents` 仅 Lead 在运行，重启前子代理均不视为存活。
- W50 与 W54 最终 patch 已在重启前落盘，先验收而不是重派：
  - W50 `b8255eec…`：真实 dshd 插件管理 CLI/serve 主分派，CLI 13调用OK、serve 26步OK，未认证8方法全401，A/B home与启动reconcile通过；Lead 全仓 test/vet/build 和定向 race 全绿。
  - W54 `acde795e…`：JSON声明ID启停、A/B隔离、恢复不重复、坏文件隔离、restart-required与Dispose清空；全仓 test/vet/build 全绿。
- W49/W51/W53 无最终 patch，已从原隔离副本重新派出，禁止 reset/checkout：
  - W49 新 owner `5fcd0c2c`；
  - W51 新 owner `405ac20e`；
  - W53 新 owner `ab02b8af`。

## 方向与证据

- 保持 Go 宿主 + Go/Viva 原生界面、逐部件插件化与隔离验证；未退 Electron/WebView。
- W50 只证明宿主插件管理产品接线完成，不冒充 UI 管理页或动态执行第三方代码已完成。
- W45 旧 history 失败仍由 W49 负责；W56 等 W49，W50 已完成不再是阻塞项。
- W51 接续时发现旧 owner 仅增加模型状态声明，壳与 UI 尚未接线，继续按真实三态验收，不把声明当完成。

## 当前三路并行

| ID | owner | 内容 |
|---|---|---|
| W49 | `5fcd0c2c` | 每会话 append-only Transport journal，跨进程原始帧恢复 |
| W51 | `405ac20e` | 模型目录 loading/loaded-empty/error/ready 显式状态 |
| W53 | `ab02b8af` | 设置关于/版本真实构建信息 |

W56 等 W49；W37 性能继续等低负载窗口。两仓当前门禁均 exit 0，无可见测试窗口。

## 02:58 追加：W53完成，继续两项独立任务

- W53 `4459621d…` 已集成：真实Version/Commit/GoVersion、逐字段缺失隐藏、两尺寸40位Commit不溢出；全仓全绿。
- W49 首轮修后真实探针 `OK=84 FAIL=0 SKIP=0`，16帧在新epoch完整恢复；另发现torn-tail后追加影响下一进程的真红，已修并等待最终patch，暂不记DONE。
- 新增W57父子槽授权/循环防护 `09f7a06e`、W58 TUI删除线 `30a89079`，写路径与W49/W51不重叠。
- 当前四路：W49/W51/W57/W58；W56等W49，W37等低负载窗口。

## 03:13 追加：W49跨进程恢复收口

- W49 `b071b6fe…` 已集成：每会话append-only Transport journal，原始seq/type/data/time持久化，启动只读恢复，torn-tail下一次append核对后仅截未提交区间。
- 最终真实探针 `OK=84 FAIL=0 SKIP=0`，16帧在新epoch逐字恢复，原text/reasoning/tool/approval四条全绿；FINAL_VERIFY OK=17，Lead全仓门禁/定向race全绿。
- 契约H6/H14已改为原始history收口；W0/W1/W2/W7同步移除已完成依赖。
- W56 MSGID已同轮外派 `a6cd71f5`；当前W51/W56/W57/W58四路并行，W37等低负载窗口。

## 未验证

W51/W56/W57/W58 成品；W37 性能；真实可见 GUI、完整工作日/A/B、附件/引用/durable feedback、真实 WPS 200。