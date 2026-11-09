# Lead 零思考派单总表

> **2026-11-08 00:35 接手状态**：web 实例 `DeepSeek Harness.exe` PID 38484（profile `web-latest2`，端口 61528）已关闭；Team Mode T15A/T11 由本 Lead 接手，T13 独立审查阻塞。本文件中的 M0/R1/R2 仅作历史与 M0 参考，**不得重开旧 web 会话或把 web 结果当宿主 stable 证据**。当前派单入口仍是 `docs/architect-official-sync-taskbooks.md`。
>
> 用途：**Lead 换会话后只看本文件就能派单，不需要重新推架构。**
>
> 执行顺序只看 **§1 当前立即派单表** 和 **§3 完成→解锁表**。
>
> 事实基线：`9cfe2f4` 已提交，标记 `dsh-tui`；M0 静态前端已由 Lead 接线（`frontendMount(front, officialWireMount(...))`）。`M0-BOOT-CALLS-CAPTURE` 在途；官方 SPA 仍缺 `__DSH_BOOT__` + `__ModuleLoader__` + `__DSH_BOOT_READY__`。

---

## 0. 三条总规则（不要变通）

1. **执行模型固定 `group/deepseek-v4.1-flash-1017`**；只有方案师用 `group/kimi-k3.1-pro` 且只读。
2. `internal/transport/server.go`、`internal/transport/api.go`、`cmd/dshd/serve.go` **只有 Lead 写**。子代理只交实现+测试+patch+接线清单；子代理不能 commit/push。
3. 同一写作用域同时只能有一个 writer。**下表 scope 已按零重叠排好，直接按同组并发即可。**

---

## 1. 当前立即派单表（R 轮）

### R1（现在，4 路并行）

| 槽位 | task_id | 模型 | 写作用域 | 为什么现在能派 |
|---|---|---|---|---|
| A | `M0-BOOT-FACADE` | deepseek-v4.1f | `internal/webfrontend/boot*.go`、新增 `cmd/dshd/serve_boot_facade.go`+测试 | 静态服务已完成；缺的是 boot 模块图，scope 不与其他路重叠 |
| B | `M0-BOOT-CALLS-CAPTURE`（已在途，**不重派**） | deepseek-v4.1f | 只写 `%TEMP%/m0-frontend/**` | 抓包结果决定 R2 的补缺清单 |
| C | `HOST-SUBAGENT-CHILD-ID-SURFACE` | deepseek-v4.1f | `modules/tool/subagent/**`、`internal/spi/subagent.go`、`cmd/dshd/serve_agent_api_test.go` | 已知 UI 委派前拿不到 childId；纯加法、不影响 A/B |
| D | `QA-RACE-TOOLCHAIN` | deepseek-v4.1f | `scripts/race*.sh`、`docs/go-toolchain.md` | 当前 `-race` 长期 UNVERIFIED；独立补证据 |

**R1 派完即停**，不要等；四路完成通知到达后进入 R2。

### R2（由完成触发，见 §3）

| 前置完成 | 立即可派 | 并行度 |
|---|---|---|
| A 完成 | `M0-SPA-RENDER-VERIFY` | 与 B/C/D 并行 |
| B 完成 | `M0-BOOT-GAPS-FIX`（按抓包拆 2–4 个 scope） | 可 2–4 路 |
| C 完成 | `HOST-SUBAGENT-INTERRUPT-REAL` | 与 A/B/D 并行 |
| D 完成 | `RELEASE-FULL-GATE` | Lead 独占，串行 |

---

## 2. 任务书正文（可直接复制给子代理）

### A · `M0-BOOT-FACADE`

**目标**：让官方 SPA 真正渲染，而不是停在 `window.__ModuleLoader__ bootstrap facade is missing`。

**必须实现**：
1. `__DSH_BOOT__` 模块图：按官方 `module-graph` 插件产出形状，列出 SPA 启动必需模块的 URL/依赖；
2. `__ModuleLoader__` 门面：官方 `import/load` 契约；缺模块或加载失败要**响亮报错**，不能静默白屏；
3. `__DSH_BOOT_READY__`：与上面同 patch 注入（官方每条 index 都补）。

**先红**：当前真机 `GET /` 只注入两个全局，入口抛 facade missing；贴原样并 exit 1。
**后绿**：
```
go test ./internal/webfrontend -count=1
go test ./cmd/dshd -run 'Frontend|BootFacade' -count=1
go vet ./...
CGO_ENABLED=0 go build ./...
```
全部 exit 0；另附**无头 Chromium 打开官方 SPA 的截图与 console 日志**，不能再出现 facade missing。

**禁止**：改 server.go/api.go/serve.go；改其它 transport 包；把 dist 嵌入二进制。
**产出**：patch + sha256 + `%TEMP%/M0-BOOT-FACADE-RESULT.md`（含 Chromium 证据与未验项）。

### C · `HOST-SUBAGENT-CHILD-ID-SURFACE`

**目标**：客户端**委派前**能铸造/知道 child id，委派后能直接续问，不再只能 `session.list` 猜。

