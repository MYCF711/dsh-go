# CHECKLIST —— 两张表，两个交付门

## A 表 · 代码交付前

- [ ] 逐项记录 build / vet / test / staticcheck / 架构门禁的 PASS、FAIL、SKIP、WARN；`ALL GREEN` 不替代未满足项
- [ ] `-race` 有实际成功输出才勾；不可用明确写“未验证”，race-guard 不算动态竞态检测
- [ ] 新增行为有测试；修 bug 的测试在旧代码或隔离旧版本上必须**红**，不临时破坏共享源码
- [ ] `go list -deps ./internal/kernel` 不含 `modules/` 与 `internal/{agent,tools,session,llm}`
- [ ] 抽掉 `modules/` 后 `go build ./internal/...` 仍通过（只在独立隔离副本验证，不移动共享工作树目录）
- [ ] 共享环境未运行含全局 taskkill 的 `scripts/regress.sh` 或含原地目录移动的 `scripts/check.sh`；先完成隔离修复再恢复入口
- [ ] 新增 SPI 接口同时交：默认实现 + 替换样本 + 禁用后 fail-loud 测试
- [ ] 每个 `Tool.Exec` 的 error 路径都设了 `Hint`
- [ ] 每个工具都声明了 `Caps()`，且新的写类工具已在 `TestWriteToolsDeclareCaps` 里认领
- [ ] 工具 schema 没有变化；若变了，`cmd/dshd/testdata/tools.snapshot.json` 的 diff 在本 PR 里
- [ ] 工具表 token ≤ 6000（真实分词实测，不估算）
- [ ] 没有把本地绝对路径、token、机器名写进代码或测试
- [ ] `PLAN.md` / `AGENTS.md` 与本 PR 的行为一致（不一致先改文档）

## B 表 · 发版前

- [ ] 从最新 `main` 切功能分支，PR 合并（不直接推 main）
- [ ] `CHANGELOG.md` 增加条目并写清**上游版本映射**（`go/X ← rc.Y`）
- [ ] 版本号符合 `<上游版本>-go.<N>`；N 是 Go 自有序号，不是上游 rc 序号
- [ ] `git tag -a v<版本>` 并推送；`gh release create` 附二进制
- [ ] Release notes 取自 CHANGELOG，**不发 npm**
- [ ] 冷启动 / 内存 / 工具表 token 三项数字实测并写进 Release notes（脚本：`scripts/measure-start.py`、`scripts/measure-mem.py`）
- [ ] `docs/p0-acceptance.md` 仅作宿主历史证据复核；不得用它签发整产品接管
- [ ] 原生产品发布候选另过 `UIPLAN.md` U0–U4：H14 契约、真宿主原生链、插件替换、10 任务 A/B、完整工作日；跨仓记录含 dshd_rev/ui_rev/配置/启动清理/断言/未验项

## 发完之后

- [ ] 用新二进制跑真实任务集（不是一次冒烟）；整产品发布还须完成 UIPLAN 的 10 任务 A/B 与完整工作日
- [ ] **把发布件下载到空目录跑一次** `dshd modules list` / `dshd tools`（v0.2.1-go.5 就是在这里翻的车）
- [ ] 更新 `MAINTENANCE.md` 的「原目标与现状」与「当前依赖顺序」，并核对 `PLAN.md` §14.1 仍是唯一宿主顺序来源
- [ ] 把踩到的坑写进 `AGENTS.md` 或 `docs/`，不要只留在会话里
