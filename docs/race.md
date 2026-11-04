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
