# Go 宿主沙盒权限：现状与实现契约

## 1. 当前结论

**第一阶段（sandbox-policy 核心）与第二阶段（生产接线）已落地；第三阶段（OS 级隔离）已落地「受控根版」的一半——它是 partial，不是完整沙箱。**

已到达的层级（每条都可按 `file:line` 复核）：

- 三档是**真实运行时档位**：`internal/sandbox/policy.go:122 New` / `:196 evaluate` 三档；`danger-full-access` 是其中一档，不再是"审批别名"；
- 每档都捆上了 sandbox：`internal/modes/modes.go:37 SandboxMode` / `:66 Builtins`（四档 preset 每档带 `Sandbox`）；
- bash / pwsh schema 都有官方成对参数 `sandbox_permissions` + `justification`（`modules/exec/bash/tools.go:144-145`、`modules/tool/pwsh/pwsh.go:54-55`，描述逐字取官方 `sandboxPermissionsDescription('command')`），per-call 升级链路真实，**配对校验在准入层与执行层各判一次**（`modules/exec/bash/sandbox_scope.go:32`、`modules/tool/pwsh/sandbox_scope.go:34`、`modules/exec/bash/tools.go:198`、`modules/tool/pwsh/pwsh.go:309`）；
- 拒绝是**起进程之前**的结构化事实：allow/deny/needs-approval + 机器可读 reason + 实际 mode（`internal/sandbox/policy.go:181-194` 的 `Decision`）；
- 官方 escalation 词表逐字落地：`internal/sandbox/escalation.go`（四个 `SANDBOX_ESCALATION_*` 码 `:24-52`、阶梯 `:69-98`、配对校验 `:107`、标记行 `:132`/`:139`、参数描述 `:146`/`:153`）；拒绝与待审批的 Hint 带官方标记行（`modules/exec/bash/tools.go:368`、`modules/tool/pwsh/pwsh.go:449`）；
- 策略层如实自报隔离层级：`internal/sandbox/policy.go` 的 `Enforcement`，有**两个**真值，逐会话按实际报：
  - `policy-only-no-os-isolation`（默认）：只有策略层；
  - `windows-acl-write-only-partial`：Windows 受控根 ACL runner 真的生效。
  两个值都出现在 `sandboxPolicy.get/set` 结果里（`cmd/dshd/serve_sandbox_policy.go` 的 `sandboxEnforcementNow`，带 `enforcement_note` 说明为什么是这一档）与 pwsh 结果的 `data.enforcement`。
- **Windows ACL runner（2026-11-20，task-129）**：`internal/sandbox/runner_windows.go` 用 `CreateRestrictedToken(WRITE_RESTRICTED=0x8)` + `SetTokenInformation(TokenIntegrityLevel, S-1-16-4096)` 把命令降成受限 Low 令牌，再用 `GetNamedSecurityInfoW`/`SetEntriesInAclW`/`SetNamedSecurityInfoW` 把**随机会话 SID** 直授给受控根（不走 `icacls`，因此不会踩 1332 `ERROR_NONE_MAPPED`）；启动交给 `os/exec` 的 `syscall.SysProcAttr.Token`（Go 的 `CreateProcessAsUser` 包装带 `lpCurrentDirectory`，所以探针踩过的 267 `ERROR_DIRECTORY` 不会重现）。机制由 `internal/sandbox/runner_windows_test.go` 的四条证明守着：根外写 `code=5`、根内写 `code=0`、删根后父目录 DACL 逐字不变、以及「同令牌同目录同 Low 完整性、只差一个 ACE」的因果对照。

**仍未到达的层级（不得写成 DONE）**：完整 OS 隔离。ACL runner 只做到：

- **作用域**只限宿主自有的 `<home>/sandbox-roots/<session-id>/`——**绝不往用户/共享工作区写驻留 ACE 或 Low 标签**（这正是官方 `dsh-sandbox-windows-acl` 的代价，见 §4）；删掉受控根即无残留（测试断言父目录 DACL 前后逐字相同）；
- 挡住的是**受控根之外的写与删**；读、网络、进程可见性、注册表、以及受控根**内部**的写都不受限；
- 受控根把命令的可写面收在一个宿主自有目录里，所以 **workspace-write 下命令写不进用户工作区**——这是有意的收窄，也正因如此它默认关闭（`DSHD_SANDBOX_ACL_ROOT`）；
- **本机实测硬事实**：PowerShell 7 在受限令牌下加载期就 `0xc0000142`（STATUS_DLL_INIT_FAILED）退出，`cmd.exe` 正常；把两个因子拆开验证过——只造受限令牌、**不降完整性级别**仍然 `0xc0000142`，所以拦住它的是受限令牌本身，不是 Low 标签。因此 pwsh-sandbox 虽然接了线，今天仍走策略层（`modules/tool/pwsh/sandbox_runner.go` 先跑能力探针，探不通如实退回并写明 `runner_note`）。

