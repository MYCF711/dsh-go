# 宿主残余缺口 + 已知未修缺陷（2026-11-15，`feat/host-residual`）

> 一句话：**已知未修的 4 条真缺陷全部修掉（每条都带能红的用例）**；
> base 插件面 9 条 PARTIAL / 4 MISSING 逐条复核后**状态一条没改**——
> `skill` 判定为**依赖锁定**、`agent-instructions` 两条剩余卡在"不是接线"、
> 方法面那 3 条结构化未实现保持现状（理由见 §3）。
> 口径未动：插件面 99 = **86 / 9 / 4**，方法面 223 = **220 / 3 / 0**。

## 1. `Subscribe` 退订删错人（`internal/updater/channel.go`）

**真因**：`Subscribe` 记下"注册那一刻的下标"，退订时
`append(listeners[:index], listeners[index+1:]...)`。下标是**快照**，而别人的退订
会让后面的下标整体前移：

```
A(0) B(1) --退订 A--> [B] --订阅 C--> [B, C]
退订 B 时用的还是 1  ⇒  删掉的是 C
```

结果是**退订了的人还在收通知、没退订的人再也收不到**，而且是并发不安全的
（切片原地 append 互相覆盖）。

**修**：订阅者改成 `subscriber{id, fn}`，`id` 单调递增、不复用；退订按 **id** 删，
`sync.Once` 保证幂等。投递顺序仍是注册顺序（快照语义不变）。

**能红的用例** `internal/updater/channel_subscribe_test.go`：
- 交错的 A/B/C 序列：修前 `B 已经退订，却仍收到 2 条通知`（同时 C 收 0 条）；
- 重复/乱序退订：修前 `third=2`（退了还在收）；
- 并发退订 8 个：修前唯一剩下的订阅者收 **0** 条（列表被踩坏）。

## 2. 宿主被强杀后 `.lock` 不回收（`internal/session`）

**真因**：宿主被强杀（任务管理器结束、断电）时 `claimWriter` 的 defer 一行都跑不到，
`<session>.lock` 留在盘上。重启后第一次 prompt 打开会话要等满 `sessionLockWait`（2s）
然后拒绝：

```
会话正被另一个进程写入（锁文件 …）；确认没有别的 dshd 在用这份会话后删掉它再 resume
```

**用户可见后果**：这个会话再也发不出去，而错误指向的进程**重启后根本不存在**。

**为什么回收不塞进 `claimWriter`**：`lock_wait_test.go` 的 T12 钉着
「只等、不删、不按 pid 强清」——那条契约保护的是**读不懂的锁**。把"探死 pid + 删锁"
塞进抢占路径，等于让每次普通抢占都带着删别人锁的能力。

**修**（新文件 `internal/session/stale_lock.go`）：
- `ReclaimStaleWriterLock(path)`：四条同时满足才删——① 锁内容是 `claimWriter` 自己
  写的形状（`pid=<正整数>` + `at=<RFC3339>`，多一行都不认）；② pid 不是本进程；
  ③ 那个 pid **确定已退出**（Windows 走 `OpenProcess`+`GetExitCodeProcess`，
  见 `pidalive_windows.go`）；④ 删除成功。删之前用 `os.Rename` **原子抢走**再删，
  并复核抢到的确实是刚读到的那份内容（防止"死锁刚被收走、新 writer 立刻抢占"时
  删掉新锁）。
- `OpenSessionAtRecoveringStale(path)`：只在 `errors.Is(err, ErrWriterBusy)` 时回收
  并重试**一次**；活进程的锁、认不出的锁照原样拒绝。
- 接线（两处 prompt 写入路径）：`cmd/dshd/main.go:879`（`dshd run --session`，
  serve 每轮的子进程写入者）、`cmd/dshd/serve_turn.go:394`（进程内 runner 落盘）。

**能红的用例** `internal/session/stale_lock_test.go`（7 条）：死进程回收后能真写 /
活进程锁不回收 / 六种认不出形状的锁一律不删 / 恢复路径在活锁下仍拒绝且保留
`ErrWriterBusy` 原文（有调用方按这段文本判"会话忙"）/ 并发回收只有一个赢家。
修复前的真实复现（`tmp/GO8-RC2/_hr/probe_repro`）：`OpenSessionAt 耗时 2.002s -> 会话正被
另一个进程写入`；修复后同一把锁 `reclaimed=true`、`seq=0` 可继续写。

## 3. `AppendChildTurn` 恒写 `turn=1`（`internal/agent/child_agents.go`）

