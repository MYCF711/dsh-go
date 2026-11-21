# 跑 `-race`（本机第一次真正跑起来）

> 2026-11-03 之前，这个仓库的 `-race` **从来没有成功跑过一次**：
> `CGO_ENABLED=0`，`gcc` / `cc` / `clang` / `cl` 一个都不存在。
> AGENTS §5 写着「`-race` 未实际成功就是未验证」——所以那是一条挂了很久的未验证。

## 现在怎么跑

```bash
bash scripts/race.sh              # 全仓
bash scripts/race.sh ./internal/transport   # 单个包
```

脚本会自己把编译器加进 PATH、设 `CGO_ENABLED=1`。

## 工具链是从哪来的

- 包：winlibs MinGW-w64 **GCC 16.2.0**（UCRT，posix-seh），解压即用，不装系统组件
- 位置：`C:/toolchains/mingw64`（可用 `DSH_MINGW` 覆盖）
- 下载：GitHub release 直连下载会卡死（`HTTP 000`），走镜像
  `https://ghfast.top/https://github.com/...` 才拿到（276 MB）

## 两个坑，都值得记

**① 路径不能有空格。**
放在 `D:/DeepSeek Harness/default-workspace/DSH-GO/tmp/toolchain/` 里时，链接器报：

```
ld.exe: cannot find D:/DeepSeek: Permission denied
```

原因是 Go 调 `gcc` 时把带空格的路径原样传给了 `ld`，`ld` 把它切成了两个参数。
**换到 `C:/toolchains/mingw64` 就好了。** 这不是 Go 的 bug，是路径选择的坑。

**② 第一次跑 `-race` 就抓出真竞争——在我自己写的测试夹具里。**

```
WARNING: DATA RACE
Write at ... by goroutine 101: (*fakeStdin).Write
Previous read at ... by goroutine 100: TestBrokerResolveWritesAllowBack
```

`fakeStdin.Write` 在 broker 的 goroutine 里被调用，测试在主 goroutine 里读
`stdin.lines` / `emitted` / `asked`——**三个都是裸变量**。
产品代码没问题（broker 自己是加了锁的），但**夹具的竞争会淹没真正的竞争**，
所以必须一起清掉：现在 `fakeStdin` 带锁，另有两个小工具类型 `emitLog` / `counter`。

清完之后：

```
go test -race ./...   39 个包全 ok，0 DATA RACE
```

## 它不在成品里

这条很重要，别搞混：

| | 成品（`DSH GO.exe`） | 开发脚本 |
|---|---|---|
| C 工具链 | **不需要，也不包含** | `scripts/race.sh` 自己找 |
| 构建 | `CGO_ENABLED=0`（默认） | `CGO_ENABLED=1`，只在那条命令里 |
| 体积 | 14.5 MB，`mingw` 字符串 0 处 | 工具链在 `C:/toolchains/mingw64`，仓外 |

验证方式（2026-11-03 实测）：

```bash
env -u CGO_ENABLED go build -o tmp/dshd-plain.exe ./cmd/dshd   # 成功
strings tmp/dshd-plain.exe | grep -ci mingw                    # 0
grep -rn "toolchains\|mingw" --include=*.go cmd/ internal/ modules/ codecs/   # 无输出
```

`h-suite.py` 里它也是**默认关闭**的：不加 `--race` 就只打印一行"跳过"。

## 成品里没有它

`-race` 变绿**不等于并发就对了**：它只覆盖被跑到的那些交错。
`TestConcurrentJournalAndHubInvariants` 仍然有用（它专门去撞 journal/hub 的交错），
两者是补充关系，不是替代关系。

## check.sh 的 race 步：输出落到文件 + 整步硬预算（2026-11-21）

这条门禁自身有两个洞，都修了：

**① 不许用 `$( )` 捕获这一步。** 命令替换要等管道写端**全部**关闭才 EOF；
`go test` 会起 test 二进制，test 二进制再起 dshd / helper，只要有一个后代进程
继承着那个写端还活着，bash 就永远等不到 EOF —— 整条门禁停在 race 这一行，
**后面 20 多道守卫一条都跑不到**（PR #104 因此漏掉 text-only 白名单漂移，靠 CI 才捞住）。
现在由 `scripts/race-step.sh` 来跑：子进程输出**重定向到文件**，bash 只等直接子进程。

**② 整步必须有预算。** `go test -timeout 30m` 只约束**单个包**，"整个 `./...`
要跑多久"原来没有任何上限，所以"卡住"只能是无限等。现在默认 **2700s**
（`DSHGO_RACE_BUDGET=<秒>` 可改），到点先按 Windows 进程树 `taskkill /T` 收掉
go → `*.test.exe` → dshd/helper（**只收我们自己这棵树**），在日志里留一行
"预算打满 + 当时哪个 test 二进制还活着"，然后**继续跑后面的守卫**。
预算不是断言：到点照样是 FAIL，只是不再是"无限等"。

判定口径：`race-step.sh` 退出码 **124 = 预算打满（这一步没跑完，不是通过）**，
其它非零 = 命令自己失败（cgo 缺失、DATA RACE、断言失败等，仍按原逻辑判）。

**顺带修掉一个真实的耗时源**：`codecs/v4` 的 `TestReadRealCorpus` 原来把
`$DSH_HOME/sessions` **全量**读一遍（本机 4944 个文件 / 3.6 GB，`-race` 下实测按
200 文件 25.9s 外推 ≈ 14 分钟），它一个包就顶掉整套 race 的大半时间，机器上再有
别的任务占着就会撞 30m 包超时。现在按路径字典序**等距取样 20 个文件 / 24 MB 上限**
（单文件 > 8 MB 跳过并报数），日志里打"语料总量 vs 本次读了多少"；
要全量深跑设 `DSHGO_REAL_CORPUS_ALL=1`。
