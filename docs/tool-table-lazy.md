# lazy：工具表第四档（按需装载）

> `--tools-mode=lazy`。前三档（`full` / `compact` / `merged`）都只改"模型看到什么"，
> lazy 多走一步：**表里只常驻最常用的 30 个，其余 27 个既没有 schema 也没有外壳**。

实现：`cmd/dshd/tool_lazy.go`。常驻名单由 `scripts/tool-table-hot.py` 从真实语料重算，
不是手抄的——见 `docs/tool-table-hot.md`。A/B 脚本：`scripts/tool-table-lazy-ab.py`。

---

## 1. 机制

| 环节 | 做法 | 为什么 |
|---|---|---|
| 常驻 | 语料前 30 名 | 覆盖 99.43% 的调用；被藏的 27 个合计 0.57% |
| 表外 | 只在系统提示目录里留"名字 — 一句话" | 模型得先知道有这个东西存在，否则藏 = 删 |
| 取用 | `tool_load`（投影层自带，不在 `modules/` 注册） | 一次 tool call 换一份完整 schema |
| 装载后 | **注入后续每一步的 `tools` 字段**（状态化） | 循环每步重建 toolDefs，表本来就能两步之间变；不注入的话模型拿到 schema 却无可调用入口 |
| 跨轮 | 装载集**不额外落盘**：每次 `tool_load` 都是一条 `tool/call`，下一轮 `run` 从会话日志回放 | serve 每轮起一个新进程，内存态活不过一轮；回放之后上一轮取过的工具这一轮直接可用（§6.1） |
| 挡回 | 调没装载的表外工具 → 返回提示，让它先 `tool_load` | 唯一这么做的一档。不挡的话模型凭记忆喊名字也能跑通，"先取用"这条规则就是假的 |

**挡回是自证的代价，不是洁癖**：挡回一次的成本是一次 `tool_load`；
换回来的是"模型会不会用这个机制"**可被证伪、可被测量**。

`Directory()` 把"规则 + 每个表外工具的名字和一句话"拼进系统提示。
`TestLazyHotListMatchesAssembly` 卡住"名单和装配一致"，
`TestLazyTableHidesAndReveals` / `TestLazyRunnerBlocksUnloaded` 卡住藏-取-挡三态。

---

## 2. A/B：模型会不会去拉工具？

这是 lazy 唯一真正的风险——**和 `merged` 是同一类赌注**（工具在那儿，模型不照做）。
所以上之前先 A/B：17 个任务 × 2 形态 × 2 次重复（**68 次真跑**），全部机器判定。

```bash
python scripts/tool-table-lazy-ab.py --runs 2      # 17 任务 × 2 形态 × 2 次
```

判据四条，全部机读、不靠人看：目标工具调没调 → 那次调用成没成 → 撞没撞步数上限 → 答案正则命中没。
lazy 侧另记：调没调 `tool_load`、拉的名字对不对。

### 结果（2026-11-04，`tmp/tool-table-lazy-ab.json`，68 行）

| 指标 | compact | lazy | 差 |
|---|---:|---:|---|
| 通过（good） | **34 / 34** | **33 / 34** | −1 |
| 首轮提示字节（均） | 5,386.6 tok | **3,946.6 tok** | **−26.7%** |
| 步数（均） | 2.12 | 2.85 | +0.73 |
| 撞步数上限 | 0 | 0 | — |
| `tool_load` 调用 | — | 25 次 | **25 次全部拉对** |
| 需要装载的表外任务行 | — | 26 | 其中 25 行正确地先取了 |

**常驻工具的任务（bash/read/glob/todo_write）两侧步数几乎一样**（2.0–3.0 vs 2.0–3.0）——
省字节没有代价。代价全部落在"表外工具"上：那 26 行平均多一步 `tool_load`。

### 唯一那一次没通过的是什么

`nameless_now_line`：lazy 侧模型**没去取 `now_line`**，改用 `pwsh` 调了两次系统时间。

