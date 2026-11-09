# 2026-11-09 00:08 定时复核

## 结论

193方法当前已核定 **IMPLEMENTED=173、OFFICIAL_UNSUPPORTED=20**。W3、Speech、Native、Office均已完成，剩余20项全部属于W2 forgePanel；其中14项已真机通过，worktree 6项只欠serve loopRunner的pre-step，t138正在收口。

可见试玩已证明：fresh bundle下create/load/open成功、模型显示正确，真实会话中的Shift+Tab会调用session.setPermission且footer变化。t137正在补每次切换后的session.load回读和registry sessionIds绑定证据，不能仅凭旧load或launchpad截图判失败。

## 当前任务

- t138：serve loopRunner接worktreePreStep，只复跑worktree六状态，不重跑已通过14项。
- t137：4模式footer+load+registry三证。
- t121：之后补effort/activity/channel与TUI干净两回合。
- t122：最终只复核失败项。

## 纠偏记录

- 截图400已证实来自旧bundle；freshness已按源码hash重建。
- launchpad不是活动聊天页，不能用它判断Shift+Tab无效。
- `/connect` 303不能被urllib自动跟随到 `/` 后误判404；探针统一NoRedirect。
- 各实现包旧台账不直接采用，按最新合并逐行计算；GO.2仍是工程代号，当前二进制0.2.2-go.1。

## 边界

goal active/disarmed，定时轮未修改goal。未commit/push/tag/发布，未reset/clean、未重启DSH/magpie、未写真实profiles。t137/t138仍在运行，不清理其进程。