Windows Job Object 仍只负责进程树回收与 `KILL_ON_JOB_CLOSE`；FS 侧仍只有路径级边界（`internal/sandbox/fs.go:15 EvaluateFS`、`internal/sandbox/runtime.go:157 CheckWrite`）。官方对应两条包：`dsh-sandbox-local`（bwrap/Landlock/Seatbelt + Windows ACL runner）与 `dsh-sandbox-windows-acl`；本仓只有后者的「受控根版」，Linux/macOS 一条都没有（`runner_other.go` fail-loud）。

不加受控根时，本仓是**三档策略 + 审批 + 路径级 FS 边界 + 进程管理**，enforcement 自报为 `policy-only-no-os-isolation`——这是有意为之的诚实标注，不是遗漏：官方 `dsh-pwsh-sandbox` 在无 runner 时 fail-closed 报 `SANDBOX_UNAVAILABLE`，本仓选择「策略层拒绝 + 明确自报未隔离」而不是「冒名沙盒」。加了受控根之后 enforce 到 `windows-acl-write-only-partial`，名字里的 partial 就是它真实的边界。

Windows 上的官方平台事实（2026-11-19 按 rc.2 asar 内源码重核，证据见 [`docs/sandbox-trio-official-shape.md`](sandbox-trio-official-shape.md)）：

- `dsh-base/cordis.patch.yml:251` 对 `bash-sandbox` 写的是 `disabled: !!js process.platform === 'win32'`（同文件 `:283` 连 `tool-bash` 也 disabled）——**bash 在 Windows 上确实没有命令级 OS 沙箱**，本仓"Windows 上没有 OS 级 bash 沙箱"在能力面上不低于官方。
- 但同一份配置 `:257` 对 `pwsh-sandbox` 写的是 `disabled: !!js process.platform !== 'win32'`——**pwsh 恰好在 Windows 上启用**，而它是把每条 pwsh 命令送进 `ctx.sandbox.confine` 的执行器（`dsh-pwsh-sandbox/src/index.ts:4-5`、`:194-196`），官方 Windows desktop 的 pwsh 命令走的是 `dsh-sandbox-windows-acl` 的受限令牌 runner。
- 所以「官方在 Windows 上没有命令级 OS 沙箱」是**从 bash 外推到 pwsh 的错结论**。本仓在 pwsh 这一侧确实比官方少一层：策略闸门 + escalation 契约有，受限令牌 runner 没有。

另一个已补的策略层契约：官方 `dsh-sandbox/src/escalation.ts` 的升级词表（严格更宽阶梯 `WIDER_MODES`、`ESCALATION_TARGETS`、`validateEscalationArgs` 的请求/理由配对、`sandboxDenialMarker` / `escalationHintMarker` 两行模型可见标记）已逐字移植到 `internal/sandbox/escalation.go`，并在 bash/pwsh 两个工具族的 schema、准入层与 Hint 里接线。这一层**不是** OS 隔离，`Enforcement` 仍自报 `policy-only-no-os-isolation`。

OS 级隔离要不要做、做到哪一层，见 §4；逐条插件结论见 `docs/base-plugins-partial19-official-shape.md`。

## 2. 第一阶段：sandbox-policy 核心（必须实现）

### 2.1 三档语义

| 模式 | 默认读 | 默认写 | 默认执行 | 一次性升级 |
|---|---|---|---|---|
| `read-only` | 按现有只读边界 | 拒绝 | 拒绝执行类工具 | 可请求单次 `workspace-write` / `danger-full-access`（官方 `WIDER_MODES`，`escalation.ts:64-67`）；必须经真实审批，未批准不得执行；会话档位不变 |
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

- `justification` 与 `sandbox_permissions` **必须同行**：只有请求没有理由、或只有理由没有请求、或理由是空白，都按官方 `validateEscalationArgs` 在起进程前报 `SANDBOX_ESCALATION_INVALID_ARGUMENT`（三句文案逐字取官方）。
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

- 官方 `dsh-base/cordis.patch.yml:251` 在 win32 **disabled** `bash-sandbox`（`:283` 连 `tool-bash` 也 disabled）；但 `:257` 在 win32 **启用** `pwsh-sandbox`，且它把每条 pwsh 命令交给 `dsh-sandbox-local` 的 Windows ACL 链（`dsh-sandbox-local/src/index.ts:184 win32: ['windows-acl']`、`:554` 无链 fail-closed）。即官方 Windows desktop 的 **pwsh** 命令级 OS 沙箱**在跑**。
- 该 runner 是 `WRITE_RESTRICTED` 受限令牌 + Low 完整性标签 + 每工作区/每会话 SID，**官方自己标 partial**；`dsh-sandbox-windows-acl/README.zh.md:127`：只限制写/删，**读、网络、进程可见性都不受限**。`:130-131` 另写明：工作区 ACE / 拒绝项 / Low 标签是**驻留改动**，`icacls` 回收不了（`ERROR_NONE_MAPPED` 1332），Low 标签存活期长于 DSH。
- 微软文档要求 restricted app 使用替代 desktop，否则仍可 SendMessage/PostMessage。

