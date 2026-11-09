# 宿主交付 · 独立 worktree 纪律（HS-WORKTREE）

> **task_id**：`HS-WORKTREE` · **owner**：`qa` · **model**：`group/deepseek-v4.1-flash-1017` · **parent**：`lead` · **depth**：1
> **worktree**：`D:/DeepSeek Harness/default-workspace/hs-worktree`（分支 `task/HS-WORKTREE`）
> **声明 base**：`9cfe2f4949d2d19a243d741160ce290f16d4d3b1`（`feat: 官方 0.2.2 接口面对齐 + dsh-TUI 直连`）
> **生效时间**：2026-11-07 23:31（+08:00）· **规则从派单当刻生效**，不等本审计任务完成才执行
> **依据**：[architect-official-sync-taskbooks.md](architect-official-sync-taskbooks.md) §2 `HS-WORKTREE`、[architect-official-sync-blueprint.md](architect-official-sync-blueprint.md) §5
> **产物**：本文件 + `$S/HS-WORKTREE/worktree-ledger.json` + `$S/HS-WORKTREE/RESULT.md` + `$S/HS-WORKTREE/replay/logs/**` + `NO_WORK.md`
> **GO.2 批次账本（T07，2026-11-08）**：`tmp/GO2-TEAM/T07/worktree-ledger.json`（生成器 `tmp/GO2-TEAM/T07/collect.py`，可零依赖复跑）
> **本文档不改产品代码**：只有本文件一个新增文件，`NO_WORK.md` 记录“无产品 patch”的证明。

---

## 0. 一句话

**没有独立 worktree + 声明 base 的交付，不算交付；patch 在声明 base 上 `git apply --check` 不为 0 的交付，不许集成。**
规则不靠自觉：每个任务在 `$S/<task_id>/BASE.json` 写六个字段（top-level / branch / base rev / status / scope / patch sha256），Lead 在集成 worktree 重放 `git apply --check` 通过才标 `INTEGRATED_GREEN`。

变量约定（全仓库统一，禁止各任务自造）：

```bash
export G="D:/DeepSeek Harness/default-workspace/DSH-GO"          # 共享树（只读）
export S="C:/Users/Administrator/AppData/Local/Temp/HOST-STABLE-20261107"
export W="D:/DeepSeek Harness/default-workspace/hs-worktree"     # 本任务 worktree
export PATH="/c/Program Files/Go/bin:$PATH"
```

---

## 1. 三态定义（唯一合法取值）

`worktree-ledger.json` 的 `status` 只允许这三个值，**没有第四种写法**（`DONE`、`PARTIAL`、`绿`、`OK` 都不合法）：

| 取值 | 判定 | 后果 |
|---|---|---|
| `COMPLIANT` | 六个字段全部以**命令输出**形式留档，且 patch 在声明 base 上 `git apply --check` exit 0，且 patch 可由声明 worktree 逐字节重放 | 允许进入集成队列 |
| `NON_COMPLIANT` | 缺任一字段，或 patch 无法在声明 base 复现，或交付直接落在共享树 / 无独立 worktree | **禁止自动集成**；只能由 Lead 逐 hunk 人工归属，并写回滚路径 |
| `UNVERIFIED` | 交付尚未发生或仍在写入（在途 / 无 patch / 无 RESULT） | **禁止集成**；不写“部分完成”，也不许用“进行中”洗绿 |

**三态之外的补充规则：**

1. `UNVERIFIED` ≠ 通过。全仓 `FAIL=0` 不能把某个 `UNVERIFIED` 顶成 `COMPLIANT`。
2. 一旦某任务被记 `NON_COMPLIANT`，**只有 Lead 能改回**，且必须附人工归属结论（谁接的、接哪几个 hunk、回滚命令）。
3. 既有共享树交付**如实记 `NON_COMPLIANT`，不得洗绿**；它们的产品质量由各自的定向门另行证明，与本纪律账互不抵扣。

---

## 2. 根因：为什么必须这么做（实测证据，不是理论）

2026-11-07 23:31–23:40 对本工作区实测（命令与日志见 §9）：

| 观测 | 数字 | 含义 |
|---|---|---|
| 共享树 `git status --short` | **95 项**（44 `M` / 2 `D` / 49 `??`）；审计窗口内漂到 **108 项**（45 `M` / 2 `D` / 61 `??`） | 共享树同时承载 7+ 个任务的未提交改动，且**在审计期间仍在增长**（并发写者） |
| 共享树 HEAD | `9cfe2f4` | HEAD 干净，但工作树与 HEAD 不等价 |
| 六份 patch 串行应用 | 6/6 `apply_exit=0`，`1035 → 1057` 文件 | patch 本身可干净叠加 |
| 集成树（base+6 patch） vs 共享树 | **40 个 `.go` 文件不同** | 共享树里有**不在任何 patch 中**的改动 |
| `cmd/dshd/serve_turn.go` | 共享树 = base + HS-04 接线 + HS-06 接线；HS-06 patch 只含后者 | 同文件双写者，patch 单独无法复现共享树 |

