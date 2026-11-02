# CHECKLIST —— 两张表，两个交付门

## A 表 · 代码交付前

- [ ] `scripts/check.sh` 全绿（build / vet / test -race / 三条架构门禁）
- [ ] 新增行为有测试；修 bug 的测试在旧代码上必须**红**
- [ ] `go list -deps ./internal/kernel` 不含 `modules/` 与 `internal/{agent,tools,session,llm}`
- [ ] 删掉 `modules/` 后 `go build ./internal/...` 仍通过（可替换性）
- [ ] 新增 SPI 接口同时交：默认实现 + 替换样本 + 禁用后 fail-loud 测试
- [ ] 每个 `Tool.Exec` 的 error 路径都设了 `Hint`
- [ ] 工具 schema 没有变化；若变了，`testdata/tools.snapshot.json` 的 diff 在本 PR 里
- [ ] 工具表 token ≤ 6000（真实分词实测，不估算）
- [ ] 没有把本地绝对路径、token、机器名写进代码或测试
- [ ] `PLAN.md` / `AGENTS.md` 与本 PR 的行为一致（不一致先改文档）

## B 表 · 发版前

- [ ] 从最新 `main` 切功能分支，PR 合并（不直接推 main）
- [ ] `CHANGELOG.md` 增加条目并写清**上游版本映射**（`go/X ← rc.Y`）
- [ ] 版本号符合 `<上游版本>-go.<上游序号>`
- [ ] `git tag -a v<版本>` 并推送；`gh release create` 附二进制
- [ ] Release notes 取自 CHANGELOG，**不发 npm**
- [ ] 冷启动 / 内存 / 工具表 token 三项数字实测并写进 Release notes

## 发完之后

- [ ] 用新二进制跑一次真实任务（不是冒烟测试）
- [ ] 更新 `MAINTENANCE.md` 的「当前状态」与「下一步」
- [ ] 把踩到的坑写进 `AGENTS.md` 或 `docs/`，不要只留在会话里
