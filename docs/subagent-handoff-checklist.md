# 派单收尾检查单

> **为什么有这份文件**：2026-11-22 一整天里，三单交付都「内容正确、收尾差一步」：
> 产物丢了、新文件没 gofmt、守卫有假阳性。**三件都不在「写代码」上，都在「收尾验证」上。**
> 这三类错误不会让交付当天变差，只会让**下一个人**以为护栏在、其实不在。
>
> **用法**：派单时把这份单子贴进任务书的「交付」小节；验收时逐条核。

---

## 一、产物必须落在**能保住**的位置

**反例（已发生）**：`wire-a` 的界面侧 `crossrepo_wire_test.go` 交付描述完整（2 断言 + 双向 allowlist + 变异 3 条全红），
但它建在 `%TEMP%/dsh-ui-*` 临时 worktree 里，**随目录清理一起消失**。主会话去取时文件已不存在。

**要求**：

- 工作树建在 **`default-workspace/`** 下（与 `DSH-GO`、`dsh-ui` 同级），**不要建在 `%TEMP%` 或系统临时目录**
- 交付时**贴出文件绝对路径**，不要只说「已创建」
- 收尾必须 `git status --short` 并贴输出 —— **空输出也是一个结果**，说明确实没留下未提交的改动

> **「子代理说交付了」和「产物在磁盘上」是两件事。**

---

## 二、新文件必须过 gofmt（以及对应语言的格式化）

**反例（已发生，同一批两次）**：

| 单 | CI 红的原因 | 代码问题 |
|---|---|---|
| `#127`（宿主） | `FAIL gofmt 未格式化: internal\transport\wire_timing_audit_test.go` | 无 |
| `#227`（界面） | 同一个疏漏，`cmd/app/wire_timing_audit_test.go` | 无 |

**两组独立作业，犯了同一个格式错误。** 说明这不是个例，是**派单模板缺一条明确要求**。

**要求**：交付前跑

```bash
export PATH="/c/Program Files/Go/bin:/c/toolchains/mingw64/bin:$PATH"
test -z "$(gofmt -l cmd internal modules codecs)" && echo "GOFMT=EMPTY"
```

**Go 之外的项目同理**：`prettier --check` / `rustfmt --check` / `black --check` 等。

---

## 三、**新建的守卫必须做变异验证**

**反例（已发生，就在主会话自己手上）**：

写了一份跨仓 wire 守卫，判据是 `strings.Contains(txt, "\""+field)`。
把宿主从 `json:"userCode,omitempty"` 改成 `json:"userCodeX,omitempty"` —— **测试仍然通过**，
因为 `"userCodeX` 以 `"userCode` 开头。

**那份守卫会以「已建立」的姿态存在，而它抓不到它要抓的那个东西。**

**要求**：

1. **每条新守卫都要有一次「改坏 → 必须红」的实证**，并贴出红的那一行
2. 变异要选**最像真实回归的那种改法**（改字段名、删赋值、换判据），不要选「删掉整个函数」这种一眼就红的
3. 对「不许误判」型守卫，**同时要有「该判的仍然要判」的反向测试** ——
   否则「一律不判」也能通过（例：`TestAuditEpochRotationGapStillRebuilds`）
4. **测试名直接写成断言的表述**，失败时不用再翻译

> **「守卫建好了」和「守卫抓得住」是两件事 —— 只有变异能分辨。**

---

## 四、合入新 main 后必须**回读内容**，不能只看「冲突=0」

**反例（已发生）**：`fix/stdin-residual` 合入新 main 时 `git merge` 报「冲突=0」、`git diff` 也正常，
但分支基线早于 `#125`，**工作树里的旧内容把 `#125` 的删除覆盖回去了** —— `askJobsOut` 从 0 处变回 2 处。

**要求**：合并后**针对本次合并涉及的关键删除/新增做一次内容回读**：

```bash
grep -c "<被删掉的符号>" <文件>        # 应为 0
git show origin/main:<文件> | grep -c   # 与本地对比
```

> **「合并无冲突」不等于「合并正确」。**

---

## 五、CI 显示 `checks` 不等于在跑

**反例（已发生）**：`#123` 连续几轮显示 `checks`，实际是

```
no checks reported on the 'fix/forge-write-boundary' branch
mergeable=CONFLICTING
```

**GitHub 在 `mergeable=CONFLICTING` 时不跑 checks。** PR 会一直「看起来在跑」，实际什么都没跑。

**要求**：`gh pr checks` 输出里出现 `no checks reported` 就立刻查 `mergeable`。

> **红了会通知你，冲突不会 —— 这比红更危险。**

---

## 六、一次只改一个机制，别在同一单里顺手重构

见 `#122` / `#123` 的教训：两单都要扫 `EvalSymlinks` 家族，撞了三处
（`officialResolveWithin`、`ResolveExistingPath`、`makeTestJunction`），解冲突花的时间超过写代码。

**要求**：派单前**核一下「这一片有没有人在改」**；同一片要么串行，要么明确切分文件边界。