**结论**：patch base 对不上，不是 patch 写坏了，是**共享树被当成工作区**。只要还有人在共享树直接改，任何 `git diff` 生成的 patch 都只能描述“当时那一刻”的差分，下一个人一改就失效；同文件双写者还会让两个 patch 对同一 hunk 各执一词（`serve_turn.go` 已实证）。

**已确认的实测反例**（`HS-06` 的 `WIRING.md` §4.0 自己就写明了）：

```text
| HS-04（WIRING §1，Lead 落盘） | serve_turn.go:109 | retrier := &offloadRetrier{access: …} | 已在共享树 |
| HS-06（本任务）              | serve_turn.go:119 | checkpointBeforeModelStep(...)          | 在 task/hs-06 分支 |
```

即：`HS-04` 的 1 行接线只存在于共享树，不在 `HS-04.patch` 里。集成时如果只 `git apply HS-04.patch`，编译通过但 **offload 闭环不生效**（静默功能缺失，比编译红更危险）。

---

## 3. 派单模板（Lead 侧，派单前 60 秒）

> 派单四件套（`LEAD.md` §3.2）+ 本纪律的 worktree 前置。**worktree 是派单前置，不是交付后补票。**

```bash
# 1) 先核现场：共享树 HEAD 必须等于要声明的 base，且该 base 已被 Lead 验收
cd "$G" && git rev-parse HEAD            # 期望：<base_rev>

# 2) 建独立 worktree（分支名固定 task/<task_id>，一个 task_id 一个 worktree）
git worktree add "$W" -b "task/<task_id>" "$(git -C "$G" rev-parse HEAD)"

# 3) 立刻落 BASE.json 六字段（执行者开工第一步，早于任何写入）
cd "$W"
mkdir -p "$S/<task_id>"
{
  printf '{\n'
  printf '  "task_id": "%s",\n' "<task_id>"
  printf '  "top_level": "%s",\n' "$(git rev-parse --show-toplevel | sed 's|\\|/|g')"
  printf '  "branch": "%s",\n' "$(git branch --show-current)"
  printf '  "base_rev": "%s",\n' "$(git rev-parse HEAD)"
  printf '  "status": %s,\n' "$(git status --short | python -c 'import json,sys;print(json.dumps(sys.stdin.read(),ensure_ascii=False))')"
  printf '  "scope": ["<认领路径1>", "<认领路径2>"],\n'
  printf '  "applied_patches": []\n'
  printf '}\n'
} > "$S/<task_id>/BASE.json"
python -m json.tool "$S/<task_id>/BASE.json" > /dev/null; echo "BASE_JSON_EXIT=$?"   # 必须 0
```

**派单硬门（缺一项不派）：**

1. `task_id / owner / model / 写作用域 / 依赖 / 先红 / 验收 / 产出` 八项齐全（`LEAD.md` §3.2）。
2. worktree 已建、`BASE.json` 六字段已落盘、`python -m json.tool` exit 0。
3. 同文件无其他 active writer（`list_agents` + 账本 + 各 worktree `git status`）。
4. 声明 base 必须是**已验收的 HEAD**，不是“某个旧提交”。
5. 若 base 是“base + 已应用 patch 栈”，必须把每个 patch 的 `path` 与 `sha256` 写进 `applied_patches`（`HS-16A` 已按此格式执行，见 `$S/HS-16A/BASE.json`）。

---

## 4. 交付模板（执行者侧）

### 4.1 只交 patch，不 commit / 不 push / 不写共享树

```bash
cd "$W"
# 只 add 认领路径；禁止 git add -A
git add -N -- <认领路径...>

# 生成 binary patch（新文件必须进 patch，空 patch 不算交付）
git diff --binary -- <认领路径...> > "$S/<task_id>/<task_id>.patch"
test -s "$S/<task_id>/<task_id>.patch" || { echo "EMPTY_PATCH"; exit 1; }

# sha256 双写（文件 + 回填 RESULT）
sha256sum "$S/<task_id>/<task_id>.patch" | tee "$S/<task_id>/<task_id>.patch.sha256"
```

### 4.2 六个字段必须出现在 RESULT.md 里（贴命令输出，不写“见上”）

```bash
git rev-parse --show-toplevel     # top-level（worktree 绝对路径）
git branch --show-current         # branch（必须是 task/<task_id>）
git rev-parse HEAD                # base rev（40 位）
git status --short                # status（原样贴，行数要与认领文件数对上）
sha256sum "$S/<task_id>/<task_id>.patch"   # patch sha256
# scope：认领路径逐条列出，并与 git status 的文件集合一致
```

### 4.3 交付时自证“patch 确实来自我的 worktree”

别人（包括 Lead）会用这条命令只读复核，**残差不许留到集成时才暴露**：

```bash
# 用临时 index 重放，绝不写别人 worktree 的真实 index
IDX="$(mktemp)"; cp "$(git -C "$W" rev-parse --git-path index)" "$IDX"
( cd "$W" \
  && GIT_INDEX_FILE="$IDX" git add -N -- <认领路径...> \
  && GIT_INDEX_FILE="$IDX" git diff --binary -- <认领路径...> ) > /tmp/regen.patch
[ "$(sha256sum < /tmp/regen.patch | cut -d' ' -f1)" = "$(sha256sum < "$S/<task_id>/<task_id>.patch" | cut -d' ' -f1)" ] \
  && echo "PROVENANCE_MATCH" || echo "PROVENANCE_MISMATCH"
```

