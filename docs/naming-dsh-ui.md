# 界面仓要改的：统合到「DSH GUI GO」

> **2026-11-04 用户定的口径（原话）**：
>
> > 「exe 要写产品名，而且要全程，代码里面无所谓。因为 exe 是面向用户的。
> >  就像 DSH 官方也是全程，而且还分大小写」
>
> 官方参照物：`D:\DeepSeek Harness\Desktop\DeepSeek Harness.exe` —— 全名、带空格、分大小写，
> 任务管理器里就叫 `DeepSeek Harness`。所以三条线的产物是：
>
> | 角色 | 产品名 | exe 文件名 |
> |---|---|---|
> | 宿主 | `DSH GO` | `DSH GO.exe` |
> | TUI | `DSH TUI GO` | `DSH TUI GO.exe` |
> | 界面 | `DSH GUI GO` | `DSH GUI GO.exe` |
>
> 换句话说：**先把产品名定下来，exe 就逐字照抄它**。`dshgo.exe` / `dshgogui.exe` / `dshtui.exe`
> 这类缩写名不算"全程"，要改。代码内部的包名、变量名、函数名随意。



> 这份是**跨仓交接单**，给 `../dsh-ui` 那边执行。本仓（`DSH-GO`）已经把 TUI 侧做完
> （`754105f` 之后、`7b4c5cf M392`），这里是同一件事在界面仓的对应清单。
>
> 起因：任务管理器里 `dshd-ac.exe` / `dshd-goal.exe` / `dshd-term.exe` / `app.exe` / `mockd.exe`
> 混在一起、名字对不上人话；用户要求"统合到应用名称下，不要散乱"。

## 0. 已经不用做的

`b4f6b32 四条验收脚本补上进程回收（孤儿宿主的来源）` 已经把**进程回收**做了。
本仓另外补的两条，界面仓可以按需跟：

| 项 | 本仓做法 | 界面仓要不要跟 |
|---|---|---|
| 父进程看门狗（`serve` 在父进程死后自己退） | `cmd/dshd/parentwatch*.go`，三个入口共用 | **建议跟**：脚本漏了 `finally` 的那条路仍然存在（Ctrl+C / 崩溃），看门狗是最后一道 |
| 统一的子进程管理器 | `scripts/_proc.py`（连进程树一起收） | **建议跟**：`app.exe` 会再起 `dshd`，只杀父会留孙子 |

## 1. 命名映射表

| 位置 | 现在 | 改成 | 判据 |
|---|---|---|---|
| 产品名（文档/关于/版本行） | `dsh-ui` | **`DSH GUI GO`** | `grep -rn "DSH GUI GO"` 命中，且文档首行是它 |
| 窗口标题**默认值** | `"dsh-ui"`（`cmd/app/main.go:1894` 的 flag default） | `"DSH GUI GO"` | 起一次 `app.exe`，`Get-Process` 的 `MainWindowTitle` 是 `DSH GUI GO` |
| 内核名 | `uikernel.New("dsh-ui")`（`cmd/app/main.go:1987`） | `uikernel.New("DSH GUI GO")` | 内核里报错/日志带的是新名 |
| 无头截图按窗定位 | `waitOwnWindow("dsh-ui", …)` / `captureWindow("dsh-ui", …)`（`cmd/app/headless_windows.go:377,391`） | 跟着标题默认值走（**别写死**，改成引用同一个常量） | `go test ./cmd/app -run Headless` 全绿 |
| 可执行文件名 | `app.exe` | **`dsh-gui.exe`**（`--title` 仍可覆盖窗口标题，用于并行多实例） | 构建脚本产物里是 `dsh-gui.exe`；`--title` 覆盖仍生效 |
| Windows 版本信息 | 无 | VERSIONINFO 资源：`ProductName=DSH GUI GO`、`FileDescription`、`CompanyName` | 任务管理器"描述"列不是空 |
| `--version` | 无 | 加一个，只读打印 `DSH GUI GO <版本>` 然后退出 | `dsh-gui.exe --version` 输出一行 |

## 2. 具体改动点（照抄即可）

```
cmd/app/main.go:1894   title := flag.String("title", "dsh-ui", ...)          → 默认值改成常量
cmd/app/main.go:1987   m.kernel = uikernel.New("dsh-ui")                     → 同一个常量
cmd/app/headless_windows.go:377,391   waitOwnWindow("dsh-ui", ...) 等        → 引用常量，不写字面量
scripts/accept-all.sh:77 / accept-session-ops.sh:40 / accept-theme.sh:48 /
e2e-workday.sh:129                    go build -o app.exe ./cmd/app          → dsh-gui.exe
README.md / AGENTS.md / PLAN.md 首段                                          → 产品名统一
```

**一根常量**：新建 `cmd/app/brand.go`（对照本仓 `cmd/dshtui/brand.go`）：

```go
const productName = "DSH GUI GO"
const binaryName  = "dsh-gui"
```

窗口标题、内核名、无头定位**全部**引用它——这样以后改名只改一处，不会出现"标题改了、无头截图还在找旧名字"。

## 3. 验收（逐条给证据，别只说"改了"）

```bash
cd ../dsh-ui
go build ./... && go vet ./...
go test ./... -count=1
grep -rn '"dsh-ui"' --include=*.go . | grep -v _test      # 期望：0 行（或只剩注释里的历史说明）
./tmp/dsh-gui.exe --version                                # 期望：DSH GUI GO <版本>
# 真机：起一次 app，看窗口标题
powershell -NoProfile -Command "Get-Process dsh-gui | Select-Object MainWindowTitle"
# 进程表：只应该看到 dsh-gui.exe（界面）与 dshd.exe（宿主），不再有 acc/goal/term 这类临时名
```

**"不再有临时名"是这条的硬判据**：`dshd-acc.exe` / `dshd-goal.exe` / `dshd-term.exe` 这种
"每个脚本各 `go build -o` 一个名字"的做法要收掉——统一 `dshd.exe`（用 `--home` 区分实例），
或者用 `scripts/_proc.py` 那样的统一管理器，让脚本不再自己命名二进制。

## 4. 为什么值得做（别把它当纯审美）

- **任务管理器是唯一的现场**。出问题时人第一眼看到的就是那一列名字；`dshd-acc.exe` ×28
  与 `dshd.exe` 混在一起，人分不清哪个是界面、哪个是宿主、哪个是昨天漏下来的。
- **名字对不上 → 排错对不上**。昨天那次 34 个进程，我先花了一轮才确认它们全来自 `dsh-ui\tmp\`。
  如果它们都叫 `dshd.exe` 而 `--home` 不同，我一条 `Get-CimInstance CommandLine` 就定位了。
- **Windows 上"描述"列空白 = 看起来像可疑进程**。同一份二进制加上 VERSIONINFO，
  用户对它的观感完全不同。

## 5. 边界

- 本仓不改 `../dsh-ui` 的任何文件（跨仓 push 要用户当次点头）。
- 界面仓改完，本仓这边不需要跟着改——两边只共享 `docs/transport-contract.md` 的契约，
  不共享命名。
