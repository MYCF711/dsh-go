# HOST-PLUGIN-MANAGER · 受限本地包最小真实竖切

> base：`af4a49a2817592d7affb99bb3a16c7e03214ba1c` + 任务开始时的共享工作树（含 H14）。未 commit、未 push、未启动 GUI。
>
> 范围只含 `internal/pluginmgr/**`、`cmd/dshd/serve_plugins*.go`、additive Transport 快照/golden、契约与本说明。既有 25 个 canonical method、canonical 类型字段和 H14 golden 未改。

## 1. 已实现的真值

`internal/pluginmgr` 实现 `list/install/enable/disable/uninstall/update/restore/reconcile`。它只管理本任务定义的 `dsh.local-plugin@1` 本地目录，不是官方完整插件生态，也不执行或动态加载包入口。

- **安装**：完整验证私有目录，默认 `enabled=false`；记录绝对 `source`、id/version/kind/entry。
- **状态**：enable/disable 原子替换 `state.json` 后才返回；list 每次重新校验当前包并按 id 排序。
- **更新**：新 SemVer 必须严格大于当前版；复制、hash 校验、staging rename 全部成功后才切换状态。失败不修改旧 current。
- **恢复**：保留所有已安装版本直到 uninstall；restore 可显式选版本，或默认选择 `previousVersion`。恢复后 previousVersion 指向恢复前版本。
- **卸载**：先把 `packages/<id>` 原子移入事务 trash，再提交删除状态；提交前失败会移回，提交后由 reconcile 幂等清理。
- **重启**：`Open(root)` 自动 reconcile；也可经 `plugin.reconcile` 显式执行。事务损坏、状态既非 before 也非 after、当前包缺失都 fail-loud，不猜、不静默删包。

## 2. 私有包边界

唯一 manifest 是包根 `plugin.json`：

```json
{
  "schema": "dsh.local-plugin@1",
  "id": "acme.audit",
  "version": "1.0.0",
  "kind": "wasm",
  "entry": "plugin.wasm",
  "files": {"plugin.wasm": "<64 位小写 sha256>"}
}
```

| 校验 | 行为 |
|---|---|
| JSON | 重复键、未知字段、尾随值、坏 JSON、超过 1 MiB 的 manifest 均拒绝 |
| 身份 | id 是受限小写串；version 是 SemVer 2.0；kind 只收 `wasm` / `process` |
| 路径 | 只收 `/` 分隔的干净相对路径；拒绝 `..`、绝对路径、盘符、反斜杠、NUL |
| 文件 | entry 必须有 hash；每个非 manifest 文件都必须登记；拒绝缺文件、额外未 hash 文件、非常规文件、symlink |
| 内容 | 源目录与复制后 staging 都逐项验证 SHA-256；复制期间 manifest/payload 改变会失败 |
| 重复 | install 已存在 id 返回 `duplicate_id`；update 不覆盖已有版本目录，只可用 restore 选择保留版 |

生产代码没有 subprocess、网络、市场客户端、`os.UserHomeDir` 或环境变量默认值。管理器根目录由调用者显式传入；包源不能位于管理器 root 内，避免递归复制/自覆盖。当前并发保证是**单管理器进程内互斥**，跨进程并发写未实现锁，不能声称支持。

## 3. 磁盘事务与恢复判据

```text
<root>/
  state.json
  packages/<id>/<version>/plugin.json + 已 hash 文件
  transactions/<random>/
    transaction.json
    staging/                  # install/update，rename 后消失
    trash/                    # uninstall 提交前的旧包
```

1. install/update：写 prepared journal → 复制并复核 staging → staged → 原子 rename 到版本目录 → installed → committing → 原子替换 state。
2. restore：不改版本目录，只以事务包住 current 状态切换。
3. uninstall：prepared → 包目录 rename 到 trash → package_removed → committing → 原子替换 state。
4. reconcile 对照**实际持久状态**判断 before/after，而不是盲信 phase：未提交 install/update 删除新目录并保留旧版；未提交 uninstall 把 trash 移回；已提交事务保留新状态并清理 journal。
5. 状态文件与同目录事务文件均使用临时文件、Sync、Close、rename。Windows 上已有版本目录不可覆盖；状态文件替换由真实生命周期与重启测试覆盖。

## 4. Transport / CLI 适配器

契约：`docs/transport-contract.md` 的 additive 节；机器文件：

- `internal/transport/testdata/plugins.snapshot.json`：8 个 `plugin.*` method、裸参数、Plugin/result 字段、四字段错误、UI 未实现声明。
- `internal/transport/testdata/plugins-golden.json`：完整 list→install→enable→update→disable→restore→reconcile→uninstall→list，以及 wrapped params 和 not_found 错误。

`cmd/dshd/serve_plugins.go` 中 HTTP 与 CLI 共用 `callPluginMethod`。成功使用 `{result:…}`，错误使用同一个 typed envelope，恰好 `{code,message,hint,retryable}`；H8 的 `TestNoHandwrittenErrorBodies` 实际扫过并通过了这些文件。

