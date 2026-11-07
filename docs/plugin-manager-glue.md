# PLUGIN-MANAGER-GLUE · 真实 CLI / serve 接线与验收

> base：`af4a49a2817592d7affb99bb3a16c7e03214ba1c` + 任务开始时共享工作树（含 W34、W48）。
> 工作目录：`C:/Users/Administrator/AppData/Local/Temp/DSH-GO-plugin-manager-glue`。
> 未 commit、未 push、未启动 GUI；没有改 `internal/pluginmgr/**`、`cmd/dshd/serve_plugins.go`、`ws.go`、canonical 快照/golden 或其他包。

## 1. 交付范围与真实行为

本任务只把 W48 已验证的入口接入产品，增量文件为：

- `cmd/dshd/main.go`
- `cmd/dshd/serve.go`
- `cmd/dshd/plugin_manager_glue_test.go`（新增）
- `internal/transport/server.go`
- `internal/transport/server_plugins_test.go`（新增）
- `docs/plugin-manager-glue.md`（新增）

### CLI

`dshd plugins ...` 现在真实进入 `runPluginsCLI(...)`，不再落到“未知子命令”。

```text
dshd plugins --root <dir> list
dshd plugins --root <dir> install --path <本地私有包目录>
dshd plugins --root <dir> enable|disable|uninstall --id <id>
dshd plugins --root <dir> update --id <id> --path <本地私有包目录>
dshd plugins --root <dir> restore --id <id> [--version <version>]
dshd plugins --root <dir> reconcile
```

- `--root` 必须显式给出；CLI 不调用 `defaultHome()`，也不读 `HOME`、`USERPROFILE`、`DSHD_HOME` 推导插件根。
- `dshd plugins --help`（以及 `-h`、`help`）在 stdout 打印完整用法并退出 0。
- 成功只在 stdout 写 `{result:…}`；失败只在 stderr 写 `{error:{code,message,hint,retryable}}` 并退出 1。主分派不再追加 `dshd:` 文本或改写这个退出码。
- 缺少 root、未知 action、卸载后再 enable 的失败分支均有真实子进程断言。

### serve / Transport

- serve 的唯一插件根推导是显式必填的 `<home>/plugins`。CLI 使用同一个路径时可与 serve 读写同一管理器。
- `pluginmgr.Open(<home>/plugins)` 在 listener ready 之前执行；`Open` 自身完成启动 reconcile 和当前包校验。损坏或悬挂事务不能让服务先 ready。
- 同一个 `*pluginmgr.Manager` 注入 `newPluginHTTPHandler(manager)` 并贯穿 listener 生命周期。每个写操作由 W48 管理器持久化；listener 停止后重开同一 home 仍返回原状态。
- `transport.Options.PluginHandler` 只在既有 POST 与 cookie 认证检查之后调用。未认证请求不会进入裸 handler；没有新增独立路由，也没有把裸 handler 暴露到非回环地址。
- handler 自己只认八个 additive 方法：`plugin.list/install/enable/disable/update/restore/reconcile/uninstall`。HTTP body 仍是裸参数，成功/错误仍使用 W48 的四字段信封。
- 未配置 `PluginHandler` 时，`/api/plugin.list` 保持闭合分派 501；canonical `session.list` 不被插件 handler 拦截。
- 25 个 canonical method、canonical 字段、WS 和 additive snapshot/golden 均未改。

## 2. 测试覆盖的真实边界

`TestPluginGlueRealDSHD` 先执行 `go build -o <临时目录>/dshd.exe .` 构建真实 `cmd/dshd` main 包，不是 `runPluginsCLI` 的测试 helper 替代品。

### 真实 CLI 子进程

每次 action 都启动一个新的真实 `dshd plugins` 进程。覆盖 help、缺 root、未知 action、空 list、install、enable、disable、update、restore、reconcile、uninstall、卸载后 list 和 not_found。包源与 manager root 都在临时测试目录中；`HOME/USERPROFILE/DSHD_HOME` 指向一个不存在的环境诱饵目录，测试结束仍断言它不存在。

