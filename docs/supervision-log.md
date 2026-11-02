# 监工日志：让 dshd 自己开发自己

> 2026-11-03 起。Lead 只写任务书、验收、修它的错；实现由 dshd 自己做。
> 每条记「派了什么 → 它交付了什么 → 错在哪 → 怎么收的」。

## 派活 1：统计真实语料的工具频率

**结果：部分成功。**

它自己写了一个 `tmp/toolfreq.mjs`，用 `node:zlib` 解了 171 个真实 v4 会话，
数出 **33,408 次 tool/call、60 种工具**。这是真数据，不是抄来的。

**它错在**：第 14 步就放弃了写 `docs/tool-frequency.md`，撞满步数收工。
数据在脚本里，文档没落盘——**产物和过程没对上**。

**怎么收的**：我把脚本输出直接整理成 `docs/tool-frequency.md`（数据是它的，格式是我的）。

## 派活 2：实现 pwsh 工具（完整任务书）

**结果：失败。**

24 步、30 次工具调用、42 万 token，**一行模块代码都没写**。
全花在读文件、验证 powershell 存不存在、写一个临时 `.go` 试验文件上。

**这是它的真实短板**：任务书里同时给了"读参考实现 + 设计 + 写实现 + 接线 + 测"，
它选择了**无限调研**而不是先动手。步数用完 = 零产出。

## 派活 3：同一个工具，收窄成"只写一个文件"

**结果：成功，且质量超过预期。**

只改一句话就通了：把范围从"实现 pwsh 工具"收窄成"新建 modules/tool/pwsh/pwsh.go 一个文件，
接线我来"。

它交的 269 行里有几处**我没让它做但它做对了**：

- `LookPath("pwsh")` 失败时退回 `powershell`，两者都没有才 fail-loud
- 退回 PowerShell 5.1 时补 `[Console]::OutputEncoding = UTF8` 序言
  ——不加这句，中文输出在 5.1 上全是乱码，这是**只有真跑过才知道的坑**
- 退出码非零时**输出照回**（模型要看报错原文），而不是只回一个 error

**它唯一的错**：`Description` 写了 127 字，超出 120 字上限，`TestEveryToolHasDescription` 报红。
我改成 47 字。

## 这一轮学到的（比代码值钱）

1. **给它一个"改一个文件"的活，它做得很好；给它"做一个功能"，它会无限调研。**
   这不是模型能力问题，是**任务粒度**问题。以后所有派活都按"一个文件 / 一个函数"切。
2. **它不知道仓库的隐性约束**（Description ≤120 字、dsh.toml 要登记、快照要更新）。
   这些必须写在任务书里，或者由监工补。
3 它会在收工时**主动问"需要我接着做吗"**——说明它知道自己只做了一半，这点比闷头交半成品好。

## 当前进度

19 个工具 / 14,911 字节 / **2,656 token**；`pwsh` 真机跑通（PowerShell 7.6.6）。

## 派活 4：github 工具（124 次，第 9 高频）

**结果：一次成。7 次工具调用、64k token、270 行。**

同样的"只写一个文件"粒度，这次比 pwsh 还顺——它甚至主动报告
「Module() 还没被装配，要我接着做吗」，边界守得很干净。

**它做的、我没要求的**：令牌按 GITHUB_TOKEN → GH_TOKEN → `gh auth token` 三级回退；
前导斜杠归一化而不是报错（`/repos/o/r` 和 `repos/o/r` 等价，这比报错友好）。

**它漏的**：没写测试（我明确说了"不许碰测试"），`Description` 又超 120 字（147）。

**我补的**：四个测试（confirm 必须、参数守卫、前导斜杠归一、令牌绝不泄漏）。

## 真机暴露的真问题

api.github.com 在那一刻**持续回 502 且正文为空**。工具原来的 Hint 是
「看状态码与正文里的 message」——正文是空的，这句话等于没说，
模型连试两次后改用 web_fetch。

改成：「5xx：这是 GitHub 那边的问题（正文常常是空的），不是你写错了。
别连着重试——隔一会儿再来，或用 gh CLI 试试是不是同一现象。」

**这条只有当上游真的挂了才会暴露**——单测永远测不出来。

