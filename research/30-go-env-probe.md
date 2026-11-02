# 30 · Go 环境探测：Windows + go1.27.3 实测

> 一次性验证工程 `WS/probe/**`（不进主工程，WS 根无 go.mod/go.sum）。环境 `go version go1.27.3 windows/amd64`，GOPROXY=`https://goproxy.cn,direct`，GOMODCACHE=`C:\Users\Administrator\go\pkg\mod`。以下数字全部本机实测。

## 0. 结论速览

| 假设 | 结论 | 证据 |
|---|---|---|
| 11 个依赖能否拿到并编译 | **11/11 全部编译且可运行** | §1 §6.1 |
| go-plugin gRPC 端到端 | **跑通**（自拉起子进程，spawn 63–87ms） | §2 |
| wazero 调 Go 编译的 wasm | **跑通，但必须 `-buildmode=c-shared`** | §3 |
| mvdan.cc/sh POSIX 解释器 | 可用；进程替换在 Windows **未实现且静默** | §4 |
| Go 空进程冷启动 | median **18.10ms** / p95 **22.68ms** | §5.1 |
| wazero+go-plugin 空二进制 | **21,367,296 B (20.4 MiB)**，`-s -w` 后 14,670,848 B | §5.2 |
| 插件子进程内存基线 | 工作集 **14,768 K**（≈14.4 MiB） | §6.2 |

## 1. 依赖版本（实测解析结果）

```
$ cd probe && go mod init probe
$ go get github.com/hashicorp/go-plugin@latest github.com/tetratelabs/wazero@latest \
    github.com/klauspost/compress@latest github.com/BurntSushi/toml@latest \
    mvdan.cc/sh/v3@latest github.com/charmbracelet/bubbletea@latest \
    github.com/charmbracelet/lipgloss@latest github.com/modelcontextprotocol/go-sdk@latest \
    golang.org/x/sync@latest github.com/google/jsonschema-go@latest \
    github.com/coder/websocket@latest google.golang.org/grpc@latest google.golang.org/protobuf@latest
$ go mod tidy && go list -m all | wc -l
94
```

| 模块 | 实测版本 | 备注 |
|---|---|---|
| github.com/hashicorp/go-plugin | **v1.8.0** | 依赖链带进 grpc v1.85.0 |
| github.com/tetratelabs/wazero | **v1.12.0** | |
| github.com/klauspost/compress | **v1.20.3** | 用 zstd 子包 |
| github.com/BurntSushi/toml | **v1.6.0** | 缓存目录转义成 `!burnt!sushi`，按路径找会找不到 |
| mvdan.cc/sh/v3 | **v3.14.1** | |
| github.com/charmbracelet/bubbletea | **v1.3.10** | |
| github.com/charmbracelet/lipgloss | **v1.1.0** | |
| github.com/modelcontextprotocol/go-sdk | **v1.8.0** | |
| golang.org/x/sync | **v0.24.0** | MVS 注意见下 |
| github.com/google/jsonschema-go | **v0.4.3** | |
| github.com/coder/websocket | **v1.8.15** | |
| google.golang.org/grpc（附） | v1.85.0 | go-plugin gRPC 模式必需 |
| google.golang.org/protobuf（附） | v1.36.12 | 用 `structpb` 免掉 protoc |

**MVS 注意**：`go get @latest` 拿到 x/sync v0.24.0，但若暂无代码 import，`tidy` 会把整条 require 删掉；之后新增 import 只会按上游最小要求解析成 **v0.23.0**。显式 `go get golang.org/x/sync@v0.24.0` 后再 `tidy` 仍保留 v0.24.0（已实测两轮）。

## 2. go-plugin gRPC 端到端：跑通

同一份 `probe.exe` 既是 host 也是 plugin（`--probe-plugin` 子模式）；**不用 protoc**——`structpb.Struct` + 手写 `grpc.ServiceDesc`，零生成代码。

```
$ go build -o bin/probe.exe ./cmd/probe && ./bin/probe.exe
proto=grpc negotiated_version=1 spawn_ms=63
rpc_result=map[echo:pong:hello-from-host gover:1.27.3 pid:16420 plugin:internal pluginOs:windows rev:7]
plugin_pid=16420
```
日志侧确认子进程真被拉起并回收：`plugin started: pid=...` → `plugin process exited: id=16420`。

**坑 1 · protocol version 不匹配**（host=2 / plugin=1）：
```
$ ./bin/probe.exe --bad-proto
FAIL client.Client(): incompatible API version with plugin. Plugin version: 1, Client versions: [2]
exit=1
```

**坑 2 · 插件握手前污染 stdout**（插件 `plugin.Serve` 前打一行普通日志即可）：
```
$ ./bin/probe.exe --loud-host
FAIL client.Client(): Unrecognized remote plugin message: THIS LINE CORRUPTS THE HANDSHAKE
This usually means the plugin was not compiled for this architecture / missing DLL / ...
  PE architecture: amd64 (current architecture: amd64)
exit=1
```
→ 报错极具误导性（提示架构/DLL）。DSH 插件侧必须在 `plugin.Serve` 前把 `os.Stdout` 指向 stderr。