`PLUGIN_GLUE_CLI_SUMMARY OK=13 FAIL=0` 的 13 是上述 13 次真实 CLI 调用；任何一步失败都会让 Go test 失败且不会打印该汇总。

### 真实 serve + ready token + cookie

测试启动真实 `dshd serve --home <临时home> --addr 127.0.0.1:0`，从 stdout 解析 `ready {url,token}`，再经 `/connect` 换取 HttpOnly cookie。

- 八个 `plugin.*` 方法逐一在无 cookie 时调用，全部 401 + 四字段 `unauthorized`，handler 不得提前执行。
- 认证后跑 list/install/enable/disable/update/restore/reconcile/uninstall；wrapped `params` 返回 400，卸载后 enable 返回 404，均检查四字段错误。
- A/B 两个 serve 进程、两个临时 home 同时运行：同一 `acme.audit` 在 A 安装并更新/恢复，在 B 保持独立版本、source 和 enabled 状态；A 卸载后 B 仍有自己的记录。
- A 在完成 restore 后停止 listener，测试只 kill 自己启动且持有 PID 的子进程，随后确认旧端口不再接受连接，再用同一 home 启动新 serve。重开后 list 仍为 v1、disabled、原 source。
- 停止期间写入一个合法但未提交的 restore journal；新 serve ready 后该 journal 已被启动 reconcile 清理，持久状态仍为 before，显式 reconcile 再返回 `recovered=0`。这是启动恢复夹具，不冒充又一次管理器崩溃测试；W48 已有真实崩溃测试。
- 真实 CLI 以 `--root <homeA>/plugins` 读取 serve 状态，证明两条入口使用同一 home 插件根；serve 卸载后 CLI 再读也为空。

`PLUGIN_GLUE_SERVE_SUMMARY OK=26 FAIL=0 (8 unauthenticated + 16 authenticated + 2 cross-process CLI)` 只在全部步骤完成后打印。

`internal/transport/server_plugins_test.go` 另用真实 listener 和 spy handler 证明：无 cookie 时 handler 调用数为 0；认证后原 path 与原裸 body 逐字透传；canonical `session.list` 不经过插件 handler；nil handler 仍是 501。

## 3. 先红后绿与反证

### 初始红

产品接线前首次运行新测试，真实 CLI `--help` 退出 2（`未知子命令: plugins`），认证后的 `plugin.list` 返回 501；Transport 测试因 `Options.PluginHandler` 不存在而编译失败。首次红跑还发现测试 recorder 错用 `strings.Builder.Bytes`，已改为 `bytes.Buffer`，没有把测试代码错误当产品缺陷。

### 最终测试对“未接线实现”的干净反证

为排除“测试后来改绿”的可能，最终在临时目录 `C:/Users/Administrator/AppData/Local/Temp/plugin-manager-glue-red-overlay` 重建任务开始时的 `main.go/serve.go/server.go`，用 Go overlay 替换测试输入；共享源码没有回滚或改写。命令：

```bash
GOFLAGS="-overlay=C:/Users/Administrator/AppData/Local/Temp/plugin-manager-glue-red-overlay/overlay.json" go test ./internal/transport ./cmd/dshd -run 'TestPluginHandlerMountedAfterCookieAuthentication|TestPluginHandlerAbsentKeepsClosedDispatchBehavior|TestPluginGlueRealDSHD' -count=1 -v
```

原样 stdout：