### 4.4 跨作用域接线只写 `WIRING.md`

`internal/transport/server.go`、`internal/transport/api.go`、`cmd/dshd/serve.go`、`cmd/dshd/main.go` 只有 Lead 能写。执行者需要接线时：

- 写 `WIRING.md`：`file:line` + 可直接粘的 hunk + 为什么必须由 Lead 串行；
- **不许**“顺手接线”，也不许把接线塞进自己的 patch 冒充同作用域改动；
- 若接线 hunk 只存在于共享树（如 `HS-04` 的 `serve_turn.go:109`），必须在 `WIRING.md` 里点名“**不在我的 patch 里**”，否则集成方会误以为 patch 已包含。

---

## 5. 集成模板（Lead 串行，唯一入口）

> 集成 = 在**独立集成 worktree 里从声明 base 重放全部 patch**，不是把 patch 打到共享树上看运气。

```bash
# 1) 建集成 worktree（从声明 base 起，与共享树隔离）
export I="$S/INTEGRATION"
rm -rf "$I"; mkdir -p "$I"
( cd "$G" && git archive <base_rev> ) | tar -x -C "$I"
cd "$I" || exit 1

# 2) 按依赖顺序串行 apply，每一步都先 --check 再 apply
for pair in "HS-01:$S/HS-01/HS-01.patch" "HS-03:$S/HS-03/HS-03-auto-title-deltas.patch"; do
  n="${pair%%:*}"; p="${pair#*:}"
  git apply --check --binary "$p" > "$S/INTEGRATION/logs/check-$n.log" 2>&1; c=$?
  git apply         --binary "$p" > "$S/INTEGRATION/logs/apply-$n.log" 2>&1; a=$?
  echo "STEP $n check_exit=$c apply_exit=$a"
  test "$c" -eq 0 -a "$a" -eq 0 || { echo "INTEGRATION_RED at $n"; break; }
done

# 3) 集成树自己的门（必须写真实 EXIT，禁管道）
export GOCACHE="$S/INTEGRATION/.gocache" GOTMPDIR="$S/INTEGRATION/.tmp"
mkdir -p "$GOCACHE" "$GOTMPDIR"
CGO_ENABLED=0 go build ./... > "$S/INTEGRATION/logs/build.log" 2>&1; echo "BUILD_EXIT=$?"
CGO_ENABLED=0 go vet   ./... > "$S/INTEGRATION/logs/vet.log"   2>&1; echo "VET_EXIT=$?"
```

**集成硬门：**

1. 每个 patch `check_exit=0` **且** `apply_exit=0`；任一非 0 即 `INTEGRATION_RED`，不许 `--3way` 硬塞、不许 `patch -p1 --forward` 蒙混。
2. 集成树 `BUILD_EXIT=0` 且 `VET_EXIT=0`（真实 EXIT，不许用管道最后一个命令的退出码）。
3. 集成树与共享树的差异必须**逐条解释**：差异里凡是“不在任何 patch 中的 `.go` 改动”，都要写清归属（谁写的、回滚命令）。本次实测有 40 个这样的文件（见 §9）。
4. 全绿后才标 `INTEGRATED_GREEN`，并回填 `worktree-ledger.json` 的 `integration` 字段。
5. 合并只 `git merge --no-ff task/<task_id>`；**禁止 `git add -A`**，禁止 `reset --hard` / `clean -fd` 覆盖现场。

---

## 6. 回滚矩阵（每一级都要能退）

| 层级 | 回滚动作 | 影响面 | 不可逆风险 |
|---|---|---|---|
| 单个 patch 未集成 | 什么也不用做，删 worktree：`git worktree remove "$W" --force` | 只影响该 worktree 目录与 `refs/heads/task/<id>` | 无（未 commit 的改动随目录删除，先确认 patch 已落 `$S`） |
| patch 已 apply 未 commit | `cd "$I" && git apply -R --binary "$S/<id>/<id>.patch"` | 集成 worktree 工作区 | 无 |
| 已在集成分支 commit | `git revert <sha>`（**不许** `reset --hard` 改写已推送历史） | 集成分支 | revert 可再 revert |
| 合并进 main 后发现 | `git revert -m 1 <merge_sha>` | main | 需要 Lead 在新 commit 上重跑门禁 |
| 共享树被直写的 hunk | **不自动回滚**：先 `git diff -- <file>` 确认归属，再 `git checkout -p -- <file>` 逐 hunk 退 | 共享树 | **禁止** `git checkout -- .` / `git reset --hard` / `git clean -fd`（会清掉别人未提交的现场） |
| worktree 元数据损坏 | `git worktree repair` / 手工删 `.git/worktrees/<id>` 后重加 | 仅 worktree 注册 | 先确认无 active writer |

**回滚前必须报影响面**（`AGENTS.md` R08）：涉及几个文件、多少行、谁还在用、怎么恢复。