## 派活 5：repo_here 工具（12 次）

**结果：成，但撞满 12 步。**

19 次工具调用交出一个 9.4KB 的文件，`go build` + `go vet` 都过。
撞步数的原因是它写完还要**自己造一个 /tmp/rhcheck 去验证**——比上次 github 那次更谨慎。

**它做的、我没要求的**：remote URL 里带凭据
（`https://user:token@github.com/...`）时**主动打码**再回显。
这是安全问题，我只在任务书里写了"三种 URL 写法"，没提凭据。

**它漏的**：`Description` 又双叒超限（158 字）。**三次派活、三次栽在同一条**——
因为这条约束只存在于仓库的测试里，不在它能看到的任何文档里。

**教训**：任务书里必须写死「Description ≤120 字」，或者干脆由监工收尾时改。

## 派活 6：exec_literal —— 第一次带上仓库手册

**这是最有信息量的一次，因为它证明了我之前一直在坑它。**

前面五次派活我都写了 `--workspace-bytes 0`——这个参数的意思是
**AGENTS.md 不进系统提示**（默认 8000 字节，写 0 = 不传）。
也就是说：我让它在**没有仓库手册**的情况下改仓库，然后怪它
`Description` 超限、不知道 `dsh.toml` 要登记、不知道快照要更新。

**那些约束全写在 AGENTS.md 里。**

A/B 对照（同一类任务）：

| | 带 AGENTS.md | 不带 |
|---|---|---|
| pwsh | — | Description 127 字，测试红 |
| github | — | Description 147 字，测试红 |
| repo_here | — | Description 158 字，测试红 |
| **exec_literal** | **51 字，一次过** | — |

3/3 失败 vs 1/1 通过。差别不在模型，在**有没有给它手册**。

**它这次还纠正了我**：任务书里我写 `Caps{Executes: true}`，
它实现成 `{Writes: true, Executes: true}`——理由是"任意程序都可能写盘"。
这个保守判断比我的对，快照期望得跟着改。

**派活命令从此固定**：

```bash
./tmp/dshd.exe run --home <tmp> --policy allow   --workspace-bytes 41000 \        # 完整 AGENTS.md，别再写 0
  --max-steps 12 --prompt "$(cat <brief 文件>)"
```

任务书正文放文件里用 `$(cat)` 传——直接内联 41KB 手册 + 长任务会撞
`Argument list too long`（这个也踩过一次）。

## 派活 7 之后：给执行者一份瘦手册

用户问：「他不需要的部分就别给，大概是 tools 和 skill 部分？」

对。它不需要 §0 里程碑史、§9 界面线不做清单、§10 主理鱼流程、§11 界面线规程、
research/UIPLAN 那些——那些是**维护者**的上下文，不是**执行者**的。

写了 `docs/WORKER.md`（6 KB，8 节）：

1. 你的产出长什么样（一个目录一个文件）
2. 骨架（照抄，别自创）
3. 八条硬约束表
4. `Hint` 的正确姿势（给三个反例）
5. 自检命令
6. **你已经栽过的坑**（Description ×4、无限调研、越界改文件）← 这节最值钱
7. 代码风格
8. 目录地图（只列它要用的）

配 `--agents docs/WORKER.md` 参数（`internal/prompt` 加了 `ExtraRulesPath`）。

**实测系统提示成本**：16,692 → **4,833 token**，少 71%。

## 但它还是超了一次

派活 8（`now_line`）时 Description 写了 **131 字**——手册里明明写了「≤120」。

原因不是它不听话：**它数不准中文字数**，中文标点也算一个字，靠猜必然偏。

所以加了 `scripts/desc-len.py`：读工具文件、按 rune 数算、超了退出码 1。
手册里也改口：「`desc-len.py` 是给你用的，不是给我的。跑一下它，红了就砍。」

**教训升级**：约束不可执行 = 没有约束。"≤120 字"是要求，`python scripts/desc-len.py X.go`
才是能落地的指令。这跟 `Hint` 的道理是同一条——**给下一步动作，不给形容词**。

## 派活 9：team_task 一族（四个工具，749 行）

**结果：成了，而且第一次主动自检。**

