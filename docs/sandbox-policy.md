# Go 宿主沙盒权限：现状与实现契约

## 1. 当前结论

**第一阶段（sandbox-policy 核心）与第二阶段（生产接线）已落地；第三阶段（OS 级隔离）尚未落地。**

已到达的层级（每条都可按 `file:line` 复核）：

- 三档是**真实运行时档位**：`internal/sandbox/policy.go:123 New` / `:197 evaluate` / `:273` 三档；`danger-full-access` 是其中一档，不再是"审批别名"；
- 每档都捆上了 sandbox：`internal/modes/modes.go:37 SandboxMode` / `:66 Builtins`（四档 preset 每档带 `Sandbox`）；
- bash / pwsh schema 都有 `sandbox_permissions`（`modules/exec/bash/tools.go:144`、`modules/tool/pwsh/pwsh.go:52`），per-call 升级链路真实（`modules/exec/bash/sandbox_scope.go:21`、`modules/tool/pwsh/pwsh.go:334 authorize`）；
- 拒绝是**起进程之前**的结构化事实：allow/deny/needs-approval + 机器可读 reason + 实际 mode（`internal/sandbox/policy.go:181-194` 的 `Decision`）；
- 策略层如实自报隔离层级：`internal/sandbox/policy.go:64-69` 的 `Enforcement`，档位只有一个真值 `policy-only-no-os-isolation`，并出现在 `sandboxPolicy.get/set` 结果里（`cmd/dshd/serve_sandbox_policy.go:281/317`、`cmd/dshd/serve.go:1104`）。

**仍未到达的层级（不得写成 DONE）**：OS 级 runner。目前命令仍以宿主用户权限起进程；Windows Job Object 只负责进程树回收与 `KILL_ON_JOB_CLOSE`，FS 侧只有路径级边界（`internal/sandbox/fs.go:15 EvaluateFS`、`internal/sandbox/runtime.go:157 CheckWrite`）。官方对应两条包：`dsh-sandbox-local`（bwrap/Landlock/Seatbelt + Windows ACL runner）与 `dsh-sandbox-windows-acl`，本仓都没有。

因此本仓当前是**三档策略 + 审批 + 路径级 FS 边界 + 进程管理**，enforcement 自报为 `policy-only-no-os-isolation`——这是有意为之的诚实标注，不是遗漏：官方 `dsh-pwsh-sandbox` 在无 runner 时 fail-closed 报 `SANDBOX_UNAVAILABLE`，本仓选择"策略层拒绝 + 明确自报未隔离"而不是"冒名沙盒"。

Windows 上还有一条官方事实需要记住：`dsh-base/cordis.patch.yml:249-251` 对 `bash-sandbox` 写的是 `disabled: !!js process.platform === 'win32'`——**官方桌面端在 Windows 上根本没跑命令级 OS 沙箱**（同文件 `:281-283` 连 `tool-bash` 也 disabled）。本仓支持范围只有 Windows（AGENTS.md §0 裁定 2026-11-08），所以"Windows 上没有 OS 级 bash 沙箱"在能力面上**不低于官方**；本仓相对官方的增量是策略闸门仍然在跑（官方在 Windows 上整行关掉）。

OS 级隔离要不要做、做到哪一层，见 §4；逐条插件结论见 `docs/base-plugins-partial19-official-shape.md`。

## 2. 第一阶段：sandbox-policy 核心（必须实现）

### 2.1 三档语义

| 模式 | 默认读 | 默认写 | 默认执行 | 一次性升级 |
|---|---|---|---|---|
| `read-only` | 按现有只读边界 | 拒绝 | 拒绝执行类工具 | 拒绝；不能在只读会话里升级成全权 |
| `workspace-write` | 工作区 + 显式 extra read + 受控 `~` 读 | 仅工作区 | 按现有审批执行，但命令仍受工作区策略 | 对指定单次调用可请求 `danger-full-access`；必须经真实审批，未批准不得执行 |
| `danger-full-access` | 不虚构路径沙盒 | 不虚构路径沙盒 | 按现有审批 | 不需要升级；仍保留工具审批、结构化输出与进程树归属 |

