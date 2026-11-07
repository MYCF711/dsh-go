# 定时方向与派单复核 · 2026-11-05 18:08

> 实际复核 18:08–18:14（UTC+08:00）。未因定时唤醒操作 goal；goal 的 revision 3 是用户当条消息明确要求的主库理念更新。

## 0. 结论

**已建立全会话共享的官方 UI 主库与入口，后续会话不再重复拆 asar/CSS。** 可见部分直接复用官方结构树和素材，只做不可见部分重构；home 主库 → dsh-ui docs/assets 镜像 → 产品实现与验收。

## 1. 主库

- 入口：`D:/DeepSeek Harness/home/DSH-UI-official.md`
- 主库：`D:/DeepSeek Harness/home/dsh-ui-official/`
- 结构：5份文档（主壳、设置、64节点差距、55部件parity、素材复用映射）
- 素材：57份官方素材/支持文件
- 清单：`LIBRARY-MANIFEST.json` 共63项（含主库README）
- 验证：`LIBRARY_VERIFY_OK files=63 structure=5 assets=57`，主库与dsh-ui镜像SHA256一致

home AGENTS、两仓AGENTS、UIPLAN、每小时自动化任务和goal均已指向该主库。

## 2. 当前门禁

- 17:57 宿主/UI全仓 test/vet/build 均 exit 0。
- 本次复核：`go test ./plugins/dssettings -count=1` exit 0；上游 `pnpm typecheck` exit 0。
- 附件审查保持 `OK=155 FAIL=0`。
- 上游源码包既有 build 8 FAIL仍如实保留，不称全绿。

## 3. 在途任务

- W109：reviewer request changes 后修 baseline ready/reset replay/clientMessageId/child exit runtime淘汰，未恢复DONE。
- W110：真实dshd首轮红已定位lastSeq=0缺键兼容，继续修并跑P0/TUI。
- W111：正式backend manifest/registry parity曾全绿，正在同步W107最新lastSeq默认0到正式目录副本，再重跑全套门禁。

## 4. 方向核对

设置中心已按主库结构完成W103/W104/W106；主壳/侧栏/会话/composer/右栏继续按主库实现。未发现Electron/WebView回退、重复设计UI、手画冒充官方、共享覆盖或危险进程操作。Lead维持拆包、派单、集成、验收，定时唤醒未操作goal。