---

## 7. 禁止清单（违反即 `NON_COMPLIANT`）

| # | 禁止 | 原因 |
|---|---|---|
| 1 | 直接改共享 main / 共享工作树 | 造成 patch base 对不上（本次实测 95 项脏改动） |
| 2 | `git add -A` / `git add .` | 会扫进别人未完成的改动（2026-11-06 事故 `3591b65`） |
| 3 | `git reset --hard` / `git clean -fd` 覆盖现场 | 不可逆，且清掉他人未提交现场 |
| 4 | 同文件多写者 | 同 hunk 冲突（`serve_turn.go` 已实证） |
| 5 | 执行者改 `server.go` / `api.go` / `serve.go` / `main.go` | 共享接线只由 Lead 串行 |
| 6 | 交付无 `BASE.json` / 无 patch sha256 | 无法复现，无法回滚 |
| 7 | 用管道最后一个命令的退出码当门禁结果 | `cmd \| head` 会把真实红变成 0（2026-11-07 踩过） |
| 8 | 把 `UNVERIFIED` 写成通过 | 三态不是修辞 |
| 9 | 移动 / 清理他人 worktree 与现场 | 只读；要动先报影响面 |
| 10 | 在共享树跑全量门禁（`check.sh` / `regress.sh`） | 会 `mv modules`、全局 taskkill；只在独占副本跑 |

---

## 8. 单写者与共享接线

1. **同一文件同一时刻只有一个 active writer**；并行任务各自独立 worktree，交付 patch，Lead 串行集成。
2. **共享接线（`server.go` / `api.go` / `serve.go` / `main.go`）只由 Lead 串行**；执行者交 `WIRING.md`。
3. 执行者再派子代理时：写权限只能是父任务子集、同文件单写者、深度 ≤ 3、单父并发 ≤ 4；父级复验后再通知根 Lead。
4. 需要“并行写同一文件”时，先拆成不同文件，拆不开就串行——**不接受“冲突概率低”作为理由**。

---

## 9. 本次审计结果（快照 2026-11-07 23:31–23:40 +08:00）

机器可读账本：`$S/HS-WORKTREE/worktree-ledger.json`（逐项含字段证据、patch sha256、回滚命令、缺口）。

| task_id | worktree / branch | base | patch sha256 | base 上 `apply --check` | 与共享树一致性 | 判定 |
|---|---|---|---|---|---|---|
| `HS-01` | **无**（共享树直写） | `9cfe2f4` | `25b43ea3…835f87` | 0 | 12/12 文件 == base+patch | **NON_COMPLIANT** |
| `HS-03` | `DSH-GO-hs03-auto-title` / `task/HS-03` | `9cfe2f4` | `862df690…873d47` | 0 | 3/3 | `COMPLIANT` |
| `HS-04` | **无**（共享树直写） | `9cfe2f4` | `05f38264…b34484` | 0 | 4/4（接线 hunk 在共享树、不在 patch） | **NON_COMPLIANT** |
| `HS-05` | `hs-05` / `task/hs-05` | `9cfe2f4` | `b6f4794e…0a75fd` | 0 | 20/20 | `COMPLIANT` |
| `HS-06` | `hs-06` / `task/hs-06` | `9cfe2f4` | `5e9cb1c2…55c1dc` | 0 | 2/3（`serve_turn.go` 与 HS-04 双写） | `COMPLIANT`（集成需人工排序） |
| `HS-10` | `hs-10` / `task/hs-10` | `9cfe2f4` | 重写中（23:33 更新） | 0 | 未入树（22 文件缺） | **UNVERIFIED** |
| `HS-11` | **无**（共享树直写） | `9cfe2f4` | `57e4e372…a59f14` | 0 | 9/9 | **NON_COMPLIANT** |
| `HS-16A` | `hs-16a` / `task/HS-16A` | `9cfe2f4` + 4 patch | 无 patch | n/a | — | **UNVERIFIED**（在途） |
| `HS-GOAL-TRUTH` | `hs-goal-truth` / `task/hs-goal-truth` | `9cfe2f4` | 无 patch | n/a | — | **UNVERIFIED**（在途） |
| `HS-WORKTREE` | `hs-worktree` / `task/HS-WORKTREE` | `9cfe2f4` | 本任务 | 0（重放见下） | 仅新增本文档 | `COMPLIANT`（待 Lead 复验） |

**统计**：`COMPLIANT` 4（HS-03 / HS-05 / HS-06 / HS-WORKTREE）· `NON_COMPLIANT` 3（HS-01 / HS-04 / HS-11）· `UNVERIFIED` 3（HS-10 / HS-16A / HS-GOAL-TRUTH）。

### 9.1 未处置缺口（Lead 必须逐条处理，不许默认绿）

