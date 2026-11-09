# 宿主稳定版发布检查单（HS-18 · T18 终审）

> **T18RR（2026-11-08）**：发布前须重跑无 override 的 193 探针，要求 **DOC_DRIFT=0、FAIL=0、UNVERIFIED=0**（T18RR/probe-methods.log）；97 插件核对 FAIL=0 UNVERIFIED=0。

> **T18R 增量（2026-11-08）**：发布前必须重跑 `tmp/GO2-TEAM/T18R/probe_plugins.py`（97 插件证据核对），要求 `FAIL=0 UNVERIFIED=0`；方法 193 unique/no_missing 与 97 same_order 保持。

> 用途：HS-19 只按本检查单逐项核；任一红即 NO-GO，不得进入提交/推送。**支持范围：Windows-only stable（用户 2026-11-08 决定；“linux不写支持，写支持win”）**；非 Windows 不属于本稳定版，`lockread_other.go` 仅是 `!windows` 兼容编译 best-effort，不要求 Linux CI/主机/WSL。口径来源：`docs/host-stable-gap-register.md`（T18RR 终审数字）、`tmp/GO2-TEAM/I06B/GO_ALLOWED.txt`、`tmp/GO2-TEAM/T07R/worktree-ledger.json`、`tmp/GO2-TEAM/T18RR/parity-summary.json`、`tmp/GO2-TEAM/T07R/LEAD-SIGNOFF.md §5`。

## 1. 解锁前置

- [ ] `tmp/GO2-TEAM/I06B/GO_ALLOWED.txt` 全部为 1（`GO_ALLOWED=1`），且 I06B RESULT 存在。
- [ ] Windows 最终 QA 绿：`go test ./...`、**`CGO_ENABLED=1 go test -race -p=1 ./... -count=1`（包级串行，正式发布 race 门）**、`internal/session` Windows 锁语义、193/97/fresh-home/sandbox/501 矩阵（相对 Windows 支持范围 UNVERIFIED=0）。非 Windows 项不作为门；默认 16 路并行 race 的历史红只作负载证据，不作为放行/掩盖依据。
- [ ] `tmp/GO2-TEAM/T07R/worktree-ledger.json` sha256 与 RESULT 声明一致，`LEAD-ADJUDICATION.md` 在；`tmp/GO2-TEAM` 下 `.go/.git/scratch` 全 0。
- [ ] 本文件与 `tmp/GO2-TEAM/T18/parity-summary.json` 的统计一致：方法 193（86/13/64/17/13）、插件 97（51/28/1/13/4）。

## 2. 发布副本（不信任工作树）

- [ ] 在 `git archive`/发布副本重跑：`go build ./...`、`go vet ./...`、`go test ./internal/transport ./cmd/dshd -count=1`、全量 `-race`（MinGW）；全部 EXIT=0，日志留档。
- [ ] 独立 QA 真实矩阵（官方 wire、真实 ConPTY、routes/streams）通过；未验项清单为空或已获 Lead 书面接受。
- [ ] `python -B scripts/doc-links.py` EXIT=0。

## 3. 文档一致性

- [ ] `docs/official-desktop-api-parity.md` / `docs/official-plugins-022-map.md` / `docs/host-stable-gap-register.md` 顶部 T18 终审口径未被回退；无 82/153、93/100、L2 未开始、P1 全 MISSING 等过期结论。
- [ ] 每个 IMPLEMENTED/PARTIAL 有 `file:line` + 测试/真实链；每个 DEFERRED 有 HS-15 决定编号；goal 只有在 `HS-GOAL-TRUTH` 真实 create→续轮→重启证据并入 A 后才可写 IMPLEMENTED。

## 4. 提交与推送

- [ ] 版本与 CHANGELOG 按稳定版 `<上游>-go.<N>`；禁止把历史 tag 当新版本。
- [ ] 只按认领路径 `git add`（禁止 `git add -A`）；`git diff --cached` 逐文件核对；不提交他人未完成改动。
- [ ] 推送需用户当次明确授权；PR/远端分支不强推。

## 5. 回滚

- [ ] 保留 `I06B/GO_ALLOWED.txt`、`T07R` 账本与各 I/T RESULT 作为发布证据；发布失败按账本回退到上一 HEAD（`9cfe2f4` 基线），不 reset/clean 共享树。