**但它的答案是对的**（`answer_ok=true`）——判据里"必须调用点名工具"没过，
任务本身过了。所以这一档的失败形态不是"干不了活"，而是**"自己找了另一条路"**，
34 次里出现 1 次。

换句话：lazy 的真实风险不是能力，是**可观测性**——
它绕开了机制，机制那一侧的账（谁被取用过）就少记一笔。

---

## 3. 上不上、怎么上

**A/B 支持上，且默认关闭**（`--tools-mode=lazy` 不显式给就不启用，
`TestLazyNeedsExplicitFlag` 卡住这点）。理由：

1. 省的是**每一步**的首轮字节（−26.7%），而代价是**表外工具那一步**的一次取用——
   我们自己的语料里表外只占 0.57% 的调用，账是明显划算的；
2. 但"模型会不会取用"是**行为**，不是**算术**。这次 25/26 是这一次的样本；
   换模型、换提示语（`AGENTS.md`/`docs/WORKER.md` 一改就可能变）都要重跑 A/B，
   不能拿这次的结果外推；
3. 所以它保持**显式开关**：要用的人自己开，且自己在自己的任务分布上复测。

**要开的话，先读 `docs/tool-table-hot.md`**：那份名单是这批语料的产物，
语料变了（换工作区、加新工具、真实使用重心迁移）名单就该重算——
`python scripts/tool-table-hot.py --go` 的输出整段替换 `defaultHotTools`，
然后跑 `go test ./cmd/dshd -run TestLazyHotListMatchesAssembly` 复验。

---

## 4. 全表（68 次真跑，逐任务）

`python scripts/tool-table-lazy-ab.py --runs 2 --md tmp/lazy-ab-table.md` 生成。
"知道去拉"= 这一轮调了 `tool_load`；"拉对"= 请求的名字里含目标工具；
"跑通"= 目标工具被调用 + 那次调用 isError=false + 没撞满步数 + 答案正则命中。