1. **共享树未提交改动持续增长**：审计快照 95 项（44 `M` / 2 `D` / 49 `??`），窗口结束时 108 项（45 `M` / 2 `D` / 61 `??`）；其中 **40 个 `.go` 文件不在任何已交付 patch 中**。集成基线不可复现且每次读取都可能变化；要么补 patch，要么人工归属并写回滚。
2. **`cmd/dshd/serve_turn.go` 双写者**：共享树 = base + HS-04 接线（`retrier := &offloadRetrier{access: …}`）+ HS-06 接线（`checkpointBeforeModelStep`）。`HS-06.patch` 只含后者，`HS-04.patch` 两者都不含。只应用 patch 会**编译通过但 offload 闭环不生效**。
3. **`HS-04` / `HS-01` / `HS-11` 无独立 worktree**：`git worktree list` 只有 `hs-05 / hs-06 / hs-10 / hs-16a / hs-goal-truth / hs-worktree / DSH-GO-hs03-auto-title`。三者只能按 `NON_COMPLIANT` 走人工归属。
4. **`HS-10` 无 `RESULT.md`**（只有 patch + 日志），且 patch 在 23:33 被重写（`213519 → 219716` 字节，sha256 `257cc1dc… → 074ba966…`）；`.sha256` 已同步，但**没有 base/branch/scope 声明**，在途不许集成。
5. **`HS-16A` 的声明基是 patch 栈**（base + HS-01/03/06/11，四个 sha256 已记录，格式正确）；它的真实 patch 还没生成，`UNVERIFIED` 期间不许并入 A 域矩阵。
6. 旧交付的六个字段里，`top-level` 一项**全部**以 worktree 路径散文形式出现，**没有一份是 `git rev-parse --show-toplevel` 的原样输出**。本次按“可由他人只读重放”判 `COMPLIANT`，但新任务起必须贴命令输出。

---

### 9.2 GO.2 批次刷新（T07 · 2026-11-08 08:25 +08:00）

> 机器可读账本：`tmp/GO2-TEAM/T07/worktree-ledger.json`
> 生成器：`tmp/GO2-TEAM/T07/collect.py`（可零依赖复跑：`python -B tmp/GO2-TEAM/T07/collect.py`）
> 原始快照：`tmp/GO2-TEAM/T07/raw/{patches,worktrees,shared-tree-attribution}.json`
> 本快照**只读**共享树与各 worktree，不改任何产品文件、不 reset/clean。

#### 9.2.1 本批 13 个任务

| task_id | worktree | base | patch | apply-check(干净 base) | 判定 |
|---|---|---|---|---|---|
| `T01` | `.agents/worktrees/t01-workspace-initdefault`（7 dirty） | `9cfe2f4` | 未交 | — | `UNVERIFIED` |
| `T02` | `.agents/worktrees/t02-settings-describe`（4 dirty） | `9cfe2f4` | `T02/T02.patch` | 0 | `COMPLIANT` |
| `T03` | `../go2-t03-schedule`（4 dirty） | `9cfe2f4` | `T03/t03-schedule.patch` | 0 | `COMPLIANT` |
| `T04` | `.agents/worktrees/hs-16a-t04`（11 dirty） | `9cfe2f4` | 未交 | — | `UNVERIFIED` |
| `T05` | `../go2-t05`（**216 dirty**） | `9cfe2f4` | 未交 | — | **`NON_COMPLIANT`** |
| `T06` | 无（证据任务，只写 `tmp/`） | — | 无产品 patch | — | `COMPLIANT` |
| `T07` | 无（本任务） | — | 无产品 patch | — | `COMPLIANT` |
| `I01` | 无（integrator 直写共享树，按设计） | — | — | — | `COMPLIANT`（10 份原样日志） |
| `I02` | 无 | — | — | — | `UNVERIFIED` |
| `I03` | 无 | — | — | — | `COMPLIANT`（11 份原样日志） |
| `I04`–`I06` | 无 | — | — | — | `UNVERIFIED` |

**`T05` 是本批唯一的产品侧 `NON_COMPLIANT`**：`../go2-t05` 有 **216 个脏文件**，其中 206 个与共享树当前未提交集合重叠——也就是说这棵 worktree 里装的是**整棵共享树的现场**，不是 T05 自己的作用域。任何从它生成的 patch 都会夹带别人半成品。处置见 §9.2.4。

#### 9.2.2 旧账收口（HS-01 / HS-04 / HS-11 的 `NON_COMPLIANT` 逐项归属）

归属判据：把 patch 应用到**干净 base**，再逐文件比对共享树（暂存层 + 工作树层，两层都看；新增文件比 blob、删除文件比缺席）。