```text
OVERLAY=C:\Users\Administrator\AppData\Local\Temp\plugin-manager-glue-red-overlay\overlay.json
FAIL	dshgo/internal/transport [build failed]
=== RUN   TestPluginGlueRealDSHD
=== RUN   TestPluginGlueRealDSHD/CLI_full_lifecycle_and_explicit_root
    plugin_manager_glue_test.go:116: plugins --help exit=2 stdout="" stderr="未知子命令: plugins\ndshd —— dsh-go 宿主\n\n用法:\n  dshd modules graph [--manifest dsh.toml]   打印装配图（按能力排序后的激活顺序）\n  dshd modules list  [--manifest dsh.toml]   列出可装配模块\n  dshd exec --tool <name> --args <json>      装配后执行一个工具\n  dshd tools [--manifest dsh.toml]           打印模型看到的工具表（JSON）\n  dshd session rewrite --in <f> --out <f>     把 v4 会话读进来再用我们的编码器写出去（验证兼容用）\n  dshd run --prompt \"<话>\" [--max-steps N]    跑一个真实 turn（模型 + 工具 + 结果回灌）\n  dshd acp                                    在 stdin/stdout 上跑 ACP 服务端（stdout 只放协议）\n  dshd doctor [--home X] [--base URL]        自检：home/会话/写锁/网关/模块，每项给下一步\n  dshd wasm inspect --module <path>          魔数/sha256/导出函数名（按名字排序）\n  dshd wasm call --module <path> --fn <f>    调用导出函数；--args 2,3 --timeout 2s 可选，返回值打成一行（如 = 5）\n  dshd version                               版本\n"
=== RUN   TestPluginGlueRealDSHD/authenticated_serve_lifecycle_startup_reconcile_and_AB_homes
    plugin_manager_glue_test.go:360: POST plugin.list status=501 want=200 body={"error":{"code":"not_implemented","hint":"宿主这一侧还没接线，换一个 method 或等它实现","message":"not implemented","retryable":false}}
--- FAIL: TestPluginGlueRealDSHD (3.06s)
    --- FAIL: TestPluginGlueRealDSHD/CLI_full_lifecycle_and_explicit_root (0.05s)
    --- FAIL: TestPluginGlueRealDSHD/authenticated_serve_lifecycle_startup_reconcile_and_AB_homes (0.14s)
FAIL
FAIL	dshgo/cmd/dshd	3.149s
FAIL
EXPECTED_UNWIRED_RED_EXIT=1
```

原样 stderr：

```text
# dshgo/internal/transport [dshgo/internal/transport.test]
internal\transport\server_plugins_test.go:31:3: unknown field PluginHandler in struct literal of type Options
```

`EXPECTED_UNWIRED_RED_EXIT=1` 由脚本断言确为 1；未把预期红包装成通过。

## 4. 最终定向绿原样输出

命令：

```bash
go test ./internal/transport ./cmd/dshd -run 'TestPluginHandlerMountedAfterCookieAuthentication|TestPluginHandlerAbsentKeepsClosedDispatchBehavior|TestPluginGlueRealDSHD' -count=1 -v
```

输出：

```text
=== RUN   TestPluginHandlerMountedAfterCookieAuthentication
transport: /api/plugin.list
transport: /api/session.list
--- PASS: TestPluginHandlerMountedAfterCookieAuthentication (0.01s)
=== RUN   TestPluginHandlerAbsentKeepsClosedDispatchBehavior
transport: /api/plugin.list
--- PASS: TestPluginHandlerAbsentKeepsClosedDispatchBehavior (0.00s)
PASS
ok  	dshgo/internal/transport	0.055s
=== RUN   TestPluginGlueRealDSHD
=== RUN   TestPluginGlueRealDSHD/CLI_full_lifecycle_and_explicit_root
    plugin_manager_glue_test.go:163: PLUGIN_GLUE_CLI_SUMMARY OK=13 FAIL=0
=== RUN   TestPluginGlueRealDSHD/authenticated_serve_lifecycle_startup_reconcile_and_AB_homes
    plugin_manager_glue_test.go:439: PLUGIN_GLUE_SERVE_SUMMARY OK=26 FAIL=0 (8 unauthenticated + 16 authenticated + 2 cross-process CLI)
--- PASS: TestPluginGlueRealDSHD (3.81s)
    --- PASS: TestPluginGlueRealDSHD/CLI_full_lifecycle_and_explicit_root (0.65s)
    --- PASS: TestPluginGlueRealDSHD/authenticated_serve_lifecycle_startup_reconcile_and_AB_homes (0.54s)
PASS
ok  	dshgo/cmd/dshd	3.866s
```