| 任务 | 用哪类工具 | 目标工具 | 形态 | 知道去拉？ | 拉对？ | 跑通？ | 步数 | 每步 prompt_tokens | 总 input | 实际调用序列 |
|---|---|---|---|---|---|---|---:|---|---:|---|
| now_line（点名） | 表外 | `now_line` | compact | — | — | 2/2 | 2, 2 | 5387/5477; 5387/5477 | 10864 | now_line; now_line |
| now_line（点名） | 表外 | `now_line` | lazy | 是（2/2） | 2/2 | 2/2 | 3, 3 | 3947/4101/4191; 3947/4194/4284 | 12332 | tool_load,now_line; tool_load,read,now_line |
| job_list（点名） | 表外 | `job_list` | compact | — | — | 2/2 | 2, 2 | 5387/5430; 5387/5438 | 10821 | job_list; job_list |
| job_list（点名） | 表外 | `job_list` | lazy | 是（2/2） | 2/2 | 2/2 | 3, 3 | 3947/4069/4112; 3947/4067/4110 | 12126 | tool_load,job_list; tool_load,job_list |
| schedule_list（点名） | 表外 | `schedule_list` | compact | — | — | 2/2 | 2, 2 | 5384/5456; 5384/5456 | 10840 | schedule_list; schedule_list |
| schedule_list（点名） | 表外 | `schedule_list` | lazy | 是（2/2） | 2/2 | 2/2 | 3, 3 | 3944/4085/4157; 3944/4085/4157 | 12186 | tool_load,schedule_list; tool_load,schedule_list |
| team_task_list（点名） | 表外 | `team_task_list` | compact | — | — | 2/2 | 2, 2 | 5386/5468; 5386/5468 | 10854 | team_task_list; team_task_list |
| team_task_list（点名） | 表外 | `team_task_list` | lazy | 是（2/2） | 2/2 | 2/2 | 3, 3 | 3946/4137/4219; 3946/4139/4221 | 12304 | tool_load,team_task_list; tool_load,team_task_list |
| watch_status（点名） | 表外 | `watch_status` | compact | — | — | 2/2 | 2, 2 | 5384/5443; 5384/5443 | 10827 | watch_status; watch_status |
| watch_status（点名） | 表外 | `watch_status` | lazy | 是（2/2） | 2/2 | 2/2 | 3, 3 | 3944/4082/4141; 3944/4084/4143 | 12169 | tool_load,watch_status; tool_load,watch_status |
| repo_here（点名） | 表外 | `repo_here` | compact | — | — | 2/2 | 2, 2 | 5391/5487; 5391/5487 | 10878 | repo_here; repo_here |
| repo_here（点名） | 表外 | `repo_here` | lazy | 是（2/2） | 2/2 | 2/2 | 3, 3 | 3951/4094/4190; 3951/4094/4190 | 12235 | tool_load,repo_here; tool_load,repo_here |
| working_directory（点名） | 表外 | `working_directory` | compact | — | — | 2/2 | 2, 2 | 5383/5461; 5383/5461 | 10844 | working_directory; working_directory |
| working_directory（点名） | 表外 | `working_directory` | lazy | 是（2/2） | 2/2 | 2/2 | 3, 3 | 3943/4115/4193; 3943/4115/4193 | 12251 | tool_load,working_directory; tool_load,working_directory |
| bash | 常驻 | `bash` | compact | — | — | 2/2 | 3, 2 | 5392/5500/5604; 5392/5493 | 13690 | read,bash; bash |
| bash | 常驻 | `bash` | lazy | — | — | 2/2 | 2, 2 | 3952/4055; 3952/4149 | 8054 | bash; bash,read |
| read | 常驻 | `read` | compact | — | — | 2/2 | 2, 2 | 5391/5589; 5391/5589 | 10980 | read; read |
| read | 常驻 | `read` | lazy | — | — | 2/2 | 2, 2 | 3951/4149; 3951/4242 | 8146 | read; read,read |
| todo_write | 常驻 | `todo_write` | compact | — | — | 2/2 | 2, 3 | 5387/5502; 5387/5502/5610 | 13694 | todo_write; todo_write,read |
| todo_write | 常驻 | `todo_write` | lazy | — | — | 2/2 | 3, 3 | 3947/4055/4170; 3947/4055/4170 | 12172 | read,todo_write; read,todo_write |
| glob | 常驻 | `glob` | compact | — | — | 2/2 | 2, 2 | 5392/5947; 5392/6010 | 11370 | glob; glob,working_directory |
| glob | 常驻 | `glob` | lazy | — | — | 2/2 | 2, 2 | 3952/4507; 3952/5044 | 8728 | glob; glob,glob |
| nameless_now_line（不点名） | 表外 | `now_line` | compact | — | — | 2/2 | 2, 2 | 5383/5473; 5383/5473 | 10856 | now_line; now_line |
| nameless_now_line（不点名） | 表外 | `now_line` | lazy | 是（1/2） | 1/2 | 1/2 | 3, 3 | 3943/4097/4187; 3943/4032/4144 | 12173 | tool_load,now_line; **pwsh,pwsh** |
| nameless_job_list（不点名） | 表外 | `job_list` | compact | — | — | 2/2 | 2, 2 | 5380/5423; 5380/5423 | 10803 | job_list; job_list |
| nameless_job_list（不点名） | 表外 | `job_list` | lazy | 是（2/2） | 2/2 | 2/2 | 3, 3 | 3940/4062/4105; 3940/4062/4105 | 12107 | tool_load,job_list; tool_load,job_list |
| nameless_schedule_list（不点名） | 表外 | `schedule_list` | compact | — | — | 2/2 | 2, 2 | 5382/5454; 5382/5454 | 10836 | schedule_list; schedule_list |
| nameless_schedule_list（不点名） | 表外 | `schedule_list` | lazy | 是（2/2） | 2/2 | 2/2 | 3, 3 | 3942/4083/4155; 3942/4083/4155 | 12180 | tool_load,schedule_list; tool_load,schedule_list |
| nameless_history_search（不点名） | 表外 | `history_search` | compact | — | — | 2/2 | 2, 2 | 5388/5591; 5388/5549 | 10958 | history_search; history_search |
| nameless_history_search（不点名） | 表外 | `history_search` | lazy | 是（2/2） | 2/2 | 2/2 | 3, 3 | 3948/4180/4428; 3948/4180/4439 | 12562 | tool_load,history_search; tool_load,history_search |
| two_hidden_tools（一次要两个） | 表外 | `schedule_list` | compact | — | — | 2/2 | 2, 2 | 5391/5491; 5391/5491 | 10882 | schedule_list,job_list; schedule_list,job_list |
| two_hidden_tools（一次要两个） | 表外 | `schedule_list` | lazy | 是（2/2） | 2/2 | 2/2 | 3, 3 | 3951/4145/4245; 3951/4145/4245 | 12341 | tool_load(一次两个名字),schedule_list,job_list; 同 |
| nameless_present（名字不像交付物） | 表外 | `present` | compact | — | — | 2/2 | 3, 3 | 5385/5556/5666; 5385/5556/5664 | 16606 | glob,working_directory,present; glob,working_directory,present |
| nameless_present（名字不像交付物） | 表外 | `present` | lazy | 是（2/2） | 2/2 | 2/2 | 4, 3 | 3945/4408/4830/4958; 3945/4303/4407 | 15398 | tool_load,glob,read,present; tool_load,glob,present |