| task_id | patch 文件数 | 与 base+patch 一致 | 一致在哪层 | 判定 | 结论 |
|---|---:|---:|---|---|---|
| `HS-01` | 12 | 12/12 | 暂存 12 | `COMPLIANT` | **人工归属完成**：共享树暂存层就是 base+HS-01.patch |
| `HS-03` | 3 | 3/3 | 暂存 3 | `COMPLIANT` | 同上 |
| `HS-04` | 4 | 4/4 | 暂存 4 | `COMPLIANT` | 同上（`serve_turn.go` 的接线 hunk 已在暂存层，双写问题见 `HS-06`） |
| `HS-05` | 20 | 20/20 | 暂存 20 | `COMPLIANT` | 同上 |
| `HS-06` | 3 | 2/3 | 暂存 2 | **`NON_COMPLIANT`** | `cmd/dshd/serve_turn.go` 已被后续写者改过，不能按 HS-06 认领 |
| `HS-10` | 23 | 23/23 | 暂存 23 | `COMPLIANT` | 人工归属完成（旧账 §9.1-4 的"无 RESULT"已由 patch 本身补齐可复现性） |
| `HS-11` | 9 | 6/9 | 暂存 6 | **`NON_COMPLIANT`** | `serve_goal_override.go` / `serve_goal_round.go` / `serve_goal_round_auto_test.go` 已被 HS-GOAL-TRUTH 系重写 |
| `HS-16A` | 4 | 3/4 | 暂存 3 | **`NON_COMPLIANT`** | `cmd/dshd/serve_agent_api_test.go` 已被 T04 系改动 |
| `HS-16D` | 5 | 5/5 | **仅工作树 5** | `COMPLIANT` | 归属完成，但**只落在工作树、没进 index**——`git add` 前谁都不能声称"已集成" |
| `HS-GOAL-TRUTH` | 13 | 0/13 | — | **`NON_COMPLIANT`** | patch **在干净 base 上 apply 失败**（`serve_goal_override.go` 冲突）：它实际依赖 HS-11 前置，属未声明依赖链的 patch stack |
| `HS-WORKTREE` | 1 | 1/1 | 暂存 1 | `COMPLIANT` | 本文档本身 |

**统计**：旧账 11 项 → `COMPLIANT` 7 · `NON_COMPLIANT` 4。相比旧快照（`COMPLIANT` 4 / `NON_COMPLIANT` 3 / `UNVERIFIED` 3），**HS-01 / HS-04 / HS-11 三条旧 `NON_COMPLIANT` 里，HS-01 / HS-04 已凭"共享树 == base+patch"的人工归属转正；HS-11 仍有 3 个文件被别人改过，不转正**。

#### 9.2.3 共享树未归属差异（原 §9.1-1 的正式收口）

| 层 | 总数 | 在已交付 patch 里 | 不在任何 patch 里 |
|---|---:|---:|---:|
| 暂存（index） | 136 | 91 | **45** |
| 未暂存 | 58 | 19 | **39** |
| 未跟踪 | 93 | 11 | **82** |

45 个暂存孤儿已逐条给出**建议归属**（`worktree-ledger.json.orphanAttribution.staged.items`，每条含 path / suggestedOwner / 依据 / 回滚命令）：

| 建议归属 | 条数 | 依据 |
|---|---:|---|
| Lead / architect | 13 | 派单与方案文档 + `docs/history/**` + 定时复核产物 |
| HS-02（mcp 插件面） | 11 | `cmd/dshd/mcp*` + `modules/tools/mcp*` |
| Lead（Web 接手） | 10 | `serve_boot_facade*` / `serve_frontend*` / `internal/webfrontend/**`（M0，可选线） |
| Lead | 5 | `AGENTS.md` / `LEAD.md` / `PLAN.md` / `UIPLAN.md` / `dsh.toml` |
| HS-16A（session 域 stream 面） | 5 | `internal/transport/official_stream*` |
| qa（HS-00） | 1 | `docs/host-stable-gap-register.md` |

**这些是 SUGGESTED，不是结论**：按 §1 规则，只有 Lead 确认后才算人工归属。未暂存的 39 条里有 4 条命中 `T01` 的写作用域（`handlers_workspace_official*` / `serve_workspace_api*`），属**在途任务自己的 patch**，patch 到达即自动覆盖；其余 35 条已按历史任务/模型面/wire 面分组，仍有 2 条 `internal/transport/handlers_pluginmanager_official*` 尚无归属，需 Lead 逐文件认领。

#### 9.2.4 本批必须处理的 3 个阻断

1. **`T05` worktree 污染（216 dirty）**：在它变成"只含 T05 作用域"之前，它的任何 patch 都不能进 I0x。要么重建 worktree（`git worktree remove` 前先确认没有未保存产物），要么按作用域逐文件重生成 patch。**恢复路径**：`git -C ../go2-t05 status --porcelain` 核对 206 个重叠文件 → 只保留 `handlers_job_terminal_official*` / `serve_terminal_api*` → 其余 `git checkout -- <path>`（逐文件，不 reset）。
2. **`HS-16D` 只在工作树、未进 index**：5 个文件全部"工作树 == base+patch，index == base"。这不是"已集成"，而是**漏 add**。恢复路径：Lead 确认后 `git add` 这 5 个文件并核 `git diff --cached`；退出：`git restore --staged <path>`。
3. **`HS-GOAL-TRUTH` patch stack 未声明**：在干净 base 上 apply 失败。恢复路径：在 `BASE.json` 里补 `dependsOn: ["HS-11/hs11-modified.patch"]` 并用 `base + 依赖链` 重放 `apply --check`；否则它不能作为"独立可集成单元"。

#### 9.2.5 本任务自己踩到的 3 个方法学坑（已修，写下来防复发）