三个相关包完整测试命令：

```bash
go test ./internal/pluginmgr ./cmd/dshd ./internal/transport -count=1
```

原样输出：

```text
ok  	dshgo/internal/pluginmgr	1.606s
ok  	dshgo/cmd/dshd	25.403s
ok  	dshgo/internal/transport	0.874s
```

## 5. 全仓 test / vet / build 原样输出

命令（`&&` 串联，任一非零都不会出现末尾标记）：

```bash
go test ./... -count=1 && go vet ./... && go build ./... && printf 'FULL_REPO_TEST_VET_BUILD_OK\n'
```

输出：

```text
ok  	dshgo/cmd/dshd	40.934s
?   	dshgo/cmd/dshd-bridge	[no test files]
ok  	dshgo/cmd/dshtui	0.906s
?   	dshgo/cmd/dshtui/assets	[no test files]
ok  	dshgo/cmd/dshtui/internal/vsinfo	0.113s
ok  	dshgo/codecs/v4	28.556s
ok  	dshgo/internal/acp	1.379s
ok  	dshgo/internal/agent	0.144s
ok  	dshgo/internal/approval	0.722s
ok  	dshgo/internal/compact	0.126s
ok  	dshgo/internal/fsutil	0.539s
ok  	dshgo/internal/kernel	0.646s
ok  	dshgo/internal/llm	0.169s
ok  	dshgo/internal/modes	0.624s
ok  	dshgo/internal/pluginmgr	5.610s
ok  	dshgo/internal/prompt	1.335s
ok  	dshgo/internal/prune	0.161s
ok  	dshgo/internal/schedule	0.870s
ok  	dshgo/internal/schema	0.149s
ok  	dshgo/internal/session	20.990s
ok  	dshgo/internal/spi	0.141s
ok  	dshgo/internal/transport	0.972s
ok  	dshgo/internal/wasmhost	22.544s
ok  	dshgo/modules/exec/bash	5.921s
ok  	dshgo/modules/fs/local	1.314s
?   	dshgo/modules/search	[no test files]
ok  	dshgo/modules/tool/ask	0.069s
ok  	dshgo/modules/tool/edit	0.746s
?   	dshgo/modules/tool/execlit	[no test files]
ok  	dshgo/modules/tool/github	9.413s
ok  	dshgo/modules/tool/glob	0.621s
ok  	dshgo/modules/tool/goal	0.363s
ok  	dshgo/modules/tool/grep	0.653s
ok  	dshgo/modules/tool/hindsight	0.122s
?   	dshgo/modules/tool/history	[no test files]
?   	dshgo/modules/tool/nowline	[no test files]
ok  	dshgo/modules/tool/plan	1.166s
ok  	dshgo/modules/tool/plugin	0.740s
ok  	dshgo/modules/tool/present	0.314s
?   	dshgo/modules/tool/pwsh	[no test files]
ok  	dshgo/modules/tool/read	0.537s
ok  	dshgo/modules/tool/readimage	0.295s
?   	dshgo/modules/tool/repohere	[no test files]
ok  	dshgo/modules/tool/schedule	1.127s
ok  	dshgo/modules/tool/skill	0.963s
ok  	dshgo/modules/tool/subagent	0.085s
?   	dshgo/modules/tool/subagentfork	[no test files]
ok  	dshgo/modules/tool/team	0.974s
ok  	dshgo/modules/tool/teamtask	0.872s
ok  	dshgo/modules/tool/todo	0.092s
ok  	dshgo/modules/tool/unlag	0.346s
ok  	dshgo/modules/tool/watch	0.817s
ok  	dshgo/modules/tool/wd	0.460s
ok  	dshgo/modules/tool/webfetch	0.107s
ok  	dshgo/modules/tool/websearch	0.086s
ok  	dshgo/modules/tool/workflow	0.085s
ok  	dshgo/modules/tool/write	0.161s
ok  	dshgo/modules/tool/wsdeps	10.796s
ok  	dshgo/modules/tools	0.104s
?   	dshgo/scripts/mineprompts	[no test files]
FULL_REPO_TEST_VET_BUILD_OK
```