**25 次 `tool_load` 的请求名字全部正确**：不点名工具的 8 次全对；一次要两个表外工具时，
模型在**同一次调用**里给了 `{"names":["schedule_list","job_list"]}`；名字完全看不出用途的
`present` 也认出来了。没有一次多取、乱取。把绕道那条任务再单独跑 6 次，lazy 6/6 全中
（该任务累计 10 次 lazy 里 1 次绕道；其余 25 次表外任务 0 次绕道）。

## 5. 字节账（首轮提示之外的那一半）

| | 全表 compact | lazy（表 + 目录） | 差 |
|---|---:|---:|---:|
| 工具数 | 57 | 31（30 常驻 + `tool_load`） | −26 |
| wire 字节（`name+description+parameters`，Caps 不发） | 16,897 | 10,066 | −6,831 |
| 系统提示目录 | 0 | 2,127 | +2,127 |
| **净** | 16,897 | 12,193 | **−4,704（−27.8%）** |

`TestLazyTableSavesBytes` 卡住"净收益 ≥20%"——目录写太长、或者藏起来的都是小工具，
这条会红。参数区（`tool-table-anatomy` 口径）8,535 → 约 5,500。

## 6. 三个边界（都是真跑，不是推演；第 1 条已于 2026-11-04 解决，见 §6.1）

- **跨轮次（resume）装载状态不保留** —— **已解决**。原文保留（当时实测到的形态、
  以及当时为什么没顺手做掉）：

  > `dshd run` 每轮一个新进程，装载集在内存里。第二轮模型凭上一轮记忆直接喊 `now_line`
  > → 被挡回（`← now_line [ERR] Error: 工具 now_line 没有装载`）→ 它读 Hint → `tool_load`
  > → 再调成功。**一次往返内自愈**，代价是每轮 +1 次调用（serve 每轮起一个子进程，
  > 这条在真机上一直成立）。

  当时没做掉的理由：装载集是**会话状态**，而 `run` 那一层当时没有"会话状态"这个概念
  （`--session` 只管历史），随手加一个落盘文件等于在 v4 旁边再开一种格式；先按最小改动
  落地、把它记成已知边界更稳。补的方式见 §6.1——**一个文件都没加**。

- **步数预算要 +1**：`--max-steps 2` 时 compact 正常收敛，lazy 把"取用"那步挤掉，
  以 `turn 用满了 2 步仍未收敛` 结束；`--max-steps 3` 两边都正常。
  serve 默认 6 步（`cmd/dshd/serve.go:106`），本次最长的任务 compact 3 步 / lazy 4 步，
  6 步还够；但要真上 serve，得先给这一格留余量。
- **绕道挡不住**：挡回只挡"表外工具名"，挡不住"换个工具把事办了"（`pwsh date`）。
  这类绕道不丢正确率，只丢这次的工具设计意图。