结论：**可对齐的是「工作区外写/删受限」这一层（ACL/受限 SID），不是完整隔离**。要做也只按官方 partial 语义做，并在结果里写明读/网络/进程不受限；不得声称已有完整沙箱，也不为了"有沙箱"自造未经验证的隔离语义。

### 4.1 已经做完的部分（2026-11-20，task-129）

独立探针（767 行，`tmp/GO8-RC5/_sbxroot/probe/`）先证明机制，再搬进宿主：

- 探针证明：受控根内写 `code=0`；根外写 `code=5 Access is denied`；删根后目录消失、`icacls` 只剩继承 ACE、**父目录 ACL 前后逐字相同**；**因果对照**——根外与根同为 Low 完整性，给根外授同一 ACE 就 `code=0`、撤销立刻回 `code=5`，所以挡住写入的是受限 SID + 目录 ACL，**不是完整性级别**。
- 宿主实现：`internal/sandbox/runner_windows.go`（令牌 / Low 标签 / DACL 直授 SID）+ `internal/sandbox/runner.go`（受控根路径、引用计数生命周期、`ProbeExecutable`）+ `internal/sandbox/runner_other.go`（非 Windows fail-loud）。
- 三条证明 + 因果对照在 `internal/sandbox/runner_windows_test.go` 里复现（无权限时**明确 SKIP 并说明**）。

**关键取舍：不采用官方的「驻留改动」**。官方把 ACE 与 Low 标签打在**工作区目录树**上，`README.zh.md:130-131` 自己写明回收不了（`icacls` 报 1332 `ERROR_NONE_MAPPED`）、标签寿命长于宿主，还会把目录树放宽给同用户下其他低完整性进程；随包发的 45 KB `diagnose-windows-sandbox-acl` 就是给误伤善后的。本仓改把 ACL 与标签落在**宿主自有的** `<home>/sandbox-roots/<session-id>/` 上：删掉目录就一点不剩，用户/共享工作区一个字节都不动。

代价（必须写明）：受控根模式下命令**写不进用户工作区**，因为可写面就是那个根。所以它是显式开关（`DSHD_SANDBOX_ACL_ROOT`），不是默认行为。

### 4.2 还没做完的部分：受限令牌下 pwsh 起不来

本机实测（2026-11-20）：PowerShell 7 在受限令牌里**加载期就** `0xc0000142`（STATUS_DLL_INIT_FAILED）退出，`cmd.exe` 正常。把两个因子拆开验证：只造受限令牌、**不降完整性级别**仍然 `0xc0000142` ⇒ 挡住它的是**受限令牌本身**，不是 Low 标签。这与「根外写不进不能归因于 IL」是同一条纪律：先做因果分离，再下结论。

因此 `pwsh-sandbox` 虽然接线了，今天生产里仍走策略层：`modules/tool/pwsh/sandbox_runner.go` 在启用时先跑一次能力探针，探不通就**如实退回**并把原因写进结果的 `runner_note`，绝不静默降级、也不假装隔离。

下一步若要让 pwsh 真在受限身份里跑，要解决的是受限令牌下的 DLL 初始化 / 窗口站桌面访问（可能与官方那个 diagnose 脚本在兜的是同一摊），属于需要另开一单的独立设计，不应在策略层或本单里顺手改。

## 5. 验收门

- 先红：旧实现没有三档与一次性升级；read-only 可写、workspace 可越界、未审批可执行危险命令的用例必须真实失败。
- 后绿：三档、A/B、一次性、后续调用不继承、未知值 fail-loud、FS canonical 边界、bash/pwsh拒绝前零进程。
- 生产：真实 dshd + session.setPermission + bash/edit/write/pwsh + 重开/fork/resume，不只 helper 单测。
- ACL runner：`go test ./internal/sandbox -run 'TestRunner|TestConfinedShellProbe' -v` 必须打印四条证明的实际 Win32 码（根外 5 / 根内 0 / 删根后无残留 / 因果对照 5→0→5）；非 Windows 或无权限时**明确 SKIP 并说明**，不许静默绿。
- `enforcement` 不许说谎：没配 `DSHD_SANDBOX_ACL_ROOT` 时必须报 `policy-only-no-os-isolation`，配了但探针不通时同样报 `policy-only` **并且**带上 `enforcement_note` / `runner_note`。
- 全仓 `go test ./...`、`go vet ./...`、`go build ./...` exit 0；相关包 `-race` 成功。
- 文档只按实际到达层级写 DONE；core 绿不等于生产沙盒完成。
