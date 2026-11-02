# 边角情况盘点（2026-11-03）

拿"最容易被忽略的输入"去打，看哪些是真 bug、哪些本来就对。
**已经对的也要写下来**——不然下一个人会重打一遍。

## 已经验过、本来就对的

| 情况 | 怎么打的 | 结果 |
|---|---|---|
| **崩溃恢复** | `run` 跑到一半 `kill -9` | 日志可读：5 事件 / 6 帧 / surface 3 节点 ✅ |
| **同会话并发 prompt** | 两条 `session/prompt` 同时发 | 一条 `该会话已有在飞的 prompt`，另一条正常跑完 ✅ |
| **超长单行**（1MB 一行） | `read` 一个 1,000,000 字节的单行文件 | 走外置：`spilled:true` + artifact 指针 + 头尾预览 ✅ |
| **Windows junction** | `mklink /J` 指向上级目录 | 遍历不跟进去，只报 1 个文件 ✅ |

四条里没有一条需要改代码。这本身就是结论：**这四类边角是稳的。**

## 一个假警报，以及它为什么值得记

`ln -s ..` 之后 `glob` 报出三个 `a.txt`：

```
sub/a.txt
sub/deeper/up/sub/a.txt
sub/loop/sub/a.txt
```

第一反应是"遍历跟了符号链接"。查下去发现：**Git Bash 的 `ln -s` 在没有符号链接权限时会静默降级成"复制"**，
磁盘上真的有三份文件。`os.Lstat` 报的是普通目录、`os.Readlink` 说"不是重解析点"。

换成真的 `mklink /J` 之后，`os.Lstat` 报的 mode 是 `?rw-rw-rw-`（既不是普通文件也不是目录），
我们的遍历本来就只收 `d.Type().IsRegular()` 的条目——**本来就跳过**。

教训：**先确认测试夹具造出来的东西到底是什么**，再判断产品有没有 bug。
这一次如果直接"修"，会往 `walk.go` 里加一段永远不生效的代码。

## 还没打的

| 情况 | 为什么重要 | 现状 |
|---|---|---|
| 磁盘满 / 只读 | 写日志失败会怎样——回滚？静默丢事件？ | ✅ 见「写不下去的时候」（已测：报错、历史不坏、不留锁） |
| 会话文件被人为截断 | 读的时候是报错还是读半截 | 未测 |
| 极深目录（>100 层） | Windows MAX_PATH | ✅ 60 层 / 1193 字符，glob 正常命中 |
| 同一 workspace 两个宿主进程 | 后写的会不会覆盖前面的帧 | ✅ 见下：写侧不丢帧；**同一会话不允许两个写入者** |
| 模型输出非法 tool_call JSON | 现在是忽略还是报错 | 未测 |

## 两个宿主同时写一个会话（2026-11-03）

`TestTwoWritersDoNotLoseFrames`：两个 writer 各追加 20 帧。

```
期望 40 条事件，实际 40 条（每个 writer 的 0..19 都在）
```

底层是 `O_APPEND`，单个 `Write` 的原子性保证了帧不会互相覆盖。
**这条本来就对，不用加锁。** 顺带确认：编码器里没有"失败回滚到原长度"那种逻辑——
所以不存在"A 回滚时把 B 的帧截掉"的风险。

## 尾部半帧：原来会把整份历史泼掉（已修）

断电/磁盘满会留下**半帧**。原来的行为：

```
$ dshd session info --in <被砍掉 6 字节的会话>
...session.v4.jsonl.zstd: 偏移 458 处块头被截断      ← 整份日志读不出来
```

**为了最后半帧丢掉前面所有事件，是最坏的结果。** 而这条行为当初是**故意**的
（`TestCorruptTailIsReported` 写着"截断的文件必须报错，而不是静默少读几条"）——
担心的是"静默地把坏文件当成完整文件用"。

两个目标可以同时满足：**留住前面的，并且大声说出来**。

```
事件: 4（5 帧）
⚠ 尾部截断: 偏移 6474 之后读不动了（上面的内容照常可用）
surface: 3 节点，其中摘要 0
```

实现上分两层：

- `splitFrames` 不再对"字节不够"报错，而是返回 `(frames, truncatedAt, nil)`；
  **真损坏**（魔数不对、保留块类型）仍然报错——那是文件真的坏了，不是写到一半。
- `v4.Log.TailTruncatedAt` 把位置带给调用方；`session info` 打警告。

测试也改了：`TestCorruptTailIsReported` 从"必须报错"改成
"**必须返回前缀 + 必须报出截断**"——契约变了，测试要跟着变，但不能变成静默。

## 模型发出坏掉的 tool_call（2026-11-03）

三种形态：参数不是 JSON、工具名不存在、参数缺字段。

**工具名不存在**和**缺字段**本来就处理得对（`unknown tool "x"` + Hint、
`file_path 为空` + Hint）。**参数不是 JSON 这条原来直接透传给工具**：

- 大多数工具自己 `json.Unmarshal` 会失败 → 报错；
- 但**不校验参数的工具会拿着垃圾照跑**（`TestMalformedToolCalls` 里的假工具就返回了 "ok"）。

修法是在流水线入口拦一次，而不是让每个工具各写一遍：

```
执行失败: 参数不是 JSON 对象: json: cannot unmarshal string into Go value of type map[string]jsontext.Value
提示: tool_call.arguments 必须是 JSON 对象（键值对）；常见原因是引号没转义或写成了字符串
```

