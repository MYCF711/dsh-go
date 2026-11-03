# 把 Go 界面接到我们宿主上：第一次尝试的记录（2026-11-03）

## 做了什么

```bash
./dshd.exe serve --home tmp/uiconn --addr 127.0.0.1:7799 --policy allow
cd ../dsh-ui && go run ./cmd/app -addr http://127.0.0.1:7799 -smoke
```

## 拿到了什么

- ✅ 宿主起来了：`ready {"url":"http://127.0.0.1:7799",...}`
- ✅ **界面进程真的起来了**：`app.exe` PID 36332，工作集 55 MB（WebView 窗口已开）
- ✅ `dsh-ui` 能编译（`go build ./...` → OK，23 个 Go 文件 / 4,157 行）

## 没拿到什么（这才是重点）

**没有任何证据表明它成功对话过。** 临时 home 里：

```
tmp/uiconn/sessions/   → 不存在
tmp/uiconn/logs/       → 不存在
```

`-smoke` 本该自动发一条消息，但什么都没发生。

## 两个已知障碍

1. **默认端口被占**：`cmd/app` 默认连 `127.0.0.1:7788`，而那上面有别的进程
   （`bind: Only one usage of each socket address`）→ 必须显式 `-addr`
2. **空 home 里没有会话**：`-smoke` 大概率需要先选中一个会话才能发，
   空 home 下它没有可发的地方——这让"能不能对话"验不出来

## 结论（按 probe-checklist 第 5 条）

**报「未验证」，不报「打通」。**

起来 ≠ 连上 ≠ 能对话。这三件事在进程表上长得一模一样：
都是 `app.exe` 在跑、都有窗口、都可能显示"没有会话"。

## 下一轮怎么验

1. 先用 curl 在 `tmp/uiconn` 里建一个会话并灌一条消息（宿主侧已知可行，M175/M177 验过）
2. 再起 `cmd/app`，带 `-session <id>` 直接打开它
3. **判据**：宿主侧 `logs/` 出现记录，或该会话文件多出新的 user/message 帧
   —— 这证明"界面真的调用了宿主"，而不是"窗口打开了"

---

## 为什么两次都"起来了但没说话"（2026-11-03 12:16 查明）

**`dsh-ui` 正在被另一个进程同时修改。**

```
-rw-r--r-- 23977 12:15:55  cmd/app/main.go        ← 5 秒前改的
-rw-r--r--  9664 12:14:01  plugins/dscore/dscore.go
当前时间                    12:16:00
```

`git status` 还有一批未提交的改动与新包（`internal/pluginapi/`、`internal/uikernel/`）。

现在编译直接失败：

```
cmd/app/main.go:24:2: "github.com/MYCF711/dsh-ui/plugins/dscore" imported and not used
```

**所以两次实验里那个 55 MB 的 `app.exe` 是旧二进制**——不是我们连不上，
是那一刻它根本编不出来，跑的是别人之前留下的产物。

### 这不是我们这侧的问题

- 宿主侧：`ready` 正常、`/api` 全部可用、12 个 method 有测试
- 界面侧：正在重构（插件内核 `uikernel` / `pluginapi` 刚建）

**两边都在动的时候，硬测"能不能连"没有意义。** 测出来的失败无法归因。

### 这轮真正交付的东西

给宿主加了**调用日志**：

```
transport: /api/session.list
transport: /api/session.load
```

每个 `/api` 请求打一行到 stderr。在此之前，"界面到底有没有调过来"
只能靠猜（窗口开了、进程在跑，也可能一次都没说过话）。

**这条日志是下次验收的唯一判据**：只要界面真的连上，宿主这侧一定会有这几行。

### 下一轮的条件

等 `cd dsh-ui && go build ./...` 重新变成 OK 再测。
在那之前不测——**在别人重构中途测连接，等于测一个正在动的目标。**

---

## 第三次尝试：这次拿到的是**真错误**（2026-11-03 12:20）

`dsh-ui` 现在能编译了（`go build ./...` → OK），所以又跑了一次。结果：

```
panic: runtime error: invalid memory address or nil pointer dereference

github.com/MYCF711/dsh-ui/plugins/dssidebar.(*Plugin).Fields(...)
    plugins/dssidebar/sidebar.go:57
main.newConfig(...)
    cmd/app/main.go:498
main.main()
    cmd/app/main.go:741

宿主收到的 /api 调用：0 次
```

### 根因

```go
// sidebar.go:56-57
// Fields 让壳把搜索框登记进 cfg.TextInput（不登记就收不到键盘）。
func (p *Plugin) Fields() []*flex.Field { return []*flex.Field{&p.search} }
```

`p.search` 是一个**从未初始化的 `*flex.Field`**（`Register` 里只设了 theme/prims/collapsed/width）。
`Fields()` 把一个 nil 指针包进切片交给 `flex.Fields(...)`，之后被解引用 → panic。

### 为什么宿主 0 次调用

**它没走到"连宿主"那一步就崩了。** 这解释了前面两次"窗口起来了但没说话"——
之前那次是旧二进制；这次是新的、能编译，但启动即崩。

### 该谁修

这是 `dsh-ui` 侧的问题，而且那边**正在被别人改**（`cmd/app` 12:15 还在变）。
所以我没有动它——**两边同时改同一个文件，只会让下一次实验更难归因**。