### 6.1 已解决：装载集是会话状态（2026-11-04）

**做法**：不在任何地方新增状态。`tool_load` 被调用的那一次，本来就已经以一条
`tool/call` 事件写进了 v4 会话日志（`internal/agent/loop.go` 的 `EventToolCall`
→ `cmdRun` 的 `Emit` → `session.ToolCallEvent`）。于是"这个会话取过哪些工具"
**已经是那份 append-only 日志里的事实**，新一轮 `run` 只需要把它**回放**出来：

- `cmd/dshd/tool_lazy.go`：`lazyLoadHistory`（从 `*v4.Log` 回放）、
  `lazyTable.restore`（校验后灌回表）、`restoreLoaded`（cmdRun 那一步的全部决策）；
- `cmd/dshd/main.go`：开会话 → 读一次日志（历史与装载集共用这份解析结果）→
  回放装载集 → 再拼系统提示目录 → 写 `system/message`。

| 候选 | 判断 | 理由 |
|---|---|---|
| (a) 会话目录下一个派生文件 | 不选 | 同一件事的**第二个写者**。日志里明明记着这次装载，再造一份就可能对不上（进程被 kill 在写 `tool/result` 之前），两边不一致时没人能判谁对；还要长出 v4 之外的新落盘格式（路径规则、写锁、清理全得自己来），收益为零 |
| (b) Transport journal 一条事件 | 不选 | 那是 serve 里**按连接**派生的投影，`run` 根本没有；而且它是同一份日志的下游，绕远。本轮也不许改 `serve*.go` |
| (c) `home` 下的按会话索引 | 不选 | 要么读路径写盘（R5 不允许），要么另做一套失效/GC（会话被删、被 fork、id 复用），而它索引的事实就在会话文件里 |
| **(d) 直接回放 v4 会话日志** | **选它** | 事实已经在 `append-only` 日志里（R5：派生投影、读路径不写盘）；跨进程天然成立；删会话即清状态；**不新增文件、不新增事件**，日志体积一个字节都不涨 |

**怎么知道自己是谁**：`run` 本来就有会话身份——`--session <id>`（空=新建）、
`sessID` 与 `logw.Path`（`session.ResolvePath`/`CreateSession` 产生）。
所以这里不需要"退化成内存态"的兜底：状态挂在这个 id 的会话文件上。
`dshd tools` 没有会话，它不回放（也无关：它只打印一张静态表）。

**压缩 / 重置 / 删除时怎么办**：

| 情形 | 行为 | 理由 |
|---|---|---|
| 会话被压缩（auto-compact / `session compact`） | 装载集**不受影响**，被遮蔽的 `tool_load` 照样算数 | 回放扫的是**原始事件**（`l.Events`），不是 surface。压缩只改模型的上下文，不改变"这个会话取过哪些工具"；而这些工具每一轮都在 `tools` 字段里，跟上下文长度无关 |
| 重置 / 换会话 | 新会话 = 新文件 = 空装载集（正确地回到未装载） | 状态就是那份文件 |
| `dshd session fork` | 派生会话**继承**父会话的装载集 | fork 复制事件，父会话的 `tool_call` 就在里面；它的历史里也真有那些 `tool_load` |
| 会话被删 | 状态随之消失，没有任何残留 | 没有第二个地方存过它 |
| 表外工具集合变化（模块卸了、常驻名单重算） | 名字对不上的**一律不装载** | `restore` 只认当前还在表外集合里的名字；宁可少给一个入口（模型下一步 `tool_load` 还能拿回来），也不能让工具表里出现装配提供不了的入口 |

**错了会怎样（红线）**：读不出来、事件是垃圾、名字过期——**唯一允许的后果是回到"什么都没装载"**。
`restoreLoaded` 连 `error` 都不返回，`cmdRun` 只往 stderr 打一行。也就是说改造前那条自愈路径
（挡回 → Hint → `tool_load`）**原样还在**：状态坏掉不会让任何工具变成不可用。
反过来说，"多装"也安全——多出来的工具只是提前出现在 `tools` 里，它本来就带着完整 schema。