未知模式、未知 `sandbox_permissions`、缺失策略上下文都必须 fail-loud，不得静默降级为 default 或放行。

### 2.2 工具接口

bash/edit/write 及 pwsh 如暴露升级参数，形状与当前官方工具一致：

```json
"sandbox_permissions": {
  "type": "string",
  "enum": ["workspace-write", "danger-full-access"]
}
```

- 参数是**单次调用**的权限请求，不改变会话模式，不写入全局状态，不影响后续调用或其他会话。
- `workspace-write` 是保持当前工作区档，不是升级；`danger-full-access` 才需要一次性更宽权限审批。
- 返回结果必须区分 `allow / deny / needs-approval`，附机器可读原因和实际 mode；审批通过只给该调用，不能记成永久授权。
- A/B 会话、并行工具、子代理必须各自解析，禁止共享一个可写全局“当前权限”。

### 2.3 FS 边界

- `workspace-write`：写只允许 canonical workspace root；extra read root 和 `~` home 读不自动获得写权限。
- `read-only`：所有写都拒绝，包括工作区内新建。
- `danger-full-access`：只有该次已批准调用可越过 workspace 写边界；FS 的读后写、防陈旧覆盖仍保留。
- canonical path、符号链接/reparse point、已存在父目录与目标不存在时的创建路径必须一致处理，不能用字符串前缀绕过。

### 2.4 bash/pwsh 边界

- 当前直接启动 shell 的事实必须写在结果和文档里，不能称 OS sandbox。
- policy 层先决定命令是否 allow/deny/needs-approval；拒绝时返回 `Result{Hint:…}, err`，不得启动进程。
- one-shot 审批未完成前不得启动 shell；审批失败后不能留下后台 job。
- Windows Job Object / Unix process group 继续负责进程树回收，不能拿它证明文件或网络隔离。

## 3. 第二阶段：生产接线

core 完成后，由 backend 单写者接入：

1. 会话 mode → runtime sandbox policy；`session.setPermission` 同步返回真实 sandbox mode，不能只改 approval。
2. FS read/write/edit、bash/pwsh、审批管线与错误四字段。
3. `internal/agent` / ACP / serve 使用同一份策略，不从工具名猜权限。
4. `session.load`、fork/resume 保留原权限，read-only 不能在恢复后变大。
5. UI 只在有真实 mode 时显示三档；没有真值不伪造。

## 4. 第三阶段：OS 级 Windows 隔离（另行评估）

本阶段不是第一阶段完成的代名词。已取证（只读，asar sha256 `5fa8a6b2…`）：

- 官方 `dsh-base/cordis.patch.yml:249-257` 在 win32 **disabled** `bash-sandbox`；`:281-283` 连 `tool-bash` 也 disabled。即官方桌面端在 Windows 上**没有**命令级 OS 沙箱在跑。
- 官方 `dsh-sandbox-local/lib/index.js:175-194` 有 Windows ACL runner，但**自己标 partial**；其 `README.zh.md:127-131` 写明：只限制写/删，**读、网络、进程可见性都不受限**；硬链接、控制台、常驻 Low 完整性标签都是已知边界。
- 微软文档要求 restricted app 使用替代 desktop，否则仍可 SendMessage/PostMessage。

结论：**可对齐的是「工作区外写/删受限」这一层（ACL/受限 SID），不是完整隔离**。要做也只按官方 partial 语义做，并在结果里写明读/网络/进程不受限；不得声称已有完整沙箱，也不为了“有沙箱”自造未经验证的隔离语义。需要独立设计、独立探针（不触碰用户文件）与回滚方案。

## 5. 验收门

- 先红：旧实现没有三档与一次性升级；read-only 可写、workspace 可越界、未审批可执行危险命令的用例必须真实失败。
- 后绿：三档、A/B、一次性、后续调用不继承、未知值 fail-loud、FS canonical 边界、bash/pwsh拒绝前零进程。
- 生产：真实 dshd + session.setPermission + bash/edit/write/pwsh + 重开/fork/resume，不只 helper 单测。
- 全仓 `go test ./...`、`go vet ./...`、`go build ./...` exit 0；相关包 `-race` 成功。
- 文档只按实际到达层级写 DONE；core 绿不等于生产沙盒完成。
