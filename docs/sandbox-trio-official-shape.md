# sandbox 三件的官方形状（`sandbox` / `bash-sandbox` / `pwsh-sandbox`）

> 取证对象：`D:\DeepSeek Harness\Desktop-0.2.2-rc.2\resources\app.asar`
> （sha256 `f87a50e96ea366050a6f7ff13f636630114d62f44132643a890e04fb13e89f65`），
> 官方包在 asar 里**带 `src/*.ts` 源码**，下列 `file:line` 都是官方 TypeScript 原文的行号。
> `cordis.patch.yml` 取 HS-00 快照同版本文件（`…/dsh-official-sync-20261107/HS-00/official/cordis.patch.yml`）。
> 抽取脚本：`tmp/GO8-RC5/_sbx/asar-extract.mjs`（只读，按条目 offset 落盘）。

## 1. 三件事各自的官方身份

| 插件 id | 官方包 | 形态 |
|---|---|---|
| `sandbox` | `@deepseek-ai/dsh-sandbox-local` | **真 runner**：按平台选链、功能探针仲裁、无链 fail-closed |
| `sandbox-policy` | `@deepseek-ai/dsh-sandbox-policy` | 策略解析（每会话档位 + cwd） |
| `bash-sandbox` | `@deepseek-ai/dsh-bash-sandbox` | 沙箱化 bash 执行器（win32 **disabled**） |
| `pwsh-sandbox` | `@deepseek-ai/dsh-pwsh-sandbox` | 沙箱化 pwsh 执行器（**只在 win32 启用**） |

`cordis.patch.yml:240-241` 把 `id: sandbox` 绑到 `dsh-sandbox-local`；抽象包
`@deepseek-ai/dsh-sandbox` 只是**契约缝**（`src/index.ts:207-226` 抽象类
`SandboxProvider.confine`、`:38` 三档 `SandboxMode`、`:141` `SANDBOX_UNAVAILABLE`
+ `:148 SandboxUnavailableError` 的 fail-closed 语义、`:98-133` `RunnerFailureRule` /
`ConfinedArgv.enforcement` / `denialSignatures`）。

## 2. 平台行为（这是本单最重要的一条）

```yaml
# cordis.patch.yml
240:    - id: sandbox
241:      name: '@deepseek-ai/dsh-sandbox-local'
249:    - id: bash-sandbox
251:      disabled: !!js process.platform === 'win32'
255:    - id: pwsh-sandbox
257:      disabled: !!js process.platform !== 'win32'
283:      disabled: !!js process.platform === 'win32'      # tool-bash，同批关掉
287:      disabled: !!js process.platform !== 'win32'      # tool-pwsh
```

- **`bash-sandbox` 在 win32 官方 disabled**（`:251`），连 `tool-bash` 也 disabled（`:283`）。
  所以「Windows 上没有 bash 命令级 OS 沙箱」在能力面上不低于官方。
- **`pwsh-sandbox` 是 win32 上唯一启用的沙箱化执行器**（`:257`）。它把每条 pwsh 命令
  argv 交给 `ctx.sandbox.confine`（`dsh-pwsh-sandbox/src/index.ts:4-5`、`:194-196`），
  再把 mode / enforcement / denied / runnerFailed 写进结果（`:96-140`、`:165-183`）。
- 因此「bash 在 win32 disabled ⇒ 官方 Windows 上没有命令级 OS 沙箱」是**错的外推**：
  官方 Windows 桌面上的 pwsh 命令**每一条**都过 ACL runner。

## 3. Windows runner 到底做了什么（`dsh-sandbox-windows-acl`）

- 链表：`dsh-sandbox-local/src/index.ts:184` `win32: ['windows-acl']`；`:186-192` 说明
  「唯一候选，不探针」，`:554` 按 `process.platform` 取链，无链则 `confine` fail-closed。
- 机制（`src/token.ts`、`src/acl.ts`、`src/grant.ts`、`src/workspace-sid.ts`）：
  调用者令牌复制成 `WRITE_RESTRICTED` 受限令牌，restricting SIDs 携带**每工作区
  SID**（`workspaceWriteSid`，由 canonical 路径确定性派生）与**每会话私有 temp SID**；
  令牌再降级为 **Low 完整性**；每次授权在同一 `SetNamedSecurityInfoW` 里写
  允许 ACE + 对 world SID 的 `FILE_DELETE_CHILD` 拒绝 + Low 标签。
- **官方自标 partial**：`dsh-sandbox-windows-acl/README.zh.md:127`
  「写入与删除受限；读取、网络与进程可见性不受限」；`:190-191` 读侧/网络不在范围。
- **代价是驻留改动**：`README.zh.md:130-131` 写明工作区 ACE / 拒绝项 / Low 标签
  **按设计常驻、不撤销**，`icacls` 回收不了（`ERROR_NONE_MAPPED` 1332），且 Low 标签
  存活期长于 DSH、会把目录树放宽给同用户下其他 Low 完整性进程。随包还发了
  `assets/diagnose-windows-sandbox-acl/`（45 KB PowerShell 诊断/恢复脚本）处理误伤。

## 4. 结论与边界

| id | 判定 | 依据 |
|---|---|---|
| `sandbox` #40 | **PARTIAL**（缺 runner） | 官方有真 Windows runner；本仓 `internal/sandbox` 只有策略层与路径边界，无令牌/ACL/标签层 |
| `bash-sandbox` #42 | **PARTIAL**（与官方同形） | win32 官方 disabled；本仓仍有三档策略闸门 + escalation 契约（多，不少） |
| `pwsh-sandbox` #43 | **PARTIAL**（缺 runner） | win32 官方启用且走 ACL runner；本仓有策略闸门 + escalation 契约，无 runner |

本单**没有**实现 runner，理由写在 `docs/sandbox-policy.md` §4 与
`docs/host-stable-gap-register.md` §4.6：它要往真实工作区目录树写**驻留 ACL 与
Low 标签**，是共享工作区上的破坏性改动，需要独立设计 + 独立探针（不在用户工作区里
落持久标签）+ 回滚方案，属需要用户拍板的独立单。**未到达的层级一律不写 DONE**
（`AGENTS.md`「沙盒不得冒名」）。