**坑 3 · ReattachConfig**：
```
$ ./bin/probe.exe --reattach
c1_echo= pong:first
reattach_config: protocol=grpc pid=32848 addr=127.0.0.1:10000 reattachFunc=false
c2_echo_via_reattach= pong:second same_pid= true
after_kill_echo_err= rpc error: code = Unavailable ... dial tcp 127.0.0.1:10000: connectex: ... actively refused it.
```
v1.8.0 的 `ReattachConfig` **只有 `Protocol/Addr/Pid/ReattachFunc`，没有 `ProtocolVersion`**；`reattachFunc=false` 表示走 `cmdrunner.ReattachFunc(pid, addr)`，跨进程只等于「dial 这个 TCP 地址 + 看住 pid」。进程内复用实测成功（same_pid=true），`Kill()` 后立刻失效。**别指望它跨 DSH 重启复活插件**：插件是原 host 的子进程，host 一退其 stdin 管道关闭，插件自己就退了。

## 3. wazero 端到端：跑通，但构建模式有硬要求

`probe/wasmsrc/main.go`（`//go:build wasip1` + `//go:wasmexport add/mul`）：

```
$ GOOS=wasip1 GOARCH=wasm go build -o bin/probe.wasm ./wasmsrc                     # 1,910,232 B
$ GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o bin/probe_shared.wasm ./wasmsrc  # 1,910,508 B

$ ./bin/wasmprobe.exe bin/probe.wasm          # 默认 command 模块
exports=_rt0_wasm_wasip1 add mul
FAIL add.Call: wasm error: unreachable
wasm stack trace:
	.runtime.abort(i32) i32
	.runtime.notInitialized()
	.add(i32,i32) i32

$ ./bin/wasmprobe.exe bin/probe_shared.wasm   # c-shared reactor
exports=_rt0_wasm_wasip1_lib add mul
called _initialize (reactor)
add(40,2)=42
mul(6,7)=42
wasm_bytes=1910508 compile_ms=320 instantiate_total_ms=320
```

**本轮最重要的 wazero 结论**：默认 `GOOS=wasip1` 产物是 *command* 模块，导出函数**存在但调不动**（`runtime.notInitialized()`）；wazero 默认还会执行 `_start` → `module closed with exit_code(0)`。要当插件用必须 `-buildmode=c-shared`（reactor，带 `_initialize`），wazero 侧还要 `wazero.NewModuleConfig().WithStartFunctions()` 关掉自动 `_start`，再显式调 `_initialize`。注意 `_initialize` **不在** `ExportedFunctions()` 列表里，但 `mod.ExportedFunction("_initialize")` 取得到——别拿导出表反查。

## 4. mvdan.cc/sh/v3 在 Windows（13 例，12 通过）

`interp.New(interp.StdIO(nil,&sb,&sb), interp.Env(expand.ListEnviron(os.Environ()...)), interp.Dir(tmp))`

| 用例 | 实测输出 |
|---|---|
| `echo alpha beta gamma \| tr ' ' '-'` | `alpha-beta-gamma` |
| `cd <tmp> && export FOO=bar-baz && echo "$(pwd) $FOO"` | `C:\Users\ADMINI~1\AppData\Local\Temp\shprobe… FOO=bar-baz` |
| `printf 'line1\nline2\n' > o.txt ; cat o.txt` | `line1` / `line2` |
| `echo second >> o.txt ; wc -l < o.txt` | `3` |
| `x=$((3*7)); echo "arith=$x sub=$(echo hi)"` | `arith=21 sub=hi` |
| `f(){ case "$1" in a*)…esac; }; f abc; if [ 1 -lt 2 ];…` | `matched-a` / `ifok` |
| `[[ abc == a* ]] && echo ok` | `ok`（bash 语法可用） |
| `arr=(x y z); echo "${arr[1]}"` | `y` |
| `set -o pipefail; false \| true; echo $?` | `status=1` |
| `touch g1.txt g2.txt && echo g*.txt` | `g1.txt g2.txt` |
| `cat <(echo from-procsub)` | **`TODO: support process substitution on Windows`** |
| `cd /tmp` | 失败（`/tmp` 在 Windows 解析为当前盘根 `\tmp`） |
| `definitely_not_a_cmd_xyz` | `run: exit status 127` |

局限：**进程替换是静默桩输出**（不报错、把 TODO 字符串当命令输出，最危险）；POSIX 绝对路径不映射；外部命令全靠宿主 PATH——本次从 Git Bash 启动所以 `tr/cat/wc/touch` 命中 Git 的 `usr\bin\*.exe`，若 DSH 由 Windows 桌面直接拉起则全部 `exit status 127`。`[[ ]]`/数组/`$(( ))`/`case`/函数/glob 均可用，比预期强。