1. **在非 git 仓库目录跑 `git apply` = 假绿**：每条都会打印 `Skipped patch '…'` 然后 **exit 0**，实际一个字节都没改。第一版收集器因此把全部 patch 判成 `apply --check=0`。现在 scratch 目录一律先 `git init -q`，并核对 `patchedBlob != pre-image blob`。
2. **`git archive` 必须按字节读**：用文本模式接 tar 流会毁掉内容，物化出来的文件不对、后续比对全错。现在改成逐文件 `git show HEAD:<path>` 写原始字节。
3. **scratch 不能放在 module 里**：Go 的 `./...` 不认 `.gitignore`，只跳过 `testdata` 与 `.` / `_` 开头的目录。2026-11-08 08:11 I03 跑 `go vet ./...` 的唯一失败就指向 `tmp/GO2-TEAM/T07/scratch/probe/cmd/dshd`。现在 scratch 固定在 `C:/Users/Administrator/AppData/Local/Temp/go2-t07-scratch`（module 外，跑完即删）。

**验证（本任务收尾实测）**：

```text
$ go list ./... | grep -c GO2-TEAM
0
$ go vet ./...
VET_EXIT=0   （0 行输出）
```

> 附带发现（不是本任务造的）：`go list ./...` 里还有 **16 个历史 `dshgo/tmp/*` 包**（`tmp/abispike/host`、`tmp/chk`、`tmp/demo` …）。它们会进 I06/HS-17 的 `go vet ./...` / `go test ./...`。本任务只报不改（不在写作用域），I06 前需 Lead 决定是否把 `tmp/` 从 module 排除或清点归属。

---

### 9.3 GO.2 最终账本（T07R · 2026-11-08 12:08 +08:00）

> 机器可读账本：`tmp/GO2-TEAM/T07R/worktree-ledger.json`
> **sha256 = `7bc1379dd065fbebe1b9fd18ba6b219b4ad6c0293b26ddf41a148982af26b0e3`**（115,566 B）
> 生成器：`tmp/GO2-TEAM/T07R/collect_t07r.py`（import 并扩展 §9.2 的 `collect.py`）
> 逐条裁定：`tmp/GO2-TEAM/T07/LEAD-ADJUDICATION.md`

| 项 | 结果 |
|---|---|
| 12 份 patch | `INTACT` 6（T02/T05/T09/T11/T13/T14）· `SUPERSEDED_DOCUMENTED` 6（T01/T03/T04/T08/T10/T12）· **`DRIFT_UNEXPLAINED` 0** |
| 分层链 | `goal-lock`：INDEX→T08(`06abc78b…`)→T12(`db8ac184…`)=共享树 **true**；`conpty`：HS-08(`64270136…`)→T10(`897e543d…`)→T13(`06753ae6…`)=共享树 **true** |
| I 系列 | I01–I14 共 13 个证据目录，全部有 RESULT + logs |
| T05 纠偏 | 旧 `go2-t05`（218 dirty）保留未动、不作交付；`go2-t05-clean`（9cfe2f4 新建）产出的 T05.patch 打到干净 base 与共享树 **4/4 逐字节相同** |
| HS-16D | **`COMPLIANT_CONTENT` / `STAGING_PENDING_RELEASE`**：patch 产出 vs `hs-16d` 交付 worktree **5/5 相同**；4 文件已被 T05 取代；**未为账面 `git add`** |
| 环境 | `go list ./...` **GO2-TEAM 命中 0**；`tmp/GO2-TEAM` 下 `.go` / `.git` **0 个**；module 内 scratch **0 个** |
| 共享树 | 286 行：staged 136 / unstaged 73 / untracked 96（MM 重叠 19）；孤儿 staged 45 / unstaged 41 / untracked 91 全部有分组裁定 |

**结论**：T07R 账本取代 §9.2 成为 GO.2 最终合规账；§9.2 保留为 08:25 中间快照。I06B 解锁条件 = 接受 `LEAD-ADJUDICATION.md` + Lead 完成其 §5 的 5 项签发。

---

## 10. 重放记录（命令真的跑过，日志在 `$S/HS-WORKTREE/replay/logs/`）

| 日志 | 内容 | 结果 |
|---|---|---|
| `integration-chain.log` | base `9cfe2f4` → HS-01 → HS-03 → HS-04 → HS-05 → HS-06 → HS-11 串行 `--check` + `apply` | 6/6 `check_exit=0 apply_exit=0`，`1035 → 1057` 文件 |
| `stack-full-build.log` | 集成树 `CGO_ENABLED=0 go build ./...` | `BUILD_EXIT=0` |
| `stack-full-vet.log` | 集成树 `CGO_ENABLED=0 go vet ./...` | `VET_EXIT=0` |
| `check-<task>-on-base.log` | 每份 patch 在干净 base 上的 `git apply --check --binary` | 8/8 `EXIT=0`（含 `hs11-modified.patch`） |
| `regen-<task>.patch` | 用**临时 index** 从各自 worktree 重放 patch | HS-03/05/06/10 四份 sha256 逐字节相同 |
| `stack-16a-chain.log` | HS-16A 声明基（base + HS-01/03/06/11）重放 | 4/4 `check_exit=0 apply_exit=0` |
| `tmp/GO2-TEAM/T07/raw/patches.json` | 全仓 23 份 patch 的文件清单 + sha256 + 字节数 | 逐份解析，含 1 份纯 unified diff（HS-GOAL-TRUTH） |
| `tmp/GO2-TEAM/T07/collect.log` | T07 账本收集全程（worktree 扫描 → 逐 patch 干净 base 重放 → 两层归属比对） | `T07_LEDGER tasks=24`，见 `ledger-summary.txt` |

