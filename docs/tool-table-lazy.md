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

## 6. 三个边界（都是真跑，不是推演）

- **跨轮次（resume）装载状态不保留**：`dshd run` 每轮一个新进程，装载集在内存里。
  第二轮模型凭上一轮记忆直接喊 `now_line` → 被挡回
  （`← now_line [ERR] Error: 工具 now_line 没有装载`）→ 它读 Hint → `tool_load` → 再调成功。
  **一次往返内自愈**，代价是每轮 +1 次调用（serve 每轮起一个子进程，这条在真机上一直成立）。
- **步数预算要 +1**：`--max-steps 2` 时 compact 正常收敛，lazy 把"取用"那步挤掉，
  以 `turn 用满了 2 步仍未收敛` 结束；`--max-steps 3` 两边都正常。
  serve 默认 6 步（`cmd/dshd/serve.go:106`），本次最长的任务 compact 3 步 / lazy 4 步，
  6 步还够；但要真上 serve，得先给这一格留余量。
- **绕道挡不住**：挡回只挡"表外工具名"，挡不住"换个工具把事办了"（`pwsh date`）。
  这类绕道不丢正确率，只丢这次的工具设计意图。

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