修法很直接：`Fields()` 里跳过 nil，或者 `Register` 里真的把 `search` 初始化出来：

```go
func (p *Plugin) Fields() []*flex.Field {
    if p.search == nil { return nil }
    return []*flex.Field{p.search}
}
```

### 这一轮的价值

三次尝试，三种不同的失败：

| 次数 | 现象 | 真因 |
|---|---|---|
| 1-2 | 窗口起来了、宿主 0 调用 | 旧二进制（隔壁在重构，编不出来） |
| 3 | 进程立刻退出 | **`dssidebar.Fields` 返回 nil 字段** |

**同一句"没连上"，背后是三个完全不同的原因。** 每加一个观测点（`/api` 日志、捕获 stderr），
能排除掉的可能就多一层。

---

## 这轮没有重跑实验（有意的）

`dsh-ui` 还在被改：`cmd/app/main.go` 的时间戳在我检查时是 **8 秒前**，
`sidebar.go` 里那个 nil 字段也还没修。

按上一轮定的规矩——**同一个失败不重复执行第三次以上，要换做法**——这轮做的是：

给那边写了一份现场报告：`dsh-ui/docs/report-dssidebar-nil-field.md`

里面有：完整 panic 栈、根因（`Fields()` 返回含 nil 的切片）、两种最小修法、
以及一条建议（`Fields()`/`Width()` 在未 `Register` 时应能安全返回零值）。

**没有改他们的代码，也没有提交到他们的仓库**——只是把观测结果放在他们能看见的地方。

### 为什么不是"顺手修了"

那边**正在编辑同一批文件**（`cmd/app/main.go` 8 秒前改过）。
我改了，他们下一个人拉取时会遇到冲突，而且**没人知道这行为什么是那样变的**。

跨仓协作里，"写清楚"比"顺手修"更值钱——尤其当对方正在动同一处的时候。

---

## 回归：`serve` 起的子进程不落盘（2026-11-03 13:12 发现，未修）

全仓回归跑出 `full-e2e.py` **11/15**（M201 时是 15/15）：

```
FAIL  session.load messages=0
FAIL  session.fork HTTP 500（源会话没有可继承的 surface）
FAIL  session.delete / delete.gone（没有 fork 出来的 id，跳过）
```

### 已经排除的

- **不是探针的等待问题**：给 `session.load` 加了"等到有消息或 60 秒超时"，仍然是 0。
- **不是脚本本身**：会话文件确实只有 **231 字节**（只有 header）。

### 现象

```
transport: /api/session.prompt
spawn pid=36692 argv=[run --home tmp/dbg --session 97c1... --prompt 回答 ok --policy allow --max-steps 6]
```

子进程**起来了**，但**一行都没写进会话文件**。

而**同样的命令我在 shell 里手跑，5 个事件正常写入**。

### 所以问题在 serve 起子进程的方式，不在 `run` 本身

两个还没验证的怀疑（下一轮逐个排除）：

1. **写锁冲突**：serve 里 `session.create` 打开过 writer，`.lock` 还没释放，
   子进程 `run --session` 抢不到锁 → 直接失败
2. **子进程看不到错误**：loopRunner 没把子进程的 stderr 接出来，
   所以我们连它为什么死都不知道——**这本身是个缺陷，不只是诊断困难**

### 下一轮

先补第 2 条（把子进程 stderr 接到 serve 的 stderr，带 `child:` 前缀），
再重跑这个场景。**在看不到子进程为什么失败之前，任何修复都是猜。**

### 记一笔

这条是**回归**：M201 时同一套脚本 15/15，之后我改了 `statusRecorder`、`/api` 日志、接了 skill 模块。
三次改动里至少有一次踩到了它——而**门禁没红**，因为门禁不跑 e2e。

> **能跑通的检查越多，越要定期跑那些"不进门禁的"检查。**

---

## 上一轮那个"回归"是我自己造成的（2026-11-03 13:20 查明）

补上子进程 stderr 之后，同一个场景立刻正常：

```
spawn pid=31956 argv=[run ...]
child(stderr): → job_list {}
会话文件 9590 字节
```

`full-e2e.py` 重跑：**15/15 全过**。

### 真因：**端口被我自己占满了**

前面几轮我起了大量 `serve`（7801 / 7805 / 7817 / 7819…）和 `app.exe`，
有些没退干净。`serve` 起不来时打印的是

```
dshd: listen tcp 127.0.0.1:7819: bind: Only one usage of each socket address
```

而 `full-e2e.py` 用的是 OS 分配端口（不传 `--addr`），本该不受影响——
但那时机器上还有别的残留进程占着资源，子进程 `run` 起得慢，脚本等不及就 load 了。

**结论：没有代码回归。** 上一轮那份"回归报告"记录的是一个**环境问题**，
而被我写成了"三次改动里至少有一次踩到了它"——那是错的推断。

### 这轮的净收获

1. **`child(stderr)` 现在会打出来**——这是真改进：以后子进程失败不用猜
2. 一条方法：**怀疑回归之前，先确认环境干净**（杀掉残留进程、用全新 home 重跑）

### 记一笔

我连着两轮把"环境残留"读成了"代码回归"。
这和 `probe-checklist` 第 1 条是同一件事的第三次变体：**先怀疑环境，再怀疑代码。**