**证据 1（真机，两轮同一会话，每轮一个新进程）**：

```bash
H=tmp/lazy-raw-after; rm -rf $H
# 第一轮：新建会话
tmp/dshd.exe run --tools-mode lazy --home $H --policy allow --max-steps 4 \
  --workspace-bytes 1 --agents tmp/ab-rules.txt \
  --prompt "用 now_line 告诉我现在是几点、几号、星期几。"
# 从 stderr 的「会话：<path>」里取 id（就是那个目录名），第二轮续它
tmp/dshd.exe run --tools-mode lazy --home $H --session <id> --policy allow --max-steps 4 \
  --workspace-bytes 1 --agents tmp/ab-rules.txt \
  --prompt "再用 now_line 报一次现在的时间。"
```

改造前/后跑同两条命令（脚本只是把这两条跑一遍、把会话日志里的事件按轮切开）：

```bash
bash tmp/lazy-resume-raw.sh tmp/dshd-before.exe before   # → tmp/lazy-resume-raw-before.txt
bash tmp/lazy-resume-raw.sh tmp/dshd.exe        after    # → tmp/lazy-resume-raw-after.txt
python tmp/lazy-resume-check.py --exe tmp/dshd-before.exe
python tmp/lazy-resume-check.py --exe tmp/dshd.exe
```

（两个脚本都在 `tmp/`，是这一轮的一次性证物，不入库；命令本身可以照上面手跑。）

第二轮（`--session <第一轮的 id>`）的实录，改造前：

```
  → now_line {}
  ← now_line [ERR] Error: 工具 now_line 没有装载
  → tool_load {"names": ["now_line"]}
  ← tool_load [ok] 已装载 now_line
  → now_line {}
  ← now_line [ok] 2026-11-04 10:01:24 星期三
stats: tool_calls=3 input=17433 output=199
```

改造后：

```
lazy：从会话回放装载集 1 个：now_line
  → now_line {}
  ← now_line [ok] 2026-11-04 10:01:39 星期三
stats: tool_calls=1 input=8554 output=60
```

改造前/后各跑两个表外任务（`now_line`、`schedule_list`，每个两轮）：

| 任务 | 第二轮 tool/call（改造前） | 第二轮 tool/call（改造后） | 挡回 |
|---|---|---|---|
| `now_line` | `now_line[ERR] → tool_load → now_line` = **3** | `now_line` = **1** | 1 → 0 |
| `schedule_list` | `schedule_list[ERR] → tool_load → schedule_list` = **3** | `schedule_list` = **1** | 1 → 0 |

第二轮 input 也从 17,401 / 17,438 降到 8,558 / 8,512（挡回那一轮多跑一步、多带一轮上下文）。
第一轮两种形态完全一样（都是 `tool_load → 目标工具`，input 12,239）。

第 3 轮（同一个会话再起一个进程，`tmp/lazy-resume-raw3-after.txt`）同样是直接可用：

```
lazy：从会话回放装载集 1 个：now_line
  → now_line {}
  ← now_line [ok] 2026-11-04 10:04:23 星期三
stats: tool_calls=1 input=10420 output=76
```

**证据 2（单测，`cmd/dshd/tool_lazy_state_test.go`）**：

- `TestLazyLoadedSetSurvivesNewProcess`：真 v4 日志 + **两个互不相干的 `lazyTable` 实例**
  （等价于两个进程）。回放前必须挡回（否则这条测试什么都没证明）、回放后两个工具都直接
  落到执行器、目录里不再重复列出它们；顺带卡住"会话目录里只有那一份日志"（不许长出 sidecar）。
- `TestLazyRestoreFallsBackToUnloaded`：日志根本读不出来 → 空装载集，**自愈路径还在**
  （挡回 + Hint，一次 `tool_load` 就恢复）；日志能读但里面全是垃圾事件（参数不是 JSON、
  名字不存在、缺参数、`null`、空数组、别的工具的调用混在里面）→ 跳过坏的、只装好的、
  一条都不装也不报错；参数写成对象（不是 JSON 字符串）也要认。