## 5. 性能口径（实测）

### 5.1 冷启动（spawn→exit，n=10，`bin/coldstart.exe <exe> 10`）

| 二进制 | median | p95 | mean | min–max |
|---|---|---|---|---|
| `empty.exe`（纯 Go，打印后退出） | **18.10ms** | **22.68ms** | 18.17ms | 14.80–22.68 |
| `sizewazero.exe`（+wazero） | 26.74ms | 37.34ms | 27.24ms | 24.57–37.34 |
| `sizeplugin.exe`（+go-plugin/grpc） | 17.59ms | 38.86ms | 19.78ms | 16.59–38.86 |
| `size.exe`（+两者） | 20.10ms | 25.13ms | 20.64ms | 18.09–25.13 |

口径：n=10 时 p95 按最近秩即**最大值**，只作上界参考；每轮第一发最慢（文件缓存冷）已含在样本内。结论：**依赖多少几乎不影响冷启动**（≈18–27ms 区间），瓶颈是进程创建本身而非二进制体积。

### 5.2 体积

| 二进制 | 原始 | `-ldflags="-s -w"` |
|---|---|---|
| empty（无依赖） | 2,494,464 B (2.4 MiB) | 1,657,344 B (1.6 MiB) |
| +wazero | 6,000,128 B (5.7 MiB) | 3,994,112 B (3.8 MiB) |
| +go-plugin（含 grpc/protobuf） | 17,304,576 B (16.5 MiB) | 11,884,032 B (11.3 MiB) |
| **+两者（任务要的空二进制）** | **21,367,296 B (20.4 MiB)** | **14,670,848 B (14.0 MiB)** |
| probe.exe（host+grpc+structpb，实际可跑） | 20,977,664 B | 14,491,136 B |
| allimports.exe（11 个全塞） | 30,081,024 B (28.7 MiB) | — |

归因：go-plugin ≈ **+14.8 MB**（几乎全来自 grpc+protobuf+genproto），wazero ≈ +3.5 MB。

## 6. 交叉结论

### 6.1 11 个依赖编译/运行情况：11/11 通过

`cmd/allimports/main.go` 对每个依赖真 import 且真跑一段（不是只编译）：

```
$ ./bin/allimports.exe
toml           OK: {Name:dsh Ports:[1 2] Env:[A=1]}
zstd           OK: 6000B -> 25B, roundtrip=6000
jsonschema     OK: type=object props=3
lipgloss       OK: " dsh "
bubbletea      OK: program_ctor_ok=true
websocket      OK: echo:acs
mcp            OK: content=&{{"sum":42} map[] <nil>} isError=false
x/sync         OK: errgroup_done=true
mvdan/sh       OK: stmts=1
wazero         OK: compiled_empty_module_ok=true
go-plugin      OK: protocol_enum=[netrpc grpc]
$ go build ./... && go vet ./cmd/...        # 均 exit=0
```

有坑但都不是编译错误的只有三处：wazero 构建模式（§3）、mvdan 进程替换静默桩（§4）、go-plugin 的三条（§2）。另：bubbletea/lipgloss 在非 TTY（管道/重定向）下不输出颜色，`style.Render("dsh")` 得到 `" dsh "` —— 是 termenv 正常降级不是 Windows 问题，但 TUI 启动要自己校验 `isatty`。

### 6.2 插件子进程内存基线（实测）

```
$ ./bin/probe.exe --hold 15 &          # host 先打印 plugin_pid=51572 再保持连接
$ tasklist //FI "PID eq 51572" //FO CSV //NH
"probe.exe","51572","Console","1","14,768 K"
```
→ **工作集 14,768 K ≈ 14.4 MiB**（已建 gRPC 连接、完成一次 RPC、空业务逻辑的 go-plugin 插件）。
附坑：`tasklist` 默认表头是 CP936，UTF-8 终端下乱码；脚本用 `//FO CSV //NH`，且参数必须写 `//FI`（Git Bash 会把 `/FI` 当路径转换）。

### 6.3 致命坑？没有，但三条必须写进设计

拉起、RPC、Reattach、Kill 四类实测全通过，不判死刑。落地固定三条：① 插件侧 stdout 归 go-plugin 独占，`Serve` 前重定向到 stderr；② 握手 `ProtocolVersion` 与 go-plugin 版本一起进契约，两边同源编译；③ 不要用 `ReattachConfig` 实现「DSH 重启后插件还在」，热复用只能同进程持有 `*plugin.Client`。

若哪天被判死刑，替代与代价（本次未实测，仅作权衡）：**stdio JSON-RPC 自研 ABI** 省掉 grpc+protobuf（体积 -14.8 MB），代价是失去 broker/双向流/健康检查且要自己处理 Windows 管道写阻塞；**只留 wazero 作 ABI** 则进程内零 IPC、体积 +3.5 MB，代价是 §3 的 reactor 限制传染给每个插件。
