# 端到端回归报告 · 2026-11-02

一次跑完三条线：**ACP 全方法**、**会话格式 Node 复验**、**P0 四条判据**。
脚本：`scripts/e2e-regression.mjs`、`scripts/verify-node-reads.mjs`、`scripts/measure-*.py`。

---

## 一、ACP 全方法（13 项，全通过）

一个进程里跑 `new / prompt / set_mode / load / fork`，第二个进程验 `resume`，
第三个进程验 `delete`。

| # | 检查 | 结果 |
|---|---|---|
| 1 | `session/new` + `mode` 参数 | PASS（`acp-418bd0cc`） |
| 2 | `session/prompt` 结束理由 | PASS（`end_turn`） |
| 3 | prompt 真的调了工具 | PASS（`["bash"]`） |
| 4 | prompt 有正文 | PASS（115 字符） |
| 5 | 审批链路被走过 | PASS（1 次） |
| 6 | `usage_update` 到达 | PASS（`used=4388, size=128000`） |
| 7 | `set_mode` 拒绝未知模式 | PASS |
| 8 | `session/load` 重放历史 | PASS（+4 块） |
| 9 | `session/fork` | PASS（`session-1c78`） |
| 10 | 会话文件落盘 | PASS（2 个） |
| 11 | `session/resume` 跨进程可用 | PASS（`["bash"]`） |
| 12 | `session/delete` | PASS |
| 13 | delete 只删掉目标会话 | PASS（2 → 1，fork 出来的还在） |

### 写这轮回归时踩的两个坑

**坑 1：先写了个"恒真"的断言**
```
record('会话文件存在', true, '（由 session/delete 之前的存在性检查确认）');
```
永远 PASS 的检查等于没有检查。改成真的去目录里找文件（`findSessions` 递归，
不猜 projectKey 的编码规则）。

**坑 2：delete 的断言写反了**
第一版断言"delete 之后一个文件都不该剩"，结果剩 1 个 —— 是 `fork` 出来的子会话。
**那不是 bug，是断言错了。** 改成"少了一个"。

> 教训：断言写错会让人误以为代码坏了，然后去改本来正确的逻辑。
> 判断依据要贴着**实际语义**（删了目标 ≠ 删了所有）。

---

## 二、会话格式 Node 复验

```
header OK: id=session-44ed0791-…  keys=9
Node 读取成功：6 帧 / 6 行 / 7093 字节
```

`keys=9` —— header 只用了白名单里的键，没有自造字段。

---

## 三、P0 四条判据（三次测量，取中位）

| 判据 | 目标 | 实测 | 余量 |
|---|---:|---:|---:|
| 常驻内存（整轮峰值） | < 150 MB | **15.3 MB** | 9.8× |
| 冷启动（起到 `initialize` 应答） | < 100 ms | **62.4 ms**（min 58.7） | 1.6× |
| 工具表 | ≤ 6k token | **1,401 token**（10 个工具） | 4.3× |
| 二进制（`-s -w`） | ≤ 25 MB | **8.7 MB** | 2.9× |

### 冷启动的波动要说清

同一台机器，今天三次测到 **56.5 / 62.4 / 65.1 ms**。**波动来自机器负载，不是代码**。
所以基线表里记的是"某次测量的 median"，不是承诺值；判断回归要看**同一天连续多次**的
中位，而不是拿今天的数和上周的数直接比。

---

## 四、结论

- ACP 的方法表走通，没有哪条链路断着。
- 会话格式没退化（Node 侧逐帧读通，header 键数合规）。
- P0 四条都在余量内，冷启动余量最小（1.6×），是最该盯着的一项。