`go vet ./...` 与 `go build ./...` 均无错误输出且退出 0，随后才打印 `FULL_REPO_TEST_VET_BUILD_OK`。

## 6. 定向 race 原样输出

工具链：Go `go1.27.3 windows/amd64`，MinGW-w64 GCC 16.2.0，`CGO_ENABLED=1`。命令：

```bash
gcc --version | head -1
go test -race ./internal/pluginmgr ./cmd/dshd ./internal/transport -run 'TestPlugin|TestLifecyclePersistsAndRestores|TestABHomeIsolation|TestReconcileAfterRealProcessCrash' -count=1 && printf 'DIRECTED_PLUGIN_GLUE_RACE_OK\n'
```

输出：

```text
gcc.exe (MinGW-W64 x86_64-ucrt-posix-seh, built by Brecht Sanders, r3) 16.2.0
ok  	dshgo/internal/pluginmgr	3.198s
ok  	dshgo/cmd/dshd	21.295s
ok  	dshgo/internal/transport	1.221s
DIRECTED_PLUGIN_GLUE_RACE_OK
```

没有 `WARNING: DATA RACE`，退出码 0；这不是 CGO=0 的跳过。

## 6.1 含最终测试文件的复跑

上面两组门禁通过后，只给测试增加了完成时打印的 `OK=n FAIL=0` 汇总。为使门禁证据严格对应最终测试文件，又原样复跑全仓和定向 race。

最终全仓命令：

```bash
go test ./... -count=1 && go vet ./... && go build ./... && printf 'FINAL_TREE_TEST_VET_BUILD_OK\n'
```

最终全仓输出：

```text
ok  	dshgo/cmd/dshd	55.555s
?   	dshgo/cmd/dshd-bridge	[no test files]
ok  	dshgo/cmd/dshtui	1.950s
?   	dshgo/cmd/dshtui/assets	[no test files]
ok  	dshgo/cmd/dshtui/internal/vsinfo	0.435s
ok  	dshgo/codecs/v4	32.600s
ok  	dshgo/internal/acp	2.117s
ok  	dshgo/internal/agent	0.452s
ok  	dshgo/internal/approval	2.181s
ok  	dshgo/internal/compact	0.441s
ok  	dshgo/internal/fsutil	1.948s
ok  	dshgo/internal/kernel	2.093s
ok  	dshgo/internal/llm	0.422s
ok  	dshgo/internal/modes	1.969s
ok  	dshgo/internal/pluginmgr	10.530s
ok  	dshgo/internal/prompt	4.272s
ok  	dshgo/internal/prune	0.137s
ok  	dshgo/internal/schedule	2.028s
ok  	dshgo/internal/schema	0.154s
ok  	dshgo/internal/session	24.017s
ok  	dshgo/internal/spi	0.122s
ok  	dshgo/internal/transport	1.013s
ok  	dshgo/internal/wasmhost	23.640s
ok  	dshgo/modules/exec/bash	9.548s
ok  	dshgo/modules/fs/local	4.074s
?   	dshgo/modules/search	[no test files]
ok  	dshgo/modules/tool/ask	0.165s
ok  	dshgo/modules/tool/edit	1.883s
?   	dshgo/modules/tool/execlit	[no test files]
ok  	dshgo/modules/tool/github	5.499s
ok  	dshgo/modules/tool/glob	1.088s
ok  	dshgo/modules/tool/goal	0.440s
ok  	dshgo/modules/tool/grep	2.133s
ok  	dshgo/modules/tool/hindsight	0.167s
?   	dshgo/modules/tool/history	[no test files]
?   	dshgo/modules/tool/nowline	[no test files]
ok  	dshgo/modules/tool/plan	4.257s
ok  	dshgo/modules/tool/plugin	4.635s
ok  	dshgo/modules/tool/present	1.496s
?   	dshgo/modules/tool/pwsh	[no test files]
ok  	dshgo/modules/tool/read	3.739s
ok  	dshgo/modules/tool/readimage	0.827s
?   	dshgo/modules/tool/repohere	[no test files]
ok  	dshgo/modules/tool/schedule	4.556s
ok  	dshgo/modules/tool/skill	3.321s
ok  	dshgo/modules/tool/subagent	0.263s
?   	dshgo/modules/tool/subagentfork	[no test files]
ok  	dshgo/modules/tool/team	3.852s
ok  	dshgo/modules/tool/teamtask	2.571s
ok  	dshgo/modules/tool/todo	0.177s
ok  	dshgo/modules/tool/unlag	1.318s
ok  	dshgo/modules/tool/watch	2.060s
ok  	dshgo/modules/tool/wd	0.387s
ok  	dshgo/modules/tool/webfetch	0.129s
ok  	dshgo/modules/tool/websearch	0.084s
ok  	dshgo/modules/tool/workflow	0.103s
ok  	dshgo/modules/tool/write	0.550s
ok  	dshgo/modules/tool/wsdeps	8.277s
ok  	dshgo/modules/tools	0.289s
?   	dshgo/scripts/mineprompts	[no test files]
FINAL_TREE_TEST_VET_BUILD_OK
```