同时确认：坏调用**不会把 turn 搞崩**——三条坏调用之后模型仍能拿到结果继续
（`TestMalformedToolCallsKeepTheTurnAlive`），每条 tool 消息都有正文可读。

## 又一次"是夹具的问题"（语料测试）

`TestSurfaceRealCorpus` 突然红了：某个会话「有 1 个替身来源却没有任何可见摘要」。

查下去发现那条消息的正文在**讨论** `compact-checkpoint` 这个协议（第 10,340 字符处），
而它的 `source.kind` 是 `plugin:hindsight`。测试用**子串**判断替身，于是把一句
"我在说明这个协议"误算成"这是一份压缩替身"。

改成解析 `source.kind == "compact-checkpoint"` 之后通过。

顺带确认这不是我的改动引起的：`git stash` 之后同一条测试照样红——
**真实语料每天都在长大，测试会随新数据变红**，这是"拿真数据当夹具"的代价，
也是它的价值。

## 极深目录与不可写 home（2026-11-03）

### 极深目录：没问题

60 层、路径长 1,193 字符（远超 260 的 MAX_PATH）：

```
$ glob {"pattern":"**/deep.txt","path":"tmp/deep"}
tmp/deep 下匹配 **/deep.txt：1 个文件（扫过 1）
```

Go 在 Windows 上用 `\?\` 长路径前缀，本来就不受 260 限制。**这一类不用管。**

### 不可写 home：报错说不清

`--home` 指到一个**普通文件**上：

```
修前: dshd: 建会话目录失败: mkdir tmpfile: The system cannot find the path specified.
修后: dshd: --home "tmp/afile" 是一个文件，不是目录；换一个目录路径（或先删掉它）
```

修前的原文三个问题：说的是 Windows 的 `mkdir` 内部错误、指到的是**父目录**不是用户给的那个
（会让人以为自己参数没错）、没说要干什么。

现在是**建任何东西之前**先看一眼（`checkHome`），`run` / `acp` 两条入口都过它。
`session info` 没有 `--home` 旗标，不受影响。

`TestCheckHomeRejectsFile` / `TestCheckHomeAllowsMissingAndDir` 钉住：
文件要拒、目录要放行、**不存在的路径不在这里拦**（那是创建阶段的事，提前拦会把
"第一次跑"误判成错误）。

## 同一份会话被两个进程同时 resume（2026-11-03）

写侧早先验过：两个 writer 各 20 帧，`O_APPEND` 保证一帧不丢。

但**会话层**还有一个坑：`OpenSession` 是"读到 N 条 → 从 N 接着编号"。
两个进程都读到 100 条，就各自从 100 开始写——

```
实测：9 条事件里出现 3 个重复 seq
```

`seq` 是 surface 折叠与压缩遮蔽的排序依据。重复意味着**顺序不确定**：
同一条 `compaction/summary` 遮蔽哪个区间，会随读取顺序漂移。
这比"打不开"糟糕得多，因为它是**静默的**。

修法：**同一时刻只允许一个写入者**（`<会话文件>.lock`，`O_EXCL` 抢）。

```
$ 进程 A resume → ok
$ 进程 B resume → 恢复失败: 会话正被另一个进程写入（锁文件 <path>.lock）；
                  确认没有别的 dshd 在用这份会话后删掉它再 resume
```

`Close()` 会删锁。崩溃留下的僵尸锁由人多删一次——**宁可让人删一次，
也不要悄悄写出一份顺序可疑的历史**。

`TestSecondWriterIsRefused` 钉住三件事：第二个写入者被拒且说清原因、
关掉之后能续写、整份日志里**没有重复 seq**。

## 写不下去的时候（2026-11-03）

盘满、文件被设只读、句柄被拔——写不进去时会发生什么。分三种情况测。

### ① 句柄已经关了还写（最容易被静默吞掉）

```
TestAppendFailsLoudlyAndKeepsHistory：
  写第 4 条 → 报错「writer 已关闭」      ← 必须报错，不能假装成功
  回读 → 仍然 3 条，没有半帧（TailTruncatedAt=0）
  重开接着写 → 第 4 条落盘，**没有重复 seq**
```

**结论：本来就对。**

### ② 会话文件变成只读后 resume

修前：

```
恢复失败: open …\session.v4.jsonl.zstd: Access is denied.
```

这句话没说清三件最该知道的事：**文件其实是能读的**（不是坏了）、是**打不开写入**、
**已有内容不受影响**。修后：

```
恢复失败: 会话能读、但打不开写入（…session.v4.jsonl.zstd）：… Access is denied.；
文件只读/被占用/磁盘满都会这样。已有 5 条事件不受影响，可以先用 `dshd session info --in` 查看
```

同时确认：**失败后不留锁**（`TestReadOnlySessionExplainsItself` 钉住），
恢复权限后 `session info` 照样读出 5 条。

### ③ 夹具又一次差点骗了我

第一次测的时候 `attrib +R` 的 sed 转义写错了，命令根本没执行，
于是"只读文件 resume 成功"——差一点就得出**相反**的结论。
换成 `os.chmod(path, 0o444)` 并**先验证夹具生效**（python 自己写一次失败）才继续。

> 判据再确认一次：**动手之前，先证明夹具真的造出了那个状态。**
> 这是这个仓库里第 7 次踩同类问题（前 6 次见 `docs/doctor.md` 与上面的记录）。