**真因**：复用型 durable 子会话的每趟往返都写
`AssistantMessageEvent(1, 1, …)`。而界面 Turn 大纲与消息分组按数值 `turn` 分组
（`internal/session/turn.go` 的说明），相同的编号把好几趟往返并成同一轮。
**为什么不能直接用 `session.NextTurn`**：子会话日志里**没有 `turn/start` 帧**
（`ChildSpawn`/`AppendChildTurn` 只写 `user/message` + `assistant/message`），
数 `turn/start` 恒为 1——那正是这个 bug 的形状。

**修**：新增 `nextChildTurn(path)`（`child_agents.go:237`）＝「日志里出现过的最大
数值 turn + 1」，来源同时算 `turn/start.turn` 与各事件自带的 `turn`（两条同向单调，
不会回退/撞号）；写不进去时退 1。

**能红的用例**（`internal/agent/child_agents_test.go`）：
spawn + 两趟复用 ⇒ assistant 消息的 turn 必须是 `[1 2 3]`；
用 `origin/main` 的实现跑同一条用例：`第 2 趟往返的数值 turn = 1，期望 2（全部实得 [1 1 1]）`。

## 4. `compactauto.go` 把摘要字数写进 `TokenCount`（`cmd/dshd/compactauto.go:79`）

**真因**：`CompactionMeta.TokenCount` 落盘成 `compaction/summary.shadowedTokenCount`，
语义是"被遮蔽那段历史有多少 token"；旧实现写的是 `len([]rune(text))` = **摘要字数**。
一次压掉几万 token、摘要几十字，落盘就是 `shadowedTokenCount: 几十`。
（`serve_commands_api.go:291` 的注释早就点名过这处，`/compact` 命令因此自己另算
`compactShadowedTokens`。）

**修**（"能力已在、只差接线"）：直接复用同包的 `compactShadowedTokens(before, from, to)`
——与 `/compact` 命令**同一个**估算器（`session.Meter`）对 `[from,to]` 区间估真实体积。

**能红的用例** `cmd/dshd/compactauto_meta_test.go`：真会话 + SSE 桩网关（摘要正文故意
1 个 rune），断言落盘值 == 区间估算值。旧实现下红在
`shadowedTokenCount = 1，应为被遮蔽区间的 token 估算 838`。

## 5. 三条判断（细节见 `docs/base-plugins-partial-rest-official-shape.md` §8）

- **`#54 skill` = 依赖锁定**：官方**有**二级（per-scope）层，而且是独立机制
  （`dsh-skill/src/index.ts:345-353` 的类注释、`:362` 的
  `new ScopedLayers<SkillLayer>(…)`、`:117` 的 `scope` 选项）。本宿主没有生产者：
  `internal/modes/modes.go:16-30` 的 preset 只有
  `Tools / Approval / Sandbox`，**不承载 provider 组合**；生产装配只挂一个
  filesystem provider 在全局层（`cmd/dshd/serve_agent_api_skills.go:88-93`）。
  在没有注册者的情况下建层 = 空壳，故不做；解锁条件＝preset 开始承载 provider 组合。
- **`#53 agent-instructions` 维持 PARTIAL**：① fs 后嵌套发现/removal 要新增一条
  durable `user/message` + 跨轮状态 + resume reconcile（不是接线）；② 官方
  `<system-reminder>` 帧形状会整体翻掉生产提示词字节，两条探针按字节钉着现形状 ⇒
  属"改判据"，需 Lead 定口径后单独做。
- **方法面 3 条 `STRUCTURED_UNIMPLEMENTED` 保持现状**：
  `account.getNotifications / getNotificationContent / reportNotificationEvent` 在默认
  `provider=Local` 下回四字段 501 并点名缺什么
  （`internal/account/notifications.go:146-156`），切
  `DSHD_ACCOUNT_PROVIDER=official` 后是真实现（`cmd/dshd/serve_account_api.go:134-180`）。
  本地没有通知后端，唯一"实现"办法是伪造通知流 = 假数据，故不补。

## 6. 门禁（本机实跑，退出码见交付答复）

```bash
export PATH="/c/Program Files/Go/bin:/c/toolchains/mingw64/bin:$PATH"
CGO_ENABLED=0 go build ./...
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 go test -timeout 20m ./... -count=1
test -z "$(gofmt -l cmd internal modules codecs)"
python scripts/gap-register-counts.py --check
python scripts/text-only-guard.py
python scripts/single-source.py
python scripts/race-guard.py
staticcheck -checks=SA*,S1* ./...
```