最终 race 输出：

```text
gcc.exe (MinGW-W64 x86_64-ucrt-posix-seh, built by Brecht Sanders, r3) 16.2.0
ok  	dshgo/internal/pluginmgr	2.344s
ok  	dshgo/cmd/dshd	13.837s
ok  	dshgo/internal/transport	1.096s
FINAL_TREE_DIRECTED_RACE_OK
```

最终 race 命令仍为：

```bash
go test -race ./internal/pluginmgr ./cmd/dshd ./internal/transport -run 'TestPlugin|TestLifecyclePersistsAndRestores|TestABHomeIsolation|TestReconcileAfterRealProcessCrash' -count=1 && printf 'FINAL_TREE_DIRECTED_RACE_OK\n'
```

两次均退出 0；vet/build 仍由 `&&` 末尾标记证明，没有错误输出。

## 7. canonical 与产物

任务中实际计算的未改夹具 SHA-256：

```text
73d5651c1098b04c7bbd8d1dd2a650c4bd902257bc8e6ba6a3643367a2369e0f *internal/transport/testdata/api.snapshot.json
17af00ec53d54d00b874256ce1c88ad446a3454e26e710bc5a20a28e1d890af9 *internal/transport/testdata/plugins.snapshot.json
f5fef9bdd486a93f9bf4239a8afb2beed3035756d6b16be35ba7aebe79b55c5d *internal/transport/testdata/plugins-golden.json
```

最终补丁：`C:/Users/Administrator/AppData/Local/Temp/plugin-manager-glue-final.patch`。补丁只含第 1 节六个文件相对任务开始共享树的增量；`serve.go` 任务前已有的 W20 三处 hunk 不带入本补丁。补丁 SHA-256 由交付正文与同目录 `.sha256` 文件给出，避免把哈希写进被哈希文件自身。

## 8. 明确未外推的范围

- 这是受限本地包的管理面 glue，不是动态加载、第三方入口执行、远端市场或官方完整插件生态。
- UI 未新增消费；additive snapshot 仍声明 `ui.implemented=false`。
- W48 的跨进程并发写锁边界不变；本任务只证明真实 CLI 读与 serve 写共享同一 root，不声称多个写进程可并发提交。
- 本任务没有修改或重验 H14 的 25 canonical 方法，也没有用插件测试冒充真实模型、WS、GUI 或完整工作日验收。
