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
