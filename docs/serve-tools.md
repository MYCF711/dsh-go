# serve 接工具循环：把 cmdRun 的装配抽出来

## 现状（2026-11-03）

`dshd serve` 的 `modelRunner` 每轮**只调一次模型，不跑工具**。
所以 `serve` 能做的是对话，不是干活。

`dshd run` 早就能跑工具了——差别只在装配：

```
main.go  cmdRun
  377  subp := &subProvider{}
  378  mods, err := loadMods(*manifest, *home, *maxChars, subp)
  380  kernel.Assemble(root, mods)          // → reg（工具注册表）、guarded（带审批的执行器）
  459  loop := &agent.Loop{ Chat, Compact, Tools: reg, Runner: guarded, Project,
                            Model, System, SessionID, CompactTools, MaxSteps, Emit }
```

`Loop` 要的六样东西，`serve` 现在**一样都没有**，因为 `serve.go` 只建了
`transport.Server` 与 `promptService`。

## 要做的事（机械，但必须一次做对）

1. 在 `cmd/dshd/` 新建 `agentloop.go`，导出（包内）工厂：

   ```go
   type loopConfig struct {
       Home, Manifest, Model, Base string
       MaxSteps, MaxChars, AutoCompact int
       ToolSchema, Policy string
       SessionID string
       Emit func(agent.Event) error
   }
   func newAgentLoop(cfg loopConfig) (*agent.Loop, *session.Log, error)
   ```

2. **把 `cmdRun` 里 377–500 那段搬进去**，`cmdRun` 改成调它。
   搬完必须证明行为没变：
   - `go test ./...` 全绿
   - 真机 `dshd run --prompt "1+1"` 与搬之前逐字对照 stats 行
   - **`scripts/e2e-regression.mjs` 16 项全过**

3. `serve` 的 `modelRunner.Run` 改成：`newAgentLoop(...)` → `loop.Turn(ctx, text)`，
   `Emit` 里把 `agent.Event` 翻译成 `ServerFrame` 推给 Hub（**这是唯一真正新的代码**）。

## 三个必须避开的坑

1. **写锁只有一个**。`cmdRun` 会拿 `<会话文件>.lock`；`serve` 里已有 `persist()`
   自己开一次。接上 Loop 之后**必须删掉 `persist`**，否则自己和自己抢锁，
   表现为"存不下来（打不开写入）"——而错误信息会指向文件系统，查不到锁上。

2. **`Compact` 与 `Emit` 的副作用顺序**。压缩要写事件，事件要推给界面；
   `Emit` 里推帧、压缩另走 `runCompactor`，两边都写同一个 `logw`——
   搬的时候连同 `runCompactor` 一起搬，不要拆到两个文件里各写一遍。

3. **`--policy` 的默认值**。`run` 默认 `ask`（终端问）；`serve` 没有终端，
   `ask` 等于永远挂起。serve 必须显式取 `deny` 或 `allow`，
   并在 ready 行之外再打一行说明当前策略——**看不见的放宽与失误没有区别**。

## 验收（不许只跑单测）

```
dshd serve --home tmp/toolchk &
  → session.create
  → session.prompt "读一下 dsh.toml 第一行"
  → WS 应收到 tool/call、tool/result、assistant/message、turn/done
  → 盘上事件里要有 tool/call 与 tool/result
```

只要 WS 里没有 `tool/call`，就是没接上——**不要拿"回答里提到了文件内容"当证据**，
模型可以凭上下文猜。