- 反证（隔离副本，改完即弃）：把 `restoreLoaded` 改成 `return nil`，
  `TestLazyLoadedSetSurvivesNewProcess` 立刻红（`回放出来的装载集不对：[]`）——
  这条守卫不是"看过就绿"。

## 7. 一处更正：哪些工具真的在表外

任务书里举的 `web_search` / `github` / `subagent` / `send_message` **都在常驻 30 名之内**
（语料名次 27 / 15 / 13 / 9），拿它们测不出 lazy。真正被藏起来的是 27 个，其中最需要
留意的是**语料里 0 次调用**的 7 个：`enter_plan_mode`、`exit_plan_mode`、`workflow`、
`history_search`、`plugin`、`now_line`、`exec_literal`——"第一次用它们的会话要多一次往返"
恰好落在最没有样本、最不能外推的地方。plan mode 那两个还牵扯模式切换，上 serve 前
建议真机跑一遍计划模式流程。

## 8. 本轮验收

```
go build ./... && go vet ./...                        通过
go test ./cmd/dshd -count=1                           通过（11.3s，含新增 TestLazy* 6 条）
go test ./... -count=1                                通过（全部 ok，EXIT=0）
go build -o tmp/dshd.exe ./cmd/dshd                   通过
干净检出：git archive HEAD | tar -x -C /tmp/hc-lazy && (cd /tmp/hc-lazy && go build ./...)
                                                      CLEAN-CHECKOUT BUILD OK
python scripts/tool-table-hot.py                      退出码 0：Go 名单 == 语料前 30 名
python scripts/tool-table-lazy-ab.py --runs 2         68 次真跑，见 §4
```

`TestLazy*` 六条：默认仍是 compact、lazy 要显式打开、常驻名单对得上装配、
表外工具不在表里且装载后出现在**下一步**的表里、`lazyRunner` 挡回未装载调用且
`tool_load` 不落真执行器、净收益 ≥20%（现 27.8%）。

## 9. 本轮验收（装载集跨轮，2026-11-04）

改动只在三处：`cmd/dshd/tool_lazy.go`（回放 + 灌回）、`cmd/dshd/main.go`（`run` 的接线）、
新增 `cmd/dshd/tool_lazy_state_test.go`。`modules/`、`internal/`、`codecs/`、`dsh.toml`、
`serve*.go` 一行没动；v4 日志格式没动；默认仍是 compact，lazy 仍要显式打开。

```
go build ./... && go vet ./...                        通过（BUILD+VET OK）
go test ./cmd/dshd -count=1                           通过（11.9s，含新增 2 条）
go test ./... -count=1                                通过（全部 ok，EXIT=0）
go build -o tmp/dshd.exe ./cmd/dshd                   通过
干净检出：git archive HEAD | tar -x -C /tmp/hc-ls && (cd /tmp/hc-ls && go build ./...)
                                                      CLEAN-CHECKOUT(HEAD) BUILD OK
只带 tracked 文件 + 新测试文件的隔离副本：build/vet/test 全过（不依赖未跟踪文件）
python tmp/lazy-resume-check.py --exe tmp/dshd-before.exe   第二轮 3 次调用 / 1 次挡回
python tmp/lazy-resume-check.py --exe tmp/dshd.exe          第二轮 1 次调用 / 0 次挡回
tmp/lazy-resume-raw3-after.txt                              同一会话第 3 轮：1 次调用 / 0 次挡回
```

`TestLazy*` 八条：上面六条 + `TestLazyLoadedSetSurvivesNewProcess`（跨"进程"回放后直接可用）、
`TestLazyRestoreFallsBackToUnloaded`（状态坏了回到未装载、自愈路径还在）。
反证见 §6.1 证据 2 最后一条。

**没验的**：serve 那条真机链（本轮不许改 `serve*.go`，`serve` 的 turn 走的是
`modelRunner`/`loopRunner`，不是 `cmdRun`）——所以"每轮一个新进程"是用
`dshd run --session` 两轮模拟的，不是 serve 自己跑出来的。