任务书给的是一个文件里放四个工具——这是历来最大的一次单文件任务。
它交的 749 行里有几处设计**比我的任务书更干净**：

- 一个 `Tool` 结构体 + `kind` 字段，`Name()` 返回 `t.kind`，
  `Description()`/`Schema()` 按 kind 分支——**省了三份重复类型**
- 存储用「写 tmp 再 rename」（我要求了），进程内 mutex 串行化（我要求了）
- 生成 id 用 `task-0001-89d1` 形状（我给了形状，它照做）

**最关键的**：它**自己跑了 `python scripts/desc-len.py`**。
前四次连续超限的坑，靠「给一条能跑的命令」治好了——手册里那句
「desc-len.py 是给你用的，不是给我的」起了作用。

**真机验证**（不是看代码，是真跑）：
```
→ team_task_create {"subject":"给 demo 加测试"}   ← [ok]
→ team_task_list {}                                ← [ok]  task-0001-89d1
→ team_task_update {action:"claim", owner:"监工"}  ← [ok]
→ team_task_update {action:"complete"}             ← [ok]
最终状态: completed | owner: 监工
板子文件：<home>/state/team-tasks.json 真的写出来了
```

**监工补的**：接线（main.go import + Module(home/state)、dsh.toml、snapshot caps）。

## 一条方法论

这次没返工的真正原因不是模型变强了，是**三件事同时到位**：

1. 任务粒度：一个文件、四个工具的边界写死
2. 手册：`docs/WORKER.md`（4.8k token）说清了 `Hint`/`Caps`/`Data`/边界
3. **可执行的约束**：`desc-len.py` 而不是「≤120 字」

前两次缺 2，前四次缺 3。

## 派活 10：team 消息（send_message / wait_agent / list_agents）

**它交得不错（452 行），但这次是监工的失误更多。**

### 我漏的基础设施

工具要在相互通信时知道「我是哪个会话」，而 dshd 原来**没有任何机制**把会话 id
传给工具。这不是它的问题——是我的任务书假设了一个不存在的东西。

补了 `spi.WithSession/SessionFrom`，绑在 `agent.Loop.Run` 入口。

### 两次"以为改好了"

1. 第一次给 `Loop` 加 `SessionID` 字段成功，但 `Run` 里注入 ctx 的补丁**没生效**——
   我按 `history` 写的签名，实际是 `hist`，`replace` 静默什么都不做。
   结果真机跑起来还是「缺少会话上下文」。
2. `dsh.toml` 里判断 `'tool-team' not in s` —— 而 `tool-teamtask` 包含这个子串，
   于是**模块根本没登记**，工具数一直是 27 不是 30。

两条都是同一个病：**改完没验证**。后来改成替换后立刻 `assert`/`grep` 回读，才没再犯。

### 对拍测试救了一次

DSH 的真实契约是 `send_message(agent_id, message)`，它按我的任务书写成了
`to`/`text`。`TestToolParityWithDSH` 直接报红——**这个测试的价值就在这**：
参数名错了，模型永远调不到。

### 真机结果

发信通了：
```
A: → send_message {"agent_id":"session-9ff...","message":"..."}   ← [ok]
B 的信箱: {"from":"session-6b68...","message":"...","id":"msg-0001"}
```

收信**没验通**：`session/resume` 按 id 恢复失败，`wait_agent` 的端到端没跑到。
单测过了（`TestSendWritesMailbox` / `TestSendWithoutSessionRejected`），但真机链路缺一环。

## 派活 10 的收尾：收信侧补验

上一轮留下的「`session/resume` 按 id 恢复失败」根因很简单：
ACP 的 handler 要求 **cwd 与 sessionId 都必填**，而 `session.ResolvePath` 本来就有按 id 兜底。

改成只要求 sessionId，补三条测试（只给 id / 给错 cwd / id 不存在），真机验通：

```
A → send_message → B 信箱 {"message":"暗号是蓝鲸七号"}
B: session/resume {"sessionId": "..."}（不给 cwd）→ ok
B: wait_agent → cursor 0 → 1，日志 ok:true
```

**这条修的是能力而不是 bug**：客户端手里常常只有 id（收别人的信、接别人没干完的活），
要求 cwd 就等于要求"你必须知道它当初建在哪个目录"。