CLI 适配器形状：

```text
plugins --root <显式管理器目录> list
plugins --root <dir> install --path <私有包目录>
plugins --root <dir> enable|disable|uninstall --id <id>
plugins --root <dir> update --id <id> --path <私有包目录>
plugins --root <dir> restore --id <id> [--version <version>]
plugins --root <dir> reconcile
```

### 明确的集成边界

任务开始时 `cmd/dshd/main.go` 的 CLI switch、`cmd/dshd/serve.go` 与 `internal/transport/server.go` 都是闭合分派，且没有插件 hook；本任务禁止修改这些文件。因此：

- 真实 socket 测试直接运行 `newPluginHTTPHandler(manager)`；真实子进程测试直接运行 `runPluginsCLI(...)`。
- **现有 dshd 主二进制的 `plugins` 子命令和既有 serve listener 尚未接线**。Lead 需由有权限的 owner 把 HTTP handler 置于既有 cookie 认证后，并把 CLI action 接到显式 home 子目录。
- 独立 HTTP 适配器测试没有冒充 `/connect` 认证已联调；生产集成必须保留现有认证，不能把裸 handler 直接暴露到非回环地址。
- UI 没有改，也没有任何 UI 字段伪造；动态加载、第三方代码执行、远端市场、跨进程写锁、真实用户插件目录均未实现/未验证。

## 5. 先红后绿与本次实证

### 红

实现前运行：

```bash
go test ./internal/pluginmgr ./cmd/dshd -run 'TestLifecyclePersistsAndRestores|TestPluginHTTPGoldenLifecycle|TestPluginCLIRealProcessLifecycle' -count=1
```

退出码 1：`internal/pluginmgr` 无产品 Go 文件，`Manager/PluginInfo/Open` 等 undefined。随后先实现管理器，再实现适配器。

第一次完整包门还抓到 `TestNoHandwrittenErrorBodies` 红：HTTP/CLI 四个出口手写了 error map。已改成唯一 `pluginEnvelope` 后重跑，未删测试、未放宽断言、未改 canonical golden。

### 绿

最终实际执行：

```bash
go test ./internal/pluginmgr ./cmd/dshd ./internal/transport -count=1
go test ./... -count=1
go vet ./...
go build ./...
```

全部退出码 0。最终一轮关键计时输出：定向包 `internal/pluginmgr 2.118s`、`cmd/dshd 21.050s`、`internal/transport 0.743s`；全仓测试 `cmd/dshd 28.865s`、`internal/pluginmgr 5.090s`、`internal/transport 0.854s`，随后 vet/build 均无错误输出，末尾打印 `FINAL_GO_GATES_OK`。

行为测试证据：

| 测试 | 真实断言 |
|---|---|
| `TestLifecyclePersistsAndRestores` | 安装默认禁用；enable/disable、update、restore、uninstall 跨 `Open` 持久；list 的 version/source/enabled 真值正确 |
| `TestFailedUpdateKeepsOldVersion` | v2 payload hash 损坏时 update 失败，当前与重启后仍为 v1 |
| `TestUpdateCommitFailureRollsBackStagedPackage` | 已 staged 的 v2 在 committing 注入失败，current 仍为 v1、新目录删除、重启不复活 |
| `TestRejectedPackagesFailLoud` | 路径逃逸、反斜杠、非法版本、坏/缺 manifest、额外/缺失文件、symlink、重复 JSON 键全部失败且状态为空 |
| `TestDuplicateIDAndVersionAreRejected` | 重复 id 返回 `duplicate_id`；非递增更新失败 |
| `TestABHomeIsolation` | 同 id 在两个临时 home 可有不同 version/source/enabled，互不可见 |
| `TestReconcileAfterRealProcessCrash` | 测试 helper 进程在 installed/package_removed/state_committed 真实 `os.Exit(86)`；重启分别回滚旧版、保留已提交新版、恢复卸载包或完成已提交卸载；第二次 reconcile=0 |
| `TestPluginHTTPGoldenLifecycle` | 12 个 golden step 经 `httptest.NewServer` 真实 HTTP socket 逐步全等比对；错误顶层与 error 对象键数也校验 |
| `TestPluginHTTPRejectsMalformedAndWrappedParams` | params 包装、未知字段、坏 JSON、null、数组全部 400 + 四字段 |
| `TestPluginCLIRealProcessLifecycle` | 每个 action 都是新的测试二进制子进程；完整生命周期、跨进程持久状态及 not_found 四字段 stderr/exit=1 |
| `TestPluginContractSnapshotIsAdditive` | canonical 仍 25 method，扩展 8 method 无交集；UI implemented=false |

所有包和 home 都是 `t.TempDir()` 下的私有测试夹具。payload 只是带版本文本的字节，不是可执行插件；测试没有访问真实用户 home、远端市场、GUI 或现有插件目录。
