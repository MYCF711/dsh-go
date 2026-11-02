# 协作一致性：三条疑点的实测（2026-11-03）

dsh-go 的设计隐含"单机单用户"。下面三条是它最可能撑不住的地方。
**先测，再改**——三条里两条是真问题，一条本来就对。

复现：`python scripts/collab-probe.py`

## ① 锁文件跨机器 / 崩溃残留

**现象**：手工放一个 `pid=1` 的 `.lock`（模拟另一台机器或崩溃留下的），再 `session/resume`。

```
有僵尸锁时 resume: 失败: 恢复失败: 会话正被另一个进程写入（锁文件 …session.v4.jsonl.zstd.lock）
无锁时   resume: 成功
```

**判定：行为正确，但不能只说"失败"。**

一份会话只允许一个写入者（M30）——这条不能松，因为两个进程各从 `len(events)`
编号会写出**重复 seq**，而 seq 是 surface 与压缩遮蔽的排序依据。

跨机器时锁文件跟着 home 走，所以另一台机器会看到它。这个"看到"是对的：
**它确实不知道那台机器还在不在写**。诚实做法是拒绝 + 说清怎么办，
而不是猜。`dshd doctor` 会把锁文件路径给出来。

### 顺带发现一个真漏洞（已修）

为了修 ②（见下），`Resume` 改用了 `OpenSessionAt` ——而**它当时不抢锁**：

```
有僵尸锁时 resume: 成功          ← 修之前
```

等于给"同一份会话两个写入者"开了后门，把 M30 那条防线绕过去了。
现在 `OpenSessionAt` 也抢锁，`TestOpenSessionAtAlsoClaimsLock` 钉住。

> 教训：换一条代码路径走入某功能时，要检查那条路上**原有的保护还在不在**。

## ② projectKey 按 cwd 字符串算

**现象**：同一个逻辑 workspace 换个路径（`…/a/proj` → `…/b/proj`），
拿**正确的 session id** 去 `session/resume`：

```
修前：失败: 恢复失败: 找不到会话 session-ecae14f3-…
修后：成功
```

**判定：真问题，已修。**

`projectKey` 按 cwd 字符串算（`--D-…-a-proj--`）。换盘符、换挂载点、
换一台机器，目录名就变了。而 **session id 是全局唯一的 uuid**——明明能唯一确定，
却因为目录名算不出来就找不到。

修法：`session.ResolvePath` 先按 `(cwd, id)` 算，找不到再按 id 全库找
（`FindByID`，`session/delete` 本来就这么做，两条路之前不一致）。
多处命中仍然报错让人指定 cwd——不替人猜。

```
按 cwd 算出的位置没有（…），按 id 也没找到: 找不到会话 X（在 … 下）
```

## ③ 审批记忆

**判定：按设计，本来就对，但需要说清。**

代码里注释写明：默认关闭，只有 `--remember-approvals` 才写盘，
位置 `<home>/state/approvals`。

- 它**跟着 home 走**：同一台机器换 home 就换一份记忆；两台机器用不同 home 就是两份
- 它**不写进 v4 日志**（M?? 的约束：日志只放会话事实）
- 实测：`--policy allow` / `deny` 下都不会产生文件——没有"问过一次"就没有记忆，
  这符合预期

风险不在实现，在**误解**：有人会以为"我在这台机器上允许过，另一台也允许"。
文档里写清即可。

## 结论

| 疑点 | 结论 | 动作 |
|---|---|---|
| ① 锁跨机器 | **本来就对**（拒绝是正确行为） | 顺带修了 `OpenSessionAt` 不抢锁的后门 |
| ② projectKey | **真问题** | `ResolvePath` 回退按 id 找 |
| ③ 审批记忆 | **本来就对** | 文档写清"跟着 home 走" |

三条里只有一条需要改代码，但**测试过程中挖出了另一个真问题**——
这也是"先测再改"值回票价的地方。

## ④ 会话列表里的重复条目（2026-11-03）

**怎么测**：造一份会话，把整个目录复制到另一个项目目录下（模拟"换机器后 projectKey 变了"），
再按不同的 cwd 调 `session/list`。

```
项目目录: ['--C-…-tmpear80qu7--', '--other-project--']

按 A 的 cwd 列 → 1 条  ['session-3fc8a85c-…']
按 B 的 cwd 列 → 0 条  []
不给 cwd     → 2 条  ['session-3fc8a85c-…', 'session-3fc8a85c-…']   ← 同一个 id 两次
```

**判定：真问题，已修。**

面板上会出现两行一模一样的条目，用户分不出哪行是哪份；更糟的是按 id 删除会撞上
`FindByID` 的「多处命中，请显式给 cwd」——列表给了两条、删除却报歧义。

修法：`ListSessions` 按 session id **合并**，保留最新的那份（列表本来就按更新倒序），
份数写进 `copies` 字段：

```json
{"sessionId": "session-3fc8a85c-…", "copies": 2, …}
```

只有一份时不打这个字段（`copies` 为 0 会被 `omitempty` 省略）——不给面板添噪声。

`TestListSessionsDedupesSameIDAcrossProjects` / `TestListSessionsSingleHasNoCopies` 钉住。

## B 线四条疑点的总结

| 疑点 | 结论 |
|---|---|
| ① 锁跨机器 | 本来就对（拒绝是对的；doctor 给路径） |
| ② projectKey 换机器 | **真问题** → `ResolvePath` 回退按 id |
| ③ 审批记忆 | 本来就对（跟着 home 走） |
| ④ 列表重复条目 | **真问题** → 按 id 合并 + `copies` |