**要求**：
- 工具 schema 增加**可选** `childSessionId`；缺省时服务端铸造；
- 工具结果返回 `ChildSessionID`；
- 不传新字段时请求形状**逐字节不变**（golden + DeepEqual）。
**先红**：当前工具无 `childSessionId`、结果无 child id，exit 1。
**后绿**：`go test ./modules/tool/subagent ./internal/spi -count=1` exit 0；`go test ./cmd/dshd -run Subagent -count=1` exit 0；附一条真实委派→拿 childId→`subagents.prompt` 成功证据。
**禁止**：改 server.go/serve.go/api.go。
**产出**：patch + `%TEMP%/HOST-SUBAGENT-CHILD-ID-SURFACE-RESULT.md`。

### D · `QA-RACE-TOOLCHAIN`

**目标**：把 `-race` 从“本机无 gcc”变成**可执行、有结果的门禁**，不谎报。
**要求**：探测本机 MinGW/gcc；可用则跑受影响包 `-race`；不可用则安装到**用户本地工具目录**（不动系统 PATH），并把可复现命令写进 `scripts/race.sh`。
**后绿**：`scripts/race.sh ./internal/transport ./internal/officialwire ./cmd/dshd` 原样退出码；失败要分类（编译器缺失/数据竞争/测试 flake）。
**产出**：脚本 + `%TEMP%/QA-RACE-TOOLCHAIN-RESULT.md`。

---

## 3. 完成 → 解锁表（Lead 只看这张）

| 收到哪个 task 的完成通知 | Lead 立即做什么 | 随后可启动 |
|---|---|---|
| `M0-BOOT-FACADE` | 复跑 `webfrontend`+`Frontend|BootFacade` 测试；在 serve.go 接入 facade 注入（**Lead 自己写**） | `M0-SPA-RENDER-VERIFY` |
| `M0-BOOT-CALLS-CAPTURE` | 打开 `calls.jsonl`，按方法**去重并分组成 2–4 个零重叠 scope** | `M0-BOOT-GAPS-FIX` |
| `HOST-SUBAGENT-CHILD-ID-SURFACE` | 复跑 subagent 三包测试 | `HOST-SUBAGENT-INTERRUPT-REAL` |
| `QA-RACE-TOOLCHAIN` | 跑 `scripts/race.sh` 并保存日志 | `RELEASE-FULL-GATE` |
| 以上 R1/R2 全绿 | Lead 跑全量 `go test ./...` | `DOC-PARITY-FINAL` + `RELEASE-TAG-DSH-TUI` |

---

## 4. 后续任务定义（到达触发条件再派）

### `M0-SPA-RENDER-VERIFY`（A 完成后）

- **模型**：deepseek-v4.1f
- **scope**：只写 `%TEMP%/m0-spa-verify/**`
- **验收**：无头 Chromium 打开真 dshd + 官方 SPA，**首屏真实渲染**，收集 console error、失败 `/api`、白屏原因。
- **产出**：截图 + `calls.jsonl` + RESULT；不改仓库。

### `M0-BOOT-GAPS-FIX`（B 完成后）

- **拆单规则**：把抓包里非 200/501 的方法按包分 scope；每组一个代理，**最多 4 路**。
- **每组必须**：官方 file:line、先红（当前 501/错误）、后绿（真实 SPA 再抓同一调用成功）、`go test ./internal/transport ./cmd/dshd` exit 0。
- **禁止**：为让 SPA 不报错返回空数组/假数据；缺外部服务仍回官方形状 501，但**不能是启动必调方法**。

### `HOST-SUBAGENT-INTERRUPT-REAL`（C 完成后）

- **scope**：`cmd/dshd/serve_agent_api.go`、`cmd/dshd/serve_subagents_api.go`、`internal/agent/**`
- **验收**：真机 continuable 子会话正在跑 → `interruptByParent` → 当前轮真取消；同 requestId 幂等；错误父 id 403。
- **注意**：这条与 C 串行，**不能和 C 同时写 serve_agent_api.go**。

### `DOC-PARITY-FINAL`（全绿后）

- **模型**：deepseek-v4.1f
- **scope**：`docs/official-desktop-api-parity.md`、`docs/official-plugins-022-map.md`、`docs/dsh-tui-compat.md`
- **验收**：计数与代码一致；`scripts/doc-links.py` exit 0；不得把 UNVERIFIED 写成 DONE。

### `RELEASE-TAG-DSH-TUI`（最后，Lead 自己做）

顺序固定：
1. `go test ./... -count=1` exit 0；
2. `go vet ./...` exit 0；
3. `go build ./...` exit 0；
4. 按目录 add 提交；
5. `sh scripts/tag-dsh-tui.sh`。
**不允许**：`git add -A`、红灯打标、把 tag 写成版本后缀。

---

## 5. Lead 每次只做四步

1. 看 **§3** 哪些前置已完成；
2. 从 **§2/§4** 复制对应任务书；
3. 同轮派出所有 scope 不重叠的任务；
4. 收到完成通知后**亲自复跑门禁**，再回到第 1 步。

不要重新设计任务，不要等待，不要轮询；完成方会主动通知。
