# 更新与发布

## 铁律

- **默认分支只接受走门的改动**；`main` 永远可构建。
- **不发 npm**。这是个 Go 二进制；分发走 GitHub Release。
- **仓库私有**：不要在任何公开文档、issue、Release 里贴本机路径或内网地址。

## 发一个版本

```bash
git checkout main && git pull
git checkout -b feat/<主题>

# 1) 改代码 + 补测试
scripts/check.sh

# 2) bump 版本 + 写 CHANGELOG（含上游映射）
#    语义化：修 bug → go.N+1；跟上游 minor → 新 <上游版本>-go.1

# 3) 提交并推分支
git add -A && git commit -m "M<n>: <一句话>"
git push -u origin feat/<主题>

# 4) PR 全绿后合并（squash）
gh pr create --fill && gh pr merge --squash --delete-branch

# 5) 回 main 打 tag 发 Release
git checkout main && git pull
git tag -a v0.2.1-go.5 -m "v0.2.1-go.5"
git push origin v0.2.1-go.5
gh release create v0.2.1-go.5 --title "0.2.1-go.5" --notes-from-tag
```

## 追上游

| 上游变化 | 我们做什么 |
|---|---|
| patch / rc 递增 | 只有**契约变化**才跟：会话格式、工具语义、ACP 方法 |
| minor | 新分支 `<新版本>-go.1`，只搬契约 + 我们要的功能 |
| 客户端 UI / provider 生态 | **不搬**（我们不用 Electron，provider 只保留 OpenAI 兼容） |

上游 `rc.1 → rc.5` 实测 20 个包里 18 个字节级零变更，`0.1.7 → 0.2.0` 只有 `dsh-session` 变了——**追更成本在 minor 边界**，别为 patch 版本重做工作。

## 兼容性

- 会话格式 `v4` 是**边界契约**：`codecs/v4` 出问题时的降级是"关掉 codec"，不是"改内核"。
- 任何需要改 `internal/kernel` 才能做的能力，先回到 `PLAN.md` §1 讨论——那说明 SPI 画错了。