> 重放全部在 `$S/HS-WORKTREE/replay/**` 内的**独立副本**里进行，共享树与其他 worktree **零写入**（`git status` 可核：本 worktree 只有本文档一个新增文件）。

---

## 11. 未验项（不写通过）

1. **真实 GUI / 官方桌面端**：本纪律只管交付流程，不解锁 M0/Electron 直连，也不代表 L1 153 方法已真实现。
2. **`-race`**：本次只有 `build` / `vet`，没有跑 `-race`（本任务无产品代码，不需要；但任何带产品 patch 的任务仍必须附真实 race 日志与 EXIT）。
3. **`HS-16A` / `HS-GOAL-TRUTH` / `HS-10` 的最终 patch**：在途，`apply --check` 未验 → 保持 `UNVERIFIED`。
4. **共享树 40 个 `.go` 差异的归属**：未逐文件判给具体任务（本任务写作用域只到本文档与 `$S/**`，不得改产品与他人现场）；由 Lead 按 §9.1 逐条处理。
5. **`worktree-ledger.json` 的自动校验**：本次是人工 + `python -m json.tool`；建议后续接 `python -B scripts/doc-links.py` 同级的 schema 守卫，但本任务不造产品脚本。
6. **T07 快照是"某一刻"**：`T07_LEDGER tasks=24 COMPLIANT=13 NON_COMPLIANT=5 UNVERIFIED=6` 采集于 2026-11-08 08:25。在途任务（T01/T04/I02/I04/I05/I06）每前进一格都会让这个数变；**只认 `worktree-ledger.json.generatedAt` 对齐的那一版**，不要拿旧数字当结论。
7. **T07 的 `orphanAttribution` 是 SUGGESTED**：45 条暂存孤儿 + 39 条未暂存孤儿只做了规则归组，**没有一条经过 Lead 确认**。按 §1 第 2 条，确认前它们仍是"未归属差异"，不能算人工归属完成。
8. **T07 没有验 `git apply --check` 之外的可集成性**：没有在集成 worktree 里真实 `apply` 再跑 build/vet（那是 I0x 的活）；也没有验 patch 与 patch 之间的冲突顺序（除 HS-06/HS-11/HS-16A/HS-GOAL-TRUTH 这 4 条已知冲突）。

---

## 12. 给 Lead 的一页操作单

```bash
# 1) 核本纪律：本 worktree 只能有一个新增文件
cd "D:/DeepSeek Harness/default-workspace/hs-worktree" && git status --short
git apply --check --binary "$S/HS-WORKTREE/HS-WORKTREE.patch"; echo "CHECK_EXIT=$?"

# 2) 核账本三态合法（只允许 COMPLIANT / NON_COMPLIANT / UNVERIFIED）
python - <<'PY'
import json
L=json.load(open(r"C:/Users/Administrator/AppData/Local/Temp/HOST-STABLE-20261107/HS-WORKTREE/worktree-ledger.json",encoding="utf-8"))
ok={"COMPLIANT","NON_COMPLIANT","UNVERIFIED"}
bad=[t["task_id"] for t in L["tasks"] if t["status"] not in ok]
print("BAD_STATUS=",bad); print("TASKS=",len(L["tasks"]))
raise SystemExit(1 if bad else 0)
PY
echo "LEDGER_SCHEMA_EXIT=$?"

# 3) 集成前先处理 §9.1 的 6 条缺口：NON_COMPLIANT 逐 hunk 人工归属，UNVERIFIED 一律不集成

# 4) GO.2 批次账本（T07 产出）：三态合法 + 本批阻断
python - <<'PYEOF'
import json
L=json.load(open(r"D:/DeepSeek Harness/default-workspace/DSH-GO/tmp/GO2-TEAM/T07/worktree-ledger.json",encoding="utf-8"))
ok={"COMPLIANT","NON_COMPLIANT","UNVERIFIED"}
bad=[t["task_id"] for t in L["batchTasks"]+L["legacyTasks"] if t["status"] not in ok]
print("BAD_STATUS=",bad)
print("BATCH=",len(L["batchTasks"]),"LEGACY=",len(L["legacyTasks"]))
print("BATCH_NON_COMPLIANT=",[t["task_id"] for t in L["batchTasks"] if t["status"]=="NON_COMPLIANT"])
raise SystemExit(1 if bad else 0)
PYEOF
echo "T07_LEDGER_SCHEMA_EXIT=$?"

# 5) 集成前必须清零的 3 条（详见 §9.2.4）：T05 worktree 污染 / HS-16D 漏 add / HS-GOAL-TRUTH patch stack
#    以及 45 条暂存孤儿的 SUGGESTED 归属逐条确认（§9.2.3）
```
